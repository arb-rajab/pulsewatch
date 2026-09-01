# Data Model
> Purpose: the authoritative description of stored data
> Project: pulsewatch (public)
> Last updated: 2026-08-29
> Depth: baseline (Architecture & Design is a baseline-depth phase per
> `docs/SDLC-EVIDENCE.md`) — but the retention/rollup schema below is
> deliberately concrete, not sketched, because Session 1 rejected
> TimescaleDB specifically to protect the learning budget
> (`00-project-brief.md` §3), which makes an efficient plain-Postgres
> rollup/retention design a real consequence of that decision this session
> has to resolve, not a detail to wave through.

## ERD

```mermaid
erDiagram
    TARGETS ||--o| TARGET_SCHEDULE : "has one"
    TARGETS ||--o{ CHECK_RESULTS : "produces"
    TARGETS ||--o{ CHECK_ROLLUPS_HOURLY : "aggregates into"
    TARGETS ||--o{ INCIDENTS : "opens"
    TARGETS }o--o| AGENTS : "assigned to (nullable)"
    INCIDENTS ||--o{ ALERT_DISPATCHES : "triggers"
    ALERT_CHANNELS ||--o{ ALERT_DISPATCHES : "receives"

    TARGETS {
        uuid id PK
        text type "http | tcp"
        text url_or_host
        int port "nullable, tcp"
        text body_match_pattern "nullable"
        int interval_seconds "10..86400, FR-003"
        int failure_threshold "default 3, NFR-011"
        int timeout_seconds
        uuid agent_id FK "nullable — null means server-executed"
        timestamptz created_at
        timestamptz deleted_at "nullable, soft delete"
    }
    TARGET_SCHEDULE {
        uuid target_id PK_FK
        timestamptz next_due_at
        timestamptz last_checked_at "nullable"
        text lease_owner "nullable, ADR-0001"
        timestamptz lease_expires_at "nullable, ADR-0001"
        int streak "ADR-0002"
        text state "healthy | suspect | alerting, ADR-0002"
    }
    CHECK_RESULTS {
        bigint id PK
        uuid target_id FK
        timestamptz checked_at
        bool success
        int latency_ms
        int status_code "nullable, http"
        text failure_reason "nullable — timeout | refused | status_mismatch | body_mismatch"
        text body_match_fragment "nullable, capped 512 bytes"
        bool body_match_fragment_truncated
    }
    CHECK_ROLLUPS_HOURLY {
        uuid target_id PK_FK
        timestamptz hour_bucket PK
        int expected_checks
        int success_count
        int failure_count
        int unknown_count
        int p50_latency_ms
        int p95_latency_ms
    }
    INCIDENTS {
        bigint id PK
        uuid target_id FK
        timestamptz opened_at
        timestamptz closed_at "nullable"
    }
    ALERT_CHANNELS {
        uuid id PK
        text type "webhook | email"
        text destination_encrypted "FR-023, write-only after creation"
        timestamptz created_at
    }
    ALERT_DISPATCHES {
        bigint id PK
        bigint incident_id FK
        uuid alert_channel_id FK
        text kind "opened | resolved"
        timestamptz dispatched_at
        bool delivery_confirmed
    }
    AGENTS {
        uuid id PK
        text name
        text credential_hash
        int report_interval_seconds
        timestamptz last_heartbeat_at "nullable"
        timestamptz registered_at
    }
    OPERATORS {
        uuid id PK
        text email
        text password_hash
    }
```

## Entity descriptions

| Entity | Purpose | Key attributes | Classification |
|---|---|---|---|
| `targets` | Operator-registered check configuration (US-001/US-002) | type, url/host, interval, threshold, `agent_id` (null = server-executed, non-null = agent-executed, ADR-0003) | Confidential — infrastructure topology (`02-requirements.md`) |
| `target_schedule` | Hot, frequently-updated scheduling + alerting-evaluation state, 1:1 with `targets` | `next_due_at`/`last_checked_at` (FR-006), `lease_owner`/`lease_expires_at` (ADR-0001), `streak`/`state` (ADR-0002) | Internal — derived operational state, not independently classified |
| `check_results` | Raw per-check outcomes (FR-007), the input to rollups | outcome, latency, failure reason, capped body-match fragment | Internal, low sensitivity, with the body-fragment caveat below |
| `check_rollups_hourly` | Hourly aggregates driving rolling-window SLO math (FR-008/FR-010), retained far longer than raw data | counts split success/failure/**unknown**, p50/p95 latency | Internal, low sensitivity |
| `incidents` | Durable alerting-state history (FR-012); also the exactly-once dispatch guard (ADR-0002) | opened_at, closed_at (nullable = currently open) | Internal |
| `alert_channels` | Operator-configured webhook/email destinations (FR-013/FR-014) | encrypted destination, write-only after creation | **Secret** — FR-023 |
| `alert_dispatches` | Record of each attempted notification, for exactly-once verification and debugging | kind (opened/resolved), delivery_confirmed | Internal |
| `agents` | Registered agent instances and their liveness (ADR-0003, NFR-008) | credential hash, report_interval, last_heartbeat_at | Agent credential: **Secret** |
| `operators` | Single-operator account (v1: exactly one row in practice, but modeled as a table, not a config constant, so the schema doesn't have to change if that ever isn't true) | email, password_hash | **Secret** (password) |

**No `operator_id` FK on `targets` or `alert_channels`, and no
`OPERATORS`-to-`TARGETS`/`ALERT_CHANNELS` relationship in the ERD above
(resolved, was previously a flagged inconsistency):** an earlier version
of this ERD drew `OPERATORS ||--o{ TARGETS` and `OPERATORS ||--o{
ALERT_CHANNELS` arrows implying an ownership column, while neither
entity's attribute table below ever listed one — Session 4's migrations
correctly built exactly what the attribute tables specified, which meant
the arrows, not the schema, were wrong. `02-requirements.md`'s roles
matrix states v1's single-operator shape explicitly as "the actual v1
shape," not a placeholder for future role separation, and
`01-scope-and-non-goals.md` lists multi-user auth/role separation as a
non-goal "matching the actual use case" — not a deferred-but-planned
feature. With exactly one operator row and no authorization decision that
will ever branch on it, an `operator_id` column would be speculative
schema for a feature this repo has explicitly decided not to build,
not a fix for a documented gap. The arrows have been removed so the ERD
matches the real schema; if a future session ever reopens multi-operator
support, that is a new `operator_id` migration written against that real
requirement, not this one.

## Invariants and where they are enforced

- **Never two overlapping checks against the same target** (FR-005,
  NFR-007) — enforced by `target_schedule`'s atomic conditional claim
  (ADR-0001), not by application-level locking alone.
- **At most one open incident per target** (US-005, FR-016) — enforced by
  a partial unique index: `CREATE UNIQUE INDEX incidents_one_open_per_target ON incidents (target_id) WHERE closed_at IS NULL;`.
  This is a hard schema constraint, not just the conditional-`INSERT`
  application guard in ADR-0002 — a bug that bypasses the guard still
  cannot create a second concurrently-open incident; Postgres rejects it.
- **Pulsewatch-downtime and agent-staleness periods excluded from SLO
  math, not counted as down** (FR-011, FR-019) — enforced at rollup-
  computation time (below), not by a separate flag on `check_results`
  (there is nothing to flag: an unknown period is the *absence* of
  `check_results` rows for a target during a span checks were expected).
- **Alert-channel credentials never returned in plaintext after creation**
  (FR-023) — `destination_encrypted` is application-level encrypted at
  rest; the read API for `alert_channels` never selects that column after
  the initial creation response.
- **Response-body-match fragments do not capture arbitrary response
  content** — `body_match_fragment` is capped at 512 bytes
  (`body_match_fragment_truncated` set `true` when the actual match
  context exceeded that). This resolves the open caveat flagged in
  `02-requirements.md`'s data classification: 512 bytes is enough to show
  an operator what did or didn't match without storing large fractions of
  a monitored target's response body — the fragment is illustrative, not a
  response archive.

## Rollup and retention strategy (plain PostgreSQL, no TimescaleDB)

Session 1's decision (`00-project-brief.md` §3) rejected TimescaleDB to
protect the two-technology learning budget. This section is the concrete
consequence that decision defers to this session: an efficient
400-day-retention rolling-window design on vanilla Postgres.

### Raw data: declarative range partitioning by day, dropped not deleted

`check_results` is a Postgres-native **declarative range-partitioned**
table, partitioned by `checked_at` into one partition per day. Retention
(default 30 days, NFR-006, operator-configurable) is enforced by the
retention job **dropping** whole partitions once fully past the retention
window (`DROP TABLE check_results_2026_07_01;`), not by row-by-row
`DELETE`. At this project's actual scale (~5–10 targets, 30–60s interval —
on the order of 10–30k raw rows/day system-wide, per
`00-project-brief.md`), a `DELETE ... WHERE checked_at < X` would work too,
but partition-drop is chosen because it is **O(1)** regardless of row
count, produces no dead-tuple bloat requiring `VACUUM` to reclaim, and is
the standard, well-documented plain-Postgres technique for exactly this
problem — the same "declarative partitioning is a Postgres core feature,
not a new technology" reasoning `00a-ledger-confirmation.md` already
applies to Postgres itself. Each partition keeps the
`(target_id, checked_at)` index `00-project-brief.md` §3 already commits
to.

**Ordering constraint (retention must never race ahead of rollup):** the
retention job only drops a partition once the rollup job has already
produced `check_rollups_hourly` rows covering that partition's full date
range — a partition is retention-eligible when `age > retention_days AND
fully_rolled_up`, not merely `age > retention_days`. This is what makes
NFR-006 ("removed within one rollup-job cycle of crossing the boundary")
and rollup correctness (SM4) both hold together instead of racing.

### Rollups: a single hourly tier, retained 400 days

One rollup table, `check_rollups_hourly`, computed by an hourly Go
background job (NFR-005) reading the just-completed hour's
`check_results` partition and writing one row per `(target_id,
hour_bucket)`:

- `success_count`, `failure_count` — straight counts from `check_results`.
- `unknown_count` — see below; this is the FR-011/FR-019 carve-out made
  concrete at the schema level.
- `p50_latency_ms`, `p95_latency_ms` — via `percentile_cont` over that
  hour's successful checks.
- `expected_checks` — `(min(bucket_end, now) - max(bucket_start,
  target.created_at)) / target.interval_seconds`, floored at 0. Computed
  from `max(bucket_start, target.created_at)` specifically so a target
  registered mid-hour doesn't show a phantom deficit for the portion of
  the hour before it existed — that isn't "unknown," it's "didn't exist
  yet," and the rollup must not conflate the two.
- `unknown_count = max(0, expected_checks - success_count - failure_count)`
  — the number of checks that should have happened (given the target's
  configured interval) but produced no `check_results` row at all. This is
  deliberately **count-based, not time-weighted**: pulsewatch is a
  check-based system, not a continuous-monitoring one, so representing
  uptime as `success_count / (success_count + failure_count)` over a
  window — with `unknown_count` excluded from both — is the natural fit
  for what's actually being measured, and matches FR-011's own language
  ("excluded from both the numerator and the denominator").

A single 400-day-retained hourly tier (NFR-012) is deliberately **not**
split into a second, coarser daily tier. At this project's scale, 400 days
× 24 buckets/target ≈ 9,600 rows per target — trivially small for Postgres
to scan directly with an index on `(target_id, hour_bucket)`, comfortably
inside NFR-009's ≤500ms p95 dashboard read budget without needing a
second, pre-aggregated tier. A daily tier would be premature machinery for
a data volume this small; if a later session's *measured* query latency
disagrees, add one then, against evidence (mirroring the same
revisit-on-evidence discipline `00-project-brief.md` §3 already applies to
the TimescaleDB question itself).

### Distinguishing the two causes of "unknown" (not surfaced to the
operator as a choice, but computed correctly regardless of cause)

`unknown_count`'s cause differs by whether the target is server-executed
or agent-executed, but both land in the same column — FR-011's requirement
is "excluded," not "the dashboard must explain why":

- **Server-executed target, pulsewatch itself was down:** if the server
  wasn't running, *no* `check_results` rows exist for *any*
  server-executed target during that span — a gap correlated across every
  server-executed target simultaneously. The rollup job detects this
  structurally: `expected_checks - actual` is simply positive because no
  attempts were made, with no special-cased "was pulsewatch down" flag
  needed — the absence of rows already is the signal.
  reconstruct the exact edges of that gap from `target_schedule`'s own
  `last_checked_at` bookkeeping across all targets (ADR-0001) if finer
  attribution is ever wanted later; not required for FR-011 as written.
- **Agent-executed target, that specific agent went stale:** `agents.
  last_heartbeat_at` directly identifies the affected window
  (ADR-0003/NFR-008), scoped to only that agent's assigned targets — other
  targets' rollups are unaffected.

## Indexing strategy

- `target_schedule`: index on `next_due_at` (the due-set scan ADR-0001 and
  ADR-0004 both depend on); primary key `target_id`.
- `check_results`: partitioned by `checked_at` (day range); per-partition
  index on `(target_id, checked_at)`.
- `check_rollups_hourly`: primary key `(target_id, hour_bucket)`, which
  also serves as the index a rolling-window query scans.
- `incidents`: partial unique index `(target_id) WHERE closed_at IS NULL`
  (the open-incident invariant above); supporting index `(target_id,
  opened_at)` for incident-history queries (US-009, FR-022).
- `agents`: unique index on credential identifier; lookups are always by
  `agent_id`, no additional index needed for staleness checks.

## Migration approach and rollback

Standard forward-only Go migrations (tooling choice — e.g. `golang-
migrate` or `goose` — deferred to Session 4/Implementation, not decided
here). Schema-only migrations (new column, new index) are written with a
paired down-migration. Migrations that touch partitioning structure (e.g.
changing the partition interval) are treated as data-affecting by default
and require an explicit, reviewed down-migration decision at the time they
are written, not a generic reversibility promise made in the abstract now.

## Retention and deletion rules

- `check_results`: raw partitions dropped once past the retention window
  (default 30 days, NFR-006) **and** rolled up — see ordering constraint
  above.
- `check_rollups_hourly`: rows deleted past 400 days (NFR-012,
  operator-configurable) via ordinary row `DELETE` — small volume (≈9,600
  rows/target ceiling) doesn't justify partitioning at this tier.
- `incidents` / `alert_dispatches`: retained indefinitely — small volume,
  genuine historical value, and no FR/NFR asks for their deletion.
- `alert_channels`: retained until the operator replaces or deletes it
  (`02-requirements.md` data classification).

## Session 12 addendum: the hourly rollup job, real, implemented

Everything above this section was designed in Session 3 but never
implemented — `check_rollups_hourly` had zero rows until this session.
Session 12's own objective was exactly this: build FR-008's hourly rollup
job (`backend/internal/rollup`) and `GET /targets/{id}/slo`
(`backend/internal/operatorapi/slo.go`), reading only from this table, per
`05-api-contracts.md`'s existing "never a raw-row scan" contract. This
section records the real decisions that implementation required.

### On-demand vs. pre-aggregated: already decided, not reopened

This wasn't a fresh choice Session 12 had to make — the "Rollup and
retention strategy" section above already committed to pre-aggregation (an
hourly Go background job) back in Session 3/4, specifically to protect the
plain-Postgres/no-TimescaleDB decision (`00-project-brief.md` §3) from
turning `/slo` into an expensive raw-row scan over `check_results` at read
time. Session 12 implemented exactly that, not an on-demand alternative.

### The rollup job's own internal design choice: recompute-everything, every tick

Within "pre-aggregated," a real choice remained: bounded incremental
lookback with a separate one-time backfill pass, or unconditionally
recomputing every historical `(target, hour_bucket)` row on every tick.
`internal/rollup.RunOnce` takes the second, simpler option — one SQL
statement, `INSERT ... ON CONFLICT DO UPDATE`, covering every target's every
fully-completed hour since that target's own `created_at`, run hourly
(NFR-005's own stated cadence) plus once immediately on process startup.

This is deliberately simple, not an oversight: this document's own "single
hourly tier, not a second daily tier" reasoning above already establishes
this project's real rollup volume as trivially small for Postgres (≤9,600
rows/target over the full 400-day retention ceiling) — the identical
argument extends to recomputing that whole small volume repeatedly. The
payoff is that the job is **self-healing** for any late-arriving
`check_results` row (an agent-reported OTLP result can genuinely arrive
after its own hour has already been rolled up) with no separate "which
buckets are dirty" bookkeeping table to maintain and keep correct.

**Measured, not assumed:** against this instance's real data (4 real
targets, ~2 days of continuous history, 3,412 real `check_results` rows),
the first-ever run — a full backfill from zero, at container startup —
wrote 181 rows in **350ms**. Comfortably inside NFR-005's ≤5-minute bound,
with roughly 14x headroom before this specific design choice would need
revisiting against real evidence, the same revisit-on-evidence discipline
this document already applies to its own no-second-tier decision. If a
future session's measured tick duration ever disagrees (i.e., the project's
real scale grows well past `00-project-brief.md`'s stated ~5-10 targets), a
bounded lookback plus a one-time backfill is the documented fallback design,
not a redesign from scratch.

**Correctness verified against real data, not just runtime:** cross-checked
`SUM(success_count + failure_count)` across every `check_rollups_hourly` row
against `count(*)` from `check_results` for every hour before the current
in-progress one — 3,412 = 3,412, an exact match on this instance's real
history.

### The `window_days`/`slo_target_pct` scope question — resolved in favor of the existing contract

Session 12's own task framing said to pick one fixed rolling window this
session (configurability was named explicitly out of scope) — but
`05-api-contracts.md` and the already-`@redocly/cli`-validated
`openapi.yaml` (Session 3.5, predating Session 12) already specify
`window_days` (default 30, range 1–400) and `slo_target_pct` (default 99.9,
range 0–100) as **request-time query parameters** on `/targets/{id}/slo` —
the identical "a lens applied over existing rollup data at read time, never
stored configuration" pattern the contract already used for
`slo_target_pct` alone.

Resolved by implementing the endpoint exactly as the existing, committed
contract specifies (both parameters, with their existing defaults and
bounds), and satisfying "pick one fixed window" at the **dashboard call
site** instead of the API layer: `frontend/src/routes/dashboard/+page.server.ts`
always calls `/slo` with no query parameters (the backend's own defaults —
30 days, 99.9%), and exposes no window picker in the UI. Reimplementing the
backend endpoint to reject `window_days` would have meant silently
diverging from a previously-validated, committed spec five sessions later,
for a distinction ("a query parameter" vs. "per-target stored
configuration") the out-of-scope note was actually drawing — not "the
existing contract itself is now overbuilt." No stored per-target SLO
configuration was added; none was needed.

30 days (the contract's existing default) was kept rather than switching to
this session's own illustrative "e.g. 24h" example, specifically because 30
days was already a real, previously-reasoned decision (`05-api-contracts.md`
Session 3.5), while 24h was only ever an example in this session's task
framing, not a requirement — deviating from a real committed default toward
an example for no functional reason would be change for its own sake.

### `uptime_pct`/`error_budget_consumed_pct` edge cases (implementation-level, not schema-level)

Two arithmetic edge cases the schema doesn't spell out, resolved in
`operatorapi/slo.go` and covered by `slo_test.go`:

- **Zero observed success-or-failure checks in the window** (e.g. a target
  younger than the window, or a window that's entirely "unknown"):
  `uptime_pct` reports `100.0` — vacuously true, no failure was ever
  observed — rather than null (the schema has no null variant) or a
  divide-by-zero. The dashboard renders this case as "no data yet" instead
  of "100.00%", since presenting a vacuous 100% as a real, earned uptime
  figure would overstate confidence for a target with nothing observed at
  all.
- **`slo_target_pct=100`** (a real, in-range request value — schema maximum
  is 100): makes `error_budget_consumed_pct`'s denominator zero. Saturates
  rather than emitting a JSON-illegal `Infinity`: `0` if `uptime_pct` is
  also `100`, else `100` (budget fully consumed against an
  infinitely-strict target).

### A real finding this session's own verification surfaced: test-cleanup DB pollution, amplified

Running this session's own `go test ./...` against the real, persistent
local `docker-compose` Postgres (not CI's ephemeral per-run service
container) left 78 orphaned test-fixture `targets` rows behind on the first
run, visible on the real operator dashboard alongside the 4 genuine targets.
Root cause: `check_results` (and now `check_rollups_hourly`) reference
`targets` with a plain `REFERENCES`, deliberately not `ON DELETE CASCADE`
(real historical data shouldn't silently vanish if a target is ever
hard-deleted) — but several packages' test fixtures (`scheduler`,
`agentapi`, `alerting`, and `operatorapi`'s pre-Session-12 tests) clean up
with a best-effort `DELETE FROM targets` that silently swallows the
resulting FK-violation error. `alerting/testdb_test.go` already documented
this as an accepted trade-off before this session.

**The full root cause took two more occurrences this session to pin down
precisely.** It isn't only "packages whose own tests explicitly write
`check_results`": this project's own live `pulsewatch-backend-1` container
runs its real scheduler continuously against the same real local Postgres a
`go test ./...` run also uses. That scheduler's 1-second tick picks up *any*
newly-inserted `targets` row with `agent_id IS NULL` — including a
transient fixture from a test that never itself calls check-execution code,
like `operatorapi`'s `/status`/`/slo`/CRUD tests — and executes a real check
against it within about a second, writing a real `check_results` row.
Session 12's own new `check_rollups_hourly` table adds the same exposure on
top: `internal/rollup`'s job recomputes every historical bucket on every
tick (the design decision above), so any test-fixture target that exists at
tick time gets a rollup row too. Session 12's own new test fixtures
(`internal/rollup`, `operatorapi/slo_test.go`) were given "delete child rows
before the target row" cleanup, which meaningfully reduces the exposure —
but doesn't eliminate it, because a live scheduler/rollup tick can still
land in the small window between a test's own non-transactional cleanup
steps. This is why the same class of leak recurred a second and third time
within this session even after the first fix (see `10-risk-register.md`'s
R-005 for the full, corrected account and the real mitigation options for a
future session: stop the local backend container before running tests,
wrap cleanup in a transaction, or apply the ordering fix everywhere).

The broader, pre-existing `check_results`/`incidents` non-cascading cleanup
gap across other packages was **not** fixed (out of scope for this
session's bounded objective) — see `11-backlog.md`'s B-006. The 78, then a
further 23, then a further 4 orphaned rows created across this session's
three test runs were each deleted from the real database after confirming,
by `created_at`, that they were exactly this session's own test-run
artifacts and not real or prior-session data — see this session's own
handoff for the full before/after evidence.
