const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');
const { environment, Element, dialogFixture } = require('./helpers/editor-tools.cjs');
const sources = {
  TypeScript: compile('frontend/core/editor-tools-loader.ts'),
  generated: fs.readFileSync('internal/coreui/assets/js/newpress-editor-tools-loader.js', 'utf8'),
};
const selector = '[data-editor-reuse-open], [data-editor-snippets-open], [data-editor-widget-open], [data-snippet-draft-open]';

function fixture(variant) {
  const f = environment(), form = new Element('form'), source = new Element('textarea'), entry = new Element('script'), feedback = new Element();
  source.value = 'draft'; source.selectionStart = 1; source.selectionEnd = 4;
  entry.dataset.editorToolsScript = '/insert-tools.js'; form.dataset = { editorUserId: '7', editorLogin: 'editor' };
  const launchers = ['reuse', 'snippets', 'widget', 'draft'].map(name => {
    const button = new Element('button'); button.hasAttribute = key => button.attributes.has(key);
    button.setAttribute(name === 'draft' ? 'data-snippet-draft-open' : `data-editor-${name}-open`, '');
    if (name === 'draft') button.dataset.post = '4';
    return button;
  });
  form.selectors.set('[data-content-source]', source); form.groups.set(selector, launchers);
  f.document.selectors.set('[data-content-editor]', form);
  f.document.selectors.set('script[data-editor-tools-loader]', entry);
  f.document.selectors.set('[data-composer-feedback]', feedback);
  const store = new Map(); f.globals.sessionStorage = { getItem: key => store.get(key) || null };
  f.window.ChunnelBlockEditor = { capture: () => ({ start: source.selectionStart, end: source.selectionEnd, value: source.value }) };
  const opened = [];
  const register = () => {
    f.window.NewPressEditorTools = new WeakMap(launchers.map((button, index) => [button, selection => opened.push({ index, selection })]));
  };
  vm.runInNewContext(sources[variant], f.globals);
  return { ...f, form, source, entry, feedback, launchers, store, opened, register,
    async complete() { register(); f.document.head.children.at(-1).emit('load'); await f.settle(); },
  };
}

for (const variant of Object.keys(sources)) {
  test(`${variant}: insertion tools wait for a click and preserve the selection captured before loading`, async () => {
    const f = fixture(variant), button = f.launchers[0];
    assert.equal(f.document.head.children.length, 0); assert.equal(f.timers.size, 0);
    assert.equal(button.emit('pointerdown').defaultPrevented, true);
    button.click(); assert.equal(f.document.head.children.length, 1);
    f.source.selectionStart = f.source.selectionEnd = 5;
    await f.complete();
    assert.deepEqual(JSON.parse(JSON.stringify(f.opened)), [{ index: 0, selection: { start: 1, end: 4, value: 'draft' } }]);
    assert.equal(button.getAttribute('aria-busy'), null);
    button.click(); await f.settle();
    assert.equal(f.document.head.children.length, 1); assert.equal(f.opened.length, 2);
    assert.equal(f.opened[1].selection.start, 5);
  });

  test(`${variant}: duplicate and competing clicks share one download and only the latest intent opens`, async () => {
    const f = fixture(variant), [first, last] = f.launchers;
    first.setAttribute('aria-busy', 'false'); first.click(); first.click(); last.click();
    assert.equal(first.getAttribute('aria-busy'), 'false'); assert.equal(last.getAttribute('aria-busy'), 'true');
    assert.equal(f.document.head.children.length, 1);
    await f.complete(); assert.equal(f.opened.length, 1); assert.equal(f.opened[0].index, 1);
    assert.equal(last.getAttribute('aria-busy'), null); assert.equal(f.feedback.hidden, true);
  });

  test(`${variant}: network errors, timeouts and missing initialization retry without opening a stale intent`, async () => {
    for (const failure of ['error', 'timeout', 'initialization']) {
      const f = fixture(variant), button = f.launchers[0]; button.click();
      const old = f.document.head.children[0];
      if (failure === 'timeout') f.flush(); else old.emit(failure === 'error' ? 'error' : 'load');
      await f.settle(); assert.equal(f.opened.length, 0, failure); assert.equal(old.isConnected, false, failure);
      assert.match(f.feedback.textContent, /重试/); assert.equal(button.getAttribute('aria-busy'), null);
      button.click(); assert.equal(f.document.head.children.length, 2);
      old.emit('load'); await f.settle(); assert.equal(f.opened.length, 0);
      await f.complete(); assert.equal(f.opened.length, 1, failure);
    }
  });

  test(`${variant}: changed input, permission, DOM or editor instance invalidates a delayed tool`, async () => {
    for (const change of ['body', 'readonly', 'disabled', 'button', 'form', 'source', 'launcher', 'editor']) {
      const f = fixture(variant), button = f.launchers[0]; button.click();
      if (change === 'body') f.source.value = 'new input';
      if (change === 'readonly') f.source.readOnly = true;
      if (change === 'disabled') f.source.disabled = true;
      if (change === 'button') button.disabled = true;
      if (change === 'form') f.form.isConnected = false;
      if (change === 'source') f.source.isConnected = false;
      if (change === 'launcher') button.isConnected = false;
      if (change === 'editor') f.window.ChunnelBlockEditor = { ...f.window.ChunnelBlockEditor };
      await f.complete(); assert.equal(f.opened.length, 0, change);
      assert.equal(button.getAttribute('aria-busy'), null, change);
      assert.equal(f.source.value, change === 'body' ? 'new input' : 'draft', change);
    }
  });

  test(`${variant}: suspended pages ignore late loads and restored pages require a fresh click`, async () => {
    const f = fixture(variant), button = f.launchers[0]; button.click();
    f.window.emit('pagehide', { persisted: true }); await f.complete();
    assert.equal(f.opened.length, 0); assert.equal(button.getAttribute('aria-busy'), null);
    button.click(); await f.settle(); assert.equal(f.opened.length, 0);
    f.window.emit('pageshow', { persisted: true }); button.click(); await f.settle();
    assert.equal(f.opened.length, 1); assert.equal(f.document.head.children.length, 1);
  });

  test(`${variant}: Escape or opening another dialog cancels delayed activation without discarding the loaded tool`, async () => {
    for (const cancellation of ['escape', 'dialog']) {
      const f = fixture(variant), button = f.launchers[0]; button.click();
      if (cancellation === 'escape') f.document.emit('keydown', { key: 'Escape' });
      else f.document.selectors.set('dialog[open]', new Element('dialog'));
      await f.complete(); assert.equal(f.opened.length, 0); assert.equal(button.getAttribute('aria-busy'), null);
      assert.equal(f.feedback.hidden, true);
      f.document.selectors.delete('dialog[open]'); button.click(); await f.settle();
      assert.equal(f.opened.length, 1); assert.equal(f.document.head.children.length, 1);
    }
  });

  test(`${variant}: locked input never starts a download; saved-version drafts do not depend on the input selection`, async () => {
    const f = fixture(variant); f.source.readOnly = true;
    f.launchers[0].click(); assert.equal(f.document.head.children.length, 0);
    f.launchers[3].click(); f.source.value = 'unsaved new text'; await f.complete();
    assert.equal(f.opened.length, 1); assert.equal(f.opened[0].index, 3); assert.equal(f.opened[0].selection, undefined);
    assert.equal(f.source.value, 'unsaved new text');
  });

  test(`${variant}: restored draft uncertainty warns before the optional script loads without replaying a request`, async () => {
    const f = fixture(variant), key = 'newpress:snippet-draft:user:7:4';
    f.store.set(key, JSON.stringify({ post_id: 4, version: 2, digest: 'a'.repeat(64), request_key: 'b'.repeat(64) }));
    assert.equal(f.window.emit('beforeunload').defaultPrevented, true);
    assert.equal(f.document.head.children.length, 0); assert.equal(f.requests.length, 0);
    f.store.set(key, JSON.stringify({ post_id: 5, version: 2, digest: 'a'.repeat(64), request_key: 'b'.repeat(64) }));
    assert.equal(f.window.emit('beforeunload').defaultPrevented, false);
    f.store.set(key, '{bad json'); assert.equal(f.window.emit('beforeunload').defaultPrevented, false);
  });

  test(`${variant}: retrying an already initialized tool does not duplicate eager handlers`, async () => {
    for (const name of ['editor-reuse', 'editor-widgets']) {
      const f = dialogFixture(name, variant); f.load(name, variant);
      f.open(); assert.equal(f.requests.length, 1, name);
      assert.equal(f.launcher.listeners.get('click').length, 1, name);
      assert.equal(f.dialog.listeners.get('close').length, 1, name);
    }
  });
}
