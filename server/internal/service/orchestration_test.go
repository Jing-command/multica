package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var testServicePool *pgxpool.Pool
var testServiceQueries *db.Queries
var testServiceUserID pgtype.UUID
var testServiceWorkspaceID pgtype.UUID
var serviceFixtureSeq atomic.Int64

var (
	serviceTestEmail         = "service-orchestration-test@multica.ai"
	serviceTestName          = "Service Orchestration Test User"
	serviceTestWorkspaceSlug = "service-orchestration-tests"
	serviceTestPrefix        = "Service Orchestration Tests"
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	serviceTestEmail = fmt.Sprintf("service-orchestration-test-%s@multica.ai", suffix)
	serviceTestName = fmt.Sprintf("Service Orchestration Test User %s", suffix)
	serviceTestWorkspaceSlug = fmt.Sprintf("service-orchestration-tests-%s", suffix)
	serviceTestPrefix = fmt.Sprintf("Service Orchestration Tests %s", suffix)

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Printf("Failed to connect to database: %v\n", err)
		os.Exit(1)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Printf("Database not reachable: %v\n", err)
		pool.Close()
		os.Exit(1)
	}

	testServicePool = pool
	testServiceQueries = db.New(pool)

	testServiceUserID, testServiceWorkspaceID, err = setupOrchestrationServiceFixture(ctx, pool)
	if err != nil {
		fmt.Printf("Failed to set up service fixture: %v\n", err)
		pool.Close()
		os.Exit(1)
	}

	code := m.Run()
	if err := cleanupOrchestrationServiceFixture(context.Background(), pool); err != nil {
		fmt.Printf("Failed to clean up service fixture: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	pool.Close()
	os.Exit(code)
}

func setupOrchestrationServiceFixture(ctx context.Context, pool *pgxpool.Pool) (pgtype.UUID, pgtype.UUID, error) {
	if err := cleanupOrchestrationServiceFixture(ctx, pool); err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, $2)
		RETURNING id
	`, serviceTestName, serviceTestEmail).Scan(&userID); err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}

	var workspaceID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, serviceTestPrefix, serviceTestWorkspaceSlug, "Temporary workspace for service tests", "SOR").Scan(&workspaceID); err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, userID); err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}

	return userID, workspaceID, nil
}

func cleanupOrchestrationServiceFixture(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, serviceTestWorkspaceSlug); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, serviceTestEmail); err != nil {
		return err
	}
	return nil
}

type orchestrationFixture struct {
	orchestratorAgentID pgtype.UUID
	workerAgentID       pgtype.UUID
	parentIssueID       pgtype.UUID
	childIssueID        pgtype.UUID
}

func seedOrchestrationEntities(t *testing.T, ctx context.Context) orchestrationFixture {
	t.Helper()

	seq := serviceFixtureSeq.Add(1)
	nameSuffix := fmt.Sprintf("%s-%d", t.Name(), seq)

	var orchestratorRuntimeID pgtype.UUID
	if err := testServicePool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, $3, 'local', 'claude', 'online', 'test', '{}'::jsonb, now(), $2)
		RETURNING id
	`, testServiceWorkspaceID, testServiceUserID, "Service Test Orchestrator Runtime "+nameSuffix).Scan(&orchestratorRuntimeID); err != nil {
		t.Fatalf("insert orchestrator runtime: %v", err)
	}

	var workerRuntimeID pgtype.UUID
	if err := testServicePool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id
		)
		VALUES ($1, NULL, $3, 'local', 'codex', 'online', 'test', '{}'::jsonb, now(), $2)
		RETURNING id
	`, testServiceWorkspaceID, testServiceUserID, "Service Test Worker Runtime "+nameSuffix).Scan(&workerRuntimeID); err != nil {
		t.Fatalf("insert worker runtime: %v", err)
	}

	var orchestratorAgentID pgtype.UUID
	if err := testServicePool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $4, '', 'local', '{}'::jsonb, $2, 'workspace', 10, $3)
		RETURNING id
	`, testServiceWorkspaceID, orchestratorRuntimeID, testServiceUserID, "Service Test Orchestrator "+nameSuffix).Scan(&orchestratorAgentID); err != nil {
		t.Fatalf("insert orchestrator agent: %v", err)
	}

	var workerAgentID pgtype.UUID
	if err := testServicePool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $4, '', 'local', '{}'::jsonb, $2, 'workspace', 1, $3)
		RETURNING id
	`, testServiceWorkspaceID, workerRuntimeID, testServiceUserID, "Service Test Worker "+nameSuffix).Scan(&workerAgentID); err != nil {
		t.Fatalf("insert worker agent: %v", err)
	}

	var parentIssueID pgtype.UUID
	if err := testServicePool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_type, creator_id,
			number, position, assignee_type, assignee_id
		)
		VALUES ($1, $3, 'todo', 'medium', 'member', $2, $5, 0, 'agent', $4)
		RETURNING id
	`, testServiceWorkspaceID, testServiceUserID, "Service Test Parent Issue "+nameSuffix, orchestratorAgentID, 990000+seq*2).Scan(&parentIssueID); err != nil {
		t.Fatalf("insert parent issue: %v", err)
	}

	var childIssueID pgtype.UUID
	if err := testServicePool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_type, creator_id,
			number, position, parent_issue_id, assignee_type, assignee_id
		)
		VALUES ($1, $5, 'todo', 'medium', 'member', $2, $6, 0, $3, 'agent', $4)
		RETURNING id
	`, testServiceWorkspaceID, testServiceUserID, parentIssueID, workerAgentID, "Service Test Child Issue "+nameSuffix, 990001+seq*2).Scan(&childIssueID); err != nil {
		t.Fatalf("insert child issue: %v", err)
	}

	childSpec, err := testServiceQueries.CreateChildSpec(ctx, db.CreateChildSpecParams{
		WorkspaceID:         testServiceWorkspaceID,
		ParentIssueID:       parentIssueID,
		ChildIssueID:        childIssueID,
		WorkerAgentID:       workerAgentID,
		OrchestratorAgentID: orchestratorAgentID,
	})
	if err != nil {
		t.Fatalf("create child spec: %v", err)
	}

	if _, err := testServiceQueries.CreateChildAcceptanceCriterion(ctx, db.CreateChildAcceptanceCriterionParams{
		ChildSpecID:   childSpec.ID,
		Ordinal:       1,
		CriterionText: "Worker output passes orchestrator review.",
	}); err != nil {
		t.Fatalf("create child acceptance criterion: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testServicePool.Exec(ctx, `DELETE FROM issue WHERE id = $1 OR id = $2`, childIssueID, parentIssueID)
		_, _ = testServicePool.Exec(ctx, `DELETE FROM agent WHERE id = $1 OR id = $2`, orchestratorAgentID, workerAgentID)
		_, _ = testServicePool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1 OR id = $2`, orchestratorRuntimeID, workerRuntimeID)
	})

	return orchestrationFixture{
		orchestratorAgentID: orchestratorAgentID,
		workerAgentID:       workerAgentID,
		parentIssueID:       parentIssueID,
		childIssueID:        childIssueID,
	}
}

func submitForReview(t *testing.T, ctx context.Context, svc *OrchestrationService, fixture orchestrationFixture, summary string) db.ChildReviewRound {
	t.Helper()

	reviewRound, err := svc.SubmitReview(ctx, SubmitReviewParams{
		ChildIssueID:      fixture.childIssueID,
		SubmitterAgentID:  fixture.workerAgentID,
		SubmissionSummary: pgtype.Text{String: summary, Valid: true},
	})
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	return reviewRound
}

func TestPublishIssueUpdated_BestEffortWhenIssueMissing(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	bus := events.New()
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, bus)

	if _, err := testServicePool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("delete child issue: %v", err)
	}

	if err := svc.publishIssueUpdated(ctx, fixture.childIssueID, fixture.workerAgentID); err != nil {
		t.Fatalf("expected publishIssueUpdated to be best-effort, got %v", err)
	}
}

func seedSiblingChildIssue(t *testing.T, ctx context.Context, fixture orchestrationFixture) pgtype.UUID {
	t.Helper()

	seq := serviceFixtureSeq.Add(1)
	nameSuffix := fmt.Sprintf("%s-sibling-%d", t.Name(), seq)

	var siblingIssueID pgtype.UUID
	if err := testServicePool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_type, creator_id,
			number, position, parent_issue_id, assignee_type, assignee_id
		)
		VALUES ($1, $5, 'todo', 'medium', 'member', $2, $6, 0, $3, 'agent', $4)
		RETURNING id
	`, testServiceWorkspaceID, testServiceUserID, fixture.parentIssueID, fixture.workerAgentID, "Service Test Sibling Child Issue "+nameSuffix, 995000+seq).Scan(&siblingIssueID); err != nil {
		t.Fatalf("insert sibling child issue: %v", err)
	}

	if _, err := testServiceQueries.CreateChildSpec(ctx, db.CreateChildSpecParams{
		WorkspaceID:         testServiceWorkspaceID,
		ParentIssueID:       fixture.parentIssueID,
		ChildIssueID:        siblingIssueID,
		WorkerAgentID:       fixture.workerAgentID,
		OrchestratorAgentID: fixture.orchestratorAgentID,
	}); err != nil {
		t.Fatalf("create sibling child spec: %v", err)
	}

	return siblingIssueID
}

func countParentTasks(t *testing.T, ctx context.Context, fixture orchestrationFixture) int {
	t.Helper()

	var count int
	if err := testServicePool.QueryRow(ctx, `
		SELECT count(*)
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
	`, fixture.parentIssueID, fixture.orchestratorAgentID).Scan(&count); err != nil {
		t.Fatalf("count parent tasks: %v", err)
	}
	return count
}

func countIssueTasksForAgent(t *testing.T, ctx context.Context, issueID, agentID pgtype.UUID) int {
	t.Helper()

	var count int
	if err := testServicePool.QueryRow(ctx, `
		SELECT count(*)
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
	`, issueID, agentID).Scan(&count); err != nil {
		t.Fatalf("count issue tasks: %v", err)
	}
	return count
}

func TestSubmitReview_WorkerCreatesSubmittedRoundAndMarksAwaitingReview(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	reviewRound, err := svc.SubmitReview(ctx, SubmitReviewParams{
		ChildIssueID:      fixture.childIssueID,
		SubmitterAgentID:  fixture.workerAgentID,
		SubmissionSummary: pgtype.Text{String: "ready for review", Valid: true},
	})
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if reviewRound.Decision != "submitted" {
		t.Fatalf("expected submitted decision, got %q", reviewRound.Decision)
	}
	if reviewRound.RoundNumber != 1 {
		t.Fatalf("expected round_number=1, got %d", reviewRound.RoundNumber)
	}

	childSpec, getErr := testServiceQueries.GetChildSpecByIssueID(ctx, fixture.childIssueID)
	if getErr != nil {
		t.Fatalf("GetChildSpecByIssueID: %v", getErr)
	}
	if childSpec.Status != "awaiting_review" {
		t.Fatalf("expected child spec status awaiting_review, got %q", childSpec.Status)
	}

	issue, getErr := testServiceQueries.GetIssue(ctx, fixture.childIssueID)
	if getErr != nil {
		t.Fatalf("GetIssue: %v", getErr)
	}
	if issue.Status != "in_review" {
		t.Fatalf("expected child issue status in_review, got %q", issue.Status)
	}

	var parentTask db.AgentTaskQueue
	if err := testServicePool.QueryRow(ctx, `
		SELECT id, agent_id, issue_id, status, priority, dispatched_at, started_at, completed_at, result, error, created_at, context, runtime_id, session_id, work_dir, trigger_comment_id
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, fixture.parentIssueID, fixture.orchestratorAgentID).Scan(
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
	if parentTask.TriggerCommentID.Valid {
		t.Fatal("expected parent wakeup task to omit trigger_comment_id")
	}
}

func TestSubmitReview_PublishesIssueUpdatedEvent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	bus := events.New()
	published := make([]events.Event, 0, 4)
	bus.SubscribeAll(func(e events.Event) {
		published = append(published, e)
	})
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, bus)

	_, err := svc.SubmitReview(ctx, SubmitReviewParams{
		ChildIssueID:      fixture.childIssueID,
		SubmitterAgentID:  fixture.workerAgentID,
		SubmissionSummary: pgtype.Text{String: "ready for review", Valid: true},
	})
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}

	for _, event := range published {
		if event.Type == protocol.EventIssueUpdated {
			return
		}
	}
	t.Fatalf("expected %s to be published, got %#v", protocol.EventIssueUpdated, published)
}

func TestSubmitReview_RejectsNonWorkerSubmitter(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	_, err := svc.SubmitReview(ctx, SubmitReviewParams{
		ChildIssueID:      fixture.childIssueID,
		SubmitterAgentID:  fixture.orchestratorAgentID,
		SubmissionSummary: pgtype.Text{String: "orchestrator cannot submit worker output", Valid: true},
	})
	if err == nil {
		t.Fatal("expected non-worker submitter to be rejected")
	}
}

func TestSubmitReview_RejectsTerminalChildStates(t *testing.T) {
	ctx := context.Background()
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	for _, terminalStatus := range []string{"done", "blocked"} {
		t.Run(terminalStatus, func(t *testing.T) {
			fixture := seedOrchestrationEntities(t, ctx)
			if _, err := testServicePool.Exec(ctx, `UPDATE child_spec SET status = $2 WHERE child_issue_id = $1`, fixture.childIssueID, terminalStatus); err != nil {
				t.Fatalf("set child spec status: %v", err)
			}
			if _, err := testServicePool.Exec(ctx, `UPDATE issue SET status = $2 WHERE id = $1`, fixture.childIssueID, terminalStatus); err != nil {
				t.Fatalf("set issue status: %v", err)
			}

			_, err := svc.SubmitReview(ctx, SubmitReviewParams{
				ChildIssueID:      fixture.childIssueID,
				SubmitterAgentID:  fixture.workerAgentID,
				SubmissionSummary: pgtype.Text{String: "should fail from terminal state", Valid: true},
			})
			if err == nil {
				t.Fatalf("expected SubmitReview to reject terminal state %q", terminalStatus)
			}
		})
	}
}

func TestSubmitReview_RejectsWhenAlreadyAwaitingReview(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitForReview(t, ctx, svc, fixture, "first submission")

	_, err := svc.SubmitReview(ctx, SubmitReviewParams{
		ChildIssueID:      fixture.childIssueID,
		SubmitterAgentID:  fixture.workerAgentID,
		SubmissionSummary: pgtype.Text{String: "duplicate submission", Valid: true},
	})
	if err == nil {
		t.Fatal("expected SubmitReview to reject duplicate submission while awaiting_review")
	}
}

func TestChildReviewRound_AllowsOnlyOneSubmittedRoundPerChildAtDatabaseLevel(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)

	childSpec, err := testServiceQueries.GetChildSpecByIssueID(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetChildSpecByIssueID: %v", err)
	}

	if _, err := testServicePool.Exec(ctx, `
		INSERT INTO child_review_round (child_spec_id, round_number, decision, summary)
		VALUES ($1, 1, 'submitted', 'first submission')
	`, childSpec.ID); err != nil {
		t.Fatalf("insert first submitted round: %v", err)
	}

	if _, err := testServicePool.Exec(ctx, `
		INSERT INTO child_review_round (child_spec_id, round_number, decision, summary)
		VALUES ($1, 2, 'submitted', 'second submission')
	`, childSpec.ID); err == nil {
		t.Fatal("expected database to reject a second submitted round for the same child")
	}
}

func TestSubmitReview_ReplaysSameIdempotencyKeyAsSameRound(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	first, err := svc.SubmitReview(ctx, SubmitReviewParams{
		ChildIssueID:      fixture.childIssueID,
		SubmitterAgentID:  fixture.workerAgentID,
		SubmissionSummary: pgtype.Text{String: "ready for review", Valid: true},
		IdempotencyKey:    "submit-r1",
		EvidenceJSON:      []byte(`{"summary":"ready for review","evidence":{"pr_url":"https://example.invalid/pr/123"}}`),
	})
	if err != nil {
		t.Fatalf("first SubmitReview: %v", err)
	}

	second, err := svc.SubmitReview(ctx, SubmitReviewParams{
		ChildIssueID:      fixture.childIssueID,
		SubmitterAgentID:  fixture.workerAgentID,
		SubmissionSummary: pgtype.Text{String: "ready for review", Valid: true},
		IdempotencyKey:    "submit-r1",
		EvidenceJSON:      []byte(`{"summary":"ready for review","evidence":{"pr_url":"https://example.invalid/pr/123"}}`),
	})
	if err != nil {
		t.Fatalf("second SubmitReview: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected same review round for repeated idempotency key, got %s and %s", first.ID.String(), second.ID.String())
	}
}

func TestSubmitReview_DefaultsMissingEvidenceToEmptyObject(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	reviewRound, err := svc.SubmitReview(ctx, SubmitReviewParams{
		ChildIssueID:     fixture.childIssueID,
		SubmitterAgentID: fixture.workerAgentID,
	})
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if got := string(reviewRound.SubmissionEvidence); got != `{}` {
		t.Fatalf("expected default submission evidence {}, got %s", got)
	}
}

func TestCreatePlanRevision_ReplaysSameIdempotencyKeyAsSameRevision(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	first, err := svc.CreatePlanRevision(ctx, CreatePlanRevisionParams{
		ChildIssueID:       fixture.childIssueID,
		RequestedByAgentID: fixture.orchestratorAgentID,
		Reason:             pgtype.Text{String: "scope changed", Valid: true},
		PlanContent:        pgtype.Text{String: "updated plan", Valid: true},
		IdempotencyKey:     "replan-r1",
	})
	if err != nil {
		t.Fatalf("first CreatePlanRevision: %v", err)
	}

	second, err := svc.CreatePlanRevision(ctx, CreatePlanRevisionParams{
		ChildIssueID:       fixture.childIssueID,
		RequestedByAgentID: fixture.orchestratorAgentID,
		Reason:             pgtype.Text{String: "scope changed", Valid: true},
		PlanContent:        pgtype.Text{String: "updated plan", Valid: true},
		IdempotencyKey:     "replan-r1",
	})
	if err != nil {
		t.Fatalf("second CreatePlanRevision: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected same plan revision for repeated idempotency key, got %s and %s", first.ID.String(), second.ID.String())
	}
}

func TestCreatePlanRevision_PublishesChildReplannedEvent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	bus := events.New()
	published := make([]events.Event, 0, 4)
	bus.SubscribeAll(func(e events.Event) {
		published = append(published, e)
	})
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, bus)

	_, err := svc.CreatePlanRevision(ctx, CreatePlanRevisionParams{
		ChildIssueID:       fixture.childIssueID,
		RequestedByAgentID: fixture.orchestratorAgentID,
		Reason:             pgtype.Text{String: "scope changed", Valid: true},
		PlanContent:        pgtype.Text{String: "updated plan", Valid: true},
		IdempotencyKey:     "replan-event-r1",
	})
	if err != nil {
		t.Fatalf("CreatePlanRevision: %v", err)
	}

	for _, event := range published {
		if event.Type == protocol.EventChildReplanned {
			return
		}
	}
	t.Fatalf("expected %s to be published, got %#v", protocol.EventChildReplanned, published)
}

func TestCreatePlanRevision_RequeuesWorkerTask(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, fixture.childIssueID, fixture.workerAgentID); err != nil {
		t.Fatalf("clear child worker tasks: %v", err)
	}

	beforeCount := countIssueTasksForAgent(t, ctx, fixture.childIssueID, fixture.workerAgentID)
	if beforeCount != 0 {
		t.Fatalf("expected no worker task before replan, got %d", beforeCount)
	}

	_, err := svc.CreatePlanRevision(ctx, CreatePlanRevisionParams{
		ChildIssueID:       fixture.childIssueID,
		RequestedByAgentID: fixture.orchestratorAgentID,
		Reason:             pgtype.Text{String: "scope changed", Valid: true},
		PlanContent:        pgtype.Text{String: "updated plan", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreatePlanRevision: %v", err)
	}

	afterCount := countIssueTasksForAgent(t, ctx, fixture.childIssueID, fixture.workerAgentID)
	if afterCount != 1 {
		t.Fatalf("expected one worker task after replan, got %d", afterCount)
	}
}

func TestCreatePlanRevision_RejectsNonOrchestratorRequester(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	_, err := svc.CreatePlanRevision(ctx, CreatePlanRevisionParams{
		ChildIssueID:       fixture.childIssueID,
		RequestedByAgentID: fixture.workerAgentID,
		Reason:             pgtype.Text{String: "worker should not request plan revision", Valid: true},
		PlanContent:        pgtype.Text{String: "invalid revision", Valid: true},
	})
	if err == nil {
		t.Fatal("expected non-orchestrator requester to be rejected")
	}
}

func TestCreatePlanRevision_RejectsTerminalChild(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child issue done: %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `UPDATE child_spec SET status = 'done' WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child spec done: %v", err)
	}

	_, err := svc.CreatePlanRevision(ctx, CreatePlanRevisionParams{
		ChildIssueID:       fixture.childIssueID,
		RequestedByAgentID: fixture.orchestratorAgentID,
		Reason:             pgtype.Text{String: "scope changed after completion", Valid: true},
		PlanContent:        pgtype.Text{String: "should be rejected", Valid: true},
	})
	if err == nil {
		t.Fatal("expected terminal child replan to be rejected")
	}
	if got := err.Error(); got != "child is terminal and cannot be replanned" {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestReview_ApprovedCompletesSubmittedRoundAndCompletesChild(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitted := submitForReview(t, ctx, svc, fixture, "ready for review")

	childSpec, err := testServiceQueries.GetChildSpecByIssueID(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetChildSpecByIssueID: %v", err)
	}
	criterion, err := testServiceQueries.CreateChildAcceptanceCriterion(ctx, db.CreateChildAcceptanceCriterionParams{
		ChildSpecID:   childSpec.ID,
		Ordinal:       2,
		CriterionText: "Second criterion",
	})
	if err != nil {
		t.Fatalf("CreateChildAcceptanceCriterion: %v", err)
	}

	res, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionApproved,
		Summary:         pgtype.Text{String: "looks good", Valid: true},
		CriterionResults: []CriterionVerdict{{
			CriterionID: criterion.ID,
			Result:      CriterionResultPass,
			Note:        pgtype.Text{String: "verified", Valid: true},
		}},
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Round.ID != submitted.ID {
		t.Fatal("expected review to complete the submitted round instead of creating a new round")
	}
	if res.Round.RoundNumber != 1 {
		t.Fatalf("expected reviewed round_number=1, got %d", res.Round.RoundNumber)
	}
	if res.Round.Decision != ReviewDecisionApproved {
		t.Fatalf("expected approved decision, got %q", res.Round.Decision)
	}
	if len(res.CriteriaResults) != 1 {
		t.Fatalf("expected 1 criterion result, got %d", len(res.CriteriaResults))
	}

	issue, err := testServiceQueries.GetIssue(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Status != "done" {
		t.Fatalf("expected child issue status done, got %q", issue.Status)
	}

	childSpec, err = testServiceQueries.GetChildSpecByIssueID(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetChildSpecByIssueID: %v", err)
	}
	if childSpec.Status != "done" {
		t.Fatalf("expected child spec status done, got %q", childSpec.Status)
	}
}

func TestReview_PublishesIssueUpdatedEvent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	bus := events.New()
	published := make([]events.Event, 0, 4)
	bus.SubscribeAll(func(e events.Event) {
		published = append(published, e)
	})
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, bus)

	submitted := submitForReview(t, ctx, svc, fixture, "ready for review")
	published = published[:0]

	_, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionApproved,
		Summary:         pgtype.Text{String: "looks good", Valid: true},
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	for _, event := range published {
		if event.Type == protocol.EventIssueUpdated {
			return
		}
	}
	t.Fatalf("expected %s to be published, got %#v", protocol.EventIssueUpdated, published)
}

func TestReview_ApprovedEnqueuesParentTaskWithoutTriggerComment(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, fixture.parentIssueID, fixture.orchestratorAgentID); err != nil {
		t.Fatalf("clear parent pending task: %v", err)
	}

	submitted := submitForReview(t, ctx, svc, fixture, "ready for review")

	res, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionApproved,
		Summary:         pgtype.Text{String: "looks good", Valid: true},
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Escalation != nil {
		t.Fatal("did not expect escalation for approved review")
	}

	var parentTask db.AgentTaskQueue
	if err := testServicePool.QueryRow(ctx, `
		SELECT id, agent_id, issue_id, status, priority, dispatched_at, started_at, completed_at, result, error, created_at, context, runtime_id, session_id, work_dir, trigger_comment_id
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, fixture.parentIssueID, fixture.orchestratorAgentID).Scan(
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
	if parentTask.TriggerCommentID.Valid {
		t.Fatal("expected parent wakeup task to omit trigger_comment_id")
	}
}

func TestReview_ChangesRequestedRequeuesWorkerTask(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `UPDATE child_spec SET max_review_rounds = 2 WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("set max_review_rounds: %v", err)
	}

	submitted := submitForReview(t, ctx, svc, fixture, "ready for review")

	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, fixture.childIssueID, fixture.workerAgentID); err != nil {
		t.Fatalf("clear child worker tasks: %v", err)
	}

	beforeCount := countIssueTasksForAgent(t, ctx, fixture.childIssueID, fixture.workerAgentID)
	if beforeCount != 0 {
		t.Fatalf("expected no worker task before changes_requested review, got %d", beforeCount)
	}

	_, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionChangesRequested,
		Summary:         pgtype.Text{String: "please revise", Valid: true},
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	afterCount := countIssueTasksForAgent(t, ctx, fixture.childIssueID, fixture.workerAgentID)
	if afterCount != 1 {
		t.Fatalf("expected one worker task after changes_requested review, got %d", afterCount)
	}
}

func TestReview_RejectsNonOrchestratorReviewer(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitted := submitForReview(t, ctx, svc, fixture, "ready for review")

	_, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.workerAgentID,
		Verdict:         ReviewDecisionApproved,
		Summary:         pgtype.Text{String: "worker cannot review itself", Valid: true},
	})
	if err == nil {
		t.Fatal("expected non-orchestrator reviewer to be rejected")
	}
}

func TestReview_ChangesRequestedMultipleRoundsDoNotDoubleCount(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `UPDATE child_spec SET max_review_rounds = 2 WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("set max_review_rounds: %v", err)
	}

	firstSubmitted := submitForReview(t, ctx, svc, fixture, "first delivery")
	if firstSubmitted.RoundNumber != 1 {
		t.Fatalf("expected first round_number=1, got %d", firstSubmitted.RoundNumber)
	}

	firstReview, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   firstSubmitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionChangesRequested,
		Summary:         pgtype.Text{String: "please revise", Valid: true},
	})
	if err != nil {
		t.Fatalf("first Review: %v", err)
	}
	if firstReview.Round.RoundNumber != 1 {
		t.Fatalf("expected first reviewed round_number=1, got %d", firstReview.Round.RoundNumber)
	}
	if firstReview.Escalation != nil {
		t.Fatal("did not expect escalation on first changes_requested verdict")
	}

	childSpec, err := testServiceQueries.GetChildSpecByIssueID(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetChildSpecByIssueID after first review: %v", err)
	}
	if childSpec.Status != "in_progress" {
		t.Fatalf("expected child spec status in_progress after first changes_requested, got %q", childSpec.Status)
	}

	secondSubmitted := submitForReview(t, ctx, svc, fixture, "second delivery")
	if secondSubmitted.RoundNumber != 2 {
		t.Fatalf("expected second round_number=2, got %d", secondSubmitted.RoundNumber)
	}

	secondReview, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   secondSubmitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionChangesRequested,
		Summary:         pgtype.Text{String: "still not enough", Valid: true},
	})
	if err != nil {
		t.Fatalf("second Review: %v", err)
	}
	if secondReview.Round.ID != secondSubmitted.ID {
		t.Fatal("expected second review to complete the second submitted round")
	}
	if secondReview.Round.RoundNumber != 2 {
		t.Fatalf("expected second reviewed round_number=2, got %d", secondReview.Round.RoundNumber)
	}
	if secondReview.Escalation == nil {
		t.Fatal("expected escalation when max_review_rounds is exhausted on round 2")
	}

	issue, err := testServiceQueries.GetIssue(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Status != "blocked" {
		t.Fatalf("expected child issue status blocked, got %q", issue.Status)
	}
}

func TestReview_BlockingVerdictCreatesEscalationAndBlocksChildSpec(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitted := submitForReview(t, ctx, svc, fixture, "ready for review")

	res, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionBlocked,
		Summary:         pgtype.Text{String: "blocked by external dependency", Valid: true},
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Escalation == nil {
		t.Fatal("expected escalation to be returned")
	}

	issue, err := testServiceQueries.GetIssue(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Status != "blocked" {
		t.Fatalf("expected child issue status blocked, got %q", issue.Status)
	}

	childSpec, err := testServiceQueries.GetChildSpecByIssueID(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetChildSpecByIssueID: %v", err)
	}
	if childSpec.Status != "blocked" {
		t.Fatalf("expected child spec status blocked, got %q", childSpec.Status)
	}
}

func TestReview_UsesDefaultEscalationReasonForEmptySummary(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitted := submitForReview(t, ctx, svc, fixture, "ready for review")

	res, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionBlocked,
		Summary:         pgtype.Text{String: "", Valid: true},
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Escalation == nil {
		t.Fatal("expected escalation to be returned")
	}
	if res.Escalation.Reason != "review escalation" {
		t.Fatalf("expected default escalation reason, got %q", res.Escalation.Reason)
	}
}

func TestReview_UsesDefaultEscalationReasonForWhitespaceSummary(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitted := submitForReview(t, ctx, svc, fixture, "ready for review")

	res, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionBlocked,
		Summary:         pgtype.Text{String: "   ", Valid: true},
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Escalation == nil {
		t.Fatal("expected escalation to be returned")
	}
	if res.Escalation.Reason != "review escalation" {
		t.Fatalf("expected default escalation reason, got %q", res.Escalation.Reason)
	}
}

func TestReview_RejectsDuplicateCriterionIDs(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitted := submitForReview(t, ctx, svc, fixture, "ready for review")
	childSpec, err := testServiceQueries.GetChildSpecByIssueID(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetChildSpecByIssueID: %v", err)
	}
	criterion, err := testServiceQueries.CreateChildAcceptanceCriterion(ctx, db.CreateChildAcceptanceCriterionParams{
		ChildSpecID:   childSpec.ID,
		Ordinal:       2,
		CriterionText: "Duplicate criterion target",
	})
	if err != nil {
		t.Fatalf("CreateChildAcceptanceCriterion: %v", err)
	}

	_, err = svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionApproved,
		CriterionResults: []CriterionVerdict{
			{CriterionID: criterion.ID, Result: CriterionResultPass},
			{CriterionID: criterion.ID, Result: CriterionResultPass},
		},
	})
	if err == nil {
		t.Fatal("expected duplicate criterion ids to be rejected")
	}
	if got := err.Error(); got != "duplicate criterion id" {
		t.Fatalf("expected duplicate criterion id error, got %q", got)
	}
}

func TestReview_FailsWhenNotAwaitingReview(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	_, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionApproved,
		Summary:         pgtype.Text{String: "should fail", Valid: true},
	})
	if err == nil {
		t.Fatal("expected review without submit-review to fail")
	}
}

func TestReview_RejectsInvalidCriterionResult(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitted := submitForReview(t, ctx, svc, fixture, "ready for review")
	childSpec, err := testServiceQueries.GetChildSpecByIssueID(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetChildSpecByIssueID: %v", err)
	}
	criterion, err := testServiceQueries.CreateChildAcceptanceCriterion(ctx, db.CreateChildAcceptanceCriterionParams{
		ChildSpecID:   childSpec.ID,
		Ordinal:       2,
		CriterionText: "Invalid criterion result target",
	})
	if err != nil {
		t.Fatalf("CreateChildAcceptanceCriterion: %v", err)
	}

	_, err = svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionApproved,
		CriterionResults: []CriterionVerdict{{
			CriterionID: criterion.ID,
			Result:      "bogus",
		}},
	})
	if err == nil {
		t.Fatal("expected invalid criterion result to be rejected")
	}
}

func TestReview_RejectsCriterionFromDifferentChild(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	otherFixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitted := submitForReview(t, ctx, svc, fixture, "ready for review")

	otherSpec, err := testServiceQueries.GetChildSpecByIssueID(ctx, otherFixture.childIssueID)
	if err != nil {
		t.Fatalf("GetChildSpecByIssueID(other): %v", err)
	}
	foreignCriterion, err := testServiceQueries.CreateChildAcceptanceCriterion(ctx, db.CreateChildAcceptanceCriterionParams{
		ChildSpecID:   otherSpec.ID,
		Ordinal:       2,
		CriterionText: "foreign criterion",
	})
	if err != nil {
		t.Fatalf("CreateChildAcceptanceCriterion(other): %v", err)
	}

	_, err = svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionApproved,
		Summary:         pgtype.Text{String: "looks good", Valid: true},
		CriterionResults: []CriterionVerdict{{
			CriterionID: foreignCriterion.ID,
			Result:      CriterionResultPass,
		}},
	})
	if err == nil {
		t.Fatal("expected foreign criterion to be rejected")
	}
}

func TestReview_UsesExplicitReviewRoundID(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `UPDATE child_spec SET max_review_rounds = 2 WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("set max_review_rounds: %v", err)
	}

	firstRound := submitForReview(t, ctx, svc, fixture, "first delivery")
	_, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   firstRound.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionChangesRequested,
		Summary:         pgtype.Text{String: "revise first delivery", Valid: true},
	})
	if err != nil {
		t.Fatalf("first Review: %v", err)
	}

	secondRound := submitForReview(t, ctx, svc, fixture, "second delivery")
	childSpec, err := testServiceQueries.GetChildSpecByIssueID(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetChildSpecByIssueID: %v", err)
	}
	criterion, err := testServiceQueries.CreateChildAcceptanceCriterion(ctx, db.CreateChildAcceptanceCriterionParams{
		ChildSpecID:   childSpec.ID,
		Ordinal:       2,
		CriterionText: "Second round criterion",
	})
	if err != nil {
		t.Fatalf("CreateChildAcceptanceCriterion: %v", err)
	}

	res, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   secondRound.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionApproved,
		Summary:         pgtype.Text{String: "second delivery approved", Valid: true},
		CriterionResults: []CriterionVerdict{{
			CriterionID: criterion.ID,
			Result:      CriterionResultPass,
			Note:        pgtype.Text{String: "verified on second round", Valid: true},
		}},
	})
	if err != nil {
		t.Fatalf("Review second round: %v", err)
	}
	if res.Round.ID != secondRound.ID {
		t.Fatalf("expected explicit review_round_id %s to be completed, got %s", secondRound.ID.String(), res.Round.ID.String())
	}
}

func TestReview_ReplaysSameIdempotencyKeyAsSameOutcome(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitted := submitForReview(t, ctx, svc, fixture, "ready for review")
	childSpec, err := testServiceQueries.GetChildSpecByIssueID(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetChildSpecByIssueID: %v", err)
	}
	criterion, err := testServiceQueries.CreateChildAcceptanceCriterion(ctx, db.CreateChildAcceptanceCriterionParams{
		ChildSpecID:   childSpec.ID,
		Ordinal:       2,
		CriterionText: "Idempotent criterion",
	})
	if err != nil {
		t.Fatalf("CreateChildAcceptanceCriterion: %v", err)
	}

	first, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		IdempotencyKey:  "review-r1",
		Verdict:         ReviewDecisionApproved,
		Summary:         pgtype.Text{String: "approved once", Valid: true},
		CriterionResults: []CriterionVerdict{{
			CriterionID: criterion.ID,
			Result:      CriterionResultPass,
			Note:        pgtype.Text{String: "verified once", Valid: true},
		}},
	})
	if err != nil {
		t.Fatalf("first Review: %v", err)
	}
	if first.Idempotent {
		t.Fatal("expected first review to be non-idempotent")
	}

	second, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   submitted.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		IdempotencyKey:  "review-r1",
		Verdict:         ReviewDecisionApproved,
		Summary:         pgtype.Text{String: "approved again", Valid: true},
		CriterionResults: []CriterionVerdict{{
			CriterionID: criterion.ID,
			Result:      CriterionResultPass,
			Note:        pgtype.Text{String: "verified again", Valid: true},
		}},
	})
	if err != nil {
		t.Fatalf("second Review: %v", err)
	}
	if !second.Idempotent {
		t.Fatal("expected second review to replay prior outcome")
	}
	if second.Round.ID != first.Round.ID {
		t.Fatalf("expected replayed round %s, got %s", first.Round.ID.String(), second.Round.ID.String())
	}
	if len(second.CriteriaResults) != 1 {
		t.Fatalf("expected replayed criterion result, got %d", len(second.CriteriaResults))
	}
	if second.CriteriaResults[0].ID != first.CriteriaResults[0].ID {
		t.Fatalf("expected replayed criterion result %s, got %s", first.CriteriaResults[0].ID.String(), second.CriteriaResults[0].ID.String())
	}
}

func TestReview_RejectsReviewRoundIDFromDifferentChild(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	otherFixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitForReview(t, ctx, svc, fixture, "ready for review")
	foreignRound := submitForReview(t, ctx, svc, otherFixture, "foreign delivery")

	_, err := svc.Review(ctx, ReviewParams{
		ChildIssueID:    fixture.childIssueID,
		ReviewRoundID:   foreignRound.ID,
		ReviewerAgentID: fixture.orchestratorAgentID,
		Verdict:         ReviewDecisionApproved,
		Summary:         pgtype.Text{String: "should reject foreign round", Valid: true},
	})
	if err == nil {
		t.Fatal("expected foreign review round to be rejected")
	}
}

func TestReportBlocked_CreatesEscalationAndBlocksChild(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	res, err := svc.ReportBlocked(ctx, ReportBlockedParams{
		ChildIssueID:    fixture.childIssueID,
		ReporterAgentID: fixture.workerAgentID,
		Reason:          "Waiting for external API key",
	})
	if err != nil {
		t.Fatalf("ReportBlocked: %v", err)
	}
	if !res.Escalation.ID.Valid {
		t.Fatal("expected escalation result to be populated")
	}

	issue, err := testServiceQueries.GetIssue(ctx, fixture.childIssueID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Status != "blocked" {
		t.Fatalf("expected child issue status blocked, got %q", issue.Status)
	}
}

func TestReportBlocked_PublishesChildBlockedReportedEvent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	bus := events.New()
	published := make([]events.Event, 0, 4)
	bus.SubscribeAll(func(e events.Event) {
		published = append(published, e)
	})
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, bus)

	_, err := svc.ReportBlocked(ctx, ReportBlockedParams{
		ChildIssueID:    fixture.childIssueID,
		ReporterAgentID: fixture.workerAgentID,
		Reason:          "Waiting for external API key",
	})
	if err != nil {
		t.Fatalf("ReportBlocked: %v", err)
	}

	for _, event := range published {
		if event.Type == protocol.EventChildBlockedReported {
			return
		}
	}
	t.Fatalf("expected %s to be published, got %#v", protocol.EventChildBlockedReported, published)
}

func TestReportBlocked_PublishesIssueUpdatedEvent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	bus := events.New()
	published := make([]events.Event, 0, 4)
	bus.SubscribeAll(func(e events.Event) {
		published = append(published, e)
	})
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, bus)

	_, err := svc.ReportBlocked(ctx, ReportBlockedParams{
		ChildIssueID:    fixture.childIssueID,
		ReporterAgentID: fixture.workerAgentID,
		Reason:          "Waiting for external API key",
	})
	if err != nil {
		t.Fatalf("ReportBlocked: %v", err)
	}

	for _, event := range published {
		if event.Type == protocol.EventIssueUpdated {
			return
		}
	}
	t.Fatalf("expected %s to be published, got %#v", protocol.EventIssueUpdated, published)
}

func TestReportBlocked_EnqueuesParentTaskWithoutTriggerComment(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, fixture.parentIssueID, fixture.orchestratorAgentID); err != nil {
		t.Fatalf("clear parent pending task: %v", err)
	}

	res, err := svc.ReportBlocked(ctx, ReportBlockedParams{
		ChildIssueID:    fixture.childIssueID,
		ReporterAgentID: fixture.workerAgentID,
		Reason:          "Waiting for external API key",
	})
	if err != nil {
		t.Fatalf("ReportBlocked: %v", err)
	}
	if !res.Escalation.ID.Valid {
		t.Fatal("expected escalation result to be populated")
	}

	var parentTask db.AgentTaskQueue
	if err := testServicePool.QueryRow(ctx, `
		SELECT id, agent_id, issue_id, status, priority, dispatched_at, started_at, completed_at, result, error, created_at, context, runtime_id, session_id, work_dir, trigger_comment_id
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, fixture.parentIssueID, fixture.orchestratorAgentID).Scan(
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
	if parentTask.TriggerCommentID.Valid {
		t.Fatal("expected parent wakeup task to omit trigger_comment_id")
	}
}

func TestSubmitReview_DoesNotEnqueueParentTaskWhenParentTaskAlreadyRunning(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, fixture.parentIssueID, fixture.orchestratorAgentID); err != nil {
		t.Fatalf("clear parent tasks: %v", err)
	}

	var taskID pgtype.UUID
	if err := testServicePool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at)
		SELECT issue.assignee_id, agent.runtime_id, issue.id, 'running', 0, now(), now()
		FROM issue
		JOIN agent ON agent.id = issue.assignee_id
		WHERE issue.id = $1 AND agent.id = $2
		RETURNING id
	`, fixture.parentIssueID, fixture.orchestratorAgentID).Scan(&taskID); err != nil {
		t.Fatalf("insert running parent task: %v", err)
	}

	beforeCount := countParentTasks(t, ctx, fixture)

	if _, err := svc.SubmitReview(ctx, SubmitReviewParams{
		ChildIssueID:      fixture.childIssueID,
		SubmitterAgentID:  fixture.workerAgentID,
		SubmissionSummary: pgtype.Text{String: "ready for review", Valid: true},
	}); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}

	afterCount := countParentTasks(t, ctx, fixture)
	if afterCount != beforeCount {
		t.Fatalf("expected running parent task to suppress duplicate enqueue, before=%d after=%d", beforeCount, afterCount)
	}
}

func TestReportBlocked_DoesNotEnqueueParentTaskWhenParentTaskAlreadyRunning(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, fixture.parentIssueID, fixture.orchestratorAgentID); err != nil {
		t.Fatalf("clear parent tasks: %v", err)
	}

	var taskID pgtype.UUID
	if err := testServicePool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at)
		SELECT issue.assignee_id, agent.runtime_id, issue.id, 'running', 0, now(), now()
		FROM issue
		JOIN agent ON agent.id = issue.assignee_id
		WHERE issue.id = $1 AND agent.id = $2
		RETURNING id
	`, fixture.parentIssueID, fixture.orchestratorAgentID).Scan(&taskID); err != nil {
		t.Fatalf("insert running parent task: %v", err)
	}

	beforeCount := countParentTasks(t, ctx, fixture)

	if _, err := svc.ReportBlocked(ctx, ReportBlockedParams{
		ChildIssueID:    fixture.childIssueID,
		ReporterAgentID: fixture.workerAgentID,
		Reason:          "Waiting for external API key",
	}); err != nil {
		t.Fatalf("ReportBlocked: %v", err)
	}

	afterCount := countParentTasks(t, ctx, fixture)
	if afterCount != beforeCount {
		t.Fatalf("expected running parent task to suppress duplicate enqueue, before=%d after=%d", beforeCount, afterCount)
	}
}

func TestSubmitReview_ConcurrentSiblingChildrenEnqueueOnlyOneParentTask(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	siblingChildIssueID := seedSiblingChildIssue(t, ctx, fixture)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, fixture.parentIssueID, fixture.orchestratorAgentID); err != nil {
		t.Fatalf("clear parent tasks: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := svc.SubmitReview(ctx, SubmitReviewParams{
			ChildIssueID:      fixture.childIssueID,
			SubmitterAgentID:  fixture.workerAgentID,
			SubmissionSummary: pgtype.Text{String: "ready from child 1", Valid: true},
		})
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := svc.SubmitReview(ctx, SubmitReviewParams{
			ChildIssueID:      siblingChildIssueID,
			SubmitterAgentID:  fixture.workerAgentID,
			SubmissionSummary: pgtype.Text{String: "ready from child 2", Valid: true},
		})
		errs <- err
	}()
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("SubmitReview concurrency failed: %v", err)
		}
	}

	if got := countParentTasks(t, ctx, fixture); got != 1 {
		t.Fatalf("expected exactly one parent task after concurrent submissions, got %d", got)
	}
}

func TestReportBlocked_RejectsUnauthorizedReporter(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	otherFixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	_, err := svc.ReportBlocked(ctx, ReportBlockedParams{
		ChildIssueID:    fixture.childIssueID,
		ReporterAgentID: otherFixture.workerAgentID,
		Reason:          "unauthorized reporter",
	})
	if err == nil {
		t.Fatal("expected non-worker/non-orchestrator reporter to be rejected")
	}
}

func TestReportBlocked_RejectsTerminalChildStates(t *testing.T) {
	ctx := context.Background()
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	for _, terminalStatus := range []string{"done", "blocked"} {
		t.Run(terminalStatus, func(t *testing.T) {
			fixture := seedOrchestrationEntities(t, ctx)
			if _, err := testServicePool.Exec(ctx, `UPDATE child_spec SET status = $2 WHERE child_issue_id = $1`, fixture.childIssueID, terminalStatus); err != nil {
				t.Fatalf("set child spec status: %v", err)
			}
			if _, err := testServicePool.Exec(ctx, `UPDATE issue SET status = $2 WHERE id = $1`, fixture.childIssueID, terminalStatus); err != nil {
				t.Fatalf("set issue status: %v", err)
			}

			_, err := svc.ReportBlocked(ctx, ReportBlockedParams{
				ChildIssueID:    fixture.childIssueID,
				ReporterAgentID: fixture.workerAgentID,
				Reason:          "should fail from terminal state",
			})
			if err == nil {
				t.Fatalf("expected ReportBlocked to reject terminal state %q", terminalStatus)
			}
		})
	}
}

func TestRecoverStuckAwaitingReview_RequeuesReviewerTask(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitForReview(t, ctx, svc, fixture, "ready for review")
	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, fixture.childIssueID, fixture.workerAgentID); err != nil {
		t.Fatalf("clear child worker tasks: %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, fixture.childIssueID, fixture.orchestratorAgentID); err != nil {
		t.Fatalf("clear child reviewer tasks: %v", err)
	}

	beforeCount := countIssueTasksForAgent(t, ctx, fixture.childIssueID, fixture.orchestratorAgentID)
	if beforeCount != 0 {
		t.Fatalf("expected no reviewer task before recovery, got %d", beforeCount)
	}

	if err := svc.RecoverStuckWorkflows(ctx); err != nil {
		t.Fatalf("RecoverStuckWorkflows: %v", err)
	}

	afterCount := countIssueTasksForAgent(t, ctx, fixture.childIssueID, fixture.orchestratorAgentID)
	if afterCount != 1 {
		t.Fatalf("expected one reviewer task after recovery, got %d", afterCount)
	}
}

func TestRecoverStuckAwaitingReview_DoesNotDuplicateRunningReviewerTask(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitForReview(t, ctx, svc, fixture, "ready for review")
	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, fixture.childIssueID, fixture.workerAgentID); err != nil {
		t.Fatalf("clear child worker tasks: %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, fixture.childIssueID, fixture.orchestratorAgentID); err != nil {
		t.Fatalf("clear child reviewer tasks: %v", err)
	}

	orchestratorAgent, err := testServiceQueries.GetAgent(ctx, fixture.orchestratorAgentID)
	if err != nil {
		t.Fatalf("GetAgent(orchestrator): %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, started_at)
		VALUES ($1, $2, $3, 'running', 1, now())
	`, fixture.orchestratorAgentID, orchestratorAgent.RuntimeID, fixture.childIssueID); err != nil {
		t.Fatalf("insert running reviewer task: %v", err)
	}

	beforeCount := countIssueTasksForAgent(t, ctx, fixture.childIssueID, fixture.orchestratorAgentID)
	if beforeCount != 1 {
		t.Fatalf("expected one running reviewer task before recovery, got %d", beforeCount)
	}

	if err := svc.RecoverStuckWorkflows(ctx); err != nil {
		t.Fatalf("RecoverStuckWorkflows: %v", err)
	}

	afterCount := countIssueTasksForAgent(t, ctx, fixture.childIssueID, fixture.orchestratorAgentID)
	if afterCount != 1 {
		t.Fatalf("expected recovery not to duplicate running reviewer task, got %d", afterCount)
	}
}

func TestFinalizeParent_AllChildrenDoneMarksComplete(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child done: %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `UPDATE child_spec SET status = 'done' WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child spec done: %v", err)
	}

	result, err := svc.FinalizeParent(ctx, FinalizeParentParams{
		ParentIssueID:         fixture.parentIssueID,
		OrchestratorAgentID:   fixture.orchestratorAgentID,
		RequireTerminalChilds: true,
	})
	if err != nil {
		t.Fatalf("FinalizeParent: %v", err)
	}
	if result.FinalOutcome != "complete" {
		t.Fatalf("expected final outcome complete, got %q", result.FinalOutcome)
	}
	if result.Status != "done" {
		t.Fatalf("expected final status done, got %q", result.Status)
	}

	parentIssue, err := testServiceQueries.GetIssue(ctx, fixture.parentIssueID)
	if err != nil {
		t.Fatalf("GetIssue(parent): %v", err)
	}
	if parentIssue.Status != "done" {
		t.Fatalf("expected persisted parent status done, got %q", parentIssue.Status)
	}
	if !parentIssue.WorkflowFinalOutcome.Valid || parentIssue.WorkflowFinalOutcome.String != "complete" {
		t.Fatalf("expected persisted parent final outcome complete, got %#v", parentIssue.WorkflowFinalOutcome)
	}
}

func TestFinalizeParent_BlockedChildMarksBlocked(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `UPDATE issue SET status = 'blocked' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child blocked: %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `UPDATE child_spec SET status = 'blocked' WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child spec blocked: %v", err)
	}

	result, err := svc.FinalizeParent(ctx, FinalizeParentParams{
		ParentIssueID:         fixture.parentIssueID,
		OrchestratorAgentID:   fixture.orchestratorAgentID,
		RequireTerminalChilds: true,
	})
	if err != nil {
		t.Fatalf("FinalizeParent: %v", err)
	}
	if result.FinalOutcome != "blocked" {
		t.Fatalf("expected final outcome blocked, got %q", result.FinalOutcome)
	}
	if result.Status != "blocked" {
		t.Fatalf("expected final status blocked, got %q", result.Status)
	}

	parentIssue, err := testServiceQueries.GetIssue(ctx, fixture.parentIssueID)
	if err != nil {
		t.Fatalf("GetIssue(parent): %v", err)
	}
	if !parentIssue.WorkflowFinalOutcome.Valid || parentIssue.WorkflowFinalOutcome.String != "blocked" {
		t.Fatalf("expected persisted parent final outcome blocked, got %#v", parentIssue.WorkflowFinalOutcome)
	}
}

func TestFinalizeParent_PublishesParentFinalizedEvent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	bus := events.New()
	published := make([]events.Event, 0, 4)
	bus.SubscribeAll(func(e events.Event) {
		published = append(published, e)
	})
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, bus)

	if _, err := testServicePool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child done: %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `UPDATE child_spec SET status = 'done' WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child spec done: %v", err)
	}

	_, err := svc.FinalizeParent(ctx, FinalizeParentParams{
		ParentIssueID:         fixture.parentIssueID,
		OrchestratorAgentID:   fixture.orchestratorAgentID,
		RequireTerminalChilds: true,
	})
	if err != nil {
		t.Fatalf("FinalizeParent: %v", err)
	}

	for _, event := range published {
		if event.Type == protocol.EventParentFinalized {
			return
		}
	}
	t.Fatalf("expected %s to be published, got %#v", protocol.EventParentFinalized, published)
}

func TestFinalizeParent_PublishesIssueUpdatedEvent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	bus := events.New()
	published := make([]events.Event, 0, 4)
	bus.SubscribeAll(func(e events.Event) {
		published = append(published, e)
	})
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, bus)

	if _, err := testServicePool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child done: %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `UPDATE child_spec SET status = 'done' WHERE child_issue_id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child spec done: %v", err)
	}

	_, err := svc.FinalizeParent(ctx, FinalizeParentParams{
		ParentIssueID:         fixture.parentIssueID,
		OrchestratorAgentID:   fixture.orchestratorAgentID,
		RequireTerminalChilds: true,
	})
	if err != nil {
		t.Fatalf("FinalizeParent: %v", err)
	}

	for _, event := range published {
		if event.Type == protocol.EventIssueUpdated {
			payload, ok := event.Payload.(map[string]any)
			if !ok {
				t.Fatalf("expected payload map, got %T", event.Payload)
			}
			issue, ok := payload["issue"].(map[string]any)
			if !ok {
				t.Fatalf("expected issue payload map, got %T", payload["issue"])
			}
			if issue["id"] != fixture.parentIssueID.String() {
				t.Fatalf("expected parent issue id %s, got %#v", fixture.parentIssueID.String(), issue["id"])
			}
			return
		}
	}
	t.Fatalf("expected %s to be published, got %#v", protocol.EventIssueUpdated, published)
}

func TestFinalizeParent_RejectsNonTerminalChildWhenRequired(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	_, err := svc.FinalizeParent(ctx, FinalizeParentParams{
		ParentIssueID:         fixture.parentIssueID,
		OrchestratorAgentID:   fixture.orchestratorAgentID,
		RequireTerminalChilds: true,
	})
	if !errors.Is(err, ErrFinalizeParentChildrenNotReady) {
		t.Fatalf("expected ErrFinalizeParentChildrenNotReady, got %v", err)
	}
}

func TestFinalizeParent_RejectsUnauthorizedAgent(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	_, err := svc.FinalizeParent(ctx, FinalizeParentParams{
		ParentIssueID:         fixture.parentIssueID,
		OrchestratorAgentID:   fixture.workerAgentID,
		RequireTerminalChilds: true,
	})
	if !errors.Is(err, ErrFinalizeParentUnauthorized) {
		t.Fatalf("expected ErrFinalizeParentUnauthorized, got %v", err)
	}
}

func TestFinalizeParent_RejectsParentWithoutChildren(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("delete child issue: %v", err)
	}

	_, err := svc.FinalizeParent(ctx, FinalizeParentParams{
		ParentIssueID:         fixture.parentIssueID,
		OrchestratorAgentID:   fixture.orchestratorAgentID,
		RequireTerminalChilds: true,
	})
	if !errors.Is(err, ErrFinalizeParentNoChildren) {
		t.Fatalf("expected ErrFinalizeParentNoChildren, got %v", err)
	}
}

func TestFinalizeParent_RejectsInconsistentChildOrchestrator(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child done: %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `
		UPDATE child_spec
		SET status = 'done', orchestrator_agent_id = $2
		WHERE child_issue_id = $1
	`, fixture.childIssueID, fixture.workerAgentID); err != nil {
		t.Fatalf("corrupt child spec orchestrator: %v", err)
	}

	_, err := svc.FinalizeParent(ctx, FinalizeParentParams{
		ParentIssueID:         fixture.parentIssueID,
		OrchestratorAgentID:   fixture.orchestratorAgentID,
		RequireTerminalChilds: true,
	})
	if !errors.Is(err, ErrFinalizeParentStateInconsistent) {
		t.Fatalf("expected ErrFinalizeParentStateInconsistent, got %v", err)
	}
}

func TestFinalizeParent_RejectsDriftedChildIssueStatus(t *testing.T) {
	ctx := context.Background()
	fixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	if _, err := testServicePool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, fixture.childIssueID); err != nil {
		t.Fatalf("mark child issue done: %v", err)
	}

	_, err := svc.FinalizeParent(ctx, FinalizeParentParams{
		ParentIssueID:         fixture.parentIssueID,
		OrchestratorAgentID:   fixture.orchestratorAgentID,
		RequireTerminalChilds: true,
	})
	if !errors.Is(err, ErrFinalizeParentStateInconsistent) {
		t.Fatalf("expected ErrFinalizeParentStateInconsistent, got %v", err)
	}
}

func TestRecoverStuckWorkflows_ContinuesAfterChildRecoveryError(t *testing.T) {
	ctx := context.Background()
	validFixture := seedOrchestrationEntities(t, ctx)
	brokenFixture := seedOrchestrationEntities(t, ctx)
	svc := NewOrchestrationService(testServicePool, testServiceQueries, nil, nil)

	submitForReview(t, ctx, svc, validFixture, "ready for review")
	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, validFixture.childIssueID, validFixture.orchestratorAgentID); err != nil {
		t.Fatalf("clear valid reviewer tasks: %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, validFixture.childIssueID, validFixture.workerAgentID); err != nil {
		t.Fatalf("clear valid worker tasks: %v", err)
	}

	if _, err := testServicePool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, brokenFixture.childIssueID); err != nil {
		t.Fatalf("mark broken child done: %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `UPDATE child_spec SET status = 'done' WHERE child_issue_id = $1`, brokenFixture.childIssueID); err != nil {
		t.Fatalf("mark broken child spec done: %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, brokenFixture.parentIssueID, brokenFixture.orchestratorAgentID); err != nil {
		t.Fatalf("clear broken parent tasks: %v", err)
	}
	if _, err := testServicePool.Exec(ctx, `UPDATE issue SET assignee_id = gen_random_uuid() WHERE id = $1`, brokenFixture.parentIssueID); err != nil {
		t.Fatalf("corrupt broken parent assignee: %v", err)
	}

	err := svc.RecoverStuckWorkflows(ctx)
	if err == nil {
		t.Fatal("expected RecoverStuckWorkflows to return aggregated error")
	}
	if !strings.Contains(err.Error(), "recover child") {
		t.Fatalf("expected aggregated recovery error to include child context, got %v", err)
	}

	afterCount := countIssueTasksForAgent(t, ctx, validFixture.childIssueID, validFixture.orchestratorAgentID)
	if afterCount != 1 {
		t.Fatalf("expected valid child reviewer task to be recovered despite sibling failure, got %d", afterCount)
	}
}
