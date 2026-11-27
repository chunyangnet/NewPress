import { node } from '../shared/dom';
import { contentList, record, errorMessage, errorName, responseError } from './editor-tool-data';
import type { ReusableContent } from './editor-tool-data';
import type { EditorSourceSelection } from './media-types';

(() => {
  'use strict';
  const form = document.querySelector<HTMLFormElement>('[data-content-editor]');
  const candidate = document.querySelector<HTMLDialogElement>('[data-editor-reuse-dialog]');
  const sourceField = form?.querySelector<HTMLTextAreaElement>('[data-content-source]'), trigger = form?.querySelector<HTMLButtonElement>('[data-editor-reuse-open]');
  const blockEditor = window.ChunnelBlockEditor;
  if (!candidate || !sourceField || !trigger || !blockEditor) return;
  const dialog = candidate, source = sourceField, launcher = trigger, editor = blockEditor;
  const search = dialog.querySelector<HTMLFormElement>('[data-editor-reuse-search]')!;
  const query = search.querySelector<HTMLInputElement>('[name="q"]')!, filter = search.querySelector<HTMLSelectElement>('[name="status"]')!;
  const results = dialog.querySelector<HTMLElement>('[data-editor-reuse-results]')!;
  const status = dialog.querySelector<HTMLElement>('[data-editor-reuse-status]')!, pageLabel = dialog.querySelector<HTMLElement>('[data-editor-reuse-page]')!;
  const previous = dialog.querySelector<HTMLButtonElement>('[data-editor-reuse-previous]')!, next = dialog.querySelector<HTMLButtonElement>('[data-editor-reuse-next]')!;
  let page = 1, pages = 0, serial = 0, busy = false;
  let controller: AbortController | null = null, selection: EditorSourceSelection | null = null;
  let returnToLauncher = true;
  const element = <K extends keyof HTMLElementTagNameMap>(tag: K, text: string) => node(tag, '', text);
  function controls() {
    dialog.setAttribute('aria-busy', String(busy));
    search.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled = busy; previous.disabled = busy || page <= 1; next.disabled = busy || page >= pages;
    results.querySelectorAll('button').forEach(button => { button.disabled = busy; });
  }
  async function request(url: string | URL, action: (payload: unknown) => void | Promise<void>) {
    controller?.abort(); const pending = new AbortController(); controller = pending;
    const id = ++serial; busy = true; controls();
    const timer = setTimeout(() => pending.abort(new DOMException('Timeout', 'TimeoutError')), 15000);
    try {
      const response = await fetch(url, {credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: pending.signal, headers: {Accept: 'application/json'}});
      const payload: unknown = await response.json();
      if (!dialog.open || id !== serial) return;
      if (pending.signal.aborted) throw pending.signal.reason;
      if (!response.ok) throw new Error(responseError(payload, '内容暂时不可用'));
      await action(payload);
    } catch (error) {
      if (dialog.open && id === serial) status.textContent = errorName(pending.signal.reason) === 'TimeoutError' ? '内容请求超时，请重试；正文保持不变' : errorName(error) === 'AbortError' ? '' : errorMessage(error, '加载失败，请重试');
    } finally {
      clearTimeout(timer);
      if (id === serial) { busy = false; controls(); }
    }
  }
  function insert(item: ReusableContent) {
    if (!dialog.open || busy || source.disabled || source.readOnly) return;
    status.textContent = '正在读取并检查内容…';
    void request(`/api/v1/admin/contents/${item.id}`, payload => {
      if (!record(payload) || payload.id !== item.id || payload.version !== item.version || typeof payload.content_html !== 'string' || payload.status === 'trash') throw new Error('原内容已变化，请重新查找后再插入');
      if (!payload.content_html.trim()) throw new Error('这篇内容的正文为空，请选择其他内容');
      if (!selection || source.disabled || source.readOnly || source.value !== selection.value) throw new Error('正文或编辑权限已变化，请重新选择插入位置');
      editor.insert('\n' + payload.content_html + '\n', selection);
      returnToLauncher = false;
      dialog.close();
    });
  }
  function load(number = 1) {
    if (busy || number < 1) return;
    results.replaceChildren(); status.textContent = '正在查找可复用内容…';
    const url = new URL('/api/v1/admin/contents', location.origin);
    url.searchParams.set('page', String(number)); url.searchParams.set('per_page', '8');
    url.searchParams.set('status', filter.value);
    if (query.value.trim()) url.searchParams.set('q', query.value.trim());
    void request(url, value => {
      const payload = contentList(value);
      page = payload.pagination.page; pages = payload.pagination.pages;
      const fragment = document.createDocumentFragment();
      for (const item of payload.items) {
        const article = document.createElement('article'), detail = document.createElement('div');
        detail.append(element('small', `${item.type === 'page' ? '页面' : '文章'} #${item.id} · v${item.version}`), element('h3', item.title || '未命名内容'), element('p', (item.excerpt || '').slice(0, 120)));
        const button = element('button', '插入正文'); button.type = 'button'; button.className = 'admin-button admin-button-quiet';
        button.setAttribute('aria-label', `插入「${item.title || '未命名内容'}」的正文`); button.addEventListener('click', () => insert(item));
        article.append(detail, button); fragment.append(article);
      }
      results.replaceChildren(fragment); pageLabel.textContent = `${page} / ${Math.max(1, pages)}`;
      status.textContent = payload.items.length ? '选择要复制到当前文章的内容。' : '没有匹配的内容。可先把常用片段保存成草稿。';
    });
  }
  launcher.addEventListener('pointerdown', event => event.preventDefault());
  launcher.addEventListener('click', () => {
    if (dialog.open || source.disabled || source.readOnly) return;
    selection = editor.capture();
    returnToLauncher = true;
    dialog.showModal(); launcher.setAttribute('aria-expanded', 'true'); query.focus(); load(1);
  });
  dialog.querySelector('[data-editor-reuse-close]')!.addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => { if (dialog.open) return; serial++; controller?.abort(); controller = null; selection = null; busy = false; launcher.setAttribute('aria-expanded', 'false'); controls(); if (returnToLauncher && launcher.isConnected) launcher.focus({ preventScroll: true }); });
  search.addEventListener('submit', event => { event.preventDefault(); load(1); });
  previous.addEventListener('click', () => load(page - 1)); next.addEventListener('click', () => load(page + 1));
  window.addEventListener('pagehide', () => {
    serial++; controller?.abort(); controller = null;
    const interrupted = busy; busy = false; controls();
    if (dialog.open && interrupted) status.textContent = '页面切换中断了请求，请重新查找或插入；正文已保留。';
  });
})();
