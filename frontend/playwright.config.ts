import { defineConfig } from '@playwright/test'

// End-to-end tests drive the real gotable binary (built into ../bin) in
// headless Chrome. See e2e/global-setup.ts.
export default defineConfig({
  testDir: './e2e',
  timeout: 60_000,
  workers: 1,
  fullyParallel: false,
  reporter: [['list']],
  globalSetup: './e2e/global-setup.ts',
  globalTeardown: './e2e/global-teardown.ts',
  use: {
    channel: process.env.PW_CHANNEL ?? 'chrome',
    headless: true,
    viewport: { width: 1440, height: 900 },
    trace: 'retain-on-failure',
  },
})
