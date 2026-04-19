package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type txStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type dbExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Handler struct {
	Queries              *db.Queries
	DB                   dbExecutor
	TxStarter            txStarter
	Hub                  *realtime.Hub
	Bus                  *events.Bus
	TaskService          *service.TaskService
	OrchestrationService *service.OrchestrationService
	EmailService         *service.EmailService
	Storage              *storage.S3Storage
	CFSigner             *auth.CloudFrontSigner
}

func New(queries *db.Queries, txStarter txStarter, hub *realtime.Hub, bus *events.Bus, emailService *service.EmailService, s3 *storage.S3Storage, cfSigner *auth.CloudFrontSigner) *Handler {
	var executor dbExecutor
	if candidate, ok := txStarter.(dbExecutor); ok {
		executor = candidate
	}

	var orchestrationService *service.OrchestrationService
	if pool, ok := txStarter.(*pgxpool.Pool); ok {
		orchestrationService = service.NewOrchestrationService(pool, queries, hub, bus)
	}

	return &Handler{
		Queries:              queries,
		DB:                   executor,
		TxStarter:            txStarter,
		Hub:                  hub,
		Bus:                  bus,
		TaskService:          service.NewTaskService(queries, hub, bus),
		OrchestrationService: orchestrationService,
		EmailService:         emailService,
		Storage:              s3,
		CFSigner:             cfSigner,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// Thin wrappers around util functions (preserve existing handler code unchanged).
func parseUUID(s string) pgtype.UUID                { return util.ParseUUID(s) }
func uuidToString(u pgtype.UUID) string             { return util.UUIDToString(u) }
func textToPtr(t pgtype.Text) *string               { return util.TextToPtr(t) }
func ptrToText(s *string) pgtype.Text               { return util.PtrToText(s) }
func strToText(s string) pgtype.Text                { return util.StrToText(s) }
func timestampToString(t pgtype.Timestamptz) string { return util.TimestampToString(t) }
func timestampToPtr(t pgtype.Timestamptz) *string   { return util.TimestampToPtr(t) }
func uuidToPtr(u pgtype.UUID) *string               { return util.UUIDToPtr(u) }

// publish sends a domain event through the event bus.
func (h *Handler) publish(eventType, workspaceID, actorType, actorID string, payload any) {
	h.Bus.Publish(events.Event{
		Type:        eventType,
		WorkspaceID: workspaceID,
		ActorType:   actorType,
		ActorID:     actorID,
		Payload:     payload,
	})
}

func isNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func requestUserID(r *http.Request) string {
	return r.Header.Get("X-User-ID")
}

func resolveMemberActor(userID string) (actorType, actorID string) {
	return "member", userID
}

func (h *Handler) resolveVerifiedAgentActorFromTask(ctx context.Context, taskID, agentID, workspaceID string) (actorType, actorID string, ok bool) {
	if taskID == "" || agentID == "" || workspaceID == "" {
		return "", "", false
	}

	task, err := h.Queries.GetAgentTask(ctx, parseUUID(taskID))
	if err != nil || uuidToString(task.AgentID) != agentID {
		return "", "", false
	}

	agent, err := h.Queries.GetAgent(ctx, parseUUID(agentID))
	if err != nil || uuidToString(agent.WorkspaceID) != workspaceID {
		return "", "", false
	}

	return "agent", agentID, true
}

func daemonScopeFromRequest(r *http.Request) (string, string, bool) {
	workspaceID := strings.TrimSpace(middleware.DaemonWorkspaceIDFromContext(r.Context()))
	daemonID := strings.TrimSpace(middleware.DaemonIDFromContext(r.Context()))
	if workspaceID == "" || daemonID == "" {
		return "", "", false
	}
	return workspaceID, daemonID, true
}

func (h *Handler) requireDaemonRuntimeScope(w http.ResponseWriter, r *http.Request, runtimeID string) (*db.AgentRuntime, bool) {
	workspaceID, daemonID, ok := daemonScopeFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid daemon token")
		return nil, false
	}
	runtime, err := h.Queries.GetAgentRuntimeForDaemonScope(r.Context(), db.GetAgentRuntimeForDaemonScopeParams{
		ID:          parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    strToText(daemonID),
	})
	if err == nil {
		return &runtime, true
	}
	if isNotFound(err) {
		writeError(w, http.StatusForbidden, "runtime not owned by daemon")
		return nil, false
	}
	slog.Error("failed to validate runtime scope", "runtime_id", runtimeID, "workspace_id", workspaceID, "daemon_id", daemonID, "error", err)
	writeError(w, http.StatusInternalServerError, "failed to validate runtime scope")
	return nil, false
}

func (h *Handler) requireDaemonTaskScope(w http.ResponseWriter, r *http.Request, taskID string) (*db.AgentTaskQueue, bool) {
	workspaceID, daemonID, ok := daemonScopeFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid daemon token")
		return nil, false
	}
	task, err := h.Queries.GetAgentTaskForDaemonScope(r.Context(), db.GetAgentTaskForDaemonScopeParams{
		ID:          parseUUID(taskID),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    strToText(daemonID),
	})
	if err == nil {
		return &task, true
	}
	if isNotFound(err) {
		writeError(w, http.StatusForbidden, "task not owned by daemon")
		return nil, false
	}
	slog.Error("failed to validate task scope", "task_id", taskID, "workspace_id", workspaceID, "daemon_id", daemonID, "error", err)
	writeError(w, http.StatusInternalServerError, "failed to validate task scope")
	return nil, false
}

// resolveActor now resolves only the authenticated member identity.
// Verified agent actor resolution must go through explicit task-bound helpers.
func (h *Handler) resolveActor(_ *http.Request, userID, _ string) (actorType, actorID string) {
	return resolveMemberActor(userID)
}

func requireUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := requestUserID(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "user not authenticated")
		return "", false
	}
	return userID, true
}

func resolveWorkspaceID(r *http.Request) string {
	// Prefer context value set by workspace middleware.
	if id := middleware.WorkspaceIDFromContext(r.Context()); id != "" {
		return id
	}
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID != "" {
		return workspaceID
	}
	return r.Header.Get("X-Workspace-ID")
}

// ctxMember returns the workspace member from context (set by workspace middleware).
func ctxMember(ctx context.Context) (db.Member, bool) {
	return middleware.MemberFromContext(ctx)
}

// ctxWorkspaceID returns the workspace ID from context (set by workspace middleware).
func ctxWorkspaceID(ctx context.Context) string {
	return middleware.WorkspaceIDFromContext(ctx)
}

// workspaceIDFromURL returns the workspace ID from context (preferred) or chi URL param (fallback).
func workspaceIDFromURL(r *http.Request, param string) string {
	if id := middleware.WorkspaceIDFromContext(r.Context()); id != "" {
		return id
	}
	return chi.URLParam(r, param)
}

// workspaceMember returns the member from middleware context, or falls back to a DB
// lookup when the handler is called directly (e.g. in tests).
func (h *Handler) workspaceMember(w http.ResponseWriter, r *http.Request, workspaceID string) (db.Member, bool) {
	if m, ok := ctxMember(r.Context()); ok {
		return m, true
	}
	return h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
}

func roleAllowed(role string, roles ...string) bool {
	for _, candidate := range roles {
		if role == candidate {
			return true
		}
	}
	return false
}

func countOwners(members []db.Member) int {
	owners := 0
	for _, member := range members {
		if member.Role == "owner" {
			owners++
		}
	}
	return owners
}

func (h *Handler) getWorkspaceMember(ctx context.Context, userID, workspaceID string) (db.Member, error) {
	return h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      parseUUID(userID),
		WorkspaceID: parseUUID(workspaceID),
	})
}

func (h *Handler) requireWorkspaceMember(w http.ResponseWriter, r *http.Request, workspaceID, notFoundMsg string) (db.Member, bool) {
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Member{}, false
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return db.Member{}, false
	}

	member, err := h.getWorkspaceMember(r.Context(), userID, workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, notFoundMsg)
		return db.Member{}, false
	}

	return member, true
}

func (h *Handler) requireWorkspaceRole(w http.ResponseWriter, r *http.Request, workspaceID, notFoundMsg string, roles ...string) (db.Member, bool) {
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, notFoundMsg)
	if !ok {
		return db.Member{}, false
	}
	if !roleAllowed(member.Role, roles...) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return db.Member{}, false
	}
	return member, true
}

func (h *Handler) loadIssueForUser(w http.ResponseWriter, r *http.Request, issueID string) (db.Issue, bool) {
	if _, ok := requireUserID(w, r); !ok {
		return db.Issue{}, false
	}

	workspaceID := resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Issue{}, false
	}

	// Try identifier format first (e.g., "JIA-42").
	if issue, ok := h.resolveIssueByIdentifier(r.Context(), issueID, workspaceID); ok {
		return issue, true
	}

	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          parseUUID(issueID),
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found")
		return db.Issue{}, false
	}
	return issue, true
}

// resolveIssueByIdentifier tries to look up an issue by "PREFIX-NUMBER" format.
func (h *Handler) resolveIssueByIdentifier(ctx context.Context, id, workspaceID string) (db.Issue, bool) {
	parts := splitIdentifier(id)
	if parts == nil {
		return db.Issue{}, false
	}
	if workspaceID == "" {
		return db.Issue{}, false
	}
	issue, err := h.Queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{
		WorkspaceID: parseUUID(workspaceID),
		Number:      parts.number,
	})
	if err != nil {
		return db.Issue{}, false
	}
	return issue, true
}

type identifierParts struct {
	prefix string
	number int32
}

func splitIdentifier(id string) *identifierParts {
	idx := -1
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] == '-' {
			idx = i
			break
		}
	}
	if idx <= 0 || idx >= len(id)-1 {
		return nil
	}
	numStr := id[idx+1:]
	num := 0
	for _, c := range numStr {
		if c < '0' || c > '9' {
			return nil
		}
		num = num*10 + int(c-'0')
	}
	if num <= 0 {
		return nil
	}
	return &identifierParts{prefix: id[:idx], number: int32(num)}
}

// getIssuePrefix fetches the issue_prefix for a workspace.
// Falls back to generating a prefix from the workspace name if the stored
// prefix is empty (e.g. workspaces created before the prefix was introduced).
func (h *Handler) getIssuePrefix(ctx context.Context, workspaceID pgtype.UUID) string {
	ws, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return ""
	}
	if ws.IssuePrefix != "" {
		return ws.IssuePrefix
	}
	return generateIssuePrefix(ws.Name)
}

func (h *Handler) loadAgentForUser(w http.ResponseWriter, r *http.Request, agentID string) (db.Agent, bool) {
	if _, ok := requireUserID(w, r); !ok {
		return db.Agent{}, false
	}

	workspaceID := resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Agent{}, false
	}

	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          parseUUID(agentID),
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return db.Agent{}, false
	}
	return agent, true
}

func (h *Handler) loadInboxItemForUser(w http.ResponseWriter, r *http.Request, itemID string) (db.InboxItem, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.InboxItem{}, false
	}

	workspaceID := resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.InboxItem{}, false
	}

	item, err := h.Queries.GetInboxItemInWorkspace(r.Context(), db.GetInboxItemInWorkspaceParams{
		ID:          parseUUID(itemID),
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "inbox item not found")
		return db.InboxItem{}, false
	}

	if item.RecipientType != "member" || uuidToString(item.RecipientID) != userID {
		writeError(w, http.StatusNotFound, "inbox item not found")
		return db.InboxItem{}, false
	}
	return item, true
}
