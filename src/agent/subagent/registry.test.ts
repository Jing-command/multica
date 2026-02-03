import { describe, it, expect, beforeEach } from "vitest";
import {
  registerSubagentRun,
  listSubagentRuns,
  getSubagentRun,
  releaseSubagentRun,
  resetSubagentRegistryForTests,
  shutdownSubagentRegistry,
} from "./registry.js";

// Note: These tests exercise the registry's in-memory state management.
// They do NOT test the full lifecycle (which requires a live Hub + AsyncAgent).

beforeEach(() => {
  resetSubagentRegistryForTests();
});

describe("subagent registry", () => {
  it("registers a run and retrieves it by ID", () => {
    const record = registerSubagentRun({
      runId: "run-1",
      childSessionId: "child-1",
      requesterSessionId: "parent-1",
      task: "Analyze code",
      label: "Code Analysis",
    });

    expect(record.runId).toBe("run-1");
    expect(record.childSessionId).toBe("child-1");
    expect(record.requesterSessionId).toBe("parent-1");
    expect(record.task).toBe("Analyze code");
    expect(record.label).toBe("Code Analysis");
    expect(record.cleanup).toBe("delete"); // default
    expect(record.createdAt).toBeGreaterThan(0);
    expect(record.startedAt).toBeGreaterThan(0); // set by watchChildAgent

    const retrieved = getSubagentRun("run-1");
    expect(retrieved).toBe(record);
  });

  it("lists runs filtered by requester session", () => {
    registerSubagentRun({
      runId: "run-1",
      childSessionId: "child-1",
      requesterSessionId: "parent-A",
      task: "Task 1",
    });
    registerSubagentRun({
      runId: "run-2",
      childSessionId: "child-2",
      requesterSessionId: "parent-B",
      task: "Task 2",
    });
    registerSubagentRun({
      runId: "run-3",
      childSessionId: "child-3",
      requesterSessionId: "parent-A",
      task: "Task 3",
    });

    const parentARuns = listSubagentRuns("parent-A");
    expect(parentARuns).toHaveLength(2);
    expect(parentARuns.map((r) => r.runId).sort()).toEqual(["run-1", "run-3"]);

    const parentBRuns = listSubagentRuns("parent-B");
    expect(parentBRuns).toHaveLength(1);
    expect(parentBRuns[0]!.runId).toBe("run-2");

    const emptyRuns = listSubagentRuns("parent-C");
    expect(emptyRuns).toHaveLength(0);
  });

  it("releases a run from the registry", () => {
    registerSubagentRun({
      runId: "run-1",
      childSessionId: "child-1",
      requesterSessionId: "parent-1",
      task: "Task",
    });

    expect(getSubagentRun("run-1")).toBeDefined();

    const released = releaseSubagentRun("run-1");
    expect(released).toBe(true);
    expect(getSubagentRun("run-1")).toBeUndefined();

    // Double release returns false
    const releasedAgain = releaseSubagentRun("run-1");
    expect(releasedAgain).toBe(false);
  });

  it("applies custom cleanup value", () => {
    const record = registerSubagentRun({
      runId: "run-keep",
      childSessionId: "child-1",
      requesterSessionId: "parent-1",
      task: "Keep session",
      cleanup: "keep",
    });

    expect(record.cleanup).toBe("keep");
  });

  it("registers a run and ends it with error when Hub is not available", () => {
    // Without Hub initialized, watchChildAgent detects missing Hub
    // and immediately ends the run with an error
    registerSubagentRun({
      runId: "run-no-hub",
      childSessionId: "child-1",
      requesterSessionId: "parent-1",
      task: "Running task",
    });

    const record = getSubagentRun("run-no-hub");
    expect(record?.startedAt).toBeGreaterThan(0);
    expect(record?.endedAt).toBeGreaterThan(0);
    expect(record?.outcome?.status).toBe("error");
    expect(record?.outcome?.error).toContain("Hub not initialized");
  });

  it("shutdownSubagentRegistry marks unfinished runs as ended", () => {
    // Directly set up a record without going through watchChildAgent
    // to simulate a run that is still active
    registerSubagentRun({
      runId: "run-active",
      childSessionId: "child-1",
      requesterSessionId: "parent-1",
      task: "Running task",
    });

    // The above run already ended due to no Hub; reset its endedAt
    // to simulate a truly active run
    const record = getSubagentRun("run-active");
    if (record) {
      record.endedAt = undefined;
      record.outcome = undefined;
    }

    shutdownSubagentRegistry();

    const after = getSubagentRun("run-active");
    expect(after?.endedAt).toBeGreaterThan(0);
    expect(after?.outcome?.status).toBe("unknown");
  });

  it("resetSubagentRegistryForTests clears all state", () => {
    registerSubagentRun({
      runId: "run-1",
      childSessionId: "child-1",
      requesterSessionId: "parent-1",
      task: "Task",
    });

    expect(listSubagentRuns("parent-1")).toHaveLength(1);

    resetSubagentRegistryForTests();

    expect(listSubagentRuns("parent-1")).toHaveLength(0);
    expect(getSubagentRun("run-1")).toBeUndefined();
  });
});
