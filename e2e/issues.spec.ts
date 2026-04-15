import { test, expect } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { loginAsDefault, createTestApi } from "./helpers";
import type { TestApiClient } from "./fixtures";

function getIssuesScopeBar(page: Page) {
  return page
    .locator('main div:has(> div > button:has-text("All")):has(> div > button:has-text("Members")):has(> div > button:has-text("Agents"))')
    .first();
}

function getNewIssueButton(page: Page) {
  return page.locator('[data-slot="sidebar-header"] > div > button');
}

function getViewToggle(page: Page) {
  return getIssuesScopeBar(page).locator('> div:last-child > button:last-child');
}

async function expectIssueVisible(page: Page, title: string): Promise<Locator> {
  const issueLink = page.getByRole("link", { name: new RegExp(title) }).first();
  await expect(issueLink).toBeVisible({ timeout: 10000 });
  return issueLink;
}

test.describe("Issues", () => {
  test.describe.configure({ mode: "serial" });

  let api: TestApiClient;

  test.beforeEach(async ({ page }) => {
    api = await createTestApi();
    await api.ensureWorkspace("E2E Workspace", "e2e-workspace");
    await loginAsDefault(page);
  });

  test.afterEach(async () => {
    if (api) {
      await api.cleanup();
    }
  });

  test("issues page shows the current empty state", async ({ page }) => {
    const timestamp = Date.now();
    const workspace = await api.createWorkspace(
      `E2E Empty ${timestamp}`,
      `e2e-empty-${timestamp}`,
    );
    const token = api.getToken();
    if (!token) {
      throw new Error("Missing E2E auth token");
    }

    await page.goto("/login");
    await page.evaluate(({ nextToken, nextWorkspaceId }) => {
      localStorage.setItem("multica_token", nextToken);
      localStorage.setItem("multica_workspace_id", nextWorkspaceId);
    }, { nextToken: token, nextWorkspaceId: workspace.id });
    await page.goto("/issues");

    await expect(page.getByText("Issues").last()).toBeVisible();
    await expect(page.getByText("No issues yet")).toBeVisible();
    await expect(page.getByText("Create an issue to get started.")).toBeVisible();
  });

  test("issues page uses stable scope and view controls", async ({ page }) => {
    await expect(getIssuesScopeBar(page)).toBeVisible();
    await expect(getIssuesScopeBar(page).getByRole("button", { name: "All" })).toBeVisible();
    await expect(getIssuesScopeBar(page).getByRole("button", { name: "Members" })).toBeVisible();
    await expect(getIssuesScopeBar(page).getByRole("button", { name: "Agents" })).toBeVisible();
    await expect(getViewToggle(page)).toBeVisible();
  });

  test("can switch between board and list view", async ({ page }) => {
    const issue = await api.createIssue("E2E View Toggle " + Date.now());

    await page.reload();
    await expect(page.getByText(issue.title)).toBeVisible();
    await expect(page.getByText("Backlog")).toBeVisible();

    const viewToggle = getViewToggle(page);

    await viewToggle.click();
    await page.getByRole("menuitem", { name: "List" }).click();
    await expect(viewToggle).toBeVisible();
    await expect(page.getByText(issue.title)).toBeVisible();

    await viewToggle.click();
    await page.getByRole("menuitem", { name: "Board" }).click();
    await expect(viewToggle).toBeVisible();
    await expect(page.getByText("Backlog")).toBeVisible();
  });

  test("can create a new issue", async ({ page }) => {
    const newIssueButton = getNewIssueButton(page);

    await expect(newIssueButton).toBeVisible();
    await newIssueButton.click();
    await expect(page.getByRole("dialog", { name: "New Issue" })).toBeVisible();

    const title = "E2E Created " + Date.now();
    const titleInput = page.getByRole("textbox", { name: "Issue title" });
    await titleInput.click();
    await titleInput.fill(title);
    await page.getByRole("button", { name: "Create Issue" }).click();

    await expect(page.getByText("Issue created")).toBeVisible({ timeout: 10000 });
    await expect(
      page.getByRole("link", { name: new RegExp(title) }).first(),
    ).toBeVisible({ timeout: 10000 });
  });

  test("can navigate to issue detail page", async ({ page }) => {
    const issue = await api.createIssue("E2E Detail Test " + Date.now());

    await page.reload();
    await expect(page.getByText(issue.title)).toBeVisible();

    const issueLink = page.getByRole("link", { name: new RegExp(issue.title) }).first();
    await issueLink.click();

    await page.waitForURL(/\/issues\/[\w-]+/);
    await expect(page.getByText("Properties")).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Issue title" })).toContainText(
      issue.title,
    );
  });

  test("can cancel issue creation", async ({ page }) => {
    const newIssueButton = getNewIssueButton(page);

    await expect(newIssueButton).toBeVisible();
    await newIssueButton.click();
    await expect(page.getByRole("dialog", { name: "New Issue" })).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Issue title" })).toBeVisible();

    await page.keyboard.press("Escape");

    await expect(page.getByRole("dialog", { name: "New Issue" })).not.toBeVisible();
    await expect(newIssueButton).toBeVisible();
  });
});
