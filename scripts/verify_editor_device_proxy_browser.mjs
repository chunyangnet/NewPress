// Automatic proxy smoke only: file-input upload is not native clipboard proof.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, webkit } from 'playwright';

const directory = resolve(process.argv[2]);
const access = JSON.parse(await readFile(resolve(directory, 'device-access.json'), 'utf8'));
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==', 'base64');
const evidence = [];
for (const [name, engine] of Object.entries({ chromium, webkit })) {
  const browser = await engine.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  try {
    await page.goto(access.invitation);
    await page.locator('#report').waitFor();
    await page.goto(access.editor);
    await page.locator('.ProseMirror').waitFor();
    const original = await page.locator('[data-content-source]').inputValue();
    await page.locator('.editor-insert-menu > summary').click();
    await page.locator('.editor-insert-menu [data-editor-media-open="body"]').click();
    await page.locator('[data-editor-media-dialog][open]').waitFor();
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-upload]').disabled);
    await page.locator('[data-editor-media-file]').setInputFiles({ name: 'device-test.png', mimeType: 'image/png', buffer: png });
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-apply]').disabled);
    await page.locator('[data-editor-media-insert] [name="alt_text"]').fill('测试图片');
    await page.locator('[data-editor-media-apply]').click();
    await page.waitForFunction(() => document.querySelector('[data-content-source]').value.includes('/media/7/device-acceptance.png'));
    assert.equal(await page.locator('.ProseMirror img[src="/media/7/device-acceptance.png"]').count(), 1);
    await page.locator('[data-composer-undo]').click();
    assert.equal(await page.locator('[data-content-source]').inputValue(), original);
    assert.deepEqual(errors, []);
    evidence.push({ engine: name, realGoPage: true, simulatedImageUpload: true, insertAndUndo: true,
      nativeClipboard: false, nativeSafari: false, errors });
  } finally { await browser.close(); }
}
await writeFile(resolve(directory, 'image-proxy-smoke.json'), JSON.stringify({ evidence }, null, 2) + '\n');
console.log('Device proxy image upload, insertion and undo passed in Chromium and WebKit; native device acceptance remains separate.');
