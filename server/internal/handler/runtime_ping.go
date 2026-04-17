package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// PingStatus represents the lifecycle of a runtime ping test.
type PingStatus string

const (
	PingPending   PingStatus = "pending"
	PingRunning   PingStatus = "running"
	PingCompleted PingStatus = "completed"
	PingFailed    PingStatus = "failed"
	PingTimeout   PingStatus = "timeout"
)

// PingRequest represents a pending or completed ping test.
type PingRequest struct {
	ID         string     `json:"id"`
	RuntimeID  string     `json:"runtime_id"`
	Status     PingStatus `json:"status"`
	Output     string     `json:"output,omitempty"`
	Error      string     `json:"error,omitempty"`
	DurationMs int64      `json:"duration_ms,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func runtimePingResponse(req db.RuntimePingRequest) PingRequest {
	resp := PingRequest{
		ID:        uuidToString(req.ID),
		RuntimeID: uuidToString(req.RuntimeID),
		Status:    PingStatus(req.Status),
		Output:    req.Output,
		Error:     req.Error,
		CreatedAt: req.CreatedAt.Time,
		UpdatedAt: req.UpdatedAt.Time,
	}
	if req.DurationMs.Valid {
		resp.DurationMs = req.DurationMs.Int64
	}
	return resp
}

// InitiatePing creates a new ping request (protected route, called by frontend).
func (h *Handler) InitiatePing(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")

	rt, err := h.Queries.GetAgentRuntime(r.Context(), parseUUID(runtimeID))
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}

	if _, ok := h.requireWorkspaceMember(w, r, uuidToString(rt.WorkspaceID), "runtime not found"); !ok {
		return
	}
	if !rt.DaemonID.Valid || rt.DaemonID.String == "" {
		writeError(w, http.StatusConflict, "runtime is not bound to a daemon")
		return
	}

	ping, err := h.Queries.CreateRuntimePingRequest(r.Context(), db.CreateRuntimePingRequestParams{
		WorkspaceID: rt.WorkspaceID,
		RuntimeID:   rt.ID,
		DaemonID:    rt.DaemonID.String,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create ping request")
		return
	}

	writeJSON(w, http.StatusOK, runtimePingResponse(ping))
}

// GetPing returns the status of a ping request (protected route, called by frontend).
func (h *Handler) GetPing(w http.ResponseWriter, r *http.Request) {
	pingID := chi.URLParam(r, "pingId")
	if _, err := h.Queries.TimeoutStaleRuntimePingRequests(r.Context(), 60); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load ping")
		return
	}

	ping, err := h.Queries.GetRuntimePingRequest(r.Context(), parseUUID(pingID))
	if err != nil {
		writeError(w, http.StatusNotFound, "ping not found")
		return
	}
	if uuidToString(ping.RuntimeID) != chi.URLParam(r, "runtimeId") {
		writeError(w, http.StatusNotFound, "ping not found")
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, uuidToString(ping.WorkspaceID), "ping not found"); !ok {
		return
	}

	writeJSON(w, http.StatusOK, runtimePingResponse(ping))
}

// ReportPingResult receives the ping result from the daemon.
func (h *Handler) ReportPingResult(w http.ResponseWriter, r *http.Request) {
	pingID := chi.URLParam(r, "pingId")
	runtimeID := chi.URLParam(r, "runtimeId")
	daemonID := middleware.DaemonIDFromContext(r.Context())
	workspaceID := middleware.DaemonWorkspaceIDFromContext(r.Context())
	if daemonID == "" || workspaceID == "" {
		writeError(w, http.StatusUnauthorized, "daemon not authenticated")
		return
	}

	ping, err := h.Queries.GetRuntimePingRequestForDaemon(r.Context(), db.GetRuntimePingRequestForDaemonParams{
		ID:          parseUUID(pingID),
		DaemonID:    daemonID,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil || uuidToString(ping.RuntimeID) != runtimeID {
		writeError(w, http.StatusNotFound, "ping not found")
		return
	}

	var req struct {
		Status     string `json:"status"`
		Output     string `json:"output"`
		Error      string `json:"error"`
		DurationMs int64  `json:"duration_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	paramsBase := struct {
		ID          pgtype.UUID
		DaemonID    string
		WorkspaceID pgtype.UUID
	}{
		ID:          parseUUID(pingID),
		DaemonID:    daemonID,
		WorkspaceID: parseUUID(workspaceID),
	}

	switch req.Status {
	case "completed":
		_, err := h.Queries.CompleteRuntimePingRequestForDaemon(r.Context(), db.CompleteRuntimePingRequestForDaemonParams{
			ID:          paramsBase.ID,
			DaemonID:    paramsBase.DaemonID,
			WorkspaceID: paramsBase.WorkspaceID,
			Output:      req.Output,
			DurationMs:  pgtype.Int8{Int64: req.DurationMs, Valid: true},
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "ping not found")
			return
		}
	case "failed":
		_, err := h.Queries.FailRuntimePingRequestForDaemon(r.Context(), db.FailRuntimePingRequestForDaemonParams{
			ID:          paramsBase.ID,
			DaemonID:    paramsBase.DaemonID,
			WorkspaceID: paramsBase.WorkspaceID,
			Error:       req.Error,
			DurationMs:  pgtype.Int8{Int64: req.DurationMs, Valid: true},
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "ping not found")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid status: "+req.Status)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
