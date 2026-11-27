(() => {
  'use strict';
  const consolePage = document.querySelector('[data-ai-pro-max]');
  if (!consolePage) return;
  consolePage.addEventListener('submit', event => {
    const message = event.submitter?.dataset.confirmAction;
    if (message && !window.confirm(message)) event.preventDefault();
  });

  const picker = consolePage.querySelector('[data-topic-picker]');
  if (!picker) return;
  const form = picker.closest('form');
  const topics = form.elements.namedItem('topics');
  const selectors = ['target', 'model', 'language'].map(name => form.elements.namedItem(name));
  const button = picker.querySelector('[data-suggest-topics]');
  const status = picker.querySelector('[data-topic-status]');
  const list = picker.querySelector('[data-topic-options]');
  let current = null;
  picker.hidden = false;

  function clearSuggestions() {
    current?.abort();
    current = null;
    button.disabled = false;
    list.replaceChildren();
    status.textContent = '';
  }
  for (const select of selectors) select.addEventListener('change', clearSuggestions);
  window.addEventListener('pagehide', clearSuggestions);

  function showOption(title) {
    const item = document.createElement('li');
    const add = document.createElement('button');
    add.type = 'button';
    add.textContent = '+ ' + title;
    add.setAttribute('aria-label', '采用选题：' + title);
    add.addEventListener('click', () => {
      const existing = topics.value.split(/\r?\n/).map(line => line.trim()).filter(Boolean);
      if (!existing.includes(title)) {
        if (existing.length >= 20) { status.textContent = '计划已有 20 个选题。'; return; }
        topics.value += (topics.value && !topics.value.endsWith('\n') ? '\n' : '') + title;
        topics.dispatchEvent(new Event('input', {bubbles: true}));
      }
      item.remove();
      status.textContent = '选题已填入，可继续编辑。';
    });
    item.append(add);
    list.append(item);
  }

  button.addEventListener('click', async () => {
    if (current) return;
    const request = new AbortController();
    current = request;
    button.disabled = true;
    list.replaceChildren();
    status.textContent = '正在生成选题…';
    try {
      const input = Object.fromEntries(selectors.map(select => [select.name, select.value]));
      const response = await fetch('/api/v1/admin/plugins/ai-pro-max/topics', {
        method:'POST', credentials:'same-origin', signal:request.signal,
        headers:{'Content-Type':'application/json','X-CSRF-Token':form.elements.namedItem('csrf_token').value},
        body:JSON.stringify(input),
      });
      const payload = await response.json();
      if (current !== request) return;
      if (!response.ok) throw new Error(payload.error?.message || '暂时无法生成选题。');
      if (!Array.isArray(payload.topics) || !payload.topics.length) throw new Error('模型没有返回选题。');
      payload.topics.forEach(showOption);
      status.textContent = '点击选题可加入计划。';
    } catch (error) {
      if (current === request) status.textContent = error.message || '暂时无法生成选题。';
    } finally {
      if (current === request) { current = null; button.disabled = false; }
    }
  });
})();
