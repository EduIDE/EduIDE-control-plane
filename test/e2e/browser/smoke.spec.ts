import { expect, test } from "@playwright/test";

// Infrastructure check without the control plane: a real browser logs in at Keycloak through the gateway OIDC filter.
test("infra: browser login at the gateway", async ({ page }) => {
  test.skip(process.env.E2E_SKIP_BROWSER === "1", "E2E_SKIP_BROWSER=1");

  await page.goto("https://smoke.e2e.test/");
  await page.waitForURL(/keycloak\.e2e\.test/);
  await page.fill("#username", "alice");
  await page.fill("#password", "alice");
  await page.click("#kc-login");
  await page.waitForURL("https://smoke.e2e.test/");
  await expect(page.locator("body")).toContainText("fake-ide");
});
