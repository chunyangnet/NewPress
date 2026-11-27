import { AnalyticsError, fetchReport } from './api';
import type { AnalyticsReport, Period, ReportState } from './types';

export function createReportController(initial: AnalyticsReport, load = fetchReport) {
  let state: ReportState = { phase: 'ready', requestedDays: initial.days, report: initial };
  const listeners = new Set<() => void>();
  let request: AbortController | null = null;
  let generation = 0, disposed = false;
  const publish = (value: ReportState) => { state = value; for (const listener of listeners) listener(); };
  async function select(days: Period) {
    if (disposed || state.phase === 'denied') return;
    request?.abort();
    const controller = new AbortController(), current = ++generation;
    request = controller;
    const previous = state.report;
    publish({ phase: 'refreshing', requestedDays: days, report: previous });
    try {
      const report = await load(days, controller.signal);
      if (disposed || current !== generation || controller.signal.aborted) return;
      // The injected loader and future adapters must obey the same period contract.
      if (report.days !== days) throw new AnalyticsError({ kind: 'data', message: '统计周期不匹配，请重试。' });
      publish({ phase: 'ready', requestedDays: days, report });
    } catch (error) {
      if (disposed || current !== generation || controller.signal.aborted) return;
      const failure = error instanceof AnalyticsError ? error.failure : { kind: 'network' as const, message: '网络连接失败，请重试。' };
      publish(failure.kind === 'session' || failure.kind === 'permission'
        ? { phase: 'denied', requestedDays: days, report: null, error: failure }
        : { phase: 'error', requestedDays: days, report: previous, error: failure });
    } finally { if (request === controller) request = null; }
  }
  return {
    getSnapshot: () => state,
    subscribe(listener: () => void) { listeners.add(listener); return () => { listeners.delete(listener); }; },
    select,
    refresh: () => select(state.requestedDays),
    suspend() { request?.abort(); generation++; request = null; if (state.phase === 'refreshing') publish({ phase: 'ready', requestedDays: state.requestedDays, report: state.report }); },
    dispose() { disposed = true; generation++; request?.abort(); request = null; listeners.clear(); },
  };
}
export type ReportController = ReturnType<typeof createReportController>;
