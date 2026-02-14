# Super Multica

**Multiplexed Information & Computing Agent**

An always-on AI agent that pulls real data, runs real computation, and takes real action — monitoring, analyzing, and acting within user-defined authorization boundaries.

See [Memo](./docs/memo.md) for product vision, architecture, and roadmap.

## Project Structure

```
apps/
├── cli/           # Command-line interface
├── desktop/       # Electron desktop app (recommended)
├── gateway/       # NestJS WebSocket gateway
├── server/        # NestJS REST API server
├── web/           # Next.js web app
└── mobile/        # React Native mobile app

packages/
├── core/          # Agent engine, hub, channels
├── sdk/           # Gateway client SDK
├── ui/            # Shared UI components (Shadcn/Tailwind v4)
├── store/         # Zustand state management
├── hooks/         # React hooks
├── types/         # Shared TypeScript types
└── utils/         # Utility functions

skills/            # Bundled agent skills
```

## Quick Start

```bash
pnpm install
```

### Development

```bash
pnpm dev              # Desktop app (standalone, no Gateway needed)
pnpm dev:gateway      # Gateway only
pnpm dev:web          # Web app only
pnpm dev:all          # Gateway + Web
```

### Local Full-Stack Development

`pnpm dev:local` starts the entire stack locally (Gateway + Desktop + Web) with isolated data directories, useful for end-to-end development and testing.

**Setup:**

1. Copy `.env.example` to `.env` at the repo root
2. Fill in `TELEGRAM_BOT_TOKEN` (get from [@BotFather](https://t.me/BotFather))
3. Run `pnpm dev:local`

**What it starts:**

| Service | Address | Notes |
|---------|---------|-------|
| Gateway | `http://localhost:4000` | Telegram long-polling mode |
| Web | `http://localhost:3000` | OAuth login flow |
| Desktop | — | Connects to local Gateway + Web |

**Data isolation:** All data goes to `~/.super-multica-dev` and `~/Documents/Multica-dev`, separate from production `~/.super-multica`.

**Related commands:**

```bash
pnpm dev:local:archive    # Archive dev data and start fresh
```

## Architecture

```
Desktop App (standalone, recommended)
  └─ Hub (embedded)
     └─ Agent Engine

Web/Mobile Clients
  → Gateway (WebSocket, :3000)
    → Hub
      → Agent Engine
```

- **Desktop App**: Electron app with embedded Hub, no Gateway needed
- **Gateway**: WebSocket server for remote clients
- **Hub**: Agent lifecycle and event distribution

## Documentation

**Getting Started**

| Topic | Link |
|-------|------|
| Development guide | [docs/development.md](./docs/development.md) |
| Credentials & LLM providers | [docs/credentials.md](./docs/credentials.md) |
| CLI usage | [docs/cli.md](./docs/cli.md) |
| Skills & tools | [docs/skills-and-tools.md](./docs/skills-and-tools.md) |
| Package management | [docs/package-management.md](./docs/package-management.md) |
| Mobile development | [docs/mobile/guide.md](./docs/mobile/guide.md) |

**Architecture & Protocols**

| Topic | Link |
|-------|------|
| Product capabilities | [docs/product-capabilities.md](./docs/product-capabilities.md) |
| Message paths (Desktop/Web/Channel) | [docs/message-paths.md](./docs/message-paths.md) |
| Client streaming protocol | [docs/client-streaming-protocol.md](./docs/client-streaming-protocol.md) |
| Hub RPC protocol | [docs/rpc.md](./docs/rpc.md) |
| Exec approval protocol | [docs/exec-approval.md](./docs/exec-approval.md) |
| Time injection design | [docs/time-injection.md](./docs/time-injection.md) |
| Channel system | [docs/channels/README.md](./docs/channels/README.md) |
| Channel media handling | [docs/channels/media-handling.md](./docs/channels/media-handling.md) |
| Desktop login integration | [docs/auth/desktop-integration.md](./docs/auth/desktop-integration.md) |
