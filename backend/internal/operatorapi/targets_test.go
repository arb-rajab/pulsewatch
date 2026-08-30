package operatorapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestTargets_FullCrudLifecycle_ThroughRealGatedHTTP(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-targets-crud@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	// Create an HTTP target.
	createBody := []byte(`{"type":"http","url":"http://example.invalid/targets-crud","interval_seconds":30,"timeout_seconds":5}`)
	wCreate := doRequest(t, r, http.MethodPost, "/api/v1/targets", cookie, createBody)
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", wCreate.Code, wCreate.Body.String())
	}
	var created targetResponse
	if err := json.Unmarshal(wCreate.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM targets WHERE id = $1::uuid`, created.ID)
	})
	if created.URL == nil || *created.URL != "http://example.invalid/targets-crud" {
		t.Fatalf("expected url echoed back, got %+v", created.URL)
	}
	if created.FailureThreshold != defaultFailureThreshold {
		t.Fatalf("expected the default failure_threshold=%d, got %d", defaultFailureThreshold, created.FailureThreshold)
	}

	// A target_schedule row must exist so the scheduler actually picks it
	// up (US-001) — this is the real bug this session's own audit found:
	// no prior code path ever inserted this row for an operator-created
	// target.
	var scheduleCount int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM target_schedule WHERE target_id = $1::uuid`, created.ID).Scan(&scheduleCount); err != nil {
		t.Fatalf("count target_schedule rows: %v", err)
	}
	if scheduleCount != 1 {
		t.Fatalf("expected exactly 1 target_schedule row for the new target, got %d", scheduleCount)
	}

	// Get it back.
	wGet := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+created.ID, cookie, nil)
	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wGet.Code, wGet.Body.String())
	}

	// List includes it.
	wList := doRequest(t, r, http.MethodGet, "/api/v1/targets", cookie, nil)
	if wList.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wList.Code, wList.Body.String())
	}
	var list []targetResponse
	if err := json.Unmarshal(wList.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	found := false
	for _, item := range list {
		if item.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("expected the newly created target to appear in the list")
	}

	// Patch mutable fields.
	patchBody := []byte(`{"interval_seconds":60,"failure_threshold":5}`)
	wPatch := doRequest(t, r, http.MethodPatch, "/api/v1/targets/"+created.ID, cookie, patchBody)
	if wPatch.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wPatch.Code, wPatch.Body.String())
	}
	var patched targetResponse
	if err := json.Unmarshal(wPatch.Body.Bytes(), &patched); err != nil {
		t.Fatalf("decode patch response: %v", err)
	}
	if patched.IntervalSeconds != 60 || patched.FailureThreshold != 5 {
		t.Fatalf("expected updated fields to persist, got %+v", patched)
	}
	if patched.URL == nil || *patched.URL != "http://example.invalid/targets-crud" {
		t.Fatal("expected the immutable url field to remain unchanged by PATCH")
	}

	// Delete (soft).
	wDelete := doRequest(t, r, http.MethodDelete, "/api/v1/targets/"+created.ID, cookie, nil)
	if wDelete.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", wDelete.Code, wDelete.Body.String())
	}

	// A soft-deleted target 404s on GET but its row (and history) still exists.
	wGetAfterDelete := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+created.ID, cookie, nil)
	if wGetAfterDelete.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a soft-deleted target, got %d: %s", wGetAfterDelete.Code, wGetAfterDelete.Body.String())
	}
	var deletedAt *string
	if err := pool.QueryRow(t.Context(), `SELECT deleted_at::text FROM targets WHERE id = $1::uuid`, created.ID).Scan(&deletedAt); err != nil {
		t.Fatalf("read deleted_at: %v", err)
	}
	if deletedAt == nil {
		t.Fatal("expected deleted_at to be set (soft delete), not the row removed entirely")
	}
}

func TestCreateTarget_RejectsHttpTargetMissingUrl(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-targets-badhttp@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	w := doRequest(t, r, http.MethodPost, "/api/v1/targets", cookie, []byte(`{"type":"http","interval_seconds":30,"timeout_seconds":5}`))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateTarget_RejectsTcpTargetMissingPort(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-targets-badtcp@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	w := doRequest(t, r, http.MethodPost, "/api/v1/targets", cookie, []byte(`{"type":"tcp","host":"db.invalid","interval_seconds":30,"timeout_seconds":5}`))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateTarget_RejectsIntervalOutOfBounds(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-targets-badinterval@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	w := doRequest(t, r, http.MethodPost, "/api/v1/targets", cookie, []byte(`{"type":"http","url":"http://example.invalid/x","interval_seconds":5,"timeout_seconds":5}`))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for interval_seconds below the 10s minimum, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetTarget_UnknownIdReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-targets-notfound@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+placeholderID, cookie, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
