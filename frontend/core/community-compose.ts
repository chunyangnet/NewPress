import { composeInputs, composeSnapshot, composeDraft, composeEqual } from '../shared/community-draft';
import type { ComposeValues } from '../shared/community-draft';
import { record } from '../shared/ledger-data';

const root = document.querySelector<HTMLElement>('[data-questions], [data-circle-page]');
if (root) mount(root);
function mount(root: HTMLElement): void {
  if (root.dataset.communityComposeReady === 'true') return;
  root.dataset.communityComposeReady = 'true';
  const circle = root.hasAttribute('data-circle-page'), owner = Number(root.dataset.userId);
  const button = (label: string, action: () => void): HTMLButtonElement => {
    const node = document.createElement('button'); node.type = 'button'; node.className = 'ui-button'; node.textContent = label; node.addEventListener('click', action); return node;
  };
  const filters = root.querySelector<HTMLFormElement>('.circle-topic-filters, .qa-filters');
  if (filters) {
    const existing = !circle ? root.querySelector<HTMLElement>('.channel-tabs[aria-label="问题状态"]') : null, nav = existing || document.createElement('nav');
    if (!existing) { nav.className = 'community-quick-filters'; nav.setAttribute('aria-label', circle ? '常用话题筛选' : '常用问题筛选'); }
    const key = circle ? 'filter' : 'status';
    const choices: [string, string][] = circle ? [['', '全部话题'], ['featured', '精华'], ['unanswered', '待解决'], ...(owner > 0 ? [['mine', '我发布的'], ['participated', '我回应的']] as [string, string][] : [])] : [['', '全部'], ['unanswered', '还没有回答'], ['open', '待解决'], ['solved', '已解决']];
    for (const [value, label] of choices) {
      const url = new URL(location.href); url.searchParams.delete('page'); url.hash = '';
      if (value) url.searchParams.set(key, value); else url.searchParams.delete(key);
      const link = existing && [...nav.querySelectorAll<HTMLAnchorElement>('a[href]')].find(item => new URL(item.href).origin === location.origin && (new URL(item.href).searchParams.get(key) || '') === value) || document.createElement('a');
      link.href = url.pathname + url.search; if (!link.textContent) link.textContent = label;
      if ((new URL(location.href).searchParams.get(key) || '') === value) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current');
      nav.append(link);
    }
    if (!existing) filters.before(nav);
    const select = filters.elements.namedItem('filter');
    if (circle && select instanceof HTMLSelectElement && owner > 0) for (const [value, label] of [['mine', '我发布的'], ['participated', '我回应的']] as const) {
      if (![...select.options].some(option => option.value === value)) select.add(new Option(label, value));
      if (new URL(location.href).searchParams.get(key) === value) select.value = value;
    }
  }
  if (!Number.isSafeInteger(owner) || owner <= 0) return;
  let active = true, suspended = false;
  const current = () => active && !suspended && root.isConnected;
  const controllers: (() => void)[] = [];
  document.addEventListener('chunnel:session', event => {
    if ((event.detail?.authenticated ? event.detail.id : 0) !== owner) { active = false; controllers.forEach(reset => reset()); }
  });
  window.addEventListener('pagehide', () => { suspended = true; });
  window.addEventListener('pageshow', async event => {
    if (!event.persisted) return;
    suspended = true;
    try {
      const response = await fetch('/api/v1/social/session', { credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: AbortSignal.timeout(12000) });
      const session = record(await response.json());
      if (!response.ok || session.authenticated !== true || session.id !== owner) { active = false; controllers.forEach(reset => reset()); return; }
      suspended = false;
    } catch { /* Keep private draft operations suspended until identity is known. */ }
  });
  for (const form of root.querySelectorAll<HTMLFormElement>('form[data-qa-command], form[data-circle-command]')) {
    const action = form.dataset.qaCommand || form.dataset.circleCommand || '';
    if (!['create', 'answer', 'topic', 'reply', 'edit', 'edit_answer', 'edit_topic', 'edit_reply'].includes(action) || form.dataset.composeReady === 'true') continue;
    const body = form.elements.namedItem('body'); if (!(body instanceof HTMLTextAreaElement || body instanceof HTMLInputElement)) continue;
    form.dataset.composeReady = 'true';
    const fields = composeInputs(form), initial = composeSnapshot(form);
    const key = ['newpress-compose-v1', owner, location.pathname, action, form.dataset.answer || form.dataset.topic || '', form.dataset.reply || ''].join(':');
    const tools = document.createElement('div'); tools.className = 'community-compose-tools';
    const note = document.createElement('span'); note.setAttribute('role', 'status'); note.textContent = '草稿自动保留在当前标签页，刷新后可恢复。';
    const count = document.createElement('small');
    const updateCount = () => { count.textContent = `${body.value.length} / ${body.maxLength > 0 ? body.maxLength : '不限'} 字符`; };
    let saved: ComposeValues | undefined, invalid = false, composing = false;
    try { const raw = sessionStorage.getItem(key); if (raw !== null) saved = composeDraft(JSON.parse(raw), fields); } catch { invalid = true; note.textContent = '草稿无法读取；请保留当前输入，核对后可删除旧草稿。'; }
    const restore = button('恢复草稿', () => {
      if (!current() || composing || !saved || (fields.some(field => field.value !== initial[field.name as keyof ComposeValues] && field.value.trim()) && !confirm('恢复草稿将替换当前输入，继续吗？'))) return;
      for (const field of fields) { const value = saved[field.name as keyof ComposeValues]; if (value !== undefined) field.value = value; }
      restore.hidden = true; body.dispatchEvent(new Event('input', { bubbles: true })); note.textContent = '草稿已恢复，请核对正文和附件后发布。'; body.focus();
    });
    restore.hidden = !saved;
    const discard = button('删除草稿', () => {
      if (!current()) return;
      try { sessionStorage.removeItem(key); saved = undefined; invalid = false; restore.hidden = true; note.textContent = '已删除保存的草稿，当前输入仍保留。'; } catch { note.textContent = '无法访问草稿存储。'; }
    });
    const save = () => {
      updateCount(); if (!current() || composing) return;
      if (!restore.hidden || invalid) { note.textContent = '有一份待处理草稿，请先恢复或删除；当前输入尚未保存。'; return; }
      try { sessionStorage.setItem(key, JSON.stringify(composeSnapshot(form))); note.textContent = '草稿已保存到当前标签页。'; } catch { note.textContent = '草稿暂时无法保存，请保留当前页面。'; }
    };
    form.addEventListener('compositionstart', () => { composing = true; });
    form.addEventListener('compositionend', () => { composing = false; save(); });
    form.addEventListener('input', save); form.addEventListener('change', save);
    form.addEventListener('community:saved', event => {
      if (!current() || !(event instanceof CustomEvent)) return;
      try {
        const detail = record(event.detail); if (detail.owner !== owner) return;
        const submitted = composeDraft(detail.values, fields), now = composeSnapshot(form);
        if (invalid || composing || !composeEqual(submitted, now) || (!restore.hidden && saved && !composeEqual(submitted, saved))) {
          note.textContent = '提交时的内容已保存，当前输入或待恢复草稿仍保留。'; if (restore.hidden && !invalid) save(); return;
        }
        sessionStorage.removeItem(key); saved = undefined; restore.hidden = true; invalid = false; note.textContent = '本次内容已发布，已清理对应草稿。';
      } catch { note.textContent = '草稿清理未确认，当前内容仍保留。'; }
    });
    controllers.push(() => { saved = undefined; tools.hidden = true; for (const field of fields) field.value = ''; });
    tools.append(restore, discard, count, note); (body.closest('label') || body).after(tools); updateCount();
    if (['create', 'topic', 'answer', 'reply'].includes(action)) {
      const guide = document.createElement('details'); guide.className = 'community-writing-guide';
      const summary = document.createElement('summary'); summary.textContent = '写作辅助 · 模板与格式'; guide.append(summary);
      for (const [label, text] of [['问题模板', '## 背景与目标\n\n\n## 遇到的问题\n\n\n## 已尝试的方法\n\n'], ['经验分享', '## 适用场景\n\n\n## 方法与步骤\n\n\n## 结果与注意事项\n\n']] as const) guide.append(button(label, () => {
        if (!current()) return; if (composing) { note.textContent = '请先完成当前输入，再插入模板。'; return; }
        if (body.value.trim() && !confirm('将模板追加到现有正文后面？')) return;
        const append = (body.value ? '\n\n' : '') + text;
        if (body.maxLength > 0 && body.value.length + append.length > body.maxLength) { note.textContent = '追加模板会超过正文长度，当前输入已保留。'; return; }
        body.setRangeText(append, body.value.length, body.value.length, 'end'); body.dispatchEvent(new Event('input', { bubbles: true })); body.focus();
      }));
      const help = document.createElement('p'); help.textContent = 'Markdown：**加粗**、- 列表、> 引用、`代码`。先写清背景与目标，再补充尝试和依据。'; guide.append(help); tools.after(guide);
    }
    const editor = form.querySelector('[data-circle-editor]');
    if (editor) { const advanced = document.createElement('details'); advanced.className = 'community-writing-guide'; const summary = document.createElement('summary'); summary.textContent = '图片、附件与正文预览'; advanced.append(summary); editor.before(advanced); advanced.append(editor); }
  }
}
