/**
 * Subagent registry — in-memory tracking + lifecycle management.
 *
 * Tracks all active subagent runs, persists state to disk,
 * watches for child completion, and triggers announce flow.
 */

import { getHub, isHubInitialized } from "../../hub/hub-singleton.js";
import { loadSubagentRuns, saveSubagentRuns } from "./registry-store.js";
import { runSubagentAnnounceFlow } from "./announce.js";
import type {
  RegisterSubagentRunParams,
  SubagentRunRecord,
} from "./types.js";
import { resolveSessionDir } from "../session/storage.js";
import { rmSync } from "node:fs";

/** Default archive retention: 60 minutes after completion */
const DEFAULT_ARCHIVE_AFTER_MS = 60 * 60 * 1000;

/** Archive sweep interval: 60 seconds */
const SWEEP_INTERVAL_MS = 60 * 1000;

// ============================================================================
// Module-level state
// ============================================================================

const subagentRuns = new Map<string, SubagentRunRecord>();
let sweepTimer: ReturnType<typeof setInterval> | undefined;
const resumedRuns = new Set<string>();

// ============================================================================
// Public API
// ============================================================================

/** Initialize registry from persisted state. Call once at startup. */
export function initSubagentRegistry(): void {
  const persisted = loadSubagentRuns();
  for (const [runId, record] of persisted) {
    subagentRuns.set(runId, record);

    // Resume incomplete runs
    if (!record.cleanupHandled) {
      if (record.endedAt) {
        // Completed but cleanup not done — run announce flow
        if (!resumedRuns.has(runId)) {
          resumedRuns.add(runId);
          handleRunCompletion(record);
        }
      } else {
        // If not ended, the child agent session is lost on restart —
        // mark as ended with unknown outcome
        record.endedAt = Date.now();
        record.outcome = { status: "unknown" };
        persist();
        if (!resumedRuns.has(runId)) {
          resumedRuns.add(runId);
          handleRunCompletion(record);
        }
      }
    }
  }

  if (subagentRuns.size > 0) {
    startSweeper();
    console.log(`[SubagentRegistry] Loaded ${subagentRuns.size} persisted run(s)`);
  }
}

/** Register a new subagent run and start tracking its lifecycle. */
export function registerSubagentRun(params: RegisterSubagentRunParams): SubagentRunRecord {
  const {
    runId,
    childSessionId,
    requesterSessionId,
    task,
    label,
    cleanup = "delete",
    timeoutSeconds,
  } = params;

  const record: SubagentRunRecord = {
    runId,
    childSessionId,
    requesterSessionId,
    task,
    label,
    cleanup,
    createdAt: Date.now(),
  };

  subagentRuns.set(runId, record);
  persist();
  startSweeper();

  // Start watching the child agent for completion
  watchChildAgent(record, timeoutSeconds);

  return record;
}

/** List all active runs for a given requester session. */
export function listSubagentRuns(requesterSessionId: string): SubagentRunRecord[] {
  const result: SubagentRunRecord[] = [];
  for (const record of subagentRuns.values()) {
    if (record.requesterSessionId === requesterSessionId) {
      result.push(record);
    }
  }
  return result;
}

/** Remove a run from the registry. */
export function releaseSubagentRun(runId: string): boolean {
  const deleted = subagentRuns.delete(runId);
  if (deleted) {
    persist();
    if (subagentRuns.size === 0) {
      stopSweeper();
    }
  }
  return deleted;
}

/** Get a run by ID. */
export function getSubagentRun(runId: string): SubagentRunRecord | undefined {
  return subagentRuns.get(runId);
}

/** Mark all active (non-ended) runs as ended with "unknown" status. Called during Hub shutdown. */
export function shutdownSubagentRegistry(): void {
  const now = Date.now();
  let updated = 0;

  for (const record of subagentRuns.values()) {
    if (!record.endedAt) {
      record.endedAt = now;
      record.outcome = { status: "unknown" };
      updated++;
    }
  }

  if (updated > 0) {
    persist();
    console.log(`[SubagentRegistry] Marked ${updated} active run(s) as ended during shutdown`);
  }

  stopSweeper();
}

/** Reset all state (for testing). */
export function resetSubagentRegistryForTests(): void {
  subagentRuns.clear();
  resumedRuns.clear();
  stopSweeper();
}

// ============================================================================
// Lifecycle watching
// ============================================================================

function watchChildAgent(record: SubagentRunRecord, timeoutSeconds?: number): void {
  const { childSessionId } = record;

  // Mark as started
  record.startedAt = Date.now();
  persist();

  const cleanup = (outcome: { status: "ok" | "error" | "timeout" | "unknown"; error?: string | undefined }) => {
    if (record.endedAt) return; // Already finalized
    if (timeoutTimer) clearTimeout(timeoutTimer);
    record.endedAt = Date.now();
    record.outcome = outcome;
    persist();
    handleRunCompletion(record);
  };

  // Set up timeout if specified
  let timeoutTimer: ReturnType<typeof setTimeout> | undefined;
  if (timeoutSeconds && timeoutSeconds > 0) {
    timeoutTimer = setTimeout(() => {
      cleanup({ status: "timeout" });

      // Try to close the child agent
      try {
        const hub = getHub();
        hub.closeAgent(childSessionId);
      } catch {
        // Hub may not be available
      }
    }, timeoutSeconds * 1000);
  }

  // Get child agent reference (Hub may not be available in tests)
  if (!isHubInitialized()) {
    cleanup({ status: "error", error: "Hub not initialized" });
    return;
  }

  const hub = getHub();
  const childAgent = hub.getAgent(childSessionId);
  if (!childAgent) {
    cleanup({ status: "error", error: "Child agent not found" });
    return;
  }

  // Wait for the child agent's task queue to drain (task completion),
  // then trigger announce flow. Uses waitForIdle() instead of consuming
  // the stream (which would conflict with Hub.consumeAgent).
  childAgent.waitForIdle().then(
    () => cleanup({ status: "ok" }),
    (err) => cleanup({
      status: "error",
      error: err instanceof Error ? err.message : String(err),
    }),
  );

  // Also handle explicit close (e.g., timeout kill, Hub shutdown)
  childAgent.onClose(() => {
    cleanup({ status: record.outcome?.status ?? "unknown" });
  });
}

// ============================================================================
// Cleanup + Announce
// ============================================================================

function handleRunCompletion(record: SubagentRunRecord): void {
  if (record.cleanupHandled) return;
  record.cleanupHandled = true;
  persist();

  // Run announce flow
  const announced = runSubagentAnnounceFlow({
    runId: record.runId,
    childSessionId: record.childSessionId,
    requesterSessionId: record.requesterSessionId,
    task: record.task,
    label: record.label,
    cleanup: record.cleanup,
    outcome: record.outcome,
    startedAt: record.startedAt,
    endedAt: record.endedAt,
  });

  if (!announced) {
    console.warn(`[SubagentRegistry] Announce flow failed for run ${record.runId}`);
    // Allow retry on next restart if announce failed.
    record.cleanupHandled = false;
    persist();
    return;
  }

  // Handle session cleanup
  if (record.cleanup === "delete") {
    deleteChildSession(record.childSessionId);
  }

  // Schedule archive
  record.archiveAtMs = Date.now() + DEFAULT_ARCHIVE_AFTER_MS;
  record.cleanupCompletedAt = Date.now();
  persist();
}

function deleteChildSession(sessionId: string): void {
  try {
    const sessionDir = resolveSessionDir(sessionId);
    rmSync(sessionDir, { recursive: true, force: true });
    console.log(`[SubagentRegistry] Deleted child session: ${sessionId}`);
  } catch (err) {
    console.warn(`[SubagentRegistry] Failed to delete child session ${sessionId}:`, err);
  }

  // Also close the agent in Hub
  try {
    const hub = getHub();
    hub.closeAgent(sessionId);
  } catch {
    // Hub may not be available
  }
}

// ============================================================================
// Archive sweeper
// ============================================================================

function startSweeper(): void {
  if (sweepTimer) return;
  sweepTimer = setInterval(sweep, SWEEP_INTERVAL_MS);
  // Don't prevent process exit
  if (sweepTimer.unref) sweepTimer.unref();
}

function stopSweeper(): void {
  if (sweepTimer) {
    clearInterval(sweepTimer);
    sweepTimer = undefined;
  }
}

function sweep(): void {
  const now = Date.now();
  let removed = 0;

  for (const [runId, record] of subagentRuns) {
    if (record.archiveAtMs !== undefined && record.archiveAtMs <= now) {
      subagentRuns.delete(runId);
      resumedRuns.delete(runId);
      removed++;
    }
  }

  if (removed > 0) {
    persist();
    console.log(`[SubagentRegistry] Archived ${removed} completed run(s)`);
  }

  if (subagentRuns.size === 0) {
    stopSweeper();
  }
}

// ============================================================================
// Persistence helper
// ============================================================================

function persist(): void {
  try {
    saveSubagentRuns(subagentRuns);
  } catch (err) {
    console.error(`[SubagentRegistry] Failed to persist runs:`, err);
  }
}
