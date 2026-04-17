package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
)

var (
	testServer      *httptest.Server
	testPool        *pgxpool.Pool
	testToken       string
	testUserID      string
	testWorkspaceID string
)

// jwtSecret is resolved at runtime via auth.JWTSecret() so it respects
// the JWT_SECRET env var (set in .env) and stays in sync with the server.

const (
	integrationTestEmail         = "integration-test@multica.ai"
	integrationTestName          = "Integration Tester"
	integrationTestWorkspaceSlug = "integration-tests"
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Printf("Skipping integration tests: could not connect to database: %v\n", err)
		os.Exit(0)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Printf("Skipping integration tests: database not reachable: %v\n", err)
		pool.Close()
		os.Exit(0)
	}

	testPool = pool
	testUserID, testWorkspaceID, err = setupIntegrationTestFixture(ctx, pool)
	if err != nil {
		fmt.Printf("Failed to set up integration test fixture: %v\n", err)
		pool.Close()
		os.Exit(1)
	}

	hub := realtime.NewHub()
	go hub.Run()

	bus := events.New()
	registerListeners(bus, hub)
	router := NewRouter(pool, hub, bus)
	testServer = httptest.NewServer(router)

	// Generate a JWT token directly for the test user
	testToken, err = generateTestJWT(testUserID, integrationTestEmail, integrationTestName)
	if err != nil {
		fmt.Printf("Failed to generate test JWT: %v\n", err)
		testServer.Close()
		pool.Close()
		os.Exit(1)
	}

	code := m.Run()

	if err := cleanupIntegrationTestFixture(context.Background(), pool); err != nil {
		fmt.Printf("Failed to clean up integration test fixture: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	testServer.Close()
	pool.Close()
	os.Exit(code)
}

func setupIntegrationTestFixture(ctx context.Context, pool *pgxpool.Pool) (string, string, error) {
	if err := cleanupIntegrationTestFixture(ctx, pool); err != nil {
		return "", "", err
	}

	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, $2)
		RETURNING id
	`, integrationTestName, integrationTestEmail).Scan(&userID); err != nil {
		return "", "", err
	}

	var workspaceID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description)
		VALUES ($1, $2, $3)
		RETURNING id
	`, "Integration Tests", integrationTestWorkspaceSlug, "Temporary workspace for router integration tests").Scan(&workspaceID); err != nil {
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
		VALUES ($1, NULL, $2, 'cloud', $3, 'online', $4, '{}'::jsonb, now())
		RETURNING id
	`, workspaceID, "Integration Test Runtime", "integration_test_runtime", "Integration test runtime").Scan(&runtimeID); err != nil {
		return "", "", err
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4)
	`, workspaceID, "Integration Test Agent", runtimeID, userID); err != nil {
		return "", "", err
	}

	return userID, workspaceID, nil
}

func cleanupIntegrationTestFixture(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `DELETE FROM auth_abuse_event WHERE identifier IN ($1, $2)`, integrationTestEmail, "integration-sendcode@multica.ai"); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM auth_abuse_event WHERE identifier = $1`, "new-integration-verify@multica.ai"); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM verification_code WHERE email IN ($1, $2, $3)`, integrationTestEmail, "integration-sendcode@multica.ai", "new-integration-verify@multica.ai"); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, integrationTestWorkspaceSlug); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM "user" WHERE email IN ($1, $2, $3)`, integrationTestEmail, "integration-sendcode@multica.ai", "new-integration-verify@multica.ai"); err != nil {
		return err
	}
	return nil
}

// Helper to make authenticated requests
func authRequest(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, testServer.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func readJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
}

func generateTestJWT(userID, email, name string) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   userID,
		"email": email,
		"name":  name,
		"exp":   time.Now().Add(72 * time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	})
	return token.SignedString(auth.JWTSecret())
}

func createDaemonTokenForTest(t *testing.T, workspaceID, daemonID string) string {
	t.Helper()

	rawToken, err := auth.GenerateDaemonToken()
	if err != nil {
		t.Fatalf("generate daemon token: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO daemon_token (token_hash, workspace_id, daemon_id, expires_at)
		VALUES ($1, $2, $3, now() + interval '1 day')
	`, auth.HashToken(rawToken), workspaceID, daemonID); err != nil {
		t.Fatalf("insert daemon token: %v", err)
	}
	return rawToken
}

func createWorkspaceRuntimeForTest(t *testing.T, slug, runtimeName, daemonID string) string {
	t.Helper()

	ctx := context.Background()
	var workspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description)
		VALUES ($1, $2, $3)
		RETURNING id
	`, runtimeName+" Workspace", slug, "Temporary workspace for daemon auth integration tests").Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, testUserID); err != nil {
		t.Fatalf("create workspace membership: %v", err)
	}

	runtimeID := createRuntimeForWorkspaceAndDaemon(t, workspaceID, runtimeName, daemonID)

	t.Cleanup(func() {
		if _, err := testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, workspaceID); err != nil {
			t.Fatalf("cleanup workspace: %v", err)
		}
	})

	return runtimeID
}

func createRuntimeForWorkspaceAndDaemon(t *testing.T, workspaceID, runtimeName, daemonID string) string {
	t.Helper()

	ctx := context.Background()
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', 'claude', 'online', $4, '{}'::jsonb, now())
		RETURNING id
	`, workspaceID, daemonID, runtimeName, runtimeName+" device").Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	return runtimeID
}

func createTaskForRuntimeTest(t *testing.T, workspaceID, runtimeID, suffix string) string {
	t.Helper()

	ctx := context.Background()
	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'local', '{}'::jsonb, $3, 'workspace', 1, $4)
		RETURNING id
	`, workspaceID, "Daemon Task Agent "+suffix, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, position)
		VALUES ($1, $2, 'todo', 'none', 'member', $3, 8000 + floor(random() * 1000)::int, 0)
		RETURNING id
	`, workspaceID, "Daemon Task Issue "+suffix, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create task: %v", err)
	}

	return taskID
}

// ---- Health ----

func TestHealth(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/health")
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "ok" {
		t.Fatalf("expected status ok, got %s", result["status"])
	}
}

// ---- Auth ----

func TestSendCodeAndVerify(t *testing.T) {
	const email = "integration-sendcode@multica.ai"
	const ip = "127.0.0.1"
	ctx := context.Background()

	testPool.Exec(ctx, `DELETE FROM auth_abuse_event WHERE identifier = $1 OR ip = $2`, email, ip)

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM auth_abuse_event WHERE identifier = $1 OR ip = $2`, email, ip)
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		var userID string
		err := testPool.QueryRow(ctx, `SELECT id FROM "user" WHERE email = $1`, email).Scan(&userID)
		if err == nil {
			rows, queryErr := testPool.Query(ctx, `
				SELECT w.id FROM workspace w JOIN member m ON m.workspace_id = w.id WHERE m.user_id = $1
			`, userID)
			if queryErr == nil {
				defer rows.Close()
				for rows.Next() {
					var wsID string
					if rows.Scan(&wsID) == nil {
						testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, wsID)
					}
				}
			}
		}
		testPool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, email)
	})

	// Step 1: Send code
	body, _ := json.Marshal(map[string]string{"email": email})
	resp, err := http.Post(testServer.URL+"/auth/send-code", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("send-code failed: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("send-code: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Read code from DB
	var code string
	err = testPool.QueryRow(ctx, `SELECT code FROM verification_code WHERE email = $1 ORDER BY created_at DESC LIMIT 1`, email).Scan(&code)
	if err != nil {
		t.Fatalf("failed to read code from DB: %v", err)
	}

	// Step 2: Verify code
	body, _ = json.Marshal(map[string]string{"email": email, "code": code})
	resp, err = http.Post(testServer.URL+"/auth/verify-code", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("verify-code failed: %v", err)
	}
	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("verify-code: expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	var loginResp struct {
		Token string `json:"token"`
		User  struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	readJSON(t, resp, &loginResp)

	if loginResp.Token == "" {
		t.Fatal("expected non-empty token")
	}
	if loginResp.User.Email != email {
		t.Fatalf("expected email '%s', got '%s'", email, loginResp.User.Email)
	}

	// Verify the token works with /api/me
	req, _ := http.NewRequest("GET", testServer.URL+"/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+loginResp.Token)
	meResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("getMe failed: %v", err)
	}
	if meResp.StatusCode != 200 {
		t.Fatalf("getMe: expected 200, got %d", meResp.StatusCode)
	}
	meResp.Body.Close()
}

func TestVerifyCodeCreatesWorkspaceForNewUser(t *testing.T) {
	const email = "new-integration-verify@multica.ai"
	const ip = "127.0.0.1"
	ctx := context.Background()

	testPool.Exec(ctx, `DELETE FROM auth_abuse_event WHERE identifier = $1 OR ip = $2`, email, ip)

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM auth_abuse_event WHERE identifier = $1 OR ip = $2`, email, ip)
		testPool.Exec(ctx, `DELETE FROM verification_code WHERE email = $1`, email)
		var userID string
		err := testPool.QueryRow(ctx, `SELECT id FROM "user" WHERE email = $1`, email).Scan(&userID)
		if err == nil {
			rows, queryErr := testPool.Query(ctx, `
				SELECT w.id FROM workspace w JOIN member m ON m.workspace_id = w.id WHERE m.user_id = $1
			`, userID)
			if queryErr == nil {
				defer rows.Close()
				for rows.Next() {
					var wsID string
					if rows.Scan(&wsID) == nil {
						testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, wsID)
					}
				}
			}
		}
		testPool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, email)
	})

	testPool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, email)

	// Send code
	body, _ := json.Marshal(map[string]string{"email": email})
	resp, err := http.Post(testServer.URL+"/auth/send-code", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("send-code failed: %v", err)
	}
	resp.Body.Close()

	// Read code from DB
	var code string
	err = testPool.QueryRow(ctx, `SELECT code FROM verification_code WHERE email = $1 ORDER BY created_at DESC LIMIT 1`, email).Scan(&code)
	if err != nil {
		t.Fatalf("failed to read code from DB: %v", err)
	}

	// Verify code
	body, _ = json.Marshal(map[string]string{"email": email, "code": code})
	resp, err = http.Post(testServer.URL+"/auth/verify-code", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("verify-code failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify-code: expected 200, got %d", resp.StatusCode)
	}

	var loginResp struct {
		Token string `json:"token"`
	}
	readJSON(t, resp, &loginResp)

	// Check workspace was created
	req, _ := http.NewRequest("GET", testServer.URL+"/api/workspaces", nil)
	req.Header.Set("Authorization", "Bearer "+loginResp.Token)
	workspacesResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("listWorkspaces failed: %v", err)
	}
	defer workspacesResp.Body.Close()

	if workspacesResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", workspacesResp.StatusCode)
	}

	var workspaces []struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	readJSON(t, workspacesResp, &workspaces)

	if len(workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(workspaces))
	}
	if !strings.Contains(workspaces[0].Name, "Workspace") {
		t.Fatalf("expected workspace name containing 'Workspace', got %q", workspaces[0].Name)
	}
}

func TestRouterSourceRegistersLogoutRouteOnlyOnce(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router source: %v", err)
	}

	logoutPostCount := strings.Count(string(source), `r.Post("/auth/logout", h.Logout)`)
	if logoutPostCount != 1 {
		t.Fatalf("expected exactly 1 logout route registration, got %d", logoutPostCount)
	}
}

func TestProtectedRoutesRequireAuth(t *testing.T) {
	paths := []string{"/api/me", "/api/issues", "/api/agents", "/api/inbox", "/api/workspaces"}

	for _, path := range paths {
		resp, err := http.Get(testServer.URL + path)
		if err != nil {
			t.Fatalf("request to %s failed: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("%s: expected 401, got %d", path, resp.StatusCode)
		}
	}
}

func TestInvalidJWT(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"garbage token", "not-a-jwt"},
		{"empty token", ""},
		{"wrong secret", func() string {
			claims := jwt.MapClaims{"sub": "test", "exp": time.Now().Add(time.Hour).Unix()}
			t, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("wrong"))
			return t
		}()},
		{"expired token", func() string {
			claims := jwt.MapClaims{"sub": "test", "exp": time.Now().Add(-time.Hour).Unix()}
			t, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(auth.JWTSecret())
			return t
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", testServer.URL+"/api/me", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Fatalf("expected 401, got %d", resp.StatusCode)
			}
		})
	}
}

func TestDaemonRoutesRejectUserJWT(t *testing.T) {
	req, err := http.NewRequest("POST", testServer.URL+"/api/daemon/heartbeat", bytes.NewReader([]byte(`{"runtime_id":"00000000-0000-0000-0000-000000000001"}`)))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401, got %d: %s", resp.StatusCode, body)
	}
}

func TestDaemonHeartbeatRejectsCrossWorkspaceRuntime(t *testing.T) {
	const daemonID = "daemon-heartbeat-cross-workspace"
	runtimeID := createWorkspaceRuntimeForTest(t, "daemon-heartbeat-cross-workspace", "Cross Workspace Runtime", daemonID)
	daemonToken := createDaemonTokenForTest(t, testWorkspaceID, daemonID)

	body, _ := json.Marshal(map[string]string{"runtime_id": runtimeID})
	req, err := http.NewRequest("POST", testServer.URL+"/api/daemon/heartbeat", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+daemonToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403, got %d: %s", resp.StatusCode, respBody)
	}
}

func TestDaemonClaimRejectsCrossWorkspaceRuntime(t *testing.T) {
	const daemonID = "daemon-claim-cross-workspace"
	runtimeID := createWorkspaceRuntimeForTest(t, "daemon-claim-cross-workspace", "Cross Workspace Claim Runtime", daemonID)
	daemonToken := createDaemonTokenForTest(t, testWorkspaceID, daemonID)

	req, err := http.NewRequest("POST", testServer.URL+"/api/daemon/runtimes/"+runtimeID+"/tasks/claim", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+daemonToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403, got %d: %s", resp.StatusCode, respBody)
	}
}

func TestDaemonTaskStatusRejectsCrossRuntimeTask(t *testing.T) {
	const daemonID = "daemon-task-cross-runtime"
	otherRuntimeID := createRuntimeForWorkspaceAndDaemon(t, testWorkspaceID, "Mismatched Runtime", "other-daemon")
	taskID := createTaskForRuntimeTest(t, testWorkspaceID, otherRuntimeID, "cross-runtime")
	daemonToken := createDaemonTokenForTest(t, testWorkspaceID, daemonID)

	req, err := http.NewRequest("GET", testServer.URL+"/api/daemon/tasks/"+taskID+"/status", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+daemonToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403, got %d: %s", resp.StatusCode, respBody)
	}
}

// ---- Issues CRUD through full router ----

func TestIssuesCRUDThroughRouter(t *testing.T) {
	// Create
	resp := authRequest(t, "POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":    "Integration test issue",
		"status":   "todo",
		"priority": "high",
	})
	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("CreateIssue: expected 201, got %d: %s", resp.StatusCode, body)
	}

	var created map[string]any
	readJSON(t, resp, &created)
	issueID := created["id"].(string)
	if created["title"] != "Integration test issue" {
		t.Fatalf("expected title 'Integration test issue', got '%s'", created["title"])
	}

	// Get
	resp = authRequest(t, "GET", "/api/issues/"+issueID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GetIssue: expected 200, got %d", resp.StatusCode)
	}
	var fetched map[string]any
	readJSON(t, resp, &fetched)
	if fetched["id"] != issueID {
		t.Fatalf("expected id %s, got %s", issueID, fetched["id"])
	}

	// Update status only — should preserve title
	resp = authRequest(t, "PUT", "/api/issues/"+issueID, map[string]any{
		"status": "in_progress",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("UpdateIssue: expected 200, got %d", resp.StatusCode)
	}
	var updated map[string]any
	readJSON(t, resp, &updated)
	if updated["status"] != "in_progress" {
		t.Fatalf("expected status 'in_progress', got '%s'", updated["status"])
	}
	if updated["title"] != "Integration test issue" {
		t.Fatalf("title should be preserved, got '%s'", updated["title"])
	}

	// Update title only — should preserve status
	resp = authRequest(t, "PUT", "/api/issues/"+issueID, map[string]any{
		"title": "Renamed integration issue",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("UpdateIssue title: expected 200, got %d", resp.StatusCode)
	}
	var updated2 map[string]any
	readJSON(t, resp, &updated2)
	if updated2["title"] != "Renamed integration issue" {
		t.Fatalf("expected title 'Renamed integration issue', got '%s'", updated2["title"])
	}
	if updated2["status"] != "in_progress" {
		t.Fatalf("status should be preserved, got '%s'", updated2["status"])
	}

	// List
	resp = authRequest(t, "GET", "/api/issues?workspace_id="+testWorkspaceID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("ListIssues: expected 200, got %d", resp.StatusCode)
	}
	var listResp map[string]any
	readJSON(t, resp, &listResp)
	total := listResp["total"].(float64)
	if total < 1 {
		t.Fatal("expected at least 1 issue")
	}

	// Delete
	resp = authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("DeleteIssue: expected 204, got %d", resp.StatusCode)
	}

	// Verify deleted
	resp = authRequest(t, "GET", "/api/issues/"+issueID, nil)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("GetIssue after delete: expected 404, got %d", resp.StatusCode)
	}
}

// ---- Comments through full router ----

func TestCommentsThroughRouter(t *testing.T) {
	// Create issue
	resp := authRequest(t, "POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "Comment integration test",
	})
	var issue map[string]any
	readJSON(t, resp, &issue)
	issueID := issue["id"].(string)

	// Create comment
	resp = authRequest(t, "POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "Integration test comment",
		"type":    "comment",
	})
	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("CreateComment: expected 201, got %d: %s", resp.StatusCode, body)
	}
	var comment map[string]any
	readJSON(t, resp, &comment)
	if comment["content"] != "Integration test comment" {
		t.Fatalf("expected content 'Integration test comment', got '%s'", comment["content"])
	}

	// Create second comment
	resp = authRequest(t, "POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "Second comment",
		"type":    "comment",
	})
	resp.Body.Close()

	// List comments
	resp = authRequest(t, "GET", "/api/issues/"+issueID+"/comments", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("ListComments: expected 200, got %d", resp.StatusCode)
	}
	var comments []map[string]any
	readJSON(t, resp, &comments)
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}

	// Cleanup
	resp = authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
	resp.Body.Close()
}

// ---- Agents through full router ----

func TestAgentsThroughRouter(t *testing.T) {
	// List
	resp := authRequest(t, "GET", "/api/agents?workspace_id="+testWorkspaceID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("ListAgents: expected 200, got %d", resp.StatusCode)
	}
	var agents []map[string]any
	readJSON(t, resp, &agents)
	if len(agents) < 1 {
		t.Fatal("expected at least 1 agent")
	}

	// Get
	agentID := agents[0]["id"].(string)
	resp = authRequest(t, "GET", "/api/agents/"+agentID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GetAgent: expected 200, got %d", resp.StatusCode)
	}
	var agent map[string]any
	readJSON(t, resp, &agent)
	if agent["id"] != agentID {
		t.Fatalf("expected agent id %s, got %s", agentID, agent["id"])
	}

	// Update status
	resp = authRequest(t, "PUT", "/api/agents/"+agentID, map[string]any{
		"status": "idle",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("UpdateAgent: expected 200, got %d", resp.StatusCode)
	}
	var updated map[string]any
	readJSON(t, resp, &updated)
	if updated["status"] != "idle" {
		t.Fatalf("expected status 'idle', got '%s'", updated["status"])
	}
	// Name should be preserved
	if updated["name"] != agents[0]["name"] {
		t.Fatalf("name should be preserved, got '%s'", updated["name"])
	}
}

// ---- Workspaces through full router ----

func TestWorkspacesThroughRouter(t *testing.T) {
	// List
	resp := authRequest(t, "GET", "/api/workspaces", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("ListWorkspaces: expected 200, got %d", resp.StatusCode)
	}
	var workspaces []map[string]any
	readJSON(t, resp, &workspaces)
	if len(workspaces) < 1 {
		t.Fatal("expected at least 1 workspace")
	}

	// Get
	wsID := workspaces[0]["id"].(string)
	resp = authRequest(t, "GET", "/api/workspaces/"+wsID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GetWorkspace: expected 200, got %d", resp.StatusCode)
	}
	var ws map[string]any
	readJSON(t, resp, &ws)
	if ws["id"] != wsID {
		t.Fatalf("expected workspace id %s, got %s", wsID, ws["id"])
	}

	// Update
	resp = authRequest(t, "PUT", "/api/workspaces/"+wsID, map[string]any{
		"description": "Integration test update",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("UpdateWorkspace: expected 200, got %d", resp.StatusCode)
	}
	var updated map[string]any
	readJSON(t, resp, &updated)
	if updated["description"] != "Integration test update" {
		t.Fatalf("expected description 'Integration test update', got '%v'", updated["description"])
	}
	// Name should be preserved
	if updated["name"] != ws["name"] {
		t.Fatalf("name should be preserved")
	}

	// Members
	resp = authRequest(t, "GET", "/api/workspaces/"+wsID+"/members", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("ListMembers: expected 200, got %d", resp.StatusCode)
	}
	var members []map[string]any
	readJSON(t, resp, &members)
	if len(members) < 1 {
		t.Fatal("expected at least 1 member")
	}
	// Verify member has user info
	if members[0]["email"] == nil || members[0]["email"] == "" {
		t.Fatal("member should have email field")
	}
	if members[0]["role"] == nil || members[0]["role"] == "" {
		t.Fatal("member should have role field")
	}
}

// ---- Inbox through full router ----

func TestInboxThroughRouter(t *testing.T) {
	resp := authRequest(t, "GET", "/api/inbox", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("ListInbox: expected 200, got %d", resp.StatusCode)
	}
	var items []map[string]any
	readJSON(t, resp, &items)
	// Inbox may be empty, just verify it returns valid JSON array
	if items == nil {
		t.Fatal("expected non-nil inbox items array")
	}
}

// ---- 404 for non-existent resources ----

func TestNonExistentResources(t *testing.T) {
	fakeUUID := "00000000-0000-0000-0000-000000000000"

	cases := []struct {
		name string
		path string
	}{
		{"issue", "/api/issues/" + fakeUUID},
		{"agent", "/api/agents/" + fakeUUID},
		{"workspace", "/api/workspaces/" + fakeUUID},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := authRequest(t, "GET", tc.path, nil)
			resp.Body.Close()
			if resp.StatusCode != 404 {
				t.Fatalf("expected 404, got %d", resp.StatusCode)
			}
		})
	}
}

// ---- Invalid request bodies ----

func TestInvalidRequestBodies(t *testing.T) {
	resp := authRequest(t, "POST", "/api/issues?workspace_id="+testWorkspaceID, nil)
	defer resp.Body.Close()
	// Sending nil body should fail with 400
	if resp.StatusCode != 400 {
		// Some handlers may return 500 for nil body, that's acceptable too
		if resp.StatusCode != 500 {
			t.Fatalf("expected 400 or 500, got %d", resp.StatusCode)
		}
	}
}

// ---- WebSocket integration through full router ----

func TestWebSocketIntegration(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://localhost:3000")

	// Connect WebSocket client
	wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http") + "/ws?workspace_id=" + testWorkspaceID
	headers := http.Header{}
	headers.Set("Cookie", "multica_auth="+testToken)
	headers.Set("Origin", "http://localhost:3000")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("WebSocket connection failed: %v", err)
	}
	defer conn.Close()

	// Allow Hub goroutine to process the register and add client to room
	time.Sleep(100 * time.Millisecond)

	// Create an issue — this should trigger a WebSocket broadcast
	resp := authRequest(t, "POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":  "WebSocket test issue",
		"status": "todo",
	})
	var issue map[string]any
	readJSON(t, resp, &issue)
	issueID := issue["id"].(string)

	// Read the WebSocket message
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("WebSocket read error: %v", err)
	}

	// Verify the message contains the issue event
	var wsMsg map[string]any
	if err := json.Unmarshal(msg, &wsMsg); err != nil {
		t.Fatalf("failed to parse WebSocket message: %v", err)
	}
	if wsMsg["type"] != "issue:created" {
		t.Fatalf("expected type 'issue:created', got '%s'", wsMsg["type"])
	}

	// Update the issue — should trigger another broadcast
	resp = authRequest(t, "PUT", "/api/issues/"+issueID, map[string]any{
		"status": "in_progress",
	})
	resp.Body.Close()

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("WebSocket read error on update: %v", err)
	}
	var updateMsg map[string]any
	json.Unmarshal(msg, &updateMsg)
	if updateMsg["type"] != "issue:updated" {
		t.Fatalf("expected type 'issue:updated', got '%s'", updateMsg["type"])
	}

	// Delete the issue — should trigger another broadcast
	resp = authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
	resp.Body.Close()

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("WebSocket read error on delete: %v", err)
	}
	var deleteMsg map[string]any
	json.Unmarshal(msg, &deleteMsg)
	if deleteMsg["type"] != "issue:deleted" {
		t.Fatalf("expected type 'issue:deleted', got '%s'", deleteMsg["type"])
	}
}
