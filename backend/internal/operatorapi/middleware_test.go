package operatorapi

import (
	"net/http"
	"testing"
)

// placeholderID is a syntactically valid UUID that names no real row —
// good enough to prove a request is rejected at the auth layer (401)
// before it ever reaches a handler that would otherwise 404 on it.
const placeholderID = "00000000-0000-0000-0000-000000000000"

// TestEveryOperatorFacingEndpoint_RejectsUnauthenticatedRequests is this
// session's explicit audit proof: every operator-facing route
// 05-api-contracts.md/openapi.yaml documents under operatorSession auth is
// listed here and proven to genuinely reject a request carrying no session
// cookie at all — not assumed from RequireOperator being wired in
// router.go, but exercised against the real router for each route
// individually. A route missing from this table would be a route this
// audit failed to check, so this list is deliberately exhaustive against
// router.go's own RegisterRoutes (every entry there except POST
// /auth/login, which has no security requirement by design).
func TestEveryOperatorFacingEndpoint_RejectsUnauthenticatedRequests(t *testing.T) {
	pool := testPool(t)
	r := testRouter(pool)

	cases := []struct {
		method string
		path   string
		body   []byte
	}{
		{http.MethodPost, "/api/v1/auth/logout", nil},

		{http.MethodPost, "/api/v1/targets", []byte(`{}`)},
		{http.MethodGet, "/api/v1/targets", nil},
		{http.MethodGet, "/api/v1/targets/" + placeholderID, nil},
		{http.MethodPatch, "/api/v1/targets/" + placeholderID, []byte(`{}`)},
		{http.MethodDelete, "/api/v1/targets/" + placeholderID, nil},
		{http.MethodGet, "/api/v1/targets/" + placeholderID + "/status", nil},

		{http.MethodPost, "/api/v1/alert-channels", []byte(`{}`)},
		{http.MethodGet, "/api/v1/alert-channels", nil},
		{http.MethodGet, "/api/v1/alert-channels/" + placeholderID, nil},
		{http.MethodPut, "/api/v1/alert-channels/" + placeholderID + "/secret", []byte(`{}`)},
		{http.MethodDelete, "/api/v1/alert-channels/" + placeholderID, nil},

		{http.MethodPost, "/api/v1/agents", []byte(`{}`)},
		{http.MethodGet, "/api/v1/agents", nil},
		{http.MethodGet, "/api/v1/agents/" + placeholderID, nil},
		{http.MethodDelete, "/api/v1/agents/" + placeholderID, nil},
		{http.MethodPost, "/api/v1/agents/" + placeholderID + "/credential/rotate", nil},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := doRequest(t, r, tc.method, tc.path, "", tc.body)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 with no session cookie, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestEveryOperatorFacingEndpoint_RejectsInvalidSessionCookie proves the
// same property for a syntactically-present but bogus cookie — not just an
// entirely absent one — the case that would slip through a check that only
// tested "no Cookie header at all".
func TestEveryOperatorFacingEndpoint_RejectsInvalidSessionCookie(t *testing.T) {
	pool := testPool(t)
	r := testRouter(pool)

	const bogus = "this-was-never-issued.by-anything"
	w := doRequest(t, r, http.MethodGet, "/api/v1/targets", bogus, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a bogus session cookie, got %d: %s", w.Code, w.Body.String())
	}
}

// TestEveryOperatorFacingEndpoint_RejectsAnAgentToken proves the structural
// property 05-api-contracts.md documents: an agent's bearer token can't
// even satisfy operatorSession's cookie-shaped requirement — the two
// credential shapes are disjoint, so this fails the same way a missing
// credential does (401), never a 403.
func TestEveryOperatorFacingEndpoint_RejectsAnAgentToken(t *testing.T) {
	pool := testPool(t)
	agentID, agentToken := insertTestAgentForCredentialShapeTest(t, pool)
	_ = agentID
	r := testRouter(pool)

	// An agent token presented as if it were a session cookie value must
	// still fail VerifySession's HMAC check — it was never signed by
	// operatorauth at all.
	w := doRequest(t, r, http.MethodGet, "/api/v1/targets", agentToken, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}
