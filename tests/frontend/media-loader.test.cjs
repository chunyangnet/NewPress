const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');
const { environment, Element } = require('./helpers/editor-tools.cjs');
const sources = { TypeScript: compile('frontend/core/media-loader.ts'), generated: fs.readFileSync('internal/coreui/assets/js/newpress-editor-media-loader.js', 'utf8') };
function fixture(variant) {
  const f = environment(), form = new Element('form'), dialog = new Element('dialog'), entry = new Element('script'), source = new Element('textarea'), cover = new Element('input'), feedback = new Element();
  const bodyButton = new Element('button'), coverButton = new Element('button'), componentButton = new Element('button'), fieldButton = new Element('button');
  bodyButton.dataset.editorMediaOpen = 'body'; coverButton.dataset.editorMediaOpen = 'cover';
  source.value = 'draft'; source.selectionStart = 1; source.selectionEnd = 4; cover.value = '/media/old.png';
  dialog.dataset.canUpload = 'true'; entry.dataset.mediaScript = '/media-dialog.js';
  form.selectors.set('[data-content-source]', source); form.selectors.set('[name="cover_url"]', cover);
  form.selectors.set('[data-editor-media-open="body"]', bodyButton); form.groups.set('[data-editor-media-open]', [bodyButton, coverButton]);
  for (const [selector, node] of [['script[data-editor-media-loader]', entry], ['[data-content-editor]', form], ['[data-editor-media-dialog]', dialog], ['[data-composer-feedback]', feedback]]) f.document.selectors.set(selector, node);
  let notice;
  f.document.createElement = tag => {
    const element = new Element(tag);
    if (tag === 'span') notice = element;
    return element;
  };
  for (const button of [bodyButton, coverButton, componentButton, fieldButton]) button.after = element => { notice = element; };
  f.globals.MutationObserver = class { observe() {} };
  f.window.ChunnelBlockEditor = { capture: () => ({ start: source.selectionStart, end: source.selectionEnd, value: source.value }) };
  const opens = [];
  const runtime = { dialog, async open(request) { opens.push(request); } };
  vm.runInNewContext(sources[variant], f.globals);
  return { ...f, form, dialog, entry, source, cover, feedback, bodyButton, coverButton, componentButton, fieldButton, opens, runtime,
    notice: () => notice,
    action(label) { const button = notice.children.find(item => item.textContent === label); assert.ok(button, label); button.click(); },
    async complete() { f.window.NewPressMediaRuntime = runtime; f.document.head.children.at(-1).emit('load'); await f.settle(); },
    paste(files) { return form.emit('paste', { target: source, clipboardData: { files } }); },
    drop(files) { return form.emit('drop', { target: source, dataTransfer: { files, types: ['Files'] } }); },
  };
}
for (const variant of Object.keys(sources)) {
  test(`${variant}: media remains unloaded until used and preserves the original body selection`, async () => {
    const f = fixture(variant); assert.equal(f.document.head.children.length, 0); assert.equal(f.timers.size, 0);
    assert.equal(f.bodyButton.emit('pointerdown').defaultPrevented, true);
    f.bodyButton.click(); f.source.selectionStart = f.source.selectionEnd = 5;
    await f.complete(); assert.equal(f.opens.length, 1);
    assert.deepEqual(JSON.parse(JSON.stringify(f.opens[0].selection)), { start: 1, end: 4, value: 'draft' });
    assert.equal(f.bodyButton.getAttribute('aria-busy'), null);
    f.coverButton.click(); await f.settle(); assert.equal(f.opens[1].mode, 'cover'); assert.equal(f.document.head.children.length, 1);
  });
  test(`${variant}: pasted and dropped files survive failed script loads and only explicit retries reach the picker`, async () => {
    for (const trigger of ['paste', 'drop']) {
      const f = fixture(variant), file = new File(['image'], trigger + '.png', { type: 'image/png' });
      const event = f[trigger]([file]); assert.equal(event.defaultPrevented, true); assert.equal(event.immediateStopped, true);
      f.document.head.children[0].emit('error'); await f.settle();
      assert.equal(f.opens.length, 0); assert.match(f.notice().textContent, /文件已保留/);
      f.action('重试加载图片工具'); await f.complete();
      assert.equal(f.opens.length, 1); assert.equal(f.opens[0].file, file); assert.equal(f.opens[0].mode, 'body');
      assert.equal(f.requests.length, 0);
    }
  });
  test(`${variant}: timeout and initialization failures can retry while late scripts cannot revive cancelled uploads`, async () => {
    for (const failure of ['timeout', 'initialization']) {
      const f = fixture(variant), file = new File(['image'], 'timeout.png'); f.paste([file]);
      if (failure === 'timeout') f.flush(); else f.document.head.children[0].emit('load');
      await f.settle(); assert.equal(f.opens.length, 0); assert.equal(f.bodyButton.getAttribute('aria-busy'), null);
      f.action('取消'); await f.complete(); assert.equal(f.opens.length, 0);
      f.bodyButton.click(); await f.settle(); assert.equal(f.opens.length, 1); assert.equal(f.opens[0].file, null);
    }
  });
  test(`${variant}: rapid body and cover intents share a download and only the last request opens`, async () => {
    const f = fixture(variant); f.bodyButton.setAttribute('aria-busy', 'false');
    f.bodyButton.click(); f.coverButton.click(); f.coverButton.click();
    assert.equal(f.document.head.children.length, 1); assert.equal(f.bodyButton.getAttribute('aria-busy'), 'false');
    await f.complete(); assert.equal(f.opens.length, 1); assert.equal(f.opens[0].mode, 'cover');
    assert.equal(f.coverButton.getAttribute('aria-busy'), null);
  });
  test(`${variant}: body, cover, permissions and replaced owners invalidate an unfinished download`, async () => {
    for (const change of ['body', 'readonly', 'fieldset', 'cover', 'editor', 'disconnected']) {
      const f = fixture(variant); (change === 'cover' ? f.coverButton : f.bodyButton).click();
      if (change === 'body') f.source.value = 'new text';
      if (change === 'readonly') f.source.readOnly = true;
      if (change === 'fieldset') f.source.matches = value => value === ':disabled';
      if (change === 'cover') f.cover.value = '/media/new.png';
      if (change === 'editor') f.window.ChunnelBlockEditor = { ...f.window.ChunnelBlockEditor };
      if (change === 'disconnected') f.source.isConnected = false;
      await f.complete(); assert.equal(f.opens.length, 0, change);
      assert.match(f.notice().textContent, /已变化/, change); assert.equal(f.notice().children.some(item => item.textContent === '重试加载图片工具'), false);
    }
  });
  test(`${variant}: Escape and page suspension cancel activation and release the pending file`, async () => {
    for (const cancellation of ['escape', 'pagehide', 'cancel']) {
      const f = fixture(variant), file = new File(['image'], 'cancel.png'); f.paste([file]);
      if (cancellation === 'escape') f.document.emit('keydown', { key: 'Escape' });
      else if (cancellation === 'pagehide') f.window.emit('pagehide');
      else f.action('取消');
      await f.complete(); assert.equal(f.opens.length, 0); assert.equal(f.bodyButton.getAttribute('aria-busy'), null);
      if (cancellation === 'pagehide') { f.bodyButton.click(); await f.settle(); assert.equal(f.opens.length, 0); f.window.emit('pageshow'); }
      f.bodyButton.click(); await f.settle(); assert.equal(f.opens.length, 1); assert.equal(f.opens[0].file, null);
    }
  });
  test(`${variant}: text and locked fields do not download media; multiple files are rejected by the same dialog`, async () => {
    const f = fixture(variant); assert.equal(f.paste([]).defaultPrevented, false);
    f.source.readOnly = true; assert.equal(f.paste([new File(['image'], 'locked.png')]).defaultPrevented, false);
    f.bodyButton.click(); assert.equal(f.document.head.children.length, 0);
    f.source.readOnly = false; f.drop([new File(['1'], 'a.png'), new File(['2'], 'b.png')]); await f.complete();
    assert.equal(f.opens[0].file, null); assert.match(f.opens[0].error, /一次拖入/);
  });
  test(`${variant}: component and field callbacks survive delayed loading and remain callable through the same public API`, async () => {
    const f = fixture(variant), callback = () => {};
    f.window.NewPressComponentMedia(f.componentButton, callback); await f.complete();
    assert.equal(f.opens[0].mode, 'component'); assert.equal(f.opens[0].component, callback);
    await f.window.NewPressMediaPicker.open(f.fieldButton, { title: 'field', onSelect: callback });
    assert.equal(f.opens[1].mode, 'field'); assert.equal(f.opens[1].pick.onSelect, callback);
    assert.equal(f.document.head.children.length, 1);
  });
}
