(() => {
  'use strict';
  const root = document.querySelector('[data-verification-settings]'); if (!root) return;
  const $ = selector => root.querySelector(selector), form = $('[data-verification-settings-form]'), fields = $('[data-verification-fields]');
  const save = $('[data-verification-save]'), test = $('[data-verification-test]'), reload = $('[data-verification-reload]'), status = $('[data-verification-status]'), preview = $('[data-verification-preview]');
  const readonly = form.dataset.readonly === 'true'; let settings, catalog = [], dirty = false, busy = false;
  const scenes = [
    ['comment', '评论与回复', '保护文章评论，包括访客评论与公开 API。'],
    ['register', '注册与邀请加入', '申请确认邮件、完成注册时分别验证。'],
    ['recover', '密码找回', '申请重置邮件和设置新密码时验证。'],
    ['login', '账号登录', '站点登录弹窗中的账号密码登录。'],
    ['admin_login', '后台登录', '管理员登录页独立控制。'],
    ['feedback', '建议与反馈', '创建反馈及回复反馈时验证。'],
  ];
  const networkNames = { local: '本地可用', domestic: '国内服务', test: '国内需实测', restricted: '国内访问受限' };
  const el = (tag, text, cls) => { const node = document.createElement(tag); if (text) node.textContent = text; if (cls) node.className = cls; return node; };
  function report(message, tone = '') { status.textContent = message; status.dataset.tone = tone; }
  function controlState() { fields.disabled = readonly || busy || !settings; save.disabled = readonly || busy || !settings; test.disabled = readonly || busy || !settings; reload.disabled = busy; }
  async function api(body) {
    const controller = new AbortController(), timer = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await fetch('/api/v1/admin/verification/settings', { credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
        ...(body ? { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': form.elements.csrf_token.value }, body: JSON.stringify(body) } : {}) });
      const data = await response.json(); if (!response.ok) throw new Error(data.error?.message || '验证设置暂不可用'); return data;
    } catch (error) { if (controller.signal.aborted) throw new Error('请求超时，当前输入已保留；请重新加载以确认保存结果'); throw error; }
    finally { clearTimeout(timer); }
  }
  function provider(id = settings.default_provider) {
    let value = settings.providers.find(p => p.id === id);
    if (!value && id !== 'local') { value = { id, site_key: '', secret: '', secret_id: '', api_secret: '', recaptcha_net: true, min_score: 0.5 }; settings.providers.push(value); }
    return value;
  }
  function summary() {
    const badge = $('[data-verification-state]'); badge.textContent = settings.enabled ? '已启用' : '已关闭'; badge.dataset.enabled = String(settings.enabled);
    const count = Object.values(settings.scenes).filter(rule => rule.enabled).length;
    $('[data-verification-scene-count]').textContent = count + ' / 6 个场景' + (!settings.enabled ? ' · 总开关已关闭' : '');
    root.querySelectorAll('[data-scene-provider]').forEach(select => {
      const rule = settings.scenes[select.dataset.sceneProvider]; select.disabled = !rule.enabled;
      select.options[0].textContent = '跟随默认 · ' + (catalog.find(p => p.id === settings.default_provider)?.name || '');
    });
  }
  function detail() {
    const id = settings.default_provider, info = catalog.find(item => item.id === id), p = provider();
    if (!info) return;
    $('[data-verification-provider-title]').textContent = info.name;
    $('[data-verification-provider-description]').textContent = info.description;
    $('[data-verification-network]').textContent = info.network_note;
    const docs = $('[data-verification-docs]'); docs.hidden = !info.docs; if (info.docs) docs.href = info.docs;
    $('[data-verification-credentials]').hidden = id === 'local'; $('[data-verification-local]').hidden = id !== 'local';
    root.querySelectorAll('[data-verification-tencent]').forEach(item => { item.hidden = id !== 'tencent'; });
    $('[data-verification-google]').hidden = !id.startsWith('recaptcha_'); $('[data-verification-score]').hidden = id !== 'recaptcha_v3';
    $('[data-verification-key-label]').textContent = id === 'geetest' ? 'Captcha ID' : id === 'tencent' ? 'CaptchaAppId' : 'Site Key';
    $('[data-verification-secret-label]').textContent = id === 'geetest' ? 'Captcha Key' : id === 'tencent' ? 'AppSecretKey' : 'Secret Key';
    if (p) {
      root.querySelectorAll('[data-verification-field]').forEach(input => { const value = p[input.dataset.verificationField]; if (input.type === 'checkbox') input.checked = !!value; else input.value = value ?? ''; });
      $('[data-verification-secret-state]').textContent = p.has_secret ? '已保存。留空保留；更改公开标识后需重新填写。' : '尚未保存密钥，仅发送到本站服务端。';
      $('[data-verification-api-secret-state]').textContent = p.has_api_secret ? '已保存。留空保留；更改 SecretId 后需重新填写。' : '用于 API 3.0 请求签名。';
    }
  }
  function render() {
    form.elements.enabled.checked = settings.enabled;
    for (const name of ['theme', 'language', 'timeout_seconds']) form.elements[name].value = settings[name];
    const choices = $('[data-verification-providers]'); choices.replaceChildren();
    for (const info of catalog) {
      const label = el('label', '', 'verification-provider-choice'), radio = el('input'), copy = el('div'), badge = el('span', networkNames[info.network], 'verification-network-tag');
      radio.type = 'radio'; radio.name = 'default_provider'; radio.value = info.id; radio.checked = settings.default_provider === info.id; badge.dataset.network = info.network;
      const saved = settings.providers.find(p => p.id === info.id); copy.append(el('b', info.name), el('small', info.id === 'local' ? '开箱即用 · 无需密钥' : saved?.has_secret ? '已保存密钥' : '填写密钥后启用'));
      label.append(radio, copy, badge); choices.append(label);
    }
    const sceneList = $('[data-verification-scenes]'); sceneList.replaceChildren();
    for (const [id, title, description] of scenes) {
      const rule = settings.scenes[id], row = el('div', '', 'verification-scene'), toggle = el('label', '', 'admin-switch admin-switch-field');
      const input = el('input'); input.type = 'checkbox'; input.setAttribute('role', 'switch'); input.checked = rule.enabled; input.dataset.sceneEnabled = id;
      const track = el('span', '', 'admin-switch-track'); track.setAttribute('aria-hidden', 'true'); track.append(el('i')); const copy = el('span', '', 'admin-switch-copy'); copy.append(el('b', title), el('small', description)); toggle.append(input, track, copy);
      const select = el('select'); select.dataset.sceneProvider = id; select.setAttribute('aria-label', title + '验证服务'); select.append(new Option('跟随默认', ''));
      for (const item of catalog) select.append(new Option(item.name, item.id)); select.value = rule.provider;
      row.append(toggle, select);
      if (id === 'comment' || id === 'feedback') { const skip = el('label', '', 'verification-check'), check = el('input'); check.type = 'checkbox'; check.checked = rule.skip_members; check.dataset.sceneSkip = id; skip.append(check, document.createTextNode('已登录用户免验证')); row.append(skip); }
      sceneList.append(row);
    }
    detail(); summary(); controlState();
    window.ChunnelSelects?.refresh?.(root);
  }
  function changed(event) {
    if (!settings || busy || readonly) return;
    const input = event.target;
    if (input.matches('[data-verification-field]')) {
      const p = provider(); if (!p) return; const key = input.dataset.verificationField;
      p[key] = input.type === 'checkbox' ? input.checked : input.type === 'number' ? Number(input.value) : input.value;
    } else if (input.dataset.sceneEnabled) settings.scenes[input.dataset.sceneEnabled].enabled = input.checked;
    else if (input.dataset.sceneProvider) settings.scenes[input.dataset.sceneProvider].provider = input.value;
    else if (input.dataset.sceneSkip) settings.scenes[input.dataset.sceneSkip].skip_members = input.checked;
    else if (input.name === 'enabled') settings.enabled = input.checked;
    else if (input.name === 'default_provider') { settings.default_provider = input.value; detail(); }
    else if (['theme', 'language', 'timeout_seconds'].includes(input.name)) settings[input.name] = input.type === 'number' ? Number(input.value) : input.value;
    else return;
    dirty = true; summary(); report('有未保存的验证设置');
  }
  form.addEventListener('input', changed);
  form.addEventListener('change', changed);
  async function load() {
    if (busy) return; busy = true; controlState(); report('正在读取验证设置…'); window.ChunnelVerification?.cancel(preview);
    try { const data = await api(); settings = data.settings; catalog = data.providers; dirty = false; render(); report(readonly ? '只读模式，登录后可保存与测试' : '设置已加载'); }
    catch (error) { report(error.message, 'error'); }
    finally { busy = false; controlState(); }
  }
  form.addEventListener('submit', async event => {
    event.preventDefault(); if (!settings || busy || readonly || !form.reportValidity()) return;
    window.ChunnelVerification?.cancel(preview); busy = true; controlState(); save.setAttribute('aria-busy', 'true'); report('正在保存验证设置…');
    try { settings = await api(settings); dirty = false; render(); report('验证设置已保存', 'success'); }
    catch (error) { report(error.message, 'error'); }
    finally { busy = false; controlState(); save.removeAttribute('aria-busy'); }
  });
  test.addEventListener('click', async () => {
    if (busy || !settings || readonly) return;
    if (dirty) { report('请先保存当前设置，再测试所选服务', 'error'); return; }
    busy = true; controlState(); report('请在下方完成验证');
    try { await window.ChunnelVerification.getToken(preview, 'preview', { preview: settings.default_provider, csrf: form.elements.csrf_token.value }); report('测试通过：前端挑战与服务端校验均已完成', 'success'); }
    catch (error) { report(error.message, 'error'); }
    finally { busy = false; controlState(); }
  });
  reload.addEventListener('click', load);
  window.addEventListener('beforeunload', event => { if (dirty) { event.preventDefault(); event.returnValue = ''; } });
  load();
})();
