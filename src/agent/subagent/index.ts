/**
 * Subagent orchestration system.
 *
 * Provides child agent spawning, lifecycle management,
 * persistent registry, and result announcement flow.
 */

export type {
  SubagentRunOutcome,
  SubagentRunRecord,
  RegisterSubagentRunParams,
  SubagentAnnounceParams,
  SubagentSystemPromptParams,
} from "./types.js";

export {
  initSubagentRegistry,
  registerSubagentRun,
  listSubagentRuns,
  releaseSubagentRun,
  getSubagentRun,
  resetSubagentRegistryForTests,
  shutdownSubagentRegistry,
} from "./registry.js";

export {
  buildSubagentSystemPrompt,
  readLatestAssistantReply,
  formatAnnouncementMessage,
  runSubagentAnnounceFlow,
} from "./announce.js";
export type { FormatAnnouncementParams } from "./announce.js";

export {
  loadSubagentRuns,
  saveSubagentRuns,
  getSubagentStorePath,
} from "./registry-store.js";
