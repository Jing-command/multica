/**
 * TestApiClient — lightweight API helper for E2E test data setup/teardown.
 *
 * Uses raw fetch so E2E tests have zero build-time coupling to the web app.
 */

import pg from "pg";

const API_BASE = process.env.NEXT_PUBLIC_API_URL ?? `http://localhost:${process.env.PORT ?? "8080"}`;
const DATABASE_URL = process.env.DATABASE_URL ?? "postgres://multica:multica@localhost:5432/multica?sslmode=disable";

interface TestWorkspace {
  id: string;
  name: string;
  slug: string;
}

export class TestApiClient {
  private token: string | null = null;
  private workspaceId: string | null = null;
  private createdIssueIds: string[] = [];
  private createdWorkspaceIds: string[] = [];

  async login(email: string, name: string) {
    // Step 1: Send verification code
    const sendRes = await fetch(`${API_BASE}/auth/send-code`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email }),
    });
    if (!sendRes.ok && sendRes.status !== 429) {
      throw new Error(`send-code failed: ${sendRes.status} ${await sendRes.text()}`);
    }

    // Step 2: Read code from database
    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      const result = await client.query(
        "SELECT code FROM verification_code WHERE email = $1 AND used = FALSE AND expires_at > now() ORDER BY created_at DESC LIMIT 1",
        [email]
      );
      if (result.rows.length === 0) {
        throw new Error(`No verification code found for ${email} in ${DATABASE_URL}`);
      }
      const code = result.rows[0].code;

      // Step 3: Verify code to get JWT
      const verifyRes = await fetch(`${API_BASE}/auth/verify-code`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, code }),
      });
      if (!verifyRes.ok) {
        throw new Error(`verify-code failed: ${verifyRes.status} ${await verifyRes.text()}`);
      }
      const data = await verifyRes.json();
      if (!data.token) {
        throw new Error(`verify-code returned no token: ${JSON.stringify(data)}`);
      }
      this.token = data.token;

      // Update user name if needed
      if (name && data.user?.name !== name) {
        const updateRes = await this.authedFetch("/api/me", {
          method: "PATCH",
          body: JSON.stringify({ name }),
        });
        if (!updateRes.ok) {
          throw new Error(`update me failed: ${updateRes.status} ${await updateRes.text()}`);
        }
      }

      return data;
    } finally {
      await client.end();
    }
  }

  async getWorkspaces(): Promise<TestWorkspace[]> {
    const res = await this.authedFetch("/api/workspaces");
    if (!res.ok) {
      throw new Error(`list workspaces failed: ${res.status} ${await res.text()}`);
    }
    const data = await res.json();
    if (!Array.isArray(data)) {
      throw new Error(`list workspaces returned non-array: ${JSON.stringify(data)}`);
    }
    return data as TestWorkspace[];
  }

  setToken(token: string) {
    this.token = token;
  }

  setWorkspaceId(id: string) {
    this.workspaceId = id;
  }

  getCurrentWorkspaceId() {
    return this.workspaceId;
  }

  async createWorkspace(name: string, slug: string) {
    const res = await this.authedFetch("/api/workspaces", {
      method: "POST",
      body: JSON.stringify({ name, slug }),
    });
    if (!res.ok) {
      throw new Error(`create workspace failed: ${res.status} ${await res.text()}`);
    }
    const workspace = (await res.json()) as TestWorkspace;
    this.workspaceId = workspace.id;
    this.createdWorkspaceIds.push(workspace.id);
    return workspace;
  }

  async deleteWorkspace(id: string) {
    const previousWorkspaceId = this.workspaceId;
    this.workspaceId = id;
    try {
      const res = await this.authedFetch(`/api/workspaces/${id}`, { method: "DELETE" });
      if (!res.ok && res.status !== 404) {
        throw new Error(`delete workspace failed: ${res.status} ${await res.text()}`);
      }
    } finally {
      this.workspaceId = previousWorkspaceId === id ? null : previousWorkspaceId;
      this.createdWorkspaceIds = this.createdWorkspaceIds.filter((workspaceId) => workspaceId !== id);
    }
  }

  async ensureWorkspace(name = "E2E Workspace", slug = "e2e-workspace") {
    const workspaces = await this.getWorkspaces();
    const workspace = workspaces.find((item) => item.slug === slug) ?? workspaces[0];
    if (workspace) {
      this.workspaceId = workspace.id;
      return workspace;
    }

    const res = await this.authedFetch("/api/workspaces", {
      method: "POST",
      body: JSON.stringify({ name, slug }),
    });
    if (res.ok) {
      const created = (await res.json()) as TestWorkspace;
      this.workspaceId = created.id;
      return created;
    }

    const refreshed = await this.getWorkspaces();
    const created = refreshed.find((item) => item.slug === slug) ?? refreshed[0];
    if (created) {
      this.workspaceId = created.id;
      return created;
    }

    throw new Error(`Failed to ensure workspace ${slug}: ${res.status} ${res.statusText}`);
  }

  async createIssue(title: string, opts?: Record<string, unknown>) {
    const res = await this.authedFetch("/api/issues", {
      method: "POST",
      body: JSON.stringify({ title, ...opts }),
    });
    const issue = await res.json();
    this.createdIssueIds.push(issue.id);
    return issue;
  }

  async deleteIssue(id: string) {
    await this.authedFetch(`/api/issues/${id}`, { method: "DELETE" });
  }

  /** Clean up all issues and workspaces created during this test. */
  async cleanup() {
    for (const id of this.createdIssueIds) {
      try {
        await this.deleteIssue(id);
      } catch {
        /* ignore — may already be deleted */
      }
    }
    this.createdIssueIds = [];

    for (const id of [...this.createdWorkspaceIds].reverse()) {
      try {
        await this.deleteWorkspace(id);
      } catch {
        /* ignore — may already be deleted */
      }
    }
    this.createdWorkspaceIds = [];
  }

  getToken() {
    return this.token;
  }

  private async authedFetch(path: string, init?: RequestInit) {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      ...((init?.headers as Record<string, string>) ?? {}),
    };
    if (this.token) headers["Authorization"] = `Bearer ${this.token}`;
    if (this.workspaceId) headers["X-Workspace-ID"] = this.workspaceId;
    return fetch(`${API_BASE}${path}`, { ...init, headers });
  }
}
