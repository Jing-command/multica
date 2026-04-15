package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestWrapCommandWithSandbox_UsesSandboxExecWhenSnapshotPresent(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox wrapping only applies on darwin")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	workDir := t.TempDir()
	cmdPath, args, err := wrapCommandWithSandbox("claude", []string{"-p", "hello"}, ExecOptions{
		Cwd:                    workDir,
		PermissionSnapshotJSON: []byte(`{"allowed_paths":["repo/**"]}`),
	})
	if err != nil {
		t.Fatalf("wrapCommandWithSandbox failed: %v", err)
	}
	if cmdPath != "sandbox-exec" {
		t.Fatalf("command path = %q, want sandbox-exec", cmdPath)
	}
	if len(args) < 5 {
		t.Fatalf("wrapped args too short: %#v", args)
	}
	if args[0] != "-p" {
		t.Fatalf("args[0] = %q, want -p", args[0])
	}
	if !strings.Contains(args[1], "deny file-write*") {
		t.Fatalf("sandbox profile missing write deny: %q", args[1])
	}
	if got := args[len(args)-3:]; got[0] != "claude" || got[1] != "-p" || got[2] != "hello" {
		t.Fatalf("wrapped command tail = %#v, want [claude -p hello]", got)
	}
}

func TestBuildSandboxProfile_AllowsWriteInAllowedScope(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox profile only enforced on darwin")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	workDir, editable, _, _ := makeSandboxFixture(t)
	profile, err := buildSandboxProfile(workDir, execenv.PermissionSnapshot{
		AllowedPaths: []string{"repo/**"},
	})
	if err != nil {
		t.Fatalf("buildSandboxProfile failed: %v", err)
	}

	if err := runSandboxedSh(profile, `printf updated > "$1"`, editable); err != nil {
		t.Fatalf("expected allowed write to succeed: %v", err)
	}
	content, err := os.ReadFile(editable)
	if err != nil {
		t.Fatalf("read editable file: %v", err)
	}
	if got := string(content); got != "updated" {
		t.Fatalf("editable content = %q, want updated", got)
	}
}

func TestBuildSandboxProfile_BlocksWriteInReadOnlyScope(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox profile only enforced on darwin")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	workDir, _, readOnly, _ := makeSandboxFixture(t)
	profile, err := buildSandboxProfile(workDir, execenv.PermissionSnapshot{
		AllowedPaths:  []string{"repo/**"},
		ReadOnlyPaths: []string{"repo/docs/**"},
	})
	if err != nil {
		t.Fatalf("buildSandboxProfile failed: %v", err)
	}

	if err := runSandboxedSh(profile, `printf denied > "$1"`, readOnly); err == nil {
		t.Fatal("expected read-only write to fail")
	}
}

func TestBuildSandboxProfile_BlocksWriteInBlockedScope(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox profile only enforced on darwin")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	workDir, _, _, blocked := makeSandboxFixture(t)
	profile, err := buildSandboxProfile(workDir, execenv.PermissionSnapshot{
		AllowedPaths: []string{"repo/**"},
		BlockedPaths: []string{"repo/blocked/**"},
	})
	if err != nil {
		t.Fatalf("buildSandboxProfile failed: %v", err)
	}

	if err := runSandboxedSh(profile, `printf denied > "$1"`, blocked); err == nil {
		t.Fatal("expected blocked write to fail")
	}
}

func TestBuildSandboxProfile_BlocksWriteOutsideWorkDirWhenAllowedPathsEmpty(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox profile only enforced on darwin")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	workDir, editable, _, blocked := makeSandboxFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWriteSandboxFile(t, outside, "seed")
	profile, err := buildSandboxProfile(workDir, execenv.PermissionSnapshot{
		BlockedPaths: []string{"repo/blocked/**"},
	})
	if err != nil {
		t.Fatalf("buildSandboxProfile failed: %v", err)
	}

	if err := runSandboxedSh(profile, `printf updated > "$1"`, editable); err != nil {
		t.Fatalf("expected non-blocked workdir write to succeed: %v", err)
	}
	if err := runSandboxedSh(profile, `printf outside > "$1"`, outside); err == nil {
		t.Fatal("expected outside workdir write to fail when allowed_paths empty")
	}
	if err := runSandboxedSh(profile, `printf denied > "$1"`, blocked); err == nil {
		t.Fatal("expected blocked write to fail")
	}
}

func TestBuildSandboxProfile_AllowsWriteInDottedDirectoryScope(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox profile only enforced on darwin")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	workDir := t.TempDir()
	target := filepath.Join(workDir, "repo", "config.d", "settings.json")
	mustWriteSandboxFile(t, target, "seed")
	profile, err := buildSandboxProfile(workDir, execenv.PermissionSnapshot{
		AllowedPaths: []string{"repo/config.d"},
	})
	if err != nil {
		t.Fatalf("buildSandboxProfile failed: %v", err)
	}

	if err := runSandboxedSh(profile, `printf updated > "$1"`, target); err != nil {
		t.Fatalf("expected dotted directory write to succeed: %v", err)
	}
}

func TestBuildSandboxProfile_BlocksChmodInAllowedScope(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox profile only enforced on darwin")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	workDir, editable, _, _ := makeSandboxFixture(t)
	profile, err := buildSandboxProfile(workDir, execenv.PermissionSnapshot{
		AllowedPaths: []string{"repo/**"},
	})
	if err != nil {
		t.Fatalf("buildSandboxProfile failed: %v", err)
	}

	if err := runSandboxed(profile, "/bin/chmod", "777", editable); err == nil {
		t.Fatal("expected chmod to fail inside allowed scope")
	}
}

func TestBuildSandboxProfile_AllowsAtomicReplaceInAllowedScope(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox profile only enforced on darwin")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	workDir, editable, _, _ := makeSandboxFixture(t)
	profile, err := buildSandboxProfile(workDir, execenv.PermissionSnapshot{
		AllowedPaths: []string{"repo/**"},
	})
	if err != nil {
		t.Fatalf("buildSandboxProfile failed: %v", err)
	}

	py := `import os, sys, tempfile
p = sys.argv[1]
fd, tmp = tempfile.mkstemp(dir=os.path.dirname(p))
os.write(fd, b"atomic")
os.close(fd)
os.replace(tmp, p)
`
	if err := runSandboxed(profile, "/usr/bin/python3", "-c", py, editable); err != nil {
		t.Fatalf("expected atomic replace to succeed: %v", err)
	}
	content, err := os.ReadFile(editable)
	if err != nil {
		t.Fatalf("read editable file: %v", err)
	}
	if got := string(content); got != "atomic" {
		t.Fatalf("editable content = %q, want atomic", got)
	}
}

func TestBuildSandboxProfile_AllowsAtomicReplaceForSingleAllowedFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox profile only enforced on darwin")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	workDir, editable, _, _ := makeSandboxFixture(t)
	sibling := filepath.Join(filepath.Dir(editable), "sibling.txt")
	mustWriteSandboxFile(t, sibling, "seed")
	profile, err := buildSandboxProfile(workDir, execenv.PermissionSnapshot{
		AllowedPaths: []string{"repo/editable.txt"},
	})
	if err != nil {
		t.Fatalf("buildSandboxProfile failed: %v", err)
	}

	py := `import os, sys
p = sys.argv[1]
tmp = p + ".tmp"
with open(tmp, "wb") as f:
    f.write(b"atomic")
os.replace(tmp, p)
`
	if err := runSandboxed(profile, "/usr/bin/python3", "-c", py, editable); err != nil {
		t.Fatalf("expected single-file atomic replace to succeed: %v", err)
	}
	if err := runSandboxedSh(profile, `printf denied > "$1"`, sibling); err == nil {
		t.Fatal("expected sibling write outside single-file scope to fail")
	}
}

func makeSandboxFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	workDir := t.TempDir()
	editable := filepath.Join(workDir, "repo", "editable.txt")
	readOnly := filepath.Join(workDir, "repo", "docs", "guide.md")
	blocked := filepath.Join(workDir, "repo", "blocked", "secret.txt")
	for _, path := range []string{editable, readOnly, blocked} {
		mustWriteSandboxFile(t, path, "seed")
	}
	return workDir, editable, readOnly, blocked
}

func mustWriteSandboxFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runSandboxedSh(profile, script string, args ...string) error {
	cmdArgs := append([]string{"-c", script, "sandbox-sh"}, args...)
	return runSandboxed(profile, "/bin/sh", cmdArgs...)
}

func runSandboxed(profile, command string, args ...string) error {
	wrapped := append([]string{"-p", profile, command}, args...)
	cmd := exec.CommandContext(context.Background(), "sandbox-exec", wrapped...)
	return cmd.Run()
}
