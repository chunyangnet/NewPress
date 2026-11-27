(() => {
  'use strict';
  const root = document.querySelector('[data-update-center]');
  if (!root) return;
  const $ = name => root.querySelector(`[data-update-${name}]`);
  const readOnly = root.dataset.readonly === 'true';
  const phases = { idle: '准备就绪', checking: '正在检查版本', available: '发现新版本', current: '已是最新版本', downloading: '正在下载更新包', verifying: '正在校验完整性', staging: '正在准备新版本', queued: '等待切换', stopping: '正在停止旧进程', backup: '正在备份站点', starting: '正在检查服务', observing: '正在观察新版本', rollback: '正在恢复原版本', completed: '更新完成', rolled_back: '已恢复原版本', failed: '更新未完成', cancelled: '任务已取消' };
  const busyPhases = new Set(['checking', 'downloading', 'verifying', 'staging', 'queued', 'stopping', 'backup', 'starting', 'observing', 'rollback']);
  const terminal = new Set(['completed', 'rolled_back', 'failed', 'cancelled']);
  const ticketKey = 'newpress-update-ticket';
  let ticket = '', snapshot = null, state = {}, sending = false, previousBytes = 0, previousTime = 0, logSignature = '', reconnects = 0, loginRequired = false;
  const settingsController = window.ChunnelAdminUI?.savebar($('source'));
  const policyForm = $('policy-form');
  const policyInputs = policyForm.elements;
  const policyBar = policyForm.querySelector('[data-admin-savebar]');
  let policyBase = null, latestPolicy = null, policyEpoch = 0, policySaving = false, policyConflict = false;
  let policyMessage = '', policyError = false;
  function policyDraft() {
    return { auto_check: policyInputs.auto_check.checked,
      check_interval_minutes: policyInputs.check_interval_minutes.valueAsNumber,
      observation_seconds: policyInputs.observation_seconds.valueAsNumber };
  }
  function policyDirty() {
    return policyBase && Object.entries(policyDraft()).some(([key, value]) => value !== policyBase[key]);
  }
  function loadPolicy(value) {
    policyBase = latestPolicy = value;
    policyInputs.auto_check.checked = value.auto_check;
    policyInputs.check_interval_minutes.value = value.check_interval_minutes;
    policyInputs.observation_seconds.value = value.observation_seconds;
    policyConflict = policyError = false;
    policyMessage = '';
  }
  function syncPolicy(value) {
    if (!value?.revision || policySaving) return;
    latestPolicy = value;
    if (value.revision === policyBase?.revision) return;
    if (policyDirty()) policyConflict = true;
    else loadPolicy(value);
  }
  function policyControls(busy) {
    const disabled = busy || readOnly || !policyBase;
    $('policy-fields').disabled = disabled;
    if (!policyBase) return;
    policyInputs.check_interval_minutes.disabled = !policyInputs.auto_check.checked;
    $('policy-defaults').hidden = !policyBase.customized;
    $('policy-defaults').disabled = policyConflict;
    $('policy-conflict').hidden = !policyConflict;
    $('policy-origin').textContent = latestPolicy.customized ? '站点自定义' : '部署默认';
    $('policy-launcher').hidden = latestPolicy.launcher_applies_window;
    window.ChunnelAdminUI?.paintSavebar(policyBar, { dirty: policyDirty(), busy: policySaving,
      readOnly, disabled: disabled || policyConflict, message: policyMessage, error: policyError });
    policyForm.setAttribute('aria-busy', String(policySaving));
  }
  try { ticket = sessionStorage.getItem(ticketKey) || ''; } catch (_) { /* Private mode may block storage. */ }
  function storeTicket(value) { ticket = value; try { if (value) sessionStorage.setItem(ticketKey, value); else sessionStorage.removeItem(ticketKey); } catch (_) {} }
  function feedback(message, error = false) { $('feedback').textContent = message; $('feedback').hidden = !message; $('feedback').className = `np-update-feedback ${error ? 'is-error' : 'is-success'}`; }
  function requireLogin() {
    loginRequired = true;
    $('login').hidden = false;
    $('connection').textContent = '需要重新登录';
    feedback('登录会话已结束，请重新登录查看更新结果。', true);
    controls();
  }
  function bytes(n) { if (!n) return '0 B'; const units = ['B', 'KiB', 'MiB', 'GiB']; const k = Math.min(3, Math.floor(Math.log(n) / Math.log(1024))); return `${(n / 1024 ** k).toFixed(k ? 1 : 0)} ${units[k]}`; }
  async function api(path = '', method = 'GET', body, progress = false) {
    const headers = { 'Accept': 'application/json' };
    if (progress) headers['X-Update-Ticket'] = ticket;
    if (method !== 'GET') headers['X-CSRF-Token'] = root.dataset.csrf;
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const response = await fetch(`/api/v1/admin/updates${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), cache: 'no-store', credentials: 'same-origin', signal: AbortSignal.timeout(12000) });
    let data; try { data = await response.json(); } catch (_) { throw new Error('服务正在切换或暂不可用，正在等待重新连接。'); }
    if (!response.ok) { const err = new Error(data.error?.message || data.message || '请求未完成，请稍后重试。'); err.status = response.status; err.code = data.error?.code; if (err.status === 401 && !progress) requireLogin(); throw err; }
    return data;
  }
  function protectedRelease() { return state.phase === 'completed' && Date.parse(state.protected_until) > Date.now(); }
  function controls() {
    const busy = busyPhases.has(state.phase) || protectedRelease() || sending || loginRequired;
    $('fields').disabled = readOnly || busy;
    settingsController?.refresh(true);
    policyControls(busy);
    const save = $('source').querySelector('[data-admin-save]');
    if (save && busy) save.disabled = true;
    $('check').disabled = readOnly || busy || !snapshot?.configured || !snapshot?.trusted_publisher;
    $('install').hidden = !snapshot?.available || terminal.has(state.phase) && ticket !== '';
    $('start').disabled = readOnly || busy || !snapshot?.managed || !snapshot?.revision || !$('backup').checked;
    $('cancel').hidden = !['checking', 'downloading', 'verifying', 'staging'].includes(state.phase) || readOnly;
    $('cancel').disabled = sending || loginRequired;
  }
  function renderState(next) {
    const changed = state.phase !== next.phase;
    state = next; root.dataset.phase = state.phase;
    $('phase').textContent = protectedRelease() ? '更新完成，重启保护中' : phases[state.phase] || '等待任务';
    $('badge').textContent = phases[state.phase] || '等待检查';
    $('percent').textContent = Math.max(0, Math.min(100, state.percent || 0));
    $('meter').value = state.percent || 0;
    const logs = state.logs || [];
    $('detail').textContent = logs.at(-1)?.message || '配置更新源，然后检查可用版本。';
    if (changed) $('announcement').textContent = phases[state.phase] || '';
    $('bytes').textContent = state.total ? `${bytes(state.downloaded)} / ${bytes(state.total)}` : '等待下载';
    const now = Date.now();
    if (state.phase === 'downloading' && previousTime && state.downloaded >= previousBytes) $('speed').textContent = `${bytes(Math.round((state.downloaded - previousBytes) * 1000 / (now - previousTime)))}/s`;
    else $('speed').textContent = '';
    previousTime = now; previousBytes = state.downloaded || 0;
    const end = busyPhases.has(state.phase) ? now : Date.parse(state.updated_at);
    const seconds = Math.max(0, Math.floor((end - Date.parse(state.started_at)) / 1000));
    $('elapsed').textContent = state.id && Number.isFinite(seconds) ? `用时 ${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}` : '尚未开始';
    const step = ({ downloading: 0, verifying: 1, staging: 2, queued: 3, stopping: 3, backup: 3, starting: 4, observing: 4, completed: 5 })[state.phase] ?? -1;
    root.querySelectorAll('[data-step]').forEach(el => { const n = Number(el.dataset.step); el.classList.toggle('is-done', n < step); el.classList.toggle('is-current', n === step); });
    const signature = JSON.stringify(logs);
    if (signature !== logSignature) {
      logSignature = signature; $('logs').replaceChildren();
      for (const log of logs) {
        const row = document.createElement('div'); row.className = `np-update-log is-${['error', 'warning'].includes(log.level) ? log.level : 'info'}`;
        const time = document.createElement('time'); time.textContent = window.ChunnelTime?.formatClock(log.time) || ''; time.dateTime = log.time;
        const level = document.createElement('span'); level.textContent = (log.level || 'info').toUpperCase();
        const message = document.createElement('p'); message.textContent = log.message;
        row.append(time, level, message); $('logs').append(row);
      }
      if (!logs.length) { const empty = document.createElement('p'); empty.className = 'np-update-log-empty'; empty.textContent = '暂无更新任务。每一个步骤，都会记录在这里。'; $('logs').append(empty); }
      $('log-count').textContent = logs.length;
      if ($('follow').checked) $('logs').scrollTop = $('logs').scrollHeight;
    }
    if (state.phase === 'completed') $('current').textContent = state.target;
    if (state.phase === 'rolled_back' && state.previous) $('current').textContent = state.previous;
    if ((ticket || busyPhases.has(state.phase)) && state.target) $('target').textContent = state.target;
    $('login').hidden = !(loginRequired || ticket && ['completed', 'rolled_back'].includes(state.phase));
    controls();
  }
  function sourceControls() {
    const form = $('source'), official = Boolean(form.elements.official?.checked);
    for (const key of ['owner', 'repo', 'ref', 'manifest']) {
      if (!form.elements[key]) continue;
      form.elements[key].disabled = official || readOnly;
      form.elements[key].required = !official;
    }
    form.elements.token.disabled = official || Boolean(snapshot?.environment_token) || readOnly;
    form.elements.clear_token.disabled = official || Boolean(snapshot?.environment_token) || readOnly;
  }
  function renderSnapshot(value) {
    if (ticket && terminal.has(value.state.phase) && !(value.state.phase === 'completed' && Date.parse(value.state.protected_until) > Date.now())) storeTicket('');
    if (snapshot?.revision !== value.revision) $('backup').checked = false;
    const discovery = value.discovery;
    $('discovery').textContent = [value.trusted_publisher ? '' : '请由运维配置核心发布公钥', discovery?.interval && discovery.interval !== '0s' ? `自动检查间隔 ${discovery.interval}` : '定时检查已关闭', discovery?.message].filter(Boolean).join(' · ');
    snapshot = value; $('current').textContent = value.current;
    $('target').textContent = value.available?.version || '—';
    $('notes-wrap').hidden = !value.available?.notes; $('notes').textContent = value.available?.notes || '';
    $('token-status').textContent = value.environment_token ? '由环境配置' : value.token_set ? '已保存' : '未配置';
    sourceControls();
    $('source-label').textContent = value.source?.official ? 'GitHub / Gitee 公开双源' : 'Gitee 私有发布源';
    renderState(value.state);
  }
  async function loadSnapshot() {
    const at = policyEpoch;
    const value = await api();
    if (at !== policyEpoch) return;
    syncPolicy(value.policy);
    renderSnapshot(value);
  }
  async function refresh() {
    try {
      if (ticket) renderState(await api('/progress', 'GET', undefined, true));
      else await loadSnapshot();
      reconnects = 0;
      if (!loginRequired) $('connection').textContent = '● 实时同步';
    } catch (err) {
      if (err.status === 401 && ticket) { storeTicket(''); return refresh(); }
      if (err.status === 401) { requireLogin(); return; }
      reconnects++; $('connection').textContent = '重新连接中…';
      if (reconnects > 20) feedback('连接仍未恢复。更新任务在服务端继续执行，请检查服务状态；不要重复提交安装。', true);
    }
  }
  async function action(fn) {
    if (sending || readOnly || loginRequired) return; sending = true; controls();
    try { feedback(''); await fn(); await refresh(); } catch (err) { feedback(err.message, true); }
    finally { sending = false; controls(); }
  }
  const policyBusy = () => readOnly || sending || loginRequired || busyPhases.has(state.phase) || protectedRelease() || !policyBase;
  function savePolicy(useDefaults) {
    if (policyBusy() || policyConflict || (!useDefaults && !policyDirty())) return;
    const body = { revision: policyBase.revision, ...(useDefaults ? { use_defaults: true } : policyDraft()) };
    action(async () => {
      policySaving = true;
      policyEpoch++;
      controls();
      try {
        loadPolicy(await api('/policy', 'PUT', body));
        policyMessage = useDefaults ? '已恢复部署默认值' : '策略已保存';
      } catch (err) {
        policyError = true;
        policyMessage = err.message;
        if (err.code === 'updates.policy_changed') policyConflict = true;
      } finally {
        policyEpoch++;
        policySaving = false;
      }
    });
  }
  policyForm.addEventListener('input', event => {
    const interval = policyInputs.check_interval_minutes;
    if (event.target === policyInputs.auto_check && !event.target.checked && !interval.validity.valid && policyBase) {
      interval.value = policyBase.check_interval_minutes;
    }
    policyMessage = ''; policyError = false; controls();
  });
  policyForm.addEventListener('submit', event => { event.preventDefault(); savePolicy(false); });
  window.addEventListener('beforeunload', event => {
    if (!readOnly && (policyDirty() || policySaving)) { event.preventDefault(); event.returnValue = ''; }
  });
  policyBar.querySelector('[data-admin-discard]').addEventListener('click', () => {
    if (!policyBusy()) { loadPolicy(latestPolicy); controls(); }
  });
  $('policy-defaults').addEventListener('click', () => savePolicy(true));
  $('policy-reload').addEventListener('click', () => {
    if (!policyBusy()) action(async () => { const value = await api(); policyEpoch++; loadPolicy(value.policy); });
  });
  $('source').elements.official?.addEventListener('change', sourceControls);
  $('source').addEventListener('submit', event => {
    event.preventDefault(); if (sending || readOnly || loginRequired || busyPhases.has(state.phase) || protectedRelease()) return;
    if (settingsController && !settingsController.start()) return;
    action(async () => {
      const form = $('source');
      const body = form.elements.official?.checked ? { official: true } :
        Object.fromEntries(['owner', 'repo', 'ref', 'manifest', 'token'].map(key => [key, form.elements[key].value.trim()]));
      if (!body.official) body.clear_token = form.elements.clear_token.checked;
      try {
        await api('/source', 'PUT', body); form.elements.token.value = ''; form.elements.clear_token.checked = false; storeTicket('');
        settingsController?.success(settingsController.snapshot());
        if (!settingsController) feedback('已保存');
      } catch (error) { if (settingsController) settingsController.fail(error.message); else throw error; }
    });
  });
  $('check').addEventListener('click', () => action(async () => { await api('/check', 'POST'); storeTicket(''); }));
  $('backup').addEventListener('change', controls);
  $('start').addEventListener('click', () => {
    if ($('start').disabled || !snapshot?.available) return;
    action(async () => {
      const result = await api('/start', 'POST', { version: snapshot.available.version, revision: snapshot.revision, backup_confirmed: $('backup').checked });
      $('backup').checked = false;
      storeTicket(result.ticket);
    });
  });
  $('cancel').addEventListener('click', () => action(() => api('/cancel', 'POST')));
  $('export').addEventListener('click', () => {
    const text = (state.logs || []).map(log => `${log.time} [${log.level.toUpperCase()}] ${log.message}`).join('\n');
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }));
    const link = document.createElement('a'); link.href = url; link.download = `newpress-update-${state.id || 'logs'}.txt`; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  });
  async function poll() { await refresh(); if (!loginRequired || ticket) setTimeout(poll, busyPhases.has(state.phase) || reconnects ? 1000 : 5000); }
  // Get source/version metadata once even when resuming a saved progress receipt.
  controls();
  loadSnapshot().catch(() => {}).finally(poll);
})();
