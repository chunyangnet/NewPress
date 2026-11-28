export {};
interface Choice {version: 1; necessary: true; analytics: boolean; savedAt: string}
(() => {
  'use strict';

  const key = 'newpress_cookie_consent_v1';
  const lifetime = 180 * 24 * 60 * 60;
  const banner = document.querySelector<HTMLElement>('[data-cookie-consent]');
  const dialog = document.querySelector<HTMLDialogElement>('[data-cookie-dialog]');
  const analytics = dialog?.querySelector<HTMLInputElement>('[data-cookie-analytics]');
  if (!banner || !dialog || !analytics || window.NewPressCookieConsent) return;

  // The theme owns consent state. The shared analytics loader only reads this
  // API, so invalid/expired choices have exactly the same meaning in both places.
  const parseChoice = (raw: string | null): Choice | null => {
    try {
      const value: unknown = JSON.parse(raw || 'null');
      if (!value || typeof value !== 'object' || !('savedAt' in value) || typeof value.savedAt !== 'string') return null;
      const age = Date.now() - Date.parse(value.savedAt);
      if ('version' in value && value.version === 1 && 'necessary' in value && value.necessary === true && 'analytics' in value && typeof value.analytics === 'boolean'
        && Number.isFinite(age) && age >= -60000 && age < lifetime * 1000) return {version: 1, necessary: true, analytics: value.analytics, savedAt: value.savedAt};
    } catch {}
    return null;
  };
  const readChoice = () => {
    let stored = null;
    let cookie = null;
    try { stored = parseChoice(localStorage.getItem(key)); } catch {}
    try {
      const item = document.cookie.split('; ').find(value => value.startsWith(key + '='));
      if (item) cookie = parseChoice(decodeURIComponent(item.slice(key.length + 1)));
    } catch {}
    // Cookies provide a fallback when browser storage is unavailable. A newer
    // denial must win even when an older copy could not be overwritten.
    if (!stored) return cookie;
    if (!cookie) return stored;
    if (stored.savedAt === cookie.savedAt) return stored.analytics ? cookie : stored;
    return Date.parse(stored.savedAt) > Date.parse(cookie.savedAt) ? stored : cookie;
  };
  let choice = readChoice();
  let memoryOnly = false;
  let opener: HTMLElement | null = null;
  const allowsAnalytics = () => choice?.analytics === true && Boolean(parseChoice(JSON.stringify(choice)));
  window.NewPressCookieConsent = Object.freeze({ allowsAnalytics });

  const clearAnalyticsCookies = () => {
    // Only known first-party analytics cookies are removed. Authentication,
    // CSRF, appearance and security-provider cookies must remain intact.
    const known = /^(?:_ga(?:_|$)|_gid$|_gat(?:_|$)|_gac_|_gcl_|AMP_TOKEN$|Hm_(?:lvt|lpvt|ck|up)_|_clck$|_clsk$|_pk_(?:id|ses|ref|cvar|hsr|testcookie)(?:\.|$))/;
    try {
      const names = document.cookie.split(';').map(item => item.trim().split('=')[0] || '').filter(name => known.test(name));
      if (!names.length) return;
      const host = window.location.hostname;
      const domains = new Set(['', host]);
      if (!/^[\d.]+$/.test(host) && !host.includes(':')) {
        const labels = host.split('.');
        for (let i = 0; i < labels.length - 1; i++) domains.add(labels.slice(i).join('.'));
      }
      const paths = new Set(['/']);
      const segments = window.location.pathname.split('/');
      for (let i = 1; i < segments.length; i++) {
        const path = segments.slice(0, i + 1).join('/');
        paths.add(path);
        paths.add(path.endsWith('/') ? path : path + '/');
      }
      for (const name of names) for (const path of paths) for (const domain of domains) {
        document.cookie = `${name}=; Max-Age=0; Path=${path}${domain ? '; Domain=' + domain : ''}; SameSite=Lax`;
      }
    } catch {}
  };

  const space = document.querySelector<HTMLElement>('[data-cookie-space]');
  const navigation = document.querySelector('.docs-mobile-nav');
  const updateLayout = () => {
    const height = banner.hidden ? 0 : Math.ceil(banner.getBoundingClientRect().height);
    const nav = navigation?.getBoundingClientRect();
    const offset = nav?.height ? Math.max(0, window.innerHeight - nav.top) + 10 : 0;
    document.documentElement.style.setProperty('--cookie-consent-height', `${height}px`);
    document.documentElement.style.setProperty('--cookie-nav-offset', `${offset}px`);
    if (space) space.hidden = banner.hidden;
  };
  const updateToggle = () => {
    const state = dialog.querySelector('[data-cookie-state]'); if (state) state.textContent = analytics.checked ? '已开启' : '已关闭';
  };
  const updateView = () => {
    banner.hidden = Boolean(choice);
    analytics.checked = allowsAnalytics();
    updateToggle();
    updateLayout();
  };
  const announceChoice = () => {
    const allowed = allowsAnalytics();
    if (!allowed) clearAnalyticsCookies();
    document.dispatchEvent(new CustomEvent('newpress:cookie-consent', { detail: { analytics: allowed } }));
    // Removing a script element cannot stop a running tracker. A fresh document
    // removes its timers and listeners and the loader then honors the denial.
    if (!allowed && window.__newpressAnalyticsLoaded) window.location.reload();
  };
  const openDialog = (event: Event) => {
    opener = event.currentTarget instanceof HTMLElement ? event.currentTarget : null;
    analytics.checked = allowsAnalytics();
    updateToggle();
    if (!dialog.open) {
      dialog.showModal();
      document.documentElement.classList.add('cookie-dialog-open');
    }
  };
  const closeDialog = () => { if (dialog.open) dialog.close(); };
  const saveChoice = (enabled: boolean) => {
    choice = { necessary: true, analytics: enabled, version: 1, savedAt: new Date().toISOString() };
    const raw = JSON.stringify(choice);
    let saved = false;
    try {
      document.cookie = `${key}=${encodeURIComponent(raw)}; Max-Age=${lifetime}; Path=/; SameSite=Lax${window.location.protocol === 'https:' ? '; Secure' : ''}`;
      saved = document.cookie.split('; ').some(item => item === key + '=' + encodeURIComponent(raw));
    } catch {}
    try { localStorage.setItem(key, raw); saved = true; }
    catch { try { localStorage.removeItem(key); } catch {} }
    memoryOnly = !saved;
    closeDialog();
    updateView();
    announceChoice();
  };
  const syncChoice = () => {
    if (memoryOnly) return;
    choice = readChoice();
    updateView();
    announceChoice();
  };

  document.querySelectorAll<HTMLElement>('[data-cookie-open]').forEach(button => {
    button.hidden = false;
    button.addEventListener('click', openDialog);
  });
  document.querySelectorAll('[data-cookie-accept]').forEach(button => button.addEventListener('click', () => saveChoice(true)));
  document.querySelectorAll('[data-cookie-reject]').forEach(button => button.addEventListener('click', () => saveChoice(false)));
  dialog.querySelectorAll('[data-cookie-close]').forEach(button => button.addEventListener('click', closeDialog));
  dialog.querySelector('[data-cookie-save]')?.addEventListener('click', () => saveChoice(analytics.checked));
  analytics.addEventListener('change', updateToggle);
  dialog.addEventListener('close', () => {
    if (dialog.open) return;
    document.documentElement.classList.remove('cookie-dialog-open');
    if (opener?.getClientRects().length) opener.focus({ preventScroll: true });
  });
  dialog.addEventListener('click', event => {
    if (event.target !== dialog) return;
    const rect = dialog.getBoundingClientRect();
    if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) closeDialog();
  });
  window.addEventListener('storage', event => {
    if (event.key !== key && event.key !== null) return;
    memoryOnly = false;
    syncChoice();
  });
  window.addEventListener('pageshow', event => { if (event.persisted) syncChoice(); });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) syncChoice(); });
  window.addEventListener('resize', updateLayout, { passive: true });
  if (typeof ResizeObserver === 'function') {
    const observer = new ResizeObserver(updateLayout);
    observer.observe(banner);
    if (navigation) observer.observe(navigation);
  }
  updateView();
  if (!allowsAnalytics()) clearAnalyticsCookies();
})();
