# Agent-Authored Comment Design

Date: 2026-04-20
Status: Proposed
Scope: Restore legitimate agent-authored comment and agent-thread semantics without reopening header-based actor spoofing

## Summary

The recent actor provenance hardening deliberately changed normal comment creation to always resolve as a member actor. That closed the `X-Agent-ID` / `X-Task-ID` spoofing hole, but it also removed the only path the system had for producing an `author_type=agent` comment on demand.

That missing capability now shows up as a semantic gap in comment-thread automation. Some thread rules implicitly depended on the existence of a real agent-authored thread root, but the old integration helper created that root through spoofed headers rather than a verified agent execution path.

This design restores agent-authored comments as an explicit, verified capability. Normal comment creation remains member-only. Agent-authored comments become possible only through a separate server-verified path bound to real execution context.

## Goals

- Preserve the security fix that prevents normal control-plane requests from spoofing agent comments
- Reintroduce legitimate `author_type=agent` comments through explicit verified execution context
- Restore the product concept of a real agent-started thread
- Keep comment automation semantics aligned with real provenance instead of client headers
- Ensure tests validate true agent-thread behavior rather than relying on spoof helpers

## Non-Goals

- Adding a new agent token or session model
- Allowing arbitrary users to post as any agent
- Expanding this round into agent attachments or agent upload flows
- Redesigning daemon authentication
- Reworking the full comment automation system beyond the provenance boundary needed here

## Current Problem

After provenance hardening, `server/internal/handler/comment.go` resolves ordinary comment creation using authenticated member identity only. That is correct for security, but it means a plain HTTP request can no longer create an `author_type=agent` comment even in cases where the system conceptually needs one.

The previous integration test coverage for agent-thread behavior depended on a helper that sent spoofed agent headers through the regular comment endpoint. That test passed only because the old provenance model trusted mixed client inputs. Now that spoofing is closed, the test no longer creates a real agent thread root, so thread automation semantics drift away from the product behavior we actually want.

The issue is therefore not that provenance hardening broke a valid feature. The issue is that the codebase never had a legitimate path for agent-authored comments in the first place.

## Design Principles

### Rule 1: Normal comment creation stays member-only

The existing `CreateComment` path remains the default for authenticated user requests. It must continue to ignore `X-Agent-ID` and `X-Task-ID` for author attribution.

### Rule 2: Agent-authored comments require explicit verified context

An `author_type=agent` comment is not a client-selected variant of normal comment creation. It is a separate capability that the server grants only after proving the relevant task, agent, issue, and workspace relationship.

### Rule 3: Thread semantics depend on real stored provenance

Whether a thread is an “agent thread” should be determined from stored comment provenance (`author_type=agent` created via the verified path), not from request headers or test-only shortcuts.

### Rule 4: The boundary must be explicit in code

The implementation should not reintroduce a generic mixed-trust actor helper on comment paths. Member comments and verified agent comments must use separate code paths with clear intent.

## Proposed Model

### Member comment path

The existing route remains responsible for ordinary comments:

- authenticate user
- load issue in current workspace
- validate request body and optional parent comment
- create comment as `author_type=member`

This path silently ignores spoofed agent headers, preserving the current anti-spoof posture.

### Verified agent comment path

Add a separate comment creation path dedicated to real agent-authored comments. The exact route name is an implementation detail, but the API shape should be explicit, for example:

- `POST /api/issues/{id}/agent-comments`

This path performs all of the following before writing the comment:

1. authenticate the caller
2. load the issue in the active workspace
3. read `X-Agent-ID` and `X-Task-ID`
4. verify the task exists and belongs to the specified agent
5. verify the task is for the target issue
6. verify the agent belongs to the same workspace and is eligible to act
7. only then create the comment with `author_type=agent`, `author_id=agentID`

The caller is still an authenticated user, but the comment provenance comes from server-verified workflow context rather than the user selecting an actor type.

## Data Flow

### Normal member comment

```text
HTTP request -> auth middleware -> load issue -> resolve member actor -> create comment(author_type=member)
```

### Verified agent comment

```text
HTTP request -> auth middleware -> load issue -> verify task/agent/issue/workspace binding -> create comment(author_type=agent)
```

### Thread continuation

Once an agent-authored root comment exists through the verified path:

- replies under that root become a real agent thread
- member replies in that thread are interpreted as continuing a conversation with the agent unless the reply clearly redirects elsewhere
- automation decisions derive from the stored root provenance, not from headers on the reply request

## API Shape

### Request body

The first version should stay intentionally narrow:

```json
{
  "content": "Here is my analysis.",
  "parent_id": "optional-comment-id",
  "type": "comment"
}
```

### Deliberately excluded in v1

Do not add the following in this round:

- attachment IDs on agent-authored comments
- agent-specific upload paths
- arbitrary historic task replay
- freeform posting as an agent outside verified task context

These are separate product and security decisions and are not required to restore real agent-thread semantics.

### Internal code shape

To avoid duplication while keeping provenance explicit, the implementation should extract small internal helpers around shared mechanics, such as:

- parent comment loading scoped to the issue
- shared comment insert helper parameterized by an already-resolved actor
- verified agent comment context helper

The important part is not the exact helper names. The important part is that `CreateComment` and the new agent-comment path do not share a mixed-trust actor-resolution entry point.

## Error Semantics

### Member path

- spoofed `X-Agent-ID` / `X-Task-ID` are ignored
- valid member action still succeeds as a member-authored comment

### Verified agent path

Use explicit failure when required context is missing or invalid:

- missing `X-Agent-ID` or `X-Task-ID` -> `400`
- task/agent mismatch -> `404` or existing verified-path equivalent
- task/issue mismatch -> `404`
- cross-workspace mismatch -> `404`
- archived or otherwise unusable agent/task state -> `404` or `409`, depending on current handler conventions

The exact status code choice should follow existing verified workflow conventions. The key property is that this path fails closed.

## Automation Semantics

### Agent-authored comments do not trigger `on_comment`

A verified agent-authored comment represents agent output, not a fresh member request. Creating that comment must not enqueue a new task for the same agent.

### Member replies inside a real agent thread may trigger the assignee agent

If a thread root is a real verified agent-authored comment, a member reply under that root should usually be treated as continuing the conversation with that agent. That preserves the product meaning of agent threads.

### `@all` in an agent thread suppresses `on_comment`

A reply containing `@all` remains a broadcast rather than a direct request for agent work. In a real agent thread, that should continue to suppress `on_comment`.

### Explicit mentions still take precedence

If a reply explicitly mentions another agent, mention-trigger semantics still apply. The system should keep the current priority ordering where explicit targeted mentions beat ambient thread semantics.

### Member-started and agent-started threads remain meaningfully different

The existing intuition should stay true:

- member-started thread -> default assumption is human-to-human conversation unless the assignee agent is explicitly brought in
- agent-started thread -> default assumption is human continuing the conversation with the agent unless the reply clearly redirects elsewhere

That distinction is only trustworthy if the thread root provenance is real.

## Testing Strategy

### Handler coverage

Add focused tests for the new boundary:

1. verified agent comment creation requires valid task-bound context
2. verified path produces `author_type=agent`
3. verified agent-authored comments do not enqueue `on_comment`
4. member replies in a verified agent thread preserve agent-conversation semantics
5. `@all` in a verified agent thread suppresses `on_comment`
6. existing anti-spoof tests still pass on the normal member path

### Orchestration or verified-path coverage

Where the verified execution context is sourced from workflow/task logic, tests should confirm:

- valid task-bound context can successfully create an agent-authored comment
- header-only input without a valid binding cannot do so

### Integration coverage

Replace any helper that currently creates “agent comments” through spoofed headers. Integration tests should seed a real verified agent comment root through the new path, then exercise thread behavior on top of that root.

The current `@all in agent thread suppresses on_comment` integration test should be rewritten against the legitimate capability rather than deleted.

## Migration Strategy

No database migration is required if the existing `comment` schema already supports `author_type=agent` and `author_id` for agent rows. This round is primarily handler and test restructuring.

If implementation discovers that some workflow metadata is required to validate the new path cleanly, that should be kept as small as possible and justified separately.

## Trade-offs

### Why not put agent comments back into `CreateComment`

That would collapse the explicit provenance boundary we just restored. The same route would again need to infer whether the request is acting as a member or an agent, which is the exact class of ambiguity that produced the spoofing vulnerability.

### Why not simply delete the old integration test

Because the desired product behavior is still valuable. Users should be able to interact inside a real agent-started thread. The test was wrong in how it created that thread root, not wrong in asserting the product semantic.

### Why not allow any assigned agent to comment without task context

That would widen the capability surface unnecessarily. Task-bound verification keeps the first version narrow, explicit, and aligned with the rest of workflow provenance hardening.

## Final Recommendation

Implement a dedicated verified agent-comment path while keeping ordinary comment creation member-only.

That gives the system a legitimate way to create `author_type=agent` comments, restores real agent-thread semantics, preserves the provenance security fix, and converts the current integration failure from a spoof-dependent artifact into a properly verified product behavior.
