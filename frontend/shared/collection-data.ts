// JSON boundaries shared by circles, link boards and editorial series.
export const record = (value: unknown): Record<string, unknown> => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('响应格式不正确，当前输入已保留。');
  return value as Record<string, unknown>;
};
export const text = (value: unknown): string => {
  if (typeof value !== 'string') throw new Error('响应文字格式不正确，当前输入已保留。');
  return value;
};
export const integer = (value: unknown, min = 0): number => {
  if (!Number.isSafeInteger(value) || typeof value !== 'number' || value < min) throw new Error('响应数值格式不正确，当前输入已保留。');
  return value;
};
export const flag = (value: unknown): boolean => { if (typeof value !== 'boolean') throw new Error('响应状态格式不正确，当前输入已保留。'); return value; };
export interface CircleCommand { action: string; version: number; key?: string; [key: string]: string | number | boolean | string[] | undefined }
export interface CircleReceipt { slug: string; version: number; topic_id: number; reply_id: number; reply_page: number }
export function circleReceipt(value: unknown, slug: string, command: CircleCommand): CircleReceipt {
  const row = record(value);
  const result = { slug: text(row.slug), version: integer(row.version, 1), topic_id: row.topic_id === undefined ? 0 : integer(row.topic_id, 1), reply_id: row.reply_id === undefined ? 0 : integer(row.reply_id, 1), reply_page: row.reply_page === undefined ? 0 : integer(row.reply_page, 1) };
  if (row.replay !== undefined) flag(row.replay);
  if (result.slug !== slug || result.version !== command.version + 1 || (['topic', 'reply', 'edit_topic', 'edit_reply'].includes(command.action) && !result.topic_id)
    || (['reply', 'edit_reply'].includes(command.action) && (!result.reply_id || !result.reply_page))
    || (['reply', 'edit_topic', 'edit_reply'].includes(command.action) && result.topic_id !== command.topic_id)
    || (command.action === 'edit_reply' && result.reply_id !== command.reply_id)) throw new Error('保存回执未匹配原操作，输入已保留，请重试原请求。');
  return result;
}
export function circleLatest(value: unknown, slug: string, owner: number) {
  const row = record(value), circle = record(row.circle);
  if (row.user_id !== owner || circle.slug !== slug || row.signed_in !== true || !text(row.csrf)) throw new Error('圈子或账号已变化，请重新载入。');
  return { csrf: text(row.csrf), circle: { version: integer(circle.version, 1), name: text(circle.name), visibility_label: text(circle.visibility_label), join_label: text(circle.join_label), role_label: text(circle.role_label), description: text(circle.description), rules: text(circle.rules) } };
}
export interface CircleFile { id: string; name: string; size: number; image: boolean }
export function circleUpload(value: unknown, slug: string, version: number) {
  const row = record(value), file = record(row.attachment), id = text(row.attachment_id);
  if (row.slug !== slug || !/^[a-f0-9]{64}$/.test(id) || file.id !== id || integer(row.version, 1) !== version + 1 || integer(file.size) > 10 * 1024 * 1024) throw new Error('附件回执未确认，请重试原上传。');
  if (row.replay !== undefined) flag(row.replay);
  return { version: integer(row.version, 1), attachment: { id, name: text(file.name), size: integer(file.size), image: flag(file.image) } };
}
export interface LinkCommand { action: string; version: number; item?: { id: number; [key: string]: string | number | boolean }; settings?: Record<string, string | boolean>; item_id?: number; key?: string }
export function linkReceipt(value: unknown, slug: string, command: LinkCommand) {
  const row = record(value), version = integer(row.version, 1), item_id = integer(row.item_id);
  // The Go protocol returns item_id only for save_item; deletion returns zero.
  if (text(row.slug) !== slug || version !== command.version + 1 || (command.action === 'save_item' ? (!item_id || !!command.item?.id && command.item.id !== item_id) : item_id !== 0)) throw new Error('保存结果未确认，输入已保留，请重试原请求。');
  flag(row.replay);
  return { slug, version, item_id };
}
export function linkMetadata(value: unknown) {
  const row = record(value);
  return { title: text(row.title), description: text(row.description), logo: text(row.logo), notice: text(row.notice) };
}
export interface SeriesChapter { id: number; title: string; status: string }
export interface SeriesInput { post_ids: number[]; kind: string; status: string; title: string; slug: string; description: string; cover_url: string }
export const seriesStatuses: Record<string, string> = { publish: '已发布', future: '定时发布', draft: '草稿', pending: '待审', private: '私密', trash: '回收站' };
export function seriesSources(value: unknown, page: number) {
  const row = record(value), seen = new Set<number>();
  if (!Array.isArray(row.items) || row.items.length > 20 || row.page !== page) throw new Error('文章列表格式不正确，保留上次查找结果。');
  const items = row.items.map(value => {
    const entry = record(value), id = integer(entry.id, 1), status = text(entry.status);
    if (seen.has(id) || !Object.hasOwn(seriesStatuses, status)) throw new Error('文章列表重复或状态无效。');
    seen.add(id); return { id, title: text(entry.title), status };
  });
  return { items, total: integer(row.total), has_more: flag(row.has_more) };
}
export function seriesReceipt(value: unknown, expectedID: number, version: number, input: SeriesInput) {
  const row = record(value), id = integer(row.id, 1), next = integer(row.version, 1);
  // The repository keeps the version on an unchanged save, including inputs
  // whose whitespace normalizes to the already stored value.
  if ((expectedID && expectedID !== id) || next < version || next > version + 1 || row.etag !== `"series-${id}-v${next}"` || row.kind !== input.kind || row.status !== input.status
    || text(row.title) !== input.title.trim() || text(row.description) !== input.description.trim() || text(row.cover_url) !== input.cover_url.trim() || !text(row.slug)
    || !Array.isArray(row.post_ids) || JSON.stringify(row.post_ids) !== JSON.stringify(input.post_ids)) throw new Error('保存回执未匹配当前选集，输入已保留，请重新载入确认。');
  return { id, version: next };
}
