import { defineConfig } from "@playwright/test";

// Marketing screenshots for apps/site — not part of the e2e suite.
export default defineConfig({
  testDir: "./test/shots",
  timeout: 600_000,
  workers: 1,
  retries: 0,
  use: { headless: true, ignoreHTTPSErrors: true },
  reporter: [["list"]],
});
