import type {} from '../shared/progression-types';
import type { ProgressionEntry } from '../shared/progression-types';
import { record, failure } from '../shared/ledger-data';
import { progressionSettingsResult, adminProgressionAccount, progressionAdjustment, progressionOrderResult, progressionPreview } from './ledger-admin-data';
import type { AdminOperation, AdminCurrency, AdminProgressionSettings, AdminProgressionSnapshot } from './ledger-admin-data';
import { required, control, values } from './ledger-admin-dom';
import type { Control } from './ledger-admin-dom';
type FieldKind = 'bool' | 'text' | 'optional' | 'integer' | 'zero' | 'number' | 'percent' | 'color' | 'key' | 'currency' | 'price' | 'benefits' | 'schedule';
type FieldSpec = [string, string, FieldKind, number?];
interface Definition { key: string; body: Exclude<keyof AdminProgressionSettings, 'version'>; title: string; description: string; fields: FieldSpec[]; list?: 'items' | 'plans' }
(() => {
  'use strict';
  const host = document.querySelector<HTMLElement>('[data-progression-admin]'), shared = window.ChunnelProgressionShared; if (!host || !shared) return;
  const root = host, P = shared;
  const owner = root.dataset.userId || '', readOnly = root.dataset.readOnly === 'true', $ = <T extends HTMLElement = HTMLElement>(selector: string): T => required<T>(root, selector), base = '/api/v1/admin/progression';
  if (!P.id(owner) && !readOnly) return;
  const status = $('[data-progression-admin-status]'), operationStatus = $('[data-progression-operation-status]'), dialog = $<HTMLDialogElement>('[data-progression-admin-dialog]');
  let baseline: AdminProgressionSettings | null = null, currencies: AdminCurrency[] = [], selected: AdminProgressionSnapshot | null = null, entries: ProgressionEntry[] = [], busy = false, serial = 0, draft: AdminOperation | null = null, previewSerial = 0, previewInput: unknown = null;
  function settings(): AdminProgressionSettings { if (!baseline) throw new Error('请先载入规则'); return baseline; }
  function account(): AdminProgressionSnapshot { if (!selected) throw new Error('请先重新查询账号'); return selected; }
  function clearAccount() { selected = null; entries = []; $('[data-progression-account]').hidden = true; $('[data-progression-account-summary]').replaceChildren(); $('[data-progression-admin-history]').replaceChildren(); }
  let pending: AdminOperation | null = P.read('experience', owner) || P.read('membership', owner);
  let hasChanges = false, saveFeedback = '', saveError = false;
  const connection = P.transport(owner, root.dataset.csrf || '', () => { serial++; previewSerial++; draft = null; clearAccount(); dialog.close(); controls(); P.message(status, '登录状态已变化，请重新打开成长管理。', 'error'); });
  const definitions: Definition[] = [
    { key: 'experience', body: 'experience', title: '经验规则', description: '经验只用于成长。控制自动奖励、经验上限与领取资格。', fields: [['enabled', '启用自动经验奖励', 'bool'], ['require_verified_email', '领取奖励需要已验证邮箱', 'bool'], ['name', '经验名称', 'text', 24], ['maximum', '账号经验上限', 'integer'], ['daily_limit', '每人近 24 小时奖励上限', 'integer']] },
    { key: 'levels', body: 'levels', title: '等级门槛', description: '按经验门槛由低到高排列，首级门槛须为 0，最多 50 级。', fields: [['enabled', '启用等级', 'bool'], ['public_badges', '在公开主页展示等级徽章', 'bool']], list: 'items' },
    { key: 'check-in', body: 'check_in', title: '签到经验', description: '选择指定间隔或自然天；会员加成和滚动 24 小时奖励上限仍然适用。', fields: [['enabled', '启用经验签到', 'bool'], ['amount', '每次基础经验', 'integer'], ['mode', '签到周期', 'schedule'], ['timezone', '自然天时区（北京时间：Asia/Shanghai）', 'text', 100], ['interval_hours', '领取间隔（小时）', 'number', 720]] },
    { key: 'registration', body: 'registration', title: '注册经验', description: '首次启用后创建的账号，每人领取一次；邮箱验证后可补领。', fields: [['enabled', '启用注册经验', 'bool'], ['amount', '注册基础经验', 'integer']] },
    { key: 'publication', body: 'publication', title: '发文经验', description: '仅奖励首次启用后创建且已公开的文章。重复发布不重复奖励。', fields: [['enabled', '启用发文经验', 'bool'], ['amount', '每篇基础经验', 'integer'], ['min_length', '最少正文字符', 'number', 100000], ['daily_limit', '每人近 24 小时最多奖励篇数', 'number', 1000]] },
    { key: 'membership', body: 'membership', title: '会员方案', description: '最多 16 个方案。停用方案停止新开通；已开通会员的权益保留至到期。', fields: [['enabled', '启用会员权益与开通', 'bool'], ['public_badges', '在公开主页展示会员徽章', 'bool']], list: 'plans' },
  ];
  const levelFields: FieldSpec[] = [['name', '等级名称', 'text', 40], ['key', '等级标识', 'key'], ['threshold', '经验门槛', 'zero'], ['color', '徽章颜色', 'color']];
  const planFields: FieldSpec[] = [['name', '方案名称', 'text', 40], ['key', '方案标识', 'key'], ['description', '方案简介', 'optional', 160], ['benefits', '权益清单', 'benefits'], ['color', '徽章颜色', 'color'], ['tier', '会员级别（1 普通 / 2 高级，可自定）', 'number', 100], ['days', '有效天数', 'number', 3650], ['experience_percent', '经验倍率（百分比）', 'percent', 1000], ['currency', '购买使用的货币', 'currency'], ['price', '购买或续费价格', 'price'], ['enabled', '允许新开通 / 续期', 'bool'], ['purchase_enabled', '允许站内币购买', 'bool']];
  const forms = new Map<string, HTMLFormElement>();
  function moduleForm(def: Definition): HTMLFormElement { const form = forms.get(def.key); if (!form) throw new Error('模块尚未载入'); return form; }
  function field(spec: FieldSpec, value: unknown, row: false | 'items' | 'plans' = false) {
    const [name, title, kind, limit] = spec, label = P.node('label', kind === 'bool' ? 'progression-toggle' : '');
    let input: Control;
    if (kind === 'schedule') {
      input = P.node('select'); ([['interval', '距离上次签到指定时长'], ['calendar_day', '按自然天（零点重置）']] as const).forEach(([key, title]) => { const option = P.node('option', '', title); option.value = key; input.append(option); }); value ||= 'interval';
    } else if (kind === 'currency') {
      input = P.node('select'); const items = currencies.some(c => c.key === value) ? currencies : [...currencies, { key: value, name: '未找到货币', enabled: false }];
      items.forEach(c => { const option = P.node('option', '', `${c.name} · ${c.key}${c.enabled ? '' : '（已关闭）'}`); option.value = String(c.key); input.append(option); });
    } else if (kind === 'benefits') { input = P.node('textarea'); input.rows = 4; input.maxLength = 1295; label.className = 'progression-field-wide'; }
    else { input = P.node('input'); input.type = kind === 'bool' ? 'checkbox' : kind === 'color' ? 'color' : ['number', 'percent'].includes(kind || '') ? 'number' : 'text'; }
    input.name = name; input.dataset[row ? 'rowField' : 'moduleField'] = kind;
    if (kind === 'bool' && input instanceof HTMLInputElement) { input.checked = !!value; input.setAttribute('role', 'switch'); } else { input.value = kind === 'benefits' ? (Array.isArray(value) ? value : []).join('\n') : String(value ?? (name === 'timezone' ? 'Asia/Shanghai' : name === 'tier' ? '1' : '')); input.required = !['optional', 'benefits'].includes(kind); }
    if (input instanceof HTMLInputElement && ['number', 'percent'].includes(kind || '')) { input.min = kind === 'percent' ? '100' : '1'; input.max = String(limit); input.step = '1'; }
    if (input instanceof HTMLInputElement && ['integer', 'zero'].includes(kind || '')) { input.inputMode = 'numeric'; input.pattern = kind === 'integer' ? '[1-9][0-9]*' : '(0|[1-9][0-9]*)'; input.maxLength = 19; }
    if (kind === 'price' && input instanceof HTMLInputElement) { input.inputMode = 'decimal'; input.pattern = '(0|[1-9][0-9]*)([.][0-9]+)?'; input.maxLength = 27; }
    if (kind === 'key' && input instanceof HTMLInputElement) { input.pattern = '[a-z]([a-z0-9\\-]{0,30}[a-z0-9])?'; input.maxLength = 32; input.autocapitalize = 'none'; input.spellcheck = false; }
    if (input instanceof HTMLInputElement && ['text', 'optional'].includes(kind || '')) input.maxLength = limit || 160;
    if (kind === 'bool' && input instanceof HTMLInputElement && window.ChunnelAdminUI) return window.ChunnelAdminUI.switchControl(input, title);
    label.append(P.node('span', '', title), input);
    if (name === 'experience_percent') label.append(P.node('small', '', '100 为原倍率，125 为 1.25 倍；只加成自动经验奖励。'));
    if (kind === 'benefits') label.append(P.node('small', '', '每行一项，最多 8 项，每项 80 个字符。用于方案展示；具体服务按已配置功能提供。'));
    if (row === 'plans' && name === 'key') label.append(P.node('small', '', '已使用的方案标识须保留。'));
    return label;
  }
  function value(input: Control): string | number | boolean | string[] {
    const kind = input.dataset.moduleField || input.dataset.rowField;
    if (kind === 'bool') return input instanceof HTMLInputElement && input.checked;
    if (kind === 'benefits') return P.benefitLines(input.value);
    if (['number', 'percent'].includes(kind || '')) { const n = Number(input.value); if (!Number.isInteger(n)) throw new Error('请输入完整整数'); return n; }
    if (['integer', 'zero'].includes(kind || '')) P.integer(input.value);
    return input.value;
  }
  function row(def: Definition, item: Record<string, unknown>) {
    const membership = def.list === 'plans', box = P.node(membership ? 'details' : 'div', membership ? 'progression-plan-editor' : 'progression-level-row'); box.dataset.record = '';
    let content = box;
    if (membership) {
      const summary = P.node('summary'), mark = P.node('span', 'progression-plan-marker'); mark.style.setProperty('--progression-accent', P.color(String(item.color || ''))); const name = P.node('span', '', String(item.name || '新会员方案')); name.dataset.planTitle = ''; summary.append(mark, name); box.append(summary); content = P.node('div'); box.append(content);
    }
    const specs = membership ? planFields : levelFields, grid = membership ? P.node('div', 'progression-fields') : content;
    specs.filter(s => s[2] !== 'bool').forEach(spec => grid.append(field(spec, item[spec[0]], def.list))); if (membership) { content.append(grid); specs.filter(s => s[2] === 'bool').forEach(spec => content.append(field(spec, item[spec[0]], def.list))); }
    const remove = P.node('button', 'progression-remove', '移除'); remove.type = 'button'; remove.addEventListener('click', () => { if (busy || readOnly || connection.lost) return; box.remove(); dirty(); }); content.append(remove); return box;
  }
  function renderModule(def: Definition) {
    const cfg = record(settings()[def.body]), old = forms.get(def.key), form = P.node('form', 'progression-module'); form.id = 'progression-' + def.key; form.dataset.module = def.key; form.dataset.settingsSection = '';
    const header = P.node('header'), heading = P.node('div'), state = P.node('span', 'progression-module-state', ''); state.dataset.moduleState = ''; heading.append(P.node('h2', '', def.title), P.node('p', '', def.description)); header.append(heading, state); form.append(header);
    const fieldset = P.node('fieldset'), grid = P.node('div', 'progression-fields'); def.fields.filter(s => s[2] === 'bool').forEach(spec => fieldset.append(field(spec, cfg[spec[0]]))); def.fields.filter(s => s[2] !== 'bool').forEach(spec => grid.append(field(spec, cfg[spec[0]]))); if (grid.children.length) fieldset.append(grid);
    if (def.list) {
      const list = P.node('div'); list.dataset.records = ''; const items = cfg[def.list]; if (!Array.isArray(items)) throw new Error('模块列表未确认'); items.forEach(item => list.append(row(def, record(item)))); const add = P.node('button', 'admin-button admin-button-secondary progression-add', def.list === 'plans' ? '新增会员方案' : '新增等级'); add.type = 'button';
      add.addEventListener('click', () => { if (busy || readOnly || connection.lost) return; const count = list.children.length, max = def.list === 'plans' ? 16 : 50; if (count >= max) { P.message(status, `最多支持 ${max} 项。`, 'error'); return; }
        const unique = P.nonce().replaceAll('-', '').slice(0, 8), item = def.list === 'plans' ? { key: 'plan-' + unique, name: '新会员方案', description: '', color: '#3476a2', tier: 1, days: 30, experience_percent: 100, currency: 'main', price: '100', enabled: true, purchase_enabled: false } : { key: 'level-' + unique, name: '新等级', threshold: String(P.integer(list.lastElementChild?.querySelector<HTMLInputElement>('[name=threshold]')?.value || '0') + 100n), color: '#3476a2' };
        const next = row(def, item); if (next instanceof HTMLDetailsElement) next.open = true; list.append(next); dirty(); }); fieldset.append(list, add);
    }
    if (typeof cfg.since === 'string' && cfg.since && !cfg.since.startsWith('0001-')) fieldset.append(P.node('p', 'progression-help', '首次开启：' + P.date(cfg.since)));
    const footer = P.node('footer'), save = P.node('button', 'admin-button admin-button-secondary', '保存此模块'); save.type = 'submit'; footer.append(P.node('p'), save); fieldset.append(footer); form.append(fieldset);
    form.addEventListener('input', () => { form.querySelectorAll('[data-record]').forEach(record => { const title = record.querySelector('[data-plan-title]'); if (title) title.textContent = required<HTMLInputElement>(record, '[name=name]').value || '未命名方案'; }); dirty(); }); form.addEventListener('change', dirty); form.addEventListener('submit', event => { event.preventDefault(); saveSettings(def); });
    if (old) old.replaceWith(form); else $('[data-progression-modules]').append(form); forms.set(def.key, form); scheduleFields(form);
  }
  function collect(def: Definition): Record<string, unknown> {
    const form = moduleForm(def), result = { ...record(settings()[def.body]) };
    form.querySelectorAll<Control>('[data-module-field]').forEach(input => { result[input.name] = value(input); });
    if (def.list) result[def.list] = [...form.querySelectorAll('[data-record]')].map(row => {
      const item: Record<string, unknown> = {}; row.querySelectorAll<Control>('[data-row-field]').forEach(input => { item[input.name] = value(input); }); return item;
    });
    return result;
  }
  $('[data-admin-discard]')?.addEventListener('click', () => { if (!baseline || busy || readOnly || connection.lost) return; definitions.forEach(renderModule); dirty(); controls(); });
  function scheduleFields(form: HTMLFormElement) {
    const calendar = form.querySelector<HTMLSelectElement>('[name=mode]')?.value === 'calendar_day';
    for (const [name, hidden] of [['interval_hours', calendar], ['timezone', !calendar]] as const) { const input = form.querySelector<Control>(`[name=${name}]`); if (input) { input.closest('label')?.toggleAttribute('hidden', hidden); input.required = !hidden; } }
  }
  function dirty() {
    forms.forEach(scheduleFields);
    if (!baseline) return false; let any = false;
    saveFeedback = ''; saveError = false;
    for (const def of definitions) { let changed = true; try { changed = !P.equal(collect(def), baseline[def.body]); } catch { /* Invalid edits remain dirty. */ } const state = required(moduleForm(def), '[data-module-state]'); state.textContent = changed ? '未保存' : ''; state.dataset.dirty = String(changed); any ||= changed; }
    (root.querySelector<HTMLElement>('[data-progression-dirty]') || $('[data-save-state]')).textContent = any ? '未保存' : '';
    window.ChunnelAdminUI?.paintSavebar($('[data-admin-savebar]'), {dirty: any, busy, readOnly: readOnly || connection.lost});
    hasChanges = any;
    if (previewInput) clearPreview('规则已变化，请重新检查。');
    return any;
  }
  function controls() {
    const locked = busy || readOnly || connection.lost || !baseline;
    forms.forEach(form => { required<HTMLFieldSetElement>(form, 'fieldset').disabled = locked; }); (root.querySelector<HTMLButtonElement>('[data-progression-save-all]') || $<HTMLButtonElement>('[data-admin-save]')).disabled = locked || !hasChanges; $<HTMLButtonElement>('[data-progression-reload]').disabled = busy || connection.lost;
    window.ChunnelAdminUI?.paintSavebar($('[data-admin-savebar]'), {dirty: hasChanges, busy, disabled: !baseline, readOnly: readOnly || connection.lost, message: saveFeedback, error: saveError});
    if (baseline) window.ChunnelAdminUI?.settingsNavigation(document);
    root.querySelectorAll<HTMLButtonElement>('[data-progression-check]').forEach(button => { button.disabled = locked; });
    $<HTMLFormElement>('[data-progression-lookup]').querySelectorAll<HTMLInputElement | HTMLButtonElement>('input,button').forEach(el => { el.disabled = locked || !!pending; });
    $<HTMLFieldSetElement>('[data-progression-adjust] fieldset').disabled = locked || !selected || !!pending;
    $<HTMLFieldSetElement>('[data-progression-member] fieldset').disabled = locked || !selected || !!pending || !!selected.pending_order;
    $<HTMLButtonElement>('[data-progression-admin-retry]').disabled = busy || readOnly || connection.lost || !pending;
    $<HTMLButtonElement>('[data-progression-admin-more]').disabled = locked || !selected || !!pending;
    $<HTMLButtonElement>('[data-progression-admin-confirm-submit]').disabled = locked || !draft;
    $('[data-progression-admin-pending]').hidden = !pending || connection.lost;
    if (pending) $('[data-progression-admin-pending-description]').textContent = operationDescription(pending);
    for (const suffix of ['recover', 'cancel', 'reason']) $<HTMLInputElement | HTMLButtonElement>('[data-progression-order-' + suffix + ']').disabled = locked || !selected?.pending_order || !!pending;
  }
  function previewNote(text: string, tone = '') { const state = $('[data-progression-preview-state]'); state.textContent = text; state.dataset.tone = tone; }
  function clearPreview(note = '尚未检查当前数值。') {
    previewSerial++; previewInput = null;
    const result = $('[data-progression-preview-result]'); result.replaceChildren(); result.hidden = true;
    P.message($('[data-progression-preview-status]'), ''); previewNote(note);
  }
  function previewTable(headers: string[], rows: string[][]) {
    const wrap = P.node('div', 'progression-table-wrap'), table = P.node('table', 'progression-table'), head = P.node('thead'), title = P.node('tr');
    headers.forEach(header => title.append(P.node('th', '', header))); head.append(title); const body = P.node('tbody');
    rows.forEach(cells => { const row = P.node('tr'); cells.forEach((cell, index) => row.append(P.node('td', index === cells.length - 1 ? 'progression-preview-status-cell' : '', cell))); body.append(row); });
    table.append(head, body); wrap.append(table); return wrap;
  }
  function paintPreview(data: ReturnType<typeof progressionPreview>) {
    const errors = data.issues.filter(issue => issue.severity === 'error'), warnings = data.issues.filter(issue => issue.severity === 'warning');
    P.message($('[data-progression-preview-status]'), data.valid ? `检查通过${warnings.length ? `，另有 ${warnings.length} 项提醒` : ''}。检查不会保存配置。` : `发现 ${errors.length} 项必须修正的问题；保存前请逐项核对。`, data.valid ? '' : 'error');
    previewNote('已检查 ' + (window.ChunnelTime?.formatClock(new Date()) || ''), data.valid ? '' : 'error');
    const nodes = [], metrics = P.node('div', 'progression-preview-metrics');
    data.metrics.forEach(metric => { const card = P.node('article', 'progression-preview-metric'); card.append(P.node('span', '', metric.label), P.node('strong', '', metric.value)); metrics.append(card); });
    nodes.push(metrics);
    if (data.issues.length) {
      const section = P.node('section', 'progression-preview-issues'), list = P.node('ul');
      data.issues.forEach(issue => { const item = P.node('li', 'progression-preview-issue'); item.dataset.tone = issue.severity; item.append(P.node('strong', '', issue.severity === 'error' ? '必须修正' : '提醒'), P.node('p', '', issue.message), P.node('small', '', issue.field + ' · ' + issue.code)); list.append(item); });
      section.append(P.node('h3', '', '规则核对'), list); nodes.push(section);
    } else nodes.push(P.node('p', 'progression-preview-success', '数值检查通过，没有发现冲突或需要留意的数值。'));
    if (data.plans.length) {
      const planSection = P.node('details', 'progression-preview-table'), summary = P.node('summary', '', `会员价格与倍率（${data.plans.length} 项）`);
      const rows = data.plans.map(plan => [ `${plan.name} · ${plan.key}`, `${plan.price} ${plan.symbol}（${plan.currency_name} · ${plan.currency}）`, `${plan.days} 天`, `${(plan.experience_percent / 100).toFixed(2)}×`, ({ ok: '正常', warning: '提醒', error: '需修正' })[plan.status] ]);
      planSection.append(summary, previewTable(['方案', '价格与币种', '有效期', '经验倍率', '状态'], rows)); nodes.push(planSection);
    }
    if (data.levels.length) {
      const levelSection = P.node('details', 'progression-preview-table'), summary = P.node('summary', '', `等级门槛（${data.levels.length} 级）`);
      const rows = data.levels.map(level => [ `${level.name} · ${level.key}`, level.threshold, `+${level.gap}`, ({ ok: '正常', warning: '提醒', error: '需修正' })[level.status] ]);
      levelSection.append(summary, previewTable(['等级', '累计门槛', '本级跨度', '状态'], rows)); nodes.push(levelSection);
    }
    const result = $('[data-progression-preview-result]'); result.replaceChildren(...nodes); result.hidden = false;
  }
  async function checkSettings() {
    if (busy || readOnly || connection.lost || !baseline) return;
    for (const form of forms.values()) if (!form.reportValidity()) { P.message(status, '请先修正输入格式，再进行数值检查。', 'error'); return; }
    let settings;
    try { settings = Object.fromEntries([['version', baseline.version], ...definitions.map(def => [def.body, collect(def)])]); }
    catch (error) { P.message(status, failure(error).message, 'error'); return; }
    clearPreview('正在检查…'); previewInput = settings; busy = true; controls(); const request = previewSerial;
    try {
      const data = progressionPreview(await connection.request(base + '/preview', { method: 'POST', body: { settings } }));
      if (request !== previewSerial) return;
      paintPreview(data);
    } catch (error) {
      if (request === previewSerial) { P.message($('[data-progression-preview-status]'), failure(error).message, 'error'); previewNote('检查未完成', 'error'); }
    } finally { busy = false; controls(); }
  }
  function acceptSettings(data: unknown, previous?: string) {
    const result = progressionSettingsResult(data, previous); baseline = result.settings; currencies = result.currencies;
    $('[data-progression-version]').textContent = '规则版本 ' + baseline.version;
  }
  const snapshots = () => new Map(definitions.map(def => [def.key, values(moduleForm(def))]));
  async function reload() {
    if (busy || connection.lost) return;
    if (baseline && dirty() && !window.confirm('重新载入会放弃当前未保存的规则，继续吗？')) return;
    const submitted = baseline ? snapshots() : null;
    busy = true; controls();
    try {
      if (!readOnly) await connection.check(); const data = await connection.request(base);
      const changed = new Set(submitted ? definitions.filter(def => !P.equal(values(moduleForm(def)), submitted.get(def.key))).map(def => def.key) : []);
      acceptSettings(data); definitions.filter(def => !changed.has(def.key)).forEach(renderModule); dirty(); clearPreview(); P.message(status, ''); if (selected) paintAccount();
    } catch (error) { P.message(status, failure(error).message, 'error'); } finally { busy = false; controls(); }
  }
  async function saveSettings(only?: Definition) {
    if (busy || readOnly || connection.lost || !baseline) return;
    const list = only ? [only] : definitions, previous = baseline.version;
    for (const def of list) if (!moduleForm(def).reportValidity()) return;
    let payload: unknown; try { payload = only ? { version: previous, settings: collect(only) } : { settings: Object.fromEntries([['version', previous], ...definitions.map(def => [def.body, collect(def)])]) }; }
    catch (error) { P.message(status, failure(error).message, 'error'); return; }
    const submitted = snapshots(), unsaved = new Set<string>();
    for (const def of definitions) if (!list.includes(def)) {
      try { if (!P.equal(collect(def), settings()[def.body])) unsaved.add(def.key); }
      catch { unsaved.add(def.key); }
    }
    busy = true; controls();
    try {
      const data = await connection.request(base + (only ? '/modules/' + only.key : ''), { method: only ? 'PATCH' : 'PUT', body: payload });
      const preserved = new Set(definitions.filter(def => unsaved.has(def.key) || !P.equal(values(moduleForm(def)), submitted.get(def.key))).map(def => def.key));
      acceptSettings(data, previous); definitions.filter(def => !preserved.has(def.key)).forEach(renderModule); dirty(); clearPreview('规则已保存，可重新检查。');
      saveFeedback = only ? only.title + '已保存' : '已保存'; P.message(status, window.ChunnelAdminUI ? '' : saveFeedback);
      if (selected) { const old = selected; try { await fetchAccount(old.username, false, old.account.user_id); } catch { clearAccount(); if (!connection.lost) { saveFeedback += '；账号刷新未完成，请重新查询账号。'; P.message(status, saveFeedback); } } }
    } catch (error) { if (!connection.lost) { saveFeedback = failure(error).message + '；输入已保留。'; saveError = true; if (!window.ChunnelAdminUI) P.message(status, saveFeedback, 'error'); } }
    finally { busy = false; controls(); }
  }
  function operationPreview(element: HTMLElement, text: string, tone = '') { element.textContent = text; element.dataset.tone = tone; }
  function updateAdjustPreview() {
    const form = $<HTMLFormElement>('[data-progression-adjust]'), output = $('[data-progression-adjust-preview]');
    if (!selected) { operationPreview(output, '输入变动后显示调整结果预检。'); return; }
    const current = P.integer(selected.account.experience), maximum = P.integer(selected.settings.experience.maximum), unit = selected.settings.experience.name;
    const raw = control(form, 'delta').value.trim();
    if (!raw) { operationPreview(output, `当前 ${current} ${unit}；输入正数增加或负数扣减。`); return; }
    let delta;
    try { delta = P.integer(raw, true); } catch { operationPreview(output, '请输入不带前导零的普通整数。', 'error'); return; }
    const next = current + delta;
    if (delta === 0n) { operationPreview(output, '经验变动为 0，不会产生成长记录。', 'error'); return; }
    if (next < 0n) { operationPreview(output, `调整后为负数；本账号最多可扣减 ${current} ${unit}。`, 'error'); return; }
    if (next > maximum) { operationPreview(output, `调整后 ${next} ${unit} 超过账号上限 ${maximum}；本次最多可增加 ${maximum - current}。`, 'error'); return; }
    let level = '';
    if (selected.settings.levels.enabled) { const before = P.levelFor(selected.account.experience, selected.settings.levels), after = P.levelFor(next.toString(), selected.settings.levels); level = `；等级 ${before?.name || '未定级'} → ${after?.name || '未定级'}`; }
    operationPreview(output, `调整后 ${next} ${unit}${level}。按已保存规则预检，提交时仍由服务器校验。`);
  }
  function updateMemberPreview() {
    const form = $<HTMLFormElement>('[data-progression-member]'), output = $('[data-progression-member-preview]');
    if (!selected) { operationPreview(output, '选择操作后显示预计到期时间。'); return; }
    const cfg = selected.settings, m = selected.account.membership, action = control(form, 'action').value, planKey = control(form, 'plan').value, days = Number(control(form, 'days').value);
    if (action === 'revoke') {
      if (!m.plan || m.revoked) operationPreview(output, '当前没有可撤销的有效会员记录。', 'error');
      else operationPreview(output, `撤销后会员「${cfg.membership.plans.find(p => p.key === m.plan)?.name || m.plan}」立即失效，历史与届满时间保留。按当前时间预检。`);
      return;
    }
    const plan = cfg.membership.plans.find(p => p.key === planKey);
    if (!cfg.membership.enabled) { operationPreview(output, '会员模块当前关闭，不能开通或续期。', 'error'); return; }
    if (!plan || !plan.enabled) { operationPreview(output, '请选择启用中的会员方案。', 'error'); return; }
    if (!Number.isInteger(days) || days < 1 || days > 3650) { operationPreview(output, '会员天数须为 1–3650 的整数。', 'error'); return; }
    const now = Date.now(), currentUntil = new Date(m.until).getTime(), active = !!m.plan && !m.revoked && currentUntil > now;
    if (action === 'grant' && active) { operationPreview(output, '账号已有有效会员；请使用续期，避免覆盖当前权益。', 'error'); return; }
    let memberVersion = 0n;
    try { memberVersion = P.integer(m.version); } catch { operationPreview(output, '当前会员版本未确认，请重新查询账号。', 'error'); return; }
    if (action === 'renew' && (memberVersion === 0n || m.revoked || m.plan !== plan.key)) { operationPreview(output, m.revoked ? '该会员已撤销，请重新开通而非续期。' : `续期只能沿用当前方案「${cfg.membership.plans.find(p => p.key === m.plan)?.name || m.plan || '无'}」。`, 'error'); return; }
    const base = action === 'renew' && currentUntil > now ? currentUntil : now, until = base + days * 86400000;
    if (until > now + 3650 * 86400000) { operationPreview(output, '续期后距离当前时间将超过 3650 天上限，请减少天数。', 'error'); return; }
    const untilISO = new Date(until).toISOString();
    operationPreview(output, `预计${action === 'renew' ? '续期' : '开通'}「${plan.name}」${days} 天，到期 ${P.date(untilISO)} · 剩余 ${P.membershipDays(untilISO, now)} 天。仅预检，提交时仍由服务器校验。`);
  }
  function paintAccount() {
    $('[data-progression-account]').hidden = false; P.summary($('[data-progression-account-summary]'), account());
    const form = $<HTMLFormElement>('[data-progression-member]'), current = control(form, 'plan').value; control(form, 'plan').replaceChildren(...settings().membership.plans.map(p => { const option = P.node('option', '', p.name + (p.enabled ? '' : '（停用）')); option.value = p.key; return option; })); control(form, 'plan').value = settings().membership.plans.some(p => p.key === current) ? current : account().account.membership.plan || settings().membership.plans[0]?.key || '';
    P.history($('[data-progression-admin-history]'), entries, account().settings); $<HTMLButtonElement>('[data-progression-admin-more]').hidden = account().next_before === '0';
    const o = account().pending_order; $('[data-progression-order-tools]').hidden = !o; if (o) { $('[data-progression-order-description]').textContent = `${o.plan_name} · ${o.days} 天 · ${o.amount} ${o.symbol}（${o.currency}）`; }
    updateAdjustPreview(); updateMemberPreview(); controls();
  }
  async function fetchAccount(username: string, more = false, expected = ''): Promise<AdminProgressionSnapshot | null> {
    const request = ++serial, cursor = more ? account().next_before : '', oldEntries = more ? entries : [];
    await connection.check(); const raw = await connection.request(base + '/account?username=' + encodeURIComponent(username) + (more ? '&before=' + cursor : ''));
    if (request !== serial || connection.lost || !root.isConnected) return null;
    const data = adminProgressionAccount(raw, username, expected), combined = [...oldEntries, ...data.entries];
    if (new Set(combined.map(entry => entry.id)).size !== combined.length) throw new Error('成长分页记录重复，请重新查询');
    selected = data; entries = combined; paintAccount(); return data;
  }
  $<HTMLFormElement>('[data-progression-lookup]').addEventListener('submit', async event => {
    event.preventDefault(); if (busy || pending || readOnly || connection.lost) return; busy = true; clearAccount(); controls();
    try { const result = await fetchAccount(control($<HTMLFormElement>('[data-progression-lookup]'), 'username').value); if (result) P.message(operationStatus, '正在管理 @' + result.username + '（#' + result.account.user_id + '）'); }
    catch (error) { if (!connection.lost) P.message(operationStatus, failure(error).message, 'error'); } finally { busy = false; controls(); }
  });
  function operationDescription(item: AdminOperation): string {
    const operation = item.kind === 'experience' ? '调整经验 ' + item.payload.delta : ({ grant: '开通', renew: '续期', revoke: '撤销' })[item.payload.action] + '会员 ' + (item.quote.plan || item.payload.plan) + (item.payload.action === 'revoke' ? '' : ' · ' + item.payload.days + ' 天');
    return '账号 @' + (item.quote.username || item.payload.user_id) + '（#' + item.payload.user_id + '）\n' + operation + '\n原因：' + item.payload.reason;
  }
  function prepare(kind: 'experience' | 'membership', form: HTMLFormElement) {
    if (busy || pending || readOnly || connection.lost || !selected) return;
    try {
      const common = { user_id: selected.account.user_id, version: settings().version, request_id: P.nonce(), reason: control(form, 'reason').value.trim() };
      if (kind === 'experience') {
        const payload = { ...common, delta: control(form, 'delta').value };
        if (!P.validPayload('experience', payload)) throw new Error('请检查经验变动和原因');
        draft = { owner, kind: 'experience', payload, quote: { username: selected.username } };
      } else {
        const action = control(form, 'action').value, payload = { ...common, member_version: selected.account.membership.version, action, plan: action === 'revoke' ? '' : control(form, 'plan').value, days: action === 'revoke' ? 0 : Number(control(form, 'days').value) };
        if (!P.validPayload('membership', payload)) throw new Error('请检查会员操作和原因');
        draft = { owner, kind: 'membership', payload, quote: { username: selected.username, plan: settings().membership.plans.find(plan => plan.key === payload.plan)?.name || '' } };
      }
      $('[data-progression-admin-confirm]').textContent = operationDescription(draft); controls(); dialog.showModal();
    } catch (error) { P.message(operationStatus, failure(error).message, 'error'); }
  }
  $<HTMLFormElement>('[data-progression-adjust]').addEventListener('submit', event => { event.preventDefault(); prepare('experience', $<HTMLFormElement>('[data-progression-adjust]')); });
  $<HTMLFormElement>('[data-progression-member]').addEventListener('submit', event => { event.preventDefault(); prepare('membership', $<HTMLFormElement>('[data-progression-member]')); });
  $<HTMLFormElement>('[data-progression-adjust]').addEventListener('input', updateAdjustPreview); $<HTMLFormElement>('[data-progression-adjust]').addEventListener('change', updateAdjustPreview);
  $<HTMLFormElement>('[data-progression-member]').addEventListener('input', updateMemberPreview); $<HTMLFormElement>('[data-progression-member]').addEventListener('change', updateMemberPreview);
  control($<HTMLFormElement>('[data-progression-member]'), 'action').addEventListener('change', event => { const form = $<HTMLFormElement>('[data-progression-member]'), revoke = control(form, 'action').value === 'revoke'; control(form, 'plan').disabled = revoke; control(form, 'days').disabled = revoke; });
  control($<HTMLFormElement>('[data-progression-member]'), 'plan').addEventListener('change', event => { const plan = baseline?.membership.plans.find(p => p.key === control($<HTMLFormElement>('[data-progression-member]'), 'plan').value); if (plan) control($<HTMLFormElement>('[data-progression-member]'), 'days').value = String(plan.days); });
  async function execute() {
    if (busy || readOnly || connection.lost || !dialog.open || !draft && !pending) return;
    if (!pending && draft) pending = draft.kind === 'experience' ? P.save('experience', owner, draft.payload, draft.quote) : P.save('membership', owner, draft.payload, draft.quote);
    if (!pending) return;
    busy = true; controls(); const original = pending;
    try {
      const result = progressionAdjustment(await connection.request(base + '/' + original.kind, { method: 'POST', body: original.payload }), original, owner);
      P.clear(original.kind, owner); pending = P.read('experience', owner) || P.read('membership', owner); draft = null; dialog.close();
      let message = result.replayed ? '原操作已完成，本次未重复执行。' : '已记入成长记录。';
      clearAccount();
      if (original.quote.username) { try { await fetchAccount(original.quote.username, false, original.payload.user_id); } catch { message += ' 账号刷新未完成，请重新查询；已确认的操作无需重复提交。'; } }
      if (!connection.lost) P.message(operationStatus, message);
    } catch (error) {
      if (P.rejected(error)) { P.clear(original.kind, owner); pending = P.read('experience', owner) || P.read('membership', owner); draft = null; dialog.close(); }
      if (!connection.lost) P.message(operationStatus, failure(error).message + (pending ? ' 原操作已保留，请重试确认。' : ''), 'error');
    } finally { busy = false; controls(); }
  }
  $<HTMLButtonElement>('[data-progression-admin-confirm-submit]').addEventListener('click', execute);
  $<HTMLButtonElement>('[data-progression-admin-retry]').addEventListener('click', () => { if (busy || connection.lost || !pending) return; draft = pending; $('[data-progression-admin-confirm]').textContent = operationDescription(pending); controls(); dialog.showModal(); });
  $<HTMLButtonElement>('[data-progression-admin-more]').addEventListener('click', async () => { if (busy || !selected || selected.next_before === '0' || pending) return; busy = true; controls(); try { await fetchAccount(selected.username, true, selected.account.user_id); } catch (error) { P.message(operationStatus, failure(error).message, 'error'); } finally { busy = false; controls(); } });
  for (const action of ['recover', 'cancel'] as const) $('[data-progression-order-' + action + ']').addEventListener('click', async () => {
    if (busy || readOnly || pending || !selected?.pending_order || connection.lost) return;
    const reason = $<HTMLInputElement>('[data-progression-order-reason]').value.trim(); if (!reason) { P.message(operationStatus, '请填写订单处理原因。', 'error'); return; }
    const old = selected, order = old.pending_order; if (!order) return;
    busy = true; controls();
    try {
      const receipt = progressionOrderResult(await connection.request(base + '/orders/' + action, { method: 'POST', body: { user_id: old.account.user_id, request_id: order.request_id, reason } }), order, action);
      let message = receipt.status === 'pending' ? '已核对，订单仍待支付确认。' : receipt.status === 'completed' ? '已确认订单完成。' : '已确认订单取消。';
      clearAccount(); try { await fetchAccount(old.username, false, old.account.user_id); } catch { message += ' 账号刷新未完成，请重新查询。'; }
      if (!connection.lost) P.message(operationStatus, message);
    } catch (error) { if (!connection.lost) P.message(operationStatus, failure(error).message + ' 请核对原订单后重试。', 'error'); }
    finally { busy = false; controls(); }
  });
  root.querySelectorAll('[data-progression-check]').forEach(button => button.addEventListener('click', checkSettings));
  $<HTMLButtonElement>('[data-progression-reload]').addEventListener('click', reload); (root.querySelector<HTMLButtonElement>('[data-progression-save-all]') || $<HTMLButtonElement>('[data-admin-save]')).addEventListener('click', () => saveSettings());
  dialog.addEventListener('cancel', event => { if (busy) event.preventDefault(); }); required<HTMLFormElement>(dialog, 'form').addEventListener('submit', event => { if (busy) event.preventDefault(); });
  window.addEventListener('beforeunload', event => { if (busy || pending || dirty()) { event.preventDefault(); event.returnValue = ''; } });
  controls(); reload();
})();
