package operatorapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// insertTestRollup writes a real check_rollups_hourly row directly — this
// package's tests exercise GetTargetSlo in isolation from
// internal/rollup's own job (which has its own dedicated tests proving the
// rollup computation itself is correct); GetTargetSlo's contract is "read
// check_rollups_hourly correctly," not "compute it."
func insertTestRollup(t *testing.T, targetID string, hourBucket time.Time, expected, success, failure, unknown int) {
	t.Helper()
	pool := testPool(t)
	_, err := pool.Exec(t.Context(), `
INSERT INTO check_rollups_hourly (target_id, hour_bucket, expected_checks, success_count, failure_count, unknown_count)
VALUES ($1::uuid, $2, $3, $4, $5, $6)
ON CONFLICT (target_id, hour_bucket) DO UPDATE SET
    expected_checks = EXCLUDED.expected_checks,
    success_count = EXCLUDED.success_count,
    failure_count = EXCLUDED.failure_count,
    unknown_count = EXCLUDED.unknown_count`,
		targetID, hourBucket, expected, success, failure, unknown,
	)
	if err != nil {
		t.Fatalf("insert test rollup: %v", err)
	}
	// check_rollups_hourly has a plain REFERENCES to targets (no ON DELETE
	// CASCADE) — without its own cleanup, this row would block
	// createTestTarget's later `DELETE FROM targets`, the same class of leak
	// internal/rollup's own test fixtures guard against.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM check_rollups_hourly WHERE target_id = $1::uuid AND hour_bucket = $2`, targetID, hourBucket)
	})
}

func TestGetTargetSlo_ComputesRealUptimeFromRollups(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-slo-uptime@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	created := createTestTarget(t, r, pool, cookie, "http://example.invalid/slo-uptime")

	// Two hours of real rollup data inside the default 30-day window: 90
	// success + 10 failure = 90% uptime, checkable arithmetic.
	hourBucket := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	insertTestRollup(t, created.ID, hourBucket, 50, 45, 5, 0)
	insertTestRollup(t, created.ID, hourBucket.Add(1*time.Hour), 50, 45, 5, 0)

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+created.ID+"/slo", cookie, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var slo targetSloResponse
	if err := json.Unmarshal(w.Body.Bytes(), &slo); err != nil {
		t.Fatalf("decode slo response: %v", err)
	}

	if slo.SuccessCount != 90 || slo.FailureCount != 10 {
		t.Fatalf("expected success=90 failure=10, got success=%d failure=%d", slo.SuccessCount, slo.FailureCount)
	}
	if slo.ExpectedChecks != 100 {
		t.Fatalf("expected expected_checks=100, got %d", slo.ExpectedChecks)
	}
	wantUptime := 90.0
	if diff := slo.UptimePct - wantUptime; diff > 0.001 || diff < -0.001 {
		t.Fatalf("expected uptime_pct=%.1f, got %.4f", wantUptime, slo.UptimePct)
	}
	if slo.WindowDays != defaultWindowDays {
		t.Fatalf("expected default window_days=%d, got %d", defaultWindowDays, slo.WindowDays)
	}
	if slo.SloTargetPct != defaultSloTargetPct {
		t.Fatalf("expected default slo_target_pct=%v, got %v", defaultSloTargetPct, slo.SloTargetPct)
	}
	// error_budget_consumed_pct = (100 - 90) / (100 - 99.9) * 100 = 10000.
	wantBudget := 10000.0
	if diff := slo.ErrorBudgetConsumedPct - wantBudget; diff > 0.5 || diff < -0.5 {
		t.Fatalf("expected error_budget_consumed_pct=%.1f, got %.4f", wantBudget, slo.ErrorBudgetConsumedPct)
	}
}

func TestGetTargetSlo_NoRollupDataYetReports100PctUptimeNotAnError(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-slo-empty@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	created := createTestTarget(t, r, pool, cookie, "http://example.invalid/slo-empty")

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+created.ID+"/slo", cookie, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var slo targetSloResponse
	if err := json.Unmarshal(w.Body.Bytes(), &slo); err != nil {
		t.Fatalf("decode slo response: %v", err)
	}
	if slo.SuccessCount != 0 || slo.FailureCount != 0 || slo.ExpectedChecks != 0 {
		t.Fatalf("expected all-zero counts for a target with no rollup rows, got %+v", slo)
	}
	if slo.UptimePct != 100.0 {
		t.Fatalf("expected uptime_pct=100.0 (vacuous — no observed failure) for zero observed checks, got %v", slo.UptimePct)
	}
}

func TestGetTargetSlo_WindowDaysNarrowsWhatIsSummed(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-slo-window@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	created := createTestTarget(t, r, pool, cookie, "http://example.invalid/slo-window")

	recentBucket := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	oldBucket := time.Now().UTC().Truncate(time.Hour).Add(-20 * 24 * time.Hour)
	insertTestRollup(t, created.ID, recentBucket, 10, 10, 0, 0)
	insertTestRollup(t, created.ID, oldBucket, 10, 0, 10, 0)

	// window_days=1 must exclude the 20-day-old bucket entirely.
	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+created.ID+"/slo?window_days=1", cookie, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var slo targetSloResponse
	if err := json.Unmarshal(w.Body.Bytes(), &slo); err != nil {
		t.Fatalf("decode slo response: %v", err)
	}
	if slo.SuccessCount != 10 || slo.FailureCount != 0 {
		t.Fatalf("expected window_days=1 to see only the recent bucket (success=10 failure=0), got success=%d failure=%d", slo.SuccessCount, slo.FailureCount)
	}
	if slo.WindowDays != 1 {
		t.Fatalf("expected window_days echoed back as 1, got %d", slo.WindowDays)
	}
}

func TestGetTargetSlo_InvalidWindowDaysIsUnprocessable(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-slo-invalid@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	created := createTestTarget(t, r, pool, cookie, "http://example.invalid/slo-invalid")

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+created.ID+"/slo?window_days=0", cookie, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for window_days=0 (below schema minimum 1), got %d: %s", w.Code, w.Body.String())
	}

	w2 := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+created.ID+"/slo?slo_target_pct=101", cookie, nil)
	if w2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for slo_target_pct=101 (above schema maximum 100), got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestGetTargetSlo_UnknownIdReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-slo-notfound@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+placeholderID+"/slo", cookie, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
