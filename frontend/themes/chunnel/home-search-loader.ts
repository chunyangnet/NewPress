import type { HomeLoad } from './home-types';
import { $ } from './home-dom';

export function initHomeSearch(load: HomeLoad): void {
  const input = $<HTMLInputElement>('[data-search-input]');
  const form = $<HTMLFormElement>('[data-search-box]');
  const searchStatus = $('[data-home-search-status]');
  const copy = searchStatus?.children[0] as HTMLParagraphElement | undefined;
  const retry = searchStatus?.children[1] as HTMLButtonElement | undefined;
  if (!input || !form || !searchStatus || !copy || !retry) return;
  let searchPending = false;
  const hideSearchStatus = () => { searchStatus.hidden = true; };
  const enhanceSearch = async () => {
    if (searchPending || window.ChunnelSearchReady) return;
    searchPending = true;
    form.setAttribute('aria-busy', 'true');
    searchStatus.hidden = false; copy.textContent = '加载预览，可回车搜索…'; retry.hidden = true;
    try { await load('chunnel-home-search.js', () => window.ChunnelSearchReady); searchStatus.remove(); }
    catch { copy.textContent = '预览失败，可回车搜索。'; retry.hidden = false; }
    finally { searchPending = false; form.removeAttribute('aria-busy'); }
  };
  // Keep WebKit from blurring the input and hiding this button before click.
  retry.addEventListener('pointerdown', event => { if (event.button === 0) event.preventDefault(); });
  retry.addEventListener('click', () => { input.focus({ preventScroll: true }); enhanceSearch(); });
  ['focus', 'input'].forEach((type) => input.addEventListener(type, () => { delete input.dataset.searchDismissed; enhanceSearch(); }));
  input.addEventListener('compositionstart', () => { input.dataset.searchComposing = 'true'; });
  input.addEventListener('compositionend', () => { delete input.dataset.searchComposing; });
  form.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape') return;
    if (event.isComposing || event.keyCode === 229 || input.dataset.searchComposing) { event.stopPropagation(); return; }
    if (searchStatus.isConnected && !searchStatus.hidden) event.preventDefault();
    input.dataset.searchDismissed = 'true'; hideSearchStatus();
  });
  form.addEventListener('focusout', (event) => { if (!(event.relatedTarget instanceof Node && form.contains(event.relatedTarget))) hideSearchStatus(); });
  form.addEventListener('submit', (event) => { if (input.dataset.searchComposing === 'true') event.preventDefault(); else hideSearchStatus(); });

}
