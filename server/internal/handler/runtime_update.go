package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type UpdateStatus string

const (
	UpdatePending   UpdateStatus = "pending"
	UpdateRunning   UpdateStatus = "running"
	UpdateCompleted UpdateStatus = "completed"
	UpdateFailed    UpdateStatus = "failed"
	UpdateTimeout   UpdateStatus = "timeout"
)

const errUpdateInProgress = "an update is already in progress for this runtime"

// UpdateRequest represents a pending or completed CLI update request.
type UpdateRequest struct {
	ID            string       `json:"id"`
	RuntimeID     string       `json:"runtime_id"`
	Status        UpdateStatus `json:"status"`
	TargetVersion string       `json:"target_version"`
	Output        string       `json:"output,omitempty"`
	Error         string       `json:"error,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

func runtimeUpdateResponse(req db.RuntimeUpdateRequest) UpdateRequest {
	return UpdateRequest{
		ID:            uuidToString(req.ID),
		RuntimeID:     uuidToString(req.RuntimeID),
		Status:        UpdateStatus(req.Status),
		TargetVersion: req.TargetVersion,
		Output:        req.Output,
		Error:         req.Error,
		CreatedAt:     req.CreatedAt.Time,
		UpdatedAt:     req.UpdatedAt.Time,
	}
}

// InitiateUpdate creates a new CLI update request (protected route, called by frontend).
func (h *Handler) InitiateUpdate(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")

	rt, err := h.Queries.GetAgentRuntime(r.Context(), parseUUID(runtimeID))
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}

	if _, ok := h.requireWorkspaceMember(w, r, uuidToString(rt.WorkspaceID), "runtime not found"); !ok {
		return
	}

	var req struct {
		TargetVersion string `json:"target_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TargetVersion == "" {
		writeError(w, http.StatusBadRequest, "target_version is required")
		return
	}
	if !rt.DaemonID.Valid || rt.DaemonID.String == "" {
		writeError(w, http.StatusConflict, "runtime is not bound to a daemon")
		return
	}

	activeCount, err := h.Queries.CountActiveRuntimeUpdateRequests(r.Context(), rt.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check update status")
		return
	}
	if activeCount > 0 {
		writeError(w, http.StatusConflict, errUpdateInProgress)
		return
	}

	update, err := h.Queries.CreateRuntimeUpdateRequest(r.Context(), db.CreateRuntimeUpdateRequestParams{
		WorkspaceID:   rt.WorkspaceID,
		RuntimeID:     rt.ID,
		DaemonID:      rt.DaemonID.String,
		TargetVersion: req.TargetVersion,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create update request")
		return
	}

	writeJSON(w, http.StatusOK, runtimeUpdateResponse(update))
}

// GetUpdate returns the status of an update request (protected route, called by frontend).
func (h *Handler) GetUpdate(w http.ResponseWriter, r *http.Request) {
	updateID := chi.URLParam(r, "updateId")
	if _, err := h.Queries.TimeoutStaleRuntimeUpdateRequests(r.Context(), 120); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load update")
		return
	}

	update, err := h.Queries.GetRuntimeUpdateRequest(r.Context(), parseUUID(updateID))
	if err != nil {
		writeError(w, http.StatusNotFound, "update not found")
		return
	}
	if uuidToString(update.RuntimeID) != chi.URLParam(r, "runtimeId") {
		writeError(w, http.StatusNotFound, "update not found")
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, uuidToString(update.WorkspaceID), "update not found"); !ok {
		return
	}

	writeJSON(w, http.StatusOK, runtimeUpdateResponse(update))
}

// ReportUpdateResult receives the update result from the daemon.
func (h *Handler) ReportUpdateResult(w http.ResponseWriter, r *http.Request) {
	updateID := chi.URLParam(r, "updateId")
	runtimeID := chi.URLParam(r, "runtimeId")
	daemonID := middleware.DaemonIDFromContext(r.Context())
	workspaceID := middleware.DaemonWorkspaceIDFromContext(r.Context())
	if daemonID == "" || workspaceID == "" {
		writeError(w, http.StatusUnauthorized, "daemon not authenticated")
		return
	}

	update, err := h.Queries.GetRuntimeUpdateRequestForDaemon(r.Context(), db.GetRuntimeUpdateRequestForDaemonParams{
		ID:          parseUUID(updateID),
		DaemonID:    daemonID,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil || uuidToString(update.RuntimeID) != runtimeID {
		writeError(w, http.StatusNotFound, "update not found")
		return
	}

	var req struct {
		Status string `json:"status"`
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	switch req.Status {
	case "completed":
		_, err := h.Queries.CompleteRuntimeUpdateRequestForDaemon(r.Context(), db.CompleteRuntimeUpdateRequestForDaemonParams{
			ID:          parseUUID(updateID),
			DaemonID:    daemonID,
			WorkspaceID: parseUUID(workspaceID),
			Output:      req.Output,
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "update not found")
			return
		}
	case "failed":
		_, err := h.Queries.FailRuntimeUpdateRequestForDaemon(r.Context(), db.FailRuntimeUpdateRequestForDaemonParams{
			ID:          parseUUID(updateID),
			DaemonID:    daemonID,
			WorkspaceID: parseUUID(workspaceID),
			Error:       req.Error,
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "update not found")
			return
		}
	case "running":
	default:
		writeError(w, http.StatusBadRequest, "invalid status: "+req.Status)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
