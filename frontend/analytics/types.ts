export type Period = 7 | 30 | 90;
export interface AnalyticsDay { date: string; views: number; searches: number; misses: number }
export interface AnalyticsRank { label: string; count: number }
export interface AnalyticsArticle {
  id: number; title: string; url: string; views: number; likes: number; bookmarks: number; comments: number;
}
// Mirrors the Go AnalyticsReport JSON contract. Calendar labels stay strings.
export interface AnalyticsReport {
  timezone: string; enabled: boolean; days: Period; start: string; end: string;
  views: number; article_views: number; searches: number; misses: number;
  dropped_since_start: number; updated_at: string;
  trend: AnalyticsDay[]; sources: AnalyticsRank[]; terms: AnalyticsRank[]; articles: AnalyticsArticle[];
}
export type ReportFailure = { kind: 'network' | 'timeout' | 'data' | 'unavailable' | 'session' | 'permission'; message: string };
export type ReportState =
  | { phase: 'ready' | 'refreshing'; requestedDays: Period; report: AnalyticsReport }
  | { phase: 'error'; requestedDays: Period; report: AnalyticsReport; error: ReportFailure }
  | { phase: 'denied'; requestedDays: Period; report: null; error: ReportFailure };
