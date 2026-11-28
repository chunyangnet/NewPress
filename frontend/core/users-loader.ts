import type {} from '../shared/account-types';

// Account editing owns the larger asset/progression validators. The directory
// needs them only after an explicit edit/create action, including deep links.
(() => {
  const host = document.querySelector<HTMLElement>('.admin-users-page');
  if (!host) return;
  const root = host;
  const source = root.dataset.usersWorkspaceSrc;
  if (!source) return;
  const owner = root.dataset.userId, csrf = root.dataset.csrf;
  let loading: Promise<void> | undefined, retired = false, serial = 0;
  const current = () => !retired && root.isConnected;
  const retire = () => { retired = true; serial++; root.dataset.userWorkspaceRetired = 'true'; };
  function load(): Promise<void> {
    if (loading) return loading;
    loading = new Promise((resolve, reject) => {
      const url = new URL(source!, location.href);
      if (url.origin !== location.origin || !url.pathname.startsWith('/core-assets/')) {
        reject(new Error('账户编辑资源地址无效。'));
        return;
      }
      const script = document.createElement('script');
      script.src = url.href;
      script.async = true;
      script.onload = () => root.dataset.userWorkspaceReady === 'true' ? resolve() : reject(new Error('账户编辑暂时不可用。'));
      script.onerror = () => reject(new Error('账户编辑资源未能载入。'));
      document.head.append(script);
    });
    return loading;
  }
  root.addEventListener('click', event => {
    if (!(event.target instanceof Element) || root.dataset.userWorkspaceReady === 'true') return;
    const link = event.target.closest<HTMLAnchorElement>('[data-user-edit], [data-user-create]');
    if (!link || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button !== 0 || !current()) return;
    event.preventDefault();
    const attempt = ++serial;
    link.setAttribute('aria-busy', 'true');
    load().then(() => { if (current() && attempt === serial && link.isConnected) link.click(); })
      .catch(() => { if (current() && attempt === serial) location.assign(link.href); })
      .finally(() => link.removeAttribute('aria-busy'));
  });
  document.addEventListener('chunnel:session', event => {
    const session = event.detail;
    if (session?.authenticated !== true || String(session.id || '') !== owner || session.csrf_token !== csrf) retire();
  });
  window.addEventListener('pagehide', retire);
  if (root.querySelector<HTMLElement>('[data-user-dialog]')?.dataset.autoOpen === 'true') {
    load().catch(() => { if (current()) window.ChunnelAdmin?.showToast('账户编辑暂时不可用，请重新载入。', 'error'); });
  }
})();
