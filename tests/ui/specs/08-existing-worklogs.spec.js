/**
 * 08-existing-worklogs.spec.js — Already-logged worklogs: display, edit, delete.
 */
'use strict';
const { test, expect } = require('../helpers/fixtures');

const DATE = '2026-06-04';

test.describe('Existing worklogs', () => {
  test.beforeEach(async ({ app, api }) => {
    // Seed one existing worklog.
    await api.put(`/api/days/${DATE}`, {
      existing: [{ id: 'ex-wl-1', issueKey: 'EDB-300', minutes: 240, comment: 'Existing work', category: 'existing', source: 'mock' }],
      suggested: [],
    });
    await app.goto('/');
    await app.evaluate(date => selectDay(date), DATE);
    await app.waitForSelector('.wl-table', { timeout: 10_000 });
  });

  test('existing worklog is shown in the Already-logged table', async ({ app }) => {
    await expect(app.locator('.wl-table').first()).toContainText('EDB-300');
    await expect(app.locator('.wl-table').first()).toContainText('4h');
  });

  test('edit icon opens inline edit mode', async ({ app }) => {
    await app.locator('button[title="Edit"]').first().click();
    await app.waitForTimeout(300);
    // An input for time should now be visible.
    await expect(app.locator('input[id^="ex-time-"]')).toBeVisible();
  });
});
