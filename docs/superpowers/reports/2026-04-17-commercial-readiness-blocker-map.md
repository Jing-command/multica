# Commercial Readiness Blocker Map

Date: 2026-04-17
Status: Draft

## 1. Completed hardening accepted for now

- Runtime durability hardening from the recent merged security sweep.
- Auth send-code and verify-code abuse hardening from the recent merged security sweep.
- Auth fail-closed startup/config behavior already completed and merged.
- Browser and WebSocket session transport hardening already completed and merged.
- Runtime offline cleanup has current database support in `server/pkg/db/queries/runtime.sql:50-67` for marking stale runtimes offline and failing dispatched/running tasks tied to offline runtimes.
- CI currently builds frontend and backend separately and runs backend migrations plus `go test ./...` in GitHub Actions (`.github/workflows/ci.yml:13-70`).
- Tagged releases currently run backend tests and GoReleaser publishing (`.github/workflows/release.yml:11-35`).
- Browser auth golden paths currently have E2E coverage for login, redirect, unauthenticated redirect, and logout (`e2e/auth.spec.ts:1-46`).

## 2. P0 blockers

### P0-1: Daemon API routes are currently wired through general user auth middleware, not machine-only daemon auth

**Affected area:** Daemon control-plane boundary

**Evidence:** `server/cmd/server/router.go:87-109` wires `/api/daemon/...` through `middleware.Auth(queries)`. The machine-scoped middleware exists in `server/internal/middleware/daemon_auth.go:34-112`, but it is not the middleware currently attached to those routes in the main checkout.

**Why it matters commercially:** A daemon control-plane should be enforceable as a machine boundary, not a user-token boundary. If daemon routes remain reachable via ordinary JWT or PAT semantics, the runtime/task control surface is broader than intended and weakens the trust model for production deployments.

**Severity:** P0

**Recommended remediation direction:** Rewire daemon routes to machine-only daemon auth, remove JWT/PAT fallback for daemon endpoints, and add focused route-level tests proving user tokens cannot access daemon task/runtime endpoints.

### P0-2: Runtime ping and update flows are still in-memory in the main checkout

**Affected area:** Runtime operations durability

**Evidence:** `server/internal/handler/runtime_ping.go:41-141` stores ping state in `PingStore`, an in-memory map. `server/internal/handler/runtime_update.go:38-138` stores update state in `UpdateStore`, also an in-memory map.

**Why it matters commercially:** Runtime health and update workflows should survive process restarts and should remain observable during production incidents. In-memory control-plane state is not durable enough for commercial operations because restart or crash paths can erase pending or historical operation state.

**Severity:** P0

**Recommended remediation direction:** Move ping/update lifecycle records to durable storage, bind them to runtime ownership, and add restart-safe handler/integration tests.

## 3. P1 blockers

### P1-1: Daemon token lifecycle and operational visibility evidence remains thin

**Affected area:** Daemon token lifecycle and daemon token-class operational evidence

**Evidence:** PAT lifecycle coverage is present in mainline (`server/internal/handler/personal_access_token.go:19,34,44-132`), and PAT usage visibility is wired via `last_used_at` exposure/update (`server/internal/middleware/auth.go:53-54`, `server/pkg/db/queries/personal_access_token.sql:23-25`). By contrast, current blocker evidence for daemon tokens is narrower: mainline artifacts shown in this review do not yet demonstrate an equivalent, operator-facing daemon token lifecycle/usage visibility path or explicit daemon-token-class boundary evidence at the same level.

**Why it matters commercially:** This is an operational assurance gap, not a confirmed bypass. Commercial operators still need stronger day-2 evidence for daemon-token rotation, usage attribution, and token-class boundary confidence.

**Severity:** P1

**Recommended remediation direction:** Scope follow-up to daemon tokens only: document and enforce daemon token lifecycle expectations, add operator-visible daemon token usage/rotation telemetry, and produce explicit daemon-token-class boundary evidence for the daemon control surface.

### P1-2: Mainline daemon task endpoints expose taskId control paths that still require an explicit object-boundary audit

**Affected area:** Daemon task control object-boundary assurance

**Evidence:** `server/internal/handler/daemon.go:306-584` exposes daemon task start, progress, complete, fail, usage, and message endpoints keyed by `taskId`. Current blocker-map evidence does not yet include a direct, consolidated mainline proof that all of these taskId entry points are fully covered by runtime/daemon ownership validation.

**Why it matters commercially:** This is an audit gap and residual commercial risk, not a confirmed bypass. Until object-boundary coverage is explicitly demonstrated across the exposed taskId control paths, production assurance remains incomplete.

**Severity:** P1

**Recommended remediation direction:** Run and document an explicit endpoint-by-endpoint object-boundary audit for the daemon task control surface, then back it with focused regression tests and only escalate severity if a concrete ownership bypass is verified.

### P1-3: Task restart and recovery semantics need explicit commercialization review even after recent hardening

**Affected area:** Task lifecycle durability and recovery

**Evidence:** `server/internal/service/task.go:172-320` has runtime-aware claim/reconcile logic and uses persisted task rows, which is good. The remaining commercialization question is whether operator-visible recovery semantics are explicit enough when daemon sessions resume, tasks fail mid-flight, or prior session state is reused from `server/internal/handler/daemon.go:268-280`.

**Why it matters commercially:** This is not an obvious auth bypass, but it affects whether the system behaves predictably during daemon crashes, reconnects, and support incidents.

**Severity:** P1

**Recommended remediation direction:** Confirm end-to-end recovery semantics for interrupted tasks and session reuse, then add narrowly targeted tests or operational tooling where ambiguity remains.

### P1-4: Commercial operations still need a clearer operator-facing readiness layer

**Affected area:** Operational visibility and release confidence

**Evidence:** CI and release workflows exist, but the current mainline evidence is still thin on production-oriented operator visibility for critical auth/runtime failure modes. The current visible automated browser coverage is limited to core auth flows in `e2e/auth.spec.ts:1-46`, and the workflow files do not by themselves prove incident-friendly observability or rollback-specific checks.

**Why it matters commercially:** This is usually survivable for a tightly managed pilot, but it raises support cost and slows incident response in real deployments.

**Severity:** P1

**Recommended remediation direction:** Define the minimum operator-facing visibility requirements for auth/runtime failures and extend verification coverage only where it closes concrete supportability gaps.

## 4. P2 follow-ups

### P2-1: Broader product-surface E2E coverage can expand after blocker closure

**Affected area:** Regression confidence

**Evidence:** Current visible E2E auth coverage is narrow and does not by itself cover the full runtime/agent/operator surface.

**Why it matters commercially:** Useful for confidence, but not a standalone launch blocker if the highest-risk auth and control-plane boundaries are fixed and the critical golden paths remain covered.

**Severity:** P2

**Recommended remediation direction:** Expand E2E coverage after the next-wave blocker fixes land, focusing on runtime/daemon/operator workflows with real commercial support value.

## 5. Recommended execution order

1. Rewire daemon routes to machine-only daemon auth and add route-level token-class separation tests.
2. Make runtime ping/update lifecycle durable in the main checkout and verify restart-safe behavior.
3. Re-audit daemon task endpoints for runtime/daemon ownership on every task action and promote any confirmed bypass to P0.
4. Tighten token lifecycle and operator visibility where the current evidence is still thin.
5. Defer broader E2E coverage expansion until the control-plane and durability blockers are closed.

## 6. Evidence appendix

- `server/cmd/server/router.go:87-109`
- `server/internal/middleware/auth.go:18-94`
- `server/internal/middleware/daemon_auth.go:34-112`
- `server/internal/handler/daemon.go:268-584`
- `server/internal/handler/runtime_ping.go:41-141`
- `server/internal/handler/runtime_update.go:38-138`
- `server/internal/handler/personal_access_token.go:44-132`
- `server/internal/auth/jwt.go:12-53`
- `server/pkg/db/queries/daemon_token.sql:1-17`
- `server/pkg/db/queries/runtime.sql:39-77`
- `server/internal/service/task.go:172-320`
- `e2e/auth.spec.ts:1-46`
- `.github/workflows/ci.yml:13-70`
- `.github/workflows/release.yml:11-35`
