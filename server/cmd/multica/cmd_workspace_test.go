package main

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestApplyWatchedWorkspaceSetsDefaultWorkspace(t *testing.T) {
	cfg := cli.CLIConfig{}

	added := applyWatchedWorkspace(&cfg, cli.WatchedWorkspace{ID: "ws-1", Name: "Workspace 1"})

	if !added {
		t.Fatal("expected workspace to be added")
	}
	if cfg.WorkspaceID != "ws-1" {
		t.Fatalf("WorkspaceID = %q, want %q", cfg.WorkspaceID, "ws-1")
	}
	if len(cfg.WatchedWorkspaces) != 1 || cfg.WatchedWorkspaces[0].ID != "ws-1" {
		t.Fatalf("WatchedWorkspaces = %+v, want ws-1", cfg.WatchedWorkspaces)
	}
}

func TestRunUnwatchRemovesWorkspaceDaemonAuthAndDefaultWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := testCmd()

	cfg := cli.CLIConfig{
		WorkspaceID: "ws-1",
		WatchedWorkspaces: []cli.WatchedWorkspace{
			{ID: "ws-1", Name: "Workspace 1"},
			{ID: "ws-2", Name: "Workspace 2"},
		},
		DaemonAuth: map[string]cli.DaemonAuthConfig{
			"ws-1": {DaemonID: "daemon-1", Token: "mdt_token_1", ExpiresAt: "2026-05-01T00:00:00Z"},
			"ws-2": {DaemonID: "daemon-2", Token: "mdt_token_2", ExpiresAt: "2026-06-01T00:00:00Z"},
		},
	}
	if err := cli.SaveCLIConfig(cfg); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	if err := runUnwatch(cmd, []string{"ws-1"}); err != nil {
		t.Fatalf("runUnwatch() error = %v", err)
	}

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
}
