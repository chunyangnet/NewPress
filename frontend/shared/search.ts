import { node } from './dom';
import { createQueryCache } from './query-cache';

(() => {
  'use strict';
  if (window.ChunnelSearchReady) return;
  const form = document.querySelector<HTMLFormElement>('[data-search-box]');
  const input = form?.querySelector<HTMLInputElement>('[data-search-input]');
  if (!form || !input) return;
  const panel = node('div', 'search-preview');
  panel.hidden = true;
  const header = node('div', 'search-preview-heading');
  header.append(node('b', '', '搜索文章'));
  const status = node('p', 'search-preview-status');
  status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
  const list = node('div', 'search-preview-list');
  list.id = 'quick-search-results'; list.setAttribute('role', 'listbox'); list.setAttribute('aria-label', '文章搜索建议');
  const retry = node('button', 'search-preview-retry', '重新加载'); retry.type = 'button'; retry.hidden = true;
  const footer = node('div', 'search-preview-footer');
  const all = node('a', '', '查看全部结果');
  const help = node('small', '', '↑ ↓ 选择 · Enter 打开');
  footer.append(all, help); panel.append(header, status, list, retry, footer); form.append(panel);
  input.setAttribute('role', 'combobox'); input.setAttribute('aria-autocomplete', 'list');
  input.setAttribute('aria-expanded', 'false'); input.setAttribute('aria-controls', list.id);
  input.setAttribute('autocomplete', 'off'); input.setAttribute('aria-keyshortcuts', 'Control+k Meta+k');

  const cache = createQueryCache<{ items: unknown[]; total?: unknown }>();
  document.addEventListener('chunnel:session', () => { cache.clear(); close(); reset(); });
  let timer: ReturnType<typeof setTimeout> | undefined, controller: AbortController | null = null;
  let sequence = 0, active = -1, composing = input.dataset.searchComposing === 'true', options: HTMLAnchorElement[] = [];
  let compositionCanceled = false, canceledValue: string | null = null;
  const query = () => input.value.trim();
  const clearSelection = () => { active = -1; input.removeAttribute('aria-activedescendant'); };
  const cancel = () => { clearTimeout(timer); controller?.abort(); controller = null; sequence++; list.removeAttribute('aria-busy'); };
  const expanded = (open: boolean) => {
    input.setAttribute('aria-expanded', String(open));
    document.querySelectorAll('[data-focus-search]').forEach(button => button.setAttribute('aria-expanded', String(open)));
  };
  const close = () => { if (composing) compositionCanceled = true; cancel(); panel.hidden = true; expanded(false); clearSelection(); };
  const show = () => {
    panel.hidden = false; expanded(true);
    all.href = `/explore/${query() ? `?${new URLSearchParams({ q: query() })}` : ''}`;
    all.textContent = query() ? '查看全部结果' : '浏览所有文章';
  };
  const reset = () => { list.replaceChildren(); options = []; clearSelection(); retry.hidden = true; };
  const message = (text: string) => { status.textContent = text; };
  const highlight = (title: string, term: string) => {
    const fragment = document.createDocumentFragment();
    const at = title.toLocaleLowerCase().indexOf(term.toLocaleLowerCase());
    if (at < 0) { fragment.append(document.createTextNode(title)); return fragment; }
    fragment.append(document.createTextNode(title.slice(0, at)), node('mark', '', title.slice(at, at + term.length)), document.createTextNode(title.slice(at + term.length)));
    return fragment;
  };
  const search = async () => {
    cancel(); reset(); show();
    const term = query();
    if (!term) { message('搜索标题、正文、作者或关键词。'); return; }
    if (composing) return;
    const request = sequence;
    const pending = new AbortController(); controller = pending;
    const timeout = setTimeout(() => pending.abort(), 8000);
    list.setAttribute('aria-busy', 'true'); message('正在寻找相关内容…');
    try {
      const cached = cache.peek(term);
      const data = cached ?? await cache.get(term, async () => {
        const response = await fetch(`/api/v1/posts?${new URLSearchParams({ q: term, limit: '5', sort: 'relevance' })}`, {
          headers: { Accept: 'application/json' }, credentials: 'same-origin', cache: 'no-store', signal: pending.signal,
        });
        if (!response.ok) throw new Error(response.status === 404 ? 'disabled' : 'unavailable');
        const value: unknown = await response.json();
        if (pending.signal.aborted) throw new DOMException('Cancelled', 'AbortError');
        if (!value || typeof value !== 'object' || !('items' in value) || !Array.isArray(value.items)) throw new Error('invalid');
        return { items: value.items, total: 'total' in value ? value.total : undefined };
      }, true);
      if (request !== sequence) return;
      data.items.slice(0, 5).forEach((item: unknown) => {
        if (!item || typeof item !== 'object' || !('slug' in item) || !('title' in item)) return;
        if (typeof item.slug !== 'string' || !item.slug || /[\/\\?#]/.test(item.slug) || ['.', '..'].includes(item.slug) || typeof item.title !== 'string') return;
        const link = node('a', 'search-preview-result');
        link.href = `/posts/${encodeURIComponent(item.slug)}`;
        link.id = `quick-search-option-${options.length}`; link.setAttribute('role', 'option'); link.setAttribute('aria-selected', 'false'); link.tabIndex = -1;
        const index = node('span', 'search-result-index', String(options.length + 1).padStart(2, '0')); index.setAttribute('aria-hidden', 'true');
        const copy = node('span', 'search-result-copy');
        const title = node('b'); title.append(highlight(item.title.slice(0, 240), term));
        const meta = ['category' in item ? item.category : null, 'author_name' in item ? item.author_name : null].filter(value => typeof value === 'string' && value.trim()).join(' · ');
        copy.append(title, node('small', '', meta.slice(0, 160) || '公开文章'));
        link.append(index, copy);
        list.append(link); options.push(link);
      });
      const total = 'total' in data && typeof data.total === 'number' && Number.isSafeInteger(data.total) && data.total >= options.length ? data.total : options.length;
      message(options.length ? `找到 ${total} 篇文章，先看这 ${options.length} 篇` : '暂时没有匹配的文章，试试更短的关键词。');
    } catch (error) {
      if (request !== sequence) return;
      reset();
      message((error instanceof Error && error.message === 'disabled') ? '快捷预览暂未开放，可以继续使用完整搜索。' : navigator.onLine ? '暂时无法加载预览，请重试或打开完整搜索。' : '当前处于离线状态，联网后再试一次。');
      retry.hidden = (error instanceof Error && error.message === 'disabled');
    } finally {
      clearTimeout(timeout);
      if (request === sequence) { list.removeAttribute('aria-busy'); controller = null; }
    }
  };
  const queue = () => {
    // Blur/overlay dismissal can precede compositionend and its final input.
    // Keep that committed text without reopening or fetching a dismissed preview.
    if (document.activeElement !== input || (composing && compositionCanceled) || input.value === canceledValue) return;
    canceledValue = null;
    cancel(); reset(); show();
    if (!query()) { message('搜索标题、正文、作者或关键词。'); return; }
    message(composing ? '输入完成后开始搜索' : '正在寻找相关内容…');
    if (!composing) timer = setTimeout(search, 240);
  };
  input.addEventListener('input', queue);
  input.addEventListener('focus', () => { compositionCanceled = false; canceledValue = null; queue(); });
  input.addEventListener('compositionstart', () => { composing = true; compositionCanceled = false; canceledValue = null; queue(); });
  input.addEventListener('compositionend', () => {
    composing = false;
    if (compositionCanceled) { canceledValue = input.value; return; }
    queue();
  });
  input.addEventListener('keydown', (event) => {
    if (event.isComposing || composing || event.keyCode === 229) { event.stopPropagation(); return; }
    if (event.key === 'Escape' && !panel.hidden) { event.preventDefault(); event.stopPropagation(); close(); return; }
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      if (panel.hidden) { search(); return; }
      if (!options.length) return;
      active = active < 0 ? event.key === 'ArrowDown' ? 0 : options.length - 1 : (active + (event.key === 'ArrowDown' ? 1 : -1) + options.length) % options.length;
      options.forEach((link, index) => link.setAttribute('aria-selected', String(index === active)));
      const option = options[active]; if (!option) return;
      input.setAttribute('aria-activedescendant', option.id);
      // Scroll only the suggestion panel; the sticky header must not move the page.
      const box = option.getBoundingClientRect(), bounds = panel.getBoundingClientRect();
      if (box.top < bounds.top) panel.scrollTop -= bounds.top - box.top;
      else if (box.bottom > bounds.bottom) panel.scrollTop += box.bottom - bounds.bottom;
    }
    if (event.key === 'Enter' && active >= 0 && !panel.hidden) { event.preventDefault(); options[active]?.click(); }
  });
  form.addEventListener('submit', (event) => { if (composing) { event.preventDefault(); return; } close(); });
  form.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape' || event.isComposing || composing || event.keyCode === 229 || panel.hidden) return;
    event.preventDefault(); event.stopPropagation(); input.focus({ preventScroll: true }); close();
  });
  form.addEventListener('focusout', (event) => { if (!(event.relatedTarget instanceof Node) || !form.contains(event.relatedTarget)) close(); });
  document.addEventListener('pointerdown', (event) => { if (!(event.target instanceof Node) || !form.contains(event.target)) close(); });
  document.addEventListener('chunnel:overlay', (event) => { if (event.detail.open) close(); });
  // Keep WebKit from blurring the input and hiding this button before click.
  retry.addEventListener('pointerdown', event => { if (event.button === 0) event.preventDefault(); });
  retry.addEventListener('click', () => { input.focus({ preventScroll: true }); search(); });
  window.addEventListener('pagehide', close);
  window.ChunnelSearchReady = true;
  if (document.activeElement === input && input.dataset.searchDismissed !== 'true') queue();
})();
