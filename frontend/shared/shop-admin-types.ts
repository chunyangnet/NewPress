import type { ShopCatalog, ShopProduct, ShopOrder, ShopOwnReview } from './shop-types';
import type { ShopCurrency, ShopCoupon } from './shop-coupon-data';
import type { ServiceConfig, ServiceLibrary } from './shop-service-types';
import type { PublicChannel } from './payment-data';

/** Unknown future catalog fields survive editing and the three-way merge. */
export interface ShopAdminProduct extends Omit<ShopProduct, 'services'> {
  description_source: string; description_author_id: string; delivery: string; codes: string[]; services: ServiceConfig | null;
  [key: string]: unknown;
}
export interface ShopAdminCatalog extends Omit<ShopCatalog, 'products'> {
  products: ShopAdminProduct[];
  [key: string]: unknown;
}
export interface ShopAdminConfig {
  catalog: ShopAdminCatalog; currencies: ShopCurrency[]; channels: PublicChannel[];
  payment_enabled: boolean; csrf_token: string; service_library: ServiceLibrary;
}
export interface ShopAdminCoupons { version: number; coupons: ShopCoupon[] }
export interface ShopAdminReviews { reviews: ShopOwnReview[]; total: number; page: number; page_size: number }
export type ShopAdminOrder = ShopOrder & { refund_previous_status: string };
export interface ShopReviewEdit { version: string; reply: string; hidden: boolean; reason: string }
export type ShopOrderAction =
  | { kind: 'ship'; carrier: string; tracking: string }
  | { kind: 'deliver'; ticket: string; content: string }
  | { kind: 'review-refund'; approved: boolean; note: string }
  | { kind: 'refund'; reference?: string; note?: string };
