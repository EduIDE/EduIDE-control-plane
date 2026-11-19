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
        // Fetch inside the page: only the browser resolves *.e2e.test to the local gateway.
        return page.evaluate(async (url) => {
          const res = await fetch(url, { credentials: "include" });
          return res.ok ? ((await res.json()) as { keys: string[] }).keys : [];
        }, `${page.url().split("#")[0]}data-state`);
      },
      { timeout: 30_000 },
    )
    .toContain("ARTEMIS_TOKEN");

  // The fragment is gone from the address bar after the landing page consumed it.
  expect(page.url()).not.toContain("artemisToken");
});

// Artemis#14702: token and repository URL with VCS credentials both travel in the fragment; the URL must survive the
// Keycloak detour, reach the IDE as GIT_URI, and never stay in the address bar.
test("launch from Artemis with the repository URL and credentials in the fragment", async ({ page }) => {
  test.skip(process.env.E2E_SKIP_BROWSER === "1", "E2E_SKIP_BROWSER=1");

  const gitUri = "https://student1:vcs-token@git.example.org/git/course/exercise-student1.git";
  const artemisUrl = "https://artemis.example.org";
  await page.goto(
    `https://ide.e2e.test/?appDef=artemis&artemisUrl=${encodeURIComponent(artemisUrl)}` +
      `#artemisToken=abc&gitUri=${encodeURIComponent(gitUri)}`,
  );

  await page.waitForURL(/keycloak\.e2e\.test/);
  await page.fill("#username", "alice");
  await page.fill("#password", "alice");
  await page.click("#kc-login");

  await page.waitForURL(/https:\/\/instance\.ide\.e2e\.test\/[a-z2-7]{20}\//, { timeout: 90_000 });
  await expect(page.locator("body")).toContainText("fake-ide");

  await expect
    .poll(
      async () =>
        page.evaluate(async (url) => {
          const res = await fetch(url, { credentials: "include" });
          return res.ok ? ((await res.json()) as { keys: string[] }).keys : [];
        }, `${page.url().split("#")[0]}data-state`),
      { timeout: 30_000 },
    )
    .toEqual(expect.arrayContaining(["GIT_URI", "ARTEMIS_TOKEN", "ARTEMIS_URL"]));

  expect(page.url()).not.toContain("vcs-token");
  expect(page.url()).not.toContain("gitUri");
});
