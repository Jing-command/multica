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

func TestCreateComment_ChildAgentCommentDoesNotEnqueueParentTask(t *testing.T) {
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

	var parentCommentCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM comment
		WHERE issue_id = $1
	`, parent.ID).Scan(&parentCommentCount); err != nil {
		t.Fatalf("count parent comments: %v", err)
	}
	if parentCommentCount != 0 {
		t.Fatalf("expected no parent bridge comment from child agent comment, got %d comments", parentCommentCount)
	}

	var parentTaskCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
	`, parent.ID, orchestratorAgentID).Scan(&parentTaskCount); err != nil {
		t.Fatalf("count parent tasks: %v", err)
	}
	if parentTaskCount != 0 {
		t.Fatalf("expected child agent comment not to enqueue parent task, got %d tasks", parentTaskCount)
	}
}

func TestCreateComment_ChildAgentCommentDoesNotEnqueueFollowupParentTaskWhenParentTaskIsRunning(t *testing.T) {
	ctx := context.Background()

	var orchestratorRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, 'Orchestrator Runtime Running', 'local', 'claude', 'online', 'test', '{}'::jsonb, now(), $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&orchestratorRuntimeID); err != nil {
		t.Fatalf("insert orchestrator runtime: %v", err)
	}

	var workerRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, 'Worker Runtime Running', 'local', 'codex', 'online', 'test', '{}'::jsonb, now(), $2)
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
		VALUES ($1, 'Worker Agent Running', '', 'local', '{}'::jsonb, $2, 'workspace', 1, $3)
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
	parent := createIssue("Parent running issue", &agentType, &orchestratorAgentID, nil)
	child := createIssue("Child running issue", &agentType, &workerAgentID, &parent.ID)

	if _, err := testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, parent.ID, orchestratorAgentID); err != nil {
		t.Fatalf("clear parent tasks: %v", err)
	}

	var runningTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, started_at)
		VALUES ($1, $2, $3, 'running', 1, now())
		RETURNING id
	`, orchestratorAgentID, orchestratorRuntimeID, parent.ID).Scan(&runningTaskID); err != nil {
		t.Fatalf("insert running parent task: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 OR issue_id = $2`, parent.ID, child.ID)
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1 OR issue_id = $2`, parent.ID, child.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1 OR id = $2`, parent.ID, child.ID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1 OR id = $2`, orchestratorAgentID, workerAgentID)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1 OR id = $2`, orchestratorRuntimeID, workerRuntimeID)
		_ = runningTaskID
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+child.ID+"/comments", map[string]any{
		"content": "Worker posted follow-up while parent task is still running",
	})
	req = withURLParam(req, "id", child.ID)
	req.Header.Set("X-Agent-ID", workerAgentID)
	testHandler.CreateComment(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var taskCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
	`, parent.ID, orchestratorAgentID).Scan(&taskCount); err != nil {
		t.Fatalf("count parent tasks: %v", err)
	}
	if taskCount != 1 {
		t.Fatalf("expected existing running parent task only, got %d tasks", taskCount)
	}

	var parentCommentCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM comment
		WHERE issue_id = $1
	`, parent.ID).Scan(&parentCommentCount); err != nil {
		t.Fatalf("count parent comments: %v", err)
	}
	if parentCommentCount != 0 {
		t.Fatalf("expected no parent bridge comment while parent task is running, got %d comments", parentCommentCount)
	}
}

func TestCreateComment_MentionedAgentCanQueueAlongsideAssignedAgentPendingTask(t *testing.T) {
	ctx := context.Background()

	var assigneeRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, 'Assigned Runtime', 'local', 'claude', 'online', 'test', '{}'::jsonb, now(), $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&assigneeRuntimeID); err != nil {
		t.Fatalf("insert assigned runtime: %v", err)
	}

	var mentionedRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, 'Mentioned Runtime', 'local', 'codex', 'online', 'test', '{}'::jsonb, now(), $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&mentionedRuntimeID); err != nil {
		t.Fatalf("insert mentioned runtime: %v", err)
	}

	var assignedAgentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Assigned Agent', '', 'local', '{}'::jsonb, $2, 'workspace', 1, $3)
		RETURNING id
	`, testWorkspaceID, assigneeRuntimeID, testUserID).Scan(&assignedAgentID); err != nil {
		t.Fatalf("insert assigned agent: %v", err)
	}

	var mentionedAgentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Mentioned Agent', '', 'local', '{}'::jsonb, $2, 'workspace', 1, $3)
		RETURNING id
	`, testWorkspaceID, mentionedRuntimeID, testUserID).Scan(&mentionedAgentID); err != nil {
		t.Fatalf("insert mentioned agent: %v", err)
	}

	agentType := "agent"
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "Mention fanout issue",
		"status":        "todo",
		"assignee_type": &agentType,
		"assignee_id":   &assignedAgentID,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1 OR id = $2`, assignedAgentID, mentionedAgentID)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1 OR id = $2`, assigneeRuntimeID, mentionedRuntimeID)
	})

	commentW := httptest.NewRecorder()
	commentReq := newRequest("POST", "/api/issues/"+issue.ID+"/comments", map[string]any{
		"content": fmt.Sprintf("Please sync with [@Mentioned](mention://agent/%s)", mentionedAgentID),
	})
	commentReq = withURLParam(commentReq, "id", issue.ID)
	testHandler.CreateComment(commentW, commentReq)
	if commentW.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", commentW.Code, commentW.Body.String())
	}

	var assignedCount, mentionedCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, issue.ID, assignedAgentID).Scan(&assignedCount); err != nil {
		t.Fatalf("count assigned agent tasks: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, issue.ID, mentionedAgentID).Scan(&mentionedCount); err != nil {
		t.Fatalf("count mentioned agent tasks: %v", err)
	}
	if assignedCount != 1 || mentionedCount != 1 {
		t.Fatalf("expected one pending task for assigned and mentioned agents, got assigned=%d mentioned=%d", assignedCount, mentionedCount)
	}
}

func TestCreateComment_NonOrchestrationChildIssueMentionOfParentAssigneeStillEnqueuesTask(t *testing.T) {
	ctx := context.Background()

	var parentAssigneeRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, 'Parent Assignee Runtime Mention', 'local', 'claude', 'online', 'test', '{}'::jsonb, now(), $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&parentAssigneeRuntimeID); err != nil {
		t.Fatalf("insert parent assignee runtime: %v", err)
	}

	var workerRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, 'Worker Runtime Non-Orch Mention', 'local', 'codex', 'online', 'test', '{}'::jsonb, now(), $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&workerRuntimeID); err != nil {
		t.Fatalf("insert worker runtime: %v", err)
	}

	var parentAssigneeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Parent Assignee', '', 'local', '{}'::jsonb, $2, 'workspace', 10, $3)
		RETURNING id
	`, testWorkspaceID, parentAssigneeRuntimeID, testUserID).Scan(&parentAssigneeID); err != nil {
		t.Fatalf("insert parent assignee agent: %v", err)
	}

	var workerAgentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Worker Agent Non-Orch Mention', '', 'local', '{}'::jsonb, $2, 'workspace', 1, $3)
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
	parent := createIssue("Parent non-orchestration mention issue", &agentType, &parentAssigneeID, nil)
	child := createIssue("Child non-orchestration mention issue", &agentType, &workerAgentID, &parent.ID)

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 OR issue_id = $2`, parent.ID, child.ID)
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1 OR issue_id = $2`, parent.ID, child.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1 OR id = $2`, parent.ID, child.ID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1 OR id = $2`, parentAssigneeID, workerAgentID)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1 OR id = $2`, parentAssigneeRuntimeID, workerRuntimeID)
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+child.ID+"/comments", map[string]any{
		"content": fmt.Sprintf("Please review [@ParentAssignee](mention://agent/%s)", parentAssigneeID),
	})
	req = withURLParam(req, "id", child.ID)
	testHandler.CreateComment(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var parentAssigneeTaskCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
	`, child.ID, parentAssigneeID).Scan(&parentAssigneeTaskCount); err != nil {
		t.Fatalf("count parent assignee child tasks: %v", err)
	}
	if parentAssigneeTaskCount != 1 {
		t.Fatalf("expected explicit mention on non-orchestration child to enqueue one task, got %d", parentAssigneeTaskCount)
	}
}

func TestCreateComment_ChildIssueMentionOfOrchestratorDoesNotEnqueueOrchestratorTask(t *testing.T) {
	ctx := context.Background()

	var orchestratorRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, 'Orchestrator Runtime Mention', 'local', 'claude', 'online', 'test', '{}'::jsonb, now(), $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&orchestratorRuntimeID); err != nil {
		t.Fatalf("insert orchestrator runtime: %v", err)
	}

	var workerRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, 'Worker Runtime Mention', 'local', 'codex', 'online', 'test', '{}'::jsonb, now(), $2)
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
		VALUES ($1, 'Worker Agent Mention', '', 'local', '{}'::jsonb, $2, 'workspace', 1, $3)
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
	parent := createIssue("Parent mention issue", &agentType, &orchestratorAgentID, nil)
	child := createIssue("Child mention issue", &agentType, &workerAgentID, &parent.ID)

	if _, err := testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 OR issue_id = $2`, parent.ID, child.ID); err != nil {
		t.Fatalf("clear pending tasks: %v", err)
	}

	if _, err := testPool.Exec(ctx, `
		INSERT INTO child_spec (workspace_id, parent_issue_id, child_issue_id, worker_agent_id, orchestrator_agent_id)
		VALUES ($1, $2, $3, $4, $5)
	`, testWorkspaceID, parent.ID, child.ID, workerAgentID, orchestratorAgentID); err != nil {
		t.Fatalf("insert child spec: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 OR issue_id = $2`, parent.ID, child.ID)
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1 OR issue_id = $2`, parent.ID, child.ID)
		testPool.Exec(ctx, `DELETE FROM child_spec WHERE child_issue_id = $1`, child.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1 OR id = $2`, parent.ID, child.ID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1 OR id = $2`, orchestratorAgentID, workerAgentID)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1 OR id = $2`, orchestratorRuntimeID, workerRuntimeID)
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+child.ID+"/comments", map[string]any{
		"content": fmt.Sprintf("Please review [@Orchestrator](mention://agent/%s)", orchestratorAgentID),
	})
	req = withURLParam(req, "id", child.ID)
	testHandler.CreateComment(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var orchestratorTaskCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
	`, child.ID, orchestratorAgentID).Scan(&orchestratorTaskCount); err != nil {
		t.Fatalf("count orchestrator child tasks: %v", err)
	}
	if orchestratorTaskCount != 0 {
		t.Fatalf("expected child comment mention to remain narrative-only for orchestrator, got %d tasks", orchestratorTaskCount)
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
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, testUserID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/daemon/register", strings.NewReader(fmt.Sprintf(`{
		"workspace_id":%q,
		"daemon_id":"daemon-preferred-runtime",
		"device_name":"test-machine",
		"runtimes":[
			{"name":"Local Codex","type":"codex","version":"1.0.0","status":"online"},
			{"name":"Local Claude","type":"claude","version":"1.0.0","status":"online"}
		]
	}`, workspaceID)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)

	testHandler.DaemonRegister(w, req)
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
