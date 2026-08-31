// Command pulsewatch runs the pulsewatch backend API server and scheduler.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentapi"
	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
	"github.com/arb-rajab/pulsewatch/backend/internal/operatorapi"
	"github.com/arb-rajab/pulsewatch/backend/internal/operatorauth"
	"github.com/arb-rajab/pulsewatch/backend/internal/scheduler"
)

// setupOperatorRouter wires the health check and 05-api-contracts.md's
// operator-facing REST surface (internal/operatorapi, gated by
// RequireOperator's session cookie — Session 8) onto their own *gin.Engine,
// served on its own listener (run(): operatorAddr) that is never published
// as a host port directly — the only way to reach it is through the
// TLS-terminating `proxy` (Caddy, R-001) or the internal Docker network.
// This is the fix for the cross-port session-cookie leak found closing out
// Session 10: operatorapi routes are simply not mounted anywhere an
// operator's real session cookie could ever be replayed in plaintext,
// regardless of what a client sends. pool may be nil only for tests that
// exercise /health alone; an operatorapi route registered against a nil
// pool will fail if actually invoked, never at registration time.
func setupOperatorRouter(pool *pgxpool.Pool, sessionSecret []byte, channelKey []byte) *gin.Engine {
	r := gin.Default()
	r.GET("/health", healthHandler)
	operatorapi.RegisterRoutes(r, pool, sessionSecret, channelKey)
	return r
}

// setupAgentRouter wires the health check and ADR-0003's agent-facing
// surface (GET /api/v1/agent/assignments, POST /v1/logs — internal/agentapi)
// onto their own *gin.Engine, served on its own listener (run(): agentAddr)
// that is the one still published as a plain-HTTP host port (docker-compose
// R-003) for agent bearer-token traffic. No operatorapi route is ever
// mounted on this engine — there is no server-side path where an operator
// session cookie is meaningful here, not merely one where it happens to be
// rejected.
func setupAgentRouter(pool *pgxpool.Pool, dispatcher alerting.Dispatcher, channelKey []byte, logger *slog.Logger) *gin.Engine {
	r := gin.Default()
	r.GET("/health", healthHandler)
	agentapi.RegisterRoutes(r, pool, dispatcher, channelKey, logger)
	return r
}

func healthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	schedCfg, err := scheduler.ConfigFromEnv()
	if err != nil {
		return fmt.Errorf("scheduler config: %w", err)
	}

	sessionSecret, err := operatorauth.SigningSecretFromEnv()
	if err != nil {
		return fmt.Errorf("operator session config: %w", err)
	}

	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()

	sched, err := scheduler.New(pool, schedCfg, slog.Default())
	if err != nil {
		return fmt.Errorf("construct scheduler: %w", err)
	}

	// The agent-facing OTLP ingestion path (internal/agentapi) dispatches
	// notifications through the identical alerting.Dispatcher/channel-key
	// construction the scheduler builds for itself (alerting.NewLogDispatcher,
	// alerting.EncryptionKeyFromEnv) — two independent, deterministic reads
	// of the same environment, not a shared mutable dependency, so no
	// coupling to the scheduler package is needed here.
	dispatcher := alerting.NewLogDispatcher(slog.Default())
	channelKey, keyErr := alerting.EncryptionKeyFromEnv()
	if keyErr != nil {
		slog.Warn("ALERT_CHANNEL_ENCRYPTION_KEY not configured; agent-reported alert dispatch will be skipped if any alert_channels row exists", "error", keyErr)
		channelKey = nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Two separate listeners, not one shared *gin.Engine (Session 11,
	// closing the cross-port session-cookie leak found at Session 10's
	// closeout): operatorSrv carries every operatorapi route and is only
	// ever reached through the internal Docker network (by `proxy`/Caddy,
	// R-001) — docker-compose.yml does not publish operatorAddr as a host
	// port at all. agentSrv carries only agentapi's routes and is the one
	// still published directly as a plain-HTTP host port for agent traffic
	// (R-003) — replaying an operator's session cookie against it can never
	// reach an operatorapi handler, because none are registered on this
	// engine, not merely because the cookie fails some check on the way in.
	const operatorAddr = ":8080"
	const agentAddr = ":8081"
	operatorSrv := &http.Server{Addr: operatorAddr, Handler: setupOperatorRouter(pool, sessionSecret, channelKey)}
	agentSrv := &http.Server{Addr: agentAddr, Handler: setupAgentRouter(pool, dispatcher, channelKey, slog.Default())}

	httpErrCh := make(chan error, 2)
	go func() {
		if serveErr := operatorSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			httpErrCh <- fmt.Errorf("operator http server: %w", serveErr)
			return
		}
		httpErrCh <- nil
	}()
	go func() {
		if serveErr := agentSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			httpErrCh <- fmt.Errorf("agent http server: %w", serveErr)
			return
		}
		httpErrCh <- nil
	}()

	schedDoneCh := make(chan error, 1)
	go func() {
		schedDoneCh <- sched.Run(ctx)
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case serveErr := <-httpErrCh:
		if serveErr != nil {
			slog.Error("http server failed", "error", serveErr)
		}
		stop() // cancel ctx so the scheduler begins its own graceful shutdown too
	}

	// A fresh bound for shutdown itself, independent of the root ctx (which
	// is already canceled by now) — matches ADR-0004's hard-shutdown-deadline
	// framing applied to the HTTP server side of the process.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), schedCfg.HardShutdownDeadline) //nolint:contextcheck // intentional fresh bound, root ctx is already canceled
	defer cancel()
	if err := operatorSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("operator http server shutdown", "error", err)
	}
	if err := agentSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("agent http server shutdown", "error", err)
	}

	if err := <-schedDoneCh; err != nil {
		slog.Error("scheduler shutdown", "error", err)
	}
	return nil
}
