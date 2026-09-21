package agentapi

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
	"github.com/arb-rajab/pulsewatch/backend/internal/livefeed"
)

// RegisterRoutes wires ADR-0003's agent-facing surface onto r:
//   - GET  /api/v1/agent/assignments — assignment-discovery poll
//   - POST /v1/logs                 — OTLP ingestion (deliberately outside
//     /api/v1, matching the OTel Collector's standard receiver path
//     convention, per 05-api-contracts.md's versioning note)
//
// Both require RequireAgent's agentToken bearer credential — there is no
// unauthenticated path into either. hub is ADR-0010's live-push fan-out
// point: IngestLogs publishes to it after the identical
// alerting.RecordCheckResult commit that already gates dispatch, the same
// Hub the operator-facing GET /events route (a separate *gin.Engine, same
// process) streams from.
func RegisterRoutes(r *gin.Engine, pool *pgxpool.Pool, dispatcher alerting.Dispatcher, channelKey []byte, logger *slog.Logger, hub *livefeed.Hub) {
	agentAuth := RequireAgent(pool)

	r.GET("/api/v1/agent/assignments", agentAuth, GetAssignments(pool))
	r.POST("/v1/logs", agentAuth, IngestLogs(pool, dispatcher, channelKey, logger, hub))
}
