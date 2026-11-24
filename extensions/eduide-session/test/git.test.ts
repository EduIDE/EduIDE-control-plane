import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { AgentClient } from "../src/agent";
import { GitRewrite, isBareHost, type RunGit } from "../src/git";

async function agent(handler: (url: string) => { status: number; body?: unknown }) {
  const calls: string[] = [];
  const server = http.createServer((req, res) => {
    calls.push(req.url ?? "");
    const { status, body } = handler(req.url ?? "");
    res.writeHead(status, { "content-type": "application/json" }).end(body === undefined ? "" : JSON.stringify(body));
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const baseUrl = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
  return { baseUrl, calls, close: () => (server.closeAllConnections(), server.close()) };
}

// Real git against a config file of its own, so the test never touches the developer's config.
function realGit() {
  const dir = mkdtempSync(join(tmpdir(), "eduide-git-"));
  const file = join(dir, "gitconfig");
  writeFileSync(file, "");
  const env = { GIT_CONFIG_GLOBAL: file, GIT_CONFIG_NOSYSTEM: "1" };
  const read = () => {
    try {
      return execFileSync("git", ["config", "--global", "--list"], { env: { ...process.env, ...env } }).toString();
    } catch {
      return "";
    }
  };
  const set = (k: string, v: string) => execFileSync("git", ["config", "--global", "--add", k, v], { env: { ...process.env, ...env } });
  return { env, read, set };
}

test("isBareHost accepts host names only", () => {
  for (const h of ["github.com", "git.example.org", "fake-git.eduide-e2e.svc", "a"]) {
    assert.equal(isBareHost(h), true, h);
  }
  for (const h of ["", "a b", "h:8080", "h/p", "u@h", "-h.com", "h-.com", "a..b", "h;rm -rf", "$(x)", "h\n", "https://h", 5, undefined]) {
    assert.equal(isBareHost(h), false, String(h));
  }
});

test("writes one insteadOf entry per provider host", async () => {
  const a = await agent(() => ({ status: 200, body: { hosts: ["github.com", "gitlab.example.org"], landingUrl: "https://ide.example.org" } }));
  const g = realGit();
  await new GitRewrite({ baseUrl: a.baseUrl, env: g.env }).sync();
  const cfg = g.read();
  assert.match(cfg, new RegExp(`url\\.${a.baseUrl}/git/github\\.com/\\.insteadof=https://github\\.com/`));
  assert.match(cfg, new RegExp(`url\\.${a.baseUrl}/git/gitlab\\.example\\.org/\\.insteadof=https://gitlab\\.example\\.org/`));
  a.close();
});

test("removes entries of hosts that left the list and keeps foreign entries", async () => {
  let hosts = ["github.com", "gitlab.example.org"];
  const a = await agent(() => ({ status: 200, body: { hosts } }));
  const g = realGit();
  g.set("url.https://mirror.example.org/.insteadOf", "https://upstream.example.org/");
  const rw = new GitRewrite({ baseUrl: a.baseUrl, env: g.env });
  await rw.sync();
  hosts = ["github.com"];
  await rw.sync();
  const cfg = g.read();
  assert.match(cfg, /github\.com/);
  assert.doesNotMatch(cfg, /gitlab\.example\.org/);
  assert.match(cfg, /url\.https:\/\/mirror\.example\.org\/\.insteadof=https:\/\/upstream\.example\.org\//);
  hosts = [];
  await rw.sync();
  assert.doesNotMatch(g.read(), /127\.0\.0\.1/);
  assert.match(g.read(), /mirror\.example\.org/);
  a.close();
});

test("does nothing when the feature is off or the agent is older", async () => {
  for (const status of [404, 403, 500]) {
    const a = await agent(() => ({ status }));
    const run: RunGit = async () => assert.fail("git must not run");
    await new GitRewrite({ baseUrl: a.baseUrl, run }).sync();
    a.close();
  }
  // Agent not reachable at all.
  await new GitRewrite({ baseUrl: "http://127.0.0.1:1", run: async () => assert.fail("git must not run") }).sync();
});

test("an empty list removes our entries and writes none", async () => {
  const a = await agent(() => ({ status: 200, body: { hosts: [] } }));
  const g = realGit();
  g.set(`url.${a.baseUrl}/git/old.example.org/.insteadOf`, "https://old.example.org/");
  await new GitRewrite({ baseUrl: a.baseUrl, env: g.env }).sync();
  assert.doesNotMatch(g.read(), /old\.example\.org/);
  a.close();
});

test("refuses hosts that are not bare host names and passes arguments without a shell", async () => {
  const a = await agent(() => ({ status: 200, body: { hosts: ["github.com", "evil.com; touch /tmp/x", "h:99", "$(id)"] } }));
  const calls: string[][] = [];
  const run: RunGit = async (args) => (calls.push(args), { code: args.includes("--get-regexp") ? 1 : 0, stdout: "" });
  await new GitRewrite({ baseUrl: a.baseUrl, run }).sync();
  const writes = calls.filter((c) => c.includes("--replace-all"));
  assert.deepEqual(writes, [["config", "--global", "--replace-all", `url.${a.baseUrl}/git/github.com/.insteadof`, "https://github.com/"]]);
  a.close();
});

test("a second sync with the same list writes nothing", async () => {
  const a = await agent(() => ({ status: 200, body: { hosts: ["github.com"] } }));
  const g = realGit();
  const writes: string[][] = [];
  const rw = new GitRewrite({
    baseUrl: a.baseUrl,
    run: async (args) => {
      if (args.includes("--replace-all") || args.includes("--unset-all")) {
        writes.push(args);
      }
      try {
        return { code: 0, stdout: execFileSync("git", args, { env: { ...process.env, ...g.env } }).toString() };
      } catch (e) {
        return { code: (e as { status: number }).status, stdout: "" };
      }
    },
  });
  await rw.sync();
  const first = writes.length;
  await rw.sync();
  assert.equal(first, 1);
  assert.equal(writes.length, first);
  a.close();
});

test("logs once and carries on when git is missing", async () => {
  const a = await agent(() => ({ status: 200, body: { hosts: ["github.com"] } }));
  const logs: string[] = [];
  const rw = new GitRewrite({ baseUrl: a.baseUrl, env: { PATH: "/nonexistent" }, log: (m) => logs.push(m) });
  // env PATH only changes the child; spawn resolves "git" with the parent PATH, so point the lookup away as well.
  const saved = process.env.PATH;
  process.env.PATH = "/nonexistent";
  try {
    await rw.sync();
    await rw.sync();
  } finally {
    process.env.PATH = saved;
  }
  assert.equal(logs.length, 1);
  assert.match(logs[0], /git is not available/);
  a.close();
});

test("the client sets Git up before it hands out launch data, and again after a reconnect", async () => {
  let claimed = false;
  let failPoll = 0;
  const a = await agent((url) => {
    if (url.startsWith("/v1/session")) {
      return failPoll-- > 0
        ? { status: 503 }
        : { status: 200, body: { version: claimed ? 1 : 0, claimed, data: claimed ? { GIT_URI: "https://github.com/o/r" } : undefined } };
    }
    return { status: 204 };
  });
  const seen: Array<Record<string, string>> = [];
  let syncs = 0;
  const c: AgentClient = new AgentClient({
    baseUrl: a.baseUrl,
    onNotice: () => undefined,
    pollTimeoutSec: 1,
    minBackoffMs: 10,
    maxBackoffMs: 20,
    beforeExpose: async () => {
      syncs++;
      // Data stays hidden while Git is being set up.
      seen.push(c.getEnv(["GIT_URI"]));
      await new Promise((r) => setTimeout(r, 30));
      seen.push(c.getEnv(["GIT_URI"]));
    },
  });
  c.start();
  try {
    await new Promise((r) => setTimeout(r, 100));
    assert.equal(syncs, 0, "not claimed yet");
    claimed = true;
    const end = Date.now() + 3000;
    while (seen.length < 2 && Date.now() < end) {
      await new Promise((r) => setTimeout(r, 10));
    }
    assert.equal(syncs, 1);
    assert.deepEqual(seen, [{}, {}]);
    await new Promise((r) => setTimeout(r, 100));
    assert.deepEqual(c.getEnv(["GIT_URI"]), { GIT_URI: "https://github.com/o/r" });
    assert.equal(syncs, 1, "a steady session does not sync again");
    failPoll = 1;
    const end2 = Date.now() + 3000;
    while (syncs < 2 && Date.now() < end2) {
      await new Promise((r) => setTimeout(r, 10));
    }
    assert.equal(syncs, 2, "the reconnect syncs again");
  } finally {
    await c.stop();
    a.close();
  }
});
