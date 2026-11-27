const nodeTest = require('node:test');
const {compile} = require('./helpers/typescript.cjs');
for (const variant of ['typescript','generated']) {
const test = (name, fn) => nodeTest(variant + ': ' + name, fn);
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = variant === 'typescript' ? compile('frontend/themes/chunnel/home-blocks.ts') : fs.readFileSync(path.join(__dirname, '../../themes/chunnel/assets/js/chunnel-home-blocks.js'), 'utf8');
const flush = () => new Promise(resolve => setImmediate(resolve));

function fixture({ effect = 'fade', autoplay = false, autohide = true, delayed = -1 } = {}) {
  let now = 0, sequence = 0, timeoutCalls = 0, controlsWrites = 0, intersect;
  const timers = new Map();
  class Element {
    constructor(children = []) {
      this.children = children; this.dataset = {}; this.attributes = new Map(); this.listeners = new Map();
      this.style = { setProperty() {} }; this.hidden = false;
    }
    contains(node) { return node === this || this.children.some(child => child.contains?.(node)); }
    setAttribute(name, value) { this.attributes.set(name, String(value)); }
    getAttribute(name) { return this.attributes.get(name); }
    addEventListener(name, fn) { this.listeners.set(name, [...(this.listeners.get(name) || []), fn]); }
    emit(name, detail = {}) {
      const event = { target: this, type: name, preventDefault() {}, stopPropagation() {}, ...detail };
      this.listeners.get(name)?.forEach(fn => fn(event));
    }
    matches(selector) { return selector === ':focus-within' && this.contains(document.activeElement); }
    focus() { focus(this); }
    prepend(...nodes) { this.children.unshift(...nodes); }
  }
  const images = Array.from({ length: 4 }, (_, i) => {
    let resolve;
    const ready = i === delayed ? new Promise(done => { resolve = done; }) : Promise.resolve();
    return { loading: 'lazy', decode: () => ready, resolve: () => resolve?.() };
  });
  const links = images.map(() => new Element());
  const slides = images.map((image, i) => {
    const slide = new Element([links[i]]);
    slide.querySelector = () => image;
    return slide;
  });
  const track = new Element(slides), dots = images.map(() => new Element());
  const prev = new Element(), next = new Element(), status = new Element();
  const controls = new Element([prev, next, status, ...dots]);
  const root = new Element([track, controls]);
  root.dataset = new Proxy({ carouselEffect: effect, autoplay: String(autoplay), autohideControls: String(autohide), interval: '2' }, {
    set(target, name, value) { if (name === 'controlsHidden') controlsWrites++; target[name] = value; return true; },
  });
  root.querySelector = selector => ({ '[data-carousel-track]': track, '[data-carousel-controls]': controls,
    '[data-carousel-status]': status, '[data-carousel-prev]': prev, '[data-carousel-next]': next })[selector];
  root.querySelectorAll = selector => selector === '[data-carousel-slide]' ? slides : dots;
  const body = new Element();
  body.quiet = false;
  body.matches = () => body.quiet;
  const document = new Element();
  document.body = body; document.activeElement = body; document.hidden = false;
  document.querySelectorAll = selector => selector === '[data-home-carousel]' ? [root] : [];
  document.createElement = () => new Element();
  const media = new Element(), connection = new Element();
  media.matches = false; connection.saveData = false;
  function focus(node) {
    const hadFocus = root.contains(document.activeElement);
    document.activeElement = node;
    if (hadFocus) root.emit('focusout');
    if (root.contains(node)) root.emit('focusin');
  }
  vm.runInNewContext(source, {
    document, Node: Element, navigator: { connection }, matchMedia: () => media, performance: { now: () => now },
    setTimeout(fn, delay = 0) { timeoutCalls++; timers.set(++sequence, { at: now + delay, fn }); return sequence; },
    clearTimeout: id => timers.delete(id),
    IntersectionObserver: class { constructor(fn) { intersect = fn; } observe() {} },
  });
  const visibility = value => { document.hidden = !value; document.emit('visibilitychange'); };
  const inViewport = value => intersect([{ intersectionRatio: value ? 1 : 0 }]);
  inViewport(true);
  return {
    root, track, slides, dots, links, document, body, images, media, connection, next, prev, focus, visibility, inViewport,
    async advance(ms) {
      const until = now + ms;
      for (;;) {
        const entry = [...timers].filter(([, t]) => t.at <= until).sort((a, b) => a[1].at - b[1].at)[0];
        if (!entry) break;
        const [id, timer] = entry;
        now = timer.at; timers.delete(id); timer.fn(); await flush();
      }
      now = until; await flush();
    },
    current: () => dots.findIndex(dot => dot.getAttribute('aria-current') === 'true'),
    moving: () => Object.hasOwn(track.dataset, 'carouselTransition'),
    end(i, name, pseudoElement = '') { slides[i].emit('animationend', { animationName: name, pseudoElement }); },
    stats: () => ({ timeoutCalls, controlsWrites, pendingTimers: timers.size }),
  };
}

// Browsers deliver separate outgoing and incoming events with microtasks between them.
for (const [effect, name] of [['rotate-3d', 'rotate'], ['stack', 'stack'], ['accordion', 'accordion']]) {
  for (const outgoingFirst of [true, false]) {
    for (const destination of [0, 2]) {
      test(`${effect}: queued move to ${destination} survives ${outgoingFirst ? 'outgoing' : 'incoming'} end first`, async () => {
        const f = fixture({ effect });
        f.next.emit('click'); await flush();
        await f.advance(100);
        f.dots[destination].emit('click'); await flush();
        await f.advance(450);
        const events = [[0, `chunnel-carousel-${name}-out`], [1, `chunnel-carousel-${name}-in`]];
        if (!outgoingFirst) events.reverse();
        for (const event of events) { f.end(...event); await flush(); }
        assert.equal(f.current(), destination);
        assert.equal(f.moving(), true, 'the queued animation must remain active after both old end events');
        await f.advance(400);
        assert.equal(f.moving(), true);
        f.end(destination, `chunnel-carousel-${name}-in`);
        assert.equal(f.moving(), false);
      });
    }
  }
}

for (const [effect, name, completion] of [['fade', 'fade-in', 1], ['rotate-3d', 'rotate-in', 1], ['stack', 'stack-in', 1], ['accordion', 'accordion-in', 1], ['page-turn', 'page-out', 0]]) {
  test(`${effect}: only its completion animation can settle the transition`, async () => {
    const f = fixture({ effect });
    f.next.emit('click'); await flush();
    f.end(completion, `chunnel-carousel-${name}`, '::before');
    f.end(completion, 'chunnel-carousel-page-shadow');
    assert.equal(f.moving(), true);
    f.end(completion, `chunnel-carousel-${name}`);
    assert.equal(f.moving(), false);
    f.next.emit('click'); await flush();
    await f.advance(750);
    assert.equal(f.moving(), false, 'missing animation events must still recover');
  });
}

const pauses = {
  hover: (f, paused) => f.root.emit(paused ? 'pointerenter' : 'pointerleave', { pointerType: 'mouse' }),
  focus: (f, paused) => f.focus(paused ? f.links[0] : f.body),
  pointer: (f, paused) => paused
    ? f.root.emit('pointerdown', { isPrimary: true, button: 0, pointerId: 1, clientX: 50, clientY: 50 })
    : f.document.emit('pointerup', { pointerId: 1, clientX: 50, clientY: 50 }),
  hidden: (f, paused) => f.visibility(!paused),
  outside: (f, paused) => f.inViewport(!paused),
  reduced: (f, paused) => { f.media.matches = paused; f.media.emit('change'); },
  saveData: (f, paused) => { f.connection.saveData = paused; f.connection.emit('change'); },
};

for (const [reason, pause] of Object.entries(pauses)) {
  test(`autoplay waiting for an image respects ${reason} and resumes after a full interval`, async () => {
    const f = fixture({ autoplay: true, delayed: 1 });
    await f.advance(2000);
    pause(f, true);
    f.images[1].resolve(); await flush();
    assert.equal(f.current(), 0);
    if (reason === 'focus') assert.equal(f.document.activeElement, f.links[0]);
    pause(f, false); await f.advance(0);
    await f.advance(1999);
    assert.equal(f.current(), 0);
    await f.advance(1);
    assert.equal(f.current(), 1);
  });
}

test('a brief pause invalidates the old autoplay request even if decode finishes after resume', async () => {
  const f = fixture({ autoplay: true, delayed: 1 });
  await f.advance(2000);
  pauses.hover(f, true);
  await f.advance(100);
  pauses.hover(f, false);
  f.images[1].resolve(); await flush();
  assert.equal(f.current(), 0);
  await f.advance(1999);
  assert.equal(f.current(), 0);
  await f.advance(1);
  assert.equal(f.current(), 1);
});

test('decode completion rechecks motion preferences even without a media event', async () => {
  const f = fixture({ autoplay: true, delayed: 1 });
  await f.advance(2000);
  f.body.quiet = true;
  f.images[1].resolve(); await flush();
  assert.equal(f.current(), 0);
  assert.equal(f.root.dataset.autoplayRunning, 'false');
});

test('manual navigation supersedes a pending automatic destination and remains available during pause', async () => {
  const f = fixture({ autoplay: true, delayed: 1 });
  await f.advance(2000);
  pauses.hover(f, true);
  f.dots[2].emit('click'); await flush();
  assert.equal(f.current(), 2);
  f.images[1].resolve(); await flush();
  assert.equal(f.current(), 2);
});

test('pausing autoplay preserves an explicit manual choice waiting for its image', async () => {
  const f = fixture({ autoplay: true, delayed: 1 });
  f.dots[1].emit('click'); await flush();
  pauses.hover(f, true);
  f.images[1].resolve(); await flush();
  assert.equal(f.current(), 1);
});

test('visibility resume restarts controls hiding with a full idle interval', async () => {
  const f = fixture();
  await f.advance(1000);
  f.visibility(false);
  await f.advance(5000);
  f.visibility(true);
  await f.advance(2599);
  assert.equal(f.root.dataset.controlsHidden, 'false');
  await f.advance(1);
  assert.equal(f.root.dataset.controlsHidden, 'true');
});

test('frequent pointer movement keeps controls visible without repeated timers or DOM writes', async () => {
  const f = fixture();
  await f.advance(1000);
  const before = f.stats();
  for (let i = 0; i < 1000; i++) f.root.emit('pointermove', { isPrimary: true });
  assert.deepEqual(f.stats(), before);
  await f.advance(2599);
  assert.equal(f.root.dataset.controlsHidden, 'false');
  await f.advance(1);
  assert.equal(f.root.dataset.controlsHidden, 'true');
  assert.equal(f.stats().timeoutCalls, 2, 'one initial timer and one extension are sufficient');
});

test('focused or pressed controls remain visible after resuming the page', async () => {
  for (const reason of ['focus', 'pointer']) {
    const f = fixture();
    f.visibility(false); f.visibility(true);
    pauses[reason](f, true);
    await f.advance(5000);
    assert.equal(f.root.dataset.controlsHidden, 'false');
    pauses[reason](f, false); await f.advance(0);
    await f.advance(2600);
    assert.equal(f.root.dataset.controlsHidden, 'true');
  }
});

test('disabling auto-hide avoids idle timers across interactions and visibility changes', async () => {
  const f = fixture({ autohide: false });
  f.root.emit('pointermove', { isPrimary: true });
  f.visibility(false); f.visibility(true);
  await f.advance(10000);
  assert.equal(f.root.dataset.controlsHidden, undefined);
  assert.equal(f.stats().timeoutCalls, 0);
});

}
