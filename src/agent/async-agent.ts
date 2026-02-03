import { v7 as uuidv7 } from "uuid";
import type { AgentEvent } from "@mariozechner/pi-agent-core";
import { Agent } from "./runner.js";
import { Channel } from "./channel.js";
import type { AgentOptions, Message } from "./types.js";

const devNull = { write: () => true } as NodeJS.WritableStream;

/** Discriminated union of legacy Message (error fallback) and raw AgentEvent */
export type ChannelItem = Message | AgentEvent;

export class AsyncAgent {
  private readonly agent: Agent;
  private readonly channel = new Channel<ChannelItem>();
  private _closed = false;
  private queue: Promise<void> = Promise.resolve();
  private closeCallbacks: Array<() => void> = [];
  readonly sessionId: string;

  constructor(options?: AgentOptions) {
    this.agent = new Agent({
      ...options,
      logger: { stdout: devNull, stderr: devNull },
    });
    this.sessionId = this.agent.sessionId;

    // Forward raw AgentEvent into the channel
    this.agent.subscribe((event: AgentEvent) => {
      this.channel.send(event);
    });
  }

  get closed(): boolean {
    return this._closed;
  }

  /** Write message to agent (non-blocking, serialized queue) */
  write(content: string): void {
    if (this._closed) throw new Error("Agent is closed");

    this.queue = this.queue
      .then(async () => {
        if (this._closed) return;
        const result = await this.agent.run(content);
        // Normal text is delivered via message_end event; only handle errors here
        if (result.error) {
          this.channel.send({ id: uuidv7(), content: `[error] ${result.error}` });
        }
      })
      .catch((err) => {
        const message = err instanceof Error ? err.message : String(err);
        this.channel.send({ id: uuidv7(), content: `[error] ${message}` });
      });
  }

  /** Continuously read channel stream (AgentEvent + error Messages) */
  read(): AsyncIterable<ChannelItem> {
    return this.channel;
  }

  /** Returns a promise that resolves when the current message queue is drained */
  waitForIdle(): Promise<void> {
    return this.queue;
  }

  /** Register a callback to be invoked when the agent is closed */
  onClose(callback: () => void): void {
    if (this._closed) {
      // Already closed, fire immediately
      callback();
      return;
    }
    this.closeCallbacks.push(callback);
  }

  /** Close agent, stop all reads, fire close callbacks */
  close(): void {
    if (this._closed) return;
    this._closed = true;
    this.channel.close();
    for (const cb of this.closeCallbacks) {
      try {
        cb();
      } catch {
        // Don't let callback errors prevent other callbacks
      }
    }
    this.closeCallbacks = [];
  }
}
