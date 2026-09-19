# Security and Threat Model
> Purpose: what can go wrong, and what stops it
> Project: pulsewatch (public)
> Last updated: 2026-09-18

## Assets and data classification

| Asset | Where | Classification | Notes |
|---|---|---|---|
| Operator password | `operators.password_hash` | Critical (credential) | bcrypt, `bcrypt.DefaultCost` (`backend/internal/operatorauth/password.go`) — never the plaintext, never logged. |
| Operator session token | `pulsewatch_session` cookie, not persisted server-side | Critical (credential) | HMAC-SHA256 over `operatorID:expiry`, stateless — see "Authentication and authorisation design". |
| `SESSION_SIGNING_SECRET`, `ALERT_CHANNEL_ENCRYPTION_KEY`, `postgres_password` | env vars / K8s `Secret` / `.env` | Critical (infra secret) | See "Secrets management" below — already covered from Session 14. |
| Agent bearer token | issued once at `POST /api/v1/agents`, never re-displayed | Critical (credential) | Stored only as a SHA-256 hash (`agents.credential_hash`) — the plaintext exists only in the one create/rotate response and on the agent host's own config. |
| Alert channel destination (webhook URL, email address, push credential) | `alert_channels.destination_encrypted` | Sensitive | AES-256-GCM at rest (`backend/internal/alerting/channel.go`) — see "Encryption at rest". A webhook URL can itself embed a bearer token or signing secret as a query parameter; this system does not know or care, it just delivers to whatever URL the operator configured. |
| Device push token (FCM/APNs) | `device_tokens.token_encrypted` | Sensitive | **Closed this session (B-012).** AES-256-GCM at rest (`alerting.EncryptDestination`, the identical helper `alert_channels.destination_encrypted` uses), keyed by the same `ALERT_CHANNEL_ENCRYPTION_KEY`. A deterministic HMAC-SHA256 blind index (`device_tokens.token_hash`, `alerting.HashDeviceToken`) carries the exact-match lookup `RegisterDeviceToken`'s upsert needs, since AES-GCM's randomised nonce can't. See "Accepted risks" for why this was previously deferred and what changed. |
| Target definitions (`targets.url_or_host`) | `targets` table | Low-moderate | Internal hostnames/URLs of the operator's own infrastructure — not secret in the sense of a credential, but reveals internal network topology if leaked; readable only by the one authenticated operator. |
| Check results / incidents / rollups | `check_results`, `incidents`, `check_rollups_hourly` | Low | Operational monitoring data about the operator's own services — no end-user PII, since pulsewatch monitors infrastructure, not user-facing applications (`01-scope-and-non-goals.md`). |
| OTel telemetry from the agent | in transit, `POST /v1/logs` | Low-moderate | Structured check results plus whatever the agent host's own OTel SDK attaches; travels in plaintext under Docker Compose (R-003, `10-risk-register.md`). |

The single-operator, self-hosted deployment model (`01-scope-and-non-goals.md`: "Multi-user auth / role separation on the dashboard" is explicitly deferred) means there is exactly one trusted principal on the operator side — the threat model below is written for "an external attacker vs. the one operator account and the one agent-facing surface," not for insider/multi-tenant isolation, which this system does not attempt.

## Trust boundaries

- **Browser ↔ frontend (SvelteKit).** TLS-terminated at the edge for both deployment targets: Caddy (`tls internal`) for Docker Compose, Kubernetes `Ingress` + `cert-manager`/Let's Encrypt for the Kubernetes target (`deploy/k8s/base/ingress.yaml`) — never inside the Go/Node process itself (no code sets `Strict-Transport-Security` or otherwise implements TLS).
- **Frontend server ↔ operator-facing backend (`operatorapi`).** Server-to-server only: SvelteKit's `load`/form-action code re-attaches the `pulsewatch_session` cookie itself via `backendFetch()` (`frontend/src/lib/server/backend.ts`) — the browser never calls the backend directly, and the backend sets no CORS policy (no `Access-Control-Allow-Origin`), so a cross-origin browser `fetch` would be blocked from reading a response even if it could attach the cookie.
- **Remote agent host ↔ agent-facing backend (`agentapi`, port 8020, ADR-0003).** The highest-value boundary crossing an untrusted network: a real agent process running on infrastructure the operator controls, but reporting in over whatever network sits between it and `backend`. Authenticated by bearer token only (no mTLS). Still plaintext HTTP under Docker Compose (R-003, `10-risk-register.md`, open); the Kubernetes target routes this traffic through the same public, cert-manager-issued TLS as the operator dashboard (R-003's Session 14 update).
- **`operatorapi` ↔ `agentapi` (same backend process, split listeners).** Deliberately two separate `gin.Engine`/`http.Server` instances with disjoint route sets (`backend/main.go`), closing R-004 (`10-risk-register.md`, closed Session 11): a replayed operator session cookie has no operator route to hit on the agent-facing listener, verified by `TestOperatorRouterHasNoAgentRoutes`/`TestAgentRouterHasNoOperatorRoutes`.
- **Backend ↔ Postgres.** Internal network only (Docker Compose network / Kubernetes `NetworkPolicy`, `deploy/k8s/base/networkpolicy.yaml`), password-authenticated, not exposed to either the browser or the agent-facing boundary.
- **Backend ↔ Redis.** Provisioned in both deployment targets (`deploy/k8s/base/redis.yaml`, `docker-compose.yml`) but not yet a real trust boundary: no backend code reads or writes it (`03-architecture.md`: "Not load-bearing... available for future use, not required by any FR/NFR this session").
- **Backend ↔ outbound alert destinations (webhook/SMTP/FCM/APNs).** The backend, on the operator's own instruction, makes outbound requests to operator-supplied destinations (a webhook URL, an SMTP relay, a push provider). This crosses from "operator-trusted configuration" to "arbitrary attacker-reachable network target" the moment an operator (or an attacker who has compromised the operator's session) can point a webhook channel at an internal address — see "Abuse cases".
- **CI ↔ GHCR.** `publish-images` (`.github/workflows/ci.yml`) pushes real images to `ghcr.io` on every merge to `main`, gated on both application CI jobs passing first (never a PR branch's untested image) — a supply-chain boundary from this repo's own CI identity (`GITHUB_TOKEN`) to a registry real deployments pull from.

## Threats (STRIDE)

| ID | Boundary | Threat | Category | L/I | Mitigation | Verified by |
|---|---|---|---|---|---|---|
| T-01 | Browser ↔ operator dashboard | Attacker guesses/brute-forces the one operator password | Spoofing | Low / High | bcrypt hashing (`operatorauth/password.go`) + `loginRateLimiter`: 5 failed attempts per 15-minute window, keyed by **both** attempted email and remote IP (`backend/internal/operatorapi/ratelimit.go`) — a blocked caller never burns a bcrypt comparison at all. | `backend/internal/operatorapi/ratelimit_test.go`, `auth_test.go` |
| T-02 | Browser ↔ operator dashboard | Attacker enumerates valid operator emails via login response timing/content | Information disclosure | Low / Low | `Login()` runs `VerifyPassword(dummyHash, password)` on a not-found email before returning the identical `invalid email or password` error either way (`backend/internal/operatorauth/login.go:36-51`) — same bcrypt cost paid, same error message, whether or not the email exists. | `operatorauth` login tests |
| T-03 | Browser ↔ operator dashboard | Session token forgery/tampering | Tampering | Low / High | HMAC-SHA256 over `operatorID:expiry`, secret sourced from `SESSION_SIGNING_SECRET` (≥32 bytes, base64) — any single-bit change invalidates the signature (`backend/internal/operatorauth/session.go`). | `operatorauth/session_test.go` |
| T-04 | Browser ↔ operator dashboard | CSRF: a malicious page makes the browser submit a mutating request using the operator's live cookie | Tampering | Low / Medium | Three layers together (`backend/internal/operatorapi/middleware.go`): `SameSite=Strict` on the session cookie (never attached cross-site), no CORS policy (blocks cross-origin JS from reading a response even if it could send one), and `RequireJSONContentType` (`middleware.go:115`) rejecting non-JSON bodies on every mutating route — closing the one residual case, a same-origin-cookie-bearing HTML form POST, since a plain HTML form cannot set `Content-Type: application/json`. | `operatorapi/auth_test.go` (415 on non-JSON) |
| T-05 | Remote agent ↔ `agentapi` | Agent bearer token theft (network capture, compromised host) used to impersonate the agent | Spoofing | Medium (Compose) / Medium | Token stored only as a SHA-256 hash server-side (`agentauth/credential.go`) — theft of the database does not recover it, but theft of the token in transit does grant full agent impersonation. Transport is still plaintext under Compose (R-003, open); real TLS under Kubernetes. `POST /api/v1/agents/:id/credential/rotate` lets an operator invalidate a suspected-compromised token. | `agentauth`, `operatorapi/agents_test.go` (rotation) |
| T-06 | Remote agent ↔ `agentapi` | Volumetric abuse of `POST /v1/logs`/`GET /agent/assignments` by a valid-but-misbehaving or captured agent | Denial of service | Low / Low | **No rate limiting exists on either agent-facing endpoint** — only the operator login path is rate-limited (`ratelimit.go`). Accepted for now: the agent count is small and operator-controlled (single self-hosted operator, `01-scope-and-non-goals.md`), not an open Internet-facing ingestion API. | Gap — no test claims otherwise |
| T-07 | Backend ↔ Postgres | SQL injection via any user/agent-controlled input | Tampering | Low / High | Parameterized queries (`$1`/`$2`) throughout every package spot-checked (`agentauth/authenticate.go`, `alerting/devicetokens.go`, `alerting/channel.go`). One dynamic-SQL construction exists (`operatorapi/targets.go`'s `UPDATE ... SET %s`), but the interpolated `%s` is always built from a fixed, hardcoded column-name allowlist (`addSet()`), never from request data — values are always bound as `$N`. | Existing handler tests exercise every mutable field |
| T-08 | Backend ↔ outbound webhook destination | Operator (or an attacker who has stolen the operator's session) configures a webhook destination pointing at an internal/private address (SSRF) | Elevation of privilege | Low / Medium | **Closed this session (B-012).** Two enforcement points: `alerting.ValidateWebhookURL` rejects a syntactically-invalid, non-http(s), or already-private/loopback/link-local-resolving destination at `CreateAlertChannel`/`RotateAlertChannelSecret` time (a same-request 422, `internal/operatorapi/alertchannels.go`); `alerting.guardedDialContext` re-resolves and validates the destination host on every single `WebhookDispatcher` connection attempt (including retries) and dials the validated address directly rather than a second, independent hostname lookup — the real control, since creation-time validation alone cannot stop DNS rebinding (a hostname free to resolve differently between the two checks). 169.254.169.254 and the whole 169.254.0.0/16 link-local range (cloud instance-metadata endpoints) are covered by the same range check as every other private/loopback/link-local/multicast address, not a special case. | `internal/alerting/ssrf_test.go` (creation-time and dispatch-time, including a literal DNS-rebinding-shaped case reaching the dial guard with no creation-time check at all); `internal/operatorapi/alertchannels_test.go` (`TestCreateAlertChannel_RejectsSSRFDestinations`, `TestRotateAlertChannelSecret_RejectsSSRFDestinations`) |
| T-09 | Backend ↔ alert_channels / device_tokens (data at rest) | Database compromise exposes alert destinations / push tokens in plaintext | Information disclosure | Low / Medium | **Closed this session (B-012).** Webhook URLs, email addresses, push credentials, and now device push tokens are all AES-256-GCM encrypted at rest (`alerting/channel.go`, `alerting/devicetokens.go`), key from `ALERT_CHANNEL_ENCRYPTION_KEY` (not itself in the database). `device_tokens.token_encrypted` replaces the old plaintext `token` column (migrations 000012/000013, backfilled via `backend/cmd/encrypt-device-tokens` for any pre-existing row); `device_tokens.token_hash` (HMAC-SHA256) is a deterministic blind index carrying the exact-match lookup the old plaintext column's uniqueness/upsert behavior depended on, since AES-GCM's randomised nonce can't support that. | `alerting/channel_test.go` (webhook/email/push credential round-trip); `alerting/devicetokens_test.go` (`TestRegisterDeviceToken_EncryptsAtRest`, `TestHashDeviceToken_DeterministicAndDistinguishesTokens`, `TestRegisterDeviceToken_MissingKeyFailsClosed`); `operatorapi/devicetokens_test.go` (`TestRegisterDeviceToken_EncryptsTokenAtRest`, reading the raw row directly, and asserting the plaintext column no longer exists in the schema at all) |
| T-10 | CI ↔ GHCR | A malicious dependency or compromised CI step publishes a backdoored image | Tampering | Low / High | `publish-images` only runs `needs: [backend, frontend]` on `push` to `main` (never a PR branch), after `govulncheck`, `npm audit`, `golangci-lint`, CodeQL, and gitleaks have all passed in the same run (`.github/workflows/ci.yml`) — see "Dependency and supply-chain controls". | CI job dependency graph itself |

## Abuse cases

- **Credential stuffing / brute force against the one operator account** — mitigated by T-01's rate limiter; not mitigated by account lockout with manual unlock (there is none — the fixed-window limiter self-clears), an accepted trade-off for a single self-hosted operator who cannot call a support line to unlock their own account.
- **Stolen agent token replayed from an unauthorized host** — the token alone is sufficient (no host/IP pinning); `credential/rotate` is the only recovery, and requires the operator to notice. There is no anomaly detection (e.g. "this agent's IP just changed") — out of scope for a v1 with no multi-agent fleet-anomaly requirement (`02-requirements.md`).
- **Compromised operator session used to redirect webhook alerts to an attacker-controlled endpoint, then silently re-pointed back** — the SSRF-specific variant (pointing the destination at an internal/private address) is now blocked at both creation and dispatch time (T-08, closed this session); a compromised session can still redirect to an attacker's own *public* endpoint, which the guard does not and cannot prevent (it is a legitimate-looking external destination, not a private one), and `CreateAlertChannel`/`alertchannels.go` still has no audit log of who changed a destination or when. Low severity in this system's own model: an attacker who already has the operator's session can already read every target/incident directly, so redirecting alerts adds little beyond what's already exposed.
- **A malicious or compromised third-party webhook/SMTP/push destination is slow or hangs, tying up dispatch workers** — ADR-0006/ADR-0007/ADR-0008 each specify their own timeout/retry/backoff bounds for exactly this; not re-litigated here.
- **An operator-provided webhook URL query string embeds a bearer token, which then appears in the encrypted-at-rest `destination_encrypted` column and in any real HTTP request log a receiving service keeps** — accepted: pulsewatch encrypts what it stores, but cannot control what the receiving end logs; this is a property of the generic-webhook model itself (`01-scope-and-non-goals.md`'s "why generic webhook, not a paging product").

## Authentication and authorisation design

- **Operator side.** Single credential type (email + bcrypt password hash), no self-registration endpoint — operators are provisioned out-of-band via `cmd/provision-operator` only (`backend/cmd/provision-operator`), matching the single-operator model. A successful login issues a stateless HMAC-signed session token (`operatorauth.IssueSession`) set as an `HttpOnly`, `Secure`, `SameSite=Strict` cookie named `pulsewatch_session` with a fixed 24h TTL (`SessionTTL`, `operatorauth/session.go`). There is no per-session revocation list by design — the package's own doc comment states revocation is by signing-secret rotation instead, a deliberate, documented trade-off for a single-operator deployment (rotating `SESSION_SIGNING_SECRET` invalidates every outstanding session at once, including the legitimate one). Logout (`POST /auth/logout`) simply clears the cookie client-side; short of a secret rotation, a stolen token remains valid until its own 24h TTL expires. Every mutating operator route requires both the `auth` (session-present-and-valid) and `csrf` (`RequireJSONContentType`) middleware (`operatorapi/router.go`).
- **Agent side.** A 32-byte random token (`crypto/rand`), issued once by an authenticated operator (`POST /api/v1/agents`) or via `cmd/seed-agent`, never displayed again. Verified as a bearer token on every `agentapi` request by hashing the presented value and looking up `agents.credential_hash` (`agentauth/authenticate.go`) — the plaintext token exists nowhere in the database. No token expiry; rotation is manual and operator-initiated only.
- **Authorisation.** Flat, not role-based: any valid operator session can act on any target/agent/alert-channel/incident (single-operator model, `01-scope-and-non-goals.md`'s explicit "Multi-user auth / role separation" non-goal). Any valid agent token can report check results only for targets already assigned to that agent (`agentapi/logs.go`, `agentauid` scoping) — an agent cannot report on another agent's targets or reach any operator-facing route (the split-router fix, R-004).

## Secrets management

*(Retained from Session 14 — the Kubernetes/Terraform-specific content below predates this session's fuller pass and is still accurate; not re-derived.)*

### Kubernetes/Terraform secrets (ADR-0005, Session 14)

- `infra/terraform/variables.tf` marks every credential-shaped variable
  (`postgres_password`, `session_signing_secret`,
  `alert_channel_encryption_key`) `sensitive = true` — Terraform redacts
  these from `plan`/`apply` console output and from `terraform.tfstate`'s
  human-readable diffs (the underlying state file itself still contains
  the real values in plaintext, Terraform's own standard behavior; this
  module does not configure a remote encrypted backend, since none is
  chosen yet — a future session should pick one, e.g. an encrypted S3/GCS
  backend with versioning, before this is used against a real production
  secret rather than staging/testing values).
- `terraform.tfvars` (the file that actually holds real secret values) is
  gitignored (`.gitignore`'s new Terraform section) and never committed —
  `terraform.tfvars.example` in the repo holds only empty placeholders.
- Kubernetes-side, secrets land in a single `Secret`
  (`pulsewatch-app-secrets`, `infra/terraform/main.tf`), consumed by
  `backend`/`postgres`/the migration `Job` via `envFrom`/`secretKeyRef` —
  never baked into an image or a ConfigMap. This is the Kubernetes-native
  minimum, not a hardened secret store: `Secret` objects are
  base64-encoded, not encrypted, at rest by default on most clusters
  (encryption-at-rest is a cluster-level configuration this module does
  not control) and are readable by anyone with `get secret` RBAC in the
  `pulsewatch` namespace. A future session should consider a real
  secrets-manager integration (e.g. `external-secrets` syncing from a
  cloud secrets manager, or Sealed Secrets for GitOps-safe encrypted
  commits) if this deployment target ever needs to satisfy a stricter
  threat model than "the operator's own cluster, the operator's own
  RBAC" — not built now, since no such stricter requirement exists yet
  for a single-operator, self-hosted deployment (`01-scope-and-non-goals.md`).
- `.env`/`.env.example` (Compose) and `terraform.tfvars.example`
  (Kubernetes) deliberately hold the identical set of required secrets —
  no deployment-target-specific secret was invented, so an operator
  moving from Compose to Kubernetes reuses the same values rather than
  generating a second set.

### Application-level secret handling

- `ALERT_CHANNEL_ENCRYPTION_KEY` (32 bytes, base64) and `SESSION_SIGNING_SECRET`
  (≥32 bytes, base64) are read once at process start (`backend/main.go`)
  and never logged; both fail process startup loudly if malformed or
  absent, rather than falling back to an insecure default.
- Test suites use fixed, non-secret, publicly-committed keys
  (`testEncryptionKey`, `testSessionSecret` — e.g.
  `backend/internal/alerting/testdb_test.go`) explicitly documented as
  test-only in their own comments; real code paths never read these,
  only the env-configured values.

## Dependency and supply-chain controls

- **`govulncheck`** (`go run golang.org/x/vuln/cmd/govulncheck@latest ./...`, `.github/workflows/ci.yml`) — Go module vulnerability scanning on every push/PR to `main`.
- **`npm audit --omit=dev`** — frontend production-dependency vulnerability scanning, same workflow.
- **`gitleaks`** (`gitleaks/gitleaks-action@v2`, full-history `fetch-depth: 0`) — secret-scanning on every push/PR, catching a committed credential before merge, not just at time of introduction.
- **CodeQL** (`github/codeql-action`, matrix over `go`/`javascript-typescript`) — static analysis for both languages on every push/PR; this project's own decision log (`09-decision-log.md`) records at least one CodeQL-flagged integer-conversion bug fixed for real and one false positive correctly dismissed with reasoning rather than silently suppressed.
- **`golangci-lint`** (pinned `v2.13.2`, matching the exact version a session runs locally before pushing) — style/correctness linting, not security-specific, but catches classes of bug (unchecked errors, some overflow patterns) that overlap with security correctness.
- **No `.github/dependabot.yml` exists** — dependency version bumps have so far been manual/ad hoc (e.g. the `go_modules` bump referenced in this repo's own commit history) rather than automated. A real, named gap: automated dependency-update PRs are a small, well-understood addition a future session could make without touching the technology-freeze rule (`01-scope-and-non-goals.md`), since Dependabot is CI configuration, not a new application dependency.
- **Image publishing gate.** `publish-images` only runs after `backend`, `frontend`, CodeQL, gitleaks, and the IaC/K8s job would all need to pass on the same commit for it to reach `main` in the first place (branch protection assumed, not itself configured in this repo's own files) — no untested or unscanned code reaches a real, pullable `ghcr.io` image.

## Accepted risks (reason + revisit trigger)

| Risk | Reason accepted | Revisit trigger |
|---|---|---|
| No rate limiting on agent-facing endpoints (T-06) | Agent count is small and operator-controlled; this is not an open Internet-facing ingestion API by design (`01-scope-and-non-goals.md`). | If a future session ever accepts agent registration from a less-trusted source, or observes real abusive agent traffic. |
| Agent-facing transport still plaintext HTTP under Docker Compose (R-003, `10-risk-register.md`) | Retained here as a cross-reference rather than restated — see the risk register for the full mitigation status (mitigated by design for the Kubernetes target; open for Compose pending a real remote-agent deployment gate). | Already tracked; see R-003 directly. |
| Stateless session tokens have no *per-session* revocation (T-03/authentication design) | A 24h TTL bounds exposure without needing a session store (matching this project's own "Redis not load-bearing" stance, `03-architecture.md`); the documented fallback (rotate `SESSION_SIGNING_SECRET`) invalidates every session, not just the compromised one, and logout is client-side-only. | If a real incident ever requires invalidating one specific stolen session without also logging out the legitimate operator — would need a minimal per-session revocation list (e.g. a denylist keyed by issued-at, since there's no session ID today), a real design change, not a config flag. |
| No `.github/dependabot.yml` (dependency and supply-chain controls, above) | Manual dependency bumps have been sufficient so far at this project's current dependency count and change velocity. | Before any session claims full automated-supply-chain-hygiene coverage, or if a `govulncheck`/`npm audit` finding is ever traced back to a dependency that had been outdated for a long, avoidable stretch. |
