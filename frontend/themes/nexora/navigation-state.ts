const normalizePath = (path: string) => path.replace(/\/+$/, '') || '/';

export function linkTarget(href: string, current: URL) {
  try {
    const target = new URL(href, current);
    return target.origin === current.origin && /^https?:$/.test(target.protocol) ? target : null;
  } catch { return null; }
}

// A single current destination, even when the server also marks its ancestors.
export function currentNavigation(items: {href: string; sectionFallback?: boolean}[], current: URL, section = ''): {index: number; kind: string} | null {
  let winner: {index: number; kind: string} | null = null;
  let best = 0;
  items.forEach((item, index) => {
    const target = linkTarget(item.href, current);
    if (!target) return;
    const path = normalizePath(target.pathname);
    const currentPath = normalizePath(current.pathname);
    const queryMatches = [...target.searchParams].every(([name, value]) => current.searchParams.getAll(name).includes(value));
    if (!queryMatches) return;
    let score = 0;
    let kind = 'page';
    if (target.hash) {
      let hash;
      try { hash = decodeURIComponent(target.hash.slice(1)); } catch { return; }
      if (path === currentPath && hash === section) { score = 3000; kind = 'location'; }
    } else if (path === currentPath) {
      score = 2000 + path.length + [...target.searchParams].length * 100;
      if (section && path === '/') score = 0;
    } else if (path !== '/' && target.pathname.endsWith('/') && currentPath.startsWith(`${path}/`)) {
      score = 1000 + path.length + [...target.searchParams].length * 100;
    } else if (item.sectionFallback) {
      score = 1;
    }
    if (score > best) { best = score; winner = { index, kind }; }
  });
  return winner;
}

export function visibleSection(sections: {id: string; top: number}[], marker: number, viewportHeight: number, atBottom: boolean) {
  let active = '';
  for (const section of [...sections].sort((a, b) => a.top - b.top)) {
    if (section.top <= marker || (atBottom && section.top < viewportHeight)) active = section.id;
  }
  return active;
}

export function initNavigationState(doc: Document, win: Window & typeof globalThis) {
  const header = doc.querySelector<HTMLElement>('[data-header]');
  const menu = doc.querySelector('#primary-nav');
  if (!header || !menu) return;
  const links = [...menu.querySelectorAll('a[href]')];
  const groups = [...menu.querySelectorAll('.nav-group')];
  const current = new URL(win.location.href);
  const isNewsPage = ['single', 'explore', 'archive', 'author'].some(kind => doc.body.classList.contains(`kind-${kind}`));
  const items = links.map(link => {
    const href = link.getAttribute('href') || '';
    const target = linkTarget(href, current);
    const newsFallback = isNewsPage && target && normalizePath(target.pathname) === '/explore' && !target.hash && !target.search;
    return { href, sectionFallback: link.hasAttribute('data-nav-section') || !!newsFallback };
  });
  const targets = new Map<string, HTMLElement>();
  for (const item of items) {
    const target = linkTarget(item.href, current);
    if (!target?.hash || normalizePath(target.pathname) !== normalizePath(current.pathname)) continue;
    try {
      const id = decodeURIComponent(target.hash.slice(1));
      const element = doc.getElementById(id);
      if (element) targets.set(id, element);
    } catch { /* A malformed fragment must not stop other navigation links. */ }
  }
  header.classList.toggle('has-hero', !!doc.querySelector('[data-hero]'));
  let scheduled = false;
  let last = '';
  const update = () => {
    scheduled = false;
    header.classList.toggle('is-scrolled', win.scrollY > 24);
    const height = header.dataset.sticky === 'false' ? 0 : header.getBoundingClientRect().height;
    const viewport = win.innerHeight;
    const atBottom = win.scrollY > 0 && win.scrollY + viewport >= doc.documentElement.scrollHeight - 4;
    const sections = [...targets].filter(([, element]) => element.getClientRects().length).map(([id, element]) => ({ id, top: element.getBoundingClientRect().top }));
    const section = visibleSection(sections, height + Math.min(120, viewport * .16), viewport, atBottom);
    const selected = currentNavigation(items, new URL(win.location.href), section);
    const key = selected ? `${selected.index}:${selected.kind}` : 'none';
    if (key === last) return;
    last = key;
    links.forEach((link, index) => {
      const active = index === selected?.index;
      link.classList.toggle('is-active', active);
      if (active && selected) link.setAttribute('aria-current', selected.kind);
      else link.removeAttribute('aria-current');
    });
    groups.forEach(group => group.classList.toggle('is-active', !!group.querySelector('a[aria-current]')));
  }
  function schedule() {
    if (scheduled) return;
    scheduled = true;
    win.requestAnimationFrame(update);
  }
  win.addEventListener('scroll', schedule, { passive: true });
  win.addEventListener('resize', schedule, { passive: true });
  win.addEventListener('hashchange', schedule);
  win.addEventListener('pageshow', schedule);
  win.addEventListener('load', schedule, { once: true });
  if (win.ResizeObserver) {
    const observer = new win.ResizeObserver(schedule);
    observer.observe(doc.querySelector('main') || doc.body);
  }
  update();
}
