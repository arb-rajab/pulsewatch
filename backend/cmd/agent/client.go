package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentapi"
)

// apiClient is this reference agent's own outbound-only connection to the
// server — GET for assignment discovery, POST for OTLP reporting — never
// the other direction (FR-018).
type apiClient struct {
	httpClient *http.Client
	cfg        config
}

func newAPIClient(cfg config) *apiClient {
	return &apiClient{httpClient: &http.Client{Timeout: 10 * time.Second}, cfg: cfg}
}

// fetchAssignments is one real REST poll against GET
// /api/v1/agent/assignments (ADR-0003 Decision 2).
func (c *apiClient) fetchAssignments(ctx context.Context) (agentapi.AssignmentList, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.ServerURL+"/api/v1/agent/assignments", nil)
	if err != nil {
		return agentapi.AssignmentList{}, fmt.Errorf("build assignments request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return agentapi.AssignmentList{}, fmt.Errorf("poll assignments: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return agentapi.AssignmentList{}, fmt.Errorf("poll assignments: unexpected status %d", resp.StatusCode)
	}

	var list agentapi.AssignmentList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return agentapi.AssignmentList{}, fmt.Errorf("decode assignments response: %w", err)
	}
	return list, nil
}

// sendLogs POSTs one OTLP ExportLogsServiceRequest carrying the given
// records to LogsURL, with the resource-level agent.id attribute
// docs/architecture/openapi.yaml requires.
func (c *apiClient) sendLogs(ctx context.Context, records ...agentapi.OtlpLogRecord) error {
	reqBody := agentapi.OtlpExportLogsServiceRequest{
		ResourceLogs: []agentapi.OtlpResourceLogs{
			{
				Resource:  agentapi.OtlpResource{Attributes: []agentapi.OtlpKeyValue{agentapi.KV(agentapi.AttrAgentID, agentapi.StringValue(c.cfg.AgentID))}},
				ScopeLogs: []agentapi.OtlpScopeLogs{{LogRecords: records}},
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal otlp request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.LogsURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build otlp request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send otlp logs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("send otlp logs: unexpected status %d", resp.StatusCode)
	}
	return nil
}
