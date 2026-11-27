import type { EngagementKind, EngagementStates } from './account-types';
export const object = (value: unknown): Record<string, unknown> => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('账号数据格式错误，请重试');
  return value as Record<string, unknown>;
};
export const text = (value: unknown): string => {
  if (typeof value !== 'string') throw new Error('账号文字格式错误，请重试');
  return value;
};
export const integer = (value: unknown, minimum = 0): number => {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < minimum) throw new Error('账号数值格式错误，请重试');
  return value;
};
export const flag = (value: unknown): boolean => {
  if (typeof value !== 'boolean') throw new Error('账号状态格式错误，请重试');
  return value;
};
export function errorMessage(error: unknown, fallback = '操作未完成，请重试'): string {
  return error && typeof error === 'object' && 'message' in error && typeof error.message === 'string' ? error.message || fallback : fallback;
}
export function parseEngagement(value: unknown): EngagementStates {
  const data = object(value), result: EngagementStates = { bookmark: [], like: [], follow: [] };
  for (const kind of ['bookmark', 'like', 'follow'] as const) {
    const items = data[kind]; if (!Array.isArray(items)) throw new Error('互动数据格式错误，请重试');
    const seen = new Set<number>();
    result[kind] = items.map(value => {
      const row = object(value), id = integer(row.target_id, 1);
      if (seen.has(id)) throw new Error('互动数据重复，请重试'); seen.add(id);
      return { target_id: id, count: integer(row.count), active: flag(row.active), collection: row.collection === undefined ? '' : text(row.collection) };
    });
  }
  return result;
}
export const engagementKind = (value: string): value is EngagementKind => ['bookmark', 'like', 'follow'].includes(value);
export function confirmed(value: unknown): void { if (object(value).ok !== true) throw new Error('操作结果尚未确认，请重试'); }
export function parseImport(value: unknown, preview: boolean): { added: number; existing: number; skipped: number } {
  const row = object(value);
  if (row.preview !== preview) throw new Error('导入结果尚未确认，请重新检查');
  return { added: integer(row.added), existing: integer(row.existing), skipped: integer(row.skipped) };
}

export interface LibraryItem {
  id: number; title: string; url: string; cover_url: string; excerpt: string; collection: string; author_name: string; author_url: string; published_at: string;
  name: string; username: string; status: string; content: string; created_at: string; updated_at: string; edit_url: string;
}
export interface LibraryData { pages: number; total: number; collections: Record<string, number>; items: LibraryItem[] }
const safeLink = (value: unknown): string => {
  const path = text(value);
  if (path && (!path.startsWith('/') || path.startsWith('//') || /[\\\u0000-\u0020\u007f]/.test(path))) throw new Error('账号链接格式错误，请重试');
  return path;
};
export function parseLibrary(value: unknown, kind: string): LibraryData {
  const data = object(value), pages = integer(data.pages, 1), total = integer(data.total);
  if (!Array.isArray(data.items)) throw new Error('个人内容格式错误，请重试');
  const collections: Record<string, number> = Object.create(null);
  if (kind === 'bookmark') for (const [key, count] of Object.entries(object(data.collections))) collections[key] = integer(count);
  const items = data.items.map(value => {
    const row = object(value);
    const item: LibraryItem = { id: integer(row.id, 1), title: '', url: '', cover_url: '', excerpt: '', collection: '', author_name: '', author_url: '', published_at: '', name: '', username: '', status: '', content: '', created_at: '', updated_at: '', edit_url: '' };
    if (kind === 'follow') { item.name = text(row.name); item.username = text(row.username); }
    else {
      item.title = text(row.title); item.url = safeLink(row.url);
      if (kind === 'posts' || kind === 'comments') {
        item.status = text(row.status);
        if (kind === 'posts') { item.excerpt = text(row.excerpt); item.updated_at = text(row.updated_at); item.edit_url = safeLink(row.edit_url); }
        else { item.content = text(row.content); item.created_at = text(row.created_at); }
      } else {
        item.excerpt = text(row.excerpt); item.collection = text(row.collection);
        item.author_name = text(row.author_name); item.author_url = safeLink(row.author_url); item.published_at = text(row.published_at);
        const cover = text(row.cover_url);
        // Public covers may be external; the account view only renders local covers.
        item.cover_url = cover.startsWith('/') && !cover.startsWith('//') ? safeLink(cover) : '';
      }
    }
    return item;
  });
  return { pages, total, collections, items };
}
export interface AccountProfile { id: number; version: number; username: string; display_name: string; bio: string; avatar_url: string; locale: string }
export function parseProfile(value: unknown, owner: number): AccountProfile {
  const row = object(value), id = integer(row.id, 1);
  if (id !== owner) throw new Error('登录账号已变化，请刷新后重试');
  const avatar = safeLink(row.avatar_url);
  if (avatar && !avatar.startsWith('/media/')) throw new Error('头像地址格式错误，请重试');
  return { id, version: integer(row.version, 1), username: text(row.username), display_name: text(row.display_name), bio: text(row.bio), avatar_url: avatar, locale: text(row.locale) };
}
