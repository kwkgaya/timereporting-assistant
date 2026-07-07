'use strict';

module.exports = async function globalTeardown() {
  const kill = proc => {
    if (!proc) return;
    try { proc.kill('SIGTERM'); } catch (_) {}
  };
  kill(global.__testAppProc);
  kill(global.__testMockProc);
  console.log('[teardown] Processes stopped.');
};
