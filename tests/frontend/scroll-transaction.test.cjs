const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const nodeTest = require('node:test');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');

for (const entry of ['TypeScript', 'generated']) {
const test = (name, fn) => nodeTest(entry + ': ' + name, fn);
const source = entry === 'TypeScript' ? compile('frontend/shared/scroll.ts') : readFileSync('themes/chunnel/assets/js/chunnel-scroll.js', 'utf8');
function fixture() {
  const calls = [], listeners = {};
  const root = { scrollHeight: 3000, style: { overflowAnchor: '', scrollBehavior: '' } };
  const history = { scrollRestoration: 'manual', state: { other: 'retained' }, replaceState(state) { this.state = state; } };
  const window = {
    scrollY: 900, scrollX: 0, innerHeight: 800,
    scrollTo(position) { calls.push(position); this.scrollY = position.top; this.scrollX = position.left; },
    addEventListener(name, handler) { listeners[name] = handler; },
  };
  vm.runInNewContext(source, { window, history, document: { documentElement: root } });
  return { window, history, root, calls, listeners, scroll: window.ChunnelScroll };
}

test('local mutations correct geometry synchronously and release the anchoring override', () => {
  const f = fixture();
  const value = f.scroll.keep(() => {
    assert.equal(f.root.style.overflowAnchor, 'none');
    f.window.scrollY = 0;
    return 'committed';
  });
  assert.equal(value, 'committed');
  assert.equal(f.window.scrollY, 900);
  assert.equal(f.calls.length, 1);
  assert.equal(f.calls[0].behavior, 'instant');
  assert.equal(f.root.style.overflowAnchor, '');
  assert.equal(f.root.style.scrollBehavior, '');
  assert.equal(f.history.scrollRestoration, 'auto');
});

test('stable content does not trigger any scrolling', () => {
  const f = fixture();
  f.scroll.keep(() => {});
  assert.equal(f.calls.length, 0);
});

test('a shorter destination clamps to its actual scrollable range once', () => {
  const f = fixture();
  f.scroll.keep(() => { f.root.scrollHeight = 1100; });
  assert.equal(f.window.scrollY, 300);
  assert.equal(f.calls.length, 1);
});

test('history restoration handles zero and leaves other history state intact', () => {
  const f = fixture();
  f.scroll.keep(() => {}, { top: 0 });
  assert.equal(f.window.scrollY, 0);
  f.listeners.pagehide();
  assert.equal(f.history.state.chunnelScroll, 0);
  assert.equal(f.history.state.other, 'retained');
});

test('failed mutations release temporary styles without scheduling a later scroll', () => {
  const f = fixture();
  f.root.style.overflowAnchor = 'auto';
  assert.throws(() => f.scroll.keep(() => { throw new Error('render failed'); }), /render failed/);
  assert.equal(f.root.style.overflowAnchor, 'auto');
  assert.equal(f.calls.length, 0);
});
}
