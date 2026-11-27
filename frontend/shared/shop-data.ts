import { record, string, number, boolean, integer, identifier, key } from './ledger-data';
import { instant } from './site-time';
import { units as currencyUnits } from './currency-amount';
import { couponCatalog, couponSession, shopCurrencyKey } from './shop-coupon-data';
import { serviceConfig } from './shop-service-data';
import { paymentOrder } from './payment-data';
import { shopKinds, shopModes, shopStatuses } from './shop-types';
import type { ShopProduct, ShopCatalog, ShopRafflePlan, ShopRaffleQuota, ShopRaffleRound, ShopReviewSummary, ShopCatalogResponse, ShopAddress, ShopQuoteInput, ShopQuote, ShopPurchase, ShopInstallment, ShopOrder, ShopTicket, ShopOrderResponse, ShopPublicReview, ShopOwnReview, ShopReviews, ShopPending } from './shop-types';

const zero = '0001-01-01T00:00:00Z';
const text = (value: unknown, max = 16000) => { const result = string(value); if ([...result].length > max) throw new Error('商城文字数据过长'); return result; };
export function shopKey(value: unknown, empty = false): string {
  const result = string(value); if (!(empty && result === '') && !/^[a-z0-9][a-z0-9_-]{0,63}$/.test(result)) throw new Error('商城商品编号未确认'); return result;
}
function enumValue<T extends string>(value: unknown, choices: readonly T[]): T {
  if (typeof value !== 'string' || !choices.includes(value as T)) throw new Error('商城状态未确认'); return value as T;
}
const kind = (value: unknown) => enumValue(value, Object.keys(shopKinds) as (keyof typeof shopKinds)[]);
const mode = (value: unknown) => enumValue(value, Object.keys(shopModes) as (keyof typeof shopModes)[]);
function amount(value: unknown): string { if (integer(value) > 1000000000000n) throw new Error('商城金额超出范围'); return string(value); }
function stock(value: unknown): string { const n = integer(value, true); if (n < -1n || n > 1000000000n) throw new Error('商城库存未确认'); return string(value); }
export function shopTime(value: unknown, empty = true): string {
  const result = string(value); if (!(empty && result === zero) && !instant(result)) throw new Error('商城时间未确认'); return result;
}
function id(value: unknown, prefix: string, empty = false): string {
  const result = string(value); if (!(empty && result === '') && !new RegExp(`^${prefix}_[a-f0-9]{28}$`).test(result)) throw new Error('商城记录编号未确认'); return result;
}
export const shopOrderID = (value: unknown) => id(value, 'so');
function list<T>(value: unknown, parse: (item: unknown) => T, max = 10000): T[] {
  if (value === null) return [];
  if (!Array.isArray(value) || value.length > max) throw new Error('商城列表未确认'); return value.map(parse);
}
function unique<T>(value: unknown, parse: (item: unknown) => T, identity: (item: T) => string | number, max = 10000): T[] {
  const result = list(value, parse, max); if (new Set(result.map(identity)).size !== result.length) throw new Error('商城列表有重复记录'); return result;
}
function optionalList<T>(value: unknown, parse: (item: unknown) => T, max = 10000): T[] { return value === undefined ? [] : list(value, parse, max); }
function map<T>(value: unknown, parse: (item: unknown) => T): Record<string, T> {
  if (value === null) return {};
  const entries = Object.entries(record(value)); if (entries.length > 10000) throw new Error('商城映射过大'); return Object.fromEntries(entries.map(([key, value]) => [shopKey(key), parse(value)]));
}
export function shopRafflePlan(value: unknown): ShopRafflePlan | null {
  if (value === null) return null;
  const v = record(value);
  const result: ShopRafflePlan = { mode: enumValue(v.mode, ['instant', 'scheduled']), draw_at: shopTime(v.draw_at), winners: number(v.winners, 1, 1000), win_bps: number(v.win_bps, 0, 10000), prize: text(v.prize, 1000), campaign: v.campaign === undefined ? '' : shopKey(v.campaign, true), ticket_limit: v.ticket_limit === undefined ? 0 : number(v.ticket_limit, 0, 1000000000), user_limit: v.user_limit === undefined ? 0 : number(v.user_limit, 0, 1000000000), order_limit: v.order_limit === undefined ? 0 : number(v.order_limit, 0, 99), tiers: optionalList(v.tiers, value => { const v = record(value); return { prize: text(v.prize, 500), ...(v.win_bps === undefined ? {} : { win_bps: number(v.win_bps, 0, 10000) }), ...(v.winners === undefined ? {} : { winners: number(v.winners, 0, 1000) }) }; }, 100) };
  if (result.mode === 'scheduled' && result.draw_at === zero) throw new Error('开奖时间未确认');
  return result;
}
export function shopProduct(value: unknown): ShopProduct {
  const v = record(value);
  const result: ShopProduct = {
    key: shopKey(v.key), title: text(v.title, 240), summary: text(v.summary, 4000), image: text(v.image, 4096), images: optionalList(v.images, value => text(value, 4096), 12), description: v.description === undefined ? '' : text(v.description, 262144), detail_images: optionalList(v.detail_images, value => text(value, 4096), 12), category: text(v.category, 160), kind: kind(v.kind), mode: mode(v.mode), enabled: boolean(v.enabled), currency: shopCurrencyKey(v.currency), precision: number(v.precision, 0, 6),
    price: amount(v.price), compare_at: amount(v.compare_at), stock: stock(v.stock), shipping_fee: amount(v.shipping_fee), deposit: amount(v.deposit), max_donation: amount(v.max_donation), release_at: shopTime(v.release_at), balance_due_at: shopTime(v.balance_due_at),
    variants: unique(v.variants, value => { const v = record(value); return { key: shopKey(v.key), title: text(v.title, 240), image: v.image === undefined ? '' : text(v.image, 4096), price: amount(v.price), stock: stock(v.stock) }; }, v => v.key, 1000),
    bundle: list(v.bundle, value => { const v = record(value); return { product: shopKey(v.product), quantity: number(v.quantity, 1, 99) }; }, 1000),
    group: v.group === null ? null : (() => { const p = record(v.group); return { size: number(p.size, 2, 1000), hours: number(p.hours, 1, 8760), price: amount(p.price) }; })(),
    installment: v.installment === null ? null : (() => { const p = record(v.installment); return { periods: unique(p.periods, v => number(v, 2, 36), v => v, 36), interval_days: number(p.interval_days, 1, 366), interest_bps: number(p.interest_bps, 0, 10000) }; })(),
    raffle: shopRafflePlan(v.raffle),
  };
  if (v.services !== undefined) result.services = serviceConfig(v.services);
  if (!result.title || result.currency === 'CNY' && result.precision !== 2 || result.mode === 'group' && !result.group || result.mode === 'installment' && !result.installment || result.kind === 'raffle' && !result.raffle) throw new Error('商品购买方案未确认');
  return result;
}
export function shopCatalog(value: unknown): ShopCatalog {
  const v = record(value);
  return { version: identifier(v.version), enabled: boolean(v.enabled), order_minutes: number(v.order_minutes, 1, 10080), terms: text(v.terms), refund_policy: text(v.refund_policy), products: unique(v.products, shopProduct, v => v.key), raffle_campaigns: optionalList(v.raffle_campaigns, value => { const v = record(value); return { key: shopKey(v.key), title: text(v.title, 240), ticket_limit: number(v.ticket_limit, 0, 1000000000), user_limit: number(v.user_limit, 0, 1000000000) }; }) };
}
export function raffleQuotas(value: unknown): Record<string, ShopRaffleQuota> {
  return map(value, value => { const v = record(value); return { product_left: number(v.product_left, -1), campaign_left: number(v.campaign_left, -1), user_product_left: number(v.user_product_left, -1), user_campaign_left: number(v.user_campaign_left, -1) }; });
}
function summary(value: unknown): ShopReviewSummary {
  const v = record(value), distribution = list(v.distribution, v => number(v), 5);
  if (distribution.length !== 5 || typeof v.average !== 'number' || !Number.isFinite(v.average) || v.average < 0 || v.average > 5) throw new Error('商品评价统计未确认');
  const count = number(v.count), positive = number(v.positive, 0, count);
  // Go counts ratings of four or five stars; this field is not a percentage.
  if (distribution.reduce((total, n) => total + n, 0) !== count || distribution.slice(3).reduce((total, n) => total + n, 0) !== positive) throw new Error('商品评价数量未确认');
  return { count, average: v.average, positive, distribution };
}
export function shopCatalogResponse(value: unknown): ShopCatalogResponse {
  const v = record(value), catalog = shopCatalog(v.catalog), coupons = couponCatalog(v);
  const result: ShopCatalogResponse = {
    catalog, ...coupons, payment_enabled: boolean(v.payment_enabled), server_time: shopTime(v.server_time, false),
    channels: unique(v.channels, value => { const v = record(value); if (!key(v.key)) throw new Error('付款渠道未确认'); return { key: v.key, name: text(v.name, 240), currency: shopCurrencyKey(v.currency), provider: text(v.provider, 80) }; }, v => v.key, 100),
    groups: unique(v.groups, value => { const v = record(value); return { id: id(v.id, 'sg'), product: shopKey(v.product), variant: shopKey(v.variant, true), size: number(v.size, 2), paid: number(v.paid), status: enumValue(v.status, ['forming', 'open']), expires_at: shopTime(v.expires_at, false) }; }, v => v.id),
    product_stats: map(v.product_stats, value => ({ ...summary(value), sold: number(record(value).sold) })), raffle_quotas: raffleQuotas(v.raffle_quotas),
    raffle_rounds: map(v.raffle_rounds, value => { const v = record(value), byPrize = v.by_prize === undefined || v.by_prize === null ? {} : Object.fromEntries(Object.entries(record(v.by_prize)).map(([key, value]) => [text(key, 500), number(value)])); return { entries: number(v.entries), winners: number(v.winners), drawn: boolean(v.drawn), by_prize: byPrize } satisfies ShopRaffleRound; }),
  };
  if (catalog.products.some(p => !coupons.currencies.some(c => c.key === p.currency && c.precision === p.precision))) throw new Error('商品结算货币未确认');
  return result;
}
export function shopSession(value: unknown, owner: string) { return { ...couponSession(value, owner), raffle_quotas: raffleQuotas(record(value).raffle_quotas) }; }
function address(value: unknown): ShopAddress { const v = record(value); return { name: text(v.name, 240), phone: text(v.phone, 80), region: text(v.region, 500), detail: text(v.detail, 2000), postal_code: text(v.postal_code, 80) }; }
function installment(value: unknown): ShopInstallment {
  const v = record(value), result = { index: number(v.index, 0, 35), title: text(v.title, 240), principal: amount(v.principal), interest: amount(v.interest), amount: amount(v.amount), due_at: shopTime(v.due_at, false), available_at: shopTime(v.available_at), paid_at: shopTime(v.paid_at), payment_ref: v.payment_ref === undefined ? '' : text(v.payment_ref, 500) };
  if (BigInt(result.principal) + BigInt(result.interest) !== BigInt(result.amount)) throw new Error('分期金额未确认'); return result;
}
export function shopQuote(value: unknown): ShopQuote {
  const v = record(value), result: ShopQuote = {
    coupon: text(v.coupon, 40), coupon_title: text(v.coupon_title, 240), discount: amount(v.discount), precision: number(v.precision, 0, 6), original_subtotal: amount(v.original_subtotal), version: identifier(v.version), product: shopKey(v.product), variant: shopKey(v.variant, true), quantity: number(v.quantity, 1, 99), kind: kind(v.kind), mode: mode(v.mode), currency: shopCurrencyKey(v.currency),
    unit_price: amount(v.unit_price), subtotal: amount(v.subtotal), shipping: amount(v.shipping), interest: amount(v.interest), total: amount(v.total), pay_now: amount(v.pay_now), requires_address: boolean(v.requires_address), schedule: unique(v.schedule, installment, v => v.index, 36), interest_description: text(v.interest_description),
  };
  if (!result.schedule.length || result.schedule.some((part, index) => part.index !== index) || result.currency === 'CNY' && result.precision !== 2
    || BigInt(result.original_subtotal) !== BigInt(result.unit_price) * BigInt(result.quantity) || BigInt(result.original_subtotal) - BigInt(result.discount) !== BigInt(result.subtotal)
    || BigInt(result.subtotal) + BigInt(result.shipping) + BigInt(result.interest) !== BigInt(result.total) || result.schedule.reduce((sum, part) => sum + BigInt(part.amount), 0n) !== BigInt(result.total)
    || result.schedule.reduce((sum, part) => sum + BigInt(part.interest), 0n) !== BigInt(result.interest) || result.schedule[0]?.amount !== result.pay_now || BigInt(result.total) < 1n) throw new Error('订单报价或付款计划未确认');
  return result;
}
export function shopPurchase(value: unknown): ShopPurchase {
  const v = record(value), request = text(v.request_id, 128);
  if (!request.trim() || /[<>\r\n\u0000]/.test(request) || v.accepted !== true) throw new Error('原下单请求无效，请先核对已有订单');
  return { coupon: text(v.coupon, 40), product: shopKey(v.product), variant: shopKey(v.variant, true), quantity: number(v.quantity, 1, 99), periods: number(v.periods, 0, 36), donation_amount: amount(v.donation_amount), group_id: id(v.group_id, 'sg', true), mode: mode(v.mode), version: identifier(v.version), request_id: request, accepted: true, address: address(v.address) };
}
export function matchQuote(quote: ShopQuote, input: ShopQuoteInput, product?: ShopProduct): void {
  if (quote.product !== input.product || quote.variant !== input.variant || quote.quantity !== input.quantity || quote.mode !== input.mode || quote.coupon !== input.coupon
    || quote.mode === 'installment' && quote.schedule.length !== input.periods || quote.kind === 'donation' && quote.unit_price !== input.donation_amount
    || product && (quote.kind !== product.kind || quote.currency !== product.currency || quote.precision !== product.precision)) throw new Error('报价未匹配所选商品，请重新核对');
}
function ticket(value: unknown, order: string): ShopTicket {
  const v = record(value); if (v.order_id !== order) throw new Error('抽奖码未匹配订单');
  return { id: id(v.id, 'st'), order_id: order, round: text(v.round, 240), status: enumValue(v.status, ['pending', 'won', 'lost', 'cancelled']), prize: text(v.prize, 1000), draw_at: shopTime(v.draw_at), drawn_at: shopTime(v.drawn_at), winners: number(v.winners), delivery: v.delivery === undefined ? '' : text(v.delivery, 262144), delivered_at: shopTime(v.delivered_at) };
}
export function shopOrder(value: unknown, owner: string, expectedID?: string): ShopOrder {
  const v = record(value), orderID = shopOrderID(v.id);
  if (v.user_id !== owner || expectedID !== undefined && orderID !== expectedID) throw new Error('订单回执未匹配当前账号或订单');
  const result: ShopOrder = { ...shopQuote(v), id: orderID, user_id: identifier(v.user_id), title: text(v.title, 240), status: enumValue(v.status, Object.keys(shopStatuses) as (keyof typeof shopStatuses)[]), paid_amount: amount(v.paid_amount), address: address(v.address), group_id: id(v.group_id, 'sg', true),
    deliveries: list(v.deliveries, value => { const v = record(value); return { product: shopKey(v.product), kind: kind(v.kind), title: text(v.title, 240), content: text(v.content, 262144), quantity: number(v.quantity, 1, 10000) }; }), tickets: unique(v.tickets, value => ticket(value, orderID), v => v.id),
    carrier: text(v.carrier, 240), tracking: text(v.tracking, 240), refund_reference: text(v.refund_reference, 1000), refund_note: text(v.refund_note, 4000), refund_review: v.refund_review === undefined ? '' : text(v.refund_review, 4000),
    created_at: shopTime(v.created_at, false), updated_at: shopTime(v.updated_at, false), expires_at: shopTime(v.expires_at, false), completed_at: shopTime(v.completed_at), shipped_at: shopTime(v.shipped_at), terms: text(v.terms), refund_policy: text(v.refund_policy), catalog_version: identifier(v.catalog_version), released: boolean(v.released) };
  if (result.version !== result.catalog_version || BigInt(result.paid_amount) > BigInt(result.total) || result.schedule.reduce((sum, part) => sum + (part.paid_at === zero ? 0n : BigInt(part.amount)), 0n) !== BigInt(result.paid_amount)) throw new Error('订单版本或付款记录未确认');
  return result;
}
export function createdShopOrder(value: unknown, owner: string, draft: ShopPurchase, expected?: ShopQuote): ShopOrder {
  const order = shopOrder(record(value).order, owner); matchQuote(order, draft);
  if (order.version !== draft.version || order.catalog_version !== draft.version || draft.group_id && order.group_id !== draft.group_id
    || order.requires_address && Object.keys(draft.address).some(key => draft.address[key as keyof ShopAddress] !== order.address[key as keyof ShopAddress])) throw new Error('订单回执未匹配原请求，恢复记录已保留');
  if (expected) {
    matchQuote(expected, draft);
    if (expected.version !== draft.version) throw new Error('确认报价版本与原请求不同');
    for (const key of ['product', 'variant', 'quantity', 'kind', 'mode', 'currency', 'precision', 'unit_price', 'original_subtotal', 'subtotal', 'discount', 'shipping', 'interest', 'total', 'pay_now', 'requires_address', 'coupon', 'coupon_title'] as const) if (order[key] !== expected[key]) throw new Error('订单条款与确认报价不同，恢复记录已保留');
    if (order.schedule.length !== expected.schedule.length || order.schedule.some((part, index) => { const previous = expected.schedule[index]; return !previous || part.index !== previous.index || part.title !== previous.title || part.principal !== previous.principal || part.interest !== previous.interest || part.amount !== previous.amount || part.available_at !== previous.available_at || order.mode === 'deposit' && index > 0 && part.due_at !== previous.due_at; })) throw new Error('订单付款计划与确认报价不同，恢复记录已保留');
    // DueAt for immediate/installment payments is recomputed at creation time by Go.
  }
  return order;
}
export function shopPayment(value: unknown, order: ShopOrder, index: number) {
  const result = paymentOrder(value, order.user_id, undefined, true), part = order.schedule[index];
  if (!part || result.kind !== 'shop' || result.currency !== order.currency || currencyUnits(result.amount, order.precision) !== BigInt(part.amount)) throw new Error('支付回执未匹配当前订单的付款金额');
  return result;
}
export function shopOrderUpdate(value: unknown, previous: ShopOrder): ShopOrder {
  const next = shopOrder(value, previous.user_id, previous.id);
  for (const key of ['product', 'variant', 'quantity', 'kind', 'mode', 'currency', 'precision', 'unit_price', 'original_subtotal', 'subtotal', 'discount', 'shipping', 'interest', 'total', 'pay_now', 'requires_address', 'coupon', 'coupon_title', 'version', 'catalog_version', 'title', 'group_id', 'created_at', 'expires_at', 'terms', 'refund_policy'] as const) {
    if (next[key] !== previous[key]) throw new Error('订单条款已变化，请重新读取原订单核对');
  }
  if (Object.keys(previous.address).some(key => previous.address[key as keyof ShopAddress] !== next.address[key as keyof ShopAddress])
    || next.schedule.length !== previous.schedule.length || next.schedule.some((part, index) => { const old = previous.schedule[index]; return !old || (['index', 'title', 'principal', 'interest', 'amount', 'due_at', 'available_at'] as const).some(key => part[key] !== old[key]); })) throw new Error('订单付款计划未匹配原订单');
  return next;
}
export function shopOrderResponse(value: unknown, owner: string, expectedID: string): ShopOrderResponse {
  const v = record(value), order = shopOrder(v.order, owner, expectedID);
  // A cancelled/expired checkout can be followed by another attempt for the same installment.
  return { order, payments: unique(v.payments, value => { const index = number(record(value).index, 0, order.schedule.length - 1); return { ...shopPayment(value, order, index), index }; }, v => v.id) };
}
export function shopOrderList(value: unknown, owner: string) { const v = record(value); if (!text(v.csrf_token, 4096)) throw new Error('订单会话未确认'); return unique(v.orders, value => shopOrder(value, owner), v => v.id); }
export function shopPending(value: unknown, owner: string): ShopPending {
  const v = record(value); if (v.owner !== owner) throw new Error('原请求所属账号不匹配');
  const draft = shopPurchase(v.draft), quote = v.quote === undefined ? undefined : shopQuote(v.quote);
  if (quote) { matchQuote(quote, draft); if (quote.version !== draft.version) throw new Error('原请求报价版本不匹配'); }
  return { owner, draft, ...(quote ? { quote } : {}) };
}
function publicReview(value: unknown): ShopPublicReview {
  const v = record(value); return { id: id(v.id, 'sr'), author: text(v.author, 240), variant: text(v.variant, 240), rating: number(v.rating, 1, 5), content: text(v.content, 4000), anonymous: boolean(v.anonymous), created_at: shopTime(v.created_at, false), reply: text(v.reply, 4000), replied_at: shopTime(v.replied_at) };
}
export function shopReviews(value: unknown, page: number): ShopReviews {
  const v = record(value), result = { reviews: unique(v.reviews, publicReview, v => v.id, 100), summary: summary(v.summary), total: number(v.total), page: number(v.page, 1, 10000), page_size: number(v.page_size, 1, 100) };
  if (result.page !== page || result.reviews.length > result.page_size) throw new Error('评价分页未匹配请求'); return result;
}
export function ownShopReview(value: unknown, order: Pick<ShopOrder, 'id' | 'user_id' | 'product'>): ShopOwnReview {
  const v = record(value); if (v.order_id !== order.id || v.user_id !== order.user_id || v.product !== order.product) throw new Error('评价回执未匹配当前订单');
  integer(v.moderator_id);
  return { ...publicReview(v), order_id: order.id, user_id: order.user_id, product: order.product, title: text(v.title, 240), hidden: boolean(v.hidden), hidden_reason: text(v.hidden_reason, 1000), updated_at: shopTime(v.updated_at, false), version: identifier(v.version), moderator_id: string(v.moderator_id) };
}
export function shopOrderReview(value: unknown, order: ShopOrder) {
  const v = record(value); return { eligible: boolean(v.eligible), note: text(v.note), review: v.review === null ? null : ownShopReview(v.review, order) };
}
