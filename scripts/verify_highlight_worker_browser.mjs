import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';

const directory = resolve(process.argv[2]), fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const evidence = [];
for (const [engine, driver] of Object.entries({chromium, firefox, webkit})) {
  const browser = await driver.launch(), context = await browser.newContext(), page = await context.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await context.route('**/*', route => ['GET','HEAD'].includes(route.request().method()) ? route.fallback() : route.fulfill({status: 403}));
  try {
    await page.goto(fixture.base + fixture.login);
    const replies = await page.evaluate(async () => {
      const script = [...document.scripts].find(node => node.src.includes('/core-assets/'));
      const url = new URL('newpress-highlight-worker.js', script.src);
      const worker = new Worker(url);
      try {
        const request = data => new Promise((resolve, reject) => {
          const timeout = setTimeout(() => reject(new Error('Worker response timed out')), 12000);
          worker.onmessage = event => {clearTimeout(timeout); resolve(event.data);};
          worker.onerror = event => {clearTimeout(timeout); reject(new Error(event.message));};
          worker.postMessage(data);
        });
        return [await request({id:1, text:'const count: number = 1;', language:'typescript'}),
          await request({id:2, text:'<script>unsafe()</script>', language:'unknown-language'}),
          await request({id:3, text:'x'.repeat(48001), language:'javascript'})];
      } finally {worker.terminate();}
    });
    assert.match(replies[0].html, /class="hljs-/); assert.equal(replies[0].label, 'TypeScript');
    assert.doesNotMatch(replies[1].html, /<script>/); assert.equal(replies[1].id, 2);
    assert.equal(replies[2].error, true); assert.deepEqual(errors, []);
    evidence.push({engine, checks:3, replies});
    console.log(engine + ': typed worker loads from the actual revision URL, escapes markup and bounds input.');
  } finally {await context.close(); await browser.close();}
}
await writeFile(resolve(directory, 'worker-browser.json'), JSON.stringify({evidence}, null, 2) + '\n');
