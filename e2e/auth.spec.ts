import { test, expect } from "@playwright/test";
import { loginAsDefault, openWorkspaceMenu } from "./helpers";

test.describe("Authentication", () => {
  test("login page renders correctly", async ({ page }) => {
    await page.goto("/login");

    await expect(page.getByText("Multica")).toBeVisible();
    await expect(page.getByText("Turn coding agents into real teammates")).toBeVisible();
    await expect(page.locator('input[placeholder="you@example.com"]')).toBeVisible();
    await expect(page.locator('button[type="submit"]')).toContainText(
      "Continue",
    );
  });

  test("login and redirect to /issues", async ({ page }) => {
    await loginAsDefault(page);

    await expect(page).toHaveURL(/\/issues/);
    await expect(page.getByText("Issues").first()).toBeVisible();
  });

  test("unauthenticated user is redirected to landing page", async ({ page }) => {
    await page.goto("/login");
    await page.evaluate(() => {
      localStorage.removeItem("multica_token");
      localStorage.removeItem("multica_workspace_id");
    });

    await page.goto("/issues");
    await page.waitForURL("**/", { timeout: 10000 });
    await expect(page).toHaveURL(/\/$/);
  });

  test("logout redirects to landing page", async ({ page }) => {
    await loginAsDefault(page);

    await openWorkspaceMenu(page);
    await page.getByText("Log out").click();

    await page.waitForURL("**/", { timeout: 10000 });
    await expect(page).toHaveURL(/\/$/);
  });

  test("logout remains logged out after reload", async ({ page }) => {
    await loginAsDefault(page);

    await openWorkspaceMenu(page);
    await page.getByText("Log out").click();

    await page.waitForURL("**/", { timeout: 10000 });
    await expect(page).toHaveURL(/\/$/);

    await page.goto("/issues");
    await page.waitForURL("**/", { timeout: 10000 });
    await expect(page).toHaveURL(/\/$/);
  });
});
