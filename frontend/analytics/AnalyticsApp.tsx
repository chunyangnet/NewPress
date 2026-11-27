import { useEffect, useLayoutEffect, useSyncExternalStore } from 'react';
import type { MouseEvent } from 'react';
import type { ReportController } from './controller';
import type { Period } from './types';
import { period } from './api';
import { Articles, Metrics, ReportNotes, SearchTerms, Sources, Trend } from './components/Report';

export function AnalyticsApp({ controller, onReady, onAccessLost }: { controller: ReportController; onReady: () => void; onAccessLost: () => void }) {
  const state = useSyncExternalStore(controller.subscribe, controller.getSnapshot);
  const report = state.report;
  useLayoutEffect(onReady, [onReady]);
  useLayoutEffect(() => { if (state.phase === 'denied') onAccessLost(); }, [state.phase, onAccessLost]);
  useEffect(() => {
    const back = () => {
      const query = new URLSearchParams(location.search);
      const days = period(query.has('days') ? Number(query.get('days')) : 30);
      if (!days || query.getAll('days').length > 1) { location.reload(); return; }
      void controller.select(days);
    };
    const suspend = () => controller.suspend();
    const resume = (event: PageTransitionEvent) => { if (event.persisted) void controller.refresh(); };
    window.addEventListener('popstate', back);
    window.addEventListener('pagehide', suspend);
    window.addEventListener('pageshow', resume);
    return () => { window.removeEventListener('popstate', back); window.removeEventListener('pagehide', suspend); window.removeEventListener('pageshow', resume); };
  }, [controller]);
  const navigate = (event: MouseEvent<HTMLAnchorElement>, days: Period) => {
    if (event.button !== 0 || event.ctrlKey || event.metaKey || event.altKey || event.shiftKey) return;
    event.preventDefault();
    const url = new URL(location.href);
    url.searchParams.set('days', String(days));
    if (url.href !== location.href) history.pushState({ ...history.state }, '', url);
    void controller.select(days);
  };
  return <div className="analytics-react" data-report-days={report?.days}>
    <header className="analytics-heading">
      <div><p className="analytics-react-eyebrow">站点观察 / ANALYTICS</p><h1>访问统计</h1></div>
      {report && <div className="analytics-heading-actions">
        <a className="analytics-action" href={`?days=${report.days}`}>简洁报告</a>
        <button className="analytics-action" type="button" onClick={() => void controller.refresh()}>刷新报告</button>
        <a className="analytics-action analytics-action-primary" href={`/api/v1/admin/analytics?days=${report.days}`} download={`newpress-analytics-${report.start}-${report.end}.json`}>导出数据</a>
      </div>}
    </header>
    <nav className="admin-section-tabs" aria-label="运营统计"><a href="/admin/analytics/" aria-current="page">访问统计</a><a href="/admin/analytics/ai/">AI 消耗</a><a href="/admin/analytics/ai/requests/">请求明细</a></nav>
    {state.phase === 'denied' ? <div className="analytics-react-status" role="alert"><h2>{state.error.message}</h2><a href={state.error.kind === 'session' ? '/admin/login/' : '/admin/'}>返回{state.error.kind === 'session' ? '登录' : '后台'}</a></div> : report && <>
      <div className="analytics-toolbar">
        <div className="analytics-period"><span className="analytics-date"><time>{report.start}</time> — <time>{report.end}</time></span><span>{report.timezone} · 含今日</span>
          <span className={`analytics-collection${report.enabled ? '' : ' is-paused'}`}><i aria-hidden="true" />采集已{report.enabled ? '开启' : '暂停'}</span></div>
        <nav className="analytics-range" aria-label="统计时间范围">{([7, 30, 90] as const).map(days => <a key={days} href={`?days=${days}`} aria-current={state.requestedDays === days ? 'page' : undefined} onClick={event => navigate(event, days)}>近 {days} 天</a>)}</nav>
      </div>
      <div className="analytics-react-feedback" role="status" aria-live="polite" aria-atomic="true">
        {state.phase === 'refreshing' ? `正在获取近 ${state.requestedDays} 天报告；当前显示近 ${report.days} 天的数据。`
          : state.phase === 'error' ? `${state.error.message} 当前保留近 ${report.days} 天的上次报告。` : `已显示近 ${report.days} 天报告。`}
        {state.phase === 'error' && <button type="button" onClick={() => void controller.refresh()}>重试</button>}
      </div>
      {!report.enabled && <p className="analytics-notice">采集已暂停。以下为保留的历史报告，暂停期间不会产生新记录。</p>}
      <div aria-busy={state.phase === 'refreshing'}>
        <Metrics report={report} />
        <div className="analytics-react-primary"><Trend report={report} /><Sources report={report} /></div>
        <div className="analytics-react-secondary"><Articles report={report} /><SearchTerms report={report} /></div>
        <ReportNotes report={report} />
      </div>
    </>}
  </div>;
}
