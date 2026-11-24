import { spawn } from "node:child_process";

// Git accounts (ADR 0023): the extension points Git at the agent for the provider hosts. Git then follows
//   url.<agent>/git/<host>/.insteadOf = https://<host>/
// to the loopback listener of the agent, which adds the user's credential outside the pod. No token is ever
// written here. Entries are written with spawn and an argument array, never through a shell.

export type RunGit = (args: string[]) => Promise<{ code: number; stdout: string }>;

export interface GitRewriteOptions {
  baseUrl: string;
  run?: RunGit;
  // Extra environment for git, for example GIT_CONFIG_GLOBAL in tests.
  env?: NodeJS.ProcessEnv;
  log?: (message: string) => void;
  timeoutMs?: number;
}

// A bare host name: labels of letters, digits and hyphens. No port, no path, no userinfo, nothing a shell or a URL could read differently.
const HOST = /^(?=.{1,253}$)[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$/;
export const isBareHost = (h: unknown): h is string => typeof h === "string" && HOST.test(h);

export class GitRewrite {
  private chain: Promise<void> = Promise.resolve();
  private warned = false;

  constructor(private readonly o: GitRewriteOptions) {}

  // Brings the global Git config in line with the agent's host list. Calls run one after the other. It never throws.
  sync(): Promise<void> {
    this.chain = this.chain.then(() => this.apply()).catch(() => undefined);
    return this.chain;
  }

  private get prefix(): string {
    return `url.${this.o.baseUrl}/git/`;
  }

  private async hosts(): Promise<string[] | undefined> {
    try {
      const r = await fetch(`${this.o.baseUrl}/v1/git/hosts`, { signal: AbortSignal.timeout(this.o.timeoutMs ?? 3000) });
      // 404 of an older agent, or anything else: the feature is off, nothing to do and nothing to show.
      if (!r.ok) {
        return undefined;
      }
      const body = (await r.json()) as { hosts?: unknown };
      return Array.isArray(body.hosts) ? body.hosts.filter(isBareHost) : undefined;
    } catch {
      return undefined;
    }
  }

  private git(args: string[]): Promise<{ code: number; stdout: string }> {
    if (this.o.run) {
      return this.o.run(args);
    }
    return new Promise((resolve) => {
      let stdout = "";
      const child = spawn("git", args, {
        shell: false,
        stdio: ["ignore", "pipe", "ignore"],
        env: { ...process.env, ...this.o.env },
        timeout: 10000,
      });
      child.stdout.on("data", (d) => (stdout += d));
      child.on("error", (e: NodeJS.ErrnoException) => {
        if (!this.warned) {
          this.warned = true;
          this.o.log?.(`git is not available (${e.code ?? "error"}), Git accounts are not set up`);
        }
        resolve({ code: -1, stdout: "" });
      });
      child.on("close", (code) => resolve({ code: code ?? -1, stdout }));
    });
  }

  private async apply(): Promise<void> {
    const hosts = await this.hosts();
    if (!hosts) {
      return;
    }
    const want = new Map(hosts.map((h) => [`${this.prefix}${h}/.insteadof`, `https://${h}/`]));
    const listed = await this.git(["config", "--global", "--get-regexp", "^url\\..*\\.insteadof$"]);
    // Exit code 1 means no such key. Anything else (no git, broken config) leaves the config alone.
    if (listed.code !== 0 && listed.code !== 1) {
      return;
    }
    const have = new Map<string, string>();
    for (const line of listed.stdout.split("\n")) {
      const i = line.indexOf(" ");
      // Git lower-cases the last part of the key; the agent URL and host in the middle keep their case.
      if (i > 0 && line.slice(0, i).startsWith(this.prefix)) {
        have.set(line.slice(0, i), line.slice(i + 1));
      }
    }
    // Only entries with the agent URL prefix are ours; any other insteadOf entry stays untouched.
    for (const key of have.keys()) {
      if (!want.has(key)) {
        await this.git(["config", "--global", "--unset-all", key]);
      }
    }
    for (const [key, value] of want) {
      if (have.get(key) !== value) {
        await this.git(["config", "--global", "--replace-all", key, value]);
      }
    }
  }
}
