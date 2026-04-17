package cli

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAddWatchedWorkspacePreservesExistingDaemonAuth(t *testing.T) {
	cfg := CLIConfig{
		DaemonAuth: map[string]DaemonAuthConfig{
			"ws-1": {
				DaemonID:  "daemon-1",
				Token:     "mdt_token_1",
				ExpiresAt: "2026-05-01T00:00:00Z",
			},
		},
	}

	if added := cfg.AddWatchedWorkspace("ws-2", "Workspace 2"); !added {
		t.Fatal("expected workspace to be added")
	}

	auth, ok := cfg.DaemonAuth["ws-1"]
	if !ok {
		t.Fatal("expected existing daemon auth to remain")
	}
	if auth.DaemonID != "daemon-1" || auth.Token != "mdt_token_1" || auth.ExpiresAt != "2026-05-01T00:00:00Z" {
		t.Fatalf("unexpected daemon auth after add: %+v", auth)
	}
}

func TestRemoveWatchedWorkspaceRemovesDaemonAuth(t *testing.T) {
	cfg := CLIConfig{
		WatchedWorkspaces: []WatchedWorkspace{
			{ID: "ws-1", Name: "Workspace 1"},
			{ID: "ws-2", Name: "Workspace 2"},
		},
		DaemonAuth: map[string]DaemonAuthConfig{
			"ws-1": {
				DaemonID:  "daemon-1",
				Token:     "mdt_token_1",
				ExpiresAt: "2026-05-01T00:00:00Z",
			},
			"ws-2": {
				DaemonID:  "daemon-2",
				Token:     "mdt_token_2",
				ExpiresAt: "2026-06-01T00:00:00Z",
			},
		},
	}

	if removed := cfg.RemoveWatchedWorkspace("ws-1"); !removed {
		t.Fatal("expected workspace to be removed")
	}

	if _, ok := cfg.DaemonAuth["ws-1"]; ok {
		t.Fatal("expected daemon auth for removed workspace to be deleted")
	}
	if _, ok := cfg.DaemonAuth["ws-2"]; !ok {
		t.Fatal("expected daemon auth for remaining workspace to be preserved")
	}
	if len(cfg.WatchedWorkspaces) != 1 || cfg.WatchedWorkspaces[0].ID != "ws-2" {
		t.Fatalf("unexpected watched workspaces after removal: %+v", cfg.WatchedWorkspaces)
	}
}

func TestUpdateCLIConfigForProfileSerializesConcurrentMutations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SaveCLIConfig(CLIConfig{
		WatchedWorkspaces: []WatchedWorkspace{{ID: "ws-1", Name: "Workspace 1"}},
		DaemonAuth: map[string]DaemonAuthConfig{
			"ws-1": {DaemonID: "daemon-1", Token: "mdt_ws1", ExpiresAt: "2026-05-01T00:00:00Z"},
		},
	}); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		<-start
		if err := UpdateCLIConfigForProfile("", func(cfg *CLIConfig) error {
			cfg.RemoveWatchedWorkspace("ws-1")
			return nil
		}); err != nil {
			t.Errorf("remove update error = %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		<-start
		if err := UpdateCLIConfigForProfile("", func(cfg *CLIConfig) error {
			if cfg.DaemonAuth == nil {
				cfg.DaemonAuth = make(map[string]DaemonAuthConfig)
			}
			cfg.DaemonAuth["ws-2"] = DaemonAuthConfig{DaemonID: "daemon-1", Token: "mdt_ws2", ExpiresAt: "2026-05-02T00:00:00Z"}
			return nil
		}); err != nil {
			t.Errorf("persist update error = %v", err)
		}
	}()

	close(start)
	wg.Wait()

	cfg, err := LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig() error = %v", err)
	}
	if len(cfg.WatchedWorkspaces) != 0 {
		t.Fatalf("WatchedWorkspaces = %+v, want empty", cfg.WatchedWorkspaces)
	}
	if _, ok := cfg.DaemonAuth["ws-1"]; ok {
		t.Fatalf("daemon auth for removed ws-1 should stay deleted: %+v", cfg.DaemonAuth)
	}
	if got := cfg.DaemonAuth["ws-2"]; got.Token != "mdt_ws2" || got.DaemonID != "daemon-1" {
		t.Fatalf("ws-2 auth = %+v", got)
	}
}

func TestUpdateCLIConfigForProfileCreatesLockFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := UpdateCLIConfigForProfile("", func(cfg *CLIConfig) error {
		cfg.UserToken = "mul_user_token"
		return nil
	}); err != nil {
		t.Fatalf("UpdateCLIConfigForProfile() error = %v", err)
	}

	configPath, err := CLIConfigPathForProfile("")
	if err != nil {
		t.Fatalf("CLIConfigPathForProfile() error = %v", err)
	}
	lockPath := filepath.Join(filepath.Dir(configPath), ".config.lock")
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("expected lock file to exist: %v", err)
	}
}
