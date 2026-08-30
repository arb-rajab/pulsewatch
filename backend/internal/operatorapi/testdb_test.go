package operatorapi

import (
	"bytes"
	"context"
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
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE id = $1::uuid`, created.ID)
	})
	return created.ID, created.Token
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
