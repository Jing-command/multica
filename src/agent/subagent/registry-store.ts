/**
 * Persistent storage for subagent run records.
 *
 * File: ~/.super-multica/subagents/runs.json
 */

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { DATA_DIR } from "../../shared/index.js";
import type { SubagentRunRecord } from "./types.js";

const SUBAGENTS_DIR = join(DATA_DIR, "subagents");
const RUNS_FILE = join(SUBAGENTS_DIR, "runs.json");

interface SubagentRunsStore {
  version: 1;
  runs: Record<string, SubagentRunRecord>;
}

function ensureDir(): void {
  if (!existsSync(SUBAGENTS_DIR)) {
    mkdirSync(SUBAGENTS_DIR, { recursive: true });
  }
}

/** Get the path to the subagent store file (for testing) */
export function getSubagentStorePath(): string {
  return RUNS_FILE;
}

/** Load all persisted subagent runs */
export function loadSubagentRuns(): Map<string, SubagentRunRecord> {
  if (!existsSync(RUNS_FILE)) return new Map();

  try {
    const content = readFileSync(RUNS_FILE, "utf-8");
    const store = JSON.parse(content) as SubagentRunsStore;

    if (store.version !== 1) {
      console.warn(`[SubagentStore] Unknown store version: ${store.version}, ignoring`);
      return new Map();
    }

    return new Map(Object.entries(store.runs));
  } catch (err) {
    console.warn(`[SubagentStore] Failed to load runs:`, err);
    return new Map();
  }
}

/** Save all subagent runs to disk */
export function saveSubagentRuns(runs: Map<string, SubagentRunRecord>): void {
  ensureDir();

  const store: SubagentRunsStore = {
    version: 1,
    runs: Object.fromEntries(runs),
  };

  writeFileSync(RUNS_FILE, JSON.stringify(store, null, 2), "utf-8");
}
