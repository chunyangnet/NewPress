import type {} from '../shared/account-types';
import { required, control, input as namedInput } from './ledger-admin-dom';
import { node, failure } from '../shared/ledger-data';
import { adminAccountRuntime } from './admin-account-runtime';
import { verificationConfig, savedVerification, mergeVerification, choice, verificationIDs, sceneIDs } from '../shared/admin-config-data';
import type { VerificationSettings, VerificationInfo, VerificationProvider, VerificationID } from '../shared/admin-config-data';
(() => {
  'use strict';
  const host = document.querySelector<HTMLElement>('[data-verification-settings]'); if (!host) return; const root = host;
  const $ = <T extends HTMLElement = HTMLElement>(selector: string): T => required<T>(root, selector); const form = $<HTMLFormElement>('[data-verification-settings-form]'), fields = $<HTMLFieldSetElement>('[data-verification-fields]');
  const save = $<HTMLButtonElement>('[data-verification-save]'), test = $<HTMLButtonElement>('[data-verification-test]'), reload = $<HTMLButtonElement>('[data-verification-reload]'), status = $('[data-verification-status]'), preview = $('[data-verification-preview]');
  const readonly = form.dataset.readonly === 'true'; let settings: VerificationSettings, baseline: VerificationSettings, catalog: VerificationInfo[] = [], dirty = false, busy = false, editing = 0;
  const scenes = [
    ['comment', '评论与回复', '保护文章评论，包括访客评论与公开 API。'],
    ['register', '注册与邀请加入', '申请确认邮件、完成注册时分别验证。'],
    ['recover', '密码找回', '申请重置邮件和设置新密码时验证。'],
    ['login', '账号登录', '站点登录弹窗中的账号密码登录。'],
    ['admin_login', '后台登录', '管理员登录页独立控制。'],
    ['feedback', '建议与反馈', '创建反馈及回复反馈时验证。'],
  ] as const;
  const networkNames = { local: '本地可用', domestic: '国内服务', test: '国内需实测', restricted: '国内访问受限' };
  const el = <K extends keyof HTMLElementTagNameMap>(tag: K, text = '', cls = '') => node(tag, cls, text);
  function report(message: string, tone = '') { status.textContent = message; status.dataset.tone = tone; }
  function controlState() { fields.disabled = readonly || !runtime.alive() || !settings; save.disabled = readonly || !runtime.alive() || busy || !settings; test.disabled = readonly || !runtime.alive() || busy || !settings; reload.disabled = busy || !runtime.alive(); }
  const runtime = adminAccountRuntime(root, root.dataset.userId || '', control(form, 'csrf_token').value, readonly, () => {
    window.ChunnelVerification?.cancel(preview);
    root.querySelectorAll<HTMLInputElement>('input[type=password]').forEach(field => { field.value = ''; });
    if (settings) settings.providers.forEach(p => { p.secret = ''; p.api_secret = ''; });
    controlState(); report('登录状态或页面已变化，请重新载入后继续。', 'error');
  });
  const api = (body?: unknown) => runtime.request('/api/v1/admin/verification/settings', body);
  function provider(id: VerificationID = settings.default_provider): VerificationProvider | undefined {
    let value = settings.providers.find(p => p.id === id);
    if (!value && id !== 'local') { value = { id, site_key: '', secret: '', secret_id: '', api_secret: '', has_secret: false, has_api_secret: false, clear_secret: false, clear_api_secret: false, recaptcha_net: true, min_score: 0.5 }; settings.providers.push(value); }
    return value;
  }
  function summary() {
    const badge = $('[data-verification-state]'); badge.textContent = settings.enabled ? '已启用' : '已关闭'; badge.dataset.enabled = String(settings.enabled);
    const count = Object.values(settings.scenes).filter(rule => rule.enabled).length;
    $('[data-verification-scene-count]').textContent = count + ' / 6 个场景' + (!settings.enabled ? ' · 总开关已关闭' : '');
    root.querySelectorAll<HTMLSelectElement>('[data-scene-provider]').forEach(select => {
      const rule = settings.scenes[choice(select.dataset.sceneProvider, sceneIDs)]; select.disabled = !rule.enabled;
      required<HTMLOptionElement>(select, 'option').textContent = '跟随默认 · ' + (catalog.find(p => p.id === settings.default_provider)?.name || '');
    });
  }
  function detail() {
    const id = settings.default_provider, info = catalog.find(item => item.id === id), p = provider();
    if (!info) return;
    $('[data-verification-provider-title]').textContent = info.name;
    $('[data-verification-provider-description]').textContent = info.description;
    $('[data-verification-network]').textContent = info.network_note;
    const docs = $<HTMLAnchorElement>('[data-verification-docs]'); docs.hidden = !info.docs; if (info.docs) docs.href = info.docs;
    $('[data-verification-credentials]').hidden = id === 'local'; $('[data-verification-local]').hidden = id !== 'local';
    root.querySelectorAll<HTMLElement>('[data-verification-tencent]').forEach(item => { item.hidden = id !== 'tencent'; });
    $('[data-verification-google]').hidden = !id.startsWith('recaptcha_'); $('[data-verification-score]').hidden = id !== 'recaptcha_v3';
    $('[data-verification-key-label]').textContent = id === 'geetest' ? 'Captcha ID' : id === 'tencent' ? 'CaptchaAppId' : 'Site Key';
    $('[data-verification-secret-label]').textContent = id === 'geetest' ? 'Captcha Key' : id === 'tencent' ? 'AppSecretKey' : 'Secret Key';
    if (p) {
      root.querySelectorAll<HTMLInputElement>('[data-verification-field]').forEach(input => { const value = p[choice(input.dataset.verificationField, ['site_key', 'secret', 'secret_id', 'api_secret', 'recaptcha_net', 'min_score', 'clear_secret', 'clear_api_secret'])]; if (input.type === 'checkbox') input.checked = !!value; else input.value = String(value ?? ''); });
      $('[data-verification-secret-state]').textContent = p.has_secret ? '已保存。留空保留；更改公开标识后需重新填写。' : '尚未保存密钥，仅发送到本站服务端。';
      $('[data-verification-api-secret-state]').textContent = p.has_api_secret ? '已保存。留空保留；更改 SecretId 后需重新填写。' : '用于 API 3.0 请求签名。';
    }
  }
  function render() {
    namedInput(form, 'enabled').checked = settings.enabled;
    for (const name of ['theme', 'language', 'timeout_seconds'] as const) control(form, name).value = String(settings[name]);
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

  }
  function changed(event: Event) {
    if (!settings || !runtime.alive() || readonly) return;
    const before=JSON.stringify(settings);
    const input = event.target; if (!(input instanceof HTMLInputElement || input instanceof HTMLSelectElement)) return; const checked = input instanceof HTMLInputElement && input.checked;
    if (input.matches('[data-verification-field]')) {
      const p = provider(); if (!p) return; const key = input.dataset.verificationField;
      if (key === 'recaptcha_net' || key === 'clear_secret' || key === 'clear_api_secret') p[key] = checked;
      else if (key === 'min_score') p.min_score = Number(input.value);
      else if (key === 'site_key' || key === 'secret_id' || key === 'secret' || key === 'api_secret') p[key] = input.value;
      else return;
      if (key === 'clear_secret' && checked) { p.secret = ''; $<HTMLInputElement>('[data-verification-field="secret"]').value = ''; }
      if (key === 'clear_api_secret' && checked) { p.api_secret = ''; $<HTMLInputElement>('[data-verification-field="api_secret"]').value = ''; }
      if (key === 'secret' && input.value) { p.clear_secret = false; $<HTMLInputElement>('[data-verification-field="clear_secret"]').checked = false; }
      if (key === 'api_secret' && input.value) { p.clear_api_secret = false; $<HTMLInputElement>('[data-verification-field="clear_api_secret"]').checked = false; }
    } else if (input.dataset.sceneEnabled) settings.scenes[choice(input.dataset.sceneEnabled, sceneIDs)].enabled = checked;
    else if (input.dataset.sceneProvider) settings.scenes[choice(input.dataset.sceneProvider, sceneIDs)].provider = choice(input.value, ['', ...verificationIDs]);
    else if (input.dataset.sceneSkip) settings.scenes[choice(input.dataset.sceneSkip, sceneIDs)].skip_members = checked;
    else if (input.name === 'enabled') settings.enabled = checked;
    else if (input.name === 'default_provider') { settings.default_provider = choice(input.value, verificationIDs); detail(); }
    else if (input.name === 'theme') settings.theme = choice(input.value, ['auto', 'light', 'dark']);
    else if (input.name === 'language') settings.language = choice(input.value, ['auto', 'zh-CN', 'en']);
    else if (input.name === 'timeout_seconds') settings.timeout_seconds = Number(input.value);
    else return;
    if(before===JSON.stringify(settings))return;
    editing++; dirty = true; summary(); report('有未保存的验证设置');
  }
  form.addEventListener('input', changed);
  form.addEventListener('change', changed);
  async function load() {
    if (busy || !runtime.alive()) return; if (dirty && !window.confirm('重新加载会放弃本区尚未保存的修改，是否继续？')) return; const serial=editing;busy = true; controlState(); report('正在读取验证设置…'); window.ChunnelVerification?.cancel(preview);
    try { const data = verificationConfig(await api());if(serial!==editing){report('读取期间有新输入，已保留当前修改；请保存或重新加载核对。');return;} settings = data.settings; baseline = structuredClone(settings); catalog = data.providers; dirty = false; render(); report(readonly ? '只读模式，登录后可保存与测试' : '设置已加载'); }
    catch (error) { if (runtime.alive()) report(failure(error).message, 'error'); }
    finally { busy = false; controlState(); }
  }
  form.addEventListener('submit', async event => {
    event.preventDefault(); if (!settings || busy || readonly || !runtime.alive() || !form.reportValidity()) return;
    const sent = structuredClone(settings), serial = editing; window.ChunnelVerification?.cancel(preview); busy = true; controlState(); save.setAttribute('aria-busy', 'true'); report('正在保存验证设置…');
    try { const saved = savedVerification(await api(sent), sent, baseline); if (!runtime.alive()) return; baseline = structuredClone(saved); const keep = serial !== editing; settings = keep ? mergeVerification(settings, sent, saved) : saved; dirty = keep; render(); report(keep ? '本次设置已保存；后续输入仍待保存。' : '验证设置已保存', 'success'); }
    catch (error) { if (runtime.alive()) report(failure(error).message, 'error'); }
    finally { busy = false; controlState(); save.removeAttribute('aria-busy'); }
  });
  test.addEventListener('click', async () => {
    if (busy || !settings || readonly || !runtime.alive()) return;
    if (dirty) { report('请先保存当前设置，再测试所选服务', 'error'); return; }
    busy = true; controlState(); report('请在下方完成验证');
    try { if (!window.ChunnelVerification) throw new Error('验证组件尚未加载，请重试'); await window.ChunnelVerification.getToken(preview, 'preview', { preview: settings.default_provider, csrf: control(form, 'csrf_token').value }); if (!runtime.alive()) return; report('测试通过：前端挑战与服务端校验均已完成', 'success'); }
    catch (error) { if (runtime.alive()) report(failure(error).message, 'error'); }
    finally { busy = false; controlState(); }
  });
  reload.addEventListener('click', load);
  window.addEventListener('beforeunload', event => { if (dirty) { event.preventDefault(); event.returnValue = ''; } });
  load();
})();
