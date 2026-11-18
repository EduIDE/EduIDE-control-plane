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
  pollTimeoutSec?: number;
  minBackoffMs?: number;
  maxBackoffMs?: number;
  heartbeatIntervalMs?: number;
  now?: () => number;
}

const noticeKey = (n: Notice) => `${n.kind}|${n.at ?? ""}|${n.message}`;

export class AgentClient {
  private version = 0;
  private session: LocalSession = { version: 0, claimed: false };
  private seen = new Set<string>();
  private lastHeartbeat = -Infinity;
  private abort = new AbortController();
  private stopped = false;
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

  private apply(s: LocalSession) {
    this.session = s;
    this.version = s.version;
    for (const n of s.notices ?? []) {
      const k = noticeKey(n);
      if (!this.seen.has(k)) {
        this.seen.add(k);
        this.o.onNotice(n);
      }
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
        this.apply((await r.json()) as LocalSession);
        backoff = min;
      } catch {
        if (this.stopped) {
          return;
        }
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
