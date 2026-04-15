# Orchestrator MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the strict orchestration MVP so Multica uses backend-governed child specs, review rounds, structured verdicts, event-driven parent aggregation, and hard worker permission boundaries instead of comment-driven orchestration.

**Architecture:** Add a new orchestration data model in PostgreSQL, expose workflow commands through backend APIs and CLI commands, centralize state transitions in a dedicated orchestration service, and update the daemon execution environment to enforce per-child permission snapshots. Keep the current issue/comment/task model, but make comments narrative-only and move workflow truth into structured orchestration records plus committed events.

**Tech Stack:** Go, Chi, pgx/pgtype, sqlc, PostgreSQL migrations, Cobra CLI, existing daemon execenv, existing realtime event bus

---

## File structure

### Database and generated queries
- Create: `server/migrations/035_orchestration_core.up.sql`
- Create: `server/migrations/035_orchestration_core.down.sql`
- Create: `server/migrations/036_orchestration_permissions.up.sql`
- Create: `server/migrations/036_orchestration_permissions.down.sql`
- Create: `server/pkg/db/queries/orchestration.sql`
- Modify: `server/pkg/db/generated/queries.go`
- Create/Regenerate: `server/pkg/db/generated/orchestration.sql.go`

### Backend service and handlers
- Create: `server/internal/service/orchestration.go`
- Create: `server/internal/handler/orchestration.go`
- Modify: `server/internal/handler/comment.go`
- Modify: `server/internal/handler/orchestrator.go`
- Modify: `server/internal/handler/issue.go`
- Modify: `server/cmd/server/router.go`
- Modify: `server/pkg/protocol/events.go`
- Create: `server/internal/service/orchestration_recovery.go`

### CLI and runtime instructions
- Modify: `server/cmd/multica/cmd_issue.go`
- Modify: `server/internal/daemon/execenv/runtime_config.go`
- Modify: `server/internal/daemon/execenv/execenv.go`
- Create: `server/internal/daemon/execenv/permissions.go`
- Create: `server/internal/daemon/execenv/permissions_test.go`

### Tests
- Create: `server/internal/handler/orchestration_test.go`
- Modify: `server/internal/handler/orchestrator_test.go`
- Modify: `server/internal/handler/trigger_test.go`
- Create: `server/internal/service/orchestration_test.go`
- Create: `server/cmd/multica/cmd_issue_orchestration_test.go`

---

## Task 1: Add structured orchestration schema and sqlc queries

**Files:**
- Create: `server/migrations/035_orchestration_core.up.sql`
- Create: `server/migrations/035_orchestration_core.down.sql`
- Create: `server/migrations/036_orchestration_permissions.up.sql`
- Create: `server/migrations/036_orchestration_permissions.down.sql`
- Create: `server/pkg/db/queries/orchestration.sql`
- Create/Regenerate: `server/pkg/db/generated/orchestration.sql.go`
- Test: `server/internal/service/orchestration_test.go`

- [ ] **Step 1: Write the failing service test for backend-controlled review rounds and plan revisions**

```go
func TestSubmitReview_CreatesNextRoundWithoutClientCounter(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	service := service.NewOrchestrationService(testPool, queries, nil, nil)

	fx := seedChildSpecFixture(t, ctx, queries, seedChildSpecParams{
		maxRounds: 2,
	})

	round, err := service.SubmitReview(ctx, service.SubmitReviewParams{
		ChildSpecID:    fx.ChildSpecID,
		SubmittedBy:    fx.WorkerAgentID,
		IdempotencyKey: "submit-round-1",
		EvidenceJSON:   []byte(`{"summary":"ready"}`),
	})
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if round.RoundNumber != 1 {
		t.Fatalf("expected round 1, got %d", round.RoundNumber)
	}

	revision, err := service.CreatePlanRevision(ctx, service.CreatePlanRevisionParams{
		ChildSpecID:    fx.ChildSpecID,
		ActorType:      "agent",
		ActorID:        fx.OrchestratorAgentID,
		Reason:         "scope update",
		ChangeSummary:  "narrow backend scope",
		IdempotencyKey: "replan-1",
	})
	if err != nil {
		t.Fatalf("CreatePlanRevision: %v", err)
	}
	if revision.RevisionNumber != 1 {
		t.Fatalf("expected revision 1, got %d", revision.RevisionNumber)
	}
}
```

- [ ] **Step 2: Run the targeted service test and verify it fails because the orchestration tables and queries do not exist yet**

Run:
```bash
cd /Users/a1234/multica/server && go test ./internal/service -run TestSubmitReview_CreatesNextRoundWithoutClientCounter
```

Expected:
```text
FAIL
... relation "child_spec" does not exist
```

- [ ] **Step 3: Add core orchestration tables in migration 035**

```sql
CREATE TABLE child_spec (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    child_issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    worker_agent_id UUID NOT NULL REFERENCES agent(id),
    reviewer_agent_id UUID NOT NULL REFERENCES agent(id),
    status TEXT NOT NULL CHECK (status IN ('todo', 'in_progress', 'awaiting_review', 'done', 'blocked', 'superseded', 'aborted')),
    review_verdict TEXT NOT NULL DEFAULT 'pending' CHECK (review_verdict IN ('pending', 'changes_requested', 'approved', 'escalated')),
    active_plan_revision INT NOT NULL DEFAULT 0,
    max_review_rounds INT NOT NULL DEFAULT 2,
    current_review_round INT NOT NULL DEFAULT 0,
    latest_open_review_round_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (child_issue_id)
);

CREATE TABLE child_acceptance_criteria (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    child_spec_id UUID NOT NULL REFERENCES child_spec(id) ON DELETE CASCADE,
    criterion_key TEXT NOT NULL,
    description TEXT NOT NULL,
    required BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order INT NOT NULL DEFAULT 0,
    UNIQUE (child_spec_id, criterion_key)
);

CREATE TABLE child_review_round (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    child_spec_id UUID NOT NULL REFERENCES child_spec(id) ON DELETE CASCADE,
    round_number INT NOT NULL,
    submitted_by_agent_id UUID NOT NULL REFERENCES agent(id),
    reviewed_by_agent_id UUID REFERENCES agent(id),
    submission_evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    verdict TEXT CHECK (verdict IN ('approved', 'changes_requested', 'escalated')),
    summary TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at TIMESTAMPTZ,
    UNIQUE (child_spec_id, round_number)
);

CREATE TABLE child_review_criterion_result (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    review_round_id UUID NOT NULL REFERENCES child_review_round(id) ON DELETE CASCADE,
    criterion_id UUID NOT NULL REFERENCES child_acceptance_criteria(id) ON DELETE CASCADE,
    verdict TEXT NOT NULL CHECK (verdict IN ('approved', 'failed', 'not_applicable')),
    notes TEXT NOT NULL DEFAULT '',
    UNIQUE (review_round_id, criterion_id)
);

CREATE TABLE child_escalation (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    child_spec_id UUID NOT NULL REFERENCES child_spec(id) ON DELETE CASCADE,
    review_round_id UUID REFERENCES child_review_round(id) ON DELETE SET NULL,
    reason_type TEXT NOT NULL,
    reason_summary TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved', 'aborted')),
    resolution_action TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ
);

CREATE TABLE plan_revision (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    child_spec_id UUID NOT NULL REFERENCES child_spec(id) ON DELETE CASCADE,
    revision_number INT NOT NULL,
    reason TEXT NOT NULL,
    created_by_actor_type TEXT NOT NULL CHECK (created_by_actor_type IN ('member', 'agent', 'system')),
    created_by_actor_id UUID,
    change_summary TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (child_spec_id, revision_number),
    UNIQUE (child_spec_id, idempotency_key)
);
```

- [ ] **Step 4: Add permission tables in migration 036**

```sql
CREATE TABLE repo_permission_policy (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    policy_name TEXT NOT NULL,
    default_mode TEXT NOT NULL CHECK (default_mode IN ('deny', 'read_only', 'allow')),
    protected_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
    allowed_path_rules JSONB NOT NULL DEFAULT '[]'::jsonb,
    allowed_tools JSONB NOT NULL DEFAULT '[]'::jsonb,
    shell_command_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, policy_name)
);

CREATE TABLE child_permission_snapshot (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    child_spec_id UUID NOT NULL REFERENCES child_spec(id) ON DELETE CASCADE,
    policy_source_id UUID NOT NULL REFERENCES repo_permission_policy(id),
    allowed_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
    read_only_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
    blocked_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
    allowed_tools JSONB NOT NULL DEFAULT '[]'::jsonb,
    shell_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (child_spec_id)
);
```

- [ ] **Step 5: Add sqlc queries for the new orchestration model**

```sql
-- name: CreateChildSpec :one
INSERT INTO child_spec (
  parent_issue_id,
  child_issue_id,
  worker_agent_id,
  reviewer_agent_id,
  status,
  max_review_rounds
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: CreateChildAcceptanceCriterion :one
INSERT INTO child_acceptance_criteria (
  child_spec_id,
  criterion_key,
  description,
  required,
  sort_order
) VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetChildSpecByIssueID :one
SELECT * FROM child_spec
WHERE child_issue_id = $1;

-- name: CreateChildReviewRound :one
INSERT INTO child_review_round (
  child_spec_id,
  round_number,
  submitted_by_agent_id,
  submission_evidence
) VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CreatePlanRevision :one
INSERT INTO plan_revision (
  child_spec_id,
  revision_number,
  reason,
  created_by_actor_type,
  created_by_actor_id,
  change_summary,
  idempotency_key
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;
```

Regenerate:
```bash
cd /Users/a1234/multica/server && make sqlc
```

Expected:
```text
sqlc generate
```

- [ ] **Step 6: Run migrations and the targeted test to verify schema + generated queries are now wired up**

Run:
```bash
cd /Users/a1234/multica/server && make migrate-up && go test ./internal/service -run TestSubmitReview_CreatesNextRoundWithoutClientCounter
```

Expected:
```text
PASS
ok  github.com/multica-ai/multica/server/internal/service
```

- [ ] **Step 7: Commit**

```bash
git add server/migrations/035_orchestration_core.up.sql server/migrations/035_orchestration_core.down.sql server/migrations/036_orchestration_permissions.up.sql server/migrations/036_orchestration_permissions.down.sql server/pkg/db/queries/orchestration.sql server/pkg/db/generated/orchestration.sql.go server/internal/service/orchestration_test.go
git commit -m "feat(orchestration): add core workflow schema"
```

---

## Task 2: Implement orchestration service for authoritative state transitions

**Files:**
- Create: `server/internal/service/orchestration.go`
- Create: `server/internal/service/orchestration_test.go`
- Modify: `server/pkg/protocol/events.go`
- Test: `server/internal/service/orchestration_test.go`

- [ ] **Step 1: Write failing tests for submit-review, reviewer verdicts, escalation, and idempotency**

```go
func TestReview_ChangesRequestedAtMaxRoundEscalatesInsteadOfReopening(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	service := service.NewOrchestrationService(testPool, queries, nil, nil)
	fx := seedChildSpecFixture(t, ctx, queries, seedChildSpecParams{maxRounds: 1})

	round, err := service.SubmitReview(ctx, service.SubmitReviewParams{
		ChildSpecID:    fx.ChildSpecID,
		SubmittedBy:    fx.WorkerAgentID,
		IdempotencyKey: "submit-r1",
		EvidenceJSON:   []byte(`{"summary":"ready"}`),
	})
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}

	result, err := service.Review(ctx, service.ReviewParams{
		ChildSpecID:      fx.ChildSpecID,
		ReviewRoundID:    round.ID,
		ReviewerAgentID:  fx.ReviewerAgentID,
		Verdict:          "changes_requested",
		Summary:          "missing tests",
		IdempotencyKey:   "review-r1",
		CriterionResults: []service.CriterionVerdict{{CriterionID: fx.CriterionIDs[0], Verdict: "failed", Notes: "missing regression test"}},
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if result.ChildSpec.ReviewVerdict != "escalated" {
		t.Fatalf("expected escalated verdict, got %s", result.ChildSpec.ReviewVerdict)
	}
	if result.ChildSpec.Status != "blocked" {
		t.Fatalf("expected blocked status after escalation, got %s", result.ChildSpec.Status)
	}
}
```

- [ ] **Step 2: Run the targeted service tests and verify they fail because the orchestration service does not exist yet**

Run:
```bash
cd /Users/a1234/multica/server && go test ./internal/service -run 'TestReview_|TestSubmitReview_'
```

Expected:
```text
FAIL
... undefined: service.NewOrchestrationService
```

- [ ] **Step 3: Add protocol events for orchestration lifecycle**

```go
const (
	EventChildReviewSubmitted        = "child:review_submitted"
	EventChildReviewApproved         = "child:review_approved"
	EventChildReviewChangesRequested = "child:review_changes_requested"
	EventChildReviewEscalated        = "child:review_escalated"
	EventChildBlockedReported        = "child:blocked_reported"
	EventChildReplanned              = "child:replanned"
	EventParentAwaitingAggregation   = "parent:awaiting_aggregation"
	EventParentFinalized             = "parent:finalized"
)
```

- [ ] **Step 4: Implement the orchestration service API and transaction boundaries**

```go
type OrchestrationService struct {
	DB      *pgxpool.Pool
	Queries *db.Queries
	Hub     *realtime.Hub
	Bus     *events.Bus
}

type SubmitReviewParams struct {
	ChildSpecID    pgtype.UUID
	SubmittedBy    pgtype.UUID
	IdempotencyKey string
	EvidenceJSON   []byte
}

type ReviewParams struct {
	ChildSpecID      pgtype.UUID
	ReviewRoundID    pgtype.UUID
	ReviewerAgentID  pgtype.UUID
	Verdict          string
	Summary          string
	IdempotencyKey   string
	CriterionResults []CriterionVerdict
}

func NewOrchestrationService(dbPool *pgxpool.Pool, queries *db.Queries, hub *realtime.Hub, bus *events.Bus) *OrchestrationService {
	return &OrchestrationService{DB: dbPool, Queries: queries, Hub: hub, Bus: bus}
}
```

- [ ] **Step 5: Implement `SubmitReview`, `Review`, `ReportBlocked`, and `CreatePlanRevision` with backend-owned counters**

```go
func (s *OrchestrationService) SubmitReview(ctx context.Context, params SubmitReviewParams) (db.ChildReviewRound, error) {
	var round db.ChildReviewRound
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		q := s.Queries.WithTx(tx)
		spec, err := q.GetChildSpec(params.ChildSpecID)
		if err != nil {
			return err
		}
		nextRound := spec.CurrentReviewRound + 1
		round, err = q.CreateChildReviewRound(ctx, db.CreateChildReviewRoundParams{
			ChildSpecID:        spec.ID,
			RoundNumber:        nextRound,
			SubmittedByAgentID: params.SubmittedBy,
			SubmissionEvidence: params.EvidenceJSON,
		})
		if err != nil {
			return err
		}
		_, err = q.UpdateChildSpecForReviewSubmission(ctx, db.UpdateChildSpecForReviewSubmissionParams{
			ID:                    spec.ID,
			Status:                "awaiting_review",
			ReviewVerdict:         "pending",
			CurrentReviewRound:    nextRound,
			LatestOpenReviewRoundID: pgtype.UUID{Bytes: round.ID.Bytes, Valid: true},
		})
		return err
	})
	return round, err
}
```

```go
func (s *OrchestrationService) Review(ctx context.Context, params ReviewParams) (ReviewResult, error) {
	var out ReviewResult
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		q := s.Queries.WithTx(tx)
		spec, err := q.GetChildSpec(params.ChildSpecID)
		if err != nil {
			return err
		}

		nextStatus := "done"
		nextVerdict := params.Verdict
		if params.Verdict == "changes_requested" {
			if spec.CurrentReviewRound >= spec.MaxReviewRounds {
				nextStatus = "blocked"
				nextVerdict = "escalated"
			} else {
				nextStatus = "in_progress"
			}
		}
		if params.Verdict == "escalated" {
			nextStatus = "blocked"
		}
		for _, item := range params.CriterionResults {
			if _, err := q.CreateChildReviewCriterionResult(ctx, db.CreateChildReviewCriterionResultParams{
				ReviewRoundID: params.ReviewRoundID,
				CriterionID:   item.CriterionID,
				Verdict:       item.Verdict,
				Notes:         item.Notes,
			}); err != nil {
				return err
			}
		}
		if _, err := q.CloseChildReviewRound(ctx, db.CloseChildReviewRoundParams{
			ID:                params.ReviewRoundID,
			ReviewedByAgentID: pgtype.UUID{Bytes: params.ReviewerAgentID.Bytes, Valid: true},
			Verdict:           nextVerdict,
			Summary:           params.Summary,
		}); err != nil {
			return err
		}
		if _, err := q.UpdateChildSpecAfterReview(ctx, db.UpdateChildSpecAfterReviewParams{
			ID:                      spec.ID,
			Status:                  nextStatus,
			ReviewVerdict:           nextVerdict,
			LatestOpenReviewRoundID: pgtype.UUID{},
		}); err != nil {
			return err
		}
		if nextVerdict == "escalated" {
			if _, err := q.CreateChildEscalation(ctx, db.CreateChildEscalationParams{
				ChildSpecID:      spec.ID,
				ReviewRoundID:    pgtype.UUID{Bytes: params.ReviewRoundID.Bytes, Valid: true},
				ReasonType:       "review_loop_exhausted",
				ReasonSummary:    params.Summary,
				ResolutionAction: "",
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}
```

- [ ] **Step 6: Run the service tests and verify submit/review behavior now passes**

Run:
```bash
cd /Users/a1234/multica/server && go test ./internal/service -run 'TestReview_|TestSubmitReview_'
```

Expected:
```text
PASS
ok  github.com/multica-ai/multica/server/internal/service
```

- [ ] **Step 7: Commit**

```bash
git add server/internal/service/orchestration.go server/internal/service/orchestration_test.go server/pkg/protocol/events.go
git commit -m "feat(orchestration): add authoritative workflow service"
```

---

## Task 3: Add HTTP handlers and routes for worker, reviewer, and orchestrator commands

**Files:**
- Create: `server/internal/handler/orchestration.go`
- Modify: `server/cmd/server/router.go`
- Modify: `server/internal/handler/issue.go`
- Create: `server/internal/handler/orchestration_test.go`
- Test: `server/internal/handler/orchestration_test.go`

- [ ] **Step 1: Write failing handler tests for `submit-review`, `review`, `report-blocked`, and `finalize-parent`**

```go
func TestSubmitReview_CreatesStructuredRoundAndMovesChildToAwaitingReview(t *testing.T) {
	fx := seedOrchestrationHTTPFixture(t)
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fx.ChildIssueID+"/workflow/submit-review", map[string]any{
		"idempotency_key": "submit-http-1",
		"summary":         "ready for reviewer",
		"evidence": map[string]any{
			"pr_url": "https://example.invalid/pr/123",
		},
	})
	req = withURLParam(req, "id", fx.ChildIssueID)
	req.Header.Set("X-Agent-ID", fx.WorkerAgentID)
	testHandler.SubmitReview(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	spec := mustLoadChildSpec(t, fx.ChildSpecID)
	if spec.Status != "awaiting_review" {
		t.Fatalf("expected awaiting_review, got %s", spec.Status)
	}
}
```

- [ ] **Step 2: Run the handler tests and verify they fail because the routes and handlers do not exist yet**

Run:
```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestSubmitReview_|TestReview_|TestFinalizeParent_'
```

Expected:
```text
FAIL
... testHandler.SubmitReview undefined
```

- [ ] **Step 3: Add handler request/response types and actor validation**

```go
type SubmitReviewRequest struct {
	IdempotencyKey string         `json:"idempotency_key"`
	Summary        string         `json:"summary"`
	Evidence       map[string]any `json:"evidence"`
}

type ReviewRequest struct {
	IdempotencyKey   string                    `json:"idempotency_key"`
	Verdict          string                    `json:"verdict"`
	Summary          string                    `json:"summary"`
	ReviewRoundID    string                    `json:"review_round_id"`
	CriterionResults []ReviewCriterionResponse `json:"criterion_results"`
}

func (h *Handler) SubmitReview(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	if actorType != "agent" {
		writeError(w, http.StatusForbidden, "only agent can submit review")
		return
	}
	var req SubmitReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IdempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key is required")
		return
	}
	evidenceJSON, _ := json.Marshal(map[string]any{
		"summary":  req.Summary,
		"evidence": req.Evidence,
	})
	round, err := h.OrchestrationService.SubmitReview(r.Context(), service.SubmitReviewParams{
		ChildSpecID:    mustLoadChildSpecIDByIssue(h.Queries, r.Context(), issue.ID),
		SubmittedBy:    parseUUID(actorID),
		IdempotencyKey: req.IdempotencyKey,
		EvidenceJSON:   evidenceJSON,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"review_round_id": uuidToString(round.ID), "status": "awaiting_review", "review_verdict": "pending"})
}
```

- [ ] **Step 4: Wire routes under issue-scoped workflow endpoints**

```go
r.Route("/{id}", func(r chi.Router) {
	r.Get("/", h.GetIssue)
	r.Put("/", h.UpdateIssue)
	r.Delete("/", h.DeleteIssue)
	r.Post("/workflow/submit-review", h.SubmitReview)
	r.Post("/workflow/report-blocked", h.ReportBlocked)
	r.Post("/workflow/review", h.ReviewChild)
	r.Post("/workflow/replan", h.ReplanChild)
	r.Post("/workflow/finalize-parent", h.FinalizeParent)
})
```

- [ ] **Step 5: Add issue response fields so frontend and CLI can see structured orchestration state**

```go
type IssueResponse struct {
	ID                 string                  `json:"id"`
	Title              string                  `json:"title"`
	Status             string                  `json:"status"`
	ParentIssueID      *string                 `json:"parent_issue_id"`
	ChildSpec          *ChildSpecResponse      `json:"child_spec,omitempty"`
	ParentOrchestration *ParentWorkflowResponse `json:"parent_orchestration,omitempty"`
}
```

- [ ] **Step 6: Run the handler tests and verify the workflow endpoints now pass**

Run:
```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestSubmitReview_|TestReview_|TestFinalizeParent_'
```

Expected:
```text
PASS
ok  github.com/multica-ai/multica/server/internal/handler
```

- [ ] **Step 7: Commit**

```bash
git add server/internal/handler/orchestration.go server/internal/handler/orchestration_test.go server/internal/handler/issue.go server/cmd/server/router.go
git commit -m "feat(orchestration): add workflow command endpoints"
```

---

## Task 4: Replace comment-driven workflow commands in the CLI and runtime instructions

**Files:**
- Modify: `server/cmd/multica/cmd_issue.go`
- Create: `server/cmd/multica/cmd_issue_orchestration_test.go`
- Modify: `server/internal/daemon/execenv/runtime_config.go`
- Test: `server/cmd/multica/cmd_issue_orchestration_test.go`

- [ ] **Step 1: Write failing CLI tests for `issue submit-review`, `issue review`, and `issue report-blocked`**

```go
func TestIssueSubmitReviewCommand_PostsWorkflowPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/issues/issue-123/workflow/submit-review" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"awaiting_review","review_verdict":"pending"}`))
	}))
	defer server.Close()

	cmd := newRootCmdForTest(server.URL)
	cmd.SetArgs([]string{"issue", "submit-review", "issue-123", "--summary", "ready", "--idempotency-key", "submit-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}
```

- [ ] **Step 2: Run the CLI tests and verify they fail because the commands do not exist yet**

Run:
```bash
cd /Users/a1234/multica/server && go test ./cmd/multica -run 'TestIssueSubmitReviewCommand|TestIssueReviewCommand|TestIssueReportBlockedCommand'
```

Expected:
```text
FAIL
... unknown command "submit-review"
```

- [ ] **Step 3: Add workflow subcommands to `cmd_issue.go`**

```go
var issueSubmitReviewCmd = &cobra.Command{
	Use:   "submit-review <issue-id>",
	Short: "Submit a child issue for structured review",
	Args:  exactArgs(1),
	RunE:  runIssueSubmitReview,
}

var issueReviewCmd = &cobra.Command{
	Use:   "review <issue-id>",
	Short: "Record a reviewer verdict for a child issue",
	Args:  exactArgs(1),
	RunE:  runIssueReview,
}

var issueReportBlockedCmd = &cobra.Command{
	Use:   "report-blocked <issue-id>",
	Short: "Report a structured blocker for a child issue",
	Args:  exactArgs(1),
	RunE:  runIssueReportBlocked,
}
```

- [ ] **Step 4: Implement the HTTP payloads for the new CLI commands**

```go
func runIssueSubmitReview(cmd *cobra.Command, args []string) error {
	body := map[string]any{
		"idempotency_key": submitReviewIdempotencyKey,
		"summary":         submitReviewSummary,
		"evidence": map[string]any{
			"pr_url": submitReviewPRURL,
		},
	}
	return apiPostJSON(fmt.Sprintf("/api/issues/%s/workflow/submit-review", args[0]), body)
}

func runIssueReview(cmd *cobra.Command, args []string) error {
	body := map[string]any{
		"idempotency_key": reviewIdempotencyKey,
		"review_round_id": reviewRoundID,
		"verdict":         reviewVerdict,
		"summary":         reviewSummary,
		"criterion_results": []map[string]any{
			{"criterion_id": reviewCriterionID, "verdict": reviewCriterionVerdict, "notes": reviewCriterionNotes},
		},
	}
	return apiPostJSON(fmt.Sprintf("/api/issues/%s/workflow/review", args[0]), body)
}
```

- [ ] **Step 5: Rewrite runtime instructions so Worker/Reviewer/Orchestrator use formal commands, not comment-driven state changes**

```go
if ctx.IsOrchestrator {
	b.WriteString("Use workflow commands for decomposition and finalization. Do not use comments as workflow truth.\n\n")
	b.WriteString("- Re-plan with the dedicated workflow endpoint through the multica CLI\n")
	b.WriteString("- Finalize parent only after all active children are terminal\n")
} else if ctx.TriggerCommentID != "" {
	b.WriteString("Comments may explain context, but they do not advance workflow state.\n")
} else {
	b.WriteString("When implementation is ready, run `multica issue submit-review <issue-id> --summary \"...\" --idempotency-key <key>`.\n")
	b.WriteString("If blocked, run `multica issue report-blocked <issue-id> --reason-type <type> --summary \"...\" --idempotency-key <key>`.\n")
}
```

- [ ] **Step 6: Run the CLI tests and verify the new command flow passes**

Run:
```bash
cd /Users/a1234/multica/server && go test ./cmd/multica -run 'TestIssueSubmitReviewCommand|TestIssueReviewCommand|TestIssueReportBlockedCommand'
```

Expected:
```text
PASS
ok  github.com/multica-ai/multica/server/cmd/multica
```

- [ ] **Step 7: Commit**

```bash
git add server/cmd/multica/cmd_issue.go server/cmd/multica/cmd_issue_orchestration_test.go server/internal/daemon/execenv/runtime_config.go
git commit -m "feat(cli): add structured orchestration commands"
```

---

## Task 5: Move parent wakeup and aggregation to child lifecycle events

**Files:**
- Modify: `server/internal/service/orchestration.go`
- Modify: `server/internal/handler/comment.go`
- Modify: `server/internal/handler/orchestrator.go`
- Modify: `server/internal/handler/orchestrator_test.go`
- Modify: `server/internal/handler/trigger_test.go`
- Test: `server/internal/handler/orchestrator_test.go`

- [ ] **Step 1: Write failing tests proving parent wakeup happens from structured review events instead of child comments**

```go
func TestReviewApproved_EnqueuesParentOrchestratorWithoutBridgeComment(t *testing.T) {
	fx := seedOrchestrationHTTPFixture(t)
	roundID := mustSubmitReviewRound(t, fx)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+fx.ChildIssueID+"/workflow/review", map[string]any{
		"idempotency_key": "review-approve-1",
		"review_round_id": roundID,
		"verdict": "approved",
		"summary": "all criteria satisfied",
		"criterion_results": []map[string]any{{
			"criterion_id": fx.CriterionIDs[0],
			"verdict":      "approved",
			"notes":        "ok",
		}},
	})
	req = withURLParam(req, "id", fx.ChildIssueID)
	req.Header.Set("X-Agent-ID", fx.ReviewerAgentID)
	testHandler.ReviewChild(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	task := mustLoadLatestParentTask(t, fx.ParentIssueID, fx.OrchestratorAgentID)
	if task.TriggerCommentID.Valid {
		t.Fatalf("expected parent wakeup without bridge comment trigger")
	}
}
```

- [ ] **Step 2: Run the orchestrator tests and verify they fail because parent wakeup is still comment-based**

Run:
```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestReviewApproved_EnqueuesParentOrchestratorWithoutBridgeComment|TestCreateComment_ChildAgentCommentEnqueuesParentTaskWithParentIssueTriggerComment'
```

Expected:
```text
FAIL
... expected parent wakeup without bridge comment trigger
```

- [ ] **Step 3: Remove child-comment bridge wakeup from `CreateComment` and keep comments narrative-only**

```go
// Remove this block entirely from CreateComment:
if authorType == "agent" && issue.ParentIssueID.Valid {
    // child-comment -> parent enqueue
}
```

- [ ] **Step 4: Wake the parent orchestrator from orchestration service events instead**

```go
func (s *OrchestrationService) enqueueParentIfNeeded(ctx context.Context, childSpec db.ChildSpec) error {
	parentIssue, err := s.Queries.GetIssue(ctx, childSpec.ParentIssueID)
	if err != nil || !parentIssue.AssigneeID.Valid {
		return err
	}
	hasPending, err := s.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: parentIssue.ID,
		AgentID: parentIssue.AssigneeID,
	})
	if err != nil || hasPending {
		return err
	}
	_, err = s.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:   parentIssue.AssigneeID,
		RuntimeID: mustLoadAgentRuntimeID(ctx, s.Queries, parentIssue.AssigneeID),
		IssueID:   parentIssue.ID,
		Priority:  2,
	})
	return err
}
```

- [ ] **Step 5: Update orchestrator instructions so aggregation is driven by structured child terminal states**

```go
const orchestratorInstructions = `You are a task orchestrator.

Workflow truth is stored in structured child workflow state, not in comments.

When you are re-triggered:
1. Inspect child workflow state.
2. If all active children are terminal, aggregate and finalize the parent.
3. If a child is escalated or blocked, decide whether to re-plan, split, replace, or abort.
4. Do not parse comments as the source of truth.`
```

- [ ] **Step 6: Run the orchestrator tests and verify parent wakeup is now event-driven**

Run:
```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestReviewApproved_EnqueuesParentOrchestratorWithoutBridgeComment|TestFinalizeParent_'
```

Expected:
```text
PASS
ok  github.com/multica-ai/multica/server/internal/handler
```

- [ ] **Step 7: Commit**

```bash
git add server/internal/service/orchestration.go server/internal/handler/comment.go server/internal/handler/orchestrator.go server/internal/handler/orchestrator_test.go server/internal/handler/trigger_test.go
git commit -m "feat(orchestration): drive parent wakeup from child events"
```

---

## Task 6: Enforce hard worker permission boundaries in the daemon execution environment

**Files:**
- Modify: `server/internal/daemon/execenv/execenv.go`
- Create: `server/internal/daemon/execenv/permissions.go`
- Create: `server/internal/daemon/execenv/permissions_test.go`
- Modify: `server/internal/daemon/execenv/runtime_config.go`
- Test: `server/internal/daemon/execenv/permissions_test.go`

- [ ] **Step 1: Write failing permission tests for blocked path writes and diff validation**

```go
func TestValidateWritePath_RejectsBlockedFile(t *testing.T) {
	snapshot := PermissionSnapshot{
		AllowedPaths: []string{"apps/web/features/issues/**"},
		BlockedPaths: []string{"server/internal/handler/**", "CLAUDE.md"},
	}
	if err := ValidateWritePath(snapshot, "server/internal/handler/comment.go"); err == nil {
		t.Fatal("expected blocked path rejection")
	}
}

func TestValidateDiffPaths_RejectsOutOfScopeDiff(t *testing.T) {
	snapshot := PermissionSnapshot{
		AllowedPaths: []string{"server/internal/service/**"},
	}
	err := ValidateDiffPaths(snapshot, []string{"server/internal/service/orchestration.go", "server/cmd/server/router.go"})
	if err == nil {
		t.Fatal("expected diff boundary rejection")
	}
}
```

- [ ] **Step 2: Run the execenv tests and verify they fail because permission validation does not exist yet**

Run:
```bash
cd /Users/a1234/multica/server && go test ./internal/daemon/execenv -run 'TestValidateWritePath_|TestValidateDiffPaths_'
```

Expected:
```text
FAIL
... undefined: PermissionSnapshot
```

- [ ] **Step 3: Add the permission snapshot model and path validation helpers**

```go
type PermissionSnapshot struct {
	AllowedPaths []string `json:"allowed_paths"`
	ReadOnlyPaths []string `json:"read_only_paths"`
	BlockedPaths []string `json:"blocked_paths"`
	AllowedTools []string `json:"allowed_tools"`
}

func ValidateWritePath(snapshot PermissionSnapshot, path string) error {
	if matchesAny(snapshot.BlockedPaths, path) {
		return fmt.Errorf("write denied for blocked path %s", path)
	}
	if !matchesAny(snapshot.AllowedPaths, path) {
		return fmt.Errorf("write denied for out-of-scope path %s", path)
	}
	if matchesAny(snapshot.ReadOnlyPaths, path) {
		return fmt.Errorf("write denied for read-only path %s", path)
	}
	return nil
}
```

- [ ] **Step 4: Thread the permission snapshot into the prepared execution environment**

```go
type TaskContextForEnv struct {
	IssueID              string
	TriggerCommentID     string
	AgentName            string
	AgentInstructions    string
	AgentSkills          []SkillContextForEnv
	Repos                []RepoContextForEnv
	IsOrchestrator       bool
	PermissionSnapshotJSON []byte
}

func writeContextFiles(workDir, provider string, task TaskContextForEnv) error {
	if len(task.PermissionSnapshotJSON) > 0 {
		if err := os.WriteFile(filepath.Join(workDir, ".agent_context", "permission_snapshot.json"), task.PermissionSnapshotJSON, 0o644); err != nil {
			return err
		}
	}
	return InjectRuntimeConfig(workDir, provider, task)
}
```

- [ ] **Step 5: Make runtime instructions explicit that denied writes are runtime-enforced, not advisory**

```go
b.WriteString("## Hard file permissions\n\n")
b.WriteString("Your allowed write scope is enforced by the execution environment. If a path is not allowed, write operations will fail. Do not attempt to work around this boundary.\n\n")
```

- [ ] **Step 6: Run the execenv tests and verify permission enforcement now passes**

Run:
```bash
cd /Users/a1234/multica/server && go test ./internal/daemon/execenv -run 'TestValidateWritePath_|TestValidateDiffPaths_'
```

Expected:
```text
PASS
ok  github.com/multica-ai/multica/server/internal/daemon/execenv
```

- [ ] **Step 7: Commit**

```bash
git add server/internal/daemon/execenv/execenv.go server/internal/daemon/execenv/runtime_config.go server/internal/daemon/execenv/permissions.go server/internal/daemon/execenv/permissions_test.go
git commit -m "feat(execenv): enforce child permission snapshots"
```

---

## Task 7: Add recovery scanning and end-to-end orchestration regression coverage

**Files:**
- Modify: `server/internal/service/orchestration.go`
- Create: `server/internal/service/orchestration_recovery.go`
- Modify: `server/internal/handler/orchestration_test.go`
- Modify: `server/internal/handler/orchestrator_test.go`
- Test: `server/internal/service/orchestration_test.go`
- Test: `server/internal/handler/orchestration_test.go`

- [ ] **Step 1: Write failing tests for stuck review recovery and parent aggregation after all children are terminal**

```go
func TestRecoverStuckAwaitingReview_RequeuesReviewerTask(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	service := service.NewOrchestrationService(testPool, queries, nil, nil)
	fx := seedChildSpecFixture(t, ctx, queries, seedChildSpecParams{status: "awaiting_review"})
	mustDeletePendingTasks(t, fx.ChildIssueID, fx.ReviewerAgentID)

	err := service.RecoverStuckWorkflows(ctx)
	if err != nil {
		t.Fatalf("RecoverStuckWorkflows: %v", err)
	}
	mustHavePendingTask(t, fx.ChildIssueID, fx.ReviewerAgentID)
}

func TestFinalizeParent_AllChildrenDoneMarksAwaitingAggregationThenDone(t *testing.T) {
	fx := seedParentWithTerminalChildren(t)
	result := mustFinalizeParent(t, fx.ParentIssueID, fx.OrchestratorAgentID)
	if result.FinalOutcome != "complete" {
		t.Fatalf("expected complete, got %s", result.FinalOutcome)
	}
}
```

- [ ] **Step 2: Run the service and handler tests and verify they fail because recovery scanning is missing**

Run:
```bash
cd /Users/a1234/multica/server && go test ./internal/service ./internal/handler -run 'TestRecoverStuckAwaitingReview_|TestFinalizeParent_AllChildrenDoneMarksAwaitingAggregationThenDone'
```

Expected:
```text
FAIL
... undefined: (*OrchestrationService).RecoverStuckWorkflows
```

- [ ] **Step 3: Implement recovery scanning for missing reviewer/orchestrator wakeups**

```go
func (s *OrchestrationService) RecoverStuckWorkflows(ctx context.Context) error {
	stuckSpecs, err := s.Queries.ListRecoverableChildSpecs(ctx)
	if err != nil {
		return err
	}
	for _, spec := range stuckSpecs {
		switch spec.Status {
		case "awaiting_review":
			if err := s.enqueueReviewerIfNeeded(ctx, spec); err != nil {
				return err
			}
		case "blocked":
			if spec.ReviewVerdict == "escalated" {
				if err := s.enqueueParentIfNeeded(ctx, spec); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
```

- [ ] **Step 4: Add a parent finalization result type and persist final outcome explicitly**

```go
type FinalizeParentResult struct {
	ParentIssueID string `json:"parent_issue_id"`
	FinalOutcome  string `json:"final_outcome"`
	Status        string `json:"status"`
}

func (s *OrchestrationService) FinalizeParent(ctx context.Context, params FinalizeParentParams) (FinalizeParentResult, error) {
	var out FinalizeParentResult
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		q := s.Queries.WithTx(tx)
		children, err := q.ListActiveChildSpecsByParentIssue(ctx, params.ParentIssueID)
		if err != nil {
			return err
		}
		outcome := "complete"
		status := "done"
		for _, child := range children {
			switch child.Status {
			case "blocked":
				outcome = "blocked"
				status = "blocked"
			case "aborted":
				if outcome != "blocked" {
					outcome = "aborted"
					status = "aborted"
				}
			case "superseded":
				if outcome == "complete" {
					outcome = "complete_with_exceptions"
				}
			}
		}
		if _, err := q.UpdateParentWorkflowFinalization(ctx, db.UpdateParentWorkflowFinalizationParams{
			ParentIssueID: params.ParentIssueID,
			Status:        status,
			FinalOutcome:  outcome,
		}); err != nil {
			return err
		}
		out = FinalizeParentResult{ParentIssueID: uuidToString(params.ParentIssueID), FinalOutcome: outcome, Status: status}
		return nil
	})
	return out, err
}
```

- [ ] **Step 5: Run the end-to-end orchestration regression tests**

Run:
```bash
cd /Users/a1234/multica/server && go test ./internal/service ./internal/handler -run 'TestRecoverStuckAwaitingReview_|TestFinalizeParent_AllChildrenDoneMarksAwaitingAggregationThenDone|TestReview_ChangesRequestedAtMaxRoundEscalatesInsteadOfReopening|TestSubmitReview_CreatesStructuredRoundAndMovesChildToAwaitingReview'
```

Expected:
```text
PASS
ok  github.com/multica-ai/multica/server/internal/service
ok  github.com/multica-ai/multica/server/internal/handler
```

- [ ] **Step 6: Run the minimum cross-package regression set for this feature**

Run:
```bash
cd /Users/a1234/multica/server && go test ./internal/service ./internal/handler ./internal/daemon/execenv ./cmd/multica
```

Expected:
```text
ok  github.com/multica-ai/multica/server/internal/service
ok  github.com/multica-ai/multica/server/internal/handler
ok  github.com/multica-ai/multica/server/internal/daemon/execenv
ok  github.com/multica-ai/multica/server/cmd/multica
```

- [ ] **Step 7: Commit**

```bash
git add server/internal/service/orchestration.go server/internal/service/orchestration_recovery.go server/internal/handler/orchestration_test.go server/internal/handler/orchestrator_test.go
git commit -m "feat(orchestration): add recovery and finalization flow"
```

---

## Spec coverage check

- Role separation: covered by Tasks 2, 3, 4, 5.
- Structured child spec / AC / review rounds / criterion results / escalations / plan revisions: covered by Tasks 1 and 2.
- Formal commands instead of comment-driven workflow: covered by Tasks 3 and 4.
- Backend-owned review round and plan revision counters: covered by Tasks 1 and 2.
- Event-driven parent wakeup and aggregation: covered by Task 5.
- Hard worker permission boundaries and snapshots: covered by Task 6.
- Recovery scanning and resumability: covered by Task 7.
- MVP-only scope, not full Phase 3 policy engine: preserved by limiting Task 6 to baseline snapshot enforcement and Task 7 to basic recovery scanning.

## Placeholder scan

Checked for: `TBD`, `TODO`, `implement later`, `appropriate error handling`, `similar to Task`, `Optionally`, and vague "write tests" steps. Fixed the remaining placeholders inline.

## Type consistency check

Planned names are consistent across tasks:
- `OrchestrationService`
- `SubmitReview`
- `Review`
- `ReportBlocked`
- `CreatePlanRevision`
- `FinalizeParent`
- `RecoverStuckWorkflows`
- `child_spec`
- `child_review_round`
- `plan_revision`
- `child_permission_snapshot`

