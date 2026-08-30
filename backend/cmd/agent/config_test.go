package main

import (
	"testing"
	"time"
)

func TestLoadConfig_RequiresServerURL(t *testing.T) {
	t.Setenv("PULSEWATCH_SERVER_URL", "")
	t.Setenv("PULSEWATCH_AGENT_TOKEN", "tok")
	t.Setenv("PULSEWATCH_AGENT_ID", "id")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected an error when PULSEWATCH_SERVER_URL is unset")
	}
}

func TestLoadConfig_RequiresToken(t *testing.T) {
	t.Setenv("PULSEWATCH_SERVER_URL", "http://example.invalid")
	t.Setenv("PULSEWATCH_AGENT_TOKEN", "")
	t.Setenv("PULSEWATCH_AGENT_ID", "id")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected an error when PULSEWATCH_AGENT_TOKEN is unset")
	}
}

func TestLoadConfig_RequiresAgentID(t *testing.T) {
	t.Setenv("PULSEWATCH_SERVER_URL", "http://example.invalid")
	t.Setenv("PULSEWATCH_AGENT_TOKEN", "tok")
	t.Setenv("PULSEWATCH_AGENT_ID", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected an error when PULSEWATCH_AGENT_ID is unset")
	}
}

func TestLoadConfig_DefaultsLogsURLFromServerURL(t *testing.T) {
	t.Setenv("PULSEWATCH_SERVER_URL", "http://example.invalid")
	t.Setenv("PULSEWATCH_AGENT_TOKEN", "tok")
	t.Setenv("PULSEWATCH_AGENT_ID", "id")
	t.Setenv("PULSEWATCH_LOGS_URL", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if want := "http://example.invalid/v1/logs"; cfg.LogsURL != want {
		t.Fatalf("expected LogsURL=%q, got %q", want, cfg.LogsURL)
	}
}

func TestLoadConfig_ExplicitLogsURLOverridesDefault(t *testing.T) {
	t.Setenv("PULSEWATCH_SERVER_URL", "http://example.invalid")
	t.Setenv("PULSEWATCH_AGENT_TOKEN", "tok")
	t.Setenv("PULSEWATCH_AGENT_ID", "id")
	t.Setenv("PULSEWATCH_LOGS_URL", "http://collector.invalid/v1/logs")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if want := "http://collector.invalid/v1/logs"; cfg.LogsURL != want {
		t.Fatalf("expected LogsURL=%q, got %q", want, cfg.LogsURL)
	}
}

func TestLoadConfig_CustomIntervals(t *testing.T) {
	t.Setenv("PULSEWATCH_SERVER_URL", "http://example.invalid")
	t.Setenv("PULSEWATCH_AGENT_TOKEN", "tok")
	t.Setenv("PULSEWATCH_AGENT_ID", "id")
	t.Setenv("PULSEWATCH_POLL_INTERVAL_SECONDS", "30")
	t.Setenv("PULSEWATCH_REPORT_INTERVAL_SECONDS", "45")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Fatalf("expected PollInterval=30s, got %v", cfg.PollInterval)
	}
	if cfg.ReportInterval != 45*time.Second {
		t.Fatalf("expected ReportInterval=45s, got %v", cfg.ReportInterval)
	}
}

func TestLoadConfig_RejectsNonPositiveInterval(t *testing.T) {
	t.Setenv("PULSEWATCH_SERVER_URL", "http://example.invalid")
	t.Setenv("PULSEWATCH_AGENT_TOKEN", "tok")
	t.Setenv("PULSEWATCH_AGENT_ID", "id")
	t.Setenv("PULSEWATCH_POLL_INTERVAL_SECONDS", "0")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected an error for PULSEWATCH_POLL_INTERVAL_SECONDS=0")
	}
}
