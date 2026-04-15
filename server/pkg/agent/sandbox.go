package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

const sandboxWriteProfile = "file-write-data file-write-create file-write-unlink"

type sandboxAllowRule struct {
	operations string
	expression string
}

func wrapCommandWithSandbox(command string, args []string, opts ExecOptions) (string, []string, error) {
	if len(opts.PermissionSnapshotJSON) == 0 || runtime.GOOS != "darwin" {
		return command, args, nil
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return "", nil, fmt.Errorf("sandbox-exec not available: %w", err)
	}
	if opts.Cwd == "" {
		return "", nil, fmt.Errorf("sandbox requires cwd")
	}

	snapshot, err := execenv.ParsePermissionSnapshotJSON(opts.PermissionSnapshotJSON)
	if err != nil {
		return "", nil, err
	}
	profile, err := buildSandboxProfile(opts.Cwd, snapshot)
	if err != nil {
		return "", nil, err
	}
	wrapped := make([]string, 0, len(args)+3)
	wrapped = append(wrapped, "-p", profile, command)
	wrapped = append(wrapped, args...)
	return "sandbox-exec", wrapped, nil
}

func buildSandboxProfile(workDir string, snapshot execenv.PermissionSnapshot) (string, error) {
	root, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox workdir: %w", err)
	}

	denyExprs, err := sandboxExpressions(root, append(snapshot.ReadOnlyPaths, snapshot.BlockedPaths...))
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")

	b.WriteString("(deny file-write*)\n")
	if len(snapshot.AllowedPaths) == 0 {
		b.WriteString("(allow ")
		b.WriteString(sandboxWriteProfile)
		b.WriteString(" ")
		b.WriteString(applySandboxExclusions(fmt.Sprintf("(subpath %q)", root), denyExprs))
		b.WriteString(")\n")
		return b.String(), nil
	}

	allowRules, err := sandboxAllowRules(root, snapshot.AllowedPaths)
	if err != nil {
		return "", err
	}
	for _, rule := range allowRules {
		b.WriteString("(allow ")
		b.WriteString(rule.operations)
		b.WriteString(" ")
		b.WriteString(applySandboxExclusions(rule.expression, denyExprs))
		b.WriteString(")\n")
	}

	return b.String(), nil
}

func sandboxAllowRules(root string, scopes []string) ([]sandboxAllowRule, error) {
	rules := make([]sandboxAllowRule, 0, len(scopes))
	for _, scope := range scopes {
		ruleSet, err := sandboxAllowRulesForScope(root, scope)
		if err != nil {
			return nil, err
		}
		rules = append(rules, ruleSet...)
	}
	return rules, nil
}

func sandboxAllowRulesForScope(root, scope string) ([]sandboxAllowRule, error) {
	cleaned := filepath.ToSlash(filepath.Clean(strings.TrimSpace(scope)))
	if cleaned == "." || cleaned == "" {
		return nil, fmt.Errorf("permission path is required")
	}
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." || strings.HasPrefix(cleaned, "/") {
		return nil, fmt.Errorf("permission path %q is outside workdir", scope)
	}
	cleaned = strings.TrimPrefix(cleaned, "./")

	if strings.HasSuffix(cleaned, "/**") {
		base := strings.TrimSuffix(strings.TrimSuffix(cleaned, "/**"), "/")
		if base == "" {
			return nil, fmt.Errorf("permission scope %q is invalid", scope)
		}
		return []sandboxAllowRule{{
			operations: sandboxWriteProfile,
			expression: fmt.Sprintf("(subpath %q)", filepath.Join(root, filepath.FromSlash(base))),
		}}, nil
	}
	if strings.ContainsAny(cleaned, "*?[") {
		return nil, fmt.Errorf("sandbox does not support glob permission scope %q", scope)
	}

	abs := filepath.Join(root, filepath.FromSlash(cleaned))
	if info, err := os.Stat(abs); err == nil {
		if info.IsDir() {
			return []sandboxAllowRule{{
				operations: sandboxWriteProfile,
				expression: fmt.Sprintf("(subpath %q)", abs),
			}}, nil
		}
		return []sandboxAllowRule{{
			operations: sandboxWriteProfile,
			expression: fmt.Sprintf("(regex #\"%s(\\.tmp)?$\")", regexp.QuoteMeta(abs)),
		}}, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat sandbox scope %q: %w", scope, err)
	}

	return []sandboxAllowRule{{
		operations: sandboxWriteProfile,
		expression: fmt.Sprintf("(subpath %q)", abs),
	}}, nil
}

func sandboxExpressions(root string, scopes []string) ([]string, error) {
	exprs := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		expr, err := sandboxScopeExpression(root, scope)
		if err != nil {
			return nil, err
		}
		if expr == "" {
			continue
		}
		exprs = append(exprs, expr)
	}
	return exprs, nil
}

func sandboxScopeExpression(root, scope string) (string, error) {
	cleaned := filepath.ToSlash(filepath.Clean(strings.TrimSpace(scope)))
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("permission path is required")
	}
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("permission path %q is outside workdir", scope)
	}
	cleaned = strings.TrimPrefix(cleaned, "./")
	if strings.HasSuffix(cleaned, "/**") {
		cleaned = strings.TrimSuffix(strings.TrimSuffix(cleaned, "/**"), "/")
		if cleaned == "" {
			return "", fmt.Errorf("permission scope %q is invalid", scope)
		}
		return fmt.Sprintf("(subpath %q)", filepath.Join(root, filepath.FromSlash(cleaned))), nil
	}
	if strings.ContainsAny(cleaned, "*?[") {
		return "", fmt.Errorf("sandbox does not support glob permission scope %q", scope)
	}
	abs := filepath.Join(root, filepath.FromSlash(cleaned))
	if info, err := os.Stat(abs); err == nil {
		if !info.IsDir() {
			return fmt.Sprintf("(literal %q)", abs), nil
		}
		return fmt.Sprintf("(subpath %q)", abs), nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat sandbox scope %q: %w", scope, err)
	}
	return fmt.Sprintf("(subpath %q)", abs), nil
}

func applySandboxExclusions(base string, exclusions []string) string {
	if len(exclusions) == 0 {
		return base
	}
	var b strings.Builder
	b.WriteString("(require-all ")
	b.WriteString(base)
	for _, exclusion := range exclusions {
		b.WriteString(" (require-not ")
		b.WriteString(exclusion)
		b.WriteString(")")
	}
	b.WriteString(")")
	return b.String()
}
