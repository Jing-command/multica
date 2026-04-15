import { expect, test } from "@playwright/test";
import { createTestApi, resetDefaultAuthForTests } from "./helpers";
import { TestApiClient } from "./fixtures";

test.beforeEach(() => {
  resetDefaultAuthForTests();
});

test("createTestApi scopes default login email per worker process", async () => {
  let loginEmail = "";

  const originalLogin = TestApiClient.prototype.login;
  const originalEnsureWorkspace = TestApiClient.prototype.ensureWorkspace;
  const originalGetToken = TestApiClient.prototype.getToken;

  TestApiClient.prototype.login = async function mockedLogin(email: string) {
    loginEmail = email;
  };
  TestApiClient.prototype.ensureWorkspace = async function mockedEnsureWorkspace() {
    return { id: "ws_shared", name: "E2E Workspace", slug: "e2e-workspace" };
  };
  TestApiClient.prototype.getToken = function mockedGetToken() {
    return "token_shared";
  };

  try {
    await createTestApi();
    expect(loginEmail).toBe(`e2e+${process.pid}@multica.ai`);
  } finally {
    TestApiClient.prototype.login = originalLogin;
    TestApiClient.prototype.ensureWorkspace = originalEnsureWorkspace;
    TestApiClient.prototype.getToken = originalGetToken;
  }
});

test("createTestApi returns isolated clients while sharing in-flight default login", async () => {
  let loginCalls = 0;
  let ensureWorkspaceCalls = 0;
  let releaseLogin: (() => void) | null = null;
  const loginGate = new Promise<void>((resolve) => {
    releaseLogin = resolve;
  });

  const originalLogin = TestApiClient.prototype.login;
  const originalEnsureWorkspace = TestApiClient.prototype.ensureWorkspace;
  const originalGetToken = TestApiClient.prototype.getToken;
  const originalSetWorkspaceId = TestApiClient.prototype.setWorkspaceId;

  TestApiClient.prototype.login = async function mockedLogin() {
    loginCalls += 1;
    await loginGate;
  };
  TestApiClient.prototype.ensureWorkspace = async function mockedEnsureWorkspace() {
    ensureWorkspaceCalls += 1;
    this.setWorkspaceId("ws_shared");
    return { id: "ws_shared", name: "E2E Workspace", slug: "e2e-workspace" };
  };
  TestApiClient.prototype.getToken = function mockedGetToken() {
    return "token_shared";
  };

  try {
    const first = createTestApi();
    const second = createTestApi();

    releaseLogin?.();

    const [firstApi, secondApi] = await Promise.all([first, second]);

    expect(loginCalls).toBe(1);
    expect(ensureWorkspaceCalls).toBe(1);
    expect(firstApi).toBeInstanceOf(TestApiClient);
    expect(secondApi).toBeInstanceOf(TestApiClient);
    expect(firstApi).not.toBe(secondApi);
    expect(firstApi.getToken()).toBe("token_shared");
    expect(secondApi.getToken()).toBe("token_shared");
    expect((firstApi as TestApiClient & { workspaceId: string | null }).workspaceId).toBe("ws_shared");
    expect((secondApi as TestApiClient & { workspaceId: string | null }).workspaceId).toBe("ws_shared");

    firstApi.setWorkspaceId("ws_first");
    expect((firstApi as TestApiClient & { workspaceId: string | null }).workspaceId).toBe("ws_first");
    expect((secondApi as TestApiClient & { workspaceId: string | null }).workspaceId).toBe("ws_shared");
  } finally {
    TestApiClient.prototype.login = originalLogin;
    TestApiClient.prototype.ensureWorkspace = originalEnsureWorkspace;
    TestApiClient.prototype.getToken = originalGetToken;
    TestApiClient.prototype.setWorkspaceId = originalSetWorkspaceId;
  }
});

test("createTestApi retries default login after a shared failure", async () => {
  let loginCalls = 0;
  let releaseLogin: (() => void) | null = null;
  const loginGate = new Promise<void>((resolve) => {
    releaseLogin = resolve;
  });

  const originalLogin = TestApiClient.prototype.login;
  const originalEnsureWorkspace = TestApiClient.prototype.ensureWorkspace;
  const originalGetToken = TestApiClient.prototype.getToken;

  TestApiClient.prototype.login = async function mockedLogin() {
    loginCalls += 1;
    await loginGate;
    throw new Error(`boom-${loginCalls}`);
  };
  TestApiClient.prototype.ensureWorkspace = async function mockedEnsureWorkspace() {
    return { id: "ws_123", name: "E2E Workspace", slug: "e2e-workspace" };
  };
  TestApiClient.prototype.getToken = function mockedGetToken() {
    return "token_retry";
  };

  try {
    const first = createTestApi();
    const second = createTestApi();

    releaseLogin?.();

    await expect(Promise.all([first, second])).rejects.toThrow("boom-");
    expect(loginCalls).toBe(1);

    TestApiClient.prototype.login = async function retryLogin() {
      loginCalls += 1;
    };

    const api = await createTestApi();
    expect(api).toBeInstanceOf(TestApiClient);
    expect(api.getToken()).toBe("token_retry");
    expect(loginCalls).toBe(2);
  } finally {
    TestApiClient.prototype.login = originalLogin;
    TestApiClient.prototype.ensureWorkspace = originalEnsureWorkspace;
    TestApiClient.prototype.getToken = originalGetToken;
  }
});

test("TestApiClient can create and clean up a temporary workspace via public helpers", async () => {
  const api = new TestApiClient();
  api.setToken("token_123");
  api.setWorkspaceId("ws_default");

  const originalFetch = global.fetch;
  const requests: Array<{ input: RequestInfo | URL; init?: RequestInit }> = [];

  global.fetch = async (input, init) => {
    requests.push({ input, init });

    if (typeof input === "string" && input.endsWith("/api/workspaces") && init?.method === "POST") {
      return new Response(
        JSON.stringify({ id: "ws_temp", name: "Temp Workspace", slug: "temp-workspace" }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }

    if (typeof input === "string" && input.endsWith("/api/workspaces/ws_temp") && init?.method === "DELETE") {
      return new Response(null, { status: 204 });
    }

    throw new Error(`Unexpected fetch: ${String(input)} ${init?.method ?? "GET"}`);
  };

  try {
    const workspace = await api.createWorkspace("Temp Workspace", "temp-workspace");

    expect(workspace.id).toBe("ws_temp");
    expect(api.getCurrentWorkspaceId()).toBe("ws_temp");

    await api.cleanup();

    expect(requests).toHaveLength(2);
    expect(String(requests[0]?.input)).toMatch(/\/api\/workspaces$/);
    expect(requests[0]?.init?.method).toBe("POST");
    expect(requests[0]?.init?.headers).toMatchObject({
      Authorization: "Bearer token_123",
      "X-Workspace-ID": "ws_default",
    });
    expect(String(requests[1]?.input)).toMatch(/\/api\/workspaces\/ws_temp$/);
    expect(requests[1]?.init?.method).toBe("DELETE");
    expect(requests[1]?.init?.headers).toMatchObject({
      Authorization: "Bearer token_123",
      "X-Workspace-ID": "ws_temp",
    });
  } finally {
    global.fetch = originalFetch;
  }
});
