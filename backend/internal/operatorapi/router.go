// Package operatorapi implements 05-api-contracts.md's operator-facing REST
// surface: session login/logout, target CRUD, alert-channel configuration,
// and agent registration/lifecycle — every route gated by RequireOperator
// (operatorauth's stateless HMAC session cookie) except /auth/login itself,
// exactly matching openapi.yaml's per-operation `security` declarations.
package operatorapi

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RegisterRoutes wires the full operator-facing surface onto r under
// /api/v1, matching openapi.yaml's servers/paths exactly. sessionSecret is
// operatorauth's HMAC signing key (operatorauth.SigningSecretFromEnv);
// channelKey is FR-023's alert-channel encryption key (may be nil — see
// CreateAlertChannel/RotateAlertChannelSecret, which fail closed with a 503
// rather than silently storing plaintext when it's unset).
func RegisterRoutes(r *gin.Engine, pool *pgxpool.Pool, sessionSecret []byte, channelKey []byte) {
	limiter := newLoginRateLimiter(loginFailureLimit, loginRateWindow)
	csrf := RequireJSONContentType()
	auth := RequireOperator(sessionSecret)

	api := r.Group("/api/v1")

	api.POST("/auth/login", csrf, LoginHandler(pool, sessionSecret, limiter))
	api.POST("/auth/logout", auth, LogoutHandler())

	api.POST("/targets", auth, csrf, CreateTarget(pool))
	api.GET("/targets", auth, ListTargets(pool))
	api.GET("/targets/:target_id", auth, GetTarget(pool))
	api.PATCH("/targets/:target_id", auth, csrf, UpdateTarget(pool))
	api.DELETE("/targets/:target_id", auth, DeleteTarget(pool))

	api.POST("/alert-channels", auth, csrf, CreateAlertChannel(pool, channelKey))
	api.GET("/alert-channels", auth, ListAlertChannels(pool))
	api.GET("/alert-channels/:alert_channel_id", auth, GetAlertChannel(pool))
	api.PUT("/alert-channels/:alert_channel_id/secret", auth, csrf, RotateAlertChannelSecret(pool, channelKey))
	api.DELETE("/alert-channels/:alert_channel_id", auth, DeleteAlertChannel(pool))

	api.POST("/agents", auth, csrf, CreateAgent(pool))
	api.GET("/agents", auth, ListAgents(pool))
	api.GET("/agents/:agent_id", auth, GetAgent(pool))
	api.DELETE("/agents/:agent_id", auth, DeleteAgent(pool))
	api.POST("/agents/:agent_id/credential/rotate", auth, csrf, RotateAgentCredential(pool))
}
