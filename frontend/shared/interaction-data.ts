import { object, text, integer, flag } from './account-data';
import type { ClientSession } from './newpress-client';
import type { Contact, Message, Conversation, Notice, NoticeBoundary, OutgoingMessage } from './interaction-types';

export function interactionFailure(value: unknown): Error & { status?: number; retryAfter?: number } {
  if (value instanceof Error) return value;
  const row = value && typeof value === 'object' && !Array.isArray(value) ? object(value) : {};
  const error = new Error(typeof row.message === 'string' ? row.message : '操作未完成，请重试');
  if (typeof row.name === 'string') error.name = row.name;
  return Object.assign(error, { status: typeof row.status === 'number' ? row.status : undefined, retryAfter: typeof row.retryAfter === 'number' ? row.retryAfter : undefined });
}

const array = (value: unknown): unknown[] => { if (!Array.isArray(value)) throw new Error('互动列表格式错误，请重试'); return value; };
const date = (value: unknown): string => { const result = text(value); if (!Number.isFinite(Date.parse(result))) throw new Error('互动时间格式错误，请重试'); return result; };
function unique<T>(values: T[], key: (value: T) => number | string): T[] {
  if (new Set(values.map(key)).size !== values.length) throw new Error('互动列表存在重复记录，请重试'); return values;
}
export function parseSocialSession(value: unknown): ClientSession {
  const row = object(value), authenticated = flag(row.authenticated);
  const session: ClientSession = { authenticated, configured: row.configured === undefined ? true : flag(row.configured) };
  if (!authenticated) return session;
  session.id = integer(row.id, 1);
  for (const key of ['name', 'csrf_token'] as const) if (row[key] !== undefined) session[key] = text(row[key]);
  for (const key of ['can_admin', 'can_publish'] as const) if (row[key] !== undefined) session[key] = flag(row[key]);
  return session;
}
export function parseContact(value: unknown, expected?: number): Contact {
  const row = object(value), id = integer(row.id, 1);
  if (expected !== undefined && id !== expected) throw new Error('联系人已变化，请重新打开会话');
  return { id, name: text(row.name), username: text(row.username), available: flag(row.available) };
}
export function parseContacts(value: unknown): { items: Contact[]; page: number; has_more: boolean } {
  const row = object(value), items = unique(array(row.items).map(item => parseContact(item)), item => item.id), more = flag(row.has_more);
  if (more && !items.length) throw new Error('联系人分页没有继续推进，请重试');
  return { items, page: integer(row.page, 1), has_more: more };
}
export function parseMessage(value: unknown, owner: number, peer: number): Message {
  const row = object(value), sender = integer(row.sender_id, 1), recipient = integer(row.recipient_id, 1);
  if (!(sender === owner && recipient === peer || sender === peer && recipient === owner)) throw new Error('消息不属于当前会话，请重新读取');
  return { id: integer(row.id, 1), sender_id: sender, recipient_id: recipient, content: text(row.content), nonce: row.nonce === undefined ? '' : text(row.nonce), created_at: date(row.created_at), read_at: row.read_at === null ? null : date(row.read_at) };
}
export function parseInbox(value: unknown, owner: number, before = 0): { items: Conversation[]; unread: number; next_cursor: number; has_more: boolean } {
  const row = object(value), more = flag(row.has_more), cursor = integer(row.next_cursor);
  const items = unique(array(row.items).map(value => {
    const item = object(value), peer = integer(item.peer_id, 1);
    if (peer === owner) throw new Error('会话联系人无效，请重试');
    return { peer_id: peer, contact: parseContact(item.contact, peer), last: parseMessage(item.last, owner, peer), unread: integer(item.unread) };
  }), item => item.peer_id);
  if (items.some((item, index) => { const previous = items[index - 1]; return before > 0 && item.last.id >= before || previous && item.last.id >= previous.last.id; }) || cursor !== (items.at(-1)?.last.id || 0) || more && (!cursor || !items.length)) throw new Error('会话分页边界无效，请重试');
  return { items, unread: integer(row.unread), next_cursor: cursor, has_more: more };
}
export interface MessagesPage { items: Message[]; contact: Contact; has_more: boolean; next_cursor: number; read_through: number }
export function parseMessages(value: unknown, owner: number, peer: number, after = 0, before = 0): MessagesPage {
  const row = object(value), items = unique(array(row.items).map(item => parseMessage(item, owner, peer)), item => item.id);
  const cursor = integer(row.next_cursor), more = flag(row.has_more);
  if (items.some(item => after > 0 && item.id <= after || before > 0 && item.id >= before)) throw new Error('消息分页边界无效，请重试');
  if (items.some((item, index) => { const previous = items[index - 1]; return previous && (after > 0 ? item.id <= previous.id : item.id >= previous.id); })) throw new Error('消息顺序错误，请重试');
  if (items.length && cursor !== items.at(-1)?.id || more && (!items.length || !cursor || after > 0 && cursor <= after)) throw new Error('消息分页没有继续推进，请重试');
  return { items, contact: parseContact(row.contact, peer), has_more: more, next_cursor: cursor, read_through: integer(row.read_through) };
}
export function parseSentMessage(value: unknown, owner: number, peer: number, pending: Pick<OutgoingMessage, 'nonce' | 'content'>): Message {
  const message = parseMessage(object(value).message, owner, peer);
  if (message.sender_id !== owner || message.nonce !== pending.nonce || message.content !== pending.content) throw new Error('消息发送结果尚未确认，请重试原消息');
  return message;
}
export interface NoticePage { items: Notice[]; unread: number; has_more: boolean; next_cursor: string; through: NoticeBoundary }
export function parseNotices(value: unknown): NoticePage {
  const row = object(value), boundary = object(row.through), cursor = text(row.next_cursor);
  if (cursor.length > 44 || !/^(?:-1|0|[1-9]\d*)\.(?:-1|0|[1-9]\d*)$/.test(cursor) || cursor.split('.').some(part => !Number.isSafeInteger(Number(part)))) throw new Error('通知分页边界无效，请重试');
  const items = unique(array(row.items).map(value => {
    const item = object(value), id = text(item.id), url = text(item.url);
    if (!/^[ma][1-9]\d*$/.test(id) || !Number.isSafeInteger(Number(id.slice(1)))) throw new Error('通知标识格式错误，请重试');
    if (url && (!url.startsWith('/') || url.startsWith('//') || /[\\\u0000-\u0020\u007f]/.test(url))) throw new Error('通知链接格式错误，请重试');
    return { id, kind: text(item.kind), title: text(item.title), url, peer_id: item.peer_id === undefined ? undefined : integer(item.peer_id, 1), created_at: date(item.created_at), read: flag(item.read) };
  }), item => item.id);
  const more = flag(row.has_more);
  if (more && !items.length) throw new Error('通知分页没有继续推进，请重试');
  return { items, unread: integer(row.unread), has_more: more, next_cursor: cursor, through: { message_id: integer(boundary.message_id), activity_id: integer(boundary.activity_id) } };
}
export function parseDraft(value: unknown, now = Date.now()): { draft: string; outbox: OutgoingMessage[] } {
  const empty = { draft: '', outbox: [] as OutgoingMessage[] };
  if (!value || typeof value !== 'object' || Array.isArray(value)) return empty;
  const row = object(value);
  if (typeof row.at !== 'number' || !Number.isFinite(row.at) || row.at > now || now - row.at > 86400000) return empty;
  const outbox: OutgoingMessage[] = [];
  for (const value of (Array.isArray(row.outbox) ? row.outbox : []).slice(-20)) {
    try {
      const item = object(value), content = text(item.content), nonce = text(item.nonce);
      if (!content || [...content].length > 2000 || !/^[A-Za-z0-9-]{16,80}$/.test(nonce) || outbox.some(item => item.nonce === nonce)) continue;
      outbox.push({ content, nonce, created_at: date(item.created_at), status: 'failed', error: '上次发送未确认，可安全重试', retryAt: 0 });
    } catch { /* Ignore corrupt stored entries without executing or sending them. */ }
  }
  return { draft: typeof row.draft === 'string' ? row.draft.slice(0, 8000) : '', outbox };
}
