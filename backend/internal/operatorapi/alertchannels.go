package operatorapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

type alertChannelCreateRequest struct {
	Type        string `json:"type" binding:"required"`
	Destination string `json:"destination" binding:"required"`
}

// alertChannelResponse mirrors openapi.yaml's AlertChannel schema exactly:
// no destination/credential property at all, structurally, not merely a
// field the handler chooses not to populate — the same FR-023 discipline
// alerting.Channel already applies on the read/dispatch side.
type alertChannelResponse struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
}

func validAlertChannelType(t string) bool {
	return t == "webhook" || t == "email"
}

// CreateAlertChannel is POST /alert-channels (FR-013/FR-014/FR-023):
// encrypts destination at rest (alerting.EncryptDestination, AES-256-GCM,
// Session 6's already-established algorithm choice — not re-decided here)
// before it ever touches the database, and returns a response with no field
// capable of carrying it back.
func CreateAlertChannel(pool *pgxpool.Pool, channelKey []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req alertChannelCreateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "malformed_request", "type and destination are required")
			return
		}
		if !validAlertChannelType(req.Type) {
			writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", "type must be either \"webhook\" or \"email\"", "type")
			return
		}
		if channelKey == nil {
			writeError(c, http.StatusServiceUnavailable, "encryption_key_unavailable", "ALERT_CHANNEL_ENCRYPTION_KEY is not configured")
			return
		}
		encrypted, err := alerting.EncryptDestination(req.Destination, channelKey)
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "encryption_failed", "could not encrypt destination")
			return
		}

		ctx := c.Request.Context()
		var resp alertChannelResponse
		resp.Type = req.Type
		err = pool.QueryRow(ctx,
			`INSERT INTO alert_channels (type, destination_encrypted) VALUES ($1, $2) RETURNING id::text, created_at`,
			req.Type, encrypted,
		).Scan(&resp.ID, &resp.CreatedAt)
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not create alert channel")
			return
		}
		c.JSON(http.StatusCreated, resp)
	}
}

// ListAlertChannels is GET /alert-channels.
func ListAlertChannels(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := pool.Query(c.Request.Context(), `SELECT id::text, type, created_at FROM alert_channels ORDER BY id`)
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not list alert channels")
			return
		}
		defer rows.Close()

		channels := []alertChannelResponse{}
		for rows.Next() {
			var ch alertChannelResponse
			if err := rows.Scan(&ch.ID, &ch.Type, &ch.CreatedAt); err != nil {
				writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read alert channels")
				return
			}
			channels = append(channels, ch)
		}
		if err := rows.Err(); err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read alert channels")
			return
		}
		c.JSON(http.StatusOK, channels)
	}
}

// GetAlertChannel is GET /alert-channels/{alert_channel_id}.
func GetAlertChannel(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var ch alertChannelResponse
		err := pool.QueryRow(c.Request.Context(),
			`SELECT id::text, type, created_at FROM alert_channels WHERE id = $1::uuid`, c.Param("alert_channel_id"),
		).Scan(&ch.ID, &ch.Type, &ch.CreatedAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(c, http.StatusNotFound, "not_found", "alert channel not found")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read alert channel")
			return
		}
		c.JSON(http.StatusOK, ch)
	}
}

type alertChannelSecretRotateRequest struct {
	Destination string `json:"destination" binding:"required"`
}

// RotateAlertChannelSecret is PUT /alert-channels/{id}/secret — the only way
// to change a channel's credential (openapi.yaml: deliberately no
// corresponding GET, and this operation's own response has no body, so
// there is structurally no response shape that could ever carry the value
// back either).
func RotateAlertChannelSecret(pool *pgxpool.Pool, channelKey []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req alertChannelSecretRotateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "malformed_request", "destination is required")
			return
		}
		if channelKey == nil {
			writeError(c, http.StatusServiceUnavailable, "encryption_key_unavailable", "ALERT_CHANNEL_ENCRYPTION_KEY is not configured")
			return
		}
		encrypted, err := alerting.EncryptDestination(req.Destination, channelKey)
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "encryption_failed", "could not encrypt destination")
			return
		}

		var id string
		err = pool.QueryRow(c.Request.Context(),
			`UPDATE alert_channels SET destination_encrypted = $1 WHERE id = $2::uuid RETURNING id::text`,
			encrypted, c.Param("alert_channel_id"),
		).Scan(&id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(c, http.StatusNotFound, "not_found", "alert channel not found")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not rotate alert channel secret")
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// DeleteAlertChannel is DELETE /alert-channels/{alert_channel_id}.
func DeleteAlertChannel(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var id string
		err := pool.QueryRow(c.Request.Context(),
			`DELETE FROM alert_channels WHERE id = $1::uuid RETURNING id::text`, c.Param("alert_channel_id"),
		).Scan(&id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(c, http.StatusNotFound, "not_found", "alert channel not found")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not delete alert channel")
			return
		}
		c.Status(http.StatusNoContent)
	}
}
