// @ts-check
const { defineConfig } = require('@playwright/test');

const APP_PORT  = 19080;
const MOCK_PORT = 19099;

module.exports = defineConfig({
  testDir: './specs',
  timeout: 30_000,
  retries: 1,
  workers: 1,          // sequential — one server instance shared across tests
  reporter: [['list'], ['html', { outputFolder: 'playwright-report', open: 'never' }]],

  use: {
    baseURL: `http://localhost:${APP_PORT}`,
    headless: true,
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },

  globalSetup: require.resolve('./helpers/global-setup.js'),
  globalTeardown: require.resolve('./helpers/global-teardown.js'),
});

module.exports.APP_PORT  = APP_PORT;
module.exports.MOCK_PORT = MOCK_PORT;
