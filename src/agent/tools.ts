import type { AgentOptions } from "./types.js";
import { createCodingTools } from "@mariozechner/pi-coding-agent";
import type { AgentTool, AgentToolResult } from "@mariozechner/pi-agent-core";
import { createExecTool } from "./tools/exec.js";
import { createProcessTool } from "./tools/process.js";
import { createGlobTool } from "./tools/glob.js";
import { createWebFetchTool, createWebSearchTool } from "./tools/web/index.js";
import { createMemoryTools } from "./tools/memory/index.js";
import { createSessionsSpawnTool } from "./tools/sessions-spawn.js";
import { filterTools } from "./tools/policy.js";
import { isMulticaError, isRetryableError } from "../shared/errors.js";

// Re-export resolveModel from providers for backwards compatibility
export { resolveModel } from "./providers/index.js";

/** Options for creating tools */
export interface CreateToolsOptions {
  cwd: string;
  /** Profile ID for memory tools (optional) */
  profileId?: string | undefined;
  /** Base directory for profiles (optional) */
  profileBaseDir?: string | undefined;
  /** Whether this agent is a subagent (passed to sessions_spawn tool) */
  isSubagent?: boolean | undefined;
  /** Session ID of the agent (passed to sessions_spawn tool) */
  sessionId?: string | undefined;
}

type ToolErrorPayload = {
  error: true;
  message: string;
  name?: string;
  code?: string;
  retryable?: boolean;
  details?: Record<string, unknown>;
};

function toToolErrorPayload(error: unknown): ToolErrorPayload {
  if (isMulticaError(error)) {
    return {
      error: true,
      message: error.message,
      name: error.name,
      code: error.code,
      retryable: error.retryable,
      details: error.details,
    };
  }

  if (error instanceof Error) {
    return {
      error: true,
      message: error.message,
      name: error.name,
      retryable: isRetryableError(error),
    };
  }

  return {
    error: true,
    message: String(error),
  };
}

function toolErrorResult(error: unknown): AgentToolResult<ToolErrorPayload> {
  const payload = toToolErrorPayload(error);
  return {
    content: [{ type: "text", text: JSON.stringify(payload, null, 2) }],
    details: payload,
  };
}

function wrapTool<TParams, TResult>(
  tool: AgentTool<TParams, TResult>,
): AgentTool<TParams, TResult> {
  const execute = tool.execute;
  return {
    ...tool,
    execute: async (...args) => {
      try {
        return await execute(...args);
      } catch (error) {
        return toolErrorResult(error) as AgentToolResult<TResult>;
      }
    },
  };
}

/**
 * Create all available tools.
 * This returns the full set before policy filtering.
 */
export function createAllTools(options: CreateToolsOptions | string): AgentTool<any>[] {
  // Support legacy string argument for backwards compatibility
  const opts: CreateToolsOptions = typeof options === "string" ? { cwd: options } : options;
  const { cwd, profileId, profileBaseDir, isSubagent, sessionId } = opts;

  const baseTools = createCodingTools(cwd).filter(
    (tool) => tool.name !== "bash",
  ) as AgentTool<any>[];

  const execTool = createExecTool(cwd);
  const processTool = createProcessTool(cwd);
  const globTool = createGlobTool(cwd);
  const webFetchTool = createWebFetchTool();
  const webSearchTool = createWebSearchTool();

  const tools: AgentTool<any>[] = [
    ...baseTools,
    execTool as AgentTool<any>,
    processTool as AgentTool<any>,
    globTool as AgentTool<any>,
    webFetchTool as AgentTool<any>,
    webSearchTool as AgentTool<any>,
  ];

  // Add memory tools if profileId is provided
  if (profileId) {
    const memoryTools = createMemoryTools({
      profileId,
      baseDir: profileBaseDir,
    });
    tools.push(...memoryTools);
  }

  // Add sessions_spawn tool (will be filtered by policy for subagents)
  const sessionsSpawnTool = createSessionsSpawnTool({
    isSubagent: isSubagent ?? false,
    sessionId,
  });
  tools.push(sessionsSpawnTool as AgentTool<any>);

  return tools;
}

/**
 * Resolve tools for an agent with policy filtering.
 *
 * Applies 4-layer filtering:
 * 1. Profile (minimal/coding/web/full)
 * 2. Global allow/deny
 * 3. Provider-specific rules
 * 4. Subagent restrictions
 */
export function resolveTools(options: AgentOptions): AgentTool<any>[] {
  const cwd = options.cwd ?? process.cwd();

  // Create all tools (including memory tools if profileId is provided)
  const allTools = createAllTools({
    cwd,
    profileId: options.profileId,
    profileBaseDir: options.profileBaseDir,
    isSubagent: options.isSubagent,
    sessionId: options.sessionId,
  });

  // Apply policy filtering
  const filtered = filterTools(allTools, {
    config: options.tools,
    provider: options.provider,
    isSubagent: options.isSubagent,
  });

  return filtered.map((tool) => wrapTool(tool));
}

/**
 * Get all available tool names (for debugging/listing).
 * Note: Memory tools require profileId, so they are not included by default.
 */
export function getAllToolNames(cwd?: string): string[] {
  const tools = createAllTools({ cwd: cwd ?? process.cwd() });
  return tools.map((t) => t.name);
}

/**
 * Get all available tool names including memory tools (for debugging/listing).
 */
export function getAllToolNamesWithMemory(cwd?: string, profileId?: string): string[] {
  const tools = createAllTools({
    cwd: cwd ?? process.cwd(),
    profileId: profileId ?? "test-profile",
  });
  return tools.map((t) => t.name);
}
