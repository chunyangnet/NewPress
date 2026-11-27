import { node } from '../shared/dom';

export function initializeSEO() {
  const seoWorkspace = document.querySelector<HTMLElement>('[data-seo-workspace]');
  if (seoWorkspace) {
    const panels = [...seoWorkspace.querySelectorAll<HTMLElement>('[data-seo-panel]')];
    const links = [...seoWorkspace.querySelectorAll<HTMLElement>('[data-seo-task]')];
    const activate = (initial = false) => {
      const target = document.getElementById(location.hash.slice(1));
      const panel = target?.closest<HTMLElement>('[data-seo-panel]');
      const id = initial && seoWorkspace.dataset.seoEditing === 'true' ? 'redirects' :
        panel && panels.includes(panel) ? panel.id : 'redirects';
      panels.forEach(item => { item.hidden = item.id !== id; });
      links.forEach(link => {
        if (link.dataset.seoTask === id) link.setAttribute('aria-current', 'page');
        else link.removeAttribute('aria-current');
      });
    };
    activate(true);
    window.addEventListener('hashchange', () => activate());
    seoWorkspace.addEventListener('click', event => {
      const link = (event.target instanceof Element ? event.target.closest<HTMLAnchorElement>('a[data-seo-task]') : null);
      if (link && link.hash === location.hash) activate();
    });
  }

}

export function initializeTools() {
  const tools = document.querySelector<HTMLElement>('[data-tools-workspace]');
  if (tools) {
    const panels = [...tools.querySelectorAll<HTMLElement>('[data-tools-panel]')];
    const links = [...tools.querySelectorAll<HTMLElement>('[data-tools-target]')];
    const query = new URLSearchParams(location.search);
    const initial = tools.dataset.toolsRestore === 'true' || location.pathname.includes('navigation-import')
      ? 'tools-recovery' : ['audit_cursor', 'audit_action', 'audit_object_type'].some(key => query.has(key))
        ? 'tools-diagnostics' : 'tools-downloads';
    const activate = () => {
      const target = document.getElementById(location.hash.slice(1));
      const panel = target?.closest<HTMLElement>('[data-tools-panel]');
      const id = panel && panels.includes(panel) ? panel.id : initial;
      panels.forEach(item => { item.hidden = item.id !== id; });
      links.forEach(link => {
        if (link.dataset.toolsTarget === id) link.setAttribute('aria-current', 'page');
        else link.removeAttribute('aria-current');
      });
      if (target && panel && panels.includes(panel)) target.scrollIntoView({ block: 'start', behavior: 'instant' });
    };
    activate();
    window.addEventListener('hashchange', activate);
    tools.addEventListener('click', event => {
      const link = (event.target instanceof Element ? event.target.closest<HTMLAnchorElement>('a[data-tools-target]') : null);
      if (link && link.hash === location.hash) activate();
    });
  }

}

export function initializeWorkspaces() {
  initializeSEO();
  initializeTools();
  // Progressive enhancement: both content sections remain visible without JS.
  document.querySelectorAll<HTMLElement>('[data-daily-workspace]').forEach(workspace => {
    const list = workspace.querySelector<HTMLElement>('[data-daily-tabs]');
    const tabs = [...workspace.querySelectorAll<HTMLElement>('[data-daily-tab]')];
    const candidates = tabs.map(tab => document.getElementById(tab.dataset.dailyTab || ''));
    if (!list || !tabs.length || candidates.some(panel => !panel)) return;
    const panels = candidates.filter((panel): panel is HTMLElement => panel !== null);
    const activate = (index: number) => {
      tabs.forEach((tab, i) => {
        tab.setAttribute('aria-selected', String(i === index));
        tab.tabIndex = i === index ? 0 : -1;
        const panel = panels[i]; if (panel) panel.hidden = i !== index;
      });
    };
    list.setAttribute('role', 'tablist');
    tabs.forEach((tab, index) => {
      const panel = panels[index]; if (!panel) return;
      tab.setAttribute('role', 'tab');
      tab.setAttribute('aria-controls', panel.id);
      panel.setAttribute('role', 'tabpanel');
      panel.setAttribute('aria-labelledby', tab.id);
      panel.tabIndex = 0;
      tab.addEventListener('click', () => activate(index));
      tab.addEventListener('keydown', event => {
        let next;
        if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
        if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length;
        if (event.key === 'Home') next = 0;
        if (event.key === 'End') next = tabs.length - 1;
        if (next === undefined) return;
        event.preventDefault(); activate(next); tabs[next]?.focus();
      });
    });
    activate(0); list.hidden = false; workspace.classList.add('daily-enhanced');
  });

  const sidebar = document.querySelector<HTMLElement>('[data-admin-sidebar]');
  const key = `newpress:admin:nav:${location.host}:${sidebar?.dataset.account || 'preview'}`;
  let groups: Record<string, unknown> = {};
  try {
    const stored: unknown = JSON.parse(localStorage.getItem(key) || '{}');
    if (stored && typeof stored === 'object' && !Array.isArray(stored)) groups = stored as Record<string, unknown>;
  } catch { /* Private storage may be unavailable. */ }
  for (const id of ['users', 'commerce']) if (typeof groups[id] !== 'boolean' && typeof groups.interaction === 'boolean') groups[id] = groups.interaction;
  document.querySelectorAll<HTMLDetailsElement>('[data-admin-group]').forEach(group => {
    const id = group.dataset.adminGroup; if (!id) return;
    if (!group.hasAttribute('data-current-group') && typeof groups[id] === 'boolean') group.open = groups[id];
    group.addEventListener('toggle', () => { groups[id] = group.open; try { localStorage.setItem(key, JSON.stringify(groups)); } catch { /* Navigation remains usable without storage. */ } });
  });
  // Keep the independently scrolling menu at the same place across documents.
  const sidebarNav = sidebar?.querySelector<HTMLElement>('.admin-nav');
  const sidebarPositionKey = `${key}:scroll`;
  if (sidebarNav) {
    try { sidebarNav.scrollTop = Math.max(0, Number(sessionStorage.getItem(sidebarPositionKey)) || 0); } catch {}
    const rememberSidebar = () => { try { sessionStorage.setItem(sidebarPositionKey, String(sidebarNav.scrollTop)); } catch {} };
    sidebarNav.addEventListener('scroll', rememberSidebar, { passive: true });
    window.addEventListener('pagehide', rememberSidebar);
  }
  const pinKey = `${key}:pins`;
  const entries = new Map([...(sidebar?.querySelectorAll<HTMLAnchorElement>('[data-admin-entry]') || [])].map(link => [link.dataset.adminEntry, link]));
  const defaults = ['content', 'media', 'comments', 'settings', 'shop'];
  let pins = defaults;
  try { const saved: unknown = JSON.parse(localStorage.getItem(pinKey) || 'null'); if (Array.isArray(saved)) pins = saved.filter((id: unknown): id is string => typeof id === 'string'); } catch {}
  pins = [...new Set(pins)].filter(id => entries.has(id) && id !== 'dashboard').slice(0, 5);
  const pinOptions = [...document.querySelectorAll<HTMLInputElement>('[data-pin-option]')];
  const drawPins = () => {
    document.querySelectorAll<HTMLElement>('[data-dashboard-pins], [data-sidebar-pins]').forEach(container => {
      container.replaceChildren();
      for (const id of pins) {
        const original = entries.get(id); if (!original) continue;
        const link = node('a', 'admin-button admin-button-quiet', original.querySelector<HTMLElement>('span')?.textContent || original.textContent);
        link.href = original.href;
        if (container.hasAttribute('data-dashboard-pins')) {
          const icon = original.querySelector<SVGElement>('svg')?.cloneNode(true);
          if (icon instanceof Element) { icon.setAttribute('aria-hidden', 'true'); icon.removeAttribute('id'); link.prepend(icon); }
        }
        container.append(link);
      }
      if (!pins.length) container.append(node('p', 'daily-empty', '尚未固定常用入口'));
    });
    pinOptions.forEach(option => { option.checked = pins.includes(option.value); });
  };
  if (sidebar) {
    const favorites = node('section', 'admin-nav-favorites');
    favorites.setAttribute('aria-label', '常用入口');
    favorites.append(node('strong', '', '常用入口'));
    const links = node('div', ''); links.setAttribute('data-sidebar-pins', ''); favorites.append(links);
    sidebar.querySelector<HTMLElement>('.admin-nav')?.prepend(favorites);
    drawPins();
  }
  pinOptions.forEach(option => option.addEventListener('change', () => {
    const message = document.querySelector<HTMLElement>('[data-pin-status]');
    if (option.checked && pins.length >= 5) { option.checked = false; if (message) message.textContent = '最多固定 5 个入口，请先取消一个。'; return; }
    pins = option.checked ? [...pins, option.value] : pins.filter(id => id !== option.value);
    try { localStorage.setItem(pinKey, JSON.stringify(pins)); if (message) message.textContent = '常用入口已保存到当前浏览器。'; }
    catch { if (message) message.textContent = '浏览器不允许保存，当前页面仍可使用。'; }
    drawPins();
  }));
  document.querySelectorAll<HTMLElement>('[data-pin-picker]').forEach(picker => picker.hidden = false);
  const input = document.querySelector<HTMLInputElement>('[data-admin-command-input]');
  const results = document.querySelector<HTMLElement>('[data-admin-command-results]');
  if (input && results) {
    const links = [...(sidebar?.querySelectorAll<HTMLAnchorElement>('a[href]') || []), ...document.querySelectorAll<HTMLAnchorElement>('[data-admin-account-menu] a[href]')];
    const close = () => { results.hidden = true; input.setAttribute('aria-expanded', 'false'); };
    input.addEventListener('input', () => {
      const needle = input.value.trim().toLocaleLowerCase(); results.replaceChildren();
      if (!needle) { close(); return; }
      const seen = new Set();
      for (const link of links) { if (!link.pathname.startsWith('/admin/') || seen.has(link.href) || !(link.textContent || '').toLocaleLowerCase().includes(needle)) continue; seen.add(link.href); const match = node('a', '', link.querySelector<HTMLElement>('span')?.textContent || link.textContent?.trim()); match.href = link.href; results.append(match); }
      if (!seen.size) results.append(node('p', '', '未找到功能'));
      results.hidden = false; input.setAttribute('aria-expanded', 'true');
    });
    input.closest('form')?.addEventListener('submit', event => { event.preventDefault(); results.querySelector<HTMLElement>('a')?.click(); });
    input.addEventListener('keydown', event => { if (event.key === 'ArrowDown') { event.preventDefault(); results.querySelector<HTMLElement>('a')?.focus(); } if (event.key === 'Escape') close(); });
    results.addEventListener('keydown', event => { if (event.key === 'Escape') { close(); input.focus(); } });
    document.addEventListener('click', event => { if (!(event.target instanceof Element) || !event.target.closest('.admin-command-wrap')) close(); });
  }
  const preview = document.querySelector<HTMLDialogElement>('[data-settings-preview-dialog]');
  document.querySelector<HTMLElement>('[data-settings-preview-open]')?.addEventListener('click', () => { preview?.showModal(); document.dispatchEvent(new CustomEvent('admin-preview-open')); });
  document.querySelector<HTMLElement>('[data-settings-preview-close]')?.addEventListener('click', () => preview?.close());
  preview?.addEventListener('close', () => document.querySelector<HTMLElement>('[data-settings-preview-open]')?.focus());
}
