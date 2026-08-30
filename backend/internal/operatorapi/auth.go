package operatorapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/operatorauth"
)

type loginRequest struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type loginResponse struct {
	OperatorID string `json:"operator_id"`
	Email      string `json:"email"`
}

// LoginHandler is POST /auth/login (05-api-contracts.md, no security
// requirement of its own — this is how a session is obtained in the first
// place). Rate-limited by both the attempted email and the caller's remote
// IP (see ratelimit.go) before any password verification happens, so a
// blocked caller never even gets to burn a bcrypt comparison.
func LoginHandler(pool *pgxpool.Pool, secret []byte, limiter *loginRateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req loginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "malformed_request", "email and password are required")
			return
		}

		now := time.Now()
		ip := c.ClientIP()
		if !limiter.allow(req.Email, now) || !limiter.allow("ip:"+ip, now) {
			c.Header("Retry-After", loginRateWindow.String())
			writeError(c, http.StatusTooManyRequests, "too_many_requests", "too many failed login attempts, try again later")
			return
		}

		identity, err := operatorauth.Login(c.Request.Context(), pool, req.Email, req.Password)
		if err != nil {
			if errors.Is(err, operatorauth.ErrInvalidCredential) {
				limiter.recordFailure(req.Email, now)
				limiter.recordFailure("ip:"+ip, now)
				writeError(c, http.StatusUnauthorized, "unauthorized", "invalid email or password")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not verify credentials")
			return
		}

		limiter.reset(req.Email)
		limiter.reset("ip:" + ip)

		token, expiresAt, err := operatorauth.IssueSession(secret, identity.OperatorID, now)
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "session_issuance_failed", "could not issue session")
			return
		}
		setSessionCookie(c, token, expiresAt)

		c.JSON(http.StatusOK, loginResponse{OperatorID: identity.OperatorID, Email: req.Email})
	}
}

// LogoutHandler is POST /auth/logout (05-api-contracts.md, operatorSession
// auth required — a caller with no valid session has nothing to log out
// of). Clears the cookie client-side, the full scope of "logout" a
// stateless, unrevocable-per-session token structurally supports —
// 05-api-contracts.md's own /auth/logout description states this
// explicitly: only rotating the server's signing secret revokes an
// already-issued token an attacker might hold a copy of, an accepted
// trade-off for v1's single-operator, self-hosted threat model, reaffirmed
// rather than silently worked around here. SessionTTL bounds the exposure
// window that trade-off leaves open.
func LogoutHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		clearSessionCookie(c)
		c.Status(http.StatusNoContent)
	}
}

func setSessionCookie(c *gin.Context, token string, expiresAt time.Time) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(operatorauth.SessionCookieName, token, int(time.Until(expiresAt).Seconds()), "/", "", true, true)
}

func clearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(operatorauth.SessionCookieName, "", -1, "/", "", true, true)
}
