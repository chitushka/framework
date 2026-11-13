import { defineConfig, devices } from "@playwright/test"

export default defineConfig({
  testDir: "./e2e-admin", workers: 1, fullyParallel: false, retries: 0,
  timeout: 60_000, forbidOnly: Boolean(process.env.CI),
  reporter: [["list"], ["html", { open: "never" }]],
  use: { ...devices["Desktop Chrome"], baseURL: "http://127.0.0.1:8281", trace: "retain-on-failure", screenshot: "only-on-failure" },
  webServer: { command: "node ../scripts/admin-e2e-server.mjs", url: "http://127.0.0.1:8281/login", timeout: 60_000, reuseExistingServer: false, gracefulShutdown: { signal: "SIGTERM", timeout: 15_000 } },
})
