(() => {
  'use strict';
  const root = document.querySelector('[data-access-settings]'); if (!root) return;
  const $ = name => root.querySelector(`[data-access-config-${name}]`);
  const form = $('form'), fields = $('fields'), save = $('save'), reload = $('reload'), test = $('test'), status = $('status');
  if (![form, fields, save, reload, test, status, $('providers'), $('sms-fields'), $('switch')?.content?.firstElementChild].every(Boolean)) return;
  const readonly = form.dataset.readonly === 'true';
  let settings, catalog = [], smsCatalog = [], defaultBase = '', dirty = false, busy = false;
  const drafts = new Map(), controllers = new Set();
  const element = (tag, text, cls) => { const n = document.createElement(tag); if (text) n.textContent = text; if (cls) n.className = cls; return n; };
  const report = (message, tone = '') => { status.textContent = message; status.dataset.tone = tone; };
  function controls() {
    fields.disabled = readonly || busy || !settings;
    save.disabled = readonly || busy || !settings;
    reload.disabled = busy;
    test.disabled = readonly || busy || !settings || dirty || !settings.sms.enabled;
    $('test-phone').disabled = readonly || busy || !settings;
  }
  async function request(action, body) {
    const controller = new AbortController(), timer = setTimeout(() => controller.abort(), action === 'sms-test' ? 25000 : 15000);
    controllers.add(controller);
    try {
      const response = await fetch(`/api/v1/admin/access/${action}`, {credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
        ...(body ? {method: 'POST', headers: {'Content-Type': 'application/json', 'X-CSRF-Token': form.elements.csrf_token.value}, body: JSON.stringify(body)} : {})});
      const data = await response.json();
      if (!response.ok) throw new Error(data.error?.message || '登录与短信配置暂不可用');
      return data;
    } catch (error) {
      if (controller.signal.aborted) throw new Error(action === 'sms-test' ? '请求超时，发送结果尚不确定，请先检查手机和服务商记录。' : '请求超时，输入已保留；重新加载可确认保存结果。');
      throw error;
    } finally { clearTimeout(timer); controllers.delete(controller); }
  }
  function markDirty() { dirty = true; controls(); report('有未保存的登录与短信配置'); }
  function inputField(container, model, key, title, help = '', type = 'text', wide = false, options) {
    const label = element('label', '', wide ? 'access-config-wide' : '');
    const input = element(type === 'textarea' ? 'textarea' : options ? 'select' : 'input');
    input.dataset.accessField = key;
    if (options) for (const [value, text] of options) input.append(new Option(text, value));
    else if (type !== 'textarea') input.type = type;
    input.value = model[key] ?? '';
    input.autocomplete = type === 'password' ? 'new-password' : 'off'; input.spellcheck = false;
    input.maxLength = type === 'textarea' ? 16384 : type === 'password' ? 4096 : key === 'endpoint' ? 2048 : 512;
    label.append(element('span', title), input);
    if (help) label.append(element('small', help));
    input.addEventListener(options ? 'change' : 'input', () => { model[key] = input.value; markDirty(); });
    container.append(label);
    return {label, input};
  }
  function secretField(container, model, title) {
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
    root.querySelectorAll('[data-access-callback]').forEach(input => { input.value = base ? base + '/auth/' + input.dataset.accessCallback + '/callback' : ''; });
  }
  function renderProviders() {
    const list = $('providers'); list.replaceChildren();
    for (const info of catalog) {
      let p = settings.providers.find(p => p.id === info.id);
      if (!p) { p = {id: info.id, enabled: false, client_id: '', secret: ''}; settings.providers.push(p); }
      const card = element('article', '', 'access-config-card'), header = element('header'), title = element('div', '', 'access-config-provider-title'), toggle = element('div', '', 'access-config-toggle');
      card.dataset.registrationProvider = info.id;
      const control = $('switch').content.firstElementChild.cloneNode(true), check = control.querySelector('input');
      card.dataset.enabled = String(p.enabled); check.type = 'checkbox'; check.checked = p.enabled; check.setAttribute('role', 'switch'); check.setAttribute('aria-label', '启用 ' + info.name + ' 登录');
      title.append(element('h4', info.name));
      if (info.application_url) {
        const apply = element('a', '申请应用', 'access-config-apply');
        apply.href = info.application_url; apply.target = '_blank'; apply.rel = 'noopener noreferrer';
        apply.setAttribute('aria-label', '申请 ' + info.name + ' 登录应用（新窗口打开）');
        title.append(apply);
      }
      toggle.append(control); header.append(title, toggle); card.append(header);
      const grid = element('div', '', 'access-config-grid');
      const id = inputField(grid, p, 'client_id', '应用 ID / Client ID'); id.input.maxLength = 200;
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
  function smsDefaults(id) {
    return {enabled: settings.sms.enabled, provider: id, access_key: '', secret: '', has_secret: false, clear_secret: false, region: 'ap-guangzhou', app_id: '', sign_name: '', template_id: '', sender: '', endpoint: '',
      template_params: id === 'aliyun' ? '{"code":"{code}"}' : '["{code}"]', message: '【你的签名】验证码：{code}，{minutes}分钟内有效。', encoding: 'json', auth: 'bearer', auth_header: 'X-API-Key',
      payload: '{"phone":"{phone}","code":"{code}","purpose":"{purpose}"}', success_path: '', success_value: '', timeout_seconds: 8};
  }
  function renderSMS() {
    const p = settings.sms, id = p.provider, grid = $('sms-fields'); grid.replaceChildren();
    form.elements.sms_enabled.checked = p.enabled; form.elements.sms_provider.value = id; form.elements.sms_timeout.value = p.timeout_seconds;
    const info = smsCatalog.find(p => p.id === id); $('sms-description').textContent = info?.description || '';
    $('docs').hidden = !info?.docs; if (info?.docs) $('docs').href = info.docs;
    const add = (key, title, help, type, wide, options) => inputField(grid, p, key, title, help, type, wide, options);
    if (['aliyun', 'tencent', 'huawei', 'twilio'].includes(id)) add('access_key', {aliyun: 'AccessKey ID', tencent: 'SecretId', huawei: 'AppKey', twilio: 'Account SID'}[id]);
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
    window.ChunnelSelects?.refresh?.(root);
  }
  function render(data) {
    settings = data.settings; catalog = data.providers; smsCatalog = data.sms_providers; defaultBase = data.default_callback_base_url || '';
    dirty = false; drafts.clear();
    form.elements.callback_base_url.value = settings.callback_base_url || ''; form.elements.callback_base_url.placeholder = defaultBase || 'https://example.com';
    const select = $('sms-provider'); select.replaceChildren(...smsCatalog.map(p => new Option(p.name, p.id)));
    renderProviders(); renderSMS(); controls();
  }
  async function load() {
    if (busy) return;
    if (dirty && !window.confirm('重新加载会放弃本区尚未保存的修改，是否继续？')) return;
    busy = true; controls(); report('正在读取登录与短信配置…');
    try { render(await request('settings')); report('配置已加载。保存后即时生效。'); }
    catch (error) { report(error.message, 'error'); }
    finally { busy = false; controls(); }
  }
  form.elements.callback_base_url.addEventListener('input', event => { settings.callback_base_url = event.target.value.trim(); callbacks(); markDirty(); });
  form.elements.sms_enabled.addEventListener('change', event => { settings.sms.enabled = event.target.checked; markDirty(); });
  form.elements.sms_timeout.addEventListener('input', event => { settings.sms.timeout_seconds = Number(event.target.value); markDirty(); });
  form.elements.sms_provider.addEventListener('change', event => { drafts.set(settings.sms.provider, settings.sms); settings.sms = drafts.get(event.target.value) || smsDefaults(event.target.value); renderSMS(); markDirty(); });
  form.addEventListener('submit', async event => {
    event.preventDefault(); if (!settings || busy || readonly || !form.reportValidity()) return;
    const body = structuredClone(settings); busy = true; controls(); report('正在保存登录与短信配置…'); save.setAttribute('aria-busy', 'true');
    try { render(await request('settings', body)); report('登录与短信配置已保存，即时生效。', 'success'); }
    catch (error) { report(error.message, 'error'); }
    finally { busy = false; controls(); save.removeAttribute('aria-busy'); }
  });
  test.addEventListener('click', async () => {
    if (!settings || busy || readonly || dirty) return;
    const phone = $('test-phone').value.trim(), output = $('test-status'); output.dataset.tone = '';
    if (!/^\+[1-9][0-9]{6,14}$/.test(phone)) { output.textContent = '请填写带国际区号的完整手机号，例如 +8613800138000'; output.dataset.tone = 'error'; $('test-phone').focus(); return; }
    busy = true; controls(); output.textContent = '正在发送测试短信…';
    try { const data = await request('sms-test', {phone, revision: settings.revision}); output.textContent = data.message; output.dataset.tone = 'success'; }
    catch (error) { output.textContent = error.message; output.dataset.tone = 'error'; }
    finally { busy = false; controls(); }
  });
  reload.addEventListener('click', load);
  const siteBar = document.querySelector('[data-settings-savebar]');
  const toggleBar = () => { if (siteBar) siteBar.hidden = !!document.querySelector('[data-access-settings]:not([hidden]), [data-verification-settings]:not([hidden]), [data-ai-config]:not([hidden])'); };
  document.addEventListener('appearance-preview-section', toggleBar); toggleBar();
  window.addEventListener('beforeunload', event => { if (dirty || busy) { event.preventDefault(); event.returnValue = ''; } });
  window.addEventListener('pagehide', () => { controllers.forEach(c => c.abort()); root.querySelectorAll('input[type=password]').forEach(input => { input.value = ''; }); if (settings) { settings.providers.forEach(p => { p.secret = ''; }); settings.sms.secret = ''; } drafts.clear(); });
  window.addEventListener('pageshow', event => { if (event.persisted) { if (!dirty) load(); else report('已保留配置修改；为保护密钥，请重新填写尚未保存的密钥。'); } });
  load();
})();
