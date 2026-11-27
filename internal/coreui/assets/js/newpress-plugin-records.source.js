(() => {
  'use strict';
  const root = document.querySelector('[data-shared-review]');
  if (!root || root.dataset.available !== 'true') return;
  const $ = selector => root.querySelector(selector);
  const form = $('[data-review-form]'), list = $('[data-review-list]');
  const status = $('[data-review-status]'), results = $('[data-review-results]');
  const previous = $('[data-review-prev]'), next = $('[data-review-next]');
  const summary = $('[data-review-summary]'), pageLabel = $('[data-review-page]');
  const access = $('[data-review-access]'), title = $('[data-review-title]');
  const owner = root.dataset.userId, ownerHeader = 'X-NewPress-Review-User';
  const controllers = new Set();
  const faults = {invalid_data: '原记录无法完整校验', index_mismatch: '记录与索引不一致', duplicate_identity: '逻辑身份重复'};
  const dispositions = {receipt_preserved: '保留原内容结果', authority_retired: '授权已退休', duplicate_preserved: '保留相同内容回执', duplicate_authority_retired: '保留已退休的授权', webhook_cancelled: '已取消待发送意图', webhook_uncertain: '已领取发送保留为待核对', webhook_preserved: '保留原发送结果', duplicate_webhook_preserved: '保留相同且不会重发的操作'};
  const webhookStates = {pending: '等待源任务成功', sending: '发送已领取', cancelled: '发送前已取消', uncertain: '待人工核对', http_confirmed: 'HTTP 已确认', reviewed_received: '人工确认已收到', reviewed_not_received: '人工确认未收到'};
  const webhookCodes = {http_2xx: '已收到完整 2xx 响应', http_status: '非 2xx 响应，业务结果待核对', transport_unconfirmed: '未取得可确认响应', response_unconfirmed: '响应不完整或超限', worker_expired: '领取到期，结果无法确认', restored: '数据库恢复后停止旧操作', maintenance: '离线维护已停止旧操作', source_cancelled: '源任务已取消', authority_changed: '发送前授权已变化'};
  let serial = 0, offset = 0, nextOffset = -1, loaded = false, locked = false, suspended = document.hidden;
  let heartbeat = 0, displayedQuery = null;
  const isObject = value => value !== null && typeof value === 'object' && !Array.isArray(value);
  const integer = (value, min = 0, max = Number.MAX_SAFE_INTEGER) => Number.isSafeInteger(value) && value >= min && value <= max;
  const domainInteger = value => integer(value, 1) || typeof value === 'bigint' && value > 0n && value <= 9223372036854775807n;
  const digest = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
  const text = (value, max) => typeof value === 'string' && value.length <= max;
  const date = value => text(value, 64) && /^\d{4}-\d\d-\d\dT/.test(value) && Number.isFinite(Date.parse(value));
  const hasFault = value => Object.prototype.hasOwnProperty.call(faults, value);
  const malformed = () => new Error('返回记录不完整或格式异常，请稍后重新查询。');
  const report = (message, tone = '') => { status.textContent = message; status.dataset.tone = tone; };
  function clearResults() {
    loaded = false; displayedQuery = null; nextOffset = -1;
    list.replaceChildren(); summary.textContent = '尚未取得结果'; pageLabel.textContent = '—';
    previous.disabled = next.disabled = true;
    results.setAttribute('aria-busy', 'false');
  }
  function invalidate(message, tone = '') {
    serial++; clearTimeout(heartbeat);
    controllers.forEach(controller => controller.abort()); controllers.clear();
    clearResults(); report(message, tone);
  }
  function deny(message) {
    locked = true; access.hidden = false;
    invalidate(message, 'error');
    form.querySelectorAll('input, select, button').forEach(control => { control.disabled = true; });
  }
  function validRecord(item, scope) {
    if (!isObject(item) || !['authority', 'content_effect', 'webhook_intent'].includes(item.type) || !digest(item.reference) || !integer(item.bytes, 0, 144 * 1024)) return false;
    if (scope === 'quarantines') {
      if (!digest(item.id) || !hasFault(item.fault) || !date(item.captured_at) || !text(item.operator, 128) || !item.operator || !text(item.reason, 1024) || !item.reason) return false;
      if (item.repair === undefined) return true;
      const repair = item.repair;
      if (!isObject(repair) || !date(repair.at) || Date.parse(repair.at) < Date.parse(item.captured_at) || !text(repair.operator, 128) || !repair.operator || !text(repair.reason, 1024) || !repair.reason || !digest(repair.output_sha256)) return false;
      const allowed = item.type === 'authority' ? ['authority_retired', 'duplicate_authority_retired'] : item.type === 'webhook_intent' ? ['webhook_cancelled', 'webhook_uncertain', 'webhook_preserved', 'duplicate_webhook_preserved'] : ['receipt_preserved', 'duplicate_preserved'];
      if (!allowed.includes(repair.disposition)) return false;
      if (repair.input_sha256 !== undefined && !digest(repair.input_sha256)) return false;
      const duplicate = repair.disposition.startsWith('duplicate_');
      return duplicate ? digest(repair.kept_reference) && repair.kept_reference === repair.output_sha256 && repair.input_sha256 === undefined : repair.kept_reference === undefined;
    }
    if (item.fault !== undefined && !hasFault(item.fault)) return false;
    if (item.index_mismatch !== undefined && typeof item.index_mismatch !== 'boolean') return false;
    if (item.plugin !== undefined && (!text(item.plugin, 64) || !/^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$/.test(item.plugin))) return false;
    if (item.type === 'authority') {
      if (item.result !== undefined || item.job_id !== undefined || item.webhook !== undefined) return false;
      if (item.revision !== undefined && !domainInteger(item.revision)) return false;
      if (['enabled', 'retired'].some(key => item[key] !== undefined && typeof item[key] !== 'boolean')) return false;
      return item.fault === 'invalid_data' || !!item.plugin && domainInteger(item.revision) && !(item.enabled && item.retired);
    }
    if (item.revision !== undefined || item.enabled !== undefined || item.retired !== undefined) return false;
    if (item.job_id !== undefined && !digest(item.job_id)) return false;
    if (item.type === 'webhook_intent') {
      if (item.result !== undefined) return false;
      if (item.webhook === undefined) return item.fault === 'invalid_data';
      const w = item.webhook, versions = {pending: 1, sending: 2, cancelled: 2, uncertain: 3, http_confirmed: 3, reviewed_received: 4, reviewed_not_received: 4};
      if (!item.plugin || !digest(item.job_id) || !isObject(w) || !digest(w.operation_id) || !Object.hasOwn(versions, w.state) || w.version !== versions[w.state] || !date(w.created_at) || !date(w.updated_at) || Date.parse(w.updated_at) < Date.parse(w.created_at) || !isObject(w.outcome)) return false;
      const active = ['pending', 'sending'].includes(w.state), o = w.outcome;
      if (active ? Object.keys(o).length !== 0 : !Object.hasOwn(webhookCodes, o.code) || (w.state === 'http_confirmed') !== (o.code === 'http_2xx')) return false;
      if (['pending', 'sending', 'cancelled'].includes(w.state) ? w.result_at !== undefined : !date(w.result_at) || Date.parse(w.result_at) < Date.parse(w.created_at) || Date.parse(w.result_at) > Date.parse(w.updated_at)) return false;
      return !(o.http_status !== undefined && !integer(o.http_status, 100, 599) || o.response_bytes !== undefined && !integer(o.response_bytes, 0, 32768) || o.response_digest !== undefined && !digest(o.response_digest));
    }
    if (item.webhook !== undefined) return false;
    if (item.result === undefined) return item.fault === 'invalid_data';
    const result = item.result;
    return !!item.plugin && digest(item.job_id) && isObject(result) && digest(result.operation_id) && domainInteger(result.post_id) && domainInteger(result.previous_version) && domainInteger(result.version) && typeof result.changed === 'boolean' && BigInt(result.version) === BigInt(result.previous_version) + (result.changed ? 1n : 0n) && date(result.committed_at);
  }
  function validPage(data, query, start) {
    if (!isObject(data) || !Array.isArray(data.items) || data.items.length > query.limit || !integer(data.total, 0, 130000) || !integer(data.next_offset, -1, 130000)) return false;
    if (data.fatal !== undefined) return query.scope === 'records' && ['invalid_quarantine', 'storage_capacity'].includes(data.fatal) && data.items.length === 0 && data.total === 0 && data.next_offset === -1;
    const expectedCount = Math.min(query.limit, Math.max(0, data.total - start));
    if (data.items.length !== expectedCount || data.next_offset !== (start + expectedCount < data.total ? start + expectedCount : -1)) return false;
    const seen = new Set();
    return data.items.every(item => {
      if (!validRecord(item, query.scope) || query.type && item.type !== query.type) return false;
      if (query.scope === 'records' && (query.plugin && item.plugin !== query.plugin || query.job && item.job_id !== query.job)) return false;
      const key = query.scope === 'records' ? item.reference : item.id;
      if (seen.has(key)) return false;
      seen.add(key); return true;
    });
  }
  const node = (tag, className = '', value = '') => {
    const element = document.createElement(tag); element.className = className; element.textContent = value; return element;
  };
  const field = (dl, label, value, full = false, code = false) => {
    const group = node('div', full ? 'shared-review-full' : '');
    const dd = node('dd'); dd.append(node(code ? 'code' : 'span', '', String(value)));
    group.append(node('dt', '', label), dd); dl.append(group);
  };
  const timestamp = value => {
    const time = new Date(value);
    const zone = document.documentElement?.dataset.siteTimezone || 'UTC';
    try { return new Intl.DateTimeFormat('zh-CN', {timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false, timeZoneName: 'short'}).format(time); }
    catch { return time.toISOString(); }
  };
  function renderRecord(item, scope) {
    const row = node('li', 'shared-review-record'), head = node('header'), dl = node('dl');
    const kind = item.type === 'authority' ? '共享授权' : item.type === 'webhook_intent' ? 'Webhook 意图' : '内容回执';
    const label = scope === 'quarantines' ? kind + ' · 隔离记录' : (item.plugin || '身份无法确认') + ' · ' + kind;
    const state = scope === 'quarantines' ? item.repair ? '已修复 · 保留审计' : '未修复 · 共享执行阻断' : item.fault ? faults[item.fault] : item.type === 'authority' ? item.retired ? '已退休' : item.enabled ? '已启用' : '已停用' : item.webhook ? webhookStates[item.webhook.state] : '原提交已记录';
    const badge = node('span', 'shared-review-tag', state);
    if (scope === 'quarantines' ? !item.repair : !!item.fault) badge.dataset.tone = 'warning';
    head.append(node('h3', '', label), badge); row.append(head, dl);
    if (scope === 'quarantines') {
      field(dl, '隔离时间', timestamp(item.captured_at)); field(dl, '声明操作人', item.operator); field(dl, '原记录故障', faults[item.fault]);
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
  async function request(path, ticket) {
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
      let data;
      try {
        const normalized = JSON.stringify(JSON.parse(raw));
        let marker = '__review_integer_';
        while (normalized.includes(marker)) marker += '_';
        const integers = [];
        data = JSON.parse(raw.replace(/"(?:[^"\\]|\\.)*"|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g, (token, number) =>
          number && /^-?\d+$/.test(number) && !Number.isSafeInteger(Number(number)) ? JSON.stringify(marker + (integers.push(BigInt(number)) - 1)) : token),
          (_, value) => typeof value === 'string' && value.startsWith(marker) ? integers[Number(value.slice(marker.length))] : value);
      } catch { throw malformed(); }
      if (serial !== ticket || root.dataset.userId !== owner || controller.signal.aborted) throw new DOMException('已取消', 'AbortError');
      return data;
    } catch (error) {
      if (controller.signal.reason === 'timeout' && serial === ticket) throw new Error('读取超时，请稍后手动查询。');
      throw error;
    } finally { clearTimeout(timeout); controllers.delete(controller); }
  }
  async function confirmSession(ticket) {
    const data = await request('/api/v1/admin/plugin-shared-review-session', ticket);
    if (!isObject(data) || data.user_id !== owner || typeof data.available !== 'boolean') throw malformed();
    if (!data.available) throw new Error('当前存储不提供共享记录核对，已清除结果。');
  }
  function scheduleCheck(ticket) {
    clearTimeout(heartbeat);
    if (!loaded || suspended || locked) return;
    heartbeat = setTimeout(async () => {
      if (serial !== ticket || !loaded || suspended || locked) return;
      try { await confirmSession(ticket); if (serial === ticket) scheduleCheck(ticket); }
      catch (error) { if (serial === ticket) invalidate(error.name === 'TypeError' ? '连接中断，已清除核对结果；恢复连接后请重新查询。' : error.message, 'error'); }
    }, 15000);
  }
  function query() {
    return {scope: form.querySelector('[data-review-scope]:checked').value, type: $('[data-review-type]').value, plugin: $('[data-review-plugin]').value.trim(), job: $('[data-review-job]').value.trim(), limit: Number($('[data-review-limit]').value)};
  }
  async function load(start = 0, supplied = null) {
    if (locked || suspended) return;
    if (!form.reportValidity()) return;
    const q = supplied || query();
    invalidate('正在读取记录…'); offset = start;
    const ticket = serial;
    title.textContent = q.scope === 'quarantines' ? '隔离与修复审计' : '原记录与结果';
    results.setAttribute('aria-busy', 'true');
    const params = new URLSearchParams({limit: String(q.limit), offset: String(start)});
    ['type', 'plugin', 'job'].forEach(key => { if (q[key]) params.set(key, q[key]); });
    try {
      const data = await request('/api/v1/admin/plugin-shared-' + (q.scope === 'quarantines' ? 'quarantines' : 'records') + '?' + params, ticket);
      if (serial !== ticket) return;
      if (!validPage(data, q, start)) throw malformed();
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
      if (serial === ticket) { clearResults(); report(error.name === 'TypeError' ? '网络连接失败，旧结果已清除；恢复连接后请重新查询。' : error.message, 'error'); }
    } finally { if (serial === ticket) results.setAttribute('aria-busy', 'false'); }
  }
  form.addEventListener('submit', event => { event.preventDefault(); void load(0); });
  form.addEventListener('input', () => { if (!locked) invalidate('筛选已变化，请查询以获取对应结果。'); });
  form.addEventListener('change', event => {
    if (locked) return;
    invalidate('筛选已变化，请查询以获取对应结果。');
    if (event.target === $('[data-review-type]')) {
      const authority = event.target.value === 'authority', job = $('[data-review-job]');
      job.disabled = authority; if (authority) job.value = '';
    }
    if (event.target.hasAttribute('data-review-scope')) void load(0);
  });
  previous.addEventListener('click', () => { if (loaded && offset > 0) void load(Math.max(0, offset - displayedQuery.limit), displayedQuery); });
  next.addEventListener('click', () => { if (loaded && nextOffset >= 0) void load(nextOffset, displayedQuery); });
  const suspend = () => { suspended = true; invalidate('页面已离开，核对结果已清除；返回后请重新查询。'); };
  const resume = () => { if (suspended && !document.hidden) { suspended = false; if (!locked) report('请重新查询，以确认当前账号和记录。'); } };
  window.addEventListener('blur', suspend); window.addEventListener('focus', resume);
  window.addEventListener('pagehide', suspend); window.addEventListener('pageshow', event => { if (event.persisted) resume(); });
  document.addEventListener('visibilitychange', () => { if (document.hidden) suspend(); else resume(); });
  document.addEventListener('chunnel:session', () => deny('会话状态已变化，已清除核对结果。请重新打开页面。'));
  try { const channel = new BroadcastChannel('chunnel.social'); channel.addEventListener('message', event => { if (event.data?.type === 'session') deny('会话状态已变化，已清除核对结果。请重新打开页面。'); }); } catch { /* Focus checks and visible-session checks remain active. */ }
  void load();
})();
