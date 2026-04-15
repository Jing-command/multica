package execenv

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
)

// PermissionSnapshot captures the worker's enforced file and tool boundaries.
type PermissionSnapshot struct {
	AllowedPaths  []string `json:"allowed_paths,omitempty"`
	ReadOnlyPaths []string `json:"read_only_paths,omitempty"`
	BlockedPaths  []string `json:"blocked_paths,omitempty"`
	AllowedTools  []string `json:"allowed_tools,omitempty"`
}

// ValidateWritePath verifies that a write target is allowed by the snapshot.
func ValidateWritePath(path string, snapshot PermissionSnapshot) error {
	normalized, err := normalizePermissionPath(path)
	if err != nil {
		return err
	}

	if matchesPermissionScope(normalized, snapshot.BlockedPaths) {
		return fmt.Errorf("write path %q is blocked", path)
	}
	if matchesPermissionScope(normalized, snapshot.ReadOnlyPaths) {
		return fmt.Errorf("write path %q is read-only", path)
	}
	if len(snapshot.AllowedPaths) == 0 {
		return nil
	}
	if !matchesPermissionScope(normalized, snapshot.AllowedPaths) {
		return fmt.Errorf("write path %q is outside allowed write scope", path)
	}
	return nil
}

// ValidateDiffPaths verifies that every diff path stays within write scope.
func ValidateDiffPaths(paths []string, snapshot PermissionSnapshot) error {
	for _, path := range paths {
		if err := ValidateWritePath(path, snapshot); err != nil {
			return err
		}
	}
	return nil
}

// ParsePermissionSnapshotJSON validates and decodes a permission snapshot payload.
func ParsePermissionSnapshotJSON(data []byte) (PermissionSnapshot, error) {
	if !json.Valid(data) {
		return PermissionSnapshot{}, fmt.Errorf("permission snapshot is invalid JSON")
	}

	var snapshot PermissionSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return PermissionSnapshot{}, fmt.Errorf("decode permission snapshot: %w", err)
	}
	return snapshot, nil
}

// LoadPermissionSnapshot searches startDir and its parents for .agent_context/permission_snapshot.json.
func LoadPermissionSnapshot(startDir string) (PermissionSnapshot, bool, error) {
	snapshotPath, err := findPermissionSnapshotPath(startDir)
	if err != nil {
		return PermissionSnapshot{}, false, err
	}
	if snapshotPath == "" {
		return PermissionSnapshot{}, false, nil
	}

	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		return PermissionSnapshot{}, false, fmt.Errorf("read permission snapshot: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return PermissionSnapshot{}, false, fmt.Errorf("permission snapshot is empty")
	}

	snapshot, err := ParsePermissionSnapshotJSON(data)
	if err != nil {
		return PermissionSnapshot{}, false, err
	}
	return snapshot, true, nil
}

// ApplyPermissionSnapshotToWorktree turns a checked-out worktree into a physically constrained tree.
// If AllowedPaths is non-empty, paths outside that scope become read-only. ReadOnlyPaths and
// BlockedPaths always override allowed scopes and remain read-only.
func ApplyPermissionSnapshotToWorktree(worktreePath string, snapshot PermissionSnapshot) error {
	if snapshot.isEmpty() {
		return nil
	}

	if len(snapshot.AllowedPaths) > 0 {
		if err := setTreeWritableState(worktreePath, false); err != nil {
			return err
		}
		if err := applyPermissionScopes(worktreePath, snapshot.AllowedPaths, true); err != nil {
			return err
		}
	}
	if err := applyPermissionScopes(worktreePath, snapshot.ReadOnlyPaths, false); err != nil {
		return err
	}
	if err := applyPermissionScopes(worktreePath, snapshot.BlockedPaths, false); err != nil {
		return err
	}
	return nil
}

func (s PermissionSnapshot) isEmpty() bool {
	return len(s.AllowedPaths) == 0 && len(s.ReadOnlyPaths) == 0 && len(s.BlockedPaths) == 0
}

func matchesPermissionScope(path string, scopes []string) bool {
	for _, scope := range scopes {
		normalizedScope, recursive, err := normalizePermissionScope(scope)
		if err != nil {
			continue
		}
		if normalizedScope == "" {
			continue
		}
		if recursive {
			if path == normalizedScope || strings.HasPrefix(path, normalizedScope+"/") {
				return true
			}
			continue
		}
		if strings.ContainsAny(normalizedScope, "*?[") {
			ok, matchErr := pathpkg.Match(normalizedScope, path)
			if matchErr == nil && ok {
				return true
			}
			continue
		}
		if path == normalizedScope || strings.HasPrefix(path, normalizedScope+"/") {
			return true
		}
	}
	return false
}

func normalizePermissionPath(path string) (string, error) {
	cleaned, err := normalizePermissionPattern(path)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(cleaned, "*?[") {
		return "", fmt.Errorf("permission path %q cannot contain glob characters", path)
	}
	return cleaned, nil
}

func normalizePermissionScope(scope string) (string, bool, error) {
	cleaned, err := normalizePermissionPattern(scope)
	if err != nil {
		return "", false, err
	}
	if strings.HasSuffix(cleaned, "/**") {
		trimmed := strings.TrimSuffix(cleaned, "/**")
		trimmed = strings.TrimSuffix(trimmed, "/")
		if trimmed == "" {
			return "", false, fmt.Errorf("permission scope %q is invalid", scope)
		}
		return trimmed, true, nil
	}
	return cleaned, false, nil
}

func normalizePermissionPattern(pattern string) (string, error) {
	cleaned := pathpkg.Clean(strings.TrimSpace(pattern))
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("permission path is required")
	}
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("permission path %q is outside workdir", pattern)
	}
	return strings.TrimPrefix(cleaned, "./"), nil
}

func applyPermissionScopes(worktreePath string, scopes []string, writable bool) error {
	for _, scope := range scopes {
		targets, err := resolvePermissionTargets(worktreePath, scope)
		if err != nil {
			return err
		}
		for _, target := range targets {
			if err := setTreeWritableState(target, writable); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolvePermissionTargets(worktreePath, scope string) ([]string, error) {
	normalizedScope, recursive, err := normalizePermissionScope(scope)
	if err != nil {
		return nil, err
	}
	if normalizedScope == "" {
		return nil, nil
	}

	if strings.ContainsAny(normalizedScope, "*?[") {
		globPattern, err := joinPermissionPattern(worktreePath, normalizedScope)
		if err != nil {
			return nil, err
		}
		matches, err := filepath.Glob(globPattern)
		if err != nil {
			return nil, fmt.Errorf("glob permission scope %q: %w", scope, err)
		}
		return uniquePermissionTargets(matches), nil
	}

	target, err := joinPermissionTarget(worktreePath, normalizedScope)
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Lstat(target); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat permission target %q: %w", scope, statErr)
	}
	if recursive {
		return []string{target}, nil
	}
	return []string{target}, nil
}

func joinPermissionTarget(worktreePath, rel string) (string, error) {
	normalized, err := normalizePermissionPath(rel)
	if err != nil {
		return "", err
	}
	target := filepath.Join(worktreePath, filepath.FromSlash(normalized))
	return ensureWithinRoot(worktreePath, target)
}

func joinPermissionPattern(worktreePath, pattern string) (string, error) {
	normalized, err := normalizePermissionPattern(pattern)
	if err != nil {
		return "", err
	}
	prefix := normalized
	if idx := strings.IndexAny(prefix, "*?["); idx >= 0 {
		prefix = prefix[:idx]
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix != "" {
		if _, err := joinPermissionTarget(worktreePath, prefix); err != nil {
			return "", err
		}
	}
	return filepath.Join(worktreePath, filepath.FromSlash(normalized)), nil
}

func ensureWithinRoot(root, target string) (string, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("resolve permission target: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("permission target %q escapes workdir", target)
	}
	return target, nil
}

func uniquePermissionTargets(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		result = append(result, p)
	}
	return result
}

func setTreeWritableState(root string, writable bool) error {
	if _, err := os.Lstat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat permission root: %w", err)
	}

	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		mode := info.Mode().Perm()
		if writable {
			mode |= 0o200
		} else {
			mode &^= 0o222
		}
		if chmodErr := os.Chmod(path, mode); chmodErr != nil {
			return fmt.Errorf("chmod %s: %w", path, chmodErr)
		}
		return nil
	})
}

func findPermissionSnapshotPath(startDir string) (string, error) {
	current := startDir
	for {
		candidate := filepath.Join(current, ".agent_context", "permission_snapshot.json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("stat permission snapshot: %w", err)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", nil
		}
		current = parent
	}
}
