/**
 * 06-wizard.spec.js — Setup wizard navigation and basic validation.
 */
'use strict';
const { test, expect } = require('../helpers/fixtures');

test.describe('Setup wizard', () => {
  test.beforeEach(async ({ app }) => {
    await app.goto('/wizard');
    await app.waitForSelector('.wizard-step');
  });

  test('starts on step 1 (Welcome) with Next button', async ({ app }) => {
    await expect(app.locator('.wizard-step')).toContainText(/welcome/i);
    await expect(app.locator('button:has-text("Next")')).toBeVisible();
  });

  test('Next advances through steps', async ({ app }) => {
    await app.locator('button:has-text("Next")').click();
    await app.waitForTimeout(300);
    // Step 2 should mention Jira URL.
    await expect(app.locator('.wizard-step')).toContainText(/jira/i);
  });

  test('Back button returns to previous step', async ({ app }) => {
    await app.locator('button:has-text("Next")').click();
    await app.waitForTimeout(300);
    await app.locator('button:has-text("Back")').click();
    await app.waitForTimeout(300);
    await expect(app.locator('.wizard-step')).toContainText(/welcome/i);
  });

  test('Jira step requires a URL before advancing', async ({ app }) => {
    await app.locator('button:has-text("Next")').click(); // → Jira URL step
    await app.waitForTimeout(300);
    // Clear any pre-filled URL.
    const urlInput = app.locator('#w-jiraBaseUrl, input[placeholder*="atlassian.net"]').first();
    if (await urlInput.count()) {
      await urlInput.fill('');
    }
    await app.locator('button:has-text("Next")').click();
    await app.waitForTimeout(400);
    // Should still be on the Jira step (validation prevented advance).
    await expect(app.locator('.wizard-step')).toContainText(/jira/i);
  });
});
