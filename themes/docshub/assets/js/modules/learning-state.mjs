export const historyKey = 'docshub:reading:v1';
export function normalizeHistory(value, origin) {
  if (!Array.isArray(value)) return [];
  const seen = new Set();
  return value.flatMap(item => {
    if (!item || typeof item.title !== 'string' || !item.title.trim() || !Number.isFinite(item.at)) return [];
    let url;
    try { url = new URL(item.url, origin); } catch { return []; }
    if (url.origin !== origin || url.username || url.password || !/^\/(posts|pages)\/[^/]+\/?$/.test(url.pathname)) return [];
    if (seen.has(url.pathname)) return [];
    seen.add(url.pathname);
    return [{ url: url.pathname, title: item.title.trim().slice(0,200), at: item.at }];
  }).sort((a,b) => b.at-a.at).slice(0,12);
}
export function rememberDocument(previous, document, origin) {
  return normalizeHistory([document, ...previous.filter(item => item.url !== document.url)], origin);
}
