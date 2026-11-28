import {required} from './ledger-admin-dom';
import {object,digest,own,pluginError,PluginRequestError} from '../shared/plugin-data-common';
import {states,codes,validWebhookPage} from '../shared/plugin-review-data';
import type {WebhookItem,WebhookQuery} from '../shared/plugin-review-data';
(() => {
  'use strict';
  const found = document.querySelector<HTMLElement>('[data-webhook-review]');
  if (!found || found.dataset.available !== 'true') return;
  const root=found;
  const eventScope=new AbortController();
  const owner = root.dataset.userId || '', form = required<HTMLFormElement>(root,'[data-webhook-filters]');
  const list = required(root,'[data-webhook-list]'), status = required(root,'[data-webhook-status]');
  const summary = required(root,'[data-webhook-summary]'), page = required(root,'[data-webhook-page]');
  const prev = required<HTMLButtonElement>(root,'[data-webhook-prev]'), next = required<HTMLButtonElement>(root,'[data-webhook-next]');
  const results = required(root,'[data-webhook-results]'), access = required(root,'[data-webhook-access]');
  const controllers = new Set<AbortController>();
  let serial = 0, heartbeat = 0, locked = false, suspended = document.hidden, loaded = false, busy = false, offset = 0, shown:WebhookQuery|null = null;
  const message = (text:string, error = false) => { status.textContent = text; status.dataset.tone = error ? 'error' : ''; };
  const node = <K extends keyof HTMLElementTagNameMap>(tag:K, text = '', className = ''):HTMLElementTagNameMap[K] => { const e = document.createElement(tag); e.textContent = text; e.className = className; return e; };
  function clear(text:string, error = false) {
    serial++; clearTimeout(heartbeat); controllers.forEach(c => c.abort()); controllers.clear();
    loaded = false; busy = false; shown = null; list.replaceChildren(); summary.textContent = '尚未取得结果'; page.textContent = '—'; prev.disabled = next.disabled = true; results.setAttribute('aria-busy', 'false');
    message(text, error);
  }
  function deny() {
    locked = true; access.hidden = false;
    clear('登录身份或权限已变化，请重新打开核对页面。', true);
    form.querySelectorAll<HTMLInputElement|HTMLSelectElement|HTMLButtonElement>('input,select,button').forEach(e => { e.disabled = true; });
  }
  async function request(url:string, options:RequestInit = {}):Promise<unknown> {
    if (root.dataset.userId !== owner) { const error = new PluginRequestError('identity'); error.status = 403; throw error; }
    const controller = new AbortController(); controllers.add(controller);
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await fetch(url, {...options, credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: controller.signal, headers: {...options.headers, 'X-NewPress-Review-User': owner}});
      if (!response.ok) {
        const error = new PluginRequestError('request'); error.status = response.status;
        try { const raw = await response.text(); if (raw.length < 16384) {const data:unknown=JSON.parse(raw);if(object(data)&&object(data.error)&&typeof data.error.code==='string')error.code=data.error.code;} } catch { /* Status still identifies the failed request. */ }
        throw error;
      }
      if (response.headers.get('X-NewPress-Review-User') !== owner) { const error = new PluginRequestError('identity'); error.status = 403; throw error; }
      const raw = await response.text();
      if (raw.length > 1024 * 1024) throw new Error('oversize');
      return JSON.parse(raw);
    } finally { clearTimeout(timeout); controllers.delete(controller); }
  }
  async function session() {
    const data = await request('/api/v1/admin/plugin-webhook-session');
    if (object(data) && data.user_id !== owner) { const error = new PluginRequestError('identity'); error.status = 403; throw error; }
    if (!object(data) || data.available !== true || typeof data.enabled !== 'boolean' || typeof data.csrf_token !== 'string' || !data.csrf_token || data.csrf_token.length > 512) throw new Error('session');
    return {user_id:owner,available:true,enabled:data.enabled,csrf_token:data.csrf_token};
  }
  function failure(error:unknown, mutation = false) {
    const e=pluginError(error);
    if ([401, 403].includes(e.status) || e.code === 'plugins.review_identity_changed') { deny(); return; }
    if (e.code === 'plugins.webhooks_corrupt') { clear('持久记录不一致，已阻止发送。请停机核对原数据。', true); return; }
    const text = mutation ? '核对结论的保存结果待确认。请先重新查询记录，再决定是否提交。' : e.status === 429 ? '查询过于频繁，请稍后手动重试。' : e.status === 409 ? '记录或登录账号已变化，请重新查询核对。' : '暂时无法确认记录，请稍后手动查询。';
    clear(text, true);
  }
  function schedule() {
    clearTimeout(heartbeat); const ticket = serial;
    heartbeat = setTimeout(async () => {
      if (!loaded || locked || suspended || busy || ticket !== serial) return;
      try { await session(); if (ticket === serial) schedule(); }
      catch (e) { if (ticket === serial) failure(e); }
    }, 15000);
  }
  function field(dl:HTMLElement, label:string, value:unknown, full = false) {
    const group = node('div', '', full ? 'shared-review-full' : ''); group.append(node('dt', label), node('dd', String(value))); dl.append(group);
  }
  function render(i:WebhookItem) {
    const row = node('li', '', 'shared-review-record'), header = node('header'), dl = node('dl');
    const badge = node('span', states[i.state], 'shared-review-tag'); if (i.state === 'uncertain') badge.dataset.tone = 'warning';
    header.append(node('h3', i.plugin + ' · ' + i.slot), badge); row.append(header, dl);
    field(dl, '目标域名', i.target); field(dl, '接受时间', i.created_at); field(dl, '记录时间', i.updated_at);
    if (i.outcome.code) field(dl, '已知结果', codes[i.outcome.code] + (i.outcome.http_status ? '（HTTP ' + i.outcome.http_status + '）' : ''), true);
    if (i.result_at) field(dl, '原结果记录时间', i.result_at);
    if (i.state === 'sending') row.append(node('p', '发送已领取。进程退出或结果记录失败时，领取到期后转为待核对。', 'shared-review-record-note'));
    if (i.state === 'pending') row.append(node('p', '源任务尚未成功或尚未被发送器领取；任务失败时继续保留等待。', 'shared-review-record-note'));
    const details = node('details'), ids = node('dl'); details.append(node('summary', '完整标识与请求摘要'), ids); row.append(details);
    field(ids, '操作 ID', i.id, true); field(ids, '任务 ID', i.job_id, true); field(ids, '请求摘要', i.fingerprint, true);
    if (i.outcome.response_digest) field(ids, '响应摘要', i.outcome.response_digest, true);
    if (i.review) { field(dl, '核对账号 ID', i.review.actor_id); field(dl, '核对原因', i.review.reason, true); }
    if (i.state === 'uncertain') {
      const disclosure = node('details'); disclosure.append(node('summary', '记录人工核对结论'));
      const review = node('form', '', 'webhook-decision'); review.autocomplete = 'off';
      const choiceLabel = node('label', '接收方核对结果'), choice = node('select'); choice.required = true; choice.name = 'decision';
      for (const [value, label] of [['', '请选择核对结论'], ['received', '人工确认已收到'], ['not_received', '人工确认未收到']] as const) { const option = node('option', label); option.value = value; choice.append(option); }
      choiceLabel.append(choice);
      const reasonLabel = node('label', '核对依据（最多 500 字）'), reason = node('textarea'); reason.name = 'reason'; reason.required = true; reason.maxLength = 500; reasonLabel.append(reason);
      const confirmation = node('label', '', 'webhook-confirm'), check = node('input'); check.type = 'checkbox'; check.required = true;
      confirmation.append(check, node('span', '我已核对接收方记录；此结论只保存审计，不触发重新发送。'));
      const submit = node('button', '记录核对结论', 'admin-button admin-button-primary'); submit.type = 'submit';
      review.append(choiceLabel, reasonLabel, confirmation, submit); disclosure.append(review); row.append(disclosure);
      review.addEventListener('submit', async event => {
        event.preventDefault(); if (locked || suspended || busy || !loaded || !review.reportValidity()) return;
        const requestReason = reason.value.trim(), decision = choice.value; if (!requestReason) { reason.focus(); return; }
        const ticket = serial; busy = true; clearTimeout(heartbeat); submit.disabled = true; message('正在记录核对结论…');
        try {
          const current = await session(); if (ticket !== serial || suspended || locked) return;
          const receipt = await request('/api/v1/admin/plugin-webhooks/' + i.id + '/review', {method: 'POST', headers: {'Content-Type': 'application/json', 'X-CSRF-Token': current.csrf_token}, body: JSON.stringify({expected_user_id: owner, version: i.version, decision, reason: requestReason})});
          if (ticket !== serial || suspended || locked) return;
          if (!object(receipt) || receipt.recorded !== true) throw new Error('核对回执未确认');
          busy = false; await load(offset, true);
        } catch (e) { if (ticket === serial) failure(e, true); }
        finally { if (ticket === serial) { busy = false; submit.disabled = false; } }
      });
    }
    return row;
  }
  const fieldControl=(name:string)=>{const field=form.elements.namedItem(name);if(!field||!('value' in field))throw new Error('缺少筛选字段');return field;};
  function query():WebhookQuery {
    return {plugin: fieldControl('plugin').value.trim(), job: fieldControl('job').value.trim(), state: fieldControl('state').value, limit: Number(fieldControl('limit').value)};
  }
  async function load(start:number, confirmedReview=false) {
    if (locked || suspended || busy || !form.reportValidity()) return;
    const q = query();
    if (q.plugin && !/^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$/.test(q.plugin) || q.job && !digest(q.job) || q.state && !own(states, q.state) || ![20, 50, 100].includes(q.limit)) { clear('筛选参数无效。', true); return; }
    clear('正在读取记录…'); const ticket = serial; results.setAttribute('aria-busy', 'true');
    try {
      await session(); if (ticket !== serial || suspended) return;
      const data = await request('/api/v1/admin/plugin-webhooks?' + new URLSearchParams({...q, limit:String(q.limit), offset: String(start)}));
      if (ticket !== serial || suspended) return;
      if (!validWebhookPage(data, q, start)) throw new Error('invalid records');
      await session(); if (ticket !== serial || suspended) return;
      list.replaceChildren(...data.items.map(render)); offset = start; shown = q; loaded = true;
      prev.disabled = start === 0; next.disabled = start + data.items.length >= data.total;
      page.textContent = data.total ? (Math.floor(start / q.limit) + 1) + ' / ' + Math.ceil(data.total / q.limit) : '—';
      summary.textContent = '共 ' + data.total + ' 条记录'; results.setAttribute('aria-busy', 'false');
      message((confirmedReview?'核对结论已保存。':'') + (data.items.length ? '已读取记录。状态变化后请手动查询。' : '当前筛选下没有记录。')); schedule();
    } catch (e) { if (ticket === serial) { failure(e); if(confirmedReview&&!locked)message('核对结论已保存，列表暂未刷新，请重新查询。',true); } }
  }
  form.addEventListener('submit', e => { e.preventDefault(); suspended = false; load(0); });
  form.addEventListener('input', () => { if (!locked) clear('筛选已变化，请重新查询。'); });
  form.addEventListener('change', () => { if (!locked) clear('筛选已变化，请重新查询。'); });
  prev.addEventListener('click', () => { if (loaded && shown && !prev.disabled) load(Math.max(0, offset - shown.limit)); });
  next.addEventListener('click', () => { if (loaded && shown && !next.disabled) load(offset + shown.limit); });
  const suspend = () => { suspended = true; if (!locked) clear('页面已离开，记录已清空；返回后请手动查询。'); };
  window.addEventListener('blur', suspend, {signal:eventScope.signal}); window.addEventListener('pagehide', suspend, {signal:eventScope.signal});
  document.addEventListener('visibilitychange', () => { if (document.hidden) suspend(); }, {signal:eventScope.signal});
  window.addEventListener('offline', () => { if (!locked) clear('网络已断开，记录已清空；恢复后请手动查询。', true); }, {signal:eventScope.signal});
  document.addEventListener('chunnel:session', deny, {signal:eventScope.signal});
  let channel:BroadcastChannel|undefined;
  try { channel = new BroadcastChannel('chunnel.social'); channel.addEventListener('message', e => { if (e.data?.type === 'session') deny(); }); } catch { /* Visible-session checks remain active. */ }
  document.addEventListener('chunnel:admin-before-replace', event => { const detail=(event as CustomEvent<import('./admin-types').AdminRegionChange>).detail; if(detail?.regions?.some(region=>region===root||region.contains(root))) { suspend(); locked=true;eventScope.abort();channel?.close(); } }, {signal:eventScope.signal});
  if (!/^[1-9][0-9]{0,18}$/.test(owner) || BigInt(owner) > 9223372036854775807n) deny();
  else if (!suspended) load(0);
})();
