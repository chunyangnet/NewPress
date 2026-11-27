import { record, string, boolean, number, identifier, integer, key, list, requestID } from './ledger-data';
import { units } from './currency-amount';

export const paymentStatuses = { pending: '待支付', paid: '权益发放中', completed: '已完成', cancelled: '已取消', refunded: '已退款', review: '待人工核对' };
export type PaymentStatus = keyof typeof paymentStatuses;
export type PurchaseKind = 'recharge' | 'membership';
export type PaymentKind = PurchaseKind | 'exchange' | 'resource' | 'shop';
export interface PaymentCurrency { key: string; name: string; symbol: string; precision: number; enabled: boolean }
export interface PaymentProduct { key: string; title: string; enabled: boolean; kind: PurchaseKind; currency: string; price: string; target_currency: string; credit: string; plan: string }
export interface PaymentExchange { key: string; title: string; enabled: boolean; from: string; to: string; from_units: string; to_units: string; fee_bps: number; minimum: string; maximum: string }
export interface PublicChannel { key: string; name: string; currency: string; provider: string }
export interface Checkout { provider_order_id: string; url?: string; qr_code?: string; method?: string; fields?: Record<string, string> }
export interface PaymentOrder {
  id: string; user_id: string; kind: PaymentKind; title: string; status: PaymentStatus; amount: string; currency: string;
  checkout: Checkout; qr_url: string; target_currency: string; credit: string; plan: string; days: number;
  created_at: string; expires_at: string; paid_at: string; completed_at: string; error_code: string;
  disclaimer: string; refund_policy: string; resource_url: string; resource_ends_at: string;
}
export interface BillingConfig { version: string; user_id: string; csrf_token: string; enabled: boolean; require_verified_email: boolean; disclaimer: string; refund_policy: string; currencies: PaymentCurrency[]; channels: PublicChannel[]; products: PaymentProduct[]; exchanges: PaymentExchange[]; orders: PaymentOrder[] }
export const configFields = ['endpoint', 'merchant_id', 'app_id', 'api_key', 'secret', 'private_key', 'public_key', 'certificate_serial', 'platform_serial', 'webhook_id', 'payment_type'] as const;
export type ConfigField = typeof configFields[number];
export type ProviderConfig = Record<ConfigField, string> & { provider: string; sandbox: boolean };
export const secretFields = ['api_key', 'secret', 'private_key'] as const;
export type SecretField = typeof secretFields[number];
export interface PaymentChannel { key: string; name: string; currency: string; enabled: boolean; config: ProviderConfig; saved_secrets: SecretField[]; clear_secrets: SecretField[] }
export interface PaymentSettings { version: string; enabled: boolean; require_verified_email: boolean; order_minutes: number; disclaimer: string; refund_policy: string; channels: PaymentChannel[]; products: PaymentProduct[]; exchanges: PaymentExchange[] }
export interface PaymentProvider { key: string; name: string; currencies: string[]; fields: ConfigField[]; supported: boolean; note: string }
export interface PaymentAdminConfig { settings: PaymentSettings; providers: PaymentProvider[]; currencies: PaymentCurrency[]; plans: { key: string; name: string; days: number; enabled: boolean }[]; public_url: string }
export interface AdminPaymentOrder extends PaymentOrder { channel: string; provider: string; transaction_id: string; credit_entry: string; member_entry: string; source_entry: string }

export const orderID = (value: unknown): value is string => typeof value === 'string' && /^np_[0-9a-f]{28}$/.test(value);
export function exponent(value: string): number { if (value === 'JPY') return 0; if (['CNY', 'USD', 'EUR', 'GBP', 'HKD', 'LDC'].includes(value)) return 2; throw new Error('支付币种未确认'); }
const text = (value: unknown, max = 16000) => { const v = string(value); if (v.length > max) throw new Error('文字数据过长'); return v; };
const token = (value: unknown, empty = false) => { if (empty && value === '') return ''; if (!key(value)) throw new Error('商品或货币标识未确认'); return value; };
const whole = (value: unknown, positive = false) => { const n = integer(value); if (positive && n === 0n) throw new Error('数量必须大于零'); return string(value); };
const timestamp = (value: unknown) => { const v = string(value); if (!v || !Number.isFinite(Date.parse(v))) throw new Error('订单时间未确认'); return v; };
const currency = (value: unknown) => { const v = string(value); exponent(v); return v; };
function unique<T>(value: unknown, parse: (item: unknown) => T, max: number, identity: (item: T) => string): T[] {
  if (!Array.isArray(value) || value.length > max) throw new Error('列表数据未确认');
  const rows = list(value, parse); if (new Set(rows.map(identity)).size !== rows.length) throw new Error('列表包含重复记录'); return rows;
}
export function paymentAmount(value: unknown): string {
  const v = string(value); if (!/^(0|[1-9][0-9]*)(\.[0-9]{1,6})?$/.test(v)) throw new Error('订单金额未确认');
  units(v, v.split('.')[1]?.length || 0); return v;
}
export function paymentCurrency(value: unknown): PaymentCurrency {
  const v = record(value); return { key: token(v.key), name: text(v.name, 160), symbol: text(v.symbol, 80), precision: number(v.precision, 0, 6), enabled: boolean(v.enabled) };
}
export function paymentProduct(value: unknown): PaymentProduct {
  const v = record(value); if (v.kind !== 'recharge' && v.kind !== 'membership') throw new Error('商品类型未确认');
  const result: PaymentProduct = { key: token(v.key), title: text(v.title, 240), enabled: boolean(v.enabled), kind: v.kind, currency: currency(v.currency), price: whole(v.price, true), target_currency: token(v.target_currency, true), credit: whole(v.credit), plan: token(v.plan, true) };
  if (result.kind === 'recharge' && (!result.target_currency || BigInt(result.credit) <= 0n) || result.kind === 'membership' && !result.plan) throw new Error('商品权益未确认');
  return result;
}
export function paymentExchange(value: unknown): PaymentExchange {
  const v = record(value), result = { key: token(v.key), title: text(v.title, 240), enabled: boolean(v.enabled), from: token(v.from), to: token(v.to), from_units: whole(v.from_units, true), to_units: whole(v.to_units, true), fee_bps: number(v.fee_bps, 0, 5000), minimum: whole(v.minimum, true), maximum: whole(v.maximum, true) };
  if (result.from === result.to || BigInt(result.minimum) > BigInt(result.maximum)) throw new Error('兑换范围未确认'); return result;
}
function checkout(value: unknown): Checkout {
  const v = record(value), result: Checkout = { provider_order_id: text(v.provider_order_id, 4096) };
  if (v.url !== undefined) result.url = text(v.url, 32768);
  if (v.qr_code !== undefined) result.qr_code = text(v.qr_code, 2048);
  if (v.method !== undefined) result.method = text(v.method, 16);
  if (v.fields !== undefined) { const rows = Object.entries(record(v.fields)); if (rows.length > 100 || rows.some(([k]) => !k || k.length > 200)) throw new Error('支付表单未确认'); result.fields = Object.fromEntries(rows.map(([k, v]) => [k, text(v, 32768)])); }
  return result;
}
export function paymentOrder(value: unknown, owner?: string, expectedID?: string, allowShop = false): PaymentOrder {
  const v = record(value);
  if (!orderID(v.id) || expectedID && v.id !== expectedID || owner && v.user_id !== owner || typeof v.status !== 'string' || !Object.hasOwn(paymentStatuses, v.status) || !['recharge', 'membership', 'exchange', 'resource', ...(allowShop ? ['shop'] : [])].includes(string(v.kind))) throw new Error('订单回执未确认，请刷新原订单');
  if (v.kind === 'exchange') token(v.currency); else currency(v.currency);
  return { id: v.id, user_id: identifier(v.user_id), kind: v.kind as PaymentKind, title: text(v.title, 240), status: v.status as PaymentStatus, amount: paymentAmount(v.amount), currency: text(v.currency, 32), checkout: checkout(v.checkout), qr_url: text(v.qr_url, 4096), target_currency: token(v.target_currency, true), credit: paymentAmount(v.credit), plan: token(v.plan, true), days: number(v.days, 0, 3650), created_at: timestamp(v.created_at), expires_at: timestamp(v.expires_at), paid_at: timestamp(v.paid_at), completed_at: timestamp(v.completed_at), error_code: text(v.error_code, 240), disclaimer: text(v.disclaimer, 12000), refund_policy: text(v.refund_policy, 8000), resource_url: text(v.resource_url, 4096), resource_ends_at: timestamp(v.resource_ends_at) };
}
export function billingConfig(value: unknown, owner: string): BillingConfig {
  const v = record(value); if (v.user_id !== owner) throw new Error('账户信息未确认，请重新载入');
  const result = { version: identifier(v.version), user_id: identifier(v.user_id), csrf_token: text(v.csrf_token, 4096), enabled: boolean(v.enabled), require_verified_email: boolean(v.require_verified_email), disclaimer: text(v.disclaimer, 12000), refund_policy: text(v.refund_policy, 8000), currencies: unique(v.currencies, paymentCurrency, 32, v => v.key), channels: unique(v.channels, item => { const v = record(item); return { key: token(v.key), name: text(v.name, 180), currency: currency(v.currency), provider: text(v.provider, 80) }; }, 40, v => v.key), products: unique(v.products, paymentProduct, 100, v => v.key), exchanges: unique(v.exchanges, paymentExchange, 100, v => v.key), orders: unique(v.orders, item => paymentOrder(item, owner), 200, v => v.id) };
  if (result.products.some(p => !p.enabled) || result.exchanges.some(x => !x.enabled)) throw new Error('可用方案列表未确认');
  return result;
}
export function emptyProviderConfig(provider: string): ProviderConfig { return { ...Object.fromEntries(configFields.map(k => [k, ''])) as Record<ConfigField, string>, provider, sandbox: false }; }
export function paymentSettings(value: unknown, previous?: string, masked = true): PaymentSettings {
  const v = record(value), version = identifier(v.version);
  if (previous && BigInt(version) !== BigInt(previous) + 1n) throw new Error('保存版本未确认，请重新载入核对');
  return { version, enabled: boolean(v.enabled), require_verified_email: boolean(v.require_verified_email), order_minutes: number(v.order_minutes, 5, 1440), disclaimer: text(v.disclaimer, 12000), refund_policy: text(v.refund_policy, 8000),
    channels: unique(v.channels, item => { const row = record(item), cfg = record(row.config), config = emptyProviderConfig(text(cfg.provider, 80)); config.sandbox = boolean(cfg.sandbox); for (const field of configFields) config[field] = text(cfg[field]);
      if (masked && secretFields.some(field => config[field] !== '')) throw new Error('商户密钥回执未脱敏，请重新载入核对');
      const secrets = (raw: unknown) => unique(raw === undefined ? [] : raw, value => { if (!secretFields.includes(value as SecretField)) throw new Error('密钥状态未确认'); return value as SecretField; }, 3, v => v);
      return { key: token(row.key), name: text(row.name, 180), currency: currency(row.currency), enabled: boolean(row.enabled), config, saved_secrets: secrets(row.saved_secrets), clear_secrets: secrets(row.clear_secrets) }; }, 40, v => v.key),
    products: unique(v.products, paymentProduct, 100, v => v.key), exchanges: unique(v.exchanges, paymentExchange, 100, v => v.key) };
}
export function paymentAdminConfig(value: unknown): PaymentAdminConfig {
  const v = record(value), settings = paymentSettings(v.settings), providers = unique(v.providers, item => { const row = record(item); return { key: text(row.key, 80), name: text(row.name, 180), currencies: unique(row.currencies, currency, 10, v => v), fields: unique(row.fields, value => { if (!configFields.includes(value as ConfigField)) throw new Error('渠道字段未确认'); return value as ConfigField; }, configFields.length, v => v), supported: boolean(row.supported), note: text(row.note) }; }, 40, v => v.key);
  if (!providers.length || settings.channels.some(ch => !providers.some(p => p.key === ch.config.provider && p.currencies.includes(ch.currency)))) throw new Error('渠道协议未确认');
  return { settings, providers, currencies: unique(v.currencies, paymentCurrency, 32, v => v.key), plans: unique(v.plans, item => { const v = record(item); return { key: token(v.key), name: text(v.name, 120), days: number(v.days, 1, 3650), enabled: boolean(v.enabled) }; }, 16, v => v.key), public_url: text(v.public_url, 4096) };
}
export function adminPaymentOrders(value: unknown, expectedID = ''): AdminPaymentOrder[] {
  const rows = unique(record(value).orders, item => { const v = record(item); return { ...paymentOrder(v, undefined, expectedID || undefined, true), channel: token(v.channel, true), provider: text(v.provider, 80), transaction_id: text(v.transaction_id, 4096), credit_entry: whole(v.credit_entry), member_entry: whole(v.member_entry), source_entry: whole(v.source_entry) }; }, 200, v => v.id);
  if (expectedID && rows.length !== 1) throw new Error('原订单未确认'); return rows;
}

export interface ExpectedPayment { kind: PurchaseKind | 'exchange'; amount: string; currency: string; credit?: string; target_currency?: string; plan?: string }
type PurchaseBody = { version: string; product: string; channel: string; accepted: true; request_id?: string };
type ExchangeBody = { version: string; rule: string; amount: string; accepted: true; request_id?: string };
export type BillingDraft = { title: string; expected: ExpectedPayment; review: string[][] } & ({ kind: 'purchase'; body: PurchaseBody } | { kind: 'exchange'; body: ExchangeBody });
export type BillingPending = BillingDraft & { owner: string };
export function billingPending(value: unknown, owner: string): BillingPending {
  const v = record(value), b = record(v.body), e = record(v.expected);
  if (v.owner !== owner || !requestID(b.request_id) || b.accepted !== true || (v.kind !== 'purchase' && v.kind !== 'exchange') || !['recharge', 'membership', 'exchange'].includes(string(e.kind))) throw new Error('原请求信息未确认');
  const common = { version: identifier(b.version), request_id: b.request_id, accepted: true as const }, expected: ExpectedPayment = { kind: e.kind as ExpectedPayment['kind'], amount: paymentAmount(e.amount), currency: text(e.currency, 32) };
  if (e.credit !== undefined) expected.credit = paymentAmount(e.credit);
  if (e.target_currency !== undefined) expected.target_currency = token(e.target_currency, true);
  if (e.plan !== undefined) expected.plan = token(e.plan, true);
  const review = list(v.review, row => { if (!Array.isArray(row) || row.length !== 2) throw new Error('原请求摘要未确认'); return row.map(v => text(v, 500)); }); if (review.length > 8) throw new Error('原请求摘要未确认');
  const base = { owner: identifier(v.owner), title: text(v.title, 240), expected, review };
  const allowed = v.kind === 'purchase' ? ['version', 'request_id', 'accepted', 'product', 'channel'] : ['version', 'request_id', 'accepted', 'rule', 'amount'];
  if (Object.keys(b).some(k => !allowed.includes(k))) throw new Error('原请求包含未知字段');
  if (v.kind === 'purchase') { if (expected.kind === 'exchange') throw new Error('原请求类型未确认'); exponent(expected.currency); return { ...base, kind: 'purchase', body: { ...common, product: token(b.product), channel: token(b.channel) } }; }
  if (expected.kind !== 'exchange' || paymentAmount(b.amount) !== expected.amount || !key(expected.currency)) throw new Error('原兑换请求未确认');
  return { ...base, kind: 'exchange', body: { ...common, rule: token(b.rule), amount: expected.amount } };
}
export function paymentReceipt(value: unknown, original: BillingPending): PaymentOrder {
  const order = paymentOrder(value, original.owner), expected = original.expected;
  for (const field of ['kind', 'amount', 'currency', 'credit', 'target_currency', 'plan'] as const) if (expected[field] !== undefined && order[field] !== expected[field]) throw new Error('返回订单与本次请求不一致，请保留原请求并联系管理员');
  // PublicOrder has no request_id, product or channel echo. Do not invent one.
  return order;
}
