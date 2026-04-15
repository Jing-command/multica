package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type OrchestrationService struct {
	DB      *pgxpool.Pool
	Queries *db.Queries
	Hub     *realtime.Hub
	Bus     *events.Bus
}

type FinalizeParentParams struct {
	ParentIssueID         pgtype.UUID
	OrchestratorAgentID   pgtype.UUID
	RequireTerminalChilds bool
}

type FinalizeParentResult struct {
	ParentIssueID string
	FinalOutcome  string
	Status        string
}

var (
	ErrFinalizeParentUnauthorized      = errors.New("only the parent orchestrator can finalize this parent issue")
	ErrFinalizeParentNoChildren        = errors.New("parent issue has no child issues to finalize")
	ErrFinalizeParentStateInconsistent = errors.New("parent orchestration state inconsistent")
	ErrFinalizeParentChildrenNotReady  = errors.New("all child issues must be terminal before finalizing parent")
)

func NewOrchestrationService(dbPool *pgxpool.Pool, queries *db.Queries, hub *realtime.Hub, bus *events.Bus) *OrchestrationService {
	return &OrchestrationService{
		DB:      dbPool,
		Queries: queries,
		Hub:     hub,
		Bus:     bus,
	}
}

type SubmitReviewParams struct {
	ChildIssueID      pgtype.UUID
	SubmitterAgentID  pgtype.UUID
	SubmissionSummary pgtype.Text
	IdempotencyKey    string
	EvidenceJSON      []byte
}

func (s *OrchestrationService) SubmitReview(ctx context.Context, params SubmitReviewParams) (db.ChildReviewRound, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return db.ChildReviewRound{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	queries := s.Queries.WithTx(tx)
	childSpec, err := queries.GetChildSpecByIssueIDForUpdate(ctx, params.ChildIssueID)
	if err != nil {
		return db.ChildReviewRound{}, fmt.Errorf("get child spec by issue id for update: %w", err)
	}
	if childSpec.WorkerAgentID != params.SubmitterAgentID {
		return db.ChildReviewRound{}, fmt.Errorf("submitter agent is not the worker for child issue")
	}

	idempotencyKey := pgtype.Text{}
	if params.IdempotencyKey != "" {
		idempotencyKey = pgtype.Text{String: params.IdempotencyKey, Valid: true}
		existingRound, getErr := queries.GetChildReviewRoundByIdempotencyKey(ctx, db.GetChildReviewRoundByIdempotencyKeyParams{
			ChildSpecID:    childSpec.ID,
			IdempotencyKey: idempotencyKey,
		})
		if getErr == nil {
			if err := tx.Commit(ctx); err != nil {
				return db.ChildReviewRound{}, fmt.Errorf("commit transaction: %w", err)
			}
			return existingRound, nil
		}
		if !errors.Is(getErr, pgx.ErrNoRows) {
			return db.ChildReviewRound{}, fmt.Errorf("get child review round by idempotency key: %w", getErr)
		}
	}
	if childSpec.Status == "done" || childSpec.Status == "blocked" {
		return db.ChildReviewRound{}, fmt.Errorf("child is terminal and cannot be submitted for review")
	}
	if childSpec.Status == "awaiting_review" {
		return db.ChildReviewRound{}, fmt.Errorf("child is already awaiting review")
	}

	submissionEvidence := params.EvidenceJSON
	if len(submissionEvidence) == 0 {
		submissionEvidence = []byte(`{}`)
	}

	reviewRound, err := queries.CreateChildReviewRoundForSubmission(ctx, db.CreateChildReviewRoundForSubmissionParams{
		ChildSpecID:        childSpec.ID,
		ReviewerAgentID:    childSpec.OrchestratorAgentID,
		Summary:            params.SubmissionSummary,
		IdempotencyKey:     idempotencyKey,
		SubmissionEvidence: submissionEvidence,
	})
	if err != nil {
		return db.ChildReviewRound{}, fmt.Errorf("create child review round submission: %w", err)
	}
	if _, err := queries.UpdateChildSpecStatus(ctx, db.UpdateChildSpecStatusParams{ID: childSpec.ID, Status: "awaiting_review"}); err != nil {
		return db.ChildReviewRound{}, fmt.Errorf("mark child spec awaiting review: %w", err)
	}
	if _, err := queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: params.ChildIssueID, Status: "in_review"}); err != nil {
		return db.ChildReviewRound{}, fmt.Errorf("mark child issue in review: %w", err)
	}
	if err := s.enqueueParentIfNeeded(ctx, tx, queries, childSpec); err != nil {
		return db.ChildReviewRound{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.ChildReviewRound{}, fmt.Errorf("commit transaction: %w", err)
	}
	if err := s.publishIssueUpdated(ctx, params.ChildIssueID, params.SubmitterAgentID); err != nil {
		return db.ChildReviewRound{}, err
	}

	return reviewRound, nil
}

type CreatePlanRevisionParams struct {
	ChildIssueID       pgtype.UUID
	RequestedByAgentID pgtype.UUID
	Reason             pgtype.Text
	PlanContent        pgtype.Text
	IdempotencyKey     string
}

func (s *OrchestrationService) CreatePlanRevision(ctx context.Context, params CreatePlanRevisionParams) (db.PlanRevision, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return db.PlanRevision{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	queries := s.Queries.WithTx(tx)
	childSpec, err := queries.GetChildSpecByIssueIDForUpdate(ctx, params.ChildIssueID)
	if err != nil {
		return db.PlanRevision{}, fmt.Errorf("get child spec by issue id for update: %w", err)
	}
	if childSpec.OrchestratorAgentID != params.RequestedByAgentID {
		return db.PlanRevision{}, fmt.Errorf("requesting agent is not the orchestrator for child issue")
	}
	if childSpec.Status == "done" || childSpec.Status == "blocked" {
		return db.PlanRevision{}, fmt.Errorf("child is terminal and cannot be replanned")
	}

	idempotencyKey := pgtype.Text{}
	if params.IdempotencyKey != "" {
		idempotencyKey = pgtype.Text{String: params.IdempotencyKey, Valid: true}
		existingRevision, getErr := queries.GetPlanRevisionByIdempotencyKey(ctx, db.GetPlanRevisionByIdempotencyKeyParams{
			ChildSpecID:    childSpec.ID,
			IdempotencyKey: idempotencyKey,
		})
		if getErr == nil {
			if err := tx.Commit(ctx); err != nil {
				return db.PlanRevision{}, fmt.Errorf("commit transaction: %w", err)
			}
			return existingRevision, nil
		}
		if !errors.Is(getErr, pgx.ErrNoRows) {
			return db.PlanRevision{}, fmt.Errorf("get plan revision by idempotency key: %w", getErr)
		}
	}

	revision, err := queries.CreatePlanRevision(ctx, db.CreatePlanRevisionParams{
		ChildSpecID:        childSpec.ID,
		RequestedByAgentID: params.RequestedByAgentID,
		Reason:             params.Reason,
		PlanContent:        params.PlanContent,
		IdempotencyKey:     idempotencyKey,
	})
	if err != nil {
		return db.PlanRevision{}, fmt.Errorf("create plan revision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.PlanRevision{}, fmt.Errorf("commit transaction: %w", err)
	}
	if s.Bus != nil {
		s.Bus.Publish(events.Event{
			Type:        protocol.EventChildReplanned,
			WorkspaceID: util.UUIDToString(childSpec.WorkspaceID),
			ActorType:   "agent",
			ActorID:     util.UUIDToString(params.RequestedByAgentID),
			Payload: map[string]any{
				"child_issue_id": util.UUIDToString(params.ChildIssueID),
				"revision_id":    util.UUIDToString(revision.ID),
			},
		})
	}

	return revision, nil
}

const (
	ReviewDecisionApproved         = "approved"
	ReviewDecisionChangesRequested = "changes_requested"
	ReviewDecisionBlocked          = "blocked"

	CriterionResultPass          = "pass"
	CriterionResultFail          = "fail"
	CriterionResultNotApplicable = "not_applicable"
)

type CriterionVerdict struct {
	CriterionID pgtype.UUID
	Result      string
	Note        pgtype.Text
}

type ReviewParams struct {
	ChildIssueID      pgtype.UUID
	ReviewRoundID     pgtype.UUID
	ReviewerAgentID   pgtype.UUID
	IdempotencyKey    string
	Verdict           string
	Summary           pgtype.Text
	CriterionResults  []CriterionVerdict
}

type ReviewResult struct {
	Round           db.ChildReviewRound
	CriteriaResults []db.ChildReviewCriterionResult
	Escalation      *db.ChildEscalation
	Idempotent      bool
}

func (s *OrchestrationService) Review(ctx context.Context, params ReviewParams) (ReviewResult, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	queries := s.Queries.WithTx(tx)
	childSpec, err := queries.GetChildSpecByIssueIDForUpdate(ctx, params.ChildIssueID)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("get child spec by issue id for update: %w", err)
	}
	if childSpec.OrchestratorAgentID != params.ReviewerAgentID {
		return ReviewResult{}, fmt.Errorf("reviewer agent is not the orchestrator for child issue")
	}
	if !params.ReviewRoundID.Valid {
		return ReviewResult{}, fmt.Errorf("review round id is required")
	}
	if params.IdempotencyKey != "" {
		existingCompletion, getErr := queries.GetChildReviewCompletionByIdempotencyKey(ctx, db.GetChildReviewCompletionByIdempotencyKeyParams{
			ChildSpecID:    childSpec.ID,
			IdempotencyKey: params.IdempotencyKey,
		})
		if getErr == nil {
			if existingCompletion.ReviewRoundID != params.ReviewRoundID {
				return ReviewResult{}, fmt.Errorf("idempotency key already used for different review round")
			}
			result, err := loadIdempotentReviewResult(ctx, queries, existingCompletion)
			if err != nil {
				return ReviewResult{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return ReviewResult{}, fmt.Errorf("commit review replay transaction: %w", err)
			}
			return result, nil
		}
		if !errors.Is(getErr, pgx.ErrNoRows) {
			return ReviewResult{}, fmt.Errorf("get child review completion by idempotency key: %w", getErr)
		}
	}
	if childSpec.Status != "awaiting_review" {
		return ReviewResult{}, fmt.Errorf("child is not awaiting review")
	}
	if params.Verdict != ReviewDecisionApproved && params.Verdict != ReviewDecisionChangesRequested && params.Verdict != ReviewDecisionBlocked {
		return ReviewResult{}, fmt.Errorf("unsupported review verdict: %s", params.Verdict)
	}

	var pendingRound db.ChildReviewRound
	if err := tx.QueryRow(ctx, `
		SELECT id, child_spec_id, round_number, reviewer_agent_id, decision, summary, created_at, idempotency_key, submission_evidence
		FROM child_review_round
		WHERE id = $1 AND child_spec_id = $2
		FOR UPDATE
	`, params.ReviewRoundID, childSpec.ID).Scan(
		&pendingRound.ID,
		&pendingRound.ChildSpecID,
		&pendingRound.RoundNumber,
		&pendingRound.ReviewerAgentID,
		&pendingRound.Decision,
		&pendingRound.Summary,
		&pendingRound.CreatedAt,
		&pendingRound.IdempotencyKey,
		&pendingRound.SubmissionEvidence,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ReviewResult{}, fmt.Errorf("submitted review round not found")
		}
		return ReviewResult{}, fmt.Errorf("load submitted review round: %w", err)
	}
	if pendingRound.Decision != "submitted" {
		return ReviewResult{}, fmt.Errorf("submitted review round already completed")
	}

	round, err := queries.CompleteChildReviewRound(ctx, db.CompleteChildReviewRoundParams{
		ID:              pendingRound.ID,
		ReviewerAgentID: params.ReviewerAgentID,
		Decision:        params.Verdict,
		Summary:         params.Summary,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ReviewResult{}, fmt.Errorf("submitted review round already completed")
		}
		return ReviewResult{}, fmt.Errorf("complete child review round: %w", err)
	}

	criteriaResults := make([]db.ChildReviewCriterionResult, 0, len(params.CriterionResults))
	for _, criterion := range params.CriterionResults {
		if criterion.Result != CriterionResultPass && criterion.Result != CriterionResultFail && criterion.Result != CriterionResultNotApplicable {
			return ReviewResult{}, fmt.Errorf("invalid criterion result: %s", criterion.Result)
		}
		acceptanceCriterion, getErr := queries.GetChildAcceptanceCriterion(ctx, criterion.CriterionID)
		if getErr != nil {
			if errors.Is(getErr, pgx.ErrNoRows) {
				return ReviewResult{}, fmt.Errorf("acceptance criterion not found")
			}
			return ReviewResult{}, fmt.Errorf("get child acceptance criterion: %w", getErr)
		}
		if acceptanceCriterion.ChildSpecID != childSpec.ID {
			return ReviewResult{}, fmt.Errorf("criterion does not belong to child spec")
		}
		criterionResult, createErr := queries.CreateChildReviewCriterionResult(ctx, db.CreateChildReviewCriterionResultParams{
			ReviewRoundID: round.ID,
			CriterionID:   criterion.CriterionID,
			Result:        criterion.Result,
			Note:          criterion.Note,
		})
		if createErr != nil {
			return ReviewResult{}, fmt.Errorf("create child review criterion result: %w", createErr)
		}
		criteriaResults = append(criteriaResults, criterionResult)
	}

	result := ReviewResult{Round: round, CriteriaResults: criteriaResults}
	summaryReason := "review escalation"
	if params.Summary.Valid {
		summaryReason = params.Summary.String
	}

	var escalationID pgtype.UUID
	switch params.Verdict {
	case ReviewDecisionApproved:
		if _, err := queries.UpdateChildSpecStatus(ctx, db.UpdateChildSpecStatusParams{ID: childSpec.ID, Status: "done"}); err != nil {
			return ReviewResult{}, fmt.Errorf("mark child spec done: %w", err)
		}
		if _, err := queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: params.ChildIssueID, Status: "done"}); err != nil {
			return ReviewResult{}, fmt.Errorf("mark child issue done: %w", err)
		}
		if err := s.enqueueParentIfNeeded(ctx, tx, queries, childSpec); err != nil {
			return ReviewResult{}, err
		}
	case ReviewDecisionChangesRequested:
		if round.RoundNumber >= childSpec.MaxReviewRounds {
			escalation, createErr := queries.CreateChildEscalation(ctx, db.CreateChildEscalationParams{
				ChildSpecID:     childSpec.ID,
				RaisedByAgentID: params.ReviewerAgentID,
				Reason:          summaryReason,
			})
			if createErr != nil {
				return ReviewResult{}, fmt.Errorf("create escalation after max rounds: %w", createErr)
			}
			result.Escalation = &escalation
			escalationID = escalation.ID
			if _, err := queries.UpdateChildSpecStatus(ctx, db.UpdateChildSpecStatusParams{ID: childSpec.ID, Status: "blocked"}); err != nil {
				return ReviewResult{}, fmt.Errorf("mark child spec blocked after escalation: %w", err)
			}
			if _, err := queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: params.ChildIssueID, Status: "blocked"}); err != nil {
				return ReviewResult{}, fmt.Errorf("mark child issue blocked after escalation: %w", err)
			}
			if err := s.enqueueParentIfNeeded(ctx, tx, queries, childSpec); err != nil {
				return ReviewResult{}, err
			}
		} else {
			if _, err := queries.UpdateChildSpecStatus(ctx, db.UpdateChildSpecStatusParams{ID: childSpec.ID, Status: "in_progress"}); err != nil {
				return ReviewResult{}, fmt.Errorf("reopen child spec after changes requested: %w", err)
			}
			if _, err := queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: params.ChildIssueID, Status: "in_progress"}); err != nil {
				return ReviewResult{}, fmt.Errorf("reopen child issue after changes requested: %w", err)
			}
		}
	case ReviewDecisionBlocked:
		escalation, createErr := queries.CreateChildEscalation(ctx, db.CreateChildEscalationParams{
			ChildSpecID:     childSpec.ID,
			RaisedByAgentID: params.ReviewerAgentID,
			Reason:          summaryReason,
		})
		if createErr != nil {
			return ReviewResult{}, fmt.Errorf("create escalation for blocked review: %w", createErr)
		}
		result.Escalation = &escalation
		escalationID = escalation.ID
		if _, err := queries.UpdateChildSpecStatus(ctx, db.UpdateChildSpecStatusParams{ID: childSpec.ID, Status: "blocked"}); err != nil {
			return ReviewResult{}, fmt.Errorf("mark child spec blocked: %w", err)
		}
		if _, err := queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: params.ChildIssueID, Status: "blocked"}); err != nil {
			return ReviewResult{}, fmt.Errorf("mark child issue blocked: %w", err)
		}
		if err := s.enqueueParentIfNeeded(ctx, tx, queries, childSpec); err != nil {
			return ReviewResult{}, err
		}
	default:
		return ReviewResult{}, fmt.Errorf("unsupported review verdict: %s", params.Verdict)
	}

	if params.IdempotencyKey != "" {
		if _, err := queries.CreateChildReviewCompletion(ctx, db.CreateChildReviewCompletionParams{
			ChildSpecID:    childSpec.ID,
			ReviewRoundID:  round.ID,
			IdempotencyKey: params.IdempotencyKey,
			EscalationID:   escalationID,
		}); err != nil {
			return ReviewResult{}, fmt.Errorf("create child review completion: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ReviewResult{}, fmt.Errorf("commit review transaction: %w", err)
	}
	if err := s.publishIssueUpdated(ctx, params.ChildIssueID, params.ReviewerAgentID); err != nil {
		return ReviewResult{}, err
	}
	return result, nil
}

func loadIdempotentReviewResult(ctx context.Context, queries *db.Queries, completion db.ChildReviewCompletion) (ReviewResult, error) {
	round, err := queries.GetChildReviewRound(ctx, completion.ReviewRoundID)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("load replayed child review round: %w", err)
	}
	criteriaResults, err := queries.ListChildReviewCriterionResultsByRound(ctx, round.ID)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("load replayed child review criterion results: %w", err)
	}
	result := ReviewResult{
		Round:           round,
		CriteriaResults: criteriaResults,
		Idempotent:      true,
	}
	if completion.EscalationID.Valid {
		escalation, err := queries.GetChildEscalation(ctx, completion.EscalationID)
		if err != nil {
			return ReviewResult{}, fmt.Errorf("load replayed child escalation: %w", err)
		}
		result.Escalation = &escalation
	}
	return result, nil
}

type ReportBlockedParams struct {
	ChildIssueID    pgtype.UUID
	ReporterAgentID pgtype.UUID
	Reason          string
}

type ReportBlockedResult struct {
	Escalation db.ChildEscalation
	Idempotent bool
}

func (s *OrchestrationService) ReportBlocked(ctx context.Context, params ReportBlockedParams) (ReportBlockedResult, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return ReportBlockedResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	queries := s.Queries.WithTx(tx)
	childSpec, err := queries.GetChildSpecByIssueIDForUpdate(ctx, params.ChildIssueID)
	if err != nil {
		return ReportBlockedResult{}, fmt.Errorf("get child spec by issue id for update: %w", err)
	}
	if childSpec.WorkerAgentID != params.ReporterAgentID && childSpec.OrchestratorAgentID != params.ReporterAgentID {
		return ReportBlockedResult{}, fmt.Errorf("reporter agent is not assigned to child issue")
	}
	if childSpec.Status == "done" || childSpec.Status == "blocked" {
		return ReportBlockedResult{}, fmt.Errorf("child is terminal and cannot be blocked")
	}

	escalation, err := queries.CreateChildEscalation(ctx, db.CreateChildEscalationParams{
		ChildSpecID:     childSpec.ID,
		RaisedByAgentID: params.ReporterAgentID,
		Reason:          params.Reason,
	})
	if err != nil {
		return ReportBlockedResult{}, fmt.Errorf("create child escalation: %w", err)
	}
	if _, err := queries.UpdateChildSpecStatus(ctx, db.UpdateChildSpecStatusParams{ID: childSpec.ID, Status: "blocked"}); err != nil {
		return ReportBlockedResult{}, fmt.Errorf("mark child spec blocked: %w", err)
	}
	if _, err := queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: params.ChildIssueID, Status: "blocked"}); err != nil {
		return ReportBlockedResult{}, fmt.Errorf("mark child issue blocked: %w", err)
	}

	if err := s.enqueueParentIfNeeded(ctx, tx, queries, childSpec); err != nil {
		return ReportBlockedResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ReportBlockedResult{}, fmt.Errorf("commit blocked transaction: %w", err)
	}
	if s.Bus != nil {
		s.Bus.Publish(events.Event{
			Type:        protocol.EventChildBlockedReported,
			WorkspaceID: util.UUIDToString(childSpec.WorkspaceID),
			ActorType:   "agent",
			ActorID:     util.UUIDToString(params.ReporterAgentID),
			Payload: map[string]any{
				"child_issue_id": util.UUIDToString(params.ChildIssueID),
				"escalation_id": util.UUIDToString(escalation.ID),
			},
		})
	}
	if err := s.publishIssueUpdated(ctx, params.ChildIssueID, params.ReporterAgentID); err != nil {
		return ReportBlockedResult{}, err
	}
	return ReportBlockedResult{Escalation: escalation}, nil
}

func (s *OrchestrationService) FinalizeParent(ctx context.Context, params FinalizeParentParams) (FinalizeParentResult, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return FinalizeParentResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	queries := s.Queries.WithTx(tx)
	var parentIssue db.Issue
	if err := tx.QueryRow(ctx, `
		SELECT id, workspace_id, title, description, status, priority, assignee_type, assignee_id,
		       creator_type, creator_id, parent_issue_id, acceptance_criteria, context_refs, position,
		       due_date, created_at, updated_at, number, workflow_final_outcome
		FROM issue
		WHERE id = $1
		FOR UPDATE
	`, params.ParentIssueID).Scan(
		&parentIssue.ID,
		&parentIssue.WorkspaceID,
		&parentIssue.Title,
		&parentIssue.Description,
		&parentIssue.Status,
		&parentIssue.Priority,
		&parentIssue.AssigneeType,
		&parentIssue.AssigneeID,
		&parentIssue.CreatorType,
		&parentIssue.CreatorID,
		&parentIssue.ParentIssueID,
		&parentIssue.AcceptanceCriteria,
		&parentIssue.ContextRefs,
		&parentIssue.Position,
		&parentIssue.DueDate,
		&parentIssue.CreatedAt,
		&parentIssue.UpdatedAt,
		&parentIssue.Number,
		&parentIssue.WorkflowFinalOutcome,
	); err != nil {
		return FinalizeParentResult{}, fmt.Errorf("lock parent issue: %w", err)
	}
	if !parentIssue.AssigneeType.Valid || parentIssue.AssigneeType.String != "agent" || !parentIssue.AssigneeID.Valid || parentIssue.AssigneeID != params.OrchestratorAgentID {
		return FinalizeParentResult{}, ErrFinalizeParentUnauthorized
	}

	children, err := queries.ListChildIssues(ctx, parentIssue.ID)
	if err != nil {
		return FinalizeParentResult{}, fmt.Errorf("load child issues: %w", err)
	}
	if len(children) == 0 {
		return FinalizeParentResult{}, ErrFinalizeParentNoChildren
	}

	finalStatus := "done"
	finalOutcome := "complete"
	for _, child := range children {
		childSpec, specErr := queries.GetChildSpecByIssueID(ctx, child.ID)
		if specErr != nil {
			if errors.Is(specErr, pgx.ErrNoRows) {
				return FinalizeParentResult{}, ErrFinalizeParentStateInconsistent
			}
			return FinalizeParentResult{}, fmt.Errorf("load child orchestration state: %w", specErr)
		}
		if childSpec.OrchestratorAgentID != params.OrchestratorAgentID {
			return FinalizeParentResult{}, ErrFinalizeParentStateInconsistent
		}

		specTerminal := childSpec.Status == "done" || childSpec.Status == "blocked"
		issueTerminal := child.Status == "done" || child.Status == "blocked"
		if specTerminal != issueTerminal {
			return FinalizeParentResult{}, ErrFinalizeParentStateInconsistent
		}
		if specTerminal && child.Status != childSpec.Status {
			return FinalizeParentResult{}, ErrFinalizeParentStateInconsistent
		}
		if !specTerminal {
			if params.RequireTerminalChilds {
				return FinalizeParentResult{}, ErrFinalizeParentChildrenNotReady
			}
			continue
		}
		if childSpec.Status == "blocked" {
			finalStatus = "blocked"
			finalOutcome = "blocked"
		}
	}

	if _, err := queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: parentIssue.ID, Status: finalStatus}); err != nil {
		return FinalizeParentResult{}, fmt.Errorf("finalize parent issue: %w", err)
	}
	if _, err := queries.UpdateIssueWorkflowFinalOutcome(ctx, db.UpdateIssueWorkflowFinalOutcomeParams{
		ID:                   parentIssue.ID,
		WorkflowFinalOutcome: pgtype.Text{String: finalOutcome, Valid: true},
	}); err != nil {
		return FinalizeParentResult{}, fmt.Errorf("persist parent final outcome: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return FinalizeParentResult{}, fmt.Errorf("commit transaction: %w", err)
	}
	if s.Bus != nil {
		s.Bus.Publish(events.Event{
			Type:        protocol.EventParentFinalized,
			WorkspaceID: util.UUIDToString(parentIssue.WorkspaceID),
			ActorType:   "agent",
			ActorID:     util.UUIDToString(params.OrchestratorAgentID),
			Payload: map[string]any{
				"parent_issue_id": util.UUIDToString(parentIssue.ID),
				"final_outcome":   finalOutcome,
				"status":          finalStatus,
			},
		})
	}
	if err := s.publishIssueUpdated(ctx, parentIssue.ID, params.OrchestratorAgentID); err != nil {
		return FinalizeParentResult{}, err
	}

	return FinalizeParentResult{
		ParentIssueID: util.UUIDToString(parentIssue.ID),
		FinalOutcome:  finalOutcome,
		Status:        finalStatus,
	}, nil
}

func (s *OrchestrationService) enqueueParentIfNeeded(ctx context.Context, tx pgx.Tx, queries *db.Queries, childSpec db.ChildSpec) error {
	if !childSpec.ParentIssueID.Valid {
		return nil
	}
	return s.enqueueIssueAgentIfNeeded(ctx, tx, queries, childSpec.ParentIssueID, true)
}

func (s *OrchestrationService) enqueueReviewerIfNeeded(ctx context.Context, tx pgx.Tx, queries *db.Queries, childSpec db.ChildSpec) error {
	return s.enqueueAgentTaskIfNeeded(ctx, tx, queries, childSpec.ChildIssueID, childSpec.OrchestratorAgentID, true)
}

func (s *OrchestrationService) enqueueIssueAgentIfNeeded(ctx context.Context, tx pgx.Tx, queries *db.Queries, issueID pgtype.UUID, suppressWhileRunning bool) error {
	issue, err := queries.GetIssue(ctx, issueID)
	if err != nil {
		return fmt.Errorf("load issue: %w", err)
	}
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		return nil
	}
	return s.enqueueAgentTaskIfNeeded(ctx, tx, queries, issue.ID, issue.AssigneeID, suppressWhileRunning)
}

func (s *OrchestrationService) enqueueAgentTaskIfNeeded(ctx context.Context, tx pgx.Tx, queries *db.Queries, issueID pgtype.UUID, agentID pgtype.UUID, suppressWhileRunning bool) error {
	var lockedIssueID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM issue WHERE id = $1 FOR UPDATE`, issueID).Scan(&lockedIssueID); err != nil {
		return fmt.Errorf("lock issue: %w", err)
	}

	issue, err := queries.GetIssue(ctx, issueID)
	if err != nil {
		return fmt.Errorf("load issue: %w", err)
	}
	if suppressWhileRunning {
		hasActive, err := queries.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{IssueID: issue.ID, AgentID: agentID})
		if err != nil {
			return fmt.Errorf("check active issue task: %w", err)
		}
		if hasActive {
			return nil
		}
	} else {
		hasPending, err := queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{IssueID: issue.ID, AgentID: agentID})
		if err != nil {
			return fmt.Errorf("check pending issue task: %w", err)
		}
		if hasPending {
			return nil
		}
	}

	agent, err := queries.GetAgent(ctx, agentID)
	if err != nil {
		return fmt.Errorf("load issue agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return nil
	}
	if !agent.RuntimeID.Valid {
		return fmt.Errorf("issue agent has no runtime")
	}

	if _, err := queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:   agentID,
		RuntimeID: agent.RuntimeID,
		IssueID:   issue.ID,
		Priority:  0,
	}); err != nil {
		if isUniqueViolation(err) {
			return nil
		}
		return fmt.Errorf("create issue task: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *OrchestrationService) publishIssueUpdated(ctx context.Context, issueID, actorID pgtype.UUID) error {
	if s.Bus == nil {
		return nil
	}
	issue, err := s.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return nil
	}
	ws, err := s.Queries.GetWorkspace(ctx, issue.WorkspaceID)
	if err != nil {
		return nil
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "agent",
		ActorID:     util.UUIDToString(actorID),
		Payload: map[string]any{
			"issue": issueToMap(issue, ws.IssuePrefix),
		},
	})
	return nil
}
