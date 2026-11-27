'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { compile, loadModule } = require('./helpers/typescript.cjs');

class Element {
  constructor() {
    this.listeners = new Map(); this.selectors = new Map(); this.groups = new Map(); this.attributes = new Map();
    this.dataset = {}; this.value = ''; this.checked = false; this.disabled = false; this.children = [];
    const classes = new Set();
    this.classList = { add: name => classes.add(name), remove: name => classes.delete(name), contains: name => classes.has(name), toggle: (name, on) => on ? classes.add(name) : classes.delete(name) };
  }
  addEventListener(name, fn) { this.listeners.set(name, [...(this.listeners.get(name) || []), fn]); }
  removeEventListener(name, fn) { this.listeners.set(name, (this.listeners.get(name) || []).filter(item => item !== fn)); }
  emit(type, values = {}) {
    const event = { type, target: this, currentTarget: this, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, ...values };
    for (const fn of this.listeners.get(type) || []) fn(event);
    return event;
  }
  querySelector(selector) { return this.selectors.get(selector) || null; }
  querySelectorAll(selector) { return this.groups.get(selector) || []; }
  closest() { return this.row || null; }
  setAttribute(name, value) { this.attributes.set(name, value); }
  removeAttribute(name) { this.attributes.delete(name); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  prepend(child) { child.parent = this; this.children.unshift(child); }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter(child => child !== this); }
}

function environment() {
  const document = new Element(), window = new Element(), timers = new Map(), toasts = [];
  let sequence = 0;
  document.createElement = () => new Element();
  window.confirm = () => true;
  window.ChunnelAdmin = { showToast: (...args) => toasts.push(args) };
  const context = { document, window, queueMicrotask,
    setTimeout(fn) { const id = ++sequence; timers.set(id, fn); return id; }, clearTimeout: id => timers.delete(id) };
  return { document, window, context, toasts, timers,
    flush() { const callbacks = [...timers.values()]; timers.clear(); callbacks.forEach(fn => fn()); } };
}

test('native form locking preserves submitter serialization, cancellation and disposal', async () => {
  const f = environment(), form = new Element(); let renders = 0, resets = 0;
  const { bindNativeSubmission } = loadModule('frontend/core/admin-native-submit.ts', f.context);
  const control = bindNativeSubmission(form, { onPending() { renders++; }, onRestore() { resets++; } });
  const cancelled = form.emit('submit'); cancelled.preventDefault(); await Promise.resolve(); f.flush();
  assert.equal(control.pending(), false); assert.equal(renders, 0);
  form.emit('submit'); assert.equal(form.emit('submit').defaultPrevented, true);
  await Promise.resolve(); assert.equal(control.pending(), true); assert.equal(renders, 0, 'leave successful submitter controls intact until serialization');
  f.window.emit('pageshow', { persisted: true }); f.flush(); assert.equal(renders, 0); assert.equal(resets, 1);
  form.emit('submit'); control.dispose(); await Promise.resolve(); f.flush();
  assert.equal(control.pending(), false); assert.equal(renders, 0);
  assert.equal(form.listeners.get('submit').length, 0); assert.equal(f.window.listeners.get('pageshow').length, 0);
});

for (const variant of ['TypeScript', 'generated']) {
  const source = name => variant === 'TypeScript' ? compile(`frontend/core/${name}.ts`) : fs.readFileSync(`internal/coreui/assets/js/chunnel-${name}.js`, 'utf8');
  for (const kind of ['content', 'comment']) {
    test(`${variant}: ${kind} bulk selection survives cancellation and bfcache without duplicate submit or stale controllers`, async () => {
      const f = environment(), form = new Element(), all = new Element(), action = new Element(), submit = new Element(), count = new Element(), live = new Element();
      const selections = [new Element(), new Element()]; selections.forEach(item => { item.row = new Element(); });
      f.document.selectors.set(`[data-${kind}-bulk]`, form);
      (kind === 'content' ? form : f.document).groups.set(`[data-${kind}-select]`, selections);
      for (const [key, item] of Object.entries({ 'select-all': all, 'bulk-action': action, 'bulk-submit': submit, 'selected-count': count, 'bulk-live': live })) form.selectors.set(`[data-${kind}-${key}]`, item);
      vm.runInNewContext(source('admin-lists'), f.context);
      assert.equal(submit.disabled, true); assert.equal(form.emit('submit').defaultPrevented, true); assert.equal(f.toasts.length, 1);
      action.value = kind === 'content' ? 'publish' : 'approved'; selections[0].checked = true; selections[0].emit('change');
      assert.equal(submit.disabled, false); assert.equal(all.indeterminate, true); assert.equal(count.textContent, '1');
      f.window.confirm = () => false; assert.equal(form.emit('submit').defaultPrevented, true); await Promise.resolve(); f.flush(); assert.equal(submit.disabled, false);
      f.window.confirm = () => true;
      const cancelled = form.emit('submit'); cancelled.preventDefault(); await Promise.resolve(); f.flush(); assert.equal(submit.disabled, false);
      form.emit('submit'); assert.equal(form.emit('submit').defaultPrevented, true); await Promise.resolve(); f.flush(); assert.equal(submit.disabled, true);
      selections[1].checked = true; selections[1].emit('change'); assert.equal(submit.disabled, true, 'selection changes must not unlock an accepted submission');
      f.window.emit('pageshow', { persisted: true }); assert.equal(submit.disabled, false); assert.equal(all.checked, true);
      f.document.emit('chunnel:admin-updated'); assert.equal(selections[0].listeners.get('change').length, 1);
      form.emit('submit');
      f.document.emit('chunnel:admin-before-replace', { detail: { regions: [{ contains: item => item === form }] } });
      await Promise.resolve(); f.flush(); assert.equal(submit.disabled, false); assert.equal(f.window.listeners.get('pageshow').length, 0);
      f.document.selectors.delete(`[data-${kind}-bulk]`); f.document.emit('chunnel:admin-updated');
    });
  }

  test(`${variant}: taxonomy slugs keep manual edits and confirmations remain cancellable after reinitialization`, () => {
    const f = environment(), form = new Element(), name = new Element(), slug = new Element();
    f.document.selectors.set('[data-taxonomy-form]', form); form.selectors.set('[data-term-name]', name); form.selectors.set('[data-term-slug]', slug);
    vm.runInNewContext(source('admin-lists'), f.context);
    name.value = 'Ａ  中文 / 分类'; name.emit('input'); assert.equal(slug.value, 'a-中文-分类');
    slug.value = 'manual'; slug.emit('input'); name.value = 'changed'; name.emit('input'); assert.equal(slug.value, 'manual');
    slug.value = ''; slug.emit('input'); name.emit('input'); assert.equal(slug.value, 'changed');
    f.document.emit('chunnel:admin-updated'); assert.equal(name.listeners.get('input').length, 1);
    f.window.confirm = () => false; assert.equal(form.emit('submit', { submitter: { dataset: { confirmAction: 'confirm' } } }).defaultPrevented, true);
  });

  test(`${variant}: media previews ignore stale reads, skip SVG/large files, and recover cancelled native uploads`, async () => {
    const f = environment(), form = new Element(), drop = new Element(), input = new Element(), title = new Element(), detail = new Element(), submit = new Element(), readers = [];
    f.document.selectors.set('[data-media-upload]', form);
    form.selectors.set('[data-media-dropzone]', drop); form.selectors.set('[data-media-file]', input); form.selectors.set('button[type="submit"]', submit);
    drop.selectors.set('[data-media-file-title]', title); drop.selectors.set('[data-media-file-detail]', detail);
    const query = drop.querySelector.bind(drop); drop.querySelector = s => s === '.admin-media-upload-preview' ? drop.children[0] || null : query(s);
    f.context.FileReader = class extends Element { constructor() { super(); readers.push(this); } readAsDataURL(file) { this.file = file; } };
    vm.runInNewContext(source('admin-media-upload'), f.context);
    const choose = (type, name, size = 64) => { input.files = [{ type, name, size }]; input.emit('change'); };
    choose('image/png', 'first.png'); choose('image/png', 'second.png');
    readers[0].result = 'data:image/png;base64,old'; readers[0].emit('load'); assert.equal(drop.children.length, 0);
    readers[1].result = 'data:image/png;base64,new'; readers[1].emit('load'); assert.equal(drop.children[0].src, readers[1].result);
    choose('image/svg+xml', 'unsafe.svg'); assert.equal(readers.length, 2); assert.equal(drop.children.length, 0); assert.equal(title.textContent, 'unsafe.svg');
    choose('image/png', 'large.png', 9 * 1024 * 1024); assert.equal(readers.length, 2); assert.match(detail.textContent, /大图/);
    const cancelled = form.emit('submit'); cancelled.preventDefault(); await Promise.resolve(); f.flush(); assert.equal(submit.disabled, false);
    form.emit('submit'); await Promise.resolve(); f.flush(); assert.equal(submit.disabled, true);
    f.window.emit('pageshow', { persisted: true }); assert.equal(submit.disabled, false);
  });
}
