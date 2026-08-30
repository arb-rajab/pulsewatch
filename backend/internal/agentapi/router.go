package agentapi

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

// RegisterRoutes wires ADR-0003's agent-facing surface onto r:
//   - GET  /api/v1/agent/assignments — assignment-discovery poll
//   - POST /v1/logs                 — OTLP ingestion (deliberately outside
//     /api/v1, matching the OTel Collector's standard receiver path
//     convention, per 05-api-contracts.md's versioning note)
//
// Both require RequireAgent's agentToken bearer credential — there is no
// unauthenticated path into either.
func RegisterRoutes(r *gin.Engine, pool *pgxpool.Pool, dispatcher alerting.Dispatcher, channelKey []byte, logger *slog.Logger) {
	agentAuth := RequireAgent(pool)

	r.GET("/api/v1/agent/assignments", agentAuth, GetAssignments(pool))
	r.POST("/v1/logs", agentAuth, IngestLogs(pool, dispatcher, channelKey, logger))
}
