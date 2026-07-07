/**
 * 02-worklog-editing.spec.js — Edit suggested worklog rows (key, time, comment).
 */
'use strict';
const { test, expect } = require('../helpers/fixtures');

const DATE = '2026-06-01';

test.describe('Worklog row editing', () => {
  test.beforeEach(async ({ app, api }) => {
    // Seed one suggested row via the API so the day has something to edit.
    await api.put(`/api/days/${DATE}`, {
      suggested: [{ issueKey: 'EDB-100', minutes: 120, comment: 'initial', category: 'activity' }],
    });
    await app.goto('/');
    await app.locator(`.iday-item[data-date="${DATE}"], .iday-item`).first().click();
    await app.waitForSelector('#sugg-table', { timeout: 10_000 });
  });

  test('editing time updates the row and summary', async ({ app }) => {
    const timeInput = app.locator('#sugg-table input[placeholder="1h 30m"]').first();
    await timeInput.fill('2h');
    await timeInput.blur();
    await app.waitForTimeout(500);
    await expect(app.locator('.summary-line')).toContainText('2h');
  });

  test('editing comment updates the row', async ({ app }) => {
    const commentInput = app.locator('#sugg-table input[onchange*="comment"]').first();
    await commentInput.fill('updated comment');
    await commentInput.blur();
    await app.waitForTimeout(500);
    // No error toast should appear.
    await expect(app.locator('#toast')).not.toBeVisible();
  });

  test('invalid issue key is rejected on submit', async ({ app }) => {
    const keyInput = app.locator('#sugg-table input[id^="key-"]').first();
    await keyInput.fill('not-a-key!!');
    await keyInput.blur();
    await app.waitForTimeout(300);
    // Click submit on the row.
    await app.locator('#sugg-table button:has-text("Submit")').first().click();
    await expect(app.locator('#toast')).toContainText(/invalid|key/i, { timeout: 5_000 });
  });

  test('parseIssueKey strips title text from display value', async ({ app }) => {
    const keyInput = app.locator('#sugg-table input[id^="key-"]').first();
    // Simulate user typing a key+title like what fetchIssueTitle sets.
    await keyInput.fill('EDB-999 — Some title text');
    await keyInput.dispatchEvent('change');
    await app.waitForTimeout(300);
    // The underlying day.suggested[0].issueKey should be just EDB-999.
    const day = await app.evaluate(() => {
      const d = window.days && window.days.find(d => d.suggested && d.suggested.length);
      return d ? d.suggested[0].issueKey : null;
    });
    expect(day).toBe('EDB-999');
  });

  test('deleting a row removes it from the table', async ({ app }) => {
    const before = await app.locator('#sugg-table tr.cat-activity').count();
    await app.locator('#sugg-table button.del-btn').first().click();
    await app.waitForTimeout(400);
    const after = await app.locator('#sugg-table tr.cat-activity').count();
    expect(after).toBe(before - 1);
  });
});
