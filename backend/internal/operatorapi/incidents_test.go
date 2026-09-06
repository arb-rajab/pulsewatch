package operatorapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestGetTargetIncidents_EmptyForFreshTarget(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-incidents-empty@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	created := createTestTarget(t, r, pool, cookie, "http://example.invalid/incidents-empty")

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+created.ID+"/incidents", cookie, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var incidents []incidentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &incidents); err != nil {
		t.Fatalf("decode incidents response: %v", err)
	}
	if len(incidents) != 0 {
		t.Fatalf("expected no incidents for a freshly-created target, got %+v", incidents)
	}
}

func TestGetTargetIncidents_UnknownIdReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-incidents-notfound@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+placeholderID+"/incidents", cookie, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestGetTargetIncidents_OpenAndResolvedNewestFirst proves the three things
// this handler adds on top of a bare SELECT: status is derived correctly
// for both an open (closed_at NULL) and a resolved (closed_at set) row,
// and ordering is opened_at DESC (newest first), not insertion order.
func TestGetTargetIncidents_OpenAndResolvedNewestFirst(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-incidents-mixed@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	created := createTestTarget(t, r, pool, cookie, "http://example.invalid/incidents-mixed")

	var olderResolvedID int64
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO incidents (target_id, opened_at, closed_at) VALUES ($1::uuid, now() - interval '2 hours', now() - interval '1 hour') RETURNING id`,
		created.ID).Scan(&olderResolvedID); err != nil {
		t.Fatalf("insert resolved test incident: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(t.Context(), `DELETE FROM incidents WHERE id = $1`, olderResolvedID) })

	var newerOpenID int64
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO incidents (target_id, opened_at) VALUES ($1::uuid, now()) RETURNING id`,
		created.ID).Scan(&newerOpenID); err != nil {
		t.Fatalf("insert open test incident: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(t.Context(), `DELETE FROM incidents WHERE id = $1`, newerOpenID) })

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+created.ID+"/incidents", cookie, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var incidents []incidentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &incidents); err != nil {
		t.Fatalf("decode incidents response: %v", err)
	}
	if len(incidents) != 2 {
		t.Fatalf("expected exactly 2 incidents, got %+v", incidents)
	}

	if incidents[0].ID != newerOpenID {
		t.Fatalf("expected the newer incident first (opened_at DESC), got id=%d first", incidents[0].ID)
	}
	if incidents[0].Status != "open" || incidents[0].ClosedAt != nil {
		t.Fatalf("expected the newer incident to read status=open, closed_at=nil, got %+v", incidents[0])
	}
	if incidents[0].TargetID != created.ID {
		t.Fatalf("expected target_id=%s, got %q", created.ID, incidents[0].TargetID)
	}

	if incidents[1].ID != olderResolvedID {
		t.Fatalf("expected the older incident second (opened_at DESC), got id=%d second", incidents[1].ID)
	}
	if incidents[1].Status != "resolved" || incidents[1].ClosedAt == nil {
		t.Fatalf("expected the older incident to read status=resolved with a non-nil closed_at, got %+v", incidents[1])
	}
}

// TestGetTargetIncidents_ScopedToOwnTarget proves this endpoint never
// leaks another target's incident history — a real risk for a
// non-parameterized WHERE clause bug, not a hypothetical one.
func TestGetTargetIncidents_ScopedToOwnTarget(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-incidents-scoped@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	targetA := createTestTarget(t, r, pool, cookie, "http://example.invalid/incidents-scoped-a")
	targetB := createTestTarget(t, r, pool, cookie, "http://example.invalid/incidents-scoped-b")

	var incidentAID int64
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO incidents (target_id, opened_at) VALUES ($1::uuid, now()) RETURNING id`,
		targetA.ID).Scan(&incidentAID); err != nil {
		t.Fatalf("insert incident for target A: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(t.Context(), `DELETE FROM incidents WHERE id = $1`, incidentAID) })

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+targetB.ID+"/incidents", cookie, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var incidents []incidentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &incidents); err != nil {
		t.Fatalf("decode incidents response: %v", err)
	}
	if len(incidents) != 0 {
		t.Fatalf("expected target B to have no incidents (target A's incident must not leak), got %+v", incidents)
	}
}
