import { record, string, number, boolean, integer, identifier, key } from './ledger-data';
import { shopCatalog, shopProduct, shopOrder, shopOrderUpdate, shopOrderID, shopKey, ownShopReview } from './shop-data';
import { coupon, couponCatalog, shopCurrencyKey } from './shop-coupon-data';
import { serviceConfig, serviceLibrary } from './shop-service-data';
import { equal } from './shop-admin-helpers';
import type { ShopAdminCatalog, ShopAdminProduct, ShopAdminConfig, ShopAdminCoupons, ShopAdminOrder, ShopAdminReviews, ShopOrderAction, ShopReviewEdit } from './shop-admin-types';
import { shopStatuses } from './shop-types';
import type { ShopOwnReview } from './shop-types';
import type { ShopCoupon } from './shop-coupon-data';

const zero = '0001-01-01T00:00:00Z';
const text = (v: unknown, limit: number) => { const result = string(v); if ([...result].length > limit) throw new Error('商城后台文字过长'); return result; };
const optionalText = (v: unknown, limit: number) => v === undefined ? '' : text(v, limit);
function list<T>(v: unknown, parse: (item: unknown) => T, limit: number): T[] {
  if (v === null) return [];
  if (!Array.isArray(v) || v.length > limit) throw new Error('商城后台列表未确认');
  return v.map(parse);
}
function unique<T>(v: unknown, parse: (item: unknown) => T, identity: (item: T) => string, limit: number): T[] {
  const result = list(v, parse, limit); if (new Set(result.map(identity)).size !== result.length) throw new Error('商城后台记录重复'); return result;
}
export function adminProduct(value: unknown): ShopAdminProduct {
  const v = record(value), product = shopProduct(v);
  const description = optionalText(v.description_source, 262144), author = v.description_author_id === undefined ? '0' : string(v.description_author_id);
  integer(author);
  if (new TextEncoder().encode(description).length > 262144 || description.includes('\0')) throw new Error('商品详细介绍未确认');
  return { ...v, ...product, description_source: description, description_author_id: author,
    delivery: optionalText(v.delivery, 16000), codes: v.codes === undefined ? [] : unique(v.codes, v => text(v, 2000), v => v, 10000),
    services: v.services === undefined || v.services === null ? null : serviceConfig(v.services) };
}
export function adminCatalog(value: unknown): ShopAdminCatalog {
  const v = record(value), basic = shopCatalog(v);
  return { ...v, ...basic, products: unique(v.products, adminProduct, p => p.key, 1000) };
}
export function adminConfig(value: unknown, readOnly = false): ShopAdminConfig {
  const v = record(value), csrf = text(v.csrf_token, 4096);
  if (!csrf && !readOnly) throw new Error('商城后台登录状态未确认');
  return { catalog: adminCatalog(v.catalog), currencies: couponCatalog({ coupons: [], currencies: v.currencies }).currencies,
    channels: unique(v.channels, value => { const v = record(value); if (!key(v.key)) throw new Error('支付渠道未确认'); return { key: v.key, name: text(v.name, 240), currency: shopCurrencyKey(v.currency), provider: text(v.provider, 80) }; }, c => c.key, 100),
    csrf_token: csrf, payment_enabled: boolean(v.payment_enabled), service_library: serviceLibrary(v.service_library) };
}
/** SaveCatalog returns the exact saved catalog except server-owned description authors and version. */
export function savedAdminCatalog(value: unknown, expected: ShopAdminCatalog): ShopAdminCatalog {
  const actual = adminCatalog(record(value).catalog), normalized = adminCatalog(expected);
  if (BigInt(actual.version) !== BigInt(expected.version) + 1n) throw new Error('商品保存版本未确认，请先重新读取核对');
  normalized.version = actual.version;
  for (const p of normalized.products) {
    const saved = actual.products.find(a => a.key === p.key);
    if (!saved || p.description_author_id !== '0' && p.description_author_id !== saved.description_author_id) throw new Error('商品保存回执未匹配');
    p.description_author_id = saved.description_author_id;
  }
  if (!equal(actual, normalized)) throw new Error('商品保存回执未匹配，请先核对已保存内容');
  return actual;
}
export function adminCoupons(value: unknown): ShopAdminCoupons {
  const v = record(value); return { version: number(v.version), coupons: unique(v.coupons, coupon, c => c.code, 500) };
}
export function savedAdminCoupons(value: unknown, previous: number, desired: ShopCoupon[]): ShopAdminCoupons {
  const result = adminCoupons(value);
  const normalized = desired.map(c => { const saved = result.coupons.find(s => s.code === c.code); return { ...c, created_at: c.created_at === zero ? saved?.created_at : c.created_at }; });
  if (result.version !== previous + 1 || !equal(result.coupons, normalized)) throw new Error('优惠券保存回执未匹配，请先重新读取核对');
  return result;
}
export function adminOrder(value: unknown): ShopAdminOrder {
  const v = record(value), order = shopOrder(v, identifier(v.user_id));
  const previous = optionalText(v.refund_previous_status, 80);
  if (previous && !Object.hasOwn(shopStatuses, previous)) throw new Error('退款前订单状态未确认');
  return { ...order, refund_previous_status: previous };
}
export function adminOrders(value: unknown): ShopAdminOrder[] { return unique(record(value).orders, adminOrder, order => order.id, 100000); }
export function adminOrderReceipt(value: unknown, previous: ShopAdminOrder, action: ShopOrderAction, recovering = false): ShopAdminOrder {
  const raw = record(value).order, order = adminOrder(raw); shopOrderUpdate(order, previous);
  if (order.paid_amount !== previous.paid_amount) throw new Error('订单付款记录变化，请重新核对');
  let matches = false;
  switch (action.kind) {
    case 'ship': matches = ['shipped', 'completed'].includes(order.status) && order.carrier === action.carrier && order.tracking === action.tracking && order.shipped_at !== zero; break;
    case 'deliver': {
      const ticket = order.tickets.find(t => t.id === action.ticket), original = previous.tickets.find(t => t.id === action.ticket);
      matches = order.status === 'completed' && !!ticket && !!original && ticket.status === 'won' && ticket.delivery === action.content && ticket.delivered_at !== zero
        && equal({ ...ticket, delivery: original.delivery, delivered_at: original.delivered_at }, original);
      break;
    }
    case 'refund': matches = order.status === 'refunded' && (previous.currency.startsWith('wallet:') ? order.refund_reference === 'wallet-refund-' + order.id : order.refund_reference === action.reference && order.refund_note === action.note); break;
    case 'review-refund': {
      const restored = previous.refund_previous_status;
      const advanced: Record<string, string[]> = {
        partially_paid: ['partially_paid', 'refund_required', 'refunded'],
        awaiting_group: ['awaiting_group', 'awaiting_release', 'awaiting_shipment', 'completed', 'refund_required', 'refunded'],
        awaiting_release: ['awaiting_release', 'awaiting_shipment', 'completed'],
        awaiting_draw: ['awaiting_draw', 'completed'],
        awaiting_shipment: ['awaiting_shipment'], shipped: ['shipped', 'completed'], completed: ['completed'],
      };
      // Rejection may immediately advance an expired group into a refund.
      // A later read cannot distinguish that from another approval with the
      // same note, so it must not clear a pending rejection in that state.
      matches = previous.status === 'refund_requested' && order.refund_previous_status === '' && order.refund_review === action.note
        && (action.approved ? ['refund_required', 'refunded'].includes(order.status)
          : !!advanced[restored]?.includes(order.status) && !(recovering && ['refund_required', 'refunded'].includes(order.status)));
      break;
    }
  }
  if (!matches) throw new Error('操作回执未匹配，请先重新读取订单核对结果');
  return order;
}
export function adminReview(value: unknown): ShopOwnReview {
  const v = record(value);
  // This parser only uses the order identity; all private review fields are checked by the shared contract.
  const identity = { id: shopOrderID(v.order_id), user_id: identifier(v.user_id), product: shopKey(v.product) };
  return ownShopReview(v, identity);
}
export function adminReviews(value: unknown, page: number): ShopAdminReviews {
  const v = record(value), result = { reviews: unique(v.reviews, adminReview, r => r.id, 100), total: number(v.total), page: number(v.page, 1), page_size: number(v.page_size, 1, 100) };
  if (result.page !== page || result.reviews.length > result.page_size || result.reviews.length > result.total) throw new Error('评价分页未匹配请求');
  return result;
}
export function savedAdminReview(value: unknown, previous: ShopOwnReview, input: ShopReviewEdit, moderator?: string): ShopOwnReview {
  const result = adminReview(record(value).review);
  if ((['id', 'order_id', 'user_id', 'product', 'title', 'variant', 'author', 'rating', 'content', 'anonymous', 'created_at'] as const).some(key => result[key] !== previous[key])
    || BigInt(result.version) !== BigInt(input.version) + 1n || result.reply !== input.reply || result.hidden !== input.hidden || result.hidden_reason !== (input.hidden ? input.reason : '')
    || moderator !== undefined && result.moderator_id !== moderator || result.moderator_id === '0') throw new Error('评价管理回执未匹配，请先核对已有回复');
  return result;
}
