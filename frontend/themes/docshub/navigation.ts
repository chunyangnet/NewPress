import { $, $$ } from './shared';
import { navigationMatch } from './state';

export function initNavigation() {
  const sidebar = $('.sidebar');
  const syncCurrent = () => {
    if (!sidebar) return;
    const links = $$('[data-nav-link]', sidebar);
    let selected, match;
    for (const link of links) {
      link.removeAttribute('aria-current');
      const candidate = navigationMatch(link.getAttribute('href') || '', location.href);
      if (candidate && (!match || candidate.score > match.score)) { selected = link; match = candidate; }
    }
    if (!selected) {
      const kind = document.body.dataset.pageKind || '';
      selected = links.find(link => (link.dataset.navKinds || '').split(' ').includes(kind));
      if (selected) match = { current: 'location', score: 0 };
    }
    if (selected && match) selected.setAttribute('aria-current', match.current);
    $$('.nav-item', sidebar).forEach(item => item.classList.toggle('has-current', !!item.querySelector('[aria-current]')));
    $$<HTMLDetailsElement>('details.nav-branch', sidebar).forEach(branch => { if (branch.querySelector('[aria-current]')) branch.open = true; });
  };
  syncCurrent();
  window.addEventListener('hashchange', syncCurrent);
  window.addEventListener('pageshow', syncCurrent);

  const dock = $('.docs-mobile-nav');
  const syncDock = () => {
    if (!dock) return;
    const links = $$('a[href]', dock);
    let selected, score = -1;
    for (const link of links) {
      const candidate = navigationMatch(link.getAttribute('href') || '', location.href);
      if (candidate && candidate.score > score) { selected = link; score = candidate.score; }
    }
    if (!selected) selected = links.find(link => (link.dataset.navKinds || '').split(' ').includes(document.body.dataset.pageKind || ''));
    links.forEach(link => { if (link === selected) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current'); });
  };
  window.ChunnelAccountNav = syncDock;
  window.addEventListener('popstate', syncDock);
  window.addEventListener('pageshow', syncDock);
  syncDock();

  const toggle = $('[data-menu-toggle]'), scrim = $('[data-menu-close]'), shell = $('.site-shell');
  const mobile = window.matchMedia('(max-width: 760px)');
  let isOpen = false, previousFocus: Element | null = null;
  const setMenu = (open: boolean, restoreFocus = true, opener: HTMLElement | null = null) => {
    open = open && mobile.matches;
    if (open && !isOpen) previousFocus = opener || document.activeElement;
    isOpen = open;
    sidebar?.classList.toggle('is-open', open);
    document.body.classList.toggle('menu-open', open);
    toggle?.setAttribute('aria-expanded', String(open));
    toggle?.setAttribute('aria-label', open ? '关闭导航' : '打开导航');
    if (scrim) scrim.hidden = !open;
    if (shell) shell.inert = open;
    if (dock) dock.inert = open;
    if (sidebar) {
      sidebar.inert = mobile.matches && !open;
      if (open) {
        sidebar.setAttribute('role', 'dialog'); sidebar.setAttribute('aria-modal', 'true');
        ($('[data-sidebar-close]', sidebar) || $('a', sidebar))?.focus();
      } else {
        sidebar.removeAttribute('role'); sidebar.removeAttribute('aria-modal');
        if (restoreFocus && mobile.matches) (previousFocus instanceof HTMLElement && previousFocus.isConnected ? previousFocus : toggle)?.focus();
      }
    }
  };
  toggle?.addEventListener('click', () => setMenu(!isOpen, true, toggle));
  scrim?.addEventListener('click', () => setMenu(false));
  $('[data-sidebar-close]')?.addEventListener('click', () => setMenu(false));
  sidebar?.addEventListener('click', event => { if (mobile.matches && event.target instanceof Element && event.target.closest('a')) setMenu(false); });
  mobile.addEventListener('change', () => setMenu(false, false));
  window.addEventListener('pageshow', () => setMenu(false, false));
  setMenu(false, false);

  const menus = $$<HTMLDetailsElement>('.topbar-dropdown,.docs-mobile-group');
  menus.forEach(menu => menu.addEventListener('toggle', () => { if (menu.open) menus.forEach(other => { if (other !== menu) other.open = false; }); }));
  document.addEventListener('click', event => menus.forEach(menu => { if (!(event.target instanceof Node) || !menu.contains(event.target)) menu.open = false; }));
  document.addEventListener('keydown', event => {
    if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === 'k') {
      const search = $<HTMLInputElement>('.search-form input');
      if (search) { event.preventDefault(); setMenu(false, false); search.focus(); search.select(); }
    }
    if (event.key === 'Escape') {
      if (isOpen) { event.preventDefault(); setMenu(false); }
      menus.forEach(menu => { if (menu.open) { menu.open = false; $('summary', menu)?.focus(); } });
    }
    if (event.key === 'Tab' && isOpen && sidebar) {
      const nodes = $$('a[href],button,summary,input,select,textarea,[tabindex="0"]', sidebar).filter(node => node.getClientRects().length && !node.matches(':disabled') && !node.closest('[hidden]'));
      const first = nodes[0], last = nodes.at(-1);
      if (event.shiftKey && (document.activeElement === first || !sidebar.contains(document.activeElement))) { event.preventDefault(); last?.focus(); }
      else if (!event.shiftKey && (document.activeElement === last || !sidebar.contains(document.activeElement))) { event.preventDefault(); first?.focus(); }
    }
  });
  const shortcut = $('[data-search-shortcut]');
  if (shortcut) shortcut.textContent = /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘ K' : 'Ctrl K';

  const backToTop = $('[data-back-to-top]');
  if (backToTop) {
    let queued = false;
    const syncBackToTop = () => {
      backToTop.hidden = window.scrollY < Math.max(400, window.innerHeight * .5);
      queued = false;
    };
    window.addEventListener('scroll', () => {
      if (!queued) { queued = true; requestAnimationFrame(syncBackToTop); }
    }, { passive: true });
    window.addEventListener('resize', syncBackToTop);
    window.addEventListener('pageshow', syncBackToTop);
    backToTop.addEventListener('click', event => {
      event.preventDefault();
      $('#main')?.focus({ preventScroll: true });
      const reduced = document.documentElement.dataset.reduceMotion === 'true' || window.matchMedia('(prefers-reduced-motion: reduce)').matches;
      window.scrollTo({ top: 0, behavior: reduced ? 'instant' : 'smooth' });
    });
    syncBackToTop();
  }
}
