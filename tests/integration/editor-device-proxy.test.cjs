const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');

test('device acceptance gates access, intercepts every write, and records manual results without article contents', async () => {
  const { startDeviceAcceptance, cases } = await import('../../scripts/serve_editor_device_acceptance.mjs');
  const upstreamRequests = [];
  const nonce = 'a'.repeat(32), editor = '/admin/content/1/edit/';
  const upstream = http.createServer((req, res) => {
    upstreamRequests.push({ method: req.method, path: req.url });
    if (req.url === `/__admin_resource_login/${nonce}`) {
      res.writeHead(303, { 'Set-Cookie': 'isolated_owner=owner-secret; HttpOnly; Path=/', Location: '/admin/' });
    } else if (req.url === editor) {
      assert.equal(req.headers.cookie, 'isolated_owner=owner-secret');
      res.writeHead(200, { 'Content-Type': 'text/html', ETag: 'original-page-tag' });
      res.write('<html><head></head><body>isolated editor</body></html>');
    } else if (req.url === `/__admin_resource_stop/${nonce}`) {
      assert.equal(req.headers.cookie, 'isolated_owner=owner-secret');
      res.writeHead(204);
    } else res.writeHead(404);
    res.end();
  });
  await new Promise(ready => upstream.listen(0, '127.0.0.1', ready));
  const tempRoot = path.resolve(os.tmpdir()), directory = await fs.mkdtemp(path.join(tempRoot, 'newpress-device-proxy-'));
  let session;
  try {
    session = await startDeviceAcceptance({ directory, fixture: {
      base: `http://127.0.0.1:${upstream.address().port}`, login: `/__admin_resource_login/${nonce}`, stop: `/__admin_resource_stop/${nonce}`,
      editor_post_id: 1, paths: { editor }, editor_receipt: { saved: true, id: 1, version: 2 }, autosave_receipt: { available: true, post_id: 1 },
    } });
    assert.equal((await fetch(session.base + editor)).status, 403);
    const admission = await fetch(session.invitation, { redirect: 'manual' });
    assert.equal(admission.status, 303);
    const cookie = admission.headers.get('set-cookie').split(';')[0];
    assert.ok(!cookie.includes('owner-secret'));
    const request = (route, options = {}) => fetch(session.base + route, { ...options, headers: { Cookie: cookie, ...options.headers } });
    const page = await request(editor);
    assert.equal(page.status, 200); assert.equal(page.headers.get('etag'), null);
    assert.match(await page.text(), /src="\/__qa\/observe.js"/);
    const portal = await request('/__qa/');
    assert.equal(portal.status, 200); assert.match(await portal.text(), /实际 Apple 设备/);
    assert.equal((await request(`/__admin_resource_stop/${nonce}`, { method: 'POST', headers: { Origin: session.base } })).status, 403);
    assert.equal((await request(`/__admin_resource_login/${nonce}`)).status, 404);
    const media = await (await request('/api/v1/admin/media')).json();
    assert.match(media.items[0].public_url, /^\/media\//, 'placeholder must satisfy the actual editor local-image contract');
    assert.equal((await request(media.items[0].public_url)).headers.get('content-type'), 'image/png');
    assert.equal((await request(editor, { method: 'POST', headers: { Origin: 'http://another-origin.test' }, body: 'version=2' })).status, 403);
    assert.equal((await request(editor, { method: 'POST', body: 'version=2' })).status, 403);
    const saved = await request(editor, { method: 'POST', headers: { Origin: session.base, 'Idempotency-Key': 'test-key' }, body: 'version=2&content_source=PRIVATE_TYPED_TEXT' });
    assert.deepEqual(await saved.json(), { saved: true, id: 1, version: 3, submission_key: 'test-key' });
    const blocked = await request('/api/v1/admin/arbitrary-future-tool', { method: 'DELETE', headers: { Origin: session.base } });
    assert.equal(blocked.status, 403);
    const body = { device: 'AUTOMATED HARNESS', version: 'not a device observation', actualSafari: false, results: {} };
    const report = payload => request('/__qa/report', { method: 'POST', headers: { Origin: session.base, 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
    assert.equal((await report(body)).status, 400);
    const accepted = await report({ ...body, actualSafari: true, results: { ime: 'pass', history: 'invalid-value' } });
    assert.equal((await accepted.json()).remaining, cases.length - 1);
    assert.equal(session.audit.reports[0].cases.find(item => item.id === 'history').result, 'not-run');
    const hostileHost = await new Promise((done, reject) => {
      const req = http.get(session.base + editor, { headers: { Host: 'attacker.example', Cookie: cookie } }, res => { res.resume(); done(res.statusCode); });
      req.on('error', reject);
    });
    assert.equal(hostileHost, 403);
    await session.close();
    assert.deepEqual(upstreamRequests.filter(item => item.method !== 'GET'), [{ method: 'POST', path: `/__admin_resource_stop/${nonce}` }]);
    const evidence = await fs.readFile(path.join(directory, 'device-session.json'), 'utf8');
    assert.ok(!evidence.includes('PRIVATE_TYPED_TEXT')); assert.ok(!evidence.includes('owner-secret'));
    assert.equal(JSON.parse(evidence).fixture_stop_status, 204);
  } finally {
    await session?.close();
    await new Promise(done => upstream.close(done));
    const relative = path.relative(tempRoot, path.resolve(directory));
    assert.ok(relative.startsWith('newpress-device-proxy-') && !relative.includes(path.sep), 'cleanup only this test-created temp directory');
    await fs.rm(directory, { recursive: true, force: true });
  }
});
