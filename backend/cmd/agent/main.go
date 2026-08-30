// Command agent is pulsewatch's minimal reference agent (03-architecture.md:
// "Lightweight Go binary, runs on infrastructure the operator controls") —
// ADR-0003's agent-initiated push model made real: it polls its own
// assignment list over an authenticated REST GET, runs its own local
// scheduler against those targets (no Postgres access of its own — its due
// computation is purely in-memory, per 03-architecture.md), executes real
// HTTP/TCP checks, and reports outcomes plus a periodic heartbeat via the
// OTLP-shaped POST /v1/logs — agent-initiated, outbound only (FR-018): this
// binary never opens a listening port.
package main

import (
	"context"
	"log"
	"log/slog"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentapi"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := newAPIClient(cfg)
	logger := slog.Default()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		heartbeatLoop(ctx, client, cfg, logger)
	}()
	go func() {
		defer wg.Done()
		schedulerLoop(ctx, client, cfg, logger)
	}()
	wg.Wait()
	return nil
}

// heartbeatLoop emits an OTLP heartbeat every cfg.ReportInterval,
// independent of any target's own check interval — ADR-0003: "an agent
// with zero assigned targets still heartbeats," so this loop never waits
// on schedulerLoop's assignment list.
func heartbeatLoop(ctx context.Context, client *apiClient, cfg config, logger *slog.Logger) {
	sendHeartbeat(ctx, client, logger) // one immediately at startup, don't wait a full interval first

	ticker := time.NewTicker(cfg.ReportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sendHeartbeat(ctx, client, logger)
		}
	}
}

func sendHeartbeat(ctx context.Context, client *apiClient, logger *slog.Logger) {
	rec := agentapi.OtlpLogRecord{
		TimeUnixNano: strconv.FormatInt(time.Now().UnixNano(), 10),
		Attributes:   []agentapi.OtlpKeyValue{agentapi.KV(agentapi.AttrEventType, agentapi.StringValue(agentapi.EventTypeHeartbeat))},
	}
	if err := client.sendLogs(ctx, rec); err != nil {
		logger.Error("send heartbeat", "error", err)
	}
}

// schedulerLoop is this agent's own local due-set scheduler
// (03-architecture.md: "the agent's own local scheduler/worker pool
// against its own local Postgres-free in-memory due-set... based on its
// last-known assignment list and each target's interval"): it re-polls
// assignments on cfg.PollInterval and, on the tighter cfg.TickInterval
// cadence, executes and reports any target whose locally-tracked next-due
// time has arrived. Single-goroutine by design — assignments/nextDue are
// touched only from here, so no locking is needed.
func schedulerLoop(ctx context.Context, client *apiClient, cfg config, logger *slog.Logger) {
	assignments := map[string]agentapi.AssignmentListItem{}
	nextDue := map[string]time.Time{}

	pollAssignments(ctx, client, logger, assignments, nextDue)

	pollTicker := time.NewTicker(cfg.PollInterval)
	defer pollTicker.Stop()
	tickTicker := time.NewTicker(cfg.TickInterval)
	defer tickTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
			pollAssignments(ctx, client, logger, assignments, nextDue)
		case <-tickTicker.C:
			runDueChecks(ctx, client, logger, assignments, nextDue)
		}
	}
}

func pollAssignments(ctx context.Context, client *apiClient, logger *slog.Logger, assignments map[string]agentapi.AssignmentListItem, nextDue map[string]time.Time) {
	list, err := client.fetchAssignments(ctx)
	if err != nil {
		logger.Error("poll assignments", "error", err)
		return
	}

	seen := make(map[string]bool, len(list.Assignments))
	for _, item := range list.Assignments {
		seen[item.TargetID] = true
		assignments[item.TargetID] = item
		if _, known := nextDue[item.TargetID]; !known {
			nextDue[item.TargetID] = time.Now() // newly assigned: check immediately, don't wait a full interval
		}
	}
	for id := range assignments {
		if !seen[id] {
			delete(assignments, id)
			delete(nextDue, id)
		}
	}
}

func runDueChecks(ctx context.Context, client *apiClient, logger *slog.Logger, assignments map[string]agentapi.AssignmentListItem, nextDue map[string]time.Time) {
	now := time.Now()
	for id, item := range assignments {
		due, known := nextDue[id]
		if !known || now.Before(due) {
			continue
		}

		outcome := executeCheck(ctx, item)
		nextDue[id] = now.Add(time.Duration(item.IntervalSeconds) * time.Second)

		if err := client.sendLogs(ctx, checkResultLogRecord(now, id, outcome)); err != nil {
			logger.Error("send check result", "target_id", id, "error", err)
		}
	}
}

func checkResultLogRecord(at time.Time, targetID string, outcome checkOutcome) agentapi.OtlpLogRecord {
	outcomeStr := agentapi.OutcomeFailure
	if outcome.success {
		outcomeStr = agentapi.OutcomeSuccess
	}
	attrs := []agentapi.OtlpKeyValue{
		agentapi.KV(agentapi.AttrEventType, agentapi.StringValue(agentapi.EventTypeCheckResult)),
		agentapi.KV(agentapi.AttrTargetID, agentapi.StringValue(targetID)),
		agentapi.KV(agentapi.AttrOutcome, agentapi.StringValue(outcomeStr)),
		agentapi.KV(agentapi.AttrLatencyMS, agentapi.IntValue(outcome.latency.Milliseconds())),
	}
	if outcome.statusCode != nil {
		attrs = append(attrs, agentapi.KV(agentapi.AttrStatusCode, agentapi.IntValue(int64(*outcome.statusCode))))
	}
	if outcome.failureReason != nil {
		attrs = append(attrs, agentapi.KV(agentapi.AttrFailureReason, agentapi.StringValue(*outcome.failureReason)))
	}
	return agentapi.OtlpLogRecord{
		TimeUnixNano: strconv.FormatInt(at.UnixNano(), 10),
		Attributes:   attrs,
	}
}
