/**
 * 03-submit.spec.js — Single-row submit and Approve & submit flows.
 * Verifies that the correct issue key is sent (regression for the stale-key bug).
 */
'use strict';
const { test, expect } = require('../helpers/fixtures');

const DATE = '2026-06-02';

test.describe('Submit flows', () => {
  test.beforeEach(async ({ app, api }) => {
    await api.put(`/api/days/${DATE}`, {
      suggested: [
        { issueKey: 'MEET-1', minutes: 30,  comment: 'Morning standup', category: 'meeting' },
        { issueKey: 'EDB-200', minutes: 390, comment: 'Feature work',    category: 'activity' },
      ],
    });
    await app.goto('/');
    await app.locator('.iday-item').first().click();
    // Navigate to the seeded date.
    await app.evaluate(date => {
      if (typeof selectDay === 'function') selectDay(date);
    }, DATE);
    await app.waitForSelector('#sugg-table', { timeout: 10_000 });
  });

  test('single-row submit moves row to Existing and uses the correct issue key', async ({ app, api }) => {
    // Submit only the first row (MEET-1 / 30m).
    await app.locator('#sugg-table button:has-text("Submit")').first().click();
    await app.waitForTimeout(1500);

    // The row should now appear in the Already-logged table, not Suggested.
    const existing = await api.getDay(DATE);
    const loggedKeys = existing.body.existing.map(w => w.issueKey);
    expect(loggedKeys).toContain('MEET-1');

    const suggestedKeys = (existing.body.suggested || []).map(w => w.issueKey);
    expect(suggestedKeys).not.toContain('MEET-1');
  });

  test('Approve & submit flushes local state and submits all rows', async ({ app, api }) => {
    // Change the comment of the first row in the UI before submitting.
    const commentInput = app.locator('#sugg-table input[onchange*="comment"]').first();
    await commentInput.fill('Edited standup comment');
    await commentInput.blur();
    await app.waitForTimeout(300);

    await app.locator('button:has-text("Approve")').click();
    await app.waitForTimeout(2000);

    const day = await api.getDay(DATE);
    expect(day.body.submitted).toBe(true);

    // Both rows should now be in Existing.
    const loggedKeys = day.body.existing.map(w => w.issueKey);
    expect(loggedKeys).toContain('MEET-1');
    expect(loggedKeys).toContain('EDB-200');

    // The edited comment should have been sent (flush-before-submit test).
    const standup = day.body.existing.find(w => w.issueKey === 'MEET-1');
    expect(standup.comment).toBe('Edited standup comment');
  });

  test('submitted day shows badge and no Approve button', async ({ app, api }) => {
    await app.locator('button:has-text("Approve")').click();
    await app.waitForTimeout(2000);

    await expect(app.locator('.badge-submitted')).toBeVisible();
    await expect(app.locator('button:has-text("Approve")')).not.toBeVisible();
  });
});
