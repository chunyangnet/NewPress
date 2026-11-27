import http from 'node:http';
import path from 'node:path';
import { readFile } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { chromium, devices } from 'playwright';
import { RenderError, targetPolicy, requestGuard, createPinnedProxy } from './device_mockup_network.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const pagePath = path.join(root, 'docs', 'DEVICE_MOCKUP_STUDIO.html');
const host = '127.0.0.1';
const port = Number(process.env.DEVICE_MOCKUP_PORT || 4173);
const maxBodyBytes = 4096;
const viewports = [
  { id: 'phone', label: '手机', width: 390, height: 844, mobile: true, device: devices['iPhone 13'] },
  { id: 'tablet', label: '平板', width: 768, height: 1024, mobile: true, device: devices['iPad (gen 7)'] },
  { id: 'laptop', label: '笔记本', width: 1440, height: 900, mobile: false },
  { id: 'desktop', label: '桌面显示器', width: 1920, height: 1080, mobile: false },
];
const localOrigins = new Set(['http://localhost:' + port, 'http://127.0.0.1:' + port]);
const localHosts = new Set(['localhost:' + port, '127.0.0.1:' + port]);
if (port === 80) {
  localOrigins.add('http://localhost');
  localOrigins.add('http://127.0.0.1');
  localHosts.add('localhost');
  localHosts.add('127.0.0.1');
}

let browser;
let browserLaunch;
let rendering = false;
const liveSessions = new Map();
const liveFrameInterval = 1000;

export async function ensureBrowser() {
  if (browser?.isConnected()) return;
  browserLaunch ??= chromium.launch({ headless: true, chromiumSandbox: true, args: ['--force-webrtc-ip-handling-policy=disable_non_proxied_udp', '--disable-quic'] }).then((instance) => { browser = instance; });
  try { await browserLaunch; } finally { browserLaunch = null; }
}

export async function closeBrowser() {
  await browser?.close();
  browser = undefined;
}

function sendLive(session, type, payload = {}) {
  if (session.closed || session.response.destroyed || session.response.writableEnded) return false;
  // Drop new frames while the client is behind, keeping the stream's memory bounded.
  if (type === 'frame' && (session.paused || session.response.writableNeedDrain)) return false;
  session.response.write(JSON.stringify({ type, ...payload }) + '\n');
  return true;
}

async function closeLive(session) {
  if (session.closed) return;
  session.closed = true;
  session.controller.abort();
  clearInterval(session.heartbeat);
  clearTimeout(session.expiry);
  liveSessions.delete(session.id);
  await Promise.allSettled([...session.contexts].map((context) => context.close()));
  session.contexts.clear();
}

function settleLiveDevice(session, viewport, error = '') {
  if (session.closed) return;
  session.settled.set(viewport.id, !error);
  if (error) sendLive(session, 'device-error', { id: viewport.id, error });
  if (session.settled.size === viewports.length && !session.initialized) {
    session.initialized = true;
    sendLive(session, 'ready', { count: [...session.settled.values()].filter(Boolean).length });
  }
  if (session.settled.size === viewports.length && ![...session.settled.values()].some(Boolean)) {
    sendLive(session, 'error', { error: '所有设备的预览均已中断，请检查网址后重新连接。' });
    session.response.end();
    void closeLive(session);
  }
}

function json(response, status, payload) {
  if (response.destroyed || response.writableEnded) return;
  response.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Cache-Control': 'no-store',
    'X-Content-Type-Options': 'nosniff',
  });
  response.end(JSON.stringify(payload));
}

function checkClient(request) {
  const remote = request.socket.remoteAddress;
  if (!['127.0.0.1', '::1', '::ffff:127.0.0.1'].includes(remote)
    || !localHosts.has((request.headers.host || '').toLowerCase())) {
    return '仅允许通过本机地址访问截图工具。';
  }
  if (request.headers.origin && !localOrigins.has(request.headers.origin)) {
    return '来源校验失败，请从本工具页面发起截图。';
  }
  if (request.method === 'POST' && request.headers['sec-fetch-site'] === 'cross-site') {
    return '来源校验失败，请从本工具页面发起截图。';
  }
  return '';
}

function readBody(request) {
  return new Promise((resolve, reject) => {
    let body = '';
    let size = 0;
    let tooLarge = false;
    request.setEncoding('utf8');
    request.on('data', (chunk) => {
      size += Buffer.byteLength(chunk);
      if (size > maxBodyBytes) {
        tooLarge = true;
        body = '';
      } else if (!tooLarge) {
        body += chunk;
      }
    });
    request.on('end', () => tooLarge ? reject(new RenderError('请求内容过大。')) : resolve(body));
    request.on('aborted', () => reject(new RenderError('请求已中断。')));
    request.on('error', reject);
  });
}

function parseUrl(value) {
  if (typeof value !== 'string' || value.length > 2048) return null;
  try {
    const url = new URL(value);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return null;
    return url;
  } catch {
    return null;
  }
}

function friendlyError(error) {
  if (error instanceof RenderError) return error.message;
  const message = String(error?.message || '');
  if (/ERR_NAME_NOT_RESOLVED|DNS/i.test(message)) return '无法解析网站域名，请检查地址和网络连接。';
  if (/ERR_CONNECTION_REFUSED/i.test(message)) return '网站拒绝连接，请确认网站或本地开发服务已启动。';
  if (/ERR_CERT|ERR_SSL/i.test(message)) return '网站的 HTTPS 证书未通过验证，请修复证书或使用可访问的地址。';
  if (/timeout|timed out/i.test(message)) return '网站加载超时，请检查网络或稍后重试。';
  if (/browser.*closed|Target page.*closed|browser.*disconnected/i.test(message)) return '渲染浏览器已断开，请重启截图工具。';
  if (/ERR_|connection|network/i.test(message)) return '无法连接网站，请检查网址和网络连接。';
  if (/Execution context|navigation/i.test(message)) return '网站跳转尚未稳定，请稍后重试。';
  return '页面加载失败，请检查网址或稍后重试。';
}

export async function renderViewport(target, policy, viewport, waitMs, live = null) {
  const guard = requestGuard(policy);
  const proxy = await createPinnedProxy(guard);
  const contextOptions = {
    proxy: { server: proxy.url, bypass: '<-loopback>' },
    viewport: { width: viewport.width, height: viewport.height },
    deviceScaleFactor: 1,
    locale: 'zh-CN',
    colorScheme: 'light',
    isMobile: viewport.mobile,
    hasTouch: viewport.mobile,
    serviceWorkers: 'block',
    acceptDownloads: false,
  };
  if (viewport.device) {
    contextOptions.userAgent = viewport.device.userAgent;
    contextOptions.screen = { width: viewport.width, height: viewport.height };
  }

  let context;
  try { context = await browser.newContext(contextOptions); }
  catch (error) { await proxy.close(); throw error; }
  live?.contexts.add(context);
  const blockedRequests = new Map();
  let page;
  let blockedNavigation = '';
  let redirectSession;
  try {
    if (live?.closed) return;
    await context.routeWebSocket('**/*', socket => socket.close());
    await context.route('**/*', async (route) => {
      const request = route.request();
      const decision = await guard(request.url());
      if (decision.allowed) return route.continue().catch(() => {});
      const reason = decision.reason || '已阻止不受支持的网站请求。';
      blockedRequests.set(reason, (blockedRequests.get(reason) || 0) + 1);
      if (request.isNavigationRequest() && request.frame() === page?.mainFrame()) blockedNavigation = reason;
      return route.abort('blockedbyclient').catch(() => {});
    });
    page = await context.newPage();
    // Playwright continues redirected requests without invoking context.route again.
    // Inspect each redirect response before Chromium follows its Location header.
    redirectSession = await context.newCDPSession(page);
    const { frameTree } = await redirectSession.send('Page.getFrameTree');
    await redirectSession.send('Fetch.enable', { patterns: [{ urlPattern: '*', requestStage: 'Response' }] });
    redirectSession.on('Fetch.requestPaused', async (event) => {
      try {
        const location = event.responseHeaders?.find((header) => header.name.toLowerCase() === 'location')?.value;
        if (event.responseStatusCode >= 300 && event.responseStatusCode < 400 && location) {
          const decision = await guard(new URL(location, event.request.url).href);
          if (!decision.allowed) {
            const reason = decision.reason || '网站跳转到了无法访问的地址。';
            blockedRequests.set(reason, (blockedRequests.get(reason) || 0) + 1);
            if (event.resourceType === 'Document' && event.frameId === frameTree.frame.id) blockedNavigation = reason;
            await redirectSession.send('Fetch.failRequest', { requestId: event.requestId, errorReason: 'BlockedByClient' });
            return;
          }
        }
        await redirectSession.send('Fetch.continueResponse', { requestId: event.requestId });
      } catch {
        await redirectSession.send('Fetch.failRequest', { requestId: event.requestId, errorReason: 'Aborted' }).catch(() => {});
      }
    });
    page.setDefaultNavigationTimeout(30000);
    page.setDefaultTimeout(10000);
    page.on('dialog', (dialog) => dialog.dismiss().catch(() => {}));
    page.on('popup', (popup) => popup.close().catch(() => {}));
    let response;
    let previousFrame;
    let previousEpoch = -1;
    const publishFrame = async () => {
      if (live.closed || live.paused || live.response.writableNeedDrain) return;
      if (blockedNavigation) throw new RenderError(blockedNavigation);
      const screenshot = await page.screenshot({ type: 'png', animations: 'allow', caret: 'initial', timeout: 15000 });
      if (blockedNavigation) throw new RenderError(blockedNavigation);
      if ((previousEpoch !== live.frameEpoch || !previousFrame?.equals(screenshot)) && sendLive(live, 'frame', {
        id: viewport.id, width: viewport.width, height: viewport.height,
        screenshot: screenshot.toString('base64'), capturedAt: Date.now(),
      })) { previousFrame = screenshot; previousEpoch = live.frameEpoch; }
    };
    try {
      response = await page.goto(target.href, { waitUntil: 'domcontentloaded', timeout: 30000 });
      if (live) await publishFrame();
      await page.waitForTimeout(Math.max(400, waitMs));
      await page.evaluate(async () => {
        const visibleImages = Array.from(document.images).filter((image) => {
          const rect = image.getBoundingClientRect();
          return rect.bottom > 0 && rect.right > 0 && rect.top < innerHeight && rect.left < innerWidth;
        });
        const imagesReady = visibleImages.map((image) => image.complete
          ? image.decode().catch(() => {})
          : new Promise((resolve) => {
            image.addEventListener('load', resolve, { once: true });
            image.addEventListener('error', resolve, { once: true });
          }));
        await Promise.race([
          Promise.all([document.fonts?.ready ?? Promise.resolve(), ...imagesReady]),
          new Promise((resolve) => setTimeout(resolve, 4500)),
        ]);
        await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      });
    } catch (error) {
      if (blockedNavigation) throw new RenderError(blockedNavigation);
      throw error;
    }
    if (blockedNavigation) throw new RenderError(blockedNavigation);
    const status = response?.status() ?? null;
    const warnings = [];
    if (status >= 400) warnings.push('网站返回 HTTP ' + status + '，当前展示的是该错误页面。');
    const blockedCount = Array.from(blockedRequests.values()).reduce((total, count) => total + count, 0);
    if (blockedCount) warnings.push('有 ' + blockedCount + ' 个请求未加载：' + Array.from(blockedRequests.keys()).join(' '));
    const metadata = {
      id: viewport.id,
      label: viewport.label,
      width: viewport.width,
      height: viewport.height,
      title: await page.title().catch(() => ''),
      finalUrl: page.url(),
      status,
      warnings,
      blockedRequests: blockedCount,
    };
    if (!live) {
      const screenshot = await page.screenshot({ type: 'png', animations: 'disabled', timeout: 15000 });
      return { ...metadata, screenshot: screenshot.toString('base64') };
    }
    await publishFrame();
    sendLive(live, 'device-ready', metadata);
    settleLiveDevice(live, viewport);
    while (!live.closed) {
      await delay(liveFrameInterval, undefined, { signal: live.controller.signal });
      await publishFrame();
    }
  } finally {
    await redirectSession?.detach().catch(() => {});
    await context.close().catch(() => {});
    live?.contexts.delete(context);
    await proxy.close();
  }
}

async function startLive(request, response) {
  if (!/^application\/json(?:\s*;|$)/i.test(request.headers['content-type'] || '')) {
    return json(response, 415, { error: '预览请求必须使用 JSON 数据。' });
  }
  let payload;
  try { payload = JSON.parse(await readBody(request)); }
  catch (error) { return json(response, 400, { error: error instanceof RenderError ? error.message : '请求数据无效。' }); }
  const target = parseUrl(payload?.url);
  if (!target) return json(response, 400, { error: '请输入有效的 HTTP 或 HTTPS 网站地址。' });
  if (liveSessions.size >= 3) return json(response, 409, { error: '已打开三个实时预览，请先关闭一个预览页面。' });
  const requestedWait = Number(payload?.waitMs);
  const waitMs = Number.isFinite(requestedWait) ? Math.max(0, Math.min(5000, Math.round(requestedWait))) : 1000;
  const session = {
    id: randomUUID(), response, controller: new AbortController(),
    contexts: new Set(), settled: new Map(), paused: false, closed: false, initialized: false, frameEpoch: 0,
  };
  liveSessions.set(session.id, session);
  session.expiry = setTimeout(() => {
    sendLive(session, 'error', { error: '预览已达到十分钟时限，请重新连接。' });
    response.end();
    void closeLive(session);
  }, 10 * 60 * 1000);
  response.once('close', () => { void closeLive(session); });
  try {
    const policy = await targetPolicy(target, request.headers['x-newpress-mockup-scope'] === 'public', { toolPort: port });
    await ensureBrowser();
    if (session.closed) return;
    response.writeHead(200, {
      'Content-Type': 'application/x-ndjson; charset=utf-8',
      'Cache-Control': 'no-store, no-transform',
      'X-Content-Type-Options': 'nosniff',
      'X-Accel-Buffering': 'no',
    });
    response.flushHeaders();
    sendLive(session, 'session', { id: session.id, interval: liveFrameInterval });
    session.heartbeat = setInterval(() => sendLive(session, 'heartbeat', { paused: session.paused }), 5000);
    await Promise.all(viewports.map(async (viewport) => {
      try { await renderViewport(target, policy, viewport, waitMs, session); }
      catch (error) { settleLiveDevice(session, viewport, friendlyError(error)); }
    }));
  } catch (error) {
    if (response.headersSent) sendLive(session, 'error', { error: friendlyError(error) });
    else json(response, error instanceof RenderError ? 400 : 500, { error: friendlyError(error) });
  } finally {
    await closeLive(session);
    if (!response.writableEnded) response.end();
  }
}

async function controlLive(request, response) {
  if (!/^application\/json(?:\s*;|$)/i.test(request.headers['content-type'] || '')) {
    return json(response, 415, { error: '预览控制必须使用 JSON 数据。' });
  }
  let payload;
  try { payload = JSON.parse(await readBody(request)); }
  catch { return json(response, 400, { error: '请求数据无效。' }); }
  const session = liveSessions.get(payload?.id);
  if (!session || session.closed) return json(response, 404, { error: '预览连接已结束，请重新连接。' });
  if (!['pause', 'resume', 'stop'].includes(payload.action)) return json(response, 400, { error: '无效的预览操作。' });
  if (payload.action === 'stop') {
    sendLive(session, 'stopped');
    session.response.end();
    await closeLive(session);
  } else {
    session.paused = payload.action === 'pause';
    if (!session.paused) session.frameEpoch++;
    sendLive(session, 'state', { paused: session.paused });
  }
  return json(response, 200, { paused: session.paused, stopped: session.closed });
}

async function render(request, response) {
  if (!/^application\/json(?:\s*;|$)/i.test(request.headers['content-type'] || '')) {
    return json(response, 415, { error: '截图请求必须使用 JSON 数据。' });
  }
  let payload;
  try {
    payload = JSON.parse(await readBody(request));
  } catch (error) {
    return json(response, 400, { error: error instanceof RenderError ? error.message : '请求数据无效。' });
  }
  const target = parseUrl(payload?.url);
  if (!target) return json(response, 400, { error: '请输入有效的 HTTP 或 HTTPS 网站地址。' });
  if (rendering) return json(response, 409, { error: '已有一组设备截图正在生成，请完成后重试。' });
  const requestedWait = Number(payload?.waitMs);
  const waitMs = Number.isFinite(requestedWait) ? Math.max(0, Math.min(5000, Math.round(requestedWait))) : 1000;

  rendering = true;
  try {
    const policy = await targetPolicy(target, request.headers['x-newpress-mockup-scope'] === 'public', { toolPort: port });
    await ensureBrowser();
    const results = await Promise.allSettled(viewports.map((viewport) => renderViewport(target, policy, viewport, waitMs)));
    const screenshots = results.map((result, index) => result.status === 'fulfilled'
      ? result.value
      : {
        id: viewports[index].id, label: viewports[index].label,
        width: viewports[index].width, height: viewports[index].height,
        error: friendlyError(result.reason),
      });
    const succeeded = screenshots.filter((item) => item.screenshot).length;
    return json(response, succeeded ? 200 : 502, {
      screenshots,
      error: succeeded ? '' : '未能加载网站：' + screenshots[0].error,
    });
  } catch (error) {
    return json(response, error instanceof RenderError ? 400 : 500, { error: friendlyError(error) });
  } finally {
    rendering = false;
  }
}

async function serveTool(response) {
  const body = await readFile(pagePath);
  response.writeHead(200, {
    'Content-Type': 'text/html; charset=utf-8',
    'Cache-Control': 'no-store',
    'X-Content-Type-Options': 'nosniff',
    'Referrer-Policy': 'no-referrer',
  });
  response.end(body);
}

async function main() {
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new RenderError('DEVICE_MOCKUP_PORT 必须是有效端口号。');
  try {
    await ensureBrowser();
  } catch {
    throw new RenderError('无法启动 Chromium 沙箱。请确认已运行 npx playwright install chromium，并使用支持浏览器沙箱的普通用户环境。');
  }

  const server = http.createServer(async (request, response) => {
    try {
      const clientError = checkClient(request);
      if (clientError) return json(response, 403, { error: clientError });
      const pathname = new URL(request.url, 'http://' + host).pathname;
      if (request.method === 'GET' && ['/', '/docs/DEVICE_MOCKUP_STUDIO.html'].includes(pathname)) return await serveTool(response);
      if (request.method === 'GET' && pathname === '/api/health') {
        return json(response, 200, { ready: Boolean(browser?.isConnected()), service: 'device-mockup', version: 6, liveSessions: liveSessions.size });
      }
      if (request.method === 'POST' && pathname === '/api/live') return await startLive(request, response);
      if (request.method === 'POST' && pathname === '/api/live/control') return await controlLive(request, response);
      if (request.method === 'POST' && pathname === '/api/render') return await render(request, response);
      if (request.method === 'GET' && pathname === '/favicon.ico') { response.writeHead(204); return response.end(); }
      response.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
      response.end('Not found');
    } catch {
      json(response, 500, { error: '截图工具暂时无法响应，请稍后重试。' });
    }
  });
  server.headersTimeout = 10000;
  server.requestTimeout = 15000;
  server.keepAliveTimeout = 5000;

  const stop = async () => {
    server.close();
    await Promise.allSettled([...liveSessions.values()].map(closeLive));
    await browser?.close().catch(() => {});
    process.exit(0);
  };
  server.once('error', async (error) => {
    process.stderr.write(error.code === 'EADDRINUSE'
      ? '端口 ' + port + ' 已被占用。请关闭旧服务，或设置 DEVICE_MOCKUP_PORT 后重新启动。\n'
      : '截图服务无法启动，请检查本机网络设置。\n');
    await browser?.close().catch(() => {});
    process.exitCode = 1;
  });
  server.listen(port, host, () => {
    process.stdout.write('截图工作进程已启动：http://localhost:' + port + '；请登录 NewPress 后台，在「工具与备份 → 宣传截图」使用。\n');
  });
  process.once('SIGINT', stop);
  process.once('SIGTERM', stop);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    process.stderr.write((error instanceof RenderError ? error.message : '截图服务启动失败，请检查运行环境。') + '\n');
    process.exitCode = 1;
  });
}
