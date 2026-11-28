import { node as element } from '../shared/dom';
import { articleSummary, record, integer, errorMessage, errorName, responseError } from './editor-tool-data';
import type { ArticleSummary } from './editor-tool-data';
import type { EditorSourceSelection } from './media-types';
import { editorToolReady, registerEditorTool } from './editor-tool-launch';

(() => {
  const formNode = document.querySelector<HTMLFormElement>('[data-content-editor]'), dialogNode = document.querySelector<HTMLDialogElement>('[data-editor-widget-dialog]');
  const sourceNode = formNode?.querySelector<HTMLTextAreaElement>('[data-content-source]'), blockEditor = window.ChunnelBlockEditor;
  const launchers = [...(formNode?.querySelectorAll<HTMLButtonElement>('[data-editor-widget-open]') || [])];
  if (!formNode || !dialogNode || !sourceNode || !blockEditor || !launchers.length) return;
  if (editorToolReady(launchers[0]!)) return;
  const form = formNode, dialog = dialogNode, source = sourceNode, editor = blockEditor;
  const find = <T extends HTMLElement = HTMLElement>(name: string) => dialog.querySelector<T>(`[data-editor-widget-${name}]`)!;
  const search = find<HTMLFormElement>('search'), results = find('results'), status = find('status'), preview = find('preview');
  const query = search.querySelector<HTMLInputElement>('[name="q"]')!;
  const previous = find<HTMLButtonElement>('previous'), next = find<HTMLButtonElement>('next'), pageLabel = find('page'), insertButton = find<HTMLButtonElement>('insert');
  const layouts = [...dialog.querySelectorAll<HTMLInputElement>('[name="article_widget_layout"]')];
  let serial = 0, busy = false, page = 1, pages = 1, inserted = false;
  let controller: AbortController | null = null, selected: ArticleSummary | null = null;
  let selection: EditorSourceSelection | null = null, launcher: HTMLElement | null = null;
  const layout = () => layouts.find(input => input.checked)?.value || 'card';
  function controls() {
    dialog.setAttribute('aria-busy', String(busy));
    previous.disabled = busy || page <= 1; next.disabled = busy || page >= pages;
    search.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled = busy;
    insertButton.disabled = busy || !selected || source.disabled || source.readOnly;
    results.querySelectorAll('button').forEach(button => { button.disabled = busy; });
  }
  function drawPreview() {
    if (!selected) {
      const empty = element('p', 'admin-article-widget-placeholder', '选择一篇文章');
      empty.append(element('span', '', '在这里看看引用效果')); preview.replaceChildren(empty); return;
    }
    const item = selected, card = document.createElement('aside');
    card.dataset.npWidget = 'post'; card.dataset.npPost = String(item.id); card.dataset.npLayout = layout();
    const part = <K extends keyof HTMLElementTagNameMap>(tag: K, name: string, text = '') => { const node = element(tag, '', text); node.dataset.npPart = name; return node; };
    // A span keeps the preview inert while using the front-end card structure.
    const link = part('span', 'link');
    if (item.cover_url) {
      const cover = part('span', 'cover'), img = document.createElement('img');
      img.src = item.cover_url; img.alt = ''; img.loading = 'lazy';
      img.addEventListener('error', () => cover.remove(), {once: true}); cover.append(img); link.append(cover);
    }
    const body = part('span', 'body');
    body.append(part('span', 'eyebrow', item.category || '推荐阅读'), part('strong', 'title', item.title));
    if (item.excerpt) body.append(part('span', 'excerpt', item.excerpt));
    body.append(part('span', 'meta', [item.author_name, '阅读全文'].filter(Boolean).join(' · ')));
    link.append(body); card.append(link); preview.replaceChildren(card);
  }
  function select(item: ArticleSummary) {
    selected = item;
    results.querySelectorAll<HTMLElement>('[data-article-choice]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.articleChoice === String(item.id))));
    drawPreview(); controls();
  }
  async function load(number = 1) {
    controller?.abort(); const pending = new AbortController(); controller = pending;
    const id = ++serial; busy = true; selected = null; drawPreview(); controls();
    results.replaceChildren(); status.textContent = '正在查找公开文章…';
    const url = new URL(form.dataset.articleWidgetsUrl || '/api/v1/admin/article-widgets', location.origin);
    url.searchParams.set('page', String(number));
    if (query.value.trim()) url.searchParams.set('q', query.value.trim());
    const timer = setTimeout(() => pending.abort(new DOMException('Timeout', 'TimeoutError')), 15000);
    try {
      const response = await fetch(url, {credentials: 'same-origin', redirect: 'error', cache: 'no-store', signal: pending.signal, headers: {Accept: 'application/json'}});
      const payload: unknown = await response.json();
      if (id !== serial || !dialog.open) return;
      if (pending.signal.aborted) throw pending.signal.reason;
      if (!response.ok) throw new Error(responseError(payload, '文章暂时无法读取，请重试'));
      if (!record(payload) || !Array.isArray(payload.items) || !record(payload.pagination) || !integer(payload.pagination.page, 1) || !integer(payload.pagination.pages, 1)) throw new Error('文章列表响应无效，请重试');
      page = payload.pagination.page; pages = payload.pagination.pages;
      const items = payload.items.map(item => articleSummary(item, editor)).filter((item): item is ArticleSummary => item !== null);
      const fragment = document.createDocumentFragment();
      for (const item of items) {
        const button = element('button', 'admin-article-widget-choice'); button.type = 'button'; button.dataset.articleChoice = String(item.id);
        button.setAttribute('aria-pressed', 'false'); button.setAttribute('aria-label', `选择文章：${item.title}`);
        const thumb = element('span', 'admin-article-widget-thumb'); thumb.setAttribute('aria-hidden', 'true');
        if (item.cover_url) {
          const img = document.createElement('img'); img.src = item.cover_url; img.alt = ''; img.loading = 'lazy';
          img.addEventListener('error', () => { thumb.textContent = '文'; }, {once: true}); thumb.append(img);
        } else thumb.textContent = '文';
        const copy = element('span', 'admin-article-widget-choice-copy');
        copy.append(element('small', '', item.category || '本站文章'), element('strong', '', item.title), element('span', '', item.author_name || '已发布'));
        const check = element('span', 'admin-article-widget-check', '✓'); check.setAttribute('aria-hidden', 'true');
        button.append(thumb, copy, check); button.addEventListener('click', () => select(item)); fragment.append(button);
      }
      results.replaceChildren(fragment); pageLabel.textContent = `${page} / ${pages}`;
      status.textContent = items.length ? '选择一篇文章，查看卡片效果。' : '没有找到匹配的公开文章，换个关键词试试。';
    } catch (error) {
      if (id === serial && dialog.open) status.textContent = errorName(pending.signal.reason) === 'TimeoutError' ? '请求超时，请重新查找；正文已保留。' : errorName(error) === 'AbortError' ? '' : errorMessage(error, '加载失败，请重试');
    } finally {
      clearTimeout(timer); if (id === serial) { busy = false; controls(); }
    }
  }
  function open(button: HTMLButtonElement, captured?: EditorSourceSelection) {
      if (dialog.open || source.disabled || source.readOnly) return;
      selection = captured ?? editor.capture(); launcher = button; inserted = false; selected = null;
      dialog.showModal(); launchers.forEach(node => node.setAttribute('aria-expanded', 'true'));
      query.focus(); void load(1);
  }
  find('close').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => {
    if (dialog.open) return;
    serial++; controller?.abort(); controller = null; busy = false; controls();
    selection = null; selected = null;
    launchers.forEach(button => button.setAttribute('aria-expanded', 'false'));
    if (!inserted && launcher?.isConnected) launcher.focus({preventScroll: true});
  });
  search.addEventListener('submit', event => { event.preventDefault(); void load(1); });
  previous.addEventListener('click', () => { if (!busy && page > 1) void load(page - 1); });
  next.addEventListener('click', () => { if (!busy && page < pages) void load(page + 1); });
  layouts.forEach(input => input.addEventListener('change', drawPreview));
  insertButton.addEventListener('click', () => {
    if (!dialog.open || busy || !selected || !selection || source.disabled || source.readOnly) return;
    try {
      if (source.value !== selection.value) throw new Error('正文已变化，请重新选择插入位置');
      const markup = editor.renderBlock({kind: 'post', postID: selected.id, url: selected.permalink, title: selected.title, layout: layout()});
      editor.insert('\n' + markup + '\n', selection); inserted = true; dialog.close();
    } catch (error) { status.textContent = errorMessage(error, '插入失败，请重新选择位置；正文已保留。'); }
  });
  window.addEventListener('pagehide', () => {
    serial++; controller?.abort(); controller = null;
    const interrupted = busy; busy = false; controls();
    if (dialog.open && interrupted) status.textContent = '页面切换中断了请求，请重新查找；正文已保留。';
  });
  registerEditorTool(launchers, open);
})();
