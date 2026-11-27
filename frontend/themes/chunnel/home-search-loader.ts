import type { HomeLoad } from './home-types';
import { $ } from './home-dom';

export function initHomeSearch(load: HomeLoad): void {
  const input = $<HTMLInputElement>('[data-search-input]');
  const form = $<HTMLFormElement>('[data-search-box]');
  if (!input || !form) return;
  let searchPending = false;
  let searchStatus: HTMLDivElement | null = null;
  let copy: HTMLParagraphElement, retry: HTMLButtonElement;
  const hideSearchStatus = () => { if (searchStatus) searchStatus.hidden = true; };
  const enhanceSearch = async () => {
    if (searchPending || window.ChunnelSearchReady) return;
    searchPending = true;
    form.setAttribute('aria-busy', 'true');
    if (!searchStatus) {
      searchStatus = document.createElement('div'); searchStatus.className = 'search-preview';
      copy = document.createElement('p'); copy.className = 'search-preview-status'; copy.setAttribute('role', 'status');
      retry = document.createElement('button'); retry.type = 'button'; retry.className = 'search-preview-retry'; retry.textContent = '重试预览';
      retry.addEventListener('click', () => { input.focus({ preventScroll: true }); enhanceSearch(); });
      searchStatus.append(copy, retry); form.append(searchStatus);
    }
    searchStatus.hidden = false; copy.textContent = '加载预览，可回车搜索…'; retry.hidden = true;
    try { await load('chunnel-home-search.js', () => window.ChunnelSearchReady); searchStatus.remove(); searchStatus = null; }
    catch { copy.textContent = '预览失败，可回车搜索。'; retry.hidden = false; }
    finally { searchPending = false; form.removeAttribute('aria-busy'); }
  };
  ['focus', 'input'].forEach((type) => input.addEventListener(type, () => { delete input.dataset.searchDismissed; enhanceSearch(); }));
  input.addEventListener('compositionstart', () => { input.dataset.searchComposing = 'true'; });
  input.addEventListener('compositionend', () => { delete input.dataset.searchComposing; });
  form.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape') return;
    if (event.isComposing || event.keyCode === 229 || input.dataset.searchComposing) { event.stopPropagation(); return; }
    if (searchStatus && !searchStatus.hidden) event.preventDefault();
    input.dataset.searchDismissed = 'true'; hideSearchStatus();
  });
  form.addEventListener('focusout', (event) => { if (!(event.relatedTarget instanceof Node && form.contains(event.relatedTarget))) hideSearchStatus(); });
  form.addEventListener('submit', (event) => { if (input.dataset.searchComposing === 'true') event.preventDefault(); else hideSearchStatus(); });

}
