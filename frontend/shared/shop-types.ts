import type { ServiceConfig, ShopKind } from './shop-service-types';
import type { CouponOption, ShopCurrency } from './shop-coupon-data';
import type { PaymentOrder, PublicChannel } from './payment-data';

export const shopKinds = { virtual: '虚拟商品', physical: '实物商品', cdk: '卡密 / CDK', donation: '捐赠', raffle: '抽奖码', bundle: '组合套装' };
export const shopModes = { direct: '直接购买', presale: '全款预售', deposit: '定金 / 尾款', installment: '分期付款', group: '拼团购买' };
export const shopStatuses = { pending: '待付款', partially_paid: '待付尾款 / 分期', awaiting_group: '等待成团', awaiting_release: '等待预售交付', awaiting_draw: '等待开奖', awaiting_shipment: '等待发货', shipped: '已发货', completed: '已完成', cancelled: '已取消', expired: '已过期', refund_requested: '待审核退款', refund_required: '退款处理中', refunded: '已登记退款' };
export type ShopMode = keyof typeof shopModes;
export type ShopStatus = keyof typeof shopStatuses;
export interface ShopVariant { key: string; title: string; image: string; price: string; stock: string }
export interface ShopBundleItem { product: string; quantity: number }
export interface ShopInstallmentPlan { periods: number[]; interval_days: number; interest_bps: number }
export interface ShopGroupPlan { size: number; price: string; hours: number }
export interface ShopRaffleTier { prize: string; win_bps?: number; winners?: number }
export interface ShopRaffleCampaign { key: string; title: string; ticket_limit: number; user_limit: number }
export interface ShopRafflePlan {
  mode: 'instant' | 'scheduled'; draw_at: string; winners: number; win_bps: number; prize: string;
  tiers: ShopRaffleTier[]; campaign: string; ticket_limit: number; user_limit: number; order_limit: number;
}
export interface ShopProduct {
  key: string; title: string; summary: string; image: string; images: string[]; description: string; detail_images: string[]; services?: ServiceConfig;
  category: string; kind: ShopKind; mode: ShopMode; enabled: boolean; currency: string; precision: number;
  price: string; compare_at: string; stock: string; shipping_fee: string; variants: ShopVariant[]; bundle: ShopBundleItem[];
  release_at: string; deposit: string; balance_due_at: string; max_donation: string;
  installment: ShopInstallmentPlan | null; group: ShopGroupPlan | null; raffle: ShopRafflePlan | null;
}
export interface ShopCatalog { version: string; enabled: boolean; order_minutes: number; terms: string; refund_policy: string; products: ShopProduct[]; raffle_campaigns: ShopRaffleCampaign[] }
export interface ShopAddress { name: string; phone: string; region: string; detail: string; postal_code: string }
export interface ShopQuoteInput { coupon: string; product: string; variant: string; quantity: number; periods: number; donation_amount: string; group_id: string; mode: ShopMode }
export interface ShopPurchase extends ShopQuoteInput { version: string; request_id: string; accepted: true; address: ShopAddress }
export interface ShopInstallment { index: number; title: string; principal: string; interest: string; amount: string; due_at: string; available_at: string; paid_at: string; payment_ref: string }
export interface ShopQuote {
  coupon: string; coupon_title: string; discount: string; precision: number; original_subtotal: string; version: string; product: string; variant: string;
  quantity: number; kind: ShopKind; mode: ShopMode; currency: string; unit_price: string; subtotal: string; shipping: string; interest: string; total: string; pay_now: string;
  requires_address: boolean; schedule: ShopInstallment[]; interest_description: string;
}
export interface ShopDelivery { product: string; kind: ShopKind; title: string; content: string; quantity: number }
export interface ShopTicket { id: string; order_id: string; round: string; status: 'pending' | 'won' | 'lost' | 'cancelled'; prize: string; draw_at: string; drawn_at: string; winners: number; delivery: string; delivered_at: string }
export interface ShopOrder extends ShopQuote {
  id: string; user_id: string; title: string; status: ShopStatus; paid_amount: string; address: ShopAddress; group_id: string;
  deliveries: ShopDelivery[]; tickets: ShopTicket[]; carrier: string; tracking: string; refund_reference: string; refund_note: string; refund_review: string;
  created_at: string; updated_at: string; expires_at: string; completed_at: string; shipped_at: string; terms: string; refund_policy: string; catalog_version: string; released: boolean;
}
export interface ShopGroup { id: string; product: string; variant: string; size: number; paid: number; status: string; expires_at: string }
export interface ShopRaffleQuota { product_left: number; campaign_left: number; user_product_left: number; user_campaign_left: number }
export interface ShopRaffleRound { entries: number; winners: number; drawn: boolean; by_prize: Record<string, number> }
export interface ShopReviewSummary { count: number; average: number; positive: number; distribution: number[] }
export interface ShopProductStats extends ShopReviewSummary { sold: number }
export interface ShopPublicReview { id: string; author: string; variant: string; rating: number; content: string; anonymous: boolean; created_at: string; reply: string; replied_at: string }
export interface ShopOwnReview extends ShopPublicReview { order_id: string; user_id: string; product: string; title: string; hidden: boolean; hidden_reason: string; updated_at: string; version: string; moderator_id: string }
export interface ShopReviews { reviews: ShopPublicReview[]; summary: ShopReviewSummary; total: number; page: number; page_size: number }
export interface ShopCatalogResponse {
  catalog: ShopCatalog; groups: ShopGroup[]; channels: PublicChannel[]; payment_enabled: boolean; currencies: ShopCurrency[]; coupons: CouponOption[];
  product_stats: Record<string, ShopProductStats>; raffle_rounds: Record<string, ShopRaffleRound>; raffle_quotas: Record<string, ShopRaffleQuota>; server_time: string;
}
export interface ShopOrderResponse { order: ShopOrder; payments: (PaymentOrder & { index: number })[] }
export interface ShopPending { owner: string; draft: ShopPurchase; quote?: ShopQuote }
