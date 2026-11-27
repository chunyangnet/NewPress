/* Reading and reply controls enhance the public document and native form. */
export function readingMinutes(text = '') {
  const ideographs = (text.match(/[\u3400-\u9fff]/g) || []).length;
  const words = (text.replace(/[\u3400-\u9fff]/g, ' ').match(/[\p{L}\p{N}]+/gu) || []).length;
  return Math.max(1, Math.ceil(ideographs / 450 + words / 220));
}

export function initReading(doc = document) {
  const body = doc.querySelector('[data-article-body]');
  if (!body || body.dataset.readingReady) return;
  body.dataset.readingReady = 'true';
  const win = doc.defaultView || window;
  const duration = doc.querySelector('[data-reading-time]');
  if (duration) {
    duration.textContent = `约 ${readingMinutes(body.textContent)} 分钟阅读`;
    duration.hidden = false;
  }
  const headings = [...body.querySelectorAll('h2, h3')].filter(heading => heading.textContent.trim());
  const toc = doc.querySelector('[data-reading-toc]');
  const list = doc.querySelector('[data-reading-toc-list]');
  if (toc && list && headings.length > 1) {
    const links = headings.map((heading, index) => {
      if (!heading.id || doc.getElementById(heading.id) !== heading) {
        let id = `nexora-heading-${index + 1}`;
        while (doc.getElementById(id)) id += '-section';
        heading.id = id;
      }
      const item = doc.createElement('li');
      if (heading.tagName === 'H3') item.className = 'toc-subheading';
      const link = doc.createElement('a');
      link.href = `#${encodeURIComponent(heading.id)}`;
      link.textContent = heading.textContent.trim();
      link.addEventListener('click', () => {
        if (!heading.hasAttribute('tabindex')) heading.setAttribute('tabindex', '-1');
        heading.focus({ preventScroll: true });
      });
      item.append(link);
      list.append(item);
      return link;
    });
    toc.hidden = false;
    toc.open = toc.dataset.tocOpen !== 'false';
    let queued = false;
    const update = () => {
      queued = false;
      const header = doc.querySelector('[data-header]');
      const offset = (header?.dataset.sticky === 'false' ? 0 : (header?.getBoundingClientRect().height || 0)) + 48;
      let active = 0;
      headings.forEach((heading, index) => { if (heading.getBoundingClientRect().top <= offset) active = index; });
      links.forEach((link, index) => {
        if (index === active) link.setAttribute('aria-current', 'location');
        else link.removeAttribute('aria-current');
      });
    };
    const queueUpdate = () => { if (!queued) { queued = true; win.requestAnimationFrame(update); } };
    win.addEventListener('scroll', queueUpdate, { passive: true });
    win.addEventListener('resize', queueUpdate);
    update();
    try {
      const target = headings.find(heading => heading.id === decodeURIComponent(win.location.hash.slice(1)));
      if (target) win.requestAnimationFrame(() => {
        const reduceMotion = doc.body?.classList?.contains('nexora-no-motion') || win.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches;
        target.scrollIntoView({ behavior: reduceMotion ? 'instant' : 'auto' });
      });
    } catch { /* A malformed hash must not disable other page controls. */ }
  }
  const copy = doc.querySelector('[data-copy-link]');
  const status = doc.querySelector('[data-copy-status]');
  if (!copy) return;
  copy.hidden = false;
  copy.addEventListener('click', async () => {
    const url = doc.querySelector('link[rel="canonical"]')?.href || win.location.href.split('#')[0];
    copy.disabled = true;
    let restoreFocus = false;
    try {
      if (win.navigator.clipboard?.writeText) await win.navigator.clipboard.writeText(url);
      else {
        const field = doc.createElement('textarea');
        field.value = url;
        field.className = 'copy-fallback';
        field.setAttribute('readonly', '');
        field.setAttribute('aria-label', '文章链接');
        doc.body.append(field);
        restoreFocus = true;
        let copied = false;
        try { field.select(); copied = doc.execCommand('copy'); }
        finally { field.remove(); }
        if (!copied) throw new Error('copy unavailable');
      }
      if (status) status.textContent = '链接已复制';
    } catch {
      if (status) status.textContent = '复制失败，请从地址栏复制链接。';
    } finally {
      copy.disabled = false;
      if (restoreFocus) copy.focus({ preventScroll: true });
    }
  });
}

export function initCommentReplies(doc = document) {
  const form = doc.querySelector('[data-comment-form]');
  if (!form || form.dataset.repliesReady) return;
  form.dataset.repliesReady = 'true';
  const parent = form.querySelector('[name="parent_id"]');
  const content = form.querySelector('[name="content"]');
  const context = form.querySelector('[data-reply-context]');
  const label = form.querySelector('[data-reply-label]');
  const cancel = form.querySelector('[data-reply-cancel]');
  if (!parent || !content || !context || !label || !cancel) return;
  let returnTo = null;
  const setReply = (id, author = '') => {
    parent.value = String(id);
    context.hidden = !id;
    label.textContent = id ? (author ? `回复 ${author}` : `回复第 ${id} 条评论`) : '';
  };
  const buttons = [...doc.querySelectorAll('[data-comment-reply]')];
  buttons.forEach(button => {
    button.hidden = false;
    button.addEventListener('click', () => {
      const id = Number(button.dataset.replyId);
      if (!Number.isSafeInteger(id) || id < 1) return;
      returnTo = button;
      setReply(id, button.dataset.replyAuthor);
      content.focus();
    });
  });
  const pending = Number(parent.value);
  if (Number.isSafeInteger(pending) && pending > 0) {
    returnTo = buttons.find(button => Number(button.dataset.replyId) === pending) || null;
    setReply(pending, returnTo?.dataset.replyAuthor);
  }
  cancel.addEventListener('click', event => {
    event.preventDefault();
    setReply(0);
    (returnTo || content).focus();
    returnTo = null;
  });
}
