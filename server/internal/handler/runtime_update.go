package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
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

var errUpdateInProgress = &updateError{msg: "an update is already in progress for this runtime"}

type updateError struct{ msg string }

func (e *updateError) Error() string { return e.msg }

func runtimeUpdateToRequest(u db.RuntimeUpdate) UpdateRequest {
	return UpdateRequest{
		ID:            uuidToString(u.ID),
		RuntimeID:     uuidToString(u.RuntimeID),
		Status:        UpdateStatus(u.Status),
		TargetVersion: u.TargetVersion,
		Output:        u.Output,
		Error:         u.Error,
		CreatedAt:     u.CreatedAt.Time,
		UpdatedAt:     u.UpdatedAt.Time,
	}
}

func (h *Handler) getUpdateRequest(ctx context.Context, updateID string) (*UpdateRequest, error) {
	update, err := h.Queries.GetRuntimeUpdate(ctx, parseUUID(updateID))
	if err != nil {
		return nil, err
	}
	if (update.Status == string(UpdatePending) || update.Status == string(UpdateRunning)) && update.CreatedAt.Valid && time.Since(update.CreatedAt.Time) > 120*time.Second {
		timedOut, err := h.Queries.SetRuntimeUpdateTimeoutForDaemon(ctx, db.SetRuntimeUpdateTimeoutForDaemonParams{
			ID:          update.ID,
			RuntimeID:   update.RuntimeID,
			WorkspaceID: update.WorkspaceID,
			DaemonID:    update.DaemonID,
		})
		if err == nil {
			update = timedOut
		} else if !isNotFound(err) {
			return nil, err
		} else {
			update, err = h.Queries.GetRuntimeUpdate(ctx, update.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	result := runtimeUpdateToRequest(update)
	return &result, nil
}

func (h *Handler) popPendingUpdateRequest(ctx context.Context, runtimeID, workspaceID, daemonID string) (*UpdateRequest, error) {
	items, err := h.Queries.PopPendingRuntimeUpdateForDaemon(ctx, db.PopPendingRuntimeUpdateForDaemonParams{
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    daemonID,
	})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	result := runtimeUpdateToRequest(items[0])
	return &result, nil
}

func (h *Handler) getUpdateRequestForWorkspace(ctx context.Context, updateID, runtimeID, workspaceID string) (*UpdateRequest, error) {
	update, err := h.Queries.GetRuntimeUpdateForWorkspace(ctx, db.GetRuntimeUpdateForWorkspaceParams{
		ID:          parseUUID(updateID),
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		return nil, err
	}
	if (update.Status == string(UpdatePending) || update.Status == string(UpdateRunning)) && update.CreatedAt.Valid && time.Since(update.CreatedAt.Time) > 120*time.Second {
		timedOut, err := h.Queries.SetRuntimeUpdateTimeoutForDaemon(ctx, db.SetRuntimeUpdateTimeoutForDaemonParams{
			ID:          update.ID,
			RuntimeID:   update.RuntimeID,
			WorkspaceID: update.WorkspaceID,
			DaemonID:    update.DaemonID,
		})
		if err == nil {
			update = timedOut
		} else if !isNotFound(err) {
			return nil, err
		} else {
			update, err = h.Queries.GetRuntimeUpdateForWorkspace(ctx, db.GetRuntimeUpdateForWorkspaceParams{
				ID:          update.ID,
				RuntimeID:   update.RuntimeID,
				WorkspaceID: update.WorkspaceID,
			})
			if err != nil {
				return nil, err
			}
		}
	}
	result := runtimeUpdateToRequest(update)
	return &result, nil
}

func (h *Handler) getUpdateRequestForDaemon(ctx context.Context, updateID, runtimeID, workspaceID, daemonID string) (*UpdateRequest, error) {
	update, err := h.Queries.GetRuntimeUpdateForDaemon(ctx, db.GetRuntimeUpdateForDaemonParams{
		ID:          parseUUID(updateID),
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    daemonID,
	})
	if err != nil {
		return nil, err
	}
	result := runtimeUpdateToRequest(update)
	return &result, nil
}

func (h *Handler) completeUpdateRequestForDaemon(ctx context.Context, updateID, runtimeID, workspaceID, daemonID, output string) error {
	_, err := h.Queries.SetRuntimeUpdateCompletedForDaemon(ctx, db.SetRuntimeUpdateCompletedForDaemonParams{
		ID:          parseUUID(updateID),
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    daemonID,
		Output:      output,
	})
	return err
}

func (h *Handler) failUpdateRequestForDaemon(ctx context.Context, updateID, runtimeID, workspaceID, daemonID, errMsg string) error {
	_, err := h.Queries.SetRuntimeUpdateFailedForDaemon(ctx, db.SetRuntimeUpdateFailedForDaemonParams{
		ID:          parseUUID(updateID),
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    daemonID,
		Error:       errMsg,
	})
	return err
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
	if !rt.DaemonID.Valid {
		writeError(w, http.StatusConflict, "runtime is not currently attached to a daemon")
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

	update, err := h.Queries.CreateRuntimeUpdate(r.Context(), db.CreateRuntimeUpdateParams{
		RuntimeID:     parseUUID(runtimeID),
		WorkspaceID:   rt.WorkspaceID,
		DaemonID:      rt.DaemonID.String,
		TargetVersion: req.TargetVersion,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, errUpdateInProgress.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create update")
		return
	}

	writeJSON(w, http.StatusOK, runtimeUpdateToRequest(update))
}

// GetUpdate returns the status of an update request (protected route, called by frontend).
func (h *Handler) GetUpdate(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	updateID := chi.URLParam(r, "updateId")

	rt, err := h.Queries.GetAgentRuntime(r.Context(), parseUUID(runtimeID))
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, uuidToString(rt.WorkspaceID), "runtime not found"); !ok {
		return
	}

	update, err := h.getUpdateRequestForWorkspace(r.Context(), updateID, runtimeID, uuidToString(rt.WorkspaceID))
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "update not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load update")
		return
	}

	writeJSON(w, http.StatusOK, update)
}

// ReportUpdateResult receives the update result from the daemon.
func (h *Handler) ReportUpdateResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeScope(w, r, runtimeID); !ok {
		return
	}

	updateID := chi.URLParam(r, "updateId")
	workspaceID, daemonID, ok := daemonScopeFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid daemon token")
		return
	}

	_, loadErr := h.getUpdateRequestForDaemon(r.Context(), updateID, runtimeID, workspaceID, daemonID)
	if loadErr != nil {
		if isNotFound(loadErr) {
			writeError(w, http.StatusNotFound, "update not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load update")
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

	var err error
	switch req.Status {
	case "completed":
		err = h.completeUpdateRequestForDaemon(r.Context(), updateID, runtimeID, workspaceID, daemonID, req.Output)
	case "failed":
		err = h.failUpdateRequestForDaemon(r.Context(), updateID, runtimeID, workspaceID, daemonID, req.Error)
	case "running":
		err = nil
	default:
		writeError(w, http.StatusBadRequest, "invalid status: "+req.Status)
		return
	}
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "update not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to store update result")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
