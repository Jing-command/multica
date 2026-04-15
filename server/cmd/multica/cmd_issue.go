package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var issueCmd = &cobra.Command{
	Use:   "issue",
	Short: "Work with issues",
}

var issueListCmd = &cobra.Command{
	Use:   "list",
	Short: "List issues in the workspace",
	RunE:  runIssueList,
}

var issueGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get issue details",
	Args:  exactArgs(1),
	RunE:  runIssueGet,
}

var issueCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new issue",
	RunE:  runIssueCreate,
}

var issueUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an issue",
	Args:  exactArgs(1),
	RunE:  runIssueUpdate,
}

var issueAssignCmd = &cobra.Command{
	Use:   "assign <id>",
	Short: "Assign an issue to a member or agent",
	Args:  exactArgs(1),
	RunE:  runIssueAssign,
}

var issueStatusCmd = &cobra.Command{
	Use:   "status <id> <status>",
	Short: "Change issue status",
	Args:  exactArgs(2),
	RunE:  runIssueStatus,
}

// Comment subcommands.

var issueCommentCmd = &cobra.Command{
	Use:   "comment",
	Short: "Work with issue comments",
}

var issueCommentListCmd = &cobra.Command{
	Use:   "list <issue-id>",
	Short: "List comments on an issue",
	Args:  exactArgs(1),
	RunE:  runIssueCommentList,
}

var issueCommentAddCmd = &cobra.Command{
	Use:   "add <issue-id>",
	Short: "Add a comment to an issue",
	Args:  exactArgs(1),
	RunE:  runIssueCommentAdd,
}

var issueCommentDeleteCmd = &cobra.Command{
	Use:   "delete <comment-id>",
	Short: "Delete a comment",
	Args:  exactArgs(1),
	RunE:  runIssueCommentDelete,
}

// Execution history subcommands.

var issueRunsCmd = &cobra.Command{
	Use:   "runs <issue-id>",
	Short: "List execution history for an issue",
	Args:  exactArgs(1),
	RunE:  runIssueRuns,
}

var issueRunMessagesCmd = &cobra.Command{
	Use:   "run-messages <task-id>",
	Short: "List messages for an execution",
	Args:  exactArgs(1),
	RunE:  runIssueRunMessages,
}

var issueChildrenCmd = &cobra.Command{
	Use:   "children <id>",
	Short: "List child issues of a parent issue",
	Args:  exactArgs(1),
	RunE:  runIssueChildren,
}

var issueSubmitReviewCmd = &cobra.Command{
	Use:   "submit-review <issue-id>",
	Short: "Submit an issue for workflow review",
	Args:  exactArgs(1),
	RunE:  runIssueSubmitReview,
}

var issueReviewCmd = &cobra.Command{
	Use:   "review <issue-id>",
	Short: "Review an issue workflow decision",
	Args:  exactArgs(1),
	RunE:  runIssueReview,
}

var issueReportBlockedCmd = &cobra.Command{
	Use:   "report-blocked <issue-id>",
	Short: "Report an issue as blocked in workflow",
	Args:  exactArgs(1),
	RunE:  runIssueReportBlocked,
}

var issueReplanCmd = &cobra.Command{
	Use:   "replan <issue-id>",
	Short: "Request a workflow replan for an issue",
	Args:  exactArgs(1),
	RunE:  runIssueReplan,
}

var issueFinalizeCmd = &cobra.Command{
	Use:   "finalize <issue-id>",
	Short: "Finalize a parent issue workflow",
	Args:  exactArgs(1),
	RunE:  runIssueFinalize,
}

var issueSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search issues by title or description",
	Args:  cobra.ExactArgs(1),
	RunE:  runIssueSearch,
}

var validIssueStatuses = []string{
	"backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled",
}

var validReviewDecisions = []string{
	"approved", "changes_requested", "blocked",
}

var validCriterionResults = []string{
	"pass", "fail", "not_applicable",
}

func init() {
	issueCmd.AddCommand(issueListCmd)
	issueCmd.AddCommand(issueGetCmd)
	issueCmd.AddCommand(issueCreateCmd)
	issueCmd.AddCommand(issueUpdateCmd)
	issueCmd.AddCommand(issueAssignCmd)
	issueCmd.AddCommand(issueStatusCmd)
	issueCmd.AddCommand(issueCommentCmd)
	issueCmd.AddCommand(issueRunsCmd)
	issueCmd.AddCommand(issueRunMessagesCmd)
	issueCmd.AddCommand(issueChildrenCmd)
	issueCmd.AddCommand(issueSubmitReviewCmd)
	issueCmd.AddCommand(issueReviewCmd)
	issueCmd.AddCommand(issueReportBlockedCmd)
	issueCmd.AddCommand(issueReplanCmd)
	issueCmd.AddCommand(issueFinalizeCmd)
	issueCmd.AddCommand(issueSearchCmd)

	issueCommentCmd.AddCommand(issueCommentListCmd)
	issueCommentCmd.AddCommand(issueCommentAddCmd)
	issueCommentCmd.AddCommand(issueCommentDeleteCmd)

	// issue list
	issueListCmd.Flags().String("output", "table", "Output format: table or json")
	issueListCmd.Flags().String("status", "", "Filter by status")
	issueListCmd.Flags().String("priority", "", "Filter by priority")
	issueListCmd.Flags().String("assignee", "", "Filter by assignee name")
	issueListCmd.Flags().Int("limit", 50, "Maximum number of issues to return")

	// issue get
	issueGetCmd.Flags().String("output", "json", "Output format: table or json")

	// issue create
	issueCreateCmd.Flags().String("title", "", "Issue title (required)")
	issueCreateCmd.Flags().String("description", "", "Issue description")
	issueCreateCmd.Flags().String("status", "", "Issue status")
	issueCreateCmd.Flags().String("priority", "", "Issue priority")
	issueCreateCmd.Flags().String("assignee", "", "Assignee name (member or agent)")
	issueCreateCmd.Flags().String("parent", "", "Parent issue ID")
	issueCreateCmd.Flags().String("due-date", "", "Due date (RFC3339 format)")
	issueCreateCmd.Flags().String("output", "json", "Output format: table or json")
	issueCreateCmd.Flags().StringSlice("attachment", nil, "File path(s) to attach (can be specified multiple times)")

	// issue update
	issueUpdateCmd.Flags().String("title", "", "New title")
	issueUpdateCmd.Flags().String("description", "", "New description")
	issueUpdateCmd.Flags().String("status", "", "New status")
	issueUpdateCmd.Flags().String("priority", "", "New priority")
	issueUpdateCmd.Flags().String("assignee", "", "New assignee name (member or agent)")
	issueUpdateCmd.Flags().String("due-date", "", "New due date (RFC3339 format)")
	issueUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	// issue status
	issueStatusCmd.Flags().String("output", "table", "Output format: table or json")

	// issue assign
	issueAssignCmd.Flags().String("to", "", "Assignee name (member or agent)")
	issueAssignCmd.Flags().Bool("unassign", false, "Remove current assignee")
	issueAssignCmd.Flags().String("output", "json", "Output format: table or json")

	// issue comment list
	issueCommentListCmd.Flags().String("output", "table", "Output format: table or json")
	issueCommentListCmd.Flags().Int("limit", 0, "Maximum number of comments to return (0 = all)")
	issueCommentListCmd.Flags().Int("offset", 0, "Number of comments to skip")
	issueCommentListCmd.Flags().String("since", "", "Only return comments created after this timestamp (RFC3339)")

	// issue runs
	issueRunsCmd.Flags().String("output", "table", "Output format: table or json")

	// issue run-messages
	issueRunMessagesCmd.Flags().String("output", "json", "Output format: table or json")
	issueRunMessagesCmd.Flags().Int("since", 0, "Only return messages after this sequence number")

	// issue comment add
	issueCommentAddCmd.Flags().String("content", "", "Comment content (required)")
	issueCommentAddCmd.Flags().String("parent", "", "Parent comment ID (reply to a specific comment)")
	issueCommentAddCmd.Flags().StringSlice("attachment", nil, "File path(s) to attach (can be specified multiple times)")
	issueCommentAddCmd.Flags().String("output", "json", "Output format: table or json")

	// issue search
	issueSearchCmd.Flags().Int("limit", 20, "Maximum number of results to return")
	issueSearchCmd.Flags().Bool("include-closed", false, "Include done and cancelled issues")
	issueSearchCmd.Flags().String("output", "table", "Output format: table or json")

	// issue children
	issueChildrenCmd.Flags().String("output", "json", "Output format: table or json")

	// issue submit-review
	issueSubmitReviewCmd.Flags().String("idempotency-key", "", "Idempotency key for submit-review request")
	issueSubmitReviewCmd.Flags().String("summary", "", "Submission summary")
	issueSubmitReviewCmd.Flags().String("pr-url", "", "Pull request URL to include in submission evidence")
	issueSubmitReviewCmd.Flags().String("output", "json", "Output format: table or json")

	// issue review
	issueReviewCmd.Flags().String("idempotency-key", "", "Idempotency key for review request")
	issueReviewCmd.Flags().String("review-round-id", "", "Submitted review round ID to resolve")
	issueReviewCmd.Flags().String("verdict", "", "Review verdict")
	issueReviewCmd.Flags().String("summary", "", "Review summary")
	issueReviewCmd.Flags().StringSlice("criterion", nil, "Criterion verdict in criterion-id:result[:note] format")
	issueReviewCmd.Flags().String("output", "json", "Output format: table or json")

	// issue report-blocked
	issueReportBlockedCmd.Flags().String("reason", "", "Blocking reason")
	issueReportBlockedCmd.Flags().String("output", "json", "Output format: table or json")

	// issue replan
	issueReplanCmd.Flags().String("idempotency-key", "", "Idempotency key for replan request")
	issueReplanCmd.Flags().String("reason", "", "Reason for replan")
	issueReplanCmd.Flags().String("plan-content", "", "Updated plan content")
	issueReplanCmd.Flags().String("output", "json", "Output format: table or json")

	// issue finalize
	issueFinalizeCmd.Flags().String("output", "json", "Output format: table or json")
}

// ---------------------------------------------------------------------------
// Issue commands
// ---------------------------------------------------------------------------

func runIssueList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	params := url.Values{}
	if client.WorkspaceID != "" {
		params.Set("workspace_id", client.WorkspaceID)
	}
	if v, _ := cmd.Flags().GetString("status"); v != "" {
		params.Set("status", v)
	}
	if v, _ := cmd.Flags().GetString("priority"); v != "" {
		params.Set("priority", v)
	}
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		params.Set("limit", fmt.Sprintf("%d", v))
	}
	if v, _ := cmd.Flags().GetString("assignee"); v != "" {
		_, aID, resolveErr := resolveAssignee(ctx, client, v)
		if resolveErr != nil {
			return fmt.Errorf("resolve assignee: %w", resolveErr)
		}
		params.Set("assignee_id", aID)
	}

	path := "/api/issues"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var result map[string]any
	if err := client.GetJSON(ctx, path, &result); err != nil {
		return fmt.Errorf("list issues: %w", err)
	}

	issuesRaw, _ := result["issues"].([]any)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, issuesRaw)
	}

	headers := []string{"ID", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE", "DUE DATE"}
	rows := make([][]string, 0, len(issuesRaw))
	for _, raw := range issuesRaw {
		issue, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		assignee := formatAssignee(issue)
		dueDate := strVal(issue, "due_date")
		if dueDate != "" && len(dueDate) >= 10 {
			dueDate = dueDate[:10]
		}
		rows = append(rows, []string{
			truncateID(strVal(issue, "id")),
			strVal(issue, "title"),
			strVal(issue, "status"),
			strVal(issue, "priority"),
			assignee,
			dueDate,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runIssueGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var issue map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+args[0], &issue); err != nil {
		return fmt.Errorf("get issue: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		assignee := formatAssignee(issue)
		dueDate := strVal(issue, "due_date")
		if dueDate != "" && len(dueDate) >= 10 {
			dueDate = dueDate[:10]
		}
		headers := []string{"ID", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE", "DUE DATE", "DESCRIPTION"}
		rows := [][]string{{
			truncateID(strVal(issue, "id")),
			strVal(issue, "title"),
			strVal(issue, "status"),
			strVal(issue, "priority"),
			assignee,
			dueDate,
			strVal(issue, "description"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	return cli.PrintJSON(os.Stdout, issue)
}

func runIssueCreate(cmd *cobra.Command, _ []string) error {
	title, _ := cmd.Flags().GetString("title")
	if title == "" {
		return fmt.Errorf("--title is required")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	// Use a longer timeout when attachments are present (file uploads can be slow).
	timeout := 15 * time.Second
	attachments, _ := cmd.Flags().GetStringSlice("attachment")
	if len(attachments) > 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	body := map[string]any{"title": title}
	if v, _ := cmd.Flags().GetString("description"); v != "" {
		body["description"] = v
	}
	if v, _ := cmd.Flags().GetString("status"); v != "" {
		body["status"] = v
	}
	if v, _ := cmd.Flags().GetString("priority"); v != "" {
		body["priority"] = v
	}
	if v, _ := cmd.Flags().GetString("parent"); v != "" {
		body["parent_issue_id"] = v
	}
	if v, _ := cmd.Flags().GetString("due-date"); v != "" {
		body["due_date"] = v
	}
	if v, _ := cmd.Flags().GetString("assignee"); v != "" {
		aType, aID, resolveErr := resolveAssignee(ctx, client, v)
		if resolveErr != nil {
			return fmt.Errorf("resolve assignee: %w", resolveErr)
		}
		body["assignee_type"] = aType
		body["assignee_id"] = aID
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues", body, &result); err != nil {
		return fmt.Errorf("create issue: %w", err)
	}

	// Upload attachments and link them to the newly created issue.
	issueID := strVal(result, "id")
	for _, filePath := range attachments {
		data, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return fmt.Errorf("read attachment %s: %w", filePath, readErr)
		}
		if _, uploadErr := client.UploadFile(ctx, data, filePath, issueID); uploadErr != nil {
			return fmt.Errorf("upload attachment %s: %w", filePath, uploadErr)
		}
		fmt.Fprintf(os.Stderr, "Uploaded %s\n", filePath)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"ID", "TITLE", "STATUS", "PRIORITY"}
		rows := [][]string{{
			truncateID(strVal(result, "id")),
			strVal(result, "title"),
			strVal(result, "status"),
			strVal(result, "priority"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	return cli.PrintJSON(os.Stdout, result)
}

func runIssueUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	body := map[string]any{}
	if cmd.Flags().Changed("title") {
		v, _ := cmd.Flags().GetString("title")
		body["title"] = v
	}
	if cmd.Flags().Changed("description") {
		v, _ := cmd.Flags().GetString("description")
		body["description"] = v
	}
	if cmd.Flags().Changed("status") {
		v, _ := cmd.Flags().GetString("status")
		body["status"] = v
	}
	if cmd.Flags().Changed("priority") {
		v, _ := cmd.Flags().GetString("priority")
		body["priority"] = v
	}
	if cmd.Flags().Changed("due-date") {
		v, _ := cmd.Flags().GetString("due-date")
		body["due_date"] = v
	}
	if cmd.Flags().Changed("assignee") {
		v, _ := cmd.Flags().GetString("assignee")
		aType, aID, resolveErr := resolveAssignee(ctx, client, v)
		if resolveErr != nil {
			return fmt.Errorf("resolve assignee: %w", resolveErr)
		}
		body["assignee_type"] = aType
		body["assignee_id"] = aID
	}

	if len(body) == 0 {
		return fmt.Errorf("no fields to update; use flags like --title, --status, --priority, --assignee, etc.")
	}

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/issues/"+args[0], body, &result); err != nil {
		return fmt.Errorf("update issue: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"ID", "TITLE", "STATUS", "PRIORITY"}
		rows := [][]string{{
			truncateID(strVal(result, "id")),
			strVal(result, "title"),
			strVal(result, "status"),
			strVal(result, "priority"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	return cli.PrintJSON(os.Stdout, result)
}

func runIssueAssign(cmd *cobra.Command, args []string) error {
	toName, _ := cmd.Flags().GetString("to")
	unassign, _ := cmd.Flags().GetBool("unassign")

	if toName == "" && !unassign {
		return fmt.Errorf("provide --to <name> or --unassign")
	}
	if toName != "" && unassign {
		return fmt.Errorf("--to and --unassign are mutually exclusive")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	body := map[string]any{}
	if unassign {
		body["assignee_type"] = nil
		body["assignee_id"] = nil
	} else {
		aType, aID, resolveErr := resolveAssignee(ctx, client, toName)
		if resolveErr != nil {
			return fmt.Errorf("resolve assignee: %w", resolveErr)
		}
		body["assignee_type"] = aType
		body["assignee_id"] = aID
	}

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/issues/"+args[0], body, &result); err != nil {
		return fmt.Errorf("assign issue: %w", err)
	}

	if unassign {
		fmt.Fprintf(os.Stderr, "Issue %s unassigned.\n", truncateID(args[0]))
	} else {
		fmt.Fprintf(os.Stderr, "Issue %s assigned to %s.\n", truncateID(args[0]), toName)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

func runIssueStatus(cmd *cobra.Command, args []string) error {
	id := args[0]
	status := args[1]

	valid := false
	for _, s := range validIssueStatuses {
		if s == status {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid status %q; valid values: %s", status, strings.Join(validIssueStatuses, ", "))
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	body := map[string]any{"status": status}
	var result map[string]any
	if err := client.PutJSON(ctx, "/api/issues/"+id, body, &result); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Issue %s status changed to %s.\n", truncateID(id), status)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Comment commands
// ---------------------------------------------------------------------------

func runIssueCommentList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	params := url.Values{}
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		params.Set("limit", fmt.Sprintf("%d", v))
	}
	if v, _ := cmd.Flags().GetInt("offset"); v > 0 {
		params.Set("offset", fmt.Sprintf("%d", v))
	}
	if v, _ := cmd.Flags().GetString("since"); v != "" {
		params.Set("since", v)
	}

	path := "/api/issues/" + args[0] + "/comments"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var comments []map[string]any
	isPaginated := len(params) > 0
	if isPaginated {
		headers, getErr := client.GetJSONWithHeaders(ctx, path, &comments)
		if getErr != nil {
			return fmt.Errorf("list comments: %w", getErr)
		}
		if total := headers.Get("X-Total-Count"); total != "" {
			fmt.Fprintf(os.Stderr, "Showing %d of %s comments.\n", len(comments), total)
		}
	} else {
		if err := client.GetJSON(ctx, path, &comments); err != nil {
			return fmt.Errorf("list comments: %w", err)
		}
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, comments)
	}

	headers := []string{"ID", "PARENT", "AUTHOR", "TYPE", "CONTENT", "CREATED"}
	rows := make([][]string, 0, len(comments))
	for _, c := range comments {
		content := strVal(c, "content")
		if utf8.RuneCountInString(content) > 80 {
			runes := []rune(content)
			content = string(runes[:77]) + "..."
		}
		created := strVal(c, "created_at")
		if len(created) >= 16 {
			created = created[:16]
		}
		parentID := strVal(c, "parent_id")
		if parentID == "" {
			parentID = "—"
		}
		rows = append(rows, []string{
			strVal(c, "id"),
			parentID,
			strVal(c, "author_type") + ":" + truncateID(strVal(c, "author_id")),
			strVal(c, "type"),
			content,
			created,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runIssueCommentAdd(cmd *cobra.Command, args []string) error {
	content, _ := cmd.Flags().GetString("content")
	if content == "" {
		return fmt.Errorf("--content is required")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	issueID := args[0]

	// Use a longer timeout when attachments are present (file uploads can be slow).
	timeout := 15 * time.Second
	attachments, _ := cmd.Flags().GetStringSlice("attachment")
	if len(attachments) > 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Upload attachments and collect their IDs.
	var attachmentIDs []string
	for _, filePath := range attachments {
		data, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return fmt.Errorf("read attachment %s: %w", filePath, readErr)
		}
		id, uploadErr := client.UploadFile(ctx, data, filePath, issueID)
		if uploadErr != nil {
			return fmt.Errorf("upload attachment %s: %w", filePath, uploadErr)
		}
		attachmentIDs = append(attachmentIDs, id)
		fmt.Fprintf(os.Stderr, "Uploaded %s\n", filePath)
	}

	body := map[string]any{"content": content}
	if parentID, _ := cmd.Flags().GetString("parent"); parentID != "" {
		body["parent_id"] = parentID
	}
	if len(attachmentIDs) > 0 {
		body["attachment_ids"] = attachmentIDs
	}
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+issueID+"/comments", body, &result); err != nil {
		return fmt.Errorf("add comment: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Comment added to issue %s.\n", truncateID(issueID))

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

func runIssueCommentDelete(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := client.DeleteJSON(ctx, "/api/comments/"+args[0]); err != nil {
		return fmt.Errorf("delete comment: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Comment %s deleted.\n", truncateID(args[0]))
	return nil
}

// ---------------------------------------------------------------------------
// Execution history commands
// ---------------------------------------------------------------------------

func runIssueRuns(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var runs []map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+args[0]+"/task-runs", &runs); err != nil {
		return fmt.Errorf("list runs: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, runs)
	}

	headers := []string{"ID", "AGENT", "STATUS", "STARTED", "COMPLETED", "ERROR"}
	rows := make([][]string, 0, len(runs))
	for _, r := range runs {
		started := strVal(r, "started_at")
		if len(started) >= 16 {
			started = started[:16]
		}
		completed := strVal(r, "completed_at")
		if len(completed) >= 16 {
			completed = completed[:16]
		}
		errMsg := strVal(r, "error")
		if utf8.RuneCountInString(errMsg) > 50 {
			runes := []rune(errMsg)
			errMsg = string(runes[:47]) + "..."
		}
		rows = append(rows, []string{
			truncateID(strVal(r, "id")),
			truncateID(strVal(r, "agent_id")),
			strVal(r, "status"),
			started,
			completed,
			errMsg,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runIssueRunMessages(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	path := "/api/daemon/tasks/" + args[0] + "/messages"
	if since, _ := cmd.Flags().GetInt("since"); since > 0 {
		path += fmt.Sprintf("?since=%d", since)
	}

	var messages []map[string]any
	if err := client.GetJSON(ctx, path, &messages); err != nil {
		return fmt.Errorf("list run messages: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, messages)
	}

	headers := []string{"SEQ", "TYPE", "TOOL", "CONTENT"}
	rows := make([][]string, 0, len(messages))
	for _, m := range messages {
		content := strVal(m, "content")
		if content == "" {
			content = strVal(m, "output")
		}
		if utf8.RuneCountInString(content) > 80 {
			runes := []rune(content)
			content = string(runes[:77]) + "..."
		}
		seq := ""
		if v, ok := m["seq"]; ok {
			seq = fmt.Sprintf("%v", v)
		}
		rows = append(rows, []string{
			seq,
			strVal(m, "type"),
			strVal(m, "tool"),
			content,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// ---------------------------------------------------------------------------
// Children command
// ---------------------------------------------------------------------------

func runIssueChildren(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var result map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+args[0]+"/children", &result); err != nil {
		return fmt.Errorf("list children: %w", err)
	}

	issuesRaw, _ := result["issues"].([]any)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, issuesRaw)
	}

	headers := []string{"ID", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE"}
	rows := make([][]string, 0, len(issuesRaw))
	for _, raw := range issuesRaw {
		issue, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			truncateID(strVal(issue, "id")),
			strVal(issue, "title"),
			strVal(issue, "status"),
			strVal(issue, "priority"),
			formatAssignee(issue),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runIssueSubmitReview(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	body := map[string]any{}
	if idempotencyKey, _ := cmd.Flags().GetString("idempotency-key"); idempotencyKey != "" {
		body["idempotency_key"] = idempotencyKey
	}
	if summary, _ := cmd.Flags().GetString("summary"); summary != "" {
		body["summary"] = summary
	}
	if prURL, _ := cmd.Flags().GetString("pr-url"); prURL != "" {
		body["evidence"] = map[string]any{"pr_url": prURL}
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+args[0]+"/workflow/submit-review", body, &result); err != nil {
		return fmt.Errorf("submit review: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

func runIssueReview(cmd *cobra.Command, args []string) error {
	verdict, _ := cmd.Flags().GetString("verdict")
	if verdict == "" {
		return fmt.Errorf("--verdict is required")
	}
	if !containsString(validReviewDecisions, verdict) {
		return fmt.Errorf("invalid verdict %q; valid values: %s", verdict, strings.Join(validReviewDecisions, ", "))
	}
	reviewRoundID, _ := cmd.Flags().GetString("review-round-id")
	if reviewRoundID == "" {
		return fmt.Errorf("--review-round-id is required")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	criteriaFlags, _ := cmd.Flags().GetStringSlice("criterion")
	criteria := make([]map[string]any, 0, len(criteriaFlags))
	for _, raw := range criteriaFlags {
		criterion, parseErr := parseWorkflowCriterion(raw)
		if parseErr != nil {
			return parseErr
		}
		criteria = append(criteria, criterion)
	}

	body := map[string]any{
		"verdict":         verdict,
		"review_round_id": reviewRoundID,
	}
	if idempotencyKey, _ := cmd.Flags().GetString("idempotency-key"); idempotencyKey != "" {
		body["idempotency_key"] = idempotencyKey
	}
	if summary, _ := cmd.Flags().GetString("summary"); summary != "" {
		body["summary"] = summary
	}
	if len(criteria) > 0 {
		body["criterion_results"] = criteria
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+args[0]+"/workflow/review", body, &result); err != nil {
		return fmt.Errorf("review issue workflow: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

func runIssueReportBlocked(cmd *cobra.Command, args []string) error {
	reason, _ := cmd.Flags().GetString("reason")
	if reason == "" {
		return fmt.Errorf("--reason is required")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	body := map[string]any{"reason": reason}
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+args[0]+"/workflow/report-blocked", body, &result); err != nil {
		return fmt.Errorf("report issue blocked: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

func runIssueReplan(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	body := map[string]any{}
	if idempotencyKey, _ := cmd.Flags().GetString("idempotency-key"); idempotencyKey != "" {
		body["idempotency_key"] = idempotencyKey
	}
	if reason, _ := cmd.Flags().GetString("reason"); reason != "" {
		body["reason"] = reason
	}
	if planContent, _ := cmd.Flags().GetString("plan-content"); planContent != "" {
		body["plan_content"] = planContent
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+args[0]+"/workflow/replan", body, &result); err != nil {
		return fmt.Errorf("replan issue workflow: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

func runIssueFinalize(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+args[0]+"/workflow/finalize-parent", map[string]any{}, &result); err != nil {
		return fmt.Errorf("finalize issue workflow: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

// ---------------------------------------------------------------------------
// Search command
// ---------------------------------------------------------------------------

func runIssueSearch(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("q", args[0])
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		params.Set("limit", fmt.Sprintf("%d", v))
	}
	if v, _ := cmd.Flags().GetBool("include-closed"); v {
		params.Set("include_closed", "true")
	}

	path := "/api/issues/search?" + params.Encode()

	var result map[string]any
	if err := client.GetJSON(ctx, path, &result); err != nil {
		return fmt.Errorf("search issues: %w", err)
	}

	issuesRaw, _ := result["issues"].([]any)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	headers := []string{"ID", "IDENTIFIER", "TITLE", "STATUS", "MATCH"}
	rows := make([][]string, 0, len(issuesRaw))
	for _, raw := range issuesRaw {
		issue, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		matchInfo := strVal(issue, "match_source")
		if snippet := strVal(issue, "matched_snippet"); snippet != "" {
			if utf8.RuneCountInString(snippet) > 50 {
				runes := []rune(snippet)
				snippet = string(runes[:47]) + "..."
			}
			matchInfo += ": " + snippet
		}
		rows = append(rows, []string{
			truncateID(strVal(issue, "id")),
			strVal(issue, "identifier"),
			strVal(issue, "title"),
			strVal(issue, "status"),
			matchInfo,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type assigneeMatch struct {
	Type string // "member" or "agent"
	ID   string // user_id for members, agent id for agents
	Name string
}

func resolveAssignee(ctx context.Context, client *cli.APIClient, name string) (string, string, error) {
	if client.WorkspaceID == "" {
		return "", "", fmt.Errorf("workspace ID is required to resolve assignees; use --workspace-id or set MULTICA_WORKSPACE_ID")
	}

	nameLower := strings.ToLower(name)
	var matches []assigneeMatch
	var errs []error

	// Search members.
	var members []map[string]any
	if err := client.GetJSON(ctx, "/api/workspaces/"+client.WorkspaceID+"/members", &members); err != nil {
		errs = append(errs, fmt.Errorf("fetch members: %w", err))
	} else {
		for _, m := range members {
			mName := strVal(m, "name")
			if strings.Contains(strings.ToLower(mName), nameLower) {
				matches = append(matches, assigneeMatch{
					Type: "member",
					ID:   strVal(m, "user_id"),
					Name: mName,
				})
			}
		}
	}

	// Search agents.
	var agents []map[string]any
	agentPath := "/api/agents?" + url.Values{"workspace_id": {client.WorkspaceID}}.Encode()
	if err := client.GetJSON(ctx, agentPath, &agents); err != nil {
		errs = append(errs, fmt.Errorf("fetch agents: %w", err))
	} else {
		for _, a := range agents {
			aName := strVal(a, "name")
			if strings.Contains(strings.ToLower(aName), nameLower) {
				matches = append(matches, assigneeMatch{
					Type: "agent",
					ID:   strVal(a, "id"),
					Name: aName,
				})
			}
		}
	}

	// If both fetches failed, report the errors instead of a misleading "not found".
	if len(errs) == 2 {
		return "", "", fmt.Errorf("failed to resolve assignee: %v; %v", errs[0], errs[1])
	}

	switch len(matches) {
	case 0:
		return "", "", fmt.Errorf("no member or agent found matching %q", name)
	case 1:
		return matches[0].Type, matches[0].ID, nil
	default:
		var parts []string
		for _, m := range matches {
			parts = append(parts, fmt.Sprintf("  %s %q (%s)", m.Type, m.Name, truncateID(m.ID)))
		}
		return "", "", fmt.Errorf("ambiguous assignee %q; matches:\n%s", name, strings.Join(parts, "\n"))
	}
}

func formatAssignee(issue map[string]any) string {
	aType := strVal(issue, "assignee_type")
	aID := strVal(issue, "assignee_id")
	if aType == "" || aID == "" {
		return ""
	}
	return aType + ":" + truncateID(aID)
}

func parseWorkflowCriterion(raw string) (map[string]any, error) {
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid --criterion %q; use criterion-id:result[:note]", raw)
	}
	if !containsString(validCriterionResults, parts[1]) {
		return nil, fmt.Errorf("invalid criterion result %q; valid values: %s", parts[1], strings.Join(validCriterionResults, ", "))
	}

	criterion := map[string]any{
		"criterion_id": parts[0],
		"result":       parts[1],
	}
	if len(parts) == 3 && parts[2] != "" {
		criterion["note"] = parts[2]
	}
	return criterion, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func truncateID(id string) string {
	if utf8.RuneCountInString(id) > 8 {
		runes := []rune(id)
		return string(runes[:8])
	}
	return id
}
