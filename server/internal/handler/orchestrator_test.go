package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCreateComment_ChildAgentCommentEnqueuesParentTaskWithParentIssueTriggerComment(t *testing.T) {
	ctx := context.Background()

	var orchestratorRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, 'Orchestrator Runtime', 'local', 'claude', 'online', 'test', '{}'::jsonb, now(), $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&orchestratorRuntimeID); err != nil {
		t.Fatalf("insert orchestrator runtime: %v", err)
	}

	var workerRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, 'Worker Runtime', 'local', 'codex', 'online', 'test', '{}'::jsonb, now(), $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&workerRuntimeID); err != nil {
		t.Fatalf("insert worker runtime: %v", err)
	}

	var orchestratorAgentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Orchestrator', '', 'local', '{}'::jsonb, $2, 'workspace', 10, $3)
		RETURNING id
	`, testWorkspaceID, orchestratorRuntimeID, testUserID).Scan(&orchestratorAgentID); err != nil {
		t.Fatalf("insert orchestrator agent: %v", err)
	}

	var workerAgentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Worker Agent', '', 'local', '{}'::jsonb, $2, 'workspace', 1, $3)
		RETURNING id
	`, testWorkspaceID, workerRuntimeID, testUserID).Scan(&workerAgentID); err != nil {
		t.Fatalf("insert worker agent: %v", err)
	}

	createIssue := func(title string, assigneeType *string, assigneeID *string, parentIssueID *string) IssueResponse {
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
			"title":           title,
			"status":          "todo",
			"assignee_type":   assigneeType,
			"assignee_id":     assigneeID,
			"parent_issue_id": parentIssueID,
		})
		testHandler.CreateIssue(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateIssue %q: expected 201, got %d: %s", title, w.Code, w.Body.String())
		}
		var issue IssueResponse
		if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
			t.Fatalf("decode issue %q: %v", title, err)
		}
		return issue
	}

	agentType := "agent"
	parent := createIssue("Parent issue", &agentType, &orchestratorAgentID, nil)
	child := createIssue("Child issue", &agentType, &workerAgentID, &parent.ID)

	if _, err := testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, parent.ID, orchestratorAgentID); err != nil {
		t.Fatalf("clear parent pending task: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 OR issue_id = $2`, parent.ID, child.ID)
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1 OR issue_id = $2`, parent.ID, child.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1 OR id = $2`, parent.ID, child.ID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1 OR id = $2`, orchestratorAgentID, workerAgentID)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1 OR id = $2`, orchestratorRuntimeID, workerRuntimeID)
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+child.ID+"/comments", map[string]any{
		"content": "Worker completed subtask",
	})
	req = withURLParam(req, "id", child.ID)
	req.Header.Set("X-Agent-ID", workerAgentID)
	testHandler.CreateComment(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var childComment CommentResponse
	if err := json.NewDecoder(w.Body).Decode(&childComment); err != nil {
		t.Fatalf("decode child comment: %v", err)
	}

	var parentTask db.AgentTaskQueue
	if err := testPool.QueryRow(ctx, `
		SELECT id, agent_id, issue_id, status, priority, dispatched_at, started_at, completed_at, result, error, created_at, context, runtime_id, session_id, work_dir, trigger_comment_id
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, parent.ID, orchestratorAgentID).Scan(
		&parentTask.ID,
		&parentTask.AgentID,
		&parentTask.IssueID,
		&parentTask.Status,
		&parentTask.Priority,
		&parentTask.DispatchedAt,
		&parentTask.StartedAt,
		&parentTask.CompletedAt,
		&parentTask.Result,
		&parentTask.Error,
		&parentTask.CreatedAt,
		&parentTask.Context,
		&parentTask.RuntimeID,
		&parentTask.SessionID,
		&parentTask.WorkDir,
		&parentTask.TriggerCommentID,
	); err != nil {
		t.Fatalf("load parent task: %v", err)
	}

	if !parentTask.TriggerCommentID.Valid {
		t.Fatal("expected parent task to include trigger_comment_id")
	}
	if uuidToString(parentTask.TriggerCommentID) == childComment.ID {
		t.Fatalf("expected parent task trigger comment to belong to parent issue, got child comment %s", childComment.ID)
	}

	var triggerIssueID string
	var triggerAuthorType string
	var triggerType string
	var triggerContent string
	if err := testPool.QueryRow(ctx, `
		SELECT issue_id, author_type, type, content
		FROM comment
		WHERE id = $1
	`, parentTask.TriggerCommentID).Scan(&triggerIssueID, &triggerAuthorType, &triggerType, &triggerContent); err != nil {
		t.Fatalf("load trigger comment: %v", err)
	}

	if triggerIssueID != parent.ID {
		t.Fatalf("expected trigger comment issue_id %s, got %s", parent.ID, triggerIssueID)
	}
	if triggerAuthorType != "agent" {
		t.Fatalf("expected trigger comment author_type agent, got %s", triggerAuthorType)
	}
	if triggerType != "system" {
		t.Fatalf("expected trigger comment type system, got %s", triggerType)
	}
	if triggerContent == "" || !containsAll(triggerContent, child.ID, childComment.ID) {
		t.Fatalf("expected bridge comment to reference child issue and comment, got %q", triggerContent)
	}
}

func TestEnsureOrchestratorAgent_RebindsPendingTasksToNewRuntime(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)

	var oldRuntimeID, newRuntimeID string
	for i, provider := range []string{"claude", "codex"} {
		var runtimeID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_runtime (
				workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
			)
			VALUES ($1, NULL, $2, 'local', $3, $4, 'test', '{}'::jsonb, now(), $5)
			RETURNING id
		`, testWorkspaceID, fmt.Sprintf("runtime-%d", i), provider, map[bool]string{true: "offline", false: "online"}[i == 0], testUserID).Scan(&runtimeID); err != nil {
			t.Fatalf("insert runtime %d: %v", i, err)
		}
		if i == 0 {
			oldRuntimeID = runtimeID
		} else {
			newRuntimeID = runtimeID
		}
	}

	var orchestratorID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Orchestrator', '', 'local', '{}'::jsonb, $2, 'workspace', 10, $3)
		RETURNING id
	`, testWorkspaceID, oldRuntimeID, testUserID).Scan(&orchestratorID); err != nil {
		t.Fatalf("insert orchestrator: %v", err)
	}

	var queuedIssueID, dispatchedIssueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, position)
		VALUES ($1, 'Runtime rebind queued', 'todo', 'medium', 'member', $2, 999001, 0)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&queuedIssueID); err != nil {
		t.Fatalf("insert queued issue: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, position)
		VALUES ($1, 'Runtime rebind dispatched', 'todo', 'medium', 'member', $2, 999002, 0)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&dispatchedIssueID); err != nil {
		t.Fatalf("insert dispatched issue: %v", err)
	}

	var queuedTaskID, dispatchedTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 1)
		RETURNING id
	`, orchestratorID, oldRuntimeID, queuedIssueID).Scan(&queuedTaskID); err != nil {
		t.Fatalf("insert queued task: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'dispatched', 1)
		RETURNING id
	`, orchestratorID, oldRuntimeID, dispatchedIssueID).Scan(&dispatchedTaskID); err != nil {
		t.Fatalf("insert dispatched task: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1 OR id = $2`, queuedTaskID, dispatchedTaskID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1 OR id = $2`, queuedIssueID, dispatchedIssueID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, orchestratorID)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1 OR id = $2`, oldRuntimeID, newRuntimeID)
	})

	ensureOrchestratorAgent(ctx, queries, parseUUID(testWorkspaceID), parseUUID(newRuntimeID), parseUUID(testUserID))

	var agentRuntimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, orchestratorID).Scan(&agentRuntimeID); err != nil {
		t.Fatalf("load orchestrator runtime: %v", err)
	}
	if agentRuntimeID != newRuntimeID {
		t.Fatalf("expected orchestrator runtime %s, got %s", newRuntimeID, agentRuntimeID)
	}

	for _, taskID := range []string{queuedTaskID, dispatchedTaskID} {
		var runtimeID string
		if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent_task_queue WHERE id = $1`, taskID).Scan(&runtimeID); err != nil {
			t.Fatalf("load task runtime %s: %v", taskID, err)
		}
		if runtimeID != newRuntimeID {
			t.Fatalf("expected task %s runtime %s, got %s", taskID, newRuntimeID, runtimeID)
		}
	}
}

func TestDaemonRegister_CreatesOrchestratorOnPreferredClaudeRuntime(t *testing.T) {
	ctx := context.Background()

	var workspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Daemon Register Workspace', 'daemon-register-workspace', 'test', 'DRW')
		RETURNING id
	`).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})

	w := performDaemonRequest(testHandler.DaemonRegister, newDaemonAuthenticatedRequest(t, "POST", "/api/daemon/register", workspaceID, "daemon-preferred-runtime", map[string]any{
		"workspace_id": workspaceID,
		"daemon_id":    "daemon-preferred-runtime",
		"device_name":  "test-machine",
		"runtimes": []map[string]any{
			{"name": "Local Codex", "type": "codex", "version": "1.0.0", "status": "online"},
			{"name": "Local Claude", "type": "claude", "version": "1.0.0", "status": "online"},
		},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("DaemonRegister: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var provider string
	if err := testPool.QueryRow(ctx, `
		SELECT ar.provider
		FROM agent a
		JOIN agent_runtime ar ON ar.id = a.runtime_id
		WHERE a.workspace_id = $1 AND a.name = $2 AND a.archived_at IS NULL
	`, workspaceID, orchestratorName).Scan(&provider); err != nil {
		t.Fatalf("load orchestrator provider: %v", err)
	}
	if provider != "claude" {
		t.Fatalf("expected orchestrator to prefer claude runtime, got %s", provider)
	}
}

func TestEnsureOrchestratorAgent_DoesNotCreateDuplicateActiveAgent(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, 'Duplicate Guard Runtime', 'local', 'claude', 'online', 'test', '{}'::jsonb, now(), $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, orchestratorName)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})

	ensureOrchestratorAgent(ctx, queries, parseUUID(testWorkspaceID), parseUUID(runtimeID), parseUUID(testUserID))
	ensureOrchestratorAgent(ctx, queries, parseUUID(testWorkspaceID), parseUUID(runtimeID), parseUUID(testUserID))

	var count int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM agent
		WHERE workspace_id = $1 AND name = $2 AND archived_at IS NULL
	`, testWorkspaceID, orchestratorName).Scan(&count); err != nil {
		t.Fatalf("count orchestrators: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 active orchestrator, got %d", count)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if part != "" && !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
