package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

func randomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func runtimePingToRequest(p db.RuntimePing) PingRequest {
	var durationMs int64
	if p.DurationMs.Valid {
		durationMs = p.DurationMs.Int64
	}
	return PingRequest{
		ID:         uuidToString(p.ID),
		RuntimeID:  uuidToString(p.RuntimeID),
		Status:     PingStatus(p.Status),
		Output:     p.Output,
		Error:      p.Error,
		DurationMs: durationMs,
		CreatedAt:  p.CreatedAt.Time,
		UpdatedAt:  p.UpdatedAt.Time,
	}
}

func (h *Handler) getPingRequest(ctx context.Context, pingID string) (*PingRequest, error) {
	ping, err := h.Queries.GetRuntimePing(ctx, parseUUID(pingID))
	if err != nil {
		return nil, err
	}
	if (ping.Status == string(PingPending) || ping.Status == string(PingRunning)) && ping.CreatedAt.Valid && time.Since(ping.CreatedAt.Time) > 60*time.Second {
		timedOut, err := h.Queries.SetRuntimePingTimeoutForDaemon(ctx, db.SetRuntimePingTimeoutForDaemonParams{
			ID:          ping.ID,
			RuntimeID:   ping.RuntimeID,
			WorkspaceID: ping.WorkspaceID,
			DaemonID:    ping.DaemonID,
		})
		if err == nil {
			ping = timedOut
		} else if !isNotFound(err) {
			return nil, err
		} else {
			ping, err = h.Queries.GetRuntimePing(ctx, ping.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	result := runtimePingToRequest(ping)
	return &result, nil
}

func (h *Handler) popPendingPingRequest(ctx context.Context, runtimeID, workspaceID, daemonID string) (*PingRequest, error) {
	items, err := h.Queries.PopPendingRuntimePingForDaemon(ctx, db.PopPendingRuntimePingForDaemonParams{
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
	result := runtimePingToRequest(items[0])
	return &result, nil
}

func (h *Handler) getPingRequestForWorkspace(ctx context.Context, pingID, runtimeID, workspaceID string) (*PingRequest, error) {
	ping, err := h.Queries.GetRuntimePingForWorkspace(ctx, db.GetRuntimePingForWorkspaceParams{
		ID:          parseUUID(pingID),
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		return nil, err
	}
	if (ping.Status == string(PingPending) || ping.Status == string(PingRunning)) && ping.CreatedAt.Valid && time.Since(ping.CreatedAt.Time) > 60*time.Second {
		timedOut, err := h.Queries.SetRuntimePingTimeoutForDaemon(ctx, db.SetRuntimePingTimeoutForDaemonParams{
			ID:          ping.ID,
			RuntimeID:   ping.RuntimeID,
			WorkspaceID: ping.WorkspaceID,
			DaemonID:    ping.DaemonID,
		})
		if err == nil {
			ping = timedOut
		} else if !isNotFound(err) {
			return nil, err
		} else {
			ping, err = h.Queries.GetRuntimePingForWorkspace(ctx, db.GetRuntimePingForWorkspaceParams{
				ID:          ping.ID,
				RuntimeID:   ping.RuntimeID,
				WorkspaceID: ping.WorkspaceID,
			})
			if err != nil {
				return nil, err
			}
		}
	}
	result := runtimePingToRequest(ping)
	return &result, nil
}

func (h *Handler) getPingRequestForDaemon(ctx context.Context, pingID, runtimeID, workspaceID, daemonID string) (*PingRequest, error) {
	ping, err := h.Queries.GetRuntimePingForDaemon(ctx, db.GetRuntimePingForDaemonParams{
		ID:          parseUUID(pingID),
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    daemonID,
	})
	if err != nil {
		return nil, err
	}
	result := runtimePingToRequest(ping)
	return &result, nil
}

func (h *Handler) completePingRequestForDaemon(ctx context.Context, pingID, runtimeID, workspaceID, daemonID, output string, durationMs int64) error {
	_, err := h.Queries.SetRuntimePingCompletedForDaemon(ctx, db.SetRuntimePingCompletedForDaemonParams{
		ID:          parseUUID(pingID),
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    daemonID,
		Output:      output,
		DurationMs:  pgtype.Int8{Int64: durationMs, Valid: true},
	})
	return err
}

func (h *Handler) failPingRequestForDaemon(ctx context.Context, pingID, runtimeID, workspaceID, daemonID, errMsg string, durationMs int64) error {
	_, err := h.Queries.SetRuntimePingFailedForDaemon(ctx, db.SetRuntimePingFailedForDaemonParams{
		ID:          parseUUID(pingID),
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    daemonID,
		Error:       errMsg,
		DurationMs:  pgtype.Int8{Int64: durationMs, Valid: true},
	})
	return err
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
	if !rt.DaemonID.Valid {
		writeError(w, http.StatusConflict, "runtime is not currently attached to a daemon")
		return
	}

	ping, err := h.Queries.CreateRuntimePing(r.Context(), db.CreateRuntimePingParams{
		RuntimeID:   parseUUID(runtimeID),
		WorkspaceID: rt.WorkspaceID,
		DaemonID:    rt.DaemonID.String,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create ping")
		return
	}

	writeJSON(w, http.StatusOK, runtimePingToRequest(ping))
}

// GetPing returns the status of a ping request (protected route, called by frontend).
func (h *Handler) GetPing(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	pingID := chi.URLParam(r, "pingId")

	rt, err := h.Queries.GetAgentRuntime(r.Context(), parseUUID(runtimeID))
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, uuidToString(rt.WorkspaceID), "runtime not found"); !ok {
		return
	}

	ping, err := h.getPingRequestForWorkspace(r.Context(), pingID, runtimeID, uuidToString(rt.WorkspaceID))
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "ping not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load ping")
		return
	}

	writeJSON(w, http.StatusOK, ping)
}

// ReportPingResult receives the ping result from the daemon.
func (h *Handler) ReportPingResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeScope(w, r, runtimeID); !ok {
		return
	}

	pingID := chi.URLParam(r, "pingId")
	workspaceID, daemonID, ok := daemonScopeFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid daemon token")
		return
	}

	_, loadErr := h.getPingRequestForDaemon(r.Context(), pingID, runtimeID, workspaceID, daemonID)
	if loadErr != nil {
		if isNotFound(loadErr) {
			writeError(w, http.StatusNotFound, "ping not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load ping")
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

	var err error
	if req.Status == "completed" {
		err = h.completePingRequestForDaemon(r.Context(), pingID, runtimeID, workspaceID, daemonID, req.Output, req.DurationMs)
	} else {
		err = h.failPingRequestForDaemon(r.Context(), pingID, runtimeID, workspaceID, daemonID, req.Error, req.DurationMs)
	}
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "ping not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to store ping result")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
