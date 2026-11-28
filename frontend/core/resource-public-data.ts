import { record, string, boolean, number, decimal } from '../shared/ledger-data';
import { paymentOrder, paymentAmount, orderID, paymentStatuses, exponent } from '../shared/payment-data';
import { units } from '../shared/currency-amount';
import type { PaymentOrder, PaymentStatus } from '../shared/payment-data';

const kinds = ['public', 'guest', 'login', 'membership', 'level', 'group', 'follow', 'like', 'comment', 'points', 'payment', 'tip', 'quiz', 'form'] as const;
export interface ResourceCondition {
  id: string; kind: typeof kinds[number]; label: string; passed: boolean; amount: number; currency: string;
  purchasable: boolean; purchase_reason: string; order_id: string; order_status: PaymentStatus | ''; order_amount: string; order_currency: string;
}
export interface ResourceGroup { name: string; passed: boolean; conditions: ResourceCondition[]; }
export interface ResourceDownload { id: string; name: string; provider: string; provider_name: string; url?: string; code?: string; password?: string; }
export interface ResourceVariant {
  id: string; name: string; kind: 'content' | 'download' | 'image' | 'video'; allowed: boolean; available: boolean; blocked: string;
  groups: ResourceGroup[]; downloads: ResourceDownload[]; questions: { id: string; title: string; options: string[] }[];
  fields: { id: string; label: string; type: 'text' | 'email' | 'textarea' | 'select' | 'checkbox'; required: boolean; options: string[] }[];
  quiz_passed: boolean; form_complete: boolean; attempts_remaining: number; pass_percent: number; body: string; media_url: string;
}
interface ResourceBase {
  post: number; id: string; title: string; starts_at: string; ends_at: string; timezone: string; remaining: number;
  selection: 'all' | 'first'; variants: ResourceVariant[];
}
export interface ResourceState extends ResourceBase {
  preview: false; author: number; revision: string; csrf: string; logged_in: boolean; user_id: string;
  payment_version: string; daily_used: number; total_used: number;
  channels: { key: string; name: string; currency: string }[]; refund_policy: string; disclaimer: string;
}
export interface ResourcePreview extends ResourceBase { preview: true; }
export type ResourceView = ResourceState | ResourcePreview;
export interface ResourceTarget { post: string; id: string; }
export interface ResourceDraft { fields: Record<string, string>; answers: Record<string, string>; }

function text(v: unknown, max = 4096): string { const s = string(v); if (s.length > max) throw new Error('资源文字数据过长'); return s; }
const optional = (v: unknown, max = 4096) => v === undefined ? '' : text(v, max);
export function resourceID(v: unknown): string { const s = string(v); if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(s)) throw new Error('资源标识未确认'); return s; }
function choice<T extends string>(v: unknown, values: readonly T[]): T { if (typeof v !== 'string' || !values.includes(v as T)) throw new Error('资源状态未确认'); return v as T; }
function rows<T>(v: unknown, max: number, parse: (v: unknown) => T, nullable = false): T[] {
  if (nullable && v === null) return [];
  if (!Array.isArray(v) || v.length > max) throw new Error('资源列表未确认'); return v.map(parse);
}
function unique<T extends { id: string }>(values: T[]): T[] { if (new Set(values.map(v => v.id)).size !== values.length) throw new Error('资源标识重复'); return values; }
function time(v: unknown): string { if (v == null) return ''; const s = text(v, 80); if (!/^\d{4}-\d{2}-\d{2}T/.test(s) || !Number.isFinite(Date.parse(s))) throw new Error('资源时间未确认'); return s; }
export function resourceURL(v: unknown, local = false): string {
  const s = text(v, 2048);
  if (!s || /[\\\u0000-\u0020\u007f]/.test(s)) throw new Error('资源地址未确认');
  const relative = s.startsWith('/') && !s.startsWith('//');
  const u = new URL(s, 'https://resource.invalid');
  if ((!relative && !/^https?:\/\//.test(s)) || !['http:', 'https:'].includes(u.protocol) || u.username || u.password || local && !relative) throw new Error('资源地址未确认');
  return s;
}
function condition(v: unknown): ResourceCondition {
  const r = record(v), kind = choice(r.kind, kinds), amount = number(r.amount, 0, 1e12), currency = text(r.currency, 64);
  if (['payment', 'points', 'tip'].includes(kind) && (amount < 1 || !resourceID(currency))) throw new Error('资源金额未确认');
  if (kind === 'payment') exponent(currency);
  const order_id = optional(r.order_id, 32), order_status = optional(r.order_status, 20), order_amount = optional(r.order_amount, 30), order_currency = optional(r.order_currency, 32);
  if (order_id) {
    if (!orderID(order_id) || !Object.hasOwn(paymentStatuses, order_status)) throw new Error('原订单未确认');
    units(paymentAmount(order_amount), exponent(order_currency));
  } else if (order_status || order_amount || order_currency) throw new Error('原订单未确认');
  // The Go API appends a display-only login prerequisite for anonymous
  // quota/quiz resources. It deliberately has no configured condition id.
  const synthetic = r.id === '' && kind === 'login' && r.passed === false && r.purchasable === false && amount === 0 && currency === '' && !order_id;
  return { id: synthetic ? '' : resourceID(r.id), kind, amount, currency, label: text(r.label, 1000), passed: boolean(r.passed), purchasable: boolean(r.purchasable), purchase_reason: optional(r.purchase_reason, 80), order_id, order_status: order_status as PaymentStatus | '', order_amount, order_currency };
}
function variant(value: unknown, preview: boolean): ResourceVariant {
  const r = record(value), kind = choice(r.kind, ['content', 'download', 'image', 'video']), allowed = boolean(r.allowed), available = boolean(r.available);
  const groups = rows(r.groups, 12, raw => { const g = record(raw), conditions = unique(rows(g.conditions, preview ? 16 : 17, condition)), passed = boolean(g.passed); if (!conditions.length || conditions.filter(c => c.id).length > 16 || passed !== conditions.every(c => c.passed) || preview && conditions.some(c => !c.id)) throw new Error('解锁条件状态未确认'); return { name: text(g.name, 180), passed, conditions }; });
  unique(groups.flatMap(g => g.conditions).filter(c => c.id)); if (!groups.length) throw new Error('解锁方式未确认');
  const downloads = unique(rows(r.downloads, 12, raw => {
    const d = record(raw), common = { id: resourceID(d.id), name: text(d.name, 180), provider: choice(d.provider, ['direct', 'baidu', 'aliyun', 'quark', 'lanzou', '115', '123pan', 'onedrive', 'google', 'dropbox']), provider_name: text(d.provider_name, 100) };
    if (preview) return { ...common, url: resourceURL(d.url), code: text(d.code, 128), password: text(d.password, 128) };
    if (d.url !== undefined || d.code !== undefined || d.password !== undefined) throw new Error('下载投影包含未核验交付');
    return common;
  }));
  const questions = unique(rows(r.questions, 20, raw => { const q = record(raw); if (q.answer !== undefined) throw new Error('答题投影包含答案'); return { id: resourceID(q.id), title: text(q.title, 500), options: rows(q.options, 10, v => text(v, 500), true) }; }, true));
  const fields = unique(rows(r.fields, 20, raw => { const f = record(raw); return { id: resourceID(f.id), label: text(f.label, 300), type: choice(f.type, ['text', 'email', 'textarea', 'select', 'checkbox']), required: boolean(f.required), options: rows(f.options, 30, v => text(v, 300), true) }; }, true));
  const blocked = preview ? '' : text(r.blocked, 80);
  if (allowed && (preview || !available || blocked || !groups.some(g => g.passed))) throw new Error('解锁状态与条件不一致');
  const body = preview || allowed ? text(r.body, 256 << 10) : '';
  const media_url = (preview || allowed) && (kind === 'image' || kind === 'video') ? resourceURL(r.media_url) : '';
  return { id: resourceID(r.id), name: text(r.name, 180), kind, allowed, available, blocked, groups, downloads, questions, fields,
    quiz_passed: preview ? false : boolean(r.quiz_passed), form_complete: preview ? false : boolean(r.form_complete), attempts_remaining: number(r.attempts_remaining, 0, 100), pass_percent: number(r.pass_percent, 1, 100), body, media_url };
}
function base(value: unknown, target: ResourceTarget, preview: boolean): ResourceBase {
  const r = record(value), post = number(r.post, 1), id = resourceID(r.id);
  if (String(post) !== target.post || id !== target.id) throw new Error('返回资源与当前页面不一致');
  const timezone = text(r.timezone, 100); try { new Intl.DateTimeFormat('en', { timeZone: timezone }); } catch { throw new Error('资源时区未确认'); }
  const starts_at = time(r.starts_at), ends_at = time(r.ends_at), selection = choice(r.selection, ['all', 'first']), variants = unique(rows(r.variants, 12, v => variant(v, preview)));
  if (!variants.length || starts_at && ends_at && Date.parse(starts_at) >= Date.parse(ends_at) || selection === 'first' && variants.filter(v => v.allowed).length > 1) throw new Error('资源版本状态未确认');
  return { post, id, timezone, starts_at, ends_at, selection, variants, title: text(r.title, 240), remaining: number(r.remaining, -1, 1000000) };
}
export function resourceState(value: unknown, target: ResourceTarget): ResourceState {
  const r = record(value), common = base(r, target, false), revision = text(r.revision, 64), user_id = decimal(r.user_id), logged_in = boolean(r.logged_in), csrf = text(r.csrf, 4096);
  if (!/^[a-f0-9]{64}$/.test(revision) || !csrf || logged_in !== (user_id !== '0') || r.preview !== undefined) throw new Error('资源身份或版本未确认');
  if (logged_in && common.variants.some(v => v.groups.some(g => g.conditions.some(c => !c.id)))) throw new Error('登录条件与当前账户不一致');
  const channels = rows(r.channels, 40, raw => { const c = record(raw), currency = text(c.currency, 32); exponent(currency); return { key: resourceID(c.key), name: text(c.name, 180), currency }; });
  if (new Set(channels.map(c => c.key)).size !== channels.length) throw new Error('支付渠道重复');
  const daily_used = number(r.daily_used), total_used = number(r.total_used); if (daily_used > total_used) throw new Error('下载次数未确认');
  return { ...common, preview: false, author: number(r.author, 0), revision, csrf, user_id, logged_in, payment_version: decimal(r.payment_version), daily_used, total_used, channels, refund_policy: text(r.refund_policy, 8000), disclaimer: text(r.disclaimer, 12000) };
}
export function resourcePreview(value: unknown, target: ResourceTarget): ResourcePreview {
  if (record(value).preview !== true) throw new Error('预览配置未确认'); return { ...base(value, target, true), preview: true };
}
export function downloadReceipt(value: unknown): { url: string; code: string; password: string; replayed: boolean; remaining: number } {
  const r = record(value); return { url: resourceURL(r.url), code: text(r.code, 128), password: text(r.password, 128), replayed: boolean(r.replayed), remaining: number(r.remaining, -1, 1000000) };
}
export function submissionReceipt(value: unknown): Pick<ResourceVariant, 'quiz_passed' | 'form_complete' | 'attempts_remaining'> {
  const r = record(value); return { quiz_passed: boolean(r.quiz_passed), form_complete: boolean(r.form_complete), attempts_remaining: number(r.attempts_remaining, 0, 100) };
}
export function pointsReceipt(value: unknown): void { const r = record(value); if (r.paid !== true) throw new Error('积分扣款尚未确认'); boolean(r.replayed); }
export function resourceOrder(value: unknown, owner: string, expected?: { id?: string; amount?: number; currency?: string }): PaymentOrder {
  const o = paymentOrder(value, owner, expected?.id);
  if (o.kind !== 'resource') throw new Error('原订单类型未确认');
  resourceURL(o.resource_url, true);
  if (expected?.currency && (o.currency !== expected.currency || units(o.amount, exponent(o.currency)) !== BigInt(expected.amount ?? -1))) throw new Error('返回订单金额与原确认不一致');
  return o;
}
export function resourceHistory(value: unknown): { title: string; url: string; created_at: string }[] {
  return rows(record(value).items, 200, raw => { const r = record(raw), created_at = time(r.created_at); if (!created_at) throw new Error('下载记录时间未确认'); return { title: text(r.title, 1000), url: resourceURL(r.url, true), created_at }; });
}
export class ResourceError extends Error {
  constructor(message: string, readonly code = '', readonly details?: unknown, readonly status = 0) { super(message); }
}
export function resourceError(value: unknown, status: number): ResourceError {
  try { const e = record(record(value).error); return new ResourceError(text(e.message, 4096) || '操作未完成，请重新核验', optional(e.code, 100), e.details, status); }
  catch { return new ResourceError('响应无法确认，请重新核验原操作', '', undefined, status); }
}
