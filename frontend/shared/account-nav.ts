import type {} from './account-types';

// Account tabs replace the URL in place; synchronize dock links and glass lens.
export function mountAccountNav(): void {
  const bar = document.querySelector<HTMLElement & { chunnelSelect?: () => void }>('.mobile-bottom-nav');
  if (!bar) return;
  const links = [...bar.querySelectorAll<HTMLAnchorElement>('a[href]')];
  const query = (url: URL): string => {
    decodeURIComponent(url.search);
    if (url.search.includes(';')) throw new Error('Invalid query');
    const value = new URLSearchParams(url.search); value.sort(); return value.toString();
  };
  const update = (): void => {
    const current = new URL(location.href);
    let best: HTMLAnchorElement | null = null, score = 0;
    for (const link of links) {
      try {
        const raw = link.getAttribute('href');
        if (!raw?.startsWith('/') || raw.startsWith('//')) continue;
        const target = new URL(raw, current), path = decodeURIComponent(target.pathname), here = decodeURIComponent(current.pathname);
        if (target.origin !== current.origin || target.hash || path !== here && !(path !== '/' && path.endsWith('/') && here.startsWith(path))) continue;
        if (target.search && query(target) !== query(current)) continue;
        const match = path.length * 2 + Number(!!target.search);
        if (match > score) { best = link; score = match; }
      } catch { /* Invalid links retain only their native navigation. */ }
    }
    for (const link of links) {
      link.classList.toggle('is-current', link === best);
      if (link === best) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current');
    }
    bar.chunnelSelect?.();
  };
  window.ChunnelAccountNav = update;
  window.addEventListener('pageshow', update);
  update();
}
mountAccountNav();
