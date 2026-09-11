package operatorapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

// This file is ADR-0007's device-token registration surface: the three
// operations pulsewatch-mobile needs to become a push destination, and
// nothing more.
//
// It is deliberately a sibling of alertchannels.go rather than part of it.
// A device token is not an alert channel: an operator configures one push
// *channel* (the provider credential) and their devices then register
// themselves against it, many-to-one, from a different client, on a
// different cadence, with a different lifecycle (a token dies when an app
// is uninstalled; a credential dies when an operator rotates it).

type deviceTokenRegisterRequest struct {
	Provider string `json:"provider" binding:"required"`
	Platform string `json:"platform" binding:"required"`
	Token    string `json:"token" binding:"required"`
}

// RegisterDeviceToken is POST /device-tokens: the mobile app hands over the
// provider-issued token for the signed-in operator's device.
//
// It is an upsert, and it returns 200 rather than 201 for that reason — an
// app re-registering an unchanged token on every launch (the normal case,
// and the only way a token that was wrongly marked dead ever comes back) is
// not creating anything.
func RegisterDeviceToken(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		operatorID, ok := OperatorIDFrom(c)
		if !ok {
			writeError(c, http.StatusUnauthorized, "unauthorized", "missing or invalid operator session")
			return
		}

		var req deviceTokenRegisterRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "malformed_request", "provider, platform and token are required")
			return
		}
		if !alerting.ValidPushProvider(req.Provider) {
			writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", "provider must be either \"fcm\" or \"apns\"", "provider")
			return
		}
		if !alerting.ValidDevicePlatform(req.Platform) {
			writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", "platform must be either \"ios\" or \"android\"", "platform")
			return
		}

		record, err := alerting.RegisterDeviceToken(c.Request.Context(), pool, operatorID, req.Provider, req.Platform, req.Token)
		if err != nil {
			if errors.Is(err, alerting.ErrInvalidDeviceToken) {
				// alerting's message describes the token's shape (empty, too
				// long), never its value.
				writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", err.Error(), "token")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not register device token")
			return
		}
		c.JSON(http.StatusOK, record)
	}
}

// ListDeviceTokens is GET /device-tokens: the operator's own registered
// devices, including revoked and dead ones with the reason they died. The
// token values themselves are structurally absent from
// alerting.DeviceTokenRecord — there is no read path that hands one back
// out, the same discipline alertChannelResponse applies to a channel's
// destination.
func ListDeviceTokens(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		operatorID, ok := OperatorIDFrom(c)
		if !ok {
			writeError(c, http.StatusUnauthorized, "unauthorized", "missing or invalid operator session")
			return
		}
		records, err := alerting.ListDeviceTokens(c.Request.Context(), pool, operatorID)
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not list device tokens")
			return
		}
		c.JSON(http.StatusOK, records)
	}
}

// UnregisterDeviceToken is DELETE /device-tokens/{device_token_id}: the
// sign-out path. It revokes rather than deletes (see
// alerting.RevokeDeviceToken), so an operator's alert_dispatches history
// stays intact, and it is scoped to the calling operator — one operator can
// never revoke another's device, even though v1 only ever has one.
func UnregisterDeviceToken(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		operatorID, ok := OperatorIDFrom(c)
		if !ok {
			writeError(c, http.StatusUnauthorized, "unauthorized", "missing or invalid operator session")
			return
		}
		err := alerting.RevokeDeviceToken(c.Request.Context(), pool, operatorID, c.Param("device_token_id"))
		if err != nil {
			if errors.Is(err, alerting.ErrDeviceTokenNotFound) {
				// Deliberately the same 404 for "no such id", "not yours",
				// and "already revoked": a caller holding a session has no
				// legitimate use for telling those apart, and the first two
				// would otherwise be an id-enumeration oracle.
				writeError(c, http.StatusNotFound, "not_found", "device token not found")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not unregister device token")
			return
		}
		c.Status(http.StatusNoContent)
	}
}
