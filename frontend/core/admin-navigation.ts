// Server-rendered admin filters update only declared regions. Editors, uploads,
// the sidebar and page controllers outside those regions keep their live state.
(() => {
  'use strict';
  const main = document.querySelector<HTMLElement>('.admin-main');
  const scroll = window.ChunnelScroll;
  if (!main || !scroll || main.querySelector('.feature-recovery')) return;
  const configurations: Record<string, { parts: string[]; keys: string[]; hash?: string }> = {
    '/admin/content/': { parts: ['.admin-content-page'], keys: ['type', 'status', 'q', 'page'] },
    '/admin/comments/': { parts: ['.admin-comments-page'], keys: ['status', 'q', 'post', 'page'] },
    '/admin/media/': { parts: ['.admin-media-status-tabs', '.admin-media-library'], keys: ['status', 'kind', 'q', 'page'] },
    '/admin/users/': { parts: ['.admin-users-index'], keys: ['role', 'status', 'q', 'page'] },
    '/admin/taxonomy/': { parts: ['.admin-taxonomy-index'], keys: ['taxonomy', 'q', 'page'] },
    '/admin/analytics/': { parts: ['[data-analytics]'], keys: ['days'] },
    '/admin/sessions/': { parts: ['.admin-sessions-page'], keys: ['scope'] },
    '/admin/series/': { parts: ['.series-admin-tools', '.series-admin-list'], keys: ['q', 'kind'] },
    '/admin/seo/': { parts: ['#redirects'], keys: ['q', 'page'], hash: '#redirects' },
    '/admin/tools/': { parts: ['#audit-events'], keys: ['audit_action', 'audit_object_type', 'audit_cursor'], hash: '#audit-events' },
    '/admin/plugins/updates/': { parts: ['.extension-main'], keys: ['channel'] },
    '/admin/appearance/updates/': { parts: ['.extension-main'], keys: ['channel'] }
  };
  const path = location.pathname;
  const config = configurations[path] || (/^\/admin\/content\/\d+\/revisions\/compare\/$/.test(path) ? { parts: ['.admin-revision-page'], keys: ['from', 'to'] } : null);
  if (!config) return;
  // The report island owns days/history while mounted. Native navigation stays
  // available when its bundle fails or the island returns to the Go report.
  const reactOwnsAnalytics = () => path === '/admin/analytics/' && main.querySelector('[data-analytics-root]')?.getAttribute?.('data-react-ready') === 'true';
  const accepts = (url: URL) => url.origin === location.origin && url.pathname === path &&
    [...url.searchParams.keys()].every(key => key === 'notice' || config.keys.includes(key));
  // An open inspector/editor is a distinct document, never a list fragment.
  if (!accepts(new URL(location.href))) return;
  const owner = document.querySelector<HTMLElement>('[data-admin-sidebar]')?.dataset.account;
  const revision = document.querySelector('link[rel="stylesheet"]')?.getAttribute('href');
  let current = location.pathname + location.search;
  let request: AbortController | null = null, timer: ReturnType<typeof setTimeout> | undefined;
  let committedState: unknown = history.state;
  const historyState = (): Record<string, unknown> => {
    const state: unknown = history.state;
    return state && typeof state === 'object' && !Array.isArray(state) ? state as Record<string, unknown> : {};
  };
  const remember = () => {
    clearTimeout(timer);
    scroll.remember();
    committedState = history.state;
  };
  const partsFor = (url: URL) => path === '/admin/taxonomy/' &&
    (url.searchParams.get('taxonomy') || 'category') !== (new URL(current, location.origin).searchParams.get('taxonomy') || 'category')
    ? ['.admin-taxonomy-page'] : config.parts;
  const permitted = (regions: Element[]) => {
    if (reactOwnsAnalytics()) return false;
    if (regions.some(region => region.querySelector('form[aria-busy="true"]'))) return false;
    return document.dispatchEvent(new CustomEvent('chunnel:admin-before-update', { cancelable: true, detail: { regions } }));
  };
  const showError = (message: string) => {
    let status = main.querySelector<HTMLElement>('[data-admin-navigation-error]');
    if (!status) {
      status = document.createElement('p');
      status.dataset.adminNavigationError = '';
      status.className = 'admin-navigation-feedback';
      status.setAttribute('role', 'status');
      main.prepend(status);
    }
    status.textContent = message;
  };
  type FocusTarget = { href: string } | { name: string };
  const load = async (target: string, { traversal = false, focus = null }: { traversal?: boolean; focus?: FocusTarget | null } = {}) => {
    const url = new URL(target, location.href);
    const parts = partsFor(url);
    const regions = parts.map(selector => main.querySelector<HTMLElement>(selector));
    if (!regions.every((region): region is HTMLElement => region !== null)) return;
    request?.abort();
    request = null;
    if (!permitted(regions)) {
      clearTimeout(timer);
      main.removeAttribute('data-navigation-loading');
      history.scrollRestoration = 'auto';
      if (traversal) history.replaceState(committedState, '', current);
      showError('请先保存或放弃当前区域的修改，再切换。');
      return;
    }
    const destinationTop = Number(historyState().chunnelScroll) || 0;
    const initialTop = window.scrollY || 0;
    if (!traversal) remember();
    else history.scrollRestoration = 'manual';
    clearTimeout(timer);
    const pending = new AbortController();
    request = pending;
    const timeout = setTimeout(() => pending.abort(), 15000);
    main.setAttribute('data-navigation-loading', '');
    const searchFields = 'form:is([method="get" i], :not([method])) input:not([type="hidden"])';
    const drafts = new Map(regions.flatMap(region => [...region.querySelectorAll<HTMLInputElement>(searchFields)].map(input => [input.name, input.value])));
    try {
      const response = await fetch(url.href, { credentials: 'same-origin', cache: 'no-store', headers: { Accept: 'text/html' }, signal: pending.signal });
      if (!response.ok || !accepts(new URL(response.url || url.href)) || !(response.headers.get('content-type') || '').includes('text/html')) throw new Error('response');
      const doc = new DOMParser().parseFromString(await response.text(), 'text/html');
      if (request !== pending || pending.signal.aborted) return;
      if (doc.querySelector<HTMLElement>('[data-admin-sidebar]')?.dataset.account !== owner || doc.querySelector('link[rel="stylesheet"]')?.getAttribute('href') !== revision) throw new Error('identity');
      const incoming = parts.map(selector => doc.querySelector<HTMLElement>(selector));
      if (!incoming.every((region): region is HTMLElement => region !== null) || !permitted(regions)) throw new Error('region');
      // Input entered while a slow search is running belongs to the user.
      if (!traversal) regions.forEach((region, index) => region.querySelectorAll<HTMLInputElement>(searchFields).forEach(input => {
        if (input.value === drafts.get(input.name)) return;
        const next = [...(incoming[index]?.querySelectorAll<HTMLInputElement>('input[name]') || [])].find(field => field.name === input.name);
        if (next) next.value = input.value;
      }));
      if (!traversal) {
        remember();
        if (url.pathname + url.search !== current) history.pushState({ ...historyState() }, '', url.pathname + url.search);
      }
      const top = traversal && (window.scrollY || 0) === initialTop ? destinationTop : window.scrollY || 0;
      // Reserve only enough room to retain the current viewport when results shrink.
      main.style.minHeight = `${Math.max(0, top + window.innerHeight - (main.getBoundingClientRect().top + window.scrollY))}px`;
      scroll.keep(() => {
        document.dispatchEvent(new CustomEvent('chunnel:admin-before-replace', { detail: { regions } }));
        regions.forEach((region, index) => { const next = incoming[index]; if (next) region.replaceWith(next); });
        main.querySelector('[data-admin-navigation-error]')?.remove();
        document.title = doc.title;
        document.dispatchEvent(new CustomEvent('chunnel:admin-updated', { detail: { regions: incoming, url: url.href } }));
        incoming.forEach(region => window.ChunnelEmoji?.enhance(region));
        if (focus) {
          const controls = incoming.flatMap(region => [...region.querySelectorAll<HTMLAnchorElement | HTMLInputElement | HTMLSelectElement | HTMLButtonElement>('a[href], input[name], select[name], button')]);
          const match = controls.find(node => 'href' in focus ? 'href' in node && node.href === focus.href : node.name === focus.name);
          match?.focus({ preventScroll: true });
        }
      }, { top });
      current = location.pathname + location.search;
    } catch (error) {
      if (request !== pending) return;
      if (traversal) history.replaceState(committedState, '', current);
      showError(pending.signal.aborted ? '加载超时，已保留当前内容，请重试。' : '内容未能更新，已保留当前页面和输入。请重试；若登录或版本已变化，请刷新页面。');
    } finally {
      clearTimeout(timeout);
      if (request === pending) {
        request = null;
        main.removeAttribute('data-navigation-loading');
        history.scrollRestoration = 'auto';
        remember();
      }
    }
  };
  main.addEventListener('click', event => {
    if (reactOwnsAnalytics()) return;
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const link = event.target instanceof Element ? event.target.closest<HTMLAnchorElement>('a[href]') : null;
    if (!link || link.hasAttribute('download') || link.target && link.target !== '_self') return;
    const url = new URL(link.href, location.href);
    if (!accepts(url) || url.hash && url.hash !== config.hash) return;
    if (url.hash && url.pathname + url.search === current) return;
    event.preventDefault();
    load(url.href, { focus: event.detail === 0 ? { href: url.href } : null });
  });
  main.addEventListener('submit', event => {
    if (reactOwnsAnalytics()) return;
    const form = event.target;
    if (event.defaultPrevented || !(form instanceof HTMLFormElement) || form.method.toLowerCase() !== 'get' || form.target && form.target !== '_self') return;
    const url = new URL(form.action, location.href);
    url.search = '';
    for (const [name, value] of new FormData(form, event.submitter)) if (String(value).trim()) url.searchParams.append(name, String(value));
    if (!accepts(url)) return;
    event.preventDefault();
    const active = document.activeElement;
    load(url.href, { focus: active && 'name' in active && typeof active.name === 'string' && active.name && form.contains(active) ? { name: active.name } : null });
  });
  window.addEventListener('popstate', () => {
    if (reactOwnsAnalytics()) return;
    if (accepts(new URL(location.href)) && (request || current !== location.pathname + location.search)) load(location.href, { traversal: true });
  });
  window.addEventListener('scroll', () => {
    clearTimeout(timer);
    if (!request) timer = setTimeout(remember, 100);
  }, { passive: true });
  window.addEventListener('pagehide', () => { clearTimeout(timer); request?.abort(); request = null; });
  window.addEventListener('pageshow', event => { if (event.persisted) main.removeAttribute('data-navigation-loading'); });
})();
