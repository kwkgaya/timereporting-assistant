/**
 * 01-day-list.spec.js — Day list rendering, navigation, and date display.
 */
'use strict';
const { test, expect } = require('../helpers/fixtures');

test.describe('Day list', () => {
  test('renders weekdays in the list', async ({ app }) => {
    const items = app.locator('.iday-item');
    await expect(items).toHaveCount({ minimum: 1 });
  });

  test('clicking a day shows it in the detail panel', async ({ app }) => {
    const first = app.locator('.iday-item').first();
    const dateText = await first.getAttribute('data-date') ??
      await first.textContent();
    await first.click();
    // Detail header should show the date.
    await expect(app.locator('h2')).toContainText('2026-06');
  });

  test('navigation arrows move to adjacent days', async ({ app }) => {
    // Navigate to first day.
    await app.locator('.iday-item').first().click();
    await app.waitForSelector('h2');
    const initial = await app.locator('h2').textContent();

    // Click next-day arrow.
    await app.locator('button.nav-btn').nth(1).click();
    await app.waitForTimeout(600);
    const next = await app.locator('h2').textContent();
    expect(next).not.toBe(initial);
  });

  test('summary line always shows Target / Existing / Suggested / Total', async ({ app }) => {
    await app.locator('.iday-item').first().click();
    await app.waitForSelector('.summary-line');
    await expect(app.locator('.summary-line')).toContainText('Target:');
    await expect(app.locator('.summary-line')).toContainText('Existing:');
    await expect(app.locator('.summary-line')).toContainText('Suggested:');
    await expect(app.locator('.summary-line')).toContainText('Total:');
  });
});
