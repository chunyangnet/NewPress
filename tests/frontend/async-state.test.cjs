const test = require('node:test');
const assert = require('node:assert/strict');
const { loadModule } = require('./helpers/typescript.cjs');

test('deadline distinguishes timeout and can be disposed before firing', () => {
  const timers = new Map(); let id = 0;
  const { requestDeadline } = loadModule('frontend/shared/async-state.ts', {
    setTimeout(fn) { timers.set(++id, fn); return id; }, clearTimeout(id) { timers.delete(id); },
  });
  const first = new AbortController(), a = requestDeadline(first);
  a.dispose(); assert.equal(timers.size, 0); assert.equal(a.timedOut, false);
  const second = new AbortController(), b = requestDeadline(second);
  [...timers.values()][0](); assert.equal(second.signal.aborted, true); assert.equal(b.timedOut, true); b.dispose();
  const third = new AbortController(), c = requestDeadline(third); third.abort();
  assert.equal(c.timedOut, false); c.dispose();
});

test('loading placeholder skips fast responses and clears delayed state', () => {
  const timers = new Map(); let id = 0;
  const { loadingPlaceholder } = loadModule('frontend/shared/ui-state.ts', {
    setTimeout(fn) { timers.set(++id, fn); return id; }, clearTimeout(id) { timers.delete(id); },
  });
  const attrs = new Map(), region = { isConnected: true, setAttribute(k,v) { attrs.set(k,v); }, removeAttribute(k) { attrs.delete(k); } };
  const fast = loadingPlaceholder(region, false); fast(); assert.equal(timers.size, 0); assert.equal(attrs.size, 0);
  const slow = loadingPlaceholder(region, true); [...timers.values()][0](); assert.equal(attrs.get('data-async-loading'), 'refreshing'); slow(); assert.equal(attrs.size, 0);
});
