import { object, text, integer, flag } from './account-data';
import type { CommentDraft, CommentPayload, CommentReceipt, CommentSubmission, PublicBadge, PublicComment, SavedEntry, GuestPreferences, CommunityDraft, CommunityPending } from './experience-types';

export function experienceError(value: unknown): Error & { code?: string; retryAfter?: number; details?: { fields?: Record<string, string> } } {
  const row = value && typeof value === 'object' && !Array.isArray(value) ? object(value) : {};
  const error = new Error(typeof row.message === 'string' ? row.message : '操作未完成，请重试');
  if (typeof row.name === 'string') error.name = row.name;
  let fields: Record<string, string> | undefined;
  if (row.details && typeof row.details === 'object' && 'fields' in row.details) {
    const raw = row.details.fields;
    if (raw && typeof raw === 'object' && !Array.isArray(raw)) fields = Object.fromEntries(Object.entries(raw).filter((pair): pair is [string, string] => typeof pair[1] === 'string'));
  }
  return Object.assign(error, { code: typeof row.code === 'string' ? row.code : undefined, retryAfter: typeof row.retryAfter === 'number' && Number.isFinite(row.retryAfter) ? Math.max(0, row.retryAfter) : undefined, details: fields ? { fields } : undefined });
}
export function parseCommentSubmission(value: unknown): CommentSubmission | null {
  try {
    const row = object(value), payload = object(row.payload), nonce = text(row.nonce);
    if (row.version !== 1 || !/^[a-zA-Z0-9._:-]{8,128}$/.test(nonce)) return null;
    const body = { parent_id: integer(payload.parent_id), content: text(payload.content), author_name: text(payload.author_name), email: text(payload.email), website: text(payload.website) };
    if (!body.content.trim() || [body.content, body.author_name, body.email, body.website].some(value => value.length > 20000)) return null;
    return { version: 1, nonce, owner: integer(row.owner), payload: body, uncertain: true };
  } catch { return null; }
}
export function parseCommentDraft(value: unknown, now = Date.now()): CommentDraft | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const row = object(value), at = row.at, parent = Number(row.parent);
  if (typeof row.content !== 'string' || !row.content || row.content.length > 20000 || typeof at !== 'number' || !Number.isFinite(at) || at > now || now - at >= 86400000) return null;
  return { content: row.content, parent: Number.isSafeInteger(parent) && parent > 0 ? String(parent) : '0', reply: typeof row.reply === 'string' ? row.reply : '', at };
}
const optionalText = (value: unknown): string | undefined => value === undefined ? undefined : text(value);
function parseBadge(value: unknown): PublicBadge {
  const row = object(value);
  return { key: text(row.key), kind: text(row.kind), name: text(row.name), icon: text(row.icon), color: text(row.color), style: text(row.style),
    description: optionalText(row.description), background: optionalText(row.background), image: optionalText(row.image), granted_at: optionalText(row.granted_at), expires_at: optionalText(row.expires_at), level: row.level === undefined ? undefined : integer(row.level) };
}
function parseComment(value: unknown, payload: CommentPayload, postID: string | undefined): PublicComment {
  const row = object(value), post = integer(row.post_id, 1), parent = row.parent_id === undefined ? 0 : integer(row.parent_id), name = text(row.author_name);
  if (postID && post !== Number(postID) || parent !== payload.parent_id || !name.trim()) throw new Error('评论回执不匹配');
  const result: PublicComment = { id: integer(row.id, 1), post_id: post, parent_id: parent, author_name: name };
  if (row.origin !== undefined && row.origin !== null) {
    const origin = object(row.origin);
    result.origin = { location: optionalText(origin.location), precision: optionalText(origin.precision), flag: optionalText(origin.flag), device: optionalText(origin.device), kind: optionalText(origin.kind) };
  }
  if (row.badges !== undefined && row.badges !== null) { if (!Array.isArray(row.badges)) throw new Error('评论身份格式错误'); result.badges = row.badges.map(parseBadge); }
  if (row.support !== undefined && row.support !== null) { const support = object(row.support); result.support = { supported: flag(support.supported), label: optionalText(support.label) }; }
  return result;
}
export function parseCommentReceipt(value: unknown, payload: CommentPayload, postID: string | undefined, submission: CommentSubmission | null, lookupOnly = false): CommentReceipt | null {
  try {
    const row = object(value), status = row.status;
    if (status !== 'approved' && status !== 'pending' && !(submission && status === 'received')) return null;
    const receipt: CommentReceipt = { status, notice: optionalText(row.notice), replayed: row.replayed === undefined ? undefined : flag(row.replayed), submission_key: optionalText(row.submission_key) };
    if (submission && receipt.submission_key !== submission.nonce || lookupOnly && receipt.replayed !== true || status === 'received' && (receipt.replayed !== true || row.comment != null)) return null;
    if (status === 'approved' || row.comment != null) receipt.comment = parseComment(row.comment, payload, postID);
    return receipt;
  } catch { return null; }
}
export function parseCommentToken(value: unknown): string | null {
  try { const token = text(object(object(value).submission).form_token); return token.trim() ? token : null; } catch { return null; }
}

export function safeExperienceURL(value: unknown, base: string): string | null {
  if (typeof value !== 'string' || !value.trim() || /[\\\u0000-\u0020\u007f]/.test(value)) return null;
  try {
    const url = new URL(value, base);
    return url.origin === new URL(base).origin && ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password && !/^\/(components|market|admin)(\/|$)/.test(url.pathname) ? url.pathname + url.search + url.hash : null;
  } catch { return null; }
}
export function savedEntry(title: string, value: unknown, base: string): SavedEntry {
  const url = new URL(safeExperienceURL(value, base) || new URL(base).pathname, base), post = /^\/posts\/[^/]+\/?$/.test(url.pathname);
  if (post) { url.search = ''; url.hash = ''; url.pathname = url.pathname.replace(/\/$/, ''); }
  const path = url.pathname + url.search + url.hash;
  return { key: post ? path : `${path}::${title}`, title: title.slice(0, 240), url: path };
}
export function parseGuestPreferences(value: unknown, base: string): GuestPreferences {
  const empty: GuestPreferences = { saved: [], following: [], following_ids: [], likes: [] };
  if (!value || typeof value !== 'object' || Array.isArray(value)) return empty;
  const row = object(value), saved = new Map<string, SavedEntry>();
  for (const value of (Array.isArray(row.saved) ? row.saved : []).slice(0, 200)) {
    if (!value || typeof value !== 'object' || Array.isArray(value)) continue;
    const item = object(value);
    if (typeof item.title !== 'string' || !safeExperienceURL(item.url, base)) continue;
    const entry = savedEntry(item.title, item.url, base); saved.set(entry.key, entry);
  }
  return { saved: [...saved.values()],
    following: Array.isArray(row.following) ? row.following.filter((v): v is string => typeof v === 'string').slice(0, 200) : [],
    following_ids: Array.isArray(row.following_ids) ? [...new Set(row.following_ids.filter((v): v is number => typeof v === 'number' && Number.isSafeInteger(v) && v > 0))].slice(0, 200) : [],
    likes: Array.isArray(row.likes) ? [...new Set(row.likes.filter((v): v is string => typeof v === 'string').slice(0, 500).map(v => v.replace(/^.*:(\d+)$/, 'post:$1')))] : [] };
}
export function parseCommunityPending(value: unknown): CommunityPending | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const row = object(value);
  if (typeof row.signature !== 'string' || row.signature.length > 10000 || typeof row.nonce !== 'string' || !/^[a-zA-Z0-9._:-]{8,128}$/.test(row.nonce) || row.guestContent !== null && typeof row.guestContent !== 'string') return null;
  return { signature: row.signature, nonce: row.nonce, guestContent: row.guestContent };
}
export function parseCommunityDraft(value: unknown, now = Date.now()): CommunityDraft {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return { content: '', pending: null };
  const row = object(value);
  if (typeof row.content !== 'string' || typeof row.at !== 'number' || !Number.isFinite(row.at) || row.at > now || now - row.at >= 86400000) return { content: '', pending: null };
  return { content: row.content.slice(0, 2000), pending: parseCommunityPending(row.pending) };
}
export function parseCommunityReceipt(value: unknown, status: 'publish' | 'draft', base: string): { id: number; status: string; url: string | null } {
  try {
    const row = object(value), post = row.content === undefined ? row : object(row.content), id = integer(post.id, 1);
    const url = safeExperienceURL(post.permalink || (typeof post.slug === 'string' ? `/posts/${post.slug}` : null), base);
    if (post.status === status && (status === 'draft' || url)) return { id, status, url };
  } catch { /* An incomplete 2xx response does not confirm the saved content. */ }
  throw new Error('保存结果暂未确认，草稿已保留，可安全重试');
}
