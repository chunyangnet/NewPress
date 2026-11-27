const test = require('node:test');
const assert = require('node:assert/strict');
const { fixture, mediaList } = require('./helpers/media-picker.cjs');
const pastedFile = () => new File(['image'], 'paste.png', { type: 'image/png' });

for (const variant of ['TypeScript', 'generated']) {
  test(`${variant}: closing and reopening cannot revive a previous clipboard file`, async () => {
    const f = fixture(variant), event = f.paste([pastedFile()]);
    assert.equal(event.defaultPrevented, true); assert.equal(event.immediateStopped, true);
    f.close(); f.bodyLauncher.click(); await f.reply(0, mediaList());
    assert.equal(f.requests.length, 2);
    await f.reply(1, mediaList()); assert.equal(f.requests.length, 2); assert.equal(f.controls.get('error').hidden, true);
  });
  test(`${variant}: failed list lookup retains the pasted file for explicit retry`, async () => {
    const f = fixture(variant), file = pastedFile(); f.paste([file]);
    await f.reply(0, { error: { message: 'offline' } }, 503); assert.equal(f.requests.length, 1);
    assert.match(f.errorText.textContent, /文件已保留/);
    f.controls.get('retry').click(); await f.reply(1, mediaList());
    assert.equal(f.requests[2].init.body.get('file'), file);
    assert.equal(f.requests[2].init.body.get('upload_source'), 'editor-body');
  });
  test(`${variant}: a retry pending across close never uploads a retained file`, async () => {
    const f = fixture(variant); f.paste([pastedFile()]); await f.reply(0, null, 503);
    f.controls.get('retry').click(); f.close(); f.bodyLauncher.click();
    await f.reply(1, mediaList()); assert.equal(f.requests.length, 3);
    await f.reply(2, mediaList()); assert.equal(f.requests.length, 3);
  });
  test(`${variant}: clipboard text does not upload and multiple files require another choice`, async () => {
    const f = fixture(variant);
    assert.equal(f.paste([]).defaultPrevented, false); assert.equal(f.requests.length, 0);
    f.paste([pastedFile(), pastedFile()]); await f.reply(0, mediaList());
    assert.match(f.errorText.textContent, /一次粘贴/); assert.equal(f.requests.length, 1);
    f.close(); f.source.readOnly = true;
    assert.equal(f.paste([pastedFile()]).defaultPrevented, false); assert.equal(f.requests.length, 1);
  });
  test(`${variant}: drop shares the single-file and read-only guards`, async () => {
    const f = fixture(variant), file = pastedFile();
    assert.equal(f.drop([file]).defaultPrevented, true); await f.reply(0, mediaList());
    assert.equal(f.requests[1].init.body.get('file'), file);
    f.close(); f.source.disabled = true; f.drop([file]); assert.equal(f.requests.length, 2);
  });
  test(`${variant}: an old multiple-file warning cannot leak into a reopened picker`, async () => {
    for (const method of ['paste', 'drop']) {
      const f = fixture(variant); f[method]([pastedFile(), pastedFile()]); f.close(); f.bodyLauncher.click();
      await f.reply(1, mediaList()); await f.reply(0, mediaList());
      assert.equal(f.controls.get('error').hidden, true); assert.equal(f.requests.length, 2);
    }
  });
}
