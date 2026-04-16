import { type Page } from "@playwright/test";
import { TestApiClient } from "./fixtures";

const DEFAULT_E2E_NAME = "E2E User";
const DEFAULT_E2E_WORKSPACE = "e2e-workspace";
const DEFAULT_E2E_WORKSPACE_NAME = "E2E Workspace";

function getDefaultE2EEmail() {
  return `e2e+${process.pid}@multica.ai`;
}

interface DefaultAuthState {
  token: string;
  workspaceId: string;
}

let defaultAuthState: DefaultAuthState | null = null;
let defaultAuthPromise: Promise<DefaultAuthState> | null = null;

async function getDefaultAuthState(): Promise<DefaultAuthState> {
  if (defaultAuthState) {
    return defaultAuthState;
  }

  if (defaultAuthPromise) {
    return defaultAuthPromise;
  }

  defaultAuthPromise = (async () => {
    const api = new TestApiClient();
    await api.login(getDefaultE2EEmail(), DEFAULT_E2E_NAME);
    const workspace = await api.ensureWorkspace(DEFAULT_E2E_WORKSPACE_NAME, DEFAULT_E2E_WORKSPACE);
    const token = api.getToken();
    if (!token) {
      throw new Error("Default E2E login returned no token");
    }
    defaultAuthState = { token, workspaceId: workspace.id };
    return defaultAuthState;
  })();

  try {
    return await defaultAuthPromise;
  } catch (error) {
    defaultAuthPromise = null;
    throw error;
  } finally {
    if (defaultAuthState) {
      defaultAuthPromise = null;
    }
  }
}

async function getDefaultApi(): Promise<TestApiClient> {
  const authState = await getDefaultAuthState();
  const api = new TestApiClient();
  api.setToken(authState.token);
  api.setWorkspaceId(authState.workspaceId);
  return api;
}

/**
 * Log in as the default E2E user and ensure the workspace exists first.
 * Authenticates via API and seeds the browser with the same session cookies.
 */
export async function loginAsDefault(page: Page) {
  const api = await getDefaultApi();
  const token = api.getToken();
  if (!token) {
    throw new Error("Default E2E login returned no token");
  }

  await page.context().addCookies([
    {
      name: "multica_auth",
      value: token,
      url: "http://localhost:3000",
      httpOnly: true,
      sameSite: "Lax",
      secure: false,
    },
    {
      name: "multica_logged_in",
      value: "1",
      url: "http://localhost:3000",
      httpOnly: false,
      sameSite: "Lax",
      secure: false,
    },
  ]);

  await page.goto("/issues");
  await page.waitForURL("**/issues", { timeout: 10000 });
}

/**
 * Create a TestApiClient logged in as the default E2E user.
 * Call api.cleanup() in afterEach to remove test data created during the test.
 */
export async function createTestApi(): Promise<TestApiClient> {
  return getDefaultApi();
}

export function resetDefaultAuthForTests() {
  defaultAuthState = null;
  defaultAuthPromise = null;
}

export async function openWorkspaceMenu(page: Page) {
  await page.getByRole("button", { name: /Workspace$/ }).click();
  await page.getByText("Workspaces").waitFor({ state: "visible" });
}
