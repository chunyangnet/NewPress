(() => {
  const panel = document.querySelector<HTMLDetailsElement>('[data-resource-panel]');
  if (!panel) return;
  const status = panel.querySelector<HTMLElement>('[data-resource-editor-status]');
  let script: HTMLScriptElement | null = null, stylesheet: HTMLLinkElement | null = null;
  let scriptLoaded = false;
  const failed = () => { if (status) status.textContent = '资源编辑器加载失败，请收起后重新打开。'; };
  const load = () => {
    if (!panel.open) return;
    if (!panel.dataset.resourceCss || !panel.dataset.resourceScript) { failed(); return; }
    if (!stylesheet) {
      const css = document.createElement('link'); css.rel = 'stylesheet'; css.href = panel.dataset.resourceCss;
      stylesheet = css;
      css.addEventListener('error', () => { if (stylesheet === css) { stylesheet = null; css.remove(); failed(); } });
      document.head.append(css);
    }
    if (script || scriptLoaded) return;
    const pending = document.createElement('script'); pending.src = panel.dataset.resourceScript; script = pending;
    pending.addEventListener('load', () => { if (script === pending) scriptLoaded = true; });
    pending.addEventListener('error', () => { if (script === pending) { script = null; pending.remove(); failed(); } });
    document.head.append(pending);
  };
  panel.addEventListener('toggle', load); load();
})();
