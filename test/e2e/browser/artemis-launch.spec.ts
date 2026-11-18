import { expect, test } from "@playwright/test";

// G16: Artemis opens the landing page with the token in the URL fragment and the user has no login yet.
// The fragment must survive the Keycloak detour, and the token must end up in the IDE.
test("launch from Artemis without an existing login", async ({ page }) => {
  test.skip(process.env.E2E_SKIP_BROWSER === "1", "E2E_SKIP_BROWSER=1");

  const gitUri = "https://git.example.org/course/exercise.git";
  await page.goto(`https://ide.e2e.test/?appDef=plain&gitUri=${encodeURIComponent(gitUri)}#artemisToken=abc`);

  // Keycloak login
  await page.waitForURL(/keycloak\.e2e\.test/);
  await page.fill("#username", "alice");
  await page.fill("#password", "alice");
  await page.click("#kc-login");

  // Landing page launches the session and sends the browser to the session URL.
  await page.waitForURL(/https:\/\/instance\.ide\.e2e\.test\/[a-z2-7]{20}\//, { timeout: 90_000 });
  await expect(page.locator("body")).toContainText("fake-ide");

  // The fake IDE reports key names only; the token is a key, never a value.
  await expect
    .poll(
      async () => {
        const res = await page.request.get(`${page.url().split("#")[0]}data-state`);
        return res.ok() ? ((await res.json()) as { keys: string[] }).keys : [];
      },
      { timeout: 30_000 },
    )
    .toContain("ARTEMIS_TOKEN");

  // The fragment is gone from the address bar after the landing page consumed it.
  expect(page.url()).not.toContain("artemisToken");
});
