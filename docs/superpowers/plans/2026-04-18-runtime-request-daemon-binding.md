# Runtime Request Daemon Binding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bind persisted runtime ping/update requests to the daemon ownership context active at creation time, so daemon claim/read/write paths enforce request ownership without replacing the current `runtime_ping` / `runtime_update` model.

**Architecture:** Keep the existing `runtime_ping` and `runtime_update` tables and add `workspace_id` plus `daemon_id` to each row. Frontend request creation captures ownership from the target runtime, daemon-side claim/read/write queries become ownership-aware, and existing handler-level daemon scope checks stay in place as defense in depth.

**Tech Stack:** Go, PostgreSQL migrations, sqlc-generated queries, Chi handlers, Go test

---

## File Structure

**Modify:**
- `server/migrations/036_runtime_request_persistence.up.sql` — source-of-truth for the current request persistence tables; inspect existing shape before adding new migration
- `server/pkg/db/queries/runtime_request.sql` — add ownership-aware create/pop/get/write queries for ping/update requests
- `server/internal/handler/runtime_ping.go` — capture `(workspace_id, daemon_id)` at request creation time and use ownership-aware daemon-side request lookups/writes
- `server/internal/handler/runtime_update.go` — same pattern for update requests
- `server/internal/handler/daemon.go` — heartbeat path must use ownership-aware pending request pop helpers
- `server/internal/handler/handler_test.go` — add coverage for persisted ownership binding and daemon-side cross-daemon/cross-workspace rejections

**Create:**
- `server/migrations/043_runtime_request_daemon_binding.up.sql` — add `workspace_id` / `daemon_id`, backfill rows, enforce `NOT NULL`, add ownership indexes
- `server/migrations/043_runtime_request_daemon_binding.down.sql` — rollback for the new columns and indexes

**Reference:**
- `docs/superpowers/specs/2026-04-18-runtime-request-daemon-binding-design.md` — approved design
- `server/internal/handler/handler.go:127-170` — existing daemon runtime/task scope enforcement used as the first defense layer

---

### Task 1: Add migration and query support for request ownership binding

**Files:**
- Create: `server/migrations/043_runtime_request_daemon_binding.up.sql`
- Create: `server/migrations/043_runtime_request_daemon_binding.down.sql`
- Modify: `server/pkg/db/queries/runtime_request.sql`
- Test: `server/internal/handler/handler_test.go`

- [ ] **Step 1: Write the failing migration/query-level tests**

Add focused handler tests in `server/internal/handler/handler_test.go` that describe the new persisted ownership behavior before changing SQL.

Add these tests near the existing ping/update persistence tests:

```go
func TestInitiatePingPersistsDaemonOwnershipContext(t *testing.T) {
	ctx := context.Background()
	runtimeID := mustGetHandlerTestRuntimeID(t)

	createW := httptest.NewRecorder()
	createReq := newRequest("POST", "/api/runtimes/"+runtimeID+"/ping", nil)
	createReq = withURLParam(createReq, "runtimeId", runtimeID)
	testHandler.InitiatePing(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiatePing: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var created PingRequest
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode created ping: %v", err)
	}

	var workspaceID, daemonID string
	if err := testPool.QueryRow(ctx, `
		SELECT workspace_id::text, daemon_id
		FROM runtime_ping
		WHERE id = $1
	`, created.ID).Scan(&workspaceID, &daemonID); err != nil {
		t.Fatalf("load runtime_ping ownership context: %v", err)
	}
	if workspaceID != testWorkspaceID {
		t.Fatalf("runtime_ping.workspace_id = %q, want %q", workspaceID, testWorkspaceID)
	}
	if daemonID != handlerTestDaemonID {
		t.Fatalf("runtime_ping.daemon_id = %q, want %q", daemonID, handlerTestDaemonID)
	}
}

func TestInitiateUpdatePersistsDaemonOwnershipContext(t *testing.T) {
	ctx := context.Background()
	runtimeID := mustGetHandlerTestRuntimeID(t)

	createW := httptest.NewRecorder()
	createReq := newRequest("POST", "/api/runtimes/"+runtimeID+"/update", map[string]any{
		"target_version": "v1.2.3",
	})
	createReq = withURLParam(createReq, "runtimeId", runtimeID)
	testHandler.InitiateUpdate(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiateUpdate: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var created UpdateRequest
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode created update: %v", err)
	}

	var workspaceID, daemonID string
	if err := testPool.QueryRow(ctx, `
		SELECT workspace_id::text, daemon_id
		FROM runtime_update
		WHERE id = $1
	`, created.ID).Scan(&workspaceID, &daemonID); err != nil {
		t.Fatalf("load runtime_update ownership context: %v", err)
	}
	if workspaceID != testWorkspaceID {
		t.Fatalf("runtime_update.workspace_id = %q, want %q", workspaceID, testWorkspaceID)
	}
	if daemonID != handlerTestDaemonID {
		t.Fatalf("runtime_update.daemon_id = %q, want %q", daemonID, handlerTestDaemonID)
	}
}
```

- [ ] **Step 2: Run the focused tests to verify they fail for the right reason**

Run:

```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestInitiatePingPersistsDaemonOwnershipContext|TestInitiateUpdatePersistsDaemonOwnershipContext' -v
```

Expected: FAIL with SQL errors like `column "workspace_id" does not exist` on `runtime_ping` / `runtime_update`.

- [ ] **Step 3: Add the migration that stores ownership context on existing request tables**

Create `server/migrations/043_runtime_request_daemon_binding.up.sql` with the exact SQL below:

```sql
ALTER TABLE runtime_ping
    ADD COLUMN workspace_id UUID,
    ADD COLUMN daemon_id TEXT;

ALTER TABLE runtime_update
    ADD COLUMN workspace_id UUID,
    ADD COLUMN daemon_id TEXT;

UPDATE runtime_ping rp
SET workspace_id = ar.workspace_id,
    daemon_id = ar.daemon_id
FROM agent_runtime ar
WHERE rp.runtime_id = ar.id;

UPDATE runtime_update ru
SET workspace_id = ar.workspace_id,
    daemon_id = ar.daemon_id
FROM agent_runtime ar
WHERE ru.runtime_id = ar.id;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM runtime_ping
        WHERE workspace_id IS NULL OR daemon_id IS NULL
    ) THEN
        RAISE EXCEPTION 'runtime_ping rows missing daemon ownership context after backfill';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM runtime_update
        WHERE workspace_id IS NULL OR daemon_id IS NULL
    ) THEN
        RAISE EXCEPTION 'runtime_update rows missing daemon ownership context after backfill';
    END IF;
END
$$;

ALTER TABLE runtime_ping
    ALTER COLUMN workspace_id SET NOT NULL,
    ALTER COLUMN daemon_id SET NOT NULL,
    ADD CONSTRAINT runtime_ping_workspace_id_fkey
        FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;

ALTER TABLE runtime_update
    ALTER COLUMN workspace_id SET NOT NULL,
    ALTER COLUMN daemon_id SET NOT NULL,
    ADD CONSTRAINT runtime_update_workspace_id_fkey
        FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;

CREATE INDEX idx_runtime_ping_runtime_workspace_daemon_created
    ON runtime_ping(runtime_id, workspace_id, daemon_id, created_at);

CREATE INDEX idx_runtime_update_runtime_workspace_daemon_created
    ON runtime_update(runtime_id, workspace_id, daemon_id, created_at);
```

Create `server/migrations/043_runtime_request_daemon_binding.down.sql` with the exact SQL below:

```sql
DROP INDEX IF EXISTS idx_runtime_update_runtime_workspace_daemon_created;
DROP INDEX IF EXISTS idx_runtime_ping_runtime_workspace_daemon_created;

ALTER TABLE runtime_update
    DROP CONSTRAINT IF EXISTS runtime_update_workspace_id_fkey,
    DROP COLUMN IF EXISTS daemon_id,
    DROP COLUMN IF EXISTS workspace_id;

ALTER TABLE runtime_ping
    DROP CONSTRAINT IF EXISTS runtime_ping_workspace_id_fkey,
    DROP COLUMN IF EXISTS daemon_id,
    DROP COLUMN IF EXISTS workspace_id;
```

- [ ] **Step 4: Apply the migration and regenerate SQL access paths**

Update `server/pkg/db/queries/runtime_request.sql` so creation/pop/get/write operations can carry ownership context.

Replace the create statements with:

```sql
-- name: CreateRuntimePing :one
INSERT INTO runtime_ping (runtime_id, workspace_id, daemon_id, status)
VALUES ($1, $2, $3, 'pending')
RETURNING *;

-- name: CreateRuntimeUpdate :one
INSERT INTO runtime_update (runtime_id, workspace_id, daemon_id, status, target_version)
VALUES ($1, $2, $3, 'pending', $4)
RETURNING *;
```

Add daemon-side ownership-aware read/query operations:

```sql
-- name: GetRuntimePingForDaemon :one
SELECT * FROM runtime_ping
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4;

-- name: GetRuntimeUpdateForDaemon :one
SELECT * FROM runtime_update
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4;

-- name: PopPendingRuntimePingForDaemon :many
WITH next_ping AS (
    SELECT id
    FROM runtime_ping
    WHERE runtime_id = $1
      AND workspace_id = $2
      AND daemon_id = $3
      AND status = 'pending'
    ORDER BY created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE runtime_ping
SET status = 'running', updated_at = now()
WHERE id = (SELECT id FROM next_ping)
RETURNING *;

-- name: PopPendingRuntimeUpdateForDaemon :many
WITH next_update AS (
    SELECT id
    FROM runtime_update
    WHERE runtime_id = $1
      AND workspace_id = $2
      AND daemon_id = $3
      AND status = 'pending'
    ORDER BY created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE runtime_update
SET status = 'running', updated_at = now()
WHERE id = (SELECT id FROM next_update)
RETURNING *;
```

Add ownership-aware write operations:

```sql
-- name: SetRuntimePingCompletedForDaemon :one
UPDATE runtime_ping
SET status = 'completed', output = $5, duration_ms = $6, updated_at = now()
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4
RETURNING *;

-- name: SetRuntimePingFailedForDaemon :one
UPDATE runtime_ping
SET status = 'failed', error = $5, duration_ms = $6, updated_at = now()
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4
RETURNING *;

-- name: SetRuntimePingTimeoutForDaemon :one
UPDATE runtime_ping
SET status = 'timeout', error = 'daemon did not respond within 60 seconds', updated_at = now()
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4
  AND status IN ('pending', 'running')
RETURNING *;

-- name: SetRuntimeUpdateCompletedForDaemon :one
UPDATE runtime_update
SET status = 'completed', output = $5, updated_at = now()
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4
RETURNING *;

-- name: SetRuntimeUpdateFailedForDaemon :one
UPDATE runtime_update
SET status = 'failed', error = $5, updated_at = now()
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4
RETURNING *;

-- name: SetRuntimeUpdateTimeoutForDaemon :one
UPDATE runtime_update
SET status = 'timeout', error = 'update did not complete within 120 seconds', updated_at = now()
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4
  AND status IN ('pending', 'running')
RETURNING *;
```

Then run:

```bash
cd /Users/a1234/multica && make migrate-up && make sqlc
```

Expected: migration succeeds and `server/pkg/db/generated/runtime_request.sql.go` updates with the new method signatures.

- [ ] **Step 5: Run the focused tests to verify they pass**

Run:

```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestInitiatePingPersistsDaemonOwnershipContext|TestInitiateUpdatePersistsDaemonOwnershipContext' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/a1234/multica/server && git add migrations/043_runtime_request_daemon_binding.up.sql migrations/043_runtime_request_daemon_binding.down.sql pkg/db/queries/runtime_request.sql pkg/db/generated/runtime_request.sql.go internal/handler/handler_test.go && git commit -m "feat(runtime): bind persisted requests to daemon ownership"
```

### Task 2: Enforce ownership-aware daemon claim and result writes in handlers

**Files:**
- Modify: `server/internal/handler/runtime_ping.go`
- Modify: `server/internal/handler/runtime_update.go`
- Modify: `server/internal/handler/daemon.go`
- Test: `server/internal/handler/handler_test.go`

- [ ] **Step 1: Write the failing handler tests for cross-daemon protection**

Add these tests to `server/internal/handler/handler_test.go` before changing the handlers:

```go
func TestDaemonHeartbeatDoesNotClaimPingOwnedByAnotherDaemon(t *testing.T) {
	ctx := context.Background()
	runtimeID := mustGetHandlerTestRuntimeID(t)

	createW := httptest.NewRecorder()
	createReq := newRequest("POST", "/api/runtimes/"+runtimeID+"/ping", nil)
	createReq = withURLParam(createReq, "runtimeId", runtimeID)
	testHandler.InitiatePing(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiatePing: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	if _, err := testPool.Exec(ctx, `UPDATE runtime_ping SET daemon_id = $2 WHERE runtime_id = $1`, runtimeID, "other-daemon"); err != nil {
		t.Fatalf("rebind ping request to other daemon: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/daemon/heartbeat", map[string]any{"runtime_id": runtimeID})
	req.Header.Del("X-User-ID")
	req.Header.Del("X-Workspace-ID")
	req.Header.Set("Authorization", "Bearer "+createDaemonTokenForTest(t, ctx, testWorkspaceID, handlerTestDaemonID))
	testHandler.DaemonHeartbeat(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DaemonHeartbeat: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "pending_ping") {
		t.Fatalf("expected no pending_ping for foreign daemon-owned request, got %s", w.Body.String())
	}
}

func TestReportPingResultRejectsRequestOwnedByAnotherDaemon(t *testing.T) {
	ctx := context.Background()
	runtimeID := mustGetHandlerTestRuntimeID(t)

	createW := httptest.NewRecorder()
	createReq := newRequest("POST", "/api/runtimes/"+runtimeID+"/ping", nil)
	createReq = withURLParam(createReq, "runtimeId", runtimeID)
	testHandler.InitiatePing(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiatePing: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var created PingRequest
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode created ping: %v", err)
	}

	if _, err := testPool.Exec(ctx, `UPDATE runtime_ping SET daemon_id = $2 WHERE id = $1`, created.ID, "other-daemon"); err != nil {
		t.Fatalf("rebind ping request to other daemon: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/ping/"+created.ID, map[string]any{
		"status": "completed",
		"output": "pong",
		"duration_ms": 12,
	})
	req = withURLParam(req, "runtimeId", runtimeID)
	req = withURLParam(req, "pingId", created.ID)
	req.Header.Del("X-User-ID")
	req.Header.Del("X-Workspace-ID")
	req.Header.Set("Authorization", "Bearer "+createDaemonTokenForTest(t, ctx, testWorkspaceID, handlerTestDaemonID))
	testHandler.ReportPingResult(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ReportPingResult: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
```

Add the same shape for update paths:

```go
func TestDaemonHeartbeatDoesNotClaimUpdateOwnedByAnotherDaemon(t *testing.T) { /* same structure using InitiateUpdate */ }
func TestReportUpdateResultRejectsRequestOwnedByAnotherDaemon(t *testing.T) { /* same structure using ReportUpdateResult */ }
```

- [ ] **Step 2: Run the focused tests to verify they fail correctly**

Run:

```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestDaemonHeartbeatDoesNotClaimPingOwnedByAnotherDaemon|TestReportPingResultRejectsRequestOwnedByAnotherDaemon|TestDaemonHeartbeatDoesNotClaimUpdateOwnedByAnotherDaemon|TestReportUpdateResultRejectsRequestOwnedByAnotherDaemon' -v
```

Expected: FAIL because heartbeat still claims requests by `runtime_id` only and result handlers still load/write requests without matching `workspace_id + daemon_id`.

- [ ] **Step 3: Implement minimal handler changes to use ownership-aware queries**

Update `server/internal/handler/runtime_ping.go`.

Change request creation to persist ownership context from the runtime:

```go
ping, err := h.Queries.CreateRuntimePing(r.Context(), db.CreateRuntimePingParams{
	RuntimeID:   parseUUID(runtimeID),
	WorkspaceID: rt.WorkspaceID,
	DaemonID:    rt.DaemonID.String,
})
```

Reject runtimes without daemon binding before create:

```go
if !rt.DaemonID.Valid || strings.TrimSpace(rt.DaemonID.String) == "" {
	writeError(w, http.StatusConflict, "runtime is not currently attached to a daemon")
	return
}
```

Change the daemon-side helper signatures to require ownership context:

```go
func (h *Handler) popPendingPingRequest(ctx context.Context, runtimeID, workspaceID, daemonID string) (*PingRequest, error)
func (h *Handler) getPingRequestForDaemon(ctx context.Context, pingID, runtimeID, workspaceID, daemonID string) (*PingRequest, error)
func (h *Handler) completePingRequestForDaemon(ctx context.Context, pingID, runtimeID, workspaceID, daemonID, output string, durationMs int64) error
func (h *Handler) failPingRequestForDaemon(ctx context.Context, pingID, runtimeID, workspaceID, daemonID, errMsg string, durationMs int64) error
```

Use the new generated queries inside those helpers.

Update `server/internal/handler/runtime_update.go` the same way:

```go
update, err := h.Queries.CreateRuntimeUpdate(r.Context(), db.CreateRuntimeUpdateParams{
	RuntimeID:     parseUUID(runtimeID),
	WorkspaceID:   rt.WorkspaceID,
	DaemonID:      rt.DaemonID.String,
	TargetVersion: req.TargetVersion,
})
```

Add the same `409 runtime is not currently attached to a daemon` guard for update creation.

Then update `server/internal/handler/daemon.go` heartbeat path to pass ownership context:

```go
if pending, err := h.popPendingPingRequest(r.Context(), req.RuntimeID, uuidToString(rt.WorkspaceID), rt.DaemonID.String); err == nil && pending != nil {
	resp["pending_ping"] = map[string]string{"id": pending.ID}
}

if pending, err := h.popPendingUpdateRequest(r.Context(), req.RuntimeID, uuidToString(rt.WorkspaceID), rt.DaemonID.String); err == nil && pending != nil {
	resp["pending_update"] = map[string]string{
		"id":             pending.ID,
		"target_version": pending.TargetVersion,
	}
}
```

In `ReportPingResult` and `ReportUpdateResult`, load the request through the new ownership-aware helpers and return `404` when it does not match the authenticated daemon context.

- [ ] **Step 4: Run the focused tests to verify they pass**

Run:

```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestDaemonHeartbeatDoesNotClaimPingOwnedByAnotherDaemon|TestReportPingResultRejectsRequestOwnedByAnotherDaemon|TestDaemonHeartbeatDoesNotClaimUpdateOwnedByAnotherDaemon|TestReportUpdateResultRejectsRequestOwnedByAnotherDaemon' -v
```

Expected: PASS.

- [ ] **Step 5: Run the existing ping/update persistence regression tests**

Run:

```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestGetPingReturnsPersistedTerminalStateWhenTimeoutUpdateLosesRace|TestGetUpdateReturnsPersistedTerminalStateWhenTimeoutUpdateLosesRace|TestGetPingTimesOutAfterHeartbeatClaimsIt' -v
```

Expected: PASS, proving timeout and persistence semantics still work after ownership-aware query changes.

- [ ] **Step 6: Commit**

```bash
cd /Users/a1234/multica/server && git add internal/handler/runtime_ping.go internal/handler/runtime_update.go internal/handler/daemon.go internal/handler/handler_test.go && git commit -m "fix(runtime): scope persisted requests to daemon ownership"
```

### Task 3: Cover runtime re-binding behavior and finish package verification

**Files:**
- Modify: `server/internal/handler/handler_test.go`
- Test: `server/internal/handler/handler_test.go`

- [ ] **Step 1: Write the failing runtime re-binding tests**

Add these tests to `server/internal/handler/handler_test.go`:

```go
func TestRuntimeRebindingDoesNotTransferExistingPingRequestOwnership(t *testing.T) {
	ctx := context.Background()
	runtimeID := mustGetHandlerTestRuntimeID(t)

	createW := httptest.NewRecorder()
	createReq := newRequest("POST", "/api/runtimes/"+runtimeID+"/ping", nil)
	createReq = withURLParam(createReq, "runtimeId", runtimeID)
	testHandler.InitiatePing(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiatePing: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var created PingRequest
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode created ping: %v", err)
	}

	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET daemon_id = $2 WHERE id = $1`, runtimeID, "rebound-daemon"); err != nil {
		t.Fatalf("rebind runtime daemon: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/daemon/heartbeat", map[string]any{"runtime_id": runtimeID})
	req.Header.Del("X-User-ID")
	req.Header.Del("X-Workspace-ID")
	req.Header.Set("Authorization", "Bearer "+createDaemonTokenForTest(t, ctx, testWorkspaceID, "rebound-daemon"))
	testHandler.DaemonHeartbeat(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DaemonHeartbeat: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), created.ID) {
		t.Fatalf("expected rebound daemon not to see existing ping request, got %s", w.Body.String())
	}
}

func TestRuntimeRebindingAssignsNewUpdateRequestsToNewDaemon(t *testing.T) {
	ctx := context.Background()
	runtimeID := mustGetHandlerTestRuntimeID(t)

	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET daemon_id = $2 WHERE id = $1`, runtimeID, "rebound-daemon"); err != nil {
		t.Fatalf("rebind runtime daemon: %v", err)
	}

	createW := httptest.NewRecorder()
	createReq := newRequest("POST", "/api/runtimes/"+runtimeID+"/update", map[string]any{
		"target_version": "v9.9.9",
	})
	createReq = withURLParam(createReq, "runtimeId", runtimeID)
	testHandler.InitiateUpdate(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("InitiateUpdate: expected 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var created UpdateRequest
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode created update: %v", err)
	}

	var daemonID string
	if err := testPool.QueryRow(ctx, `SELECT daemon_id FROM runtime_update WHERE id = $1`, created.ID).Scan(&daemonID); err != nil {
		t.Fatalf("load update daemon ownership: %v", err)
	}
	if daemonID != "rebound-daemon" {
		t.Fatalf("runtime_update.daemon_id = %q, want rebound-daemon", daemonID)
	}
}
```

- [ ] **Step 2: Run the focused tests to verify they fail correctly**

Run:

```bash
cd /Users/a1234/multica/server && go test ./internal/handler -run 'TestRuntimeRebindingDoesNotTransferExistingPingRequestOwnership|TestRuntimeRebindingAssignsNewUpdateRequestsToNewDaemon' -v
```

Expected: the first test fails before ownership is fully respected across heartbeat claim paths, or the second test fails if create paths are not reading the runtime's current daemon binding.

- [ ] **Step 3: Make the minimal fixes required by the runtime re-binding tests**

Only if a test from Step 2 still fails, fix the smallest missing behavior in `runtime_ping.go`, `runtime_update.go`, or `daemon.go`.

If the implementation from Task 2 already makes both tests pass, do not add extra code.

- [ ] **Step 4: Run the full handler package**

Run:

```bash
cd /Users/a1234/multica/server && go test ./internal/handler
```

Expected: PASS.

- [ ] **Step 5: Run the affected server package to confirm integration still holds**

Run:

```bash
cd /Users/a1234/multica/server && go test ./cmd/server -run 'TestDaemonHeartbeatRejectsCrossWorkspaceRuntime|TestDaemonClaimRejectsCrossWorkspaceRuntime|TestDaemonTaskStatusRejectsCrossRuntimeTask' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/a1234/multica/server && git add internal/handler/handler_test.go internal/handler/runtime_ping.go internal/handler/runtime_update.go internal/handler/daemon.go && git commit -m "test(runtime): lock request ownership across daemon rebinding"
```
