(() => {
  'use strict';
  const root = document.querySelector('[data-questions], [data-circle-page]');
  if (!root) return;
  const circle = root.hasAttribute('data-circle-page');
  const button = (label, action) => {
    const el = document.createElement('button');
    el.type = 'button'; el.className = 'ui-button'; el.textContent = label;
    el.addEventListener('click', action); return el;
  };
  // Keep URLs shareable and preserve all other active filters.
  const filters = root.querySelector('.circle-topic-filters, .qa-filters');
  if (filters) {
    // Themes may already render status tabs for native navigation. Complete
    // those tabs instead of adding a second row with the same choices.
    const existing = !circle && root.querySelector('.channel-tabs[aria-label="问题状态"]');
    const nav = existing || document.createElement('nav');
    if (!existing) {
      nav.className = 'community-quick-filters';
      nav.setAttribute('aria-label', circle ? '常用话题筛选' : '常用问题筛选');
    }
    const key = circle ? 'filter' : 'status';
    const choices = circle ? [['', '全部话题'], ['featured', '精华'], ['unanswered', '待解决'], ...(Number(root.dataset.userId) > 0 ? [['mine', '我发布的'], ['participated', '我回应的']] : [])] : [['', '全部'], ['unanswered', '还没有回答'], ['open', '待解决'], ['solved', '已解决']];
    for (const [value, label] of choices) {
      const url = new URL(location.href); url.searchParams.delete('page'); url.hash = '';
      if (value) url.searchParams.set(key, value); else url.searchParams.delete(key);
      const link = existing && [...nav.querySelectorAll('a[href]')].find(item => (new URL(item.href).searchParams.get(key) || '') === value) || document.createElement('a');
      link.href = url.pathname + url.search;
      if (!link.textContent) link.textContent = label;
      if ((new URL(location.href).searchParams.get(key) || '') === value) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
      nav.append(link);
    }
    if (!existing) filters.before(nav);
    const select = filters.elements.namedItem('filter');
    if (circle && select && Number(root.dataset.userId) > 0) {
      for (const [value, label] of [['mine', '我发布的'], ['participated', '我回应的']]) {
        const option = new Option(label, value); option.selected = new URL(location.href).searchParams.get(key) === value; select.add(option);
      }
    }
  }
  if (!Number(root.dataset.userId)) return;
  for (const form of root.querySelectorAll('form[data-qa-command], form[data-circle-command]')) {
    const action = form.dataset.qaCommand || form.dataset.circleCommand;
    if (!['create', 'answer', 'topic', 'reply', 'edit', 'edit_answer', 'edit_topic', 'edit_reply'].includes(action)) continue;
    const body = form.elements.namedItem('body'); if (!body) continue;
    const fields = ['title', 'body', 'tags', 'kind', 'format'].map(name => form.elements.namedItem(name)).filter(Boolean);
    const initial = new Map(fields.map(field => [field, field.value]));
    const key = ['newpress-compose-v1', root.dataset.userId, location.pathname, action, form.dataset.answer || form.dataset.topic || '', form.dataset.reply || ''].join(':');
    const tools = document.createElement('div'); tools.className = 'community-compose-tools';
    const note = document.createElement('span'); note.setAttribute('role', 'status');
    note.textContent = '草稿自动保留在当前标签页，刷新后可恢复。';
    const count = document.createElement('small');
    const updateCount = () => { count.textContent = `${body.value.length} / ${body.maxLength > 0 ? body.maxLength : '不限'} 字符`; };
    let saved = null;
    try { saved = JSON.parse(sessionStorage.getItem(key)); } catch {}
    const restore = button('恢复草稿', () => {
      if (!saved || (fields.some(field => field.value !== initial.get(field) && field.value.trim()) && !confirm('恢复草稿将替换当前输入，继续吗？'))) return;
      for (const field of fields) if (typeof saved[field.name] === 'string') field.value = saved[field.name];
      restore.hidden = true; body.dispatchEvent(new Event('input', { bubbles: true }));
      note.textContent = '草稿已恢复，请核对正文和附件后发布。'; body.focus();
    });
    restore.hidden = !saved;
    const discard = button('删除草稿', () => {
      try { sessionStorage.removeItem(key); saved = null; restore.hidden = true; note.textContent = '已删除保存的草稿，当前输入仍保留。'; } catch { note.textContent = '无法访问草稿存储。'; }
    });
    const save = () => {
      updateCount();
      if (!restore.hidden) { note.textContent = '有一份待恢复草稿，请先恢复或删除；当前输入尚未保存。'; return; }
      const value = Object.fromEntries(fields.map(field => [field.name, field.value]));
      try { sessionStorage.setItem(key, JSON.stringify(value)); note.textContent = '草稿已保存到当前标签页。'; } catch { note.textContent = '草稿暂时无法保存，请保留当前页面。'; }
    };
    form.addEventListener('input', save); form.addEventListener('change', save);
    form.addEventListener('community:saved', () => { try { sessionStorage.removeItem(key); } catch {} });
    tools.append(restore, discard, count, note); body.closest('label').after(tools); updateCount();
    if (['create', 'topic', 'answer', 'reply'].includes(action)) {
      const guide = document.createElement('details'); guide.className = 'community-writing-guide';
      const summary = document.createElement('summary'); summary.textContent = '写作辅助 · 模板与格式'; guide.append(summary);
      for (const [label, text] of [['问题模板', '## 背景与目标\n\n\n## 遇到的问题\n\n\n## 已尝试的方法\n\n'], ['经验分享', '## 适用场景\n\n\n## 方法与步骤\n\n\n## 结果与注意事项\n\n']]) {
        guide.append(button(label, () => {
          if (body.value.trim() && !confirm('将模板追加到现有正文后面？')) return;
          body.value += (body.value ? '\n\n' : '') + text;
          body.dispatchEvent(new Event('input', { bubbles: true })); body.focus();
        }));
      }
      const help = document.createElement('p'); help.textContent = 'Markdown：**加粗**、- 列表、> 引用、`代码`。先写清背景与目标，再补充尝试和依据。'; guide.append(help);
      tools.after(guide);
    }
    // Existing attachment and preview behavior stays intact inside a native disclosure.
    const editor = form.querySelector('[data-circle-editor]');
    if (editor) {
      const advanced = document.createElement('details'); advanced.className = 'community-writing-guide';
      const summary = document.createElement('summary'); summary.textContent = '图片、附件与正文预览'; advanced.append(summary);
      editor.before(advanced); advanced.append(editor);
    }
  }
})();
