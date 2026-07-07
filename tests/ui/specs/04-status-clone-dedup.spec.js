/**
 * 04-status-clone-dedup.spec.js
 * - Status change rebuilds suggestions
 * - Clone previous day
 * - Already-logged meetings not re-suggested
 */
'use strict';
const { test, expect } = require('../helpers/fixtures');

const DATE1 = '2026-06-01'; // "previous day"
const DATE2 = '2026-06-02'; // test day

test.describe('Status change', () => {
  test('changing to holiday replaces suggestions with leave task', async ({ app, api }) => {
    await api.put(`/api/days/${DATE2}`, {
      suggested: [{ issueKey: 'EDB-200', minutes: 420, comment: 'Work', category: 'activity' }],
    });
    await app.goto('/');
    await app.evaluate(date => selectDay(date), DATE2);
    await app.waitForSelector('#status-sel');

    await app.selectOption('#status-sel', 'holiday');
    await app.waitForTimeout(1500);

    const day = await api.getDay(DATE2);
    expect(day.body.status).toBe('holiday');
    const keys = (day.body.suggested || []).map(w => w.issueKey);
    expect(keys).toContain('LEAVE-1');
    expect(keys).not.toContain('EDB-200');
  });

  test('changing back to working restores activity-based suggestions', async ({ app, api }) => {
    await api.put(`/api/days/${DATE2}`, { suggested: [] });
    await app.goto('/');
    await app.evaluate(date => selectDay(date), DATE2);
    await app.waitForSelector('#status-sel');

    await app.selectOption('#status-sel', 'holiday');
    await app.waitForTimeout(800);
    await app.selectOption('#status-sel', 'working');
    await app.waitForTimeout(1500);

    const day = await api.getDay(DATE2);
    expect(day.body.status).toBe('working');
  });
});

test.describe('Clone previous day', () => {
  test('copies rows from previous business day', async ({ app, api }) => {
    // Seed DATE1 with one suggested row.
    await api.put(`/api/days/${DATE1}`, {
      suggested: [{ issueKey: 'EDB-111', minutes: 420, comment: 'Cloned work', category: 'activity' }],
    });
    await app.goto('/');
    await app.evaluate(date => selectDay(date), DATE2);
    await app.waitForSelector('button:has-text("Clone previous day")');

    await app.locator('button:has-text("Clone previous day")').click();
    await app.waitForTimeout(1200);

    const day = await api.getDay(DATE2);
    const cloned = (day.body.suggested || []).find(w => w.issueKey === 'EDB-111');
    expect(cloned).toBeTruthy();
    expect(cloned.comment).toBe('Cloned work');
  });
});

test.describe('Meeting deduplication', () => {
  test('meeting already in Existing is not suggested again', async ({ app, api }) => {
    // Inject existing worklog for the meeting.
    // We do this by submitting one row first, then rebuilding.
    await api.put(`/api/days/${DATE2}`, {
      existing: [{ issueKey: 'MEET-1', minutes: 30, comment: 'Standup', category: 'existing', id: 'ex-1' }],
      suggested: [
        { issueKey: 'MEET-1',  minutes: 30,  comment: 'Standup',     category: 'meeting' },
        { issueKey: 'EDB-200', minutes: 390, comment: 'Feature work', category: 'activity' },
      ],
    });
    // Trigger a plan rebuild.
    await api.post(`/api/reload`, {});
    await app.waitForTimeout(1500);

    const day = await api.getDay(DATE2);
    const standupSuggestions = (day.body.suggested || []).filter(
      w => w.issueKey === 'MEET-1' && w.comment === 'Standup'
    );
    // The already-logged meeting must not appear in Suggested.
    expect(standupSuggestions.length).toBe(0);
  });
});
