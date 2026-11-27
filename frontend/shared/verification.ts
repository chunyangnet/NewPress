import {record} from './collection-data';
import {verificationConfig,verificationChallenge,verificationReceipt} from './form-data';
import {failure as asFailure} from './collection-runtime';
import type {VerificationOptions,VerificationProof,VerificationTask} from './verification-types';
import type {} from './account-types';
(() => {
  'use strict';
  if (window.ChunnelVerification) return;
  const scripts = new Map<string,Promise<void>>(), tasks = new Map<HTMLElement,VerificationTask & {signature:string}>();
  const observer=new MutationObserver(()=>{for(const [owner,task]of tasks)if(!owner.isConnected)task.cancel();});
  let readySequence = 0;
  const failure = (message:string) => Object.assign(new Error(message), { code: 'verification.cancelled' });
  const node = <K extends keyof HTMLElementTagNameMap>(tag:K, text?:string, className?:string):HTMLElementTagNameMap[K] => { const el = document.createElement(tag); if (text) el.textContent = text; if (className) el.className = className; return el; };
  async function api(path:string, body?:{scene:string;preview?:string;proof:VerificationProof}, options:VerificationOptions & {signal?:AbortSignal} = {}):Promise<unknown> {
    const controller = new AbortController(), abort=()=>controller.abort();let timedOut=false;
    const timer = setTimeout(() => {timedOut=true;abort();}, 20000);
    options.signal?.addEventListener('abort',abort,{once:true});if(options.signal?.aborted)abort();
    try {
      const response = await fetch('/api/v1/verification/' + path, { credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
        ...(body ? { method: 'POST', headers: { 'Content-Type': 'application/json', ...(options.csrf ? { 'X-CSRF-Token': options.csrf } : {}) }, body: JSON.stringify(body) } : {}) });
      const data=record(await response.json());
      if(controller.signal.aborted)throw new DOMException('验证已取消','AbortError');
      if (!response.ok){const detail=record(data.error);throw Object.assign(new Error(typeof detail.message==='string'?detail.message:'验证服务暂不可用'),{code:detail.code});}
      return data;
    } catch (error) { if (timedOut) throw new Error('验证请求超时，请重试'); throw error; }
    finally { clearTimeout(timer);options.signal?.removeEventListener('abort',abort); }
  }
  function loadScript(url:string, waitForReady = false) {
    if (scripts.has(url)) return scripts.get(url);
    const promise = new Promise<void>((resolve, reject) => {
      const script = document.createElement('script'), callback: `__chunnelVerificationReady${number}`|'' = waitForReady ? `__chunnelVerificationReady${++readySequence}` : '';
      let settled = false;
      const complete = (error?:Error) => {
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
  function cancel(owner:HTMLElement) { tasks.get(owner)?.cancel(); }
  async function getToken(owner:HTMLElement, scene:string, options:VerificationOptions = {}):Promise<string> {
    if(!owner.isConnected)throw failure('验证页面已离开，输入内容已保留');
    options={preview:options.preview,csrf:options.csrf};
    const signature=JSON.stringify({scene,...options}), existing=tasks.get(owner);if(existing){if(existing.signature===signature)return existing.promise;existing.cancel();}
    let resolve:(token:string)=>void=()=>{}, reject:(error:Error)=>void=()=>{}, dispose = () => {}, generation = 0, finished = false;
    let requestController:AbortController|undefined;
    const cleanup=()=>{try{dispose();}catch{/* Provider cleanup cannot strand a cancelled task. */}dispose=()=>{};};
    const promise = new Promise<string>((yes, no) => { resolve = yes; reject = no; });
    const box = node('section', '', 'verification-box'); box.setAttribute('aria-label', '人机验证');
    const heading = node('div', '', 'verification-heading'),providerLabel=node('span');heading.append(node('strong', '安全验证'),providerLabel);
    const host = node('div', '', 'verification-widget'), status = node('p', '正在准备验证…', 'verification-status'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
    const actions = node('div', '', 'verification-actions'), retry = node('button', '重新加载', 'ui-button ui-button-quiet ui-button-small'), abort = node('button', '取消', 'ui-button ui-button-quiet ui-button-small');
    retry.type = abort.type = 'button'; retry.hidden = true; actions.append(retry, abort); box.append(heading, host, status, actions);
    const before = owner.querySelector('footer, button[type="submit"]');
    if (before) before.before(box); else owner.append(box);
    const finish = (error:Error|null, token='') => {
      if (finished) return; finished = true; generation++;requestController?.abort();cleanup();tasks.delete(owner);box.remove();if(!tasks.size)observer.disconnect();
      if (error) reject(error); else resolve(token);
    };
    const task = {promise, signature, cancel: () => finish(failure('验证已取消，输入内容已保留')) }; tasks.set(owner, task);observer.observe(document.body,{childList:true,subtree:true});
    abort.addEventListener('click', task.cancel);
    const report = (problem:unknown) => { const error=asFailure(problem);status.textContent = error.message || '验证暂不可用，请重试'; status.dataset.error = 'true'; retry.hidden = false; };
    async function start() {
      const current = ++generation;requestController?.abort();requestController=new AbortController();const requestOptions={...options,signal:requestController.signal};cleanup(); host.replaceChildren(); retry.hidden = true; status.dataset.error = 'false'; status.textContent = '正在加载验证组件…';
      const alive = () => !finished && current === generation && owner.isConnected;
      let submitting = false, phaseTimer:ReturnType<typeof setTimeout>|undefined;
      const clearPhase = () => { clearTimeout(phaseTimer); phaseTimer = undefined; };
      const beginPhase = () => {
        clearPhase();
        phaseTimer = setTimeout(() => {
          if (!alive()) return;
          generation++;requestController?.abort();cleanup();host.replaceChildren();
          report(new Error('验证组件响应超时，请检查网络后重新加载'));
        }, 15000);
      };
      dispose = clearPhase;
      const verify = async (proof:VerificationProof) => {
        if (!alive() || submitting) return; submitting = true; status.textContent = '正在确认验证结果…';
        try {
          const result = await api('verify', { scene, preview: options.preview, proof }, requestOptions);
          if (!alive()) return;
          finish(null, verificationReceipt(result,!!options.preview));
        } catch (error) { if (alive()) report(error); } finally { submitting = false; }
      };
      const failed = () => { if (alive()) report(new Error('验证未完成或已过期，请重新加载')); };
      try {
        const config = verificationConfig(await api('config?scene=' + encodeURIComponent(scene) + (options.preview ? '&preview=' + encodeURIComponent(options.preview) : ''),undefined,requestOptions),scene);
        if (!alive()) return;
        if (!config.enabled) { finish(null, ''); return; }
        providerLabel.textContent = { local: '本地图片验证', turnstile: 'Turnstile', geetest: '极验 4', tencent: '腾讯云验证码', hcaptcha: 'hCaptcha', recaptcha_v2: 'reCAPTCHA v2', recaptcha_v3: 'reCAPTCHA v3' }[config.provider];
        const theme = config.theme === 'auto' ? (document.documentElement.dataset.theme || (window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')) : config.theme;
        const language = config.language === 'auto' ? document.documentElement.lang || 'zh-CN' : config.language;
        status.textContent = '请完成下方验证，完成后继续提交。';
        const compact = box.clientWidth < 330;
        if (config.provider === 'local') {
          const challenge = verificationChallenge(await api('challenge', { scene, preview: options.preview, proof: {} }, requestOptions));
          if (!alive()) return;
          const row = node('div', '', 'verification-local'), picture = node('img'); picture.src = challenge.image; picture.width = 190; picture.height = 60; picture.alt = '验证码图片，请输入图中的五位数字';
          const label = node('label', '图片中的数字'), input = node('input'); input.type = 'text'; input.inputMode = 'numeric'; input.autocomplete = 'off'; input.maxLength = 5; input.placeholder = '输入 5 位数字'; label.append(input);
          const confirm = node('button', '确认验证', 'ui-button ui-button-primary ui-button-small'); confirm.type = 'button';
          const check = () => { if (!/^\d{5}$/.test(input.value.trim())) { report(new Error('请输入图片中的 5 位数字')); input.focus(); return; } return verify({ challenge_id: challenge.id, answer: input.value.trim() }); };
          confirm.addEventListener('click', check); input.addEventListener('keydown', event => { if (event.key === 'Enter' && !event.isComposing && event.keyCode!==229) { event.preventDefault(); event.stopPropagation(); check(); } });
          row.append(picture, label, confirm); host.append(row); retry.hidden = false; retry.textContent = '换一张'; input.focus({ preventScroll: true }); box.scrollIntoView({ block: 'nearest', behavior: 'instant' });
        } else if (config.provider === 'turnstile') {
          await loadScript('https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit', true); if (!alive()) return;
          const sdk=window.turnstile;if(!sdk)throw new Error('验证组件尚未就绪');
          const id = sdk.render(host, { sitekey: config.site_key, action: scene, theme, language, size: 'flexible', retry: 'never', callback: token => verify({ token }), 'expired-callback': failed, 'error-callback': () => { failed(); return true; } });
          dispose = () => { try { sdk.remove(id); } catch {} };
        } else if (config.provider === 'hcaptcha') {
          await loadScript('https://js.hcaptcha.com/1/api.js?render=explicit&hl=' + encodeURIComponent(language), true); if (!alive()) return;
          const sdk=window.hcaptcha;if(!sdk)throw new Error('验证组件尚未就绪');
          const id = sdk.render(host, { sitekey: config.site_key, theme, size: compact ? 'compact' : 'normal', callback: token => verify({ token }), 'expired-callback': failed, 'error-callback': failed });
          dispose = () => { try { sdk.remove(id); } catch {} };
        } else if (config.provider === 'geetest') {
          await loadScript('https://static.geetest.com/v4/gt4.js'); if (!alive()) return;
          beginPhase();
          const initialize=window.initGeetest4;if(!initialize)throw new Error('验证组件尚未就绪');
          initialize({ captchaId: config.site_key, product: 'bind', language: language.startsWith('zh') ? 'zho' : 'eng' }, widget => {
            if (!alive()) { try { widget.destroy?.(); } catch { /* A late SDK callback cannot revive its cancelled owner. */ } return; }
            dispose = () => { clearPhase(); widget.destroy?.(); };
            const open = node('button', '点击开始极验验证', 'ui-button ui-button-outline'); open.type = 'button'; open.disabled = true; host.append(open);
            open.addEventListener('click', () => widget.showCaptcha());
            widget.onReady(() => { if (alive()) { clearPhase(); open.disabled = false; } }).onSuccess(() => { const proof = widget.getValidate(); if (proof) verify({ lot_number: proof.lot_number, captcha_output: proof.captcha_output, pass_token: proof.pass_token, gen_time: proof.gen_time }); }).onError(failed);
          });
        } else if (config.provider === 'tencent') {
          await loadScript('https://ssl.captcha.qq.com/TCaptcha.js'); if (!alive()) return;
          const Captcha=window.TencentCaptcha;if(!Captcha)throw new Error('验证组件尚未就绪');
          const widget = new Captcha(config.site_key, result => { if (result.ret === 0 && !String(result.ticket).startsWith('trerror')) verify({ token: result.ticket, randstr: result.randstr }); else failed(); }, { userLanguage: language.startsWith('zh') ? 'zh-cn' : 'en' });
          dispose = () => widget.destroy?.(); const open = node('button', '点击开始腾讯验证', 'ui-button ui-button-outline'); open.type = 'button'; open.addEventListener('click', () => widget.show()); host.append(open);
        } else if (config.provider.startsWith('recaptcha_')) {
          const v3 = config.provider === 'recaptcha_v3', domain = config.recaptcha_net ? 'www.recaptcha.net' : 'www.google.com';
          await loadScript('https://' + domain + '/recaptcha/api.js?render=' + (v3 ? encodeURIComponent(config.site_key) : 'explicit') + '&hl=' + encodeURIComponent(language)); if (!alive()) return;
          beginPhase();
          const sdk=window.grecaptcha;if(!sdk)throw new Error('验证组件尚未就绪');
          await new Promise<void>(done => sdk.ready(done)); if (!alive()) return; clearPhase();
          if (v3) { beginPhase(); const token = await sdk.execute(config.site_key, { action: scene }); if (alive()) { clearPhase(); await verify({ token }); } }
          else { const id = sdk.render(host, { sitekey: config.site_key, theme, size: compact ? 'compact' : 'normal', callback: token => verify({ token }), 'expired-callback': failed, 'error-callback': failed }); dispose = () => { try { sdk.reset(id); } catch {} }; }
        } else throw new Error('当前验证服务不受支持，请联系管理员');
      } catch (error) { clearPhase(); if (alive()) report(error); }
    }
    retry.addEventListener('click', start); start(); return promise;
  }
  window.ChunnelVerification = { getToken, cancel };
  // Native HTML forms share the same proof exchange as JSON submissions.
  document.addEventListener('submit', async event => {
    const form = event.target;if(!(form instanceof HTMLFormElement))return;
    const scene = form.matches?.('.admin-login-form') ? 'admin_login' : form.matches?.('[data-comment-form]:not([data-async-ready])') ? 'comment' : '';
    if (!scene) return;
    if (form.dataset.submitOnce === 'true') { event.preventDefault(); event.stopImmediatePropagation(); return; }
    if (form.dataset.verificationSubmitting === 'true') return;
    event.preventDefault(); event.stopImmediatePropagation();
    if (form.dataset.verificationPending === 'true') return;
    form.dataset.verificationPending = 'true';
    const submitter = event.submitter;
    try {
      const token = await getToken(form, scene);
      if(!form.isConnected)throw failure('验证页面已离开，输入内容已保留');
      let field = form.querySelector<HTMLInputElement>('input[name="verification_token"]'); if (!field) { field = node('input'); field.type = 'hidden'; field.name = 'verification_token'; form.append(field); } field.value = token;
      form.dataset.verificationSubmitting = 'true';
      try {
        if (submitter?.isConnected && (submitter instanceof HTMLButtonElement || submitter instanceof HTMLInputElement) && submitter.form===form && !submitter.disabled && ['submit','image'].includes(submitter.type)) form.requestSubmit(submitter); else form.requestSubmit();
      } finally { delete form.dataset.verificationSubmitting; }
    } catch (error) { window.Chunnel?.showToast?.(asFailure(error).message); }
    finally { delete form.dataset.verificationPending; }
  }, true);
  const cancelAll=()=>{for(const task of tasks.values())task.cancel();};
  window.addEventListener('pagehide',cancelAll);
  window.addEventListener('pageshow',event=>{if(event.persisted)cancelAll();});
})();
