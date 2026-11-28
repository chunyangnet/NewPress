import { record, string, boolean, number, key, requestID, maximum } from './ledger-data';
import type { PublicBadge } from './experience-types';

export const badgeIcons = ['award', 'sparkles', 'star', 'heart', 'diamond', 'shield-check', 'badge-check', 'zap', 'pen-line', 'newspaper'] as const;
const actions = ['apply', 'equip', 'grant', 'revoke', 'approve', 'reject'] as const;
function invalid(message = '身份或打赏数据未确认，请重新载入'): never { throw new Error(message); }
function one<T extends string>(value: unknown, choices: readonly T[]): T {
  const text = string(value); return choices.find(v => v === text) ?? invalid();
}
function rows<T>(value: unknown, max: number, parse: (v: unknown) => T, nullable = false): T[] {
  if (nullable && value === null) return [];
  if (!Array.isArray(value) || value.length > max) invalid();
  return value.map(parse);
}
function unique<T>(values: T[], keyOf: (v: T) => string): T[] {
  if (new Set(values.map(keyOf)).size !== values.length) invalid('清单中存在重复编号'); return values;
}
function code(value: unknown): string { if (!key(value)) invalid('身份或货币编号无效'); return value; }
export function recognitionText(value: unknown, min: number, max: number): string {
  const text = string(value), length = [...text].length;
  if (text !== text.trim() || length < min || length > max || /[\p{Cc}\p{Cf}\p{Cs}<>]/u.test(text)) invalid('文字长度或格式无效');
  return text;
}
function time(value: unknown, zero = false): string {
  const text = string(value);
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?(?:Z|[+-]\d\d:\d\d)$/.test(text) || !Number.isFinite(Date.parse(text)) || (!zero && text.startsWith('0001-'))) invalid();
  return text;
}
function color(value: unknown): string { const text = string(value); if (!/^#[0-9a-f]{6}$/.test(text)) invalid(); return text; }
export function localBadgeImage(value: unknown): value is string {
  if (typeof value !== 'string' || !value.startsWith('/media/') || value.length > 500 || /[\\\r\n\t?#]/.test(value)) return false;
  try { const path = decodeURIComponent(value); return !path.includes('..') && !/[\\\r\n\t]/.test(path) && /\.(png|jpe?g|gif|webp|avif)$/i.test(path); } catch { return false; }
}
export function tipUnits(value: unknown, precision: number): bigint {
  const text = string(value);
  if (text.length > 27 || !new RegExp('^(?:0|[1-9][0-9]{0,18})' + (precision ? '(?:\\.[0-9]{1,' + precision + '})?' : '') + '$').test(text)) invalid('请输入符合当前币种精度的正数');
  const [whole = '', fraction = ''] = text.split('.');
  const units = BigInt(whole) * 10n ** BigInt(precision) + BigInt(fraction.padEnd(precision, '0') || '0');
  if (units > maximum) invalid('金额超出范围'); return units;
}
function presets(value: unknown): string[] {
  const list = rows(value, 6, v => { const text = string(v); if (tipUnits(text, 6) <= 0n) invalid(); return text; });
  if (!list.length) invalid(); return list;
}
export interface RecognitionBadge extends PublicBadge {
  kind: 'title' | 'medal' | 'verified'; description: string; background: string; image: string; enabled: boolean;
}
export interface RecognitionSettings {
  version: number; tips_enabled: boolean; hide_amounts: boolean; allow_hide_amount: boolean;
  verification_enabled: boolean; verification_help: string; max_medals: number;
  currencies: { key: string; presets: string[] }[]; badges: RecognitionBadge[];
}
export function recognitionBadge(value: unknown): RecognitionBadge {
  const b = record(value), image = string(b.image); if (image && !localBadgeImage(image)) invalid('请选择有效的媒体库图片');
  return { key: code(b.key), kind: one(b.kind, ['title', 'medal', 'verified']), name: recognitionText(b.name, 1, 20), description: recognitionText(b.description, 0, 160),
    icon: one(b.icon, badgeIcons), color: color(b.color), background: color(b.background), style: one(b.style, ['soft', 'outline', 'solid']), image, enabled: boolean(b.enabled) };
}
export function recognitionSettings(value: unknown): RecognitionSettings {
  const s = record(value), currencies = unique(rows(s.currencies, 16, v => { const c = record(v); return { key: code(c.key), presets: presets(c.presets) }; }, true), c => c.key);
  const tips_enabled = boolean(s.tips_enabled); if (tips_enabled && !currencies.length) invalid('请至少选择一个打赏钱包');
  return { version: number(s.version, 1), tips_enabled, hide_amounts: boolean(s.hide_amounts), allow_hide_amount: boolean(s.allow_hide_amount),
    verification_enabled: boolean(s.verification_enabled), verification_help: recognitionText(s.verification_help, 0, 500), max_medals: number(s.max_medals, 1, 8), currencies,
    badges: unique(rows(s.badges, 100, recognitionBadge, true), b => b.key) };
}
export interface RecognitionApplication { key: string; label: string; evidence: string; status: 'pending' | 'approved' | 'rejected' | 'revoked'; reason: string; updated_at: string }
export interface RecognitionIdentity {
  version: number; grants: { key: string; label: string; granted_at: string; expires_at: string; actor_id: number }[];
  equipped: string[]; application?: RecognitionApplication;
  history: { action: typeof actions[number]; key: string; reason: string; actor_id: number; created_at: string }[];
}
function application(value: unknown): RecognitionApplication {
  const a = record(value); return { key: code(a.key), label: recognitionText(a.label, 1, 30), evidence: recognitionText(a.evidence, 1, 1000),
    status: one(a.status, ['pending', 'approved', 'rejected', 'revoked']), reason: recognitionText(a.reason, 0, 200), updated_at: time(a.updated_at) };
}
export function recognitionIdentity(value: unknown): RecognitionIdentity {
  const a = record(value), grants = unique(rows(a.grants, 100, v => {
    const g = record(v), granted_at = time(g.granted_at), expires_at = time(g.expires_at, true);
    if (!expires_at.startsWith('0001-') && Date.parse(expires_at) <= Date.parse(granted_at)) invalid();
    return { key: code(g.key), label: recognitionText(g.label, 0, 30), granted_at, expires_at, actor_id: number(g.actor_id, 1) };
  }, true), g => g.key), equipped = unique(rows(a.equipped, 100, code, true), k => k);
  if (equipped.some(k => !grants.some(g => g.key === k))) invalid();
  const result: RecognitionIdentity = { version: number(a.version, 1), grants, equipped, history: rows(a.history, 100, v => {
    const h = record(v); return { action: one(h.action, actions), key: string(h.key), reason: recognitionText(h.reason, 0, 200), actor_id: number(h.actor_id, 1), created_at: time(h.created_at) };
  }, true) };
  if (a.application != null) result.application = application(a.application); return result;
}
function publicBadges(value: unknown): PublicBadge[] {
  return unique(rows(value, 100, v => {
    const b = record(v), result: PublicBadge = { key: code(b.key), kind: one(b.kind, ['title', 'medal', 'verified']), name: recognitionText(b.name, 1, 20),
      icon: one(b.icon, badgeIcons), color: color(b.color), style: one(b.style, ['soft', 'outline', 'solid']) };
    if (b.description !== undefined) result.description = recognitionText(b.description, 0, 160);
    if (b.background !== undefined) result.background = color(b.background);
    if (b.image !== undefined) { const image = string(b.image); if (image && !localBadgeImage(image)) invalid(); result.image = image; }
    if (b.granted_at !== undefined) result.granted_at = time(b.granted_at);
    if (b.expires_at !== undefined) result.expires_at = time(b.expires_at);
    return result;
  }, true), b => b.key);
}
export interface RecognitionAccount { user_id: number; username: string; name: string; identity: RecognitionIdentity; badges: PublicBadge[] }
export function recognitionAccount(value: unknown, username: string): RecognitionAccount {
  const a = record(value), user = string(a.username);
  // Match Go's case-insensitive username lookup without restricting Unicode names.
  if (user.toLowerCase() !== username.trim().toLowerCase()) invalid('查询返回的成员不一致');
  return { user_id: number(a.user_id, 1), username: user, name: string(a.name), identity: recognitionIdentity(a.identity), badges: publicBadges(a.badges) };
}
export function recognitionSocial(value: unknown, user: number) {
  const a = record(value); if (number(a.user_id, 1) !== user) invalid('登录账号已变化，请刷新页面');
  const identity = recognitionIdentity(a.identity), catalog = unique(rows(a.catalog, 100, recognitionBadge, true), b => b.key);
  if (identity.grants.some(g => !catalog.some(b => b.key === g.key)) || (identity.application && !catalog.some(b => b.key === identity.application?.key && b.kind === 'verified'))) invalid();
  return { user_id: user, identity, catalog, badges: publicBadges(a.badges), verification_enabled: boolean(a.verification_enabled), verification_help: recognitionText(a.verification_help, 0, 500), max_medals: number(a.max_medals, 1, 8) };
}
export function recognitionAdmin(value: unknown) {
  const a = record(value), settings = recognitionSettings(a.settings), pending = unique(rows(a.pending, 100, v => {
    const p = record(v), item = application(p.application); if (item.status !== 'pending') invalid();
    return { user_id: number(p.user_id, 1), username: string(p.username), name: string(p.name), version: number(p.version, 1), application: item };
  }), p => String(p.user_id));
  const pending_total = number(a.pending_total, pending.length);
  return { settings, pending, pending_total, currencies: unique(rows(a.currencies, 32, v => { const c = record(v); return { key: code(c.key), name: string(c.name), enabled: boolean(c.enabled), transfers: boolean(c.transfers) }; }), c => c.key) };
}
export interface IdentityMutation {
  user_id: number; version: number; action: typeof actions[number]; key?: string; label?: string; evidence?: string; reason?: string; days?: number; equipped?: string[];
}
export function identityMutation(value: unknown): IdentityMutation {
  const b = record(value), action = one(b.action, actions);
  const result: IdentityMutation = { user_id: number(b.user_id, 1), version: number(b.version, 1), action };
  if (action === 'equip') result.equipped = unique(rows(b.equipped, 8, code), k => k);
  else {
    result.key = code(b.key); result.label = recognitionText(b.label ?? '', action === 'apply' ? 1 : 0, 30);
    result.evidence = recognitionText(b.evidence ?? '', action === 'apply' ? 1 : 0, 1000);
    result.reason = recognitionText(b.reason ?? '', action === 'apply' ? 0 : 1, 200); result.days = number(b.days ?? 0, 0, 3650);
  }
  return result;
}
export function identityReceipt(value: unknown, body: IdentityMutation, actor: number, before?: RecognitionIdentity): RecognitionIdentity {
  const identity = recognitionIdentity(record(value).identity), event = identity.history.at(-1), app = identity.application;
  if (identity.version !== body.version + 1 || !event || event.action !== body.action || event.key !== (body.key || '') || event.reason !== (body.reason || '') || event.actor_id !== actor) invalid('身份变更回执未确认，请先核对状态');
  if (body.action === 'equip' && JSON.stringify(identity.equipped) !== JSON.stringify(body.equipped)) invalid();
  if (body.action === 'apply' && (!app || app.status !== 'pending' || app.key !== body.key || app.label !== body.label || app.evidence !== body.evidence || app.updated_at !== event.created_at)) invalid();
  if (body.action === 'revoke' && (identity.grants.some(g => g.key === body.key) || identity.equipped.includes(body.key || ''))) invalid();
  if (body.action === 'approve' || body.action === 'reject') if (!app || app.key !== body.key || app.status !== (body.action === 'approve' ? 'approved' : 'rejected') || app.reason !== body.reason || app.updated_at !== event.created_at) invalid();
  if (before && (body.action === 'approve' || body.action === 'reject')) {
    const original = before.application;
    if (!original || original.status !== 'pending' || original.key !== body.key || app?.label !== original.label || app?.evidence !== original.evidence) invalid('认证回执与原申请不一致');
  }
  if (body.action === 'grant' || body.action === 'approve') {
    const grant = identity.grants.find(g => g.key === body.key);
    if (!grant || grant.actor_id !== actor || grant.label !== (body.action === 'approve' ? app?.label : body.label || '') || grant.granted_at !== event.created_at) invalid();
    if ((body.days || 0) === 0 ? !grant.expires_at.startsWith('0001-') : Date.parse(grant.expires_at) - Date.parse(grant.granted_at) !== (body.days || 0) * 86400000) invalid();
  }
  return identity;
}
export function tipState(value: unknown, user: number) {
  const d = record(value); if (number(d.user_id, 1) !== user) invalid('打赏账号不一致，请重新打开');
  const currencies = unique(rows(d.currencies, 16, v => {
    const c = record(v), precision = number(c.precision, 0, 6), balance = string(c.balance), min = string(c.min), max = string(c.max);
    tipUnits(balance, precision); if (tipUnits(min, precision) <= 0n || tipUnits(max, precision) < tipUnits(min, precision)) invalid();
    return { key: code(c.key), name: string(c.name), symbol: string(c.symbol), precision, version: number(c.version, 1), balance, min, max, presets: presets(c.presets) };
  }), c => c.key);
  return { user_id: user, enabled: boolean(d.enabled), author: string(d.author), settings_version: number(d.settings_version, 1), hide_amounts: boolean(d.hide_amounts), allow_hide_amount: boolean(d.allow_hide_amount), currencies };
}
export interface TipMutation { post_id: number; request_id: string; currency: string; amount: string; hidden: boolean; settings_version: number; currency_version: number }
export function tipPending(value: unknown, post: number): TipMutation {
  const b = record(value); if (number(b.post_id, 1) !== post || !requestID(b.request_id)) invalid('上次打赏记录无效');
  const amount = string(b.amount), precision = amount.split('.')[1]?.length || 0;
  if (precision > 6 || tipUnits(amount, precision) <= 0n) invalid();
  return { post_id: post, request_id: b.request_id, currency: code(b.currency), amount, hidden: boolean(b.hidden), settings_version: number(b.settings_version, 1), currency_version: number(b.currency_version, 1) };
}
export function tipReceipt(value: unknown, body: TipMutation): string {
  const r = record(value); if (r.completed !== true || r.request_id !== body.request_id) invalid('打赏结果暂未确认，请重试原操作'); return string(r.message);
}
