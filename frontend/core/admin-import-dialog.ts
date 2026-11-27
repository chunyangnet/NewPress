export function initializeImportDialog(document: Document) {
  const importDialog = document.querySelector<HTMLDialogElement>('[data-import-dialog]');
  const setDialog = (open: boolean) => {
    if (!importDialog) return;
    if (open && !importDialog.open) { importDialog.showModal(); importDialog.querySelector<HTMLButtonElement>('.admin-dialog-close')?.focus(); }
    else if (!open && importDialog.open) importDialog.close();
    document.body.classList.toggle('admin-dialog-open', open);
  };
  document.querySelector('[data-import-open]')?.addEventListener('click', () => setDialog(true));
  importDialog?.addEventListener('close', () => { if (!importDialog.open) document.body.classList.remove('admin-dialog-open'); });
  importDialog?.addEventListener('keydown', (event) => {
    if (event.key !== 'Tab') return;
    const focusable = [...importDialog.querySelectorAll<HTMLElement>('button, input, select, textarea, a[href], [tabindex]')]
      .filter((element) => element.tabIndex >= 0 && !element.matches(':disabled') && element.getClientRects().length);
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
  });
  importDialog?.querySelectorAll('[data-import-close]').forEach((button) => button.addEventListener('click', () => setDialog(false)));

  return importDialog;
}
