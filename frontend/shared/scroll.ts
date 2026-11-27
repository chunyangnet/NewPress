import type { ScrollAPI, ScrollPosition } from './scroll-types';

// One owner for scroll positioning. Full document navigation uses the browser;
// local updates commit their DOM and position together, before the next paint.
(() => {
  'use strict';
  const root = document.documentElement;
  try { history.scrollRestoration = 'auto'; } catch {}

  const remember = () => {
    try {
      const state: unknown = history.state;
      history.replaceState({ ...(state && typeof state === 'object' ? state : {}), chunnelScroll: window.scrollY || 0 }, '');
    } catch {}
  };
  const restore = (top: number, left = window.scrollX || 0) => {
    const max = Math.max(0, root.scrollHeight - window.innerHeight);
    const target = Math.min(max, Math.max(0, Number(top) || 0));
    if (Math.abs((window.scrollY || 0) - target) > 0.5 || (window.scrollX || 0) !== left) {
      window.scrollTo({ top: target, left, behavior: 'instant' });
    }
  };
  const keep = <T>(change: () => T, position: ScrollPosition = {}) => {
    const top = position.top ?? (window.scrollY || 0);
    const left = position.left ?? (window.scrollX || 0);
    const anchor = root.style.overflowAnchor;
    const behavior = root.style.scrollBehavior;
    root.style.overflowAnchor = 'none';
    root.style.scrollBehavior = 'auto';
    try {
      const result = change();
      // Reading the final document height flushes layout while anchoring is
      // disabled. Never schedule a later scroll that could fight user input.
      restore(top, left);
      return result;
    } finally {
      root.style.overflowAnchor = anchor;
      root.style.scrollBehavior = behavior;
    }
  };
  const error = (region: Element, message: string) => {
    if (window.Chunnel?.showToast) { window.Chunnel.showToast(message, 'error'); return; }
    let status = region.querySelector<HTMLElement>('[data-navigation-error]');
    if (!status) {
      status = document.createElement('p');
      status.dataset.navigationError = '';
      status.setAttribute('role', 'status');
      region.append(status);
    }
    status.textContent = message;
  };
  window.addEventListener('pagehide', () => {
    remember();
    try { history.scrollRestoration = 'auto'; } catch {}
  });
  window.ChunnelScroll = { keep, restore, remember, error } satisfies ScrollAPI;
})();
