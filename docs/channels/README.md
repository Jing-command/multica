# Channel System

The Channel system connects external messaging platforms (Telegram, Discord, etc.) to the Hub's agent. Each platform is a **plugin** that translates platform-specific APIs into a unified interface.

> For media handling details (audio transcription, image/video description), see [media-handling.md](./media-handling.md).
> For message flow across all three I/O paths (Desktop / Web / Channel), see [message-paths.md](../message-paths.md).

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  credentials.json5                                          │
│  { channels: { telegram: { default: { botToken } } } }     │
└──────────────────────┬──────────────────────────────────────┘
                       │ loadChannelsConfig()
                       ▼
┌─────────────────────────────────────────────────────────────┐
│  Channel Manager (manager.ts)                               │
│                                                             │
│  startAll() → iterate plugins → startAccount() per account  │
│  ensureSubscribed() → listen for agent lifecycle events     │
│                                                             │
│  Incoming:                                                  │
│    routeIncoming() → 👀 ack + debouncer → agent.write()    │
│  Outgoing:                                                  │
│    activeRoute → aggregator → plugin.outbound.*()           │
│                                                             │
│  State:                                                     │
│    pendingRoutes[] ─(FIFO)→ activeRoute + activeAcks        │
│    ackBuffer[] ─(snapshot on flush)→ pendingRoutes[].acks   │
└──────────┬──────────────────────────────────────────────────┘
           │
           ▼
┌─────────────────────────────────────────────────────────────┐
│  InboundDebouncer (inbound-debouncer.ts)                    │
│  500ms idle window / 2000ms hard cap per conversationId     │
│  Each flush → snapshot route + acks → agent.write()         │
└──────────┬──────────────────────────────────────────────────┘
           │
           ▼
┌─────────────────────────────────────────────────────────────┐
│  Plugin Registry (registry.ts)                              │
│  registerChannel(plugin) / listChannels() / getChannel(id)  │
└──────────┬──────────────────────────────────────────────────┘
           │
           ▼
┌─────────────────────────────────────────────────────────────┐
│  Channel Plugins (e.g. telegram.ts)                         │
│                                                             │
│  config    — resolve account credentials                    │
│  gateway   — receive messages (polling / webhook)           │
│  outbound  — send replies, typing, reactions (👀 ack)       │
│  downloadMedia() — download media files to local disk       │
└─────────────────────────────────────────────────────────────┘
```

## Plugin Interface

Each channel plugin implements `ChannelPlugin` (defined in `types.ts`):

```typescript
interface ChannelPlugin {
  readonly id: string;                          // "telegram", "discord", etc.
  readonly meta: { name: string; description: string };
  readonly chunkerConfig?: BlockChunkerConfig;  // override text chunking per platform
  readonly config: ChannelConfigAdapter;        // credential resolution
  readonly gateway: ChannelGatewayAdapter;      // receive messages
  readonly outbound: ChannelOutboundAdapter;    // send replies
  downloadMedia?(fileId: string, accountId: string): Promise<string>;  // optional
}
```

### Three Adapters

| Adapter | Role | Key Methods |
|---------|------|-------------|
| **config** | Resolve credentials from `credentials.json5` | `listAccountIds()`, `resolveAccount()`, `isConfigured()` |
| **gateway** | Receive inbound messages from the platform | `start(accountId, config, onMessage, signal)` |
| **outbound** | Send replies back to the platform | `sendText()`, `replyText()`, `sendTyping?()`, `addReaction?()`, `removeReaction?()` |

### downloadMedia (optional)

Platforms that support media (voice, image, video, document) implement `downloadMedia()` to download files to `~/.super-multica/cache/media/` with UUID filenames. The Manager calls this before processing media.

## Message Flow

### Inbound (Platform → Agent)

```
User sends message in Telegram
  → grammy long-polling → onMessage callback
    → ChannelManager.routeIncoming()
      1. Update lastRoute (reply target)
      2. Start typing indicator (repeats every 5s)
      3. Add 👀 reaction to this message (ack)
      4. Push ack route to ackBuffer
      5. If media: routeMedia() → download → transcribe/describe → text
      6. Push text into InboundDebouncer

InboundDebouncer (per conversationId):
  ┌─ 500ms idle window: wait for more messages
  │  If another message arrives within 500ms, reset timer and append
  │  If 2000ms since first message, force-flush immediately
  └─ On flush:
       1. Snapshot lastRoute → route
       2. Snapshot ackBuffer → acks, clear buffer
       3. Push { route, acks } to pendingRoutes queue
       4. Call agent.write(combinedText)
```

All media is converted to text before the agent sees it. See [media-handling.md](./media-handling.md) for details.

### Outbound (Agent → Platform)

```
agent.write() queued → agent.run() starts
  → agent_start event
    1. Shift entry from pendingRoutes queue
    2. Set activeRoute = entry.route (stable for entire run)
    3. Set activeAcks = entry.acks

  → message_start (assistant)
    1. Create MessageAggregator wired to activeRoute
  → message_update (assistant)
    1. Feed text deltas to aggregator
  → message_end (assistant)
    1. Aggregator flushes final block, then null out
    (May repeat if agent does multi-turn tool calls)

  → Aggregator emits BlockReply chunks:
    Block 0: plugin.outbound.replyText()   // reply to original message
    Block N: plugin.outbound.sendText()     // follow-up messages

  → agent_end event
    1. Remove 👀 from all activeAcks messages
    2. Clear activeRoute and activeAcks
    3. If pendingRoutes is empty → stop typing
       If more pending → keep typing for next run
```

The **MessageAggregator** buffers streaming LLM output and splits it into blocks at natural text boundaries (paragraphs, code blocks). This is necessary because messaging platforms cannot consume raw streaming deltas.

## Route Queue Pattern

The channel system uses a FIFO queue to correctly route replies when multiple messages arrive while the agent is busy. This solves the "reply-to mismatch" problem where rapid-fire messages would cause replies to target the wrong original message.

### State Fields

| Field | Type | Purpose |
|-------|------|---------|
| `lastRoute` | `LastRoute \| null` | Where the most recent channel message came from. Updated on every incoming message. |
| `pendingRoutes` | `{ route, acks }[]` | FIFO queue of snapshotted routes, one per debouncer flush. Dequeued on `agent_start`. |
| `activeRoute` | `LastRoute \| null` | Route for the currently running agent. Set on `agent_start`, cleared on `agent_end`. Stable across all turns within one run. |
| `ackBuffer` | `LastRoute[]` | Accumulates 👀 ack targets between debouncer flushes. Snapshotted and cleared on each flush. |
| `activeAcks` | `LastRoute[]` | All messages with 👀 in the current run. Cleaned up on `agent_end`. |

### Lifecycle

```
Message A arrives          → lastRoute = A, ackBuffer = [A], 👀 on A
Message B arrives (50ms)   → lastRoute = B, ackBuffer = [A, B], 👀 on B
  ─── 500ms idle ───
Debouncer flushes          → pendingRoutes.push({ route: B, acks: [A, B] })
                             ackBuffer = [], agent.write("A\nB")

Message C arrives          → lastRoute = C, ackBuffer = [C], 👀 on C
  ─── 500ms idle ───
Debouncer flushes          → pendingRoutes.push({ route: C, acks: [C] })
                             ackBuffer = [], agent.write("C")

agent_start (run 1)        → activeRoute = B, activeAcks = [A, B]
  (agent processes "A\nB", replies to message B)
agent_end (run 1)          → remove 👀 from A and B, pendingRoutes still has 1 → keep typing

agent_start (run 2)        → activeRoute = C, activeAcks = [C]
  (agent processes "C", replies to message C)
agent_end (run 2)          → remove 👀 from C, pendingRoutes empty → stop typing
```

### Why agent_start / agent_end (not message_end)

In multi-turn agent runs (e.g. when the agent uses tools), `message_end` fires once per assistant message — potentially multiple times per `agent.run()`. Using `message_end` for state management would:
- Clear `activeRoute` mid-run, causing the next turn's aggregator to pick up the wrong route
- Remove 👀 too early (before the agent is actually done)
- Stop typing between tool-call turns

`agent_start` and `agent_end` fire exactly once per `agent.run()`, making them the correct lifecycle boundaries.

### lastRoute vs activeRoute

- **`lastRoute`** — global, updated on every incoming message. Used for: typing indicators, error reporting, creating aggregators when no activeRoute exists.
- **`activeRoute`** — per-run, set from queue on `agent_start`. Used for: reply targeting via aggregator. Guarantees that a run's reply goes to the correct message even if new messages arrive during processing.

Desktop and Web always receive agent events independently via their own mechanisms (IPC / Gateway). `clearLastRoute()` is called when a desktop/web message arrives to prevent channel forwarding.

## Inbound Debouncer

The `InboundDebouncer` (`inbound-debouncer.ts`) batches rapid-fire messages from the same conversation into a single `agent.write()` call. This prevents the agent from processing incomplete thoughts when users send multiple short messages quickly.

**Parameters:**
- `delayMs` (default 500ms) — idle window: how long to wait after each message before flushing
- `maxWaitMs` (default 2000ms) — hard cap: max time since first message before force-flushing

**Behavior:**
- Messages within 500ms of each other are combined with newlines
- Messages >500ms apart get independent flushes and separate agent runs
- No busy-awareness: each flush is independent regardless of agent state
- Each flush triggers a route snapshot (lastRoute + ackBuffer) pushed to the pendingRoutes queue

## Typing and Reaction Lifecycle

### Typing Indicator
- **Start:** `routeIncoming()` — starts a 5s repeating interval (Telegram requires re-sending "typing" every 5s)
- **Stop:** `agent_end` — only if `pendingRoutes` is empty (all queued runs complete). If runs remain queued, typing persists.
- **Also stops on:** `clearLastRoute()` (desktop/web message), `stopAccount()`, `stopAll()`, `agent_error`

### 👀 Ack Reaction
- **Add:** `routeIncoming()` — immediately on each message, before debouncing
- **Track:** pushed to `ackBuffer`, then snapshotted into `pendingRoutes[].acks` on debouncer flush, then moved to `activeAcks` on `agent_start`
- **Remove:** `agent_end` — iterates `activeAcks` and removes 👀 from each message
- **Also removed on:** `agent_error`

This ensures every queued message shows 👀 while waiting, and all 👀 are cleaned up precisely when the agent finishes processing that batch.

## Configuration

Channel credentials are stored in `~/.super-multica/credentials.json5` under the `channels` key:

```json5
{
  channels: {
    telegram: {
      default: {
        botToken: "123456:ABC-DEF..."
      }
    },
    // discord: { default: { botToken: "..." } },
  }
}
```

Each channel ID maps to accounts (keyed by account ID, typically `"default"`). The config adapter for each plugin knows how to extract and validate its credentials.

## Adding a New Plugin

1. Create `src/channels/plugins/<name>.ts` implementing `ChannelPlugin`
2. Register it in `src/channels/index.ts`:
   ```typescript
   import { <name>Channel } from "./plugins/<name>.js";
   registerChannel(<name>Channel);
   ```
3. Add the config shape to the `channels` section of `credentials.json5`

### Implementation Checklist

- [ ] `config` adapter: parse credentials from `credentials.json5`
- [ ] `gateway` adapter: connect to platform, normalize messages to `ChannelMessage`
- [ ] `outbound` adapter: `sendText`, `replyText`, optional `sendTyping`, `addReaction`, `removeReaction`
- [ ] `downloadMedia` (if platform supports media): download to `MEDIA_CACHE_DIR`
- [ ] Group filtering: only respond to messages directed at the bot
- [ ] Graceful shutdown: respect the `AbortSignal` passed to `gateway.start()`

## File Map

| File | Role |
|------|------|
| `src/channels/types.ts` | All type definitions (`ChannelPlugin`, `ChannelMessage`, `DeliveryContext`, etc.) |
| `src/channels/manager.ts` | `ChannelManager` — bridges plugins to the Hub's agent, route queue, typing/ack lifecycle |
| `src/channels/inbound-debouncer.ts` | `InboundDebouncer` — batches rapid-fire messages per conversationId |
| `src/channels/registry.ts` | Plugin registry (`registerChannel`, `listChannels`, `getChannel`) |
| `src/channels/config.ts` | Load channel config from `credentials.json5` |
| `src/channels/index.ts` | Bootstrap: register built-in plugins, re-export public API |
| `src/channels/plugins/telegram.ts` | Telegram plugin (grammy, long polling) |
| `src/channels/plugins/telegram-format.ts` | Markdown → Telegram HTML converter |
| `src/media/transcribe.ts` | Audio transcription (local whisper → OpenAI API) |
| `src/media/describe-image.ts` | Image description (OpenAI Vision API) |
| `src/media/describe-video.ts` | Video description (ffmpeg frame + Vision API) |
| `src/shared/paths.ts` | `MEDIA_CACHE_DIR` path constant |
| `src/hub/message-aggregator.ts` | Streaming text → block chunking for channel delivery |
| `packages/ui/src/components/message-list.tsx` | UI rendering with `stripUserMetadata()` for clean display |

## Current Plugins

| Plugin | Platform | Transport | Library |
|--------|----------|-----------|---------|
| `telegram` | Telegram | Long polling | grammy |

Planned: Discord, Feishu, LINE, etc.
