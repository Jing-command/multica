package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// ---------------------------------------------------------------------------
// Daemon Registration & Heartbeat
// ---------------------------------------------------------------------------

type DaemonEnrollRequest struct {
	DaemonID string `json:"daemon_id"`
}

type DaemonEnrollResponse struct {
	Token string `json:"token"`
}

type DaemonRegisterRequest struct {
	WorkspaceID string `json:"workspace_id"`
	DaemonID    string `json:"daemon_id"`
	DeviceName  string `json:"device_name"`
	CLIVersion  string `json:"cli_version"` // multica CLI version
	Runtimes    []struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Version string `json:"version"` // agent CLI version (claude/codex)
		Status  string `json:"status"`
	} `json:"runtimes"`
}

func (h *Handler) DaemonEnroll(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "workspaceId")
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}

	var req DaemonEnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.DaemonID = strings.TrimSpace(req.DaemonID)
	if req.DaemonID == "" {
		writeError(w, http.StatusBadRequest, "daemon_id is required")
		return
	}

	rawToken, err := auth.GenerateDaemonToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate daemon token")
		return
	}
	if err := h.Queries.DeleteDaemonTokensByWorkspaceAndDaemon(r.Context(), db.DeleteDaemonTokensByWorkspaceAndDaemonParams{
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    req.DaemonID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rotate daemon token")
		return
	}
	createdToken, err := h.Queries.CreateDaemonToken(r.Context(), db.CreateDaemonTokenParams{
		TokenHash:   auth.HashToken(rawToken),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    req.DaemonID,
		UserID:      member.UserID,
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(30 * 24 * time.Hour), Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist daemon token")
		return
	}

	slog.Info("daemon enrolled", "workspace_id", workspaceID, "daemon_id", req.DaemonID, "user_id", uuidToString(createdToken.UserID))
	writeJSON(w, http.StatusOK, DaemonEnrollResponse{Token: rawToken})
}

func (h *Handler) DaemonRegister(w http.ResponseWriter, r *http.Request) {
	var req DaemonRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	authWorkspaceID := middleware.DaemonWorkspaceIDFromContext(r.Context())
	authDaemonID := strings.TrimSpace(middleware.DaemonIDFromContext(r.Context()))
	authUserID := strings.TrimSpace(middleware.DaemonUserIDFromContext(r.Context()))
	req.DeviceName = strings.TrimSpace(req.DeviceName)

	if authDaemonID == "" {
		writeError(w, http.StatusUnauthorized, "invalid daemon token")
		return
	}
	if authWorkspaceID == "" {
		writeError(w, http.StatusUnauthorized, "invalid daemon token")
		return
	}
	if authUserID == "" {
		writeError(w, http.StatusUnauthorized, "invalid daemon token")
		return
	}
	if len(req.Runtimes) == 0 {
		writeError(w, http.StatusBadRequest, "at least one runtime is required")
		return
	}

	ownerID := parseUUID(authUserID)

	ws, err := h.Queries.GetWorkspace(r.Context(), parseUUID(authWorkspaceID))
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}

	resp := make([]AgentRuntimeResponse, 0, len(req.Runtimes))
	for _, runtime := range req.Runtimes {
		provider := strings.TrimSpace(runtime.Type)
		if provider == "" {
			provider = "unknown"
		}
		name := strings.TrimSpace(runtime.Name)
		if name == "" {
			name = provider
			if req.DeviceName != "" {
				name = fmt.Sprintf("%s (%s)", provider, req.DeviceName)
			}
		}
		deviceInfo := strings.TrimSpace(req.DeviceName)
		if runtime.Version != "" && deviceInfo != "" {
			deviceInfo = fmt.Sprintf("%s · %s", deviceInfo, runtime.Version)
		} else if runtime.Version != "" {
			deviceInfo = runtime.Version
		}
		status := "online"
		if runtime.Status == "offline" {
			status = "offline"
		}
		metadata, _ := json.Marshal(map[string]any{
			"version":     runtime.Version,
			"cli_version": req.CLIVersion,
		})

		registered, err := h.Queries.UpsertAgentRuntime(r.Context(), db.UpsertAgentRuntimeParams{
			WorkspaceID: parseUUID(authWorkspaceID),
			DaemonID:    strToText(authDaemonID),
			Name:        name,
			RuntimeMode: "local",
			Provider:    provider,
			Status:      status,
			DeviceInfo:  deviceInfo,
			Metadata:    metadata,
			OwnerID:     ownerID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to register runtime: "+err.Error())
			return
		}
		resp = append(resp, runtimeToResponse(registered))
	}

	slog.Info("daemon registered", "workspace_id", authWorkspaceID, "daemon_id", authDaemonID, "runtimes_count", len(resp))

	// Ensure the built-in Orchestrator agent exists for this workspace.
	if len(resp) > 0 {
		preferredRuntime := selectPreferredOrchestratorRuntimeResponses(resp)
		ensureOrchestratorAgent(r.Context(), h.Queries, parseUUID(authWorkspaceID), parseUUID(preferredRuntime.ID), ownerID)
	}

	h.publish(protocol.EventDaemonRegister, authWorkspaceID, "system", "", map[string]any{
		"runtimes": resp,
	})

	// Include workspace repos so the daemon can cache them locally.
	var repos []RepoData
	if ws.Repos != nil {
		json.Unmarshal(ws.Repos, &repos)
	}
	if repos == nil {
		repos = []RepoData{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"runtimes": resp, "repos": repos})
}

// DaemonDeregister marks runtimes as offline when the daemon shuts down.
func (h *Handler) DaemonDeregister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuntimeIDs []string `json:"runtime_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.RuntimeIDs) == 0 {
		writeError(w, http.StatusBadRequest, "runtime_ids is required")
		return
	}

	affectedWorkspaces := make(map[string]bool)

	for _, rid := range req.RuntimeIDs {
		rt, ok := h.requireDaemonRuntimeScope(w, r, rid)
		if !ok {
			return
		}
		if err := h.Queries.SetAgentRuntimeOfflineForDaemonScope(r.Context(), db.SetAgentRuntimeOfflineForDaemonScopeParams{
			ID:          parseUUID(rid),
			WorkspaceID: rt.WorkspaceID,
			DaemonID:    rt.DaemonID,
		}); err != nil {
			slog.Warn("deregister: failed to set offline", "runtime_id", rid, "error", err)
			continue
		}
		affectedWorkspaces[uuidToString(rt.WorkspaceID)] = true
	}

	// Notify frontend clients so they re-fetch runtime list.
	for wsID := range affectedWorkspaces {
		h.publish(protocol.EventDaemonRegister, wsID, "system", "", map[string]any{
			"action": "deregister",
		})
	}

	slog.Info("daemon deregistered", "runtime_ids", req.RuntimeIDs)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type DaemonHeartbeatRequest struct {
	RuntimeID string `json:"runtime_id"`
}

func (h *Handler) DaemonHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req DaemonHeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.RuntimeID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}

	rt, ok := h.requireDaemonRuntimeScope(w, r, req.RuntimeID)
	if !ok {
		return
	}

	_, err := h.Queries.UpdateAgentRuntimeHeartbeatForDaemonScope(r.Context(), db.UpdateAgentRuntimeHeartbeatForDaemonScopeParams{
		ID:          parseUUID(req.RuntimeID),
		WorkspaceID: rt.WorkspaceID,
		DaemonID:    rt.DaemonID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "heartbeat failed")
		return
	}

	slog.Debug("daemon heartbeat", "runtime_id", req.RuntimeID)

	resp := map[string]any{"status": "ok"}

	// Check for pending ping requests for this runtime.
	if pending, err := h.popPendingPingRequest(r.Context(), req.RuntimeID, uuidToString(rt.WorkspaceID), rt.DaemonID.String); err == nil && pending != nil {
		resp["pending_ping"] = map[string]string{"id": pending.ID}
	}

	// Check for pending update requests for this runtime.
	if pending, err := h.popPendingUpdateRequest(r.Context(), req.RuntimeID, uuidToString(rt.WorkspaceID), rt.DaemonID.String); err == nil && pending != nil {
		resp["pending_update"] = map[string]string{
			"id":             pending.ID,
			"target_version": pending.TargetVersion,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// ClaimTaskByRuntime atomically claims the next queued task for a runtime.
// The response includes the agent's name and skills, fetched fresh from the DB.
func (h *Handler) ClaimTaskByRuntime(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeScope(w, r, runtimeID); !ok {
		return
	}

	task, err := h.TaskService.ClaimTaskForRuntime(r.Context(), parseUUID(runtimeID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to claim task: "+err.Error())
		return
	}

	if task == nil {
		slog.Debug("no task to claim", "runtime_id", runtimeID)
		writeJSON(w, http.StatusOK, map[string]any{"task": nil})
		return
	}

	// Build response with fresh agent data (name + skills).
	resp := taskToResponse(*task)
	if agent, err := h.Queries.GetAgent(r.Context(), task.AgentID); err == nil {
		skills := h.TaskService.LoadAgentSkills(r.Context(), task.AgentID)
		resp.Agent = &TaskAgentData{
			ID:           uuidToString(agent.ID),
			Name:         agent.Name,
			Instructions: agent.Instructions,
			Skills:       skills,
		}
	}

	// Include workspace ID and repos so the daemon can set up worktrees.
	if issue, err := h.Queries.GetIssue(r.Context(), task.IssueID); err == nil {
		resp.WorkspaceID = uuidToString(issue.WorkspaceID)
		if ws, err := h.Queries.GetWorkspace(r.Context(), issue.WorkspaceID); err == nil && ws.Repos != nil {
			var repos []RepoData
			if json.Unmarshal(ws.Repos, &repos) == nil && len(repos) > 0 {
				resp.Repos = repos
			}
		}
	}

	// Look up the prior session for this (agent, issue) pair so the daemon
	// can resume the Claude Code conversation context.
	if prior, err := h.Queries.GetLastTaskSession(r.Context(), db.GetLastTaskSessionParams{
		AgentID: task.AgentID,
		IssueID: task.IssueID,
	}); err == nil && prior.SessionID.Valid {
		resp.PriorSessionID = prior.SessionID.String
		if prior.WorkDir.Valid {
			resp.PriorWorkDir = prior.WorkDir.String
		}
	}

	slog.Info("task claimed by runtime", "task_id", uuidToString(task.ID), "runtime_id", runtimeID, "agent_id", uuidToString(task.AgentID), "prior_session", resp.PriorSessionID)
	writeJSON(w, http.StatusOK, map[string]any{"task": resp})
}

// ListPendingTasksByRuntime returns queued/dispatched tasks for a runtime.
func (h *Handler) ListPendingTasksByRuntime(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	workspaceID, daemonID, ok := daemonScopeFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid daemon token")
		return
	}
	if _, ok := h.requireDaemonRuntimeScope(w, r, runtimeID); !ok {
		return
	}

	tasks, err := h.Queries.ListPendingTasksByRuntimeForDaemonScope(r.Context(), db.ListPendingTasksByRuntimeForDaemonScopeParams{
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    strToText(daemonID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pending tasks")
		return
	}

	resp := make([]AgentTaskResponse, len(tasks))
	for i, t := range tasks {
		resp[i] = taskToResponse(t)
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// Task Lifecycle (called by daemon)
// ---------------------------------------------------------------------------

// StartTask marks a dispatched task as running.
func (h *Handler) StartTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if _, ok := h.requireDaemonTaskScope(w, r, taskID); !ok {
		return
	}

	task, err := h.TaskService.StartTask(r.Context(), parseUUID(taskID))
	if err != nil {
		slog.Warn("start task failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("task started", "task_id", taskID, "agent_id", uuidToString(task.AgentID))
	writeJSON(w, http.StatusOK, taskToResponse(*task))
}

// ReportTaskProgress broadcasts a progress update.
type TaskProgressRequest struct {
	Summary string `json:"summary"`
	Step    int    `json:"step"`
	Total   int    `json:"total"`
}

func (h *Handler) ReportTaskProgress(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	task, ok := h.requireDaemonTaskScope(w, r, taskID)
	if !ok {
		return
	}

	var req TaskProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	workspaceID := ""
	if issue, err := h.Queries.GetIssue(r.Context(), task.IssueID); err == nil {
		workspaceID = uuidToString(issue.WorkspaceID)
	}

	h.TaskService.ReportProgress(r.Context(), taskID, workspaceID, req.Summary, req.Step, req.Total)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// CompleteTask marks a running task as completed.
type TaskCompleteRequest struct {
	PRURL     string `json:"pr_url"`
	Output    string `json:"output"`
	SessionID string `json:"session_id"` // Claude session ID for future resumption
	WorkDir   string `json:"work_dir"`   // working directory used during execution
}

func (h *Handler) CompleteTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if _, ok := h.requireDaemonTaskScope(w, r, taskID); !ok {
		return
	}

	var req TaskCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result, _ := json.Marshal(req)
	task, err := h.TaskService.CompleteTask(r.Context(), parseUUID(taskID), result, req.SessionID, req.WorkDir)
	if err != nil {
		slog.Warn("complete task failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("task completed", "task_id", taskID, "agent_id", uuidToString(task.AgentID))
	writeJSON(w, http.StatusOK, taskToResponse(*task))
}

// ReportTaskUsage stores per-task token usage. Called independently of
// complete/fail so usage is captured even when tasks fail or are blocked.
type TaskUsagePayload struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

func (h *Handler) ReportTaskUsage(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if _, ok := h.requireDaemonTaskScope(w, r, taskID); !ok {
		return
	}

	var req struct {
		Usage []TaskUsagePayload `json:"usage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	for _, u := range req.Usage {
		if err := h.Queries.UpsertTaskUsage(r.Context(), db.UpsertTaskUsageParams{
			TaskID:           parseUUID(taskID),
			Provider:         u.Provider,
			Model:            u.Model,
			InputTokens:      u.InputTokens,
			OutputTokens:     u.OutputTokens,
			CacheReadTokens:  u.CacheReadTokens,
			CacheWriteTokens: u.CacheWriteTokens,
		}); err != nil {
			slog.Warn("upsert task usage failed", "task_id", taskID, "model", u.Model, "error", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetTaskStatus returns the current status of a task.
// Used by the daemon to check whether a task was cancelled mid-execution.
func (h *Handler) GetTaskStatus(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	task, ok := h.requireDaemonTaskScope(w, r, taskID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": task.Status})
}

// FailTask marks a running task as failed.
type TaskFailRequest struct {
	Error string `json:"error"`
}

func (h *Handler) FailTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if _, ok := h.requireDaemonTaskScope(w, r, taskID); !ok {
		return
	}

	var req TaskFailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	task, err := h.TaskService.FailTask(r.Context(), parseUUID(taskID), req.Error)
	if err != nil {
		slog.Warn("fail task failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("task failed", "task_id", taskID, "agent_id", uuidToString(task.AgentID), "task_error", req.Error)
	writeJSON(w, http.StatusOK, taskToResponse(*task))
}

// ---------------------------------------------------------------------------
// Task Messages (live agent output)
// ---------------------------------------------------------------------------

type TaskMessageRequest struct {
	Seq     int            `json:"seq"`
	Type    string         `json:"type"`
	Tool    string         `json:"tool,omitempty"`
	Content string         `json:"content,omitempty"`
	Input   map[string]any `json:"input,omitempty"`
	Output  string         `json:"output,omitempty"`
}

type TaskMessageBatchRequest struct {
	Messages []TaskMessageRequest `json:"messages"`
}

// ReportTaskMessages receives a batch of agent execution messages from the daemon.
func (h *Handler) ReportTaskMessages(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	task, ok := h.requireDaemonTaskScope(w, r, taskID)
	if !ok {
		return
	}

	var req TaskMessageBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Messages) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	workspaceID := ""
	if issue, err := h.Queries.GetIssue(r.Context(), task.IssueID); err == nil {
		workspaceID = uuidToString(issue.WorkspaceID)
	}

	for _, msg := range req.Messages {
		// Redact sensitive information before persisting or broadcasting.
		msg.Content = redact.Text(msg.Content)
		msg.Output = redact.Text(msg.Output)
		msg.Input = redact.InputMap(msg.Input)

		var inputJSON []byte
		if msg.Input != nil {
			inputJSON, _ = json.Marshal(msg.Input)
		}
		h.Queries.CreateTaskMessage(r.Context(), db.CreateTaskMessageParams{
			TaskID:  parseUUID(taskID),
			Seq:     int32(msg.Seq),
			Type:    msg.Type,
			Tool:    pgtype.Text{String: msg.Tool, Valid: msg.Tool != ""},
			Content: pgtype.Text{String: msg.Content, Valid: msg.Content != ""},
			Input:   inputJSON,
			Output:  pgtype.Text{String: msg.Output, Valid: msg.Output != ""},
		})

		if workspaceID != "" {
			h.publish(protocol.EventTaskMessage, workspaceID, "system", "", protocol.TaskMessagePayload{
				TaskID:  taskID,
				IssueID: uuidToString(task.IssueID),
				Seq:     msg.Seq,
				Type:    msg.Type,
				Tool:    msg.Tool,
				Content: msg.Content,
				Input:   msg.Input,
				Output:  msg.Output,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ListTaskMessages returns the persisted messages for a task (for catch-up after reconnect).
func (h *Handler) ListTaskMessages(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	workspaceID, daemonID, ok := daemonScopeFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid daemon token")
		return
	}

	task, ok := h.requireDaemonTaskScope(w, r, taskID)
	if !ok {
		return
	}

	var (
		messages []db.TaskMessage
		err      error
	)
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		sinceSeq, parseErr := strconv.Atoi(sinceStr)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid since parameter")
			return
		}
		messages, err = h.Queries.ListTaskMessagesSinceForDaemonScope(r.Context(), db.ListTaskMessagesSinceForDaemonScopeParams{
			TaskID:      parseUUID(taskID),
			Seq:         int32(sinceSeq),
			WorkspaceID: parseUUID(workspaceID),
			DaemonID:    strToText(daemonID),
		})
	} else {
		messages, err = h.Queries.ListTaskMessagesForDaemonScope(r.Context(), db.ListTaskMessagesForDaemonScopeParams{
			TaskID:      parseUUID(taskID),
			WorkspaceID: parseUUID(workspaceID),
			DaemonID:    strToText(daemonID),
		})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list task messages")
		return
	}

	issueID := uuidToString(task.IssueID)

	resp := make([]protocol.TaskMessagePayload, len(messages))
	for i, m := range messages {
		var input map[string]any
		if m.Input != nil {
			json.Unmarshal(m.Input, &input)
		}
		resp[i] = protocol.TaskMessagePayload{
			TaskID:  taskID,
			IssueID: issueID,
			Seq:     int(m.Seq),
			Type:    m.Type,
			Tool:    m.Tool.String,
			Content: m.Content.String,
			Input:   input,
			Output:  m.Output.String,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) getIssueForWorkspace(w http.ResponseWriter, r *http.Request, issueID string) (*db.Issue, bool) {
	issue, err := h.Queries.GetIssue(r.Context(), parseUUID(issueID))
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found")
		return nil, false
	}
	if _, ok := h.requireWorkspaceMember(w, r, uuidToString(issue.WorkspaceID), "issue not found"); !ok {
		return nil, false
	}
	return &issue, true
}

// GetActiveTaskForIssue returns all currently active tasks for an issue.
// Returns { tasks: [...] } array (may be empty).
func (h *Handler) GetActiveTaskForIssue(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	if _, ok := h.getIssueForWorkspace(w, r, issueID); !ok {
		return
	}

	tasks, err := h.Queries.ListActiveTasksByIssue(r.Context(), parseUUID(issueID))
	if err != nil {
		tasks = nil
	}

	resp := make([]AgentTaskResponse, len(tasks))
	for i, t := range tasks {
		resp[i] = taskToResponse(t)
	}

	writeJSON(w, http.StatusOK, map[string]any{"tasks": resp})
}

// CancelTask cancels a running or queued task by ID.
func (h *Handler) CancelTask(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.getIssueForWorkspace(w, r, issueID)
	if !ok {
		return
	}

	taskID := chi.URLParam(r, "taskId")
	task, err := h.Queries.GetTaskByIssueForWorkspace(r.Context(), db.GetTaskByIssueForWorkspaceParams{
		ID:          parseUUID(taskID),
		IssueID:     parseUUID(issueID),
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to validate task")
		return
	}

	cancelled, err := h.TaskService.CancelTask(r.Context(), task.ID)
	if err != nil {
		slog.Warn("cancel task failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("task cancelled by user", "task_id", taskID, "issue_id", uuidToString(cancelled.IssueID))
	writeJSON(w, http.StatusOK, taskToResponse(*cancelled))
}

// ListTasksByIssue returns all tasks (any status) for an issue — used for execution history.
func (h *Handler) ListTasksByIssue(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	if _, ok := h.getIssueForWorkspace(w, r, issueID); !ok {
		return
	}

	tasks, err := h.Queries.ListTasksByIssue(r.Context(), parseUUID(issueID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}

	resp := make([]AgentTaskResponse, len(tasks))
	for i, t := range tasks {
		resp[i] = taskToResponse(t)
	}

	writeJSON(w, http.StatusOK, resp)
}
