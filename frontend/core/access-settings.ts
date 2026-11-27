import type {} from '../shared/account-types';
import { required, control, input as namedInput } from './ledger-admin-dom';
import { node, failure } from '../shared/ledger-data';
import { adminAccountRuntime } from './admin-account-runtime';
import { accessConfig, savedAccess, mergeAccess, choice, smsIDs } from '../shared/admin-config-data';
import type { AccessSettings, AccessConfig, LoginProvider, SMSSettings, SMSString, SMSID, LoginInfo, SMSInfo } from '../shared/admin-config-data';
type TextKey<T> = { [K in keyof T]: T[K] extends string ? K : never }[keyof T];
(() => {
  'use strict';
  const host = document.querySelector<HTMLElement>('[data-access-settings]'); if (!host) return; const root = host;
  const $ = <T extends HTMLElement = HTMLElement>(name: string): T => required<T>(root, `[data-access-config-${name}]`);
  const form = $<HTMLFormElement>('form'), fields = $<HTMLFieldSetElement>('fields'), save = $<HTMLButtonElement>('save'), reload = $<HTMLButtonElement>('reload'), test = $<HTMLButtonElement>('test'), status = $('status');
  if (![form, fields, save, reload, test, status, $('providers'), $('sms-fields'), $<HTMLTemplateElement>('switch')?.content?.firstElementChild].every(Boolean)) return;
  const readonly = form.dataset.readonly === 'true';
  let settings: AccessSettings, baseline: AccessSettings, catalog: LoginInfo[] = [], smsCatalog: SMSInfo[] = [], defaultBase = '', dirty = false, busy = false, editing = 0, smsUncertain = false;
  const drafts = new Map<SMSID, SMSSettings>();
  const element = <K extends keyof HTMLElementTagNameMap>(tag: K, text = '', cls = '') => node(tag, cls, text);
  const report = (message: string, tone = '') => { status.textContent = message; status.dataset.tone = tone; };
  function controls() {
    fields.disabled = readonly || !runtime.alive() || !settings;
    save.disabled = readonly || !runtime.alive() || busy || !settings;
    reload.disabled = busy || !runtime.alive();
    test.disabled = readonly || !runtime.alive() || busy || !settings || dirty || smsUncertain || !settings.sms.enabled;
    $<HTMLInputElement>('test-phone').disabled = readonly || !runtime.alive() || busy || !settings;
  }
  const runtime = adminAccountRuntime(root, root.dataset.userId || '', control(form, 'csrf_token').value, readonly, () => {
    root.querySelectorAll<HTMLInputElement>('input[type=password]').forEach(field => { field.value = ''; });
    if (settings) { settings.providers.forEach(p => { p.secret = ''; }); settings.sms.secret = ''; }
    drafts.clear(); controls(); report('登录状态或页面已变化，请重新载入后继续。', 'error');
  });
  const request = (action: string, body?: unknown) => runtime.request('/api/v1/admin/access/' + action, body, action === 'sms-test' ? 25000 : 15000);
  function markDirty() { if (readonly || !runtime.alive()) return; editing++; dirty = true; controls(); report('有未保存的登录与短信配置'); }
  function inputField<T extends LoginProvider | SMSSettings, K extends TextKey<T>>(container: HTMLElement, model: T, key: K, title: string, help = '', type = 'text', wide = false, options?: string[][]) {
    const label = element('label', '', wide ? 'access-config-wide' : '');
    const input = element(type === 'textarea' ? 'textarea' : options ? 'select' : 'input');
    input.dataset.accessField = String(key);
    if (options) for (const [value, text] of options) input.append(new Option(text || '', value || ''));
    else if (input instanceof HTMLInputElement) input.type = type;
    input.value = String(model[key] ?? '');
    input.autocomplete = type === 'password' ? 'new-password' : 'off'; input.spellcheck = false;
    if (!(input instanceof HTMLSelectElement)) input.maxLength = type === 'textarea' ? 16384 : type === 'password' ? 4096 : key === 'endpoint' ? 2048 : 512;
    label.append(element('span', title), input);
    if (help) label.append(element('small', help));
    input.addEventListener(options ? 'change' : 'input', () => { model[key] = input.value as T[K]; markDirty(); });
    container.append(label);
    return {label, input};
  }
  function secretField(container: HTMLElement, model: LoginProvider | SMSSettings, title: string) {
    const {label, input} = inputField(container, model, 'secret', title, model.has_secret ? '已保存密钥。留空保留，更换账号或接口地址后须重新填写。' : '尚未保存密钥。保存后不会回显。', 'password');
    const row = element('span', '', 'access-config-secret-actions'), clear = element('input'), text = element('span', '清除已保存密钥');
    clear.type = 'checkbox'; clear.checked = !!model.clear_secret;
    clear.setAttribute('aria-label', '清除' + title);
    // Keep this checkbox outside the label containing the password input.
    const clearLabel = element('label', '', 'access-config-secret-actions'); clearLabel.append(clear, text); row.append(clearLabel); container.append(row);
    clear.addEventListener('change', () => { model.clear_secret = clear.checked; if (clear.checked) { model.secret = ''; input.value = ''; } markDirty(); });
    input.addEventListener('input', () => { if (input.value) { model.clear_secret = false; clear.checked = false; } });
    label.classList.add('access-config-secret');
  }
  function callbacks() {
    const base = (settings.callback_base_url || defaultBase).replace(/\/+$/, '');
    root.querySelectorAll<HTMLInputElement>('[data-access-callback]').forEach(input => { input.value = base ? base + '/auth/' + input.dataset.accessCallback + '/callback' : ''; });
  }
  function renderProviders() {
    const list = $('providers'); list.replaceChildren();
    for (const info of catalog) {
      let p = settings.providers.find(p => p.id === info.id);
      if (!p) { p = {id: info.id, enabled: false, client_id: '', secret: '', has_secret: false, clear_secret: false}; settings.providers.push(p); }
      const card = element('article', '', 'access-config-card'), header = element('header'), title = element('div', '', 'access-config-provider-title'), toggle = element('div', '', 'access-config-toggle');
      card.dataset.registrationProvider = info.id;
      const template = $<HTMLTemplateElement>('switch').content.firstElementChild; if (!template) throw new Error('缺少开关模板'); const toggleNode = template.cloneNode(true); if (!(toggleNode instanceof HTMLElement)) throw new Error('开关模板无效'); const check = required<HTMLInputElement>(toggleNode, 'input');
      card.dataset.enabled = String(p.enabled); check.type = 'checkbox'; check.checked = p.enabled; check.setAttribute('role', 'switch'); check.setAttribute('aria-label', '启用 ' + info.name + ' 登录');
      title.append(element('h4', info.name));
      if (info.application_url) {
        const apply = element('a', '申请应用', 'access-config-apply');
        apply.href = info.application_url; apply.target = '_blank'; apply.rel = 'noopener noreferrer';
        apply.setAttribute('aria-label', '申请 ' + info.name + ' 登录应用（新窗口打开）');
        title.append(apply);
      }
      toggle.append(toggleNode); header.append(title, toggle); card.append(header);
      const grid = element('div', '', 'access-config-grid');
      const id = inputField(grid, p, 'client_id', '应用 ID / Client ID'); if (id.input instanceof HTMLInputElement) id.input.maxLength = 200;
      secretField(grid, p, info.name + ' 应用密钥 / Client Secret');
      const callback = element('div', '', 'access-config-callback'), url = element('input'), copy = element('button', '复制', 'admin-button admin-button-quiet');
      url.readOnly = true; url.dataset.accessCallback = info.id; url.setAttribute('aria-label', info.name + ' 回调地址'); copy.type = 'button'; copy.setAttribute('aria-label', '复制 ' + info.name + ' 回调地址');
      copy.addEventListener('click', async () => { try { await navigator.clipboard.writeText(url.value); report('已复制 ' + info.name + ' 回调地址' + (dirty ? '；配置尚未保存' : '')); } catch { url.focus(); url.select(); report('请复制已选中的回调地址'); } });
      const effective = element('small', !p.enabled ? '尚未开启，不影响其他登录方式。' : '配置已保存，点击「检查注册状态」核对回调地址和服务状态。', 'registration-field-status');
      callback.append(url, copy); card.append(grid, callback, effective); list.append(card);
      card.addEventListener('input', () => { effective.textContent = '配置已修改，保存后点击检查可查看实际状态。'; delete effective.dataset.ok; });
      check.addEventListener('change', () => { p.enabled = check.checked; card.dataset.enabled = String(p.enabled); count(); markDirty(); });
    }
    callbacks(); count();
  }
  function count() { $('count').textContent = settings.providers.filter(p => p.enabled).length + ' / 6 个平台已启用'; }
  function smsDefaults(id: SMSID): SMSSettings {
    return {enabled: settings.sms.enabled, provider: id, access_key: '', secret: '', has_secret: false, clear_secret: false, region: 'ap-guangzhou', app_id: '', sign_name: '', template_id: '', sender: '', endpoint: '',
      template_params: id === 'aliyun' ? '{"code":"{code}"}' : '["{code}"]', message: '【你的签名】验证码：{code}，{minutes}分钟内有效。', encoding: 'json', auth: 'bearer', auth_header: 'X-API-Key',
      payload: '{"phone":"{phone}","code":"{code}","purpose":"{purpose}"}', success_path: '', success_value: '', timeout_seconds: 8};
  }
  function renderSMS() {
    const p = settings.sms, id = p.provider, grid = $('sms-fields'); grid.replaceChildren();
    namedInput(form, 'sms_enabled').checked = p.enabled; control(form, 'sms_provider').value = id; control(form, 'sms_timeout').value = String(p.timeout_seconds);
    const info = smsCatalog.find(p => p.id === id); $('sms-description').textContent = info?.description || '';
    $<HTMLAnchorElement>('docs').hidden = !info?.docs; if (info?.docs) $<HTMLAnchorElement>('docs').href = info.docs;
    const add = (key: SMSString, title: string, help = '', type = 'text', wide = false, options?: string[][]) => inputField(grid, p, key, title, help, type, wide, options);
    if (['aliyun', 'tencent', 'huawei', 'twilio'].includes(id)) add('access_key', ({aliyun: 'AccessKey ID', tencent: 'SecretId', huawei: 'AppKey', twilio: 'Account SID'} as Partial<Record<SMSID, string>>)[id] || '账号标识');
    if (id === 'custom' || id === 'huawei') add('endpoint', '完整发送接口地址', '公网 HTTPS 地址，包含接口路径；更改地址须重新填写密钥。', 'url', true);
    if (id === 'custom') {
      add('encoding', '请求格式', 'JSON 与表单均使用 POST。', 'text', false, [['json', 'JSON'], ['form', 'Form URL Encoded']]);
      const auth = add('auth', '认证方式', '', 'text', false, [['bearer', 'Bearer Token'], ['basic', 'HTTP Basic'], ['header', '自定义认证头'], ['none', '无认证 / 请求体认证']]);
      auth.input.addEventListener('change', renderSMS);
      if (p.auth === 'basic') add('access_key', 'Basic 用户名');
      if (p.auth === 'header') add('auth_header', '认证头名称', '例如 X-API-Key。');
    }
    secretField(grid, p, {aliyun: 'AccessKey Secret', tencent: 'SecretKey', huawei: 'AppSecret', yunpian: 'API Key', twilio: 'Auth Token', custom: '网关令牌 / 密码'}[id]);
    if (id === 'tencent') { add('app_id', '短信应用 SDKAppID'); add('region', '地域', '例如 ap-guangzhou、ap-beijing、ap-nanjing。'); }
    if (['aliyun', 'tencent', 'huawei'].includes(id)) {
      add('sign_name', id === 'huawei' ? '签名名称（通用模板必填）' : '短信签名', '填写服务商审核通过的签名。');
      add('template_id', '模板 ID / Template Code');
      add('template_params', '模板参数', id === 'aliyun' ? 'JSON 对象，例如 {"code":"{code}"}。参数名需与审核模板一致。' : '有序 JSON 数组，例如 ["{code}","{minutes}"]。顺序需与审核模板一致。', 'textarea', true);
    }
    if (id === 'huawei') add('sender', '签名通道号', '国内签名分配的通道号，例如 csms100000001。');
    if (id === 'twilio') add('sender', '发送号码 / Messaging Service SID', '填写已开通的 From 号码，或 MG 开头的服务 SID。');
    if (id === 'twilio' || id === 'yunpian') add('message', '短信内容', '必须包含 {code}；签名及正文应与服务商审核内容一致。', 'textarea', true);
    if (id === 'custom') {
      add('payload', '请求参数模板（JSON）', '例如 {"mobile":"{phone}","content":"验证码 {code}","apikey":"{secret}"}。选择表单时会自动转为 URL 编码。', 'textarea', true);
      add('success_path', '业务成功字段', '例如 code、data.success。留空表示 HTTP 2xx 即成功。');
      add('success_value', '业务成功值', '例如 0、OK 或 true。必须与接口实际返回值一致。');
    }
  }
  function render(data: AccessConfig, preserve = false) {
    settings = data.settings; catalog = data.providers; smsCatalog = data.sms_providers; defaultBase = data.default_callback_base_url || '';
    if (!preserve) { dirty = false; drafts.clear(); baseline = structuredClone(settings); }
    control(form, 'callback_base_url').value = settings.callback_base_url || ''; namedInput(form, 'callback_base_url').placeholder = defaultBase || 'https://example.com';
    const select = $<HTMLSelectElement>('sms-provider'); select.replaceChildren(...smsCatalog.map(p => new Option(p.name, p.id)));
    renderProviders(); renderSMS(); controls();
  }
  async function load() {
    if (busy || !runtime.alive()) return;
    if (dirty && !window.confirm('重新加载会放弃本区尚未保存的修改，是否继续？')) return;
    const serial=editing;busy = true; controls(); report('正在读取登录与短信配置…');
    try { const data=accessConfig(await request('settings'));if(serial!==editing){report('读取期间有新输入，已保留当前修改；请保存或重新加载核对。');return;}render(data); report('配置已加载。保存后即时生效。'); }
    catch (error) { if (runtime.alive()) report(failure(error).message, 'error'); }
    finally { busy = false; controls(); }
  }
  control(form, 'callback_base_url').addEventListener('input', event => { settings.callback_base_url = namedInput(form, 'callback_base_url').value.trim(); callbacks(); markDirty(); });
  namedInput(form, 'sms_enabled').addEventListener('change', event => { settings.sms.enabled = namedInput(form, 'sms_enabled').checked; markDirty(); });
  control(form, 'sms_timeout').addEventListener('input', event => { settings.sms.timeout_seconds = Number(control(form, 'sms_timeout').value); markDirty(); });
  control(form, 'sms_provider').addEventListener('change', event => { drafts.set(settings.sms.provider, settings.sms); settings.sms = drafts.get(choice(control(form, 'sms_provider').value, smsIDs)) || smsDefaults(choice(control(form, 'sms_provider').value, smsIDs)); renderSMS(); markDirty(); });
  form.addEventListener('submit', async event => {
    event.preventDefault(); if (!settings || busy || readonly || !runtime.alive() || !form.reportValidity()) return;
    const body = structuredClone(settings), serial = editing; busy = true; controls(); report('正在保存登录与短信配置…'); save.setAttribute('aria-busy', 'true');
    try { const receipt = savedAccess(await request('settings', body), body, baseline); if (!runtime.alive()) return; const keep = serial !== editing; baseline = structuredClone(receipt.settings); const smsDraft=drafts.get(body.sms.provider);if(smsDraft){const updated=mergeAccess({...settings,sms:smsDraft},body,receipt.settings);drafts.set(body.sms.provider,updated.sms);} if (keep) receipt.settings = mergeAccess(settings, body, receipt.settings); render(receipt, keep); dirty = keep; report(keep ? '本次配置已保存；后续输入仍待保存。' : '登录与短信配置已保存，即时生效。', 'success'); }
    catch (error) { if (runtime.alive()) report(failure(error).message + '；输入已保留，重新加载可核对保存结果。', 'error'); }
    finally { busy = false; controls(); save.removeAttribute('aria-busy'); }
  });
  test.addEventListener('click', async () => {
    if (!settings || busy || readonly || dirty || smsUncertain || !runtime.alive()) return;
    const phone = $<HTMLInputElement>('test-phone').value.trim(), output = $('test-status'); output.dataset.tone = '';
    if (!/^\+[1-9][0-9]{6,14}$/.test(phone)) { output.textContent = '请填写带国际区号的完整手机号，例如 +8613800138000'; output.dataset.tone = 'error'; $<HTMLInputElement>('test-phone').focus(); return; }
    busy = true; controls(); output.textContent = '正在发送测试短信…';
    try { const data = await request('sms-test', {phone, revision: settings.revision}); if (data.ok !== true || typeof data.message !== 'string') throw new Error('测试短信回执未确认'); output.textContent = data.message; output.dataset.tone = 'success'; }
    catch (caught) { if (!runtime.alive()) return; const error = failure(caught); smsUncertain = !error.status || error.status >= 500; output.textContent = smsUncertain ? '测试短信发送结果尚未确认，请先检查手机和服务商记录；本页已暂停再次发送。' : error.message; output.dataset.tone = 'error'; }
    finally { busy = false; controls(); }
  });
  reload.addEventListener('click', load);
  const siteBar = document.querySelector<HTMLElement>('[data-settings-savebar]');
  const toggleBar = () => { if (siteBar) siteBar.hidden = !!document.querySelector('[data-access-settings]:not([hidden]), [data-verification-settings]:not([hidden]), [data-ai-config]:not([hidden])'); };
  document.addEventListener('appearance-preview-section', toggleBar); toggleBar();
  window.addEventListener('beforeunload', event => { if (dirty || busy) { event.preventDefault(); event.returnValue = ''; } });
  load();
})();
