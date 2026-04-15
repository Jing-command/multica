package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (s *OrchestrationService) RecoverStuckWorkflows(ctx context.Context) error {
	childSpecs, err := s.listRecoverableChildSpecs(ctx)
	if err != nil {
		return err
	}

	recoveryErrors := make([]error, 0)
	for _, childSpec := range childSpecs {
		if err := s.recoverChildSpec(ctx, childSpec); err != nil {
			recoveryErrors = append(recoveryErrors, err)
		}
	}
	if len(recoveryErrors) > 0 {
		return errors.Join(recoveryErrors...)
	}

	return nil
}

func (s *OrchestrationService) recoverChildSpec(ctx context.Context, childSpec db.ChildSpec) error {
	tx, beginErr := s.DB.Begin(ctx)
	if beginErr != nil {
		return fmt.Errorf("begin recovery transaction for child %s: %w", util.UUIDToString(childSpec.ChildIssueID), beginErr)
	}

	queries := s.Queries.WithTx(tx)
	lockedChildSpec, lockErr := queries.GetChildSpecByIssueIDForUpdate(ctx, childSpec.ChildIssueID)
	if lockErr != nil {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
			return errors.Join(
				fmt.Errorf("lock child spec for recovery %s: %w", util.UUIDToString(childSpec.ChildIssueID), lockErr),
				fmt.Errorf("rollback recovery transaction for child %s: %w", util.UUIDToString(childSpec.ChildIssueID), rollbackErr),
			)
		}
		return fmt.Errorf("lock child spec for recovery %s: %w", util.UUIDToString(childSpec.ChildIssueID), lockErr)
	}

	switch lockedChildSpec.Status {
	case "awaiting_review":
		if err := s.enqueueReviewerIfNeeded(ctx, tx, queries, lockedChildSpec); err != nil {
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("rollback recovery transaction for child %s: %w", util.UUIDToString(childSpec.ChildIssueID), rollbackErr))
			}
			return fmt.Errorf("recover child %s awaiting review: %w", util.UUIDToString(childSpec.ChildIssueID), err)
		}
	case "done", "blocked":
		if err := s.enqueueParentIfNeeded(ctx, tx, queries, lockedChildSpec); err != nil {
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("rollback recovery transaction for child %s: %w", util.UUIDToString(childSpec.ChildIssueID), rollbackErr))
			}
			return fmt.Errorf("recover child %s terminal state %s: %w", util.UUIDToString(childSpec.ChildIssueID), lockedChildSpec.Status, err)
		}
	default:
		if err := tx.Rollback(ctx); err != nil {
			return fmt.Errorf("rollback skipped recovery transaction for child %s: %w", util.UUIDToString(childSpec.ChildIssueID), err)
		}
		return nil
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit recovery transaction for child %s: %w", util.UUIDToString(childSpec.ChildIssueID), err)
	}
	return nil
}

func (s *OrchestrationService) listRecoverableChildSpecs(ctx context.Context) ([]db.ChildSpec, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT id, workspace_id, parent_issue_id, child_issue_id, worker_agent_id, orchestrator_agent_id, status, max_review_rounds, created_at, updated_at
		FROM child_spec
		WHERE status IN ('awaiting_review', 'done', 'blocked')
		ORDER BY updated_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list recoverable child specs: %w", err)
	}
	defer rows.Close()

	childSpecs := make([]db.ChildSpec, 0)
	for rows.Next() {
		var childSpec db.ChildSpec
		if err := rows.Scan(
			&childSpec.ID,
			&childSpec.WorkspaceID,
			&childSpec.ParentIssueID,
			&childSpec.ChildIssueID,
			&childSpec.WorkerAgentID,
			&childSpec.OrchestratorAgentID,
			&childSpec.Status,
			&childSpec.MaxReviewRounds,
			&childSpec.CreatedAt,
			&childSpec.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan recoverable child spec: %w", err)
		}
		childSpecs = append(childSpecs, childSpec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recoverable child specs: %w", err)
	}
	return childSpecs, nil
}
