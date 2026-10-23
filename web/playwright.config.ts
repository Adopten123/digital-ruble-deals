import { defineConfig } from '@playwright/test';
import { E2E_BASE_URL, statePath } from './e2e/support.ts';

export default defineConfig({
  testDir: './e2e',
  outputDir: './test-results/artifacts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: Boolean(process.env.CI),
  timeout: 30_000,
  expect: { timeout: 7_000 },
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }]],
  use: {
    baseURL: E2E_BASE_URL,
    headless: true,
    locale: 'ru-RU',
    timezoneId: 'Europe/Moscow',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'setup', testMatch: /auth\.setup\.ts/ },
    { name: 'api', testMatch: /api\/.*\.spec\.ts/, dependencies: ['setup'] },
    {
      name: 'chromium', testMatch: /ui\/.*\.spec\.ts/,
      testIgnore: /responsive\.spec\.ts/, dependencies: ['setup'],
      use: { browserName: 'chromium', viewport: { width: 1440, height: 1000 }, storageState: statePath('customer') },
    },
    {
      name: 'mobile', testMatch: /ui\/responsive\.spec\.ts/, dependencies: ['setup'],
      use: { browserName: 'chromium', viewport: { width: 375, height: 812 }, isMobile: true, hasTouch: true, storageState: statePath('customer') },
    },
  ],
});
