import createClient from "openapi-fetch";
import type { paths, components } from "./schema.gen.js";

export type Session = components["schemas"]["Session"];
export type Workspace = components["schemas"]["Workspace"];
export type Template = components["schemas"]["Template"];
export type Overview = components["schemas"]["Overview"];
export type Problem = components["schemas"]["Problem"];
export type LaunchRequest = components["schemas"]["LaunchRequest"];

/** Creates a typed client. `baseUrl` ends with /api/v1. `token` returns a fresh OIDC access token. */
export function createControlPlaneClient(baseUrl: string, token: () => Promise<string> | string) {
  const client = createClient<paths>({ baseUrl });
  client.use({
    async onRequest({ request }) {
      request.headers.set("Authorization", `Bearer ${await token()}`);
      return request;
    },
  });
  return client;
}

/**
 * Reads a Server-Sent Events stream with fetch, because EventSource cannot send the
 * Authorization header. Calls onEvent per event until the stream ends or signal aborts.
 */
export async function readEvents<T>(
  url: string,
  token: string,
  onEvent: (event: string, data: T) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(url, { headers: { Authorization: `Bearer ${token}`, Accept: "text/event-stream" }, signal });
  if (!res.ok || !res.body) throw new Error(`events: HTTP ${res.status}`);
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) return;
    buf += value;
    let i: number;
    while ((i = buf.indexOf("\n\n")) >= 0) {
      const block = buf.slice(0, i);
      buf = buf.slice(i + 2);
      const parsed = parseEvent(block);
      if (parsed) onEvent(parsed.event, JSON.parse(parsed.data) as T);
    }
  }
}

/** Parses one SSE block; returns undefined for comments and empty blocks. */
export function parseEvent(block: string): { event: string; data: string } | undefined {
  let event = "message";
  const data: string[] = [];
  for (const line of block.split("\n")) {
    if (line.startsWith(":") || line === "") continue;
    const [field, ...rest] = line.split(":");
    const value = rest.join(":").replace(/^ /, "");
    if (field === "event") event = value;
    else if (field === "data") data.push(value);
  }
  return data.length ? { event, data: data.join("\n") } : undefined;
}
