// A user-selected report mode loads the optional framework/chart bundle.
// Both initial and full interaction costs are checked by HTTP resource tests.
const resources = new Map<string, Promise<void>>();
function resource(tag: 'script' | 'link', address: string): Promise<void> {
  const url = new URL(address, location.href);
  if (url.origin !== location.origin) return Promise.reject(new Error('Report asset must be same-origin'));
  const key = `${tag}:${url.href}`;
  const existing = resources.get(key);
  if (existing) return existing;
  const pending = new Promise<void>((resolve, reject) => {
    const element = tag === 'script' ? document.createElement('script') : document.createElement('link');
    if (element instanceof HTMLScriptElement) element.src = url.href;
    else { element.rel = 'stylesheet'; element.href = url.href; }
    const timeout = window.setTimeout(() => { element.remove(); reject(new Error('timeout')); }, 15000);
    element.onload = () => { window.clearTimeout(timeout); resolve(); };
    element.onerror = () => { window.clearTimeout(timeout); element.remove(); reject(new Error('load')); };
    document.head.append(element);
  });
  const cached = pending.catch((error: unknown) => { resources.delete(key); throw error; });
  resources.set(key, cached);
  return cached;
}
async function activate(host: HTMLElement) {
  const button = host.querySelector<HTMLButtonElement>('[data-analytics-activate]');
  const feedback = host.querySelector<HTMLElement>('[data-analytics-load-status]');
  const script = host.dataset.analyticsScript, style = host.dataset.analyticsStyle;
  if (!button || !feedback || !script || !style || button.disabled) return;
  button.disabled = true;
  feedback.textContent = '正在加载交互图表…';
  try {
    await Promise.all([resource('script', script), resource('link', style)]);
    if (!host.isConnected) return;
    if (!window.NewPressAnalytics) throw new Error('Report entry did not initialize');
    host.dataset.analyticsRequested = 'true';
    if (location.hash !== '#interactive') history.replaceState({ ...history.state }, '', location.pathname + location.search + '#interactive');
    feedback.textContent = '';
    window.NewPressAnalytics.mount();
  } catch {
    delete host.dataset.analyticsRequested;
    feedback.textContent = '交互图表未能加载，可继续查看当前报告或重试。';
  } finally { button.disabled = false; }
}
function initialize() {
  document.querySelectorAll<HTMLElement>('[data-analytics]').forEach(host => {
    const button = host.querySelector<HTMLButtonElement>('[data-analytics-activate]');
    if (!button || button.dataset.bound) return;
    button.dataset.bound = 'true'; button.hidden = false;
    button.addEventListener('click', () => void activate(host));
    if (location.hash === '#interactive') void activate(host);
  });
}
initialize();
document.addEventListener('chunnel:admin-updated', initialize);
