package usage

import (
	"log/slog"
	"path/filepath"
	"strings"
)

// Record represents aggregated token usage for one (date, provider, model) tuple.
type Record struct {
	Date             string `json:"date"`         // "2006-01-02"
	WorkspaceID      string `json:"workspace_id"` // daemon workspace inferred from log path or execution context
	Provider         string `json:"provider"`     // "claude" or "codex"
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

// Scanner scans local CLI log files for token usage data.
type Scanner struct {
	logger *slog.Logger
}

// NewScanner creates a new usage scanner.
func NewScanner(logger *slog.Logger) *Scanner {
	return &Scanner{logger: logger}
}

// Scan reads local JSONL log files for both Claude Code and Codex CLI,
// and returns aggregated usage records keyed by (date, provider, model).
func (s *Scanner) Scan() []Record {
	var records []Record

	claudeRecords := s.scanClaude()
	records = append(records, claudeRecords...)

	codexRecords := s.scanCodex()
	records = append(records, codexRecords...)

	return records
}

// aggregation key for merging records.
type aggKey struct {
	Date        string
	WorkspaceID string
	Provider    string
	Model       string
}

func mergeRecords(records []Record) []Record {
	m := make(map[aggKey]*Record)
	for _, r := range records {
		k := aggKey{Date: r.Date, WorkspaceID: r.WorkspaceID, Provider: r.Provider, Model: r.Model}
		if existing, ok := m[k]; ok {
			existing.InputTokens += r.InputTokens
			existing.OutputTokens += r.OutputTokens
			existing.CacheReadTokens += r.CacheReadTokens
			existing.CacheWriteTokens += r.CacheWriteTokens
		} else {
			copy := r
			m[k] = &copy
		}
	}
	result := make([]Record, 0, len(m))
	for _, r := range m {
		result = append(result, *r)
	}
	return result
}

func workspaceIDFromPath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		if strings.HasPrefix(part, "ws-") || strings.HasPrefix(part, "workspace-") || isLikelyUUID(part) {
			return part
		}
	}
	return ""
}

func isLikelyUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, ch := range s {
		switch i {
		case 8, 13, 18, 23:
			if ch != '-' {
				return false
			}
		default:
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				return false
			}
		}
	}
	return true
}
