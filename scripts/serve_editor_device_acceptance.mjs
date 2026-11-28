// Temporary device acceptance for the isolated Go browser fixture only.
// Business writes never reach Go. All accounts/content are disposable test data.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { randomBytes } from 'node:crypto';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { networkInterfaces } from 'node:os';

export const cases = [
  ['ime', '中文候选输入', '在正文末尾用系统拼音连续输入“今天继续核对中文输入”，选候选词；中途移动光标、删除再输入。确认无漏字、重字或光标跳动。'],
  ['selection', '触摸选区和格式', '长按正文，拖动选区手柄选择几个字，点加粗；继续在选区前后输入。确认格式范围和光标位置正确。'],
  ['history', '撤销和重做', '用编辑器的撤销、重做按钮回退并恢复刚才的文字与格式，确认没有丢掉此前正文。'],
  ['source', '源码往返', '切到 HTML 源码，输入一段中文，再切回可视编辑；重复两次。确认文字、结构和输入位置正常。'],
  ['text-paste', '系统文本和富文本粘贴', '从下方测试文字或备忘录复制中文、多行文字和带加粗/列表的富文本，用系统菜单粘贴到正文；撤销、重做，核对内容。'],
  ['image-paste', '图片粘贴与插入', '长按下方测试图片复制，回到编辑器正文，长按并选择“粘贴”；应直接插入同站图片，或打开图片工具显示上传反馈；若打开工具，请填写替代文本后插入。确认图片可见，再撤销。若系统没有图片粘贴入口，记录“当前设备无法完成”及原因。测试上传只返回下方的占位图。'],
  ['mobile', '键盘和窄屏布局', '键盘弹出时打开/关闭设置面板与插入菜单，横竖屏切换，再回到正文。确认可见光标、按钮可点、页面没有失控横向滚动。'],
  ['save', '保存反馈与继续编辑', '修改标题并点保存，看到保存成功后继续编辑。这里只验证模拟回执与界面反馈，刷新会恢复测试原文。'],
  ['tabs', '跨标签副本', '在另一个 Safari 标签打开同一编辑地址，分别输入不同文字，再回到原标签；确认不会静默覆盖另一份编辑，恢复提示符合实际状态。'],
];
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAMAAAACACAIAAADS5vE8AAACqklEQVR4nO3dMW4aQRiGYRxxFZo0XCNdCtNFPohP4INY6VI4lXMNN2m4TGQlUhQELMO3dv7ZeZ4SwcpaXv+7oNFwc/v8tGrx8vVn0/O3Xz46/oLPz4emZ8MBAREREBEBEREQEQERERARAREREBEBEREQEQERERARAREREJF1tfUljt/X+TGBiAiIiICICIiIgIgIiIiAiKxX/fj26fORR48+OKP/dPzN3cOqB+uO01m0/eN9FxlVD2jAdPrKqPQ90OD1HGRUU92A1NNFQ0UDUk8vDRUNiF5UDMj46WgIrautL3k9vnvnftZXlZtABf/JStkXOz/lAqIvAiIiICICIiIgIgIiIiAiAmLR64Emvd1ameu+sts0/j3VvhhsZQIREdBJldcB1iEgIgI6xxCaJCCWtT/QWy8G2j/eN82Vzd1DtQ9K25a3wP5AlDbiJax1orgTOmPEgJjRoAEZQnMZNCDmIiAi4wbkKjaLcQNiFkMHZAjlhg6I3OgBGUKh0QMiJCBDKCIgIgJ65U5oOfsDMe9bMNz+QPRFQH+4il1HQEQE9JchdAUBERHQPwyhVgIiIqBDhlATARER0BGG0OUERERARAR0nKvYhQREREAnGUKXGG5/oAXY2h+oF4bQJJcwIgKaUG1/u2oERERA0wyhMwTE2D+28j7jwRA6xQQiIiAiAiIiICICIiIgIgIiIiAiN7fPT00veIf1Q761m/Gng+wPRGkVL2F+n+uU3Y/vq2IqBkRHigZkCHUxfuoGpKEu6ikdkIbq19PBeqDf17JhP9jvCqfTR0DDZrQrn05PAZ25s662UfrLYBuxl74Hoj4BEREQEQERERARAbGs/YEcv6/zYwIRERARAREREBEBEREQEQERERARAREREBEBEREQEQERERARAbFK/AI3nOniB1RWGgAAAABJRU5ErkJggg==', 'base64');
const escape = value => String(value).replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char]));
const isPrivate = host => host === '127.0.0.1' || /^10\./.test(host) || /^192\.168\./.test(host) || /^172\.(1[6-9]|2\d|3[01])\./.test(host);
const bounded = (value, length = 1000) => typeof value === 'string' ? value.slice(0, length) : '';

export async function startDeviceAcceptance({ directory, fixture, host = '127.0.0.1', lifetimeMs = 20 * 60 * 1000, stopFixture = true, caseIDs = cases.map(([id]) => id) }) {
  const upstream = new URL(fixture.base);
  assert.equal(upstream.protocol, 'http:');
  assert.equal(upstream.hostname, '127.0.0.1', 'Only the isolated loopback fixture is allowed.');
  assert.match(fixture.login, /^\/__admin_resource_login\/[a-f0-9]{32}$/);
  assert.match(fixture.stop, /^\/__admin_resource_stop\/[a-f0-9]{32}$/);
  assert.ok(isPrivate(host), 'Bind to loopback or one explicit private interface.');
  assert.ok(Object.values(networkInterfaces()).flat().some(item => item?.address === host), 'Bind address must belong to this computer.');
  assert.ok(lifetimeMs > 0 && lifetimeMs <= 20 * 60 * 1000);
  assert.ok(caseIDs.length && new Set(caseIDs).size === caseIDs.length && caseIDs.every(id => cases.some(([known]) => id === known)), 'Select existing acceptance cases.');
  const selectedCases = cases.filter(([id]) => caseIDs.includes(id));
  const login = await fetch(new URL(fixture.login, upstream), { redirect: 'manual', signal: AbortSignal.timeout(10000) });
  assert.equal(login.status, 303);
  const ownerCookie = login.headers.getSetCookie().map(value => value.split(';')[0]).join('; ');
  assert.ok(ownerCookie);
  const token = randomBytes(24).toString('hex'), control = randomBytes(24).toString('hex');
  const cookieName = 'newpress_device_acceptance';
  const editor = `/admin/content/${Number(fixture.editor_post_id)}/edit/`;
  assert.equal(editor, fixture.paths.editor);
  const autosave = editor.replace('/edit/', '/autosave/');
  const imageItem = { id: 7, status: 'active', kind: 'image', width: 192, height: 128, size_bytes: png.length,
    title: '隔离验收占位图', filename: 'device-acceptance.png', public_url: '/media/7/device-acceptance.png', alt_text: '', caption: '' };
  const audit = { started_at: new Date().toISOString(), expires_at: new Date(Date.now() + lifetimeMs).toISOString(),
    mode: 'temporary isolated fixture; every business write intercepted', upstream: fixture.base,
    upstream_business_writes: 0, case_ids: caseIDs, requests: [], reports: [], telemetry: [] };
  await mkdir(directory, { recursive: true });
  let base = '', closing = false, timer;
  let flush = Promise.resolve();
  const record = () => {
    const data = JSON.stringify(audit, null, 2) + '\n';
    flush = flush.then(() => writeFile(resolve(directory, 'device-session.json'), data));
    return flush;
  };
  const requestBody = async (req, limit = 65536) => {
    const chunks = []; let size = 0;
    for await (const chunk of req) {
      size += chunk.length;
      if (size > limit) { const error = new Error('Request too large'); error.status = 413; throw error; }
      chunks.push(chunk);
    }
    return Buffer.concat(chunks);
  };
  const server = createServer(async (req, res) => {
    const send = (status, body = '', type = 'text/plain; charset=utf-8', headers = {}) => {
      res.writeHead(status, { 'Content-Type': type, 'Cache-Control': 'no-store', 'Referrer-Policy': 'no-referrer',
        'X-Content-Type-Options': 'nosniff', 'X-Robots-Tag': 'noindex, nofollow', ...headers });
      res.end(req.method === 'HEAD' ? undefined : body);
    };
    const json = (status, body) => send(status, JSON.stringify(body), 'application/json; charset=utf-8');
    try {
      if (req.headers.host !== new URL(base).host || !req.url?.startsWith('/') || req.url.startsWith('//')) return send(403, 'Host not allowed');
      const url = new URL(req.url, base), path = url.pathname;
      if (path === '/__qa/stop' && req.method === 'POST') {
        if (req.headers['x-qa-control'] !== control || ![host, '127.0.0.1'].includes(req.socket.remoteAddress)) return send(403, 'Local control required');
        send(202, 'Stopping'); setImmediate(() => close('explicit-owner-stop').catch(console.error)); return;
      }
      if (path === `/__qa/start/${token}` && req.method === 'GET') {
        return send(303, '', 'text/plain', { 'Set-Cookie': `${cookieName}=${token}; Path=/; HttpOnly; SameSite=Strict; Max-Age=1200`, Location: '/__qa/' });
      }
      if (!(req.headers.cookie || '').split(';').some(value => value.trim() === `${cookieName}=${token}`)) return send(403, 'Open the current acceptance invitation first.');
      if (closing) return send(410, 'Acceptance session ended');
      if (!['GET', 'HEAD'].includes(req.method) && req.headers.origin !== base) return send(403, 'Same-origin request required');
      if (req.method === 'GET' && path === '/__qa/') return send(200, portal(editor, audit.expires_at, selectedCases), 'text/html; charset=utf-8', {
        'Content-Security-Policy': "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
      });
      if (req.method === 'GET' && path === '/__qa/style.css') return send(200, portalStyle, 'text/css; charset=utf-8');
      if (req.method === 'GET' && path === '/__qa/form.js') return send(200, formScript, 'text/javascript; charset=utf-8');
      if (req.method === 'GET' && path === '/__qa/observe.js') return send(200, observerScript, 'text/javascript; charset=utf-8');
      if (['GET', 'HEAD'].includes(req.method) && ['/__qa/test.png', imageItem.public_url].includes(path)) return send(200, png, 'image/png');
      if (req.method === 'POST' && path === '/__qa/telemetry') {
        const data = JSON.parse((await requestBody(req)).toString());
        if (audit.telemetry.length < 1200) audit.telemetry.push({ at: new Date().toISOString(), user_agent: bounded(req.headers['user-agent'], 400),
          secure_context: data.secureContext === true, touch_callout: data.touchCallout === true, paragraph_line_height: bounded(data.paragraphLineHeight, 20), observer: 'event metadata only; does not establish native IME or Safari pass',
          events: Array.isArray(data.events) ? data.events.slice(0, 80).map(item => ({ type: bounded(item.type, 40), trusted: item.trusted === true,
            input_type: bounded(item.inputType, 50), formats: Array.isArray(item.formats) ? item.formats.slice(0, 8).map(x => bounded(x, 60)) : [],
            error: item.error === true })) : [] });
        await record(); return json(200, { received: true });
      }
      if (req.method === 'POST' && path === '/__qa/report') {
        const data = JSON.parse((await requestBody(req)).toString());
        if (!bounded(data.device, 100).trim() || !bounded(data.version, 100).trim() || data.actualSafari !== true) return json(400, { error: '请填写设备、系统版本，并确认使用实际 Safari。' });
        const report = { at: new Date().toISOString(), device: bounded(data.device, 100), version: bounded(data.version, 100),
          actual_safari_declared_by_operator: true, user_agent: bounded(req.headers['user-agent'], 400),
          note: bounded(data.note), scope: 'manual observation with isolated server-side simulated saves/uploads; HTTP LAN origin',
          cases: selectedCases.map(([id]) => ({ id, result: ['pass', 'fail', 'unavailable', 'not-run'].includes(data.results?.[id]) ? data.results[id] : 'not-run' })) };
        audit.reports.push(report); await record(); return json(200, { received: true, at: report.at, remaining: report.cases.filter(item => item.result !== 'pass').length });
      }
      if (req.method === 'GET' && path === '/api/v1/admin/updates') return json(503, { error: { message: '隔离验收未配置更新源' } });
      if (req.method === 'GET' && path === '/api/v1/admin/media/7') return json(200, imageItem);
      if (req.method === 'GET' && path === '/api/v1/admin/media') return json(200, { items: [imageItem], pagination: { page: 1, pages: 1, total: 1 }, can_upload: true, max_upload_bytes: 1048576 });
      if (!['GET', 'HEAD'].includes(req.method)) {
        // This branch never forwards a request, even if a new editor tool is added.
        const metadata = { at: new Date().toISOString(), method: req.method, path, result: 'blocked' };
        if (audit.requests.length < 2000) audit.requests.push(metadata);
        if (req.method === 'POST' && (path === editor || path === autosave)) {
          const form = new URLSearchParams((await requestBody(req, 2 * 1024 * 1024)).toString());
          const version = Number(form.get('version'));
          if (!Number.isSafeInteger(version) || version < 1) { await record(); return json(400, { error: { message: '无效测试版本' } }); }
          metadata.result = 'simulated-receipt'; await record();
          return json(200, path === editor ? { ...fixture.editor_receipt, submission_key: req.headers['idempotency-key'] || form.get('submission_key') || '', version: version + 1 }
            : { ...fixture.autosave_receipt, source_version: version, updated_at: new Date().toISOString() });
        }
        if (req.method === 'POST' && path === '/api/v1/admin/media') {
          // Consume the upload transiently and retain format/size metadata only.
          const body = await requestBody(req, 2 * 1024 * 1024);
          metadata.result = 'simulated-placeholder-image'; metadata.bytes = body.length;
          metadata.contains_png = body.includes(png.subarray(0, 8));
          await record(); return json(200, { item: imageItem, duplicate: false });
        }
        await record(); req.resume(); return json(403, { error: { message: '隔离验收已拦截此操作；测试数据未写入。' } });
      }
      // Restrict reads too; fixture login/stop and unrelated administration stay private.
      const allowed = path === editor || path === '/api/v1/session' || path.startsWith('/core-assets/') || path.startsWith('/theme-assets/')
        || path.startsWith('/assets/') || path.startsWith('/emoji-assets/') || path.startsWith('/api/v1/admin/') || path.startsWith('/api/v1/emoji');
      if (!allowed) return send(404, 'This route is outside the editor acceptance.');
      const response = await fetch(new URL(path + url.search, upstream), { method: req.method, redirect: 'manual',
        headers: { Cookie: ownerCookie, Accept: req.headers.accept || '*/*' }, signal: AbortSignal.timeout(15000) });
      const headers = Object.fromEntries([...response.headers].filter(([name]) => !['set-cookie', 'content-length', 'content-encoding', 'transfer-encoding', 'connection'].includes(name)));
      const type = response.headers.get('content-type') || 'application/octet-stream';
      let body = Buffer.from(await response.arrayBuffer());
      if (type.includes('text/html')) {
        let html = body.toString('utf8').replaceAll(upstream.origin, base);
        if (path === editor) html = html.replace('</head>', '<script src="/__qa/observe.js" defer></script></head>');
        body = Buffer.from(html);
        const revision = html.match(/\/core-assets\/([a-f0-9]{24})\//)?.[1];
        if (revision && !audit.core_revision) { audit.core_revision = revision; await record(); }
        delete headers.etag;
      }
      if (headers.location) {
        const redirect = new URL(headers.location, upstream);
        if (redirect.origin !== upstream.origin) return send(502, 'External redirect blocked');
        headers.location = redirect.pathname + redirect.search;
      }
      send(response.status, body, type, { ...headers, 'Cache-Control': 'no-store' });
    } catch (error) {
      if (!res.headersSent) send(error.status || 502, error.status === 413 ? 'Request exceeds the isolated acceptance limit.' : 'Acceptance request failed.');
      else res.end();
    }
  });
  async function close(reason = 'explicit-owner-stop') {
    if (closing) return;
    closing = true; clearTimeout(timer);
    await new Promise(resolveClose => { server.close(resolveClose); server.closeIdleConnections(); });
    if (stopFixture) {
      try {
        const response = await fetch(new URL(fixture.stop, upstream), { method: 'POST', headers: { Cookie: ownerCookie }, signal: AbortSignal.timeout(10000) });
        audit.fixture_stop_status = response.status;
      } catch { audit.fixture_stop_status = 'unavailable'; }
    }
    audit.stopped_at = new Date().toISOString(); audit.stop_reason = reason; await record();
  }
  await new Promise((ready, reject) => { server.once('error', reject); server.listen(0, host, ready); });
  base = `http://${host}:${server.address().port}`;
  const invitation = `${base}/__qa/start/${token}`;
  await writeFile(resolve(directory, 'device-access.json'), JSON.stringify({ base, invitation, control, editor: base + editor, expires_at: audit.expires_at }, null, 2) + '\n', { mode: 0o600 });
  await record();
  timer = setTimeout(() => close('time-limit').catch(console.error), lifetimeMs);
  return { base, invitation, close, audit };
}

function portal(editor, expires, selectedCases) {
  return `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Safari 编辑器实机验收</title><link rel="stylesheet" href="/__qa/style.css"><script src="/__qa/form.js" defer></script><main>
  <p class="eyebrow">NEWPRESS · 实机验收</p><h1>用你的设备完成最后一轮输入检查</h1>
  <p>本页连接电脑上的临时测试数据。编辑页保持原有交互；保存与上传只模拟回执，刷新后恢复测试原文。不要输入个人资料。</p>
  <p>请用 iPhone／iPad 的 Safari，和电脑连接同一个 Wi-Fi。入口在 <time datetime="${escape(expires)}">${escape(expires)}</time> 到期。这里是局域网 HTTP，受安全上下文限制的能力需另行验收。</p>
  <a class="primary" href="${escape(editor)}" target="newpress-device-editor">打开测试编辑器 ↗</a><p>编辑器在另一标签页打开；完成后回到本页逐项记录。需要跨标签时，长按上面的入口并选择新标签页。</p>
  <form id="report"><div class="fields"><label>设备型号<input name="device" required maxlength="100" placeholder="例如 iPhone 15"></label><label>iOS／iPadOS 版本<input name="version" required maxlength="100" placeholder="填写设备实际版本"></label></div>
  <label class="confirm"><input type="checkbox" name="actualSafari" required> 我正在实际 Apple 设备的 Safari 中操作</label>
  ${selectedCases.map(([id, title, instruction], index) => `<fieldset><legend>${index + 1}. ${escape(title)}</legend><p>${escape(instruction)}</p><label>观察结果<select name="${id}"><option value="not-run">未测试</option><option value="pass">通过</option><option value="fail">发现问题</option><option value="unavailable">当前设备无法完成</option></select></label></fieldset>`).join('')}
  <label>问题和复现步骤<textarea name="note" rows="4" maxlength="1000" placeholder="记录哪一步出错、实际表现。无法完成的项目也请写明原因。"></textarea></label><button class="primary" type="submit">提交本次观察</button><p id="result" role="status"></p></form>
  <section><h2>复制测试材料</h2><p>中文粘贴测试：你好，NewPress 🐳<br>第二行：标点、空格 与数字 123。</p><p>这一段有<strong>加粗中文</strong>与<em>斜体内容</em>。</p><ul><li>第一项</li><li>第二项</li></ul><img class="test-image" src="/media/7/device-acceptance.png" alt="可长按复制的测试图片" width="192" height="128"></section>
  </main></html>`;
}
const portalStyle = `:root{color-scheme:light dark;font-family:system-ui,-apple-system,sans-serif;background:#f4f5f7;color:#182335}body{margin:0}main{max-width:760px;margin:auto;padding:32px 20px 72px}h1{font-size:30px;line-height:1.3;letter-spacing:-.03em}p{line-height:1.7}.eyebrow{font-size:12px;letter-spacing:.15em;color:#49657c}fieldset,section{margin:20px 0;padding:18px;border:1px solid #c7d0dc;border-radius:12px;background:#fff}legend{font-weight:650;padding:0 6px}label{display:block;font-size:15px}input:not([type=checkbox]),select,textarea{display:block;box-sizing:border-box;width:100%;font:inherit;margin:8px 0 14px;padding:12px;border:1px solid #a9b8c9;border-radius:7px;background:#fff;color:#182335}.fields{display:grid;grid-template-columns:1fr 1fr;gap:16px}.confirm{display:flex;align-items:center;gap:8px;line-height:1.5}.confirm input{width:20px;height:20px}.primary{display:inline-block;font:inherit;text-decoration:none;padding:13px 18px;margin:12px 0;background:#163d68;color:white;border:0;border-radius:8px}.test-image{image-rendering:pixelated;border:1px solid #91a0b5}#result{font-weight:600}@media(max-width:520px){.fields{grid-template-columns:1fr;gap:0}h1{font-size:25px}}@media(prefers-color-scheme:dark){:root{background:#121b28;color:#dbe4ef}.eyebrow{color:#96b8d4}fieldset,section{background:#1c2a3b;border-color:#435369}input:not([type=checkbox]),select,textarea{background:#162334;color:#e2e9f2;border-color:#54657b}.primary{background:#24639f}}`;
const formScript = `const form=document.querySelector('#report'), output=document.querySelector('#result');
const restore=()=>{try{const saved=JSON.parse(sessionStorage.getItem('newpress-device-report')||'{}');for(const [name,value] of Object.entries(saved)){const control=form.elements.namedItem(name);if(control)control.type==='checkbox'?control.checked=value:control.value=value;}}catch{}};
restore();form.addEventListener('change',()=>{const state=Object.fromEntries(new FormData(form));state.actualSafari=form.elements.actualSafari.checked;try{sessionStorage.setItem('newpress-device-report',JSON.stringify(state));}catch{}});
for(const time of document.querySelectorAll('time'))time.textContent=new Date(time.dateTime).toLocaleString();
form.addEventListener('submit',async event=>{event.preventDefault();const button=form.querySelector('button[type=submit]');button.disabled=true;output.textContent='正在提交…';
const data=Object.fromEntries(new FormData(form));const payload={device:data.device,version:data.version,actualSafari:form.elements.actualSafari.checked,note:data.note,results:data};
try{const response=await fetch('/__qa/report',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});const result=await response.json();if(!response.ok)throw new Error(result.error||'提交失败');output.textContent='已收到本次观察。仍有 '+result.remaining+' 项未通过或未完成；可补充后再次提交。';}catch(error){output.textContent='未提交成功：'+error.message+'。请保留本页并在聊天里说明情况。';}finally{button.disabled=false;}});`;
const observerScript = `(()=>{let events=[];const send=()=>{if(!events.length)return;const body=JSON.stringify({events:events.splice(0,80),secureContext:isSecureContext,touchCallout:CSS.supports('-webkit-touch-callout','none'),paragraphLineHeight:document.querySelector('.newpress-document p')?getComputedStyle(document.querySelector('.newpress-document p')).lineHeight:''});fetch('/__qa/telemetry',{method:'POST',headers:{'Content-Type':'application/json'},body,keepalive:true}).catch(()=>{});};
for(const type of ['compositionstart','compositionend','beforeinput','paste'])document.addEventListener(type,event=>{if(!event.target.closest?.('[data-content-editor]'))return;events.push({type,trusted:event.isTrusted,inputType:event.inputType||'',formats:event.clipboardData?[...event.clipboardData.types]:[]});if(events.length>=60)send();},true);
window.addEventListener('error',()=>events.push({type:'error',error:true}));window.addEventListener('unhandledrejection',()=>events.push({type:'unhandledrejection',error:true}));
document.addEventListener('visibilitychange',send);window.addEventListener('pagehide',send);setInterval(send,5000);})();`;

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  assert.ok(process.argv[2], 'Usage: node scripts/serve_editor_device_acceptance.mjs <isolated fixture directory> <private LAN IPv4> [case-id,case-id]');
  const directory = resolve(process.argv[2]);
  const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
  const session = await startDeviceAcceptance({ directory, fixture, host: process.argv[3] || '127.0.0.1', ...(process.argv[4] ? { caseIDs: process.argv[4].split(',') } : {}) });
  console.log(`Device acceptance ready: ${session.invitation}\nExpires: ${session.audit.expires_at}`);
  for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => session.close(signal).catch(console.error));
}
