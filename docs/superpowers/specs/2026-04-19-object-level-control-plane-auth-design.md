# Object-Level Control-Plane Auth Design

Date: 2026-04-19
Status: Proposed
Scope: Object-level authorization hardening for runtime request reads and issue task control-plane endpoints

## Summary

Multica has already hardened daemon-side ownership checks for persisted runtime requests and daemon task callbacks. The remaining high-value gap is on the frontend control plane: several authenticated endpoints still resolve objects by global IDs without fully proving that the requested object belongs to the parent resource in the URL and to the caller's current workspace.

This design closes that gap for the smallest high-value set of endpoints:

- `GetPing`
- `GetUpdate`
- `GetActiveTaskForIssue`
- `ListTasksByIssue`
- `CancelTask`

The design does not introduce a new auth model. It applies one consistent rule to existing handlers: first prove the parent resource belongs to the caller's workspace, then prove the child object belongs to that parent resource, and return `404` when that proof fails.

## Goals

- Eliminate object-level read/write leaks caused by global-ID lookups on the control plane
- Ensure runtime request reads are scoped to both the URL runtime and the caller's workspace
- Ensure issue task reads/cancel actions are scoped to both the URL issue and the caller's workspace
- Use a consistent, minimal-leakage error model for object mismatches
- Keep the implementation limited to handler/query/test changes without widening into a larger auth rewrite

## Non-Goals

- Reworking daemon-side auth or daemon callback flows
- Changing database schema or adding migrations
- Extending the work to runtime usage/activity endpoints in the same round
- Rewriting task orchestration service behavior
- Performing a repository-wide control-plane auth normalization beyond the five targeted endpoints

## Success Criteria

This design is successful when all of the following are true:

- `GetPing` only returns a ping request if it belongs to both the URL `runtimeId` and the caller's workspace
- `GetUpdate` only returns an update request if it belongs to both the URL `runtimeId` and the caller's workspace
- `GetActiveTaskForIssue` and `ListTasksByIssue` only return tasks for an issue in the caller's workspace
- `CancelTask` only cancels a task if that task belongs to the URL issue in the caller's workspace
- All object mismatches for these endpoints resolve to deterministic `404` responses instead of leaking object existence

## Current Risk

### Runtime request reads are globally addressed

`GetPing` and `GetUpdate` currently load requests by global request ID, without binding that request to the URL runtime.

- `runtime_ping.go` resolves `pingId` and calls a global `GetRuntimePing(...)`
- `runtime_update.go` resolves `updateId` and calls a global `GetRuntimeUpdate(...)`
- The route shape already includes `runtimeId`, but that parent-child relationship is not enforced during the read

This means a caller who knows a valid ping/update ID can potentially read request data outside the intended runtime or workspace boundary.

### Issue task endpoints rely too much on path shape

The issue-facing task endpoints live under issue routes, but the handlers do not consistently prove that the task they operate on actually belongs to the URL issue and workspace.

This creates the classic object-level authorization risk:

- the caller is authenticated
- the endpoint shape looks scoped
- but the underlying object lookup is still too global

## Design Principles

## Rule 1: Validate the parent resource first

Before reading or mutating a child object, the handler must first prove that the parent resource from the URL belongs to the caller's current workspace.

- runtime routes validate the runtime
- issue routes validate the issue

## Rule 2: Validate that the child object belongs to the parent

Once the parent resource is validated, the child object lookup must include the parent identifier.

Examples:

- ping must belong to the URL runtime
- update must belong to the URL runtime
- task must belong to the URL issue

## Rule 3: Object mismatches return `404`

If the child object does not belong to the validated parent/workspace scope, the handler returns `404`.

This includes cases where:

- the object does not exist
- the object exists but belongs to another runtime
- the object exists but belongs to another issue
- the object exists but belongs to another workspace

This minimizes object-existence leakage and keeps the semantics consistent.

## Endpoint Design

### 1. `GetPing`

Current route:

`GET /api/runtimes/{runtimeId}/ping/{pingId}`

Required flow:

1. Load `runtimeId`
2. Verify the runtime exists
3. Verify the caller is a member of that runtime's workspace
4. Load the ping request using:
   - `pingId`
   - `runtimeId`
   - `workspace_id`
5. If not found, return `404 ping not found`

Required new query:

```sql
-- name: GetRuntimePingForWorkspace :one
SELECT * FROM runtime_ping
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3;
```

### 2. `GetUpdate`

Current route:

`GET /api/runtimes/{runtimeId}/update/{updateId}`

Required flow:

1. Load `runtimeId`
2. Verify the runtime exists
3. Verify the caller is a member of that runtime's workspace
4. Load the update request using:
   - `updateId`
   - `runtimeId`
   - `workspace_id`
5. If not found, return `404 update not found`

Required new query:

```sql
-- name: GetRuntimeUpdateForWorkspace :one
SELECT * FROM runtime_update
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3;
```

### 3. `GetActiveTaskForIssue`

Current route shape is already issue-scoped, but the handler must explicitly prove issue/workspace ownership before returning task data.

Required flow:

1. Load `issueId`
2. Verify the issue exists
3. Verify the caller is a member of that issue's workspace
4. Only then fetch the active task for that issue
5. If issue validation fails, return `404 issue not found`

This endpoint may continue using issue-scoped list/read logic as long as the issue ownership check happens first.

### 4. `ListTasksByIssue`

Required flow:

1. Load `issueId`
2. Verify the issue exists
3. Verify the caller is a member of that issue's workspace
4. Only then list tasks for that issue
5. If issue validation fails, return `404 issue not found`

### 5. `CancelTask`

Current risk is highest here because the mutation uses `taskId` and can otherwise act on the wrong object.

Required flow:

1. Load `issueId`
2. Verify the issue exists
3. Verify the caller is a member of that issue's workspace
4. Load the task using:
   - `taskId`
   - `issueId`
   - `workspace_id`
5. If no matching task exists, return `404 task not found`
6. Only then invoke cancellation

Required new query:

```sql
-- name: GetTaskByIssueForWorkspace :one
SELECT atq.*
FROM agent_task_queue atq
JOIN issue i ON i.id = atq.issue_id
WHERE atq.id = $1
  AND atq.issue_id = $2
  AND i.workspace_id = $3;
```

## SQL Strategy

## Runtime request queries

Add only the two read queries needed for frontend control-plane scoping:

- `GetRuntimePingForWorkspace`
- `GetRuntimeUpdateForWorkspace`

Do not change the existing daemon-scoped queries introduced in the prior runtime request daemon binding work.

## Issue task query

Add one task lookup query for issue/workspace-scoped mutations:

- `GetTaskByIssueForWorkspace`

For `GetActiveTaskForIssue` and `ListTasksByIssue`, prefer to reuse existing issue-scoped task queries after validating issue ownership, rather than introducing redundant SQL unless implementation reveals a concrete need.

## Error Semantics

Use these exact object-level error responses:

- `404 ping not found`
- `404 update not found`
- `404 issue not found`
- `404 task not found`

These responses apply both when the object truly does not exist and when it exists outside the validated parent/workspace scope.

Do not return `403` for these object mismatches in this design. The goal is to avoid confirming object existence across tenancy boundaries.

## Testing Strategy

## Runtime request tests

Required tests:

1. `GetPing` returns the request when it belongs to the URL runtime in the caller's workspace
2. `GetPing` returns `404` when the ping exists but belongs to a different runtime
3. `GetUpdate` returns the request when it belongs to the URL runtime in the caller's workspace
4. `GetUpdate` returns `404` when the update exists but belongs to a different runtime

Recommended additional tests:

5. `GetPing` returns `404` when the request belongs to a different workspace
6. `GetUpdate` returns `404` when the request belongs to a different workspace

## Issue task tests

Required tests:

1. `GetActiveTaskForIssue` only returns an active task for the validated issue
2. `ListTasksByIssue` only returns tasks for the validated issue
3. `CancelTask` succeeds for a task that belongs to the URL issue in the caller's workspace
4. `CancelTask` returns `404` for a task that belongs to a different issue

Recommended additional tests:

5. issue-scoped task endpoints return `404` when the issue is outside the caller's workspace

## Trade-offs

## Why not only fix `GetPing` / `GetUpdate`

That would close the most obvious request-read leak quickly, but it would leave the same object-level auth pattern unresolved on issue-facing task endpoints. Since those task endpoints are in the same risk class and still fall within a small handler/query/test-only slice, it is more efficient to fix them together.

## Why not expand further in this round

There are likely other endpoints that would benefit from the same pattern, but expanding now would turn a clear high-value auth hardening pass into a broader control-plane rewrite. The current design deliberately stops at the five endpoints with the clearest evidence and best risk/reward ratio.

## Final Recommendation

Implement one focused round of control-plane object-level auth hardening that covers exactly these endpoints:

- `GetPing`
- `GetUpdate`
- `GetActiveTaskForIssue`
- `ListTasksByIssue`
- `CancelTask`

Use parent-resource validation first, then child-object scoping, and normalize all object mismatches to `404`.

This gives Multica a clean, commercially meaningful improvement in multi-tenant control-plane safety without dragging the work into a larger auth redesign.
