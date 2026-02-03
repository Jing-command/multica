/**
 * Subagent orchestration types.
 *
 * Models the lifecycle of spawned child agents:
 * created → started → ended → cleanup
 */

/** Final outcome of a subagent run */
export type SubagentRunOutcome = {
  status: "ok" | "error" | "timeout" | "unknown";
  error?: string | undefined;
};

/** Persistent record tracking a single subagent run */
export type SubagentRunRecord = {
  /** Unique run identifier (UUIDv7) */
  runId: string;
  /** Session ID of the child agent */
  childSessionId: string;
  /** Session ID of the parent (requester) agent */
  requesterSessionId: string;
  /** The task description / prompt given to the child */
  task: string;
  /** Optional human-readable label */
  label?: string | undefined;
  /** Session cleanup strategy after completion */
  cleanup: "delete" | "keep";
  /** Timestamp when the run was created */
  createdAt: number;
  /** Timestamp when the child agent started execution */
  startedAt?: number | undefined;
  /** Timestamp when the child agent finished */
  endedAt?: number | undefined;
  /** Final status of the run */
  outcome?: SubagentRunOutcome | undefined;
  /** Scheduled auto-archive time (ms since epoch) */
  archiveAtMs?: number | undefined;
  /** Whether the cleanup/announce flow has been initiated */
  cleanupHandled?: boolean | undefined;
  /** Timestamp when cleanup completed */
  cleanupCompletedAt?: number | undefined;
};

/** Parameters for registering a new subagent run */
export type RegisterSubagentRunParams = {
  runId: string;
  childSessionId: string;
  requesterSessionId: string;
  task: string;
  label?: string | undefined;
  cleanup?: "delete" | "keep" | undefined;
  timeoutSeconds?: number | undefined;
};

/** Parameters for the announce flow */
export type SubagentAnnounceParams = {
  runId: string;
  childSessionId: string;
  requesterSessionId: string;
  task: string;
  label?: string | undefined;
  cleanup: "delete" | "keep";
  outcome?: SubagentRunOutcome | undefined;
  startedAt?: number | undefined;
  endedAt?: number | undefined;
};

/** Parameters for building the subagent system prompt */
export type SubagentSystemPromptParams = {
  requesterSessionId: string;
  childSessionId: string;
  label?: string | undefined;
  task: string;
};
