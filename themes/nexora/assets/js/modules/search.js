export function initSearch(doc, win, navigation) {
  const dialog = doc.querySelector('#site-search');
  if (!dialog || typeof dialog.showModal !== 'function') return;
  let returnTo = null;
  let backdropStart = false;
  const outside = event => {
    if (event.target !== dialog) return false;
    const rect = dialog.getBoundingClientRect();
    return event.clientY < rect.top || event.clientY > rect.bottom || event.clientX < rect.left || event.clientX > rect.right;
  };
  doc.querySelectorAll('[data-search-open]').forEach(link => link.addEventListener('click', event => {
    if (event.button > 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    navigation.close();
    returnTo = link;
    if (!dialog.open) dialog.showModal();
    doc.body.classList.add('search-open');
    const input = dialog.querySelector('input[type="search"]');
    if (input && !input.value) input.value = new URL(win.location.href).searchParams.get('q') || '';
    input?.focus();
    input?.select();
  }));
  dialog.querySelector('[data-search-close]')?.addEventListener('click', () => dialog.close());
  dialog.addEventListener('pointerdown', event => { backdropStart = outside(event); });
  dialog.addEventListener('click', event => {
    if (backdropStart && outside(event)) dialog.close();
    backdropStart = false;
  });
  dialog.addEventListener('close', () => {
    doc.body.classList.remove('search-open');
    if (returnTo?.isConnected) returnTo.focus();
  });
  dialog.querySelector('form')?.addEventListener('submit', event => {
    const input = dialog.querySelector('input[type="search"]');
    if (!input) return;
    input.value = input.value.trim();
    if (!input.value) {
      event.preventDefault();
      input.focus();
      input.reportValidity();
    }
  });
  win.addEventListener('pagehide', () => { if (dialog.open) dialog.close(); });
}
