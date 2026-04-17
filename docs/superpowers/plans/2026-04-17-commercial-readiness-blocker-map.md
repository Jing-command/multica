# Commercial Readiness Blocker Map Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild a current commercial-readiness blocker map from the live Multica codebase, classify remaining blockers by severity, and produce the next focused implementation scope for the highest-value commercialization risks.

**Architecture:** This work is an evidence-first audit and scoping pass, not a broad code-change phase. The implementation produces two authoritative docs: a blocker report grounded in current code and workflows, and a next-wave scope that converts only the highest-severity gaps into the next implementation target.

**Tech Stack:** Markdown docs, Git, Go backend source, Next.js E2E coverage, GitHub Actions workflow config.

---

## File map

### Create

- `docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md` — evidence-backed commercialization blocker report with P0/P1/P2 classification.
- `docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md` — scoped follow-up document naming the next implementation wave and why it wins.

### Modify

- `docs/superpowers/plans/2026-04-17-commercial-readiness-blocker-map.md` — this plan, only if self-review reveals gaps.

### Existing references to inspect and cite

- `docs/superpowers/specs/2026-04-17-commercial-readiness-blocker-map-design.md` — approved design and scope guardrails.
- `server/cmd/server/router.go:82-109` — public auth routes, daemon routes, and current middleware wiring.
- `server/internal/middleware/auth.go:18-94` — JWT/PAT middleware behavior.
- `server/internal/middleware/daemon_auth.go:34-112` — machine-token-only daemon auth behavior that may not be wired in yet.
- `server/internal/handler/daemon.go:35-584` — daemon registration, task lifecycle, and daemon-facing task APIs.
- `server/internal/handler/runtime_ping.go:14-200` — ping lifecycle persistence model.
- `server/internal/handler/runtime_update.go:12-221` — update lifecycle persistence model.
- `server/internal/handler/personal_access_token.go:44-132` — PAT creation, listing, and revocation semantics.
- `server/internal/auth/jwt.go:12-53` — JWT secret/token generation primitives.
- `server/pkg/db/queries/daemon_token.sql:1-17` — daemon token lookup and cleanup capabilities.
- `server/pkg/db/queries/runtime.sql:1-77` — runtime ownership and stale-runtime cleanup queries.
- `server/internal/service/task.go:33-320` — task enqueue/claim/complete/fail ownership and lifecycle logic.
- `e2e/auth.spec.ts:1-46` — browser auth/logout golden-path coverage.
- `.github/workflows/ci.yml:13-70` — current CI verification shape.
- `.github/workflows/release.yml:11-35` — release and CLI publishing behavior.
- `.worktrees/auth-abuse-guard/.planning/reports/SESSION_REPORT.md:9-72` — prior merged hardening summary to avoid re-solving closed work.

---

### Task 1: Set up the blocker report skeleton from the approved design

**Files:**
- Create: `docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md`
- Create: `docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md`
- Reference: `docs/superpowers/specs/2026-04-17-commercial-readiness-blocker-map-design.md`

- [ ] **Step 1: Create the blocker report skeleton**

Write `docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md` with exactly this starter content:

```md
# Commercial Readiness Blocker Map

Date: 2026-04-17
Status: Draft

## 1. Completed hardening accepted for now

- Runtime durability hardening from the recent merged security sweep.
- Auth send-code and verify-code abuse hardening from the recent merged security sweep.
- Daemon control-plane machine-auth hardening already completed in a follow-up worktree and merged.
- Auth fail-closed startup/config behavior already completed and merged.
- Browser and WebSocket session transport hardening already completed and merged.

## 2. P0 blockers

## 3. P1 blockers

## 4. P2 follow-ups

## 5. Recommended execution order

## 6. Evidence appendix
```

- [ ] **Step 2: Create the next-wave scope skeleton**

Write `docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md` with exactly this starter content:

```md
# Commercial Readiness Next-Wave Scope

Date: 2026-04-17
Status: Draft

## 1. Selected next wave

## 2. Why this wave is first

## 3. What this wave includes

## 4. What this wave explicitly does not include

## 5. Required verification
```

- [ ] **Step 3: Verify the files exist and match the intended headings**

Run: `cd /Users/a1234/multica && python3 - <<'PY'
from pathlib import Path
for path in [
    Path('docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md'),
    Path('docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md'),
]:
    text = path.read_text()
    print(path)
    print(text.splitlines()[0])
PY`

Expected output:

```text
docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md
# Commercial Readiness Blocker Map
docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md
# Commercial Readiness Next-Wave Scope
```

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md
git commit -m "docs(readiness): scaffold blocker assessment outputs"
```

---

### Task 2: Audit auth and boundary enforcement for commercial blockers

**Files:**
- Modify: `docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md`
- Reference: `server/cmd/server/router.go:82-109`
- Reference: `server/internal/middleware/auth.go:18-94`
- Reference: `server/internal/middleware/daemon_auth.go:34-112`
- Reference: `server/internal/handler/daemon.go:35-584`
- Reference: `server/internal/handler/personal_access_token.go:44-132`
- Reference: `server/internal/auth/jwt.go:12-53`
- Reference: `server/pkg/db/queries/daemon_token.sql:1-17`

- [ ] **Step 1: Capture daemon-route middleware evidence**

Read the daemon route wiring and auth middleware behavior and add this subsection under `## 2. P0 blockers`:

```md
### P0-1: Daemon API routes are currently wired through general user auth middleware, not machine-only daemon auth

**Affected area:** Daemon control-plane boundary

**Evidence:** `server/cmd/server/router.go:87-109` wires `/api/daemon/...` through `middleware.Auth(queries)`. The machine-scoped middleware exists in `server/internal/middleware/daemon_auth.go:34-112`, but it is not the middleware currently attached to those routes in the main checkout.

**Why it matters commercially:** A daemon control-plane should be enforceable as a machine boundary, not a user-token boundary. If daemon routes remain reachable via ordinary JWT or PAT semantics, the runtime/task control surface is broader than intended and weakens the trust model for production deployments.

**Severity:** P0

**Recommended remediation direction:** Rewire daemon routes to machine-only daemon auth, remove JWT/PAT fallback for daemon endpoints, and add focused route-level tests proving user tokens cannot access daemon task/runtime endpoints.
```

- [ ] **Step 2: Capture token lifecycle evidence**

Add this subsection under `## 3. P1 blockers`:

```md
### P1-1: Token lifecycle coverage is still thin for commercial operations

**Affected area:** PAT and daemon token hygiene

**Evidence:** `server/internal/handler/personal_access_token.go:44-132` supports PAT creation/list/revoke, and `server/pkg/db/queries/daemon_token.sql:1-17` supports daemon token lookup and deletion. The current mainline evidence does not yet show operator-facing rotation policy, explicit last-used visibility for daemon tokens, or route tests that enforce token-class separation across all daemon endpoints.

**Why it matters commercially:** This does not immediately imply an auth bypass, but it weakens day-2 operational trust. Commercial deployments need clearer token lifecycle discipline to support rotation, incident response, and boundary verification.

**Severity:** P1

**Recommended remediation direction:** Add explicit daemon token lifecycle management expectations, route-level separation tests, and operational visibility for token usage/rotation state.
```

- [ ] **Step 3: Capture task/control-plane ownership risk if present in current code**

Add this subsection under `## 3. P1 blockers`:

```md
### P1-2: Daemon task control endpoints still need a fresh object-boundary audit after recent hardening

**Affected area:** Task lifecycle ownership

**Evidence:** `server/internal/handler/daemon.go:306-584` exposes task start, progress, complete, fail, usage, and message endpoints keyed by task ID. Recent hardening work reportedly improved daemon/runtime/task ownership checks, but the current checkout still needs a focused mainline re-audit to ensure every daemon-facing task endpoint enforces runtime/daemon ownership consistently rather than trusting global task IDs alone.

**Why it matters commercially:** If any daemon task endpoint accepts a valid machine context but fails to bind the target task to the owning runtime or daemon, production operators could face cross-runtime control leakage.

**Severity:** P1

**Recommended remediation direction:** Audit each daemon task endpoint against runtime/daemon ownership, add regression tests per endpoint, and promote any confirmed bypass to P0 immediately.
```

- [ ] **Step 4: Run a focused grep to prove the middleware mismatch is still present**

Run: `grep -n "Route(\"/api/daemon\"\|Use(middleware.Auth\|Use(middleware.DaemonAuth" /Users/a1234/multica/server/cmd/server/router.go`

Expected output contains lines showing `/api/daemon` and `Use(middleware.Auth(queries))`, and does not show `Use(middleware.DaemonAuth(` on that route group.

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md
git commit -m "docs(readiness): record auth boundary blockers"
```

---

### Task 3: Audit durability and restart consistency gaps

**Files:**
- Modify: `docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md`
- Reference: `server/internal/handler/runtime_ping.go:14-200`
- Reference: `server/internal/handler/runtime_update.go:12-221`
- Reference: `server/pkg/db/queries/runtime.sql:39-77`
- Reference: `server/internal/service/task.go:172-320`

- [ ] **Step 1: Record ping/update persistence findings**

Add this subsection under `## 2. P0 blockers` if the code still matches the current in-memory implementation:

```md
### P0-2: Runtime ping and update flows are still in-memory in the main checkout

**Affected area:** Runtime operations durability

**Evidence:** `server/internal/handler/runtime_ping.go:41-141` stores ping state in `PingStore`, an in-memory map. `server/internal/handler/runtime_update.go:38-138` stores update state in `UpdateStore`, also an in-memory map.

**Why it matters commercially:** Runtime health and update workflows should survive process restarts and should remain observable during production incidents. In-memory control-plane state is not durable enough for commercial operations because restart or crash paths can erase pending or historical operation state.

**Severity:** P0

**Recommended remediation direction:** Move ping/update lifecycle records to durable storage, bind them to runtime ownership, and add restart-safe handler/integration tests.
```

- [ ] **Step 2: Record offline-runtime cleanup as accepted existing mitigation, not a blocker**

Under `## 1. Completed hardening accepted for now`, append this bullet if the query still exists unchanged:

```md
- Runtime offline cleanup has current database support in `server/pkg/db/queries/runtime.sql:50-67` for marking stale runtimes offline and failing dispatched/running tasks tied to offline runtimes.
```

- [ ] **Step 3: Record residual task lifecycle audit need**

Add this subsection under `## 3. P1 blockers`:

```md
### P1-3: Task restart and recovery semantics need explicit commercialization review even after recent hardening

**Affected area:** Task lifecycle durability and recovery

**Evidence:** `server/internal/service/task.go:172-320` has runtime-aware claim/reconcile logic and uses persisted task rows, which is good. The remaining commercialization question is whether operator-visible recovery semantics are explicit enough when daemon sessions resume, tasks fail mid-flight, or prior session state is reused from `server/internal/handler/daemon.go:268-280`.

**Why it matters commercially:** This is not an obvious auth bypass, but it affects whether the system behaves predictably during daemon crashes, reconnects, and support incidents.

**Severity:** P1

**Recommended remediation direction:** Confirm end-to-end recovery semantics for interrupted tasks and session reuse, then add narrowly targeted tests or operational tooling where ambiguity remains.
```

- [ ] **Step 4: Verify the persistence finding with direct file search**

Run: `grep -n "type PingStore\|type UpdateStore\|map\[string\]\*PingRequest\|map\[string\]\*UpdateRequest" /Users/a1234/multica/server/internal/handler/runtime_ping.go /Users/a1234/multica/server/internal/handler/runtime_update.go`

Expected output contains `type PingStore`, `type UpdateStore`, and the in-memory map fields.

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md
git commit -m "docs(readiness): record durability blockers"
```

---

### Task 4: Audit operational readiness and release discipline

**Files:**
- Modify: `docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md`
- Reference: `.github/workflows/ci.yml:13-70`
- Reference: `.github/workflows/release.yml:11-35`
- Reference: `e2e/auth.spec.ts:1-46`

- [ ] **Step 1: Record current strengths that are acceptable for now**

Under `## 1. Completed hardening accepted for now`, append these bullets:

```md
- CI currently builds frontend and backend separately and runs backend migrations plus `go test ./...` in GitHub Actions (`.github/workflows/ci.yml:13-70`).
- Tagged releases currently run backend tests and GoReleaser publishing (`.github/workflows/release.yml:11-35`).
- Browser auth golden paths currently have E2E coverage for login, redirect, unauthenticated redirect, and logout (`e2e/auth.spec.ts:1-46`).
```

- [ ] **Step 2: Record ops visibility or release-risk gap if still present**

Add this subsection under `## 3. P1 blockers`:

```md
### P1-4: Commercial operations still need a clearer operator-facing readiness layer

**Affected area:** Operational visibility and release confidence

**Evidence:** CI and release workflows exist, but the current mainline evidence is still thin on production-oriented operator visibility for critical auth/runtime failure modes. The current visible automated browser coverage is limited to core auth flows in `e2e/auth.spec.ts:1-46`, and the workflow files do not by themselves prove incident-friendly observability or rollback-specific checks.

**Why it matters commercially:** This is usually survivable for a tightly managed pilot, but it raises support cost and slows incident response in real deployments.

**Severity:** P1

**Recommended remediation direction:** Define the minimum operator-facing visibility requirements for auth/runtime failures and extend verification coverage only where it closes concrete supportability gaps.
```

- [ ] **Step 3: Record any lower-priority follow-up explicitly as P2**

Add this subsection under `## 4. P2 follow-ups`:

```md
### P2-1: Broader product-surface E2E coverage can expand after blocker closure

**Affected area:** Regression confidence

**Evidence:** Current visible E2E auth coverage is narrow and does not by itself cover the full runtime/agent/operator surface.

**Why it matters commercially:** Useful for confidence, but not a standalone launch blocker if the highest-risk auth and control-plane boundaries are fixed and the critical golden paths remain covered.

**Severity:** P2

**Recommended remediation direction:** Expand E2E coverage after the next-wave blocker fixes land, focusing on runtime/daemon/operator workflows with real commercial support value.
```

- [ ] **Step 4: Run a quick check that CI and release workflows still include the cited commands**

Run: `grep -n "pnpm build && pnpm typecheck && pnpm test\|go test ./...\|goreleaser" /Users/a1234/multica/.github/workflows/ci.yml /Users/a1234/multica/.github/workflows/release.yml`

Expected output contains the frontend build/typecheck/test line, backend `go test ./...`, and GoReleaser invocation.

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md
git commit -m "docs(readiness): record ops and release findings"
```

---

### Task 5: Finalize severity ordering and choose the next wave

**Files:**
- Modify: `docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md`
- Modify: `docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md`
- Reference: `.worktrees/auth-abuse-guard/.planning/reports/SESSION_REPORT.md:9-72`

- [ ] **Step 1: Fill in the execution order in the blocker report**

Replace the empty `## 5. Recommended execution order` section in `docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md` with:

```md
## 5. Recommended execution order

1. Rewire daemon routes to machine-only daemon auth and add route-level token-class separation tests.
2. Make runtime ping/update lifecycle durable in the main checkout and verify restart-safe behavior.
3. Re-audit daemon task endpoints for runtime/daemon ownership on every task action and promote any confirmed bypass to P0.
4. Tighten token lifecycle and operator visibility where the current evidence is still thin.
5. Defer broader E2E coverage expansion until the control-plane and durability blockers are closed.
```

- [ ] **Step 2: Fill in the evidence appendix with the exact references used**

Replace the empty `## 6. Evidence appendix` section with:

```md
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
- `.worktrees/auth-abuse-guard/.planning/reports/SESSION_REPORT.md:9-72`
```

- [ ] **Step 3: Fill in the next-wave scope doc with the selected wave**

Replace the empty sections in `docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md` with exactly this content:

```md
## 1. Selected next wave

Daemon control-plane boundary closure and runtime operation durability.

## 2. Why this wave is first

This wave removes the highest-risk commercialization gaps exposed by the current mainline state: the daemon route group is still wired through general user auth in `server/cmd/server/router.go:87-109`, and ping/update control flows are still in-memory in `server/internal/handler/runtime_ping.go:41-141` and `server/internal/handler/runtime_update.go:38-138`. Together these gaps weaken the production trust boundary and operator confidence more than lower-priority cleanup or broader coverage work.

## 3. What this wave includes

- Rewire `/api/daemon/...` to machine-only daemon auth.
- Remove JWT/PAT fallback acceptance on daemon endpoints.
- Add route-level tests proving user tokens cannot access daemon control-plane APIs.
- Persist ping/update lifecycle state durably.
- Add focused tests for runtime ownership, restart-safe visibility, and result reporting on durable ping/update records.
- Re-audit daemon task endpoints for runtime/daemon ownership while touching the same control-plane surface.

## 4. What this wave explicitly does not include

- Broad frontend feature work unrelated to commercialization blockers.
- Cosmetic code cleanup.
- Large-scale observability platform additions.
- Full product-surface E2E expansion beyond the control-plane paths directly affected by this wave.

## 5. Required verification

- Backend tests covering daemon route auth separation.
- Backend tests covering durable ping/update lifecycle behavior.
- Regression checks for task lifecycle endpoints touched during the ownership audit.
- Existing auth/browser golden path checks remain green if any session/auth surfaces are touched.
```

- [ ] **Step 4: Verify the blocker report contains at least two P0s and four P1/P2 follow-ups**

Run: `cd /Users/a1234/multica && python3 - <<'PY'
from pathlib import Path
text = Path('docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md').read_text()
print('P0 count =', text.count('### P0-'))
print('P1 count =', text.count('### P1-'))
print('P2 count =', text.count('### P2-'))
PY`

Expected output:

```text
P0 count = 2
P1 count = 4
P2 count = 1
```

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md
git commit -m "docs(readiness): choose next commercialization wave"
```

---

### Task 6: Final review and handoff

**Files:**
- Modify: `docs/superpowers/plans/2026-04-17-commercial-readiness-blocker-map.md` (only if self-review finds a gap)
- Review: `docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md`
- Review: `docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md`

- [ ] **Step 1: Read both output docs end-to-end and check for unsupported claims**

Run: `cd /Users/a1234/multica && python3 - <<'PY'
from pathlib import Path
for path in [
    Path('docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md'),
    Path('docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md'),
]:
    print(f'--- {path} ---')
    print(path.read_text())
PY`

Expected: every blocker statement cites a concrete file/line reference already listed in the plan.

- [ ] **Step 2: Run a placeholder scan on the report outputs**

Run: `cd /Users/a1234/multica && grep -RniE "TBD|TODO|implement later|fill in details|appropriate error handling|handle edge cases|Similar to Task" docs/superpowers/reports/2026-04-17-commercial-readiness-*.md`

Expected: no output.

- [ ] **Step 3: Confirm the final diff is limited to the expected docs**

Run: `cd /Users/a1234/multica && git diff --name-only -- docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md docs/superpowers/plans/2026-04-17-commercial-readiness-blocker-map.md`

Expected output:

```text
docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md
docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md
```

If Step 2 caused a legitimate change to this plan during self-review, then the plan path may also appear.

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/reports/2026-04-17-commercial-readiness-blocker-map.md docs/superpowers/reports/2026-04-17-commercial-readiness-next-wave.md docs/superpowers/plans/2026-04-17-commercial-readiness-blocker-map.md
git commit -m "docs(readiness): finalize commercialization blocker assessment"
```

---

## Self-review

### Spec coverage

- Current blocker report output: covered by Tasks 1 through 5.
- Severity classification using code-backed evidence: covered by Tasks 2 through 5.
- Distinguishing already-completed hardening from current blockers: covered by Tasks 1, 4, and 5, with the prior session report cited.
- Focused next-wave implementation scope instead of a giant roadmap: covered by Task 5.
- Scope control against unrelated refactors or cosmetic improvements: enforced by the selected-wave content in Task 5 and the diff check in Task 6.

### Placeholder scan

- Report outputs contain no unresolved placeholder markers or deferred-implementation notes.
- Every doc-writing step includes exact markdown to write.
- Every verification step includes an exact command and expected output.

### Type consistency

- Blocker identifiers (`P0-1`, `P0-2`, `P1-1` through `P1-4`, `P2-1`) are used consistently across the report and verification step.
- The next-wave scope matches the execution order in the blocker report.
- Every cited file path appears in the file map or in a task reference list above.
