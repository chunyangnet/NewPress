const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');

class Element {
  constructor(tagName = 'div', namespaceURI = null) {
    this.tagName = tagName.toUpperCase(); this.namespaceURI = namespaceURI;
    this.children = []; this.attributes = new Map(); this.listeners = new Map(); this.dataset = {};
    const classes = new Set();
    this.classList = {
      add: (...names) => names.forEach(name => classes.add(name)),
      remove: (...names) => names.forEach(name => classes.delete(name)),
      contains: name => classes.has(name),
      toggle(name, enabled = !classes.has(name)) { enabled ? classes.add(name) : classes.delete(name); return enabled; },
    };
  }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  removeAttribute(name) { this.attributes.delete(name); }
  addEventListener(type, callback) { const callbacks = this.listeners.get(type) || []; callbacks.push(callback); this.listeners.set(type, callbacks); }
  removeEventListener(type, callback) { this.listeners.set(type, (this.listeners.get(type) || []).filter(item => item !== callback)); }
  async emit(type, values = {}) {
    const event = {type, target: this, currentTarget: this, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, ...values};
    await Promise.all((this.listeners.get(type) || []).map(callback => callback(event)));
    return event;
  }
  querySelector() { return null; }
  querySelectorAll() { return []; }
  closest() { return null; }
  append(...children) { children.forEach(child => { child.parent = this; this.children.push(child); }); }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter(child => child !== this); }
  get textContent() { return (this.text || '') + this.children.map(child => child.textContent).join(''); }
  set textContent(value) { this.text = String(value); this.children = []; }
}

function fixture({reducedMotion = false, toastStack = true} = {}) {
  const document = new Element(), window = new Element(), stack = new Element();
  const timers = new Map(), frames = new Map(); let nextID = 0;
  document.body = new Element('body');
  document.querySelector = selector => toastStack && selector === '[data-admin-toast-stack]' ? stack : null;
  document.createElement = tag => new Element(tag);
  document.createElementNS = (namespace, tag) => new Element(tag, namespace);
  window.matchMedia = query => Object.assign(new Element(), {matches: query === '(prefers-reduced-motion: reduce)' && reducedMotion});
  window.setTimeout = (callback, delay) => { const id = ++nextID; timers.set(id, {callback, delay}); return id; };
  window.clearTimeout = id => timers.delete(id);
  const context = vm.createContext({
    document, window, Element, HTMLElement: Element, HTMLFormElement: class extends Element {},
    queueMicrotask,
    requestAnimationFrame(callback) { const id = ++nextID; frames.set(id, callback); return id; },
    fetch() { assert.fail('a page without editor or menu components must not make requests'); },
  });
  return {
    document, window, stack, timers, frames,
    load(entry, variant) {
      const typescript = ['chunnel-admin', 'chunnel-admin-lists', 'chunnel-admin-comments', 'chunnel-admin-settings', 'chunnel-admin-media-upload', 'chunnel-admin-login', 'chunnel-admin-menu'].includes(entry);
      const filename = typescript && variant === 'source' ? `frontend/core/${entry.replace('chunnel-', '')}.ts`
        : path.join(__dirname, '../../internal/coreui/assets/js', `${entry}${variant === 'source' ? '.source' : ''}.js`);
      new vm.Script(typescript && variant === 'source' ? compile(filename) : fs.readFileSync(filename, 'utf8'), {filename}).runInContext(context);
    },
    renderFrame() { const callbacks = [...frames.values()]; frames.clear(); callbacks.forEach(callback => callback()); },
    runTimers() { const callbacks = [...timers.values()]; timers.clear(); callbacks.forEach(({callback}) => callback()); },
  };
}

for (const variant of ['source', 'built']) {
  test(`${variant}: complete admin bundles load without editor or menu DOM and leave no background work`, async () => {
    const f = fixture();
    for (const entry of ['chunnel-admin', 'chunnel-admin-lists', 'chunnel-admin-comments', 'chunnel-admin-settings', 'chunnel-admin-media-upload', 'chunnel-admin-login', 'chunnel-admin-editor', 'chunnel-admin-menu']) f.load(entry, variant);
    assert.equal(typeof f.window.ChunnelAdmin.icon, 'function');
    assert.equal(typeof f.window.ChunnelAdmin.showToast, 'function');
    await f.document.emit('chunnel:admin-updated');
    await f.document.emit('chunnel:admin-before-replace', {detail: {regions: []}});
    await f.document.emit('click', {target: f.document.body});
    await f.document.emit('keydown', {key: 'Escape'});
    await f.window.emit('pageshow', {persisted: true});
    await f.window.emit('pagehide');
    assert.equal(f.timers.size, 0);
    assert.equal(f.frames.size, 0);
    assert.equal(f.stack.children.length, 0);
  });

  for (const reducedMotion of [false, true]) {
    test(`${variant}: shell helpers render safe feedback and finish toast cleanup (reduced motion: ${reducedMotion})`, async () => {
      const f = fixture({reducedMotion}); f.load('chunnel-admin', variant);
      const helpers = f.window.ChunnelAdmin;
      const icon = helpers.icon('check-circle');
      assert.equal(icon.namespaceURI, 'http://www.w3.org/2000/svg');
      assert.equal(icon.getAttribute('aria-hidden'), 'true');
      assert.equal(icon.children[0].getAttribute('href'), '#icon-check-circle');
      const message = '<img src=x onerror=alert(1)> 保存失败';
      helpers.showToast(message, 'error');
      assert.equal(f.stack.children.length, 1);
      const toast = f.stack.children[0];
      assert.ok(toast.textContent.includes(message));
      const descendants = element => [element, ...element.children.flatMap(descendants)];
      assert.equal(descendants(toast).some(element => element.tagName === 'IMG'), false);
      f.renderFrame();
      assert.equal(toast.classList.contains('is-visible'), true);
      f.runTimers();
      assert.equal(toast.classList.contains('is-visible'), false);
      assert.equal(f.timers.size, 1);
      assert.equal([...f.timers.values()][0].delay === 0, reducedMotion);
      f.runTimers();
      assert.equal(f.stack.children.length, 0);
      assert.equal(f.timers.size, 0);
    });
  }

  test(`${variant}: shell feedback is optional when its outlet is absent`, () => {
    const f = fixture({toastStack: false}); f.load('chunnel-admin', variant);
    f.window.ChunnelAdmin.showToast('保存完成', 'success');
    assert.equal(f.timers.size, 0);
    assert.equal(f.frames.size, 0);
  });
}
