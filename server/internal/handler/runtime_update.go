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

func (h *Handler) getUpdateRequest(ctx context.Context, runtimeID, updateID string) (*UpdateRequest, error) {
	update, err := h.Queries.GetRuntimeUpdateForRuntime(ctx, db.GetRuntimeUpdateForRuntimeParams{
		ID:        parseUUID(updateID),
		RuntimeID: parseUUID(runtimeID),
	})
	if err != nil {
		return nil, err
	}
	if (update.Status == string(UpdatePending) || update.Status == string(UpdateRunning)) && update.CreatedAt.Valid && time.Since(update.CreatedAt.Time) > 120*time.Second {
		timedOut, err := h.Queries.SetRuntimeUpdateTimeout(ctx, update.ID)
		if err == nil {
			update = timedOut
		} else if !isNotFound(err) {
			return nil, err
		} else {
			update, err = h.Queries.GetRuntimeUpdateForRuntime(ctx, db.GetRuntimeUpdateForRuntimeParams{
				ID:        update.ID,
				RuntimeID: parseUUID(runtimeID),
			})
			if err != nil {
				return nil, err
			}
		}
	}
	result := runtimeUpdateToRequest(update)
	return &result, nil
}

func (h *Handler) popPendingUpdateRequest(ctx context.Context, runtimeID string) (*UpdateRequest, error) {
	items, err := h.Queries.PopPendingRuntimeUpdate(ctx, parseUUID(runtimeID))
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	result := runtimeUpdateToRequest(items[0])
	return &result, nil
}

func (h *Handler) completeUpdateRequest(ctx context.Context, runtimeID, updateID, output string) error {
	_, err := h.Queries.SetRuntimeUpdateCompletedForRuntime(ctx, db.SetRuntimeUpdateCompletedForRuntimeParams{
		ID:        parseUUID(updateID),
		RuntimeID: parseUUID(runtimeID),
		Output:    output,
	})
	return err
}

func (h *Handler) failUpdateRequest(ctx context.Context, runtimeID, updateID, errMsg string) error {
	_, err := h.Queries.SetRuntimeUpdateFailedForRuntime(ctx, db.SetRuntimeUpdateFailedForRuntimeParams{
		ID:        parseUUID(updateID),
		RuntimeID: parseUUID(runtimeID),
		Error:     errMsg,
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

	update, err := h.getUpdateRequest(r.Context(), runtimeID, updateID)
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
	if _, ok := h.loadDaemonRuntime(r, runtimeID); !ok {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}

	updateID := chi.URLParam(r, "updateId")

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
		err = h.completeUpdateRequest(r.Context(), runtimeID, updateID, req.Output)
	case "failed":
		err = h.failUpdateRequest(r.Context(), runtimeID, updateID, req.Error)
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
