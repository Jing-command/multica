package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWritePath_BlockedPathRejected(t *testing.T) {
	t.Parallel()

	snapshot := PermissionSnapshot{
		AllowedPaths: []string{"server/internal/service/**"},
		BlockedPaths: []string{"server/internal/handler/**", "CLAUDE.md"},
	}

	err := ValidateWritePath("server/internal/handler/comment.go", snapshot)
	if err == nil {
		t.Fatal("expected blocked path to be rejected")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked path error, got %v", err)
	}
}

func TestValidateWritePath_ReadOnlyPathRejected(t *testing.T) {
	t.Parallel()

	snapshot := PermissionSnapshot{
		AllowedPaths:  []string{"apps/web/**"},
		ReadOnlyPaths: []string{"apps/web/docs/**"},
	}

	err := ValidateWritePath("apps/web/docs/guide.md", snapshot)
	if err == nil {
		t.Fatal("expected read-only path to be rejected")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only path error, got %v", err)
	}
}

func TestValidateDiffPaths_OutOfScopeRejected(t *testing.T) {
	t.Parallel()

	snapshot := PermissionSnapshot{
		AllowedPaths: []string{"server/internal/service/**"},
	}

	err := ValidateDiffPaths([]string{"server/internal/service/orchestration.go", "server/cmd/server/router.go"}, snapshot)
	if err == nil {
		t.Fatal("expected out-of-scope diff path to be rejected")
	}
	if !strings.Contains(err.Error(), "outside allowed write scope") {
		t.Fatalf("expected out-of-scope error, got %v", err)
	}
}

func TestWriteContextFiles_WritesPermissionSnapshot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	snapshotJSON := []byte(`{"allowed_paths":["repo/**"],"read_only_paths":["repo/docs/**"],"blocked_paths":["repo/.git/**"],"allowed_tools":["Read","Edit"]}`)

	ctx := TaskContextForEnv{
		IssueID:                "issue-with-permissions",
		PermissionSnapshotJSON: snapshotJSON,
	}

	if err := writeContextFiles(dir, "claude", ctx); err != nil {
		t.Fatalf("writeContextFiles failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, ".agent_context", "permission_snapshot.json"))
	if err != nil {
		t.Fatalf("failed to read permission snapshot: %v", err)
	}
	if string(content) != string(snapshotJSON) {
		t.Fatalf("permission snapshot content = %q, want %q", string(content), string(snapshotJSON))
	}
}

func TestWriteContextFiles_RemovesStalePermissionSnapshot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	contextDir := filepath.Join(dir, ".agent_context")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatalf("mkdir context dir: %v", err)
	}
	stalePath := filepath.Join(contextDir, "permission_snapshot.json")
	if err := os.WriteFile(stalePath, []byte(`{"allowed_paths":["repo/**"]}`), 0o644); err != nil {
		t.Fatalf("write stale snapshot: %v", err)
	}

	if err := writeContextFiles(dir, "claude", TaskContextForEnv{IssueID: "issue-without-permissions"}); err != nil {
		t.Fatalf("writeContextFiles failed: %v", err)
	}

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("expected stale permission snapshot to be removed, got err=%v", err)
	}
}

func TestInjectRuntimeConfig_IncludesHardPermissionsWhenSnapshotPresent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ctx := TaskContextForEnv{
		IssueID:                "issue-with-permissions",
		PermissionSnapshotJSON: []byte(`{"allowed_paths":["repo/**"]}`),
	}

	if err := InjectRuntimeConfig(dir, "claude", ctx); err != nil {
		t.Fatalf("InjectRuntimeConfig failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("failed to read CLAUDE.md: %v", err)
	}

	s := string(content)
	for _, want := range []string{
		"## Hard file permissions",
		"Your allowed write scope is enforced by the execution environment after repository checkout.",
		"Do not attempt to work around this boundary.",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("CLAUDE.md missing %q", want)
		}
	}
}

func TestLoadPermissionSnapshot_FindsNearestSnapshot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	nested := filepath.Join(root, "repo", "subdir")
	if err := os.MkdirAll(filepath.Join(root, ".agent_context"), 0o755); err != nil {
		t.Fatalf("mkdir context dir: %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent_context", "permission_snapshot.json"), []byte(`{"allowed_paths":["repo/**"],"blocked_paths":["repo/.git/**"]}`), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	snapshot, found, err := LoadPermissionSnapshot(nested)
	if err != nil {
		t.Fatalf("LoadPermissionSnapshot failed: %v", err)
	}
	if !found {
		t.Fatal("expected permission snapshot to be found")
	}
	if len(snapshot.AllowedPaths) != 1 || snapshot.AllowedPaths[0] != "repo/**" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestApplyPermissionSnapshotToWorktree_EnforcesFilesystemBoundaries(t *testing.T) {
	t.Parallel()

	worktree := filepath.Join(t.TempDir(), "repo")
	mustMkdirAll(t, filepath.Join(worktree, "allowed"))
	mustMkdirAll(t, filepath.Join(worktree, "other"))
	mustWriteFile(t, filepath.Join(worktree, "allowed", "editable.go"), "package allowed\n")
	mustWriteFile(t, filepath.Join(worktree, "allowed", "blocked.go"), "package blocked\n")
	mustWriteFile(t, filepath.Join(worktree, "other", "outside.go"), "package outside\n")

	snapshot := PermissionSnapshot{
		AllowedPaths: []string{"allowed/**"},
		BlockedPaths: []string{"allowed/blocked.go"},
	}

	if err := ApplyPermissionSnapshotToWorktree(worktree, snapshot); err != nil {
		t.Fatalf("ApplyPermissionSnapshotToWorktree failed: %v", err)
	}
	t.Cleanup(func() {
		makePermissionTreeWritable(t, worktree)
	})

	if err := os.WriteFile(filepath.Join(worktree, "allowed", "editable.go"), []byte("package allowed\n// updated\n"), 0o644); err != nil {
		t.Fatalf("expected allowed file to stay writable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "allowed", "new.go"), []byte("package allowed\n"), 0o644); err != nil {
		t.Fatalf("expected creating file in allowed dir to succeed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "allowed", "blocked.go"), []byte("package blocked\n// denied\n"), 0o644); err == nil {
		t.Fatal("expected blocked file write to fail")
	}
	if err := os.WriteFile(filepath.Join(worktree, "other", "outside.go"), []byte("package outside\n// denied\n"), 0o644); err == nil {
		t.Fatal("expected out-of-scope file write to fail")
	}
	if err := os.WriteFile(filepath.Join(worktree, "other", "new.go"), []byte("package outside\n"), 0o644); err == nil {
		t.Fatal("expected creating file outside allowed scope to fail")
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func makePermissionTreeWritable(t *testing.T, root string) {
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
		t.Fatalf("make permission tree writable: %v", err)
	}
}
