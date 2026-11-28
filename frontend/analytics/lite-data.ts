import { calendar } from '../shared/site-time';

export type SeriesKey = 'views' | 'searches' | 'misses';
export interface NativeDay { date: string; views: number; searches: number; misses: number }
export interface NativeSource { label: string; count: number }
type Attributes = Record<string, string | undefined>;
export const number = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 1 });
export const compact = new Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 1 });
export const display = (value: number): string => value >= 1000000 ? compact.format(value) : number.format(value);
export const isSeries = (value: unknown): value is SeriesKey => value === 'views' || value === 'searches' || value === 'misses';

export function readCount(value: unknown): number | null {
  if (typeof value !== 'string' && typeof value !== 'number') return null;
  if (typeof value === 'string' && !/^\d+$/.test(value)) return null;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : null;
}
export function readNativeReport(data: Attributes, daily: Attributes[], sources: Attributes[]) {
  const views = readCount(data.views), articles = readCount(data.articleViews);
  const searches = readCount(data.searches), misses = readCount(data.misses);
  const days = readCount(data.days);
  if (views === null || articles === null || searches === null || misses === null ||
      !days || ![7, 30, 90].includes(days) || daily.length !== days || articles > views || misses > searches || searches > views || !data.timezone) return null;
  const rows: NativeDay[] = [], ranks: NativeSource[] = [];
  for (const row of daily) {
    const v = readCount(row.views), s = readCount(row.searches), m = readCount(row.misses);
    if (!row.date || !calendar(row.date) || v === null || s === null || m === null || m > s || s > v) return null;
    // Calendar arithmetic only checks consecutive server labels; it never rebuckets.
    const previous = rows.at(-1)?.date;
    if (previous && Date.parse(row.date + 'T00:00:00Z') - Date.parse(previous + 'T00:00:00Z') !== 86400000) return null;
    rows.push({ date: row.date, views: v, searches: s, misses: m });
  }
  for (const key of ['views', 'searches', 'misses'] as const) {
    if (rows.reduce((sum, row) => sum + row[key], 0) !== { views, searches, misses }[key]) return null;
  }
  for (const source of sources) {
    const count = readCount(source.sourceCount);
    if (count === null || typeof source.sourceLabel !== 'string') return null;
    ranks.push({ label: source.sourceLabel, count });
  }
  if (ranks.reduce((sum, rank) => sum + rank.count, 0) > views) return null;
  return { rows, sources: ranks, totals: { views, articles, searches, misses }, timezone: data.timezone };
}
export function percent(value: number, total: number): string {
  if (!(total > 0)) return '—';
  const ratio = Math.min(100, Math.max(0, value) / total * 100);
  return ratio > 0 && ratio < 0.1 ? '<0.1%' : number.format(ratio) + '%';
}
export function makeSeries(rows: Partial<Record<SeriesKey, number>>[], key: SeriesKey, width = 900, top = 20, bottom = 180) {
  const values = rows.map(row => Math.max(0, row[key] ?? 0));
  const total = values.reduce((sum, value) => sum + value, 0), peak = Math.max(0, ...values);
  const magnitude = Math.max(1, 10 ** Math.floor(Math.log10(Math.max(1, peak / 4))));
  const step = ([1, 2, 3, 4, 5, 6, 8, 10].find(unit => unit * magnitude >= peak / 4) ?? 10) * magnitude;
  const ceiling = Math.max(4, step * 4);
  const points = values.map((value, index) => ({
    x: values.length === 1 ? width / 2 : index * width / Math.max(1, values.length - 1),
    y: bottom - value / ceiling * (bottom - top), value,
  }));
  return { total, peak, average: rows.length ? total / rows.length : 0, ceiling, points,
    ticks: Array.from({ length: 5 }, (_, index) => ceiling - index * ceiling / 4) };
}
export function sourceBreakdown(sources: NativeSource[], total: number) {
  if (!(total > 0)) return [];
  let used = 0;
  const parts = sources.map((source, index) => {
    const value = Math.min(Math.max(0, source.count), Math.max(0, total - used));
    const part = { label: source.label, count: value, share: value / total * 100, offset: used / total * 100, index };
    used += value; return part;
  });
  if (used < total) parts.push({ label: '其余来源', count: total - used, share: (total - used) / total * 100, offset: used / total * 100, index: -1 });
  return parts;
}
export function nearestIndex(x: number, width: number, length: number): number {
  if (!Number.isFinite(x) || !Number.isFinite(width) || width <= 0 || length < 2) return 0;
  return Math.max(0, Math.min(length - 1, Math.round(x / width * (length - 1))));
}
