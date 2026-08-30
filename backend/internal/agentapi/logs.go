package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentauth"
	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

type partialSuccess struct {
	RejectedLogRecords int64  `json:"rejectedLogRecords"`
	ErrorMessage       string `json:"errorMessage"`
}

type exportLogsServiceResponse struct {
	PartialSuccess *partialSuccess `json:"partialSuccess,omitempty"`
}

// IngestLogs is POST /v1/logs (ADR-0003): the server's own OTLP ingestion
// endpoint, reached via the OTel Collector's exporter pipeline in the full
// deployment shape, or directly by a caller presenting the same agentToken
// bearer credential (this session's own end-to-end proof, cmd/agent). Two
// record shapes share this endpoint, distinguished by
// pulsewatch.event_type — see this package's doc comment and
// docs/architecture/openapi.yaml for the full contract.
//
// Every check_result record reaches alerting.RecordCheckResult — the exact
// same shared write path scheduler.releaseAndRecord uses for a
// server-executed check — via recordAgentCheckResult (record.go). There is
// no second, agent-specific alert-evaluation path anywhere in this
// function.
func IngestLogs(pool *pgxpool.Pool, dispatcher alerting.Dispatcher, channelKey []byte, logger *slog.Logger) gin.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(c *gin.Context) {
		identity := identityFromContext(c)
		ctx := c.Request.Context()

		var req OtlpExportLogsServiceRequest
		if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
			writeError(c, http.StatusUnprocessableEntity, "malformed_request", "request body is not a valid OTLP ExportLogsServiceRequest")
			return
		}

		var rejected int64
		var lastErr string
		reject := func(n int64, reason string) {
			rejected += n
			lastErr = reason
		}

		for _, rl := range req.ResourceLogs {
			resourceAttrs := attrMap(rl.Resource.Attributes)
			if resourceAgentID, ok := resourceAttrs[AttrAgentID].asString(); ok && resourceAgentID != identity.AgentID {
				// Defense in depth: bearer auth already scopes this whole
				// request to identity.AgentID — a resourceLogs block
				// explicitly claiming a different agent.id is either a
				// client bug or a spoofing attempt. Reject every record in
				// it rather than guess which identity was intended.
				var count int64
				for _, sl := range rl.ScopeLogs {
					count += int64(len(sl.LogRecords))
				}
				reject(count, "resource agent.id does not match the authenticated agent")
				continue
			}

			for _, sl := range rl.ScopeLogs {
				for _, rec := range sl.LogRecords {
					if err := processLogRecord(ctx, pool, dispatcher, channelKey, logger, identity, rec); err != nil {
						if isInfraError(err) {
							logger.Error("otlp ingestion: infrastructure error", "error", err, "agent_id", identity.AgentID)
							writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not persist check result")
							return
						}
						reject(1, err.Error())
					}
				}
			}
		}

		resp := exportLogsServiceResponse{}
		if rejected > 0 {
			resp.PartialSuccess = &partialSuccess{RejectedLogRecords: rejected, ErrorMessage: lastErr}
		}
		c.JSON(http.StatusOK, resp)
	}
}

// recordError distinguishes a validation failure (reject this one record,
// keep processing the rest) from an infrastructure failure (abort the
// whole request with 503 — 05-api-contracts.md's error model: "Postgres
// unavailable... surfaced explicitly rather than hanging or a bare 500").
type recordError struct {
	msg   string
	infra bool
}

func (e *recordError) Error() string { return e.msg }

func isInfraError(err error) bool {
	var re *recordError
	return errors.As(err, &re) && re.infra
}

func validationErr(msg string) error { return &recordError{msg: msg} }
func infraErr(msg string) error      { return &recordError{msg: msg, infra: true} }

// processLogRecord handles one logRecord — heartbeat or check_result — per
// docs/architecture/openapi.yaml's /v1/logs description.
func processLogRecord(ctx context.Context, pool *pgxpool.Pool, dispatcher alerting.Dispatcher, channelKey []byte, logger *slog.Logger, identity agentauth.Identity, rec OtlpLogRecord) error {
	nanos, err := strconv.ParseInt(rec.TimeUnixNano, 10, 64)
	if err != nil {
		return validationErr("timeUnixNano is not a valid integer")
	}
	checkedAt := time.Unix(0, nanos).UTC()

	attrs := attrMap(rec.Attributes)
	eventType, ok := attrs[AttrEventType].asString()
	if !ok {
		return validationErr("missing pulsewatch.event_type")
	}

	switch eventType {
	case EventTypeHeartbeat:
		if err := agentauth.RecordHeartbeat(ctx, pool, identity.AgentID, checkedAt); err != nil {
			return infraErr(err.Error())
		}
		return nil

	case EventTypeCheckResult:
		return processCheckResult(ctx, pool, dispatcher, channelKey, logger, identity, checkedAt, attrs)

	default:
		return validationErr("unknown pulsewatch.event_type")
	}
}

func processCheckResult(ctx context.Context, pool *pgxpool.Pool, dispatcher alerting.Dispatcher, channelKey []byte, logger *slog.Logger, identity agentauth.Identity, checkedAt time.Time, attrs map[string]OtlpValue) error {
	targetID, ok := attrs[AttrTargetID].asString()
	if !ok {
		return validationErr("missing pulsewatch.target_id")
	}
	outcome, ok := attrs[AttrOutcome].asString()
	if !ok || (outcome != OutcomeSuccess && outcome != OutcomeFailure) {
		return validationErr("missing or invalid pulsewatch.outcome")
	}
	latencyMS, ok := attrs[AttrLatencyMS].asInt()
	if !ok {
		return validationErr("missing or invalid pulsewatch.latency_ms")
	}

	var statusCode *int32
	if n, ok := attrs[AttrStatusCode].asInt(); ok {
		v := int32(n) //nolint:gosec // HTTP status codes fit comfortably in int32
		statusCode = &v
	}
	var failureReason *string
	if s, ok := attrs[AttrFailureReason].asString(); ok && s != "" {
		failureReason = &s
	}

	// A target this agent isn't (or is no longer) assigned to is rejected,
	// not silently written — symmetric with 05-api-contracts.md's "an
	// agent's identity comes from its own token" structural guarantee: an
	// agent must not be able to write a result for a target it doesn't own.
	failureThreshold, err := lookupAssignedTarget(ctx, pool, targetID, identity.AgentID)
	if err != nil {
		if errors.Is(err, errTargetNotAssigned) {
			return validationErr("target is not assigned to the authenticated agent")
		}
		return infraErr(err.Error())
	}

	recorded, err := recordAgentCheckResult(ctx, pool, targetID, failureThreshold, alerting.CheckResultInput{
		TargetID:      targetID,
		CheckedAt:     checkedAt,
		Success:       outcome == OutcomeSuccess,
		LatencyMS:     int(latencyMS),
		StatusCode:    statusCode,
		FailureReason: failureReason,
	})
	if err != nil {
		return infraErr(err.Error())
	}

	if recorded.Dispatch != nil {
		alerting.NotifyChannels(ctx, pool, dispatcher, channelKey, *recorded.Dispatch, logger)
	}
	return nil
}

// errTargetNotAssigned means the (target_id, agent_id) pair doesn't match
// any live target row — either the target doesn't exist, is soft-deleted,
// or (the security-relevant case) belongs to a different agent entirely.
var errTargetNotAssigned = errors.New("target not assigned to this agent")

func lookupAssignedTarget(ctx context.Context, pool *pgxpool.Pool, targetID, agentID string) (failureThreshold int, err error) {
	const stmt = `SELECT failure_threshold FROM targets WHERE id = $1::uuid AND agent_id = $2::uuid AND deleted_at IS NULL`
	if scanErr := pool.QueryRow(ctx, stmt, targetID, agentID).Scan(&failureThreshold); scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return 0, errTargetNotAssigned
		}
		return 0, fmt.Errorf("look up assigned target %s: %w", targetID, scanErr)
	}
	return failureThreshold, nil
}
