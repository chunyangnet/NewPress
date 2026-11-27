const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');
const { environment, Element } = require('./helpers/editor-tools.cjs');
const sources = { TypeScript: compile('frontend/core/component-loader.ts'), generated: fs.readFileSync('internal/coreui/assets/js/newpress-component-loader.js', 'utf8') };
function fixture(variant) {
  const f = environment(), entry = new Element('script'), dialog = new Element('dialog'), source = new Element('textarea'), launcher = new Element('button'), feedback = new Element(), anchor = new Element('meta');
  class Script extends Element { constructor() { super('script'); } }
  f.globals.HTMLScriptElement = Script; f.document.createElement = tag => tag === 'script' ? new Script() : new Element(tag);
  source.value = 'draft'; entry.dataset = { componentScript: '/library.js', componentStyle: '/library.css' };
  anchor.before = node => { node.insertedBeforeAnchor = true; f.document.head.append(node); };
  for (const [selector, element] of [['script[data-component-loader]', entry], ['[data-component-library]', dialog], ['[data-content-source]', source], ['[data-composer-feedback]', feedback], ['[data-component-style-anchor]', anchor]]) f.document.selectors.set(selector, element);
  f.document.groups.set('[data-component-library-open]', [launcher]);
  f.window.ChunnelBlockEditor = { capture: () => ({ start: 5, end: 5, value: source.value }) };
  const opens = [], edits = [];
  const library = { open: (...args) => opens.push(args), edit: (...args) => edits.push(args), decode: value => { if (value === 'invalid') throw new Error('bad configuration'); return { encoded: value }; } };
  vm.runInNewContext(sources[variant], f.globals);
  return { ...f, source, launcher, feedback, dialog, opens, edits, library,
    async complete() { f.window.NewPressComponentLibrary = library; f.document.head.children.forEach(asset => asset.emit('load')); await f.settle(); } };
}
for (const variant of Object.keys(sources)) {
  test(`${variant}: components load only on explicit use, deduplicate and preserve stylesheet order`, async () => {
    const f = fixture(variant); assert.equal(f.document.head.children.length, 0); assert.equal(f.dialog.dataset.componentLazy, 'true');
    f.launcher.click(); f.launcher.click(); assert.equal(f.document.head.children.length, 2);
    assert.equal(f.document.head.children[0].insertedBeforeAnchor, true); await f.complete();
    assert.equal(f.opens.length, 1); assert.equal(f.opens[0][1].value, 'draft');
    f.launcher.click(); await f.settle(); assert.equal(f.opens.length, 2); assert.equal(f.document.head.children.length, 2);
  });
  test(`${variant}: failed script retries without reloading an already loaded stylesheet`, async () => {
    const f = fixture(variant); f.launcher.click(); const [style, script] = f.document.head.children;
    style.emit('load'); script.emit('error'); await f.settle(); assert.match(f.feedback.textContent, /重试/);
    f.launcher.click(); assert.equal(f.document.head.children.length, 3); await f.complete(); assert.equal(f.opens.length, 1);
  });
  test(`${variant}: timeouts, changed documents, locks and pagehide never open a stale component dialog`, async () => {
    for (const kind of ['timeout', 'changed', 'locked', 'pagehide']) {
      const f = fixture(variant); f.launcher.click();
      if (kind === 'timeout') f.flush(); if (kind === 'changed') f.source.value = 'new draft'; if (kind === 'locked') f.source.readOnly = true; if (kind === 'pagehide') f.window.emit('pagehide');
      await f.complete(); assert.equal(f.opens.length, 0, kind); assert.equal(f.source.value, kind === 'changed' ? 'new draft' : 'draft');
      assert.equal(f.launcher.getAttribute('aria-busy'), null);
    }
  });
  test(`${variant}: editing an existing component defers decoding and retains the captured source`, async () => {
    const f = fixture(variant), selection = { start: 0, end: 5, value: 'draft' };
    const pending = f.window.NewPressComponentLoader.edit('saved-data', selection, f.launcher); await f.complete(); await pending;
    assert.equal(f.edits.length, 1); assert.equal(f.edits[0][0].encoded, 'saved-data'); assert.equal(f.edits[0][1], selection);
    await f.window.NewPressComponentLoader.edit('invalid', selection, f.launcher); assert.equal(f.edits.length, 1); assert.match(f.feedback.textContent, /bad configuration/);
  });
}
