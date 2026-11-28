import { requestDeadline } from './async-state';
// Opt-in public list navigation. Keep the site shell and unrelated components
// alive; only replace a region whose template explicitly declares this contract.
(() => {
  'use strict';
  const region = document.querySelector<HTMLElement>('[data-list-page]');
  if (!region || !window.ChunnelScroll) return;
  const kind = region.dataset.listPage;
  const initialPath = location.pathname;
  const family = initialPath.split('/')[1];
  const accepts = (url: URL) => url.origin === location.origin && (
    kind === 'archive' ? ['categories', 'tags'].includes(family || '') && url.pathname.startsWith(`/${family}/`) : url.pathname === initialPath
  );
  const scroll = window.ChunnelScroll;
  let request: AbortController | null = null;
  let current = location.pathname + location.search;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const remember = () => { clearTimeout(timer); scroll.remember(); };
  const load = async (url: string, { traversal = false, page = false, focus = null }: {traversal?: boolean; page?: boolean; focus?: {href?: string; name?: string} | null} = {}) => {
    clearTimeout(timer);
    const top = Number(history.state?.chunnelScroll) || 0;
    const initialTop = window.scrollY || 0;
    const drafts = new Map([...region.querySelectorAll<HTMLInputElement>('input[type="search"][name]')].map(input => [input.name, input.value]));
    if (traversal) history.scrollRestoration = 'manual';
    else remember();
    request?.abort();
    const pending = new AbortController();
    const deadline = requestDeadline(pending);
    request = pending;
    region.setAttribute('aria-busy', 'true');
    try {
      const response = await fetch(url, { credentials: 'same-origin', headers: { Accept: 'text/html' }, signal: pending.signal });
      if (!response.ok || !accepts(new URL(response.url || url, location.href)) || !(response.headers.get('content-type') || '').includes('text/html')) throw new Error('list unavailable');
      const doc = new DOMParser().parseFromString(await response.text(), 'text/html');
      if (pending.signal.aborted || request !== pending) return;
      const next = doc.querySelector(`[data-list-page="${kind}"]`);
      if (!next) throw new Error('list missing');
      if (!traversal) {
        region.querySelectorAll<HTMLInputElement>('input[type="search"][name]').forEach(input => {
          if (input.value === drafts.get(input.name)) return;
          const target = [...next.querySelectorAll<HTMLInputElement>('input[type="search"][name]')].find(field => field.name === input.name);
          if (target) target.value = input.value;
        });
      }
      if (!traversal) {
        remember();
        history.pushState({ ...history.state }, '', url);
      }
      scroll.keep(() => {
        region.replaceChildren(...next.childNodes);
        document.title = doc.title;
        const canonical = document.querySelector<HTMLLinkElement>('link[rel="canonical"]');
        const nextCanonical = doc.querySelector<HTMLLinkElement>('link[rel="canonical"]');
        if (canonical && nextCanonical) canonical.href = nextCanonical.href;
        if (focus) {
          const target = [...region.querySelectorAll<HTMLAnchorElement|HTMLInputElement>('a[href], input[name]:not([type="hidden"])')].find(el => focus.href && el instanceof HTMLAnchorElement && el.href === focus.href || focus.name && el.name === focus.name);
          target?.focus({ preventScroll: true });
        }
        document.dispatchEvent(new CustomEvent('chunnel:guest-preferences'));
      }, traversal && (window.scrollY || 0) === initialTop ? { top } : {});
      current = location.pathname + location.search;
      if (page) scroll.restore(region.getBoundingClientRect().top + window.scrollY - 96);
    } catch (error) {
      if (request !== pending || pending.signal.aborted && !deadline.timedOut) return;
      if (traversal) history.replaceState(history.state, '', current);
      scroll.error(region, deadline.timedOut ? '加载超时，已保留当前列表，请再次操作重试。' : '内容加载失败，已保留当前列表，请再次操作重试。');
    } finally {
      deadline.dispose();
      if (request === pending) {
        request = null;
        history.scrollRestoration = 'auto';
        region.removeAttribute('aria-busy');
        remember();
      }
    }
  };
  region.addEventListener('click', event => {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const link = event.target instanceof Element && event.target.closest<HTMLAnchorElement>('a[href]');
    if (!link || link.hasAttribute('download') || link.target && link.target !== '_self') return;
    const url = new URL(link.href, location.href);
    if (!accepts(url) || url.hash && url.pathname + url.search === current) return;
    event.preventDefault();
    if (url.pathname + url.search === current && !request) return;
    load(url.href, { page: !!link.closest('.portal-pagination'), focus: event.detail === 0 ? { href: url.href } : null });
  });
  region.addEventListener('submit', event => {
    const form = event.target;
    if (event.defaultPrevented || !(form instanceof HTMLFormElement) || form.method.toLowerCase() !== 'get' || form.target && form.target !== '_self') return;
    const url = new URL(form.action, location.href);
    if (!accepts(url)) return;
    event.preventDefault();
    url.search = '';
    for (const [name, value] of new FormData(form)) if (String(value).trim()) url.searchParams.append(name, String(value));
    const active = document.activeElement;
    load(url.href, { focus: active instanceof HTMLInputElement && form.contains(active) && active.name ? { name: active.name } : null });
  });
  window.addEventListener('popstate', () => {
    if (accepts(new URL(location.href)) && (request || current !== location.pathname + location.search)) load(location.href, { traversal: true });
  });
  window.addEventListener('scroll', () => {
    clearTimeout(timer);
    if (!request) timer = setTimeout(remember, 150);
  }, { passive: true });
  const suspend = () => { clearTimeout(timer); request?.abort(); request = null; region.removeAttribute('aria-busy'); history.scrollRestoration = 'auto'; };
  window.addEventListener('pagehide', suspend);
  window.addEventListener('pageshow', event => { if (event.persisted) suspend(); });
})();
