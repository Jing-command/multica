-- name: CreateChildSpec :one
INSERT INTO child_spec (
    workspace_id,
    parent_issue_id,
    child_issue_id,
    worker_agent_id,
    orchestrator_agent_id,
    status,
    max_review_rounds
) VALUES (
    $1, $2, $3, $4, $5, COALESCE(sqlc.narg('status'), 'todo'), COALESCE(sqlc.narg('max_review_rounds'), 1)
) RETURNING *;

-- name: UpdateChildSpecStatus :one
UPDATE child_spec SET
    status = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: CreateChildAcceptanceCriterion :one
INSERT INTO child_acceptance_criteria (
    child_spec_id,
    ordinal,
    criterion_text
) VALUES (
    $1, $2, $3
) RETURNING *;

-- name: GetChildAcceptanceCriterion :one
SELECT * FROM child_acceptance_criteria
WHERE id = $1;

-- name: GetChildSpecByIssueID :one
SELECT * FROM child_spec
WHERE child_issue_id = $1;

-- name: GetChildSpecByIssueIDForUpdate :one
SELECT * FROM child_spec
WHERE child_issue_id = $1
FOR UPDATE;

-- name: GetChildReviewRoundByIdempotencyKey :one
SELECT * FROM child_review_round
WHERE child_spec_id = $1 AND idempotency_key = $2;

-- name: CreateChildReviewRoundForSubmission :one
INSERT INTO child_review_round (
    child_spec_id,
    round_number,
    reviewer_agent_id,
    decision,
    summary,
    idempotency_key,
    submission_evidence
) VALUES (
    $1,
    COALESCE((SELECT MAX(round_number) + 1 FROM child_review_round WHERE child_spec_id = $1), 1),
    $2,
    'submitted',
    $3,
    $4,
    $5
) RETURNING *;

-- name: CompleteChildReviewRound :one
UPDATE child_review_round SET
    reviewer_agent_id = $2,
    decision = $3,
    summary = $4
WHERE id = $1 AND decision = 'submitted'
RETURNING *;

-- name: GetPlanRevisionByIdempotencyKey :one
SELECT * FROM plan_revision
WHERE child_spec_id = $1 AND idempotency_key = $2;

-- name: CreatePlanRevision :one
INSERT INTO plan_revision (
    child_spec_id,
    revision_number,
    requested_by_agent_id,
    reason,
    plan_content,
    idempotency_key
) VALUES (
    $1,
    COALESCE((SELECT MAX(revision_number) + 1 FROM plan_revision WHERE child_spec_id = $1), 1),
    $2,
    $3,
    $4,
    $5
) RETURNING *;

-- name: GetLatestPendingChildReviewRound :one
SELECT * FROM child_review_round
WHERE child_spec_id = $1 AND decision = 'submitted'
ORDER BY round_number DESC
LIMIT 1;

-- name: GetLatestChildReviewRound :one
SELECT * FROM child_review_round
WHERE child_spec_id = $1
ORDER BY round_number DESC
LIMIT 1;

-- name: CreateChildReviewCriterionResult :one
INSERT INTO child_review_criterion_result (
    review_round_id,
    criterion_id,
    result,
    note
) VALUES (
    $1, $2, $3, $4
) RETURNING *;

-- name: ListChildReviewCriterionResultsByRound :many
SELECT * FROM child_review_criterion_result
WHERE review_round_id = $1
ORDER BY created_at ASC;

-- name: CreateChildEscalation :one
INSERT INTO child_escalation (
    child_spec_id,
    raised_by_agent_id,
    reason,
    status
) VALUES (
    $1, $2, $3, 'open'
) RETURNING *;

-- name: GetChildEscalation :one
SELECT * FROM child_escalation
WHERE id = $1;

-- name: CreateChildReviewCompletion :one
INSERT INTO child_review_completion (
    child_spec_id,
    review_round_id,
    idempotency_key,
    escalation_id
) VALUES (
    $1, $2, $3, $4
) RETURNING *;

-- name: GetChildReviewCompletionByIdempotencyKey :one
SELECT * FROM child_review_completion
WHERE child_spec_id = $1 AND idempotency_key = $2;

-- name: GetChildReviewRound :one
SELECT * FROM child_review_round
WHERE id = $1;

-- name: GetLatestOpenChildEscalation :one
SELECT * FROM child_escalation
WHERE child_spec_id = $1 AND status = 'open'
ORDER BY created_at DESC
LIMIT 1;
