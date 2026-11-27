// Shared immediate controls. The homepage and other public pages use this same source.
(() => {
  const $ = <T extends HTMLElement = HTMLElement>(selector: string) => document.querySelector<T>(selector);
  const header = $('[data-header]');
  const toggle = $('[data-nav-toggle]');
  const nav = $('[data-nav]');
  const row = nav?.closest('.nav-row');
  const search = $('[data-search-box]');
  const input = $<HTMLInputElement>('[data-search-input]');
  const mobile = matchMedia('(max-width: 700px)');
  const hideSearch = () => search?.classList.remove('is-visible', 'is-focused');
  const closeNav = () => { if (row?.classList.contains('is-open')) toggle?.click(); };
  const focusSearch = () => { closeNav(); search?.classList.add('is-visible', 'is-focused'); input?.focus({ preventScroll: true }); };
  // Anchor clearance is for article content. Applying it to a sticky search
  // caret makes the browser scroll the page whenever long input scrolls sideways.
  search?.addEventListener('focusin', () => document.documentElement.classList.add('is-search-editing'));
  search?.addEventListener('focusout', (event) => { if (!(event.relatedTarget instanceof Node) || !search.contains(event.relatedTarget)) document.documentElement.classList.remove('is-search-editing'); });
  toggle?.addEventListener('click', () => {
    const open = toggle.getAttribute('aria-expanded') !== 'true';
    hideSearch();
    toggle.setAttribute('aria-expanded', String(open));
    const label = toggle.querySelector('b');
    if (label) label.textContent = open ? '收起菜单' : '展开菜单';
    nav?.toggleAttribute('data-open', open);
    row?.classList.toggle('is-open', open);
    header?.classList.toggle('has-open-menu', open);
  });
  nav?.addEventListener('click', (event) => {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const link = event.target instanceof Element && event.target.closest<HTMLAnchorElement>('a[href]');
    if (!link || link.hasAttribute('download') || link.target && link.target !== '_self') return;
    const target = new URL(link.href, location.href), current = new URL(location.href);
    if (target.origin === current.origin && target.pathname === current.pathname && target.search === current.search && target.href.includes('#')) closeNav();
  });
  window.addEventListener('pageshow', (event) => {
    if (!event.persisted) return;
    closeNav();
    hideSearch();
    document.documentElement.classList.remove('is-search-editing');
  });
  mobile.addEventListener('change', closeNav);
  document.addEventListener('pointerdown', (event) => { if (mobile.matches && (!(event.target instanceof Node) || !header?.contains(event.target)) && !document.querySelector('dialog[open]')) closeNav(); });
  document.addEventListener('click', (event) => {
    if (!(event.target instanceof Element)) return;
    if (event.target.closest('[data-scroll-top]')) window.scrollTo({ top: 0, behavior: matchMedia('(prefers-reduced-motion: reduce)').matches || document.body.matches('.theme-force-reduced-motion, .theme-motion-minimal') ? 'instant' : 'smooth' });
    if (event.target.closest('[data-focus-search]')) focusSearch();
  });
  document.addEventListener('keydown', (event) => {
    if (event.defaultPrevented || event.isComposing || document.querySelector('dialog[open]')) return;
    if (event.key === 'Escape') {
      const account = $<HTMLDetailsElement>('[data-account-menu]');
      if (account?.open) { account.open = false; account.querySelector('summary')?.focus(); return; }
      if (row?.classList.contains('is-open')) { closeNav(); toggle?.focus(); }
      if (search?.classList.contains('is-visible') && search.contains(document.activeElement)) $('.header-search-toggle')?.focus();
      hideSearch();
      return;
    }
    if (event.target instanceof Element && event.target.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"])')) return;
    const shortcut = !event.altKey && !event.shiftKey && (event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k';
    if (input && (shortcut || (event.key === '/' && !event.ctrlKey && !event.metaKey && !event.altKey))) { event.preventDefault(); focusSearch(); }
  });
  input?.addEventListener('blur', () => search?.classList.remove('is-focused'));
})();
