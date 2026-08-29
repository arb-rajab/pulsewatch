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

	"github.com/arb-rajab/pulsewatch/backend/internal/scheduler"
)

func setupRouter() *gin.Engine {
	r := gin.Default()
	r.GET("/health", healthHandler)
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

	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()

	sched, err := scheduler.New(pool, schedCfg, slog.Default())
	if err != nil {
		return fmt.Errorf("construct scheduler: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: ":8080", Handler: setupRouter()}

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
