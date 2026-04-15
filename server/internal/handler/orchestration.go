package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type submitIssueReviewRequest struct {
	IdempotencyKey string         `json:"idempotency_key"`
	Summary        string         `json:"summary"`
	Evidence       map[string]any `json:"evidence"`
}

type reportIssueBlockedRequest struct {
	Reason string `json:"reason"`
}

type reviewIssueWorkflowCriterionRequest struct {
	CriterionID string  `json:"criterion_id"`
	Result      string  `json:"result"`
	Note        *string `json:"note"`
}

type reviewIssueWorkflowRequest struct {
	IdempotencyKey  string                                `json:"idempotency_key"`
	ReviewRoundID   string                                `json:"review_round_id"`
	Verdict         string                                `json:"verdict"`
	Summary         string                                `json:"summary"`
	CriterionResults []reviewIssueWorkflowCriterionRequest `json:"criterion_results"`
}

type replanIssueWorkflowRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	Reason         string `json:"reason"`
	PlanContent    string `json:"plan_content"`
}

func (h *Handler) requireWorkflowAgent(w http.ResponseWriter, r *http.Request, issue db.Issue) (string, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return "", false
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	if actorType != "agent" {
		writeError(w, http.StatusForbidden, "workflow command requires assigned agent actor")
		return "", false
	}
	taskID := r.Header.Get("X-Task-ID")
	if taskID == "" {
		writeError(w, http.StatusForbidden, "workflow command requires assigned agent task")
		return "", false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), parseUUID(taskID))
	taskMatchesIssue := err == nil && uuidToString(task.IssueID) == uuidToString(issue.ID)
	if !taskMatchesIssue && err == nil && issue.ParentIssueID.Valid {
		taskMatchesIssue = uuidToString(task.IssueID) == uuidToString(issue.ParentIssueID)
	}
	taskIsActive := err == nil && (task.Status == "queued" || task.Status == "dispatched" || task.Status == "running")
	if err != nil || uuidToString(task.AgentID) != actorID || !taskMatchesIssue || !taskIsActive {
		writeError(w, http.StatusForbidden, "workflow command requires assigned agent task")
		return "", false
	}
	return actorID, true
}

func (h *Handler) workflowIssueResponse(w http.ResponseWriter, r *http.Request, issue db.Issue) {
	writeJSON(w, http.StatusOK, h.issueResponse(r.Context(), issue))
}

func mapWorkflowError(err error) (int, string) {
	if errors.Is(err, pgx.ErrNoRows) {
		return http.StatusNotFound, "workflow state not found"
	}
	if errors.Is(err, service.ErrFinalizeParentUnauthorized) {
		return http.StatusForbidden, err.Error()
	}
	if errors.Is(err, service.ErrFinalizeParentNoChildren) ||
		errors.Is(err, service.ErrFinalizeParentStateInconsistent) ||
		errors.Is(err, service.ErrFinalizeParentChildrenNotReady) {
		return http.StatusConflict, err.Error()
	}

	msg := err.Error()
	if strings.Contains(msg, "submitted review round not found") ||
		strings.Contains(msg, "acceptance criterion not found") {
		return http.StatusNotFound, msg
	}
	if strings.Contains(msg, "review round id is required") ||
		strings.Contains(msg, "unsupported review verdict") ||
		strings.Contains(msg, "invalid criterion result") ||
		strings.Contains(msg, "criterion does not belong") ||
		strings.Contains(msg, "duplicate criterion id") ||
		strings.Contains(msg, "idempotency key already used for different review round") {
		return http.StatusBadRequest, msg
	}
	if strings.Contains(msg, "child is already awaiting review") ||
		strings.Contains(msg, "child is not awaiting review") ||
		strings.Contains(msg, "child is terminal and cannot") ||
		strings.Contains(msg, "submitted review round already completed") {
		return http.StatusConflict, msg
	}
	if strings.Contains(msg, "is not the worker for child issue") ||
		strings.Contains(msg, "is not the orchestrator for child issue") ||
		strings.Contains(msg, "is not assigned to child issue") {
		return http.StatusForbidden, msg
	}
	return http.StatusInternalServerError, "internal server error"
}

func (h *Handler) SubmitIssueReview(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	agentID, ok := h.requireWorkflowAgent(w, r, issue)
	if !ok {
		return
	}
	var req submitIssueReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if h.OrchestrationService == nil {
		writeError(w, http.StatusInternalServerError, "orchestration service unavailable")
		return
	}
	evidenceJSON, err := json.Marshal(req.Evidence)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid evidence payload")
		return
	}
	_, err = h.OrchestrationService.SubmitReview(r.Context(), service.SubmitReviewParams{
		ChildIssueID:      issue.ID,
		SubmitterAgentID:  parseUUID(agentID),
		SubmissionSummary: ptrToText(&req.Summary),
		IdempotencyKey:    req.IdempotencyKey,
		EvidenceJSON:      evidenceJSON,
	})
	if err != nil {
		status, msg := mapWorkflowError(err)
		writeError(w, status, msg)
		return
	}
	updated, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workflow issue")
		return
	}

	h.workflowIssueResponse(w, r, updated)
}

func (h *Handler) ReportIssueBlocked(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	agentID, ok := h.requireWorkflowAgent(w, r, issue)
	if !ok {
		return
	}
	var req reportIssueBlockedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}
	if h.OrchestrationService == nil {
		writeError(w, http.StatusInternalServerError, "orchestration service unavailable")
		return
	}
	_, err := h.OrchestrationService.ReportBlocked(r.Context(), service.ReportBlockedParams{
		ChildIssueID:    issue.ID,
		ReporterAgentID: parseUUID(agentID),
		Reason:          reason,
	})
	if err != nil {
		status, msg := mapWorkflowError(err)
		writeError(w, status, msg)
		return
	}
	updated, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workflow issue")
		return
	}

	h.workflowIssueResponse(w, r, updated)
}

func (h *Handler) ReviewIssueWorkflow(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	agentID, ok := h.requireWorkflowAgent(w, r, issue)
	if !ok {
		return
	}
	var req reviewIssueWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ReviewRoundID == "" {
		writeError(w, http.StatusBadRequest, "review_round_id is required")
		return
	}
	if req.Verdict == "" {
		writeError(w, http.StatusBadRequest, "verdict is required")
		return
	}
	criteria := make([]service.CriterionVerdict, 0, len(req.CriterionResults))
	for _, criterion := range req.CriterionResults {
		if criterion.CriterionID == "" {
			writeError(w, http.StatusBadRequest, "criterion_id is required")
			return
		}
		if criterion.Result != service.CriterionResultPass && criterion.Result != service.CriterionResultFail && criterion.Result != service.CriterionResultNotApplicable {
			writeError(w, http.StatusBadRequest, "invalid criterion result")
			return
		}
		criteria = append(criteria, service.CriterionVerdict{
			CriterionID: parseUUID(criterion.CriterionID),
			Result:      criterion.Result,
			Note:        ptrToText(criterion.Note),
		})
	}
	if h.OrchestrationService == nil {
		writeError(w, http.StatusInternalServerError, "orchestration service unavailable")
		return
	}
	reviewSummary := strings.TrimSpace(req.Summary)
	_, err := h.OrchestrationService.Review(r.Context(), service.ReviewParams{
		ChildIssueID:     issue.ID,
		ReviewRoundID:    parseUUID(req.ReviewRoundID),
		ReviewerAgentID:  parseUUID(agentID),
		IdempotencyKey:   req.IdempotencyKey,
		Verdict:          req.Verdict,
		Summary:          strToText(reviewSummary),
		CriterionResults: criteria,
	})
	if err != nil {
		status, msg := mapWorkflowError(err)
		writeError(w, status, msg)
		return
	}
	updated, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workflow issue")
		return
	}

	h.workflowIssueResponse(w, r, updated)
}

func (h *Handler) ReplanIssueWorkflow(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	agentID, ok := h.requireWorkflowAgent(w, r, issue)
	if !ok {
		return
	}
	var req replanIssueWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if h.OrchestrationService == nil {
		writeError(w, http.StatusInternalServerError, "orchestration service unavailable")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	planContent := strings.TrimSpace(req.PlanContent)
	_, err := h.OrchestrationService.CreatePlanRevision(r.Context(), service.CreatePlanRevisionParams{
		ChildIssueID:       issue.ID,
		RequestedByAgentID: parseUUID(agentID),
		Reason:             strToText(reason),
		PlanContent:        strToText(planContent),
		IdempotencyKey:     req.IdempotencyKey,
	})
	if err != nil {
		status, msg := mapWorkflowError(err)
		writeError(w, status, msg)
		return
	}
	updated, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workflow issue")
		return
	}

	h.workflowIssueResponse(w, r, updated)
}

func (h *Handler) FinalizeParentWorkflow(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	agentID, ok := h.requireWorkflowAgent(w, r, issue)
	if !ok {
		return
	}
	if h.OrchestrationService == nil {
		writeError(w, http.StatusInternalServerError, "orchestration service unavailable")
		return
	}

	_, err := h.OrchestrationService.FinalizeParent(r.Context(), service.FinalizeParentParams{
		ParentIssueID:         issue.ID,
		OrchestratorAgentID:   parseUUID(agentID),
		RequireTerminalChilds: true,
	})
	if err != nil {
		status, msg := mapWorkflowError(err)
		writeError(w, status, msg)
		return
	}

	updated, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workflow issue")
		return
	}

	h.workflowIssueResponse(w, r, updated)
}
