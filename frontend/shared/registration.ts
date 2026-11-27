import type { RegistrationController } from './account-types';
import { required, input } from './account-dom';
import { object } from './account-data';
import { registrationFields, isChannel, isField, isFlow, parsePolicy, parseBinding, parseCode, accessFailure } from './registration-data';
import type { AccessFlow, ContactChannel, FieldMode, RegistrationField, RegistrationPolicy, PendingBinding, AccessFailure } from './registration-data';
type Payload = Record<string, string>;
interface CodeState { challenge?: string; until?: number; target?: string }

export function mountRegistration(): void {
  if (window.ChunnelRegistration) return;
  function mount(root: HTMLElement): RegistrationController | undefined {
    if (!root || !root.querySelector('[data-access-register]')) return;
    if (root.chunnelRegistration) return root.chunnelRegistration;
    root.dataset.registrationReady = 'true';
    const modal = root.hasAttribute('data-access-modal');
    const $ = <T extends Element = HTMLElement>(selector: string): T => required<T>(root, selector);
    const form = $<HTMLFormElement>('[data-access-register]'), recovery = $<HTMLFormElement>('[data-access-request]'), reset = $<HTMLFormElement>('[data-access-complete]');
    const result = $('[data-access-result]'), flows = [...root.querySelectorAll<HTMLButtonElement>('[data-access-flow]')];
    let policy: RegistrationPolicy | null = null, pending: PendingBinding | null = null, busy = false, generation = 0, finished = false, uncertain: Payload | null = null;
    let flow: AccessFlow = 'register', token = '', linkKind = '', controller: AbortController | null = null;
    const codes: Record<ContactChannel, CodeState> = { email: {}, phone: {} };
    const id = () => btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(32)))).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '');
    const report = (message = '', error = false) => { result.textContent = message; result.dataset.error = String(error); };
    const currentFields = () => ({display_name: input(form, 'display_name'), username: input(form, 'username'), email: input(form, 'email'), phone: input(form, 'phone'), password: input(form, 'password'), invitation_code: input(form, 'invitation_code'), registration_code: input(form, 'registration_code'), confirm_password: input(form, 'confirm_password'), email_code: input(form, 'email_code'), phone_code: input(form, 'phone_code')});
    const regionSelect = $<HTMLSelectElement>('[data-phone-region]');
    const phoneRegion = () => policy?.phone_region || { selectable: true, default_code: '+86', codes: ['+86'] };
    const phoneValue = () => {
      const value = input(form, 'phone').value.trim().replace(/[\s()\-]/g, '');
      return !value || value.startsWith('+') ? value : (phoneRegion().selectable ? regionSelect.value : phoneRegion().default_code) + value;
    };
    const codeChannel = (): ContactChannel => { const channel = $<HTMLSelectElement>('[data-code-login-channel]').value; if (!isChannel(channel)) throw new Error('验证码通道无效'); return channel; };
    const contactValue = (channel: ContactChannel) => channel === 'phone' ? phoneValue() : input(form, channel).value.trim();
    function configurePhone() {
      const region = phoneRegion();
      regionSelect.replaceChildren(...region.codes.map(code => { const option = document.createElement('option'); option.value = code; option.textContent = code; return option; }));
      regionSelect.value = region.default_code;
      regionSelect.hidden = !region.selectable;
      $('[data-phone-region-fixed]').hidden = region.selectable;
      $('[data-phone-region-fixed]').textContent = region.default_code;
    }
    function phoneValid() {
      const field = input(form, 'phone'), value = phoneValue(), region = phoneRegion();
      const codes = region.selectable ? region.codes : [region.default_code];
      const valid = !value || /^\+[1-9][0-9]{7,14}$/.test(value) && (flow === 'login' || codes.some(code => value.startsWith(code) && value.length > code.length));
      field.setCustomValidity(valid ? '' : '请输入有效的手机号，并使用本站允许的区号');
      return valid;
    }
    function animateFlow() {
      if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches || document.body?.matches('.theme-force-reduced-motion, .theme-motion-minimal, .theme-motion-off')) return;
      [form, recovery, reset].filter(owner => !owner.hidden).forEach(owner => owner.animate?.([{ opacity: .4, transform: 'translateY(6px)' }, { opacity: 1, transform: 'none' }], { duration: 180, easing: 'ease-out' }));
    }
    function readLocation() {
      const invite = new URLSearchParams(location.search).get('invite');
      if (invite && /^[A-Za-z0-9_-]{4,128}$/.test(invite)) {
        input(form, 'invitation_code').value = invite;
        try { sessionStorage.setItem('np_referral', invite); } catch { /* The current form keeps the URL invitation when storage is unavailable. */ }
      } else {
        try { const saved = sessionStorage.getItem('np_referral'); if (saved && /^[A-Za-z0-9_-]{4,128}$/.test(saved)) input(form, 'invitation_code').value = saved; }
        catch { /* Optional referral persistence must not prevent login or registration. */ }
      }
      if (modal) { flow = 'register'; return; }
      const fragment = new URLSearchParams(location.hash.slice(1));
      token = fragment.get('token') || ''; linkKind = fragment.get('kind') || 'register';
      const requested = new URLSearchParams(location.search).get('flow');
      flow = token && linkKind === 'recover' ? 'recover' : isFlow(requested) ? requested : 'register';
      if (location.hash) history.replaceState(null, '', location.pathname + location.search);
      if (fragment.has('oauth_error')) report('快捷登录未完成。请重新授权，或选择其他登录方式。', true);
    }
    async function api(path: string, body?: Payload): Promise<Record<string, unknown>> {
      const request = new AbortController(), timer = setTimeout(() => request.abort(), 15000);
      controller = request;
      try {
        const response = await fetch('/api/v1/social/access' + path, { credentials: 'same-origin', cache: 'no-store', signal: request.signal, ...(body ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}) });
        let data; try { data = object(await response.json()); } catch { throw new Error('未能确认处理结果，请保留当前页面后重试'); }
        if (!response.ok) {
          const failure = data.error && typeof data.error === 'object' ? object(data.error) : {};
          const details = failure.details && typeof failure.details === 'object' ? object(failure.details) : {};
          const error: AccessFailure = accessFailure({ message: failure.message || '操作暂时无法完成', fields: details.fields });
          error.code = typeof failure.code === 'string' ? failure.code : ''; error.definitive = response.status >= 400 && response.status < 500;
          error.retryAfter = Number(response.headers.get('Retry-After')) || 0;
          throw error;
        }
        if (body && data.ok !== true) throw new Error('未能确认处理结果，请保留当前页面后重试');
        return data;
      } catch (error) { if (request.signal.aborted) throw new Error('请求超时，结果尚未确认。请点击重试以核对原提交。'); throw error; }
      finally { clearTimeout(timer); if (controller === request) controller = null; }
    }
    function markBusy(active: boolean, owner?: HTMLFormElement) {
      busy = active; root.setAttribute('aria-busy', String(active));
      flows.forEach(button => { button.disabled = active || !!uncertain; });
      root.querySelectorAll<HTMLButtonElement>('button[type="submit"]').forEach(button => {
        button.disabled = active || finished;
        button.dataset.submitState = active && button.form === owner ? 'pending' : finished ? 'success' : 'idle';
        if (active && button.form === owner) { button.dataset.originalLabel = button.textContent || ''; button.textContent = '正在处理…'; }
        else if (button.dataset.originalLabel) { button.textContent = button.dataset.originalLabel; delete button.dataset.originalLabel; }
      });
      [form, recovery, reset].forEach(owner => owner.querySelectorAll('input').forEach(input => { input.readOnly = active || owner === form && !!uncertain; }));
      $<HTMLSelectElement>('[data-code-login-channel]').disabled = active || !!uncertain;
      regionSelect.disabled = active || !!uncertain || input(form, 'phone').disabled;
      root.querySelectorAll<HTMLButtonElement>('[data-account-login]').forEach(button => { button.disabled = active || !!uncertain; });
      tickCodes();
    }
    function fieldMode(key: RegistrationField): FieldMode {
      if (!policy) return 'hidden';
      if (flow === 'login') return key === $<HTMLSelectElement>('[data-code-login-channel]').value ? 'required' : 'hidden';
      if (flow === 'bind') {
        if (pending?.existing) return pending.required.includes(key) ? 'required' : 'hidden';
        if (['invitation_code', 'registration_code'].includes(key)) return policy.fields[key];
        return (pending?.required || policy.oauth_bindings || []).includes(key) ? 'required' : 'hidden';
      }
      if (key === 'email' && token) return 'hidden';
      if (key === 'invitation_code' && token && linkKind === 'invite') return 'hidden';
      return policy.fields[key] || 'hidden';
    }
    function paintFields() {
      if (!policy) return;
      const f = currentFields();
      for (const row of form.querySelectorAll<HTMLElement>('[data-registration-field]')) {
        const key = row.dataset.registrationField; if (!isField(key)) continue;
        const mode = fieldMode(key), input = f[key];
        row.hidden = mode === 'hidden'; input.disabled = row.hidden; input.required = mode === 'required';
        const marker = row.querySelector('[data-field-requirement]'); if (marker) marker.textContent = mode === 'required' ? '必填' : '选填';
      }
      const confirm = $('[data-registration-confirm]');
      regionSelect.disabled = busy || !!uncertain || f.phone.disabled;
      $('[data-phone-hint]').textContent = flow === 'login' ? '输入本地号码；已有账号也可粘贴完整国际号码登录。' : phoneRegion().selectable ? '选择区号后填写手机号码。' : `本站仅接受 ${phoneRegion().default_code} 区号，请填写本地手机号码。`;
      phoneValid();
      confirm.hidden = f.password.disabled || !policy.password_confirmation;
      f.confirm_password.disabled = confirm.hidden;
      f.confirm_password.required = !confirm.hidden && (f.password.required || !!f.password.value);
      for (const channel of (['email', 'phone'] as const)) {
        const panel = $(`[data-contact-code="${channel}"]`);
        panel.hidden = f[channel].disabled || !(flow === 'login' || (channel === 'email' ? policy.email_verification : policy.phone_verification));
        input(form, channel + '_code').disabled = panel.hidden;
        input(form, channel + '_code').required = !panel.hidden && !!f[channel].value.trim();
        const available = channel === 'email' ? policy.email_available : policy.sms_available;
        required(panel, '[data-code-unavailable]').hidden = available;
        required(panel, '[data-send-code]').hidden = !available;
      }
      const link = $<HTMLAnchorElement>('[data-registration-code-link]');
      link.hidden = !policy.registration_code_url || f.registration_code.disabled;
      if (!link.hidden) { const url = new URL(policy.registration_code_url, location.origin); if (url.protocol === 'https:' || url.origin === location.origin) link.href = url.href; else link.hidden = true; }
    }
    function render() {
      if (!policy) return;
      const config = policy;
      if (flow === 'login' && policy.code_login_enabled === false) {
        flow = 'register';
        if (!modal) history.replaceState(null, '', location.pathname);
        report('本站已关闭验证码登录，请使用密码或快捷登录。', true);
      }
      const binding = flow === 'bind', login = flow === 'login', recover = flow === 'recover';
      $('[data-access-switch]').hidden = binding || !!token || finished;
      const providers = root.querySelector<HTMLElement>('[data-oauth-login]'); if (providers) providers.hidden = binding && !!pending || finished;
      flows.forEach(button => {
        button.hidden = button.dataset.accessFlow === 'login' && config.code_login_enabled === false;
        button.setAttribute('aria-pressed', String(button.dataset.accessFlow === flow));
      });
      $('[data-access-heading]').textContent = binding ? '完善账号信息' : login ? '验证码登录' : recover ? token ? '设置新密码' : '找回账号' : '创建账号';
      $('[data-access-kicker]').textContent = binding ? pending?.provider || '快捷登录' : recover ? '账号恢复' : '欢迎加入';
      let allowed = login || binding ? policy.configured !== false : policy.registration === 'open' || policy.registration === 'invite' && (!!token || policy.fields.invitation_code !== 'hidden');
      const unavailable = !login && !binding && (['email', 'phone'] as const).some(channel => fieldMode(channel) === 'required' && (channel === 'email' ? config.email_verification : config.phone_verification) && !(channel === 'email' ? config.email_available : config.sms_available));
      if (unavailable) allowed = false;
      recovery.hidden = finished || !recover || !!token || !policy.recovery_available;
      reset.hidden = finished || !recover || !token;
      form.hidden = finished || recover || !allowed || binding && !pending;
      $('[data-code-login-choice]').hidden = !login;
      $('[data-access-intro]').textContent = finished ? '你的账号已就绪。' : binding ? pending ? `已通过 ${pending.provider} 验证。补齐以下信息后继续。` : '请选择快捷登录方式重新授权，再完善账号信息。' : login ? '使用已验证的邮箱或手机号登录。' : recover ? token ? '设置新密码后，旧会话会退出。' : '输入绑定邮箱，接收密码重置链接。' : !allowed ? unavailable ? '所需验证通道暂不可用，请联系管理员或使用快捷登录。' : policy.registration === 'invite' ? '本站采用邀请注册。请打开管理员发送的邀请邮件。' : '本站暂未开放注册，你可以先浏览公开内容。' : '按你的习惯建立账号，收藏与创作从这里开始。';
      $('[data-registration-submit]').textContent = uncertain ? '重试并确认原提交' : binding ? '完成绑定并继续' : login ? '验证并登录' : '创建账号';
      $('[data-access-identity]').hidden = true;
      for (const key of ['username', 'display_name']) { input(reset, key).disabled = true; input(reset, key).required = false; }
      input(reset, 'confirm_password').required = true;
      $('[data-access-finish]').textContent = '保存新密码';
      $('[data-access-send]').textContent = '发送重置邮件';
      paintFields(); tickCodes();
      $('[data-registration-retry-note]').hidden = !uncertain;
    }
    function fieldErrors(value: unknown) {
      const error = accessFailure(value);
      if (error.code === 'auth.code_login_disabled' && policy) { policy.code_login_enabled = false; render(); }
      if (flow === 'bind' && ['auth.binding_expired', 'auth.provider_disabled'].includes(error.code || '')) pending = null;
      report(error.fields ? Object.values(error.fields).join('；') : error.message || '操作失败，请重试', true);
      if (error.fields) for (const [key, message] of Object.entries(error.fields)) {
        const input = form.elements.namedItem(key) || reset.elements.namedItem(key); if (!(input instanceof HTMLInputElement) || input.disabled) continue;
        input.setAttribute('aria-invalid', 'true');
        const row = input.closest('label, [data-contact-code]');
        if (row) { let note = row.querySelector('.access-field-error'); if (!note) { note = document.createElement('small'); note.className = 'access-field-error'; row.append(note); } note.textContent = message; }
      }
      if (!modal || root.closest('dialog')?.open) root.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus({ preventScroll: true });
    }
    function clearErrors(owner: HTMLFormElement) {
      owner.querySelectorAll('[aria-invalid]').forEach(input => input.removeAttribute('aria-invalid'));
      owner.querySelectorAll('.access-field-error').forEach(note => { if (!note.hasAttribute('data-code-unavailable')) note.remove(); });
    }
    async function run(owner: HTMLFormElement, action: (current: () => boolean) => Promise<void>) {
      if (!policy || busy || finished || owner.hidden || !owner.reportValidity()) return;
      const turn = generation; clearErrors(owner); markBusy(true, owner); report('');
      try { await action(() => generation === turn); }
      catch (error) { if (generation === turn) fieldErrors(error); }
      finally { if (generation === turn) { markBusy(false, owner); render(); } }
    }
    function success(data: Record<string, unknown>, recovered = false) {
      finished = true; uncertain = null; token = ''; form.reset(); reset.reset(); report('');
      try { sessionStorage.removeItem('np_referral'); } catch { /* Storage is optional. */ }
      $('[data-access-success]').hidden = false;
      $('[data-access-success-title]').textContent = recovered ? '密码已更新' : '欢迎回来，已为你登录';
      $('[data-access-success-text]').textContent = recovered ? '请使用新密码登录。' : data.username ? `你的用户名：${data.username}` : '现在可以继续阅读与创作。';
      if (!recovered) {
        // An interaction hub can own session refresh and broadcast after it has
        // resumed the pending action. A second local channel would race that read.
        if (document.dispatchEvent(new CustomEvent('chunnel:account-authenticated', { cancelable: true, detail: { root } }))) {
          try { const channel = new BroadcastChannel('chunnel.social'); channel.postMessage({ type: 'session' }); channel.close(); } catch {}
        }
      }
    }
    form.addEventListener('submit', event => {
      event.preventDefault();
      if (!policy || busy || form.hidden) return;
      const f = currentFields();
      phoneValid();
      if (!f.confirm_password.disabled && f.password.value !== f.confirm_password.value) { fieldErrors({ fields: { confirm_password: '两次输入的密码不一致' } }); return; }
      run(form, async current => {
        const submittedFlow = flow, config = policy; if (!config) return;
        let payload = uncertain;
        if (!payload) {
          payload = { request_id: id(), token, kind: linkKind };
          if (submittedFlow === 'bind') { if (!pending) throw new Error('请重新授权'); payload.binding_id = pending.binding_id; }
          for (const key of registrationFields) if (f[key] && !f[key].disabled) payload[key] = key === 'phone' ? phoneValue() : f[key].value;
          if (!f.confirm_password.disabled) payload.confirm_password = f.confirm_password.value;
          for (const channel of (['email', 'phone'] as const)) if (!f[channel].disabled) { payload[channel + '_challenge'] = codes[channel].challenge || ''; payload[channel + '_code'] = input(form, channel + '_code').value; }
        }
        const verification = window.ChunnelVerification ? await window.ChunnelVerification.getToken(form, submittedFlow === 'login' ? 'login' : 'register') : '';
        if (!current()) return;
        try {
          let data;
          if (submittedFlow === 'login') { const channel = codeChannel(); data = await api('/code-login', { channel, target: contactValue(channel), challenge: codes[channel].challenge || '', code: input(form, channel + '_code').value, verification_token: verification }); }
          else { uncertain = payload; data = await api(submittedFlow === 'bind' ? '/bind' : '/register', { ...payload, verification_token: verification }); }
          if (current()) success(data);
        } catch (error) { if (current() && accessFailure(error).definitive) uncertain = null; throw error; }
      });
    });
    recovery.addEventListener('submit', event => { event.preventDefault(); run(recovery, async current => {
      const email = input(recovery, 'email').value.trim();
      const verification_token = window.ChunnelVerification ? await window.ChunnelVerification.getToken(recovery, 'recover') : '';
      if (!current()) return;
      await api('/request', { kind: 'recover', email, verification_token });
      if (current()) report('申请已受理。请检查收件箱及垃圾邮件，一分钟后可以重新申请。');
    }); });
    reset.addEventListener('submit', event => { event.preventDefault();
      if (input(reset, 'password').value !== input(reset, 'confirm_password').value) { report('两次输入的密码不一致', true); return; }
      run(reset, async current => {
        const submitted: Payload = { token, kind: 'recover', password: input(reset, 'password').value, confirm_password: input(reset, 'confirm_password').value };
        submitted.verification_token = window.ChunnelVerification ? await window.ChunnelVerification.getToken(reset, 'recover') : '';
        if (!current()) return; const data = await api('/complete', submitted); if (current()) success(data, true);
      });
    });
    function tickCodes() {
      if (!policy) return;
      for (const button of root.querySelectorAll<HTMLButtonElement>('[data-send-code]')) {
        const channel = button.dataset.sendCode; if (!isChannel(channel)) continue; const seconds = Math.max(0, Math.ceil(((codes[channel].until || 0) - Date.now()) / 1000));
        button.disabled = busy || !!uncertain || seconds > 0 || !input(form, channel).value.trim();
        button.textContent = seconds ? `${seconds} 秒后重发` : '获取验证码';
      }
    }
    root.querySelectorAll<HTMLButtonElement>('[data-send-code]').forEach(button => button.addEventListener('click', () => {
      if (busy || uncertain || button.disabled) return;
      const channel = button.dataset.sendCode; if (!isChannel(channel)) return; const field = input(form, channel);
      if (channel === 'phone') phoneValid();
      if (!field.value.trim() || !field.reportValidity()) return;
      const turn = generation, target = contactValue(channel), purpose = flow === 'login' ? 'login' : flow === 'bind' ? 'bind' : 'register';
      markBusy(true); button.dataset.submitState = 'pending'; report('');
      (async () => {
        try {
          const verification_token = window.ChunnelVerification ? await window.ChunnelVerification.getToken(form, purpose === 'login' ? 'login' : 'register') : '';
          if (turn !== generation) return;
          const data = parseCode(await api('/code', { channel, target, purpose, verification_token }));
          if (turn !== generation) return;
          codes[channel] = { challenge: data.challenge, until: Date.now() + data.retry_after * 1000, target };
          report(channel === 'email' ? '验证码已发送，请检查邮箱。' : '验证码已发送，请查看短信。');
          input(form, channel + '_code').focus({ preventScroll: true });
        } catch (value) { const error = accessFailure(value); if (turn === generation) { if (error.retryAfter) codes[channel].until = Date.now() + error.retryAfter * 1000; fieldErrors(error); } }
        finally { if (turn === generation) { button.dataset.submitState = 'idle'; markBusy(false); } }
      })();
    }));
    [form, recovery, reset].forEach(owner => owner.addEventListener('input', event => {
      const target = event.target; if (!(target instanceof HTMLInputElement)) return;
      target.removeAttribute('aria-invalid'); target.closest('label, [data-contact-code]')?.querySelector('.access-field-error')?.remove();
      if (owner !== form) return;
      if (target.name === 'phone' && target.value.trim().startsWith('+')) {
        const region = phoneRegion(), allowed = region.selectable ? region.codes : [region.default_code];
        const value = phoneValue(), prefix = [...allowed].sort((a, b) => b.length - a.length).find(code => value.startsWith(code));
        if (prefix) { regionSelect.value = prefix; target.value = value.slice(prefix.length); }
      }
      if (isChannel(target.name)) { const channel = target.name; if (codes[channel].target !== contactValue(channel)) { codes[channel].challenge = ''; input(form, channel + '_code').value = ''; } }
      paintFields(); tickCodes();
    }));
    regionSelect.addEventListener('change', () => {
      if (codes.phone.target !== phoneValue()) { codes.phone.challenge = ''; input(form, 'phone_code').value = ''; }
      paintFields(); tickCodes();
    });
    $<HTMLSelectElement>('[data-code-login-channel]').addEventListener('change', () => { paintFields(); report(''); });
    function switchFlow(next: unknown) {
      if (!isFlow(next) || next === 'bind' || !policy || busy || uncertain || finished || token) return false;
      if (next === 'login' && policy.code_login_enabled === false) { report('本站已关闭验证码登录，请使用密码或快捷登录。', true); return false; }
      if (next === flow) return true;
      window.ChunnelVerification?.cancel(form); window.ChunnelVerification?.cancel(recovery);
      for (const channel of (['email', 'phone'] as const)) { codes[channel].challenge = ''; input(form, channel + '_code').value = ''; }
      [form, recovery, reset].forEach(clearErrors);
      flow = next; report('');
      if (!modal) { history.replaceState(null, '', location.pathname + (flow === 'register' ? '' : '?flow=' + flow)); window.ChunnelAccountNav?.(); }
      render(); animateFlow(); return true;
    }
    flows.forEach(button => button.addEventListener('click', () => switchFlow(button.dataset.accessFlow)));
    // Closing a dialog preserves in-flight or uncertain submissions. Reopening
    // reconciles that same request; switching identity while it is pending cannot.
    root.addEventListener('click', event => {
      if ((busy || uncertain) && event.target instanceof Element && event.target.closest('[data-account-login], .oauth-provider')) { event.preventDefault(); event.stopPropagation(); report('请先等待处理完成，或重试确认上次提交的结果。', true); }
    });
    if (modal) root.closest('dialog')?.addEventListener('close', () => { [form, recovery, reset].forEach(owner => window.ChunnelVerification?.cancel(owner)); });
    if (!modal) {
    window.addEventListener('hashchange', () => {
      if (!location.hash) return;
      generation++; controller?.abort(); [form, recovery, reset].forEach(owner => window.ChunnelVerification?.cancel(owner));
      uncertain = null; finished = false; busy = false; form.reset(); reset.reset(); pending = null; codes.email = {}; codes.phone = {};
      $('[data-access-success]').hidden = true; report(''); readLocation(); markBusy(false); if (!policy || flow === 'bind') loadConfig(); else render();
    });
    }
    window.addEventListener('pageshow', event => { if (event.persisted) { generation++; controller?.abort(); [form, recovery, reset].forEach(owner => window.ChunnelVerification?.cancel(owner)); markBusy(false); if (!policy) loadConfig(); else render(); } });
    window.addEventListener('pagehide', () => { controller?.abort(); });
    setInterval(tickCodes, 1000);
    readLocation();
    async function loadConfig() {
      const turn = generation;
      try {
        const data = parsePolicy(await api(''));
        if (turn !== generation) return;
        if (flow === 'bind') {
          const identity = parseBinding(await api('/pending'));
          if (turn !== generation) return;
          pending = identity; input(form, 'display_name').value = pending.display_name || '';
          for (const key of ['email', 'phone', 'username'] as const) input(form, key).value = pending[key] || '';
        }
        policy = data; configurePhone(); render();
      } catch (error) { if (turn === generation) { $('[data-access-intro]').textContent = '账号服务暂不可用，请刷新页面或重新授权。'; fieldErrors(error); } }
    }
    let loading: Promise<void> | null;
    const apiController = root.chunnelRegistration = {
      canLeave: () => !busy && !uncertain,
      async open(next = 'register') {
        if (!policy) { loading ||= loadConfig().finally(() => { loading = null; }); await loading; }
        switchFlow(next);
      },
    };
    loading = loadConfig().finally(() => { loading = null; });
    return apiController;
  }
  window.ChunnelRegistration = { mount };
  document.querySelectorAll<HTMLElement>('[data-account-access]').forEach(mount);
}
mountRegistration();
