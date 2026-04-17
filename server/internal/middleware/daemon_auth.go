package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Daemon context keys.
type daemonContextKey int

const (
	ctxKeyDaemonWorkspaceID daemonContextKey = iota
	ctxKeyDaemonID
)

// DaemonWorkspaceIDFromContext returns the workspace ID set by DaemonAuth middleware.
func DaemonWorkspaceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyDaemonWorkspaceID).(string)
	return id
}

// DaemonIDFromContext returns the daemon ID set by DaemonAuth middleware.
func DaemonIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyDaemonID).(string)
	return id
}

// DaemonAuth validates daemon auth tokens (mdt_ prefix) and injects daemon context.
func DaemonAuth(queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				slog.Debug("daemon_auth: missing authorization header", "path", r.URL.Path)
				writeError(w, http.StatusUnauthorized, "missing authorization header")
				return
			}

			tokenString := strings.TrimPrefix(authHeader, "Bearer ")
			if tokenString == authHeader {
				slog.Debug("daemon_auth: invalid format", "path", r.URL.Path)
				writeError(w, http.StatusUnauthorized, "invalid authorization format")
				return
			}

			if !strings.HasPrefix(tokenString, "mdt_") {
				writeError(w, http.StatusUnauthorized, "daemon token required")
				return
			}

			if queries == nil {
				slog.Warn("daemon_auth: daemon queries unavailable", "path", r.URL.Path)
				writeError(w, http.StatusUnauthorized, "invalid daemon token")
				return
			}

			hash := auth.HashToken(tokenString)
			dt, err := queries.GetDaemonTokenByHash(r.Context(), hash)
			if err != nil {
				slog.Warn("daemon_auth: invalid daemon token", "path", r.URL.Path, "error", err)
				writeError(w, http.StatusUnauthorized, "invalid daemon token")
				return
			}

			ctx := context.WithValue(r.Context(), ctxKeyDaemonWorkspaceID, uuidToString(dt.WorkspaceID))
			ctx = context.WithValue(ctx, ctxKeyDaemonID, dt.DaemonID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
