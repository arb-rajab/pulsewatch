# ADR-0005 — Infrastructure-as-Code (Terraform) and Kubernetes as a Second, Production Deployment Target

- **Date:** 2026-09-06
- **Status:** accepted

## Context

`00a-ledger-confirmation.md` froze this repository's learning budget at
exactly two new technologies (Go concurrency patterns, OpenTelemetry
collector pipelines, Rule D3) and named Release & Deployment plus
Operations & Maintenance as its two deep SDLC phases. `01-scope-and-non-goals.md`
correspondingly lists "a third new technology" as an explicit non-goal.
Sessions 1–13 respected that freeze: everything shipped so far
(scheduler/leasing, alert-suppression, the agent, operator auth, the
dashboard, TLS via Caddy, the SLO/rollup job) runs on the original stack,
deployed exclusively via `docker compose up` on a single host
(`08-deployment-and-operations.md`).

That deployment story has a real, named gap against this repo's own
deep-phase claim. `00a-ledger-confirmation.md`'s own justification for
choosing Release & Deployment as a deep phase says the system must
"handle agent/server version skew during a rolling upgrade, and run
migrations against a live monitoring database without losing in-flight
check history." `docker compose up -d --build` cannot demonstrate a
*rolling* upgrade at all — compose replaces a service's single container
outright (a brief full-stop, not an overlap), so the "old and new process
briefly coexist" scenario ADR-0001's own leasing design was explicitly
built to survive (see that ADR's "Restart-boundary race" and its revisit
trigger: "If a future session ever adds a second server instance for
real... this mechanism already generalizes without modification") has
never actually been exercised end-to-end. Single-host compose also has no
real answer for "the host itself dies" (no scheduling failover, no
declarative infrastructure to rebuild from), which sits awkwardly next to
Operations & Maintenance's own stated subject matter ("what happens when
the thing that watches everything else goes down," `00-project-brief.md`).

**This ADR is a deliberate, reasoned amendment to the Session 0 ledger, not
a silent violation of it.** Rule D3's freeze was correct for Sessions 1–13:
until the application itself worked (scheduler, alerting, agent, auth,
SLO), a third and fourth technology would have been premature machinery
solving a deployment problem the project hadn't yet earned the right to
have. That is no longer true — the v1 feature surface named in
`01-scope-and-non-goals.md`'s MVP boundary is functionally complete
(scheduler, checks, SLO/rollup, alerting, agent, dashboard, auth), and the
two deep phases this repo committed to cannot be demonstrated with real
rigor on `docker compose` alone. The project owner has confirmed this
expansion explicitly (see `09-decision-log.md`'s Session 14 entry) rather
than this being an implementation-driven scope creep.

## Options considered

### A — Stay on Docker Compose only; write the rolling-upgrade/failover story as prose without a real second deployment target

Cheapest option; zero new technology. **Rejected.** This project's own
practice (see every prior session's handoff — Session 13's entire
objective was replacing a *described* auth capture with a *real* one) is
that an unverified claim is worse than an honestly-scoped absence. A
"rolling upgrade" story that only exists as a paragraph, never exercised
against a real orchestrator that can actually run two replicas
simultaneously and drain one, would be exactly the kind of claim this
project's own culture exists to avoid making.

### B — Add Kubernetes only, applied by hand (`kubectl apply -f`), no IaC tool

Gets a real orchestrator without a second new technology. **Rejected as
insufficient, not wrong.** Hand-applied manifests give the rolling-update
mechanics (the actual thing this ADR needs to prove) without needing
Terraform at all — and this ADR's Kubernetes-manifest layer is in fact
applied this way (`kubectl`/Kustomize, see Decision below). But it leaves
the cluster-level platform state (namespace, secrets, ingress controller,
TLS issuer) as manual, undocumented, non-reproducible operator actions —
exactly the kind of implicit infrastructure this developer's other
flagships already accepted as a real cost only when there was no
production deployment at stake (`privacy-forge`'s Session 24 descoping,
`lexicon`'s local-only proof). pulsewatch's whole premise is being the
project that *does* take Release & Deployment seriously; declaring the
platform layer out of scope here would undercut that premise for a small
savings (one more Terraform-shaped learning objective).

### C — Add Terraform + Kubernetes, with Terraform authoring every workload object directly (`kubernetes_deployment`, `kubernetes_service`, etc. for the whole application)

**Rejected.** This uses Terraform for two structurally different jobs at
once: infrequently-changing platform state (namespace, secrets, ingress
controller, cert issuer) and frequently-changing, per-release application
state (a new backend image tag on every deploy). Terraform's plan/apply
model is a poor fit for the second kind — a routine image-tag bump would
require a full `terraform plan`/`apply` cycle against provider-managed
state for what is, in Kubernetes terms, a one-line `kubectl set image` /
manifest edit — and conflates "did the platform change" with "did the
application version change" in one state file, one drift-detection
surface, and one apply blast radius.

### D — Terraform for platform/cluster-level infrastructure; Kustomize-based plain Kubernetes manifests for the application workload, applied separately (chosen)

Terraform owns what is genuinely infrastructure: the `pulsewatch`
namespace, Kubernetes `Secret` objects sourced from Terraform variables
(never committed — `infra/terraform/variables.tf`), and the shared
platform services every workload needs (`ingress-nginx` and `cert-manager`,
installed via the `helm_release` resource against the `hashicorp/helm`
provider, plus a `ClusterIssuer` for real Let's Encrypt certificates —
Kubernetes's answer to the self-signed-cert trade-off `08-deployment-and-
operations.md` already named and accepted for `docker compose`'s Caddy
setup). The application workload itself (`backend`, `frontend`,
`otel-collector`, `postgres`, `redis`, the migration `Job`) is plain
Kubernetes YAML under `deploy/k8s/`, composed with Kustomize (`kubectl
kustomize` — built into `kubectl`, not a third-and-fourth technology of
its own) and applied with `kubectl apply -k` / a thin rollout script,
mirroring `docker-compose.yml`'s own service topology service-for-service
so the two deployment targets stay conceptually the same system, not two
divergent designs.

## Decision

**Option D.** This adds exactly two new technologies under a named,
reasoned supplementary budget — **Terraform** (platform/cluster-level IaC)
and **Kubernetes** (a real orchestrator, via plain manifests + Kustomize,
not a third technology of its own) — bringing this repository's total
learning-objective count to four, explicitly exceeding Rule D3's original
cap of two by a deliberate amendment recorded here and in
`09-decision-log.md`, not a silent violation of it.

Concretely:

- `infra/terraform/` — a Terraform module using the `hashicorp/kubernetes`
  and `hashicorp/helm` providers against a Kubernetes cluster the operator
  already controls (consistent with this project's own "agents run on
  infrastructure the operator controls" business assumption,
  `00-project-brief.md` — pulsewatch does not provision or lease cloud
  compute on the operator's behalf, matching the non-goal against becoming
  a hosted product). Creates the `pulsewatch` namespace, the `Secret`
  objects the application needs (from `sensitive` Terraform variables),
  and installs `ingress-nginx` + `cert-manager` plus a `ClusterIssuer`.
- `deploy/k8s/base/` + `deploy/k8s/overlays/production/` — Kustomize
  manifests for every `docker-compose.yml` service, preserving that file's
  own real decisions rather than re-deriving them: `backend`'s two
  independent listeners stay two independent `Service` objects (R-004's
  router split is a code-level fact this deployment layer must not
  paper over by fronting both with one Service); the migration step stays
  a distinct, ordered pre-step (a Kubernetes `Job`, applied and waited on
  before the `backend` Deployment is updated — see `08-deployment-and-
  operations.md`'s new Kubernetes rollout procedure) rather than an init
  container silently re-run on every pod restart.
- `deploy/k8s/rollout.sh` — the ordered apply script (migration Job →
  wait for completion → apply/update Deployments → wait for rollout) that
  makes "migrations complete before new code runs against them" an
  enforced sequence, not an assumption, matching `docker-compose.yml`'s
  own `depends_on: condition: service_completed_successfully` for
  `migrate`.

**What this does not change:** Docker Compose remains the documented
local/dev deployment path (`08-deployment-and-operations.md`'s existing
section, untouched) — Kubernetes is additive, a second, production-shaped
target, not a replacement. No application code, schema, or ADR-0001–0004
decision is reopened by this ADR; this is purely a deployment-layer
addition.

## Trade-offs accepted

- **A real learning-budget overage**, named explicitly rather than hidden:
  four new technologies total against Rule D3's original cap of two. This
  ADR is that reasoning, on the record, not a retroactive excuse.
- **This sandboxed environment cannot exercise `terraform init`/`plan`/
  `apply` or a real `kubectl apply` against a live cluster** — no
  outbound access to the Terraform provider registry, and no Docker daemon
  or Kubernetes cluster available in this container (confirmed directly:
  `docker info` fails to reach a daemon; no `kubectl`/`helm`/`terraform`
  were even installed before this session). Every file this ADR's
  companion work adds was validated the ways that *are* available
  offline — `terraform fmt -check` (HCL syntax/formatting, no provider
  needed), `kubectl kustomize` (a real, full Kustomize build — base +
  overlay merge, no cluster contact required), and direct YAML/HCL
  parsing — but **not** a real `terraform apply` or a real scheduled pod
  reaching `Running`. This is the same honesty standard Session 10 applied
  to its own untested Let's Encrypt real-domain upgrade path: documented
  and structurally sound, not silently claimed as verified end-to-end.
  Tracked as B-009 in `11-backlog.md` — a future session with real cluster
  access must close this gap before Kubernetes can be called this
  project's *proven* production target rather than its documented one.
- **Two deployment targets now need to be kept in sync by hand**
  (`docker-compose.yml` and `deploy/k8s/`) whenever a service's
  environment variables, ports, or image build changes. Accepted because
  the alternative (generating one from the other, or replacing Compose
  with `kompose`-style tooling) is exactly the kind of premature
  abstraction this project's own engineering guidance argues against for
  two targets that will not multiply — there is no plan to add a third.

## Consequences

- `01-scope-and-non-goals.md`'s "a third new technology" row is updated to
  point here rather than silently contradicted — see that file's own
  Session 14 addendum.
- `08-deployment-and-operations.md` gains a new "Kubernetes (production
  deployment target)" section: rollout procedure, the migration-ordering
  guarantee, the agent/server version-compatibility contract during a
  rolling update, and TLS via `cert-manager` (replacing Caddy's
  self-signed default with real Let's Encrypt certificates when a real
  domain is available — the same upgrade Session 10 already documented as
  available for Compose, now real for Kubernetes instead of aspirational).
- `10-risk-register.md` gains a new risk for the untested-apply gap named
  above; `11-backlog.md` gains B-009 (real cluster verification) and
  B-010 (an actual cloud-VM-plus-k3s bootstrap module for an operator with
  no existing cluster — deliberately out of this ADR's own scope, see
  Revisit triggers).

## Revisit triggers

- If a real Kubernetes cluster ever becomes available to a pulsewatch
  session (a real cloud account, a home k3s node, or this sandbox gaining
  registry/daemon access), B-009's real `terraform apply` +
  `deploy/k8s/rollout.sh` verification against live infrastructure
  becomes the next session's actual objective — not optional polish.
- If this developer wants pulsewatch's Terraform to also provision the
  cluster itself (a VM plus k3s, for an operator with no existing
  cluster), that is B-010, a distinct, separately-scoped module — not
  folded into this ADR's own BYO-cluster decision without a fresh
  options-considered pass (a cloud VM introduces real per-provider
  credentials and cost, a materially different trade-off than "manage
  resources in a cluster the operator already pays for").

## Session 15 update — real-cluster verification (B-009)

Not a reopening of this ADR's Decision (Terraform for platform state,
Kustomize for the workload, applied separately, BYO-cluster only — all
unchanged). This is the measured evidence Session 14 could not get and
named as the gap: a real, if minimal and self-built, single-node
Kubernetes cluster, running in this same class of sandbox, exercising the
committed `deploy/k8s/base/` manifests for real. Raw output for every
claim below is saved under `docs/project-memory/evidence/session15-*` per
`00c-evidence-preservation.md` — this section is a pointer to those files,
not a replacement for them.

**What is now proven true, not just documented:**

- A real Kubernetes v1.37.0 control plane (etcd, kube-apiserver,
  kube-controller-manager, kube-scheduler, kube-proxy, kubelet — real
  upstream binaries, not simulated) plus real containerd + runc actually
  schedules and runs pods on this sandbox class, once two
  sandbox-specific blockers are worked around (both were genuinely
  environment quirks, not Kubernetes or this repo's own bugs — see
  `session15-rootcause-*.txt`):
  1. This sandbox's outer sandboxing kills container creation outright
     when a container's cgroup path starts with the literal segments
     `kubepods/besteffort` (or any other kubelet QoS-class name in that
     position) *and* the container joins a new network namespace — almost
     certainly because this VM is itself scheduled as a pod on a real
     Kubernetes host, and that path prefix is reserved for the host's own
     kubelet. Fixed by starting kubelet with `cgroupsPerQOS: false`.
  2. containerd's default pod-sandbox spec sets a very negative
     `oom_score_adj` (-998, to protect the pause container), which this
     sandbox's own resource controls reject with `Permission denied`,
     crashing `runc`'s process bootstrap before it ever reaches
     Kubernetes-visible logs (`can't get final child's PID from pipe:
     EOF` — a famously vague runc error whose real cause here took
     replaying the exact failing OCI spec directly through `runc
     --debug` to find). Fixed via containerd's own documented escape
     hatch for nested/restricted environments: `restrict_oom_score_adj =
     true`.
- `deploy/k8s/base/postgres.yaml`, `redis.yaml`, `backend.yaml`,
  `backend-config.yaml`, `migration-job.yaml`, and
  `networkpolicy.yaml` — the real, committed manifests, rendered with the
  real standalone `kustomize` CLI (installable this session via `go
  install`, closing another Session 14 gap) — applied against the real
  API server above and reached real `Running`/`Complete` status: a real
  Postgres 16 StatefulSet with a bound PVC, a real Redis Deployment, and a
  real backend Deployment whose migration Job ran this repo's actual
  `backend/migrations/*.sql` files to completion — in that order — before
  the backend Deployment was ever applied, the same two-phase ordering
  `deploy/k8s/rollout.sh` enforces (the script itself wasn't run
  byte-for-byte, since it also drives a `frontend` Deployment this session
  didn't build an image for; its Job-then-Deployment sequence was
  reproduced with the equivalent `kubectl` calls against the same
  rendered manifests).
- The real `backend/cmd/agent` binary, talking through the real
  `backend-agent` Service's ClusterIP (not the pod IP directly — real
  kube-proxy iptables DNAT), successfully polled assignments and posted
  logs continuously, including across three live rolling updates of the
  backend Deployment.
- A real rolling update (`maxUnavailable: 0`, `maxSurge: 1`) was measured,
  not assumed: continuous 0.3s-interval health polling against both
  backend Services during three real pod replacements recorded 6 failed
  sub-requests out of 464 (each a single ~0.3-1s blip exactly at the
  moment the old pod's Endpoint was removed, never a sustained gap). This
  is a real, if small, violation of `maxUnavailable: 0`'s literal
  zero-drop intent — root cause: kube-proxy's iptables Endpoint removal is
  asynchronous with the terminating pod actually stopping, a well-known
  Kubernetes characteristic, not a flaw in this repo's design. Applied the
  standard fix — a `lifecycle.preStop: {exec: {command: ["sleep",
  "5"]}}` on the backend container in `deploy/k8s/base/backend.yaml` — which
  reduced the gap; a fully zero-drop guarantee at this measurement
  precision would need a real load balancer/Ingress in front (out of
  reach without cert-manager/ingress-nginx images, see below), so this is
  named as a real, now-mitigated-but-not-eliminated risk rather than
  claimed fixed.

**What remains unverified, and precisely why:**

- **Terraform `init`/`plan`/`apply` against `infra/terraform/`.** The
  Terraform CLI itself is installable this session (unlike Session 14—
  `releases.hashicorp.com` is reachable here), but `registry.terraform.io`
  — the Terraform *provider* registry, a different host — returns an
  explicit `403 Forbidden` organization-policy denial, both sessions. No
  tool substitution closes this: it is a network-policy decision for
  whoever administers the sandbox, not a capability gap. See
  `session15-terraform-cli-installable-registry-still-blocked.txt`.
- **cert-manager / Let's Encrypt real certificate issuance.** Not
  attempted this session: `infra/terraform`'s `helm_release` for
  cert-manager cannot run without the Terraform provider registry above,
  and cert-manager's own container images (from `quay.io`) are blocked by
  the same universal container-registry restriction documented below.
- **NetworkPolicy enforcement.** The policy objects in
  `networkpolicy.yaml` apply cleanly and are real, schema-valid API
  objects — but this sandbox's only installable CNI (the reference
  `bridge`+`host-local` plugins; anything else needs a container image)
  has no NetworkPolicy dataplane, so they are provably *not enforced*
  here: a pod the policy does not allow can still reach the port it
  guards. Confirmed directly, not assumed — see
  `session15-networkpolicy-not-enforced.txt`. A production CNI (Calico,
  Cilium, or a cloud provider's own) is what actually enforces these
  already-correct policies; that requires container images this sandbox
  cannot pull (next point).
- **Container-image registries are universally blocked at the blob layer
  in this sandbox** — Docker Hub, GHCR, `registry.k8s.io`, `quay.io`, and
  AWS's public ECR were all tested directly this session: every registry
  API call (auth, manifest) succeeds, but the actual layer download
  redirects to a CDN (`production.cloudfront.docker.com`,
  `pkg-containers.githubusercontent.com`, etc.) that returns `403
  Forbidden` regardless of registry. This is why every image this session
  used (backend/agent binaries, Postgres, Redis, CoreDNS, the `migrate`
  CLI, a minimal pause container) was built from source or from real
  Ubuntu packages (`debootstrap` + `apt`, both of which *are* reachable)
  rather than pulled — a real, working substitute for verifying this
  repo's own manifests, but it means the frontend (needs `node:22-slim`
  as a build stage — buildable the same way, just not attempted this
  session for time) and `otel-collector` (needs the `otelcol-contrib`
  distribution specifically, not just any OpenTelemetry Collector build)
  were not part of this session's real-cluster proof.

## Revisit triggers (added, Session 15)

- If a future session's sandbox allows outbound access to
  `registry.terraform.io` specifically (not just general internet
  access), B-009's Terraform half becomes closeable the same way this
  session closed the Kubernetes half.
- If a future session's sandbox allows outbound access to any container
  registry's blob-storage CDN (not just the registry API), re-attempt
  with real upstream images (`postgres:16-alpine`, `redis:7-alpine`,
  `ghcr.io/OWNER/pulsewatch-backend`, etc.) instead of this session's
  from-source substitutes, and extend to frontend + otel-collector +
  cert-manager + a policy-capable CNI — that would close every remaining
  gap this section lists in one pass.
