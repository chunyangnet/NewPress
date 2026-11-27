const focusableSelector = 'a[href], button:not([disabled]), summary, input:not([disabled]), [tabindex="0"]';

export function visibleFocusables(root) {
  return [...root.querySelectorAll(focusableSelector)].filter(node => node.getClientRects().length && !node.closest('[inert]'));
}

export function initNavigation(doc, win) {
  const header = doc.querySelector('[data-header]');
  const menu = doc.querySelector('#primary-nav');
  const toggle = doc.querySelector('[data-menu-toggle]');
  if (!header || !menu || !toggle) return { close() {} };
  const desktop = win.matchMedia('(min-width: 761px)');
  const groups = [...menu.querySelectorAll('.nav-group')];
  const background = [doc.querySelector('main'), doc.querySelector('.site-footer')].filter(Boolean);
  const wasInert = new Map();
  let isOpen = false;

  const summary = group => group.querySelector(':scope > summary');
  function closeGroups(except = null) {
    groups.forEach(group => {
      if (group !== except && !group.contains(except) && !except?.contains(group)) group.open = false;
    });
  }
  function setMenu(open, restoreFocus = false) {
    open = !!open && !desktop.matches;
    if (open === isOpen) return;
    isOpen = open;
    menu.classList.toggle('is-open', open);
    doc.body.classList.toggle('menu-open', open);
    toggle.setAttribute('aria-expanded', String(open));
    toggle.setAttribute('aria-label', open ? '关闭主导航' : '展开主导航');
    toggle.dataset.open = String(open);
    if (open) {
      background.forEach(element => { wasInert.set(element, element.inert); element.inert = true; });
      const current = menu.querySelector('a[aria-current]');
      if (current) {
        let group = current.closest('.nav-group');
        while (group) { group.open = true; group = group.parentElement.closest('.nav-group'); }
      }
      (current || visibleFocusables(menu)[0] || toggle).focus();
    } else {
      background.forEach(element => { element.inert = wasInert.get(element) || false; });
      wasInert.clear();
      closeGroups();
      if (restoreFocus) toggle.focus({ preventScroll: true });
    }
  }
  toggle.addEventListener('click', () => setMenu(!isOpen));
  menu.addEventListener('click', event => {
    const link = event.target.closest('a[href]');
    if (!link || event.defaultPrevented || event.button > 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    let parent = link.closest('.nav-group');
    while (parent?.parentElement.closest('.nav-group')) parent = parent.parentElement.closest('.nav-group');
    const returnTo = isOpen ? toggle : parent && summary(parent);
    setMenu(false);
    closeGroups();
    if (returnTo && link.getClientRects().length === 0) returnTo.focus();
  });
  groups.forEach(group => {
    group.addEventListener('toggle', event => {
      if (event.target !== group) return;
      if (group.open) closeGroups(group);
    });
    summary(group)?.addEventListener('keydown', event => {
      if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
      event.preventDefault();
      group.open = true;
      closeGroups(group);
      const links = visibleFocusables(group).filter(node => node !== summary(group));
      (event.key === 'ArrowDown' ? links[0] : links.at(-1))?.focus();
    });
  });
  menu.addEventListener('keydown', event => {
    if (event.defaultPrevented || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const options = visibleFocusables(menu);
    const index = options.indexOf(doc.activeElement);
    if (index < 0) return;
    event.preventDefault();
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? options.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + options.length) % options.length;
    options[next]?.focus();
  });
  doc.addEventListener('keydown', event => {
    if (event.key === 'Escape') {
      const group = doc.activeElement?.closest('.nav-group[open]');
      if (group) {
        event.preventDefault(); group.open = false; summary(group)?.focus(); return;
      }
      if (isOpen) { event.preventDefault(); setMenu(false, true); }
      else closeGroups();
    }
    if (event.key !== 'Tab' || !isOpen) return;
    const options = visibleFocusables(header);
    const first = options[0], last = options.at(-1);
    if (event.shiftKey && (doc.activeElement === first || !header.contains(doc.activeElement))) {
      event.preventDefault(); last?.focus();
    } else if (!event.shiftKey && (doc.activeElement === last || !header.contains(doc.activeElement))) {
      event.preventDefault(); first?.focus();
    }
  });
  doc.addEventListener('click', event => {
    if (!header.contains(event.target)) { setMenu(false); closeGroups(); }
  });
  doc.addEventListener('focusin', event => {
    if (isOpen && !header.contains(event.target)) (visibleFocusables(menu)[0] || toggle).focus();
    else if (!header.contains(event.target)) closeGroups();
  });
  desktop.addEventListener('change', () => {
    const focused = doc.activeElement;
    setMenu(false);
    closeGroups();
    if (!desktop.matches && menu.contains(focused)) toggle.focus();
  });
  win.addEventListener('pagehide', () => setMenu(false));
  return { close: () => { setMenu(false); closeGroups(); } };
}
