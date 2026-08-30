package operatorapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/arb-rajab/pulsewatch/backend/internal/operatorauth"
)

// RequireOperator is the operatorSession security scheme
// (05-api-contracts.md/openapi.yaml) as Gin middleware: every operator-facing
// route below requires it. A missing, malformed, or expired cookie is
// always 401 — 05-api-contracts.md: "no 403... there are only ever two
// disjoint identity types" — there is no lesser-privilege outcome to fall
// back to.
//
// Unlike agentapi's RequireAgent, the verified Identity is not stashed in
// the request context for handlers to read back: 02-requirements.md's
// single-operator baseline means every operator-facing resource (targets,
// alert channels, agents) is global, not scoped per-operator-account, so
// there is no handler in this package that has any use for *which*
// operator is calling — only *that* the caller is a genuinely authenticated
// one. Verifying and discarding the identity here, rather than threading an
// unused value through every handler, is the same "don't build for a
// distinction the data model doesn't have" discipline this repo already
// applies to the roles matrix itself (no ABAC, no third role).
func RequireOperator(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Cookie(operatorauth.SessionCookieName)
		if err != nil || cookie == "" {
			writeError(c, http.StatusUnauthorized, "unauthorized", "missing or invalid operator session")
			c.Abort()
			return
		}
		if _, err := operatorauth.VerifySession(secret, cookie, time.Now()); err != nil {
			writeError(c, http.StatusUnauthorized, "unauthorized", "missing or invalid operator session")
			c.Abort()
			return
		}
		c.Next()
	}
}

// RequireJSONContentType is this repo's CSRF mitigation for the operator
// surface, applied to every state-changing (non-GET) request. It is
// deliberately not a synchronizer/double-submit CSRF token — see
// 12-session-handoff.md and docs/SDLC-EVIDENCE.md for the full reasoning
// this comment summarizes:
//
//  1. The session cookie is SameSite=Strict (05-api-contracts.md), not Lax.
//     A Strict cookie is never attached to any cross-site request at all —
//     not top-level navigation, not a subresource fetch, not a form
//     submission from another origin — in every evergreen browser. Contrast
//     privacy-forge's T-11/T-12/T-13 threat model, which defended a
//     server-rendered session cookie that (like most login sessions) had to
//     tolerate top-level cross-site navigation and therefore couldn't rely
//     on Strict alone; pulsewatch's frontend is a pure SPA with no
//     server-rendered page a cross-site link needs to land on with the
//     session intact, so Strict was already the documented choice and
//     carries no such cost here.
//  2. Given that, the classical CSRF vector (attacker page triggers a
//     request, the victim's cookie rides along, the server acts on it) has
//     no request shape that can even occur: there is no cross-site request
//     this cookie is ever attached to in the first place.
//  3. This handler is real defense-in-depth for the residual case a
//     SameSite implementation bug or a legacy non-conforming browser would
//     represent: an HTML form (the only way a browser sends a
//     *same-origin-cookie-bearing* cross-origin request without
//     JavaScript) cannot set an arbitrary Content-Type — only
//     application/x-www-form-urlencoded, multipart/form-data, or
//     text/plain. Every mutating handler in this package only ever accepts
//     application/json, so a bare HTML form POST can never satisfy it.
//     JavaScript could set the header, but cross-origin JavaScript cannot
//     attach credentials to a request against this API at all: this server
//     enables no CORS policy (no Access-Control-Allow-Origin), so a
//     browser refuses a cross-origin credentialed fetch/XHR before the
//     request is even sent with the cookie attached.
//
// A bespoke synchronizer token would duplicate protection this combination
// (SameSite=Strict + no CORS + JSON-only bodies) already provides
// structurally, for a single-operator self-hosted tool with no third-party
// integrator ever expected to POST into this API — copying privacy-forge's
// token mechanism here without this reasoning would be exactly the kind of
// un-examined pattern-reuse this session was asked to avoid.
func RequireJSONContentType() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodDelete {
			c.Next()
			return
		}
		contentType := c.GetHeader("Content-Type")
		if !strings.HasPrefix(contentType, "application/json") {
			writeError(c, http.StatusUnsupportedMediaType, "unsupported_media_type", "request body must be application/json")
			c.Abort()
			return
		}
		c.Next()
	}
}
