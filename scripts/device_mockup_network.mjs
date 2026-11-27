import http from 'node:http';
import net from 'node:net';
import { lookup } from 'node:dns/promises';

export class RenderError extends Error {}

function hostnameOf(url) {
  return url.hostname.replace(/^\[|\]$/g, '').replace(/\.$/, '').toLowerCase();
}

function ipv6Words(address) {
  let value = address.toLowerCase();
  if (value.includes('%')) return null;
  if (value.includes('.')) {
    const colon = value.lastIndexOf(':');
    const octets = value.slice(colon + 1).split('.').map(Number);
    if (octets.length !== 4 || octets.some(n => !Number.isInteger(n) || n < 0 || n > 255)) return null;
    value = value.slice(0, colon + 1) + ((octets[0] << 8) | octets[1]).toString(16) + ':' + ((octets[2] << 8) | octets[3]).toString(16);
  }
  const sides = value.split('::');
  if (sides.length > 2) return null;
  const left = sides[0] ? sides[0].split(':') : [];
  const right = sides[1] ? sides[1].split(':') : [];
  const fill = sides.length === 2 ? 8 - left.length - right.length : 0;
  const parts = [...left, ...Array(Math.max(0, fill)).fill('0'), ...right];
  if (parts.length !== 8) return null;
  const words = parts.map(part => /^[0-9a-f]{1,4}$/.test(part) ? parseInt(part, 16) : NaN);
  return words.some(Number.isNaN) ? null : words;
}

function mappedIPv4(address) {
  const words = ipv6Words(address);
  if (words && words.slice(0, 5).every(n => n === 0) && words[5] === 0xffff) {
    return [words[6] >> 8, words[6] & 255, words[7] >> 8, words[7] & 255].join('.');
  }
  return address;
}

export function isPrivateAddress(value) {
  const address = mappedIPv4(value);
  if (net.isIP(address) === 4) {
    const [a, b, c] = address.split('.').map(Number);
    return a === 0 || a === 10 || a === 127 || a >= 224 || address === '168.63.129.16'
      || (a === 100 && b >= 64 && b <= 127) || (a === 169 && b === 254)
      || (a === 172 && b >= 16 && b <= 31)
      || (a === 192 && (b === 168 || (b === 0 && (c === 0 || c === 2)) || (b === 88 && c === 99)))
      || (a === 198 && (b === 18 || b === 19 || (b === 51 && c === 100)))
      || (a === 203 && b === 0 && c === 113);
  }
  const words = ipv6Words(address);
  if (!words || !net.isIP(address)) return true;
  // Reject transition networks as well as private/reserved ranges; a public
  // looking 6to4, Teredo or NAT64 address can embed a private IPv4 destination.
  return (words[0] & 0xe000) !== 0x2000
    || (words[0] === 0x2001 && (words[1] < 0x200 || words[1] === 0xdb8))
    || words[0] === 0x2002 || (words[0] === 0x3fff && words[1] < 0x1000);
}

function isLoopbackAddress(address) {
  address = mappedIPv4(address);
  if (net.isIP(address) === 4) return address.startsWith('127.');
  const words = ipv6Words(address);
  return Boolean(words && words.slice(0, 7).every(n => n === 0) && words[7] === 1);
}

function isLocalAddress(value) {
  const address = mappedIPv4(value);
  if (isLoopbackAddress(address)) return true;
  if (net.isIP(address) === 4) {
    const [a, b] = address.split('.').map(Number);
    return a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168);
  }
  const words = ipv6Words(address);
  return Boolean(words && net.isIP(address) && (words[0] & 0xfe00) === 0xfc00);
}

export function isMetadata(hostname, addresses) {
  return ['metadata', 'metadata.google.internal', 'instance-data', 'instance-data.ec2.internal'].includes(hostname)
    || addresses.some(value => {
      const address = mappedIPv4(value);
      const words = ipv6Words(address);
      return ['169.254.169.254', '169.254.170.2', '100.100.100.200', '168.63.129.16'].includes(address)
        || Boolean(words && words[0] === 0xfd00 && words[1] === 0xec2 && words.slice(2, 7).every(n => n === 0) && words[7] === 0x254);
    });
}

async function resolveAddresses(hostname) {
  if (net.isIP(hostname)) return [hostname];
  let timer;
  try {
    const records = await Promise.race([
      lookup(hostname, { all: true, verbatim: true }),
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('DNS timeout')), 5000); }),
    ]);
    if (!records.length) throw new Error('Empty DNS result');
    return records.map(record => record.address);
  } catch {
    throw new RenderError('无法解析网站域名，请检查地址和网络连接。');
  } finally {
    clearTimeout(timer);
  }
}

function isToolAddress(url, addresses, toolPort) {
  return Number(url.port || (url.protocol === 'https:' ? 443 : 80)) === toolPort && addresses.some(isLoopbackAddress);
}

export async function targetPolicy(target, publicOnly = false, { resolve = resolveAddresses, toolPort = 4173 } = {}) {
  const hostname = hostnameOf(target);
  const addresses = await resolve(hostname);
  if (!addresses.length || addresses.some(a => !net.isIP(a))) throw new RenderError('网站域名没有可用地址。');
  if (isMetadata(hostname, addresses)) throw new RenderError('此地址属于云服务元数据端点，无法截图。');
  if (addresses.some(a => isPrivateAddress(a) && !isLocalAddress(a))) throw new RenderError('不能访问保留地址或地址转换网络。');
  if (isToolAddress(target, addresses, toolPort)) throw new RenderError('请输入要截图的网站地址，不能使用截图工具自身的地址。');
  if (publicOnly && addresses.some(isPrivateAddress)) throw new RenderError('在线截图仅支持公开网站，请输入可从互联网访问的地址。');
  return { localOrigin: addresses.some(isPrivateAddress) ? target.origin : null, localAddresses: new Set(addresses), resolve, toolPort };
}

export function requestGuard(policy) {
  const dnsCache = new Map();
  return async value => {
    let url;
    try { url = new URL(value); } catch { return { allowed: false, reason: '网站请求了无效的资源地址。' }; }
    if (['data:', 'blob:', 'about:'].includes(url.protocol)) return { allowed: true };
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return { allowed: false, reason: '已阻止非 HTTP(S) 的网站资源。' };
    const hostname = hostnameOf(url);
    try {
      if (!dnsCache.has(hostname)) {
        if (dnsCache.size >= 256) throw new RenderError('网站请求的域名过多。');
        dnsCache.set(hostname, policy.resolve(hostname));
      }
      const addresses = await dnsCache.get(hostname);
      if (!addresses.length || addresses.some(a => !net.isIP(a))) throw new RenderError('网站域名没有可用地址。');
      if (isMetadata(hostname, addresses)) throw new RenderError('已阻止网站访问云服务元数据地址。');
      if (addresses.some(a => isPrivateAddress(a) && !isLocalAddress(a))) throw new RenderError('已阻止网站访问保留地址或地址转换网络。');
      if (isToolAddress(url, addresses, policy.toolPort)) throw new RenderError('已阻止网站跳转或请求截图工具自身。');
      if (addresses.some(isPrivateAddress) && (url.origin !== policy.localOrigin || addresses.some(a => !policy.localAddresses.has(a)))) {
        throw new RenderError('已阻止网站访问其他本机或内网地址。输入该本地站点的完整地址可直接为它截图。');
      }
      return { allowed: true, addresses };
    } catch (error) {
      return { allowed: false, reason: error.message };
    }
  };
}

// The browser never resolves or connects to a destination directly. Both HTTP
// and HTTPS CONNECT use numeric addresses returned by the same policy check.
export async function connectChecked(addresses, port, connect = net.createConnection, signal) {
  for (const address of addresses) {
    if (signal?.aborted) throw new Error('Connection canceled');
    if (!net.isIP(address)) throw new Error('Expected a checked IP address');
    try {
      return await new Promise((resolve, reject) => {
        const socket = connect({ host: address, port, family: net.isIP(address) });
        const abort = () => socket.destroy(new Error('Connection canceled'));
        const cleanup = () => signal?.removeEventListener('abort', abort);
        signal?.addEventListener('abort', abort, { once: true });
        socket.setTimeout(5000, () => socket.destroy(new Error('Connection timeout')));
        socket.once('error', error => { cleanup(); reject(error); });
        socket.once('connect', () => { cleanup(); socket.setTimeout(30000); resolve(socket); });
        if (signal?.aborted) abort();
      });
    } catch { /* Try the next already-checked address, never resolve again. */ }
  }
  throw new Error('Connection unavailable');
}

function cleanHeaders(headers) {
  const drop = new Set(['connection', 'proxy-connection', 'proxy-authorization', 'proxy-authenticate', 'keep-alive', 'upgrade', 'te', 'trailer', 'transfer-encoding']);
  for (const name of String(headers.connection || '').split(',')) drop.add(name.trim().toLowerCase());
  return Object.fromEntries(Object.entries(headers).filter(([name]) => !drop.has(name.toLowerCase())));
}

export async function createPinnedProxy(guard, { requestTimeoutMs = 30000, tunnelTimeoutMs = 10 * 60 * 1000 } = {}) {
  if (![requestTimeoutMs, tunnelTimeoutMs].every(value => Number.isSafeInteger(value) && value > 0)) throw new Error('Invalid proxy deadline');
  const sockets = new Set();
  const operations = new Set();
  let closed = false;
  // A single HTTP/1 connection can pipeline many requests. The socket limit
  // alone does not bound pending DNS lookups, dials, or upstream responses.
  const startOperation = (timeout, cancel) => {
    if (closed || operations.size >= 32) return null;
    const controller = new AbortController();
    let settingUp = true, finished = false;
    const release = () => { if (finished && !settingUp) operations.delete(operation); };
    const operation = {
      signal: controller.signal,
      finish() {
        if (!finished) {
          finished = true;
          clearTimeout(timer);
          controller.abort();
        }
        release();
      },
      cancel() { operation.finish(); cancel(); },
      ready() { settingUp = false; release(); },
    };
    const timer = setTimeout(() => operation.cancel(), timeout);
    timer.unref();
    operations.add(operation);
    return operation;
  };
  const track = socket => {
    sockets.add(socket);
    socket.once('close', () => sockets.delete(socket));
    socket.on('error', () => socket.destroy());
    socket.setTimeout(30000, () => socket.destroy());
    return socket;
  };
  const dial = async (target, operation) => {
    const decision = await guard(target.href);
    if (closed || operation.signal.aborted || !decision.allowed || !decision.addresses?.length) throw new Error('Destination blocked');
    const socket = track(await connectChecked(decision.addresses, Number(target.port || (target.protocol === 'https:' ? 443 : 80)), net.createConnection, operation.signal));
    if (closed || operation.signal.aborted) { socket.destroy(); throw new Error('Proxy closed'); }
    return socket;
  };
  const server = http.createServer(async (request, response) => {
    let upstream, socket, agent;
    const operation = startOperation(requestTimeoutMs, () => response.destroy());
    if (!operation) { response.writeHead(503, { Connection: 'close' }); response.end(); return; }
    response.once('close', () => { operation.finish(); upstream?.destroy(); agent?.destroy(); socket?.destroy(); });
    try {
      const target = new URL(request.url);
      if (target.protocol !== 'http:' || target.username || target.password || target.hash) throw new Error('Invalid proxy URL');
      socket = await dial(target, operation);
      if (request.destroyed || response.destroyed) { socket.destroy(); return; }
      agent = new http.Agent({ keepAlive: false });
      agent.createConnection = () => socket;
      const headers = cleanHeaders(request.headers);
      headers.host = target.host;
      upstream = http.request(target, { method: request.method, headers, agent }, remote => {
        let received = 0;
        remote.on('error', () => response.destroy());
        remote.on('data', chunk => { received += chunk.length; if (received > 32 * 1024 * 1024) { upstream.destroy(); response.destroy(); } });
        response.writeHead(remote.statusCode, cleanHeaders(remote.headers));
        remote.pipe(response);
      });
      let sent = 0;
      request.on('data', chunk => { sent += chunk.length; if (sent > 1024 * 1024) { upstream.destroy(); response.destroy(); } });
      upstream.on('error', () => { if (!response.headersSent) response.writeHead(502); response.end(); });
      request.pipe(upstream);
    } catch {
      socket?.destroy();
      if (!response.headersSent) response.writeHead(403);
      response.end();
    } finally {
      operation.ready();
    }
  });
  server.on('connect', async (request, client, head) => {
    let remote;
    const operation = startOperation(tunnelTimeoutMs, () => client.destroy());
    if (!operation) { client.end('HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\n\r\n'); return; }
    client.once('close', () => { operation.finish(); remote?.destroy(); });
    try {
      const target = new URL('https://' + request.url);
      if (target.username || target.password || target.pathname !== '/' || target.search || target.hash || !target.hostname) throw new Error('Invalid tunnel');
      remote = await dial(target, operation);
      if (client.destroyed) { remote.destroy(); return; }
      let transferred = head.length;
      const bound = chunk => { transferred += chunk.length; if (transferred > 64 * 1024 * 1024) { client.destroy(); remote.destroy(); } };
      client.on('data', bound);
      remote.on('data', bound);
      remote.once('close', () => client.destroy());
      client.write('HTTP/1.1 200 Connection Established\r\n\r\n');
      if (head.length) remote.write(head);
      client.pipe(remote).pipe(client);
    } catch {
      remote?.destroy();
      client.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n');
    } finally {
      operation.ready();
    }
  });
  server.on('connection', track);
  server.on('upgrade', (_request, socket) => socket.destroy());
  server.on('clientError', (_error, socket) => socket.destroy());
  server.maxConnections = 32;
  server.headersTimeout = 10000;
  server.requestTimeout = 30000;
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  return {
    url: 'http://127.0.0.1:' + server.address().port,
    async close() {
      closed = true;
      for (const operation of operations) operation.cancel();
      for (const socket of sockets) socket.destroy();
      await new Promise(resolve => server.close(resolve));
    },
  };
}
