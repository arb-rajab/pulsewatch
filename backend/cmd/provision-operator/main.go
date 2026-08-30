// Command provision-operator is this repo's real answer to
// 02-requirements.md's single-operator baseline's chicken-and-egg
// bootstrapping problem: there is no operator account for /auth/login to
// authenticate against until one is created, and 05-api-contracts.md's own
// operatorSession-gated surface has no endpoint that could create the first
// one (every operator-facing endpoint, including this same problem's
// analogue for agent provisioning, requires operatorSession auth — see
// cmd/seed-agent's own doc comment). Solved the same honest way bookslot
// solves its own first-owner-account gap: a real, local, DB-connected CLI
// that calls the real provisioning function (operatorauth.CreateOperator),
// never a hardcoded default email/password shipped in code or a migration.
//
// This tool only bootstraps the *first* operator account — it refuses to
// run if one already exists (02-requirements.md: "v1 is single-operator";
// there is no multi-operator feature this tool should silently grow into).
// It does not offer a password-reset/rotate path for an existing operator:
// that is a distinct, real operational need (a lost-password recovery flow)
// this session did not build, flagged here explicitly rather than silently
// left to look implemented — today, recovering a lost operator password
// requires direct database access (e.g. `psql`, UPDATE operators SET
// password_hash = ...` computed via a short Go snippet calling
// operatorauth.HashPassword), the same class of manual workaround this
// project already documents elsewhere for gaps it hasn't built a tool for
// yet.
//
// Usage:
//
//	echo 'a-real-password' | provision-operator -email you@example.com
//
// The password is read from stdin, one line, trimmed of its trailing
// newline — deliberately not a -password flag, which would leave the
// plaintext visible in shell history and process listings (`ps`) for the
// life of the process, a real exposure a human-chosen account password
// should not have (contrast agentauth's generated bearer tokens, which
// cmd/seed-agent does pass around as plain output, since those are
// one-time-shown display values with no shell-history/process-list
// motivation to hide differently).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/operatorauth"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "provision-operator:", err)
		os.Exit(1)
	}
}

func run(args []string, in *os.File, out *os.File) error {
	fs := flag.NewFlagSet("provision-operator", flag.ContinueOnError)
	email := fs.String("email", "", "email address for the new operator account (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*email) == "" {
		return errors.New("-email is required")
	}

	password, err := readPassword(in)
	if err != nil {
		return err
	}
	if password == "" {
		return errors.New("no password provided on stdin")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()

	existing, err := operatorauth.CountOperators(ctx, pool)
	if err != nil {
		return fmt.Errorf("check for existing operators: %w", err)
	}
	if existing > 0 {
		return fmt.Errorf("refusing to run: %d operator account(s) already exist — this tool only bootstraps the first operator (02-requirements.md's single-operator baseline); see this command's own package doc for the lost-password-recovery gap this leaves, and use direct database access for that today", existing)
	}

	created, err := operatorauth.CreateOperator(ctx, pool, *email, password)
	if err != nil {
		return fmt.Errorf("create operator: %w", err)
	}

	if _, err := fmt.Fprintf(out, "operator_id: %s\n", created.ID); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if _, err := fmt.Fprintf(out, "email:       %s\n", created.Email); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if _, err := fmt.Fprintln(out, "Operator account created. Log in at POST /api/v1/auth/login with this email and the password you provided."); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func readPassword(in *os.File) (string, error) {
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		return "", nil
	}
	return strings.TrimRight(scanner.Text(), "\r\n"), nil
}
