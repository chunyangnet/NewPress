import type { ServiceContext, ServiceCurrency, ServiceModule, ServiceProduct, ShopKind } from './shop-service-types';

export function safeLink(value: unknown): string {
  if (typeof value !== 'string' || !value) return '';
  if (/^#shop-product-(description|reviews|specifications|rules)$/.test(value)) return value;
  if (value.length > 2000 || /[\\\s<>\u0000]/.test(value) || value.startsWith('//') || !value.startsWith('/') && !value.startsWith('https://')) return '';
  try {
    const url = new URL(value, 'https://shop.invalid'), path = decodeURIComponent(url.pathname);
    if (url.username || url.password || /[\\\r\n\t\u0000]/.test(path) || value.startsWith('/') && (url.origin !== 'https://shop.invalid' || path.startsWith('//'))) return '';
    return value;
  } catch { return ''; }
}

function date(value: string | undefined): string {
  if (!value || value.startsWith('0001-') || !Number.isFinite(Date.parse(value))) return '以商品说明为准';
  const timeZone = typeof document === 'undefined' ? 'Asia/Shanghai' : document.documentElement.dataset.siteTimezone || 'Asia/Shanghai';
  try { return new Date(value).toLocaleString('zh-CN', { timeZone, hour12: false }); }
  catch { return new Date(value).toLocaleString('zh-CN', { timeZone: 'UTC', hour12: false }); }
}

export function context(product: ServiceProduct, currencies: ServiceCurrency[] = []): Record<string, string | number> {
  const currency = currencies.find(item => item.key === product.currency);
  const currencyName = currency?.name || (product.currency === 'CNY' ? '人民币' : product.currency || '所选货币');
  const requested = product.currency === 'CNY' ? 2 : product.precision ?? currency?.precision ?? 2;
  // Product drafts can contain incomplete form values. Never exponentiate an unchecked precision.
  const validPrecision = Number.isInteger(requested) && requested >= 0 && requested <= 6;
  const precision = validPrecision ? requested : 2;
  const rawFee = product.shipping_fee || '0';
  const fee = /^(0|[1-9]\d{0,12})$/.test(rawFee) && BigInt(rawFee) <= 1000000000000n ? BigInt(rawFee) : null;
  const scale = 10n ** BigInt(precision);
  const display = (value: number | undefined, fallback: string) => value !== undefined && Number.isFinite(value) ? value : fallback;
  return {
    '结算货币': currencyName,
    '运费': fee === null || !validPrecision ? '以商品说明为准' : `${currency?.symbol || (product.currency === 'CNY' ? '¥' : currencyName + ' ')}${fee / scale}${precision ? '.' + String(fee % scale).padStart(precision, '0') : ''}`,
    '成团人数': display(product.group?.size, '指定'), '拼团时限': display(product.group?.hours, '约定'),
    '发售时间': date(product.release_at), '尾款截止': date(product.balance_due_at),
    '分期期数': product.installment?.periods?.join(' / ') || '指定', '付款间隔': display(product.installment?.interval_days, '约定'),
    '每期费率': product.installment && Number.isFinite(product.installment.interest_bps) ? `${product.installment.interest_bps / 100}%` : '以付款计划为准',
    '开奖时间': date(product.raffle?.draw_at), '中奖概率': product.raffle && Number.isFinite(product.raffle.win_bps) ? `${product.raffle.win_bps / 100}%` : '以开奖规则为准',
    '中奖名额': display(product.raffle?.winners, '指定'),
  };
}

export function defaults(product: ServiceProduct, products: ServiceProduct[] = []): ServiceModule[] {
  const physical = product.kind === 'physical' || product.kind === 'bundle' && product.bundle?.some(item => products.find(p => p.key === item.product)?.kind === 'physical');
  const deliveries: Record<ShopKind, [string, string, string]> = {
    virtual: ['file-down', '订单交付', '付清后查看专属内容'], physical: ['shopping-bag', '实物配送', '运费 {运费}'],
    cdk: ['key-round', '自动发卡', '付清后在订单中查看'], donation: ['heart', '捐赠记录', '付款后可查询记录'],
    raffle: ['tags', '抽奖码', product.raffle?.mode === 'instant' ? '付款后即时开奖' : '按指定时间开奖'],
    bundle: ['boxes', '组合交付', physical ? '包含实物配送' : '各商品分别交付'],
  };
  const templates: Record<ShopKind, string> = { virtual: 'order-delivery', physical: 'physical-shipping', cdk: 'activation', donation: 'donation-record', raffle: product.raffle?.mode === 'instant' ? 'instant-raffle' : 'scheduled-raffle', bundle: 'bundle-delivery' };
  const delivery = deliveries[product.kind] || ['shopping-bag', '商品交付', '交付方式见购买须知'];
  return [
    { template: templates[product.kind] || '', icon: delivery[0], title: delivery[1], description: delivery[2], link: '' },
    { template: 'currency', icon: 'wallet', title: '{结算货币}结算', description: '下单前核对付款金额', link: '' },
    { template: 'refund', icon: 'rotate-ccw', title: '退款与售后', description: '查看购买与退款规则', link: '#shop-product-rules' },
  ];
}

export function resolve(product: ServiceProduct, { products = [], currencies = [] }: ServiceContext = {}): ServiceModule[] {
  const config = product.services;
  const items = config?.mode === 'hidden' ? [] : config?.mode === 'custom' ? config.items || [] : defaults(product, products);
  const values = context(product, currencies);
  const interpolate = (value: string) => value.replace(/\{([^{}]+)\}/g, (token: string, key: string) => Object.hasOwn(values, key) ? String(values[key]) : token);
  return items.map(item => ({ ...item, title: interpolate(item.title || ''), description: interpolate(item.description || ''), link: safeLink(item.link) }));
}

export const shopServices = { safeLink, context, defaults, resolve };
