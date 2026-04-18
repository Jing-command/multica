# Runtime Request Daemon Binding Design

Date: 2026-04-18
Status: Proposed
Scope: Commercial-readiness hardening for persisted runtime ping/update requests

## Summary

Multica already hardened daemon control-plane authentication and runtime/task callback scope checks, and it already persists runtime ping/update requests in the database. The remaining gap is that persisted runtime requests are still keyed only by `runtime_id` plus request ID, while the daemon ownership boundary is enforced mainly in handler logic.

This design closes that gap without adopting the heavier request-table redesign from `Jing-command/multica#8`.

The recommended approach is to keep the existing `runtime_ping` and `runtime_update` tables, but extend each row with the minimum daemon ownership context needed for commercially safe operation:

- `workspace_id`
- `daemon_id`

That allows daemon request claim/read/write paths to enforce ownership using both handler-level checks and data-level request binding, without introducing a brand-new request model.

## Goals

- Preserve the existing `runtime_ping` / `runtime_update` persistence model
- Bind each persisted runtime request to the daemon ownership context active at creation time
- Prevent one daemon from claiming, reading, timing out, or writing results for another daemon's request
- Keep the current daemon control-plane and runtime/task scope model intact
- Reduce real commercial risk without introducing a large schema/model rewrite

## Non-Goals

- Replacing `runtime_ping` / `runtime_update` with `runtime_ping_request` / `runtime_update_request`
- Reworking the whole runtime request lifecycle around a new abstraction
- Automatically transferring existing requests when a runtime is re-bound to a different daemon
- Maximizing theoretical schema purity at the cost of migration and implementation complexity

## Success Criteria

The design is successful when all of the following are true:

- Every persisted ping/update request records which workspace and daemon owned the target runtime when the request was created
- A daemon cannot claim or update a request unless the request matches its authenticated `(workspace_id, daemon_id)` context
- Runtime request ownership remains stable even if the runtime is later re-registered under a different daemon
- Existing frontend and daemon APIs remain recognizable and do not require a new request model
- The implementation can be added incrementally on top of the current `main` branch

## Current State

### What is already strong

The current `main` branch already has strong handler-level daemon ownership enforcement:

- `requireDaemonRuntimeScope(...)` validates `(runtime_id, workspace_id, daemon_id)` before daemon runtime actions
- `requireDaemonTaskScope(...)` validates `(task_id, workspace_id, daemon_id)` before daemon task actions
- `/api/daemon/*` routes already run on strict daemon tokens

This means the primary unresolved issue is not daemon auth itself. The unresolved issue is that persisted runtime requests do not carry enough ownership context of their own.

### What is currently weak

The current request persistence model stores:

- `runtime_ping.runtime_id`
- `runtime_update.runtime_id`

but not:

- `workspace_id`
- `daemon_id`

As a result, request ownership is inferred indirectly:

1. validate the runtime via handler-level daemon scope checks
2. load the persisted request by ID or runtime
3. compare the request's runtime back to the authenticated daemon context

This is workable, but it leaves the request record itself under-specified for a commercially hardened system.

## Recommended Architecture

## Core Decision

Keep the existing request tables and add the minimum ownership context required for robust daemon binding.

### Tables to keep

- `runtime_ping`
- `runtime_update`

### Columns to add

Both tables gain:

- `workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE`
- `daemon_id TEXT NOT NULL`

These values are copied from the target runtime at request creation time.

### Why this is the recommended middle path

This approach is stronger than the current design because the request row itself carries daemon ownership context.

It is lighter than PR #8's redesign because it does not:

- introduce new request tables
- require a new conceptual request model
- require a full rewrite of ping/update persistence logic

For commercial readiness, this gives meaningful risk reduction with lower implementation and migration cost.

## Ownership Model

## Binding rule

A persisted runtime request belongs to the daemon ownership context active when the request was created.

For every request row, ownership is defined by:

- `runtime_id`
- `workspace_id`
- `daemon_id`

### Stability rule

Ownership is creation-time stable.

If a runtime is later re-registered under a different daemon:

- existing request rows do **not** change ownership
- the new daemon does **not** automatically inherit those old requests
- only requests created after the re-binding belong to the new daemon

This preserves auditability and avoids surprising request transfer across daemon instances.

## Data Flow

### 1. Frontend creates a ping request

`InitiatePing` flow:

1. Read the target runtime by `runtime_id`
2. Verify the caller can access the runtime's workspace
3. Verify the runtime currently has a daemon binding
4. Create `runtime_ping` with:
   - `runtime_id`
   - `workspace_id`
   - `daemon_id`
   - `status = pending`

If the runtime has no daemon binding, the request is rejected instead of creating an unowned request.

### 2. Frontend creates an update request

`InitiateUpdate` follows the same rule:

1. Read the target runtime
2. Verify workspace access
3. Verify the runtime has a daemon binding
4. Create `runtime_update` with:
   - `runtime_id`
   - `workspace_id`
   - `daemon_id`
   - `target_version`
   - `status = pending`

### 3. Daemon heartbeat claims pending work

When a daemon heartbeats for a runtime, pending request pop logic must no longer filter only by `runtime_id`.

It must claim only rows matching the authenticated daemon ownership context:

- `runtime_id`
- `workspace_id`
- `daemon_id`
- `status = pending`

This ensures that a daemon cannot claim another daemon's persisted request, even if runtime IDs or request IDs are guessed or stale state exists elsewhere.

### 4. Daemon reports ping/update results

When the daemon reports a ping or update result, request read/write paths must also filter by the bound ownership context.

Required match set:

- `request_id`
- `runtime_id`
- `workspace_id`
- `daemon_id`

This should apply to:

- request load
- completion writes
- failure writes
- timeout transition writes

The handler-level runtime scope check remains in place as the first defense. The data-layer request binding becomes the second defense.

## Error Handling

## Request creation errors

### Runtime not found

Return `404`.

### User cannot access runtime workspace

Keep existing workspace membership behavior.

### Runtime has no daemon binding

Return a client-visible rejection rather than creating an orphaned request.

Recommended response:

- `409` with a clear message such as: `runtime is not currently attached to a daemon`

This is preferable to silently creating a request the daemon cannot ever consume.

## Daemon-side request access errors

### Invalid daemon token

Return `401`.

### Request exists but does not belong to the authenticated daemon context

For request-level read/write operations, return `404`.

Reasoning:

- request IDs are internal control-plane objects
- there is little value in revealing whether the request exists for another daemon
- `404` minimizes object-existence leakage

### Runtime/task ownership mismatches

Existing runtime/task ownership behavior stays as-is.

Current runtime/task scope mismatches already use `403`, and this design does not change that decision.

## Timeout Handling

Current timeout behavior remains:

- ping timeout window: 60 seconds
- update timeout window: 120 seconds

The only change is that timeout updates must also be ownership-scoped through the request row's bound `(runtime_id, workspace_id, daemon_id)` context.

That prevents one daemon from timing out another daemon's request as an unintended side effect.

## Database and Query Design

## Schema changes

### `runtime_ping`

Add:

- `workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE`
- `daemon_id TEXT NOT NULL`

Recommended indexes:

- `(runtime_id, workspace_id, daemon_id, created_at)` for claim order

Do not add an extra pending-only partial index in the first implementation. If claim performance later regresses under measurement, that optimization can be proposed separately.

### `runtime_update`

Add:

- `workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE`
- `daemon_id TEXT NOT NULL`

Recommended indexes:

- `(runtime_id, workspace_id, daemon_id, created_at)`
- preserve the existing active-update uniqueness semantics for a runtime, unless implementation review shows they should be upgraded to include ownership context

## Query changes

The generated queries should be updated so daemon-side request operations are ownership-aware.

Examples of affected operations:

- create ping/update request
- pop pending ping/update request
- get ping/update request for daemon result handling
- set ping/update completed
- set ping/update failed
- set ping/update timeout

Frontend-side read operations may keep simpler lookup behavior if they are already protected by authenticated workspace access and do not cross daemon boundaries. The important hardening target is daemon-side claim/read/write.

## Migration Strategy

This should be implemented as an additive migration sequence:

1. add nullable `workspace_id` / `daemon_id` columns
2. backfill existing rows from `agent_runtime`
3. make columns `NOT NULL`
4. add indexes needed for ownership-scoped claim/read/write paths
5. switch code paths to the new ownership-aware queries

Backfill rule:

- for each existing request row, read the current `agent_runtime` referenced by `runtime_id`
- copy its `workspace_id` and `daemon_id` into the request row

If any existing row points to a runtime without daemon binding, the migration should fail loudly or explicitly clean/resolve those rows during the migration plan. This must not be silently ignored.

## Testing Strategy

## Required tests

### 1. Request creation binds ownership context

For both ping and update:

- create request against a runtime
- verify persisted row stores the expected `workspace_id` and `daemon_id`

### 2. Cross-daemon claim rejection

- same workspace
- two daemon identities
- daemon A must not pop daemon B's request

### 3. Cross-workspace rejection

- reuse the same daemon ID string across different workspaces
- verify a daemon still cannot claim/read/write requests outside its authenticated workspace scope

### 4. Result write protection

For both ping and update:

- use a valid request ID
- authenticate as the wrong daemon context
- verify completion/failure write does not succeed

### 5. Runtime re-binding behavior

- create request while runtime belongs to daemon A
- re-bind runtime to daemon B
- verify daemon B cannot consume daemon A's existing request
- verify new requests created after re-binding belong to daemon B

### 6. Timeout path protection

- ensure timeout transitions only affect requests owned by the authenticated daemon context

## Optional tests

- migration backfill tests if migration tooling already supports them cleanly
- performance-sensitive pending-request selection tests if claim queries become more complex

## Trade-offs

## Why not leave the current model alone

Leaving the current model untouched keeps request ownership implicit and overly dependent on handler discipline. That is acceptable for an early implementation, but it is weaker than needed for commercial-readiness hardening.

## Why not adopt PR #8 wholesale

PR #8's request-table redesign gives a stronger DB-first model, but it is a larger architectural fork from current `main` than is justified for this stage. It would increase migration scope, code churn, and merge risk while solving a problem that can be addressed with a smaller extension to the current model.

## Final Recommendation

Implement lightweight daemon ownership binding on top of the current request persistence model.

Concretely:

- keep `runtime_ping` and `runtime_update`
- add `workspace_id` and `daemon_id`
- bind ownership at creation time
- require daemon-side claim/read/write queries to match request ownership context
- keep existing handler-level runtime/task scope checks as defense in depth
- do not automatically transfer old requests across daemon re-binding

This is the best balance between security hardening, migration safety, implementation cost, and commercial-readiness value.
