const test = require('node:test');
const assert = require('node:assert/strict');
const { fixture, mediaList } = require('./helpers/media-picker.cjs');
for (const variant of ['TypeScript', 'generated']) {
  test(variant + ': interrupted image upload retains its file and requires explicit retry', async () => {
    const f = fixture(variant), file = new File(['image'], 'retained.png', { type: 'image/png' });
    void f.open({ file }); await f.reply(0, mediaList());
    assert.equal(f.requests[1].init.body.get('file'), file);
    f.hide(); assert.equal(f.requests[1].init.signal.aborted, true);
    assert.equal(f.searchButton.disabled, false); assert.equal(f.controls.get('progress').hidden, true);
    assert.equal(f.requests.length, 2); assert.match(f.errorText.textContent, /中断/);
    f.controls.get('retry').click(); await f.reply(2, mediaList());
    assert.equal(f.requests[3].init.body.get('file'), file);
  });
  test(variant + ': closing a restored picker during retry cannot upload its old file', async () => {
    const f = fixture(variant); void f.open({ file: new File(['image'], 'retained.png', { type: 'image/png' }) });
    await f.reply(0, mediaList()); f.hide(); f.controls.get('retry').click();
    f.close(); await f.reply(2, mediaList());
    assert.equal(f.requests.length, 3);
  });
  test(variant + ': failed list recovery keeps the explicit upload retry available', async () => {
    const f = fixture(variant), file = new File(['image'], 'retained.png', { type: 'image/png' });
    void f.open({ file }); await f.reply(0, mediaList()); f.hide();
    f.controls.get('retry').click(); await f.reply(2, { error: { message: 'offline' } }, 503);
    assert.equal(f.controls.get('retry').hidden, false); assert.equal(f.requests.length, 3);
    f.controls.get('retry').click(); await f.reply(3, mediaList());
    assert.equal(f.requests[4].init.body.get('file'), file);
  });
  test(variant + ': retrying image display preserves fields and ignores obsolete image events', async () => {
    const f = fixture(variant); void f.open(); await f.reply(0, mediaList()); f.select();
    const oldImage = f.preview.firstElementChild, elements = f.details.elements;
    elements.alt_text.value = 'My alt'; elements.caption.value = 'My caption'; elements.width.value = '50'; elements.decorative.checked = true;
    oldImage.emit('error'); f.controls.get('retry').click();
    assert.equal(elements.alt_text.value, 'My alt'); assert.equal(elements.caption.value, 'My caption');
    assert.equal(elements.width.value, '50'); assert.equal(elements.decorative.checked, true);
    oldImage.emit('load'); assert.equal(f.controls.get('apply').disabled, true);
    f.ready(); assert.equal(f.controls.get('apply').disabled, false);
  });
}
