package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// requestError is returned by postJSON/getJSON when the server responds with an error status.
type requestError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *requestError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// isWorkspaceNotFoundError returns true if the error is a 404 with "workspace not found" body.
func isWorkspaceNotFoundError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	if reqErr.StatusCode != http.StatusNotFound {
		return false
	}
	return strings.Contains(strings.ToLower(reqErr.Body), "workspace not found")
}

// Client handles HTTP communication with the Multica server daemon API.
type Client struct {
	baseURL   string
	userToken string
	client    *http.Client
}

// NewClient creates a new daemon API client.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// SetUserToken sets the control-plane user token.
func (c *Client) SetUserToken(token string) {
	c.userToken = token
}

// UserToken returns the control-plane user token.
func (c *Client) UserToken() string {
	return c.userToken
}

func (c *Client) ClaimTask(ctx context.Context, runtimeID, token string) (*Task, error) {
	var resp struct {
		Task *Task `json:"task"`
	}
	if err := c.postJSONWithToken(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/tasks/claim", runtimeID), token, map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp.Task, nil
}

func (c *Client) StartTask(ctx context.Context, taskID, token string) error {
	return c.postJSONWithToken(ctx, fmt.Sprintf("/api/daemon/tasks/%s/start", taskID), token, map[string]any{}, nil)
}

func (c *Client) ReportProgress(ctx context.Context, taskID, token, summary string, step, total int) error {
	return c.postJSONWithToken(ctx, fmt.Sprintf("/api/daemon/tasks/%s/progress", taskID), token, map[string]any{
		"summary": summary,
		"step":    step,
		"total":   total,
	}, nil)
}

// TaskMessageData represents a single agent execution message for batch reporting.
type TaskMessageData struct {
	Seq     int            `json:"seq"`
	Type    string         `json:"type"`
	Tool    string         `json:"tool,omitempty"`
	Content string         `json:"content,omitempty"`
	Input   map[string]any `json:"input,omitempty"`
	Output  string         `json:"output,omitempty"`
}

func (c *Client) ReportTaskMessages(ctx context.Context, taskID, token string, messages []TaskMessageData) error {
	return c.postJSONWithToken(ctx, fmt.Sprintf("/api/daemon/tasks/%s/messages", taskID), token, map[string]any{
		"messages": messages,
	}, nil)
}

func (c *Client) CompleteTask(ctx context.Context, taskID, token, output, branchName, sessionID, workDir string) error {
	body := map[string]any{"output": output}
	if branchName != "" {
		body["branch_name"] = branchName
	}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if workDir != "" {
		body["work_dir"] = workDir
	}
	return c.postJSONWithToken(ctx, fmt.Sprintf("/api/daemon/tasks/%s/complete", taskID), token, body, nil)
}

func (c *Client) ReportTaskUsage(ctx context.Context, taskID, token string, usage []TaskUsageEntry) error {
	if len(usage) == 0 {
		return nil
	}
	return c.postJSONWithToken(ctx, fmt.Sprintf("/api/daemon/tasks/%s/usage", taskID), token, map[string]any{
		"usage": usage,
	}, nil)
}

func (c *Client) FailTask(ctx context.Context, taskID, token, errMsg string) error {
	return c.postJSONWithToken(ctx, fmt.Sprintf("/api/daemon/tasks/%s/fail", taskID), token, map[string]any{
		"error": errMsg,
	}, nil)
}

// GetTaskStatus returns the current status of a task. Used by the daemon to
// detect if a task was cancelled while it was executing.
func (c *Client) GetTaskStatus(ctx context.Context, taskID, token string) (string, error) {
	var resp struct {
		Status string `json:"status"`
	}
	if err := c.getJSONWithToken(ctx, fmt.Sprintf("/api/daemon/tasks/%s/status", taskID), token, &resp); err != nil {
		return "", err
	}
	return resp.Status, nil
}

func (c *Client) ReportUsage(ctx context.Context, runtimeID, token string, entries []map[string]any) error {
	return c.postJSONWithToken(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/usage", runtimeID), token, map[string]any{
		"entries": entries,
	}, nil)
}

// HeartbeatResponse contains the server's response to a heartbeat, including any pending actions.
type HeartbeatResponse struct {
	Status        string         `json:"status"`
	PendingPing   *PendingPing   `json:"pending_ping,omitempty"`
	PendingUpdate *PendingUpdate `json:"pending_update,omitempty"`
}

// PendingPing represents a ping test request from the server.
type PendingPing struct {
	ID string `json:"id"`
}

// PendingUpdate represents a CLI update request from the server.
type PendingUpdate struct {
	ID            string `json:"id"`
	TargetVersion string `json:"target_version"`
}

func (c *Client) SendHeartbeat(ctx context.Context, runtimeID, token string) (*HeartbeatResponse, error) {
	var resp HeartbeatResponse
	if err := c.postJSONWithToken(ctx, "/api/daemon/heartbeat", token, map[string]string{
		"runtime_id": runtimeID,
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ReportPingResult(ctx context.Context, runtimeID, pingID, token string, result map[string]any) error {
	return c.postJSONWithToken(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/ping/%s/result", runtimeID, pingID), token, result, nil)
}

// ReportUpdateResult sends the CLI update result back to the server.
func (c *Client) ReportUpdateResult(ctx context.Context, runtimeID, updateID, token string, result map[string]any) error {
	return c.postJSONWithToken(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/update/%s/result", runtimeID, updateID), token, result, nil)
}

// WorkspaceInfo holds minimal workspace metadata returned by the API.
type WorkspaceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DaemonEnrollResponse holds the enrollment response.
type DaemonEnrollResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// ListWorkspaces fetches all workspaces the authenticated user belongs to.
func (c *Client) ListWorkspaces(ctx context.Context) ([]WorkspaceInfo, error) {
	var workspaces []WorkspaceInfo
	if err := c.getJSONWithToken(ctx, "/api/workspaces", c.userToken, &workspaces); err != nil {
		return nil, err
	}
	return workspaces, nil
}

func (c *Client) EnrollDaemonToken(ctx context.Context, workspaceID, daemonID string) (*DaemonEnrollResponse, error) {
	var resp DaemonEnrollResponse
	if err := c.postJSONWithToken(ctx, fmt.Sprintf("/api/daemon/workspaces/%s/enroll", workspaceID), c.userToken, map[string]string{
		"daemon_id": daemonID,
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Deregister(ctx context.Context, runtimeIDs []string, token string) error {
	return c.postJSONWithToken(ctx, "/api/daemon/deregister", token, map[string]any{
		"runtime_ids": runtimeIDs,
	}, nil)
}

// RegisterResponse holds the server's response to a daemon registration.
type RegisterResponse struct {
	Runtimes []Runtime  `json:"runtimes"`
	Repos    []RepoData `json:"repos"`
}

func (c *Client) Register(ctx context.Context, req map[string]any, token string) (*RegisterResponse, error) {
	var resp RegisterResponse
	if err := c.postJSONWithToken(ctx, "/api/daemon/register", token, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) postJSONWithToken(ctx context.Context, path, token string, reqBody any, respBody any) error {
	var body io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &requestError{Method: http.MethodPost, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if respBody == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(respBody)
}

func (c *Client) getJSONWithToken(ctx context.Context, path, token string, respBody any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &requestError{Method: http.MethodGet, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if respBody == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(respBody)
}
