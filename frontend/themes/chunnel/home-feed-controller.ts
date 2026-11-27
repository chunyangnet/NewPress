import type {} from './home-types';
// Loaded only after an explicit feed interaction.
export function createHomeFeed() {
  const $ = (selector: string) => document.querySelector<HTMLElement>(selector);
  // Keep the previous results visible while a new view is fetched.
  const feedPanel = $('#latest');
  let feedRequest: AbortController | null = null;
  let feedLocation = location.pathname + location.search;
  let feedScrollTimer: ReturnType<typeof setTimeout> | undefined;
  let badgeStyles: Promise<void> | null | undefined;
  const ensureBadgeStyles = (doc: Document, next: HTMLElement): Promise<void> => {
    if (!next.querySelector('.np-article-badge') || $('link[data-article-badge-styles]')) return Promise.resolve();
    if (badgeStyles) return badgeStyles;
    const source = doc.querySelector('link[data-article-badge-styles]');
    if (!source) return Promise.reject(new Error('badge styles missing'));
    const href = new URL(source.getAttribute('href') || '', location.href);
    if (href.origin !== location.origin) return Promise.reject(new Error('badge styles origin'));
    badgeStyles = new Promise<void>((resolve, reject) => {
      const link = document.createElement('link');
      link.rel = 'stylesheet'; link.href = href.href;
      const done = (error?: Error) => {
        clearTimeout(timer); link.onload = link.onerror = null;
        if (error) { link.remove(); badgeStyles = null; reject(error); }
        else { link.setAttribute('data-article-badge-styles', ''); resolve(); }
      };
      const timer = setTimeout(() => done(new Error('badge styles timeout')), 15000);
      link.onload = () => done();
      link.onerror = () => done(new Error('badge styles unavailable'));
      document.head.append(link);
    });
    return badgeStyles;
  };
  const rememberFeedScroll = () => {
    clearTimeout(feedScrollTimer);
    try { history.replaceState({ ...history.state, chunnelScroll: window.scrollY || 0 }, ''); } catch {}
  };
  const placeFeed = () => {
    const panel = $('#latest');
    if (!panel) return;
    const top = panel.getBoundingClientRect().top + (window.scrollY || 0);
    const target = Math.max(0, top - 96);
    if (Math.abs((window.scrollY || 0) - target) < 2) return;
    window.scrollTo({ top: target, behavior: 'instant' });
  };
  const loadFeed = async (url: string, { history: mode = 'push', scroll = false }: { history?: 'push' | 'replace' | 'none'; scroll?: boolean } = {}) => {
    const panel = $('#latest');
    if (!panel) { location.assign(url); return; }
    clearTimeout(feedScrollTimer);
    const restoreTop = Number(history.state?.chunnelScroll) || 0;
    const initialTop = window.scrollY || 0;
    if (mode === 'none') history.scrollRestoration = 'manual';
    if (mode !== 'none') rememberFeedScroll();
    const request = new AbortController();
    feedRequest?.abort();
    feedRequest = request;
    panel.setAttribute('aria-busy', 'true');
    try {
      const response = await fetch(url, { credentials: 'same-origin', headers: { Accept: 'text/html' }, signal: request.signal });
      if (!response.ok || !(response.headers.get('content-type') || '').includes('text/html')) throw new Error('feed unavailable');
      const doc = new DOMParser().parseFromString(await response.text(), 'text/html');
      if (request.signal.aborted || feedRequest !== request) return;
      const next = doc.querySelector<HTMLElement>('#latest');
      if (!next) throw new Error('feed missing');
      await ensureBadgeStyles(doc, next);
      if (request.signal.aborted || feedRequest !== request) return;
      if (mode !== 'none') {
        rememberFeedScroll();
        history[mode === 'replace' ? 'replaceState' : 'pushState']({ ...history.state }, '', url);
      }
      const commit = () => {
        const active = document.activeElement;
        const focusHref = active && panel.contains(active) ? active.closest<HTMLAnchorElement>('a[href]')?.href : null;
        const focusGroup = focusHref && active?.closest('nav')?.getAttribute('aria-label');
        panel.className = next.className;
        panel.innerHTML = next.innerHTML;
        feedLocation = location.pathname + location.search;
        document.title = doc.title;
        window.ChunnelHome?.refreshFeed();
        // Replacing the list removes the keyboard's current control. Restore it
        // only when it still owned focus at commit time, without moving scroll.
        if (focusHref) {
          let target: HTMLElement | null | undefined = [...panel.querySelectorAll<HTMLAnchorElement>('a[href]')].find(link => link.href === focusHref
            && (!focusGroup || link.closest('nav')?.getAttribute('aria-label') === focusGroup));
          if (!target) {
            target = panel.querySelector('h2');
            target?.setAttribute('tabindex', '-1');
          }
          target?.focus({ preventScroll: true });
        }
      };
      const position = mode === 'none' && (window.scrollY || 0) === initialTop ? { top: restoreTop } : {};
      if (window.ChunnelScroll) window.ChunnelScroll.keep(commit, position);
      else commit();
      if (scroll) placeFeed();
    } catch (error) {
      if ((error instanceof Error && error.name === 'AbortError') || request.signal.aborted || feedRequest !== request) return;
      if (mode === 'none') history.replaceState(history.state, '', feedLocation);
      window.ChunnelScroll?.error(panel, '内容加载失败，已保留当前列表，请再次点击重试。');
    } finally {
      if (feedRequest === request) {
        feedRequest = null;
        history.scrollRestoration = 'auto';
        $('#latest')?.removeAttribute('aria-busy');
        rememberFeedScroll();
      }
    }
  };
  const applyFeedView = (url: string, { traversal = false } = {}) => {
    const target = new URL(url, location.href);
    const current = new URL(feedLocation, location.origin);
    const view = target.searchParams.get('view');
    if (view !== 'list' && view !== 'grid') return false;
    const query = (value: URL) => { const copy = new URL(value); copy.searchParams.delete('view'); copy.searchParams.sort(); return copy.pathname + copy.search; };
    if (query(target) !== query(current)) return false;
    const panel = $('#latest'), feed = panel?.querySelector('.home-feed');
    if (!panel || !feed || !window.ChunnelScroll) return false;
    const top = Number(history.state?.chunnelScroll) || 0;
    feedRequest?.abort(); feedRequest = null;
    panel.removeAttribute('aria-busy');
    if (!traversal) {
      rememberFeedScroll();
      history.pushState({ ...history.state }, '', target);
    }
    window.ChunnelScroll.keep(() => {
      feed.classList.toggle('home-feed-list', view === 'list');
      feed.classList.toggle('home-feed-grid', view === 'grid');
      panel.querySelectorAll<HTMLAnchorElement>('.home-view-switch a').forEach(link => {
        if (new URL(link.href).searchParams.get('view') === view) link.setAttribute('aria-current', 'true');
        else link.removeAttribute('aria-current');
      });
      panel.querySelectorAll<HTMLAnchorElement>('.home-filters a, .home-sort-links a, .pagination a').forEach(link => {
        const next = new URL(link.href);
        next.searchParams.set('view', view);
        link.href = next.href;
      });
    }, traversal ? { top } : {});
    feedLocation = location.pathname + location.search;
    rememberFeedScroll();
    history.scrollRestoration = 'auto';
    return true;
  };
  if (feedPanel) {
    const feedLinks = '#latest .home-filters a, #latest .home-sort-links a, #latest .home-view-switch a, #latest .pagination a';
    document.addEventListener('click', (event) => {
      if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      if (!(event.target instanceof Element) || !event.target.closest(feedLinks)) return;
      const link = event.target.closest<HTMLAnchorElement>('a[href]');
      if (!link || (link.target && link.target !== '_self') || link.hasAttribute('download')) return;
      const url = new URL(link.href, location.href);
      if (url.origin !== location.origin || url.pathname !== '/') return;
      event.preventDefault();
      if (url.href === location.href && !feedRequest) return;
      if (link.closest('.home-view-switch') && applyFeedView(url.href)) return;
      loadFeed(url.href, { history: url.href === location.href ? 'replace' : 'push', scroll: !!link.closest('.pagination') });
    });
    window.addEventListener('scroll', () => {
      clearTimeout(feedScrollTimer);
      if (!feedRequest) feedScrollTimer = setTimeout(rememberFeedScroll, 150);
    }, { passive: true });
    window.addEventListener('popstate', () => {
      if (location.pathname === '/' && (feedRequest || feedLocation !== location.pathname + location.search)) {
        if (applyFeedView(location.href, { traversal: true })) return;
        loadFeed(location.href, { history: 'none', scroll: false });
      }
    });
    window.addEventListener('pagehide', () => { clearTimeout(feedScrollTimer); feedRequest?.abort(); });
  }

  window.ChunnelHomeFeedReady = true;
  return { loadFeed, applyFeedView, ensureBadgeStyles };
}
