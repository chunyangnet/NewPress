import {required} from './ledger-admin-dom';
import {object as isObject,pluginError} from '../shared/plugin-data-common';
import {faults,dispositions,webhookStates,webhookCodes,validRecordPage,parseReviewJSON} from '../shared/plugin-review-data';
import type {RecordQuery,SharedRecord} from '../shared/plugin-review-data';
(() => {
  'use strict';
  const found = document.querySelector<HTMLElement>('[data-shared-review]');
  if (!found || found.dataset.available !== 'true') return;
  const root=found;
  const eventScope=new AbortController();
  const $ = <T extends HTMLElement=HTMLElement>(selector:string):T => required<T>(root,selector);
  const form = $<HTMLFormElement>('[data-review-form]'), list = $('[data-review-list]');
  const status = $('[data-review-status]'), results = $('[data-review-results]');
  const previous = $<HTMLButtonElement>('[data-review-prev]'), next = $<HTMLButtonElement>('[data-review-next]');
  const summary = $('[data-review-summary]'), pageLabel = $('[data-review-page]');
  const access = $('[data-review-access]'), title = $('[data-review-title]');
  const owner = root.dataset.userId || '', ownerHeader = 'X-NewPress-Review-User';
  const controllers = new Set<AbortController>();
  let serial = 0, offset = 0, nextOffset = -1, loaded = false, locked = false, suspended = document.hidden;
  let heartbeat = 0, displayedQuery:RecordQuery|null = null;
  const malformed = () => new Error('返回记录不完整或格式异常，请稍后重新查询。');
  const report = (message:string, tone = '') => { status.textContent = message; status.dataset.tone = tone; };
  function clearResults() {
    loaded = false; displayedQuery = null; nextOffset = -1;
    list.replaceChildren(); summary.textContent = '尚未取得结果'; pageLabel.textContent = '—';
    previous.disabled = next.disabled = true;
    results.setAttribute('aria-busy', 'false');
  }
  function invalidate(message:string, tone = '') {
    serial++; clearTimeout(heartbeat);
    controllers.forEach(controller => controller.abort()); controllers.clear();
    clearResults(); report(message, tone);
  }
  function deny(message:string) {
    locked = true; access.hidden = false;
    invalidate(message, 'error');
    form.querySelectorAll<HTMLInputElement|HTMLSelectElement|HTMLButtonElement>('input, select, button').forEach(control => { control.disabled = true; });
  }
  const node = <K extends keyof HTMLElementTagNameMap>(tag:K, className = '', value = ''):HTMLElementTagNameMap[K] => {
    const element = document.createElement(tag); element.className = className; element.textContent = value; return element;
  };
  const field = (dl:HTMLElement, label:string, value:unknown, full = false, code = false) => {
    const group = node('div', full ? 'shared-review-full' : '');
    const dd = node('dd'); dd.append(node(code ? 'code' : 'span', '', String(value)));
    group.append(node('dt', '', label), dd); dl.append(group);
  };
  const timestamp = (value:string|undefined) => {
    if (!value) return '—';
    const time = new Date(value);
    const zone = document.documentElement?.dataset.siteTimezone || 'UTC';
    try { return new Intl.DateTimeFormat('zh-CN', {timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false, timeZoneName: 'short'}).format(time); }
    catch { return time.toISOString(); }
  };
  function renderRecord(item:SharedRecord, scope:string) {
    const row = node('li', 'shared-review-record'), head = node('header'), dl = node('dl');
    const kind = item.type === 'authority' ? '共享授权' : item.type === 'webhook_intent' ? 'Webhook 意图' : '内容回执';
    const label = scope === 'quarantines' ? kind + ' · 隔离记录' : (item.plugin || '身份无法确认') + ' · ' + kind;
    const state = scope === 'quarantines' ? item.repair ? '已修复 · 保留审计' : '未修复 · 共享执行阻断' : item.fault ? faults[item.fault || 'invalid_data'] : item.type === 'authority' ? item.retired ? '已退休' : item.enabled ? '已启用' : '已停用' : item.webhook ? webhookStates[item.webhook.state] : '原提交已记录';
    const badge = node('span', 'shared-review-tag', state);
    if (scope === 'quarantines' ? !item.repair : !!item.fault) badge.dataset.tone = 'warning';
    head.append(node('h3', '', label), badge); row.append(head, dl);
    if (scope === 'quarantines') {
      field(dl, '隔离时间', timestamp(item.captured_at)); field(dl, '声明操作人', item.operator); field(dl, '原记录故障', faults[item.fault || 'invalid_data']);
      field(dl, '隔离原因', item.reason, true);
      if (item.repair) {
        field(dl, '修复时间', timestamp(item.repair.at)); field(dl, '修复操作人', item.repair.operator); field(dl, '修复结果', dispositions[item.repair.disposition]);
        field(dl, '修复原因', item.repair.reason, true);
      }
    } else if (item.type === 'content_effect' && item.result) {
      field(dl, '内容 ID', item.result.post_id); field(dl, '当次版本', item.result.previous_version + ' → ' + item.result.version); field(dl, '是否修改', item.result.changed ? '已提交变更' : '无变化，沿用版本');
      field(dl, '提交时间', timestamp(item.result.committed_at), true);
      row.append(node('p', 'shared-review-record-note', '这是原操作的提交结果，不代表内容当前版本。'));
    } else if (item.webhook) {
      field(dl, '接受时间', timestamp(item.webhook.created_at)); field(dl, '记录时间', timestamp(item.webhook.updated_at));
      if (item.webhook.result_at) field(dl, '原结果记录时间', timestamp(item.webhook.result_at));
      if (item.webhook.outcome.code) field(dl, '原结果', webhookCodes[item.webhook.outcome.code] + (item.webhook.outcome.http_status ? '（HTTP ' + item.webhook.outcome.http_status + '）' : ''), true);
      row.append(node('p', 'shared-review-record-note', 'HTTP 确认不等于接收方业务完成。维护不会重新发送；业务结果可到 Webhook 核对页确认。'));
    } else if (item.type === 'authority' && item.revision) {
      field(dl, '授权修订号', item.revision); field(dl, '启用状态', item.enabled ? '已启用' : '已停用'); field(dl, '授权状态', item.retired ? '已退休，须重新核对' : '未退休');
    }
    if (item.index_mismatch) row.append(node('p', 'shared-review-record-note', '此记录还存在索引不一致。'));
    const details = node('details'), identifiers = node('dl');
    details.append(node('summary', '', '查看完整标识与核对摘要'), identifiers);
    if (item.id) field(identifiers, '隔离 ID', item.id, true, true);
    if (item.job_id) field(identifiers, '任务 ID', item.job_id, true, true);
    if (item.result) field(identifiers, '操作 ID', item.result.operation_id, true, true);
    if (item.webhook) field(identifiers, '操作 ID', item.webhook.operation_id, true, true);
    field(identifiers, '原记录摘要', item.reference, true, true); field(identifiers, '原记录字节数', item.bytes);
    if (item.repair) {
      field(identifiers, '修复后摘要', item.repair.output_sha256, true, true);
      if (item.repair.input_sha256) field(identifiers, '替换文件摘要', item.repair.input_sha256, true, true);
      if (item.repair.kept_reference) field(identifiers, '明确保留的记录', item.repair.kept_reference, true, true);
    }
    row.append(details); return row;
  }
  async function request(path:string, ticket:number):Promise<unknown> {
    const controller = new AbortController(); controllers.add(controller);
    const timeout = setTimeout(() => controller.abort('timeout'), 12000);
    try {
      const response = await fetch(path, {method: 'GET', credentials: 'same-origin', redirect: 'error', cache: 'no-store', headers: {Accept: 'application/json', [ownerHeader]: owner}, signal: controller.signal});
      if (serial !== ticket || root.dataset.userId !== owner || controller.signal.aborted) throw new DOMException('已取消', 'AbortError');
      if ([401, 403, 409].includes(response.status)) { deny('登录账号或访问权限已变化，已清除核对结果。请重新打开页面。'); throw new DOMException('会话已变化', 'AbortError'); }
      if (!response.ok) {
        if (response.status === 429) throw new Error('查询过于频繁，已清除旧结果。请稍后手动查询。');
        if (response.status === 503) throw new Error('共享记录暂不可用，请稍后手动查询。文件存储不提供此功能。');
        throw new Error('未能读取记录，请稍后手动查询。');
      }
      if (response.headers.get(ownerHeader) !== owner) { deny('无法确认结果所属账号，已清除核对结果。请重新打开页面。'); throw new DOMException('账号未确认', 'AbortError'); }
      const raw = await response.text();
      if (raw.length > 2 * 1024 * 1024) throw malformed();
      // Validate the original syntax before rewriting integer tokens. A marker
      // absent from the source distinguishes numeric tokens from user text.
      // BigInt keeps int64 values exact without turning numeric audit text into
      // strings that would pass the projection validator.
      let data:unknown;
      try { data=parseReviewJSON(raw); } catch { throw malformed(); }
      if (serial !== ticket || root.dataset.userId !== owner || controller.signal.aborted) throw new DOMException('已取消', 'AbortError');
      return data;
    } catch (error) {
      if (controller.signal.reason === 'timeout' && serial === ticket) throw new Error('读取超时，请稍后手动查询。');
      throw error;
    } finally { clearTimeout(timeout); controllers.delete(controller); }
  }
  async function confirmSession(ticket:number) {
    const data = await request('/api/v1/admin/plugin-shared-review-session', ticket);
    if (!isObject(data) || data.user_id !== owner || typeof data.available !== 'boolean') throw malformed();
    if (!data.available) throw new Error('当前存储不提供共享记录核对，已清除结果。');
  }
  function scheduleCheck(ticket:number) {
    clearTimeout(heartbeat);
    if (!loaded || suspended || locked) return;
    heartbeat = setTimeout(async () => {
      if (serial !== ticket || !loaded || suspended || locked) return;
      try { await confirmSession(ticket); if (serial === ticket) scheduleCheck(ticket); }
      catch (error) { if (serial === ticket) invalidate(pluginError(error).name === 'TypeError' ? '连接中断，已清除核对结果；恢复连接后请重新查询。' : pluginError(error).message, 'error'); }
    }, 15000);
  }
  function query():RecordQuery {
    return {scope: required<HTMLInputElement>(form,'[data-review-scope]:checked').value, type: $<HTMLSelectElement>('[data-review-type]').value, plugin: $<HTMLInputElement>('[data-review-plugin]').value.trim(), job: $<HTMLInputElement>('[data-review-job]').value.trim(), limit: Number($<HTMLSelectElement>('[data-review-limit]').value)};
  }
  async function load(start = 0, supplied:RecordQuery|null = null) {
    if (locked || suspended) return;
    if (!form.reportValidity()) return;
    const q = supplied || query();
    invalidate('正在读取记录…'); offset = start;
    const ticket = serial;
    title.textContent = q.scope === 'quarantines' ? '隔离与修复审计' : '原记录与结果';
    results.setAttribute('aria-busy', 'true');
    const params = new URLSearchParams({limit: String(q.limit), offset: String(start)});
    (['type', 'plugin', 'job'] as const).forEach(key => { if (q[key]) params.set(key, q[key]); });
    try {
      const data = await request('/api/v1/admin/plugin-shared-' + (q.scope === 'quarantines' ? 'quarantines' : 'records') + '?' + params, ticket);
      if (serial !== ticket) return;
      if (!validRecordPage(data, q, start)) throw malformed();
      if (data.fatal) {
        report(data.fatal === 'invalid_quarantine' ? '隔离证据无法校验，无法展示可核对结果。请由运维人员核查完整备份与隔离记录。' : '保留记录超出安全读取预算，无法展示可核对结果。请由运维人员处理。', 'error');
        return;
      }
      await confirmSession(ticket);
      if (serial !== ticket || suspended || locked) return;
      const rows = data.items.map(item => renderRecord(item, q.scope));
      list.replaceChildren(...rows);
      loaded = true; displayedQuery = q; nextOffset = data.next_offset;
      previous.disabled = start === 0; next.disabled = nextOffset === -1;
      summary.textContent = '当前筛选共 ' + data.total + ' 条';
      pageLabel.textContent = data.items.length ? (start + 1) + '–' + (start + data.items.length) + ' / ' + data.total : '本页 0 条';
      report(data.items.length ? '已读取本页记录。各次查询之间记录可能变化。' : start > 0 ? '记录数量已变化，本页没有结果；可返回上一页或重新查询。' : '没有符合当前筛选的记录。');
      scheduleCheck(ticket);
    } catch (error) {
      if (serial === ticket) { clearResults(); report(pluginError(error).name === 'TypeError' ? '网络连接失败，旧结果已清除；恢复连接后请重新查询。' : pluginError(error).message, 'error'); }
    } finally { if (serial === ticket) results.setAttribute('aria-busy', 'false'); }
  }
  form.addEventListener('submit', event => { event.preventDefault(); void load(0); });
  form.addEventListener('input', () => { if (!locked) invalidate('筛选已变化，请查询以获取对应结果。'); });
  form.addEventListener('change', event => {
    if (locked) return;
    invalidate('筛选已变化，请查询以获取对应结果。');
    if (event.target === $<HTMLSelectElement>('[data-review-type]')) {
      const authority = $<HTMLSelectElement>('[data-review-type]').value === 'authority', job = $<HTMLInputElement>('[data-review-job]');
      job.disabled = authority; if (authority) job.value = '';
    }
    if (event.target && 'hasAttribute' in event.target && typeof event.target.hasAttribute==='function' && event.target.hasAttribute('data-review-scope')) void load(0);
  });
  previous.addEventListener('click', () => { if (loaded && displayedQuery && offset > 0) void load(Math.max(0, offset - displayedQuery.limit), displayedQuery); });
  next.addEventListener('click', () => { if (loaded && nextOffset >= 0) void load(nextOffset, displayedQuery); });
  const suspend = () => { suspended = true; invalidate('页面已离开，核对结果已清除；返回后请重新查询。'); };
  const resume = () => { if (suspended && !document.hidden) { suspended = false; if (!locked) report('请重新查询，以确认当前账号和记录。'); } };
  window.addEventListener('blur', suspend, {signal:eventScope.signal}); window.addEventListener('focus', resume, {signal:eventScope.signal});
  window.addEventListener('pagehide', suspend, {signal:eventScope.signal}); window.addEventListener('pageshow', event => { if (event.persisted) resume(); }, {signal:eventScope.signal});
  document.addEventListener('visibilitychange', () => { if (document.hidden) suspend(); else resume(); }, {signal:eventScope.signal});
  document.addEventListener('chunnel:session', () => deny('会话状态已变化，已清除核对结果。请重新打开页面。'), {signal:eventScope.signal});
  let channel:BroadcastChannel|undefined;
  try { channel = new BroadcastChannel('chunnel.social'); channel.addEventListener('message', event => { if (event.data?.type === 'session') deny('会话状态已变化，已清除核对结果。请重新打开页面。'); }); } catch { /* Focus checks and visible-session checks remain active. */ }
  document.addEventListener('chunnel:admin-before-replace', event => { const detail=(event as CustomEvent<import('./admin-types').AdminRegionChange>).detail; if(detail?.regions?.some(region=>region===root||region.contains(root))) { locked=true; suspend();eventScope.abort();channel?.close(); } }, {signal:eventScope.signal});
  void load();
})();
