package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type capturedWorkflowRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

func resetCommandForTest(cmd *cobra.Command) {
	cmd.SetArgs(nil)
	resetFlagSetForTest(cmd.Flags())
	resetFlagSetForTest(cmd.PersistentFlags())
	for _, child := range cmd.Commands() {
		resetCommandForTest(child)
	}
}

func resetFlagSetForTest(flags *pflag.FlagSet) {
	flags.VisitAll(func(flag *pflag.Flag) {
		flag.Changed = false
		switch flag.Value.Type() {
		case "stringSlice":
			_ = flag.Value.Set("")
		default:
			_ = flag.Value.Set(flag.DefValue)
		}
	})
}

func TestIssueWorkflowCommandsSendExpectedRequests(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantPath string
		wantBody map[string]any
	}{
		{
			name:     "submit review",
			args:     []string{"--server-url", "SERVER_URL", "issue", "submit-review", "issue-123", "--idempotency-key", "submit-cli-1", "--summary", "ready for review", "--pr-url", "https://example.invalid/pr/123", "--output", "json"},
			wantPath: "/api/issues/issue-123/workflow/submit-review",
			wantBody: map[string]any{
				"idempotency_key": "submit-cli-1",
				"summary":         "ready for review",
				"evidence": map[string]any{
					"pr_url": "https://example.invalid/pr/123",
				},
			},
		},
		{
			name:     "review",
			args:     []string{"--server-url", "SERVER_URL", "issue", "review", "issue-123", "--idempotency-key", "review-cli-1", "--review-round-id", "round-123", "--verdict", "approved", "--summary", "verified", "--criterion", "criterion-1:pass:looks good", "--criterion", "criterion-2:not_applicable", "--output", "json"},
			wantPath: "/api/issues/issue-123/workflow/review",
			wantBody: map[string]any{
				"idempotency_key": "review-cli-1",
				"review_round_id": "round-123",
				"verdict":         "approved",
				"summary":         "verified",
				"criterion_results": []any{
					map[string]any{"criterion_id": "criterion-1", "result": "pass", "note": "looks good"},
					map[string]any{"criterion_id": "criterion-2", "result": "not_applicable"},
				},
			},
		},
		{
			name:     "report blocked",
			args:     []string{"--server-url", "SERVER_URL", "issue", "report-blocked", "issue-123", "--reason", "waiting on API access", "--output", "json"},
			wantPath: "/api/issues/issue-123/workflow/report-blocked",
			wantBody: map[string]any{"reason": "waiting on API access"},
		},
		{
			name:     "replan",
			args:     []string{"--server-url", "SERVER_URL", "issue", "replan", "issue-123", "--idempotency-key", "replan-cli-1", "--reason", "scope changed", "--plan-content", "updated plan", "--output", "json"},
			wantPath: "/api/issues/issue-123/workflow/replan",
			wantBody: map[string]any{"idempotency_key": "replan-cli-1", "reason": "scope changed", "plan_content": "updated plan"},
		},
		{
			name:     "finalize",
			args:     []string{"--server-url", "SERVER_URL", "issue", "finalize", "issue-123", "--output", "json"},
			wantPath: "/api/issues/issue-123/workflow/finalize-parent",
			wantBody: map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured capturedWorkflowRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured.Method = r.Method
				captured.Path = r.URL.Path
				if err := json.NewDecoder(r.Body).Decode(&captured.Body); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "issue-123", "status": "in_review"})
			}))
			defer srv.Close()

			args := append([]string(nil), tt.args...)
			for i := range args {
				if args[i] == "SERVER_URL" {
					args[i] = srv.URL
				}
			}

			resetCommandForTest(rootCmd)
			rootCmd.SetArgs(args)
			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("execute command: %v", err)
			}

			if captured.Method != http.MethodPost {
				t.Fatalf("method = %q, want %q", captured.Method, http.MethodPost)
			}
			if captured.Path != tt.wantPath {
				t.Fatalf("path = %q, want %q", captured.Path, tt.wantPath)
			}
			if !reflect.DeepEqual(captured.Body, tt.wantBody) {
				t.Fatalf("body = %#v, want %#v", captured.Body, tt.wantBody)
			}
		})
	}
}

func TestIssueReviewCommandAcceptsBlockedVerdict(t *testing.T) {
	resetCommandForTest(rootCmd)
	var captured capturedWorkflowRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Method = r.Method
		captured.Path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&captured.Body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "issue-123", "status": "blocked"})
	}))
	defer srv.Close()

	rootCmd.SetArgs([]string{"--server-url", srv.URL, "issue", "review", "issue-123", "--review-round-id", "round-123", "--verdict", "blocked", "--summary", "waiting on dependency", "--output", "json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute command: %v", err)
	}
	if captured.Path != "/api/issues/issue-123/workflow/review" {
		t.Fatalf("path = %q", captured.Path)
	}
	if got := captured.Body["verdict"]; got != "blocked" {
		t.Fatalf("verdict = %#v, want blocked", got)
	}
}

func TestIssueReviewCommandRequiresReviewRoundID(t *testing.T) {
	resetCommandForTest(rootCmd)
	rootCmd.SetArgs([]string{"issue", "review", "issue-123", "--verdict", "approved"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected missing review_round_id to fail")
	}
	if got := err.Error(); got != "--review-round-id is required" {
		t.Fatalf("error = %q", got)
	}
}

func TestIssueReviewCommandRejectsInvalidCriterionResult(t *testing.T) {
	resetCommandForTest(rootCmd)
	rootCmd.SetArgs([]string{"--server-url", "http://example.invalid", "issue", "review", "issue-123", "--review-round-id", "round-123", "--verdict", "approved", "--criterion", "criterion-1:passed"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected invalid criterion result to fail")
	}
	if got := err.Error(); got != "invalid criterion result \"passed\"; valid values: pass, fail, not_applicable" {
		t.Fatalf("error = %q", got)
	}
}
