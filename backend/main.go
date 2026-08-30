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

// setupRouter wires the health check, ADR-0003's agent-facing surface (GET
// /api/v1/agent/assignments, POST /v1/logs — internal/agentapi), and
// 05-api-contracts.md's operator-facing REST surface (internal/operatorapi,
// gated by RequireOperator's session cookie — Session 8). pool may be nil
// only for tests that exercise /health alone; any agentapi/operatorapi
// route registered against a nil pool will fail if actually invoked, never
// at registration time.
func setupRouter(pool *pgxpool.Pool, dispatcher alerting.Dispatcher, channelKey []byte, sessionSecret []byte, logger *slog.Logger) *gin.Engine {
	r := gin.Default()
	r.GET("/health", healthHandler)
	agentapi.RegisterRoutes(r, pool, dispatcher, channelKey, logger)
	operatorapi.RegisterRoutes(r, pool, sessionSecret, channelKey)
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

	srv := &http.Server{Addr: ":8080", Handler: setupRouter(pool, dispatcher, channelKey, sessionSecret, slog.Default())}

	httpErrCh := make(chan error, 1)
	go func() {
		if serveErr := srv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			httpErrCh <- serveErr
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
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("http server shutdown", "error", err)
	}

	if err := <-schedDoneCh; err != nil {
		slog.Error("scheduler shutdown", "error", err)
	}
	return nil
}
