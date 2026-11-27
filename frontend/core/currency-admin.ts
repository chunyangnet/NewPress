import type {} from '../shared/account-types';
import type {} from '../shared/currency-types';
import { failure, record } from '../shared/ledger-data';
import { currencySettingsResult, currencyCreation, currencyAdjustment, adminWallet } from './ledger-admin-data';
import type { AdminCurrencySettings, AdminWallet } from './ledger-admin-data';
import { required, control, input, readOnly as setReadOnly } from './ledger-admin-dom';
import type { Control } from './ledger-admin-dom';
interface Module { key: string; element: HTMLElement; fields: Control[] }
(() => {
  'use strict';
  const host = document.querySelector<HTMLElement>('[data-currency-admin]'), shared = window.ChunnelCurrency;
  if (!host || !shared) return;
  const root = host, C = shared, owner = root.dataset.userId || '', currency = root.dataset.currencyKey || '';
  if (!C.currencyKey(currency) || !C.id(owner) && root.dataset.readOnly !== 'true') return;
  const $ = <T extends HTMLElement = HTMLElement>(selector: string): T => required<T>(root, selector), base = '/api/v1/admin/currencies/' + encodeURIComponent(currency);
  const settings = $<HTMLFormElement>('[data-currency-settings]'), adjust = $<HTMLFormElement>('[data-currency-adjust]'), lookup = $<HTMLFormElement>('[data-currency-lookup]');
  const create = $<HTMLFormElement>('[data-currency-create]'), switchForm = $<HTMLFormElement>('[data-currency-switch]'), select = $<HTMLSelectElement>('[data-currency-select]');
  const status = $('[data-currency-status]'), adjustStatus = $('[data-currency-adjust-status]'), createStatus = $('[data-currency-create-status]');
  const readOnly = root.dataset.readOnly === 'true';
  const fields = [...settings.querySelectorAll<Control>('[data-currency-field]')];
  const modules: Module[] = [...settings.querySelectorAll<HTMLElement>('[data-currency-module]')].map(element => ({
    key: element.dataset.currencyModule || '', element, fields: [...element.querySelectorAll<Control>('[data-currency-field]')],
  }));
  const raw = (field: Control) => field.dataset.currencyField === 'toggle' && field instanceof HTMLInputElement ? field.checked : field.value;
  let baseline = Object.fromEntries(fields.map(field => [field.name, raw(field)]));
  let version = input(settings, 'version').value, precision = Number(input(settings, 'precision').value);
  let dirty = false, saving = false, adjusting = false, creating = false, leaving = false, accessLost = false, sessionEpoch = 0;
  let selected: AdminWallet | null = null, lookupSerial = 0;
  let lookupController: AbortController | undefined;
  const controllers = new Set<AbortController>();
  const csrf = root.dataset.csrf || '';
  let saveFeedback = '', saveError = false;
  let pending = C.readPending('adjust', owner, currency), creation = C.readPending('create', owner);

  function loseAccess() {
    if (accessLost) return;
    accessLost = true; sessionEpoch++; lookupSerial++; selected = null; controllers.forEach(controller => controller.abort());
    $('[data-currency-account]').hidden = true; $('[data-currency-admin-history]').replaceChildren(); $<HTMLFormElement>('[data-currency-more-form]').hidden = true;
    C.message(status, '登录账号已变化，请重新打开货币管理。', 'error'); controls();
  }
  async function api(path: string, { method = 'GET', body, controller = new AbortController() }: { method?: string; body?: unknown; controller?: AbortController } = {}): Promise<Record<string, unknown>> {
    if (accessLost || !root.isConnected) throw new Error('页面或登录状态已变化，请重新打开页面');
    const timer = setTimeout(() => controller.abort(), 12000), activeEpoch = sessionEpoch; controllers.add(controller);
    try {
      const response = await fetch(path, { method, credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
        headers: { Accept: 'application/json', ...(body !== undefined ? { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf } : {}) },
        ...(body !== undefined ? { body: JSON.stringify(body) } : {}) });
      const data: unknown = await response.json().catch(() => null);
      if (activeEpoch !== sessionEpoch || accessLost || !root.isConnected) throw new Error('页面或登录状态已变化，请重新打开页面');
      if (controller.signal.aborted) throw new Error('连接超时，原操作已保留');
      if (!response.ok) {
        let problem: Record<string, unknown> = {}; try { problem = record(record(data).error); } catch { /* Preserve uncertain non-JSON failures. */ }
        const error = failure({ message: problem.message || '服务暂不可用，请稍后重试', code: problem.code, status: response.status });
        if ([401, 403].includes(response.status) && error.code?.startsWith('auth.')) loseAccess();
        throw error;
      }
      return record(data);
    } catch (error) {
      if (controller.signal.aborted && !accessLost) throw new Error('连接超时，输入和操作编号已保留，请重试原请求');
      if (error instanceof TypeError) throw new Error('网络连接失败，输入和操作编号已保留');
      throw error;
    } finally { clearTimeout(timer); controllers.delete(controller); }
  }
  $('[data-admin-discard]')?.addEventListener('click', () => { if (saving || readOnly || accessLost) return; settings.reset(); preview(); });
  const moduleDirty = (module: Module) => module.fields.some(field => raw(field) !== baseline[field.name]);
  function preview() {
    const calendar = control(settings, 'check_in_mode')?.value === 'calendar_day';
    for (const [name, hidden] of [['check_in_interval_hours', calendar], ['check_in_timezone', !calendar]] as const) { const field = control(settings, name); if (field) { field.closest('label')?.toggleAttribute('hidden', hidden); field.required = !hidden; } }
    saveFeedback = ''; saveError = false;
    dirty = modules.some(moduleDirty);
    $('[data-currency-preview-name]').textContent = control(settings, 'name').value || '货币名称';
    $('[data-currency-preview-symbol]').textContent = control(settings, 'symbol').value || '单位';
    $('[data-currency-preview-state]').textContent = dirty ? '未保存预览' : input(settings, 'enabled').checked ? '已启用' : '未启用';
    settings.querySelectorAll('[data-currency-unit]').forEach(element => { element.textContent = control(settings, 'symbol').value; });
    const saveState = root.querySelector<HTMLElement>('[data-currency-dirty]') || $('[data-save-state]');
    saveState.textContent = dirty ? '未保存' : '';
    window.ChunnelAdminUI?.paintSavebar($('[data-admin-savebar]'), {dirty, busy: saving, readOnly: readOnly || accessLost});
    for (const module of modules) {
      const changed = moduleDirty(module), state = required(module.element, '[data-currency-module-state]');
      state.textContent = changed ? '未保存' : ''; state.dataset.dirty = String(changed);
    }
  }
  function fieldValue(field: Control, digits = precision): string | number | boolean {
    if (field.dataset.currencyField === 'toggle') return field instanceof HTMLInputElement && field.checked;
    if (field.dataset.currencyField === 'integer') {
      if (!/^[0-9]+$/.test(field.value) || !Number.isSafeInteger(Number(field.value))) throw new Error('位数、间隔和次数必须为整数');
      return Number(field.value);
    }
    if (field.dataset.currencyField === 'amount') {
      const amount = C.units(field.value.trim(), digits);
      if (amount <= 0n) throw new Error('额度与奖励金额必须大于零');
      return C.format(amount, digits);
    }
    return field.value.trim();
  }
  function payload(module: Module | null | undefined) {
    const digits = module && module.key !== 'identity' ? precision : Number(fieldValue(input(settings, 'precision')));
    if (module && Number(input(settings, 'precision').value) !== precision) {
      if (module.key !== 'identity') throw new Error('小数位数尚未保存，请先保存货币定义或保存全部规则');
      if (modules.some(item => item !== module && moduleDirty(item))) throw new Error('小数位数和其他模块同时有修改，请使用“保存全部规则”一起保存');
    }
    return Object.fromEntries((module ? module.fields : fields).map(field => [field.name, fieldValue(field, digits)]));
  }
  function installSettings(value: AdminCurrencySettings, savedModule: Module | null = null, submitted?: Record<string, string | boolean>) {
    const previous = baseline, source = record(value), saved = new Set((savedModule ? savedModule.fields : fields).map(field => field.name));
    for (const field of fields) {
      const flag = field.dataset.currencyField === 'toggle', next = source[field.name];
      if (flag ? typeof next !== 'boolean' : !['string', 'number'].includes(typeof next)) throw new Error('规则返回字段不完整，请重新载入');
    }
    for (const field of fields) {
      const preserve = submitted ? raw(field) !== submitted[field.name] || !saved.has(field.name) && raw(field) !== previous[field.name] : false;
      const next = source[field.name];
      if (field instanceof HTMLInputElement && field.dataset.currencyField === 'toggle') {
        const flag = next === true; baseline[field.name] = flag; field.defaultChecked = flag; if (!preserve) field.checked = flag;
      } else {
        const text = String(next); baseline[field.name] = text;
        if (field instanceof HTMLSelectElement) [...field.options].forEach(option => { option.defaultSelected = option.value === text; }); else field.defaultValue = text;
        if (!preserve) field.value = text;
      }
    }
    version = value.version; precision = value.precision;
    input(settings, 'version').value = version; input(settings, 'version').defaultValue = version;
    input(settings, 'precision').readOnly = value.precision_locked;
    $('[data-currency-precision-help]').textContent = value.precision_locked ? '已有流水，位数已锁定。' : '首次记账后锁定。修改时会同步显示其他模块的金额格式。';
    if (!pending) control(adjust, 'version').value = version;
    root.querySelectorAll('[data-currency-unit]').forEach(element => { element.textContent = value.symbol; });
    const option = [...select.options].find(option => option.value === currency);
    if (option) { option.textContent = value.name + ' · ' + currency + (value.enabled ? '' : '（已关闭）'); select.selectedIndex = select.selectedIndex; }
    preview();
  }
  function controls() {
    const blocked = readOnly || accessLost, busy = saving || adjusting || creating;
    required<HTMLFieldSetElement>(settings, 'fieldset').disabled = blocked || busy;
    window.ChunnelAdminUI?.paintSavebar($('[data-admin-savebar]'), {dirty, busy: saving, disabled: busy, readOnly: blocked, message: saveFeedback, error: saveError});
    select.disabled = busy || accessLost; required<HTMLButtonElement>(switchForm, '[type=submit]').disabled = busy || accessLost;
    required<HTMLFieldSetElement>(create, 'fieldset').disabled = blocked || busy;
    for (const field of ['key', 'name', 'symbol', 'precision']) setReadOnly(create, field, !!creation);
    required<HTMLButtonElement>(create, '[type=submit]').textContent = creation ? '重试原创建，确认结果' : '创建货币';
    for (const field of ['user_id', 'delta', 'reason']) setReadOnly(adjust, field, !!pending || busy);
    required<HTMLButtonElement>(adjust, '[type=submit]').disabled = blocked || busy;
    required<HTMLButtonElement>(adjust, '[type=submit]').textContent = pending ? '重试原调整，确认结果' : '核对并调整余额';
    control(lookup, 'username').disabled = blocked || !!pending || busy;
    required<HTMLButtonElement>(lookup, '[type=submit]').disabled = blocked || !!pending || busy;
    if (pending) for (const field of ['user_id', 'delta', 'reason', 'request_id', 'version'] as const) control(adjust, field).value = pending[field];
  }
  settings.addEventListener('input', preview);
  settings.addEventListener('change', preview);
  settings.addEventListener('submit', async event => {
    event.preventDefault(); if (saving || adjusting || creating || readOnly || accessLost) return;
    const submitter = event.submitter;
    const module = submitter instanceof HTMLButtonElement && submitter.name === 'module' ? modules.find(item => item.key === submitter.value) : null;
    if (submitter instanceof HTMLButtonElement && submitter.name === 'module' && !module) return;
    let body: unknown;
    try { const values = payload(module); body = module ? { version, values } : { settings: { version, ...values } }; }
    catch (error) { C.message(module ? required(module.element, '[data-currency-module-message]') : status, failure(error).message, 'error'); return; }
    const notice = module ? required(module.element, '[data-currency-module-message]') : status;
    const submitted = Object.fromEntries(fields.map(field => [field.name, raw(field)])), previousVersion = version;
    saving = true; controls(); C.message(notice, '正在保存…');
    try {
      const data = currencySettingsResult(await api(base + (module ? '/modules/' + module.key : ''), { method: module ? 'PATCH' : 'PUT', body }), currency, previousVersion, module?.key);
      installSettings(data, module || null, submitted);
      if (module) { required(module.element, '[data-currency-module-state]').textContent = moduleDirty(module) ? '未保存' : '已保存'; C.message(notice, ''); }
      else { saveFeedback = '已保存'; C.message(notice, ''); }
    } catch (error) { saveFeedback = failure(error).message + ' 当前输入已保留。'; saveError = true; if (module || !window.ChunnelAdminUI) C.message(notice, saveFeedback, 'error'); }
    finally { saving = false; controls(); }
  });
  $('[data-currency-reload]').addEventListener('click', async () => {
    if (saving || adjusting || creating || accessLost || (dirty && !window.confirm('重新载入会覆盖尚未保存的规则，是否继续？'))) return;
    const submitted = Object.fromEntries(fields.map(field => [field.name, raw(field)]));
    saving = true; controls();
    try {
      installSettings(currencySettingsResult(await api(base), currency), null, submitted);
      for (const module of modules) C.message(required(module.element, '[data-currency-module-message]'), '已载入最新规则。');
      C.message(status, '已载入最新规则。');
    } catch (error) { C.message(status, failure(error).message, 'error'); }
    finally { saving = false; controls(); }
  });
  function navigate(key: string, confirmed = false) {
    if (!C.currencyKey(key) || saving || adjusting || creating) return;
    if (!confirmed && dirty && !window.confirm('切换货币会放弃当前未保存的模块修改，是否继续？')) { select.value = currency; return; }
    leaving = true; window.location.assign('/admin/currency/?currency=' + encodeURIComponent(key));
  }
  select.addEventListener('change', () => { if (select.value !== currency) navigate(select.value); });
  switchForm.addEventListener('submit', event => { event.preventDefault(); if (select.value !== currency) navigate(select.value); });
  window.addEventListener('beforeunload', event => { if (dirty && !leaving) { event.preventDefault(); event.returnValue = ''; } });

  create.addEventListener('submit', async event => {
    event.preventDefault(); if (creating || saving || adjusting || readOnly || accessLost) return;
    if (dirty && !window.confirm('创建完成后会打开新货币。当前模块有未保存的修改，是否继续？')) return;
    if (!creation) {
      try {
        const digits = control(create, 'precision').value;
        if (!/^[0-6]$/.test(digits)) throw new Error('小数位数需要在 0 到 6 之间');
        creation = C.savePending('create', owner, { key: control(create, 'key').value.trim(), name: control(create, 'name').value.trim(), symbol: control(create, 'symbol').value.trim(), precision: Number(digits), request_id: control(create, 'request_id').value || C.nonce() });
      } catch (error) { C.message(createStatus, failure(error).message, 'error'); return; }
    }
    creating = true; controls(); C.message(createStatus, '正在创建货币…');
    try {
      const data = await api('/api/v1/admin/currencies', { method: 'POST', body: { key: creation.key, name: creation.name, symbol: creation.symbol, precision: creation.precision, request_id: creation.request_id } });
      currencyCreation(data, creation);
      const key = creation.key; C.clearPending('create', owner); creation = null; creating = false;
      navigate(key, true);
    } catch (error) {
      if (C.definiteRejection(error)) { C.clearPending('create', owner); creation = null; control(create, 'request_id').value = C.nonce(); }
      C.message(createStatus, failure(error).message + (creation ? ' 已保留原创建信息，请重试确认。' : ' 请修改后重试。'), 'error');
    } finally { creating = false; controls(); }
  });

  function paintAccount(value: AdminWallet) {
    selected = value;
    $('[data-currency-account]').hidden = false;
    $('[data-currency-account-username]').textContent = '@' + value.username;
    $('[data-currency-account-name]').textContent = value.display_name;
    $('[data-currency-account-state]').textContent = value.status === 'active' ? '正常账号' : '已停用账号';
    $('[data-currency-account-balance]').textContent = value.balance;
    $('[data-currency-account] [data-currency-unit]').textContent = value.settings.symbol;
    if (!pending) control(adjust, 'user_id').value = value.user_id;
    C.history($('[data-currency-admin-history]'), value.entries);
    const more = $<HTMLFormElement>('[data-currency-more-form]'); more.hidden = !value.next_before;
    control(more, 'username').value = value.username; control(more, 'before').value = value.next_before;
    if (value.settings.precision_locked) input(settings, 'precision').readOnly = true;
  }
  async function loadAccount(username: string, before = '', expected = '') {
    const request = ++lookupSerial; lookupController?.abort(); const controller = new AbortController(); lookupController = controller;
    C.message($('[data-currency-account-status]'), '正在查询账号…');
    try {
      const query = new URLSearchParams({ username }); if (before) query.set('before', before);
      const value = adminWallet(await api(base + '/wallet?' + query, { controller }), currency, username, expected);
      if (request !== lookupSerial || accessLost) return;
      paintAccount(value); C.message($('[data-currency-account-status]'), '');
    } catch (error) {
      if (request !== lookupSerial || accessLost) return;
      selected = null; $('[data-currency-account]').hidden = true; $('[data-currency-admin-history]').replaceChildren(); $<HTMLFormElement>('[data-currency-more-form]').hidden = true;
      if (!pending) control(adjust, 'user_id').value = '';
      C.message($('[data-currency-account-status]'), failure(error).message, 'error');
    }
  }
  lookup.addEventListener('submit', event => { event.preventDefault(); if (!readOnly && !pending && !accessLost && !saving && !adjusting && !creating) loadAccount(control(lookup, 'username').value.trim()); });
  $<HTMLFormElement>('[data-currency-more-form]').addEventListener('submit', event => { event.preventDefault(); if (accessLost || saving || adjusting || creating || pending) return; const form = $<HTMLFormElement>('[data-currency-more-form]'); loadAccount(control(form, 'username').value, control(form, 'before').value, selected?.user_id); });

  adjust.addEventListener('submit', async event => {
    event.preventDefault(); if (adjusting || saving || creating || readOnly || accessLost) return;
    if (!pending) {
      try {
        const amount = C.units(control(adjust, 'delta').value.trim(), precision, true), user = control(adjust, 'user_id').value.trim(), reason = control(adjust, 'reason').value.trim();
        if (!C.id(user) || amount === 0n || !reason) throw new Error('请填写用户编号、非零金额和调整原因');
        const delta = C.format(amount, precision), target = selected?.user_id === user ? '@' + selected.username : '用户 #' + user;
        if (!window.confirm('确认向 ' + target + (amount > 0n ? ' 发放 ' : ' 扣减 ') + C.format(amount > 0n ? amount : -amount, precision) + ' ' + baseline.symbol + '（' + currency + '）？\n原因：' + reason)) return;
        pending = C.savePending('adjust', owner, { user_id: user, delta, reason, version, precision, request_id: control(adjust, 'request_id').value || C.nonce() }, currency);
      } catch (error) { C.message(adjustStatus, failure(error).message, 'error'); return; }
    }
    adjusting = true; controls(); C.message(adjustStatus, '正在确认余额调整…');
    try {
      const data = currencyAdjustment(await api(base + '/adjust', { method: 'POST', body: { user_id: pending.user_id, delta: pending.delta, reason: pending.reason, version: pending.version, request_id: pending.request_id } }), pending);
      const target = pending.user_id;
      C.clearPending('adjust', owner, currency); pending = null;
      control(adjust, 'request_id').value = C.nonce(); control(adjust, 'version').value = version; control(adjust, 'delta').value = ''; control(adjust, 'reason').value = '';
      C.message(adjustStatus, '已确认用户 #' + target + ' 的余额调整，流水 #' + data.id + '，该笔之后余额 ' + data.balance + ' ' + baseline.symbol + '。');
      if (selected?.user_id === target) await loadAccount(selected.username, '', target);
      input(settings, 'precision').readOnly = true; $('[data-currency-precision-help]').textContent = '已有流水，位数已锁定。';
    } catch (error) {
      if (C.definiteRejection(error)) { C.clearPending('adjust', owner, currency); pending = null; control(adjust, 'request_id').value = C.nonce(); }
      C.message(adjustStatus, failure(error).message + (pending ? ' 原调整已保留，请重试确认，避免另建一笔。' : ' 当前输入已保留。'), 'error');
    } finally { adjusting = false; controls(); }
  });
  if (pending) {
    $('[data-currency-account]').hidden = true; $('[data-currency-admin-history]').replaceChildren(); $<HTMLFormElement>('[data-currency-more-form]').hidden = true;
    C.message(adjustStatus, '这项货币的上次调整结果尚未确认。请重试原调整。');
  }
  if (creation) {
    $<HTMLDetailsElement>('[data-currency-create-panel]').open = true;
    for (const field of ['key', 'name', 'symbol', 'precision', 'request_id'] as const) control(create, field).value = String(creation[field]);
    C.message(createStatus, '上次创建结果尚未确认，原信息已恢复。请重试原创建。');
  }
  document.addEventListener('chunnel:session', event => { if (event.detail?.authenticated !== true || String(event.detail.id || '') !== owner || event.detail.csrf_token !== csrf) loseAccess(); });
  window.addEventListener('pagehide', loseAccess); window.addEventListener('pageshow', event => { if (event.persisted) loseAccess(); });
  controls(); preview();
  root.querySelectorAll<HTMLTimeElement>('time[datetime]').forEach(element => { const label = window.ChunnelTime?.formatDateTime(element.dateTime); if (label) element.textContent = label; });
  if (!readOnly && !pending && control(lookup, 'username').value) loadAccount(control(lookup, 'username').value);
})();
