import { record } from '../shared/ledger-data';
import { recognitionAdmin, recognitionSettings, recognitionAccount, identityMutation, identityReceipt } from '../shared/recognition-data';
import type { RecognitionSettings, RecognitionBadge, RecognitionAccount, RecognitionIdentity, IdentityMutation } from '../shared/recognition-data';
import type { PublicBadge } from '../shared/experience-types';
import { required, field, check, report, errorMessage, busy, sessionClient } from './recognition-dom';

const root = document.querySelector<HTMLElement>('[data-recognition-admin]');
if (root) mount(root);
function mount(root: HTMLElement): void {
  const form = required<HTMLFormElement>(root, '[data-recognition-settings]'), list = required(root, '[data-recognition-designs]');
  const status = required(root, '[data-recognition-status]'), state = required(root, '[data-recognition-account-status]');
  const lookupForm = required<HTMLFormElement>(root, '[data-recognition-lookup]'), manage = required<HTMLFormElement>(root, '[data-recognition-manage]');
  const readOnly = root.dataset.readOnly === 'true', actor = Number(root.dataset.userId), csrf = root.dataset.csrf || '';
  const signature = () => JSON.stringify([...form.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>('input,select,textarea')].map(e => [e.value, e instanceof HTMLInputElement ? e.checked : false]));
  const manageSignature = () => JSON.stringify(['action', 'key', 'label', 'reason', 'days'].map(k => field(manage, k).value));
  let data: ReturnType<typeof recognitionAdmin> | undefined, account: RecognitionAccount | undefined;
  let saving = false, managing = false, editVersion = 0, baseline = '', configSerial = 0, lookupSerial = 0, manageEdits = 0;
  let uncertainSettings: { cfg: RecognitionSettings; signature: string } | undefined;
  const uncertainIdentity = new Map<number, { body: IdentityMutation; before: RecognitionIdentity }>();
  const client = sessionClient(() => {
    configSerial++; lookupSerial++; data = undefined; account = undefined; uncertainSettings = undefined; uncertainIdentity.clear();
    baseline = ''; form.hidden = true; form.reset(); list.replaceChildren(); required(root, '[data-recognition-pending]').replaceChildren();
    required(root, '[data-recognition-account]').hidden = true; required(root, '[data-recognition-evidence]').textContent = '';
    manage.reset(); saving = managing = false; busy(form, false); busy(manage, false);
    report(status, '登录状态已变化，请刷新页面后继续。', true); report(state, ''); publishDraft();
  });
  client.client?.setSession({ authenticated: actor > 0, id: actor > 0 ? actor : undefined, csrf_token: csrf, configured: true });
  const active = (epoch: number) => client.current(epoch) && root.isConnected;
  async function session(epoch: number): Promise<void> {
    const s = await client.session();
    if (!active(epoch) || (!readOnly && (!s.authenticated || s.id !== actor || s.csrf_token !== csrf))) throw new Error('登录状态已变化，请刷新页面');
  }
  async function request(path: string, method = 'GET', body?: object): Promise<unknown> {
    if (!client.client) throw new Error('页面资源未加载，请刷新后重试');
    return client.client.request(path, { method, body });
  }
  function publishDraft(): void { document.dispatchEvent(new CustomEvent('newpress:feature-status-draft', { detail: { dirty: !!baseline && signature() !== baseline } })); }
  function edited(): void { editVersion++; publishDraft(); }
  form.addEventListener('input', edited); form.addEventListener('change', edited);
  manage.addEventListener('input', () => { manageEdits++; }); manage.addEventListener('change', () => { manageEdits++; });
  window.addEventListener('beforeunload', e => { if (baseline && signature() !== baseline) { e.preventDefault(); e.returnValue = ''; } });
  const node = <K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] => {
    const n = document.createElement(tag); if (text !== undefined) n.textContent = text; if (cls) n.className = cls; return n;
  };
  const kinds = { title: '头衔', medal: '勋章', verified: '认证' };
  const icons = { award: '奖章', sparkles: '闪光', star: '星星', heart: '爱心', diamond: '菱形', 'shield-check': '盾牌', 'badge-check': '认证', zap: '闪电', 'pen-line': '创作', newspaper: '文章' };
  function input(label: string, key: string, value: string | boolean, type = 'text', options?: Record<string, string>): HTMLLabelElement {
    const wrap = node('label', label), e = options ? node('select') : node('input'); e.dataset.field = key;
    if (options) { Object.entries(options).forEach(([key, text]) => { const o = node('option', text); o.value = key; e.append(o); }); e.value = String(value); }
    else if (e instanceof HTMLInputElement) { e.type = type; if (type === 'checkbox') e.checked = value === true; else e.value = String(value); }
    if (type === 'checkbox') wrap.className = 'recognition-check';
    if (['key', 'name', 'color', 'background'].includes(key)) e.required = true;
    if (e instanceof HTMLInputElement) {
      if (key === 'key') { e.pattern = '[a-z]([a-z0-9-]{0,30}[a-z0-9])?'; e.maxLength = 32; }
      if (key === 'name') e.maxLength = 20; if (key === 'description') e.maxLength = 160;
      if (key === 'image') { e.dataset.mediaInput = ''; e.dataset.mediaTitle = '勋章图片'; e.placeholder = '/media/…（PNG / WebP）'; e.maxLength = 500; }
    }
    e.disabled = readOnly; wrap.append(e); return wrap;
  }
  function valueOf(card: Element): Record<string, string | boolean> {
    const out: Record<string, string | boolean> = {};
    card.querySelectorAll<HTMLInputElement | HTMLSelectElement>('[data-field]').forEach(e => { if (e.dataset.field) out[e.dataset.field] = e instanceof HTMLInputElement && e.type === 'checkbox' ? e.checked : e.value.trim(); });
    return out;
  }
  function displayBadge(value: Partial<PublicBadge>, decorative = false): HTMLElement {
    return window.NewPressRecognition?.badge(value, { interactive: false, decorative }) || node('span', value.name || '');
  }
  function design(b: RecognitionBadge, open = false): void {
    const card = node('details', undefined, 'recognition-design'); card.open = open;
    const summary = node('summary'), preview = node('span'), type = node('small'); summary.append(preview, type);
    const body = node('div', undefined, 'recognition-design-body'), first = node('div', undefined, 'recognition-fields');
    first.append(input('名称', 'name', b.name), input('类型', 'kind', b.kind, 'text', kinds), input('标识（英文，授予后不可改变）', 'key', b.key)); body.append(first, input('说明', 'description', b.description));
    const style = node('div', undefined, 'recognition-fields'); style.append(input('图标', 'icon', b.icon, 'text', icons), input('文字与强调色', 'color', b.color, 'color'), input('底色', 'background', b.background, 'color')); body.append(style);
    const more = node('div', undefined, 'recognition-fields'); more.append(input('展示样式', 'style', b.style, 'text', { soft: '柔和底色', outline: '描边', solid: '实色' }), input('自定义勋章图片（媒体库地址）', 'image', b.image), input('启用展示', 'enabled', b.enabled, 'checkbox')); body.append(more);
    const remove = node('button', '移除设计', 'admin-button admin-button-quiet'); remove.type = 'button'; remove.disabled = readOnly;
    remove.addEventListener('click', () => { card.remove(); edited(); report(status, '设计已从当前草稿移除，保存后生效。已授予的设计请改用停用。'); }); body.append(remove); card.append(summary, body);
    const update = () => {
      const values = valueOf(card), view: Partial<PublicBadge> = {};
      for (const k of ['key', 'kind', 'name', 'description', 'icon', 'color', 'background', 'style', 'image'] as const) view[k] = String(values[k] || '');
      preview.replaceChildren(displayBadge(view, view.kind === 'medal')); if (view.kind === 'medal') preview.append(node('span', view.name, 'identity-medal-name'));
      type.textContent = (kinds[b.kind] && kinds[view.kind as RecognitionBadge['kind']] || '') + (values.enabled ? ' · 已启用' : ' · 已停用');
    };
    card.addEventListener('input', update); card.addEventListener('change', update); update(); list.append(card);
  }
  function renderPending(): void {
    if (!data) return;
    const pending = required(root, '[data-recognition-pending]'); pending.replaceChildren(); required(root, '[data-recognition-pending-count]').textContent = `${data.pending_total} 个待审核申请`;
    data.pending.forEach(p => { const b = node('button', p.name || p.username, 'admin-button admin-button-secondary'); b.type = 'button'; b.addEventListener('click', () => { field(lookupForm, 'username').value = p.username; void lookup(p.username); }); pending.append(b); });
    if (!data.pending.length) pending.append(node('p', '当前没有待审核的认证申请。', 'recognition-help'));
  }
  function render(): void {
    if (!data) return; const cfg = data.settings;
    for (const k of ['tips_enabled', 'hide_amounts', 'allow_hide_amount', 'verification_enabled'] as const) check(form, k).checked = cfg[k];
    field(form, 'max_medals').value = String(cfg.max_medals); field(form, 'verification_help').value = cfg.verification_help;
    const currencies = required(root, '[data-recognition-currencies]'); currencies.replaceChildren();
    const all = [...data.currencies]; cfg.currencies.forEach(c => { if (!all.some(x => x.key === c.key)) all.push({ key: c.key, name: c.key, enabled: false, transfers: false }); });
    all.forEach(c => {
      const chosen = cfg.currencies.find(x => x.key === c.key), row = node('div', undefined, 'recognition-currency-row'); row.dataset.currency = c.key;
      const label = node('label', undefined, 'recognition-check'), box = node('input'); box.type = 'checkbox'; box.checked = !!chosen; box.dataset.currencyEnabled = '';
      label.append(box, node('span', c.name + ' · ' + c.key + (!c.enabled || !c.transfers ? '（需开启钱包与转账）' : '')));
      const presets = node('label', '快捷数值（逗号分隔）'), value = node('input'); value.value = (chosen?.presets || ['10', '30', '50']).join(', '); value.dataset.currencyPresets = ''; value.maxLength = 180;
      presets.append(value); row.append(label, presets); currencies.append(row);
    });
    list.replaceChildren(); cfg.badges.forEach(b => design(b)); form.hidden = false; renderPending(); baseline = signature(); publishDraft();
    if (readOnly) root.querySelectorAll<HTMLInputElement | HTMLButtonElement | HTMLSelectElement | HTMLTextAreaElement>('[data-recognition-settings] input,[data-recognition-settings] select,[data-recognition-settings] button,[data-recognition-manage] input,[data-recognition-manage] select,[data-recognition-manage] button').forEach(e => e.disabled = true);
  }
  async function load(): Promise<void> {
    if (saving) return; const ticket = ++configSerial, epoch = client.epoch(), revision = editVersion;
    report(status, '正在读取配置…');
    try {
      await session(epoch); const fresh = recognitionAdmin(await request('/api/v1/admin/recognition'));
      if (!active(epoch) || ticket !== configSerial) return;
      if (revision !== editVersion) { report(status, '读取期间有新修改，当前输入已保留。'); return; }
      data = fresh; uncertainSettings = undefined; render(); report(status, '配置已载入。');
    } catch (e) { if (active(epoch) && ticket === configSerial) report(status, errorMessage(e), true); }
  }
  required(root, '[data-recognition-reload]').addEventListener('click', () => { if (baseline && signature() !== baseline && !window.confirm('重新载入会放弃尚未保存的互动设置，是否继续？')) return; void load(); });
  required(root, '[data-recognition-add]').addEventListener('click', () => {
    if (readOnly || !data) return; if (list.children.length >= 100) { report(status, '最多配置 100 个身份设计', true); return; }
    design({ key: 'badge-' + crypto.randomUUID().slice(0, 8), kind: 'medal', name: '新勋章', description: '记录值得纪念的社区贡献', icon: 'award', color: '#2563eb', background: '#eff6ff', style: 'soft', image: '', enabled: true }, true);
    edited(); list.lastElementChild?.querySelector<HTMLInputElement>('[data-field=name]')?.focus();
  });
  function capture(): RecognitionSettings {
    if (!data) throw new Error('请先读取配置'); const cfg = { ...data.settings };
    for (const k of ['tips_enabled', 'hide_amounts', 'allow_hide_amount', 'verification_enabled'] as const) cfg[k] = check(form, k).checked;
    cfg.max_medals = Number(field(form, 'max_medals').value); cfg.verification_help = field(form, 'verification_help').value.trim();
    return recognitionSettings({ ...cfg, badges: [...list.children].map(valueOf), currencies: [...root.querySelectorAll<HTMLElement>('[data-currency]')].filter(row => required<HTMLInputElement>(row, '[data-currency-enabled]').checked).map(row => ({ key: row.dataset.currency, presets: required<HTMLInputElement>(row, '[data-currency-presets]').value.split(/[,，]/).map(s => s.trim()).filter(Boolean) })) });
  }
  form.addEventListener('submit', async e => {
    e.preventDefault(); if (readOnly || saving || !data) return;
    const epoch = client.epoch(), ticket = ++configSerial, savedSignature = signature(); let cfg: RecognitionSettings;
    try { cfg = uncertainSettings?.cfg || capture(); } catch (e) { report(status, errorMessage(e), true); return; }
    saving = true; busy(form, true);
    try {
      await session(epoch); if (!active(epoch)) return;
      if (uncertainSettings) {
        const original = uncertainSettings, fresh = recognitionAdmin(await request('/api/v1/admin/recognition'));
        if (!active(epoch) || ticket !== configSerial) return;
        if (JSON.stringify(fresh.settings) === JSON.stringify({ ...original.cfg, version: original.cfg.version + 1 })) { baseline = original.signature; report(status, '已核对上次设置保存成功；新的编辑内容仍保留。'); }
        else report(status, fresh.settings.version === original.cfg.version ? '上次设置尚未确认生效，请检查后再次保存。' : '设置版本已变化，当前草稿已保留，请核对后再保存。', true);
        data = fresh; uncertainSettings = undefined; renderPending(); publishDraft(); return;
      }
      uncertainSettings = { cfg, signature: savedSignature };
      const receipt = record(await request('/api/v1/admin/recognition', 'PUT', cfg));
      if (!active(epoch) || ticket !== configSerial) return;
      if (receipt.saved !== true) throw new Error('保存结果未确认，请先核对');
      data.settings = { ...cfg, version: cfg.version + 1 }; uncertainSettings = undefined; baseline = savedSignature; publishDraft();
      document.dispatchEvent(new Event('newpress:feature-status-refresh'));
      report(status, signature() === baseline ? '互动与身份设置已保存。' : '已保存提交时的设置，保存期间的新修改仍保留，请继续保存。');
    } catch (e) { if (active(epoch) && ticket === configSerial) report(status, errorMessage(e) + '；当前编辑内容已保留，再次保存将先核对上次结果。', true); }
    finally { if (active(epoch) && ticket === configSerial) { saving = false; busy(form, false); } }
  });
  function renderAccount(preserve: boolean): void {
    if (!account || !data) return;
    const summary = required(root, '[data-recognition-member-summary]'); summary.replaceChildren(node('h3', account.name + ' · @' + account.username));
    const badges = node('div', undefined, 'identity-badges'); account.badges.forEach(b => badges.append(displayBadge(b))); summary.append(badges);
    const p = account.identity.application;
    report(required(root, '[data-recognition-evidence]'), p ? `认证状态：${({ pending: '待审核', approved: '已通过', rejected: '已驳回', revoked: '已撤销' })[p.status]}\n公开说明：${p.label}\n申请说明（仅本人和管理员可见）：${p.evidence}${p.reason ? '\n审核原因：' + p.reason : ''}` : '');
    const selected = field(manage, 'key').value;
    field(manage, 'key').replaceChildren(...data.settings.badges.map(b => { const o = node('option', kinds[b.kind] + ' · ' + b.name); o.value = b.key; return o; }));
    if (preserve) field(manage, 'key').value = selected;
    else { manage.reset(); if (p?.status === 'pending') { field(manage, 'key').value = p.key; field(manage, 'action').value = 'approve'; } }
    required(root, '[data-recognition-account]').hidden = false;
  }
  async function lookup(username: string): Promise<void> {
    if (!username.trim()) return;
    const ticket = ++lookupSerial, epoch = client.epoch(), revision = manageEdits; account = undefined; required(root, '[data-recognition-account]').hidden = true; report(state, '正在查询成员…');
    try {
      await session(epoch); const fresh = recognitionAccount(await request('/api/v1/admin/recognition/account?username=' + encodeURIComponent(username.trim())), username);
      if (!active(epoch) || ticket !== lookupSerial) return;
      account = fresh; renderAccount(revision !== manageEdits); report(state, uncertainIdentity.has(fresh.user_id) ? '上次操作结果待核对，再次执行时会先读取状态。' : '');
    } catch (e) { if (active(epoch) && ticket === lookupSerial) report(state, errorMessage(e), true); }
  }
  lookupForm.addEventListener('submit', e => { e.preventDefault(); void lookup(field(lookupForm, 'username').value.trim()); });
  manage.addEventListener('submit', async e => {
    e.preventDefault(); if (readOnly || managing || !account || !data) return;
    const original = account, epoch = client.epoch(), ticket = lookupSerial, originalSignature = manageSignature(); let body: IdentityMutation;
    try { body = uncertainIdentity.get(original.user_id)?.body || identityMutation({ user_id: original.user_id, version: original.identity.version, action: field(manage, 'action').value, key: field(manage, 'key').value, label: field(manage, 'label').value.trim(), reason: field(manage, 'reason').value.trim(), days: Number(field(manage, 'days').value) }); }
    catch (e) { report(state, errorMessage(e), true); return; }
    managing = true; busy(manage, true); const selected = () => active(epoch) && ticket === lookupSerial && account?.user_id === original.user_id;
    try {
      await session(epoch); if (!selected()) return;
      if (uncertainIdentity.has(original.user_id)) {
        const fresh = recognitionAccount(await request('/api/v1/admin/recognition/account?username=' + encodeURIComponent(original.username)), original.username);
        if (!active(epoch) || fresh.user_id !== original.user_id) return;
        let message: string;
        try { identityReceipt({ identity: fresh.identity }, body, actor, uncertainIdentity.get(original.user_id)?.before); message = '已核对上次身份操作成功。'; }
        catch { message = fresh.identity.version === body.version ? '上次操作尚未确认生效，请检查后再次执行。' : '成员身份已变化，请核对当前状态后再操作。'; }
        uncertainIdentity.delete(original.user_id); if (selected()) { account = fresh; renderAccount(true); report(state, message); } return;
      }
      uncertainIdentity.set(original.user_id, { body, before: original.identity });
      const response = await request('/api/v1/admin/recognition/account', 'POST', body); if (!active(epoch)) return;
      const identity = identityReceipt(response, body, actor, original.identity); uncertainIdentity.delete(original.user_id);
      if (selected()) { account = { ...original, identity }; report(state, '成员身份已更新。'); }
      try {
        const fresh = recognitionAccount(await request('/api/v1/admin/recognition/account?username=' + encodeURIComponent(original.username)), original.username);
        if (selected() && fresh.user_id === original.user_id) { account = fresh; renderAccount(manageSignature() !== originalSignature); report(state, '成员身份已更新。'); }
        const freshConfig = recognitionAdmin(await request('/api/v1/admin/recognition'));
        if (active(epoch) && data) { data.pending = freshConfig.pending; data.pending_total = freshConfig.pending_total; renderPending(); }
      } catch { if (selected()) report(state, '成员身份已更新，列表刷新未完成，可稍后重新查询。', true); }
    } catch (e) { if (selected()) report(state, errorMessage(e) + '；再次执行将先核对上次结果。', true); }
    finally { if (active(epoch)) { managing = false; busy(manage, false); } }
  });
  window.addEventListener('pageshow', e => { if (e.persisted) void load(); });
  void load();
}
