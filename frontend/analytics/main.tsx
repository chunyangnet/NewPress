import { Component } from 'react';
import type { ReactNode } from 'react';
import { createRoot } from 'react-dom/client';
import type { Root } from 'react-dom/client';
import { AnalyticsApp } from './AnalyticsApp';
import { createReportController } from './controller';
import type { ReportController } from './controller';
import { parseReport } from './api';

class ReportBoundary extends Component<{ onFailure: () => void; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() { return { failed: true }; }
  componentDidCatch() { this.props.onFailure(); }
  render() { return this.state.failed ? null : this.props.children; }
}
const mounted = new Map<HTMLElement, { root: Root; controller: ReportController }>();
function initialize() {
  document.querySelectorAll<HTMLElement>('[data-analytics][data-analytics-requested][data-analytics-report]').forEach(host => {
    if (mounted.has(host)) return;
    const container = host.querySelector<HTMLElement>('[data-analytics-root]');
    const fallback = host.querySelector<HTMLElement>('[data-analytics-fallback]');
    if (!container || !fallback) return;
    try {
      const report = parseReport(JSON.parse(host.dataset.analyticsReport ?? '') as unknown);
      const controller = createReportController(report);
      const root = createRoot(container);
      mounted.set(host, { root, controller });
      const onFailure = () => {
        delete container.dataset.reactReady;
        delete host.dataset.analyticsRequested;
        fallback.hidden = false;
        // The preserved native report belongs to the original server period.
        // Restore that URL before native navigation takes ownership again.
        const url = new URL(location.href);
        url.searchParams.set('days', String(report.days)); url.hash = '';
        history.replaceState({ ...history.state }, '', url);
        const feedback = fallback.querySelector('[data-analytics-load-status]');
        if (feedback) feedback.textContent = '交互图表初始化失败，已恢复简洁报告。';
        queueMicrotask(() => {
          controller.dispose(); root.unmount(); mounted.delete(host);
          document.dispatchEvent(new CustomEvent('newpress:analytics-fallback', { detail: { host } }));
        });
      };
      const onReady = () => { container.dataset.reactReady = 'true'; fallback.hidden = true; document.dispatchEvent(new CustomEvent('newpress:analytics-ready', { detail: { host } })); };
      const onAccessLost = () => {
        delete host.dataset.analyticsReport;
        // Never restore an older identity's server report after a denied request.
        fallback.replaceChildren();
        const notice = document.createElement('p');
        notice.textContent = '报告访问权限已失效，请重新登录后查看。';
        fallback.append(notice);
      };
      root.render(<ReportBoundary onFailure={onFailure}><AnalyticsApp controller={controller} onReady={onReady} onAccessLost={onAccessLost} /></ReportBoundary>);
    } catch {
      fallback.hidden = false;
      const feedback = fallback.querySelector('[data-analytics-load-status]');
      if (feedback) feedback.textContent = '报告数据无法读取，已保留简洁报告。';
    }
  });
}
if (!window.NewPressAnalytics) {
window.NewPressAnalytics = { mount: initialize };
document.addEventListener('chunnel:admin-updated', initialize);
document.addEventListener('chunnel:admin-before-replace', event => {
  const detail: unknown = (event as CustomEvent<unknown>).detail;
  if (!detail || typeof detail !== 'object' || !('regions' in detail) || !Array.isArray(detail.regions)) return;
  const regions: unknown[] = detail.regions;
  for (const [host, { root, controller }] of mounted) {
    if (regions.some(region => region instanceof Element && (region === host || region.contains(host)))) {
      const container = host.querySelector<HTMLElement>('[data-analytics-root]');
      if (container) delete container.dataset.reactReady;
      controller.dispose(); root.unmount(); mounted.delete(host);
    }
  }
});
}
