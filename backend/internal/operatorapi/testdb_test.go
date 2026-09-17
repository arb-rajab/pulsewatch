package operatorapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentauth"
	"github.com/arb-rajab/pulsewatch/backend/internal/operatorauth"
)

// defaultTestDatabaseURL matches every other package's own test convention.
const defaultTestDatabaseURL = "postgres://pulsewatch:pulsewatch@localhost:5432/pulsewatch?sslmode=disable"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = defaultTestDatabaseURL
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("skipping: could not create postgres pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("skipping: postgres not reachable at %s: %v", url, err)
	}

	t.Cleanup(pool.Close)
	return pool
}

// testEncryptionKey mirrors agentapi's own fixed, non-secret 32-byte test
// key exactly — alert_channels is a global table shared by every package's
// test suite against the same Postgres.
var testEncryptionKey = []byte{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
}

// testSessionSecret is a fixed, non-secret HMAC key for these tests —
// real code paths never see this value, only operatorauth.SigningSecretFromEnv
// reads the real env-configured one.
var testSessionSecret = []byte("test-only-fixed-32-byte-secret-k")

func testRouter(pool *pgxpool.Pool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterRoutes(r, pool, testSessionSecret, testEncryptionKey)
	return r
}

// insertTestOperator creates a real operator via the real provisioning path
// (operatorauth.CreateOperator) — never a hand-rolled row.
func insertTestOperator(t *testing.T, pool *pgxpool.Pool, email, password string) string {
	t.Helper()
	created, err := operatorauth.CreateOperator(t.Context(), pool, email, password)
	if err != nil {
		t.Fatalf("create test operator: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM operators WHERE id = $1::uuid`, created.ID)
	})
	return created.ID
}

// insertTestAgentForCredentialShapeTest creates a real agent via the real
// agentauth provisioning path — used only to prove operatorapi's
// operatorSession middleware rejects a syntactically different credential
// shape (an agentToken bearer value), never to exercise agentauth itself
// (that package has its own exhaustive tests).
func insertTestAgentForCredentialShapeTest(t *testing.T, pool *pgxpool.Pool) (id, token string) {
	t.Helper()
	created, err := agentauth.CreateAgent(t.Context(), pool, "test-credential-shape-agent", 60)
	if err != nil {
		t.Fatalf("create test agent: %v", err)
	}
	deleteAgentCleanup(t, pool, created.ID)
	return created.ID, created.Token
}

// deleteAgentCleanup deletes an agent on a fresh, non-canceled context —
// never t.Context() inside the t.Cleanup closure itself (B-006: see
// deleteTargetCascade below for why that context is already canceled by
// the time Cleanup functions run). Any target still assigned to this agent
// (plain REFERENCES, no ON DELETE CASCADE) must be cleaned up or
// unassigned first — same ordering requirement as deleteTargetCascade's
// own child tables.
func deleteAgentCleanup(t *testing.T, pool *pgxpool.Pool, agentID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE id = $1::uuid`, agentID)
	})
}

// realSessionCookie issues a real, valid session token via operatorauth
// directly (bypassing the login flow, which is exercised by its own
// dedicated tests) — the value every other handler test needs to prove a
// gated endpoint accepts a genuinely valid session.
func realSessionCookie(t *testing.T, operatorID string) string {
	t.Helper()
	token, _, err := operatorauth.IssueSession(testSessionSecret, operatorID, time.Now())
	if err != nil {
		t.Fatalf("issue test session: %v", err)
	}
	return token
}

// doRequest is a small httptest convenience: build a request (optional
// session cookie and JSON body), run it through the router, return the
// recorder.
func doRequest(t *testing.T, r *gin.Engine, method, path, sessionCookie string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	var bodyReader *bytes.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	} else {
		bodyReader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, path, bodyReader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if sessionCookie != "" {
		req.AddCookie(&http.Cookie{Name: operatorauth.SessionCookieName, Value: sessionCookie})
	}
	// A real client always sets this explicitly on a mutating request, body
	// or not (e.g. POST .../credential/rotate takes no body by design) —
	// RequireJSONContentType's CSRF mitigation depends on exactly that,
	// since an HTML form can submit a body-less POST just as easily as one
	// with a body.
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// mustRequest/recordRequest give tests that need to set headers doRequest
// doesn't parameterize (e.g. a deliberately non-JSON Content-Type) direct
// access to the underlying *http.Request before it's served.
func mustRequest(t *testing.T, method, path string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}

func recordRequest(r *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// randomHex gives each test's device token a unique value — device_tokens
// is a global table these tests don't get an isolated view of, exactly like
// alert_channels.
func randomHex(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("read random suffix: %v", err)
	}
	return hex.EncodeToString(buf)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal test body: %v", err)
	}
	return encoded
}

// cleanupDeviceToken deletes a registration at test end. Unlike this
// package's older fixtures (B-006), this one genuinely completes: nothing
// references device_tokens.
func cleanupDeviceToken(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM device_tokens WHERE id = $1::uuid`, id)
	})
}

// deleteTargetCascade removes a target and every row that a plain
// REFERENCES-only child table (no ON DELETE CASCADE) still holds against
// it, in dependency order, before deleting the target itself.
//
// Root cause of B-006 in this package specifically: targets_test.go,
// status_test.go, and agents_test.go's own createTestTarget/inline target
// cleanups predate this file's shared testPool/t.Cleanup convention and
// used `t.Context()` *inside* their t.Cleanup closures. testing.T.Context
// is documented as "canceled just before Cleanup-registered functions are
// called" — so that context was already canceled the instant each closure
// ran, and pool.Exec returned context.Canceled immediately, without ever
// reaching Postgres. The delete wasn't merely losing a race with a foreign
// key; it never ran at all. This helper fixes both problems at once: a
// fresh, non-canceled context (matching every other package's own
// convention), and child-before-parent deletes so a target an in-process
// check pipeline has already attached incidents/check_results/rollups to
// still deletes cleanly. target_schedule needs no explicit delete: it's
// the one child table with ON DELETE CASCADE (see its own migration's
// comment).
func deleteTargetCascade(pool *pgxpool.Pool, targetID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = pool.Exec(ctx, `DELETE FROM alert_dispatches WHERE incident_id IN (SELECT id FROM incidents WHERE target_id = $1::uuid)`, targetID)
	_, _ = pool.Exec(ctx, `DELETE FROM incidents WHERE target_id = $1::uuid`, targetID)
	_, _ = pool.Exec(ctx, `DELETE FROM check_results WHERE target_id = $1::uuid`, targetID)
	_, _ = pool.Exec(ctx, `DELETE FROM check_rollups_hourly WHERE target_id = $1::uuid`, targetID)
	_, _ = pool.Exec(ctx, `DELETE FROM targets WHERE id = $1::uuid`, targetID)
}
