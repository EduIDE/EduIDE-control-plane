import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { AgentClient, type LocalSession, type Notice } from "../src/agent";

function fakeAgent() {
  let session: LocalSession = { version: 0, claimed: false };
  let failNext = 0;
  let hangNext = 0;
  const pending: Array<() => void> = [];
  const heartbeats: number[] = [];
  const queries: string[] = [];
  const server = http.createServer((req, res) => {
    if (req.method === "POST" && req.url === "/v1/heartbeat") {
      heartbeats.push(Date.now());
      res.writeHead(204).end();
      return;
    }
    queries.push(req.url ?? "");
    if (hangNext > 0) {
      hangNext--;
      return;
    }
    if (failNext > 0) {
      failNext--;
      res.writeHead(503).end();
      return;
    }
    const after = Number(new URL(req.url!, "http://x").searchParams.get("after"));
    const reply = () => {
      res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify(session));
    };
    if (session.version > after) {
      reply();
    } else {
      pending.push(reply);
    }
  });
  return {
    server,
    heartbeats,
    queries,
    fail: (n: number) => (failNext = n),
    hang: (n: number) => (hangNext = n),
    set(s: LocalSession) {
      session = s;
      pending.splice(0).forEach((p) => p());
    },
    async listen() {
      await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
      return `http://127.0.0.1:${(server.address() as { port: number }).port}`;
    },
    close() {
      pending.splice(0).forEach((p) => p());
      server.closeAllConnections();
      server.close();
    },
  };
}

async function until(cond: () => boolean, ms = 3000) {
  const end = Date.now() + ms;
  while (!cond()) {
    assert.ok(Date.now() < end, "condition not met in time");
    await new Promise((r) => setTimeout(r, 10));
  }
}

async function setup(opts: { pollTimeoutSec?: number; now?: () => number } = {}) {
  const agent = fakeAgent();
  const notices: Notice[] = [];
  const c = new AgentClient({
    baseUrl: await agent.listen(),
    onNotice: (n) => notices.push(n),
    pollTimeoutSec: 1,
    minBackoffMs: 10,
    ...opts,
  });
  c.start();
  return { agent, c, notices, done: async () => (await c.stop(), agent.close()) };
}

test("getEnv and getEnvState return at once and follow the agent state", async () => {
  const { agent, c, done } = await setup();
  assert.deepEqual(c.getEnvState(), { injected: false, environment: {} });
  assert.deepEqual(c.getEnv(["A"]), {});
  agent.set({ version: 1, claimed: true, data: { A: "1", B: "2" } });
  await until(() => c.getEnvState().injected);
  assert.deepEqual(c.getEnv(["A", "MISSING", "toString"]), { A: "1" });
  assert.deepEqual(c.getEnvState(), { injected: true, environment: { A: "1", B: "2" } });
  await done();
});

test("claimed without data counts as injected", async () => {
  const { agent, c, done } = await setup();
  agent.set({ version: 1, claimed: true });
  await until(() => c.getEnvState().injected);
  assert.deepEqual(c.getEnvState(), { injected: true, environment: {} });
  await done();
});

test("notices shown once, polls use latest version", async () => {
  const { agent, c, notices, done } = await setup();
  const n: Notice = { kind: "idle-warning", message: "idle" };
  agent.set({ version: 1, claimed: true, data: {}, notices: [n] });
  await until(() => notices.length === 1);
  agent.set({ version: 2, claimed: true, data: {}, notices: [n, { kind: "info", message: "hi" }] });
  await until(() => agent.queries.some((q) => q.startsWith("/v1/session?after=2&timeout=1")));
  assert.deepEqual(notices.map((x) => x.kind), ["idle-warning", "info"]);
  assert.ok(c.getEnvState().injected);
  await done();
});

test("backs off on errors and recovers", async () => {
  const { agent, c, done } = await setup();
  agent.fail(3);
  agent.set({ version: 1, claimed: true, data: { A: "1" } });
  await until(() => c.getEnvState().injected);
  assert.ok(agent.queries.length >= 4);
  await done();
});

test("a hung poll times out and the loop retries", async () => {
  const { agent, c, done } = await setup({ pollTimeoutSec: 0 });
  agent.hang(1);
  agent.set({ version: 1, claimed: true, data: { A: "1" } });
  await until(() => c.getEnvState().injected, 5000);
  assert.ok(agent.queries.length >= 2);
  await done();
});

test("heartbeat is throttled, force bypasses", async () => {
  let t = 1_000_000;
  const { agent, c, done } = await setup({ now: () => t });
  await c.heartbeat();
  await c.heartbeat();
  t += 61_000;
  await c.heartbeat();
  await c.heartbeat(true);
  assert.equal(agent.heartbeats.length, 3);
  await done();
});

test("stop ends the loop while a poll is pending", async () => {
  const { done } = await setup();
  await new Promise((r) => setTimeout(r, 50));
  await done();
});
