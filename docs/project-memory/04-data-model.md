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
