const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');

test('Chromium screenshots use pinned DNS and block redirects, frames, fetch and WebSockets to other local services', { timeout: 60000 }, async t => {
  const { ensureBrowser, closeBrowser, renderViewport } = await import('../../scripts/device_mockup_server.mjs');
  const { targetPolicy } = await import('../../scripts/device_mockup_network.mjs');
  let forbiddenHits = 0, socketHits = 0, allowedHits = 0;
  const forbidden = http.createServer((_req, res) => { forbiddenHits++; res.end('PRIVATE DATA'); });
  forbidden.on('upgrade', (_req, socket) => { socketHits++; socket.destroy(); });
  await new Promise(resolve => forbidden.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => forbidden.close(resolve)));
  const blocked = 'http://127.0.0.1:' + forbidden.address().port;
  const target = http.createServer((req, res) => {
    allowedHits++;
    if (req.url === '/redirect') { res.writeHead(302, { Location: blocked }); res.end(); return; }
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.end(`<!doctype html><title>Security regression fixture</title><h1>Allowed screenshot</h1>
      <iframe src="/redirect"></iframe><img src="${blocked}/image">
      <script>
      fetch('${blocked}/fetch').catch(() => {});
      try { new WebSocket('${blocked.replace('http:', 'ws:')}/socket'); } catch {}
      </script>`);
  });
  await new Promise(resolve => target.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => target.close(resolve)));
  // .invalid cannot resolve in Chromium. Success proves traffic uses the
  // policy's numeric address through the proxy, including its loopback override.
  const url = new URL('http://screenshot.invalid:' + target.address().port + '/');
  const policy = await targetPolicy(url, false, { resolve: async () => ['127.0.0.1'] });
  await ensureBrowser();
  t.after(closeBrowser);
  const result = await renderViewport(url, policy, { id: 'security', label: 'Security', width: 390, height: 844, mobile: false }, 500);
  assert.equal(result.status, 200);
  assert.equal(result.title, 'Security regression fixture');
  assert.ok(Buffer.from(result.screenshot, 'base64').subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])));
  assert.ok(allowedHits >= 1);
  assert.equal(forbiddenHits, 0, 'private HTTP service was contacted');
  assert.equal(socketHits, 0, 'private WebSocket service was contacted');
  await assert.rejects(renderViewport(new URL('/redirect', url), policy, { id: 'redirect', width: 390, height: 844, mobile: false }, 0), /已阻止/);
  assert.equal(forbiddenHits, 0, 'private redirect destination was contacted');
});
