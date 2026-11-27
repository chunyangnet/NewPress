import { record, string, number, boolean, integer, identifier, key } from './ledger-data';
import { instant } from './site-time';
import type { ServiceCurrency } from './shop-service-types';

export interface ShopCurrency extends ServiceCurrency { enabled: boolean; balance?: string }
export interface ShopCoupon {
  code: string; title: string; kind: 'fixed' | 'percent'; value: string; minimum: string; maximum_discount: string; currency: string;
  kinds: string[]; modes: string[]; products: string[]; categories: string[]; levels: string[]; memberships: string[];
  starts_at: string; ends_at: string; created_at: string; claim_limit: number; use_limit: number; per_user_limit: number; home: boolean; enabled: boolean;
}
export interface CouponOption extends ShopCoupon {
  claimed: boolean; can_claim: boolean; user_claim_count: number; available_count: number; claim_count: number; remaining: number;
}
export interface CouponCatalog { coupons: CouponOption[]; currencies: ShopCurrency[] }
/** Only the session fields consumed by coupon cards; raffle quotas have their own consumer. */
export interface CouponSession extends CouponCatalog { user_id: string; csrf_token: string }
export interface CouponClaim { id: string; code: string; user_id: string; status: 'available'; claimed_at: string }
export interface PendingCoupon { owner: string; code: string; count: number; confirmed?: string; rejected?: boolean }

const zeroTime = '0001-01-01T00:00:00Z';
const text = (value: unknown, max: number) => { const v = string(value); if ([...v].length > max) throw new Error('优惠券文字过长'); return v; };
function date(value: unknown, zero = false): string {
  const v = string(value);
  if (!(zero && v === zeroTime) && !instant(v)) throw new Error('优惠券时间未确认');
  return v;
}
export function shopCurrencyKey(value: unknown, empty = false): string {
  if (empty && value === '' || value === 'CNY') return string(value);
  if (typeof value !== 'string' || !value.startsWith('wallet:') || !key(value.slice(7))) throw new Error('商城货币未确认');
  return value;
}
function amount(value: unknown): string { if (integer(value) > 1000000000000n) throw new Error('优惠券金额超出范围'); return string(value); }
function code(value: unknown): string {
  const v = string(value); if (!/^[A-Z0-9][A-Z0-9_-]{2,39}$/.test(v)) throw new Error('优惠券编号未确认'); return v;
}
function list<T>(value: unknown, parse: (item: unknown) => T, max: number): T[] {
  if (value === null) return [];
  if (!Array.isArray(value) || value.length > max) throw new Error('商城列表未确认');
  return value.map(parse);
}
function unique<T>(value: unknown, parse: (item: unknown) => T, max: number, identity: (item: T) => string): T[] {
  const result = list(value, parse, max);
  if (new Set(result.map(identity)).size !== result.length) throw new Error('商城列表包含重复编号');
  return result;
}
function currency(value: unknown): ShopCurrency {
  const v = record(value), result: ShopCurrency = { key: shopCurrencyKey(v.key), name: text(v.name, 160), symbol: text(v.symbol, 32), precision: number(v.precision, 0, 6), enabled: boolean(v.enabled) };
  if (!result.name || result.key === 'CNY' && result.precision !== 2) throw new Error('商城货币精度未确认');
  if (v.balance !== undefined) { integer(v.balance); result.balance = string(v.balance); }
  return result;
}
export function coupon(value: unknown): ShopCoupon {
  const v = record(value);
  if (v.kind !== 'fixed' && v.kind !== 'percent') throw new Error('优惠券类型未确认');
  const result: ShopCoupon = {
    code: code(v.code), title: text(v.title, 80), kind: v.kind, value: amount(v.value), minimum: amount(v.minimum), maximum_discount: amount(v.maximum_discount), currency: shopCurrencyKey(v.currency, true),
    kinds: list(v.kinds, string, 6), modes: list(v.modes, string, 5), products: list(v.products, string, 500), categories: list(v.categories, value => text(value, 80), 500), levels: list(v.levels, string, 500), memberships: list(v.memberships, string, 500),
    starts_at: date(v.starts_at), ends_at: date(v.ends_at), created_at: date(v.created_at, true), claim_limit: number(v.claim_limit, 0, 1000000), use_limit: number(v.use_limit, 0, 1000000), per_user_limit: number(v.per_user_limit, 1, 100), home: boolean(v.home), enabled: boolean(v.enabled),
  };
  if (!result.title.trim() || BigInt(result.value) <= 0n || Date.parse(result.ends_at) <= Date.parse(result.starts_at)
    || result.kind === 'fixed' && !result.currency || result.kind === 'percent' && (BigInt(result.value) > 10000n || !result.currency && (result.minimum !== '0' || result.maximum_discount !== '0'))
    || result.kinds.some(kind => !['virtual', 'physical', 'cdk', 'donation', 'raffle', 'bundle'].includes(kind))
    || result.modes.some(mode => !['direct', 'presale', 'deposit', 'installment', 'group'].includes(mode))) throw new Error('优惠券使用条件未确认');
  return result;
}
export function couponOption(value: unknown): CouponOption {
  const v = record(value), result = { ...coupon(v), claimed: boolean(v.claimed), can_claim: boolean(v.can_claim), user_claim_count: number(v.user_claim_count), available_count: number(v.available_count), claim_count: number(v.claim_count), remaining: number(v.remaining) };
  if (result.available_count > result.user_claim_count || result.user_claim_count > result.claim_count || result.claimed !== (result.available_count > 0)
    || result.remaining !== (result.claim_limit ? Math.max(0, result.claim_limit - result.claim_count) : 0)
    || result.can_claim && (result.user_claim_count >= result.per_user_limit || result.claim_limit > 0 && result.remaining === 0)) throw new Error('优惠券领取状态未确认');
  return result;
}
export function couponCatalog(value: unknown): CouponCatalog {
  const v = record(value), result = { coupons: unique(v.coupons, couponOption, 500, v => v.code), currencies: unique(v.currencies, currency, 32, v => v.key) };
  if (result.coupons.some(v => v.currency && !result.currencies.some(currency => currency.key === v.currency))) throw new Error('优惠券结算货币缺失');
  return result;
}
export function couponSession(value: unknown, owner?: string): CouponSession {
  const v = record(value), result = { ...couponCatalog(v), user_id: identifier(v.user_id), csrf_token: text(v.csrf_token, 4096) };
  if (!result.csrf_token || owner !== undefined && result.user_id !== owner) throw new Error('领券账号已变化，请重新载入');
  return result;
}
export function couponClaim(value: unknown, owner: string, expectedCode: string): CouponClaim {
  const v = record(record(value).claim), id = string(v.id);
  if (!/^sc_[a-f0-9]{28}$/.test(id) || v.user_id !== owner || v.code !== expectedCode || v.status !== 'available' || v.order_id !== undefined && v.order_id !== '' || v.used_at !== zeroTime) throw new Error('领取回执未确认，请先核对领取记录');
  return { id, code: code(v.code), user_id: identifier(v.user_id), status: 'available', claimed_at: date(v.claimed_at) };
}
export function pendingCoupon(value: unknown, owner: string, expectedCode: string): PendingCoupon {
  const v = record(value);
  if (v.owner !== owner || v.code !== expectedCode) throw new Error('领取核对记录不匹配');
  const result: PendingCoupon = { owner: identifier(v.owner), code: code(v.code), count: number(v.count) };
  if (v.confirmed !== undefined) { const id = string(v.confirmed); if (!/^sc_[a-f0-9]{28}$/.test(id)) throw new Error('领取核对记录无效'); result.confirmed = id; }
  if (v.rejected !== undefined) result.rejected = boolean(v.rejected);
  if (result.confirmed && result.rejected) throw new Error('领取核对状态无效');
  return result;
}
export function couponAmount(value: string, currency: string, currencies: ShopCurrency[]): string {
  const item = currencies.find(v => v.key === currency);
  if (!item) throw new Error('优惠券结算货币缺失');
  const n = integer(value), scale = 10n ** BigInt(item.precision);
  return `${item.symbol || item.name + ' '}${n / scale}${item.precision ? '.' + String(n % scale).padStart(item.precision, '0') : ''}`;
}
