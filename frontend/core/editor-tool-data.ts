export const record = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
export const integer = (value: unknown, minimum = 0): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum;
export const errorName = (error: unknown): string => record(error) && typeof error.name === 'string' ? error.name : '';
export const errorMessage = (error: unknown, fallback: string): string => record(error) && typeof error.message === 'string' && error.message ? error.message : fallback;
export const responseError = (payload: unknown, fallback: string): string => record(payload) ? errorMessage(payload.error, fallback) : fallback;

export interface ReusableContent { id: number; version: number; type: string; title: string; excerpt: string }
export function contentList(value: unknown): { items: ReusableContent[]; pagination: { page: number; pages: number } } {
  if (!record(value) || !Array.isArray(value.items) || !record(value.pagination)
    || !integer(value.pagination.page, 1) || !integer(value.pagination.pages)) throw new Error('内容列表响应无效，请重试');
  const items: ReusableContent[] = [];
  for (const item of value.items) {
    if (!record(item) || !integer(item.id, 1) || !integer(item.version, 1) || typeof item.title !== 'string') continue;
    items.push({ id: item.id, version: item.version, type: typeof item.type === 'string' ? item.type : 'post',
      title: item.title, excerpt: typeof item.excerpt === 'string' ? item.excerpt : '' });
  }
  return { items, pagination: { page: value.pagination.page, pages: value.pagination.pages } };
}

export function localCoverURL(value: unknown): value is string {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') || /[\u0000-\u0020\\<>"']/.test(value)) return false;
  try { const url = new URL(value, 'https://newpress.invalid'); return url.origin === 'https://newpress.invalid' && url.pathname === value.split(/[?#]/, 1)[0]; } catch { return false; }
}

export interface ArticleSummary { id: number; title: string; permalink: string; excerpt: string; author_name: string; category: string; cover_url: string }
export function articleSummary(item: unknown, editor: { localPostURL(value: string): boolean }): ArticleSummary | null {
  if (!record(item) || !integer(item.id, 1) || typeof item.title !== 'string' || !item.title.trim() || [...item.title].length > 200
    || typeof item.permalink !== 'string' || !editor.localPostURL(item.permalink)) return null;
  return { id: item.id, title: item.title, permalink: item.permalink, excerpt: typeof item.excerpt === 'string' ? [...item.excerpt].slice(0, 160).join('') : '',
    author_name: typeof item.author_name === 'string' ? item.author_name : '', category: typeof item.category === 'string' ? item.category : '',
    cover_url: localCoverURL(item.cover_url) ? item.cover_url : '' };
}

export interface LinkReport {
  post_id: number; version: number; truncated: boolean; counts: Record<string, number>;
  items: { status: string; url: string; message: string; target: string }[];
}
export function linkReport(value: unknown): LinkReport {
  if (!record(value) || !integer(value.post_id, 1) || !integer(value.version, 1) || typeof value.truncated !== 'boolean'
    || !record(value.counts) || !Array.isArray(value.items)) throw new Error('链接检查响应无效，请重试。');
  const counts: Record<string, number> = {};
  for (const [key, count] of Object.entries(value.counts)) { if (!integer(count)) throw new Error('链接数量响应无效，请重试。'); counts[key] = count; }
  const items = value.items.map(item => {
    if (!record(item) || typeof item.url !== 'string' || typeof item.status !== 'string' || typeof item.message !== 'string'
      || item.target !== undefined && typeof item.target !== 'string') throw new Error('链接检查明细无效，请重试。');
    return { url: item.url, status: item.status, message: item.message, target: item.target ?? '' };
  });
  return { post_id: value.post_id, version: value.version, truncated: value.truncated, counts, items };
}

export function seoSources(value: unknown): { total: number; page: number; pages: number; items: { id: number; title: string; type: string; status: string }[] } {
  if (!record(value) || !integer(value.total) || !integer(value.page, 1) || !integer(value.pages) || !Array.isArray(value.items)) throw new Error('内容查找响应无效，请重试。');
  const items = value.items.map(item => {
    if (!record(item) || !integer(item.id, 1) || typeof item.title !== 'string' || typeof item.type !== 'string' || typeof item.status !== 'string') throw new Error('内容查找条目无效，请重试。');
    return { id: item.id, title: item.title, type: item.type, status: item.status };
  });
  return { total: value.total, page: value.page, pages: value.pages, items };
}
