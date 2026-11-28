export const historyKey = 'docshub:reading:v1';
export interface ReadingRecord {url: string; title: string; at: number}
export function normalizeHistory(value: unknown, origin: string): ReadingRecord[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set();
  return value.flatMap((item: unknown) => {
    if (!item || typeof item !== 'object' || !('title' in item) || typeof item.title !== 'string' || !item.title.trim()
      || !('at' in item) || typeof item.at !== 'number' || !Number.isFinite(item.at) || !('url' in item) || typeof item.url !== 'string') return [];
    let url;
    try { url = new URL(item.url, origin); } catch { return []; }
    if (url.origin !== origin || url.username || url.password || !/^\/(posts|pages)\/[^/]+\/?$/.test(url.pathname)) return [];
    if (seen.has(url.pathname)) return [];
    seen.add(url.pathname);
    return [{ url: url.pathname, title: item.title.trim().slice(0,200), at: item.at }];
  }).sort((a,b) => b.at-a.at).slice(0,12);
}
export function rememberDocument(previous: ReadingRecord[], document: ReadingRecord, origin: string) {
  return normalizeHistory([document, ...previous.filter(item => item.url !== document.url)], origin);
}
