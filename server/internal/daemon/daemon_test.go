package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/usage"
)

func TestNormalizeServerBaseURL(t *testing.T) {
	t.Parallel()

	got, err := NormalizeServerBaseURL("ws://localhost:8080/ws")
	if err != nil {
		t.Fatalf("NormalizeServerBaseURL returned error: %v", err)
	}
	if got != "http://localhost:8080" {
		t.Fatalf("expected http://localhost:8080, got %s", got)
	}
}

func TestBuildPromptContainsIssueID(t *testing.T) {
	t.Parallel()

	issueID := "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	prompt := BuildPrompt(Task{
		IssueID: issueID,
		Agent: &AgentData{
			Name: "Local Codex",
			Skills: []SkillData{
				{Name: "Concise", Content: "Be concise."},
			},
		},
	})

	// Prompt should contain the issue ID and CLI hint.
	for _, want := range []string{
		issueID,
		"multica issue get",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}

	// Skills should NOT be inlined in the prompt (they're in runtime config).
	for _, absent := range []string{"## Agent Skills", "Be concise."} {
		if strings.Contains(prompt, absent) {
			t.Fatalf("prompt should NOT contain %q (skills are in runtime config)", absent)
		}
	}
}

func TestBuildPromptNoIssueDetails(t *testing.T) {
	t.Parallel()

	prompt := BuildPrompt(Task{
		IssueID: "test-id",
		Agent:   &AgentData{Name: "Test"},
	})

	// Prompt should not contain issue title/description (agent fetches via CLI).
	for _, absent := range []string{"**Issue:**", "**Summary:**"} {
		if strings.Contains(prompt, absent) {
			t.Fatalf("prompt should NOT contain %q — agent fetches details via CLI", absent)
		}
	}
}

func TestIsWorkspaceNotFoundError(t *testing.T) {
	t.Parallel()

	err := &requestError{
		Method:     http.MethodPost,
		Path:       "/api/daemon/register",
		StatusCode: http.StatusNotFound,
		Body:       `{"error":"workspace not found"}`,
	}
	if !isWorkspaceNotFoundError(err) {
		t.Fatal("expected workspace not found error to be recognized")
	}

	if isWorkspaceNotFoundError(&requestError{StatusCode: http.StatusInternalServerError, Body: `{"error":"workspace not found"}`}) {
		t.Fatal("did not expect 500 to be treated as workspace not found")
	}
}

func TestResolveAuthLoadsUserTokenAndWorkspaceAuth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {
				DaemonID:  "daemon-1",
				Token:     "mdt_token_ws1",
				ExpiresAt: "2026-05-01T00:00:00Z",
			},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	d := newTestDaemon("http://localhost:8080")

	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}
	if d.userToken != "mul_user_token" {
		t.Fatalf("userToken = %q, want %q", d.userToken, "mul_user_token")
	}
	if d.client.UserToken() != "mul_user_token" {
		t.Fatalf("client user token = %q, want %q", d.client.UserToken(), "mul_user_token")
	}
	if got := d.workspaceAuth["ws-1"]; got.Token != "mdt_token_ws1" || got.DaemonID != "daemon-1" || got.ExpiresAt != "2026-05-01T00:00:00Z" {
		t.Fatalf("workspace auth = %+v", got)
	}
}

func TestResolveAuthRequiresUserToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newTestDaemon("http://localhost:8080")

	err := d.resolveAuth()
	if err == nil {
		t.Fatal("expected resolveAuth() to fail without user token")
	}
	if !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("resolveAuth() error = %v, want not authenticated", err)
	}
}

func TestEnsureWorkspaceDaemonAuthEnrollsMissingToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	var enrollCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/daemon/workspaces/ws-1/enroll":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			if auth := r.Header.Get("Authorization"); auth != "Bearer mul_user_token" {
				t.Fatalf("authorization = %q, want Bearer mul_user_token", auth)
			}
			enrollCalls.Add(1)
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode enroll body: %v", err)
			}
			if body["daemon_id"] != "daemon-1" {
				t.Fatalf("daemon_id = %q, want daemon-1", body["daemon_id"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"mdt_new_token","expires_at":"2026-05-17T00:00:00Z"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	d.cfg.DaemonID = "daemon-1"
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}

	authCfg, err := d.ensureWorkspaceDaemonAuth(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("ensureWorkspaceDaemonAuth() error = %v", err)
	}
	if enrollCalls.Load() != 1 {
		t.Fatalf("enroll calls = %d, want 1", enrollCalls.Load())
	}
	if authCfg.Token != "mdt_new_token" {
		t.Fatalf("token = %q, want %q", authCfg.Token, "mdt_new_token")
	}

	cfg, err := cli.LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig() error = %v", err)
	}
	if got := cfg.DaemonAuth["ws-1"]; got.Token != "mdt_new_token" || got.DaemonID != "daemon-1" || got.ExpiresAt != "2026-05-17T00:00:00Z" {
		t.Fatalf("saved daemon auth = %+v", got)
	}
}

func TestEnsureWorkspaceDaemonAuthRotatesExpiringToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {
				DaemonID:  "daemon-1",
				Token:     "mdt_old_token",
				ExpiresAt: time.Now().Add(6 * 24 * time.Hour).UTC().Format(time.RFC3339),
			},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	var enrollCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon/workspaces/ws-1/enroll" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		enrollCalls.Add(1)
		if auth := r.Header.Get("Authorization"); auth != "Bearer mul_user_token" {
			t.Fatalf("enroll auth = %q, want user token", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"mdt_rotated_token","expires_at":"2026-05-20T00:00:00Z"}`))
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}

	authCfg, err := d.ensureWorkspaceDaemonAuth(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("ensureWorkspaceDaemonAuth() error = %v", err)
	}
	if enrollCalls.Load() != 1 {
		t.Fatalf("enroll calls = %d, want 1", enrollCalls.Load())
	}
	if authCfg.Token != "mdt_rotated_token" {
		t.Fatalf("token = %q, want rotated token", authCfg.Token)
	}
}

func TestEnsureWorkspaceDaemonAuthReenrollsOnDaemonIDMismatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {
				DaemonID:  "old-daemon",
				Token:     "mdt_old_token",
				ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339),
			},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	var enrollCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon/workspaces/ws-1/enroll" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		enrollCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"mdt_new_token","expires_at":"2026-05-20T00:00:00Z"}`))
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	d.cfg.DaemonID = "daemon-1"
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}

	authCfg, err := d.ensureWorkspaceDaemonAuth(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("ensureWorkspaceDaemonAuth() error = %v", err)
	}
	if enrollCalls.Load() != 1 {
		t.Fatalf("enroll calls = %d, want 1", enrollCalls.Load())
	}
	if authCfg.DaemonID != "daemon-1" || authCfg.Token != "mdt_new_token" {
		t.Fatalf("authCfg = %+v", authCfg)
	}
}

func TestReenrollWorkspaceDaemonAuthCleansUpWorkspaceNotFoundImmediately(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WorkspaceID: "ws-1",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {DaemonID: "old-daemon", Token: "mdt_old_token", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon/workspaces/ws-1/enroll" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"workspace not found"}`))
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	d.cfg.DaemonID = "daemon-1"
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1", Provider: "claude", Status: "online"}
	d.runtimeWorkspace["rt-1"] = "ws-1"
	d.workspaceAuth["ws-1"] = cli.DaemonAuthConfig{DaemonID: "old-daemon", Token: "mdt_old_token", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)}

	_, err := d.reenrollWorkspaceDaemonAuth(context.Background(), "ws-1")
	if err == nil {
		t.Fatal("expected reenrollWorkspaceDaemonAuth() to fail")
	}

	cfg, loadErr := cli.LoadCLIConfig()
	if loadErr != nil {
		t.Fatalf("LoadCLIConfig() error = %v", loadErr)
	}
	if len(cfg.WatchedWorkspaces) != 0 {
		t.Fatalf("WatchedWorkspaces = %+v, want empty", cfg.WatchedWorkspaces)
	}
	if cfg.WorkspaceID != "" {
		t.Fatalf("WorkspaceID = %q, want empty", cfg.WorkspaceID)
	}
	if _, ok := cfg.DaemonAuth["ws-1"]; ok {
		t.Fatalf("ws-1 daemon auth should be removed from config: %+v", cfg.DaemonAuth)
	}
	if _, ok := d.workspaces["ws-1"]; ok {
		t.Fatalf("workspace ws-1 should be removed from memory: %+v", d.workspaces)
	}
	if _, ok := d.runtimeIndex["rt-1"]; ok {
		t.Fatalf("runtime rt-1 should be removed from memory: %+v", d.runtimeIndex)
	}
	if _, ok := d.runtimeWorkspace["rt-1"]; ok {
		t.Fatalf("runtime workspace rt-1 should be removed from memory: %+v", d.runtimeWorkspace)
	}
	if _, ok := d.workspaceAuth["ws-1"]; ok {
		t.Fatalf("workspace auth ws-1 should be removed from memory: %+v", d.workspaceAuth)
	}
}

func TestDaemonRoutesUseWorkspaceScopedTokens(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {DaemonID: "daemon-1", Token: "mdt_ws1", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)},
			"ws-2": {DaemonID: "daemon-1", Token: "mdt_ws2", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/daemon/register":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode register body: %v", err)
			}
			workspaceID, _ := body["workspace_id"].(string)
			wantAuth := map[string]string{"ws-1": "Bearer mdt_ws1", "ws-2": "Bearer mdt_ws2"}[workspaceID]
			if auth := r.Header.Get("Authorization"); auth != wantAuth {
				t.Fatalf("register auth for %s = %q, want %q", workspaceID, auth, wantAuth)
			}
			w.Header().Set("Content-Type", "application/json")
			if workspaceID == "ws-1" {
				_, _ = w.Write([]byte(`{"runtimes":[{"id":"rt-1","name":"Claude","provider":"claude","status":"online"}],"repos":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"runtimes":[{"id":"rt-2","name":"Claude","provider":"claude","status":"online"}],"repos":[]}`))
		case "/api/daemon/heartbeat":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode heartbeat body: %v", err)
			}
			wantAuth := map[string]string{"rt-1": "Bearer mdt_ws1", "rt-2": "Bearer mdt_ws2"}[body["runtime_id"]]
			if auth := r.Header.Get("Authorization"); auth != wantAuth {
				t.Fatalf("heartbeat auth for %s = %q, want %q", body["runtime_id"], auth, wantAuth)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	d.cfg.Agents = map[string]AgentEntry{"claude": {Path: "/usr/bin/true"}}
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}
	resp1, err := d.registerRuntimesForWorkspace(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("register ws-1: %v", err)
	}
	resp2, err := d.registerRuntimesForWorkspace(context.Background(), "ws-2")
	if err != nil {
		t.Fatalf("register ws-2: %v", err)
	}
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.workspaces["ws-2"] = &workspaceState{workspaceID: "ws-2", runtimeIDs: []string{"rt-2"}}
	for _, rt := range resp1.Runtimes {
		d.runtimeIndex[rt.ID] = rt
		d.runtimeWorkspace[rt.ID] = "ws-1"
	}
	for _, rt := range resp2.Runtimes {
		d.runtimeIndex[rt.ID] = rt
		d.runtimeWorkspace[rt.ID] = "ws-2"
	}
	if _, err := d.sendHeartbeatForRuntime(context.Background(), "rt-1"); err != nil {
		t.Fatalf("heartbeat rt-1: %v", err)
	}
	if _, err := d.sendHeartbeatForRuntime(context.Background(), "rt-2"); err != nil {
		t.Fatalf("heartbeat rt-2: %v", err)
	}
}

func TestDaemonRoute401ReenrollsOnceAndRetries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {
				DaemonID:  "daemon-1",
				Token:     "mdt_old_token",
				ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339),
			},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	var heartbeatCalls atomic.Int32
	var enrollCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/daemon/heartbeat":
			call := heartbeatCalls.Add(1)
			if call == 1 {
				if auth := r.Header.Get("Authorization"); auth != "Bearer mdt_old_token" {
					t.Fatalf("first heartbeat auth = %q, want old token", auth)
				}
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid daemon token"}`))
				return
			}
			if auth := r.Header.Get("Authorization"); auth != "Bearer mdt_rotated_token" {
				t.Fatalf("retry heartbeat auth = %q, want rotated token", auth)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/daemon/workspaces/ws-1/enroll":
			enrollCalls.Add(1)
			if auth := r.Header.Get("Authorization"); auth != "Bearer mul_user_token" {
				t.Fatalf("enroll auth = %q, want user token", auth)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"mdt_rotated_token","expires_at":"2026-05-20T00:00:00Z"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1", Provider: "claude", Status: "online"}
	d.runtimeWorkspace["rt-1"] = "ws-1"

	resp, err := d.sendHeartbeatForRuntime(context.Background(), "rt-1")
	if err != nil {
		t.Fatalf("sendHeartbeatForRuntime() error = %v", err)
	}
	if heartbeatCalls.Load() != 2 {
		t.Fatalf("heartbeat calls = %d, want 2", heartbeatCalls.Load())
	}
	if enrollCalls.Load() != 1 {
		t.Fatalf("enroll calls = %d, want 1", enrollCalls.Load())
	}
	if resp.Status != "ok" {
		t.Fatalf("heartbeat status = %q, want ok", resp.Status)
	}
}

func TestDaemonRoute401RetriesOnlyOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {
				DaemonID:  "daemon-1",
				Token:     "mdt_old_token",
				ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339),
			},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	var heartbeatCalls atomic.Int32
	var enrollCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/daemon/heartbeat":
			heartbeatCalls.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid daemon token"}`))
		case "/api/daemon/workspaces/ws-1/enroll":
			enrollCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"mdt_rotated_token","expires_at":"2026-05-20T00:00:00Z"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1", Provider: "claude", Status: "online"}
	d.runtimeWorkspace["rt-1"] = "ws-1"

	_, err := d.sendHeartbeatForRuntime(context.Background(), "rt-1")
	if err == nil {
		t.Fatal("expected sendHeartbeatForRuntime() to fail")
	}
	if heartbeatCalls.Load() != 2 {
		t.Fatalf("heartbeat calls = %d, want 2", heartbeatCalls.Load())
	}
	if enrollCalls.Load() != 1 {
		t.Fatalf("enroll calls = %d, want 1", enrollCalls.Load())
	}
}

func TestDaemonRoute401DoesNotRetryAfterWorkspaceRemovedDuringReenroll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {
				DaemonID:  "daemon-1",
				Token:     "mdt_old_token",
				ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339),
			},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	beforeMemoryUpdate := make(chan struct{})
	allowFinish := make(chan struct{})
	persistWorkspaceDaemonAuthBeforeMemoryUpdateHook = func() {
		close(beforeMemoryUpdate)
		<-allowFinish
	}
	defer func() { persistWorkspaceDaemonAuthBeforeMemoryUpdateHook = nil }()

	var heartbeatCalls atomic.Int32
	var enrollCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/daemon/heartbeat":
			call := heartbeatCalls.Add(1)
			if call == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid daemon token"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/daemon/workspaces/ws-1/enroll":
			enrollCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"mdt_rotated_token","expires_at":"2026-05-20T00:00:00Z"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1", Provider: "claude", Status: "online"}
	d.runtimeWorkspace["rt-1"] = "ws-1"
	d.workspaceAuth["ws-1"] = cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_old_token", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)}

	errCh := make(chan error, 1)
	go func() {
		_, err := d.sendHeartbeatForRuntime(context.Background(), "rt-1")
		errCh <- err
	}()

	<-beforeMemoryUpdate
	if err := cli.UpdateCLIConfigForProfile("", func(cfg *cli.CLIConfig) error {
		cfg.RemoveWatchedWorkspace("ws-1")
		return nil
	}); err != nil {
		t.Fatalf("UpdateCLIConfigForProfile() error = %v", err)
	}
	close(allowFinish)

	err := <-errCh
	if err == nil {
		t.Fatal("expected sendHeartbeatForRuntime() to fail")
	}
	if heartbeatCalls.Load() != 1 {
		t.Fatalf("heartbeat calls = %d, want 1", heartbeatCalls.Load())
	}
	if enrollCalls.Load() != 1 {
		t.Fatalf("enroll calls = %d, want 1", enrollCalls.Load())
	}

	cfg, loadErr := cli.LoadCLIConfig()
	if loadErr != nil {
		t.Fatalf("LoadCLIConfig() error = %v", loadErr)
	}
	if _, ok := cfg.DaemonAuth["ws-1"]; ok {
		t.Fatalf("ws-1 daemon auth should stay removed from config: %+v", cfg.DaemonAuth)
	}
	if _, ok := d.workspaceAuth["ws-1"]; ok {
		t.Fatalf("ws-1 workspace auth should stay removed from memory: %+v", d.workspaceAuth)
	}
}

func TestConcurrentRefreshForSameWorkspaceSharesLatestAuth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {
				DaemonID:  "daemon-1",
				Token:     "mdt_old_token",
				ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339),
			},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	var heartbeatCalls atomic.Int32
	var enrollCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/daemon/heartbeat":
			call := heartbeatCalls.Add(1)
			if call <= 2 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid daemon token"}`))
				return
			}
			if auth := r.Header.Get("Authorization"); auth != "Bearer mdt_rotated_token" {
				t.Fatalf("retry heartbeat auth = %q, want rotated token", auth)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/daemon/workspaces/ws-1/enroll":
			enrollCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"mdt_rotated_token","expires_at":"2026-05-20T00:00:00Z"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1", "rt-2"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1", Provider: "claude", Status: "online"}
	d.runtimeIndex["rt-2"] = Runtime{ID: "rt-2", Provider: "claude", Status: "online"}
	d.runtimeWorkspace["rt-1"] = "ws-1"
	d.runtimeWorkspace["rt-2"] = "ws-1"
	d.workspaceAuth["ws-1"] = cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_old_token", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)}

	type result struct {
		resp *HeartbeatResponse
		err  error
	}
	resCh := make(chan result, 2)
	go func() {
		resp, err := d.sendHeartbeatForRuntime(context.Background(), "rt-1")
		resCh <- result{resp: resp, err: err}
	}()
	go func() {
		resp, err := d.sendHeartbeatForRuntime(context.Background(), "rt-2")
		resCh <- result{resp: resp, err: err}
	}()

	for range 2 {
		res := <-resCh
		if res.err != nil {
			t.Fatalf("sendHeartbeatForRuntime() error = %v", res.err)
		}
		if res.resp == nil || res.resp.Status != "ok" {
			t.Fatalf("heartbeat response = %+v", res.resp)
		}
	}
	if enrollCalls.Load() != 1 {
		t.Fatalf("enroll calls = %d, want 1", enrollCalls.Load())
	}
	if heartbeatCalls.Load() != 4 {
		t.Fatalf("heartbeat calls = %d, want 4", heartbeatCalls.Load())
	}
	if got := d.workspaceAuth["ws-1"]; got.Token != "mdt_rotated_token" || got.DaemonID != "daemon-1" {
		t.Fatalf("workspace auth = %+v", got)
	}
}

func TestRegisterRuntimesForWorkspace401ReenrollsOnceAndRetries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {
				DaemonID:  "daemon-1",
				Token:     "mdt_old_token",
				ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339),
			},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	var registerCalls atomic.Int32
	var enrollCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/daemon/register":
			call := registerCalls.Add(1)
			if call == 1 {
				if auth := r.Header.Get("Authorization"); auth != "Bearer mdt_old_token" {
					t.Fatalf("first register auth = %q, want old token", auth)
				}
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid daemon token"}`))
				return
			}
			if auth := r.Header.Get("Authorization"); auth != "Bearer mdt_rotated_token" {
				t.Fatalf("retry register auth = %q, want rotated token", auth)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"runtimes":[{"id":"rt-1","name":"Claude","provider":"claude","status":"online"}],"repos":[]}`))
		case "/api/daemon/workspaces/ws-1/enroll":
			enrollCalls.Add(1)
			if auth := r.Header.Get("Authorization"); auth != "Bearer mul_user_token" {
				t.Fatalf("enroll auth = %q, want user token", auth)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"mdt_rotated_token","expires_at":"2026-05-20T00:00:00Z"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	d.cfg.Agents = map[string]AgentEntry{"claude": {Path: "/usr/bin/true"}}
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}

	resp, err := d.registerRuntimesForWorkspace(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("registerRuntimesForWorkspace() error = %v", err)
	}
	if registerCalls.Load() != 2 {
		t.Fatalf("register calls = %d, want 2", registerCalls.Load())
	}
	if enrollCalls.Load() != 1 {
		t.Fatalf("enroll calls = %d, want 1", enrollCalls.Load())
	}
	if len(resp.Runtimes) != 1 || resp.Runtimes[0].ID != "rt-1" {
		t.Fatalf("resp.Runtimes = %+v", resp.Runtimes)
	}
}

func TestRegisterRuntimesForWorkspaceCleansUpWorkspaceNotFoundImmediately(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		WorkspaceID: "ws-1",
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {DaemonID: "daemon-1", Token: "mdt_old_token", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/daemon/register" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"workspace not found"}`))
			return
		}
		t.Fatalf("unexpected path: %s", r.URL.Path)
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	d.cfg.Agents = map[string]AgentEntry{"claude": {Path: "/usr/bin/true"}}
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1", Provider: "claude", Status: "online"}
	d.runtimeWorkspace["rt-1"] = "ws-1"
	d.workspaceAuth["ws-1"] = cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_old_token", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)}

	_, err := d.registerRuntimesForWorkspace(context.Background(), "ws-1")
	if err == nil {
		t.Fatal("expected registerRuntimesForWorkspace() to fail")
	}

	cfg, loadErr := cli.LoadCLIConfig()
	if loadErr != nil {
		t.Fatalf("LoadCLIConfig() error = %v", loadErr)
	}
	if len(cfg.WatchedWorkspaces) != 0 {
		t.Fatalf("WatchedWorkspaces = %+v, want empty", cfg.WatchedWorkspaces)
	}
	if cfg.WorkspaceID != "" {
		t.Fatalf("WorkspaceID = %q, want empty", cfg.WorkspaceID)
	}
	if _, ok := cfg.DaemonAuth["ws-1"]; ok {
		t.Fatalf("ws-1 daemon auth should be removed from config: %+v", cfg.DaemonAuth)
	}
	if _, ok := d.workspaces["ws-1"]; ok {
		t.Fatalf("workspace ws-1 should be removed from memory: %+v", d.workspaces)
	}
	if _, ok := d.runtimeIndex["rt-1"]; ok {
		t.Fatalf("runtime rt-1 should be removed from memory: %+v", d.runtimeIndex)
	}
	if _, ok := d.runtimeWorkspace["rt-1"]; ok {
		t.Fatalf("runtime workspace rt-1 should be removed from memory: %+v", d.runtimeWorkspace)
	}
	if _, ok := d.workspaceAuth["ws-1"]; ok {
		t.Fatalf("workspace auth ws-1 should be removed from memory: %+v", d.workspaceAuth)
	}
}

func TestSyncWorkspacesFromAPIRemovesDeletedWorkspaceState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken:   "mul_user_token",
		WorkspaceID: "ws-1",
		WatchedWorkspaces: []cli.WatchedWorkspace{
			{ID: "ws-1", Name: "Workspace 1"},
			{ID: "ws-2", Name: "Workspace 2"},
		},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {DaemonID: "daemon-1", Token: "mdt_ws1", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)},
			"ws-2": {DaemonID: "daemon-1", Token: "mdt_ws2", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspaces" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer mul_user_token" {
			t.Fatalf("list workspaces auth = %q, want user token", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"ws-2","name":"Workspace 2"}]`))
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.workspaces["ws-2"] = &workspaceState{workspaceID: "ws-2", runtimeIDs: []string{"rt-2"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1", Provider: "claude", Status: "online"}
	d.runtimeIndex["rt-2"] = Runtime{ID: "rt-2", Provider: "claude", Status: "online"}
	d.runtimeWorkspace["rt-1"] = "ws-1"
	d.runtimeWorkspace["rt-2"] = "ws-2"
	d.workspaceAuth["ws-1"] = cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_ws1", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)}
	d.workspaceAuth["ws-2"] = cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_ws2", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)}

	d.syncWorkspacesFromAPI(context.Background())

	stored, err := cli.LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig() error = %v", err)
	}
	if stored.WorkspaceID != "" {
		t.Fatalf("WorkspaceID = %q, want empty", stored.WorkspaceID)
	}
	if len(stored.WatchedWorkspaces) != 1 || stored.WatchedWorkspaces[0].ID != "ws-2" {
		t.Fatalf("WatchedWorkspaces = %+v, want only ws-2", stored.WatchedWorkspaces)
	}
	if _, ok := stored.DaemonAuth["ws-1"]; ok {
		t.Fatalf("DaemonAuth for ws-1 should be removed: %+v", stored.DaemonAuth)
	}
	if _, ok := stored.DaemonAuth["ws-2"]; !ok {
		t.Fatalf("DaemonAuth for ws-2 should remain: %+v", stored.DaemonAuth)
	}
	if _, ok := d.workspaces["ws-1"]; ok {
		t.Fatalf("workspace ws-1 should be removed from daemon state: %+v", d.workspaces)
	}
	if _, ok := d.runtimeIndex["rt-1"]; ok {
		t.Fatalf("runtime rt-1 should be removed from daemon state: %+v", d.runtimeIndex)
	}
	if _, ok := d.runtimeWorkspace["rt-1"]; ok {
		t.Fatalf("runtime workspace rt-1 should be removed from daemon state: %+v", d.runtimeWorkspace)
	}
	if _, ok := d.workspaceAuth["ws-1"]; ok {
		t.Fatalf("workspace auth for ws-1 should be removed from daemon state: %+v", d.workspaceAuth)
	}
	if _, ok := d.workspaceAuth["ws-2"]; !ok {
		t.Fatalf("workspace auth for ws-2 should remain: %+v", d.workspaceAuth)
	}
}

func TestReloadWorkspacesRefreshesAuthAndUserTokenFromConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-2", Name: "Workspace 2"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-2": {DaemonID: "daemon-1", Token: "mdt_ws2_new", ExpiresAt: "2026-05-21T00:00:00Z"},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	d := newTestDaemon("http://localhost:8080")
	d.userToken = "mul_user_token"
	d.client.SetUserToken("mul_user_token")
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.workspaces["ws-2"] = &workspaceState{workspaceID: "ws-2", runtimeIDs: []string{"rt-2"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1", Provider: "claude", Status: "online"}
	d.runtimeIndex["rt-2"] = Runtime{ID: "rt-2", Provider: "claude", Status: "online"}
	d.runtimeWorkspace["rt-1"] = "ws-1"
	d.runtimeWorkspace["rt-2"] = "ws-2"
	d.workspaceAuth["ws-1"] = cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_ws1_old", ExpiresAt: "2026-05-20T00:00:00Z"}
	d.workspaceAuth["ws-2"] = cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_ws2_old", ExpiresAt: "2026-05-20T00:00:00Z"}

	d.reloadWorkspaces(context.Background())

	if d.userToken != "" {
		t.Fatalf("userToken = %q, want empty", d.userToken)
	}
	if d.client.UserToken() != "" {
		t.Fatalf("client user token = %q, want empty", d.client.UserToken())
	}
	if _, ok := d.workspaces["ws-1"]; ok {
		t.Fatalf("workspace ws-1 should be removed from daemon state: %+v", d.workspaces)
	}
	if _, ok := d.runtimeIndex["rt-1"]; ok {
		t.Fatalf("runtime rt-1 should be removed from daemon state: %+v", d.runtimeIndex)
	}
	if _, ok := d.runtimeWorkspace["rt-1"]; ok {
		t.Fatalf("runtime workspace rt-1 should be removed from daemon state: %+v", d.runtimeWorkspace)
	}
	if _, ok := d.workspaceAuth["ws-1"]; ok {
		t.Fatalf("workspace auth for ws-1 should be removed from daemon state: %+v", d.workspaceAuth)
	}
	if got := d.workspaceAuth["ws-2"]; got.Token != "mdt_ws2_new" || got.DaemonID != "daemon-1" || got.ExpiresAt != "2026-05-21T00:00:00Z" {
		t.Fatalf("workspace auth for ws-2 = %+v", got)
	}
}

func TestReportUsageRecordsSkipsAmbiguousProviderRouting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		UserToken: "mul_user_token",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {DaemonID: "daemon-1", Token: "mdt_ws1", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)},
			"ws-2": {DaemonID: "daemon-1", Token: "mdt_ws2", ExpiresAt: time.Now().Add(14 * 24 * time.Hour).UTC().Format(time.RFC3339)},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	var usageCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/daemon/runtimes/rt-1/usage" || r.URL.Path == "/api/daemon/runtimes/rt-2/usage" {
			usageCalls.Add(1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	d := newTestDaemon(server.URL)
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1", Provider: "claude", Status: "online"}
	d.runtimeIndex["rt-2"] = Runtime{ID: "rt-2", Provider: "claude", Status: "online"}
	d.runtimeWorkspace["rt-1"] = "ws-1"
	d.runtimeWorkspace["rt-2"] = "ws-2"
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.workspaces["ws-2"] = &workspaceState{workspaceID: "ws-2", runtimeIDs: []string{"rt-2"}}
	if err := d.resolveAuth(); err != nil {
		t.Fatalf("resolveAuth() error = %v", err)
	}

	d.reportUsageRecords(context.Background(), []usage.Record{{Provider: "claude", Model: "sonnet"}})

	if usageCalls.Load() != 0 {
		t.Fatalf("usage calls = %d, want 0", usageCalls.Load())
	}
}

func TestBuildAgentEnvUsesUserToken(t *testing.T) {
	d := newTestDaemon("http://localhost:8080")
	d.userToken = "mul_user_token"
	task := Task{
		ID:          "task-1",
		AgentID:     "agent-1",
		RuntimeID:   "rt-1",
		IssueID:     "issue-1",
		WorkspaceID: "ws-1",
		Agent:       &AgentData{Name: "Agent"},
	}
	env := d.buildAgentEnv(task, "Agent", nil)

	if got := env["MULTICA_TOKEN"]; got != "mul_user_token" {
		t.Fatalf("MULTICA_TOKEN = %q, want user token", got)
	}
	if got := env["MULTICA_WORKSPACE_ID"]; got != "ws-1" {
		t.Fatalf("MULTICA_WORKSPACE_ID = %q, want ws-1", got)
	}
}

func TestPersistWorkspaceDaemonAuthPreservesConcurrentWrites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}, {ID: "ws-2", Name: "Workspace 2"}},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	d := newTestDaemon("http://localhost:8080")

	errCh := make(chan error, 2)
	go func() {
		_, err := d.persistWorkspaceDaemonAuth("ws-1", cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_ws1", ExpiresAt: "2026-05-20T00:00:00Z"})
		errCh <- err
	}()
	go func() {
		_, err := d.persistWorkspaceDaemonAuth("ws-2", cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_ws2", ExpiresAt: "2026-05-21T00:00:00Z"})
		errCh <- err
	}()

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("persistWorkspaceDaemonAuth() error = %v", err)
		}
	}

	cfg, err := cli.LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig() error = %v", err)
	}
	if got := cfg.DaemonAuth["ws-1"]; got.Token != "mdt_ws1" || got.DaemonID != "daemon-1" {
		t.Fatalf("ws-1 auth = %+v", got)
	}
	if got := cfg.DaemonAuth["ws-2"]; got.Token != "mdt_ws2" || got.DaemonID != "daemon-1" {
		t.Fatalf("ws-2 auth = %+v", got)
	}
}

func TestPersistWorkspaceDaemonAuthSkipsUnwatchedWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-2", Name: "Workspace 2"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {DaemonID: "daemon-1", Token: "stale", ExpiresAt: "2026-05-01T00:00:00Z"},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	d := newTestDaemon("http://localhost:8080")
	d.workspaceAuth["ws-1"] = cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "stale", ExpiresAt: "2026-05-01T00:00:00Z"}

	if _, err := d.persistWorkspaceDaemonAuth("ws-1", cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_new", ExpiresAt: "2026-05-21T00:00:00Z"}); err != nil {
		t.Fatalf("persistWorkspaceDaemonAuth() error = %v", err)
	}

	cfg, err := cli.LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig() error = %v", err)
	}
	if _, ok := cfg.DaemonAuth["ws-1"]; ok {
		t.Fatalf("ws-1 daemon auth should not be written back: %+v", cfg.DaemonAuth)
	}
	if _, ok := d.workspaceAuth["ws-1"]; ok {
		t.Fatalf("ws-1 workspace auth should be cleared from memory: %+v", d.workspaceAuth)
	}
}

func TestPersistWorkspaceDaemonAuthDoesNotRestoreMemoryAfterRemoval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	d := newTestDaemon("http://localhost:8080")
	beforeMemoryUpdate := make(chan struct{})
	allowFinish := make(chan struct{})
	persistWorkspaceDaemonAuthBeforeMemoryUpdateHook = func() {
		close(beforeMemoryUpdate)
		<-allowFinish
	}
	defer func() { persistWorkspaceDaemonAuthBeforeMemoryUpdateHook = nil }()

	errCh := make(chan error, 1)
	go func() {
		_, err := d.persistWorkspaceDaemonAuth("ws-1", cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_new", ExpiresAt: "2026-05-21T00:00:00Z"})
		errCh <- err
	}()

	<-beforeMemoryUpdate
	if err := cli.UpdateCLIConfigForProfile("", func(cfg *cli.CLIConfig) error {
		cfg.RemoveWatchedWorkspace("ws-1")
		return nil
	}); err != nil {
		t.Fatalf("UpdateCLIConfigForProfile() error = %v", err)
	}
	close(allowFinish)

	if err := <-errCh; err != nil {
		t.Fatalf("persistWorkspaceDaemonAuth() error = %v", err)
	}

	cfg, err := cli.LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig() error = %v", err)
	}
	if _, ok := cfg.DaemonAuth["ws-1"]; ok {
		t.Fatalf("ws-1 daemon auth should stay removed from config: %+v", cfg.DaemonAuth)
	}
	if _, ok := d.workspaceAuth["ws-1"]; ok {
		t.Fatalf("ws-1 workspace auth should stay removed from memory: %+v", d.workspaceAuth)
	}
}

func TestPersistWorkspaceDaemonAuthDoesNotRestoreMemoryAfterRewatchWithoutAuth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	d := newTestDaemon("http://localhost:8080")
	beforeMemoryUpdate := make(chan struct{})
	allowFinish := make(chan struct{})
	persistWorkspaceDaemonAuthBeforeMemoryUpdateHook = func() {
		close(beforeMemoryUpdate)
		<-allowFinish
	}
	defer func() { persistWorkspaceDaemonAuthBeforeMemoryUpdateHook = nil }()

	errCh := make(chan error, 1)
	go func() {
		_, err := d.persistWorkspaceDaemonAuth("ws-1", cli.DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_old", ExpiresAt: "2026-05-21T00:00:00Z"})
		errCh <- err
	}()

	<-beforeMemoryUpdate
	if err := cli.UpdateCLIConfigForProfile("", func(cfg *cli.CLIConfig) error {
		cfg.RemoveWatchedWorkspace("ws-1")
		cfg.AddWatchedWorkspace("ws-1", "Workspace 1")
		if cfg.DaemonAuth != nil {
			delete(cfg.DaemonAuth, "ws-1")
		}
		return nil
	}); err != nil {
		t.Fatalf("UpdateCLIConfigForProfile() error = %v", err)
	}
	close(allowFinish)

	if err := <-errCh; err != nil {
		t.Fatalf("persistWorkspaceDaemonAuth() error = %v", err)
	}

	cfg, err := cli.LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig() error = %v", err)
	}
	if _, ok := cfg.DaemonAuth["ws-1"]; ok {
		t.Fatalf("ws-1 daemon auth should stay absent from config: %+v", cfg.DaemonAuth)
	}
	if _, ok := d.workspaceAuth["ws-1"]; ok {
		t.Fatalf("ws-1 workspace auth should not be restored in memory: %+v", d.workspaceAuth)
	}
}

func newTestDaemon(baseURL string) *Daemon {
	return &Daemon{
		cfg: Config{
			DaemonID: "daemon-1",
			Profile:  "",
		},
		client:        NewClient(baseURL),
		logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		workspaces:           make(map[string]*workspaceState),
		runtimeIndex:         make(map[string]Runtime),
		runtimeWorkspace:     make(map[string]string),
		workspaceAuth:        make(map[string]cli.DaemonAuthConfig),
		workspaceRefreshLock: make(map[string]*sync.Mutex),
	}
}
