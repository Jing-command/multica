package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/resend/resend-go/v2"
)

var testHandler *Handler
var testPool *pgxpool.Pool
var testUserID string
var testWorkspaceID string

func newRestartedTestHandler() *Handler {
	hub := realtime.NewHub()
	go hub.Run()
	return New(db.New(testPool), testPool, hub, events.New(), service.NewEmailService(), nil, nil)
}

func mustGetHandlerTestRuntimeID(t *testing.T) string {
	t.Helper()

	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT id
		FROM agent_runtime
		WHERE workspace_id = $1 AND name = $2
		LIMIT 1
	`, testWorkspaceID, "Handler Test Runtime").Scan(&runtimeID); err != nil {
		t.Fatalf("lookup handler test runtime id: %v", err)
	}
	return runtimeID
}

const (
	handlerTestEmail         = "handler-test@multica.ai"
	handlerTestName          = "Handler Test User"
	handlerTestWorkspaceSlug = "handler-tests"
	handlerTestDaemonID      = "handler-test-daemon"
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

	var runtimeID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'cloud', $4, 'online', $5, '{}'::jsonb, now())
		RETURNING id
	`, workspaceID, handlerTestDaemonID, "Handler Test Runtime", "handler_test_runtime", "Handler test runtime").Scan(&runtimeID); err != nil {
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

func ensureAuthAbuseEventTable(ctx context.Context, t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS auth_abuse_event (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			event_type TEXT NOT NULL,
			identifier TEXT NOT NULL,
			ip TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		t.Fatalf("ensure auth_abuse_event table: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_auth_abuse_event_type_created_at
			ON auth_abuse_event(event_type, created_at)
	`); err != nil {
		t.Fatalf("ensure auth_abuse_event type index: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_auth_abuse_event_identifier_type_created_at
			ON auth_abuse_event(identifier, event_type, created_at)
	`); err != nil {
		t.Fatalf("ensure auth_abuse_event identifier index: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_auth_abuse_event_ip_type_created_at
			ON auth_abuse_event(ip, event_type, created_at)
	`); err != nil {
		t.Fatalf("ensure auth_abuse_event ip index: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_auth_abuse_event_ip_identifier_type_created_at
			ON auth_abuse_event(ip, identifier, event_type, created_at)
	`); err != nil {
		t.Fatalf("ensure auth_abuse_event ip identifier index: %v", err)
	}
}

func cleanupAuthAbuseEvents(ctx context.Context, t *testing.T, email string) {
	t.Helper()
	ensureAuthAbuseEventTable(ctx, t)
	if _, err := testPool.Exec(ctx, `DELETE FROM auth_abuse_event WHERE identifier = $1`, email); err != nil {
		t.Fatalf("cleanup auth_abuse_event by identifier: %v", err)
	}
}

func cleanupAuthAbuseEventsByIP(ctx context.Context, t *testing.T, ip string) {
	t.Helper()
	ensureAuthAbuseEventTable(ctx, t)
	if _, err := testPool.Exec(ctx, `DELETE FROM auth_abuse_event WHERE ip = $1`, ip); err != nil {
		t.Fatalf("cleanup auth_abuse_event by ip: %v", err)
	}
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

func withDaemonIdentity(req *http.Request, workspaceID, daemonID string) *http.Request {
	ctx := middleware.DaemonContextWithIdentity(req.Context(), workspaceID, daemonID)
	return req.WithContext(ctx)
}

func withURLParam(req *http.Request, key, value string) *http.Request {
	rctx, _ := req.Context().Value(chi.RouteCtxKey).(*chi.Context)
	if rctx == nil {
		rctx = chi.NewRouteContext()
	}
	rctx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
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
	const email = "sendcode-test@multica.ai"
	const ip = "203.0.113.11"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
	})

	w := httptest.NewRecorder()
	body := map[string]string{"email": email}
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	testHandler.SendCode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SendCode: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["message"] == "" {
		t.Fatal("SendCode: expected non-empty message")
	}
}

func TestSendCodeCooldownReturnsGenericSuccess(t *testing.T) {
	const email = "cooldown-generic@multica.ai"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
	})

	body := map[string]string{"email": email}
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.10:1234"
	testHandler.SendCode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first SendCode: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	buf.Reset()
	json.NewEncoder(&buf).Encode(body)
	req = httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.10:1234"
	testHandler.SendCode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("second SendCode: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["message"] != "Verification code sent" {
		t.Fatalf("expected generic success message, got %#v", resp)
	}
}

func TestSendCodeBlocksIdentifierWindow(t *testing.T) {
	const email = "identifier-window@multica.ai"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
	})

	for i := 0; i < 5; i++ {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO auth_abuse_event (event_type, identifier, ip, created_at)
			VALUES ('send_code_requested', $1, '203.0.113.20', now() - interval '1 minute')
		`, email); err != nil {
			t.Fatalf("seed auth_abuse_event: %v", err)
		}
	}

	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.20:1234"
	testHandler.SendCode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SendCode identifier window: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM verification_code WHERE email = $1`, email).Scan(&count); err != nil {
		t.Fatalf("count verification_code: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no new verification code row, got %d", count)
	}
}

func TestSendCodeBlocksIPWindow(t *testing.T) {
	const email = "ip-window@multica.ai"
	const ip = "203.0.113.21"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
	})

	for i := 0; i < 20; i++ {
		seedEmail := fmt.Sprintf("ip-window-seed-%d@multica.ai", i)
		if _, err := testPool.Exec(ctx, `
			INSERT INTO auth_abuse_event (event_type, identifier, ip, created_at)
			VALUES ('send_code_requested', $1, $2, now() - interval '1 minute')
		`, seedEmail, ip); err != nil {
			t.Fatalf("seed auth_abuse_event: %v", err)
		}
	}

	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	testHandler.SendCode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SendCode IP window: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM verification_code WHERE email = $1`, email).Scan(&count); err != nil {
		t.Fatalf("count verification_code: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no new verification code row, got %d", count)
	}
}

func TestSendCodeBlocksIPIdentifierConcentration(t *testing.T) {
	const email = "ip-identifier-window@multica.ai"
	const ip = "203.0.113.22"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
	})

	for i := 0; i < 3; i++ {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO auth_abuse_event (event_type, identifier, ip, created_at)
			VALUES ('send_code_requested', $1, $2, now() - interval '1 minute')
		`, email, ip); err != nil {
			t.Fatalf("seed auth_abuse_event: %v", err)
		}
	}

	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	testHandler.SendCode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SendCode IP+identifier window: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM verification_code WHERE email = $1`, email).Scan(&count); err != nil {
		t.Fatalf("count verification_code: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no new verification code row, got %d", count)
	}
}

func TestVerifyCode(t *testing.T) {
	const email = "verify-test@multica.ai"
	const ip = "203.0.113.42"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
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
	req.RemoteAddr = ip + ":1234"
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
	req.RemoteAddr = ip + ":1234"
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
	const ip = "203.0.113.43"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
	})

	// Send code
	w := httptest.NewRecorder()
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email})
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	testHandler.SendCode(w, req)

	// Verify with wrong code
	w = httptest.NewRecorder()
	buf.Reset()
	json.NewEncoder(&buf).Encode(map[string]string{"email": email, "code": "000000"})
	req = httptest.NewRequest("POST", "/auth/verify-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	testHandler.VerifyCode(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("VerifyCode (wrong code): expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestVerifyCodeWithoutActiveCodeDoesNotRecordFailedEvent(t *testing.T) {
	const email = "verify-no-active-code@multica.ai"
	const ip = "203.0.113.32"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)
	cleanupAuthAbuseEvents(ctx, t, email)
	cleanupAuthAbuseEventsByIP(ctx, t, ip)
	if _, err := testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email); err != nil {
		t.Fatalf("cleanup verification_code: %v", err)
	}
	t.Cleanup(func() {
		if _, err := testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email); err != nil {
			t.Fatalf("cleanup verification_code: %v", err)
		}
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
	})

	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email, "code": "000000"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/auth/verify-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	testHandler.VerifyCode(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("VerifyCode without active code: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "invalid or expired code" {
		t.Fatalf("expected generic verify error, got %#v", resp)
	}

	var count int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM auth_abuse_event
		WHERE event_type = 'verify_code_failed' AND identifier = $1 AND ip = $2
	`, email, ip).Scan(&count); err != nil {
		t.Fatalf("count verify_code_failed events: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no verify_code_failed events without active code, got %d", count)
	}
}

func TestVerifyCodeBruteForceProtection(t *testing.T) {
	const email = "bruteforce-test@multica.ai"
	const ip = "203.0.113.44"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
	})

	// Send code
	w := httptest.NewRecorder()
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email})
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
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
		req.RemoteAddr = ip + ":1234"
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
	req.RemoteAddr = ip + ":1234"
	testHandler.VerifyCode(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("after lockout: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestVerifyCodeBlocksIdentifierFailedWindowAcrossCodes(t *testing.T) {
	const email = "verify-identifier-window@multica.ai"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
	})

	for i := 0; i < 10; i++ {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO auth_abuse_event (event_type, identifier, ip, created_at)
			VALUES ('verify_code_failed', $1, '203.0.113.30', now() - interval '1 minute')
		`, email); err != nil {
			t.Fatalf("seed verify_code_failed: %v", err)
		}
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO verification_code (email, code, expires_at)
		VALUES ($1, '123456', now() + interval '10 minutes')
	`, email); err != nil {
		t.Fatalf("seed verification_code: %v", err)
	}

	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email, "code": "123456"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/auth/verify-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.30:1234"
	testHandler.VerifyCode(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("VerifyCode identifier failed window: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "invalid or expired code" {
		t.Fatalf("expected generic verify error, got %#v", resp)
	}
}

func TestVerifyCodeBlocksIPFailedWindow(t *testing.T) {
	const email = "verify-ip-window@multica.ai"
	const ip = "203.0.113.31"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
	})

	for i := 0; i < 25; i++ {
		seedEmail := fmt.Sprintf("verify-ip-seed-%d@multica.ai", i)
		if _, err := testPool.Exec(ctx, `
			INSERT INTO auth_abuse_event (event_type, identifier, ip, created_at)
			VALUES ('verify_code_failed', $1, $2, now() - interval '1 minute')
		`, seedEmail, ip); err != nil {
			t.Fatalf("seed verify_code_failed: %v", err)
		}
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO verification_code (email, code, expires_at)
		VALUES ($1, '123456', now() + interval '10 minutes')
	`, email); err != nil {
		t.Fatalf("seed verification_code: %v", err)
	}

	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email, "code": "123456"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/auth/verify-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	testHandler.VerifyCode(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("VerifyCode IP failed window: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "invalid or expired code" {
		t.Fatalf("expected generic verify error, got %#v", resp)
	}
}

func TestSendCodeBlockedAttemptsKeepIdentifierWindowHot(t *testing.T) {
	const email = "blocked-identifier-window@multica.ai"
	const ip = "203.0.113.40"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
	})

	for i := 0; i < 5; i++ {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO auth_abuse_event (event_type, identifier, ip, created_at)
			VALUES ('send_code_requested', $1, $2, now() - interval '20 minutes')
		`, email, ip); err != nil {
			t.Fatalf("seed stale send_code_requested: %v", err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO auth_abuse_event (event_type, identifier, ip, created_at)
			VALUES ('send_code_blocked', $1, $2, now() - interval '1 minute')
		`, email, ip); err != nil {
			t.Fatalf("seed recent send_code_blocked: %v", err)
		}
	}

	decision := testHandler.evaluateSendCodeGuard(ctx, email, ip, time.Now())
	if decision.Allow {
		t.Fatalf("expected identifier window to stay blocked after sustained blocked attempts")
	}
	if decision.Reason != "identifier_window" {
		t.Fatalf("expected identifier_window reason, got %q", decision.Reason)
	}
}

func TestVerifyCodeBlockedAttemptsKeepIdentifierWindowHot(t *testing.T) {
	const email = "blocked-verify-window@multica.ai"
	const ip = "203.0.113.41"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
	})

	for i := 0; i < 10; i++ {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO auth_abuse_event (event_type, identifier, ip, created_at)
			VALUES ('verify_code_failed', $1, $2, now() - interval '20 minutes')
		`, email, ip); err != nil {
			t.Fatalf("seed stale verify_code_failed: %v", err)
		}
	}
	for i := 0; i < 10; i++ {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO auth_abuse_event (event_type, identifier, ip, created_at)
			VALUES ('verify_code_blocked', $1, $2, now() - interval '1 minute')
		`, email, ip); err != nil {
			t.Fatalf("seed recent verify_code_blocked: %v", err)
		}
	}

	decision := testHandler.evaluateVerifyCodeGuard(ctx, email, ip, time.Now())
	if decision.Allow {
		t.Fatalf("expected verify failed window to stay blocked after sustained blocked attempts")
	}
	if decision.Reason != "identifier_failed_window" {
		t.Fatalf("expected identifier_failed_window reason, got %q", decision.Reason)
	}
}

func TestSendCodeGuardAllowsRetryWhenRecentCodeExistsWithoutRequestedEvent(t *testing.T) {
	const email = "unsent-code-does-not-cooldown@multica.ai"
	const ip = "203.0.113.47"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
	})

	if _, err := testPool.Exec(ctx, `
		INSERT INTO verification_code (email, code, expires_at, created_at)
		VALUES ($1, '123456', now() + interval '10 minutes', now())
	`, email); err != nil {
		t.Fatalf("seed verification_code: %v", err)
	}

	decision := testHandler.evaluateSendCodeGuard(ctx, email, ip, time.Now())
	if !decision.Allow {
		t.Fatalf("expected retry to remain allowed when only an unsent code row exists, got blocked with reason %q", decision.Reason)
	}
}

func TestSendCodeEmailFailureDoesNotPoisonCooldownOrAbuseState(t *testing.T) {
	const email = "sendcode-email-failure@multica.ai"
	const ip = "203.0.113.46"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
	})

	failingHandler := *testHandler
	failingHandler.EmailService = service.NewEmailService()
	setPrivateField(failingHandler.EmailService, "client", resend.NewCustomClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("smtp unavailable")
	})}, "test-key"))
	setPrivateField(failingHandler.EmailService, "fromEmail", "noreply@multica.ai")

	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	failingHandler.SendCode(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("SendCode email failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}

	decision := testHandler.evaluateSendCodeGuard(ctx, email, ip, time.Now())
	if !decision.Allow {
		t.Fatalf("expected retry to remain allowed after email failure, got blocked with reason %q", decision.Reason)
	}

	var codeCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM verification_code WHERE email = $1`, email).Scan(&codeCount); err != nil {
		t.Fatalf("count verification_code: %v", err)
	}
	if codeCount != 0 {
		t.Fatalf("expected no persisted verification code after email failure, got %d", codeCount)
	}

	var eventCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM auth_abuse_event
		WHERE identifier = $1 AND ip = $2 AND event_type = 'send_code_requested'
	`, email, ip).Scan(&eventCount); err != nil {
		t.Fatalf("count auth_abuse_event: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("expected no send_code_requested event after email failure, got %d", eventCount)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func setPrivateField(target any, fieldName string, value any) {
	v := reflect.ValueOf(target).Elem().FieldByName(fieldName)
	reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

func TestVerifyCodeRejectsMagicCodeWithoutExplicitOptIn(t *testing.T) {
	const email = "magic-code-disabled@multica.ai"
	const ip = "203.0.113.48"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)

	prevAppEnv, hadAppEnv := os.LookupEnv("APP_ENV")
	prevMagicOptIn, hadMagicOptIn := os.LookupEnv("AUTH_ENABLE_MAGIC_CODE")
	if err := os.Setenv("APP_ENV", "development"); err != nil {
		t.Fatalf("set APP_ENV: %v", err)
	}
	if err := os.Unsetenv("AUTH_ENABLE_MAGIC_CODE"); err != nil {
		t.Fatalf("unset AUTH_ENABLE_MAGIC_CODE: %v", err)
	}
	defer func() {
		if hadAppEnv {
			_ = os.Setenv("APP_ENV", prevAppEnv)
		} else {
			_ = os.Unsetenv("APP_ENV")
		}
		if hadMagicOptIn {
			_ = os.Setenv("AUTH_ENABLE_MAGIC_CODE", prevMagicOptIn)
		} else {
			_ = os.Unsetenv("AUTH_ENABLE_MAGIC_CODE")
		}
	}()

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
	})

	if _, err := testPool.Exec(ctx, `
		INSERT INTO verification_code (email, code, expires_at)
		VALUES ($1, '123456', now() + interval '10 minutes')
	`, email); err != nil {
		t.Fatalf("seed verification_code: %v", err)
	}

	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": email, "code": "888888"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/auth/verify-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	testHandler.VerifyCode(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("VerifyCode magic code without opt-in: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "invalid or expired code" {
		t.Fatalf("expected generic verify error, got %#v", resp)
	}
}

func TestVerifyCodeCreatesWorkspace(t *testing.T) {
	const email = "workspace-verify-test@multica.ai"
	const ip = "203.0.113.45"
	ctx := context.Background()
	ensureAuthAbuseEventTable(ctx, t)

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		cleanupAuthAbuseEvents(ctx, t, email)
		cleanupAuthAbuseEventsByIP(ctx, t, ip)
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
	req.RemoteAddr = ip + ":1234"
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
	req.RemoteAddr = ip + ":1234"
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

func TestLogoutClearsAuthCookie(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/auth/logout", nil)
	req.Header.Set("X-User-ID", testUserID)

	testHandler.Logout(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("Logout: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	cookies := w.Result().Cookies()
	for _, cookie := range cookies {
		if cookie.Name == "multica_auth" {
			if cookie.Value != "" {
				t.Fatalf("expected auth cookie to be cleared, got %q", cookie.Value)
			}
			if cookie.MaxAge >= 0 {
				t.Fatalf("expected cleared auth cookie MaxAge < 0, got %d", cookie.MaxAge)
			}
			return
		}
	}

	t.Fatal("expected logout to clear multica_auth cookie")
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

func TestDaemonRegisterMissingWorkspaceReturns404(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/daemon/register", bytes.NewBufferString(`{
		"workspace_id":"00000000-0000-0000-0000-000000000001",
		"daemon_id":"local-daemon",
		"device_name":"test-machine",
		"runtimes":[{"name":"Local Codex","type":"codex","version":"1.0.0","status":"online"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req = withDaemonIdentity(req, "00000000-0000-0000-0000-000000000001", "local-daemon")

	testHandler.DaemonRegister(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("DaemonRegister: expected 404, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "workspace not found") {
		t.Fatalf("DaemonRegister: expected workspace not found error, got %s", w.Body.String())
	}
}

func TestPingPersistsAcrossHandlerRestart(t *testing.T) {
	runtimeID := mustGetHandlerTestRuntimeID(t)

	createW := httptest.NewRecorder()
	createReq := withURLParam(newRequest("POST", "/api/runtimes/"+runtimeID+"/ping", nil), "runtimeId", runtimeID)
	testHandler.InitiatePing(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiatePing: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var created PingRequest
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode created ping: %v", err)
	}
	defer func() {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/ping/"+created.ID+"/result", map[string]any{
			"status":      "completed",
			"output":      "cleanup",
			"duration_ms": 1,
		}), "pingId", created.ID)
		req = withURLParam(req, "runtimeId", runtimeID)
		req = withDaemonIdentity(req, testWorkspaceID, handlerTestDaemonID)
		testHandler.ReportPingResult(w, req)
	}()

	restarted := newRestartedTestHandler()

	heartbeatW := httptest.NewRecorder()
	heartbeatReq := newRequest("POST", "/api/daemon/heartbeat", map[string]any{"runtime_id": runtimeID})
	heartbeatReq = withDaemonIdentity(heartbeatReq, testWorkspaceID, handlerTestDaemonID)
	restarted.DaemonHeartbeat(heartbeatW, heartbeatReq)
	if heartbeatW.Code != http.StatusOK {
		t.Fatalf("DaemonHeartbeat: expected 200, got %d: %s", heartbeatW.Code, heartbeatW.Body.String())
	}

	var heartbeatResp map[string]any
	if err := json.NewDecoder(heartbeatW.Body).Decode(&heartbeatResp); err != nil {
		t.Fatalf("decode heartbeat response: %v", err)
	}
	pendingPing, ok := heartbeatResp["pending_ping"].(map[string]any)
	if !ok {
		t.Fatalf("expected pending_ping after restart, got %s", heartbeatW.Body.String())
	}
	if pendingPing["id"] != created.ID {
		t.Fatalf("pending_ping.id = %v, want %s", pendingPing["id"], created.ID)
	}

	reportW := httptest.NewRecorder()
	reportReq := withURLParam(newRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/ping/"+created.ID+"/result", map[string]any{
		"status":      "completed",
		"output":      "pong",
		"duration_ms": 12,
	}), "pingId", created.ID)
	reportReq = withURLParam(reportReq, "runtimeId", runtimeID)
	reportReq = withDaemonIdentity(reportReq, testWorkspaceID, handlerTestDaemonID)
	restarted.ReportPingResult(reportW, reportReq)
	if reportW.Code != http.StatusOK {
		t.Fatalf("ReportPingResult: expected 200, got %d: %s", reportW.Code, reportW.Body.String())
	}

	restartedAgain := newRestartedTestHandler()
	getW := httptest.NewRecorder()
	getReq := withURLParam(newRequest("GET", "/api/pings/"+created.ID, nil), "pingId", created.ID)
	restartedAgain.GetPing(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GetPing after restart: expected 200, got %d: %s", getW.Code, getW.Body.String())
	}

	var got PingRequest
	if err := json.NewDecoder(getW.Body).Decode(&got); err != nil {
		t.Fatalf("decode persisted ping: %v", err)
	}
	if got.Status != PingCompleted {
		t.Fatalf("ping status = %q, want %q", got.Status, PingCompleted)
	}
	if got.Output != "pong" {
		t.Fatalf("ping output = %q, want pong", got.Output)
	}
	if got.DurationMs != 12 {
		t.Fatalf("ping duration_ms = %d, want 12", got.DurationMs)
	}
}

func TestUpdatePersistsAcrossHandlerRestart(t *testing.T) {
	runtimeID := mustGetHandlerTestRuntimeID(t)

	createW := httptest.NewRecorder()
	createReq := withURLParam(newRequest("POST", "/api/runtimes/"+runtimeID+"/update", map[string]any{
		"target_version": "v9.9.9",
	}), "runtimeId", runtimeID)
	testHandler.InitiateUpdate(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiateUpdate: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var created UpdateRequest
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode created update: %v", err)
	}
	defer func() {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/update/"+created.ID+"/result", map[string]any{
			"status": "completed",
			"output": "cleanup",
		}), "updateId", created.ID)
		req = withURLParam(req, "runtimeId", runtimeID)
		req = withDaemonIdentity(req, testWorkspaceID, handlerTestDaemonID)
		testHandler.ReportUpdateResult(w, req)
	}()

	restarted := newRestartedTestHandler()

	heartbeatW := httptest.NewRecorder()
	heartbeatReq := newRequest("POST", "/api/daemon/heartbeat", map[string]any{"runtime_id": runtimeID})
	heartbeatReq = withDaemonIdentity(heartbeatReq, testWorkspaceID, handlerTestDaemonID)
	restarted.DaemonHeartbeat(heartbeatW, heartbeatReq)
	if heartbeatW.Code != http.StatusOK {
		t.Fatalf("DaemonHeartbeat: expected 200, got %d: %s", heartbeatW.Code, heartbeatW.Body.String())
	}

	var heartbeatResp map[string]any
	if err := json.NewDecoder(heartbeatW.Body).Decode(&heartbeatResp); err != nil {
		t.Fatalf("decode heartbeat response: %v", err)
	}
	pendingUpdate, ok := heartbeatResp["pending_update"].(map[string]any)
	if !ok {
		t.Fatalf("expected pending_update after restart, got %s", heartbeatW.Body.String())
	}
	if pendingUpdate["id"] != created.ID {
		t.Fatalf("pending_update.id = %v, want %s", pendingUpdate["id"], created.ID)
	}
	if pendingUpdate["target_version"] != "v9.9.9" {
		t.Fatalf("pending_update.target_version = %v, want v9.9.9", pendingUpdate["target_version"])
	}

	reportW := httptest.NewRecorder()
	reportReq := withURLParam(newRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/update/"+created.ID+"/result", map[string]any{
		"status": "completed",
		"output": "updated",
	}), "updateId", created.ID)
	reportReq = withURLParam(reportReq, "runtimeId", runtimeID)
	reportReq = withDaemonIdentity(reportReq, testWorkspaceID, handlerTestDaemonID)
	restarted.ReportUpdateResult(reportW, reportReq)
	if reportW.Code != http.StatusOK {
		t.Fatalf("ReportUpdateResult: expected 200, got %d: %s", reportW.Code, reportW.Body.String())
	}

	restartedAgain := newRestartedTestHandler()
	getW := httptest.NewRecorder()
	getReq := withURLParam(newRequest("GET", "/api/updates/"+created.ID, nil), "updateId", created.ID)
	restartedAgain.GetUpdate(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GetUpdate after restart: expected 200, got %d: %s", getW.Code, getW.Body.String())
	}

	var got UpdateRequest
	if err := json.NewDecoder(getW.Body).Decode(&got); err != nil {
		t.Fatalf("decode persisted update: %v", err)
	}
	if got.Status != UpdateCompleted {
		t.Fatalf("update status = %q, want %q", got.Status, UpdateCompleted)
	}
	if got.Output != "updated" {
		t.Fatalf("update output = %q, want updated", got.Output)
	}
	if got.TargetVersion != "v9.9.9" {
		t.Fatalf("update target_version = %q, want v9.9.9", got.TargetVersion)
	}
}

func TestGetPingReturnsPersistedTerminalStateWhenTimeoutUpdateLosesRace(t *testing.T) {
	ctx := context.Background()
	runtimeID := mustGetHandlerTestRuntimeID(t)

	createW := httptest.NewRecorder()
	createReq := withURLParam(newRequest("POST", "/api/runtimes/"+runtimeID+"/ping", nil), "runtimeId", runtimeID)
	testHandler.InitiatePing(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiatePing: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var created PingRequest
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode created ping: %v", err)
	}

	if _, err := testPool.Exec(ctx, `
		UPDATE runtime_ping
		SET created_at = $2, updated_at = $2
		WHERE id = $1
	`, created.ID, time.Now().Add(-2*time.Minute)); err != nil {
		t.Fatalf("age ping request: %v", err)
	}

	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT id FROM runtime_ping WHERE id = $1 FOR UPDATE`, created.ID); err != nil {
		t.Fatalf("lock ping row: %v", err)
	}

	resultCh := make(chan *PingRequest, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := testHandler.getPingRequest(ctx, created.ID)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	time.Sleep(50 * time.Millisecond)

	if _, err := tx.Exec(ctx, `
		UPDATE runtime_ping
		SET status = 'completed', output = 'pong', duration_ms = 7, updated_at = now()
		WHERE id = $1
	`, created.ID); err != nil {
		t.Fatalf("complete ping in tx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("getPingRequest returned error: %v", err)
	case got := <-resultCh:
		if got.Status != PingCompleted {
			t.Fatalf("ping status = %q, want %q", got.Status, PingCompleted)
		}
		if got.Output != "pong" {
			t.Fatalf("ping output = %q, want pong", got.Output)
		}
		if got.DurationMs != 7 {
			t.Fatalf("ping duration_ms = %d, want 7", got.DurationMs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for getPingRequest")
	}
}

func TestGetUpdateReturnsPersistedTerminalStateWhenTimeoutUpdateLosesRace(t *testing.T) {
	ctx := context.Background()
	runtimeID := mustGetHandlerTestRuntimeID(t)

	createW := httptest.NewRecorder()
	createReq := withURLParam(newRequest("POST", "/api/runtimes/"+runtimeID+"/update", map[string]any{
		"target_version": "v1.2.3",
	}), "runtimeId", runtimeID)
	testHandler.InitiateUpdate(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiateUpdate: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var created UpdateRequest
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode created update: %v", err)
	}

	if _, err := testPool.Exec(ctx, `
		UPDATE runtime_update
		SET created_at = $2, updated_at = $2
		WHERE id = $1
	`, created.ID, time.Now().Add(-3*time.Minute)); err != nil {
		t.Fatalf("age update request: %v", err)
	}

	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT id FROM runtime_update WHERE id = $1 FOR UPDATE`, created.ID); err != nil {
		t.Fatalf("lock update row: %v", err)
	}

	resultCh := make(chan *UpdateRequest, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := testHandler.getUpdateRequest(ctx, created.ID)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	time.Sleep(50 * time.Millisecond)

	if _, err := tx.Exec(ctx, `
		UPDATE runtime_update
		SET status = 'completed', output = 'updated', updated_at = now()
		WHERE id = $1
	`, created.ID); err != nil {
		t.Fatalf("complete update in tx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("getUpdateRequest returned error: %v", err)
	case got := <-resultCh:
		if got.Status != UpdateCompleted {
			t.Fatalf("update status = %q, want %q", got.Status, UpdateCompleted)
		}
		if got.Output != "updated" {
			t.Fatalf("update output = %q, want updated", got.Output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for getUpdateRequest")
	}
}

func TestGetPingTimesOutAfterHeartbeatClaimsIt(t *testing.T) {
	ctx := context.Background()
	runtimeID := mustGetHandlerTestRuntimeID(t)

	createW := httptest.NewRecorder()
	createReq := withURLParam(newRequest("POST", "/api/runtimes/"+runtimeID+"/ping", nil), "runtimeId", runtimeID)
	testHandler.InitiatePing(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiatePing: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var created PingRequest
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode created ping: %v", err)
	}

	heartbeatW := httptest.NewRecorder()
	heartbeatReq := newRequest("POST", "/api/daemon/heartbeat", map[string]any{"runtime_id": runtimeID})
	heartbeatReq = withDaemonIdentity(heartbeatReq, testWorkspaceID, handlerTestDaemonID)
	testHandler.DaemonHeartbeat(heartbeatW, heartbeatReq)
	if heartbeatW.Code != http.StatusOK {
		t.Fatalf("DaemonHeartbeat: expected 200, got %d: %s", heartbeatW.Code, heartbeatW.Body.String())
	}

	if _, err := testPool.Exec(ctx, `
		UPDATE runtime_ping
		SET created_at = $2, updated_at = $2
		WHERE id = $1
	`, created.ID, time.Now().Add(-2*time.Minute)); err != nil {
		t.Fatalf("age claimed ping request: %v", err)
	}

	got, err := testHandler.getPingRequest(ctx, created.ID)
	if err != nil {
		t.Fatalf("getPingRequest returned error: %v", err)
	}
	if got.Status != PingTimeout {
		t.Fatalf("ping status = %q, want %q", got.Status, PingTimeout)
	}
	if got.Error != "daemon did not respond within 60 seconds" {
		t.Fatalf("ping error = %q, want timeout error", got.Error)
	}
}
