/**
 * 05-settings.spec.js — Settings page load, edit, and save.
 */
'use strict';
const { test, expect } = require('../helpers/fixtures');

test.describe('Settings page', () => {
  test.beforeEach(async ({ app }) => {
    await app.goto('/settings');
    await app.waitForSelector('#meetingKey');
  });

  test('loads current config values', async ({ app }) => {
    await expect(app.locator('#meetingKey')).toHaveValue('MEET-1');
    await expect(app.locator('#leaveKey')).toHaveValue('LEAVE-1');
    await expect(app.locator('#workdayHours')).toHaveValue('7');
  });

  test('save button is visible at the top of the page', async ({ app }) => {
    // The save bar should be the first section — check it appears above all other sections.
    const saveBtnBox = await app.locator('button:has-text("Save & rebuild plans")').boundingBox();
    const firstSectionBox = await app.locator('section').nth(1).boundingBox();
    expect(saveBtnBox.y).toBeLessThan(firstSectionBox.y);
  });

  test('editing meeting key and saving persists the value', async ({ app, api }) => {
    await app.locator('#meetingKey').fill('MEET-99');
    await app.locator('button:has-text("Save & rebuild plans")').click();
    await app.waitForTimeout(2000);

    const cfg = await api.get('/api/config');
    expect(cfg.body.meetingKey).toBe('MEET-99');

    // Restore.
    await api.put('/api/config', { meetingKey: 'MEET-1' });
  });

  test('date range fields persist via API', async ({ app, api }) => {
    await app.locator('#reportFrom').fill('2026-05-01');
    await app.locator('#reportTo').fill('2026-05-31');
    await app.locator('button:has-text("Save & rebuild plans")').click();
    await app.waitForTimeout(1500);

    const cfg = await api.get('/api/config');
    expect(cfg.body.reportFrom).toBe('2026-05-01');
    expect(cfg.body.reportTo).toBe('2026-05-31');

    // Restore.
    await api.put('/api/config', { reportFrom: '', reportTo: '' });
  });

  test('Settings footer shows app version', async ({ app }) => {
    await expect(app.locator('footer')).toContainText('Timereporting Assistant');
  });
});
