import type { HomeLoad } from './home-types';
import { $ } from './home-dom';

export function initHomeFeatures(entry: URL, load: HomeLoad): () => boolean {
  const controls = '[data-home-checkin], [data-saved-open], [data-favorite], [data-follow], [data-reaction], [data-share], [data-unavailable], [data-account-login], [data-account-register], [data-account-logout], [data-drawer-open], [data-modal-open], [data-message-to], [data-toast]';
  let intent: { target: HTMLElement } | null | undefined, featureReady = false;
  // The theme supplies the hidden status markup with the page, so its initial
  // script only binds behavior. Missing custom markup must not throw.
  const status = $('[data-home-feature-status]');
  const message = status?.children[0] as HTMLSpanElement | undefined;
  const retry = status?.children[1] as HTMLButtonElement | undefined;
  const dismiss = status?.children[2] as HTMLButtonElement | undefined;
  if (!status || !message || !retry || !dismiss) return () => false;
  document.body.append(status);
  const clear = (restore = false) => {
    const previous = intent; intent = null;
    previous?.target.removeAttribute('aria-busy');
    if (restore || status.contains(document.activeElement)) previous?.target.focus({ preventScroll: true });
    status.hidden = true;
  };
  const activate = async (target: HTMLElement) => {
    clear(); const action = { target }; intent = action;
    target.setAttribute('aria-busy', 'true');
    message.textContent = '正在加载…'; retry.hidden = true; status.hidden = false;
    try {
      await Promise.all([
        load(`chunnel-home-overlays${entry.pathname.includes('-glass') ? '-glass' : ''}.css`),
        load('chunnel-home-features.js', () => window.ChunnelHomeFeaturesReady),
      ]);
      featureReady = true;
      if (intent !== action || !target.isConnected) return;
      clear(); target.click();
    } catch {
      if (intent !== action) return;
      target.removeAttribute('aria-busy');
      message.textContent = '加载失败，请重试。'; retry.hidden = false;
      if (document.activeElement === target) retry.focus({ preventScroll: true });
    }
  };
  document.addEventListener('click', (event) => {
    const target = event.target instanceof Element && event.target.closest<HTMLElement>(controls);
    if (!target || featureReady || (target.matches('a') && (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey))) return;
    event.preventDefault(); event.stopImmediatePropagation();
    if (intent?.target !== target || !target.hasAttribute('aria-busy')) activate(target);
  }, true);
  dismiss.addEventListener('click', () => clear(true));
  retry.addEventListener('click', () => { if (intent) activate(intent.target); });
  document.addEventListener('keydown', (event) => {
    if (intent && event.key === 'Escape' && !event.isComposing) { event.preventDefault(); clear(true); }
  }, true);
  document.addEventListener('pointerdown', (event) => {
    if (intent && event.target instanceof Element && !event.target.closest(controls) && !status.contains(event.target)) clear();
  }, true);
  document.addEventListener('focusin', (event) => {
    if (intent && event.target instanceof Node && !intent.target.contains(event.target) && !status.contains(event.target)) clear();
  });
  window.addEventListener('pagehide', () => clear());

  return () => featureReady;
}
