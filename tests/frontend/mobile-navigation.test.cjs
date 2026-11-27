const nodeTest = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const { compile } = require('./helpers/typescript.cjs');
for (const entry of ['TypeScript', 'generated']) {
const test = (name, fn) => nodeTest(entry + ': ' + name, fn);
const source = entry === 'TypeScript' ? compile('frontend/shared/mobile-nav.ts') : fs.readFileSync(process.env.CHUNNEL_NAV_SOURCE || 'themes/chunnel/assets/js/chunnel-shell-glass.js', 'utf8');

// Run the actual pointer controller with deterministic geometry and event propagation.
// Browser checks separately exercise native scrolling, click synthesis and CSS materials.
function fixture({ glass = true, reduced = false, minimal = false, gestureDisabled = false, plus = false, address = 'https://example.test/page-0' } = {}) {
  const callbacks = new Map(); let nextFrame = 0, resize, reads = 0, widthWrites = 0;
  const location = new URL(address);
  class Element {
    constructor(tag = 'div', rect = { left: 12, right: 378, top: 760, bottom: 824, width: 366 }) {
      this.tag = tag; this.rect = rect; this.listeners = new Map();
      this.style = new Proxy({}, { set(target, key, value) { if (key === 'width') widthWrites++; target[key] = value; return true; } });
      this.classes = new Set(); this.attributes = new Map(); this.children = []; this.captures = new Set(); this.clicks = 0;
      this.classList = { add: (...keys) => keys.forEach(k => this.classes.add(k)), remove: (...keys) => keys.forEach(k => this.classes.delete(k)),
        toggle: (k, on) => on ? this.classes.add(k) : this.classes.delete(k), contains: k => this.classes.has(k) };
    }
    addEventListener(type, fn) { this.listeners.set(type, [...(this.listeners.get(type) || []), fn]); }
    emit(type, event) { for (const fn of this.listeners.get(type) || []) { fn(event); if (event.stopped) break; } }
    getBoundingClientRect() { reads++; return this.rect; }
    get clientLeft() { reads++; return 1; }
    setAttribute(k, v) { this.attributes.set(k, v); }
    get href() { return this.tag === 'a' ? new URL(this.attributes.get('href') || '', location.href).href : undefined; }
    get host() { return this.href ? new URL(this.href).host : undefined; }
    matches(selector) {
      if (selector === '[target=_blank],[download]') return this.attributes.get('target') === '_blank' || this.attributes.has('download');
      if (selector.includes('a[')) return this.tag === 'a' && this.attributes.has('href') && this.attributes.get('target') !== '_blank' && !this.attributes.has('download');
      return this.attributes.get('aria-current') === 'page';
    }
    closest(selector) { return selector === 'a, button' && ['a', 'button'].includes(this.tag) ? this : null; }
    contains(el) { return this === el || this.children.includes(el); }
    prepend(el) { this.children.unshift(el); }
    hasPointerCapture(id) { return this.captures.has(id); }
    setPointerCapture(id) { this.captures.add(id); }
    releasePointerCapture(id) { this.captures.delete(id); this.emit('lostpointercapture', { pointerId: id, target: this }); }
    click() { const event = dispatch('click', { target: this, detail: 0 }); if (!event.defaultPrevented) this.clicks++; }
    focus() { document.activeElement = this; }
  }
  const bar = new Element(), document = new Element(), window = new Element(), media = new Element();
  media.matches = reduced; document.body = { matches: selector => selector === ".theme-mobile-gesture-off" ? gestureDisabled : minimal }; window.PointerEvent = class {};
  const items = Array.from({ length: 5 }, (_, i) => new Element(i === 4 ? 'button' : 'a', { left: 20 + i * 70, right: 88 + i * 70, top: 767, bottom: 817, width: 68 }));
  items.filter(item => item.tag === 'a').forEach((item, i) => item.setAttribute('href', '/page-' + i));
  if (plus) items[2].classes.add('mobile-create');
  items[0].setAttribute('aria-current', 'page'); bar.children = [...items]; bar.querySelectorAll = () => items;
  bar.querySelector = () => items.find(item => item.attributes.get('aria-current') === 'page') || null;
  document.documentElement = new Element();
  document.querySelector = selector => glass && selector === 'html[data-style="liquid-glass"] .mobile-bottom-nav' ? bar : null; document.createElement = tag => new Element(tag);
  function dispatch(type, extra = {}) {
    const event = { target: items[0], pointerId: 1, isPrimary: true, button: 0, buttons: 1, clientX: 54, clientY: 790,
      detail: 1, preventDefault() { this.defaultPrevented = true; }, stopImmediatePropagation() { this.stopped = true; }, ...extra };
    document.emit(type, event); if (!event.stopped) bar.emit(type, event); return event;
  }
  vm.runInNewContext(source, { document, window, Element, Node: Element, location, matchMedia: () => media,
    requestAnimationFrame: fn => { callbacks.set(++nextFrame, fn); return nextFrame; }, cancelAnimationFrame: id => callbacks.delete(id),
    ResizeObserver: class { constructor(fn) { resize = fn; } observe() {} },
  });
  const tick = () => { const queued = [...callbacks.values()]; callbacks.clear(); queued.forEach(fn => fn()); };
  return { bar, items, document, window, media, lens: bar.children[0], dispatch, tick, resize: () => resize(), reads: () => reads, widthWrites: () => widthWrites, queued: () => callbacks.size };
}

test('classic navigation is not enhanced', () => {
  const f = fixture({ glass: false }); assert.equal(f.bar.children.length, 5); assert.equal(f.bar.listeners.size, 0);
});

test('disabled gesture module leaves all native links usable', () => {
 const f=fixture({gestureDisabled:true}); assert.equal(f.bar.listeners.size,0);
});

test('central plus is independent and releasing a navigation drag above it does not activate', () => {
 const f=fixture({plus:true});
 f.dispatch('pointerdown',{target:f.items[2],clientX:194}); assert.ok(!f.bar.classes.has('is-pressing'));
 f.dispatch('pointerdown'); f.dispatch('pointermove',{clientX:194}); f.tick();
 assert.equal(f.lens.style.opacity,'0'); f.dispatch('pointerup',{clientX:194});
 assert.equal(f.items.reduce((n,item)=>n+item.clicks,0),0);
});

test('an active custom central link stays outside the navigation lens', () => {
 const f=fixture({plus:true});
 f.items[0].attributes.delete('aria-current'); f.items[2].setAttribute('aria-current','page');
 f.resize(); assert.equal(f.lens.style.opacity,'0');
 f.dispatch('pointerdown',{target:f.items[2],clientX:194});
 assert.ok(!f.bar.classes.has('is-pressing'));
 assert.equal(f.dispatch('click',{target:f.items[2]}).defaultPrevented,undefined);
});

test('a tap retains the native click without synthesizing a second one', () => {
  const f = fixture(); f.dispatch('pointerdown'); assert.ok(f.bar.classes.has('is-pressing'));
  f.dispatch('pointerup'); assert.equal(f.items[0].clicks, 0);
  assert.equal(f.dispatch('click').defaultPrevented, undefined); assert.ok(!f.bar.classes.has('is-pressing'));
});

test('drag previews a destination and activates only on release; its native follow-up click is consumed', () => {
  const f = fixture(); f.dispatch('pointerdown'); f.dispatch('pointermove', { clientX: 194 }); f.tick();
  assert.ok(f.items[2].classes.has('is-target')); assert.equal(f.items[2].clicks, 0); assert.ok(f.bar.hasPointerCapture(1));
  f.dispatch('pointerup', { clientX: 194 }); assert.equal(f.items[2].clicks, 1); assert.equal(f.items[0].clicks, 0);
  assert.ok(f.dispatch('click', { target: f.bar }).defaultPrevented); assert.ok(!f.bar.hasPointerCapture(1));
});

test('pointer movement coalesces to one frame and does not read layout', () => {
  const f = fixture(); f.dispatch('pointerdown'); const reads = f.reads();
  for (let x = 70; x < 240; x++) f.dispatch('pointermove', { clientX: x });
  assert.equal(f.queued(), 1); f.tick(); assert.equal(f.reads(), reads); assert.ok(f.items[3].classes.has('is-target'));
});

test('press, move and release reuse measured geometry without resizing the lens', () => {
  const f = fixture(), reads = f.reads(), writes = f.widthWrites();
  f.dispatch('pointerdown');
  for (const clientX of [124, 194, 264]) { f.dispatch('pointermove', { clientX }); f.tick(); }
  f.dispatch('pointerup', { clientX: 264 });
  assert.equal(f.reads(), reads); assert.equal(f.widthWrites(), writes);
});

test('release keeps the lens on the destination until navigation instead of springing back', () => {
  const f = fixture(); f.dispatch('pointerdown'); f.dispatch('pointermove', { clientX: 194 }); f.tick();
  f.dispatch('pointerup', { clientX: 194 });
  assert.match(f.lens.style.transform, /translate3d\(147px,0px,0\)/);
  assert.ok(f.items[2].classes.has('is-target'));
  assert.equal(f.items[0].attributes.get('aria-current'), 'page');
  f.window.emit('pageshow', {});
  assert.match(f.lens.style.transform, /translate3d\(7px,0px,0\)/);
  assert.ok(!f.lens.classes.has('is-ready'));
});

test('an in-page account view refresh realigns the lens and cancels a pending drag', () => {
  const f=fixture();f.dispatch('pointerdown');f.dispatch('pointermove',{clientX:194});
  f.items[0].attributes.delete('aria-current');f.items[3].setAttribute('aria-current','page');
  f.bar.chunnelSelect();
  assert.equal(f.queued(),0);assert.ok(!f.bar.classes.has('is-pressing'));
  assert.match(f.lens.style.transform,/translate3d\(217px,0px,0\)/);f.dispatch('pointerup',{clientX:194});assert.equal(f.items[2].clicks,0);
  assert.ok(f.dispatch('click').defaultPrevented);
});

test('viewport changes refresh geometry before the next press', () => {
  const f = fixture();
  f.bar.rect = {...f.bar.rect, top: 660, bottom: 724};
  f.window.emit('resize', {});
  f.dispatch('pointerdown', {clientY: 690});
  f.dispatch('pointermove', {clientX: 194, clientY: 690}); f.tick();
  f.dispatch('pointerup', {clientX: 194, clientY: 690});
  assert.equal(f.items[2].clicks, 1);
});

test('unequal navigation widths change the transform without resizing the moving layer', () => {
  const f = fixture(); f.items[2].rect = {...f.items[2].rect, width: 80, right: 240}; f.resize();
  const writes = f.widthWrites();
  f.dispatch('pointerdown'); f.dispatch('pointermove', {clientX: 200}); f.tick(); f.dispatch('pointerup', {clientX: 200});
  assert.equal(f.widthWrites(), writes); assert.equal(f.lens.style.width, '68px');
  assert.match(f.lens.style.transform, /translate3d\(153px,0px,0\) scale\(1\.17647/);
});

test('new-window and download links retain the current-page highlight after activation', () => {
  for (const [key, value] of [['target', '_blank'], ['download', 'file']]) {
    const f = fixture(); f.items[2].setAttribute(key, value);
    f.dispatch('pointerdown'); f.dispatch('pointermove', {clientX: 194}); f.tick(); f.dispatch('pointerup', {clientX: 194});
    assert.equal(f.items[2].clicks, 1); assert.match(f.lens.style.transform, /translate3d\(7px,0px,0\)/);
  }
});

for (const href of ['#section', '/page-0#section', 'https://example.test/page-0#section', '/page-0']) {
  for (const drag of [false, true]) test(`same-document navigation: ${drag ? 'drag' : 'tap'} ${href} retains the current-page lens`, () => {
    const f = fixture({address: 'https://example.test/page-0#previous'});
    f.items[2].setAttribute('href', href);
    const reads = f.reads(), writes = f.widthWrites();
    f.dispatch('pointerdown', drag ? {} : {target: f.items[2], clientX: 194});
    if (drag) { f.dispatch('pointermove', {clientX: 194}); f.tick(); }
    f.dispatch('pointerup', {target: f.items[2], clientX: 194});
    assert.match(f.lens.style.transform, /translate3d\(7px,0px,0\)/);
    assert.equal(f.items[2].classes.has('is-target'), false);
    assert.equal(f.items[0].attributes.get('aria-current'), 'page');
    assert.equal(f.items[2].clicks, drag ? 1 : 0);
    assert.equal(!!f.dispatch('click', {target: f.items[2]}).defaultPrevented, drag);
    assert.equal(f.reads(), reads); assert.equal(f.widthWrites(), writes); assert.equal(f.queued(), 0);
  });
}

for (const href of ['/page-0?view=next#section', '/page-2#section', 'https://other.test/page-0#section', '/page-0%23section']) {
  test(`same-document navigation: ${href} still previews its new document until navigation`, () => {
    const f = fixture(); f.items[2].setAttribute('href', href);
    f.dispatch('pointerdown'); f.dispatch('pointermove', {clientX: 194}); f.tick(); f.dispatch('pointerup', {clientX: 194});
    assert.equal(f.items[2].clicks, 1); assert.match(f.lens.style.transform, /translate3d\(147px,0px,0\)/);
    assert.equal(f.items[2].classes.has('is-target'), true);
  });
}

test('same-document navigation: matching queries and encoded fragments retain the current page', () => {
  const f = fixture({address: 'https://example.test/page-0?view=one#previous'});
  f.items[2].setAttribute('href', '?view=one#中文');
  f.dispatch('pointerdown'); f.dispatch('pointermove', {clientX: 194}); f.tick(); f.dispatch('pointerup', {clientX: 194});
  assert.equal(f.items[2].clicks, 1); assert.match(f.lens.style.transform, /translate3d\(7px,0px,0\)/);
});

for (const href of ['mailto:reader@example.test', 'tel:+8613800000000']) {
  test(`non-document navigation: ${href} retains the current-page highlight`, () => {
    const f=fixture(); f.items[2].setAttribute('href',href);
    f.dispatch('pointerdown');f.dispatch('pointermove',{clientX:194});f.tick();f.dispatch('pointerup',{clientX:194});
    assert.equal(f.items[2].clicks,1);assert.match(f.lens.style.transform,/translate3d\(7px,0px,0\)/);
    assert.equal(f.items[2].classes.has('is-target'),false);
  });
}

test('transferring implicit touch capture from the link to the bar keeps the drag alive', () => {
  const f = fixture(); f.dispatch('pointerdown'); f.dispatch('pointermove', { clientX: 124 });
  f.dispatch('lostpointercapture', { target: f.items[0] }); f.tick(); assert.ok(f.bar.classes.has('is-dragging'));
  f.dispatch('pointermove', { clientX: 194 }); f.tick(); f.dispatch('pointerup', { clientX: 194 }); assert.equal(f.items[2].clicks, 1);
});

test('starting a drag near the edge of a tab never makes the lens jump backwards', () => {
  const f = fixture(); f.dispatch('pointerdown', { clientX: 22 });
  const x = () => Number(f.lens.style.transform.match(/translate3d\(([-\d.]+)px/)[1]);
  const before = x(); f.dispatch('pointermove', { clientX: 32 }); f.tick(); assert.ok(x() > before);
  f.dispatch('pointermove', { clientX: 377 }); f.tick(); assert.ok(x() + 68 < f.bar.rect.width + 3);
});

test('vertical intent leaves native scrolling available and cancels navigation', () => {
  const f = fixture(); f.dispatch('pointerdown');
  assert.equal(f.dispatch('pointermove', { clientY: 765 }).defaultPrevented, undefined);
  f.dispatch('pointerup', { clientY: 765 }); assert.ok(!f.bar.classes.has('is-pressing')); assert.ok(f.dispatch('click').defaultPrevented);
  assert.equal(f.items.reduce((n, item) => n + item.clicks, 0), 0);
});

test('releasing a horizontal drag outside the bar never follows a link', () => {
  const f = fixture(); f.dispatch('pointerdown'); f.dispatch('pointermove', { clientX: 194, clientY: 650 }); f.tick();
  f.dispatch('pointerup', { clientX: 194, clientY: 650 }); assert.equal(f.items[2].clicks, 0); assert.ok(f.dispatch('click').defaultPrevented);
});

test('the drag can leave the bar and return before a valid release', () => {
  const f = fixture(); f.dispatch('pointerdown'); f.dispatch('pointermove', { clientX: 194 }); f.tick();
  f.dispatch('pointermove', { clientX: 194, clientY: 650 }); f.tick(); assert.equal(f.lens.style.opacity, '0');
  f.dispatch('pointermove', { clientX: 264 }); f.tick(); f.dispatch('pointerup', { clientX: 264 }); assert.equal(f.items[3].clicks, 1);
});

for (const reason of ['pointercancel', 'lostpointercapture', 'contextmenu', 'resize', 'blur', 'visibilitychange', 'Escape', 'second touch']) {
  test(`${reason} clears an in-progress drag without activating a destination`, () => {
    const f = fixture(); f.dispatch('pointerdown'); f.dispatch('pointermove', { clientX: 194 });
    if (reason === 'resize') f.resize();
    else if (reason === 'blur') f.window.emit('blur', {});
    else if (reason === 'Escape') f.dispatch('keydown', { key: 'Escape' });
    else if (reason === 'second touch') f.dispatch('pointerdown', { pointerId: 2, isPrimary: false, target: f.document });
    else f.dispatch(reason, { target: f.bar });
    f.tick(); f.dispatch('pointerup', { clientX: 194 });
    assert.equal(f.items[2].clicks, 0); assert.ok(!f.bar.classes.has('is-pressing')); assert.equal(f.queued(), 0);
  });
}

test('cancellation cannot swallow a later keyboard activation or a fresh tap', () => {
  const f = fixture(); f.dispatch('pointerdown'); f.dispatch('pointercancel');
  assert.equal(f.dispatch('click', { detail: 0 }).defaultPrevented, undefined);
  f.dispatch('pointerdown', { pointerId: 2 }); f.dispatch('pointerup', { pointerId: 2 });
  assert.equal(f.dispatch('click', { pointerId: 2 }).defaultPrevented, undefined);
});

test('cancelling before a scheduled frame does not stall the next drag', () => {
  const f = fixture(); f.dispatch('pointerdown'); f.dispatch('pointermove', { clientX: 194 }); f.dispatch('pointercancel');
  f.dispatch('pointerdown', { pointerId: 2 }); f.dispatch('pointermove', { pointerId: 2, clientX: 264 }); f.tick();
  assert.ok(f.items[3].classes.has('is-target')); f.dispatch('pointerup', { pointerId: 2, clientX: 264 }); assert.equal(f.items[3].clicks, 1);
});

test('leaving before horizontal intent clears the pressed state', () => {
  const f = fixture(); f.dispatch('pointerdown'); f.dispatch('pointerleave');
  assert.ok(!f.bar.classes.has('is-pressing')); f.dispatch('pointerup'); assert.equal(f.items[0].clicks, 0);
});

test('modified clicks and non-primary mouse buttons keep native browser behavior', () => {
  for (const extra of [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }, { button: 2 }]) {
    const f = fixture(); f.dispatch('pointerdown', extra); assert.ok(!f.bar.classes.has('is-pressing'));
    assert.equal(f.dispatch('click', extra).defaultPrevented, undefined);
  }
});

test('reduced motion keeps drag selection but removes elastic deformation', () => {
  for (const preference of [{ reduced: true }, { minimal: true }]) {
    const f = fixture(preference); f.dispatch('pointerdown'); f.dispatch('pointermove', { clientX: 184 }); f.tick();
    assert.match(f.lens.style.transform, /scale\(1,1\)/); f.dispatch('pointerup', { clientX: 184 }); assert.equal(f.items[2].clicks, 1);
  }
});

test('a button destination receives a single replay compatible with lazy loading', () => {
  const f = fixture(); f.dispatch('pointerdown'); f.dispatch('pointermove', { clientX: 334 }); f.tick();
  f.dispatch('pointerup', { clientX: 334 }); assert.equal(f.items[4].clicks, 1); assert.equal(f.document.activeElement, f.items[4]); assert.ok(f.dispatch('click').stopped);
});

}
