package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// config holds this reference agent's runtime settings — deliberately
// minimal (03-architecture.md's own framing: "doesn't need every feature,
// but needs to be real").
type config struct {
	// ServerURL is where GET /api/v1/agent/assignments is polled.
	ServerURL string
	// LogsURL is where OTLP check_result/heartbeat records are POSTed —
	// defaults to ServerURL + "/v1/logs" (the real deployment shape routes
	// this through the OTel Collector instead; this reference agent talks
	// directly to the server, matching this session's own end-to-end proof
	// scope: "agent binary -> real network call -> real backend").
	LogsURL string
	// Token is the agentToken bearer credential (agentauth.CreateAgent /
	// cmd/seed-agent's output).
	Token string
	// AgentID is this agent's own id — carried as the OTLP resource-level
	// agent.id attribute (docs/architecture/openapi.yaml).
	AgentID string
	// PollInterval is how often assignments are re-polled (ADR-0003
	// Trade-offs: "up to one poll interval" of assignment-change lag).
	PollInterval time.Duration
	// ReportInterval is how often a heartbeat is emitted, independent of
	// any target's own check interval (ADR-0003 Decision 3).
	ReportInterval time.Duration
	// TickInterval is the local due-set scan cadence — the agent's own
	// in-memory equivalent of the server's ADR-0004 tick loop.
	TickInterval time.Duration
}

func loadConfig() (config, error) {
	cfg := config{
		PollInterval:   60 * time.Second,
		ReportInterval: 60 * time.Second,
		TickInterval:   1 * time.Second,
	}

	cfg.ServerURL = os.Getenv("PULSEWATCH_SERVER_URL")
	if cfg.ServerURL == "" {
		return config{}, errors.New("PULSEWATCH_SERVER_URL is required")
	}
	cfg.LogsURL = os.Getenv("PULSEWATCH_LOGS_URL")
	if cfg.LogsURL == "" {
		cfg.LogsURL = cfg.ServerURL + "/v1/logs"
	}
	cfg.Token = os.Getenv("PULSEWATCH_AGENT_TOKEN")
	if cfg.Token == "" {
		return config{}, errors.New("PULSEWATCH_AGENT_TOKEN is required")
	}
	cfg.AgentID = os.Getenv("PULSEWATCH_AGENT_ID")
	if cfg.AgentID == "" {
		return config{}, errors.New("PULSEWATCH_AGENT_ID is required")
	}

	if v := os.Getenv("PULSEWATCH_POLL_INTERVAL_SECONDS"); v != "" {
		d, err := parsePositiveSeconds(v)
		if err != nil {
			return config{}, fmt.Errorf("PULSEWATCH_POLL_INTERVAL_SECONDS: %w", err)
		}
		cfg.PollInterval = d
	}
	if v := os.Getenv("PULSEWATCH_REPORT_INTERVAL_SECONDS"); v != "" {
		d, err := parsePositiveSeconds(v)
		if err != nil {
			return config{}, fmt.Errorf("PULSEWATCH_REPORT_INTERVAL_SECONDS: %w", err)
		}
		cfg.ReportInterval = d
	}

	return cfg, nil
}

func parsePositiveSeconds(v string) (time.Duration, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("must be a positive integer, got %q", v)
	}
	return time.Duration(n) * time.Second, nil
}
