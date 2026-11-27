import type { ShopMode, ShopProduct, ShopRaffleQuota, ShopRaffleRound, ShopRafflePlan, ShopRaffleCampaign } from './shop-types';
import type { ShopCurrency } from './shop-coupon-data';
import { units as currencyUnits } from './currency-amount';
import { instant } from './site-time';

export function amountText(value: string | bigint, precision: number): string {
  if (!Number.isInteger(precision) || precision < 0 || precision > 6) throw new Error('金额精度未确认');
  const n = BigInt(value), scale = 10n ** BigInt(precision);
  if (n < 0n) throw new Error('金额不能为负数');
  return precision ? `${n / scale}.${String(n % scale).padStart(precision, '0')}` : String(n);
}
export function money(value: string | bigint, currency = 'CNY', precision = 2, item?: Pick<ShopCurrency, 'name' | 'symbol'>): string {
  const symbol = item?.symbol || (currency === 'CNY' ? '¥' : `${item?.name || currency} `);
  return symbol + amountText(value, precision);
}
/** Donation input keeps its existing UI limit; payment receipts use currencyUnits directly. */
export function units(value: string, precision = 2): string {
  const result = currencyUnits(value, precision);
  if (result > 100000000000n) throw new Error('金额超出允许范围。');
  return String(result);
}
export const isDate = (value: unknown): value is string => typeof value === 'string' && !value.startsWith('0001-') && instant(value) !== null;
export const optionsFor = (p: Pick<ShopProduct, 'mode'>): ShopMode[] => ['group', 'installment'].includes(p.mode) ? [p.mode, 'direct'] : [p.mode];
export function safeURL(value: string, base = typeof location === 'undefined' ? 'https://example.test' : location.href): string {
  if (!value || /[\u0000-\u0020\u007f\\]/.test(value)) return '';
  try {
    const url = new URL(value, base);
    return !url.username && !url.password && (url.protocol === 'https:' || url.protocol === 'http:' && url.origin === new URL(base).origin) ? url.href : '';
  } catch { return ''; }
}
type RaffleView = { kind?: string; stock?: string | number; raffle?: Partial<ShopRafflePlan> | null };
export function raffleDeadline(product?: RaffleView | null): number {
  return product?.kind === 'raffle' && product.raffle?.mode === 'scheduled' ? Date.parse(product.raffle.draw_at || '') : Infinity;
}
export function rafflePhase(deadline: number, now: number, counts: Partial<ShopRaffleRound> | undefined, sampledAt: number): string {
  if (counts?.drawn) return '已开奖';
  if (now >= deadline) return counts?.entries === 0 && sampledAt >= deadline ? '本轮无人参与' : '已截止，等待开奖';
  const minutes = Math.ceil((deadline - now) / 60000);
  if (minutes < 60) return `距截止 ${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  if (hours >= 24) return `距截止 ${Math.floor(hours / 24)} 天 ${hours % 24} 小时`;
  return `距截止 ${hours} 小时 ${minutes % 60} 分钟`;
}
export function rafflePurchaseCapacity(product: RaffleView, publicQuota?: Partial<ShopRaffleQuota>, personalQuota?: Partial<ShopRaffleQuota>, campaign?: Partial<ShopRaffleCampaign>) {
  const capLeft = (value?: number) => Number(value) > 0 ? Number(value) : -1;
  const limits: [string, number][] = [
    ['库存', Number(product.stock ?? -1)],
    ['本商品券码', personalQuota?.product_left ?? publicQuota?.product_left ?? capLeft(product.raffle?.ticket_limit)],
    ['活动券码', personalQuota?.campaign_left ?? publicQuota?.campaign_left ?? capLeft(campaign?.ticket_limit)],
    ['本商品每人限购', personalQuota?.user_product_left ?? capLeft(product.raffle?.user_limit)],
    ['活动每人限购', personalQuota?.user_campaign_left ?? capLeft(campaign?.user_limit)],
  ];
  if (product.raffle?.order_limit) limits.push(['每单限购', product.raffle.order_limit]);
  const finite = limits.filter(([, value]) => Number.isFinite(value) && value >= 0).map(([, value]) => value);
  const soldOut = limits.slice(0, 3).some(([, value]) => value === 0);
  return { max: Math.min(99, ...finite), soldOut, personalLimit: !soldOut && limits.slice(3, 5).some(([, value]) => value === 0), limits };
}
export function raffleCountdown(deadline: number, now: number) {
  let seconds = Number.isFinite(deadline - now) ? Math.max(0, Math.ceil((deadline - now) / 1000)) : 0;
  const days = Math.floor(seconds / 86400); seconds %= 86400;
  const hours = Math.floor(seconds / 3600); seconds %= 3600;
  return { days, hours, minutes: Math.floor(seconds / 60), seconds: seconds % 60 };
}
