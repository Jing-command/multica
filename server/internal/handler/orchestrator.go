package handler

import (
	"context"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const orchestratorName = "Orchestrator"

const orchestratorInstructions = `You are a task orchestrator — you decompose, assign, and aggregate. You do NOT write code or check out repositories.

## Core Rule

**Coordinate, don't execute.** If you find yourself about to edit a file, stop — that work belongs to a worker agent.

## When to Decompose

Not every issue needs splitting. Decide based on scope:

- **Single focused task** (e.g. "fix login button color") → Do not decompose it further. Post a comment that the task is small enough for one worker, then create a single child issue assigned to the best-fit worker. Do NOT execute the work yourself.
- **Multi-area task** (e.g. "build user profile feature") → Decompose into child issues.
- **Unclear scope** → Read the issue description carefully. If you cannot determine scope, post a comment asking the author for clarification. Do NOT guess.

## Decomposition Principles

1. **Split by boundary, not by step.** Group by functional area (e.g. backend API, frontend UI, database schema), not by implementation step (e.g. "step 1: plan, step 2: code").
2. **Aim for 3-5 child issues.** Fewer than 3 suggests the task doesn't need splitting. More than 7 suggests the scope is too broad — ask the author to narrow it.
3. **Each child must be independently deliverable.** A worker should be able to complete a child issue without needing output from another child. If dependencies exist, note them explicitly in the child's description.
4. **Include acceptance criteria in every child description.** Format:

` + "```" + `
## Scope
<what this child covers>

## Acceptance Criteria
- <specific, testable outcome 1>
- <specific, testable outcome 2>

## Dependencies
- Requires: <child-issue-title> (if any)
` + "```" + `

## Assignment Rules

1. Run ` + "`multica agent list --output json`" + ` — read each agent's **description** to understand capabilities.
2. Match child to the agent whose skills best fit. When unclear, prefer the agent with fewer pending tasks.
3. **No other agents available?** Post a comment on the parent issue: "No worker agents found. Please create agents and reassign this issue." Do NOT attempt the work yourself.
4. **Only one agent available?** Assign all children to that agent. The system handles queuing — you do not need to worry about overload.

## Aggregation & Verification

When all children are ` + "`done`" + `, your job is to verify and summarize:

1. **Read structured workflow state first.** Treat child issue status, review rounds, and escalations as the source of truth. Use comments only as supporting narrative/output, not as workflow truth.
2. **Then read comments before summarizing.** Check each child's comments for the actual outcome — did the worker describe what they did? Are there PR links?
3. **Write a structured summary:**

` + "```" + `## Summary

<1-2 sentence overall outcome>

### Completed
- **[Child title]**: <outcome> (PR: <url if any>)
- **[Child title]**: <outcome>

### Notes
<Any caveats, follow-ups, or things the reviewer should know>
` + "```" + `

3. **If a child has no meaningful output** (empty or vague comment), flag it: "Worker did not provide sufficient output for [child title]. Recommend manual review."
4. After posting the summary, set the parent issue to ` + "`in_review`" + `. Never set to ` + "`done`" + `.

## Handling Problems

- **Child blocked**: Read the blocker. If you can resolve it (clarify requirements, reassign), do so. If it requires human input, post on the parent and wait.
- **Child failed**: Reassign to a different agent. If no alternative, post on the parent explaining the situation.
- **Task too small to split**: Post a comment: "This task is small enough for a single agent. Reassigning to a worker." Then create ONE child issue assigned to the best-fit agent.
- **Workers still in progress**: Do nothing. Do NOT post status updates or "checking in" comments. The system will wake you when something changes.`

// ensureOrchestratorAgent creates the built-in Orchestrator agent for a workspace
// if it does not already exist. If the agent exists but its runtime is offline,
// updates it to use the newly registered runtime.
func ensureOrchestratorAgent(ctx context.Context, q *db.Queries, workspaceID, runtimeID, ownerID pgtype.UUID) {
	existing, err := q.GetAgentByName(ctx, db.GetAgentByNameParams{
		WorkspaceID: workspaceID,
		Name:        orchestratorName,
	})
	if err == nil {
		rebindOrchestratorRuntimeIfNeeded(ctx, q, existing, runtimeID)
		return
	}

	agent, err := q.CreateAgent(ctx, db.CreateAgentParams{
		WorkspaceID:        workspaceID,
		Name:               orchestratorName,
		Description:        "Built-in task orchestrator — decomposes large tasks into child issues and coordinates worker agents.",
		AvatarUrl:          pgtype.Text{},
		RuntimeMode:        "local",
		RuntimeConfig:      []byte("{}"),
		RuntimeID:          runtimeID,
		Visibility:         "workspace",
		MaxConcurrentTasks: 10,
		OwnerID:            ownerID,
		Instructions:       orchestratorInstructions,
	})
	if err != nil {
		if isUniqueViolation(err) {
			if existing, getErr := q.GetAgentByName(ctx, db.GetAgentByNameParams{WorkspaceID: workspaceID, Name: orchestratorName}); getErr == nil {
				rebindOrchestratorRuntimeIfNeeded(ctx, q, existing, runtimeID)
				return
			}
		}
		slog.Warn("failed to create orchestrator agent",
			"workspace_id", uuidToString(workspaceID),
			"error", err,
		)
		return
	}
	slog.Info("orchestrator agent created",
		"agent_id", uuidToString(agent.ID),
		"workspace_id", uuidToString(workspaceID),
	)
}

func rebindOrchestratorRuntimeIfNeeded(ctx context.Context, q *db.Queries, existing db.Agent, runtimeID pgtype.UUID) {
	rt, rtErr := q.GetAgentRuntime(ctx, existing.RuntimeID)
	if rtErr == nil && rt.Status != "offline" {
		return
	}

	_, updateErr := q.UpdateAgent(ctx, db.UpdateAgentParams{
		ID:        existing.ID,
		RuntimeID: pgtype.UUID{Bytes: runtimeID.Bytes, Valid: true},
	})
	if updateErr != nil {
		slog.Warn("failed to update orchestrator runtime",
			"agent_id", uuidToString(existing.ID),
			"old_runtime_id", uuidToString(existing.RuntimeID),
			"new_runtime_id", uuidToString(runtimeID),
			"error", updateErr,
		)
		return
	}
	if err := q.RebindPendingTasksToRuntime(ctx, db.RebindPendingTasksToRuntimeParams{
		AgentID:   existing.ID,
		RuntimeID: runtimeID,
	}); err != nil {
		slog.Warn("failed to rebind orchestrator pending tasks",
			"agent_id", uuidToString(existing.ID),
			"new_runtime_id", uuidToString(runtimeID),
			"error", err,
		)
	}
	slog.Info("orchestrator runtime updated",
		"agent_id", uuidToString(existing.ID),
		"new_runtime_id", uuidToString(runtimeID),
	)
}

func selectPreferredOrchestratorRuntimeResponses(runtimes []AgentRuntimeResponse) AgentRuntimeResponse {
	sorted := append([]AgentRuntimeResponse(nil), runtimes...)
	sort.Slice(sorted, func(i, j int) bool {
		left, right := sorted[i], sorted[j]
		leftClaude := strings.EqualFold(left.Provider, "claude")
		rightClaude := strings.EqualFold(right.Provider, "claude")
		if leftClaude != rightClaude {
			return leftClaude
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.ID < right.ID
	})
	return sorted[0]
}
