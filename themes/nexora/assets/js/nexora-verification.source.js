/* Nexora verification presentation adapter. Adapted from NewPress core UI (Apache-2.0).
 * Uses only /api/v1/verification; provider secrets and verification decisions stay in core. */
(() => {
  'use strict';
  if (window.NexoraVerification) return;
  const scripts = new Map(), tasks = new Map();
  let readySequence = 0;
  const failure = message => Object.assign(new Error(message), { code: 'verification.cancelled' });
  const node = (tag, text, className) => { const el = document.createElement(tag); if (text) el.textContent = text; if (className) el.className = className; return el; };
  async function api(path, body, options = {}) {
    const controller = new AbortController(), timer = setTimeout(() => controller.abort(), 20000);
    try {
      const response = await fetch('/api/v1/verification/' + path, { credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
        ...(body ? { method: 'POST', headers: { 'Content-Type': 'application/json', ...(options.csrf ? { 'X-CSRF-Token': options.csrf } : {}) }, body: JSON.stringify(body) } : {}) });
      const data = await response.json();
      if (!response.ok) throw Object.assign(new Error(data.error?.message || '验证服务暂不可用'), { code: data.error?.code });
      return data;
    } catch (error) { if (controller.signal.aborted) throw new Error('验证请求超时，请重试'); throw error; }
    finally { clearTimeout(timer); }
  }
  function loadScript(url, waitForReady = false) {
    if (scripts.has(url)) return scripts.get(url);
    const promise = new Promise((resolve, reject) => {
      const script = document.createElement('script'), callback = waitForReady ? '__nexoraVerificationReady' + (++readySequence) : '';
      let settled = false;
      const complete = error => {
        if (settled) return; settled = true; clearTimeout(timer);
        if (callback) delete window[callback];
        if (error) { script.remove(); scripts.delete(url); reject(error); } else resolve();
      };
      const timer = setTimeout(() => complete(new Error('验证组件加载超时，请检查网络后重试')), 15000);
      if (callback) window[callback] = () => complete();
      script.src = url + (callback ? (url.includes('?') ? '&' : '?') + 'onload=' + callback : ''); script.async = true;
      script.onload = () => { if (!waitForReady) complete(); };
      script.onerror = () => complete(new Error('验证组件加载失败，请重试或联系管理员'));
      document.head.append(script);
    });
    scripts.set(url, promise); return promise;
  }
  function cancel(owner) { tasks.get(owner)?.cancel(); }
  async function getToken(owner, scene, options = {}) {
    if (tasks.has(owner)) return tasks.get(owner).promise;
    let resolve, reject, dispose = () => {}, generation = 0, finished = false;
    const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
    const box = node('section', '', 'verification-box'); box.setAttribute('aria-label', '人机验证');
    const heading = node('div', '', 'verification-heading'); heading.append(node('strong', '安全验证'), node('span'));
    const host = node('div', '', 'verification-widget'), status = node('p', '正在准备验证…', 'verification-status'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
    const actions = node('div', '', 'verification-actions'), retry = node('button', '重新加载', 'verification-button'), abort = node('button', '取消', 'verification-button');
    retry.type = abort.type = 'button'; retry.hidden = true; actions.append(retry, abort); box.append(heading, host, status, actions);
    const before = owner.querySelector('footer, button[type="submit"]');
    if (before) before.before(box); else owner.append(box);
    const finish = (error, token) => {
      if (finished) return; finished = true; generation++; dispose(); tasks.delete(owner); box.remove();
      if (error) reject(error); else resolve(token);
    };
    const task = { promise, cancel: () => finish(failure('验证已取消，输入内容已保留')) }; tasks.set(owner, task);
    abort.addEventListener('click', task.cancel);
    let config;
    const report = error => { status.textContent = error.message || '验证暂不可用，请重试'; status.dataset.error = 'true'; retry.hidden = false; };
    async function start() {
      const current = ++generation; dispose(); dispose = () => {}; host.replaceChildren(); retry.hidden = true; status.dataset.error = 'false'; status.textContent = '正在加载验证组件…';
      const alive = () => !finished && current === generation && owner.isConnected;
      let submitting = false, phaseTimer;
      const clearPhase = () => { clearTimeout(phaseTimer); phaseTimer = undefined; };
      const beginPhase = () => {
        clearPhase();
        phaseTimer = setTimeout(() => {
          if (!alive()) return;
          generation++; dispose(); host.replaceChildren();
          report(new Error('验证组件响应超时，请检查网络后重新加载'));
        }, 15000);
      };
      dispose = clearPhase;
      const verify = async proof => {
        if (!alive() || submitting) return; submitting = true; status.textContent = '正在确认验证结果…';
        try {
          const result = await api('verify', { scene, preview: options.preview, proof }, options);
          if (!alive()) return;
          if (options.preview ? result.preview !== true : (typeof result.token !== 'string' || !result.token)) throw new Error('验证回执无效，请重试');
          finish(null, options.preview ? 'preview-passed' : result.token);
        } catch (error) { if (alive()) report(error); } finally { submitting = false; }
      };
      const failed = () => { if (alive()) report(new Error('验证未完成或已过期，请重新加载')); };
      try {
        config = await api('config?scene=' + encodeURIComponent(scene) + (options.preview ? '&preview=' + encodeURIComponent(options.preview) : ''));
        if (!alive()) return;
        if (!config.enabled) { finish(null, ''); return; }
        heading.lastChild.textContent = { local: '本地图片验证', turnstile: 'Turnstile', geetest: '极验 4', tencent: '腾讯云验证码', hcaptcha: 'hCaptcha', recaptcha_v2: 'reCAPTCHA v2', recaptcha_v3: 'reCAPTCHA v3' }[config.provider] || '';
        const theme = config.theme === 'auto' ? 'light' : config.theme;
        const language = config.language === 'auto' ? document.documentElement.lang || 'zh-CN' : config.language;
        status.textContent = '请完成下方验证，完成后继续提交。';
        const compact = box.clientWidth < 330;
        if (config.provider === 'local') {
          const challenge = await api('challenge', { scene, preview: options.preview, proof: {} }, options);
          if (!alive()) return;
          const row = node('div', '', 'verification-local'), picture = node('img'); picture.src = challenge.image; picture.width = 190; picture.height = 60; picture.alt = '验证码图片，请输入图中的五位数字';
          const label = node('label', '图片中的数字'), input = node('input'); input.type = 'text'; input.inputMode = 'numeric'; input.autocomplete = 'off'; input.maxLength = 5; input.placeholder = '输入 5 位数字'; label.append(input);
          const confirm = node('button', '确认验证', 'verification-button verification-confirm'); confirm.type = 'button';
          const check = () => { if (!/^\d{5}$/.test(input.value.trim())) { report(new Error('请输入图片中的 5 位数字')); input.focus(); return; } return verify({ challenge_id: challenge.id, answer: input.value.trim() }); };
          confirm.addEventListener('click', check); input.addEventListener('keydown', event => { if (event.key === 'Enter' && !event.isComposing) { event.preventDefault(); event.stopPropagation(); check(); } });
          row.append(picture, label, confirm); host.append(row); retry.hidden = false; retry.textContent = '换一张'; input.focus({ preventScroll: true }); box.scrollIntoView({ block: 'nearest', behavior: 'instant' });
        } else if (config.provider === 'turnstile') {
          await loadScript('https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit', true); if (!alive()) return;
          const id = window.turnstile.render(host, { sitekey: config.site_key, action: scene, theme, language, size: 'flexible', retry: 'never', callback: token => verify({ token }), 'expired-callback': failed, 'error-callback': () => { failed(); return true; } });
          dispose = () => { try { window.turnstile.remove(id); } catch {} };
        } else if (config.provider === 'hcaptcha') {
          await loadScript('https://js.hcaptcha.com/1/api.js?render=explicit&hl=' + encodeURIComponent(language), true); if (!alive()) return;
          const id = window.hcaptcha.render(host, { sitekey: config.site_key, theme, size: compact ? 'compact' : 'normal', callback: token => verify({ token }), 'expired-callback': failed, 'error-callback': failed });
          dispose = () => { try { window.hcaptcha.remove(id); } catch {} };
        } else if (config.provider === 'geetest') {
          await loadScript('https://static.geetest.com/v4/gt4.js'); if (!alive()) return;
          beginPhase();
          window.initGeetest4({ captchaId: config.site_key, product: 'bind', language: language.startsWith('zh') ? 'zho' : 'eng' }, widget => {
            if (!alive()) { widget.destroy?.(); return; }
            dispose = () => { clearPhase(); widget.destroy?.(); };
            const open = node('button', '点击开始极验验证', 'verification-button'); open.type = 'button'; open.disabled = true; host.append(open);
            open.addEventListener('click', () => widget.showCaptcha());
            widget.onReady(() => { if (alive()) { clearPhase(); open.disabled = false; } }).onSuccess(() => { const proof = widget.getValidate(); if (proof) verify({ lot_number: proof.lot_number, captcha_output: proof.captcha_output, pass_token: proof.pass_token, gen_time: proof.gen_time }); }).onError(failed);
          });
        } else if (config.provider === 'tencent') {
          await loadScript('https://ssl.captcha.qq.com/TCaptcha.js'); if (!alive()) return;
          const widget = new window.TencentCaptcha(config.site_key, result => { if (result.ret === 0 && !String(result.ticket).startsWith('trerror')) verify({ token: result.ticket, randstr: result.randstr }); else failed(); }, { userLanguage: language.startsWith('zh') ? 'zh-cn' : 'en' });
          dispose = () => widget.destroy?.(); const open = node('button', '点击开始腾讯验证', 'verification-button'); open.type = 'button'; open.addEventListener('click', () => widget.show()); host.append(open);
        } else if (config.provider.startsWith('recaptcha_')) {
          const v3 = config.provider === 'recaptcha_v3', domain = config.recaptcha_net ? 'www.recaptcha.net' : 'www.google.com';
          await loadScript('https://' + domain + '/recaptcha/api.js?render=' + (v3 ? encodeURIComponent(config.site_key) : 'explicit') + '&hl=' + encodeURIComponent(language)); if (!alive()) return;
          beginPhase();
          await new Promise(done => window.grecaptcha.ready(done)); if (!alive()) return; clearPhase();
          if (v3) { beginPhase(); const token = await window.grecaptcha.execute(config.site_key, { action: scene }); if (alive()) { clearPhase(); await verify({ token }); } }
          else { const id = window.grecaptcha.render(host, { sitekey: config.site_key, theme, size: compact ? 'compact' : 'normal', callback: token => verify({ token }), 'expired-callback': failed, 'error-callback': failed }); dispose = () => { try { window.grecaptcha.reset(id); } catch {} }; }
        } else throw new Error('当前验证服务不受支持，请联系管理员');
      } catch (error) { clearPhase(); if (alive()) report(error); }
    }
    retry.addEventListener('click', start); start(); return promise;
  }
  window.NexoraVerification = { getToken, cancel };
  // Native HTML forms share the same proof exchange as JSON submissions.
  document.addEventListener('submit', async event => {
    const form = event.target;
    const scene = form.matches?.('[data-comment-form]') ? 'comment' : '';
    if (!scene) return;
    if (form.dataset.submitOnce === 'true') { event.preventDefault(); event.stopImmediatePropagation(); return; }
    if (form.dataset.verificationSubmitting === 'true') return;
    event.preventDefault(); event.stopImmediatePropagation();
    if (form.dataset.verificationPending === 'true') return;
    form.dataset.verificationPending = 'true';
    form.querySelector('[data-verification-error]')?.remove();
    const submitter = event.submitter;
    try {
      const token = await getToken(form, scene);
      let field = form.querySelector('input[name="verification_token"]'); if (!field) { field = node('input'); field.type = 'hidden'; field.name = 'verification_token'; form.append(field); } field.value = token;
      form.dataset.verificationSubmitting = 'true';
      if (submitter?.isConnected && !submitter.disabled) form.requestSubmit(submitter); else form.requestSubmit();
      delete form.dataset.verificationSubmitting;
    } catch (error) { let message = form.querySelector('[data-verification-error]'); if (!message) { message = node('p', '', 'form-error'); message.dataset.verificationError = ''; message.setAttribute('role', 'alert'); form.append(message); } message.textContent = error.message || '验证暂不可用，请重试'; }
    finally { delete form.dataset.verificationPending; }
  }, true);
  window.addEventListener('pagehide', () => { for (const task of tasks.values()) task.cancel(); });
})();
