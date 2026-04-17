package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var testHandler *Handler
var testPool *pgxpool.Pool
var testUserID string
var testWorkspaceID string

const (
	handlerTestEmail         = "handler-test@multica.ai"
	handlerTestName          = "Handler Test User"
	handlerTestWorkspaceSlug = "handler-tests"
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Printf("Skipping tests: could not connect to database: %v\n", err)
		os.Exit(0)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Printf("Skipping tests: database not reachable: %v\n", err)
		pool.Close()
		os.Exit(0)
	}

	queries := db.New(pool)
	hub := realtime.NewHub()
	go hub.Run()
	bus := events.New()
	emailSvc := service.NewEmailService()
	testHandler = New(queries, pool, hub, bus, emailSvc, nil, nil)
	testPool = pool

	testUserID, testWorkspaceID, err = setupHandlerTestFixture(ctx, pool)
	if err != nil {
		fmt.Printf("Failed to set up handler test fixture: %v\n", err)
		pool.Close()
		os.Exit(1)
	}

	code := m.Run()
	if err := cleanupHandlerTestFixture(context.Background(), pool); err != nil {
		fmt.Printf("Failed to clean up handler test fixture: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	pool.Close()
	os.Exit(code)
}

func ensureRuntimeRequestTables(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runtime_id_workspace_daemon_unique
			ON agent_runtime(id, workspace_id, daemon_id)
	`); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS runtime_ping_request (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
			runtime_id UUID NOT NULL,
			daemon_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed', 'timeout')),
			output TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			duration_ms BIGINT,
			claimed_at TIMESTAMPTZ,
			completed_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			FOREIGN KEY (runtime_id, workspace_id, daemon_id)
				REFERENCES agent_runtime(id, workspace_id, daemon_id)
				ON DELETE CASCADE
		)
	`); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_runtime_ping_request_runtime_created
			ON runtime_ping_request(runtime_id, created_at ASC)
	`); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_runtime_ping_request_pending
			ON runtime_ping_request(created_at ASC)
			WHERE status = 'pending'
	`); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS runtime_update_request (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
			runtime_id UUID NOT NULL,
			daemon_id TEXT NOT NULL,
			target_version TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed', 'timeout')),
			output TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			claimed_at TIMESTAMPTZ,
			completed_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			FOREIGN KEY (runtime_id, workspace_id, daemon_id)
				REFERENCES agent_runtime(id, workspace_id, daemon_id)
				ON DELETE CASCADE
		)
	`); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_runtime_update_request_runtime_created
			ON runtime_update_request(runtime_id, created_at ASC)
	`); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_runtime_update_request_pending
			ON runtime_update_request(created_at ASC)
			WHERE status = 'pending'
	`); err != nil {
		return err
	}

	return nil
}

func setupHandlerTestFixture(ctx context.Context, pool *pgxpool.Pool) (string, string, error) {
	if err := cleanupHandlerTestFixture(ctx, pool); err != nil {
		return "", "", err
	}

	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, $2)
		RETURNING id
	`, handlerTestName, handlerTestEmail).Scan(&userID); err != nil {
		return "", "", err
	}

	var workspaceID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Handler Tests", handlerTestWorkspaceSlug, "Temporary workspace for handler tests", "HAN").Scan(&workspaceID); err != nil {
		return "", "", err
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, userID); err != nil {
		return "", "", err
	}

	if err := ensureRuntimeRequestTables(ctx, pool); err != nil {
		return "", "", err
	}

	var runtimeID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, workspaceID, "handler-test-daemon", "Handler Test Runtime", "handler_test_runtime", "Handler test runtime").Scan(&runtimeID); err != nil {
		return "", "", err
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4)
	`, workspaceID, "Handler Test Agent", runtimeID, userID); err != nil {
		return "", "", err
	}

	return userID, workspaceID, nil
}

func cleanupHandlerTestFixture(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, handlerTestWorkspaceSlug); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, handlerTestEmail); err != nil {
		return err
	}
	return nil
}

func newRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	return req
}

func withURLParam(req *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func newDaemonAuthenticatedRequest(t *testing.T, method, path, workspaceID, daemonID string, body any) *http.Request {
	t.Helper()

	token := fmt.Sprintf("mdt_handler_test_%d", time.Now().UnixNano())
	_, err := testHandler.Queries.CreateDaemonToken(context.Background(), db.CreateDaemonTokenParams{
		TokenHash:   auth.HashToken(token),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    daemonID,
		ExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(time.Hour),
			Valid: true,
		},
	})
	if err != nil {
		t.Fatalf("create daemon token: %v", err)
	}

	req := newRequest(method, path, body)
	req.Header.Del("X-User-ID")
	req.Header.Del("X-Workspace-ID")
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func performDaemonRequest(handlerFunc http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	wrapped := middleware.DaemonAuth(testHandler.Queries)(handlerFunc)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	return w
}

func createDaemonTaskFixture(t *testing.T, workspaceID, daemonID, runtimeName, issueTitle, taskStatus string) string {
	t.Helper()
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, workspaceID, daemonID, runtimeName, runtimeName+"_provider", runtimeName+" device").Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4)
		RETURNING id
	`, workspaceID, runtimeName+" Agent", runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, position)
		VALUES ($1, $2, 'todo', 'none', 'member', $3, floor(random() * 1000000)::int + 10000, 0)
		RETURNING id
	`, workspaceID, issueTitle, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, $4, 0)
		RETURNING id
	`, agentID, runtimeID, issueID, taskStatus).Scan(&taskID); err != nil {
		t.Fatalf("create task: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})

	return taskID
}

func TestIssueCRUD(t *testing.T) {
	// Create
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":    "Test issue from Go test",
		"status":   "todo",
		"priority": "medium",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created IssueResponse
	json.NewDecoder(w.Body).Decode(&created)
	if created.Title != "Test issue from Go test" {
		t.Fatalf("CreateIssue: expected title 'Test issue from Go test', got '%s'", created.Title)
	}
	if created.Status != "todo" {
		t.Fatalf("CreateIssue: expected status 'todo', got '%s'", created.Status)
	}
	issueID := created.ID

	// Get
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/issues/"+issueID, nil)
	req = withURLParam(req, "id", issueID)
	testHandler.GetIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetIssue: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var fetched IssueResponse
	json.NewDecoder(w.Body).Decode(&fetched)
	if fetched.ID != issueID {
		t.Fatalf("GetIssue: expected id '%s', got '%s'", issueID, fetched.ID)
	}

	// Update - partial (only status)
	w = httptest.NewRecorder()
	status := "in_progress"
	req = newRequest("PUT", "/api/issues/"+issueID, map[string]any{
		"status": status,
	})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated IssueResponse
	json.NewDecoder(w.Body).Decode(&updated)
	if updated.Status != "in_progress" {
		t.Fatalf("UpdateIssue: expected status 'in_progress', got '%s'", updated.Status)
	}
	if updated.Title != "Test issue from Go test" {
		t.Fatalf("UpdateIssue: title should be preserved, got '%s'", updated.Title)
	}
	if updated.Priority != "medium" {
		t.Fatalf("UpdateIssue: priority should be preserved, got '%s'", updated.Priority)
	}

	// List
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/issues?workspace_id="+testWorkspaceID, nil)
	testHandler.ListIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListIssues: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var listResp map[string]any
	json.NewDecoder(w.Body).Decode(&listResp)
	issues := listResp["issues"].([]any)
	if len(issues) == 0 {
		t.Fatal("ListIssues: expected at least 1 issue")
	}

	// Delete
	w = httptest.NewRecorder()
	req = newRequest("DELETE", "/api/issues/"+issueID, nil)
	req = withURLParam(req, "id", issueID)
	testHandler.DeleteIssue(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteIssue: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify deleted
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/issues/"+issueID, nil)
	req = withURLParam(req, "id", issueID)
	testHandler.GetIssue(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GetIssue after delete: expected 404, got %d", w.Code)
	}
}

func TestCommentCRUD(t *testing.T) {
	// Create an issue first
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "Comment test issue",
	})
	testHandler.CreateIssue(w, req)
	var issue IssueResponse
	json.NewDecoder(w.Body).Decode(&issue)
	issueID := issue.ID

	// Create comment
	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "Test comment from Go test",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.CreateComment(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// List comments
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/issues/"+issueID+"/comments", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.ListComments(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListComments: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var comments []CommentResponse
	json.NewDecoder(w.Body).Decode(&comments)
	if len(comments) != 1 {
		t.Fatalf("ListComments: expected 1 comment, got %d", len(comments))
	}
	if comments[0].Content != "Test comment from Go test" {
		t.Fatalf("ListComments: expected content 'Test comment from Go test', got '%s'", comments[0].Content)
	}

	// Cleanup
	w = httptest.NewRecorder()
	req = newRequest("DELETE", "/api/issues/"+issueID, nil)
	req = withURLParam(req, "id", issueID)
	testHandler.DeleteIssue(w, req)
}

func TestAgentCRUD(t *testing.T) {
	// List agents
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/agents?workspace_id="+testWorkspaceID, nil)
	testHandler.ListAgents(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListAgents: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var agents []AgentResponse
	json.NewDecoder(w.Body).Decode(&agents)
	if len(agents) == 0 {
		t.Fatal("ListAgents: expected at least 1 agent")
	}

	// Update agent status
	agentID := agents[0].ID
	w = httptest.NewRecorder()
	req = newRequest("PUT", "/api/agents/"+agentID, map[string]any{
		"status": "idle",
	})
	req = withURLParam(req, "id", agentID)
	testHandler.UpdateAgent(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateAgent: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated AgentResponse
	json.NewDecoder(w.Body).Decode(&updated)
	if updated.Status != "idle" {
		t.Fatalf("UpdateAgent: expected status 'idle', got '%s'", updated.Status)
	}
	if updated.Name != agents[0].Name {
		t.Fatalf("UpdateAgent: name should be preserved, got '%s'", updated.Name)
	}
}

func TestWorkspaceCRUD(t *testing.T) {
	// List workspaces
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/workspaces", nil)
	testHandler.ListWorkspaces(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListWorkspaces: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var workspaces []WorkspaceResponse
	json.NewDecoder(w.Body).Decode(&workspaces)
	if len(workspaces) == 0 {
		t.Fatal("ListWorkspaces: expected at least 1 workspace")
	}

	// Get workspace
	wsID := workspaces[0].ID
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/workspaces/"+wsID, nil)
	req = withURLParam(req, "id", wsID)
	testHandler.GetWorkspace(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetWorkspace: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSendCode(t *testing.T) {
	w := httptest.NewRecorder()
	body := map[string]string{"email": "sendcode-test@multica.ai"}
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	testHandler.SendCode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SendCode: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["message"] == "" {
		t.Fatal("SendCode: expected non-empty message")
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM verification_code WHERE email = $1`, "sendcode-test@multica.ai")
	})
}

func TestSendCodeRateLimit(t *testing.T) {
	const email = "ratelimit-test@multica.ai"
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM verification_code WHERE email = $1`, email)
	})

	// First request should succeed
	w := httptest.NewRecorder()
	body := map[string]string{"email": email}
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	testHandler.SendCode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SendCode (first): expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Second request within 60s should be rate limited
	w = httptest.NewRecorder()
	buf.Reset()
	json.NewEncoder(&buf).Encode(body)
	req = httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	testHandler.SendCode(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("SendCode (second): expected 429, got %d: %s", w.Code, w.Body.String())
	}
}

func TestVerifyCode(t *testing.T) {
	const email = "verify-test@multica.ai"
	ctx := context.Background()

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		user, err := testHandler.Queries.GetUserByEmail(ctx, email)
		if err == nil {
			workspaces, listErr := testHandler.Queries.ListWorkspaces(ctx, user.ID)
			if listErr == nil {
				for _, workspace := range workspaces {
					_ = testHandler.Queries.DeleteWorkspace(ctx, workspace.ID)
				}
			}
		}
		testPool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, email)
	})

	// Send code first
	w := httptest.NewRecorder()
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email})
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	testHandler.SendCode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SendCode: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Read code from DB
	dbCode, err := testHandler.Queries.GetLatestVerificationCode(ctx, email)
	if err != nil {
		t.Fatalf("GetLatestVerificationCode: %v", err)
	}

	// Verify with correct code
	w = httptest.NewRecorder()
	buf.Reset()
	json.NewEncoder(&buf).Encode(map[string]string{"email": email, "code": dbCode.Code})
	req = httptest.NewRequest("POST", "/auth/verify-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	testHandler.VerifyCode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("VerifyCode: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp LoginResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Token == "" {
		t.Fatal("VerifyCode: expected non-empty token")
	}
	if resp.User.Email != email {
		t.Fatalf("VerifyCode: expected email '%s', got '%s'", email, resp.User.Email)
	}
}

func TestVerifyCodeWrongCode(t *testing.T) {
	const email = "wrong-code-test@multica.ai"
	ctx := context.Background()

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
	})

	// Send code
	w := httptest.NewRecorder()
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email})
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	testHandler.SendCode(w, req)

	// Verify with wrong code
	w = httptest.NewRecorder()
	buf.Reset()
	json.NewEncoder(&buf).Encode(map[string]string{"email": email, "code": "000000"})
	req = httptest.NewRequest("POST", "/auth/verify-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	testHandler.VerifyCode(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("VerifyCode (wrong code): expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestVerifyCodeBruteForceProtection(t *testing.T) {
	const email = "bruteforce-test@multica.ai"
	ctx := context.Background()

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
	})

	// Send code
	w := httptest.NewRecorder()
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email})
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	testHandler.SendCode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SendCode: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Read actual code so we can try it after lockout
	dbCode, err := testHandler.Queries.GetLatestVerificationCode(ctx, email)
	if err != nil {
		t.Fatalf("GetLatestVerificationCode: %v", err)
	}

	// Exhaust all 5 attempts with wrong codes
	for i := 0; i < 5; i++ {
		w = httptest.NewRecorder()
		buf.Reset()
		json.NewEncoder(&buf).Encode(map[string]string{"email": email, "code": "000000"})
		req = httptest.NewRequest("POST", "/auth/verify-code", &buf)
		req.Header.Set("Content-Type", "application/json")
		testHandler.VerifyCode(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: expected 400, got %d", i+1, w.Code)
		}
	}

	// Now even the correct code should be rejected (code is locked out)
	w = httptest.NewRecorder()
	buf.Reset()
	json.NewEncoder(&buf).Encode(map[string]string{"email": email, "code": dbCode.Code})
	req = httptest.NewRequest("POST", "/auth/verify-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	testHandler.VerifyCode(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("after lockout: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestVerifyCodeCreatesWorkspace(t *testing.T) {
	const email = "workspace-verify-test@multica.ai"
	ctx := context.Background()

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		user, err := testHandler.Queries.GetUserByEmail(ctx, email)
		if err == nil {
			workspaces, listErr := testHandler.Queries.ListWorkspaces(ctx, user.ID)
			if listErr == nil {
				for _, workspace := range workspaces {
					_ = testHandler.Queries.DeleteWorkspace(ctx, workspace.ID)
				}
			}
		}
		testPool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, email)
	})

	// Send code
	w := httptest.NewRecorder()
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email})
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	testHandler.SendCode(w, req)

	// Read code from DB
	dbCode, err := testHandler.Queries.GetLatestVerificationCode(ctx, email)
	if err != nil {
		t.Fatalf("GetLatestVerificationCode: %v", err)
	}

	// Verify
	w = httptest.NewRecorder()
	buf.Reset()
	json.NewEncoder(&buf).Encode(map[string]string{"email": email, "code": dbCode.Code})
	req = httptest.NewRequest("POST", "/auth/verify-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	testHandler.VerifyCode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("VerifyCode: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	user, err := testHandler.Queries.GetUserByEmail(ctx, email)
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}

	workspaces, err := testHandler.Queries.ListWorkspaces(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("ListWorkspaces: expected 1 workspace, got %d", len(workspaces))
	}
	if !strings.Contains(workspaces[0].Name, "Workspace") {
		t.Fatalf("expected auto-created workspace name, got %q", workspaces[0].Name)
	}
}

func TestResolveActor(t *testing.T) {
	ctx := context.Background()

	// Look up the agent created by the test fixture.
	var agentID string
	err := testPool.QueryRow(ctx,
		`SELECT id FROM agent WHERE workspace_id = $1 AND name = $2`,
		testWorkspaceID, "Handler Test Agent",
	).Scan(&agentID)
	if err != nil {
		t.Fatalf("failed to find test agent: %v", err)
	}

	// Create a task for the agent so we can test X-Task-ID validation.
	var issueID string
	err = testPool.QueryRow(ctx,
		`INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, position)
		 VALUES ($1, 'resolveActor test', 'todo', 'none', 'member', $2, 9999, 0)
		 RETURNING id`, testWorkspaceID, testUserID,
	).Scan(&issueID)
	if err != nil {
		t.Fatalf("failed to create test issue: %v", err)
	}

	// Look up runtime_id for the agent.
	var runtimeID string
	err = testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID)
	if err != nil {
		t.Fatalf("failed to get agent runtime_id: %v", err)
	}

	var taskID string
	err = testPool.QueryRow(ctx,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		 VALUES ($1, $2, $3, 'queued', 0)
		 RETURNING id`, agentID, runtimeID, issueID,
	).Scan(&taskID)
	if err != nil {
		t.Fatalf("failed to create test task: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})

	tests := []struct {
		name          string
		agentIDHeader string
		taskIDHeader  string
		wantActorType string
		wantIsAgent   bool
	}{
		{
			name:          "no headers returns member",
			wantActorType: "member",
		},
		{
			name:          "valid agent ID returns agent",
			agentIDHeader: agentID,
			wantActorType: "agent",
			wantIsAgent:   true,
		},
		{
			name:          "non-existent agent ID returns member",
			agentIDHeader: "00000000-0000-0000-0000-000000000099",
			wantActorType: "member",
		},
		{
			name:          "valid agent + valid task returns agent",
			agentIDHeader: agentID,
			taskIDHeader:  taskID,
			wantActorType: "agent",
			wantIsAgent:   true,
		},
		{
			name:          "valid agent + wrong task returns member",
			agentIDHeader: agentID,
			taskIDHeader:  "00000000-0000-0000-0000-000000000099",
			wantActorType: "member",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newRequest("GET", "/test", nil)
			if tt.agentIDHeader != "" {
				req.Header.Set("X-Agent-ID", tt.agentIDHeader)
			}
			if tt.taskIDHeader != "" {
				req.Header.Set("X-Task-ID", tt.taskIDHeader)
			}

			actorType, actorID := testHandler.resolveActor(req, testUserID, testWorkspaceID)

			if actorType != tt.wantActorType {
				t.Errorf("actorType = %q, want %q", actorType, tt.wantActorType)
			}
			if tt.wantIsAgent {
				if actorID != tt.agentIDHeader {
					t.Errorf("actorID = %q, want agent %q", actorID, tt.agentIDHeader)
				}
			} else {
				if actorID != testUserID {
					t.Errorf("actorID = %q, want user %q", actorID, testUserID)
				}
			}
		})
	}
}

func TestDaemonRegisterAcceptsAuthenticatedDaemonWithMatchingBody(t *testing.T) {
	ctx := context.Background()
	daemonID := fmt.Sprintf("handler-register-daemon-%d", time.Now().UnixNano())

	req := newDaemonAuthenticatedRequest(t, "POST", "/api/daemon/register", testWorkspaceID, daemonID, map[string]any{
		"workspace_id": testWorkspaceID,
		"daemon_id":    daemonID,
		"device_name":  "test-machine",
		"cli_version":  "1.2.3",
		"runtimes": []map[string]any{{
			"name":    "Handler Register Runtime",
			"type":    "codex",
			"version": "1.0.0",
			"status":  "online",
		}},
	})

	w := performDaemonRequest(testHandler.DaemonRegister, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DaemonRegister: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Runtimes []struct {
			ID string `json:"id"`
		} `json:"runtimes"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Runtimes) != 1 {
		t.Fatalf("DaemonRegister: expected 1 runtime, got %d", len(resp.Runtimes))
	}

	var ownerID pgtype.UUID
	var storedDaemonID pgtype.Text
	err := testPool.QueryRow(ctx, `
		SELECT owner_id, daemon_id
		FROM agent_runtime
		WHERE id = $1
	`, resp.Runtimes[0].ID).Scan(&ownerID, &storedDaemonID)
	if err != nil {
		t.Fatalf("query registered runtime: %v", err)
	}
	if !storedDaemonID.Valid || storedDaemonID.String != daemonID {
		t.Fatalf("DaemonRegister: expected daemon_id %q, got %+v", daemonID, storedDaemonID)
	}
	if ownerID.Valid {
		t.Fatalf("DaemonRegister: expected empty owner_id, got %s", uuidToString(ownerID))
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, orchestratorName)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, resp.Runtimes[0].ID)
	})
}

func TestDaemonRegisterRejectsBodyIdentityMismatch(t *testing.T) {
	req := newDaemonAuthenticatedRequest(t, "POST", "/api/daemon/register", testWorkspaceID, "handler-test-daemon", map[string]any{
		"workspace_id": testWorkspaceID,
		"daemon_id":    "different-daemon",
		"device_name":  "test-machine",
		"runtimes": []map[string]any{{
			"name":   "Handler Register Runtime Mismatch",
			"type":   "codex",
			"status": "online",
		}},
	})

	w := performDaemonRequest(testHandler.DaemonRegister, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("DaemonRegister: expected 403 for body/context mismatch, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDaemonDeregisterRejectsForeignRuntime(t *testing.T) {
	ctx := context.Background()

	var otherWorkspaceID, runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Handler Daemon Deregister Access Test", "handler-daemon-deregister-access-test", "Temporary workspace for daemon deregister auth test", "HDD").Scan(&otherWorkspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, otherWorkspaceID, "handler-deregister-foreign-daemon", "Handler Deregister Foreign Runtime", "handler_deregister_foreign_runtime", "Handler deregister foreign runtime").Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, otherWorkspaceID)
	})

	req := newDaemonAuthenticatedRequest(t, "POST", "/api/daemon/deregister", testWorkspaceID, "handler-test-daemon", map[string]any{
		"runtime_ids": []string{runtimeID},
	})

	w := performDaemonRequest(testHandler.DaemonDeregister, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("DaemonDeregister: expected 404 for foreign runtime, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReportPingResultRejectsMismatchedRuntimePath(t *testing.T) {
	ctx := context.Background()
	daemonID := fmt.Sprintf("handler-ping-daemon-%d", time.Now().UnixNano())

	var runtimeID, otherRuntimeID, pingID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, daemonID, "Handler Ping Result Runtime", "handler_ping_result_runtime", "Handler ping result runtime").Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, daemonID, "Handler Ping Result Other Runtime", "handler_ping_result_other_runtime", "Handler ping result other runtime").Scan(&otherRuntimeID); err != nil {
		t.Fatalf("create other runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO runtime_ping_request (workspace_id, runtime_id, daemon_id, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING id
	`, testWorkspaceID, runtimeID, daemonID).Scan(&pingID); err != nil {
		t.Fatalf("create ping request: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id IN ($1, $2)`, runtimeID, otherRuntimeID)
	})

	req := newDaemonAuthenticatedRequest(t, "POST", "/api/daemon/runtimes/"+otherRuntimeID+"/ping/"+pingID+"/result", testWorkspaceID, daemonID, map[string]any{
		"status":      "completed",
		"output":      "pong",
		"duration_ms": 12,
	})
	req = withURLParam(req, "runtimeId", otherRuntimeID)
	req = withURLParam(req, "pingId", pingID)

	w := performDaemonRequest(testHandler.ReportPingResult, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ReportPingResult: expected 404 for mismatched runtime path, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReportUpdateResultRejectsMismatchedRuntimePath(t *testing.T) {
	ctx := context.Background()
	daemonID := fmt.Sprintf("handler-update-daemon-%d", time.Now().UnixNano())

	var runtimeID, otherRuntimeID, updateID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, daemonID, "Handler Update Result Runtime", "handler_update_result_runtime", "Handler update result runtime").Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, daemonID, "Handler Update Result Other Runtime", "handler_update_result_other_runtime", "Handler update result other runtime").Scan(&otherRuntimeID); err != nil {
		t.Fatalf("create other runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO runtime_update_request (workspace_id, runtime_id, daemon_id, target_version, status)
		VALUES ($1, $2, $3, $4, 'pending')
		RETURNING id
	`, testWorkspaceID, runtimeID, daemonID, "9.9.9").Scan(&updateID); err != nil {
		t.Fatalf("create update request: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id IN ($1, $2)`, runtimeID, otherRuntimeID)
	})

	req := newDaemonAuthenticatedRequest(t, "POST", "/api/daemon/runtimes/"+otherRuntimeID+"/update/"+updateID+"/result", testWorkspaceID, daemonID, map[string]any{
		"status": "completed",
		"output": "updated",
	})
	req = withURLParam(req, "runtimeId", otherRuntimeID)
	req = withURLParam(req, "updateId", updateID)

	w := performDaemonRequest(testHandler.ReportUpdateResult, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ReportUpdateResult: expected 404 for mismatched runtime path, got %d: %s", w.Code, w.Body.String())
	}
}

func TestInitiatePingCreatesDurableRequest(t *testing.T) {
	ctx := context.Background()

	var runtimeID string
	err := testPool.QueryRow(ctx, `
		SELECT id FROM agent_runtime
		WHERE workspace_id = $1 AND name = $2
	`, testWorkspaceID, "Handler Test Runtime").Scan(&runtimeID)
	if err != nil {
		t.Fatalf("failed to find test runtime: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/runtimes/"+runtimeID+"/ping", nil)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.InitiatePing(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("InitiatePing: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ID        string `json:"id"`
		RuntimeID string `json:"runtime_id"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RuntimeID != runtimeID {
		t.Fatalf("InitiatePing: expected runtime_id %q, got %q", runtimeID, resp.RuntimeID)
	}
	if resp.Status != "pending" {
		t.Fatalf("InitiatePing: expected status pending, got %q", resp.Status)
	}

	var persisted struct {
		Status string
	}
	err = testPool.QueryRow(ctx, `
		SELECT status
		FROM runtime_ping_request
		WHERE id = $1
	`, resp.ID).Scan(&persisted.Status)
	if err != nil {
		t.Fatalf("query runtime_ping_request: %v", err)
	}
	if persisted.Status != "pending" {
		t.Fatalf("runtime_ping_request: expected status pending, got %q", persisted.Status)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM runtime_ping_request WHERE id = $1`, resp.ID)
	})
}

func TestInitiateUpdateCreatesDurableRequest(t *testing.T) {
	ctx := context.Background()

	var runtimeID string
	err := testPool.QueryRow(ctx, `
		SELECT id FROM agent_runtime
		WHERE workspace_id = $1 AND name = $2
	`, testWorkspaceID, "Handler Test Runtime").Scan(&runtimeID)
	if err != nil {
		t.Fatalf("failed to find test runtime: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/runtimes/"+runtimeID+"/update", map[string]any{
		"target_version": "1.2.3",
	})
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.InitiateUpdate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("InitiateUpdate: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ID            string `json:"id"`
		RuntimeID     string `json:"runtime_id"`
		TargetVersion string `json:"target_version"`
		Status        string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RuntimeID != runtimeID {
		t.Fatalf("InitiateUpdate: expected runtime_id %q, got %q", runtimeID, resp.RuntimeID)
	}
	if resp.TargetVersion != "1.2.3" {
		t.Fatalf("InitiateUpdate: expected target_version 1.2.3, got %q", resp.TargetVersion)
	}
	if resp.Status != "pending" {
		t.Fatalf("InitiateUpdate: expected status pending, got %q", resp.Status)
	}

	var persisted struct {
		TargetVersion string
		Status        string
	}
	err = testPool.QueryRow(ctx, `
		SELECT target_version, status
		FROM runtime_update_request
		WHERE id = $1
	`, resp.ID).Scan(&persisted.TargetVersion, &persisted.Status)
	if err != nil {
		t.Fatalf("query runtime_update_request: %v", err)
	}
	if persisted.TargetVersion != "1.2.3" {
		t.Fatalf("runtime_update_request: expected target_version 1.2.3, got %q", persisted.TargetVersion)
	}
	if persisted.Status != "pending" {
		t.Fatalf("runtime_update_request: expected status pending, got %q", persisted.Status)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM runtime_update_request WHERE id = $1`, resp.ID)
	})
}

func TestGetPingRejectsNonMember(t *testing.T) {
	ctx := context.Background()

	var otherWorkspaceID, runtimeID, pingID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Handler Ping Access Test", "handler-ping-access-test", "Temporary workspace for ping auth test", "HPA").Scan(&otherWorkspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, otherWorkspaceID, "handler-ping-access-daemon", "Handler Ping Access Runtime", "handler_ping_access_runtime", "Handler ping access runtime").Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO runtime_ping_request (workspace_id, runtime_id, daemon_id, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING id
	`, otherWorkspaceID, runtimeID, "handler-ping-access-daemon").Scan(&pingID); err != nil {
		t.Fatalf("create ping request: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, otherWorkspaceID)
	})

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/runtimes/"+runtimeID+"/ping/"+pingID, nil)
	req = withURLParam(req, "runtimeId", runtimeID)
	req = withURLParam(req, "pingId", pingID)
	testHandler.GetPing(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GetPing: expected 404 for non-member, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReportRuntimeUsageRejectsForeignRuntime(t *testing.T) {
	ctx := context.Background()

	var otherWorkspaceID, runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Handler Runtime Usage Access Test", "handler-runtime-usage-access-test", "Temporary workspace for runtime usage auth test", "HRU").Scan(&otherWorkspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, otherWorkspaceID, "handler-runtime-usage-foreign-daemon", "Handler Runtime Usage Foreign Runtime", "handler_runtime_usage_foreign_runtime", "Handler runtime usage foreign runtime").Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, otherWorkspaceID)
	})

	req := newDaemonAuthenticatedRequest(t, "POST", "/api/daemon/runtimes/"+runtimeID+"/usage", testWorkspaceID, "handler-test-daemon", map[string]any{
		"entries": []map[string]any{{
			"date":          "2026-04-17",
			"provider":      "anthropic",
			"model":         "claude-sonnet-4-5",
			"input_tokens":  10,
			"output_tokens": 20,
		}},
	})
	req = withURLParam(req, "runtimeId", runtimeID)

	w := performDaemonRequest(testHandler.ReportRuntimeUsage, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ReportRuntimeUsage: expected 404 for foreign runtime, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetPingRejectsWrongRuntimePath(t *testing.T) {
	ctx := context.Background()
	daemonID := fmt.Sprintf("handler-get-ping-daemon-%d", time.Now().UnixNano())

	var runtimeID, otherRuntimeID, pingID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, daemonID, "Handler Get Ping Runtime", "handler_get_ping_runtime", "Handler get ping runtime").Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, daemonID, "Handler Get Ping Other Runtime", "handler_get_ping_other_runtime", "Handler get ping other runtime").Scan(&otherRuntimeID); err != nil {
		t.Fatalf("create other runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO runtime_ping_request (workspace_id, runtime_id, daemon_id, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING id
	`, testWorkspaceID, runtimeID, daemonID).Scan(&pingID); err != nil {
		t.Fatalf("create ping request: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id IN ($1, $2)`, runtimeID, otherRuntimeID)
	})

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/runtimes/"+otherRuntimeID+"/ping/"+pingID, nil)
	req = withURLParam(req, "runtimeId", otherRuntimeID)
	req = withURLParam(req, "pingId", pingID)
	testHandler.GetPing(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GetPing: expected 404 for wrong runtime path, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetUpdateRejectsNonMember(t *testing.T) {
	ctx := context.Background()

	var otherWorkspaceID, runtimeID, updateID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Handler Update Access Test", "handler-update-access-test", "Temporary workspace for update auth test", "HUA").Scan(&otherWorkspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, otherWorkspaceID, "handler-update-access-daemon", "Handler Update Access Runtime", "handler_update_access_runtime", "Handler update access runtime").Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO runtime_update_request (workspace_id, runtime_id, daemon_id, target_version, status)
		VALUES ($1, $2, $3, $4, 'pending')
		RETURNING id
	`, otherWorkspaceID, runtimeID, "handler-update-access-daemon", "9.9.9").Scan(&updateID); err != nil {
		t.Fatalf("create update request: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, otherWorkspaceID)
	})

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/runtimes/"+runtimeID+"/update/"+updateID, nil)
	req = withURLParam(req, "runtimeId", runtimeID)
	req = withURLParam(req, "updateId", updateID)
	testHandler.GetUpdate(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GetUpdate: expected 404 for non-member, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetUpdateRejectsWrongRuntimePath(t *testing.T) {
	ctx := context.Background()
	daemonID := fmt.Sprintf("handler-get-update-daemon-%d", time.Now().UnixNano())

	var runtimeID, otherRuntimeID, updateID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, daemonID, "Handler Get Update Runtime", "handler_get_update_runtime", "Handler get update runtime").Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, daemonID, "Handler Get Update Other Runtime", "handler_get_update_other_runtime", "Handler get update other runtime").Scan(&otherRuntimeID); err != nil {
		t.Fatalf("create other runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO runtime_update_request (workspace_id, runtime_id, daemon_id, target_version, status)
		VALUES ($1, $2, $3, $4, 'pending')
		RETURNING id
	`, testWorkspaceID, runtimeID, daemonID, "9.9.9").Scan(&updateID); err != nil {
		t.Fatalf("create update request: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id IN ($1, $2)`, runtimeID, otherRuntimeID)
	})

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/runtimes/"+otherRuntimeID+"/update/"+updateID, nil)
	req = withURLParam(req, "runtimeId", otherRuntimeID)
	req = withURLParam(req, "updateId", updateID)
	testHandler.GetUpdate(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GetUpdate: expected 404 for wrong runtime path, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDaemonGetTaskStatusRejectsTaskOutsideDaemonOwnership(t *testing.T) {
	ctx := context.Background()

	var otherWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Handler Daemon Status Access Test", "handler-daemon-status-access-test", "Temporary workspace for daemon task status auth test", "HDS").Scan(&otherWorkspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, otherWorkspaceID)
	})

	taskID := createDaemonTaskFixture(t, otherWorkspaceID, "handler-status-foreign-daemon", "Handler Status Foreign Runtime", "handler status foreign issue", "running")

	req := newDaemonAuthenticatedRequest(t, "GET", "/api/daemon/tasks/"+taskID+"/status", testWorkspaceID, "handler-test-daemon", nil)
	req = withURLParam(req, "taskId", taskID)

	w := performDaemonRequest(testHandler.GetTaskStatus, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GetTaskStatus: expected 404 for foreign daemon task, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDaemonReportTaskProgressRejectsTaskOutsideDaemonOwnership(t *testing.T) {
	ctx := context.Background()

	var otherWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Handler Daemon Progress Access Test", "handler-daemon-progress-access-test", "Temporary workspace for daemon task progress auth test", "HDP").Scan(&otherWorkspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, otherWorkspaceID)
	})

	taskID := createDaemonTaskFixture(t, otherWorkspaceID, "handler-progress-foreign-daemon", "Handler Progress Foreign Runtime", "handler progress foreign issue", "running")

	req := newDaemonAuthenticatedRequest(t, "POST", "/api/daemon/tasks/"+taskID+"/progress", testWorkspaceID, "handler-test-daemon", map[string]any{
		"summary": "foreign progress update",
		"step":    1,
		"total":   2,
	})
	req = withURLParam(req, "taskId", taskID)

	w := performDaemonRequest(testHandler.ReportTaskProgress, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ReportTaskProgress: expected 404 for foreign daemon task, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDaemonHeartbeatRejectsForeignRuntime(t *testing.T) {
	ctx := context.Background()

	var otherWorkspaceID, runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Handler Daemon Heartbeat Access Test", "handler-daemon-heartbeat-access-test", "Temporary workspace for daemon heartbeat auth test", "HDH").Scan(&otherWorkspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, otherWorkspaceID, "handler-heartbeat-foreign-daemon", "Handler Heartbeat Foreign Runtime", "handler_heartbeat_foreign_runtime", "Handler heartbeat foreign runtime").Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, otherWorkspaceID)
	})

	req := newDaemonAuthenticatedRequest(t, "POST", "/api/daemon/heartbeat", testWorkspaceID, "handler-test-daemon", map[string]any{
		"runtime_id": runtimeID,
	})

	w := performDaemonRequest(testHandler.DaemonHeartbeat, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("DaemonHeartbeat: expected 404 for foreign runtime, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDaemonClaimTaskByRuntimeRejectsForeignRuntime(t *testing.T) {
	ctx := context.Background()

	var otherWorkspaceID, foreignRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Handler Daemon Claim Access Test", "handler-daemon-claim-access-test", "Temporary workspace for daemon claim auth test", "HDC").Scan(&otherWorkspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, otherWorkspaceID)
	})

	taskID := createDaemonTaskFixture(t, otherWorkspaceID, "handler-claim-foreign-daemon", "Handler Claim Foreign Runtime", "handler claim foreign issue", "queued")
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent_task_queue WHERE id = $1`, taskID).Scan(&foreignRuntimeID); err != nil {
		t.Fatalf("lookup task runtime: %v", err)
	}

	req := newDaemonAuthenticatedRequest(t, "POST", "/api/daemon/runtimes/"+foreignRuntimeID+"/claim", testWorkspaceID, "handler-test-daemon", nil)
	req = withURLParam(req, "runtimeId", foreignRuntimeID)

	w := performDaemonRequest(testHandler.ClaimTaskByRuntime, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ClaimTaskByRuntime: expected 404 for foreign runtime, got %d: %s", w.Code, w.Body.String())
	}
}
