import { Agent as PiAgentCore, type AgentEvent, type AgentMessage, type ThinkingLevel } from "@mariozechner/pi-agent-core";
import { getModel } from "@mariozechner/pi-ai";
import { createCodingTools } from "@mariozechner/pi-coding-agent";

export type AgentRunResult = {
  text: string;
  error?: string;
};

export type AgentLogger = {
  stdout?: NodeJS.WritableStream;
  stderr?: NodeJS.WritableStream;
};

export type AgentOptions = {
  provider?: string;
  model?: string;
  systemPrompt?: string;
  thinkingLevel?: ThinkingLevel;
  cwd?: string;
  logger?: AgentLogger;
};

function extractText(message: AgentMessage | undefined): string {
  if (!message || typeof message !== "object" || !("content" in message)) return "";
  const content = (message as { content?: Array<{ type: string; text?: string }> }).content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((c) => c.type === "text")
    .map((c) => c.text ?? "")
    .join("");
}

function toolDisplayName(name: string): string {
  const map: Record<string, string> = {
    read: "ReadFile",
    write: "WriteFile",
    edit: "EditFile",
    bash: "Bash",
    grep: "Grep",
    find: "FindFiles",
    ls: "ListDir",
  };
  return map[name] || name;
}

function formatToolArgs(name: string, args: unknown): string {
  if (!args || typeof args !== "object") return "";
  const record = args as Record<string, unknown>;
  const get = (key: string) => (record[key] !== undefined ? String(record[key]) : "");
  switch (name) {
    case "read":
      return get("path") || get("file");
    case "write":
      return get("path") || get("file");
    case "edit":
      return get("path") || get("file");
    case "grep":
      return [get("pattern"), get("path") || get("directory")].filter(Boolean).join(" ");
    case "find":
      return [get("glob") || get("pattern"), get("path") || get("directory")].filter(Boolean).join(" ");
    case "ls":
      return get("path") || get("directory");
    case "bash":
      return get("command");
    default:
      return "";
  }
}

function formatToolLine(name: string, args: unknown): string {
  const title = toolDisplayName(name);
  const argText = formatToolArgs(name, args);
  return argText ? `• Used ${title} (${argText})` : `• Used ${title}`;
}

export class Agent {
  private readonly agent: PiAgentCore;
  private readonly stdout: NodeJS.WritableStream;
  private readonly stderr: NodeJS.WritableStream;
  private lastAssistantText = "";
  private printedLen = 0;
  private streaming = false;

  constructor(options: AgentOptions = {}) {
    this.stdout = options.logger?.stdout ?? process.stdout;
    this.stderr = options.logger?.stderr ?? process.stderr;

    this.agent = new PiAgentCore();
    if (options.systemPrompt) this.agent.setSystemPrompt(options.systemPrompt);
    if (options.thinkingLevel) this.agent.setThinkingLevel(options.thinkingLevel);

    if (options.provider && options.model) {
      this.agent.setModel(getModel(options.provider, options.model));
    } else {
      this.agent.setModel(getModel("kimi-coding", "kimi-k2-thinking"));
    }

    const cwd = options.cwd ?? process.cwd();
    this.agent.setTools(createCodingTools(cwd));
    this.agent.subscribe((event) => this.handleEvent(event));
  }

  async run(prompt: string): Promise<AgentRunResult> {
    this.lastAssistantText = "";
    await this.agent.prompt(prompt);
    return { text: this.lastAssistantText, error: this.agent.state.error };
  }

  private handleEvent(event: AgentEvent) {
    switch (event.type) {
      case "message_start": {
        const msg = event.message;
        if (msg.role === "assistant") {
          this.streaming = true;
          this.printedLen = 0;
          const text = extractText(msg);
          if (text.length > 0) {
            this.stdout.write(text);
            this.printedLen = text.length;
          }
        }
        break;
      }
      case "message_update": {
        const msg = event.message;
        if (msg.role === "assistant") {
          const text = extractText(msg);
          if (text.length > this.printedLen) {
            this.stdout.write(text.slice(this.printedLen));
            this.printedLen = text.length;
          }
        }
        break;
      }
      case "message_end": {
        const msg = event.message;
        if (msg.role === "assistant") {
          const text = extractText(msg);
          if (text.length > this.printedLen) {
            this.stdout.write(text.slice(this.printedLen));
            this.printedLen = text.length;
          }
          if (this.streaming) this.stdout.write("\n");
          this.streaming = false;
          this.lastAssistantText = text;
        }
        break;
      }
      case "tool_execution_start":
        this.stderr.write(`${formatToolLine(event.toolName, event.args)}\n`);
        break;
      case "tool_execution_end":
        if (event.isError) {
          const errorText = extractText(event.result) || "Tool failed";
          this.stderr.write(`• Tool error (${toolDisplayName(event.toolName)}): ${errorText}\n`);
        }
        break;
      default:
        break;
    }
  }
}
