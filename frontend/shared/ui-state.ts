/** Delayed visual feedback without delaying the actual operation lock. */
export function loadingPlaceholder(region: HTMLElement, retain: boolean) {
  const timer = setTimeout(() => {
    if (!region.isConnected) return;
    region.setAttribute('data-async-loading', retain ? 'refreshing' : 'loading');
  }, 150);
  return () => { clearTimeout(timer); region.removeAttribute('data-async-loading'); };
}
