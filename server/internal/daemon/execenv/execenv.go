// Package execenv manages isolated per-task execution environments for the daemon.
// Each task gets its own directory with injected context files. Repositories are
// checked out on demand by the agent via `multica repo checkout`.
package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// RepoContextForEnv describes a workspace repo available for checkout.
type RepoContextForEnv struct {
	URL         string // remote URL
	Description string // human-readable description
}

// PrepareParams holds all inputs needed to set up an execution environment.
type PrepareParams struct {
	WorkspacesRoot string            // base path for all envs (e.g., ~/multica_workspaces)
	WorkspaceID    string            // workspace UUID — tasks are grouped under this
	TaskID         string            // task UUID — used for directory name
	AgentName      string            // for git branch naming only
	Provider       string            // agent provider ("claude", "codex") — determines skill injection paths
	Task           TaskContextForEnv // context data for writing files
}

// TaskContextForEnv is the subset of task context used for writing context files.
type TaskContextForEnv struct {
	IssueID                string
	TriggerCommentID       string // comment that triggered this task (empty for on_assign)
	AgentName              string
	AgentInstructions      string // agent identity/persona instructions, injected into CLAUDE.md
	AgentSkills            []SkillContextForEnv
	Repos                  []RepoContextForEnv // workspace repos available for checkout
	IsOrchestrator         bool                // true for the built-in Orchestrator agent
	PermissionSnapshotJSON []byte
}

// SkillContextForEnv represents a skill to be written into the execution environment.
type SkillContextForEnv struct {
	Name    string
	Content string
	Files   []SkillFileContextForEnv
}

// SkillFileContextForEnv represents a supporting file within a skill.
type SkillFileContextForEnv struct {
	Path    string
	Content string
}

// Environment represents a prepared, isolated execution environment.
type Environment struct {
	// RootDir is the top-level env directory ({workspacesRoot}/{task_id_short}/).
	RootDir string
	// WorkDir is the directory to pass as Cwd to the agent ({RootDir}/workdir/).
	WorkDir string
	// CodexHome is the path to the per-task CODEX_HOME directory (set only for codex provider).
	CodexHome string

	logger *slog.Logger // for cleanup logging
}

// EnforcePermissionSnapshotIfPresent loads the trusted execenv-written permission snapshot
// for env.WorkDir and applies filesystem-level enforcement to the checked out repo root.
// It intentionally runs after checkout/update so execenv never chmods an empty placeholder
// directory and interferes with later git worktree creation.
func (env *Environment) EnforcePermissionSnapshotIfPresent(worktreePath string) error {
	if env == nil {
		return nil
	}
	return EnforcePermissionSnapshotIfPresent(env.WorkDir, worktreePath)
}

// EnforcePermissionSnapshotIfPresent loads the trusted execenv-written permission snapshot
// from workDir/.agent_context/permission_snapshot.json and applies filesystem-level
// enforcement only to worktreePath. Repository contents are never consulted as a snapshot
// source, so a repo-owned .agent_context/permission_snapshot.json cannot override policy.
func EnforcePermissionSnapshotIfPresent(workDir, worktreePath string) error {
	snapshotPath := filepath.Join(workDir, ".agent_context", "permission_snapshot.json")
	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read permission snapshot: %w", err)
	}

	snapshot, err := ParsePermissionSnapshotJSON(data)
	if err != nil {
		return err
	}
	repoSnapshot, err := snapshotForRepo(worktreePath, snapshot)
	if err != nil {
		return err
	}
	return ApplyPermissionSnapshotToWorktree(worktreePath, repoSnapshot)
}

func snapshotForRepo(worktreePath string, snapshot PermissionSnapshot) (PermissionSnapshot, error) {
	repoName := filepath.Base(filepath.Clean(worktreePath))
	if repoName == "." || repoName == string(filepath.Separator) || repoName == "" {
		return PermissionSnapshot{}, fmt.Errorf("resolve repo name from worktree path")
	}

	allowed, err := rebasePermissionScopesForRepo(repoName, snapshot.AllowedPaths)
	if err != nil {
		return PermissionSnapshot{}, err
	}
	readOnly, err := rebasePermissionScopesForRepo(repoName, snapshot.ReadOnlyPaths)
	if err != nil {
		return PermissionSnapshot{}, err
	}
	blocked, err := rebasePermissionScopesForRepo(repoName, snapshot.BlockedPaths)
	if err != nil {
		return PermissionSnapshot{}, err
	}

	if len(snapshot.AllowedPaths) > 0 && len(allowed) == 0 {
		allowed = []string{".multica-no-write-access"}
	}

	return PermissionSnapshot{
		AllowedPaths:  allowed,
		ReadOnlyPaths: readOnly,
		BlockedPaths:  blocked,
		AllowedTools:  append([]string(nil), snapshot.AllowedTools...),
	}, nil
}

func rebasePermissionScopesForRepo(repoName string, scopes []string) ([]string, error) {
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		rebased, ok, err := rebasePermissionScopeForRepo(repoName, scope)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		result = append(result, rebased)
	}
	return result, nil
}

func rebasePermissionScopeForRepo(repoName, scope string) (string, bool, error) {
	normalized, recursive, err := normalizePermissionScope(scope)
	if err != nil {
		return "", false, err
	}
	if normalized == "" {
		return "", false, nil
	}

	if normalized == repoName {
		return ".", true, nil
	}

	prefix := repoName + "/"
	if !strings.HasPrefix(normalized, prefix) {
		return "", false, nil
	}

	rebased := strings.TrimPrefix(normalized, prefix)
	if rebased == "" {
		rebased = "."
	}
	if recursive {
		if rebased == "." {
			return ".", true, nil
		}
		return rebased + "/**", true, nil
	}
	return rebased, true, nil
}

// Prepare creates an isolated execution environment for a task.
// The workdir starts empty (no repo checkouts). The agent checks out repos
// on demand via `multica repo checkout <url>`.
func Prepare(params PrepareParams, logger *slog.Logger) (*Environment, error) {
	if params.WorkspacesRoot == "" {
		return nil, fmt.Errorf("execenv: workspaces root is required")
	}
	if params.WorkspaceID == "" {
		return nil, fmt.Errorf("execenv: workspace ID is required")
	}
	if params.TaskID == "" {
		return nil, fmt.Errorf("execenv: task ID is required")
	}

	envRoot := filepath.Join(params.WorkspacesRoot, params.WorkspaceID, shortID(params.TaskID))

	// Remove existing env if present (defensive — task IDs are unique).
	if _, err := os.Stat(envRoot); err == nil {
		if err := os.RemoveAll(envRoot); err != nil {
			return nil, fmt.Errorf("execenv: remove existing env: %w", err)
		}
	}

	// Create directory tree.
	workDir := filepath.Join(envRoot, "workdir")
	for _, dir := range []string{workDir, filepath.Join(envRoot, "output"), filepath.Join(envRoot, "logs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("execenv: create directory %s: %w", dir, err)
		}
	}

	env := &Environment{
		RootDir: envRoot,
		WorkDir: workDir,
		logger:  logger,
	}

	// Write context files into workdir (skills go to provider-native paths).
	if err := writeContextFiles(workDir, params.Provider, params.Task); err != nil {
		return nil, fmt.Errorf("execenv: write context files: %w", err)
	}

	// For Codex, set up a per-task CODEX_HOME seeded from ~/.codex/ with skills.
	if params.Provider == "codex" {
		codexHome := filepath.Join(envRoot, "codex-home")
		if err := prepareCodexHome(codexHome, logger); err != nil {
			return nil, fmt.Errorf("execenv: prepare codex-home: %w", err)
		}
		if len(params.Task.AgentSkills) > 0 {
			if err := writeSkillFiles(filepath.Join(codexHome, "skills"), params.Task.AgentSkills); err != nil {
				return nil, fmt.Errorf("execenv: write codex skills: %w", err)
			}
		}
		env.CodexHome = codexHome
	}

	logger.Info("execenv: prepared env", "root", envRoot, "repos_available", len(params.Task.Repos))
	return env, nil
}

// Reuse wraps an existing workdir into an Environment and refreshes context files.
// Returns nil if the workdir does not exist (caller should fall back to Prepare).
func Reuse(workDir, provider string, task TaskContextForEnv, logger *slog.Logger) *Environment {
	if _, err := os.Stat(workDir); err != nil {
		return nil
	}

	env := &Environment{
		RootDir: filepath.Dir(workDir),
		WorkDir: workDir,
		logger:  logger,
	}

	// Refresh context files (issue_context.md, skills).
	if err := writeContextFiles(workDir, provider, task); err != nil {
		logger.Warn("execenv: refresh context files failed", "error", err)
	}

	logger.Info("execenv: reusing env", "workdir", workDir)
	return env
}

// Cleanup tears down the execution environment.
// If removeAll is true, the entire env root is deleted. Otherwise, workdir is
// removed but output/ and logs/ are preserved for debugging.
func (env *Environment) Cleanup(removeAll bool) error {
	if env == nil {
		return nil
	}

	if err := MakeTreeWritable(env.WorkDir); err != nil && !os.IsNotExist(err) {
		env.logger.Warn("execenv: cleanup unlock workdir failed", "error", err)
		return err
	}

	if removeAll {
		if err := os.RemoveAll(env.RootDir); err != nil {
			env.logger.Warn("execenv: cleanup removeAll failed", "error", err)
			return err
		}
		return nil
	}

	// Partial cleanup: remove workdir, keep output/ and logs/.
	if err := os.RemoveAll(env.WorkDir); err != nil {
		env.logger.Warn("execenv: cleanup workdir failed", "error", err)
		return err
	}
	return nil
}

// MakeTreeWritable removes filesystem-level permission enforcement under root.
func MakeTreeWritable(root string) error {
	return setTreeWritableState(root, true)
}

// MakeTreeReadOnly applies a fail-closed read-only lock under root.
func MakeTreeReadOnly(root string) error {
	return setTreeWritableState(root, false)
}
