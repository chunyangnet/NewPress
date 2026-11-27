const nodeTest = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');
for (const entry of ['TypeScript', 'generated']) {
const test = (name, fn) => nodeTest(entry + ': ' + name, fn);
const source = entry === 'TypeScript' ? compile('frontend/shared/motion.ts') : fs.readFileSync('themes/chunnel/assets/js/chunnel-motion.js', 'utf8');

function fixture({ reduced = false, quiet = false } = {}) {
  let now = 0, sequence = 0, mutation;
  const frames = new Map(), events = new Map();
  const listen = (type, fn) => events.set(type, fn);
  const media = { matches: reduced, addEventListener: (_, fn) => events.set('preference', fn) };
  const document = { hidden: false, body: { matches: () => quiet }, addEventListener: listen };
  const window = { addEventListener: listen };
  vm.runInNewContext(source, {
    window, document, matchMedia: () => media, performance: { now: () => now },
    requestAnimationFrame: fn => { frames.set(++sequence, fn); return sequence; },
    cancelAnimationFrame: id => frames.delete(id),
    MutationObserver: class { constructor(fn) { mutation = fn; } observe() {} },
  });
  return {
    surface: window.ChunnelMotion.surface,
    element: () => ({ isConnected: true, style: { transform: 'rotate(0deg)', opacity: '.9' } }),
    tick(dt) { now += dt; const batch = [...frames.values()]; frames.clear(); batch.forEach(fn => fn(now)); },
    queued: () => frames.size,
    reduced() { media.matches = true; events.get('preference')(); },
    minimal() { quiet = true; mutation(); },
    hide() { document.hidden = true; events.get('visibilitychange')(); },
    pagehide() { events.get('pagehide')(); },
  };
}
const x = el => Number(el.style.transform.match(/translate3d\(([-\d.e]+)/)[1]);

test('critically damped surfaces converge without overshoot at 30, 60 and 120 Hz', async () => {
  for (const rate of [30, 60, 120]) {
    const f = fixture(), el = f.element();
    const animation = f.surface(el, { x: 100, opacity: 0 }, {}, { duration: 360 });
    let previous = 100;
    for (let i = 0; i < rate; i++) {
      f.tick(1000 / rate);
      assert.ok(x(el) <= previous + 1e-9 && x(el) >= 0);
      assert.ok(Number(el.style.opacity) >= 0 && Number(el.style.opacity) <= 1);
      previous = x(el);
    }
    await animation.finished;
    assert.equal(x(el), 0); assert.equal(f.queued(), 0);
    animation.cancel(); assert.equal(el.style.transform, 'rotate(0deg)'); assert.equal(el.style.opacity, '.9');
  }
});

test('lively surfaces have bounded light rebound and stop scheduling frames', async () => {
  const f = fixture(), el = f.element();
  const animation = f.surface(el, { x: 100 }, {}, { lively: true });
  let minimum = 100;
  for (let i = 0; i < 60; i++) { f.tick(16); minimum = Math.min(minimum, x(el)); }
  await animation.finished;
  assert.ok(minimum < 0 && minimum > -5);
  assert.equal(x(el), 0); assert.equal(f.queued(), 0);
});

test('retargeting keeps position and velocity, and obsolete cancellation cannot reset the new spring', async () => {
  const f = fixture(), el = f.element();
  const opening = f.surface(el, { x: 100, opacity: 0 }, {});
  f.tick(45); const before = x(el);
  const closing = f.surface(el, {}, { x: 100, opacity: 0 });
  assert.equal(x(el), before);
  opening.cancel(); assert.equal(x(el), before);
  f.tick(1); assert.ok(x(el) < before, 'existing velocity must survive the reversal');
  for (let i = 0; i < 60; i++) f.tick(16);
  await Promise.all([opening.finished, closing.finished]);
  assert.equal(x(el), 100); assert.equal(f.queued(), 0);
  closing.cancel(); assert.equal(el.style.transform, 'rotate(0deg)');
});

test('frame rate does not change the physical position at the same elapsed time', () => {
  const values = [1, 2, 4, 8].map(parts => {
    const f = fixture(), el = f.element(); f.surface(el, { x: 100 }, {});
    for (let i = 0; i < parts; i++) f.tick(80 / parts);
    return x(el);
  });
  values.forEach(value => assert.ok(Math.abs(value - values[0]) < 1e-8));
});

test('OS and site reduced motion finish immediately without a frame loop', async () => {
  for (const options of [{ reduced: true }, { quiet: true }]) {
    const f = fixture(options), el = f.element();
    const animation = f.surface(el, { x: 100, opacity: 0 }, {});
    await animation.finished;
    assert.equal(x(el), 0); assert.equal(el.style.opacity, '1'); assert.equal(f.queued(), 0);
  }
});

test('changing motion preference or leaving the page settles all active surfaces', async () => {
  for (const action of ['reduced', 'minimal', 'hide', 'pagehide']) {
    const f = fixture(), first = f.element(), second = f.element();
    const a = f.surface(first, { x: 100 }, {}), b = f.surface(second, { y: 20 }, {});
    f.tick(32); f[action]();
    await Promise.all([a.finished, b.finished]);
    assert.equal(x(first), 0); assert.equal(f.queued(), 0);
  }
});

test('cancel restores caller styles and resolves the lifecycle without leaked frames', async () => {
  const f = fixture(), el = f.element();
  const animation = f.surface(el, { x: 100, opacity: 0 }, {});
  f.tick(40); animation.cancel(); await animation.finished;
  assert.equal(el.style.transform, 'rotate(0deg)'); assert.equal(el.style.opacity, '.9'); assert.equal(f.queued(), 0);
});

test('detached surfaces and delayed frames settle safely', async () => {
  for (const detach of [false, true]) {
    const f = fixture(), el = f.element(); const animation = f.surface(el, { x: 100 }, {});
    if (detach) el.isConnected = false;
    f.tick(10000); await animation.finished;
    assert.equal(x(el), 0); assert.equal(f.queued(), 0);
  }
});

test('generated CSS curves use monotonic critical damping and bounded lively damping', async () => {
  const { springCurve } = await import('../../scripts/motion.mjs');
  for (const damping of [1, .82]) {
    const values = springCurve(damping).split(',').map(Number);
    assert.equal(values[0], 0); assert.equal(values.at(-1), 1);
    assert.ok(values.every(value => Number.isFinite(value) && value >= 0 && value < 1.05));
    if (damping === 1) assert.ok(values.every((value, i) => !i || value >= values[i - 1]));
    else assert.ok(values.some(value => value > 1));
  }
});

}
