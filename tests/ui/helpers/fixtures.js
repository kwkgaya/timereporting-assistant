/**
 * fixtures.js — Playwright test fixtures shared across all spec files.
 *
 * - `app`   : a Page already at http://localhost:19080/ with the day list loaded.
 * - `api`   : thin wrapper around the app's REST API for direct state seeding.
 * - `reset` : clears all mock Jira worklogs so each test starts clean.
 */
'use strict';
const { test: base, expect } = require('@playwright/test');
const http = require('http');

const APP_PORT  = 19080;
const MOCK_PORT = 19099;
const APP_BASE  = `http://localhost:${APP_PORT}`;
const MOCK_BASE = `http://localhost:${MOCK_PORT}`;

/**
 * Simple JSON fetch over Node's built-in http (no extra deps).
 */
function jsonFetch(url, method = 'GET', body = null) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const opts = {
      hostname: u.hostname,
      port: u.port,
      path: u.pathname + u.search,
      method,
      headers: { 'Content-Type': 'application/json', 'Origin': APP_BASE },
    };
    const req = http.request(opts, res => {
      let data = '';
      res.on('data', c => (data += c));
      res.on('end', () => {
        try { resolve({ status: res.statusCode, body: JSON.parse(data) }); }
        catch (_) { resolve({ status: res.statusCode, body: data }); }
      });
    });
    req.on('error', reject);
    if (body) req.write(JSON.stringify(body));
    req.end();
  });
}

exports.test = base.extend({
  /** Resets mock Jira worklogs before each test, then navigates to the app root. */
  app: async ({ page }, use) => {
    // Clear all mock worklogs via the app's proxy endpoint.
    await jsonFetch(`${APP_BASE}/api/mock/clear-worklogs`, 'POST');
    // Reload plans so the app picks up the cleared state.
    await jsonFetch(`${APP_BASE}/api/reload`, 'POST');
    // Wait for reload to finish (the stream endpoint is async; poll status).
    await new Promise(r => setTimeout(r, 1500));
    await page.goto('/');
    await page.waitForSelector('.iday-item', { timeout: 20_000 });
    await use(page);
  },

  /** Direct REST helper for seeding / asserting server state. */
  api: async ({}, use) => {
    await use({
      get:  (path)        => jsonFetch(`${APP_BASE}${path}`),
      put:  (path, body)  => jsonFetch(`${APP_BASE}${path}`, 'PUT', body),
      post: (path, body)  => jsonFetch(`${APP_BASE}${path}`, 'POST', body),
      getDay: (date)      => jsonFetch(`${APP_BASE}/api/days/${date}`),
    });
  },
});

exports.expect = expect;
exports.APP_BASE  = APP_BASE;
exports.MOCK_BASE = MOCK_BASE;
