# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed

- Session number and title: **Session 14 — IaC/Kubernetes Scope Expansion:
  ADR-0005, Terraform Platform Layer, Kubernetes Deployment Path.**
- Objective: the project owner confirmed a deliberate, reasoned expansion
  of this repo's frozen technology budget (Rule D3,
  `00a-ledger-confirmation.md`) to add Terraform and Kubernetes as a real
  second, production-shaped deployment target — giving this repo's own
  claimed deep phases (Release & Deployment, Operations & Maintenance)
  a real rolling-upgrade and declarative-infrastructure story that Docker
  Compose alone structurally cannot demonstrate. Document the decision as
  a proper ADR (not a silent violation of the freeze), update the
  non-goals ledger accordingly, and build the actual Terraform module and
  Kubernetes manifests — not just describe them.
- Status: **complete for what this sandbox could do — explicitly,
  honestly incomplete for real-cluster verification.** Every file is
  written and validated with every offline tool available (`terraform
  fmt`, a real `kubectl kustomize` build, direct manifest inspection).
  No `terraform apply` or `kubectl apply` against live infrastructure was
  possible: this session's sandbox has no outbound access to
  `registry.terraform.io` (confirmed: `connect_rejected` via the egress
  proxy), no Docker daemon (`docker info` fails to reach one), and no
  `kubectl`/`helm`/`terraform` were even installed at session start
  (`terraform` and `kubectl` were installed via direct binary download;
  `helm` could not be — `get.helm.sh` and `github.com` releases were both
  blocked by the same egress policy, which is exactly why the Kubernetes
  workload layer uses plain manifests + Kustomize instead of a Helm chart
  that could never have been `helm lint`-verified in this environment).

## Correcting the task brief this session started from

The task description handed to this session claimed the repo was at
"Session 4 complete... no monitoring logic, agent code, or alerting
exists yet" and asked to confirm Session 5's scheduler/leasing scope
before continuing. **That was stale.** `git log` and every
`docs/project-memory/` file at session start showed Sessions 1–13 already
complete and pushed to `main`: scheduler/leasing (ADR-0001, Session 5),
alert-suppression (ADR-0002, Session 6), the agent (ADR-0003, Session 7),
operator auth (Session 8), the dashboard (Session 9), real TLS (Session
10), the router-split fix (Session 11, R-004), the SLO/rollup job
(Session 12), and a rollup-cadence/real-auth-capture verification pass
(Session 13). This session picked up from the real state (Session 13's
own handoff, next-recommended-session list, and the project owner's
explicit new instruction) rather than re-deriving Session 5's own
already-shipped, already-tested scheduler design from scratch.

## Work completed

### `docs/adr/ADR-0005-iac-terraform-and-kubernetes-deployment.md` (new)
Full options-considered ADR: why Compose-only or hand-applied Kubernetes
manifests are insufficient for this repo's own deep-phase claims, why
Terraform authoring every workload object directly was rejected (mixes
infrequently-changing platform state with per-release image-tag
churn), and why the chosen split — Terraform for platform/cluster state,
Kustomize-based plain manifests for the application workload, applied
separately — is the right shape. Names the sandbox's inability to
`terraform apply`/`kubectl apply` against live infrastructure explicitly,
with what *was* verified and how.

### `docs/project-memory/01-scope-and-non-goals.md` (edited)
The "a third new technology" non-goal row is annotated (not deleted) with
a pointer to ADR-0005: the application-layer freeze (no TimescaleDB,
Prometheus/Grafana, message queue) stays fully in force; the deployment
layer gets a named, reasoned supplementary budget of two (Terraform,
Kubernetes).

### `docs/project-memory/09-decision-log.md` (edited)
New Session 14 entry, short-form index pointing at ADR-0005, stating
plainly that this is an amendment to Rule D3 the project owner confirmed,
not scope creep discovered after the fact.

### `docs/project-memory/10-risk-register.md` (edited)
- New **R-008**: the offline-only validation gap (no live cluster/
  registry access in this sandbox) — open, next review Session 15.
- **R-003** narrowed further: still open for Docker Compose exactly as
  before, but the Kubernetes target's Ingress now routes agent traffic
  through the same real, `cert-manager`-issued TLS certificate the
  operator dashboard uses (impossible to do this safely for Compose's
  Caddy setup without the CA-distribution cost Session 11 explicitly
  named and rejected — a real public cert has no such cost).

### `docs/project-memory/11-backlog.md` (edited)
New **B-009** (real-cluster verification — the next session's actual
objective), **B-010** (a cloud-VM-plus-k3s bootstrap module, deliberately
out of ADR-0005's own BYO-cluster scope), **B-011** (Postgres backup/
restore for the Kubernetes target), **B-012** (a real, pre-existing gap
discovered this session: `06-security-threat-model.md` and
`07-testing-strategy.md` are empty section-header skeletons beyond what
this session added to each — not caused by this session, but surfaced by
it).

### `docs/project-memory/08-deployment-and-operations.md` (edited)
New "Kubernetes (production deployment target)" section: the
rolling-update contract and why ADR-0001's leasing design makes the
old/new-pod overlap window safe; a real migration policy (expand/
contract, backward-compatible-for-one-release) for the first time this
project's migrations must actually survive concurrent old/new backend
code, not just a restart boundary; the agent/server compatibility
contract during a rolling update (ADR-0003's agent-initiated polling
model is what makes this tractable); the TLS/R-003 narrowing; the rollout
procedure; and an explicit "what's not yet real" section.

### `docs/project-memory/06-security-threat-model.md`, `07-testing-strategy.md` (edited)
Both were empty skeletons (section headers, no content) before this
session — a real, pre-existing gap this session did not create but did
discover while trying to add its own scoped content. Added: a Secrets
management section (Kubernetes/Terraform secrets handling, explicitly
scoped to this session's own work, with the broader file gap named as
B-012 rather than silently left unremarked); a CI quality-gate section
covering the new offline validation job and its real limits.

### `infra/terraform/` (new)
`versions.tf`, `variables.tf`, `main.tf`, `outputs.tf`,
`terraform.tfvars.example`, `README.md`. Creates the `pulsewatch`
namespace, a `Secret` mirroring `.env.example`'s own required/optional
values one-for-one, `ingress-nginx` and `cert-manager` via `helm_release`,
and a Let's Encrypt `ClusterIssuer` (defaults to the ACME **staging**
server so a first real apply can't accidentally burn a production
rate-limit slot). BYO-cluster only — does not provision cloud compute
(ADR-0005's Decision; B-010 tracks the alternative). Verified with
`terraform fmt -check -diff -recursive` (clean). `terraform validate`
could not run — no registry access to install the `kubernetes`/`helm`
provider plugins this config requires (`registry.terraform.io`:
`connect_rejected`).

### `deploy/k8s/` (new)
`base/` (Namespace-less — namespace comes from Terraform — Deployments/
Services for `backend` [two Services, `backend-operator`/`backend-agent`,
matching R-004's router split rather than collapsing it], `frontend`,
`redis`, `otel-collector`; a `StatefulSet` + headless Service for
`postgres`; a `Job` for migrations; an `Ingress`; two `NetworkPolicy`
objects restricting Postgres/Redis ingress to `backend` only) and
`overlays/production/` (image-tag placeholders `rollout.sh` sets per
release, an Ingress-hostname patch, a production memory-limit patch).
`rollout.sh` enforces migration-Job-then-Deployments ordering (a
Kubernetes Job has no equivalent of Compose's `depends_on: condition:
service_completed_successfully`). `README.md` per directory.

**Verified for real, offline:** `kubectl kustomize --load-restrictor
LoadRestrictionsNone deploy/k8s/overlays/production` builds successfully
— 18 real Kubernetes objects rendered. Inspected directly (not just "it
didn't error"): image substitution applied correctly
(`ghcr.io/OWNER/pulsewatch-backend:latest` etc.), the two
`configMapGenerator`-produced ConfigMaps' hash-suffixed names were
correctly rewritten in both the `otel-collector` Deployment's and the
migration `Job`'s volume references, the production memory-limit patch
and the Ingress-hostname patch both applied, and every Service/Deployment
selector stayed scoped to exactly `app.kubernetes.io/name` (the
kustomization's `labels` transformer did not leak into selector matching,
confirmed by direct inspection — a real, specific thing that could have
silently broken cross-object matching if `includeSelectors` had defaulted
differently). `--load-restrictor LoadRestrictionsNone` is required and
documented: the kustomization deliberately references
`../../../backend/migrations/*.sql` and
`../../../otel-collector-config.yaml` from outside `deploy/k8s/` so the
two deployment targets can never silently drift out of sync, which
Kustomize's default path-traversal guard otherwise blocks.

**Not verified — named, not hidden:** no live cluster exists in this
sandbox, so no `kubectl apply` ever reached a real API server, no pod
ever reached `Running`, and `rollout.sh`'s dependency on the *standalone*
`kustomize` CLI (`kustomize edit set image` — distinct from `kubectl
kustomize`, which has no `edit` subcommand) was never exercised, since
that binary could not be installed either (same blocked `github.com`
release path as `helm`). `bash -n deploy/k8s/rollout.sh` confirms syntax
only.

### `.github/workflows/ci.yml` (edited)
New `iac-and-k8s-manifests-validate` job (the same two offline checks
above, now gating every push/PR) and new `publish-images` job (builds and
pushes `backend`/`frontend` to GHCR on push to `main` only, gated on the
existing `backend`/`frontend` test jobs) — closing
`08-deployment-and-operations.md`'s own previously-named "no CD pipeline
publishes images anywhere yet" gap for the Kubernetes target
specifically. Verified: `python3 -c "yaml.safe_load(...)"` parses
cleanly; job structure reviewed directly (not executed — this session has
no way to trigger a real GitHub Actions run).

### `docs/project-memory/13-release-notes.md`, `.gitignore` (edited)
Release notes gain an "Unreleased/Added" entry for both new CI jobs and
the Kubernetes deployment target itself, with the "not yet verified
against a live cluster" caveat repeated rather than left implicit.
`.gitignore` gains Terraform state/cache/`*.tfvars` exclusions (with
`terraform.tfvars.example` explicitly un-ignored).

## Decisions made

- **Terraform owns platform state; Kustomize-based plain manifests own
  the application workload; the two are applied separately, never in one
  `terraform apply`.** Chosen specifically because per-release image-tag
  changes (the routine case) would otherwise force a full Terraform
  plan/apply cycle against provider-managed state for what Kubernetes
  itself treats as a one-line change — see ADR-0005's Option C for the
  full rejection reasoning.
- **BYO-cluster, not a cloud-VM-provisioning module.** Matches this
  project's own "agents run on infrastructure the operator controls"
  business assumption (`00-project-brief.md`) and avoids inventing real
  per-cloud-provider credentials/cost this session had no way to test
  against anyway. B-010 tracks the alternative as a distinct,
  separately-scoped future decision.
- **Helm was not used for the application workload, specifically because
  this sandbox could not install the `helm` binary to verify a chart with
  `helm lint`/`helm template`.** Plain Kustomize manifests were chosen in
  part *because* they could be genuinely validated offline
  (`kubectl kustomize`, bundled in the already-available `kubectl`) —
  this is a real, stated reason for the technical choice, not merely an
  ADR footnote. Terraform *does* still use Helm (`helm_release` resources
  for `ingress-nginx`/`cert-manager`) since that layer's correctness could
  not be verified offline either way (no provider registry access), so
  there was no offline-verifiability reason to avoid it there.
- **Migration policy going forward is expand/contract, not "migrations
  just run before the app starts."** The rolling-update overlap window
  (old and new backend pods briefly coexisting) is new with this
  deployment target — Compose's replace-outright restart never created
  it — so a migration that breaks the *previous* release's still-running
  pod during that window is a newly-real hazard this session named
  explicitly (`08-deployment-and-operations.md`) rather than leaving
  implicit.
- **`use_staging_acme` defaults to `true`.** A real first `terraform
  apply` against a real cluster is far more likely to hit a DNS/ingress
  misconfiguration on the first attempt than to succeed cleanly — this
  default means that failure mode costs nothing against Let's Encrypt's
  real production rate limits.

## Real gaps found and named during this session

1. **The task brief that started this session was stale** (see
   "Correcting the task brief" above) — corrected by reading real
   project-memory/git state rather than trusting the brief's own summary.
2. **`06-security-threat-model.md` and `07-testing-strategy.md` are
   empty skeletons** beyond section headers, discovered while trying to
   add this session's own scoped content to each. Not caused by this
   session (predates it, per `git log` — both files were last touched at
   Session 3/1 respectively and never filled in since). Tracked as B-012,
   named as a real, sizeable future session's worth of work, not
   backfilled shallowly under this session's own time pressure.
3. **This sandbox cannot exercise real IaC.** No outbound access to the
   Terraform provider registry or to `github.com`/`get.helm.sh` release
   downloads, no Docker daemon, no pre-installed Kubernetes tooling.
   Named exhaustively above and in R-008/B-009 rather than worked around
   with a fabricated "verified" claim.

## Verification performed (all real, offline; see R-008/B-009 for what remains real-cluster-only)

- `terraform fmt -check -diff -recursive` (`infra/terraform/`): clean
  after one real formatting fix (`main.tf`'s Secret `data` block
  alignment).
- `terraform validate`: attempted, failed as expected — "Missing required
  provider" for both `hashicorp/kubernetes` and `hashicorp/helm`, since
  `terraform init` cannot reach `registry.terraform.io` in this sandbox
  (`connect_rejected`, confirmed directly via `curl`). Not silently
  skipped — the failure and its cause are recorded here and in
  ADR-0005/R-008.
- `kubectl kustomize --load-restrictor LoadRestrictionsNone
  deploy/k8s/overlays/production`: succeeds, 18 objects, output inspected
  directly with a real Python/PyYAML pass (not just eyeballed) — image
  tags, ConfigMap hash-reference rewriting, both patches, and every
  Service/Deployment selector all confirmed correct. Re-run a second time
  after all subsequent edits; byte-identical to the first successful
  render.
- `bash -n deploy/k8s/rollout.sh`: clean.
- `python3 -c "yaml.safe_load(open('.github/workflows/ci.yml'))"`: clean,
  all 7 jobs (5 pre-existing + 2 new) present.
- `docker info`, `command -v terraform/kubectl/helm/kind/k3d/minikube`,
  `curl` against `registry.terraform.io`/`get.helm.sh`/`github.com`: all
  run and their real output is what R-008/ADR-0005/this handoff's own
  "Status" line quote — not summarized from memory.

## Open questions and risks

- **R-008 (opened this session):** offline-only IaC/K8s validation — no
  live cluster or registry access in this sandbox. Open, next review
  Session 15.
- **R-003 (narrowed further this session):** still fully open for Docker
  Compose; structurally mitigated by design (not yet cluster-verified,
  R-008) for the Kubernetes target.
- **R-002, R-005, R-006, R-007 (carried forward, untouched this
  session):** unchanged — see `10-risk-register.md`. This session's scope
  was deployment-layer only; no scheduler/alerting/rollup code was
  touched.
- **B-009 (real-cluster verification), B-010 (cloud-VM bootstrap
  module), B-011 (Kubernetes-target Postgres backup/restore), B-012
  (fill the two empty SDLC-phase docs) — all opened this session.**
- **B-002, B-004, B-005, B-006, B-007, B-008 (carried forward from
  Session 13, untouched):** see `11-backlog.md`.

## Next recommended session

- Proposed session title: **Session 15 — Real-Cluster Verification of
  ADR-0005 (B-009), or `GET /targets/{id}/incidents` (B-002) if no real
  cluster is available to a session yet.** B-009 is the honest, correct
  next step if a future session's environment has real registry/cluster
  access (`terraform init`/`apply`, `deploy/k8s/rollout.sh` against an
  actual cluster, confirming pods reach `Running` and the Ingress issues
  a real staging certificate) — this is not optional polish, it is what
  turns ADR-0005 from "documented" into "proven," matching this project's
  own standard for everything else it calls done. If no such environment
  exists yet, `/incidents` (B-002) remains the best-scoped pure-feature
  alternative, exactly as Session 13's own handoff already reasoned.
  - Do **not** reopen ADR-0001–0005 without new measured/real evidence
    per each one's own Revisit triggers.
  - Do **not** attempt to backfill B-012 (the empty security/testing
    docs) as a side effect of an unrelated session — it is sizeable
    enough to deserve its own bounded session.
- Inputs required: this handoff; ADR-0005; `10-risk-register.md` (R-003,
  R-008, plus carried-forward R-002/R-005/R-006/R-007);
  `11-backlog.md` (B-002, B-009 through B-012).
- Definition of done: whichever item is picked up, verified with real
  evidence per this project's own established standard.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main` (this session developed on
`claude/pulsewatch-infra-k8s-a5ry2f` per its own worker-branch
instructions), unreleased (pre-v0.1.0). Everything Session 13's own
handoff listed as complete remains complete and untouched; this session
added a second, production-shaped deployment target (Kubernetes, via
Terraform + Kustomize) alongside the existing Docker Compose local/dev
path, plus the ADR/decision-log/risk-register/backlog documentation that
change requires.

**Problem being solved:** unchanged — see `00-project-brief.md`. This
session did not add an application feature; it added a deployment-layer
capability this repo's own claimed deep phases (Release & Deployment,
Operations & Maintenance) needed and previously lacked a real way to
demonstrate.

**Users:** unchanged — single operator plus the machine "Agent" role (`02-requirements.md`).

**Current stack:** everything from Session 13 unchanged, plus:
- Deployment: Terraform (`infra/terraform/`) and Kubernetes
  (`deploy/k8s/`, Kustomize-based) as a second deployment target. Docker
  Compose unchanged, still the local/dev default.
- CI: two new jobs in `.github/workflows/ci.yml` (offline IaC/K8s
  validation; GHCR image publishing on `main`).
- No application code (Go, SvelteKit, SQL schema) was touched this
  session.

**Architecture decisions that must not be reversed:** ADR-0001–0004
unchanged, per Session 13's own list. New: **ADR-0005** — Terraform for
platform state, Kustomize-based plain manifests (not Helm) for the
application workload, applied separately, BYO-cluster only. Must not be
silently collapsed back into "Terraform manages everything" or "just use
`kubectl apply -f` by hand" without a fresh options-considered pass.

**Implementation state:**
- Done: ADR-0005; `01-scope-and-non-goals.md`/`09-decision-log.md`/
  `10-risk-register.md`/`11-backlog.md`/`13-release-notes.md` updates;
  the full Terraform platform-layer module; the full Kubernetes
  workload-layer manifest set + rollout script; the new
  `08-deployment-and-operations.md` Kubernetes section (rollout,
  migration policy, agent-compat contract, TLS/R-003 narrowing); scoped
  additions to `06-security-threat-model.md`/`07-testing-strategy.md`;
  two new CI jobs.
- In progress: nothing mid-flight.
- Not started: real-cluster verification (B-009) — this is the load-
  bearing gap, not a nice-to-have; a cloud-VM bootstrap module (B-010);
  Kubernetes-target Postgres backup/restore (B-011); filling
  `06-security-threat-model.md`/`07-testing-strategy.md` for the whole
  system (B-012); every item already open at Session 13's close
  (B-002, B-004 through B-008, R-002, R-005 through R-007).

**Constraints and non-goals:** `01-scope-and-non-goals.md`, as amended by
this session for the deployment layer only — the application-layer
technology freeze (no TimescaleDB/Prometheus/Grafana/message queue) is
unchanged and must not be reopened by this ADR.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** this session's real
rigor was almost entirely Release & Deployment / Operations & Maintenance
depth — a genuine options-considered ADR, a real (if offline-only)
Terraform module and Kubernetes manifest set, and a design-level rollout
contract (rolling-update safety, migration expand/contract policy,
agent-compatibility contract) reasoned through from this project's own
existing ADRs rather than invented fresh. The one place this session
could not reach real rigor — a live cluster to apply against — is named
exhaustively rather than glossed over, consistent with Session 13's own
practice of correcting overclaims rather than repeating them.

**Task for this session (project-owner-confirmed scope expansion) — now
complete for what this sandbox could verify:** document and build a real
second Kubernetes/Terraform deployment target for pulsewatch, as a
deliberate, reasoned amendment to the Session 0 technology freeze. **Done
— see Work completed above, with the real-cluster-verification gap
stated as clearly as every other honestly-scoped absence in this
project's history.**

**Definition of done — met, with one explicit carve-out:**
- ADR-0005 written with real options-considered rigor, matching
  ADR-0001–0004's own bar.
- `01-scope-and-non-goals.md` amended (not silently violated) with a
  clear pointer to the new ADR.
- Real Terraform module and Kubernetes manifests exist, are internally
  consistent with `docker-compose.yml`'s own real service topology and
  R-004's router split, and are validated with every offline tool this
  sandbox actually has (documented exactly which tools those are and
  which ones it doesn't).
- `08-deployment-and-operations.md`'s new section gives a real,
  reasoned answer to "safe rollout while continuing to monitor,"
  "migrations," and "agent/server version compatibility during a rolling
  deploy" — the exact three things the task's own framing named.
- **Carve-out, stated as a gate rather than a caveat:** real-cluster
  verification (B-009) is NOT done, and this file does not claim it is.
  A future session with real registry/cluster access must complete it
  before this deployment target can be called this project's *proven*
  production path rather than its documented one.
- Local HEAD confirmed to match `origin/<worker-branch>` after commit/
  push — see this session's own closing message for the real `git log`/
  `git status` proof.

**Files to attach or paste for Session 15:**
- `docs/adr/ADR-0005-iac-terraform-and-kubernetes-deployment.md`
- `10-risk-register.md` (R-003, R-008) and `11-backlog.md` (B-009
  through B-012)
- `infra/terraform/README.md`, `deploy/k8s/README.md`

**Ground rules:** Do not change the application stack (Go/Gin/SvelteKit/
Postgres/Redis/OTel) or reopen the application-layer technology freeze.
Do not reopen ADR-0001–0005 without new measured/real evidence per each
one's own Revisit triggers. Do not touch `privacy-forge`,
`laravel-consent-guard`, `bookslot`, or `lexicon`. Do not claim
real-cluster verification (B-009) is done without actually running
`terraform apply`/`deploy/k8s/rollout.sh` against a live cluster and
recording real output, the same discipline Session 13 already applied to
its own auth-capture correction.
