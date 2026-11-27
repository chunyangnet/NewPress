import type { ExperienceDOM } from './experience-types';
import { experienceError } from './experience-data';
export function createDiscovery(discovery: HTMLElement | null, dom: Pick<ExperienceDOM,'$'|'$$'|'optional'>, refreshPreferences: () => void) {
  const {$$,optional} = dom;
  const discoveryRegion = () => discovery?.querySelector<HTMLElement>('[data-discovery-results]') || null;
  const discoveryStatus = () => discovery?.querySelector<HTMLElement>('[data-discovery-status]') || null;
  let discoveryRequest: AbortController | null = null;
  let discoveryLocation = location.pathname + location.search;
  let discoveryScrollTimer: ReturnType<typeof setTimeout> | undefined;
  const rememberDiscoveryScroll = () => {
    clearTimeout(discoveryScrollTimer);
    try { history.replaceState({ ...history.state, chunnelScroll: window.scrollY || 0 }, ''); } catch {}
  };

  function withCurrentView(href: string) {
    const url = new URL(href, location.href);
    const view = new URL(location.href).searchParams.get('view') === 'list' ? 'list' : 'grid';
    if (view === 'list') url.searchParams.set('view', 'list'); else url.searchParams.delete('view');
    return url;
  }

  function syncDiscoveryView(revealCategory = false) {
    const view = new URL(location.href).searchParams.get('view') === 'list' ? 'list' : 'grid';
    const results = optional('[data-results-view]');
    if (results) results.dataset.resultsView = view;
    $$('[data-view-toggle]').forEach((button) => {
      const active = button.dataset.viewToggle === view;
      button.classList.toggle('is-active', active);
      button.setAttribute('aria-pressed', String(active));
    });
    $$<HTMLInputElement>('[data-discovery-view]').forEach((input) => { input.value = view; });
    if (!discovery) return;
    if (revealCategory) {
      // The server replaces the filter strip. Reveal its current link without scrolling the page.
      const strip = discovery.querySelector<HTMLElement>('.explore-chips');
      const current = strip?.querySelector('[aria-current]');
      if (current && strip && strip.scrollWidth > strip.clientWidth) {
        const item = current.getBoundingClientRect(), viewport = strip.getBoundingClientRect();
        if (item.left < viewport.left) strip.scrollLeft += item.left - viewport.left;
        else if (item.right > viewport.right) strip.scrollLeft += item.right - viewport.right;
      }
    }
    discovery.querySelectorAll<HTMLAnchorElement>('[data-discovery-results] a[href]').forEach((link) => {
      const target = new URL(link.href, location.href);
      if (target.origin !== location.origin || target.pathname !== '/explore/') return;
      if (!target.search) return;
      const url = withCurrentView(target.href);
      link.href = url.pathname + url.search + link.hash;
    });
    discoveryLocation = location.pathname + location.search;
  }

  function placeDiscovery(userInitiated: boolean) {
    const region = discoveryRegion();
    if (!region) return;
    const top = region.getBoundingClientRect().top + (window.scrollY || 0);
    const target = Math.max(0, top - 96);
    if (!userInitiated || Math.abs((window.scrollY || 0) - target) < 2) return;
    window.scrollTo({ top: target, behavior: 'instant' });
  }

  async function loadDiscovery(url: string, { history: mode = 'push', focus = null, scroll = false }: {history?: 'push' | 'replace' | 'none'; focus?: string | null; scroll?: boolean} = {}): Promise<void> {
    if (!discovery) return;
    const region = discoveryRegion() || discovery;
    clearTimeout(discoveryScrollTimer);
    const restoreTop = Number(history.state?.chunnelScroll) || 0;
    const initialTop = window.scrollY || 0;
    if (mode === 'none') history.scrollRestoration = 'manual';
    const queryDraft = optional<HTMLInputElement>('#explore-query')?.value;
    if (mode !== 'none') rememberDiscoveryScroll();
    const request = new AbortController();
    discoveryRequest?.abort();
    discoveryRequest = request;
    discovery.classList.add('is-loading');
    region.setAttribute('aria-busy', 'true');
    try {
      const response = await fetch(url, { credentials: 'same-origin', headers: { Accept: 'text/html' }, signal: request.signal });
      if (!response.ok || !(response.headers.get('content-type') || '').includes('text/html')) throw new Error('discovery unavailable');
      const doc = new DOMParser().parseFromString(await response.text(), 'text/html');
      if (request.signal.aborted || discoveryRequest !== request) return;
      const nextPage = doc.querySelector<HTMLElement>('[data-discovery]');
      const next = doc.querySelector<HTMLElement>('[data-discovery-results]') || nextPage;
      if (!next) throw new Error('discovery results missing');
      if (mode !== 'none') {
        rememberDiscoveryScroll();
        history[mode === 'replace' ? 'replaceState' : 'pushState']({ ...history.state }, '', url);
      }
      const commit = () => {
        const active = document.activeElement;
        if (nextPage && nextPage.dataset.discoveryMode !== discovery.dataset.discoveryMode) {
          discovery.replaceChildren(...nextPage.childNodes);
          discovery.dataset.discoveryMode = nextPage.dataset.discoveryMode || '';
        } else {
          region.replaceWith(next);
        }
        // Keep the live header and its listeners; copy only server-selected states.
        for (const selector of ['#primary-nav', '.utility-nav', '.mobile-bottom-nav']) {
          const live = document.querySelector(selector), rendered = doc.querySelector(selector);
          if (!live || !rendered) continue;
          const oldNodes = live.querySelectorAll('[class*="nav-item-"]');
          const newNodes = rendered.querySelectorAll('[class*="nav-item-"]');
          oldNodes.forEach((item, i) => {
            const updated = newNodes[i];
            if (!updated || item.getAttribute('href') !== updated.getAttribute('href')) return;
            for (const name of ['is-active', 'is-current']) item.classList.toggle(name, updated.classList.contains(name));
          });
          const links = live.querySelectorAll('a'), nextLinks = rendered.querySelectorAll('a');
          links.forEach((link, i) => {
            const updated = nextLinks[i];
            if (!updated || link.getAttribute('href') !== updated.getAttribute('href')) return;
            if (updated.hasAttribute('aria-current')) link.setAttribute('aria-current', updated.getAttribute('aria-current') || 'page');
            else link.removeAttribute('aria-current');
          });
        }
        const canonical = document.querySelector<HTMLLinkElement>('link[rel="canonical"]');
        const updatedCanonical = doc.querySelector<HTMLLinkElement>('link[rel="canonical"]');
        if (canonical && updatedCanonical) canonical.href = updatedCanonical.href;
        document.title = doc.title;
        const query = doc.querySelector<HTMLInputElement>('#explore-query');
        const field = optional<HTMLInputElement>('#explore-query');
        if (query && field && (field.value === queryDraft || mode === 'none')) field.value = query.value;
        const form = discovery.querySelector('[data-discovery-search]');
        const nextForm = doc.querySelector('[data-discovery-search]');
        if (form && nextForm) {
          form.querySelectorAll('input[type="hidden"]').forEach((input) => input.remove());
          nextForm.querySelectorAll('input[type="hidden"]').forEach((input) => form.append(input.cloneNode(true)));
        }
        for (const selector of ['.explore-hero h1 .portal-count', '.explore-facts']) {
          const current = discovery.querySelector(selector), updated = doc.querySelector(selector);
          if (current && updated) current.innerHTML = updated.innerHTML;
        }
        syncDiscoveryView(true);
        refreshPreferences();
        const summary = next.querySelector('[data-content-summary]');
        const status = discoveryStatus();
        if (status) status.textContent = summary ? summary.textContent?.trim() || '' : '内容已更新';
        // A slow response must not pull focus away from another field or dialog.
        // Restore only controls actually removed by this update.
        if (active && !active.isConnected) {
          const controls = [...discovery.querySelectorAll<HTMLElement>('a[href], input[name], select[name], button[type="submit"]')];
          let target = controls.find(control => active.id && control.id === active.id
            || active.getAttribute('href') && control.getAttribute('href') === active.getAttribute('href')
            || active.getAttribute('name') && control.getAttribute('name') === active.getAttribute('name'));
          if (!target && active.matches('button[type="submit"]')) target = discovery.querySelector<HTMLElement>('[data-discovery-sort] button[type="submit"]') || undefined;
          if (!target && focus) target = discovery.querySelector<HTMLElement>(focus) || undefined;
          if (!target) {
            target = discovery.querySelector<HTMLElement>('h1, h2') || undefined;
            target?.setAttribute('tabindex', '-1');
          }
          target?.focus({ preventScroll: true });
        }
      };
      const position = mode === 'none' && (window.scrollY || 0) === initialTop ? { top: restoreTop } : {};
      if (window.ChunnelScroll) window.ChunnelScroll.keep(commit, position);
      else commit();
      if (scroll) placeDiscovery(true);
    } catch (error) {
      if (experienceError(error).name === 'AbortError' || request.signal.aborted || discoveryRequest !== request) return;
      if (mode === 'none') history.replaceState(history.state, '', discoveryLocation);
      window.ChunnelScroll?.error(region, '内容加载失败，已保留当前列表，请再次操作重试。');
    } finally {
      if (discoveryRequest === request) {
        discoveryRequest = null;
        history.scrollRestoration = 'auto';
        discovery.classList.remove('is-loading');
        discoveryRegion()?.removeAttribute('aria-busy');
        rememberDiscoveryScroll();
      }
    }
  }

  function mount(): void {
  if (discovery) {
    discovery.addEventListener('click', (event) => {
      if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      if (!(event.target instanceof Element)) return;
      if (event.target.closest('[data-view-toggle]') && discoveryRequest) {
        discoveryRequest.abort();
        discoveryRequest = null;
        history.scrollRestoration = 'auto';
        discovery.classList.remove('is-loading');
        discoveryRegion()?.removeAttribute('aria-busy');
      }
      const link = event.target.closest<HTMLAnchorElement>('a[href]');
      if (!link || (link.target && link.target !== '_self') || link.hasAttribute('download')) return;
      const url = new URL(link.href, location.href);
      if (url.origin !== location.origin || url.pathname !== '/explore/') return;
      event.preventDefault();
      if (url.href === location.href && !discoveryRequest) return;
      const chip = link.closest('.explore-chips');
      loadDiscovery(url.href, { history: url.href === location.href ? 'replace' : 'push', focus: chip ? '.explore-chips a[aria-current]' : null, scroll: !!link.closest('.portal-pagination') });
    });
    discovery.addEventListener('submit', (event) => {
      const form = event.target;
      if (!(form instanceof HTMLFormElement) || !form.matches('[data-discovery-search], [data-discovery-sort]')) return;
      event.preventDefault();
      const url = new URL(form.getAttribute('action') || location.pathname, location.href);
      url.search = '';
      for (const [name, value] of new FormData(form)) if (String(value).trim()) url.searchParams.set(name, String(value));
      loadDiscovery(url.href, { scroll: form.matches('[data-discovery-search]') });
    });
    window.addEventListener('scroll', () => {
      clearTimeout(discoveryScrollTimer);
      if (!discoveryRequest) discoveryScrollTimer = setTimeout(rememberDiscoveryScroll, 150);
    }, { passive: true });
    window.addEventListener('popstate', () => {
      if (location.pathname === '/explore/' && (discoveryRequest || discoveryLocation !== location.pathname + location.search)) {
        loadDiscovery(location.href, { history: 'none', scroll: false });
      }
    });
    window.addEventListener('pagehide', () => { clearTimeout(discoveryScrollTimer); discoveryRequest?.abort(); });
  }
  document.addEventListener('click', (event) => {
    if (event.target instanceof Element && event.target.closest('[data-view-toggle]')) syncDiscoveryView();
  });
  syncDiscoveryView(true);


  }
  return {loadDiscovery, syncDiscoveryView, mount};
}
