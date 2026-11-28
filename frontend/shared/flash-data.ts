import { record, string, number, boolean, requestID } from './ledger-data';
import { parseEngagement } from './account-data';

export const flashActions = ['save', 'submit', 'withdraw', 'publish', 'reject', 'unpublish', 'trash', 'restore'] as const;
export type FlashAction = typeof flashActions[number];
export interface FlashDraft {
  title: string; body: string; source_name: string; source_url: string; comments: boolean;
  important: boolean; review_note: string; published_at: string; published_timezone: string;
}
export interface FlashBody extends FlashDraft { expected_user_id: number; id: number; version: number; action: FlashAction }
export interface FlashPending { schema: 1; endpoint: string; key: string; body: FlashBody; draft: FlashDraft; baseline: FlashDraft }
export const stateOnly = (action: FlashAction): boolean => ['withdraw', 'reject', 'unpublish', 'trash', 'restore'].includes(action);
export function flashAction(value: unknown): FlashAction {
  if (!flashActions.includes(value as FlashAction)) throw new Error('快报操作未确认');
  return value as FlashAction;
}
export function flashZone(value: unknown): string {
  const zone = string(value);
  if (!/^[A-Za-z][A-Za-z0-9_+./-]{0,99}$/.test(zone) || zone.includes('..')) throw new Error('发布时间的时区未确认');
  return zone;
}
export function flashDraft(value: unknown): FlashDraft {
  const raw = record(value);
  const field = (key: string, limit: number): string => {
    const result = string(raw[key]);
    if (result.length > limit || /\u0000/.test(result)) throw new Error('快报字段超出允许范围');
    return result;
  };
  const published_at = field('published_at', 16), published_timezone = field('published_timezone', 100);
  if (published_at && !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(published_at)) throw new Error('发布时间格式未确认');
  if (published_timezone) flashZone(published_timezone);
  return { title: field('title', 200), body: field('body', 10000), source_name: field('source_name', 200), source_url: field('source_url', 2048),
    comments: boolean(raw.comments), important: boolean(raw.important), review_note: field('review_note', 1000), published_at, published_timezone };
}
export function flashBody(value: unknown, owner: number, admin: boolean): FlashBody {
  const raw = record(value), expected_user_id = number(raw.expected_user_id, 1), id = number(raw.id), version = number(raw.version), action = flashAction(raw.action);
  if (expected_user_id !== owner || (id === 0) !== (version === 0)) throw new Error('快报账号或版本未确认');
  const draft = flashDraft(raw);
  if (!admin && (!['save', 'submit', 'withdraw'].includes(action) || draft.important || draft.review_note || draft.published_at || draft.published_timezone)) throw new Error('当前账号不能提交编辑专用字段');
  return { expected_user_id, id, version, action, ...draft };
}
export function flashPending(value: unknown, endpoint: string, owner: number, id: number): FlashPending {
  const raw = record(value);
  if (raw.schema !== 1 || raw.endpoint !== endpoint || !requestID(raw.key) || !['/api/v1/admin/flash', '/api/v1/flash/submissions'].includes(endpoint)) throw new Error('上次快报保存记录未确认');
  const body = flashBody(raw.body, owner, endpoint === '/api/v1/admin/flash');
  if (body.id !== id) throw new Error('上次保存属于另一份快报');
  const draft = flashDraft(raw.draft), baseline = flashDraft(raw.baseline);
  if (!sameFlashDraft(draft, {...flashDraft(body), review_note: body.action === 'reject' ? body.review_note : draft.review_note})) throw new Error('上次快报提交快照与请求不一致');
  return { schema: 1, endpoint, key: raw.key, body, draft, baseline };
}
export function flashReceipt(value: unknown, body: FlashBody): { id: number; version: number; replayed: boolean; message: string } {
  const raw = record(value), id = number(raw.id, 1), version = number(raw.version, 1), replayed = boolean(raw.replayed), message = string(raw.message);
  if ((body.id && id !== body.id) || version !== body.version + 1 || !message || message.length > 500) throw new Error('快报保存回执未确认，请核对原请求');
  return { id, version, replayed, message };
}
export const sameFlashDraft = (a: FlashDraft, b: FlashDraft): boolean => (Object.keys(a) as (keyof FlashDraft)[]).every(key => a[key] === b[key]);
export function flashEngagement(value: unknown, ids: number[]) {
  const data = parseEngagement(value);
  if (data.follow.length || [...data.like, ...data.bookmark].some(row => !ids.includes(row.target_id))) throw new Error('快报互动状态与当前列表不一致');
  return data;
}
export interface FlashEngagementBody { kind: 'like' | 'bookmark'; target_id: number; active: boolean; collection: string }
export function flashEngagementReceipt(value: unknown, body: FlashEngagementBody): void {
  const raw = record(value);
  if (raw.ok !== true || raw.target_id !== body.target_id || raw.active !== body.active || raw.collection !== body.collection) throw new Error('互动回执未确认，请重试原操作');
}
export function flashShare(value: string, origin: string): string {
  const url = new URL(value, origin);
  if (/[\\\u0000-\u0020\u007f]/.test(value) || url.origin !== origin || url.username || url.password || !url.pathname.startsWith('/flash/')) throw new Error('快报分享地址未确认');
  return url.href;
}
