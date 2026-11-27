import type { HomeLoad } from './home-types';
import { $ } from './home-dom';

export function initHomeFeedLoader(load: HomeLoad): void {
  const feedLinks = '#latest .home-filters a, #latest .home-sort-links a, #latest .home-view-switch a, #latest .pagination a';
  let feedIntent: HTMLAnchorElement | null | undefined;
  document.addEventListener('click', async (event) => {
    if (window.ChunnelHomeFeedReady || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const link = event.target instanceof Element && event.target.closest<HTMLAnchorElement>(feedLinks);
    if (!link || (link.target && link.target !== '_self') || link.hasAttribute('download')) return;
    const url = new URL(link.href, location.href);
    if (url.origin !== location.origin || url.pathname !== '/') return;
    event.preventDefault();
    feedIntent = link;
    $('#latest')?.setAttribute('aria-busy', 'true');
    try {
      await load('chunnel-home-feed.js', () => window.ChunnelHomeFeedReady);
      if (feedIntent !== link || !link.isConnected) return;
      feedIntent = null;
      $('#latest')?.removeAttribute('aria-busy');
      link.click();
    } catch {
      if (feedIntent === link) {
        feedIntent = null;
        const panel = $('#latest');
        panel?.removeAttribute('aria-busy');
        if (panel) window.ChunnelScroll?.error(panel, '列表切换暂时不可用，请再次点击重试。');
      }
    }
  });
  window.addEventListener('pagehide', () => { feedIntent = null; });

}
