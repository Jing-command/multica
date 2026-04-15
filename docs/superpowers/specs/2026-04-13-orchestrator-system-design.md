# Orchestrator System Design

Date: 2026-04-13
Status: Draft for review

## 1. Objective

Define a strict orchestration system for Multica where parent issues are decomposed, child issues are executed by worker agents, acceptance is handled by reviewer agents, and aggregation is handled by an orchestrator agent. The system must not rely on free-form comments or AI-maintained counters as workflow truth. Workflow truth lives in backend-managed structured state.

## 2. Goals

- Separate planning, execution, and review into distinct agent roles.
- Make child lifecycle progression deterministic and backend-controlled.
- Replace comment-driven orchestration with command and event-driven orchestration.
- Enforce file access restrictions as hard runtime boundaries, not prompt-only rules.
- Support worker-reviewer iteration with bounded review rounds.
- Escalate unresolved review loops back to orchestrator.
- Make the system idempotent, recoverable, and testable.

## 3. Non-goals

- Letting orchestrator directly implement code changes.
- Letting workers define their own acceptance criteria.
- Letting reviewers rewrite task scope or decomposition topology.
- Using comments as the authoritative source of task state.
- Letting AI increment review round counters, plan revision counters, or similar authority-bearing fields.

## 4. Core roles and responsibility boundaries

### 4.1 Orchestrator

The Orchestrator owns planning, decomposition, re-planning, escalation handling, and parent-level aggregation.

It may:
- inspect the parent issue
- create child specs
- assign worker and reviewer agents
- re-plan a child after escalation
- split or supersede child issues when the plan is wrong
- finalize the parent outcome

It must not:
- directly implement child work
- directly approve child completion
- act as the primary reviewer for normal child completion

### 4.2 Worker

The Worker owns execution of a single child issue.

It may:
- modify only explicitly allowed files
- attach evidence
- submit work for review
- report blockers
- revise work in response to reviewer feedback

It must not:
- change acceptance criteria
- self-approve completion
- change plan revision numbers
- increment review round counters
- write outside its authorized file boundary

### 4.3 Reviewer

The Reviewer owns acceptance.

It may:
- evaluate structured acceptance criteria
- issue `approved`, `changes_requested`, or `escalated` verdicts
- score each criterion independently
- attach review evidence

It must not:
- redefine child scope on its own
- execute implementation changes as part of normal review
- decompose the parent issue

## 5. Workflow truth model

The system has three truth layers:

1. **Structured database state** — the only authoritative workflow truth.
2. **Commands and emitted events** — authoritative transitions into and out of states.
3. **Comments and narrative text** — explanatory only, never workflow truth.

Comments may still exist for human readability, but they do not advance state, create rounds, close rounds, or finalize tasks.

## 6. Child issue state model

Each child has an execution state and a review verdict dimension.

### 6.1 Execution state

- `todo`
- `in_progress`
- `awaiting_review`
- `done`
- `blocked`

Rules:
- Worker start moves `todo -> in_progress`.
- `submit-review` moves `in_progress -> awaiting_review`.
- Reviewer `approved` moves `awaiting_review -> done`.
- Reviewer `changes_requested` moves `awaiting_review -> in_progress`.
- `report-blocked` moves worker-owned active work into `blocked`.
- Orchestrator may reopen or supersede blocked or escalated children through explicit orchestration actions.

### 6.2 Review verdict dimension

- `pending`
- `changes_requested`
- `approved`
- `escalated`

Rules:
- A child enters `pending` when a formal review submission is created.
- Reviewer sets the terminal verdict for the active review round.
- `approved` is required before a child is considered complete.
- `escalated` routes control back to Orchestrator.

### 6.3 Review rounds

A review round is a backend-created record pairing:
- one formal worker `submit-review`
- one terminal reviewer verdict (`approved`, `changes_requested`, or `escalated`)

Important constraints:
- Review rounds are created only by backend command handling.
- Comments never create or close a round.
- AI does not increment round counters.
- The backend enforces `current_round <= max_rounds`.
- If `changes_requested` would exceed max rounds, the backend converts the flow into escalation instead of opening another worker-review loop.

## 7. Parent orchestration state model

Parent issues carry a separate orchestration status:

- `planning`
- `dispatching`
- `running`
- `needs_replan`
- `awaiting_aggregation`
- `done`
- `blocked`
- `aborted`

The parent orchestration status is separate from the final parent outcome. Status describes process position; final outcome describes the terminal result selected at parent finalization.

Rules:
- Creation of child specs and assignments drives `planning -> dispatching -> running`.
- Any escalated child can move the parent into `needs_replan`.
- When all active children are terminal, parent enters `awaiting_aggregation`.
- Only Orchestrator may move parent to `done`, `blocked`, or `aborted`.
- Parent progression is event-driven from child lifecycle records, not from comments.

## 8. Structured data model

The design introduces the following authority-bearing objects.

### 8.1 `child_spec`

Stores the current executable definition of a child task.

Fields include:
- `id`
- `parent_issue_id`
- `child_issue_id`
- `worker_agent_id`
- `reviewer_agent_id`
- `scope_summary`
- `status`
- `active_plan_revision`
- `max_review_rounds`
- `permission_snapshot_id`
- timestamps

### 8.2 `child_acceptance_criteria`

Stores structured acceptance criteria for a child.

Fields include:
- `id`
- `child_spec_id`
- `criterion_key`
- `description`
- `required`
- `sort_order`

Reviewer decisions are evaluated against these records, not against free-form worker text.

### 8.3 `child_review_round`

Stores each formal review cycle.

Fields include:
- `id`
- `child_spec_id`
- `round_number`
- `submitted_by_agent_id`
- `reviewed_by_agent_id`
- `submission_evidence`
- `verdict`
- `summary`
- `created_at`
- `closed_at`

This table is the source of truth for round counting.

### 8.4 `child_review_criterion_result`

Stores reviewer verdicts per acceptance criterion.

Fields include:
- `id`
- `review_round_id`
- `criterion_id`
- `verdict` (`approved`, `failed`, `not_applicable`)
- `notes`

### 8.5 `child_escalation`

Stores escalations that require orchestrator intervention.

Fields include:
- `id`
- `child_spec_id`
- `review_round_id`
- `reason_type`
- `reason_summary`
- `status`
- `resolution_action`

### 8.6 `repo_permission_policy`

Stores workspace or repository-wide hard permission policy.

Fields include:
- `id`
- `workspace_id`
- `policy_name`
- `default_mode`
- `protected_paths`
- `allowed_path_rules`
- `allowed_tools`
- `shell_command_policy`

This is the source policy. Workers do not directly edit it.

### 8.7 `child_permission_snapshot`

Stores the exact permission boundary for one child execution context.

Fields include:
- `id`
- `child_spec_id`
- `policy_source_id`
- `allowed_paths`
- `read_only_paths`
- `blocked_paths`
- `allowed_tools`
- `shell_policy`
- `created_at`

This snapshot freezes the worker boundary for that child run, so later policy edits do not retroactively change in-flight execution semantics.

### 8.8 `plan_revision`

Stores backend-managed plan versioning.

Fields include:
- `id`
- `child_spec_id`
- `revision_number`
- `reason`
- `created_by_actor_type`
- `created_by_actor_id`
- `change_summary`
- `created_at`

Important constraints:
- Revision numbers are allocated by backend transaction logic only.
- AI may request re-plan, but cannot choose or increment revision numbers.

## 9. Formal command protocol

Comments are no longer workflow commands. The workflow advances only through explicit commands.

### 9.1 Worker commands

#### `submit-review`

Purpose:
- declare that the current child work is ready for formal review

Required payload:
- `child_spec_id`
- `idempotency_key`
- evidence bundle or references
- optional implementation summary

Effects:
- create a new `child_review_round`
- move execution state to `awaiting_review`
- emit `child.review_submitted`

#### `report-blocked`

Purpose:
- declare the worker cannot proceed without intervention

Required payload:
- `child_spec_id`
- `idempotency_key`
- blocker type
- blocker summary
- optional evidence

Effects:
- move execution state to `blocked`
- emit `child.blocked_reported`

#### `attach-evidence`

Purpose:
- add structured evidence without changing workflow state

Effects:
- append evidence record only

### 9.2 Reviewer commands

#### `review`

Purpose:
- close the active review round with a terminal verdict

Required payload:
- `child_spec_id`
- `review_round_id`
- `idempotency_key`
- verdict
- criterion-level results
- summary

Allowed verdicts:
- `approved`
- `changes_requested`
- `escalated`

Effects:
- `approved` => child execution state `done`, emit `child.review_approved`
- `changes_requested` => child execution state `in_progress`, emit `child.review_changes_requested`
- `escalated` => create `child_escalation`, emit `child.review_escalated`

### 9.3 Orchestrator commands

#### `create-child`
Create a child spec, acceptance criteria, worker assignment, reviewer assignment, and permission snapshot.

#### `replan-child`
Create a new backend-managed plan revision and update the active child spec accordingly.

#### `split-child`
Supersede an invalid child and replace it with multiple new child specs.

#### `finalize-parent`
Aggregate the terminal results of all active children and set parent outcome.

## 10. Event model

Events are emitted from committed backend state transitions.

Core events:
- `child.review_submitted`
- `child.review_approved`
- `child.review_changes_requested`
- `child.review_escalated`
- `child.blocked_reported`
- `child.escalation_opened`
- `child.replanned`
- `child.superseded`
- `parent.all_children_terminal`
- `parent.awaiting_aggregation`
- `parent.finalized`

Rules:
- Events are derivative of committed DB state.
- Consumers may rebuild queues or notifications from events.
- Event replay must be safe and idempotent.

## 11. Idempotency and transaction rules

Every authority-bearing command requires an idempotency key.

Must be idempotent:
- `submit-review`
- `review`
- `report-blocked`
- `replan-child`
- `split-child`
- `finalize-parent`

Transaction rules:
- state update and event emission registration happen in the same transaction boundary
- round creation and state movement happen atomically
- review verdict application and criterion result persistence happen atomically
- revision allocation happens atomically with spec update

## 12. Permission isolation and execution environment

The worker permission model is a hard boundary implemented in the execution environment, not a natural-language instruction.

### 12.1 Global policy

The repository has a global permission policy defining:
- files that workers may never modify
- files or directories that only certain task classes may modify
- allowed tool classes
- shell command restrictions

### 12.2 Per-child snapshot

When Orchestrator creates a child, backend materializes a `child_permission_snapshot` from the current global policy plus the child scope.

### 12.3 Runtime enforcement layers

The execution environment enforces three layers:

1. **Filesystem boundary**
   - unauthorized paths are mounted or exposed as read-only or not exposed at all
   - worker cannot physically write to blocked files

2. **Tooling guardrails**
   - edit, patch, git, and shell write operations are checked against the snapshot
   - denied operations fail before mutation

3. **Diff audit before submission**
   - `submit-review` validates that produced diffs stay within the authorized boundary
   - out-of-bound changes are rejected automatically

### 12.4 Role-specific boundaries

- Worker: narrowest permissions, path-scoped
- Reviewer: normally read-only plus review command privileges
- Orchestrator: planning and coordination authority, not broad implementation write authority by default

## 13. Aggregation protocol

Parent aggregation is based on structured child outcomes.

### 13.1 Aggregation trigger

Aggregation begins when all active children are terminal with one of:
- `done`
- `blocked`
- `superseded`
- `aborted`

### 13.2 Parent finalization outcomes

Orchestrator may finalize parent with a separate final outcome field:
- `complete`
- `complete_with_exceptions`
- `blocked`
- `aborted`

On successful finalization, the parent orchestration status becomes terminal (`done`, `blocked`, or `aborted`) and the final outcome field captures the specific result.

### 13.3 Aggregation summary

The summary may be rendered into comments for humans, but the finalization decision must be computed from structured child outcomes, not from comment parsing.

## 14. Escalation and re-planning

Escalation happens when:
- reviewer determines the work cannot be approved within normal revision flow
- max review rounds would be exceeded
- worker reports a blocker requiring plan change
- acceptance criteria are internally inconsistent

Orchestrator resolution actions include:
- revise scope
- replace worker
- replace reviewer
- split child
- supersede child
- abort child

Every re-plan creates a new backend-managed plan revision.

## 15. Failure recovery and resumability

The system must be resumable without relying on AI memory.

Principles:
- DB state is authoritative
- queues are reconstructable from state plus events
- in-flight command retries are safe via idempotency keys
- recovery scanners can detect stuck states and re-dispatch tasks

Recovery scenarios:
- worker crashed after producing output but before submit
- reviewer crashed after reading but before verdict
- orchestrator crashed after creating some but not all children
- event consumer missed an event and rebuilds from stored state

## 16. Testing contract

### 16.1 State machine tests

Verify legal and illegal transitions for:
- child execution state
- review verdict state
- parent orchestration state

### 16.2 Command transaction tests

Verify each formal command:
- persists state atomically
- emits the expected event
- respects idempotency
- rejects invalid actor or invalid state

### 16.3 Counter authority tests

Verify backend, not AI, controls:
- review round numbering
- max-round escalation behavior
- plan revision numbering

### 16.4 Permission boundary tests

Verify:
- blocked paths cannot be edited by worker
- read-only paths remain unchanged
- out-of-scope diffs fail `submit-review`
- reviewer and orchestrator do not inherit unintended write access

### 16.5 End-to-end orchestration tests

Verify:
- normal child pass flow
- reviewer-requested iteration loop
- max-round escalation
- blocked child with orchestrator re-plan
- parent aggregation from structured terminal child states

## 17. MVP scope

The MVP must include:
- distinct Orchestrator, Worker, Reviewer roles
- structured child spec and structured acceptance criteria
- formal worker and reviewer commands
- backend-managed review rounds
- backend-managed plan revisions
- event-driven parent wakeup and aggregation
- global hard permission policy
- per-child permission snapshots
- idempotent command handling
- basic recovery scanning

The MVP does not require:
- full policy engine sophistication
- complex multi-level child trees
- advanced scheduling optimization
- natural-language comment parsing for workflow control

## 18. Phase plan

### Phase 1

- introduce role separation
- introduce structured child spec and AC records
- add formal commands
- add backend-managed review rounds
- move parent wakeup to child lifecycle events
- implement baseline hard permission enforcement

### Phase 2

- add escalation records
- add re-plan and revision handling
- add max-round automatic escalation
- add recovery scanners

### Phase 3

- add richer permission policy tooling
- add split-child and supersession flows
- add topology versioning and more advanced aggregation logic

## 19. Key design decisions

1. Acceptance is owned by Reviewer, not Orchestrator and not humans by default.
2. Workflow truth is structured state, not comments.
3. Review round counting is backend-controlled and transactionally enforced.
4. Plan revision counting is backend-controlled and transactionally enforced.
5. Worker file restrictions are enforced physically by runtime boundaries.
6. Parent orchestration reacts to events, not free-form comment activity.
7. Aggregation uses structured child outcomes, not text summarization as source of truth.

## 20. Result

This design turns Multica orchestration from a comment-triggered best-effort workflow into a strict backend-governed orchestration system with role separation, bounded review loops, hard execution boundaries, and recoverable state transitions.