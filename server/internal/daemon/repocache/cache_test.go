package repocache

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func testLogger() *slog.Logger {
	return slog.Default()
}

func TestBareDirName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, want string
	}{
		{"https://github.com/org/my-repo.git", "my-repo.git"},
		{"https://github.com/org/my-repo", "my-repo.git"},
		{"git@github.com:org/my-repo.git", "my-repo.git"},
		{"git@github.com:org/my-repo", "my-repo.git"},
		{"https://github.com/org/repo/", "repo.git"},
		{"my-repo", "my-repo.git"},
		{"", "repo.git"},
	}
	for _, tt := range tests {
		if got := bareDirName(tt.input); got != tt.want {
			t.Errorf("bareDirName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsBareRepo(t *testing.T) {
	t.Parallel()

	// A directory with a HEAD file should be detected as bare.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	if !isBareRepo(dir) {
		t.Error("expected bare repo to be detected")
	}

	// An empty directory should not.
	emptyDir := t.TempDir()
	if isBareRepo(emptyDir) {
		t.Error("expected empty dir to not be detected as bare repo")
	}
}

// createTestRepo creates a local git repo with an initial commit and returns its path.
func createTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", dir},
		{"-C", dir, "commit", "--allow-empty", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git setup failed: %s: %v", out, err)
		}
	}
	return dir
}

func TestSyncAndLookup(t *testing.T) {
	t.Parallel()
	sourceRepo := createTestRepo(t)
	cacheRoot := t.TempDir()

	cache := New(cacheRoot, testLogger())

	// Sync should clone the repo.
	err := cache.Sync("ws-123", []RepoInfo{
		{URL: sourceRepo, Description: "test repo"},
	})
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Lookup should find the cached repo.
	path := cache.Lookup("ws-123", sourceRepo)
	if path == "" {
		t.Fatal("expected to find cached repo")
	}
	if !isBareRepo(path) {
		t.Fatalf("expected bare repo at %s", path)
	}

	// Lookup for unknown URL should return empty.
	if got := cache.Lookup("ws-123", "https://github.com/org/unknown"); got != "" {
		t.Fatalf("expected empty for unknown URL, got %q", got)
	}

	// Lookup for unknown workspace should return empty.
	if got := cache.Lookup("ws-999", sourceRepo); got != "" {
		t.Fatalf("expected empty for unknown workspace, got %q", got)
	}
}

func TestSyncFetchesExisting(t *testing.T) {
	t.Parallel()
	sourceRepo := createTestRepo(t)
	cacheRoot := t.TempDir()

	cache := New(cacheRoot, testLogger())

	// First sync: clone.
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("first sync failed: %v", err)
	}

	// Record the HEAD commit hash in the cache.
	barePath := cache.Lookup("ws-1", sourceRepo)
	oldHead := gitHead(t, barePath)

	// Add a commit to source.
	cmd := exec.Command("git", "-C", sourceRepo, "commit", "--allow-empty", "-m", "second")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add commit failed: %s: %v", out, err)
	}
	sourceHead := gitHead(t, sourceRepo)
	if sourceHead == oldHead {
		t.Fatal("source HEAD should differ after new commit")
	}

	// Second sync: should fetch (not re-clone).
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("second sync failed: %v", err)
	}

	// Verify the cache HEAD was updated.
	newHead := gitHead(t, barePath)
	if newHead == oldHead {
		t.Fatal("expected cache HEAD to be updated after fetch")
	}
	if newHead != sourceHead {
		t.Fatalf("expected cache HEAD %s to match source HEAD %s", newHead, sourceHead)
	}
}

func gitHead(t *testing.T, repoPath string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD failed in %s: %v", repoPath, err)
	}
	return strings.TrimSpace(string(out))
}

func TestWorktreeFromCache(t *testing.T) {
	t.Parallel()
	sourceRepo := createTestRepo(t)
	cacheRoot := t.TempDir()

	cache := New(cacheRoot, testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	barePath := cache.Lookup("ws-1", sourceRepo)
	if barePath == "" {
		t.Fatal("expected cached repo")
	}

	// Create a worktree from the bare cache — this is the actual use case.
	worktreeDir := filepath.Join(t.TempDir(), "work")
	cmd := exec.Command("git", "-C", barePath, "worktree", "add", "-b", "test-branch", worktreeDir, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add failed: %s: %v", out, err)
	}
	defer exec.Command("git", "-C", barePath, "worktree", "remove", "--force", worktreeDir).Run()

	// Verify worktree exists and is on the right branch.
	cmd = exec.Command("git", "-C", worktreeDir, "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("show branch failed: %v", err)
	}
	if got := trimLine(string(out)); got != "test-branch" {
		t.Fatalf("expected branch 'test-branch', got %q", got)
	}
}

func TestCreateWorktree(t *testing.T) {
	t.Parallel()
	sourceRepo := createTestRepo(t)
	cacheRoot := t.TempDir()

	cache := New(cacheRoot, testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	workDir := t.TempDir()
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir,
		AgentName:   "Code Reviewer",
		TaskID:      "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
	})
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	// Verify the worktree was created.
	if _, err := os.Stat(result.Path); os.IsNotExist(err) {
		t.Fatalf("worktree path does not exist: %s", result.Path)
	}

	// Verify branch name format.
	if !strings.HasPrefix(result.BranchName, "agent/code-reviewer/") {
		t.Errorf("expected branch to start with 'agent/code-reviewer/', got %q", result.BranchName)
	}

	// Verify the worktree is on the correct branch.
	cmd := exec.Command("git", "-C", result.Path, "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("show branch failed: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != result.BranchName {
		t.Errorf("expected branch %q, got %q", result.BranchName, got)
	}
}

func TestCreateWorktreeNotCached(t *testing.T) {
	t.Parallel()
	cacheRoot := t.TempDir()
	cache := New(cacheRoot, testLogger())

	_, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     "https://github.com/org/nonexistent",
		WorkDir:     t.TempDir(),
		AgentName:   "Agent",
		TaskID:      "test-task-id",
	})
	if err == nil {
		t.Fatal("expected error for uncached repo")
	}
	if !strings.Contains(err.Error(), "not found in cache") {
		t.Errorf("expected 'not found in cache' error, got: %v", err)
	}
}

func TestCreateWorktree_AppliesPermissionSnapshotToNewWorktree(t *testing.T) {
	t.Parallel()

	sourceRepo := createTestRepo(t)
	repoDirName := repoNameFromURL(sourceRepo)
	cacheRoot := t.TempDir()
	cache := New(cacheRoot, testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".agent_context"), 0o755); err != nil {
		t.Fatalf("mkdir context dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".agent_context", "permission_snapshot.json"), []byte(`{"allowed_paths":["`+repoDirName+`/README.md"]}`), 0o644); err != nil {
		t.Fatalf("write permission snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "CLAUDE.md"), []byte("runtime metadata\n"), 0o644); err != nil {
		t.Fatalf("seed runtime file: %v", err)
	}

	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir,
		AgentName:   "Code Reviewer",
		TaskID:      "11111111-2222-3333-4444-555555555555",
	})
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}
	t.Cleanup(func() {
		makeTreeWritable(t, workDir)
	})

	if err := os.WriteFile(filepath.Join(result.Path, "blocked.txt"), []byte("denied\n"), 0o644); err == nil {
		t.Fatal("expected write outside allowed scope to fail")
	}
	if err := os.WriteFile(filepath.Join(workDir, "CLAUDE.md"), []byte("runtime metadata updated\n"), 0o644); err != nil {
		t.Fatalf("expected workdir sibling file to stay writable: %v", err)
	}
}

func TestCreateWorktree_AppliesPermissionSnapshotToExistingWorktree(t *testing.T) {
	t.Parallel()

	sourceRepo := createTestRepo(t)
	repoDirName := repoNameFromURL(sourceRepo)
	cacheRoot := t.TempDir()
	cache := New(cacheRoot, testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	workDir := t.TempDir()
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir,
		AgentName:   "Code Reviewer",
		TaskID:      "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	})
	if err != nil {
		t.Fatalf("initial CreateWorktree failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(result.Path, "blocked.txt"), []byte("allowed before snapshot\n"), 0o644); err != nil {
		t.Fatalf("seed blocked file before enforcement: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "CLAUDE.md"), []byte("runtime metadata\n"), 0o644); err != nil {
		t.Fatalf("seed runtime file: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(workDir, ".agent_context"), 0o755); err != nil {
		t.Fatalf("mkdir context dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".agent_context", "permission_snapshot.json"), []byte(`{"allowed_paths":["`+repoDirName+`/README.md"]}`), 0o644); err != nil {
		t.Fatalf("write permission snapshot: %v", err)
	}

	result, err = cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir,
		AgentName:   "Code Reviewer",
		TaskID:      "ffffffff-1111-2222-3333-444444444444",
	})
	if err != nil {
		t.Fatalf("reused CreateWorktree failed: %v", err)
	}
	t.Cleanup(func() {
		makeTreeWritable(t, workDir)
	})

	if err := os.WriteFile(filepath.Join(result.Path, "blocked.txt"), []byte("denied after snapshot\n"), 0o644); err == nil {
		t.Fatal("expected existing worktree to be re-locked by permission snapshot")
	}
	if err := os.WriteFile(filepath.Join(workDir, "CLAUDE.md"), []byte("runtime metadata refreshed\n"), 0o644); err != nil {
		t.Fatalf("expected workdir sibling file to stay writable after reuse: %v", err)
	}

	if err := execenv.ValidateWritePath(repoDirName+"/README.md", execenv.PermissionSnapshot{AllowedPaths: []string{repoDirName + "/README.md"}}); err != nil {
		t.Fatalf("expected snapshot sanity check to pass: %v", err)
	}
}

func TestCreateWorktree_ReuseUnlocksPreviouslyLockedRepoBeforeUpdate(t *testing.T) {
	t.Parallel()

	sourceRepo := createTestRepo(t)
	repoDirName := repoNameFromURL(sourceRepo)
	readmePath := filepath.Join(sourceRepo, "README.md")
	if err := os.WriteFile(readmePath, []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write initial README: %v", err)
	}
	cmd := exec.Command("git", "-C", sourceRepo, "add", "README.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add README failed: %s: %v", out, err)
	}
	cmd = exec.Command("git", "-C", sourceRepo, "commit", "-m", "add readme")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit README failed: %s: %v", out, err)
	}

	cacheRoot := t.TempDir()
	cache := New(cacheRoot, testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	workDir := t.TempDir()
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir,
		AgentName:   "Code Reviewer",
		TaskID:      "12345678-1111-2222-3333-444444444444",
	})
	if err != nil {
		t.Fatalf("initial CreateWorktree failed: %v", err)
	}
	t.Cleanup(func() {
		makeTreeWritable(t, workDir)
	})

	if err := os.MkdirAll(filepath.Join(workDir, ".agent_context"), 0o755); err != nil {
		t.Fatalf("mkdir context dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".agent_context", "permission_snapshot.json"), []byte(`{"allowed_paths":["`+repoDirName+`/allowed/**"]}`), 0o644); err != nil {
		t.Fatalf("write permission snapshot: %v", err)
	}
	if err := applyPermissionSnapshot(workDir, result.Path); err != nil {
		t.Fatalf("apply permission snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(result.Path, "README.md"), []byte("locked\n"), 0o644); err == nil {
		t.Fatal("expected README to be locked before reuse update")
	}

	if err := os.WriteFile(readmePath, []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("update source README: %v", err)
	}
	cmd = exec.Command("git", "-C", sourceRepo, "commit", "-am", "update readme")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit updated README failed: %s: %v", out, err)
	}

	result, err = cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir,
		AgentName:   "Code Reviewer",
		TaskID:      "87654321-1111-2222-3333-444444444444",
	})
	if err != nil {
		t.Fatalf("reused CreateWorktree should unlock and update locked repo: %v", err)
	}

	barePath := cache.Lookup("ws-1", sourceRepo)
	sourceHead := gitHead(t, sourceRepo)
	cachedHead := gitHead(t, barePath)
	worktreeHead := gitHead(t, result.Path)
	readme, err := os.ReadFile(filepath.Join(result.Path, "README.md"))
	if err != nil {
		t.Fatalf("read updated README: %v", err)
	}
	if got := string(readme); got != "v2\n" {
		t.Fatalf("expected updated README content, got %q (source=%s cached=%s worktree=%s)", got, sourceHead, cachedHead, worktreeHead)
	}
	if err := os.WriteFile(filepath.Join(result.Path, "README.md"), []byte("locked again\n"), 0o644); err == nil {
		t.Fatal("expected README to be re-locked after reuse update")
	}
}

func TestCreateWorktree_ReuseFailureDoesNotLeaveLockedRepoWritable(t *testing.T) {
	t.Parallel()

	sourceRepo := createTestRepo(t)
	repoDirName := repoNameFromURL(sourceRepo)
	readmePath := filepath.Join(sourceRepo, "README.md")
	if err := os.WriteFile(readmePath, []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write initial README: %v", err)
	}
	cmd := exec.Command("git", "-C", sourceRepo, "add", "README.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add README failed: %s: %v", out, err)
	}
	cmd = exec.Command("git", "-C", sourceRepo, "commit", "-m", "add readme")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit README failed: %s: %v", out, err)
	}

	cacheRoot := t.TempDir()
	cache := New(cacheRoot, testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	workDir := t.TempDir()
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir,
		AgentName:   "Code Reviewer",
		TaskID:      "99999999-1111-2222-3333-444444444444",
	})
	if err != nil {
		t.Fatalf("initial CreateWorktree failed: %v", err)
	}
	t.Cleanup(func() {
		makeTreeWritable(t, workDir)
	})

	if err := os.MkdirAll(filepath.Join(workDir, ".agent_context"), 0o755); err != nil {
		t.Fatalf("mkdir context dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".agent_context", "permission_snapshot.json"), []byte(`{"allowed_paths":["`+repoDirName+`/allowed/**"]}`), 0o644); err != nil {
		t.Fatalf("write valid permission snapshot: %v", err)
	}
	if err := applyPermissionSnapshot(workDir, result.Path); err != nil {
		t.Fatalf("apply permission snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(result.Path, "README.md"), []byte("locked\n"), 0o644); err == nil {
		t.Fatal("expected README to be locked before reuse failure")
	}

	if err := os.WriteFile(filepath.Join(workDir, ".agent_context", "permission_snapshot.json"), []byte(`{"allowed_paths":[`), 0o644); err != nil {
		t.Fatalf("corrupt permission snapshot: %v", err)
	}

	_, err = cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir,
		AgentName:   "Code Reviewer",
		TaskID:      "88888888-1111-2222-3333-444444444444",
	})
	if err == nil {
		t.Fatal("expected reused CreateWorktree to fail with invalid trusted snapshot")
	}
	if err := os.WriteFile(filepath.Join(result.Path, "README.md"), []byte("should still be locked\n"), 0o644); err == nil {
		t.Fatal("expected repo to remain locked after reuse failure")
	}
}

func trimLine(s string) string {
	return strings.TrimSpace(s)
}

func makeTreeWritable(t *testing.T, root string) {
	t.Helper()
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		return os.Chmod(path, info.Mode().Perm()|0o700)
	}); err != nil {
		t.Fatalf("make tree writable: %v", err)
	}
}
