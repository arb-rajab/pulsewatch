package agentapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentauth"
)

// identityContextKey is the gin.Context key RequireAgent sets and
// identityFromContext reads — unexported so nothing outside this package
// can set or read it directly, only via those two functions.
const identityContextKey = "agentapi.agent_identity"

// RequireAgent is the agentToken security scheme
// (05-api-contracts.md/openapi.yaml) as Gin middleware: every agent-facing
// route (GetAssignments, IngestLogs) requires it. A missing or invalid
// bearer credential is always 401 — 05-api-contracts.md: "no 403... an
// agent's bearer token can't even satisfy an operator endpoint's security
// requirement," and symmetrically here, a request with no valid agentToken
// at all has no identity to authorize anything against.
func RequireAgent(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c.GetHeader("Authorization"))
		identity, err := agentauth.Authenticate(c.Request.Context(), pool, token)
		if err != nil {
			writeError(c, http.StatusUnauthorized, "unauthorized", "missing or invalid agent credential")
			c.Abort()
			return
		}
		c.Set(identityContextKey, identity)
		c.Next()
	}
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimPrefix(header, prefix)
}

// identityFromContext reads back what RequireAgent set. Callers (the
// handlers themselves) only ever run after RequireAgent per router.go's
// wiring, so the zero-value fallback here is unreachable in practice, not a
// silent failure mode this package relies on.
func identityFromContext(c *gin.Context) agentauth.Identity {
	v, _ := c.Get(identityContextKey)
	identity, _ := v.(agentauth.Identity)
	return identity
}
