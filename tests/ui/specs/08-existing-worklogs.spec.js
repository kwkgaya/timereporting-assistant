/**
 * 08-existing-worklogs.spec.js — Already-logged worklogs: display, edit, delete.
 */
'use strict';
const { test, expect } = require('../helpers/fixtures');

const DATE = '2026-06-04';

test.describe('Existing worklogs', () => {
  test.beforeEach(async ({ app, api }) => {
    // Seed a suggested row and submit it so the day has a real logged worklog.
    await api.put(`/api/days/${DATE}`, {
      suggested: [{ issueKey: 'EDB-300', minutes: 240, comment: 'Existing work', category: 'manual' }],
    });
    await api.post(`/api/days/${DATE}/rows/0/submit`);
    await app.goto('/');
    // Wait for the initial day to render before switching, so the app's own
    // startup render cannot overwrite our selection.
    await app.waitForSelector('#detail h2', { timeout: 20_000 });
    await app.evaluate(date => selectDay(date), DATE);
    await expect(app.locator('#detail h2')).toContainText(DATE);
    await app.waitForSelector('tr.cat-existing', { timeout: 10_000 });
  });

  test('existing worklog is shown in the Already-logged table', async ({ app }) => {
    // The issue key lives in a read-only input, so assert on its value.
    await expect(app.locator('tr.cat-existing input[id^="ex-key-"]').first()).toHaveValue(/EDB-300/);
    await expect(app.locator('.wl-table').first()).toContainText('4h');
  });

  test('edit deletes the worklog and re-opens it in the Suggested section', async ({ app }) => {
    await app.locator('button[title^="Edit"]').first().click();
    await app.waitForTimeout(800);
    // The worklog now appears as a highlighted, editable suggested row.
    const row = app.locator('#sugg-table tr.row-editing').first();
    await expect(row).toBeVisible();
    // Issue key is not editable on a re-submission row.
    await expect(row.locator('input[id^="key-"]')).toHaveAttribute('readonly', '');
    // It is gone from the logged table.
    await expect(app.locator('tr.cat-existing')).toHaveCount(0);
  });
});
