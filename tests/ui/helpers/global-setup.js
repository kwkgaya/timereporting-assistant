/**
 * global-setup.js — builds Go binaries and starts mock Jira + the review UI
 * before the entire Playwright test run. Both processes are stored in
 * process.env so global-teardown can kill them.
 */
'use strict';
const { execSync, spawn } = require('child_process');
const path  = require('path');
const http  = require('http');
const fs    = require('fs');

const ROOT      = path.resolve(__dirname, '../../..');
const BIN_DIR   = path.join(ROOT, '.tmp-test-bin');
const APP_BIN   = path.join(BIN_DIR, 'timeporting-test.exe');
const MOCK_BIN  = path.join(BIN_DIR, 'mockjira-test.exe');
const APP_PORT  = 19080;
const MOCK_PORT = 19099;

// Minimal config for the test run — points at mock Jira, no real Jira needed.
const TEST_CONFIG = {
  jira: { baseURL: `http://localhost:${MOCK_PORT}`, email: 'test@example.com' },
  meetingIssueKey: 'MEET-1',
  leaveIssueKey: 'LEAVE-1',
  workdayHours: 7,
  mockJiraPort: MOCK_PORT,
  webPort: APP_PORT,
  target: 'mock',
  logMeetingsSeparately: true,
  autoUpdate: false,
};

const CFG_PATH = path.join(BIN_DIR, 'test-config.json');

function waitFor(url, timeoutMs = 15_000) {
  return new Promise((resolve, reject) => {
    const deadline = Date.now() + timeoutMs;
    const poll = () => {
      http.get(url, res => {
        if (res.statusCode < 500) return resolve();
        if (Date.now() > deadline) return reject(new Error(`Timeout waiting for ${url}`));
        setTimeout(poll, 300);
      }).on('error', () => {
        if (Date.now() > deadline) return reject(new Error(`Timeout waiting for ${url}`));
        setTimeout(poll, 300);
      });
    };
    poll();
  });
}

module.exports = async function globalSetup() {
  fs.mkdirSync(BIN_DIR, { recursive: true });
  fs.writeFileSync(CFG_PATH, JSON.stringify(TEST_CONFIG, null, 2));

  console.log('[setup] Building Go test binaries…');
  execSync(
    `go build -o "${APP_BIN}" ./cmd/timeporting && go build -o "${MOCK_BIN}" ./cmd/mockjira`,
    { cwd: ROOT, stdio: 'pipe' }
  );

  console.log('[setup] Starting mock Jira…');
  const mockProc = spawn(MOCK_BIN, ['-port', String(MOCK_PORT)], {
    cwd: BIN_DIR, detached: false,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  mockProc.stdout.on('data', d => process.stdout.write('[mockjira] ' + d));
  mockProc.stderr.on('data', d => process.stderr.write('[mockjira] ' + d));
  await waitFor(`http://localhost:${MOCK_PORT}/api/worklogs`);
  console.log('[setup] Mock Jira ready.');

  console.log('[setup] Starting review UI…');
  // Use a fixed date range so tests are deterministic.
  const appProc = spawn(
    APP_BIN,
    ['-config', CFG_PATH, '-from', '2026-06-01', '-to', '2026-06-30', '-no-browser'],
    { cwd: BIN_DIR, detached: false, stdio: ['ignore', 'pipe', 'pipe'] }
  );
  appProc.stdout.on('data', d => process.stdout.write('[app] ' + d));
  appProc.stderr.on('data', d => process.stderr.write('[app] ' + d));
  await waitFor(`http://localhost:${APP_PORT}/api/status`);
  console.log('[setup] Review UI ready.');

  // Store PIDs for teardown.
  process.env._TEST_MOCK_PID = String(mockProc.pid);
  process.env._TEST_APP_PID  = String(appProc.pid);
  // Keep references so they aren't GC'd.
  global.__testMockProc = mockProc;
  global.__testAppProc  = appProc;
};
