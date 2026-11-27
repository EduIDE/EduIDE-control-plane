export interface Notice {
  kind: "idle-warning" | "max-warning" | "info";
  message: string;
  at?: string;
}

export interface LocalSession {
  version: number;
  claimed: boolean;
  data?: Record<string, string>;
  notices?: Notice[];
}

export interface EnvState {
  injected: boolean;
  environment: Record<string, string>;
}

export interface AgentClientOptions {
  baseUrl: string;
  onNotice: (n: Notice) => void;
  // Remembers shown notices across IDE reloads (a VS Code Memento such as globalState).
  store?: { get<T>(key: string, fallback: T): T; update(key: string, value: unknown): Thenable<void> | Promise<void> };
  pollTimeoutSec?: number;
  minBackoffMs?: number;
  maxBackoffMs?: number;
  heartbeatIntervalMs?: number;
  now?: () => number;
  // Runs once the session is claimed, and again after the agent was unreachable, before the launch data is handed out.
  beforeExpose?: () => Promise<void>;
}

const STORE_KEY = "eduide.shownNotices";
const MAX_REMEMBERED = 32;
const noticeKey = (n: Notice) => `${n.kind}|${n.at ?? ""}|${n.message}`;

export class AgentClient {
  private version = 0;
  private session: LocalSession = { version: 0, claimed: false };
  private seen = new Set<string>();
  private milestones = new Set<string>();
  private lastHeartbeat = -Infinity;
  private abort = new AbortController();
  private stopped = false;
  private needSync = true;
  private loop?: Promise<void>;

  constructor(private readonly o: AgentClientOptions) {}

  start(): void {
    this.loop = this.run();
  }

  async stop(): Promise<void> {
    this.stopped = true;
    this.abort.abort();
    await this.loop;
  }

  // The agent stores launch data atomically with the claim, so claimed means injected.
  getEnvState(): EnvState {
    return { injected: this.session.claimed, environment: { ...this.session.data } };
  }

  // Same as the data bridge: returns at once with the requested keys that are set.
  getEnv(keys: string[]): Record<string, string> {
    const env = this.session.data ?? {};
    return Object.fromEntries(keys.filter((k) => Object.hasOwn(env, k)).map((k) => [k, env[k]]));
  }

  // Sends a heartbeat unless one went out within the interval. force skips the throttle.
  async heartbeat(force = false): Promise<void> {
    const now = (this.o.now ?? Date.now)();
    if (!force && now - this.lastHeartbeat < (this.o.heartbeatIntervalMs ?? 60000)) {
      return;
    }
    this.lastHeartbeat = now;
    // The first heartbeat that goes out marks the first user input.
    void this.milestone("first-input");
    try {
      const r = await fetch(`${this.o.baseUrl}/v1/heartbeat`, {
        method: "POST",
        signal: this.abort.signal,
      });
      if (!r.ok) {
        this.lastHeartbeat = -Infinity;
      }
    } catch {
      this.lastHeartbeat = -Infinity;
    }
  }

  // Reports a milestone once per name. The agent keeps the first report, so errors are ignored after one retry.
  async milestone(name: string, at: string = new Date().toISOString()): Promise<void> {
    if (this.milestones.has(name)) {
      return;
    }
    this.milestones.add(name);
    for (let i = 0; i < 2; i++) {
      try {
        await fetch(`${this.o.baseUrl}/v1/milestones`, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ name, at }),
          signal: this.abort.signal,
        });
        return;
      } catch {
        // retry once on network failure
      }
    }
  }

  private apply(s: LocalSession) {
    this.session = s;
    this.version = s.version;
    for (const n of s.notices ?? []) {
      const k = noticeKey(n);
      if (this.seen.has(k)) {
        continue;
      }
      this.seen.add(k);
      // Notices with an end time are remembered by kind and end time; the end time ends their relevance.
      const pk = n.at ? `${n.kind}|${n.at}` : undefined;
      const shown = pk ? this.o.store?.get<string[]>(STORE_KEY, []) ?? [] : [];
      if (pk && shown.includes(pk)) {
        continue;
      }
      if (pk) {
        void this.o.store?.update(STORE_KEY, [...shown, pk].slice(-MAX_REMEMBERED));
      }
      this.o.onNotice(n);
    }
  }

  private async run(): Promise<void> {
    const min = this.o.minBackoffMs ?? 500;
    const max = this.o.maxBackoffMs ?? 15000;
    let backoff = min;
    const timeout = this.o.pollTimeoutSec ?? 30;
    while (!this.stopped) {
      try {
        const r = await fetch(
          `${this.o.baseUrl}/v1/session?after=${this.version}&timeout=${timeout}`,
          // A hung agent must not stall the loop forever.
          { signal: AbortSignal.any([this.abort.signal, AbortSignal.timeout((timeout + 1) * 2000)]) },
        );
        if (!r.ok) {
          throw new Error(`status ${r.status}`);
        }
        const s = (await r.json()) as LocalSession;
        // Other extensions read the launch data through the commands. They must not see it before Git is set up.
        if (s.claimed && this.needSync) {
          this.needSync = false;
          await this.o.beforeExpose?.().catch(() => undefined);
        }
        this.apply(s);
        backoff = min;
      } catch {
        if (this.stopped) {
          return;
        }
        this.needSync = true;
        await new Promise<void>((resolve) => {
          const onAbort = () => (clearTimeout(t), resolve());
          const t = setTimeout(() => {
            this.abort.signal.removeEventListener("abort", onAbort);
            resolve();
          }, backoff);
          this.abort.signal.addEventListener("abort", onAbort, { once: true });
        });
        backoff = Math.min(backoff * 2, max);
      }
    }
  }
}
