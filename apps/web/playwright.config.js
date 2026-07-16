import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./test/e2e",
  timeout: 120_000,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  use: {
    headless: true,
    ignoreHTTPSErrors: true,
  },
  reporter: [["list"]],
});
