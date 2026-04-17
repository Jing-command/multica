package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var orchestrationHandlerSeq atomic.Int64

type orchestrationHandlerFixture struct {
	parentIssueID        string
	childIssueID         string
	orchestratorAgentID  string
	workerAgentID        string
	outsiderAgentID      string
	orchestratorTaskID   string
	workerChildTaskID    string
	workerRuntimeID      string
}

func seedOrchestrationHandlerFixture(t *testing.T, ctx context.Context) orchestrationHandlerFixture {
	t.Helper()

	seq := orchestrationHandlerSeq.Add(1)
	suffix := fmt.Sprintf("%s-%d", t.Name(), seq)

	createRuntime := func(name, provider string) string {
		t.Helper()
		var runtimeID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_runtime (
				workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
			)
			VALUES ($1, NULL, $2, 'local', $3, 'online', 'test', '{}'::jsonb, now(), $4)
			RETURNING id
		`, testWorkspaceID, name+" "+suffix, provider, testUserID).Scan(&runtimeID); err != nil {
			t.Fatalf("insert runtime %s: %v", name, err)
		}
		return runtimeID
	}

	createAgent := func(name, runtimeID string, maxConcurrent int) string {
		t.Helper()
		var agentID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent (
				workspace_id, name, description, runtime_mode, runtime_config,
				runtime_id, visibility, max_concurrent_tasks, owner_id
			)
			VALUES ($1, $2, '', 'local', '{}'::jsonb, $3, 'workspace', $4, $5)
			RETURNING id
		`, testWorkspaceID, name+" "+suffix, runtimeID, maxConcurrent, testUserID).Scan(&agentID); err != nil {
			t.Fatalf("insert agent %s: %v", name, err)
		}
		return agentID
	}

	orchestratorRuntimeID := createRuntime("Handler Orchestrator Runtime", "claude")
	workerRuntimeID := createRuntime("Handler Worker Runtime", "codex")
	outsiderRuntimeID := createRuntime("Handler Outsider Runtime", "claude")

	orchestratorAgentID := createAgent("Handler Orchestrator", orchestratorRuntimeID, 10)
	workerAgentID := createAgent("Handler Worker", workerRuntimeID, 1)
	outsiderAgentID := createAgent("Handler Outsider", outsiderRuntimeID, 1)

	var parentIssueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, position, assignee_type, assignee_id)
		VALUES ($1, $2, 'todo', 'medium', 'member', $3, $4, 0, 'agent', $5)
		RETURNING id
	`, testWorkspaceID, "Handler Parent "+suffix, testUserID, 880000+seq*2, orchestratorAgentID).Scan(&parentIssueID); err != nil {
		t.Fatalf("insert parent issue: %v", err)
	}

	var childIssueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_type, creator_id,
			number, position, parent_issue_id, assignee_type, assignee_id
		)
		VALUES ($1, $2, 'todo', 'medium', 'member', $3, $4, 0, $5, 'agent', $6)
		RETURNING id
	`, testWorkspaceID, "Handler Child "+suffix, testUserID, 880001+seq*2, parentIssueID, workerAgentID).Scan(&childIssueID); err != nil {
		t.Fatalf("insert child issue: %v", err)
	}

	childSpec, err := testHandler.Queries.CreateChildSpec(ctx, db.CreateChildSpecParams{
		WorkspaceID:         parseUUID(testWorkspaceID),
		ParentIssueID:       parseUUID(parentIssueID),
		ChildIssueID:        parseUUID(childIssueID),
		WorkerAgentID:       parseUUID(workerAgentID),
		OrchestratorAgentID: parseUUID(orchestratorAgentID),
	})
	if err != nil {
		t.Fatalf("create child spec: %v", err)
	}

	if _, err := testHandler.Queries.CreateChildAcceptanceCriterion(ctx, db.CreateChildAcceptanceCriterionParams{
		ChildSpecID:   childSpec.ID,
		Ordinal:       1,
		CriterionText: "Worker output passes orchestrator review.",
	}); err != nil {
		t.Fatalf("create child acceptance criterion: %v", err)
	}

	orchestratorTaskID := createAgentTaskForIssue(t, ctx, orchestratorAgentID, parentIssueID)
	workerChildTaskID := createAgentTaskForIssue(t, ctx, workerAgentID, childIssueID)

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1 OR id = $2`, orchestratorTaskID, workerChildTaskID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1 OR id = $2`, childIssueID, parentIssueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1 OR id = $2 OR id = $3`, orchestratorAgentID, workerAgentID, outsiderAgentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1 OR id = $2 OR id = $3`, orchestratorRuntimeID, workerRuntimeID, outsiderRuntimeID)
	})

	return orchestrationHandlerFixture{
		parentIssueID:       parentIssueID,
		childIssueID:        childIssueID,
		orchestratorAgentID: orchestratorAgentID,
		workerAgentID:       workerAgentID,
		outsiderAgentID:     outsiderAgentID,
		orchestratorTaskID:  orchestratorTaskID,
		workerChildTaskID:   workerChildTaskID,
		workerRuntimeID:     workerRuntimeID,
	}
}

func TestSubmitReviewWorkflow_AllowsAssignedWorkerAgent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"idempotency_key": "submit-http-1",
		"summary":         "ready for orchestrator review",
		"evidence": map[string]any{
			"pr_url": "https://example.invalid/pr/123",
		},
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.workerAgentID)
	req.Header.Set("X-Task-ID", fixture.workerChildTaskID)

	testHandler.SubmitIssueReview(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if issue.Status != "in_review" {
		t.Fatalf("expected child issue status in_review, got %q", issue.Status)
	}
	if issue.Orchestration == nil || issue.Orchestration.Child == nil {
		t.Fatal("expected child orchestration state in issue response")
	}
	if issue.Orchestration.Child.Status != "awaiting_review" {
		t.Fatalf("expected child orchestration status awaiting_review, got %q", issue.Orchestration.Child.Status)
	}
}

func createAgentTaskForIssue(t *testing.T, ctx context.Context, agentID, issueID string) string {
	t.Helper()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("load agent runtime: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create agent task: %v", err)
	}
	return taskID
}

func TestSubmitReviewWorkflow_RejectsMemberSpoofingAgentHeaderWithoutTask(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"summary": "member should not be able to submit as worker",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.workerAgentID)

	testHandler.SubmitIssueReview(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitReviewWorkflow_RejectsMemberSpoofingAgentHeaderWithWrongTask(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"summary": "member should not be able to submit as worker",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.workerAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)

	testHandler.SubmitIssueReview(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitReviewWorkflow_AllowsAssignedWorkerAgentWithMatchingTask(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"summary": "ready for orchestrator review with task validation",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.workerAgentID)
	req.Header.Set("X-Task-ID", fixture.workerChildTaskID)

	testHandler.SubmitIssueReview(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitReviewWorkflow_DefaultsMissingEvidenceToEmptyObject(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"summary": "ready without evidence",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.workerAgentID)
	req.Header.Set("X-Task-ID", fixture.workerChildTaskID)

	testHandler.SubmitIssueReview(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	childSpec, err := testHandler.Queries.GetChildSpecByIssueID(ctx, parseUUID(fixture.childIssueID))
	if err != nil {
		t.Fatalf("load child spec: %v", err)
	}
	reviewRound, err := testHandler.Queries.GetLatestPendingChildReviewRound(ctx, childSpec.ID)
	if err != nil {
		t.Fatalf("load pending review round: %v", err)
	}
	if got := string(reviewRound.SubmissionEvidence); got != `{}` {
		t.Fatalf("expected submission evidence {}, got %s", got)
	}
}

func TestSubmitReviewWorkflow_RejectsCompletedTask(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, fixture.workerChildTaskID); err != nil {
		t.Fatalf("complete worker task: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"summary": "completed task should not authorize workflow",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.workerAgentID)
	req.Header.Set("X-Task-ID", fixture.workerChildTaskID)

	testHandler.SubmitIssueReview(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReportIssueBlocked_RejectsWhitespaceReason(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/report-blocked", map[string]any{
		"reason": "   ",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.workerAgentID)
	req.Header.Set("X-Task-ID", fixture.workerChildTaskID)

	testHandler.ReportIssueBlocked(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMapWorkflowError_ReturnsInternalServerErrorForUnexpectedError(t *testing.T) {
	status, msg := mapWorkflowError(errors.New("boom"))
	if status != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", status)
	}
	if msg != "internal server error" {
		t.Fatalf("expected generic internal error message, got %q", msg)
	}
}

func TestReviewWorkflow_AllowsAssignedOrchestratorAgent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	submit := httptest.NewRecorder()
	submitReq := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"idempotency_key": "submit-http-review-1",
		"summary":         "ready for review",
		"evidence": map[string]any{
			"pr_url": "https://example.invalid/pr/124",
		},
	})
	submitReq = withURLParam(submitReq, "id", fixture.childIssueID)
	submitReq.Header.Set("X-Agent-ID", fixture.workerAgentID)
	submitReq.Header.Set("X-Task-ID", fixture.workerChildTaskID)
	testHandler.SubmitIssueReview(submit, submitReq)
	if submit.Code != http.StatusOK {
		t.Fatalf("submit review setup failed: %d: %s", submit.Code, submit.Body.String())
	}

	childSpec, err := testHandler.Queries.GetChildSpecByIssueID(ctx, parseUUID(fixture.childIssueID))
	if err != nil {
		t.Fatalf("load child spec: %v", err)
	}
	criterion, err := testHandler.Queries.CreateChildAcceptanceCriterion(ctx, db.CreateChildAcceptanceCriterionParams{
		ChildSpecID:   childSpec.ID,
		Ordinal:       2,
		CriterionText: "Verified in handler review test",
	})
	if err != nil {
		t.Fatalf("create acceptance criterion: %v", err)
	}

	reviewRound, err := testHandler.Queries.GetLatestPendingChildReviewRound(ctx, childSpec.ID)
	if err != nil {
		t.Fatalf("load pending review round: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/review", map[string]any{
		"idempotency_key": "review-http-1",
		"review_round_id": uuidToString(reviewRound.ID),
		"verdict":         "approved",
		"summary":         "looks good",
		"criterion_results": []map[string]any{{
			"criterion_id": uuidToString(criterion.ID),
			"result":       "pass",
			"note":         "verified",
		}},
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)

	testHandler.ReviewIssueWorkflow(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if issue.Status != "done" {
		t.Fatalf("expected child issue status done, got %q", issue.Status)
	}
	if issue.Orchestration == nil || issue.Orchestration.Child == nil || issue.Orchestration.Child.LatestReview == nil {
		t.Fatal("expected latest review in orchestration state")
	}
	if issue.Orchestration.Child.LatestReview.Decision != "approved" {
		t.Fatalf("expected latest review decision approved, got %q", issue.Orchestration.Child.LatestReview.Decision)
	}
}

func TestReviewWorkflow_ReplaysSameIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	submit := httptest.NewRecorder()
	submitReq := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"idempotency_key": "submit-http-review-replay-1",
		"summary":         "ready for replay review",
	})
	submitReq = withURLParam(submitReq, "id", fixture.childIssueID)
	submitReq.Header.Set("X-Agent-ID", fixture.workerAgentID)
	submitReq.Header.Set("X-Task-ID", fixture.workerChildTaskID)
	testHandler.SubmitIssueReview(submit, submitReq)
	if submit.Code != http.StatusOK {
		t.Fatalf("submit review setup failed: %d: %s", submit.Code, submit.Body.String())
	}

	childSpec, err := testHandler.Queries.GetChildSpecByIssueID(ctx, parseUUID(fixture.childIssueID))
	if err != nil {
		t.Fatalf("load child spec: %v", err)
	}
	reviewRound, err := testHandler.Queries.GetLatestPendingChildReviewRound(ctx, childSpec.ID)
	if err != nil {
		t.Fatalf("load pending review round: %v", err)
	}

	payload := map[string]any{
		"idempotency_key": "review-http-replay-1",
		"review_round_id": uuidToString(reviewRound.ID),
		"verdict":         "blocked",
		"summary":         "still blocked",
	}

	first := httptest.NewRecorder()
	firstReq := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/review", payload)
	firstReq = withURLParam(firstReq, "id", fixture.childIssueID)
	firstReq.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	firstReq.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.ReviewIssueWorkflow(first, firstReq)
	if first.Code != http.StatusOK {
		t.Fatalf("first review expected 200, got %d: %s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	secondReq := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/review", payload)
	secondReq = withURLParam(secondReq, "id", fixture.childIssueID)
	secondReq.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	secondReq.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.ReviewIssueWorkflow(second, secondReq)
	if second.Code != http.StatusOK {
		t.Fatalf("replay review expected 200, got %d: %s", second.Code, second.Body.String())
	}

	var issue IssueResponse
	if err := json.NewDecoder(second.Body).Decode(&issue); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if issue.Status != "blocked" {
		t.Fatalf("expected replayed child issue status blocked, got %q", issue.Status)
	}
	if issue.Orchestration == nil || issue.Orchestration.Child == nil || issue.Orchestration.Child.LatestReview == nil {
		t.Fatal("expected replayed latest review in orchestration state")
	}
	if issue.Orchestration.Child.LatestReview.Decision != "blocked" {
		t.Fatalf("expected replayed latest review decision blocked, got %q", issue.Orchestration.Child.LatestReview.Decision)
	}
}

func TestReviewWorkflow_ReturnsBadRequestForUnsupportedVerdict(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	submit := httptest.NewRecorder()
	submitReq := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"summary": "ready for invalid verdict review",
	})
	submitReq = withURLParam(submitReq, "id", fixture.childIssueID)
	submitReq.Header.Set("X-Agent-ID", fixture.workerAgentID)
	submitReq.Header.Set("X-Task-ID", fixture.workerChildTaskID)
	testHandler.SubmitIssueReview(submit, submitReq)
	if submit.Code != http.StatusOK {
		t.Fatalf("submit review setup failed: %d: %s", submit.Code, submit.Body.String())
	}

	childSpec, err := testHandler.Queries.GetChildSpecByIssueID(ctx, parseUUID(fixture.childIssueID))
	if err != nil {
		t.Fatalf("load child spec: %v", err)
	}
	reviewRound, err := testHandler.Queries.GetLatestPendingChildReviewRound(ctx, childSpec.ID)
	if err != nil {
		t.Fatalf("load pending review round: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/review", map[string]any{
		"review_round_id": uuidToString(reviewRound.ID),
		"verdict":         "unsupported",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.ReviewIssueWorkflow(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReviewWorkflow_ReturnsBadRequestForInvalidCriterionResult(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	submit := httptest.NewRecorder()
	submitReq := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"summary": "ready for invalid criterion review",
	})
	submitReq = withURLParam(submitReq, "id", fixture.childIssueID)
	submitReq.Header.Set("X-Agent-ID", fixture.workerAgentID)
	submitReq.Header.Set("X-Task-ID", fixture.workerChildTaskID)
	testHandler.SubmitIssueReview(submit, submitReq)
	if submit.Code != http.StatusOK {
		t.Fatalf("submit review setup failed: %d: %s", submit.Code, submit.Body.String())
	}

	childSpec, err := testHandler.Queries.GetChildSpecByIssueID(ctx, parseUUID(fixture.childIssueID))
	if err != nil {
		t.Fatalf("load child spec: %v", err)
	}
	criterion, err := testHandler.Queries.CreateChildAcceptanceCriterion(ctx, db.CreateChildAcceptanceCriterionParams{
		ChildSpecID:   childSpec.ID,
		Ordinal:       2,
		CriterionText: "Criterion for invalid result test",
	})
	if err != nil {
		t.Fatalf("create acceptance criterion: %v", err)
	}
	reviewRound, err := testHandler.Queries.GetLatestPendingChildReviewRound(ctx, childSpec.ID)
	if err != nil {
		t.Fatalf("load pending review round: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/review", map[string]any{
		"review_round_id": uuidToString(reviewRound.ID),
		"verdict":         "approved",
		"criterion_results": []map[string]any{{
			"criterion_id": uuidToString(criterion.ID),
			"result":       "bogus",
		}},
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.ReviewIssueWorkflow(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReviewWorkflow_ReturnsBadRequestForDuplicateCriterionIDs(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	submit := httptest.NewRecorder()
	submitReq := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"summary": "ready for duplicate criterion review",
	})
	submitReq = withURLParam(submitReq, "id", fixture.childIssueID)
	submitReq.Header.Set("X-Agent-ID", fixture.workerAgentID)
	submitReq.Header.Set("X-Task-ID", fixture.workerChildTaskID)
	testHandler.SubmitIssueReview(submit, submitReq)
	if submit.Code != http.StatusOK {
		t.Fatalf("submit review setup failed: %d: %s", submit.Code, submit.Body.String())
	}

	childSpec, err := testHandler.Queries.GetChildSpecByIssueID(ctx, parseUUID(fixture.childIssueID))
	if err != nil {
		t.Fatalf("load child spec: %v", err)
	}
	criterion, err := testHandler.Queries.CreateChildAcceptanceCriterion(ctx, db.CreateChildAcceptanceCriterionParams{
		ChildSpecID:   childSpec.ID,
		Ordinal:       2,
		CriterionText: "Criterion for duplicate result test",
	})
	if err != nil {
		t.Fatalf("create acceptance criterion: %v", err)
	}
	reviewRound, err := testHandler.Queries.GetLatestPendingChildReviewRound(ctx, childSpec.ID)
	if err != nil {
		t.Fatalf("load pending review round: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/review", map[string]any{
		"review_round_id": uuidToString(reviewRound.ID),
		"verdict":         "approved",
		"criterion_results": []map[string]any{
			{
				"criterion_id": uuidToString(criterion.ID),
				"result":       "pass",
			},
			{
				"criterion_id": uuidToString(criterion.ID),
				"result":       "pass",
			},
		},
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.ReviewIssueWorkflow(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReviewWorkflow_ReturnsNotFoundForUnknownReviewRound(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	submit := httptest.NewRecorder()
	submitReq := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"summary": "ready for missing round review",
	})
	submitReq = withURLParam(submitReq, "id", fixture.childIssueID)
	submitReq.Header.Set("X-Agent-ID", fixture.workerAgentID)
	submitReq.Header.Set("X-Task-ID", fixture.workerChildTaskID)
	testHandler.SubmitIssueReview(submit, submitReq)
	if submit.Code != http.StatusOK {
		t.Fatalf("submit review setup failed: %d: %s", submit.Code, submit.Body.String())
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/review", map[string]any{
		"review_round_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"verdict":         "approved",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.ReviewIssueWorkflow(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReviewWorkflow_ReturnsConflictWhenChildNoLongerAwaitingReview(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	submit := httptest.NewRecorder()
	submitReq := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/submit-review", map[string]any{
		"summary": "ready for conflict review",
	})
	submitReq = withURLParam(submitReq, "id", fixture.childIssueID)
	submitReq.Header.Set("X-Agent-ID", fixture.workerAgentID)
	submitReq.Header.Set("X-Task-ID", fixture.workerChildTaskID)
	testHandler.SubmitIssueReview(submit, submitReq)
	if submit.Code != http.StatusOK {
		t.Fatalf("submit review setup failed: %d: %s", submit.Code, submit.Body.String())
	}

	childSpec, err := testHandler.Queries.GetChildSpecByIssueID(ctx, parseUUID(fixture.childIssueID))
	if err != nil {
		t.Fatalf("load child spec: %v", err)
	}
	reviewRound, err := testHandler.Queries.GetLatestPendingChildReviewRound(ctx, childSpec.ID)
	if err != nil {
		t.Fatalf("load pending review round: %v", err)
	}

	first := httptest.NewRecorder()
	firstReq := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/review", map[string]any{
		"review_round_id": uuidToString(reviewRound.ID),
		"verdict":         "approved",
	})
	firstReq = withURLParam(firstReq, "id", fixture.childIssueID)
	firstReq.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	firstReq.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.ReviewIssueWorkflow(first, firstReq)
	if first.Code != http.StatusOK {
		t.Fatalf("first review expected 200, got %d: %s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	secondReq := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/review", map[string]any{
		"review_round_id": uuidToString(reviewRound.ID),
		"verdict":         "approved",
	})
	secondReq = withURLParam(secondReq, "id", fixture.childIssueID)
	secondReq.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	secondReq.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.ReviewIssueWorkflow(second, secondReq)

	if second.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", second.Code, second.Body.String())
	}
}

func TestReportBlockedWorkflow_AllowsAssignedWorker(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/report-blocked", map[string]any{
		"reason": "  worker is blocked on dependency  ",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.workerAgentID)
	req.Header.Set("X-Task-ID", fixture.workerChildTaskID)

	testHandler.ReportIssueBlocked(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if issue.Status != "blocked" {
		t.Fatalf("expected child issue status blocked, got %q", issue.Status)
	}
	if issue.Orchestration == nil || issue.Orchestration.Child == nil {
		t.Fatal("expected child orchestration state in response")
	}
	if issue.Orchestration.Child.Status != "blocked" {
		t.Fatalf("expected child orchestration status blocked, got %q", issue.Orchestration.Child.Status)
	}
	if issue.Orchestration.Child.OpenEscalation == nil {
		t.Fatal("expected open escalation in response")
	}
	if issue.Orchestration.Child.OpenEscalation.Reason != "worker is blocked on dependency" {
		t.Fatalf("expected trimmed escalation reason, got %q", issue.Orchestration.Child.OpenEscalation.Reason)
	}
}

func TestReportBlockedWorkflow_RejectsUnassignedAgent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/report-blocked", map[string]any{
		"reason": "outsider cannot report blocked",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.outsiderAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)

	testHandler.ReportIssueBlocked(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReplanWorkflow_AllowsAssignedOrchestrator(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/replan", map[string]any{
		"idempotency_key": "replan-http-1",
		"reason":          "scope changed",
		"plan_content":    "updated plan",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)

	testHandler.ReplanIssueWorkflow(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	childSpec, err := testHandler.Queries.GetChildSpecByIssueID(ctx, parseUUID(fixture.childIssueID))
	if err != nil {
		t.Fatalf("load child spec: %v", err)
	}
	revision, err := testHandler.Queries.GetPlanRevisionByIdempotencyKey(ctx, db.GetPlanRevisionByIdempotencyKeyParams{
		ChildSpecID:    childSpec.ID,
		IdempotencyKey: strToText("replan-http-1"),
	})
	if err != nil {
		t.Fatalf("load plan revision by idempotency key: %v", err)
	}
	if got := textToPtr(revision.Reason); got == nil || *got != "scope changed" {
		t.Fatalf("expected persisted replan reason, got %#v", got)
	}
	if got := textToPtr(revision.PlanContent); got == nil || *got != "updated plan" {
		t.Fatalf("expected persisted replan content, got %#v", got)
	}
}

func TestReplanWorkflow_RejectsTerminalChild(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child issue done: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE child_spec SET status = 'done' WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child spec done: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/replan", map[string]any{
		"idempotency_key": "replan-http-terminal-1",
		"reason":          "scope changed after completion",
		"plan_content":    "should be rejected",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)

	testHandler.ReplanIssueWorkflow(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReplanWorkflow_DoesNotPersistEmptyReasonOrPlanContent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.childIssueID+"/workflow/replan", map[string]any{
		"idempotency_key": "replan-http-empty-1",
		"reason":          "   ",
		"plan_content":    "",
	})
	req = withURLParam(req, "id", fixture.childIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)

	testHandler.ReplanIssueWorkflow(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	childSpec, err := testHandler.Queries.GetChildSpecByIssueID(ctx, parseUUID(fixture.childIssueID))
	if err != nil {
		t.Fatalf("load child spec: %v", err)
	}
	revision, err := testHandler.Queries.GetPlanRevisionByIdempotencyKey(ctx, db.GetPlanRevisionByIdempotencyKeyParams{
		ChildSpecID:    childSpec.ID,
		IdempotencyKey: strToText("replan-http-empty-1"),
	})
	if err != nil {
		t.Fatalf("load plan revision by idempotency key: %v", err)
	}
	if revision.Reason.Valid {
		t.Fatalf("expected empty reason to remain null, got %#v", revision.Reason)
	}
	if revision.PlanContent.Valid {
		t.Fatalf("expected empty plan_content to remain null, got %#v", revision.PlanContent)
	}
}

func TestGetIssue_IncludesStructuredOrchestrationState(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+fixture.childIssueID, nil)
	req = withURLParam(req, "id", fixture.childIssueID)
	testHandler.GetIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if issue.Orchestration == nil || issue.Orchestration.Child == nil {
		t.Fatal("expected child orchestration state in issue response")
	}
	if issue.Orchestration.Child.ParentIssueID != fixture.parentIssueID {
		t.Fatalf("expected parent_issue_id %s, got %s", fixture.parentIssueID, issue.Orchestration.Child.ParentIssueID)
	}
	if issue.Orchestration.Child.WorkerAgentID != fixture.workerAgentID {
		t.Fatalf("expected worker_agent_id %s, got %s", fixture.workerAgentID, issue.Orchestration.Child.WorkerAgentID)
	}
	if issue.Orchestration.Child.OrchestratorAgentID != fixture.orchestratorAgentID {
		t.Fatalf("expected orchestrator_agent_id %s, got %s", fixture.orchestratorAgentID, issue.Orchestration.Child.OrchestratorAgentID)
	}
}

func assertListedIssuesOmitOrchestration(t *testing.T, issues []IssueResponse, ids ...string) {
	t.Helper()

	if len(issues) == 0 {
		t.Fatal("expected list response to include issues")
	}

	targets := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		targets[id] = struct{}{}
	}

	for _, issue := range issues {
		if _, ok := targets[issue.ID]; ok {
			if issue.Orchestration != nil {
				t.Fatalf("expected list issue %s to omit orchestration state, got %#v", issue.ID, issue.Orchestration)
			}
		}
	}
}

func TestListIssues_DoesNotIncludeStructuredOrchestrationState(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues?workspace_id="+testWorkspaceID, nil)
	testHandler.ListIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Issues []IssueResponse `json:"issues"`
		Total  int64           `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	assertListedIssuesOmitOrchestration(t, resp.Issues, fixture.childIssueID, fixture.parentIssueID)
}

func TestListOpenIssues_DoesNotIncludeStructuredOrchestrationState(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues?workspace_id="+testWorkspaceID+"&open_only=true", nil)
	testHandler.ListIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Issues []IssueResponse `json:"issues"`
		Total  int64           `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	assertListedIssuesOmitOrchestration(t, resp.Issues, fixture.childIssueID, fixture.parentIssueID)
}

func TestFinalizeParentWorkflow_OrchestratorCanFinalizeDoneChildren(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child done: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE child_spec SET status = 'done' WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child spec done: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.parentIssueID+"/workflow/finalize-parent", nil)
	req = withURLParam(req, "id", fixture.parentIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.FinalizeParentWorkflow(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if issue.Status != "done" {
		t.Fatalf("expected parent status done, got %q", issue.Status)
	}
	if issue.Orchestration == nil || issue.Orchestration.Parent == nil {
		t.Fatal("expected parent orchestration state in issue response")
	}
	if issue.Orchestration.Parent.FinalOutcome == nil || *issue.Orchestration.Parent.FinalOutcome != "complete" {
		t.Fatalf("expected parent final outcome complete, got %#v", issue.Orchestration.Parent.FinalOutcome)
	}
}

func TestFinalizeParentWorkflow_BlockedChildBlocksParent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'blocked' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child blocked: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE child_spec SET status = 'blocked' WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child spec blocked: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.parentIssueID+"/workflow/finalize-parent", nil)
	req = withURLParam(req, "id", fixture.parentIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.FinalizeParentWorkflow(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if issue.Status != "blocked" {
		t.Fatalf("expected parent status blocked, got %q", issue.Status)
	}
	if issue.Orchestration == nil || issue.Orchestration.Parent == nil {
		t.Fatal("expected parent orchestration state in issue response")
	}
	if issue.Orchestration.Parent.FinalOutcome == nil || *issue.Orchestration.Parent.FinalOutcome != "blocked" {
		t.Fatalf("expected parent final outcome blocked, got %#v", issue.Orchestration.Parent.FinalOutcome)
	}
}

func TestParentIssueResponse_ClearsFinalOutcomeAfterReopen(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child done: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE child_spec SET status = 'done' WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child spec done: %v", err)
	}

	finalize := httptest.NewRecorder()
	finalizeReq := newRequest("POST", "/api/issues/"+fixture.parentIssueID+"/workflow/finalize-parent", nil)
	finalizeReq = withURLParam(finalizeReq, "id", fixture.parentIssueID)
	finalizeReq.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	finalizeReq.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.FinalizeParentWorkflow(finalize, finalizeReq)
	if finalize.Code != http.StatusOK {
		t.Fatalf("finalize expected 200, got %d: %s", finalize.Code, finalize.Body.String())
	}

	update := httptest.NewRecorder()
	updateReq := newRequest("PUT", "/api/issues/"+fixture.parentIssueID, map[string]any{
		"status": "in_progress",
	})
	updateReq = withURLParam(updateReq, "id", fixture.parentIssueID)
	testHandler.UpdateIssue(update, updateReq)
	if update.Code != http.StatusOK {
		t.Fatalf("reopen expected 200, got %d: %s", update.Code, update.Body.String())
	}

	var issue IssueResponse
	if err := json.NewDecoder(update.Body).Decode(&issue); err != nil {
		t.Fatalf("decode reopen response: %v", err)
	}
	if issue.Status != "in_progress" {
		t.Fatalf("expected reopened parent status in_progress, got %q", issue.Status)
	}
	if issue.Orchestration == nil || issue.Orchestration.Parent == nil {
		t.Fatal("expected parent orchestration state in response")
	}
	if issue.Orchestration.Parent.FinalOutcome != nil {
		t.Fatalf("expected final outcome to be cleared after reopen, got %#v", issue.Orchestration.Parent.FinalOutcome)
	}
}

func TestBatchUpdateIssues_ClearsFinalOutcomeAfterReopen(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child done: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE child_spec SET status = 'done' WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child spec done: %v", err)
	}

	finalize := httptest.NewRecorder()
	finalizeReq := newRequest("POST", "/api/issues/"+fixture.parentIssueID+"/workflow/finalize-parent", nil)
	finalizeReq = withURLParam(finalizeReq, "id", fixture.parentIssueID)
	finalizeReq.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	finalizeReq.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.FinalizeParentWorkflow(finalize, finalizeReq)
	if finalize.Code != http.StatusOK {
		t.Fatalf("finalize expected 200, got %d: %s", finalize.Code, finalize.Body.String())
	}

	update := httptest.NewRecorder()
	updateReq := newRequest("PUT", "/api/issues/batch", map[string]any{
		"issue_ids": []string{fixture.parentIssueID},
		"updates": map[string]any{
			"status": "in_progress",
		},
	})
	testHandler.BatchUpdateIssues(update, updateReq)
	if update.Code != http.StatusOK {
		t.Fatalf("batch reopen expected 200, got %d: %s", update.Code, update.Body.String())
	}

	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(fixture.parentIssueID))
	if err != nil {
		t.Fatalf("load reopened parent: %v", err)
	}
	if issue.Status != "in_progress" {
		t.Fatalf("expected reopened parent status in_progress, got %q", issue.Status)
	}
	if issue.WorkflowFinalOutcome.Valid {
		t.Fatalf("expected final outcome to be cleared after batch reopen, got %#v", issue.WorkflowFinalOutcome)
	}
}

func TestFinalizeParentWorkflow_RejectsWhenChildStillNonTerminal(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.parentIssueID+"/workflow/finalize-parent", nil)
	req = withURLParam(req, "id", fixture.parentIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.FinalizeParentWorkflow(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFinalizeParentWorkflow_RejectsDriftedChildIssueStatus(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child issue done: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.parentIssueID+"/workflow/finalize-parent", nil)
	req = withURLParam(req, "id", fixture.parentIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.FinalizeParentWorkflow(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFinalizeParentWorkflow_RejectsUnassignedAgent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.parentIssueID+"/workflow/finalize-parent", nil)
	req = withURLParam(req, "id", fixture.parentIssueID)
	req.Header.Set("X-Agent-ID", fixture.outsiderAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.FinalizeParentWorkflow(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFinalizeParentWorkflow_RejectsWhenChildOrchestratorDiffersFromParentAssignee(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, 'Handler Alternate Orchestrator Runtime', 'local', 'claude', 'online', 'test', '{}'::jsonb, now(), $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert alternate runtime: %v", err)
	}

	var alternateOrchestratorID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Handler Alternate Orchestrator', '', 'local', '{}'::jsonb, $2, 'workspace', 10, $3)
		RETURNING id
	`, testWorkspaceID, runtimeID, testUserID).Scan(&alternateOrchestratorID); err != nil {
		t.Fatalf("insert alternate orchestrator: %v", err)
	}

	var secondChildID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_type, creator_id,
			number, position, parent_issue_id, assignee_type, assignee_id
		)
		VALUES ($1, 'Handler Second Child', 'done', 'medium', 'member', $2, 889999, 0, $3, 'agent', $4)
		RETURNING id
	`, testWorkspaceID, testUserID, fixture.parentIssueID, fixture.workerAgentID).Scan(&secondChildID); err != nil {
		t.Fatalf("insert second child: %v", err)
	}

	if _, err := testHandler.Queries.CreateChildSpec(ctx, db.CreateChildSpecParams{
		WorkspaceID:         parseUUID(testWorkspaceID),
		ParentIssueID:       parseUUID(fixture.parentIssueID),
		ChildIssueID:        parseUUID(secondChildID),
		WorkerAgentID:       parseUUID(fixture.workerAgentID),
		OrchestratorAgentID: parseUUID(alternateOrchestratorID),
	}); err != nil {
		t.Fatalf("create second child spec: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, secondChildID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, alternateOrchestratorID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})

	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1 OR id = $2`, fixture.childIssueID, secondChildID); err != nil {
		t.Fatalf("mark children done: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE child_spec SET status = 'done' WHERE child_issue_id = $1 OR child_issue_id = $2`, fixture.childIssueID, secondChildID); err != nil {
		t.Fatalf("mark child specs done: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fixture.parentIssueID+"/workflow/finalize-parent", nil)
	req = withURLParam(req, "id", fixture.parentIssueID)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.FinalizeParentWorkflow(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFinalizeParentWorkflow_SupportsIdentifierRouteParam(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child done: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE child_spec SET status = 'done' WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child spec done: %v", err)
	}

	var number int32
	if err := testPool.QueryRow(ctx, `SELECT number FROM issue WHERE id = $1`, fixture.parentIssueID).Scan(&number); err != nil {
		t.Fatalf("load parent number: %v", err)
	}
	identifier := fmt.Sprintf("HAN-%d", number)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+identifier+"/workflow/finalize-parent", nil)
	req = withURLParam(req, "id", identifier)
	req.Header.Set("X-Agent-ID", fixture.orchestratorAgentID)
	req.Header.Set("X-Task-ID", fixture.orchestratorTaskID)
	testHandler.FinalizeParentWorkflow(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestClaimTaskByRuntime_IncludesPermissionSnapshotJSON(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationHandlerFixture(t, ctx)

	snapshotJSON := `{"allowed_paths":["repo/allowed/**"],"read_only_paths":["repo/docs/**"],"blocked_paths":["repo/.git/**"],"allowed_tools":["Read","Edit"]}`
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue
		SET context = $2::jsonb
		WHERE id = $1
	`, fixture.workerChildTaskID, fmt.Sprintf(`{"permission_snapshot_json":%s}`, snapshotJSON)); err != nil {
		t.Fatalf("attach permission snapshot to task context: %v", err)
	}

	runtimeID := fixture.workerRuntimeID
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET daemon_id = $2 WHERE id = $1`, runtimeID, "orchestration-worker-daemon"); err != nil {
		t.Fatalf("attach daemon_id to worker runtime: %v", err)
	}

	rawToken := createDaemonTokenForTest(t, ctx, testWorkspaceID, "orchestration-worker-daemon")
	defer testPool.Exec(ctx, `DELETE FROM daemon_token WHERE daemon_id = $1`, "orchestration-worker-daemon")

	handler := middleware.DaemonAuth(testHandler.Queries)(http.HandlerFunc(testHandler.ClaimTaskByRuntime))
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", map[string]any{})
	req = withURLParam(req, "runtimeId", runtimeID)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Task *struct {
			ID                     string                 `json:"id"`
			PermissionSnapshot     map[string]any         `json:"permission_snapshot"`
			PermissionSnapshotJSON json.RawMessage        `json:"permission_snapshot_json"`
		} `json:"task"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Task == nil {
		t.Fatal("expected claimed task")
	}
	var wantSnapshot map[string]any
	if err := json.Unmarshal([]byte(snapshotJSON), &wantSnapshot); err != nil {
		t.Fatalf("decode expected snapshot: %v", err)
	}

	var gotSnapshot map[string]any
	if err := json.Unmarshal(resp.Task.PermissionSnapshotJSON, &gotSnapshot); err != nil {
		t.Fatalf("decode returned snapshot: %v", err)
	}

	if !reflect.DeepEqual(gotSnapshot, wantSnapshot) {
		t.Fatalf("expected permission_snapshot_json %s, got %s", snapshotJSON, string(resp.Task.PermissionSnapshotJSON))
	}
	if !reflect.DeepEqual(resp.Task.PermissionSnapshot, wantSnapshot) {
		t.Fatalf("expected permission_snapshot %#v, got %#v", wantSnapshot, resp.Task.PermissionSnapshot)
	}
}
