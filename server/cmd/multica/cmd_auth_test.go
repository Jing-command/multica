package main

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// testCmd returns a minimal cobra.Command with the --profile persistent flag
// registered, matching the rootCmd setup used in production.
func testCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.PersistentFlags().String("profile", "", "")
	return cmd
}

func TestResolveAppURL(t *testing.T) {
	cmd := testCmd()

	t.Run("prefers MULTICA_APP_URL", func(t *testing.T) {
		t.Setenv("MULTICA_APP_URL", "http://localhost:14000")
		t.Setenv("FRONTEND_ORIGIN", "http://localhost:13000")

		if got := resolveAppURL(cmd); got != "http://localhost:14000" {
			t.Fatalf("resolveAppURL() = %q, want %q", got, "http://localhost:14000")
		}
	})

	t.Run("falls back to FRONTEND_ORIGIN", func(t *testing.T) {
		t.Setenv("MULTICA_APP_URL", "")
		t.Setenv("FRONTEND_ORIGIN", "http://localhost:13026")

		if got := resolveAppURL(cmd); got != "http://localhost:13026" {
			t.Fatalf("resolveAppURL() = %q, want %q", got, "http://localhost:13026")
		}
	})

	t.Run("defaults to production", func(t *testing.T) {
		t.Setenv("MULTICA_APP_URL", "")
		t.Setenv("FRONTEND_ORIGIN", "")
		t.Setenv("HOME", t.TempDir()) // avoid reading real config

		if got := resolveAppURL(cmd); got != "https://multica.ai" {
			t.Fatalf("resolveAppURL() = %q, want %q", got, "https://multica.ai")
		}
	})
}

func TestNormalizeAPIBaseURL(t *testing.T) {
	t.Run("converts websocket base URL", func(t *testing.T) {
		if got := normalizeAPIBaseURL("ws://localhost:18106/ws"); got != "http://localhost:18106" {
			t.Fatalf("normalizeAPIBaseURL() = %q, want %q", got, "http://localhost:18106")
		}
	})

	t.Run("keeps http base URL", func(t *testing.T) {
		if got := normalizeAPIBaseURL("http://localhost:8080"); got != "http://localhost:8080" {
			t.Fatalf("normalizeAPIBaseURL() = %q, want %q", got, "http://localhost:8080")
		}
	})

	t.Run("falls back to raw value for invalid URL", func(t *testing.T) {
		if got := normalizeAPIBaseURL("://bad-url"); got != "://bad-url" {
			t.Fatalf("normalizeAPIBaseURL() = %q, want %q", got, "://bad-url")
		}
	})
}

func TestRunAuthLogoutClearsDaemonAuthWithoutUserToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := testCmd()

	cfg := cli.CLIConfig{
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {
				DaemonID:  "daemon-1",
				Token:     "mdt_token_1",
				ExpiresAt: "2026-05-01T00:00:00Z",
			},
		},
	}
	if err := cli.SaveCLIConfig(cfg); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	if err := runAuthLogout(cmd, nil); err != nil {
		t.Fatalf("runAuthLogout() error = %v", err)
	}

	stored, err := cli.LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig() error = %v", err)
	}
	if stored.UserToken != "" {
		t.Fatalf("UserToken = %q, want empty", stored.UserToken)
	}
	if len(stored.DaemonAuth) != 0 {
		t.Fatalf("DaemonAuth = %+v, want empty", stored.DaemonAuth)
	}
}

func TestPersistLoginClearsWorkspaceState(t *testing.T) {
	cfg := cli.CLIConfig{
		WorkspaceID:       "ws-1",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {DaemonID: "daemon-1", Token: "mdt_token_1", ExpiresAt: "2026-05-01T00:00:00Z"},
		},
	}

	persistLogin(&cfg, "mul_user_token", "http://localhost:8080", "http://localhost:3000")

	if cfg.UserToken != "mul_user_token" {
		t.Fatalf("UserToken = %q, want %q", cfg.UserToken, "mul_user_token")
	}
	if cfg.WorkspaceID != "" {
		t.Fatalf("WorkspaceID = %q, want empty", cfg.WorkspaceID)
	}
	if len(cfg.WatchedWorkspaces) != 0 {
		t.Fatalf("WatchedWorkspaces = %+v, want empty", cfg.WatchedWorkspaces)
	}
	if len(cfg.DaemonAuth) != 0 {
		t.Fatalf("DaemonAuth = %+v, want empty", cfg.DaemonAuth)
	}
}

func TestUpdateCLIConfigForProfileWithPersistLoginClearsWorkspaceState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cli.SaveCLIConfig(cli.CLIConfig{
		WorkspaceID:       "ws-1",
		WatchedWorkspaces: []cli.WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {DaemonID: "daemon-1", Token: "mdt_token_1", ExpiresAt: "2026-05-01T00:00:00Z"},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	if err := cli.UpdateCLIConfigForProfile("", func(cfg *cli.CLIConfig) error {
		persistLogin(cfg, "mul_user_token", "http://localhost:8080", "http://localhost:3000")
		return nil
	}); err != nil {
		t.Fatalf("UpdateCLIConfigForProfile() error = %v", err)
	}

	stored, err := cli.LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig() error = %v", err)
	}
	if stored.UserToken != "mul_user_token" {
		t.Fatalf("UserToken = %q, want %q", stored.UserToken, "mul_user_token")
	}
	if stored.WorkspaceID != "" {
		t.Fatalf("WorkspaceID = %q, want empty", stored.WorkspaceID)
	}
	if len(stored.WatchedWorkspaces) != 0 {
		t.Fatalf("WatchedWorkspaces = %+v, want empty", stored.WatchedWorkspaces)
	}
	if len(stored.DaemonAuth) != 0 {
		t.Fatalf("DaemonAuth = %+v, want empty", stored.DaemonAuth)
	}
}
