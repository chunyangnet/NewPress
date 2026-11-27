import { fetchPreview, syncPreviewColorMode } from './appearance-preview-support';

(() => {
  const panelNode = document.querySelector<HTMLElement>('[data-appearance-preview]'), formNode = document.querySelector<HTMLFormElement>('#theme-settings-form');
  if (!panelNode || !formNode) return;
  const panel = panelNode, form = formNode, dialog = panel.closest('dialog');
  const stage = panel.querySelector<HTMLElement>('[data-preview-stage]')!, canvas = panel.querySelector<HTMLElement>('[data-preview-canvas]')!;
  const empty = panel.querySelector<HTMLElement>('[data-preview-empty]')!, status = panel.querySelector<HTMLElement>('[data-preview-status]')!;
  const context = panel.querySelector<HTMLElement>('[data-preview-context]')!, viewport = panel.querySelector<HTMLElement>('[data-preview-viewport]')!;
  if (!stage || !canvas || !empty || !status || !context || !viewport) return;
  const readOnly = panel.dataset.previewReadonly === 'true';
  const sectionTargets: Record<string, string> = { header: '.site-header', home: '[data-home-portal]', feed: '#latest', article: '#article-body', modules: '#community', footer: '.site-footer' };
  let page: 'home' | 'article' = 'home', device: 'desktop' | 'mobile' = 'desktop', section = 'brand';
  let frame: HTMLIFrameElement | undefined, controller: AbortController | undefined, timer: ReturnType<typeof setTimeout> | undefined;
  let generation = 0, loadedPage = '', sizing = 0, frameTimer: ReturnType<typeof setTimeout> | undefined;
  const syncColorMode = (doc: Document | null | undefined) => syncPreviewColorMode(document, doc);
  document.addEventListener('chunnel:color-mode', () => syncColorMode(frame?.contentDocument));
  const sheet = [...document.styleSheets].find(item => item.href?.includes('/chunnel-appearance-preview.css'));
  let rule: CSSStyleRule | undefined;
  try { rule = sheet && [...sheet.cssRules].find((item): item is CSSStyleRule => item instanceof CSSStyleRule && item.selectorText === '.appearance-preview-frame'); } catch { /* A missing stylesheet remains a normal preview failure. */ }
  const resize = () => {
    const width = device === 'mobile' ? 390 : 1440, scale = Math.min(1, Math.max(1, canvas.clientWidth) / width);
    rule?.style.setProperty('width', `${width}px`); rule?.style.setProperty('height', `${Math.round(stage.clientHeight / scale)}px`); rule?.style.setProperty('transform', `scale(${scale})`);
    viewport.textContent = `${width} px`;
  };
  const resizeObserver = new ResizeObserver(() => { cancelAnimationFrame(sizing); sizing = requestAnimationFrame(resize); }); resizeObserver.observe(stage);
  const locate = () => {
    if (!frame?.contentDocument || !frame.contentWindow) return;
    const target = frame.contentDocument.querySelector(sectionTargets[section] || 'body');
    const top = target ? target.getBoundingClientRect().top + frame.contentWindow.scrollY : 0;
    frame.contentWindow.scrollTo({ top: Math.max(0, top - (section === 'footer' ? 0 : 100)), behavior: 'instant' });
  };
  const describe = () => {
    panel.querySelectorAll<HTMLElement>('[data-preview-page]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.previewPage === page)));
    const heading = document.getElementById(`settings-title-${section}`)?.textContent || '品牌与视觉';
    context.textContent = `${page === 'article' ? '文章' : '首页'} · ${heading}`;
  };
  const fail = (message: string) => {
    stage.setAttribute('aria-busy', 'false'); status.textContent = message; status.dataset.tone = 'error';
    if (!frame) {
      empty.hidden = false;
      const title = empty.querySelector('strong'), text = empty.querySelector('p');
      if (title) title.textContent = '暂时无法预览'; if (text) text.textContent = message;
    }
  };
  const cancel = () => {
    ++generation; controller?.abort(); clearTimeout(timer); clearTimeout(frameTimer);
    canvas.querySelectorAll('iframe:not(.is-ready)').forEach(node => node.remove());
  };
  const refresh = async () => {
    if (dialog && !dialog.open) return;
    cancel(); const current = generation;
    const request = new AbortController(); controller = request;
    const requestPage = page;
    stage.setAttribute('aria-busy', 'true'); status.textContent = '正在更新预览…'; status.dataset.tone = '';
    try {
      const url = new URL(panel.dataset.previewUrl || '', location.href);
      if (url.origin !== location.origin || !panel.dataset.previewUrl) throw new Error('预览地址无效');
      url.searchParams.set('page', requestPage);
      const body = readOnly ? undefined : new URLSearchParams([...new FormData(form)].map(([key, value]) => [key, String(value)]));
      const result = await fetchPreview(url.href, body, request.signal);
      if (current !== generation || request.signal.aborted) return;
      if (!result.ok) {
        const label = result.field && form.querySelector(`[data-setting-field="${CSS.escape(result.field)}"] label`)?.textContent;
        fail(result.error === 'preview_article_missing' ? '还没有公开文章，发布后即可预览阅读页。' : result.status === 422 ? `请先完善${label ? `“${label.replace('*', '')}”` : '标记的设置'}；当前画面未更新。` : '预览未更新，请重试或重新登录。');
        return;
      }
      const doc = new DOMParser().parseFromString(result.html, 'text/html'); syncColorMode(doc);
      doc.querySelectorAll('script, iframe, object, embed, base, noscript, meta[http-equiv="refresh"]').forEach(node => node.remove());
      doc.querySelectorAll('[autofocus]').forEach(node => node.removeAttribute('autofocus'));
      doc.querySelectorAll('*').forEach(node => { [...node.attributes].forEach(attr => { if (/^on/i.test(attr.name)) node.removeAttribute(attr.name); }); });
      doc.querySelectorAll('link[href^="/theme-settings.css"]').forEach(node => node.remove());
      doc.body.inert = true;
      doc.querySelectorAll('img[loading="lazy"]').forEach(img => img.removeAttribute('loading'));
      const next = document.createElement('iframe'); next.className = 'appearance-preview-frame';
      next.title = requestPage === 'article' ? '当前设置下的文章预览' : '当前设置下的首页预览';
      next.setAttribute('sandbox', 'allow-same-origin'); next.setAttribute('referrerpolicy', 'no-referrer');
      const loadTimeout = setTimeout(() => { if (current === generation) { next.remove(); fail('预览页面加载超时，请重试。'); } }, 15000); frameTimer = loadTimeout;
      next.addEventListener('load', () => {
        clearTimeout(loadTimeout);
        if (current !== generation || !next.isConnected) { next.remove(); return; }
        const previewDoc = next.contentDocument;
        if (!previewDoc?.body) { next.remove(); fail('预览加载失败，请重试。'); return; }
        syncColorMode(previewDoc);
        try {
          const previewSheet = [...previewDoc.styleSheets].reverse().find(sheet => sheet.href && new URL(sheet.href).origin === location.origin);
          if (!previewSheet) throw new Error('Stylesheet unavailable');
          previewSheet.insertRule(result.css, previewSheet.cssRules.length);
          previewSheet.insertRule('html { scroll-behavior: auto !important; }', previewSheet.cssRules.length);
          previewSheet.insertRule('[data-reveal], .reveal { opacity: 1 !important; transform: none !important; }', previewSheet.cssRules.length);
        } catch { next.remove(); fail('预览样式加载失败，请重试。'); return; }
        const previousScroll = frame?.contentWindow?.scrollY || 0;
        frame?.remove(); frame = next; next.classList.add('is-ready'); empty.hidden = true; resize();
        if (loadedPage === requestPage) next.contentWindow?.scrollTo(0, previousScroll); else locate();
        loadedPage = requestPage; stage.setAttribute('aria-busy', 'false'); status.dataset.tone = '';
        status.textContent = readOnly ? '已显示站点当前外观 · 只读' : '预览已同步 · 保存后应用到站点';
      }, { once: true });
      next.srcdoc = '<!doctype html>\n' + doc.documentElement.outerHTML; canvas.append(next);
    } catch { if (current === generation) fail('预览加载失败，请检查连接后重试。'); }
  };
  const schedule = () => {
    cancel(); status.textContent = '设置已修改，正在准备预览…';
    if (!dialog || dialog.open) timer = setTimeout(() => { void refresh(); }, 350);
  };
  document.addEventListener('appearance-preview-change', schedule);
  document.addEventListener('appearance-preview-section', event => {
    const detail: unknown = (event as CustomEvent<unknown>).detail; if (typeof detail !== 'string') return;
    section = detail.replace(/^settings-/, ''); const nextPage = section === 'article' ? 'article' : 'home';
    if (page !== nextPage) { page = nextPage; schedule(); } else locate(); describe();
  });
  panel.querySelectorAll<HTMLElement>('[data-preview-page]').forEach(button => button.addEventListener('click', () => {
    const value = button.dataset.previewPage; if (value !== 'home' && value !== 'article') return;
    page = value; section = page === 'article' ? 'article' : 'brand'; describe(); schedule();
  }));
  panel.querySelectorAll<HTMLElement>('[data-preview-device]').forEach(button => button.addEventListener('click', () => {
    const value = button.dataset.previewDevice; if (value !== 'desktop' && value !== 'mobile') return;
    device = value; panel.querySelectorAll<HTMLElement>('[data-preview-device]').forEach(item => item.setAttribute('aria-pressed', String(item === button))); resize(); locate();
  }));
  panel.querySelector('[data-preview-refresh]')?.addEventListener('click', () => { void refresh(); });
  dialog?.addEventListener('close', () => { cancel(); stage.setAttribute('aria-busy', 'false'); });
  window.addEventListener('pagehide', event => { cancel(); cancelAnimationFrame(sizing); if (!event.persisted) resizeObserver.disconnect(); });
  window.addEventListener('pageshow', event => { if (event.persisted) schedule(); });
  document.addEventListener('admin-preview-open', () => { describe(); resize(); schedule(); });
  describe(); resize(); schedule();
})();
