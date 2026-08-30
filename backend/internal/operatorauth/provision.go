package operatorauth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// provisionDB is the minimal surface CreateOperator/CountOperators need —
// mirrors agentauth's own provisionDB interface, satisfied by *pgxpool.Pool
// (the only real caller: cmd/provision-operator).
type provisionDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Created is the result of provisioning a new operator account. Unlike
// agentauth.Created, there is no one-time token here — an operator
// authenticates with the password they themselves chose, not a generated
// credential shown once.
type Created struct {
	ID    string
	Email string
}

// CountOperators reports how many operator rows exist — used by
// cmd/provision-operator to enforce 02-requirements.md's single-operator
// baseline at the bootstrapping tool itself, not just as an unenforced
// convention.
func CountOperators(ctx context.Context, db provisionDB) (int, error) {
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM operators`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count operators: %w", err)
	}
	return count, nil
}

// CreateOperator is the real operator-provisioning mechanism: hash the
// chosen password and insert the row. Exported for cmd/provision-operator to
// call — this repo's bootstrapping answer to the same first-account
// chicken-and-egg problem bookslot solves for its own first-owner account,
// solved the same honest way: a real CLI calling a real function, never a
// hardcoded default credential shipped in code or a migration.
func CreateOperator(ctx context.Context, db provisionDB, email, password string) (Created, error) {
	hash, err := HashPassword(password)
	if err != nil {
		return Created{}, fmt.Errorf("hash operator password: %w", err)
	}

	const stmt = `INSERT INTO operators (email, password_hash) VALUES ($1, $2) RETURNING id::text`
	var id string
	if err := db.QueryRow(ctx, stmt, email, hash).Scan(&id); err != nil {
		return Created{}, fmt.Errorf("insert operator %q: %w", email, err)
	}
	return Created{ID: id, Email: email}, nil
}
