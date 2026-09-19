import { defineConfig, devices } from '@playwright/test';

// Frontend verification only. Uses the installed Google Chrome (channel) so
// no browser download is needed; run `npm run dev` first or set E2E_BASE_URL.
export default defineConfig({
  testDir: './tests',
  testIgnore: ['**/unit/**'],
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  timeout: 90000,
  expect: { timeout: 15000 },
  outputDir: process.env.PW_OUTPUT_DIR || 'test-results',
  use: {
    baseURL: process.env.E2E_BASE_URL || 'http://127.0.0.1:5173',
    browserName: 'chromium',
    channel:
      process.env.PW_CHANNEL ||
      (process.platform === 'win32' ? 'chrome' : undefined),
    trace: 'off',
    screenshot: 'off',
  },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'] } },
    {
      name: 'tablet',
      use: {
        viewport: { width: 820, height: 1180 },
        deviceScaleFactor: 2,
        isMobile: true,
        hasTouch: true,
      },
    },
    { name: 'mobile', use: { ...devices['Pixel 7'] } },
  ],
});
