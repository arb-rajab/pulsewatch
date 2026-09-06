# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed

- Session number and title: **Session 15 — Real-Cluster Verification of
  ADR-0005 (B-009).**
- Objective: Session 14 built a real Terraform module and Kubernetes
  manifest set but could only validate them offline (`terraform fmt`,
  `kubectl kustomize`) — no live cluster or registry access existed in
  that sandbox. B-009 asked whether the Kubernetes deployment target
  actually works: stand up a real cluster, apply the real manifests, and
  prove — or honestly disprove — the rolling-update safety, migration
  ordering, and agent/server compatibility claims ADR-0005 makes.
- Status: **the Kubernetes workload half is now real-cluster-verified.**
  A real Kubernetes v1.37.0 control plane and containerd/runc runtime was
  built from scratch in this session's sandbox (no `kind`/`minikube`
  available, no container-registry access — see below), and the real,
  committed `deploy/k8s/base/` manifests were applied against it,
  reaching real `Running`/`Complete` status. The Terraform/platform half,
  cert-manager/Let's Encrypt issuance, and NetworkPolicy enforcement on a
  real CNI remain unverified — named precisely below, not glossed over.

## What this session's sandbox actually had (materially different from Session 14's)

Worth stating up front because it explains why this session could do what
Session 14 couldn't, and where the new limits are:

- A working Docker daemon was installable (`dockerd` binary present but
  not running; started it directly) — Session 14's sandbox had neither
  the daemon nor a way to start one.
- `kubectl`, the real Kubernetes **server** binaries (`kube-apiserver`,
  `kube-controller-manager`, `kube-scheduler`, `kubelet`, `kube-proxy`),
  and `etcd` were all installable from real upstream sources
  (`dl.k8s.io`, Ubuntu's own `apt` archive) — none of these were
  installable in Session 14's sandbox.
- The standalone `kustomize` CLI and `coredns` were buildable via `go
  install` against `proxy.golang.org` (allowed) — closing Session 14's
  specific "couldn't verify `kustomize edit set image`" gap.
- **But: no container registry's blob-storage CDN is reachable.** Docker
  Hub, GHCR, `registry.k8s.io`, `quay.io`, and public ECR were all tested
  directly — every registry's own API (auth, manifest lookup) succeeds,
  but the actual image-layer download redirects to a CDN
  (`production.cloudfront.docker.com`, `pkg-containers.githubusercontent.com`,
  etc.) that returns `403 Forbidden` regardless of which registry. This
  means `kind`/`k3d`/`minikube` (all of which pull a multi-hundred-MB node
  image) are not viable here, and neither is any workload using an
  upstream image directly. Worked around by building every image this
  session needed from source or from real Ubuntu packages instead — see
  "Work completed" below.
- **`registry.terraform.io` is still explicitly blocked** (organization
  policy, `403` on the CONNECT itself) — the one part of Session 14's gap
  this sandbox does not close, confirmed precisely rather than assumed
  (see `docs/project-memory/evidence/session15-terraform-cli-installable-registry-still-blocked.txt`).

## Work completed

### A real, from-scratch single-node Kubernetes v1.37.0 cluster
Real upstream binaries (not simulated): `etcd` 3.4.30, `kube-apiserver`/
`kube-controller-manager`/`kube-scheduler`/`kubelet`/`kube-proxy` v1.37.0,
`containerd` v2.2.2, `runc` 1.3.4 — all running as bare processes on the
session VM (self-signed PKI generated with `openssl`; no `kubeadm`, since
`kubeadm`'s own control-plane images can't be pulled either). Two real,
sandbox-specific blockers had to be root-caused and fixed before a single
pod would reach `Running` — both are genuine environment quirks, not bugs
in Kubernetes or this repo:

1. **This sandbox kills container creation when a cgroup path looks like
   a real kubelet pod's** (`kubepods/besteffort/...`, specifically that
   exact prefix) **combined with a new network namespace.** Isolated via
   systematic `ctr run --cgroup <path> --cni` A/B testing directly against
   `containerd`/`runc` (bypassing kubelet to get clean signal) — almost
   certainly because this sandbox VM is itself scheduled as a pod on a
   real Kubernetes host, and that cgroup prefix is reserved for the host's
   own kubelet. Fixed: `kubelet`'s `cgroupsPerQOS: false` +
   `enforceNodeAllocatable: []`, which stops kubelet from constructing that
   hierarchy at all. Evidence:
   `docs/project-memory/evidence/session15-rootcause-cgroup-path-block.txt`.
2. **containerd's default pod-sandbox `oom_score_adj` (-998) is rejected
   by this sandbox's own resource controls**, crashing `runc`'s process
   bootstrap before any Kubernetes-visible log line — surfaced only as
   the famously unhelpful `can't get final child's PID from pipe: EOF`.
   Root-caused by wrapping the `runc` binary to capture the exact OCI
   bundle `containerd` generated, then replaying it directly through
   `runc --debug`, which showed the real underlying error:
   `failed to update /proc/self/oom_score_adj: Permission denied`. Fixed:
   containerd's own documented escape hatch for nested/restricted
   environments, `restrict_oom_score_adj = true`. Evidence:
   `docs/project-memory/evidence/session15-rootcause-oom-score-adj-nsexec-failure.log`.

Also needed and built: a standalone `containerd` instance with the CRI
plugin enabled (Docker's own embedded `containerd` doesn't expose it), a
minimal from-scratch "pause" sandbox image (a five-line Go binary, since
`registry.k8s.io/pause` can't be pulled — see above), the reference
`bridge`+`host-local` CNI plugins (Ubuntu's `containernetworking-plugins`
package), and CoreDNS (built via `go install`, RBAC'd against the real
API server) for real in-cluster DNS — none of this existed as a
docker-compose-style shortcut; it's the actual minimum a from-scratch
cluster needs. First real pod reaching `Running`:
`docs/project-memory/evidence/session15-first-real-pod-running.txt`.

A separate, real bug was found and fixed along the way: once `kube-proxy`
started managing `iptables`, its own `FORWARD`-chain rules pushed ahead of
Docker's, and neither chain had an `ACCEPT` rule for the manually-created
`cni0` bridge — silently breaking all pod-to-pod traffic (FORWARD chain
default-policy `DROP`). Fixed with an explicit `iptables -I FORWARD -i
cni0 ... -j ACCEPT` rule. (A second, unrelated flakiness source was also
found and fixed: several control-plane processes were accidentally
launched without this session's custom `NO_PROXY` addition for the
cluster's own IP, so their traffic to the API server silently routed
through this environment's outbound agent proxy instead of directly —
explaining several confusing "stuck syncing" episodes until diagnosed via
`/proc/<pid>/environ`.)

### Real application manifests, applied for real
`deploy/k8s/base/postgres.yaml`, `redis.yaml`, `backend.yaml`,
`backend-config.yaml`, `migration-job.yaml`, and `networkpolicy.yaml` —
the actual, committed manifests, rendered with a real standalone
`kustomize` build (image names substituted to locally-built images via a
scratch overlay, not committed — see "Files not committed" below) — were
`kubectl apply`'d against the cluster above and reached real status:

- **Postgres 16** (StatefulSet, a real bound `PersistentVolume` since no
  dynamic provisioner exists here — a static `hostPath` PV was created by
  hand) and **Redis 7**, both built from real Ubuntu packages
  (`debootstrap` + `apt install postgresql redis-server`, since
  `postgres:16-alpine`/`redis:7-alpine` can't be pulled) — real
  `Running`.
- **The migration Job**, using a real `golang-migrate` v4.19.1 CLI (built
  via `go install`, since `migrate/migrate` can't be pulled), ran all
  nine of this repo's actual `backend/migrations/*.sql` files to
  completion against the real Postgres above, in the same
  Job-before-Deployment order `deploy/k8s/rollout.sh` enforces. Real
  output: `docs/project-memory/evidence/session15-migration-job-real-output.txt`.
- **The backend Deployment**, running this repo's actual compiled
  `backend`/`backend/cmd/agent` binaries (built locally with `go build`,
  packaged into `FROM scratch` images — no base-image pull needed for
  Go's static binaries), reached real `Running`, connected to the real
  Postgres/Redis above via real in-cluster DNS (CoreDNS), and served real
  traffic.
- **`backend/cmd/agent`** (this repo's real reference agent, not a
  stand-in), provisioned via a real `backend/cmd/seed-agent` run against
  the real Postgres, polled `GET /api/v1/agent/assignments` and posted
  `POST /v1/logs` continuously through the real `backend-agent` Service's
  ClusterIP (real `kube-proxy` iptables DNAT, not the pod IP directly).
  Evidence: `docs/project-memory/evidence/session15-real-agent-through-clusterip.log`.
- **Three real rolling updates** of the backend Deployment
  (`maxUnavailable: 0`, `maxSurge: 1`) were exercised while a background
  process polled both backend Services' `/health` every 0.3s. Result: 6
  failed sub-requests out of 464 polls, each a single ~0.3-1s blip
  exactly at the moment the old pod's Endpoint was removed — a real, if
  small, gap in the literal zero-drop guarantee, not a sustained outage.
  Root cause: `kube-proxy`'s iptables Endpoint removal is asynchronous
  with the terminating pod actually stopping — a known Kubernetes
  characteristic, not a defect in this repo's design. Fixed with the
  standard mitigation. Evidence:
  `docs/project-memory/evidence/session15-rolling-update-health-poll.log`.

### `deploy/k8s/base/backend.yaml` (edited)
Added `lifecycle.preStop: {exec: {command: ["sleep", "5"]}}` on the
backend container — the fix for the measured rolling-update gap above.
`terminationGracePeriodSeconds` (40s) already comfortably covers this new
5s delay plus `SCHEDULER_HARD_SHUTDOWN_DEADLINE`'s 30s, so no other value
needed to change.

### `docs/adr/ADR-0005-iac-terraform-and-kubernetes-deployment.md` (edited)
New "Session 15 update — real-cluster verification (B-009)" section
appended (not a reopening of the ADR's Decision): what is now proven true
with evidence-file pointers, what remains unverified and precisely why
(Terraform provider-registry policy block, cert-manager/Let's Encrypt,
NetworkPolicy enforcement, frontend/otel-collector images), and updated
Revisit triggers naming exactly what a future session with different
egress access would need to do to close each remaining item.

### `docs/project-memory/09-decision-log.md`, `10-risk-register.md`, `11-backlog.md`, `13-release-notes.md` (edited)
- Decision log: new Session 15 entry summarizing the above as an
  amendment, not a reversal.
- Risk register: R-008 narrowed from "everything about the K8s target is
  unverified" to precisely the three items still open (Terraform,
  cert-manager, NetworkPolicy enforcement) plus the two images not built
  (frontend, otel-collector) — the workload layer itself is now Low risk,
  real-cluster-verified.
- Backlog: B-009 marked substantially closed with a pointer to what's
  real now; new **B-013** carries the narrowed remainder (Terraform
  apply, cert-manager, NetworkPolicy-on-a-real-CNI, frontend/otel-collector
  images) as its own bounded scope for whichever future session gets a
  sandbox with the right egress access.
- Release notes: Unreleased/Added entry updated with the real-verification
  result and the `preStop` fix.

### `docs/project-memory/08-deployment-and-operations.md` (edited)
Migration expand/contract section updated: the Job-then-Deployment
*ordering* is now confirmed real (not just designed), though no migration
in this repo has yet needed the actual expand/contract *split* (all nine
existing migrations only add tables). "What's not yet real" section
rewritten to list exactly the four items in R-008/B-013 rather than the
previous blanket "nothing has been applied to a live cluster."

### `docs/project-memory/evidence/session15-*` (new)
Nine files, following this project's own established evidence-preservation
practice (`00c-evidence-preservation.md`) — raw command output captured
live, not retyped from memory, for every claim in this handoff: cluster
version/process proof, both root-cause investigations, the first real pod,
the real migration Job output, the real agent-through-ClusterIP traffic
and backend access log, the rolling-update health-poll timeline, the
NetworkPolicy non-enforcement test, and the precise Terraform/registry
re-confirmation.

## Real gaps found and named during this session

1. **Two sandbox-specific container-runtime blockers** (cgroup-path
   detection, `oom_score_adj` rejection) that would have made *any*
   from-scratch Kubernetes cluster in this sandbox class impossible
   without root-causing them specifically — not a Kubernetes or repo bug,
   but worth naming precisely for whichever future session hits the same
   wall in a similarly-sandboxed environment.
2. **Container-registry blob downloads are blocked universally** in this
   sandbox (not just Docker Hub — GHCR, `registry.k8s.io`, `quay.io`,
   public ECR all fail identically at the CDN layer). This is a more
   precise, more useful finding than Session 14's "no Docker daemon" —
   it means even a sandbox that *does* get a working Docker daemon still
   can't use `kind`/`k3d`/`minikube` or any upstream image directly under
   this specific egress policy.
3. **A real, measured, small gap in `maxUnavailable: 0`'s zero-drop
   guarantee** during rolling updates — found by actually measuring, not
   assumed either way. Mitigated with the standard `preStop` fix; not
   claimed as fully eliminated, since a complete guarantee at this
   measurement precision would need a real load balancer/Ingress this
   sandbox can't build (needs `ingress-nginx`'s image).
4. **NetworkPolicy enforcement was assumed but never actually tested**
   before this session — Session 14 authored real, correct policy objects
   but had no way to confirm whether "correct" meant "enforced." Session
   15 tested it directly and found the honest answer: the objects are
   real and correct, enforcement depends entirely on the CNI plugin the
   operator installs, and this sandbox's own minimal CNI does not enforce
   them. This is exactly the kind of "documented vs. proven" gap this
   project's own standard exists to catch.

## Open questions and risks

- **R-008 (narrowed this session):** Terraform `apply`, cert-manager/Let's
  Encrypt issuance, and NetworkPolicy enforcement on a real CNI remain
  open — precisely scoped, not vague. See `10-risk-register.md`.
- **R-002, R-005, R-006, R-007 (carried forward, untouched this
  session):** unchanged — see `10-risk-register.md`. This session's scope
  was Kubernetes real-cluster verification only; no scheduler/alerting/
  rollup application code was touched (the `preStop` manifest addition is
  the only application-adjacent change).
- **B-013 (opened this session):** the narrowed remainder of B-009 — see
  `11-backlog.md`.
- **B-002, B-004 through B-008, B-010, B-011, B-012 (carried forward from
  Session 14, untouched):** see `11-backlog.md`.

## Next recommended session

- Proposed session title: **`GET /targets/{id}/incidents` (B-002)** — the
  best-scoped pure-feature item, per Session 13/14's own reasoning, now
  that B-009's real-cluster verification (the load-bearing infra gap) has
  been substantially closed. B-013 (Terraform/cert-manager/NetworkPolicy/
  frontend-image) remains open but is blocked on sandbox egress policy a
  session cannot change from inside itself — pick it up only when a
  future session's environment demonstrably allows `registry.terraform.io`
  and/or container-registry blob downloads (test directly at session
  start, the way this session and Session 14 both did, rather than
  assuming either way).
  - Do **not** reopen ADR-0001–0005 without new measured/real evidence
    per each one's own Revisit triggers.
  - Do **not** attempt to backfill B-012 (the empty security/testing
    docs) as a side effect of an unrelated session — it is sizeable
    enough to deserve its own bounded session.
- Inputs required: this handoff; ADR-0005 (including its Session 15
  section); `10-risk-register.md` (R-008, plus carried-forward
  R-002/R-005/R-006/R-007); `11-backlog.md` (B-002, B-013, B-010 through
  B-012); `docs/project-memory/evidence/session15-*` if literal proof is
  needed rather than this narrative.
- Definition of done: whichever item is picked up, verified with real
  evidence per this project's own established standard — files under
  `docs/project-memory/evidence/`, not just prose.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main` (this session developed on
`claude/b-009-cluster-verification-olzirx` per its own worker-branch
instructions), unreleased (pre-v0.1.0). Everything Session 14's own
handoff listed as complete remains complete; this session did not add an
application feature or change the deployment target's design — it proved
(and, where measurement found a real small gap, fixed) that Session 14's
Kubernetes deployment target actually works against a real cluster.

**Problem being solved:** unchanged — see `00-project-brief.md`.

**Users:** unchanged — single operator plus the machine "Agent" role (`02-requirements.md`).

**Current stack:** unchanged from Session 14 (Terraform + Kubernetes as a
second deployment target, Docker Compose still the local/dev default). No
new technology was added — this session verified existing design, it
didn't expand scope.

**Architecture decisions that must not be reversed:** ADR-0001–0005
unchanged in their Decisions. ADR-0005 gained a Session 15 evidence
section (additive) — must not be read as reopening BYO-cluster/
Terraform-platform-Kustomize-workload split/no-Helm-for-workload without a
fresh options-considered pass and new Revisit-trigger-qualifying evidence.

**Implementation state:**
- Done: real-cluster verification of the Kubernetes workload layer
  (postgres/redis/backend/migration-Job/NetworkPolicy-objects-exist);
  real rolling-update measurement and the resulting `preStop` fix;
  precise re-confirmation of the Terraform provider-registry block; all
  documentation/evidence updates listed above.
- In progress: nothing mid-flight.
- Not started (B-013, this session's own narrowed follow-up): `terraform
  apply`; cert-manager/Let's Encrypt real issuance; NetworkPolicy
  enforcement against a real policy-capable CNI; frontend + otel-collector
  images. Also still open, unrelated to this session: B-002, B-004
  through B-008, B-010, B-011, B-012, R-002, R-005 through R-007.

**Constraints and non-goals:** unchanged — see `01-scope-and-non-goals.md`.
This session did not touch the application-layer technology freeze.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** this session's rigor was
almost entirely in *proving* Release & Deployment / Operations &
Maintenance claims Session 14 could only design — two genuine
sandbox-environment root-causes solved by direct low-level debugging
(`runc --debug` replay, systematic A/B isolation) rather than guesswork,
a real measured finding (the rolling-update blip) acted on rather than
either ignored or assumed away, and a real negative result (NetworkPolicy
non-enforcement) reported honestly rather than left ambiguous. The
remaining gaps (Terraform, cert-manager, frontend/otel images) are named
as precisely as what was achieved, per this project's own standing
practice of correcting overclaims rather than accumulating them.

**Task for this session (B-009) — now substantially complete, remainder
tracked as B-013:** prove, with real evidence, that ADR-0005's Kubernetes
deployment target actually works. **Done for the workload layer — see
Work completed above.** Terraform/platform layer, cert-manager, and
NetworkPolicy enforcement remain open, blocked on this sandbox's egress
policy rather than on anything a future session could fix by working
harder within the same sandbox class.

**Definition of done — met for the workload layer, with three
precisely-named carve-outs:**
- A real Kubernetes cluster ran the real, committed manifests to real
  `Running`/`Complete` status. ✅
- The migration-then-deploy ordering was exercised for real, not just
  scripted. ✅
- The agent/server compatibility contract was exercised for real across
  real rolling updates, with continuous measurement rather than a
  point-in-time check. ✅ (and the measurement surfaced a real small gap,
  which was fixed)
- **Carve-out 1:** `terraform apply` — blocked by explicit
  `registry.terraform.io` policy, re-confirmed precisely this session.
- **Carve-out 2:** cert-manager/Let's Encrypt real issuance — depends on
  carve-out 1, not attempted.
- **Carve-out 3:** NetworkPolicy enforcement on a real CNI — tested
  directly and found NOT enforced by this sandbox's own minimal CNI (an
  honest negative result, not an untested assumption); a policy-capable
  CNI would enforce these same already-correct objects.
- Local HEAD confirmed to match `origin/<worker-branch>` after commit/
  push — see this session's own closing message for the real `git log`/
  `git status` proof.

**Files to attach or paste for the next session:**
- `docs/adr/ADR-0005-iac-terraform-and-kubernetes-deployment.md` (especially its Session 15 section)
- `10-risk-register.md` (R-008) and `11-backlog.md` (B-002, B-013)
- `docs/project-memory/evidence/session15-*` (literal proof, if requested)

**Ground rules:** Do not change the application stack (Go/Gin/SvelteKit/
Postgres/Redis/OTel) or reopen the application-layer technology freeze.
Do not reopen ADR-0001–0005 without new measured/real evidence per each
one's own Revisit triggers. Do not touch `privacy-forge`,
`laravel-consent-guard`, `bookslot`, or `lexicon`. Do not claim B-013's
remaining scope (Terraform apply, cert-manager, NetworkPolicy enforcement)
is done without actually running it against real infrastructure with a
sandbox whose egress policy allows it, and without saving the real output
under `docs/project-memory/evidence/` per this project's own standard.
