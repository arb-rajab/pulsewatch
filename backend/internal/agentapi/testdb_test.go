package agentapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentauth"
	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

// defaultTestDatabaseURL mirrors every other package's own test convention
// (scheduler, alerting, agentauth) — the CI backend job's Postgres service
// container.
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

func testRouter(pool *pgxpool.Pool, dispatcher alerting.Dispatcher, channelKey []byte) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterRoutes(r, pool, dispatcher, channelKey, nopLogger())
	return r
}

func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// insertTestAgent creates a real agent via the real provisioning path
// (agentauth.CreateAgent) — never a hand-rolled row with a fake hash.
func insertTestAgent(t *testing.T, pool *pgxpool.Pool, name string, reportIntervalSeconds int) (id, token string) {
	t.Helper()
	created, err := agentauth.CreateAgent(t.Context(), pool, name, reportIntervalSeconds)
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

// testTargetOpts configures insertAssignedTarget's fixture.
type testTargetOpts struct {
	targetType       string
	urlOrHost        string
	port             *int32
	intervalSeconds  int
	timeoutSeconds   int
	failureThreshold int
}

func defaultTestTargetOpts() testTargetOpts {
	return testTargetOpts{
		targetType:       "http",
		urlOrHost:        "http://example.invalid/agentapi-test",
		intervalSeconds:  60,
		timeoutSeconds:   5,
		failureThreshold: 3,
	}
}

// insertAssignedTarget creates a real target (agent_id = agentID) + its
// target_schedule row — the fixture both assignments_test.go and
// logs_test.go need, since GET /agent/assignments and POST /v1/logs both
// operate only on agent-assigned targets.
func insertAssignedTarget(t *testing.T, pool *pgxpool.Pool, agentID string, opts testTargetOpts) string {
	t.Helper()
	ctx := t.Context()

	var targetID string
	err := pool.QueryRow(ctx, `
INSERT INTO targets (type, url_or_host, port, interval_seconds, timeout_seconds, failure_threshold, agent_id)
VALUES ($1, $2, $3, $4, $5, $6, $7::uuid)
RETURNING id::text`,
		opts.targetType, opts.urlOrHost, opts.port, opts.intervalSeconds, opts.timeoutSeconds, opts.failureThreshold, agentID,
	).Scan(&targetID)
	if err != nil {
		t.Fatalf("insert assigned target: %v", err)
	}

	_, err = pool.Exec(ctx, `INSERT INTO target_schedule (target_id, next_due_at) VALUES ($1::uuid, now())`, targetID)
	if err != nil {
		t.Fatalf("insert target_schedule: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM targets WHERE id = $1::uuid`, targetID)
	})

	return targetID
}

func fetchStreakState(t *testing.T, pool *pgxpool.Pool, targetID string) (streak int, state string) {
	t.Helper()
	if err := pool.QueryRow(t.Context(), `SELECT streak, state FROM target_schedule WHERE target_id = $1::uuid`, targetID).Scan(&streak, &state); err != nil {
		t.Fatalf("fetch streak/state: %v", err)
	}
	return streak, state
}

func countCheckResultsFor(t *testing.T, pool *pgxpool.Pool, targetID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM check_results WHERE target_id = $1::uuid`, targetID).Scan(&count); err != nil {
		t.Fatalf("count check_results: %v", err)
	}
	return count
}

// testEncryptionKey mirrors alerting's and scheduler's own testEncryptionKey
// exactly (a fixed, non-secret 32-byte key) — alert_channels is a global
// table shared by every package's test suite against the same Postgres, so
// using the same fixed key everywhere avoids one package's tests failing to
// decrypt another's leftover rows.
var testEncryptionKey = []byte{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
}

// insertTestAlertChannel creates a real alert_channels row — without one,
// NotifyChannels correctly has nowhere to dispatch to at all (zero
// configured channels is not an error, per alerting.NotifyChannels' own doc
// comment), so any test asserting a real dispatch happened needs this
// fixture first, exactly like scheduler's own
// alert_lifecycle_test.go:insertTestAlertChannel.
func insertTestAlertChannel(t *testing.T, pool *pgxpool.Pool, destination string) string {
	t.Helper()
	encrypted, err := alerting.EncryptDestination(destination, testEncryptionKey)
	if err != nil {
		t.Fatalf("encrypt test destination: %v", err)
	}
	var channelID string
	err = pool.QueryRow(t.Context(), `
INSERT INTO alert_channels (type, destination_encrypted) VALUES ('webhook', $1) RETURNING id::text`, encrypted,
	).Scan(&channelID)
	if err != nil {
		t.Fatalf("insert test alert_channel: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM alert_channels WHERE id = $1::uuid`, channelID)
	})
	return channelID
}

func fetchLastHeartbeat(t *testing.T, pool *pgxpool.Pool, agentID string) *time.Time {
	t.Helper()
	var at *time.Time
	if err := pool.QueryRow(t.Context(), `SELECT last_heartbeat_at FROM agents WHERE id = $1::uuid`, agentID).Scan(&at); err != nil {
		t.Fatalf("fetch last_heartbeat_at: %v", err)
	}
	return at
}

// spyDispatcher records every Dispatch call it receives, keyed by channel
// id — mirrors scheduler's own spyDispatcher (alert_lifecycle_test.go)
// exactly, including the channel-id scoping: alert_channels is a global
// table, and other packages' test suites may have their own real channel
// rows alive concurrently against the same shared test Postgres (go test
// runs different packages' test binaries in parallel by default), so a
// raw total call count would be spuriously inflated by dispatches to
// channels this test never created. Scoping by this test's own channelID
// (callsFor) is what scheduler's own tests already rely on for exactly
// this reason.
type spyDispatcher struct {
	mu    sync.Mutex
	calls []spyCall
}

type spyCall struct {
	channelID string
	req       alerting.DispatchRequest
}

func (s *spyDispatcher) Dispatch(_ context.Context, channel alerting.Channel, req alerting.DispatchRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, spyCall{channelID: channel.ID, req: req})
	return nil
}

func (s *spyDispatcher) callsFor(channelID string) []alerting.DispatchRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []alerting.DispatchRequest
	for _, c := range s.calls {
		if c.channelID == channelID {
			out = append(out, c.req)
		}
	}
	return out
}

// doRequest is a small httptest convenience: build a request (with an
// optional bearer token and JSON body), run it through the router, return
// the recorder.
func doRequest(t *testing.T, r *gin.Engine, method, path, bearerToken string, body []byte) *httptest.ResponseRecorder {
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
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
