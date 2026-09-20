# ADR-0009 — A Serverless Function as a Third, Parallel Deployment Target: OTLP Payload Normalization

- **Date:** 2026-09-19
- **Status:** accepted

## Context

ADR-0005 made Kubernetes (via Terraform + kustomize) this project's real,
production deployment target, alongside Docker Compose for local
development. Both are always-on: a long-lived process holds a database
connection pool and a webhook dispatcher for the lifetime of the pod. That
shape is correct for the whole application — the scheduler leases targets
continuously, the alerting state machine has to see every check result to
compute a streak, and `NotifyChannels` needs a live connection to write
`alert_dispatches`. None of that is a good fit for a short-lived,
pay-per-invocation function, and nothing in this session forces it to be.

What this session asked for was narrower: does *any* piece of this system's
functionality genuinely fit a serverless, edge-deployed invocation model,
well enough to demonstrate the pattern for real rather than force it onto
code that doesn't want it? `backend/internal/agentapi` (ADR-0003) is where
to look, because `POST /v1/logs` is this system's one inbound,
webhook-shaped surface: an external caller (the reference agent binary, or
the OTel Collector's exporter pipeline in the full deployment shape) posts
a JSON body and gets a response back, with no session or long-lived
connection of its own.

Reading `logs.go` end to end shows that endpoint does two genuinely
different things in sequence:

1. **Parse and validate the wire format** — decode the OTLP JSON, dispatch
   on `pulsewatch.event_type`, and for a `check_result` record, range-check
   `pulsewatch.latency_ms`/`pulsewatch.status_code` before the int64→int32
   narrowing conversion (the exact fix for CodeQL's
   `go/incorrect-integer-conversion` finding the file's own comments
   document). This part touches no external state. Given the same bytes, it
   always produces the same normalized record or the same rejection reason.
2. **Persist and dispatch** — look up whether the target belongs to the
   authenticated agent (`lookupAssignedTarget`), write through
   `alerting.RecordCheckResult` inside a transaction
   (`recordAgentCheckResult`), and call `alerting.NotifyChannels` on a state
   transition. This part is exactly the incident-lifecycle write path this
   session was told not to touch, and it structurally cannot run without a
   database connection and the caller's verified agent identity.

Step 1 is the stateless, pure-function half of a webhook receiver: no DB,
no dispatcher, no per-caller identity, safe to invoke as many times as
retries need, and cheap enough that most invocations should cost nothing.
It is also the one place a malformed or hostile payload first gets shaped
before touching anything with a database connection. That combination —
pure transform, no ambient state, useful at the edge of the request path —
is what "genuinely well-suited to serverless" means here, and it is the
piece this session extracted.

## Decision

### 1. `serverless/otlp-normalizer/` is a new, independent Go module — a parallel capability, not an extraction from `backend/internal/agentapi`

It is deployed as an AWS Lambda function (Go, `provided.al2023` custom
runtime, built as `bootstrap` via `aws-lambda-go`) behind an HTTP API,
defined in an AWS SAM template. It accepts the same
`{"logRecords": [...]}` shape `POST /v1/logs` embeds (one `timeUnixNano` +
`attributes[]` record, `pulsewatch.event_type` distinguishing
`heartbeat`/`check_result`, same string/int OTLP `AnyValue` encoding) and
returns, for each record, either a normalized `checkResult`/`heartbeat` or
a rejection reason — the same field-by-field validation rules
`processLogRecord`/`processCheckResult` apply, including the identical
int32 range checks on latency and status code.

**Considered and rejected: importing `backend/internal/agentapi`'s types
and factoring the validation logic into a shared package both sides
call.** That would have been the more DRY choice, and was the first design
tried. It was rejected because it fails this session's own explicit
constraint in practice, not just in spirit: `agentapi.OtlpLogRecord`/
`OtlpKeyValue`/`OtlpValue` and the constants naming their attribute keys
are used unqualified throughout `backend/internal/agentapi/logs_test.go`
and directly as `agentapi.X` from `backend/cmd/agent`'s reference client
(`client.go`, `main.go`). Moving them to a new package to make them
importable from a Lambda module means editing every one of those call
sites in the real, currently-green ingestion pipeline — exactly the
incident/agent-facing surface this session was told to leave alone — to
serve a parallel deployment target that doesn't need to touch it at all.
A ~90-line, independently tested reimplementation of a stable, narrowly-
scoped wire contract (`otlp.go` in the new module) costs far less than that
risk, and keeps the two deployment targets genuinely decoupled: a change to
this Lambda's Go module graph (a new `aws-lambda-go` version, for instance)
can never affect what `go build ./...` resolves for the real backend, and
vice versa.

### 2. This is additive, not a replacement — both paths exist, and only one of them writes anything

`POST /v1/logs` on the K8s-deployed backend is completely unchanged by this
session: same file, same tests, same behavior, still the only path that
actually persists a check result or opens an incident. The Lambda is a
second, independent front door to the *same validation contract*, useful
on its own terms:

- As a fast, cheap pre-validation step an API Gateway or edge layer can run
  in front of the real ingestion endpoint, rejecting a malformed batch (a
  buggy agent build, a broken exporter config) in a few milliseconds
  without spending a database connection or a pod's CPU on it.
- As a way to validate an OTLP payload's shape from a context that
  shouldn't hold agent bearer tokens or database credentials at all — a CI
  check on a new agent build, or a third party integrating against this
  wire format who wants to confirm their payload is well-formed before
  ever presenting real credentials to the stateful endpoint.

It does not, and structurally cannot, replace any part of `POST /v1/logs`:
it has no agent identity to check `lookupAssignedTarget`'s ownership rule
against, and no database to write `check_results`/`target_schedule` rows
or call `alerting.NotifyChannels`. A caller that wants a check result
actually recorded still has exactly one path: the real, unchanged K8s
endpoint.

### 3. Local verification: SAM build succeeds; `sam local invoke`'s container pull is blocked by this sandbox's own egress policy, not by the function

`sam build` (via the module's `Makefile`, `BuildMethod: makefile` in
`template.yaml`) produces a real `provided.al2023` Linux/amd64 `bootstrap`
binary from this module — the literal artifact SAM would deploy — with no
network access beyond the Go module proxy already used to build the rest
of this repo. `sam validate --lint` passes against `template.yaml`.

`sam local invoke` was also attempted, and failed for a reason worth
recording precisely: it needs to pull
`public.ecr.aws/sam/emulation-provided.al2023` to run the built binary
inside a container that emulates the real Lambda runtime, and this
sandbox's egress proxy returns `403 Forbidden` on that pull (confirmed
independently — `docker pull hello-world` against Docker Hub fails
identically). That is an environment-level policy denial, not a defect in
this function or its template; a normal developer machine with unrestricted
Docker Hub/ECR Public access runs `sam local invoke -e events/valid-batch.json`
against this exact module unmodified.

In place of the blocked container run, verification here is the same
question `sam local invoke` answers — does the built handler, given a real
API Gateway event, return the correct response — asked directly: `go test
./...` calls `handleRequest` with `events.APIGatewayV2HTTPRequest` values
built from the same JSON bodies as `events/valid-batch.json` and
`events/invalid-latency.json`, and separately exercises `normalizeRecord`
against the identical boundary cases
`backend/internal/agentapi/logs_test.go`'s
`TestIngestLogs_LatencyMSBoundaries`/`TestIngestLogs_StatusCodeBoundaries`
already prove for the real endpoint, so this reimplementation is checked
against the same ground truth, not just its own assumptions about the
contract.

## What is and isn't verified

**Verified, for real:**

- `go build ./...` and `go vet ./...` are clean; `go test ./...` passes,
  including boundary-value tests for `pulsewatch.latency_ms`/
  `pulsewatch.status_code` mirroring the real endpoint's own boundary
  tests, malformed-timestamp/missing-field/unknown-event-type rejection,
  and heartbeat vs. check_result dispatch.
- `sam build` produces a real `provided.al2023` deployable artifact from
  this module, and `sam validate --lint` accepts `template.yaml`.
- `handleRequest` (the actual Lambda entry point `lambda.Start` wires up)
  was invoked directly with `events.APIGatewayV2HTTPRequest` values built
  from the same request bodies committed under `events/`, producing the
  expected `200`/normalized-batch and `422`/malformed-body responses.

**Not verified, and named rather than implied:** no invocation ran inside
the actual containerized Lambda runtime emulator (`sam local invoke`) or
against a real deployed API Gateway + Lambda, because this sandbox's egress
policy blocks the container image pull both require (see Decision §3). No
real AWS account, credentials, or deployment was used or is required to
reproduce anything in this ADR.

## Consequences

- `serverless/otlp-normalizer/go.mod` is its own module (`go 1.25.0`,
  `github.com/aws/aws-lambda-go`), independent of `backend/go.mod` — a
  second Go module in this repo, not a second package inside the existing
  one. `go build ./...` / `go test ./...` run from `backend/` are entirely
  unaffected by it.
- `.gitignore` gains `.aws-sam/` and `samconfig.toml` (SAM's own build
  output and optional local deploy config), the same treatment
  `.terraform/`/`.tfstate` already get for ADR-0005's IaC.
- The normalization rules now exist in two places (`backend/internal/
  agentapi`'s real endpoint and this module) by deliberate choice — see
  Decision §1. If the wire contract changes, both need updating; this
  module's own boundary tests are written to mirror the real endpoint's
  test names/cases specifically so that drift shows up as a failing test
  here, not as a silent divergence.
- This does not change `docs/architecture/openapi.yaml`: that document
  still describes exactly one ingestion endpoint, the real one. The Lambda
  is deployment-target documentation (this ADR) plus its own
  `template.yaml`, not a second entry in the public API contract, since it
  is not part of the contract any external agent is expected to call for
  recording a real result.
- `.github/workflows/ci.yml` gains a `serverless-otlp-normalizer` job,
  scoped to `serverless/otlp-normalizer/` and independent of the `backend`/
  `frontend` jobs: `go vet`/`go test -race`, then `sam build` and `sam
  validate --lint` (installed via `pip install aws-sam-cli`, no AWS
  credentials or container runtime needed for either). It does not attempt
  `sam local invoke` or any deploy step, for the reason Decision §3 names.
  No image-publish step was added — this function is not deployed anywhere
  by this repo's CI, only built and tested; wiring a real `sam deploy` to
  an AWS account is a later session's decision to make, not this one's.
