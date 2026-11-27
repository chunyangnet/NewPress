import { bindNativeSubmission } from './admin-native-submit';
(() => {
  'use strict';
  const body = document.body;
  const reducedMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
  const toastStack = document.querySelector('[data-admin-toast-stack]');
  const icon = (name: string) => {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.classList.add('icon', `icon-${name}`);
    svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', `#icon-${name}`);
    svg.append(use);
    return svg;
  };

  const showToast = (message: string, tone = 'info') => {
    if (!toastStack || !message) return;
    const toast = document.createElement('div');
    toast.className = 'admin-toast';
    toast.setAttribute('data-tone', tone);
    const mark = document.createElement('span');
    mark.append(icon(tone === 'error' ? 'circle-x' : tone === 'success' ? 'check-circle' : 'info'));
    const copy = document.createElement('p');
    const title = document.createElement('b');
    title.textContent = tone === 'error' ? '操作未完成' : tone === 'success' ? '操作完成' : '提示';
    const detail = document.createElement('small');
    detail.textContent = message;
    copy.append(title, detail);
    const close = document.createElement('button');
    close.type = 'button';
    close.setAttribute('aria-label', '关闭提示');
    close.append(icon('x'));
    close.addEventListener('click', () => toast.remove());
    toast.append(mark, copy, close);
    toastStack.append(toast);
    requestAnimationFrame(() => toast.classList.add('is-visible'));
    window.setTimeout(() => {
      toast.classList.remove('is-visible');
      window.setTimeout(() => toast.remove(), reducedMotion ? 0 : 360);
    }, 4300);
  };

  window.ChunnelAdmin = Object.freeze({ icon, showToast });

  // Consume the verified update status; the update source can be connected later.
  const versionEntry = document.querySelector<HTMLElement>('[data-admin-update-status]');
  if (versionEntry && location.pathname === '/admin/') {
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(), 8000);
    void fetch('/api/v1/admin/updates', {credentials:'same-origin', cache:'no-store', signal:controller.signal, headers:{Accept:'application/json'}}).then(async response => {
      if (!response.ok) return;
      const value: unknown = await response.json();
      if (!value || typeof value !== 'object' || !('available' in value)) return;
      const available = value.available;
      if (!available || typeof available !== 'object' || !('version' in available) || typeof available.version !== 'string' || !available.version || available.version === versionEntry.dataset.adminVersion) return;
      const badge = versionEntry.querySelector<HTMLElement>('[data-admin-update-badge]'); if (badge) badge.hidden = false;
      const key = `newpress:update-dismissed:${versionEntry.dataset.adminVersion}:${available.version}`;
      try { if (localStorage.getItem(key)) return; } catch { /* Storage is optional. */ }
      const notice = document.createElement('aside'); notice.className = 'admin-update-notice'; notice.setAttribute('role','status'); notice.setAttribute('aria-live','polite');
      const title = document.createElement('strong'); title.textContent = `NewPress ${available.version} 已可更新`;
      const copy = document.createElement('p'); copy.textContent = '新版本已通过更新源验证，可查看更新说明与安装条件。';
      const link = document.createElement('a'); link.href = '/admin/updates/'; link.textContent = '查看更新';
      const close = document.createElement('button'); close.type = 'button'; close.setAttribute('aria-label','暂不提醒此版本'); close.append(icon('x'));
      close.addEventListener('click', () => { try { localStorage.setItem(key,'1'); } catch { /* Storage is optional. */ } notice.remove(); versionEntry.querySelector('a')?.focus({preventScroll:true}); });
      notice.append(title,copy,link,close); body.append(notice);
    }).catch(() => { /* An offline update service must not interrupt the dashboard. */ }).finally(() => clearTimeout(timer));
    window.addEventListener('pagehide', () => controller.abort(), {once:true});
  }

  const sidebar = document.querySelector<HTMLElement>('[data-admin-sidebar]');
  const sidebarToggle = document.querySelector<HTMLButtonElement>('[data-admin-nav-open]');
  const workspace = document.querySelector<HTMLElement>('.admin-workspace');
  const mobileSidebar = window.matchMedia('(max-width: 1050px)');
  function setSidebar(open: boolean, restoreFocus = true) {
    if (!sidebar) return;
    const active = open && mobileSidebar.matches;
    body.classList.toggle('admin-nav-visible', active);
    sidebarToggle?.setAttribute('aria-expanded', String(active));
    sidebar.inert = mobileSidebar.matches && !active;
    if (workspace) workspace.inert = active;
    if (active) {
      sidebar.setAttribute('role', 'dialog');
      sidebar.setAttribute('aria-modal', 'true');
      document.querySelector<HTMLButtonElement>('[data-admin-nav-close]')?.focus();
    } else {
      sidebar.removeAttribute('role'); sidebar.removeAttribute('aria-modal');
      if (restoreFocus && mobileSidebar.matches) sidebarToggle?.focus({ preventScroll: true });
    }
  }
  sidebarToggle?.addEventListener('click', () => setSidebar(true));
  document.querySelector('[data-admin-nav-close]')?.addEventListener('click', () => setSidebar(false));
  mobileSidebar.addEventListener('change', () => setSidebar(false, sidebar?.contains(document.activeElement)));
  setSidebar(false, false);
  sidebar?.addEventListener('keydown', (event) => {
    if (event.key !== 'Tab' || !body.classList.contains('admin-nav-visible')) return;
    const items = [...sidebar.querySelectorAll<HTMLElement>('a[href], button:not(:disabled), summary, [tabindex="0"]')].filter(el => el.getClientRects().length);
    const first = items[0], last = items.at(-1);
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
  });
  document.addEventListener('click', (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (!target) return;
    if (body.classList.contains('admin-nav-visible') && !target.closest('[data-admin-sidebar], [data-admin-nav-open]')) setSidebar(false);
    const toastTrigger = target.closest<HTMLElement>('[data-admin-toast]');
    if (toastTrigger) showToast(toastTrigger.dataset.adminToast || toastTrigger.dataset.toast || '');
  });
  sidebar?.querySelectorAll('a').forEach((link) => link.addEventListener('click', () => setSidebar(false, false)));

  const accountToggle = document.querySelector<HTMLButtonElement>('[data-admin-account-toggle]');
  const accountMenu = document.querySelector<HTMLElement>('[data-admin-account-menu]');
  const setAccountMenu = (open: boolean, focusFirst = false) => {
    if (!accountMenu || !accountToggle) return;
    accountToggle.setAttribute('aria-expanded', String(open));
    accountMenu.hidden = !open;
    if (open && focusFirst) accountMenu.querySelector<HTMLElement>('a[href], button:not(:disabled)')?.focus();
  };
  accountToggle?.addEventListener('click', () => {
    const open = accountToggle.getAttribute('aria-expanded') === 'true';
    setAccountMenu(!open);
  });
  accountToggle?.addEventListener('keydown', (event) => {
    if (event.key !== 'ArrowDown') return;
    event.preventDefault();
    setAccountMenu(true, true);
  });
  const account = document.querySelector<HTMLElement>('.admin-account');
  account?.addEventListener('focusout', (event) => {
    if (event.relatedTarget instanceof Node && !account.contains(event.relatedTarget)) setAccountMenu(false);
  });
  document.addEventListener('click', (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (accountMenu && !target?.closest('.admin-account')) {
      setAccountMenu(false);
    }
  });

  const quickDraftForm = document.querySelector<HTMLFormElement>('[data-quick-draft]');
  if (quickDraftForm) {
    const title = quickDraftForm.querySelector<HTMLInputElement>('input[name="title"]');
    const bodyInput = quickDraftForm.querySelector<HTMLTextAreaElement>('textarea[name="content_source"]');
    const titleCount = quickDraftForm.querySelector('[data-quick-draft-title-count]');
    const bodyCount = quickDraftForm.querySelector('[data-quick-draft-body-count]');
    const submit = quickDraftForm.querySelector<HTMLButtonElement>('[data-quick-draft-submit]');
    const submitContent = submit ? [...submit.childNodes].map(node => node.cloneNode(true)) : [];
    const updateCounts = () => {
      if (titleCount) titleCount.textContent = `${[...(title?.value || '')].length}/200`;
      if (bodyCount) bodyCount.textContent = `${[...(bodyInput?.value || '')].length} 字`;
    };
    updateCounts();
    title?.addEventListener('input', updateCounts);
    bodyInput?.addEventListener('input', updateCounts);
    bindNativeSubmission(quickDraftForm, {
      onPending() {
        if (submit) { submit.disabled = true; submit.setAttribute('aria-busy', 'true'); }
      },
      onRestore() {
        if (!submit) return;
        submit.disabled = false;
        submit.removeAttribute('aria-busy');
        submit.replaceChildren(...submitContent.map(node => node.cloneNode(true)));
        updateCounts();
      },
    });
  }

  document.addEventListener('click', async event => {
    const button = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-copy-value]') : null;
    if (!button) return;
    const value = button.dataset.copyValue || '';
    try { await navigator.clipboard.writeText(value); showToast(`${value} 已复制`, 'success'); }
    catch { showToast('浏览器未授予剪贴板权限', 'error'); }
  });

  document.addEventListener('submit', (event) => {
    const form = event.target instanceof HTMLFormElement ? event.target : null;
    if (!form?.matches('form[data-confirm]')) return;
    if (!window.confirm(form.dataset.confirm)) event.preventDefault();
  }, true);

  document.addEventListener('keydown', (event) => {
    if (event.defaultPrevented || event.isComposing) return;
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
      event.preventDefault();
      document.querySelector<HTMLInputElement>('[data-admin-command-input]')?.focus();
    }
    if (event.key === 'Escape') {
      if (document.querySelector('dialog[open]')) return;
      if (body.classList.contains('admin-nav-visible')) setSidebar(false);
      if (accountMenu && !accountMenu.hidden) {
        setAccountMenu(false); accountToggle?.focus();
      }
    }
  });

  if (!document.querySelector('[data-progression-admin]')) window.ChunnelAdminUI?.settingsNavigation(document);

})();
