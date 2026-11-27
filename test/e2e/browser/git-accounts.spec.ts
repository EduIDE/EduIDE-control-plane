import { expect, type Page, test } from "@playwright/test";

// Git accounts through the landing page (ADR 0023). Runs in the Git phase of run.sh, with the providers on and the
// fake provider (test/fake-git) reachable on 127.0.0.1:E2E_FAKE_GIT_PORT through kubectl port-forward.
// The browser cannot resolve the fake's cluster address, so the authorize step is answered here: the route does
// what the provider does (a redirect to the landing page with code and state) by asking the fake through the forward.
const forward = process.env.E2E_FAKE_GIT_PORT;
const FAKE = "fake-git.eduide-e2e.svc";
const PRIVATE_REPO = `https://${FAKE}/course/ex1.git`;
const OTHER_REPO = `https://${FAKE}/course/other.git`;

test.describe.configure({ mode: "serial" });
test.skip(process.env.E2E_SKIP_BROWSER === "1" || !forward, "needs the Git phase of run.sh (E2E_FAKE_GIT_PORT)");

async function stubAuthorize(page: Page) {
  await page.route(`https://${FAKE}/**`, async (route) => {
    const u = new URL(route.request().url());
    const res = await page.request.get(`https://127.0.0.1:${forward}${u.pathname}${u.search}`, { maxRedirects: 0 });
    await route.fulfill({ status: res.status(), headers: { location: res.headers()["location"] ?? "" } });
  });
}

// Opens a landing page URL, signs in at Keycloak when asked to, and returns once the page has rendered.
async function open(page: Page, url: string) {
  await page.goto(url);
  // The first URL already matches the landing page, so wait for whatever renders first: the Keycloak form or the header.
  const form = page.locator("#username");
  const header = page.locator("header .brand");
  await form.or(header).first().waitFor({ timeout: 60_000 });
  if (await form.isVisible()) {
    await form.fill("alice");
    await page.fill("#password", "alice");
    await page.click("#kc-login");
  }
  await header.waitFor({ timeout: 60_000 });
}

// Calls the API as alice from inside the page: only the browser resolves *.e2e.test to the local gateway.
async function api(page: Page, method: string, path: string, body?: unknown) {
  return page.evaluate(
    async ({ method, path, body }) => {
      const tr = await fetch("https://keycloak.e2e.test/realms/eduide/protocol/openid-connect/token", {
        method: "POST",
        headers: { "content-type": "application/x-www-form-urlencoded" },
        body: new URLSearchParams({ grant_type: "password", client_id: "eduide-landing", username: "alice", password: "alice" }),
      });
      const { access_token } = (await tr.json()) as { access_token: string };
      const r = await fetch(`/api/v1${path}`, {
        method,
        headers: { authorization: `Bearer ${access_token}`, "content-type": "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      return { status: r.status, body: r.status === 204 ? undefined : await r.json().catch(() => undefined) };
    },
    { method, path, body },
  );
}

test("connect through the page", async ({ page }) => {
  await stubAuthorize(page);
  await open(page, "https://ide.e2e.test/");
  await api(page, "DELETE", "/git/providers/fake");
  await page.reload();

  const row = page.getByTestId("git-provider-fake");
  await expect(page.getByRole("heading", { name: "Git accounts" })).toBeVisible();
  await expect(row).toContainText("Fake Git");
  await expect(row).toContainText("Not connected");
  await row.getByTestId("git-connect").click();

  // The provider redirects to /git/callback, the page completes the flow and returns to where it was.
  await expect(page.getByTestId("git-provider-fake")).toContainText("Connected as e2euser", { timeout: 30_000 });
  expect(page.url()).not.toMatch(/code=|state=/);

  // The page never holds the token: the list says connected and nothing else about the account.
  const list = await api(page, "GET", "/git/providers");
  expect(JSON.stringify(list.body)).not.toMatch(/token/i);

  // Disconnect asks first.
  await page.getByTestId("git-disconnect").click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.getByTestId("git-disconnect-confirm").click();
  await expect(page.getByTestId("git-provider-fake")).toContainText("Not connected");
});

test("a start link to the private repository asks to connect, then launches", async ({ page }) => {
  await stubAuthorize(page);
  await open(page, "https://ide.e2e.test/");
  await api(page, "DELETE", "/git/providers/fake");

  await page.goto(`https://ide.e2e.test/?appDef=plain&gitUri=${encodeURIComponent(PRIVATE_REPO)}`);
  await expect(page.getByTestId("git-gate-connect")).toContainText("This repository needs your Fake Git account", { timeout: 30_000 });
  await expect(page.getByRole("button", { name: /anyway/i })).toHaveCount(0);

  // Connect, come back to the same link and continue this launch without opening the link again.
  await page.getByTestId("git-gate-connect-button").click();
  await page.waitForURL(/https:\/\/instance\.ide\.e2e\.test\/[a-z2-7]{20}\//, { timeout: 120_000 });
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
    .toContain("GIT_URI");
});

test("the approval page approves only after a click", async ({ page }) => {
  await open(page, "https://ide.e2e.test/");
  const sessions = await api(page, "GET", "/sessions");
  const running = (sessions.body as Array<{ name: string; phase: string }>).find((s) => s.phase === "Ready");
  expect(running, "the session of the previous test").toBeTruthy();
  const name = running!.name;
  const approved = async () =>
    ((await api(page, "GET", `/sessions/${name}/git/repositories`)).body as { approved: Array<{ repository: string }> }).approved;

  await page.goto(`https://ide.e2e.test/?session=${name}&approveRepo=${encodeURIComponent(OTHER_REPO)}&access=read`);
  await expect(page.getByRole("heading", { name: "Allow Git access for a session?" })).toBeVisible();
  await expect(page.getByTestId("approve-repository")).toHaveText(`https://${FAKE}/course/other`);
  await expect(page.getByTestId("approve-access")).toHaveText("Read (clone and fetch)");
  await expect(page.getByText("Only allow this if you just ran a Git command")).toBeVisible();
  // Opening the link approved nothing.
  expect(await approved()).toEqual([]);

  await page.getByTestId("approve-allow").click();
  await expect(page.getByText("Repeat the Git command in your IDE.")).toBeVisible();
  expect((await approved()).map((a) => a.repository)).toEqual([`https://${FAKE}/course/other`]);

  await page.getByTestId("approve-remove").click();
  await expect.poll(approved).toEqual([]);

  // A link with a made-up access value is refused and nothing is asked of the service.
  await page.goto(`https://ide.e2e.test/?session=${name}&approveRepo=${encodeURIComponent(OTHER_REPO)}&access=root`);
  await expect(page.getByText("This link is not valid. Nothing was approved.")).toBeVisible();
});

test("cleanup: disconnect and delete the session", async ({ page }) => {
  await open(page, "https://ide.e2e.test/");
  const sessions = await api(page, "GET", "/sessions");
  for (const s of sessions.body as Array<{ name: string }>) {
    await api(page, "DELETE", `/sessions/${s.name}`);
  }
  expect((await api(page, "DELETE", "/git/providers/fake")).status).toBe(204);
});
