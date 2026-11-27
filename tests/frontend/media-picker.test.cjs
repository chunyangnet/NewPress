const test = require('node:test');
const assert = require('node:assert/strict');
const { fixture, mediaList, imageItem, Element } = require('./helpers/media-picker.cjs');
const { loadModule } = require('./helpers/typescript.cjs');
const data = loadModule('frontend/core/media-data.ts', { URL, location: { origin: 'https://newpress.test' } });

test('media data validates item types, list controls and upload confirmation', () => {
  for (const invalid of [null, [], {}, imageItem({ id: 1.5 }), imageItem({ status: 'trash' }), imageItem({ kind: 'document' }),
    imageItem({ width: 0 }), imageItem({ height: Infinity }), imageItem({ size_bytes: '128' }), imageItem({ alt_text: {} })]) {
    assert.equal(data.decodeMediaImage(invalid), null);
  }
  assert.equal(data.decodeMediaImage(imageItem()).id, 1);
  for (const invalid of [null, {}, mediaList([], { items: null }), mediaList([], { can_upload: 'true' }),
    mediaList([], { max_upload_bytes: -1 }), mediaList([], { pagination: { page: 1, pages: 0, total: 0 } })]) {
    assert.throws(() => data.decodeMediaList(invalid), /响应/);
  }
  assert.equal(data.decodeMediaList(mediaList([null, imageItem(), imageItem({ status: 'trash' })])).items.length, 1);
  for (const invalid of [null, {}, { item: imageItem() }, { item: imageItem({ id: -1 }), duplicate: false }]) assert.throws(() => data.decodeMediaUpload(invalid), /确认/);
  assert.equal(data.decodeMediaUpload({ item: imageItem(), duplicate: true }).duplicate, true);
});

test('media URL accepts local media and rejects credentials, foreign paths and parser normalization tricks', () => {
  for (const public_url of ['/media/1/image.png', 'https://newpress.test/media/1/image.png']) assert.equal(data.mediaURL(imageItem({ public_url })), '/media/1/image.png');
  for (const public_url of ['https://foreign.test/media/a', '//foreign.test/media/a', '/other/a', '/media/../admin/',
    'javascript:alert(1)', '/media/a?x=1', '/media/a#x', '/media/a\\b', '/media/a\n', 'https://user@newpress.test/media/a']) {
    assert.equal(data.mediaURL(imageItem({ public_url })), null, public_url);
  }
});

for (const variant of ['TypeScript', 'generated']) {
  const ready = async (extra = {}, options) => {
    const f = fixture(variant, options); void f.open(extra); await f.reply(0, mediaList()); f.select(); f.ready(); return f;
  };
  test(`${variant}: field selection revalidates the item and calls once with full metadata`, async () => {
    const f = await ready({}, { editor: false });
    assert.equal(f.fields.hidden, true); const pending = f.submit(); void f.submit();
    assert.equal(f.requests.length, 2); assert.equal(f.requests[1].url, '/api/v1/admin/media/1');
    assert.equal(f.requests[1].init.credentials, 'same-origin'); assert.equal(f.requests[1].init.redirect, 'error');
    assert.equal(f.requests[1].init.headers.get('Accept'), 'application/json');
    await f.reply(1, imageItem({ alt_text: 'Latest alt' })); await pending;
    assert.equal(f.selections.length, 1); assert.equal(f.selections[0].item.alt_text, 'Latest alt');
    assert.equal(f.selections[0].url, '/media/1/image.png'); assert.equal(f.dialog.open, false);
    assert.equal(f.launcher.focused, true); assert.equal(f.launcher.getAttribute('aria-expanded'), 'false');
  });

  test(`${variant}: async field selection cannot close a reopened picker`, async () => {
    let finish;
    const f = await ready({ onSelect: () => new Promise(resolve => { finish = resolve; }) });
    const pending = f.submit(); await f.reply(1, imageItem()); f.close(); void f.open();
    finish(); await pending; assert.equal(f.dialog.open, true); assert.equal(f.searchButton.disabled, true);
    await f.reply(2, mediaList()); assert.equal(f.searchButton.disabled, false);
  });

  test(`${variant}: callback failure leaves selected image available for another try`, async () => {
    let attempts = 0;
    const f = await ready({ onSelect: () => { if (++attempts === 1) throw new Error('field full'); } });
    let pending = f.submit(); await f.reply(1, imageItem()); await pending;
    assert.equal(f.dialog.open, true); assert.match(f.errorText.textContent, /field full/); assert.equal(f.controls.get('apply').disabled, false);
    pending = f.submit(); await f.reply(2, imageItem()); await pending; assert.equal(f.dialog.open, false);
  });

  test(`${variant}: stale or mismatched media revalidation never changes a field`, async () => {
    for (const item of [imageItem({ status: 'trash' }), imageItem({ id: 2 }), imageItem({ public_url: '/admin/' }), { id: 1 }]) {
      const f = await ready(); const pending = f.submit(); await f.reply(1, item); await pending;
      assert.equal(f.selections.length, 0); assert.equal(f.dialog.open, true); assert.match(f.errorText.textContent, /不可用/);
    }
    let disabled = false;
    const f = await ready({ disabled: () => disabled }); const pending = f.submit(); disabled = true;
    await f.reply(1, imageItem()); await pending; assert.equal(f.selections.length, 0); assert.match(f.errorText.textContent, /不可编辑/);
  });

  test(`${variant}: body insertion preserves selection and serializes image text as text`, async () => {
    const f = fixture(variant); f.bodyLauncher.click(); await f.reply(0, mediaList()); f.select(); f.ready();
    assert.equal(f.fields.hidden, false); f.details.elements.alt_text.value = '"<alt>'; f.details.elements.caption.value = '<caption>'; f.details.elements.width.value = '50';
    const pending = f.submit(); await f.reply(1, imageItem()); await pending;
    assert.equal(f.insertions.length, 1); assert.equal(f.insertions[0].selection.value, 'draft');
    assert.match(f.insertions[0].markup, /height="25"/); assert.match(f.insertions[0].markup, /&quot;&lt;alt>/); assert.match(f.insertions[0].markup, /&lt;caption>/);
    assert.equal(f.dialog.open, false); assert.equal(f.bodyLauncher.focused, undefined);
  });

  test(`${variant}: changed or newly locked body cannot be overwritten after revalidation`, async () => {
    for (const lock of [f => { f.source.value = 'new text'; }, f => { f.source.readOnly = true; }]) {
      const f = fixture(variant); f.bodyLauncher.click(); await f.reply(0, mediaList()); f.select(); f.ready();
      const pending = f.submit(); lock(f); await f.reply(1, imageItem()); await pending;
      assert.equal(f.insertions.length, 0); assert.equal(f.dialog.open, true); assert.equal(f.controls.get('error').hidden, false);
    }
  });

  test(`${variant}: blank body alt requires decorative consent and native validation`, async () => {
    const f = fixture(variant); f.bodyLauncher.click(); await f.reply(0, mediaList()); f.select(); f.ready();
    f.details.elements.alt_text.value = ' '; await f.submit();
    assert.equal(f.requests.length, 1); assert.match(f.details.elements.alt_text.validationMessage, /替代文本/);
    f.details.elements.alt_text.emit('input'); assert.equal(f.details.elements.alt_text.validationMessage, '');
    f.details.elements.decorative.checked = true; f.details.elements.decorative.emit('change');
    assert.equal(f.details.elements.alt_text.required, false);
    f.details.valid = false; await f.submit(); assert.equal(f.requests.length, 1);
    f.details.valid = true; const pending = f.submit(); await f.reply(1, imageItem()); await pending;
    assert.match(f.insertions[0].markup, /alt=""/);
  });

  test(`${variant}: source-mode fallback validates before closing and keeps its range`, async () => {
    const f = fixture(variant), replacements = []; delete f.window.ChunnelBlockEditor;
    let error = 'too long'; f.window.ChunnelEmoji = { validateReplacement: () => error,
      replaceSelection: (source, markup, selection) => replacements.push({ source, markup, selection }) };
    f.bodyLauncher.click(); await f.reply(0, mediaList()); f.select(); f.ready();
    let pending = f.submit(); await f.reply(1, imageItem()); await pending;
    assert.equal(f.dialog.open, true); assert.equal(replacements.length, 0);
    error = ''; pending = f.submit(); await f.reply(2, imageItem()); await pending;
    assert.equal(replacements[0].selection.start, 5); assert.equal(f.dialog.open, false); assert.equal(f.source.focused, true);
  });

  test(`${variant}: cover and component consumers retain their metadata and focus behavior`, async () => {
    const cover = fixture(variant); cover.coverLauncher.click(); await cover.reply(0, mediaList()); cover.select(); cover.ready();
    let pending = cover.submit(); await cover.reply(1, imageItem()); await pending;
    assert.equal(cover.cover.value, '/media/1/image.png'); assert.equal(cover.cover.focused, true);
    const f = fixture(variant), button = new Element('button'), selections = [];
    f.window.NewPressComponentMedia(button, (url, item) => selections.push({ url, item }));
    await f.reply(0, mediaList()); f.select(); f.ready(); pending = f.submit(); await f.reply(1, imageItem()); await pending;
    assert.equal(selections[0].item.alt_text, 'Library alt'); assert.equal(button.focused, true);
  });

  test(`${variant}: malformed and mismatched list pages unlock controls and allow retry`, async () => {
    for (const payload of [{}, mediaList([], { pagination: { page: 1, pages: 0, total: 0 } }), mediaList([], { pagination: { page: 3, pages: 1, total: 0 } })]) {
      const f = fixture(variant); void f.open(); await f.reply(0, payload);
      assert.equal(f.grid.children.length, 0); assert.equal(f.searchButton.disabled, false); assert.equal(f.controls.get('retry').hidden, false);
      f.controls.get('retry').click(); await f.reply(1, mediaList()); assert.equal(f.grid.children.length, 1);
    }
  });

  test(`${variant}: out-of-range page is corrected once and old search results are ignored`, async () => {
    const f = fixture(variant); void f.open(); await f.reply(0, mediaList([], { pagination: { page: 1, pages: 2, total: 21 } }));
    f.controls.get('next').click(); await f.reply(1, mediaList([], { pagination: { page: 2, pages: 1, total: 0 } }));
    assert.match(f.requests[2].url, /page=1/); await f.reply(2, mediaList());
    f.query.value = 'old'; f.search.emit('submit'); f.query.value = 'new'; f.search.emit('submit');
    assert.equal(f.requests[3].init.signal.aborted, true);
    await f.reply(4, mediaList([imageItem({ id: 2, title: 'new' })])); await f.reply(3, mediaList());
    assert.equal(f.grid.children[0].dataset.editorImageId, '2');
  });

  test(`${variant}: upload retry retains file and sends routing and CSRF fields`, async () => {
    const file = new File(['image'], 'retained.png', { type: 'image/png' }), f = fixture(variant); void f.open({ file, uploadSource: 'avatar' });
    await f.reply(0, mediaList()); const upload = f.requests[1];
    assert.equal(upload.init.body.get('upload_source'), 'avatar'); assert.equal(upload.init.body.get('csrf_token'), 'test-csrf');
    assert.equal(upload.init.headers.get('X-CSRF-Token'), 'test-csrf');
    await f.reply(1, { error: { code: 'media.duplicate_trashed', message: 'restore', details: { media_id: 5 } } }, 409);
    assert.equal(f.recovery.hidden, false); assert.equal(f.recovery.href, '/admin/media/5/edit/');
    f.controls.get('retry').click(); assert.equal(f.requests[2].init.body.get('file'), file);
    await f.reply(2, { item: imageItem(), duplicate: true }); await f.reply(3, mediaList());
    assert.match(f.controls.get('status').textContent, /相同图片/); assert.equal(f.file.value, '');
  });

  test(`${variant}: upload blocks unsupported, oversized, empty and unauthorized files`, async () => {
    for (const file of [new File([], 'empty.png'), new File(['x'], 'bad.svg', { type: 'image/svg+xml' }), new File([new Uint8Array(1048577)], 'big.png')]) {
      const f = fixture(variant); void f.open({ file }); await f.reply(0, mediaList());
      assert.equal(f.requests.length, 1); assert.equal(f.controls.get('error').hidden, false);
    }
    for (const options of [{ canUpload: false }, { csrf: false }]) {
      const f = fixture(variant, options); void f.open({ file: new File(['image'], 'x.png') }); await f.reply(0, mediaList());
      if (options.canUpload === false) await f.reply(1, mediaList());
      assert.equal(f.requests.some(request => request.init.method === 'POST'), false);
      assert.equal(f.controls.get('error').hidden, false);
    }
  });

  test(`${variant}: network, login and timeout errors unlock controls without losing input`, async () => {
    const f = fixture(variant); void f.open(); f.query.value = 'retained';
    await f.reply(0, { error: { message: 42 } }, 401); assert.match(f.errorText.textContent, /登录/); assert.equal(f.query.value, 'retained');
    f.controls.get('retry').click(); f.requests[1].reject(new TypeError('offline')); await f.settle(); assert.match(f.errorText.textContent, /网络/);
    f.controls.get('retry').click();
    const timer = [...f.timers.values()].find(item => item.delay === 15000); assert.ok(timer); timer.callback();
    f.requests[2].reject(new DOMException('aborted', 'AbortError')); await f.settle();
    assert.match(f.errorText.textContent, /超时/); assert.equal(f.searchButton.disabled, false); assert.equal(f.query.value, 'retained');
  });

  test(`${variant}: timeout during response body parsing is still reported as timeout`, async () => {
    const f = fixture(variant); void f.open(); let reject;
    f.requests[0].respondBody(new Promise((resolve, fail) => { reject = fail; })); await f.settle();
    [...f.timers.values()][0].callback(); reject(new DOMException('aborted', 'AbortError')); await f.settle();
    assert.match(f.errorText.textContent, /超时/); assert.equal(f.searchButton.disabled, false);
  });
}
