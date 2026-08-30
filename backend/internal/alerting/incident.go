package alerting

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// incidentDB is the minimal surface OpenIncident/CloseIncident need —
// satisfied by both *pgxpool.Pool and pgx.Tx, so the same guarded write
// works standalone (this package's own concurrency proofs) or inside the
// scheduler's release transaction (ADR-0002: "the same transaction as the
// check-result insert"). Begin is used only by OpenIncident, to isolate a
// lost race — see its own doc comment for why that matters.
type incidentDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// postgresUniqueViolation is the SQLSTATE Postgres raises when an INSERT
// conflicts with a unique index — including the partial unique index
// incidents_one_open_per_target (04-data-model.md) that backs OpenIncident
// below.
const postgresUniqueViolation = "23505"

// OpenIncident is ADR-0002's guarded "cross the threshold" write: an INSERT
// gated by an application-level NOT EXISTS check, backed by the
// incidents_one_open_per_target partial unique index at the schema level.
// Two callers can both pass the NOT EXISTS check before either commits — a
// real, provable race, not a hypothetical one (see
// incident_concurrency_test.go) — so the index, not this function's own
// guard, is what makes the outcome exactly-once: Postgres allows only one of
// the resulting INSERTs to actually complete; the loser's INSERT fails with
// a unique-violation error.
//
// That failure aborts whatever transaction it happened in — Postgres will
// not accept any further statement in an aborted transaction until it is
// rolled back. Since OpenIncident is called from inside
// scheduler.releaseAndRecord's own larger transaction (check_results
// insert, this incidents write, then the target_schedule update — all one
// commit, per ADR-0002), a lost race must not be allowed to poison that
// outer transaction and lose the check result along with it. So
// OpenIncident always runs its INSERT inside its own Begin/Commit: when db
// is a bare pool, that's a real, independent transaction; when db is
// already an in-progress pgx.Tx (releaseAndRecord's case), pgx implements
// that nested Begin as a SAVEPOINT, and Commit/Rollback on it become RELEASE
// SAVEPOINT / ROLLBACK TO SAVEPOINT — containing a lost race to just this
// one guarded write and leaving the caller's outer transaction perfectly
// usable either way. This was found and fixed via
// TestOpenIncident_WithoutUniqueIndex_DuplicatesOccur_ThenRestored's own
// restore-and-reverify step, which failed with exactly this
// transaction-poisoning symptom before this fix.
//
// A nil *DispatchRequest with a nil error means "no row was written" — via
// either guard: the NOT EXISTS check saw an incident already open, or this
// caller lost the race and Postgres rejected its INSERT with a unique
// violation. Both mean the same thing to the caller: don't dispatch.
func OpenIncident(ctx context.Context, db incidentDB, targetID string) (*DispatchRequest, error) {
	subTx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin open-incident guard for target %s: %w", targetID, err)
	}
	defer func() { _ = subTx.Rollback(ctx) }() // no-op once committed

	const stmt = `
INSERT INTO incidents (target_id, opened_at)
SELECT $1::uuid, now()
WHERE NOT EXISTS (SELECT 1 FROM incidents WHERE target_id = $1::uuid AND closed_at IS NULL)
RETURNING id`

	var incidentID int64
	err = subTx.QueryRow(ctx, stmt, targetID).Scan(&incidentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // rolled back via defer — nothing was written, nothing to keep
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == postgresUniqueViolation {
			return nil, nil // lost the race — rolled back via defer, outer transaction (if any) is unaffected
		}
		return nil, fmt.Errorf("open incident for target %s: %w", targetID, err)
	}

	if err := subTx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit open-incident guard for target %s: %w", targetID, err)
	}
	return &DispatchRequest{IncidentID: incidentID, TargetID: targetID, Kind: "opened"}, nil
}

// CloseIncident is ADR-0002's guarded resolution write: the same
// exactly-once shape as OpenIncident, on the other edge transition. Only
// one incidents row can ever be open per target (the same partial unique
// index guarantees that), so a concurrent double-close can't create a
// duplicate the way a double-open could — but the conditional
// "WHERE closed_at IS NULL RETURNING id" still guards a caller racing
// against another caller that already closed it: zero rows, no dispatch.
// Unlike OpenIncident, an UPDATE matching zero rows is not a Postgres error
// at all (nothing aborts), so this needs no nested transaction of its own —
// it runs directly against whatever db it's given.
func CloseIncident(ctx context.Context, db incidentDB, targetID string) (*DispatchRequest, error) {
	const stmt = `UPDATE incidents SET closed_at = now() WHERE target_id = $1::uuid AND closed_at IS NULL RETURNING id`

	var incidentID int64
	err := db.QueryRow(ctx, stmt, targetID).Scan(&incidentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("close incident for target %s: %w", targetID, err)
	}
	return &DispatchRequest{IncidentID: incidentID, TargetID: targetID, Kind: "resolved"}, nil
}

// ApplyIncidentAction dispatches to Open/Close/no-op per a Transition's
// Action, so the scheduler's release path doesn't need its own switch over
// IncidentAction.
func ApplyIncidentAction(ctx context.Context, db incidentDB, targetID string, action IncidentAction) (*DispatchRequest, error) {
	switch action {
	case IncidentActionOpen:
		return OpenIncident(ctx, db, targetID)
	case IncidentActionClose:
		return CloseIncident(ctx, db, targetID)
	default:
		return nil, nil
	}
}
