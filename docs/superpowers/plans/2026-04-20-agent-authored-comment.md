# Agent-Authored Comment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a verified agent-authored comment path that restores real agent-thread semantics without reopening header-based actor spoofing.

**Architecture:** Keep `CreateComment` member-only and add a separate `CreateAgentComment` handler bound to verified `task -> agent -> issue -> workspace` context. Reuse small comment helpers for parent loading and insert mechanics, then move integration tests from spoofed `X-Agent-ID` roots to the new verified route.

**Tech Stack:** Go, Chi router, sqlc-generated queries, pgx/pgtype, Go test

---

## File Structure

**Modify:**
- `server/internal/handler/comment.go` — add the verified agent-comment handler plus small shared helpers for parent loading and comment insertion
- `server/cmd/server/router.go` — register the explicit `POST /api/issues/{id}/agent-comments` route
- `server/internal/handler/handler_test.go` — add focused handler tests for the new verified comment boundary
- `server/cmd/server/comment_trigger_integration_test.go` — replace spoof-based “agent thread” helpers with verified agent-comment helpers

**Reference:**
- `docs/superpowers/specs/2026-04-20-agent-authored-comment-design.md` — approved design
- `server/internal/handler/comment.go` — current member-only `CreateComment` path and on-comment logic
- `server/internal/handler/orchestration.go` — existing verified task-bound agent pattern via `resolveVerifiedAgentActorFromTask(...)`
- `server/internal/handler/orchestration_test.go` — reusable task-bound fixture data via `seedOrchestrationHandlerFixture(...)`
- `server/cmd/server/comment_trigger_integration_test.go` — current spoof-based `postCommentAsAgent(...)` helper that must be removed from integration semantics

---

### Task 1: Add the verified agent-comment handler

**Files:**
- Modify: `server/internal/handler/comment.go`
- Modify: `server/cmd/server/router.go`
- Test: `server/internal/handler/handler_test.go`

- [ ] **Step 1: Write the failing handler tests**

Add these focused tests to `server/internal/handler/handler_test.go` before changing production code:

```go
func TestCreateAgentCommentRequiresVerifiedTaskContext(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/agent-comments", map[string]any{
		"content": "agent output",
		"type":    "comment",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.workerAgentID)

	testHandler.CreateAgentComment(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateAgentCommentCreatesAgentAuthoredComment(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/agent-comments", map[string]any{
		"content": "Here is my analysis.",
		"type":    "comment",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.workerAgentID)
	req.Header.Set("X-Task-ID", fixture.workerChildTaskID)

	testHandler.CreateAgentComment(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var comment CommentResponse
	if err := json.NewDecoder(w.Body).Decode(&comment); err != nil {
		t.Fatalf("decode comment: %v", err)
	}
	if comment.AuthorType != "agent" {
		t.Fatalf("author_type = %q, want agent", comment.AuthorType)
	}
	if comment.AuthorID != fixture.workerAgentID {
		t.Fatalf("author_id = %q, want %q", comment.AuthorID, fixture.workerAgentID)
	}
}

func TestCreateAgentCommentDoesNotEnqueueOnComment(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	var before int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, fixture.childIssueID).Scan(&before); err != nil {
		t.Fatalf("count tasks before comment: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/agent-comments", map[string]any{
		"content": "Verified agent output",
		"type":    "comment",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.workerAgentID)
	req.Header.Set("X-Task-ID", fixture.workerChildTaskID)

	testHandler.CreateAgentComment(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var after int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, fixture.childIssueID).Scan(&after); err != nil {
		t.Fatalf("count tasks after comment: %v", err)
	}
	if after != before {
		t.Fatalf("task count changed from %d to %d; agent-authored comments must not enqueue on_comment", before, after)
	}
}
```

- [ ] **Step 2: Run the focused handler tests to verify they fail**

Run:

```bash
cd "/Users/a1234/multica/.claude/worktrees/obj-auth-task1/server" && go test ./internal/handler -run 'TestCreateAgentCommentRequiresVerifiedTaskContext|TestCreateAgentCommentCreatesAgentAuthoredComment|TestCreateAgentCommentDoesNotEnqueueOnComment' -v
```

Expected: FAIL with `testHandler.CreateAgentComment undefined`.

- [ ] **Step 3: Implement the verified agent-comment path**

In `server/internal/handler/comment.go`, add a small shared helper set and the new handler. Keep `CreateComment` member-only.

Add a parent loader that scopes the parent to the same issue:

```go
func (h *Handler) loadCommentParent(ctx context.Context, issueID string, parentID *string) (pgtype.UUID, *db.Comment, error) {
	if parentID == nil {
		return pgtype.UUID{}, nil, nil
	}
	parsed := parseUUID(*parentID)
	parent, err := h.Queries.GetComment(ctx, parsed)
	if err != nil || uuidToString(parent.IssueID) != issueID {
		return pgtype.UUID{}, nil, errors.New("invalid parent comment")
	}
	return parsed, &parent, nil
}
```

Add a shared insert helper so `CreateComment` and `CreateAgentComment` do not duplicate write logic:

```go
func (h *Handler) createCommentWithActor(ctx context.Context, issue db.Issue, req CreateCommentRequest, actorType, actorID string) (db.Comment, *db.Comment, error) {
	parentID, parentComment, err := h.loadCommentParent(ctx, uuidToString(issue.ID), req.ParentID)
	if err != nil {
		return db.Comment{}, nil, err
	}

	content := mention.ExpandIssueIdentifiers(ctx, h.Queries, issue.WorkspaceID, req.Content)
	comment, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  actorType,
		AuthorID:    parseUUID(actorID),
		Content:     content,
		Type:        req.Type,
		ParentID:    parentID,
	})
	if err != nil {
		return db.Comment{}, nil, err
	}
	return comment, parentComment, nil
}
```

Add a verified agent resolver dedicated to comment creation:

```go
func (h *Handler) requireVerifiedAgentCommentActor(w http.ResponseWriter, r *http.Request, issue db.Issue) (string, bool) {
	taskID := r.Header.Get("X-Task-ID")
	agentID := r.Header.Get("X-Agent-ID")
	if taskID == "" || agentID == "" {
		writeError(w, http.StatusBadRequest, "X-Agent-ID and X-Task-ID are required")
		return "", false
	}

	actorType, actorID, ok := h.resolveVerifiedAgentActorFromTask(r.Context(), taskID, agentID, uuidToString(issue.WorkspaceID))
	if !ok || actorType != "agent" {
		writeError(w, http.StatusForbidden, "verified agent comment context required")
		return "", false
	}

	task, err := h.Queries.GetAgentTask(r.Context(), parseUUID(taskID))
	if err != nil || uuidToString(task.IssueID) != uuidToString(issue.ID) {
		writeError(w, http.StatusForbidden, "verified agent comment context required")
		return "", false
	}
	return actorID, true
}
```

Add the handler itself:

```go
func (h *Handler) CreateAgentComment(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	if _, ok := requireUserID(w, r); !ok {
		return
	}

	var req CreateCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if req.Type == "" {
		req.Type = "comment"
	}

	agentID, ok := h.requireVerifiedAgentCommentActor(w, r, issue)
	if !ok {
		return
	}

	comment, _, err := h.createCommentWithActor(r.Context(), issue, req, "agent", agentID)
	if err != nil {
		if err.Error() == "invalid parent comment" {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}

	groupedAtt := h.groupAttachments(r, []pgtype.UUID{comment.ID})
	resp := commentToResponse(comment, nil, groupedAtt[uuidToString(comment.ID)])
	h.publish(protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), "agent", agentID, map[string]any{
		"comment":             resp,
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
	})
	writeJSON(w, http.StatusCreated, resp)
}
```

In the existing `CreateComment`, replace the inline parent-load/insert block with the shared helper while keeping member-only actor resolution:

```go
authorType, authorID := resolveMemberActor(userID)
comment, parentComment, err := h.createCommentWithActor(r.Context(), issue, req, authorType, authorID)
if err != nil {
	if err.Error() == "invalid parent comment" {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to create comment")
	return
}
```

In `server/cmd/server/router.go`, register the explicit route next to the existing comment route:

```go
r.Route("/api/issues", func(r chi.Router) {
	...
	r.Route("/{id}", func(r chi.Router) {
		...
		r.Post("/comments", h.CreateComment)
		r.Post("/agent-comments", h.CreateAgentComment)
		...
	})
})
```

- [ ] **Step 4: Run the focused handler tests to verify they pass**

Run:

```bash
cd "/Users/a1234/multica/.claude/worktrees/obj-auth-task1/server" && go test ./internal/handler -run 'TestCreateAgentCommentRequiresVerifiedTaskContext|TestCreateAgentCommentCreatesAgentAuthoredComment|TestCreateAgentCommentDoesNotEnqueueOnComment' -v
```

Expected: PASS.

- [ ] **Step 5: Run the handler regression slice**

Run:

```bash
cd "/Users/a1234/multica/.claude/worktrees/obj-auth-task1/server" && go test ./internal/handler -run 'Test(CreateAgentComment|CreateCommentIgnoresSpoofedAgentHeaders|SpoofedAgentCommentDoesNotSuppressOnCommentTrigger|RequireWorkflowAgentAcceptsVerifiedTaskBoundAgentContext|RequireWorkflowAgentRejectsHeaderOnlyAgentContextWithoutVerifiedTask)' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git -C "/Users/a1234/multica/.claude/worktrees/obj-auth-task1" add server/internal/handler/comment.go server/cmd/server/router.go server/internal/handler/handler_test.go && git -C "/Users/a1234/multica/.claude/worktrees/obj-auth-task1" commit -m "feat(comment): add verified agent comment route"
```

### Task 2: Move integration tests to real agent-thread roots

**Files:**
- Modify: `server/cmd/server/comment_trigger_integration_test.go`

- [ ] **Step 1: Rewrite the integration tests to call a verified helper**

In `server/cmd/server/comment_trigger_integration_test.go`, replace the spoof helper usage in the two agent-thread subtests so they call a new helper named `postVerifiedAgentComment(...)`:

```go
t.Run("reply to agent thread without mentions triggers agent", func(t *testing.T) {
	clearTasks(t, issueID)
	threadID := postVerifiedAgentComment(t, issueID, "I analyzed the issue.", agentID, nil)
	postComment(t, issueID, "Looks good, please proceed", strPtr(threadID))
	if n := countPendingTasks(t, issueID); n != 1 {
		t.Errorf("expected 1 pending task, got %d", n)
	}
})
```

```go
t.Run("@all in agent thread suppresses on_comment", func(t *testing.T) {
	clearTasks(t, issueID)
	threadID := postVerifiedAgentComment(t, issueID, "Here is my analysis.", agentID, nil)
	postComment(t, issueID, "[@All](mention://all/all) FYI for the team", strPtr(threadID))
	if n := countPendingTasks(t, issueID); n != 0 {
		t.Errorf("expected 0 pending tasks (@all in agent thread), got %d", n)
	}
})
```

- [ ] **Step 2: Run the focused integration tests to verify they fail**

Run:

```bash
cd "/Users/a1234/multica/.claude/worktrees/obj-auth-task1/server" && go test ./cmd/server -run 'TestCommentTrigger(OnComment|AtAllSuppression)' -v
```

Expected: FAIL with `undefined: postVerifiedAgentComment`.

- [ ] **Step 3: Add verified integration helpers and remove spoof-only agent roots**

In the same test file, add a task-bound authenticated request helper plus a verified agent-comment helper. Reuse the existing test DB directly for the queue row setup.

Add the authenticated request helper:

```go
func authRequestWithAgentTask(t *testing.T, method, path string, body any, agentID, taskID string) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, testServer.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}
```

Add a task creator for the integration package:

```go
func createQueuedTaskForIssue(t *testing.T, issueID, agentID string) string {
	t.Helper()
	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("load agent runtime: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create queued task: %v", err)
	}
	return taskID
}
```

Add the verified comment helper and delete the old `postCommentAsAgent(...)` helper once nothing uses it anymore:

```go
func postVerifiedAgentComment(t *testing.T, issueID, content, agentID string, parentID *string) string {
	t.Helper()
	taskID := createQueuedTaskForIssue(t, issueID, agentID)
	defer testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)

	body := map[string]any{
		"content": content,
		"type":    "comment",
	}
	if parentID != nil {
		body["parent_id"] = *parentID
	}

	resp := authRequestWithAgentTask(t, "POST", "/api/issues/"+issueID+"/agent-comments", body, agentID, taskID)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("postVerifiedAgentComment: expected 201, got %d: %s", resp.StatusCode, b)
	}

	var comment map[string]any
	readJSON(t, resp, &comment)
	return comment["id"].(string)
}
```

- [ ] **Step 4: Run the focused integration tests to verify they pass**

Run:

```bash
cd "/Users/a1234/multica/.claude/worktrees/obj-auth-task1/server" && go test ./cmd/server -run 'TestCommentTrigger(OnComment|AtAllSuppression)' -v
```

Expected: PASS.

- [ ] **Step 5: Run the broader comment-trigger integration slice**

Run:

```bash
cd "/Users/a1234/multica/.claude/worktrees/obj-auth-task1/server" && go test ./cmd/server -run 'TestCommentTrigger(OnComment|AtAllSuppression|OnAssignNoStatusGate|OnMentionNoStatusGate|ThreadInheritedMention)' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git -C "/Users/a1234/multica/.claude/worktrees/obj-auth-task1" add server/cmd/server/comment_trigger_integration_test.go && git -C "/Users/a1234/multica/.claude/worktrees/obj-auth-task1" commit -m "test(comment): use verified agent thread roots"
```
