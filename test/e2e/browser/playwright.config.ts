import { defineConfig } from "@playwright/test";

const port = process.env.E2E_PORT ?? "18443";

// Chromium resolves *.e2e.test to the gateway on 127.0.0.1. The test CA is not in the browser trust store,
// so certificate errors are ignored; this only works against the throwaway kind cluster.
export default defineConfig({
  testDir: ".",
  timeout: 120_000,
  retries: 0,
  reporter: [["list"]],
  use: {
    ignoreHTTPSErrors: true,
    launchOptions: { args: [`--host-resolver-rules=MAP *.e2e.test 127.0.0.1:${port}`] },
  },
});
