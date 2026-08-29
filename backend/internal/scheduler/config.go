// Package scheduler implements ADR-0001's Postgres row-leasing claim/release
// mechanism running on ADR-0004's bounded worker pool.
package scheduler

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds the scheduler's runtime tuning knobs. ADR-0004's Consequences
// section requires these be configuration, not hardcoded constants — the
// numbers below are reasoned defaults, not immutable ones.
type Config struct {
	// WorkerPoolSize is the number of long-lived worker goroutines (ADR-0004: 20, operator-configurable).
	WorkerPoolSize int
	// TickInterval is how often the scheduler scans for due targets (ADR-0004: 1s).
	TickInterval time.Duration
	// LeaseSafetyMargin is added to a target's own timeout to compute lease
	// duration (ADR-0001: "configured check timeout plus a fixed safety margin, e.g. +5s").
	LeaseSafetyMargin time.Duration
	// HardShutdownDeadline bounds how long graceful shutdown waits for
	// in-flight workers before force-canceling them (ADR-0004: e.g. 30s).
	HardShutdownDeadline time.Duration
}

// DefaultConfig returns ADR-0004's stated reasoned defaults.
func DefaultConfig() Config {
	return Config{
		WorkerPoolSize:       20,
		TickInterval:         1 * time.Second,
		LeaseSafetyMargin:    5 * time.Second,
		HardShutdownDeadline: 30 * time.Second,
	}
}

// ConfigFromEnv loads Config from the environment, matching this repo's
// existing ${VAR:-default} docker-compose pattern, falling back to
// DefaultConfig for anything unset or invalid.
func ConfigFromEnv() (Config, error) {
	cfg := DefaultConfig()

	if v := os.Getenv("SCHEDULER_WORKER_POOL_SIZE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("SCHEDULER_WORKER_POOL_SIZE must be a positive integer, got %q", v)
		}
		cfg.WorkerPoolSize = n
	}

	if v := os.Getenv("SCHEDULER_TICK_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("SCHEDULER_TICK_INTERVAL must be a positive duration, got %q", v)
		}
		cfg.TickInterval = d
	}

	if v := os.Getenv("SCHEDULER_LEASE_SAFETY_MARGIN"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return Config{}, fmt.Errorf("SCHEDULER_LEASE_SAFETY_MARGIN must be a non-negative duration, got %q", v)
		}
		cfg.LeaseSafetyMargin = d
	}

	if v := os.Getenv("SCHEDULER_HARD_SHUTDOWN_DEADLINE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("SCHEDULER_HARD_SHUTDOWN_DEADLINE must be a positive duration, got %q", v)
		}
		cfg.HardShutdownDeadline = d
	}

	return cfg, nil
}
