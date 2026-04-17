# Daemon Boundary and Runtime Durability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the daemon control-plane machine-only and replace in-memory ping/update workflow truth with durable database-backed request records.

**Architecture:** Rewire `/api/daemon` to strict daemon auth, remove PAT/JWT fallback from `DaemonAuth`, and persist ping/update lifecycle into dedicated SQL-backed request tables. Refactor the runtime ping/update handlers plus daemon poll/result paths to use durable records, then tighten daemon task endpoint ownership checks and prove the new boundary with focused backend tests.

**Tech Stack:** Go, Chi router, PostgreSQL migrations, sqlc, pgx/pgtype, existing handler + middleware tests.

---

## File map

### Create

- `server/migrations/036_runtime_ping_request.up.sql` — create durable ping request table and indexes.
- `server/migrations/036_runtime_ping_request.down.sql` — drop durable ping request table.
- `server/migrations/037_runtime_update_request.up.sql` — create durable update request table and indexes.
- `server/migrations/037_runtime_update_request.down.sql` — drop durable update request table.
- `server/pkg/db/queries/runtime_ping_request.sql` — sqlc queries for ping lifecycle.
- `server/pkg/db/queries/runtime_update_request.sql` — sqlc queries for update lifecycle.
- `server/internal/middleware/daemon_auth_test.go` — focused daemon auth middleware tests.

### Modify

- `server/cmd/server/router.go:87-109` — swap daemon route middleware from `Auth` to `DaemonAuth`.
- `server/internal/middleware/daemon_auth.go:34-112` — remove PAT/JWT fallback and enforce daemon-only context semantics.
- `server/internal/handler/handler.go:34-66` — remove in-memory `PingStore` / `UpdateStore` fields from handler construction.
- `server/internal/handler/runtime_ping.go:14-200` — replace in-memory ping state with DB-backed request records.
- `server/internal/handler/runtime_update.go:12-221` — replace in-memory update state with DB-backed request records.
- `server/internal/handler/daemon.go:190-220` — load pending ping/update requests from durable storage in heartbeat flow.
- `server/internal/handler/daemon.go:285-584` — enforce daemon/runtime ownership for task and result endpoints.
- `server/internal/handler/handler_test.go:723-742` and adjacent auth/runtime tests — add handler/integration coverage for durable requests and daemon ownership.
- `server/internal/middleware/auth_test.go` — keep existing user auth tests intact if imports/shared helpers need adjustment.
- `server/pkg/db/generated/models.go` — generated sqlc models.
- `server/pkg/db/generated/runtime_ping_request.sql.go` — generated ping request queries.
- `server/pkg/db/generated/runtime_update_request.sql.go` — generated update request queries.

### Existing references to follow

- `docs/superpowers/specs/2026-04-17-daemon-boundary-and-runtime-durability-design.md`
- `server/cmd/server/router.go:82-121`
- `server/internal/middleware/daemon_auth.go:14-112`
- `server/internal/handler/handler.go:34-66`
- `server/internal/handler/runtime_ping.go:14-200`
- `server/internal/handler/runtime_update.go:12-221`
- `server/internal/handler/daemon.go:190-220`
- `server/internal/handler/daemon.go:285-584`
- `server/pkg/db/queries/runtime.sql:1-77`
- `server/internal/middleware/auth_test.go:1-198`
- `server/internal/handler/handler_test.go:723-742`

---

### Task 1: Add failing middleware tests for daemon-only auth

**Files:**
- Create: `server/internal/middleware/daemon_auth_test.go`
- Modify: `server/internal/middleware/daemon_auth.go`
- Test: `server/internal/middleware/daemon_auth_test.go`

- [ ] **Step 1: Write a daemon auth test file with failing expectations**

Create `server/internal/middleware/daemon_auth_test.go` with this content:

```go
package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func daemonAuthMiddleware(next http.Handler) http.Handler {
	return DaemonAuth(nil)(next)
}

func TestDaemonAuth_RejectsPATFallback(t *testing.T) {
	handler := daemonAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	req := httptest.NewRequest("POST", "/api/daemon/heartbeat", nil)
	req.Header.Set("Authorization", "Bearer mul_pat_should_not_work")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestDaemonAuth_RejectsJWTFallback(t *testing.T) {
	handler := daemonAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	token := generateToken(validClaims(), []byte("daemon-jwt-fallback-should-not-work"))
	req := httptest.NewRequest("POST", "/api/daemon/heartbeat", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestDaemonAuth_RequiresDaemonTokenPrefix(t *testing.T) {
	handler := daemonAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	req := httptest.NewRequest("POST", "/api/daemon/heartbeat", nil)
	req.Header.Set("Authorization", "Bearer not-a-daemon-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestDaemonWorkspaceIDFromContext_EmptyWhenUnset(t *testing.T) {
	if got := DaemonWorkspaceIDFromContext(httptest.NewRequest("GET", "/", nil).Context()); got != "" {
		t.Fatalf("expected empty workspace id, got %q", got)
	}
}

var _ = db.Queries{}
```

- [ ] **Step 2: Run the focused middleware tests to verify failure**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1/server && go test ./internal/middleware -run 'TestDaemonAuth_|TestDaemonWorkspaceIDFromContext_'`

Expected: FAIL because `DaemonAuth` still accepts PAT/JWT fallback and likely panics or misbehaves under the daemon-only expectations.

- [ ] **Step 3: Write the minimal daemon-only middleware implementation**

Replace the body of `DaemonAuth` in `server/internal/middleware/daemon_auth.go` with this implementation, keeping the context key helpers intact:

```go
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
				slog.Warn("daemon_auth: rejected non-daemon token", "path", r.URL.Path)
				writeError(w, http.StatusUnauthorized, "invalid daemon token")
				return
			}

			if queries == nil {
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
```

- [ ] **Step 4: Run the middleware tests to verify they pass**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1/server && go test ./internal/middleware -run 'TestDaemonAuth_|TestDaemonWorkspaceIDFromContext_'`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/internal/middleware/daemon_auth.go server/internal/middleware/daemon_auth_test.go
git commit -m "fix(daemon): enforce daemon-only auth middleware"
```

---

### Task 2: Rewire daemon routes to machine-only auth

**Files:**
- Modify: `server/cmd/server/router.go:87-109`
- Test: `server/internal/middleware/daemon_auth_test.go`

- [ ] **Step 1: Add a route-level failing test that user auth no longer applies to daemon routes**

Append this test to `server/internal/middleware/daemon_auth_test.go`:

```go
func TestDaemonAuth_RejectsUserJWTOnDaemonRoute(t *testing.T) {
	handler := DaemonAuth(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	req := httptest.NewRequest("POST", "/api/daemon/register", nil)
	req.Header.Set("Authorization", "Bearer "+generateToken(validClaims(), auth.JWTSecret()))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run the targeted middleware test**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1/server && go test ./internal/middleware -run 'TestDaemonAuth_RejectsUserJWTOnDaemonRoute'`

Expected: PASS after Task 1 changes.

- [ ] **Step 3: Rewire the daemon route group**

In `server/cmd/server/router.go`, change this block:

```go
	// Daemon API routes (all require a valid token)
	r.Route("/api/daemon", func(r chi.Router) {
		r.Use(middleware.Auth(queries))
```

To this:

```go
	// Daemon API routes (machine-only daemon auth)
	r.Route("/api/daemon", func(r chi.Router) {
		r.Use(middleware.DaemonAuth(queries))
```

- [ ] **Step 4: Run router and middleware tests**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1/server && go test ./cmd/server ./internal/middleware`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/cmd/server/router.go server/internal/middleware/daemon_auth.go server/internal/middleware/daemon_auth_test.go
git commit -m "fix(router): require daemon auth on control plane"
```

---

### Task 3: Add durable ping and update request tables plus sqlc queries

**Files:**
- Create: `server/migrations/036_runtime_ping_request.up.sql`
- Create: `server/migrations/036_runtime_ping_request.down.sql`
- Create: `server/migrations/037_runtime_update_request.up.sql`
- Create: `server/migrations/037_runtime_update_request.down.sql`
- Create: `server/pkg/db/queries/runtime_ping_request.sql`
- Create: `server/pkg/db/queries/runtime_update_request.sql`
- Modify: `server/pkg/db/generated/models.go`
- Modify: `server/pkg/db/generated/runtime_ping_request.sql.go`
- Modify: `server/pkg/db/generated/runtime_update_request.sql.go`

- [ ] **Step 1: Write the ping request migration**

Create `server/migrations/036_runtime_ping_request.up.sql` with:

```sql
CREATE TABLE runtime_ping_request (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    runtime_id UUID NOT NULL REFERENCES agent_runtime(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    daemon_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed', 'timeout')),
    output TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_runtime_ping_request_runtime_status_created_at
    ON runtime_ping_request(runtime_id, status, created_at);

CREATE INDEX idx_runtime_ping_request_daemon_status_created_at
    ON runtime_ping_request(daemon_id, status, created_at);
```

Create `server/migrations/036_runtime_ping_request.down.sql` with:

```sql
DROP TABLE IF EXISTS runtime_ping_request;
```

- [ ] **Step 2: Write the update request migration**

Create `server/migrations/037_runtime_update_request.up.sql` with:

```sql
CREATE TABLE runtime_update_request (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    runtime_id UUID NOT NULL REFERENCES agent_runtime(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    daemon_id TEXT NOT NULL,
    target_version TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed', 'timeout')),
    output TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_runtime_update_request_runtime_status_created_at
    ON runtime_update_request(runtime_id, status, created_at);

CREATE INDEX idx_runtime_update_request_daemon_status_created_at
    ON runtime_update_request(daemon_id, status, created_at);
```

Create `server/migrations/037_runtime_update_request.down.sql` with:

```sql
DROP TABLE IF EXISTS runtime_update_request;
```

- [ ] **Step 3: Write the ping sqlc query file**

Create `server/pkg/db/queries/runtime_ping_request.sql` with:

```sql
-- name: CreateRuntimePingRequest :one
INSERT INTO runtime_ping_request (runtime_id, workspace_id, daemon_id, status)
VALUES ($1, $2, $3, 'pending')
RETURNING *;

-- name: GetRuntimePingRequest :one
SELECT * FROM runtime_ping_request
WHERE id = $1;

-- name: GetRuntimePingRequestForDaemon :one
SELECT * FROM runtime_ping_request
WHERE id = $1 AND daemon_id = $2 AND workspace_id = $3;

-- name: ClaimNextRuntimePingRequestForDaemon :one
UPDATE runtime_ping_request
SET status = 'running', updated_at = now()
WHERE id = (
    SELECT id FROM runtime_ping_request
    WHERE runtime_id = $1 AND daemon_id = $2 AND workspace_id = $3 AND status = 'pending'
    ORDER BY created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: CompleteRuntimePingRequest :one
UPDATE runtime_ping_request
SET status = 'completed', output = $2, error = '', duration_ms = $3, updated_at = now(), completed_at = now()
WHERE id = $1
RETURNING *;

-- name: FailRuntimePingRequest :one
UPDATE runtime_ping_request
SET status = 'failed', output = '', error = $2, duration_ms = $3, updated_at = now(), completed_at = now()
WHERE id = $1
RETURNING *;

-- name: TimeoutStaleRuntimePingRequests :many
UPDATE runtime_ping_request
SET status = 'timeout', error = 'daemon did not respond within 60 seconds', updated_at = now(), completed_at = now()
WHERE status IN ('pending', 'running')
  AND created_at < now() - interval '60 seconds'
RETURNING *;
```

- [ ] **Step 4: Write the update sqlc query file**

Create `server/pkg/db/queries/runtime_update_request.sql` with:

```sql
-- name: CreateRuntimeUpdateRequest :one
INSERT INTO runtime_update_request (runtime_id, workspace_id, daemon_id, target_version, status)
VALUES ($1, $2, $3, $4, 'pending')
RETURNING *;

-- name: GetRuntimeUpdateRequest :one
SELECT * FROM runtime_update_request
WHERE id = $1;

-- name: GetRuntimeUpdateRequestForDaemon :one
SELECT * FROM runtime_update_request
WHERE id = $1 AND daemon_id = $2 AND workspace_id = $3;

-- name: CountActiveRuntimeUpdateRequests :one
SELECT count(*)::int AS count
FROM runtime_update_request
WHERE runtime_id = $1 AND status IN ('pending', 'running');

-- name: ClaimNextRuntimeUpdateRequestForDaemon :one
UPDATE runtime_update_request
SET status = 'running', updated_at = now()
WHERE id = (
    SELECT id FROM runtime_update_request
    WHERE runtime_id = $1 AND daemon_id = $2 AND workspace_id = $3 AND status = 'pending'
    ORDER BY created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: CompleteRuntimeUpdateRequest :one
UPDATE runtime_update_request
SET status = 'completed', output = $2, error = '', updated_at = now(), completed_at = now()
WHERE id = $1
RETURNING *;

-- name: FailRuntimeUpdateRequest :one
UPDATE runtime_update_request
SET status = 'failed', output = '', error = $2, updated_at = now(), completed_at = now()
WHERE id = $1
RETURNING *;

-- name: TimeoutStaleRuntimeUpdateRequests :many
UPDATE runtime_update_request
SET status = 'timeout', error = 'update did not complete within 120 seconds', updated_at = now(), completed_at = now()
WHERE status IN ('pending', 'running')
  AND created_at < now() - interval '120 seconds'
RETURNING *;
```

- [ ] **Step 5: Run sqlc generation**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1 && make sqlc`

Expected: sqlc regenerates `server/pkg/db/generated/models.go`, `server/pkg/db/generated/runtime_ping_request.sql.go`, and `server/pkg/db/generated/runtime_update_request.sql.go` without errors.

- [ ] **Step 6: Verify generated query methods exist**

Run: `grep -n "CreateRuntimePingRequest\|ClaimNextRuntimePingRequestForDaemon\|CreateRuntimeUpdateRequest\|CountActiveRuntimeUpdateRequests" /Users/a1234/multica/.worktrees/commercial-readiness-wave1/server/pkg/db/generated/runtime_ping_request.sql.go /Users/a1234/multica/.worktrees/commercial-readiness-wave1/server/pkg/db/generated/runtime_update_request.sql.go`

Expected: output shows all four generated method names.

- [ ] **Step 7: Commit**

```bash
git add server/migrations/036_runtime_ping_request.up.sql server/migrations/036_runtime_ping_request.down.sql server/migrations/037_runtime_update_request.up.sql server/migrations/037_runtime_update_request.down.sql server/pkg/db/queries/runtime_ping_request.sql server/pkg/db/queries/runtime_update_request.sql server/pkg/db/generated/models.go server/pkg/db/generated/runtime_ping_request.sql.go server/pkg/db/generated/runtime_update_request.sql.go
git commit -m "feat(runtime): add durable control-plane request records"
```

---

### Task 4: Replace in-memory ping/update handlers with durable request flow

**Files:**
- Modify: `server/internal/handler/handler.go:34-66`
- Modify: `server/internal/handler/runtime_ping.go:14-200`
- Modify: `server/internal/handler/runtime_update.go:12-221`
- Test: `server/internal/handler/handler_test.go`

- [ ] **Step 1: Add failing handler tests for durable ping/update creation**

Append these tests to `server/internal/handler/handler_test.go`:

```go
func TestInitiatePingCreatesDurableRequest(t *testing.T) {
	ctx := context.Background()
	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent_runtime WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("load runtime: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/runtimes/"+runtimeID+"/ping", nil)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.InitiatePing(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("InitiatePing: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("expected request id in response")
	}

	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM runtime_ping_request WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("load runtime_ping_request: %v", err)
	}
	if status != "pending" {
		t.Fatalf("expected pending status, got %q", status)
	}
}

func TestInitiateUpdateCreatesDurableRequest(t *testing.T) {
	ctx := context.Background()
	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent_runtime WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("load runtime: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/runtimes/"+runtimeID+"/update", map[string]any{"target_version": "v0.3.0"})
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.InitiateUpdate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("InitiateUpdate: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("expected request id in response")
	}

	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM runtime_update_request WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("load runtime_update_request: %v", err)
	}
	if status != "pending" {
		t.Fatalf("expected pending status, got %q", status)
	}
}
```

- [ ] **Step 2: Run the focused handler tests to verify failure**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1/server && go test ./internal/handler -run 'TestInitiatePingCreatesDurableRequest|TestInitiateUpdateCreatesDurableRequest'`

Expected: FAIL because the new tables/queries are not yet wired into the handlers.

- [ ] **Step 3: Remove in-memory fields from handler construction**

In `server/internal/handler/handler.go`, delete these fields from `Handler`:

```go
	PingStore    *PingStore
	UpdateStore  *UpdateStore
```

And delete these constructor assignments:

```go
		PingStore:    NewPingStore(),
		UpdateStore:  NewUpdateStore(),
```

- [ ] **Step 4: Replace `InitiatePing`, `GetPing`, and `ReportPingResult` with durable query flow**

In `server/internal/handler/runtime_ping.go`, keep the response struct shape but replace the handler implementations with this code:

```go
func pingRequestToResponse(req db.RuntimePingRequest) PingRequest {
	return PingRequest{
		ID:         uuidToString(req.ID),
		RuntimeID:  uuidToString(req.RuntimeID),
		Status:     PingStatus(req.Status),
		Output:     req.Output,
		Error:      req.Error,
		DurationMs: req.DurationMs,
		CreatedAt:  req.CreatedAt.Time,
		UpdatedAt:  req.UpdatedAt.Time,
	}
}

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
		writeError(w, http.StatusConflict, "runtime is not attached to a daemon")
		return
	}

	ping, err := h.Queries.CreateRuntimePingRequest(r.Context(), db.CreateRuntimePingRequestParams{
		RuntimeID:   rt.ID,
		WorkspaceID: rt.WorkspaceID,
		DaemonID:    rt.DaemonID.String,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create ping request")
		return
	}

	writeJSON(w, http.StatusOK, pingRequestToResponse(ping))
}

func (h *Handler) GetPing(w http.ResponseWriter, r *http.Request) {
	pingID := chi.URLParam(r, "pingId")

	_, _ = h.Queries.TimeoutStaleRuntimePingRequests(r.Context())
	ping, err := h.Queries.GetRuntimePingRequest(r.Context(), parseUUID(pingID))
	if err != nil {
		writeError(w, http.StatusNotFound, "ping not found")
		return
	}

	writeJSON(w, http.StatusOK, pingRequestToResponse(ping))
}

func (h *Handler) ReportPingResult(w http.ResponseWriter, r *http.Request) {
	pingID := chi.URLParam(r, "pingId")

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

	daemonID := middleware.DaemonIDFromContext(r.Context())
	workspaceID := middleware.DaemonWorkspaceIDFromContext(r.Context())
	if daemonID == "" || workspaceID == "" {
		writeError(w, http.StatusUnauthorized, "invalid daemon context")
		return
	}

	if _, err := h.Queries.GetRuntimePingRequestForDaemon(r.Context(), db.GetRuntimePingRequestForDaemonParams{
		ID:          parseUUID(pingID),
		DaemonID:    daemonID,
		WorkspaceID: parseUUID(workspaceID),
	}); err != nil {
		writeError(w, http.StatusNotFound, "ping not found")
		return
	}

	switch req.Status {
	case "completed":
		if _, err := h.Queries.CompleteRuntimePingRequest(r.Context(), db.CompleteRuntimePingRequestParams{
			ID:         parseUUID(pingID),
			Output:     req.Output,
			DurationMs: req.DurationMs,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to complete ping")
			return
		}
	case "failed":
		if _, err := h.Queries.FailRuntimePingRequest(r.Context(), db.FailRuntimePingRequestParams{
			ID:         parseUUID(pingID),
			Error:      req.Error,
			DurationMs: req.DurationMs,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to fail ping")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

- [ ] **Step 5: Replace `InitiateUpdate`, `GetUpdate`, and `ReportUpdateResult` with durable query flow**

In `server/internal/handler/runtime_update.go`, replace the handler implementations with this code:

```go
func updateRequestToResponse(req db.RuntimeUpdateRequest) UpdateRequest {
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
		writeError(w, http.StatusConflict, "runtime is not attached to a daemon")
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

	activeCount, err := h.Queries.CountActiveRuntimeUpdateRequests(r.Context(), rt.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check active updates")
		return
	}
	if activeCount > 0 {
		writeError(w, http.StatusConflict, "an update is already in progress for this runtime")
		return
	}

	update, err := h.Queries.CreateRuntimeUpdateRequest(r.Context(), db.CreateRuntimeUpdateRequestParams{
		RuntimeID:     rt.ID,
		WorkspaceID:   rt.WorkspaceID,
		DaemonID:      rt.DaemonID.String,
		TargetVersion: req.TargetVersion,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create update request")
		return
	}

	writeJSON(w, http.StatusOK, updateRequestToResponse(update))
}

func (h *Handler) GetUpdate(w http.ResponseWriter, r *http.Request) {
	updateID := chi.URLParam(r, "updateId")

	_, _ = h.Queries.TimeoutStaleRuntimeUpdateRequests(r.Context())
	update, err := h.Queries.GetRuntimeUpdateRequest(r.Context(), parseUUID(updateID))
	if err != nil {
		writeError(w, http.StatusNotFound, "update not found")
		return
	}

	writeJSON(w, http.StatusOK, updateRequestToResponse(update))
}

func (h *Handler) ReportUpdateResult(w http.ResponseWriter, r *http.Request) {
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

	daemonID := middleware.DaemonIDFromContext(r.Context())
	workspaceID := middleware.DaemonWorkspaceIDFromContext(r.Context())
	if daemonID == "" || workspaceID == "" {
		writeError(w, http.StatusUnauthorized, "invalid daemon context")
		return
	}

	if _, err := h.Queries.GetRuntimeUpdateRequestForDaemon(r.Context(), db.GetRuntimeUpdateRequestForDaemonParams{
		ID:          parseUUID(updateID),
		DaemonID:    daemonID,
		WorkspaceID: parseUUID(workspaceID),
	}); err != nil {
		writeError(w, http.StatusNotFound, "update not found")
		return
	}

	switch req.Status {
	case "completed":
		if _, err := h.Queries.CompleteRuntimeUpdateRequest(r.Context(), db.CompleteRuntimeUpdateRequestParams{
			ID:     parseUUID(updateID),
			Output: req.Output,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to complete update")
			return
		}
	case "failed":
		if _, err := h.Queries.FailRuntimeUpdateRequest(r.Context(), db.FailRuntimeUpdateRequestParams{
			ID:    parseUUID(updateID),
			Error: req.Error,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to fail update")
			return
		}
	case "running":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	default:
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

- [ ] **Step 6: Run the focused handler tests**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1/server && go test ./internal/handler -run 'TestInitiatePingCreatesDurableRequest|TestInitiateUpdateCreatesDurableRequest'`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add server/internal/handler/handler.go server/internal/handler/runtime_ping.go server/internal/handler/runtime_update.go server/internal/handler/handler_test.go
git commit -m "feat(runtime): persist ping and update requests"
```

---

### Task 5: Move daemon poll/result flow to durable records and enforce ownership

**Files:**
- Modify: `server/internal/handler/daemon.go:190-220`
- Modify: `server/internal/handler/daemon.go:285-584`
- Test: `server/internal/handler/handler_test.go`

- [ ] **Step 1: Add failing handler tests for daemon-side ownership and heartbeat polling**

Update `server/internal/handler/handler_test.go` imports to include:

```go
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
```

Then append these helpers and tests to `server/internal/handler/handler_test.go`:

```go
func issueDaemonToken(ctx context.Context, t *testing.T, workspaceID, daemonID string) string {
	t.Helper()
	raw, err := auth.GenerateDaemonToken()
	if err != nil {
		t.Fatalf("generate daemon token: %v", err)
	}
	if _, err := testHandler.Queries.CreateDaemonToken(ctx, db.CreateDaemonTokenParams{
		TokenHash:   auth.HashToken(raw),
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    daemonID,
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("create daemon token: %v", err)
	}
	return raw
}

func daemonRequest(method, path, body, token string) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestDaemonHeartbeatReturnsPendingDurablePing(t *testing.T) {
	ctx := context.Background()
	var runtimeID, daemonID string
	if err := testPool.QueryRow(ctx, `SELECT id, daemon_id FROM agent_runtime WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&runtimeID, &daemonID); err != nil {
		t.Fatalf("load runtime: %v", err)
	}

	var pingID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO runtime_ping_request (runtime_id, workspace_id, daemon_id, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING id
	`, runtimeID, testWorkspaceID, daemonID).Scan(&pingID); err != nil {
		t.Fatalf("seed runtime_ping_request: %v", err)
	}

	w := httptest.NewRecorder()
	token := issueDaemonToken(ctx, t, testWorkspaceID, daemonID)
	req := daemonRequest("POST", "/api/daemon/heartbeat", `{"runtime_id":"`+runtimeID+`"}`, token)
	handler := middleware.DaemonAuth(testHandler.Queries)(http.HandlerFunc(testHandler.DaemonHeartbeat))
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DaemonHeartbeat: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), pingID) {
		t.Fatalf("expected pending ping id %s in response, got %s", pingID, w.Body.String())
	}
}

func TestReportPingResultRejectsWrongDaemonBoundary(t *testing.T) {
	ctx := context.Background()
	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent_runtime WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("load runtime: %v", err)
	}

	var pingID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO runtime_ping_request (runtime_id, workspace_id, daemon_id, status)
		VALUES ($1, $2, 'real-daemon', 'running')
		RETURNING id
	`, runtimeID, testWorkspaceID).Scan(&pingID); err != nil {
		t.Fatalf("seed runtime_ping_request: %v", err)
	}

	w := httptest.NewRecorder()
	wrongToken := issueDaemonToken(ctx, t, testWorkspaceID, "wrong-daemon")
	req := daemonRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/ping/"+pingID+"/result", `{"status":"completed","output":"ok","duration_ms":10}`, wrongToken)
	req = withURLParam(req, "runtimeId", runtimeID)
	req = withURLParam(req, "pingId", pingID)
	handler := middleware.DaemonAuth(testHandler.Queries)(http.HandlerFunc(testHandler.ReportPingResult))
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 2: Run the focused ownership tests to verify failure**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1/server && go test ./internal/handler -run 'TestDaemonHeartbeatReturnsPendingDurablePing|TestReportPingResultRejectsWrongDaemonBoundary'`

Expected: FAIL because the heartbeat path still uses in-memory stores and ownership checks are not yet bound to daemon context.

- [ ] **Step 3: Refactor daemon heartbeat pending-poll logic**

In `server/internal/handler/daemon.go`, replace the `pending_ping` and `pending_update` portions of `DaemonHeartbeat` with this code:

```go
	daemonWorkspaceID := middleware.DaemonWorkspaceIDFromContext(r.Context())
	daemonID := middleware.DaemonIDFromContext(r.Context())
	if daemonWorkspaceID == "" || daemonID == "" {
		writeError(w, http.StatusUnauthorized, "invalid daemon context")
		return
	}

	resp := map[string]any{"status": "ok"}

	if pending, err := h.Queries.ClaimNextRuntimePingRequestForDaemon(r.Context(), db.ClaimNextRuntimePingRequestForDaemonParams{
		RuntimeID:   parseUUID(req.RuntimeID),
		DaemonID:    daemonID,
		WorkspaceID: parseUUID(daemonWorkspaceID),
	}); err == nil {
		resp["pending_ping"] = map[string]string{"id": uuidToString(pending.ID)}
	}

	if pending, err := h.Queries.ClaimNextRuntimeUpdateRequestForDaemon(r.Context(), db.ClaimNextRuntimeUpdateRequestForDaemonParams{
		RuntimeID:   parseUUID(req.RuntimeID),
		DaemonID:    daemonID,
		WorkspaceID: parseUUID(daemonWorkspaceID),
	}); err == nil {
		resp["pending_update"] = map[string]string{
			"id":             uuidToString(pending.ID),
			"target_version": pending.TargetVersion,
		}
	}
```

- [ ] **Step 4: Add daemon/task ownership checks for task and result endpoints**

Insert this helper near the daemon handlers in `server/internal/handler/daemon.go`:

```go
func (h *Handler) requireDaemonTaskBoundary(ctx context.Context, task db.AgentTaskQueue) error {
	daemonWorkspaceID := middleware.DaemonWorkspaceIDFromContext(ctx)
	daemonID := middleware.DaemonIDFromContext(ctx)
	if daemonWorkspaceID == "" || daemonID == "" {
		return pgx.ErrNoRows
	}

	runtime, err := h.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		return err
	}
	if uuidToString(runtime.WorkspaceID) != daemonWorkspaceID {
		return pgx.ErrNoRows
	}
	if !runtime.DaemonID.Valid || runtime.DaemonID.String != daemonID {
		return pgx.ErrNoRows
	}
	return nil
}
```

Then add this guard pattern to `StartTask`, `ReportTaskProgress`, `CompleteTask`, `GetTaskStatus`, `FailTask`, `ReportTaskUsage`, `ReportTaskMessages`, and `ListTaskMessages` immediately after loading the task:

```go
	if err := h.requireDaemonTaskBoundary(r.Context(), task); err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
```

For handlers that do not currently load the task first, load it with `h.Queries.GetAgentTask(...)` before proceeding.

- [ ] **Step 5: Run the focused daemon tests**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1/server && go test ./internal/handler -run 'TestDaemonHeartbeatReturnsPendingDurablePing|TestReportPingResultRejectsWrongDaemonBoundary'`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add server/internal/handler/daemon.go server/internal/handler/runtime_ping.go server/internal/handler/runtime_update.go server/internal/handler/handler_test.go
git commit -m "fix(daemon): bind control-plane actions to daemon ownership"
```

---

### Task 6: Final backend verification

**Files:**
- Review: `server/cmd/server/router.go`
- Review: `server/internal/middleware/daemon_auth.go`
- Review: `server/internal/handler/runtime_ping.go`
- Review: `server/internal/handler/runtime_update.go`
- Review: `server/internal/handler/daemon.go`
- Review: `server/internal/middleware/daemon_auth_test.go`
- Review: `server/internal/handler/handler_test.go`

- [ ] **Step 1: Run focused middleware and handler suites**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1/server && go test ./internal/middleware ./internal/handler ./cmd/server`

Expected: PASS.

- [ ] **Step 2: Run the full backend test suite**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1/server && go test ./...`

Expected: PASS.

- [ ] **Step 3: Run worktree-safe backend verification**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1 && ENV_FILE=.env.worktree make test`

Expected: PASS.

- [ ] **Step 4: Review scope control in the final diff**

Run: `cd /Users/a1234/multica/.worktrees/commercial-readiness-wave1 && git diff --name-only`

Expected output limited to:

```text
server/cmd/server/router.go
server/internal/middleware/daemon_auth.go
server/internal/middleware/daemon_auth_test.go
server/internal/handler/handler.go
server/internal/handler/runtime_ping.go
server/internal/handler/runtime_update.go
server/internal/handler/daemon.go
server/internal/handler/handler_test.go
server/migrations/036_runtime_ping_request.up.sql
server/migrations/036_runtime_ping_request.down.sql
server/migrations/037_runtime_update_request.up.sql
server/migrations/037_runtime_update_request.down.sql
server/pkg/db/queries/runtime_ping_request.sql
server/pkg/db/queries/runtime_update_request.sql
server/pkg/db/generated/models.go
server/pkg/db/generated/runtime_ping_request.sql.go
server/pkg/db/generated/runtime_update_request.sql.go
server/internal/middleware/auth_test.go
```

- [ ] **Step 5: Commit**

```bash
git add server/cmd/server/router.go server/internal/middleware/daemon_auth.go server/internal/middleware/daemon_auth_test.go server/internal/handler/handler.go server/internal/handler/runtime_ping.go server/internal/handler/runtime_update.go server/internal/handler/daemon.go server/internal/handler/handler_test.go server/migrations/036_runtime_ping_request.up.sql server/migrations/036_runtime_ping_request.down.sql server/migrations/037_runtime_update_request.up.sql server/migrations/037_runtime_update_request.down.sql server/pkg/db/queries/runtime_ping_request.sql server/pkg/db/queries/runtime_update_request.sql server/pkg/db/generated/models.go server/pkg/db/generated/runtime_ping_request.sql.go server/pkg/db/generated/runtime_update_request.sql.go server/internal/middleware/auth_test.go
git commit -m "feat(daemon): harden control plane and persist runtime operations"
```

---

## Self-review

### Spec coverage

- Router rewiring and daemon-only middleware: covered by Tasks 1 and 2.
- Durable ping/update request models and flow: covered by Tasks 3 and 4.
- Daemon task endpoint ownership audit: covered by Task 5.
- Focused backend verification without broad platform expansion: covered by Task 6.

### Placeholder scan

- No `TBD`, `TODO`, or deferred implementation markers remain.
- Every code-changing step includes concrete SQL or Go code.
- Every verification step includes an exact command and expected result.

### Type consistency

- `runtime_ping_request` and `runtime_update_request` tables use the same status vocabulary defined in the spec.
- Durable query method names match the handler code that consumes them.
- Daemon auth and daemon task boundary checks consistently use `middleware.DaemonWorkspaceIDFromContext` and `middleware.DaemonIDFromContext`.
