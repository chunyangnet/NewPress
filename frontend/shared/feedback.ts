import type {Savebar} from '../core/admin-types';
import {required,field,checked,pageRequests,failure} from './collection-runtime';
import {feedbackReceipt} from './form-data';
import type {FeedbackCommand,FeedbackGrant} from './form-data';
(() => {
  'use strict';
  const root = document.querySelector<HTMLElement>('[data-feedback-page], [data-feature-admin]');
  if (!root) return;
  const admin = root.hasAttribute('data-feature-admin');
  const status = root.querySelector<HTMLElement>('[data-feedback-status]');
  const conflict = root.querySelector<HTMLElement>('[data-feedback-conflict]');
  const attempts = new WeakMap<HTMLElement,{signature:string;key:string;version:number}>();
  const dirtyForms = new Set<HTMLElement>();
  let settingsController:Savebar|null|undefined;
  let busy = false, dirty = false, leaving = false, sessionChanged = false;
  const notice = (text:string, node?:HTMLElement) => {
    if (settingsController && node?.matches('form')) { settingsController.message(text); return; }
    if (status) { status.hidden = false; status.textContent = text; }
    const local = node?.querySelector('[data-form-status]');
    if (local) local.textContent = text;
  };
  const requests=pageRequests(Number(root.dataset.userId),()=>{sessionChanged=true;root.querySelectorAll<HTMLButtonElement|HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement>('button,input,select,textarea').forEach(control=>control.disabled=true);root.querySelectorAll<HTMLFormElement>('[data-feedback-form]').forEach(form=>window.ChunnelVerification?.cancel(form));notice('登录账号已变化。请先复制并保留输入，再刷新页面继续。');});
  const preview = () => {
    const form = root.querySelector<HTMLFormElement>('[data-feedback-form]');
    if (!admin || !form) return;
    const value=(name:string)=>field(form,name), check=(name:string)=>checked(form,name);
    const update = (selector:string, text:string) => { const element = root.querySelector(selector); if (element) element.textContent = text; };
    update('[data-live-name]', value('name').value || '页面名称');
    update('[data-live-description]', value('description').value || '写一段简短的介绍。');
    update('[data-live-submit]', check('submissions') ? value('submit_label').value || '提交反馈' : '暂停提交');
    update('[data-preview-slug]', value('slug').value || 'feedback');
    update('[data-live-features]', check('enabled') ? ([['voting', '投票'], ['comments', '讨论'], ['roadmap', '进度看板'], ['moderation', '先审后发'], ['private_submissions', '私密反馈']] as const).filter(([key]) => check(key)).map(([, label]) => label).join(' · ') || '仅展示反馈' : '已停用 · 仅管理员预览');
    required(root,'.feature-preview-paper').dataset.accent = value('accent').value;
    required<HTMLOptionElement>(form,'[name=default_view] [value="roadmap"]').disabled = !check('roadmap');
    if (!check('roadmap')) value('default_view').value = 'list';
    required<HTMLInputElement>(form,'[name=slug]').setCustomValidity(value('slug').value === 'new' ? '请使用其他访问别名，new 为保留名称。' : '');
    const kinds = [...form.querySelectorAll<HTMLInputElement>('[name="kinds"]')];
    kinds[0]?.setCustomValidity(kinds.some(input => input.checked) ? '' : '请至少选择一种反馈类型。');
  };
  const commandFor = (node:HTMLElement) => {
    const form = node instanceof HTMLFormElement ? node : null;
    const action = (form ? form.dataset.feedbackForm : node.dataset.feedbackAction)||'';
    const cmd:FeedbackCommand = { action, version: action === 'create' ? 0 : Number(root.dataset.version) };
    let slug = root.dataset.slug||'';
    if (action === 'configure' || action === 'create') {
      if(!form)throw new Error('缺少反馈设置表单');
      const f=(name:string)=>field(form,name);
      slug = f('slug').value.trim();
      cmd.settings = {};
      for (const key of ['name', 'description', 'guidelines', 'submit_label', 'default_view', 'accent']) cmd.settings[key] = f(key).value.trim();
      for (const key of ['enabled', 'submissions', 'voting', 'comments', 'official_replies', 'roadmap', 'history', 'moderation', 'private_submissions']) cmd.settings[key] = checked(form,key);
      cmd.settings.page_size = Number(f('page_size').value);
      cmd.settings.kinds = [...form.querySelectorAll<HTMLInputElement>('[name="kinds"]:checked')].map(input => input.value);
      cmd.settings.categories = f('categories').value.split(/\r?\n/).map(value => value.trim()).filter(Boolean);
      cmd.settings.permissions = [...form.querySelectorAll<HTMLElement>('[data-grant-role]')].map(row => {
        const grant:FeedbackGrant = {role:row.dataset.grantRole||'',read:false,submit:false,vote:false,comment:false,moderate:false};
        for (const key of ['read', 'submit', 'vote', 'comment', 'moderate'] as const) grant[key] = row.querySelector<HTMLInputElement>(`[data-grant="${key}"]`)?.checked || false;
        return grant;
      });
    } else if (form) {
      for (const [key, value] of new FormData(form)) cmd[key] = key === 'private' ? true : String(value).trim();
    }
    if (node.dataset.item) cmd.item_id = Number(node.dataset.item);
    if (node.dataset.reply) cmd.reply_id = Number(node.dataset.reply);
    if (node.dataset.enabled !== undefined) cmd.enabled = node.dataset.enabled === 'true';
    if (node.dataset.official === 'true') cmd.official = true;
    return { slug, cmd };
  };
  const execute = async (node:HTMLElement) => {
    if (busy || sessionChanged || node.matches(':disabled')) return;
    const { slug, cmd } = commandFor(node);
    const signature = JSON.stringify({ slug, cmd: { ...cmd, version: 0 } });
    let attempt = attempts.get(node);
    if (!attempt || attempt.signature !== signature) { attempt = { signature, key: crypto.randomUUID(), version: cmd.version }; attempts.set(node, attempt); }
    cmd.version = attempt.version;
    cmd.key = attempt.key;
    busy = true;
    if (settingsController && node.matches('form') && !settingsController.start()) {busy=false;return;}
    root.setAttribute('aria-busy', 'true');
    const controls = [...root.querySelectorAll<HTMLButtonElement|HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement>('button,input,select,textarea')].map(control => ({ control, disabled: control.disabled }));
    controls.forEach(({ control }) => { control.disabled = true; });
    if (conflict) conflict.hidden = true;
    notice('正在保存…', node);
    try {
      if (window.ChunnelVerification && (cmd.action === 'submit' || cmd.action === 'reply')) cmd.verification_token = await window.ChunnelVerification.getToken(node, 'feedback');
      if (sessionChanged) throw new Error('登录账号已变化。当前输入已保留，请刷新后重试。');
      const response = await requests.request('/api/v1/feedback/' + encodeURIComponent(slug), {
        method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': root.dataset.csrf || '' }, body: JSON.stringify(cmd),
      });
      const result = feedbackReceipt(response,slug,cmd);
      if (sessionChanged) throw new Error('登录账号已变化。当前输入已保留，请复制后刷新并核对保存结果。');
      // An unchanged submitted form can stay on screen while another form is
      // dirty. Keep its identity so clicking again cannot append a duplicate.
      if(['vote','configure','create'].includes(cmd.action))attempts.delete(node);
      root.dataset.version=String(Math.max(Number(root.dataset.version)||0,result.version));
      if (admin && ['configure', 'create'].includes(cmd.action)) {
        dirtyForms.delete(node); dirty = dirtyForms.size > 0;
        settingsController?.success();
        if (cmd.action === 'configure') return;
      }
      if (cmd.action === 'vote') {
        const voted = result.voted;
        const count = result.vote_count;
        node.dataset.enabled = String(!voted); node.setAttribute('aria-pressed', String(voted)); node.classList.toggle('is-voted', voted);
        required(node,'b').textContent = String(count); required(node,'small').textContent = voted ? '已支持' : '支持';
        const title = (node.getAttribute('aria-label')||'').split('：').slice(1).join('：');
        node.setAttribute('aria-label', (voted ? '撤回对' : '支持') + '：' + title);
        notice(voted ? '已支持。再次点击可撤回。' : '已撤回支持。', node);
      } else {
        notice('已保存，正在打开最新内容…', node);
        let target = admin ? '/admin/features/?edit=' + encodeURIComponent(slug) : location.pathname + location.search;
        if (cmd.action === 'submit' || cmd.action === 'reply') {
          target = '/feedback/' + encodeURIComponent(slug) + '/items/' + result.item_id;
          if (cmd.action === 'reply') target += '?page=' + result.reply_page + '#reply-' + result.reply_id;
        }
        dirtyForms.delete(node);
        if (dirtyForms.size) {
          notice('当前操作已保存。另一处表单仍有未提交的输入，请先保留，再打开最新内容。', node);
          if (conflict) {
            conflict.hidden = false;
            required(conflict,'strong').textContent = '已保存，其他输入仍保留在此页';
            required<HTMLAnchorElement>(conflict,'[data-feedback-latest]').href = target;
          }
        } else { leaving = true; location.assign(target); }
      }
    } catch (problem) {
      if(sessionChanged)return;const error=failure(problem);
      notice(error.name === 'TimeoutError' ? '响应超时。输入已保留，原样重试会识别同一请求。' : error.message, node);
      if (settingsController && node.matches('form')) settingsController.fail(error.message);
      if (error.status === 409 && conflict) {
        conflict.hidden = false;
        required<HTMLAnchorElement>(conflict,'[data-feedback-latest]').href = admin && cmd.action === 'create' ? '/admin/features/?edit=' + encodeURIComponent(slug) : location.pathname + location.search;
      }
    } finally {
      busy = false; root.removeAttribute('aria-busy');
      if(!sessionChanged){controls.forEach(({ control, disabled }) => { control.disabled = disabled; });settingsController?.refresh(true);}
    }
  };
  const markForm = (form:HTMLElement|null) => { if (!form) return; if (settingsController && !settingsController.dirty()) dirtyForms.delete(form); else dirtyForms.add(form); dirty = dirtyForms.size > 0; };
  root.addEventListener('input', event => { if(!(event.target instanceof Element))return;preview(); markForm(event.target.closest<HTMLFormElement>('[data-feedback-form]')); settingsController?.refresh(); });
  root.addEventListener('change', event => {
    if(!(event.target instanceof Element))return;
    const form = event.target.closest<HTMLFormElement>('[data-feedback-form]');
    const field = event.target.closest<HTMLInputElement>('[data-grant]');
    if (field) {
      const row = field.closest<HTMLElement>('[data-grant-role]');if(!row)return;
      if (field.dataset.grant === 'read' && !field.checked) row.querySelectorAll<HTMLInputElement>('[data-grant]').forEach(input => { input.checked = false; });
      else if (field.checked) required<HTMLInputElement>(row,'[data-grant="read"]').checked = true;
    }
    preview();
    markForm(form); settingsController?.refresh();
  });
  root.addEventListener('submit', event => {
    if(!(event.target instanceof Element))return;
    const form = event.target.closest<HTMLFormElement>('[data-feedback-form]'); if (!form) return;
    event.preventDefault(); preview(); if (form.reportValidity()) execute(form);
  });
  root.addEventListener('click', event => {
    if(!(event.target instanceof Element))return;
    const action = event.target.closest<HTMLElement>('[data-feedback-action]'); if (action) execute(action);
    if (event.target.closest('[data-feedback-compose]')) {
      const details = root.querySelector<HTMLDetailsElement>('#feedback-compose');
      if (details) { details.open = true; details.querySelector<HTMLInputElement>('[name="title"]')?.focus({ preventScroll: true }); }
    }
  });
  document.addEventListener('chunnel:session', event => {
    const session = event.detail;
    if (!session) return;
    const id = session.authenticated ? Number(session.id) : 0;
    if (id === Number(root.dataset.userId)) { if (session.csrf_token) root.dataset.csrf = session.csrf_token; return; }
    sessionChanged = true;
    if (!dirty && !busy) { leaving = true; location.reload(); return; }
    notice('登录账号已变化。请先复制并保留输入，再刷新页面继续。');
  });
  window.addEventListener('beforeunload', event => { if ((dirty || busy) && !leaving) { event.preventDefault(); event.returnValue = ''; } });
  preview();
  if (admin) {
    const form = root.querySelector<HTMLFormElement>('[data-feedback-form]');
    settingsController = window.ChunnelAdminUI?.savebar(form, { allowClean: form?.dataset.feedbackForm === 'create', onDiscard: () => { if(form)dirtyForms.delete(form); dirty = dirtyForms.size > 0; preview(); } });
  }
})();
