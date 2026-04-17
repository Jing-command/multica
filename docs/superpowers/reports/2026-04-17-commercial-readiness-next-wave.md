# Commercial Readiness Next-Wave Scope

Date: 2026-04-17
Status: Draft

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
