# Daemon Boundary and Runtime Durability Design

Date: 2026-04-17
Status: Approved for planning

## 1. Objective

Close the highest-priority commercialization gaps identified in the current blocker map by hardening the daemon control-plane boundary and making runtime ping/update operations durable across process restarts.

This phase focuses on the live mainline gaps currently visible in the worktree:

- daemon routes are still wired through general user auth middleware instead of a machine-only daemon boundary
- runtime ping and update workflows still rely on in-memory stores rather than durable request records

## 2. Goals

- Make `/api/daemon/...` reachable only through daemon-scoped machine tokens.
- Remove PAT and JWT fallback behavior from daemon control-plane authentication.
- Persist ping/update request state so runtime operations survive process restarts and remain observable.
- Bind daemon control-plane operations to explicit daemon/workspace/runtime ownership boundaries.
- Use this same control-plane wave to perform an endpoint-by-endpoint ownership audit on daemon task endpoints.
- Add focused backend tests that prove auth separation, durable request behavior, and ownership enforcement.

## 3. Non-goals

- Reworking the general user auth model.
- Building a full daemon token rotation product surface in this phase.
- Expanding observability into a broad platform effort.
- Expanding frontend work beyond what is required for existing consumers to keep functioning.
- Performing broad E2E expansion outside the daemon/runtime control paths touched here.

## 4. Current state

### 4.1 Daemon route boundary

`server/cmd/server/router.go:87-109` currently wires `/api/daemon/...` through `middleware.Auth(queries)`.

That means the daemon control-plane is still entered through the same auth middleware family used for user-facing JWT/PAT access, rather than a dedicated machine boundary.

### 4.2 Daemon auth middleware behavior

`server/internal/middleware/daemon_auth.go:34-112` already contains daemon-token handling, but still falls back to PAT and JWT validation.

This means even after route rewiring, the middleware itself would still not represent a strict machine-only boundary unless the fallback behavior is removed.

### 4.3 Ping and update durability

`server/internal/handler/runtime_ping.go:41-141` stores ping state in `PingStore`, an in-memory map.

`server/internal/handler/runtime_update.go:38-138` stores update state in `UpdateStore`, also in memory.

These stores are not durable across server restarts and are not suitable as the workflow truth for commercial operations.

## 5. Design summary

This phase converts the daemon control-plane into a machine-only surface and replaces in-memory ping/update state with database-backed request records.

The implementation stays narrow:

- route boundary corrected at the router layer
- daemon identity enforced at the middleware layer
- ping/update lifecycle moved into SQL-backed state
- daemon task endpoints audited under the same boundary model

The core principle is that control-plane truth must live in durable backend state and be operated only by machine-scoped credentials.

## 6. Responsibility boundaries

### 6.1 User auth vs daemon auth

User auth and daemon auth must be distinct trust boundaries.

- user routes continue to use the existing auth middleware model
- daemon routes use daemon-only auth
- daemon routes must not accept PAT or JWT tokens
- daemon middleware must not simulate user identity by setting `X-User-ID`

The daemon boundary should surface daemon/workspace identity, not user identity.

### 6.2 Runtime operation state

Ping and update lifecycle state must move out of handler-local memory and into persistent request records.

Handlers should only:

- create a request
- read a request
- claim a pending request for daemon execution
- update request status/result

Handlers should not own the authoritative lifecycle state in memory.

### 6.3 Ownership enforcement

Every daemon-facing control operation must prove boundary ownership:

- the daemon token identifies a daemon and workspace
- the runtime involved must belong to that daemon/workspace boundary
- task or request IDs are never trusted on their own
- result-reporting endpoints must reject records outside the caller's daemon boundary

## 7. Data model

This phase should introduce two durable request models rather than a single generalized control-operation table.

### 7.1 `runtime_ping_request`

Suggested fields:

- `id`
- `runtime_id`
- `workspace_id`
- `daemon_id`
- `status` (`pending`, `running`, `completed`, `failed`, `timeout`)
- `output`
- `error`
- `duration_ms`
- `created_at`
- `updated_at`
- `completed_at`

### 7.2 `runtime_update_request`

Suggested fields:

- `id`
- `runtime_id`
- `workspace_id`
- `daemon_id`
- `target_version`
- `status` (`pending`, `running`, `completed`, `failed`, `timeout`)
- `output`
- `error`
- `created_at`
- `updated_at`
- `completed_at`

### 7.3 Why two tables

Ping and update are related but not identical workflows.

Using two focused tables keeps:

- queries simpler
- indexes clearer
- handler code more explicit
- future operational reporting easier to reason about

This phase should prefer clarity over premature generalization.

## 8. Request flow design

### 8.1 Ping flow

1. Frontend calls `InitiatePing`.
2. Handler loads the runtime and confirms workspace membership on the user-facing side.
3. Handler creates a `runtime_ping_request` row in `pending` state.
4. Daemon-side control flow loads the next pending ping for its authorized runtime boundary.
5. Claiming the request moves it to `running`.
6. Daemon reports `completed` or `failed` result back to the server.
7. `GetPing` reads the durable row from the database.
8. Timeout behavior is derived from durable timestamps and persisted back to the record rather than computed only in process-local memory.

### 8.2 Update flow

1. Frontend calls `InitiateUpdate`.
2. Handler validates workspace access and target version input.
3. Handler creates a `runtime_update_request` row in `pending` state.
4. Only one pending/running update should exist per runtime at a time.
5. Daemon claims the pending update and transitions it to `running`.
6. Daemon reports `completed` or `failed` back to the server.
7. `GetUpdate` reads the durable row from the database.
8. Timeout behavior is resolved from durable timestamps, not only from in-memory state.

## 9. Middleware and router behavior

### 9.1 Router

Update `server/cmd/server/router.go` so the `/api/daemon` route group uses daemon-only middleware.

This is the external boundary correction and should be obvious in the route wiring.

### 9.2 Daemon auth middleware

Update `server/internal/middleware/daemon_auth.go` so it:

- accepts only `mdt_` daemon tokens
- rejects PAT and JWT tokens without fallback
- stores daemon/workspace identity in context
- does not set user headers as a substitute identity model

This middleware should define the control-plane contract clearly enough that handlers do not need to reinterpret token classes.

## 10. Daemon task endpoint audit requirements

This phase should explicitly review daemon task endpoints in `server/internal/handler/daemon.go` that operate on:

- task status reads
- task start
- task progress
- task complete/fail
- task usage
- task messages

The audit standard is:

- a daemon may only act on tasks belonging to its authorized runtime/workspace boundary
- any endpoint currently relying only on globally supplied IDs must be tightened
- if a concrete ownership bypass is found during implementation, it should be treated as part of this phase rather than deferred

## 11. Files expected to change

### Create

- new migrations for `runtime_ping_request`
- new migrations for `runtime_update_request`
- SQL query files for durable ping/update lifecycle operations

### Modify

- `server/cmd/server/router.go`
- `server/internal/middleware/daemon_auth.go`
- `server/internal/handler/runtime_ping.go`
- `server/internal/handler/runtime_update.go`
- `server/internal/handler/daemon.go`
- sqlc generated files under `server/pkg/db/generated/`
- relevant backend tests in handler, middleware, and integration locations

## 12. Testing strategy

### 12.1 Auth boundary tests

Add focused tests proving:

- daemon routes reject JWT user tokens
- daemon routes reject PAT tokens
- daemon routes accept valid daemon tokens
- daemon middleware produces daemon/workspace context rather than user identity semantics

### 12.2 Durable request tests

Add focused tests proving:

- `InitiatePing` creates a durable pending request
- `GetPing` reads durable state
- `ReportPingResult` updates only authorized requests
- update flow behaves equivalently
- one-runtime-at-a-time update constraints hold
- timeout behavior remains visible after restart-safe reads

### 12.3 Ownership audit tests

Add targeted tests for daemon task endpoints showing:

- access succeeds inside the correct runtime/daemon boundary
- access fails outside the correct boundary

### 12.4 Regression scope

This phase should run targeted backend verification during development and preserve existing auth/browser golden paths.

It does not need to widen the entire E2E surface as part of the same implementation wave.

## 13. Recommended implementation order

1. Rewire daemon routes to daemon-only middleware.
2. Remove PAT/JWT fallback from daemon auth middleware.
3. Introduce durable ping/update request tables and sqlc queries.
4. Refactor ping/update handlers to use durable request records.
5. Tighten result-reporting and pending-claim flows around runtime/daemon ownership.
6. Audit daemon task endpoints for the same ownership model.
7. Add and run focused backend tests.

## 14. Rationale

This wave is the right next move because it closes the two clearest P0 commercialization blockers without turning into a broad platform rewrite.

It improves the system at the exact point where commercial trust breaks first:

- who is allowed to operate the daemon control-plane
- whether runtime control operations remain trustworthy after restart or incident conditions

If those two boundaries are not sound, broader token hygiene, observability expansion, or larger test suites do not materially change launch readiness.