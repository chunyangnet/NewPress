const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const net = require('node:net');
const { EventEmitter } = require('node:events');
const network = import('../../scripts/device_mockup_network.mjs');

async function listen(server) {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return server.address().port;
}
function close(server) { return new Promise(resolve => server.close(resolve)); }
function request(proxy, target, options = {}) {
  const url = new URL(proxy);
  return new Promise((resolve, reject) => {
    const req = http.request({ hostname: url.hostname, port: url.port, path: target, method: 'GET', ...options }, res => {
      let body = '';
      res.setEncoding('utf8');
      res.on('data', part => body += part);
      res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body }));
      res.on('error', reject);
    });
    req.on('error', reject);
    req.end();
  });
}

test('screenshot address policy rejects reserved and IPv6 transition networks', async () => {
  const { isPrivateAddress, isMetadata } = await network;
  for (const address of ['127.0.0.1', '10.0.0.1', '169.254.169.254', '168.63.129.16', '192.88.99.1', '100.64.0.1',
    '198.18.0.1', '192.0.2.1', '::1', '::ffff:127.0.0.1', '::8.8.8.8', '64:ff9b::808:808',
    '2002:7f00:1::', '2001::1', '2001:db8::1', '3fff::1', 'fd00:ec2::254', 'fe80::1%eth0']) {
    assert.equal(isPrivateAddress(address), true, address);
  }
  for (const address of ['8.8.8.8', '1.1.1.1', '2606:4700:4700::1111', '::ffff:8.8.8.8']) {
    assert.equal(isPrivateAddress(address), false, address);
  }
  for (const address of ['::ffff:169.254.169.254', 'fd00:ec2:0:0:0:0:0:254', '168.63.129.16']) {
    assert.equal(isMetadata('example.test', [address]), true, address);
  }
});

test('public screenshot policy rejects mixed DNS and local DNS rebinding', async () => {
  const { targetPolicy, requestGuard } = await network;
  const url = new URL('http://target.test:8080');
  await assert.rejects(targetPolicy(url, true, { resolve: async () => ['8.8.8.8', '127.0.0.1'] }));
  for (const address of ['2002:7f00:1::', '64:ff9b::a9fe:a9fe', 'fd00:ec2::254', '0.0.0.0', '169.254.169.254']) {
    await assert.rejects(targetPolicy(url, false, { resolve: async () => [address] }));
  }
  let answers = ['127.0.0.1'];
  const policy = await targetPolicy(url, false, { resolve: async () => answers });
  answers = ['10.0.0.8'];
  assert.equal((await requestGuard(policy)(url.href)).allowed, false);
  answers = ['127.0.0.1'];
  const guard = requestGuard(policy);
  assert.equal((await guard(url.href)).allowed, true);
  assert.equal((await guard('http://target.test:8081/')).allowed, false);
  assert.equal((await guard('https://target.test:8080/')).allowed, false);
});

test('dialing uses checked numeric addresses without a second DNS lookup', async () => {
  const { connectChecked } = await network;
  const dialed = [];
  const socket = await connectChecked(['8.8.8.8'], 443, options => {
    dialed.push(options);
    const connection = new EventEmitter();
    connection.setTimeout = () => connection;
    connection.destroy = () => connection;
    queueMicrotask(() => connection.emit('connect'));
    return connection;
  });
  assert.ok(socket);
  assert.deepEqual(dialed, [{ host: '8.8.8.8', port: 443, family: 4 }]);
  await assert.rejects(connectChecked(['rebound.test'], 443), /checked IP/);
});

test('proxy pins DNS, preserves origin Host, and blocks unapproved hosts and redirects', async t => {
  const { targetPolicy, requestGuard, createPinnedProxy } = await network;
  let hits = 0, forbiddenHits = 0;
  const forbidden = http.createServer((_req, res) => { forbiddenHits++; res.end('private'); });
  const forbiddenPort = await listen(forbidden);
  t.after(() => close(forbidden));
  const target = http.createServer((req, res) => {
    hits++;
    assert.equal(req.headers['proxy-authorization'], undefined);
    if (req.url === '/redirect') {
      res.writeHead(302, { Location: 'http://127.0.0.1:' + forbiddenPort + '/' });
      res.end();
    } else res.end(req.headers.host);
  });
  const port = await listen(target);
  t.after(() => close(target));
  const origin = 'http://never-resolve-in-browser.invalid:' + port;
  const policy = await targetPolicy(new URL(origin), false, { resolve: async () => ['127.0.0.1'] });
  const proxy = await createPinnedProxy(requestGuard(policy));
  t.after(() => proxy.close());
  const response = await request(proxy.url, origin + '/', { headers: { 'Proxy-Authorization': 'secret' } });
  assert.equal(response.status, 200);
  assert.equal(response.body, 'never-resolve-in-browser.invalid:' + port);
  const redirect = await request(proxy.url, origin + '/redirect');
  assert.equal(redirect.status, 302);
  assert.equal((await request(proxy.url, redirect.headers.location)).status, 403);
  assert.equal((await request(proxy.url, 'http://other.invalid:' + port + '/')).status, 403);
  assert.equal((await request(proxy.url, 'http://user:password@never-resolve-in-browser.invalid:' + port + '/')).status, 403);
  assert.equal(hits, 2);
  assert.equal(forbiddenHits, 0);
});

test('HTTPS CONNECT obeys the same destination boundary and closes tunnels', async t => {
  const { targetPolicy, requestGuard, createPinnedProxy } = await network;
  let hits = 0;
  const target = net.createServer(socket => {
    hits++;
    // Deliberate tunnel shutdown may reset the fixture's peer on Windows.
    socket.on('error', error => assert.equal(error.code, 'ECONNRESET'));
    socket.pipe(socket);
  });
  const port = await listen(target);
  t.after(() => close(target));
  const policy = await targetPolicy(new URL('https://tunnel.invalid:' + port), false, { resolve: async () => ['127.0.0.1'] });
  const proxy = await createPinnedProxy(requestGuard(policy));
  t.after(() => proxy.close());
  const endpoint = new URL(proxy.url);
  const tunnel = host => new Promise((resolve, reject) => {
    const req = http.request({ hostname: endpoint.hostname, port: endpoint.port, method: 'CONNECT', path: host });
    req.on('connect', (res, socket) => resolve({ res, socket }));
    req.on('error', reject);
    req.end();
  });
  const allowed = await tunnel('tunnel.invalid:' + port);
  assert.equal(allowed.res.statusCode, 200);
  const echoed = new Promise(resolve => allowed.socket.once('data', data => resolve(data.toString())));
  allowed.socket.write('tunnel bytes');
  assert.equal(await echoed, 'tunnel bytes');
  allowed.socket.destroy();
  const blocked = await tunnel('other.invalid:' + port);
  assert.equal(blocked.res.statusCode, 403);
  blocked.socket.destroy();
  assert.equal(hits, 1);
});

test('pipelined requests cannot bypass the proxy operation limit', { timeout: 5000 }, async t => {
  const { targetPolicy, requestGuard, createPinnedProxy } = await network;
  let hits = 0;
  const responses = new Set();
  const target = http.createServer((_req, res) => { hits++; responses.add(res); });
  const port = await listen(target);
  const policy = await targetPolicy(new URL('http://pipeline.invalid:' + port), false, { resolve: async () => ['127.0.0.1'] });
  const proxy = await createPinnedProxy(requestGuard(policy));
  const endpoint = new URL(proxy.url);
  const client = net.createConnection({ host: endpoint.hostname, port: endpoint.port });
  client.on('error', () => {});
  t.after(async () => {
    client.destroy();
    await proxy.close();
    for (const response of responses) response.destroy();
    target.closeAllConnections();
    await close(target);
  });
  await new Promise(resolve => client.once('connect', resolve));
  client.write(Array.from({ length: 48 }, () => `GET http://pipeline.invalid:${port}/ HTTP/1.1\r\nHost: pipeline.invalid:${port}\r\n\r\n`).join(''));
  await new Promise(resolve => setTimeout(resolve, 300));
  assert.ok(hits > 0, 'fixture never reached the allowed server');
  assert.ok(hits <= 32, `one client created ${hits} concurrent upstream requests`);
});

test('a continuously streaming HTTP response still reaches its absolute deadline', { timeout: 5000 }, async t => {
  const { targetPolicy, requestGuard, createPinnedProxy } = await network;
  let targetClosed;
  const closed = new Promise(resolve => { targetClosed = resolve; });
  const target = http.createServer((_req, res) => {
    res.writeHead(200);
    const timer = setInterval(() => res.write('x'), 10);
    res.once('close', () => { clearInterval(timer); targetClosed(); });
  });
  const port = await listen(target);
  const policy = await targetPolicy(new URL('http://stream.invalid:' + port), false, { resolve: async () => ['127.0.0.1'] });
  const proxy = await createPinnedProxy(requestGuard(policy), { requestTimeoutMs: 100 });
  t.after(async () => { await proxy.close(); target.closeAllConnections(); await close(target); });
  const endpoint = new URL(proxy.url);
  const req = http.get({ hostname: endpoint.hostname, port: endpoint.port, path: `http://stream.invalid:${port}/` }, res => {
    res.on('error', () => {});
    res.resume();
  });
  req.on('error', () => {});
  t.after(() => req.destroy());
  const completed = await Promise.race([closed.then(() => true), new Promise(resolve => setTimeout(() => resolve(false), 700))]);
  assert.equal(completed, true, 'continuous data evaded the response deadline');
});

test('an active CONNECT tunnel still reaches its absolute deadline', { timeout: 5000 }, async t => {
  const { targetPolicy, requestGuard, createPinnedProxy } = await network;
  const target = net.createServer(socket => {
    socket.on('error', error => assert.equal(error.code, 'ECONNRESET'));
    socket.pipe(socket);
  });
  const port = await listen(target);
  const policy = await targetPolicy(new URL('https://deadline.invalid:' + port), false, { resolve: async () => ['127.0.0.1'] });
  const proxy = await createPinnedProxy(requestGuard(policy), { tunnelTimeoutMs: 100 });
  t.after(async () => { await proxy.close(); await close(target); });
  const endpoint = new URL(proxy.url);
  const socket = await new Promise((resolve, reject) => {
    const req = http.request({ hostname: endpoint.hostname, port: endpoint.port, method: 'CONNECT', path: 'deadline.invalid:' + port });
    req.on('connect', (res, connection) => {
      assert.equal(res.statusCode, 200);
      resolve(connection);
    });
    req.on('error', reject);
    req.end();
  });
  socket.on('error', () => {});
  socket.resume();
  const closed = new Promise(resolve => socket.once('close', resolve));
  const timer = setInterval(() => socket.write('keep active'), 10);
  t.after(() => { clearInterval(timer); socket.destroy(); });
  const completed = await Promise.race([closed.then(() => true), new Promise(resolve => setTimeout(() => resolve(false), 700))]);
  assert.equal(completed, true, 'continuous tunnel data evaded the deadline');
});

test('completed and rejected requests release proxy operation slots', async t => {
  const { targetPolicy, requestGuard, createPinnedProxy } = await network;
  const target = http.createServer((_req, res) => res.end('ok'));
  const port = await listen(target);
  const origin = 'http://release.invalid:' + port;
  const policy = await targetPolicy(new URL(origin), false, { resolve: async () => ['127.0.0.1'] });
  const proxy = await createPinnedProxy(requestGuard(policy));
  t.after(async () => { await proxy.close(); await close(target); });
  for (let index = 0; index < 40; index++) {
    assert.equal((await request(proxy.url, 'http://other.invalid:' + port + '/')).status, 403);
    assert.equal((await request(proxy.url, origin + '/')).body, 'ok');
  }
});
