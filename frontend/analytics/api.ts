import { calendar, instant } from '../shared/site-time';
import type { AnalyticsArticle, AnalyticsDay, AnalyticsRank, AnalyticsReport, Period, ReportFailure } from './types';

export class AnalyticsError extends Error {
  constructor(public readonly failure: ReportFailure) { super(failure.message); }
}
const invalid = (): never => { throw new AnalyticsError({ kind: 'data', message: '统计数据格式不正确，请重试。' }); };
const object = (value: unknown): Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : invalid();
const text = (value: unknown): string => typeof value === 'string' ? value : invalid();
const count = (value: unknown): number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : invalid();
const dateLabel = (value: unknown): string => typeof value === 'string' && calendar(value) ? value : invalid();
const list = <T>(value: unknown, maximum: number, parse: (value: unknown) => T): T[] => Array.isArray(value) && value.length <= maximum ? value.map(parse) : invalid();
export function period(value: unknown): Period | null { return value === 7 || value === 30 || value === 90 ? value : null; }
function rank(value: unknown): AnalyticsRank { const row = object(value); return { label: text(row.label), count: count(row.count) }; }
function day(value: unknown): AnalyticsDay {
  const row = object(value);
  const result = { date: dateLabel(row.date), views: count(row.views), searches: count(row.searches), misses: count(row.misses) };
  if (result.misses > result.searches || result.searches > result.views) invalid();
  return result;
}
function article(value: unknown): AnalyticsArticle {
  const row = object(value), url = text(row.url), id = count(row.id);
  // The report only links current public articles on this origin.
  if (!id || !url.startsWith('/posts/') || /[\\\s\x00-\x1f]/.test(url)) invalid();
  return { id, url, title: text(row.title), views: count(row.views), likes: count(row.likes), bookmarks: count(row.bookmarks), comments: count(row.comments) };
}
export function parseReport(value: unknown, expectedDays?: Period): AnalyticsReport {
  const data = object(value), days = period(data.days);
  if (!days || expectedDays !== undefined && days !== expectedDays || typeof data.enabled !== 'boolean') return invalid();
  const timezone = text(data.timezone), updated = text(data.updated_at);
  if (!timezone || timezone === 'Local') invalid();
  try { new Intl.DateTimeFormat('en-US', { timeZone: timezone }); } catch { invalid(); }
  // An empty Go store legitimately emits the RFC3339 zero time.
  if (updated !== '0001-01-01T00:00:00Z' && !instant(updated)) invalid();
  const report: AnalyticsReport = {
    timezone, days, enabled: data.enabled, updated_at: updated,
    start: dateLabel(data.start), end: dateLabel(data.end),
    views: count(data.views), article_views: count(data.article_views), searches: count(data.searches), misses: count(data.misses),
    dropped_since_start: count(data.dropped_since_start), trend: list(data.trend, 90, day),
    sources: list(data.sources, 10, rank), terms: list(data.terms, 20, rank), articles: list(data.articles, 20, article),
  };
  if (report.trend.length !== days || report.article_views > report.views) invalid();
  // UTC arithmetic validates consecutive labels; displayed dates are never converted.
  const start = Date.parse(report.start + 'T00:00:00Z');
  report.trend.forEach((row, index) => {
    if (row.date !== new Date(start + index * 86400000).toISOString().slice(0, 10)) invalid();
  });
  if (report.trend.at(-1)?.date !== report.end) invalid();
  for (const key of ['views', 'searches', 'misses'] as const) {
    if (report.trend.reduce((sum, row) => sum + row[key], 0) !== report[key]) invalid();
  }
  if (report.sources.reduce((sum, row) => sum + row.count, 0) > report.views || report.terms.reduce((sum, row) => sum + row.count, 0) > report.misses) invalid();
  if (report.articles.reduce((sum, row) => sum + row.views, 0) > report.article_views) invalid();
  return report;
}

export async function fetchReport(days: Period, signal: AbortSignal): Promise<AnalyticsReport> {
  const controller = new AbortController();
  let timedOut = false;
  const abort = () => controller.abort();
  signal.addEventListener('abort', abort, { once: true });
  if (signal.aborted) controller.abort();
  const timeout = window.setTimeout(() => { timedOut = true; controller.abort(); }, 15000);
  try {
    const response = await fetch(`/api/v1/admin/analytics?days=${days}`, {
      credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: controller.signal,
      headers: { Accept: 'application/json' },
    });
    if (!response.ok) {
      const failure: ReportFailure = response.status === 401 ? { kind: 'session', message: '登录已失效，请重新登录。' }
        : response.status === 403 ? { kind: 'permission', message: '当前账号没有查看统计报告的权限。' }
        : { kind: 'unavailable', message: '统计报告暂时不可用，请稍后重试。' };
      throw new AnalyticsError(failure);
    }
    let data: unknown;
    try { data = await response.json(); } catch { return invalid(); }
    if (controller.signal.aborted) throw new DOMException('Aborted', 'AbortError');
    return parseReport(data, days);
  } catch (error) {
    if (timedOut) throw new AnalyticsError({ kind: 'timeout', message: '获取报告超时，请重试。' });
    if (error instanceof AnalyticsError || signal.aborted) throw error;
    throw new AnalyticsError({ kind: 'network', message: '网络连接失败，请检查连接后重试。' });
  } finally {
    window.clearTimeout(timeout);
    signal.removeEventListener('abort', abort);
  }
}
