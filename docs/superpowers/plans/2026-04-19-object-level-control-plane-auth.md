# Object-Level Control-Plane Auth Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the highest-value frontend control-plane object-authorization gaps by scoping runtime request reads and issue task endpoints to both their parent resource and the caller’s workspace.

**Architecture:** Keep the existing auth model and endpoint shapes, but change handler flow to validate the parent resource first and then load the child object through parent-scoped SQL. Runtime request reads get new workspace-scoped queries, while issue task handlers reuse issue ownership validation plus one new task-by-issue-and-workspace lookup for cancellation.

**Tech Stack:** Go, PostgreSQL SQL files, sqlc-generated queries, Chi handlers, Go test

---

## File Structure

**Modify:**
- `server/internal/handler/runtime_ping.go` — make `GetPing` validate the runtime/workspace and load ping requests through a runtime/workspace-scoped query
- `server/internal/handler/runtime_update.go` — same pattern for `GetUpdate`
- `server/internal/handler/daemon.go` — make `GetActiveTaskForIssue`, `ListTasksByIssue`, and `CancelTask` validate issue ownership before returning or mutating tasks
- `server/pkg/db/queries/runtime_request.sql` — add `GetRuntimePingForWorkspace` and `GetRuntimeUpdateForWorkspace`
- `server/pkg/db/queries/agent.sql` — add `GetTaskByIssueForWorkspace`
- `server/pkg/db/generated/runtime_request.sql.go` — regenerated sqlc output for runtime request query additions
- `server/pkg/db/generated/agent.sql.go` — regenerated sqlc output for task lookup query addition
- `server/internal/handler/handler_test.go` — add handler-level tests for runtime request object auth and issue task object auth

**Reference:**
- `docs/superpowers/specs/2026-04-19-object-level-control-plane-auth-design.md` — approved design
- `server/cmd/server/router.go:247-255` — runtime route shapes that already carry `runtimeId`
- `server/internal/handler/daemon.go:691-740` — current issue task control-plane handlers
- `server/internal/handler/runtime_ping.go:174-189` — current global-ID `GetPing`
- `server/internal/handler/runtime_update.go:181-196` — current global-ID `GetUpdate`

---

### Task 1: Scope runtime request reads to runtime and workspace

**Files:**
- Modify: `server/pkg/db/queries/runtime_request.sql`
- Modify: `server/pkg/db/generated/runtime_request.sql.go`
- Modify: `server/internal/handler/runtime_ping.go`
- Modify: `server/internal/handler/runtime_update.go`
- Test: `server/internal/handler/handler_test.go`

- [ ] **Step 1: Write the failing runtime request read tests**

Add these tests to `server/internal/handler/handler_test.go` before changing any SQL or handler code:

```go
func TestGetPingReturnsRequestForMatchingRuntimeAndWorkspace(t *testing.T) {
	ctx := context.Background()
	runtimeID := mustGetHandlerTestRuntimeID(t)
	if _, err := testPool.Exec(ctx, `DELETE FROM runtime_ping WHERE runtime_id = $1`, runtimeID); err != nil {
		t.Fatalf("cleanup runtime_ping: %v", err)
	}
	t.Cleanup(func() {
		if _, err := testPool.Exec(ctx, `DELETE FROM runtime_ping WHERE runtime_id = $1`, runtimeID); err != nil {
			t.Fatalf("cleanup runtime_ping: %v", err)
		}
	})

	createW := httptest.NewRecorder()
	createReq := withURLParam(newRequest("POST", "/api/runtimes/"+runtimeID+"/ping", nil), "runtimeId", runtimeID)
	testHandler.InitiatePing(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiatePing: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var created PingRequest
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode created ping: %v", err)
	}

	getW := httptest.NewRecorder()
	getReq := newRequest("GET", "/api/runtimes/"+runtimeID+"/ping/"+created.ID, nil)
	getReq = withURLParam(getReq, "runtimeId", runtimeID)
	getReq = withURLParam(getReq, "pingId", created.ID)
	testHandler.GetPing(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GetPing: expected 200, got %d: %s", getW.Code, getW.Body.String())
	}
}

func TestGetPingReturnsNotFoundForRequestFromDifferentRuntime(t *testing.T) {
	ctx := context.Background()
	runtimeID := mustGetHandlerTestRuntimeID(t)
	otherRuntimeID := createRuntimeForWorkspaceAndDaemon(t, testWorkspaceID, "Other GetPing Runtime", handlerTestDaemonID)
	if _, err := testPool.Exec(ctx, `DELETE FROM runtime_ping WHERE runtime_id = $1 OR runtime_id = $2`, runtimeID, otherRuntimeID); err != nil {
		t.Fatalf("cleanup runtime_ping: %v", err)
	}
	t.Cleanup(func() {
		if _, err := testPool.Exec(ctx, `DELETE FROM runtime_ping WHERE runtime_id = $1 OR runtime_id = $2`, runtimeID, otherRuntimeID); err != nil {
			t.Fatalf("cleanup runtime_ping: %v", err)
		}
		if _, err := testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, otherRuntimeID); err != nil {
			t.Fatalf("cleanup other runtime: %v", err)
		}
	})

	createW := httptest.NewRecorder()
	createReq := withURLParam(newRequest("POST", "/api/runtimes/"+otherRuntimeID+"/ping", nil), "runtimeId", otherRuntimeID)
	testHandler.InitiatePing(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiatePing: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var created PingRequest
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode created ping: %v", err)
	}

	getW := httptest.NewRecorder()
	getReq := newRequest("GET", "/api/runtimes/"+runtimeID+"/ping/"+created.ID, nil)
	getReq = withURLParam(getReq, "runtimeId", runtimeID)
	getReq = withURLParam(getReq, "pingId", created.ID)
	testHandler.GetPing(getW, getReq)
	if getW.Code != http.StatusNotFound {
		t.Fatalf("GetPing: expected 404, got %d: %s", getW.Code, getW.Body.String())
	}
}
```

Add the same shape for updates:

```go
func TestGetUpdateReturnsRequestForMatchingRuntimeAndWorkspace(t *testing.T) { /* analogous to ping */ }
func TestGetUpdateReturnsNotFoundForRequestFromDifferentRuntime(t *testing.T) { /* analogous to ping */ }
```

- [ ] **Step 2: Run the focused tests to verify they fail for the right reason**

Run:

```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestGetPingReturnsRequestForMatchingRuntimeAndWorkspace|TestGetPingReturnsNotFoundForRequestFromDifferentRuntime|TestGetUpdateReturnsRequestForMatchingRuntimeAndWorkspace|TestGetUpdateReturnsNotFoundForRequestFromDifferentRuntime' -v
```

Expected: FAIL because `GetPing` / `GetUpdate` still load requests by global ID only and ignore the URL `runtimeId`.

- [ ] **Step 3: Add the workspace-scoped runtime request SQL**

Update `server/pkg/db/queries/runtime_request.sql` by adding these exact queries after the existing global `GetRuntimePing` / `GetRuntimeUpdate` queries:

```sql
-- name: GetRuntimePingForWorkspace :one
SELECT * FROM runtime_ping
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3;

-- name: GetRuntimeUpdateForWorkspace :one
SELECT * FROM runtime_update
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3;
```

Then regenerate SQL accessors:

```bash
cd /Users/a1234/multica/server && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate
```

Expected: `server/pkg/db/generated/runtime_request.sql.go` now contains `GetRuntimePingForWorkspace` and `GetRuntimeUpdateForWorkspace`.

- [ ] **Step 4: Implement minimal `GetPing` / `GetUpdate` scoping in handlers**

In `server/internal/handler/runtime_ping.go`, add a workspace-scoped frontend helper and use it from `GetPing`:

```go
func (h *Handler) getPingRequestForWorkspace(ctx context.Context, pingID, runtimeID, workspaceID string) (*PingRequest, error) {
	ping, err := h.Queries.GetRuntimePingForWorkspace(ctx, db.GetRuntimePingForWorkspaceParams{
		ID:          parseUUID(pingID),
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		return nil, err
	}
	result := runtimePingToRequest(ping)
	return &result, nil
}
```

Update `GetPing` to:

```go
runtimeID := chi.URLParam(r, "runtimeId")
rt, err := h.Queries.GetAgentRuntime(r.Context(), parseUUID(runtimeID))
if err != nil {
	writeError(w, http.StatusNotFound, "runtime not found")
	return
}
if _, ok := h.requireWorkspaceMember(w, r, uuidToString(rt.WorkspaceID), "runtime not found"); !ok {
	return
}

ping, err := h.getPingRequestForWorkspace(r.Context(), pingID, runtimeID, uuidToString(rt.WorkspaceID))
```

In `server/internal/handler/runtime_update.go`, add the matching helper and update `GetUpdate` in the same way:

```go
func (h *Handler) getUpdateRequestForWorkspace(ctx context.Context, updateID, runtimeID, workspaceID string) (*UpdateRequest, error) {
	update, err := h.Queries.GetRuntimeUpdateForWorkspace(ctx, db.GetRuntimeUpdateForWorkspaceParams{
		ID:          parseUUID(updateID),
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		return nil, err
	}
	result := runtimeUpdateToRequest(update)
	return &result, nil
}
```

Use the same parent-resource validation flow as `GetPing`.

- [ ] **Step 5: Run the focused tests to verify they pass**

Run:

```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestGetPingReturnsRequestForMatchingRuntimeAndWorkspace|TestGetPingReturnsNotFoundForRequestFromDifferentRuntime|TestGetUpdateReturnsRequestForMatchingRuntimeAndWorkspace|TestGetUpdateReturnsNotFoundForRequestFromDifferentRuntime' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/a1234/multica/server && git add pkg/db/queries/runtime_request.sql pkg/db/generated/runtime_request.sql.go internal/handler/runtime_ping.go internal/handler/runtime_update.go internal/handler/handler_test.go && git commit -m "fix(handler): scope runtime request reads to parent runtime"
```

### Task 2: Scope issue task endpoints to issue and workspace

**Files:**
- Modify: `server/pkg/db/queries/agent.sql`
- Modify: `server/pkg/db/generated/agent.sql.go`
- Modify: `server/internal/handler/daemon.go`
- Test: `server/internal/handler/handler_test.go`

- [ ] **Step 1: Write the failing issue task object-auth tests**

Add these tests to `server/internal/handler/handler_test.go` before changing SQL/handlers:

```go
func TestCancelTaskReturnsNotFoundForTaskFromDifferentIssue(t *testing.T) {
	ctx := context.Background()

	issueA := createIssueForTaskTest(t, ctx, "Issue A")
	issueB := createIssueForTaskTest(t, ctx, "Issue B")
	taskID := createQueuedTaskForIssueTest(t, ctx, issueB)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueA+"/tasks/"+taskID+"/cancel", nil)
	req = withURLParam(req, "id", issueA)
	req = withURLParam(req, "taskId", taskID)
	testHandler.CancelTask(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("CancelTask: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListTasksByIssueRequiresIssueWorkspaceAccess(t *testing.T) {
	ctx := context.Background()
	otherWorkspaceID := createWorkspaceForTaskAuthTest(t, ctx)
	issueID := createIssueInWorkspaceForTaskTest(t, ctx, otherWorkspaceID, "Foreign issue")
	createQueuedTaskForIssueInWorkspaceTest(t, ctx, otherWorkspaceID, issueID)

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+issueID+"/tasks", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.ListTasksByIssue(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ListTasksByIssue: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
```

Add matching tests for active task reads:

```go
func TestGetActiveTaskForIssueRequiresIssueWorkspaceAccess(t *testing.T) { /* same access pattern */ }
func TestGetActiveTaskForIssueOnlyReturnsTasksForRequestedIssue(t *testing.T) { /* ensure no leakage across issues */ }
```

If the helper fixtures above do not already exist, create the smallest local helper functions inside `handler_test.go` that:
- insert an issue in the existing workspace
- insert an issue in another workspace
- insert a queued/dispatched/running task for a given issue

Keep the helpers local to the tests added in this task.

- [ ] **Step 2: Run the focused tests to verify they fail correctly**

Run:

```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestCancelTaskReturnsNotFoundForTaskFromDifferentIssue|TestListTasksByIssueRequiresIssueWorkspaceAccess|TestGetActiveTaskForIssueRequiresIssueWorkspaceAccess|TestGetActiveTaskForIssueOnlyReturnsTasksForRequestedIssue' -v
```

Expected: FAIL because the current handlers do not validate issue ownership before listing/cancelling tasks, and `CancelTask` still acts on `taskId` alone.

- [ ] **Step 3: Add the issue/workspace-scoped task lookup query**

Update `server/pkg/db/queries/agent.sql` by adding this exact query after `CancelAgentTask`:

```sql
-- name: GetTaskByIssueForWorkspace :one
SELECT atq.*
FROM agent_task_queue atq
JOIN issue i ON i.id = atq.issue_id
WHERE atq.id = $1
  AND atq.issue_id = $2
  AND i.workspace_id = $3;
```

Then regenerate SQL accessors:

```bash
cd /Users/a1234/multica/server && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate
```

Expected: `server/pkg/db/generated/agent.sql.go` now contains `GetTaskByIssueForWorkspace`.

- [ ] **Step 4: Implement minimal issue/task ownership checks in handlers**

In `server/internal/handler/daemon.go`, add a helper:

```go
func (h *Handler) getIssueForWorkspace(r *http.Request, issueID string) (*db.Issue, bool) {
	issue, err := h.Queries.GetIssue(r.Context(), parseUUID(issueID))
	if err != nil {
		return nil, false
	}
	if _, ok := h.requireWorkspaceMember(httptest.NewRecorder(), r, uuidToString(issue.WorkspaceID), "issue not found"); !ok {
		return nil, false
	}
	return &issue, true
}
```

Do **not** keep the `httptest.NewRecorder()` shape above; instead implement the real helper with `w http.ResponseWriter` and return early in handlers. The helper should use the existing `requireWorkspaceMember(..., "issue not found")` behavior.

Update `GetActiveTaskForIssue` to:
1. load the issue
2. require workspace membership with error text `issue not found`
3. only then call `ListActiveTasksByIssue`

Update `ListTasksByIssue` with the same issue validation first.

Update `CancelTask` to:
1. load the issue
2. require workspace membership with error text `issue not found`
3. use `GetTaskByIssueForWorkspace` with:
   - `taskId`
   - `issueId`
   - `issue.WorkspaceID`
4. if not found, return `404 task not found`
5. only then call `h.TaskService.CancelTask(...)`

- [ ] **Step 5: Run the focused tests to verify they pass**

Run:

```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestCancelTaskReturnsNotFoundForTaskFromDifferentIssue|TestListTasksByIssueRequiresIssueWorkspaceAccess|TestGetActiveTaskForIssueRequiresIssueWorkspaceAccess|TestGetActiveTaskForIssueOnlyReturnsTasksForRequestedIssue' -v
```

Expected: PASS.

- [ ] **Step 6: Run the relevant existing package tests**

Run:

```bash
cd /Users/a1234/multica/server && go test ./internal/handler
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /Users/a1234/multica/server && git add pkg/db/queries/agent.sql pkg/db/generated/agent.sql.go internal/handler/daemon.go internal/handler/handler_test.go && git commit -m "fix(handler): enforce object auth on issue task endpoints"
```
