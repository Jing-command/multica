import { test, expect } from "@playwright/test";
import { loginAsDefault } from "./helpers";

test.describe("Settings", () => {
  test("updating workspace name reflects in sidebar immediately", async ({
    page,
  }) => {
    await loginAsDefault(page);

    await page.getByRole("link", { name: "Settings" }).click();
    await page.waitForURL("**/settings");

    await page.getByRole("tab", { name: "General" }).click();
    const generalPanel = page.getByRole("tabpanel", { name: "General" });
    const nameInput = generalPanel
      .locator("div", { has: page.getByText(/^Name$/) })
      .locator("input");
    const originalName = (await nameInput.inputValue()).trim();
    const newName = "Renamed WS " + Date.now();
    await nameInput.clear();
    await nameInput.fill(newName);

    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText("Workspace settings saved").last()).toBeVisible({
      timeout: 5000,
    });

    await expect(page.getByRole("button", { name: new RegExp(newName) })).toBeVisible();

    await nameInput.clear();
    await nameInput.fill(originalName);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText("Workspace settings saved").last()).toBeVisible({
      timeout: 5000,
    });
  });
});
