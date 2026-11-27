import { createShopRecovery } from '../shared/shop-recovery';
import { create, APIError } from '../shared/newpress-client';
import type { ClientSession } from '../shared/newpress-client';
import { node, failure, nonce } from '../shared/ledger-data';
import { createSiteTime } from '../shared/site-time';
import { units as paymentUnits } from '../shared/currency-amount';
import { shopKinds as kinds, shopModes as modes, shopStatuses as statuses } from '../shared/shop-types';
import type { ShopProduct, ShopCatalog, ShopMode, ShopOrder, ShopQuote, ShopPurchase, ShopPending, ShopInstallment, ShopRafflePlan, ShopRaffleRound, ShopRaffleQuota, ShopProductStats, ShopPublicReview, ShopGroup } from '../shared/shop-types';
import type { ShopKind } from '../shared/shop-service-types';
import type { CouponOption, ShopCurrency, PendingCoupon } from '../shared/shop-coupon-data';
import { couponClaim, pendingCoupon } from '../shared/shop-coupon-data';
import type { PaymentOrder, PublicChannel } from '../shared/payment-data';
import { shopCatalogResponse, shopSession, shopQuote, matchQuote, createdShopOrder, shopOrderResponse, shopOrderList, shopPayment, shopReviews, shopOrderReview, ownShopReview, shopOrderID, shopOrderUpdate } from '../shared/shop-data';
import { money as formatMoney, amountText, units, isDate, optionsFor, safeURL, raffleDeadline, rafflePhase, rafflePurchaseCapacity, raffleCountdown } from '../shared/shop-helpers';
import { resolve as resolveServices } from '../shared/shop-services';

type Choice = [string, string, string?];
type ReceiptRow = [string, string, boolean?];
interface StorefrontState {
  catalog: ShopCatalog | null; channels: PublicChannel[]; currencies: ShopCurrency[]; coupons: CouponOption[]; groups: ShopGroup[];
  owner: string; filter: string; modeFilter: string; query: string; sort: string; product: ShopProduct | null; quote: ShopQuote | null; draft: ShopPurchase | null; order: ShopOrder | null;
  busy: boolean; serial: number; orderSerial: number; poll: number | undefined; polls: number; shareTimer: number | undefined;
  category: string; currency: string; inStock: boolean; stats: Record<string, ShopProductStats>; featuredIndex: number;
  reviewRating: number; reviewPage: number; reviewSort: string; reviewReplied: boolean; reviewSerial: number; orderReviewSerial: number;
  personalSerial: number; pendingCouponClaim: string; raffleQuotas: Record<string, ShopRaffleQuota>; personalRaffleQuotas: Record<string, ShopRaffleQuota>;
  raffleRounds: Record<string, ShopRaffleRound>; catalogReadAt: number; viewRound: ShopRaffleRound | undefined; viewRoundAt: number;
  gallery: string[]; galleryIndex: number; galleryVariant: string | null;
}
function required<T>(value: T | null | undefined): T { if (value === null || value === undefined) throw new Error('商城页面状态未确认，请刷新后重试'); return value; }
function inputField(form: HTMLFormElement, name: string): HTMLInputElement {
  const value = form.elements.namedItem(name); if (!(value instanceof HTMLInputElement)) throw new Error('商城输入控件未确认：' + name); return value;
}
function addressField(form: HTMLFormElement): HTMLTextAreaElement {
  const value = form.elements.namedItem('address'); if (!(value instanceof HTMLTextAreaElement)) throw new Error('商城地址控件未确认'); return value;
}
function selectedMode(value: string): ShopMode { if (!Object.hasOwn(modes, value)) throw new Error('购买方案未确认'); return value as ShopMode; }
const kindLabel = (value: string) => Object.hasOwn(kinds, value) ? kinds[value as ShopKind] : '';
const modeLabel = (value: string) => Object.hasOwn(modes, value) ? modes[value as ShopMode] : '';

(() => {
  const targetRoot = document.querySelector('[data-shop]'); if (!(targetRoot instanceof HTMLElement)) return;
  const root = targetRoot;
  function element<T extends HTMLElement>(selector: string, type: { new(): T }): T {
    const result = root?.querySelector(selector); if (!(result instanceof type)) throw new Error('商城页面控件缺失：' + selector); return result;
  }
  const $ = (selector: string) => element(selector, HTMLElement);
  const $input = (selector: string) => element(selector, HTMLInputElement);
  const $button = (selector: string) => element(selector, HTMLButtonElement);
  const $form = (selector: string) => element(selector, HTMLFormElement);
  const $dialog = (selector: string) => element(selector, HTMLDialogElement);
  const $$ = (selector: string) => [...root.querySelectorAll(selector)].filter((el): el is HTMLElement => el instanceof HTMLElement);
  let generation = 0, retired = false, catalogSerial = 0, ordersSerial = 0, paymentSerial = 0, claimBusy = false;
  let expectedToken = '';
  const active = (epoch: number) => !retired && epoch === generation && root.isConnected;
  const $buttons = (selector: string) => $$(selector).filter((el): el is HTMLButtonElement => el instanceof HTMLButtonElement);
  const client = create({ onUnauthorized: () => identity(null) });
  const recovery = createShopRecovery({ getItem: key => sessionStorage.getItem(key), setItem: (key, value) => sessionStorage.setItem(key, value), removeItem: key => sessionStorage.removeItem(key) });
  const couponAttempts = new Map<string, PendingCoupon>();
  const couponKey = (owner: string, code: string) => 'newpress.shop.coupon.pending.' + owner + '.' + code;
  const couponAttempt = (owner: string, code: string) => { const key = couponKey(owner, code), raw = sessionStorage.getItem(key); return couponAttempts.get(key) || (raw === null ? null : pendingCoupon(JSON.parse(raw) as unknown, owner, code)); };
  const rememberCoupon = (pending: PendingCoupon) => { sessionStorage.setItem(couponKey(pending.owner, pending.code), JSON.stringify(pending)); couponAttempts.set(couponKey(pending.owner, pending.code), pending); };
  const forgetCoupon = (pending: PendingCoupon) => { sessionStorage.removeItem(couponKey(pending.owner, pending.code)); couponAttempts.delete(couponKey(pending.owner, pending.code)); };
  const api = (path: string, method = 'GET', body?: unknown, anonymous = false) => client.request('/api/v1/shop' + path, { method, body, anonymous });
  const button = (text: string, cls = 'ui-button ui-button-secondary') => { const el = node('button', cls, text); el.type = 'button'; return el; };
  const state: StorefrontState = { catalog: null, channels: [], currencies: [], coupons: [], raffleQuotas: {}, personalRaffleQuotas: {}, pendingCouponClaim: '', groups: [], owner: '', filter: 'all', modeFilter: '', query: '', sort: 'featured', product: null, quote: null, draft: null, order: null, busy: false, serial: 0, orderSerial: 0, poll: undefined, polls: 0, shareTimer: undefined,
    category: '', currency: '', inStock: false, stats: {}, featuredIndex: 0, reviewRating: 0, reviewPage: 1, reviewSort: 'latest', reviewReplied: false, reviewSerial: 0, orderReviewSerial: 0, personalSerial: 0, raffleRounds: {}, catalogReadAt: 0, viewRound: undefined, viewRoundAt: 0, gallery: [], galleryIndex: 0, galleryVariant: null };
  const emptyStats = (): ShopProductStats => ({ count: 0, average: 0, positive: 0, distribution: [0,0,0,0,0], sold: 0 });
  const currencyInfo = (key: string) => state.currencies.find(item => item.key === key);
  const money = (value: string | bigint = '0', currency = 'CNY', precision = currency === 'CNY' ? 2 : currencyInfo(currency)?.precision ?? 2) => formatMoney(value, currency, precision, currencyInfo(currency));
  const catalog = () => required(state.catalog), currentProduct = () => required(state.product);
  const productPage = $('[data-shop-product-page]'), productContent = $('[data-shop-detail-content]'), productMissing = $('[data-shop-product-missing]'), productLoading = $('[data-shop-detail-loading]');
  const checkout = $dialog('[data-shop-checkout]'), orderDialog = $dialog('[data-shop-order-dialog]');
  const purchase = $form('[data-shop-purchase]'), confirm = $form('[data-shop-confirm]');
  let opener: HTMLElement | null = null, raffleTimer: number | undefined, serverEpoch: number | undefined;
  const shopNow = () => serverEpoch === undefined ? Date.now() : serverEpoch + performance.now();
  const raffleClosed = (p: ShopProduct | null) => shopNow() >= raffleDeadline(p);
  const campaignFor = (p: ShopProduct | null) => state.catalog?.raffle_campaigns?.find(c => c.key === p?.raffle?.campaign);
  const raffleCapacity = (p: ShopProduct) => rafflePurchaseCapacity({ ...p, stock: state.catalog?.products.find(item => item.key === p.key)?.stock ?? p.stock }, state.raffleQuotas[p.key], state.personalRaffleQuotas[p.key], campaignFor(p));
  const raffleSold = (p: ShopProduct) => p.kind === 'raffle' && raffleCapacity(p).soldOut;
  const cutoffMessage = '本轮抽奖已截止。已付款的抽奖码可在订单中查看结果。';

  const time = createSiteTime(() => document.documentElement.dataset.siteTimezone);
  const date = (value: unknown) => isDate(value) ? time.formatDateTime(value) : '—';
  function message(text = '', target = '[data-shop-message]', error = false) { const el = $(target); el.textContent = text; el.hidden = !text; el.dataset.error = String(error); }
  function show(dialog: HTMLDialogElement) { if (!dialog.open) dialog.showModal(); }
  function close(dialog: HTMLDialogElement) { dialog.close(); if (dialog === checkout && !productPage.hidden) $button('[data-shop-buy]').focus(); else opener?.focus(); }
  function setBusy(value: boolean) { state.busy = value; required(purchase.querySelector('fieldset')).disabled = value; if (state.product) updateSelection(); updateConfirmButton(); applyRaffleCutoff(); confirm.querySelectorAll('input').forEach(el=>{el.disabled=value;}); root.querySelectorAll<HTMLInputElement>('[data-shop-order-channel] input').forEach(el=>{el.disabled=value;}); $buttons('[data-shop-checkout-close],[data-shop-order-close],[data-shop-order-sync],[data-shop-order-cancel]').forEach(el => { el.disabled = value; }); }
  const tag = (text: string, accent = false) => { const el = node('span', 'shop-tag', text); el.dataset.accent = String(accent); return el; };
  const empty = (title: string, text: string) => { const el = node('div', 'shop-empty'); el.append(node('strong', '', title), node('p', '', text)); return el; };
  const productURL = (key: string) => `/shop/product/${encodeURIComponent(key)}/`;
  function visual(p: ShopProduct, source = p.image || p.images?.[0], eager = false) {
    const url = source && safeURL(source);
    if (url) { const img = node('img'); img.src = url; img.alt = p.title; img.loading = eager ? 'eager' : 'lazy'; if (eager) img.fetchPriority = 'high'; img.decoding = 'async'; img.addEventListener('error', () => img.replaceWith(art(p)), { once: true }); return img; }
    return art(p);
  }
  function icon(name: string) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg'), use = document.createElementNS(svg.namespaceURI, 'use');
    svg.setAttribute('class', 'icon'); svg.setAttribute('viewBox', '0 0 24 24'); svg.setAttribute('aria-hidden', 'true'); use.setAttribute('href', `#icon-${name}`); svg.append(use); return svg;
  }
  function selectImage(index: number, scroll = false) {
    if (!state.product) return;
    const images = state.gallery || []; state.galleryIndex = images.length ? (index + images.length) % images.length : 0;
    $('[data-shop-detail-visual]').replaceChildren(visual(state.product, images[state.galleryIndex], true));
    $('[data-shop-gallery-count]').textContent = images.length ? `${state.galleryIndex + 1} / ${images.length}` : '暂无图片';
    $$('[data-shop-gallery-image]').forEach(el => { const selected = Number(el.dataset.shopGalleryImage) === state.galleryIndex; el.setAttribute('aria-pressed', String(selected)); if (selected && scroll) el.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'instant' }); });
  }
  function renderGallery(p: ShopProduct) {
    state.galleryVariant = null;
    state.gallery = [...new Set([p.image, ...(p.images || []), ...(p.variants || []).map(v => v.image)].filter(Boolean).map(value => safeURL(value)).filter(Boolean))];
    $('[data-shop-gallery-wrap]').hidden = !state.gallery.length;
    $('[data-shop-gallery-next]').hidden = state.gallery.length < 2;
    $('[data-shop-gallery-thumbnails]').replaceChildren(...state.gallery.map((url, index) => {
      const thumb = button('', 'shop-gallery-thumb'); thumb.dataset.shopGalleryImage = String(index); thumb.setAttribute('aria-label', `查看第 ${index + 1} 张商品图片`); thumb.append(visual(p, url)); return thumb;
    }));
    selectImage(0);
  }
  async function shareProduct() {
    if (!state.product) return;
    const data = { title: state.product.title, url: new URL(productURL(currentProduct().key), location.origin).href };
    try {
      if (navigator.share) { await navigator.share(data); return; }
      await navigator.clipboard.writeText(data.url); message('商品链接已复制', '[data-shop-share-status]');
    } catch (caught) { const error = failure(caught);
      if (error.name === 'AbortError') return;
      const status = $('[data-shop-share-status]'), link = node('a', '', data.url); link.href = data.url;
      status.replaceChildren(node('span', '', '可复制此商品链接：'), link); status.hidden = false;
    }
    clearTimeout(state.shareTimer); state.shareTimer = setTimeout(() => { $('[data-shop-share-status]').hidden = true; }, 8000);
  }
  function renderProductRating(p: ShopProduct) {
    const stats=state.stats[p.key]||emptyStats(), rating=$('[data-shop-detail-rating]');rating.replaceChildren(icon('star'),node('span','',stats.count?`${stats.average.toFixed(1)} 分 · ${stats.count} 条评价`:'暂无买家评价'),...(stats.sold?[node('span','',`已售 ${stats.sold}`)]:[]),icon('chevron-right'));
  }
  function renderProductFacts(p: ShopProduct) {
    renderProductRating(p);
    const physical = p.kind === 'physical' || p.kind === 'bundle' && (p.bundle || []).some(b => catalog().products.find(v => v.key === b.product)?.kind === 'physical');
    const delivery = { virtual: ['file-down', '订单交付', '付清后查看专属内容'], physical: ['shopping-bag', '实物配送', `运费 ${money(p.shipping_fee || '0', p.currency, p.precision)}`], cdk: ['key-round', '自动发卡', '付清后在订单中查看'], donation: ['heart', '捐赠记录', '付款后可查询记录'], raffle: ['tags', '抽奖码', p.raffle?.mode === 'instant' ? '付款后即时开奖' : '按指定时间开奖'], bundle: ['boxes', '组合交付', physical ? '包含实物配送' : '各商品分别交付'] }[p.kind];
    const currencyName = currencyInfo(p.currency)?.name || p.currency, badges = $('[data-shop-detail-tags]');
    badges.replaceChildren(...[[delivery[0], kinds[p.kind]], ['wallet', currencyName], ...(p.category ? [['tags', p.category]] : [])].map(([name, label]) => { const el = node('span', 'shop-tag'); el.append(icon(name || ''), document.createTextNode(label || '')); return el; }));
    const services = resolveServices(p, { products: catalog().products, currencies: state.currencies }), serviceHost = $('[data-shop-detail-service]');
    serviceHost.hidden = !services.length; serviceHost.dataset.columns = String(Math.min(3, services.length));
    serviceHost.replaceChildren(...services.map(item => {
      const el = node(item.link ? 'a' : 'div', 'shop-service-item'); if (el instanceof HTMLAnchorElement) el.href = item.link || '';
      el.append(icon(item.icon), node('b', '', item.title)); if (item.description) el.append(node('small', '', item.description)); return el;
    }));
    $('[data-shop-specifications]').replaceChildren(...[['商品名称', p.title], ['商品类型', kinds[p.kind]], ['商品分类', p.category], ['购买方案', modes[p.mode]], ['计价货币', currencyName], ['可选规格', (p.variants || []).map(v => v.title).join('、')]].filter(([, value]) => value).map(([label, value]) => { const row = node('div'); row.append(node('dt', '', label), node('dd', '', value)); return row; }));
    const details = $('[data-shop-detail-images]'); details.replaceChildren(...[...new Set(p.detail_images || [])].filter(value => safeURL(value)).map((url, index) => { const img = node('img'); img.src = safeURL(url); img.alt = `${p.title}详情图 ${index + 1}`; img.loading = 'lazy'; img.decoding = 'async'; img.addEventListener('error', () => { img.replaceWith(node('p', 'shop-note', `第 ${index + 1} 张详情图暂时无法加载`)); }, { once: true }); return img; }));
  }
  function selectInfoPanel(hash: string, { updateHistory = false, scroll = false, focus = false } = {}) {
    const tabs = $$('.shop-product-info-tabs [role="tab"]'), selected = tabs.find(tab => (tab.getAttribute('href') || '') === hash);
    if (!selected || !state.product || productContent.hidden) return false;
    const activePanel = $('[data-shop-info-panel]:not([hidden])'), moveFocus = focus || activePanel?.contains(document.activeElement);
    tabs.forEach(tab => { const active = tab === selected; tab.setAttribute('aria-selected', String(active)); tab.tabIndex = active ? 0 : -1; });
    $$('[data-shop-info-panel]').forEach(panel => { panel.hidden = panel.id !== selected.getAttribute('aria-controls'); });
    if (updateHistory && location.hash !== hash) history.pushState(history.state, '', hash);
    if (moveFocus) selected.focus({ preventScroll: true });
    const menu = $('.shop-product-info-tabs');
    if (scroll) menu.scrollIntoView({ block: 'start', behavior: 'instant' });
    // Scroll only the tab strip horizontally; the selected panel may be much shorter.
    const tabRect = selected.getBoundingClientRect(), menuRect = menu.getBoundingClientRect();
    if (tabRect.left < menuRect.left) menu.scrollLeft -= menuRect.left - tabRect.left;
    else if (tabRect.right > menuRect.right) menu.scrollLeft += tabRect.right - menuRect.right;
    return true;
  }
  function syncInfoPanel() {
    const hash = location.hash, known = $$('.shop-product-info-tabs [role="tab"]').some(tab => (tab.getAttribute('href') || '') === hash);
    selectInfoPanel(known ? hash : '#shop-product-description', { scroll: known });
  }
  function renderOffer(p: ShopProduct, mode: ShopMode) {
    const host = $('[data-shop-detail-offer]'), coupon = state.coupons.find(c => (c.claimed || c.can_claim || !state.owner) && couponFits({ ...c, claimed: true }, p, mode));
    host.hidden = !coupon; host.replaceChildren(); if (!coupon) return;
    const copy = node('div', 'shop-offer-copy'), action = button(coupon.claimed ? '去使用' : '领取', 'shop-offer-action');
    const amount = coupon.kind === 'percent' ? `${amountText(coupon.value, 2).replace(/\.?0+$/, '')}% 折扣` : `减 ${money(coupon.value, coupon.currency, p.precision)}`;
    copy.append(node('b', '', coupon.title), node('span', '', [amount, BigInt(coupon.minimum || '0') > 0n ? `满 ${money(coupon.minimum, p.currency, p.precision)} 可用` : '无金额门槛'].join(' · ')));
    action.dataset.shopOfferCoupon = coupon.code; action.append(icon('chevron-right')); host.append(icon('tags'), copy, action);
  }
  function art(p: ShopProduct) { const el = node('span', 'shop-product-art'); el.append(node('span', '', kinds[p.kind]), node('b', '', ({ virtual: '数', physical: '物', cdk: '码', donation: '捐', raffle: '奖', bundle: '组' })[p.kind]), node('span', '', p.category || '暂无商品图片')); return el; }
  const displayPrice = (p: ShopProduct) => p.mode === 'group' ? required(p.group).price : p.mode === 'deposit' ? p.deposit : p.variants?.length ? p.variants.reduce((lowest, v) => BigInt(v.price) < BigInt(lowest) ? v.price : lowest, required(p.variants[0]).price) : p.price;
  function marketCouponFits(c: CouponOption, p: ShopProduct) {
    return c.enabled && (c.claimed || c.can_claim || !state.owner) && Date.parse(c.starts_at) <= Date.now() && Date.parse(c.ends_at) > Date.now() && (!c.currency || c.currency === p.currency) && (!c.products?.length || c.products.includes(p.key)) && (!c.kinds?.length || c.kinds.includes(p.kind)) && (!c.modes?.length || c.modes.includes(p.mode)) && (!c.categories?.length || c.categories.includes(p.category));
  }
  function renderMarket() {
    const products = state.catalog?.products || [], categories = [...new Set(products.map(p => p.category).filter(Boolean))];
    $('[data-shop-categories]').replaceChildren(...['', ...categories].map(value => { const b = button(value || '首页', ''); b.dataset.shopCategory = value; b.setAttribute('aria-pressed', String(state.category === value)); return b; }));
    const entries = [['kind', 'all', '全部商品', 'store'], ['mode', 'group', '拼团', 'users'], ['mode', 'presale', '预售', 'calendar-check'], ['mode', 'installment', '分期', 'wallet'], ['kind', 'virtual', '虚拟商品', 'file-down'], ['kind', 'physical', '实物', 'shopping-bag'], ['kind', 'cdk', '卡密', 'key-round'], ['kind', 'raffle', '抽奖', 'tags'], ['kind', 'bundle', '组合套装', 'boxes'], ['coupon', '', '领券中心', 'tags']];
    $('[data-shop-shortcuts]').replaceChildren(...entries.filter(([type,value]) => type === 'coupon' || value === 'all' || products.some(p => p[type === 'mode' ? 'mode' : 'kind'] === value)).map(([type,value,label,name], index) => { const b = button('', 'shop-market-shortcut'); b.dataset.shopShortcut = type; b.dataset.value = value; b.dataset.tone = String(index % 6); const badge = node('span', 'shop-shortcut-icon'); badge.append(icon(name || '')); b.append(badge, node('span', '', label)); b.setAttribute('aria-pressed', 'false'); return b; }));
    $('[data-shop-currency-filters]').replaceChildren(node('span', '', '结算货币'), ...[['', '全部'], ...state.currencies.filter(c => products.some(p => p.currency === c.key)).map(c => [c.key,c.name])].map(([key,label]) => { const b = button(label || '', ''); b.dataset.shopCurrency = key; b.setAttribute('aria-pressed', String(state.currency === key)); return b; }));
    renderFeatured(); renderMarketCoupons();
  }
  function renderFeatured() {
    const list = (state.catalog?.products || []).filter(p => p.image || p.images?.length).slice(0,5), host = $('[data-shop-featured]'); host.hidden = !list.length;
    if (!list.length) return;
    state.featuredIndex = (state.featuredIndex + list.length) % list.length;
    const p = required(list[state.featuredIndex]), link = node('a','shop-featured-slide'), copy = node('div','shop-featured-copy'); link.href = productURL(p.key);
    copy.append(node('span','shop-featured-label',p.category || kinds[p.kind]), node('h2','',p.title), node('p','',p.summary), node('span','shop-featured-action','查看商品 ›'));
    link.append(visual(p, undefined, true),copy); $('[data-shop-featured-content]').replaceChildren(link);
    $('[data-shop-featured-dots]').replaceChildren(...list.map((item,index) => { const b = button('',''); b.dataset.shopFeatured = String(index); b.setAttribute('aria-label',`查看推荐商品 ${index+1}：${item.title}`); b.setAttribute('aria-pressed',String(index === state.featuredIndex)); return b; }));
    $('.shop-featured-controls').hidden = list.length < 2;
  }
  let couponTab = "available";
  let couponLauncher: HTMLElement | null = null;
  function renderMarketCoupons() {
    const coupons = state.coupons.filter(c => c.enabled && Date.parse(c.starts_at) <= Date.now() && Date.parse(c.ends_at) > Date.now() && (couponTab === "claimed" ? c.claimed : !c.claimed));
    root.querySelectorAll<HTMLElement>("[data-shop-coupon-tab]").forEach(tab => tab.setAttribute("aria-pressed", String(tab.dataset.shopCouponTab === couponTab)));
    $('[data-shop-market-coupon-list]').replaceChildren(...(coupons.length ? coupons.map(c => { const card = node('article','shop-market-coupon'), copy = node('div'), amount = c.kind === 'percent' ? `减 ${amountText(c.value,2).replace(/\.?0+$/,'')}%` : money(c.value,c.currency), claim = button(c.claimed ? '已领取' : state.owner && !c.can_claim ? '暂不可领' : '领取','');
      copy.append(node('b','',c.title),node('small','',`${BigInt(c.minimum || '0') > 0n ? `满 ${money(c.minimum,c.currency)} 可用` : '无金额门槛'} · ${c.currency ? currencyInfo(c.currency)?.name || c.currency : '多种货币'}`),node('small','',`有效期至 ${date(c.ends_at)}`));
      const scope=[c.products?.length?'限指定商品':'',...(c.kinds||[]).map(k=>kindLabel(k)),...(c.modes||[]).map(m=>modeLabel(m)),c.levels?.length?'限指定等级':'',c.memberships?.length?'限指定会员':''];if(scope.some(Boolean))copy.append(node('small','',scope.filter(Boolean).join(' · ')));
      let unresolved = false;
      if (state.owner) { try { unresolved = !!couponAttempt(state.owner, c.code); } catch { unresolved = true; } }
      if (unresolved) claim.textContent = '核对领取结果';
      claim.dataset.shopClaim = c.code; claim.disabled = claimBusy || state.busy || !unresolved && (c.claimed || Boolean(state.owner && !c.can_claim)); card.append(node('strong','',amount),copy,claim); return card; }) : [empty(couponTab === 'claimed' ? state.owner ? '还没有领取优惠券' : '登录后查看你的优惠券' : '暂无可领取的优惠券', couponTab === 'claimed' ? '可在「可领取」中挑选优惠券，领取后在结算时使用。' : '新的优惠券会在这里展示，先逛逛喜欢的商品吧。')]));
    if (couponTab === 'claimed' && !state.owner) {
      const login=button('登录查看优惠券','shop-coupon-login');
      login.addEventListener('click',()=>{couponDialog.close();document.querySelector<HTMLElement>('[data-account-login]')?.click();});
      $('[data-shop-market-coupon-list]').append(login);
    }
  }
  let couponDialog = $('[data-shop-market-coupons]') as HTMLDialogElement;
  // Older installed themes may still provide the inline coupon section.
  if (!(couponDialog instanceof HTMLDialogElement)) {
    const previous = couponDialog as HTMLElement, upgraded = document.createElement('dialog');
    for (const attribute of previous.attributes) if (attribute.name !== 'hidden') upgraded.setAttribute(attribute.name, attribute.value);
    upgraded.classList.add('shop-coupon-dialog'); upgraded.append(...previous.childNodes); previous.replaceWith(upgraded); couponDialog = upgraded;
  }
  if (!couponDialog.querySelector('[data-shop-coupon-tab]')) {
    const tabs = node('nav','shop-coupon-tabs'); tabs.setAttribute('aria-label','优惠券分类');
    for (const [value,label] of [['available','可领取'],['claimed','我的优惠券']] as const) { const tab=button(label,'');tab.dataset.shopCouponTab=value;tab.setAttribute('aria-pressed',String(value==='available'));tabs.append(tab); }
    couponDialog.querySelector('header')?.after(tabs);
  }
  if (!couponDialog.querySelector('[data-shop-coupon-message]')) {
    const feedback=node('p','shop-coupon-feedback');feedback.dataset.shopCouponMessage='';feedback.setAttribute('role','status');feedback.setAttribute('aria-live','polite');couponDialog.querySelector('[data-shop-market-coupon-list]')?.before(feedback);
  }
  couponDialog.addEventListener('close', () => { couponLauncher?.focus({preventScroll:true}); });
  couponDialog.addEventListener('click', event => { if (event.target !== couponDialog) return; const rect = couponDialog.getBoundingClientRect(); if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) couponDialog.close(); });
  function showMarketCoupons() {
    if ($('[data-shop-catalog]').hidden) { location.href = '/shop/?coupons=1'; return; }
    const dialog = $('[data-shop-market-coupons]') as HTMLDialogElement;
    couponLauncher = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    renderMarketCoupons(); if (!dialog.open) dialog.showModal();
    dialog.querySelector<HTMLElement>('[data-shop-coupons-close]')?.focus();
  }
  function renderProducts() {
    if (!state.catalog) return;
    const list = (state.catalog?.products || []).filter(p => (state.filter === 'all' || p.kind === state.filter) && (!state.modeFilter || p.mode === state.modeFilter) && (!state.category || p.category === state.category) && (!state.currency || p.currency === state.currency) && (!state.inStock || String(p.stock) !== '0' && !raffleClosed(p) && !raffleSold(p)) && (!state.query || `${p.title} ${p.summary} ${p.category}`.toLowerCase().includes(state.query.toLowerCase())));
    if (state.sort === 'low' || state.sort === 'high') list.sort((a, b) => { if (a.currency !== b.currency) return a.currency.localeCompare(b.currency); const n = BigInt(displayPrice(a)) - BigInt(displayPrice(b)); return (n < 0n ? -1 : n > 0n ? 1 : 0) * (state.sort === 'low' ? 1 : -1); });
    if (state.sort === 'title') list.sort((a, b) => a.title.localeCompare(b.title, 'zh-CN'));
    if (state.sort === 'sales') list.sort((a,b) => (state.stats[b.key]?.sold || 0) - (state.stats[a.key]?.sold || 0));
    if (state.sort === 'rating') list.sort((a,b) => (state.stats[b.key]?.average || 0) - (state.stats[a.key]?.average || 0));
    $('[data-shop-results-title]').textContent = state.category || (state.modeFilter ? modeLabel(state.modeFilter) : kindLabel(state.filter) || '全部商品');
    $('[data-shop-results-count]').textContent = `${list.length} 件商品${['low','high'].includes(state.sort) && new Set(list.map(p => p.currency)).size > 1 ? ' · 同种货币内排序' : ''}`;
    $('[data-shop-reset]').hidden = state.filter === 'all' && !state.modeFilter && !state.query && !state.category && !state.currency && !state.inStock;
    $$('[data-shop-category]').forEach(el => el.setAttribute('aria-pressed',String(el.dataset.shopCategory === state.category)));
    $$('[data-shop-currency]').forEach(el => el.setAttribute('aria-pressed',String(el.dataset.shopCurrency === state.currency)));
    $$('[data-shop-shortcut]').forEach(el => el.setAttribute('aria-pressed',String(el.dataset.shopShortcut === 'mode' ? state.modeFilter === el.dataset.value : el.dataset.shopShortcut === 'kind' && state.filter === el.dataset.value && !state.modeFilter)));
    $$('[data-shop-filters] button').forEach(el => el.setAttribute('aria-pressed', String(el.dataset.shopFilter === state.filter)));
    $$('[data-shop-mode-filter]').forEach(el => el.setAttribute('aria-pressed', String(el.dataset.shopModeFilter === state.modeFilter)));
    $$('[data-shop-sort]').forEach(el => el.setAttribute('aria-pressed', String(el.dataset.shopSort === state.sort)));
    const cards = list.map(p => {
      const card = node('article', 'shop-product'), open = node('a', 'shop-product-open'), body = node('div', 'shop-product-body');
      open.href = productURL(p.key); open.setAttribute('aria-label', `查看${p.title}详情`); open.append(visual(p));
      const coupon = state.coupons.find(c => marketCouponFits(c,p)), stats = state.stats[p.key] || emptyStats();
      const flagText = raffleClosed(p) ? '参与已截止' : raffleSold(p) ? '抽奖码已售罄' : String(p.stock) === '0' ? '暂时售罄' : p.mode !== 'direct' ? modes[p.mode] : coupon ? '可领优惠券' : kinds[p.kind]; open.append(node('span', 'shop-product-ribbon', flagText));
      const tags = node('div', 'shop-tags'); if (coupon) tags.append(tag('领券优惠',true)); if (p.kind === 'physical' && String(p.shipping_fee) === '0') tags.append(tag('包邮',true)); else tags.append(tag(kinds[p.kind])); if (p.mode !== 'direct') tags.append(tag(modes[p.mode], true));
      const h = node('h3'), title = node('a', '', p.title); title.href = productURL(p.key); h.append(title);
      const price = node('div', 'shop-product-price'), amount = amountText(displayPrice(p),p.precision), prefix = money(displayPrice(p),p.currency,p.precision).slice(0,-amount.length), priceNumber = node('strong'); priceNumber.append(node('small','',prefix),document.createTextNode(amount.replace(/\.0+$/,''))); price.append(priceNumber);
      if (p.variants?.length > 1) price.append(node('small', '', '起'));
      if (p.mode !== 'deposit' && BigInt(p.compare_at || '0') > BigInt(displayPrice(p))) price.append(node('del', '', money(p.compare_at,p.currency,p.precision)));
      if (p.mode === 'deposit') price.append(node('small', '', '定金')); if (p.mode === 'group') price.append(node('small', '', `${required(p.group).size} 人团`));
      price.append(node('span','shop-product-sales',stats.sold ? `已售 ${stats.sold}` : '暂无成交'));
      const bottom = node('div', 'shop-product-bottom');
      if (stats.count) { const rating = node('span','shop-card-rating',`${stats.average.toFixed(1)} 分 · ${stats.count} 条评价`); rating.prepend(icon('star')); bottom.append(rating); } else bottom.append(node('span','','暂无评价'));
      bottom.append(node('span','',raffleClosed(p) ? '查看本期奖池' : raffleSold(p) ? '抽奖码已售罄' : String(p.stock) === '0' ? '暂时售罄' : p.variants?.length ? `${p.variants.length} 种规格` : p.category || ''));
      body.append(h,price,tags,bottom); card.append(open, body); return card;
    });
    $('[data-shop-products]').replaceChildren(...(cards.length ? cards : [empty(state.catalog?.enabled ? '这里还没有商品' : '商城暂未开放', state.query || state.filter !== 'all' || state.modeFilter ? '试试其他关键词，或清除筛选看看。' : '商品上架后会显示在这里，已有订单仍可查询。')]));
    $('[data-shop-products]').setAttribute('aria-busy', 'false');
  }
  function clearPersonal(): void {
    state.personalRaffleQuotas = {};
    state.currencies = state.currencies.map(({ balance, ...currency }) => currency);
    state.coupons = state.coupons.map(c => ({ ...c, claimed: false, can_claim: false, available_count: 0, user_claim_count: 0 }));
  }
  async function loadCatalog() {
    const serial = ++catalogSerial, epoch = generation;
    const result = shopCatalogResponse(await api('/catalog', 'GET', undefined, true));
    if (!active(epoch) || serial !== catalogSerial) return;
    state.catalog = result.catalog; state.channels = result.channels; state.currencies = result.currencies; state.coupons = result.coupons; state.groups = result.groups;
    state.stats = result.product_stats; state.raffleRounds = result.raffle_rounds; state.raffleQuotas = result.raffle_quotas;
    state.personalRaffleQuotas = {}; state.personalSerial++;
    state.catalogReadAt = Date.parse(result.server_time); serverEpoch = state.catalogReadAt - performance.now();
    if (state.product?.kind === 'raffle') {
      const product = state.product, current = result.catalog.products.find(p => p.key === product.key);
      if (current && JSON.stringify(current.raffle) === JSON.stringify(product.raffle)) renderRafflePool(current.raffle);
      updateSelection();
    }
    scheduleRaffleClock();
    $('[data-shop-availability]').textContent = result.catalog.enabled ? result.catalog.products.length + ' 件在售商品' : '暂未开放购买';
    renderMarket(); renderProducts();
    if (state.owner) await loadPersonal({ authenticated: true, id: state.owner });
    return result;
  }
  async function loadPersonal(session: { authenticated: boolean; id?: string | number }): Promise<boolean> {
    if (!session.authenticated || String(session.id) !== state.owner || retired) return false;
    const owner = state.owner, serial = ++state.personalSerial, epoch = generation;
    try {
      const result = shopSession(await api('/session'), owner);
      if (!active(epoch) || owner !== state.owner || serial !== state.personalSerial) return false;
      if (!expectedToken || result.csrf_token !== expectedToken) throw new Error('登录状态已变化，请刷新后重新核对');
      state.currencies = result.currencies; state.coupons = result.coupons; state.personalRaffleQuotas = result.raffle_quotas;
      for (const c of result.coupons) {
        try { const pending = couponAttempt(owner, c.code); if (pending && (c.user_claim_count > pending.count || pending.rejected)) forgetCoupon(pending); } catch { /* A broken claim record remains blocked at the write boundary. */ }
      }
      refreshCouponField(); renderMarketCoupons(); renderProducts();
      if (state.product?.kind === 'raffle') { renderRafflePool(state.product.raffle, state.viewRound, state.viewRoundAt); updateSelection(); }
      return true;
    } catch {
      if (active(epoch) && serial === state.personalSerial) { clearPersonal(); refreshCouponField(); renderMarketCoupons(); }
      return false;
    }
  }
  function couponMessage(text: string, error = false) { message(text, couponDialog.open ? '[data-shop-coupon-message]' : '[data-shop-message]', error); }
  async function claimCoupon(code: string): Promise<void> {
    if (state.busy || claimBusy || retired) return;
    claimBusy = true;
    let epoch = generation;
    try {
      const initialOwner = state.owner, initialAttempt = initialOwner ? couponAttempt(initialOwner, code) : null;
      const owner = await authenticated(); epoch = generation; claimBusy = true;
      const previous = (owner === initialOwner ? initialAttempt : null) || couponAttempt(owner, code);
      if (!await loadPersonal({ authenticated: true, id: owner }) || !active(epoch)) throw new Error('领取条件暂未确认，请重新读取后再试');
      const pending = couponAttempt(owner, code);
      if (previous && !pending) { state.pendingCouponClaim = ''; couponMessage(previous.rejected ? '上次请求未成功，领取条件已刷新，可再次点击领取。' : '已核对上次领取结果，优惠券已领取。'); return; }
      if (pending) { couponMessage(pending.confirmed ? '已领取，领取状态仍待刷新。' : '上次领取结果仍待核对，请稍后刷新；不会重复领取。'); return; }
      const current = state.coupons.find(c => c.code === code);
      if (!current?.can_claim) { state.pendingCouponClaim = ''; couponMessage(current?.claimed ? '这张优惠券已领取，可在适用商品中选择。' : '当前无法领取这张优惠券。'); return; }
      const attempt: PendingCoupon = { owner, code, count: current.user_claim_count };
      rememberCoupon(attempt);
      let receipt;
      try { receipt = couponClaim(await api('/coupons/claim', 'POST', { code }), owner, code); }
      catch (caught) {
        const error = failure(caught);
        if (active(epoch) && error.status && [400, 401, 403, 404, 409, 422, 429].includes(error.status)) { try { rememberCoupon({ ...attempt, rejected: true }); } catch {} }
        throw caught;
      }
      if (!active(epoch) || owner !== state.owner) return;
      state.pendingCouponClaim = '';
      try { rememberCoupon({ ...attempt, confirmed: receipt.id }); } catch { couponAttempts.set(couponKey(owner, code), { ...attempt, confirmed: receipt.id }); }
      const refreshed = await loadPersonal({ authenticated: true, id: owner });
      if (active(epoch) && refreshed && couponDialog.open) couponTab = 'claimed';
      if (active(epoch)) couponMessage(refreshed ? '优惠券已领取，可在符合条件的商品下单时使用。' : '优惠券已领取；余额和领取条件暂时无法刷新，请稍后核对。');
    } finally { if (active(epoch)) { claimBusy = false; renderMarketCoupons(); if (couponDialog.open) couponDialog.querySelector<HTMLElement>(`[data-shop-coupon-tab="${couponTab}"]`)?.focus({preventScroll:true}); } }
  }
  function activeWallet(currency: string,amount='0') { const item=state.currencies.find(c=>c.key===currency&&c.enabled&&c.balance!==undefined);return item&&BigInt(item.balance||'0')>=BigInt(amount||'0')?item:null; }
  function paymentOptions(currency: string,amount='0'): Choice[] { const entries: Choice[]=state.channels.filter(c=>c.currency===currency).map(c=>[c.key,c.name]);const wallet=activeWallet(currency,amount);if(wallet)entries.push(['wallet',`${wallet.name}支付（可用 ${money(wallet.balance,wallet.key,wallet.precision)}）`]);return entries; }
  function selectedSubtotal(p: ShopProduct,mode: ShopMode) {
    const variant=p.variants?.find(item=>item.key===inputField(purchase, 'variant').value),quantity=BigInt(Math.max(1,Number(inputField(purchase, 'quantity').value)||1));
    let unit=mode==='group'?required(p.group).price:mode==='deposit'?p.deposit:variant?.price||p.price;
    if(p.kind==='donation')try{unit=units(inputField(purchase, 'donation').value,p.precision)}catch{}
    return BigInt(unit||'0')*quantity;
  }
  function couponFits(c: CouponOption,p: ShopProduct,mode: ShopMode) { const now=Date.now(),subtotal=selectedSubtotal(p,mode);if(!c.claimed||!c.enabled||Date.parse(c.starts_at)>now||Date.parse(c.ends_at)<=now)return false;if(c.kinds?.length&&!c.kinds.includes(p.kind)||c.modes?.length&&!c.modes.includes(mode)||c.products?.length&&!c.products.includes(p.key)||c.categories?.length&&!c.categories.includes(p.category)||c.currency&&c.currency!==p.currency||BigInt(c.minimum||'0')>subtotal)return false;return true; }
  function refreshCouponField() {
    const p=state.product;if(!p)return;const field=$('[data-shop-coupon-field]'),choices=$('[data-shop-coupon-choices]'),mode=selectedMode(inputField(purchase, 'mode').value),keep=inputField(purchase, 'coupon').value;
    const list=state.coupons.filter(c=>couponFits(c,p,mode));field.hidden=!list.length;const entries: Choice[]=[['','不使用优惠券','按商品金额结算'],...list.map<Choice>(c=>{const amount=c.kind==='percent'?`${amountText(c.value,2).replace(/\.?0+$/,'')}% 折扣`:`立减 ${money(c.value,c.currency,p.precision)}`,minimum=BigInt(c.minimum||'0')>0n&&c.currency?`满 ${money(c.minimum,c.currency,currencyInfo(c.currency)?.precision??p.precision)} 可用`:'';return[c.code,c.title,[amount,minimum].filter(Boolean).join(' · ')]})];
    // Keep the focused coupon radio when only quantity or selection changes.
    const signature=JSON.stringify(entries),selected=list.some(c=>c.code===keep)?keep:'';
    if(choices.dataset.shopChoicesSignature!==signature){renderChoices(choices,'coupon',entries,selected);choices.dataset.shopChoicesSignature=signature;}else chooseValue(choices,'coupon',selected);
    renderOffer(p,mode);
  }
  function renderChoices(container: HTMLElement, name: string, entries: Choice[], selected = '') {
    const match=entries.find(([value])=>String(value)===String(selected)),initial=match||entries[0];
    container.setAttribute('role','radiogroup');container.replaceChildren(...entries.map(([value,title,detail=''])=>{const label=node('label','shop-choice-option'),input=node('input');input.type='radio';input.name=`shop_${name}_choice`;input.value=String(value);input.checked=Boolean(initial&&String(initial[0])===String(value));input.disabled=state.busy&&['channel','order_channel'].includes(name);input.addEventListener('change',()=>{const form=container.closest('form'),field=form?.elements.namedItem(name);if(field instanceof HTMLInputElement)field.value=input.value;});const copy=node('span','shop-choice-copy');copy.append(node('b','',title));if(detail)copy.append(node('small','',detail));label.append(input,copy);return label;}));
    const field=container.closest('form')?.elements.namedItem(name);if(field instanceof HTMLInputElement)field.value=initial?String(initial[0]):'';
  }
  function chooseValue(container: HTMLElement, name: string, value: string) {
    const input=[...container.querySelectorAll<HTMLInputElement>('input[type="radio"]')].find(item=>item.value===String(value));if(!input)return false;input.checked=true;const field=container.closest('form')?.elements.namedItem(name);if(field instanceof HTMLInputElement)field.value=input.value;return true;
  }
  function appendField(key: string, label: string, input: HTMLInputElement) { const el = node('label', '', label); input.name = key; el.append(input); required(purchase.querySelector('.shop-fields')).insertBefore(el, $('[data-shop-group-field]')); el.dataset.shopDynamic = ''; return el; }
  function appendChoiceField(key: string, label: string, entries: Choice[], selected: string) { const field=node('fieldset','shop-choice-field');field.dataset.shopDynamic='';field.append(node('legend','',label));const input=node('input');input.type='hidden';input.name=key;input.value=String(selected??'');const choices=node('div','shop-choice-list');field.append(input,choices);required(purchase.querySelector('.shop-fields')).insertBefore(field,$('[data-shop-group-field]'));renderChoices(choices,key,entries,selected);return field; }
  function openProduct(key: string) {
    const p = state.catalog?.products.find(p => p.key === key);
    productPage.hidden = false; $('[data-shop-catalog]').hidden = true; $('[data-shop-orders]').hidden = true;
    $('[data-shop-store-crumb]').hidden = true; $('[data-shop-store-heading]').hidden = true; $('[data-shop-store-tabs]').hidden = true;
    if (!p) { productContent.hidden = true; productLoading.hidden = true; productMissing.hidden = false; document.title = '商品暂不可用 · 商城'; return false; }
    state.product = p; state.quote = null; state.draft = null; state.serial++;
    purchase.reset(); required(purchase.querySelector('fieldset')).disabled = state.busy; $$('[data-shop-dynamic]').forEach(el => el.remove());
    root.dataset.shopView = 'product'; renderGallery(p);
    productContent.hidden = false; productLoading.hidden = true; productMissing.hidden = true; document.title = `${p.title} · 商城`; $('[data-shop-breadcrumb-title]').textContent = p.title;
    $('#shop-detail-title').textContent = p.title; $('[data-shop-subtitle]').textContent = p.summary; $('[data-shop-subtitle]').hidden = !p.summary; const description = $('[data-shop-description]'); if (description.dataset.shopDescriptionProduct !== p.key) { description.textContent = p.description || p.summary; delete description.dataset.shopRich; } description.hidden = !description.textContent.trim() && !description.children.length;
    renderChoices($('[data-shop-mode-choices]'),'mode',optionsFor(p).map(mode=>[mode,mode==='direct'&&p.mode==='group'?'单独购买':modes[mode],mode==='direct'&&p.mode==='group'?money(p.price,p.currency,p.precision):'']),p.mode);
    $('[data-shop-mode-field]').hidden = p.mode === 'direct';
    $('[data-shop-variant-field]').hidden = !p.variants?.length;
    renderChoices($('[data-shop-variant-choices]'),'variant',p.variants?.length?p.variants.map(v=>[v.key,v.title,money(v.price,p.currency,p.precision)]):[['','默认规格','']],p.variants?.[0]?.key||'');
    $$('[data-shop-variant-choices] .shop-choice-option').forEach((el, index) => { const variant = p.variants?.[index]; if (variant?.image && safeURL(variant.image)) { const img = visual({ ...p, title: variant.title }, variant.image); img.classList.add('shop-variant-image'); if (img instanceof HTMLImageElement) img.alt = ''; el.insertBefore(img, el.querySelector('.shop-choice-copy')); } });
    if (p.installment && p.mode === 'installment') appendChoiceField('periods','分期期数',required(p.installment).periods.map(n=>[String(n),`${n} 期`,`每 ${required(p.installment).interval_days} 天支付一期`]),String(required(p.installment).periods[0]));
    if (p.kind === 'donation') { const input = node('input'); input.type = 'text'; input.inputMode = 'decimal'; input.value = amountText(p.price,p.precision); input.required = true; appendField('donation', `捐赠金额（${money(p.price,p.currency,p.precision)} – ${money(p.max_donation,p.currency,p.precision)}）`, input); }
    const address = p.kind === 'physical' || p.kind === 'bundle' && (p.bundle || []).some(b => catalog().products.find(v => v.key === b.product)?.kind === 'physical');
    $('[data-shop-address]').hidden = !address; ['recipient', 'phone'].forEach(name => { inputField(purchase, name).required = address; }); addressField(purchase).required = address;
    const rules = [];
    if (p.kind === 'virtual') rules.push('付清后在订单中获取专属交付内容。');
    if (p.kind === 'cdk') rules.push('付清后自动发放卡密，请在订单详情中查看并妥善保存。');
    if (p.kind === 'physical') rules.push('付清后由商家发货，物流单号将在订单中更新。');
    if (p.kind === 'bundle') rules.push('套装包含：' + (p.bundle || []).map(b => `${catalog().products.find(v => v.key === b.product)?.title || b.product} × ${b.quantity}`).join('、'));
    if (p.kind === 'raffle') rules.push(`奖品：${required(p.raffle).prize}`, required(p.raffle).mode === 'instant' ? `支付后即时开奖，每个抽奖码中奖概率 ${required(p.raffle).win_bps / 100}%。` : `${date(required(p.raffle).draw_at)} 开奖，从本轮已支付的抽奖码中抽出 ${required(p.raffle).winners} 个中奖码（不足时按实际参与数）。`, '中奖结果保存在订单中；奖品领取请联系商家。');
    $('[data-shop-rules]').textContent = [...rules, catalog().terms, catalog().refund_policy].filter(Boolean).join('\n\n');
    renderRafflePool(p.kind === 'raffle' ? p.raffle : null);
    renderProductFacts(p);
    state.reviewPage = 1; state.reviewRating = 0; state.reviewReplied = false; loadReviews();
    refreshCouponField();
    scheduleRaffleClock();
    message('', '[data-shop-detail-message]'); updateSelection(); syncInfoPanel(); return true;
  }
  function updateSelection() {
    const p = state.product; if (!p) return;
    const mode = selectedMode(inputField(purchase, 'mode').value), v = p.variants?.find(item => item.key === inputField(purchase, 'variant').value);
    const price = mode === 'group' ? required(p.group).price : mode === 'deposit' ? p.deposit : v?.price || p.price;
    const amount = amountText(price, p.precision), [whole = '', fraction = ''] = amount.split('.'), prefix = money(price, p.currency, p.precision).slice(0, -amount.length);
    $('[data-shop-detail-price]').replaceChildren(node('span', 'shop-price-symbol', prefix), node('span', 'shop-price-integer', whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',')), ...(fraction && /[1-9]/.test(fraction) ? [node('span', 'shop-price-fraction', `.${fraction}`)] : [])); $('[data-shop-purchase-price]').textContent = money(price,p.currency,p.precision);
    if (state.galleryVariant !== (v?.key || '')) { state.galleryVariant = v?.key || ''; const index = state.gallery.indexOf(v?.image ? safeURL(v.image) : ''); if (index >= 0) selectImage(index); }
    const compare = BigInt(p.compare_at || '0'), currentPrice = BigInt(price), discount = $('[data-shop-discount]');
    $('[data-shop-compare-price]').hidden = mode === 'deposit' || compare <= currentPrice; $('[data-shop-compare-price]').textContent = money(p.compare_at,p.currency,p.precision);
    discount.hidden = mode === 'deposit' || compare <= currentPrice; discount.textContent = discount.hidden ? '' : `直降 ${money(String(compare - currentPrice),p.currency,p.precision)}`;
    $('[data-shop-price-label]').textContent = mode === 'group' ? `${required(p.group).size} 人拼团价` : mode === 'deposit' ? `定金 · 商品总价 ${money(p.price,p.currency,p.precision)}` : '商品价格';
    $('[data-shop-price-label]').hidden = !['group', 'deposit'].includes(mode);
    const note = mode === 'presale' ? `全款预售，预计 ${date(p.release_at)} 起交付。` : mode === 'deposit' ? `先付定金，${date(p.release_at)} 起可付尾款，请于 ${date(p.balance_due_at)} 前付清。` : mode === 'installment' ? `每期费率 ${required(p.installment).interest_bps / 100}%，按商品原始本金计算每期利息。付清后交付；下单前可核对完整账单。` : mode === 'group' ? `${required(p.group).size} 位不同用户付款后成团，限时 ${required(p.group).hours} 小时。未成团将进入退款处理。` : '一次付清，按商品约定交付。';
    $('[data-shop-mode-note]').textContent = mode === 'direct' ? '' : note;
    $('[data-shop-group-field]').hidden = mode !== 'group'; if (mode !== 'group') inputField(purchase, 'group_id').value = '';
    const limited = mode === 'group' || p.kind === 'donation', stocks = [Number(p.stock), Number(v?.stock ?? -1)].filter(n => n >= 0), available = stocks.length ? Math.min(...stocks) : -1;
    const capacity = p.kind === 'raffle' ? raffleCapacity(p) : null;
    const sold = capacity ? capacity.soldOut : available === 0;
    const max = capacity ? capacity.max : limited ? 1 : Math.max(1, Math.min(99, ...stocks));
    const blocked = max === 0;
    const stock=$('[data-shop-stock]');
    stock.textContent = capacity && blocked ? (capacity.personalLimit ? '已达到个人限购' : '抽奖码已售罄') : sold ? '暂时售罄' : mode==='group'?'拼团每单 1 件':p.kind==='donation'?'每单 1 份':capacity ? `最多可购 ${max} 张` : available<0?'库存充足':`剩余 ${available} 件`;
    stock.dataset.state=blocked || sold?'sold':limited?'fixed':'available';
    const inputMax = Math.max(1, max);
    inputField(purchase, 'quantity').max = String(inputMax); inputField(purchase, 'quantity').readOnly = blocked || inputMax <= 1 || !catalog().enabled; inputField(purchase, 'quantity').value = String(Math.max(1, Math.min(inputMax, Math.trunc(Number(inputField(purchase, 'quantity').value)) || 1)));
    refreshCouponField();
    $buttons('[data-shop-quantity-step="-1"]').forEach(el => { el.disabled = Number(inputField(purchase, 'quantity').value) <= 1 || !catalog().enabled; });
    $buttons('[data-shop-quantity-step="1"]').forEach(el => { el.disabled = blocked || Number(inputField(purchase, 'quantity').value) >= inputMax || !catalog().enabled; });
    if (purchase.elements.namedItem('periods')) { const field=required(inputField(purchase, 'periods').closest<HTMLElement>('.shop-choice-field'));field.hidden=mode!=='installment';inputField(purchase, 'periods').disabled=mode!=='installment';field.querySelectorAll<HTMLInputElement>('input[type="radio"]').forEach(el=>{el.disabled=mode!=='installment';}); }
    const quantity=Number(inputField(purchase, 'quantity').value)||1,range=$input('[data-shop-quantity-range]'),rangeWrap=$('[data-shop-quantity-range-wrap]');
    range.max=String(inputMax);range.value=String(quantity);range.disabled=blocked||inputMax<=1||!catalog().enabled;rangeWrap.hidden=inputMax<=1;
    range.style.setProperty('--shop-quantity-progress',`${inputMax>1?(quantity-1)/(inputMax-1)*100:0}%`);range.setAttribute('aria-valuetext',`${quantity} 件`);
    $('[data-shop-quantity-value]').textContent=`已选 ${quantity} 件`;$('[data-shop-quantity-max]').textContent=`${capacity && !state.owner ? '单次上限' : '上限'} ${inputMax} 件`;
    const groups = mode === 'group' ? state.groups.filter(g => g.product === p.key && Date.parse(g.expires_at) > Date.now()).slice(0, 5) : [];
    $('[data-shop-groups]').replaceChildren(...groups.map(g => { const el = node('div', 'shop-group-row'), text = node('div'); text.append(node('b', '', `${g.paid}/${g.size} 人已付款`), node('p', '', `${date(g.expires_at)} 截止`)); const join = button('参加拼团', ''); join.addEventListener('click', () => { inputField(purchase, 'group_id').value = g.id; message('已选择该团，核对订单后即可参加。', '[data-shop-detail-message]'); }); el.append(text, join); return el; }));
    const retry = hasRecovery();
    const buy = $button('[data-shop-buy]'), disabled = state.busy || (!retry && (sold || blocked || !catalog().enabled));
    const label = retry ? '查看上次下单结果' : blocked ? (capacity?.personalLimit ? '已达到个人限购' : '抽奖码已售罄') : sold ? '暂时售罄' : mode === 'group' ? '立即拼团' : mode === 'deposit' ? '支付定金' : p.kind === 'raffle' ? '购买抽奖码' : '立即购买';
    // A blur/change can happen between pointerdown and click. Keep the pressed button intact.
    if (buy.disabled !== disabled) buy.disabled = disabled;
    if (buy.textContent !== label) buy.textContent = label;
    applyRaffleCutoff();
  }
  const quoteInput = () => ({ product: currentProduct().key, variant: inputField(purchase, 'variant').value, quantity: Number(inputField(purchase, 'quantity').value), mode: selectedMode(inputField(purchase, 'mode').value), coupon: inputField(purchase, 'coupon').value, periods: inputField(purchase, 'mode').value === 'installment' ? Number(inputField(purchase, 'periods').value) : 0, donation_amount: currentProduct().kind === 'donation' ? units(inputField(purchase, 'donation').value,currentProduct().precision) : '0', group_id: inputField(purchase, 'mode').value === 'group' ? inputField(purchase, 'group_id').value.trim() : '' });
  function receipt(rows: ReceiptRow[]) { const dl = node('dl', 'shop-receipt'); for (const [key, value, total] of rows) { const row = node('div', total ? 'shop-total' : ''); row.append(node('dt', '', key), node('dd', '', value)); dl.append(row); } return dl; }
  function schedule(parts: ShopInstallment[], order?: ShopOrder) {
    const wrap = node('div', 'shop-table-wrap'), table = node('table', 'shop-schedule'), head = node('thead'), tr = node('tr'); ['付款安排', '本金 / 利息', '应付', '到期 / 状态'].forEach(t => tr.append(node('th', '', t))); head.append(tr); table.append(head); const body = node('tbody');
    parts.forEach(part => { const currency=order?.currency||state.quote?.currency||'CNY',precision=order?.precision??state.quote?.precision??2;const row = node('tr'); row.append(node('td', '', part.title), node('td', '', `${money(part.principal,currency,precision)} / ${money(part.interest,currency,precision)}`), node('td', '', money(part.amount,currency,precision))); const status = node('td', '', isDate(part.paid_at) ? '已支付' : date(part.due_at));
      const firstUnpaid = parts.find(v => !isDate(v.paid_at)); if (order && !isDate(part.paid_at) && firstUnpaid === part && ['pending', 'partially_paid'].includes(order.status)) { const locked = isDate(part.available_at) && Date.parse(part.available_at) > Date.now(); if (locked) status.append(node('p', 'shop-note', `${date(part.available_at)} 起可支付`)); else { const pay = button('支付本期', ''); pay.dataset.shopPay = String(part.index); pay.disabled = paymentOptions(order.currency,part.amount).length===0; status.append(node('br'), pay); } }
      row.append(status); body.append(row);
    }); table.append(body); wrap.append(table); return wrap;
  }
  function renderQuote() {
    const q = required(state.quote), p = currentProduct(), currency=q.currency, places=q.precision, fragment = document.createDocumentFragment(); const rows: ReceiptRow[]=[['购买方式',`${modes[q.mode]} · ${q.quantity} 件`],['商品金额',money(q.original_subtotal||q.subtotal,currency,places)]];if(BigInt(q.discount||'0')>0n)rows.push(['优惠券',`${q.coupon_title} · -${money(q.discount,currency,places)}`]);rows.push(['优惠后商品金额',money(q.subtotal,currency,places)],['运费',money(q.shipping,currency,places)],['总利息',money(q.interest,currency,places)],['全部应付',money(q.total,currency,places)],['本次支付',money(q.pay_now,currency,places),true]);fragment.append(node('h3','',p.title),receipt(rows));
    if (q.requires_address) fragment.append(node('p', 'shop-note', `收货：${required(state.draft).address.name} · ${required(state.draft).address.phone} · ${required(state.draft).address.detail}`));
    if (q.schedule.length > 1) fragment.append(schedule(q.schedule), node('p', 'shop-note', q.interest_description || $('[data-shop-mode-note]').textContent));
    fragment.append(node('p', 'shop-note', $('[data-shop-rules]').textContent));
    $('[data-shop-quote]').replaceChildren(fragment);const channels=paymentOptions(q.currency,q.pay_now);renderChoices($('[data-shop-channel-choices]'),'channel',channels,channels[0]?.[0]||'');
    $('[data-shop-refund-policy]').textContent = catalog().refund_policy; inputField(confirm, 'accepted').checked = false;
    updateConfirmButton();
    message(channels.length ? '' : '当前支付渠道不可用或站内余额不足，请联系商家。', '[data-shop-checkout-message]');
  }
  async function authenticated(): Promise<string> {
    if (retired) throw new Error('页面已暂停，请刷新后继续');
    const session = await client.getSession(true); identity(session);
    if (!session.authenticated || !session.id || !session.csrf_token) { if (couponDialog.open) couponDialog.close(); document.querySelector<HTMLElement>('[data-account-login]')?.click(); throw new Error('请先登录，再继续购买或查看订单。'); }
    return String(session.id);
  }
  function identity(session: ClientSession | null): void {
    const owner = session?.authenticated && session.id ? String(session.id) : '', token = session?.csrf_token || '';
    client.setSession(session);
    if (owner === state.owner && token === expectedToken) return;
    if (state.owner && owner !== state.owner) state.pendingCouponClaim = '';
    generation++; expectedToken = token; state.owner = owner; state.personalSerial++; state.order = null; state.draft = null; state.quote = null;
    state.serial++; state.orderSerial++; state.orderReviewSerial++; ordersSerial++; paymentSerial++; claimBusy = false; state.busy = false;
    clearTimeout(state.poll); state.poll = undefined; clearPersonal(); renderMarketCoupons();
    checkout.close(); orderDialog.close(); $('[data-shop-payment]').replaceChildren(); $('[data-shop-order-detail]').replaceChildren(); $('[data-shop-order-review]').replaceChildren(); $('[data-shop-order-list]').replaceChildren(); $('[data-shop-order-count]').textContent = '';
    if (!owner) $('[data-shop-order-list]').append(empty('登录后查看订单', '使用购买时的账号登录，即可查看付款和交付记录。'));
    if (state.product) updateSelection();
    setBusy(false);
  }
  function readPending(): ShopPurchase | null {
    try { return recovery.read(state.owner)?.draft || null; }
    catch { return null; } // Only used for display. All writes call recovery.read directly and propagate errors.
  }
  function hasRecovery(): boolean {
    try { return !!recovery.read(state.owner); } catch { return !!state.owner; }
  }
  function confirmedPending(pending: ShopPending): string {
    try { return recovery.forget(pending) ? '' : '另有待核对请求，请先查看我的订单。'; }
    catch { return '订单已确认，但浏览器恢复记录未能清理；再次进入时请核对该订单。'; }
  }
  async function createRequest(pending: ShopPending) {
    const epoch = generation;
    try { return await api('/orders', 'POST', pending.draft); }
    catch (error) {
      // These domain errors are returned after Go has checked for an existing matching request.
      // A generic 4xx, transport failure or malformed success cannot prove that no order exists.
      if (active(epoch) && state.owner === pending.owner && error instanceof APIError && [404, 409, 422].includes(error.status)
        && ['shop.conflict', 'shop.disabled', 'shop.not_found', 'shop.stock', 'shop.raffle_total', 'shop.raffle_user_limit', 'shop.raffle_order_limit', 'shop.raffle_closed', 'shop.state', 'shop.ineligible'].includes(error.code)) {
        confirmedPending(pending);
      }
      throw error;
    }
  }
  function displayOrder(order: ShopOrder): void {
    renderOrder(order); renderPayment(null); show(orderDialog); state.polls = 0; schedulePoll();
  }
  purchase.addEventListener('change', event => {
    const target = event.target;
    if (target instanceof HTMLInputElement && ['mode', 'variant', 'quantity', 'donation', 'periods', 'coupon', 'shop_mode_choice', 'shop_variant_choice', 'shop_periods_choice', 'shop_coupon_choice'].includes(target.name)) updateSelection();
  });
  purchase.addEventListener('submit', async event => {
    event.preventDefault(); if (state.busy || retired) return;
    const product = state.product; if (!product) return;
    let epoch = generation;
    setBusy(true); message('', '[data-shop-detail-message]');
    try {
      const owner = await authenticated(); epoch = generation; setBusy(true);
      if (state.product !== product) return;
      const serial = ++state.serial, pending = recovery.read(owner);
      if (pending) {
        const response = await createRequest(pending);
        if (!active(epoch) || serial !== state.serial || owner !== state.owner) return;
        const order = createdShopOrder(response, owner, pending.draft, pending.quote), warning = confirmedPending(pending);
        checkout.close(); displayOrder(order); message('已恢复上次下单结果，请在此订单继续付款。' + warning, '[data-shop-order-message]');
        return;
      }
      if (!await loadPersonal({ authenticated: true, id: owner })) throw new Error('账户余额与购买条件暂未确认，请刷新后重试');
      if (!active(epoch) || serial !== state.serial || state.product !== product) return;
      refreshCouponField(); const input = quoteInput();
      if (raffleClosed(product)) throw new Error(cutoffMessage);
      const result = await api('/quote', 'POST', input, true);
      if (!active(epoch) || serial !== state.serial || state.product !== product) return;
      const quote = shopQuote(result.quote); matchQuote(quote, input, product);
      if (quote.version !== catalog().version) { await loadCatalog(); if (active(epoch)) openProduct(input.product); throw new Error('商品信息或库存已更新，请重新核对订单。'); }
      if (raffleClosed(product)) throw new Error(cutoffMessage);
      state.quote = quote;
      state.draft = { ...input, version: quote.version, request_id: nonce(), accepted: true, address: { name: inputField(purchase, 'recipient').value.trim(), phone: inputField(purchase, 'phone').value.trim(), region: '', detail: addressField(purchase).value.trim(), postal_code: '' } };
      renderQuote(); show(checkout);
    } catch (caught) { const error = failure(caught); if (active(epoch) && error.name !== 'AbortError') message(error.message, '[data-shop-detail-message]', true); }
    finally { if (active(epoch)) setBusy(false); }
  });
  confirm.addEventListener('submit', async event => {
    event.preventDefault(); if (state.busy || retired || !state.draft || !state.quote || !inputField(confirm, 'accepted').checked) return;
    const draft = state.draft, expected = state.quote, owner = state.owner, epoch = generation, channel = inputField(confirm, 'channel').value;
    setBusy(true); message('正在创建订单…', '[data-shop-checkout-message]');
    let confirmed = false;
    try {
      if (await authenticated() !== owner || !active(epoch)) throw new Error('登录状态已变化，请重新核对订单。');
      const saved = recovery.read(owner);
      if (raffleClosed(state.product) && saved?.draft.request_id !== draft.request_id) throw new Error(cutoffMessage);
      if (!channel || !paymentOptions(expected.currency, expected.pay_now).some(c => c[0] === channel)) throw new Error('付款渠道或可用余额已变化，请重新核对');
      const pending = recovery.remember({ owner, draft, quote: expected });
      const response = await createRequest(pending);
      if (!active(epoch) || owner !== state.owner) return;
      const order = createdShopOrder(response, owner, pending.draft, pending.quote || expected);
      confirmed = true; const warning = confirmedPending(pending);
      state.draft = null; state.quote = null; checkout.close(); displayOrder(order);
      message('订单已创建。' + warning, '[data-shop-order-message]');
      try { await payOrder(0, channel); }
      catch (caught) { if (active(epoch)) message('订单已创建，付款结果尚待核对：' + failure(caught).message, '[data-shop-order-message]', true); }
      void loadCatalog().catch(() => { /* The confirmed create/payment result is retained. */ });
    } catch (caught) {
      const error = failure(caught);
      if (active(epoch) && error.name !== 'AbortError') message((confirmed ? '订单已创建。' : '') + error.message, orderDialog.open ? '[data-shop-order-message]' : '[data-shop-checkout-message]', true);
    } finally { if (active(epoch)) setBusy(false); }
  });
  function reviewStars(rating: number) {
    const stars = node('span','shop-review-stars'); stars.setAttribute('aria-label',`${rating} 星，满分 5 星`);
    for (let i=1;i<=5;i++) { const star = icon('star'); star.dataset.filled = String(i<=rating); stars.append(star); }
    return stars;
  }
  function reviewCard(r: ShopPublicReview) {
    const card = node('article','shop-review-card'), head = node('header'), avatar = node('span','shop-review-avatar',r.anonymous ? '匿' : Array.from(r.author || '买')[0]), meta = node('div','shop-review-meta'), time = node('time','',date(r.created_at)); time.dateTime=r.created_at;
    meta.append(node('b','',r.author),node('span','','已购评价')); head.append(avatar,meta,reviewStars(r.rating));
    card.append(head,node('p','shop-review-content',r.content)); const detail=node('div','shop-review-detail');detail.append(time); if(r.variant)detail.append(node('span','',r.variant));card.append(detail);
    if (r.reply) { const reply=node('div','shop-review-reply');reply.append(node('b','','商家回复'),node('p','',r.reply),node('time','',date(r.replied_at)));card.append(reply); }
    return card;
  }
  async function loadReviews(): Promise<void> {
    if (!state.product) return;
    const serial=++state.reviewSerial, key=currentProduct().key, status=$('[data-shop-review-status]'); status.hidden=false;status.textContent='正在读取评价…';$('[data-shop-review-list]').setAttribute('aria-busy','true');
    try {
      const page = state.reviewPage, epoch = generation;
      const result=shopReviews(await api(`/products/${encodeURIComponent(key)}/reviews?page=${page}&rating=${state.reviewRating}&sort=${state.reviewSort}&replied=${state.reviewReplied}`,'GET',undefined,true), page);
      if (!active(epoch)) return;
      if(serial!==state.reviewSerial || key!==state.product?.key)return;
      if(result.page>Math.max(1,Math.ceil(result.total/result.page_size))){state.reviewPage=Math.max(1,Math.ceil(result.total/result.page_size));return loadReviews();}
      const summary=result.summary, host=$('[data-shop-review-summary]'), score=node('div','shop-review-score'), bars=node('div','shop-review-bars');
      $('[data-shop-review-count]').textContent=`（${summary.count}）`;
      score.append(node('strong','',summary.count?summary.average.toFixed(1):'—'),reviewStars(summary.count?Math.round(summary.average):0),node('small','',summary.count?`${Math.round(summary.positive/summary.count*100)}% 的买家给出 4 星及以上`:'暂无评分'));
      for(let i=5;i>=1;i--){const row=node('div'),bar=node('progress');bar.max=Math.max(1,summary.count);bar.value=summary.distribution[i-1] || 0;bar.setAttribute('aria-label',`${i} 星：${summary.distribution[i-1]} 条`);row.append(node('span','',`${i} 星`),bar,node('small','',String(summary.distribution[i-1])));bars.append(row);}
      host.replaceChildren(score,bars);
      const filters=$('[data-shop-review-filters]'); filters.replaceChildren(...[0,5,4,3,2,1].map(n=>{const b=button(n?`${n} 星（${summary.distribution[n-1]}）`:`全部（${summary.count}）`,'');b.dataset.shopReviewRating=String(n);b.setAttribute('aria-pressed',String(state.reviewRating===n));return b;}));
      const replied=button('商家已回复','');replied.dataset.shopReviewReplied='';replied.setAttribute('aria-pressed',String(state.reviewReplied));filters.append(replied);
      const sorts=node('div','shop-review-sort');sorts.setAttribute('role','group');sorts.setAttribute('aria-label','评价排序');for(const [key,label] of [['latest','最新评价'],['high','评分从高到低'],['low','评分从低到高']]){const b=button(label || '','');b.dataset.shopReviewSort=key;b.setAttribute('aria-pressed',String(state.reviewSort===key));sorts.append(b);}filters.append(sorts);
      $('[data-shop-review-list]').replaceChildren(...(result.reviews.length?result.reviews.map(reviewCard):[empty(summary.count?'没有符合条件的评价':'还没有买家评价',summary.count?'可以切换星级，查看其他评价。':'订单完成后，可以在我的订单中分享使用感受。')]));
      const pagination=$('[data-shop-review-pagination]');pagination.replaceChildren();const pages=Math.max(1,Math.ceil(result.total/result.page_size));
      if(pages>1){for(const [label,value] of [['上一页',result.page-1],['下一页',result.page+1]] as const){const b=button(label || '','');b.dataset.shopReviewPage=String(value);b.disabled=value<1||value>pages;pagination.append(b);}pagination.append(node('span','',`${result.page} / ${pages}`));}
      const stats=state.stats[key]||emptyStats();state.stats[key]={...stats,...summary};renderProductRating(state.product);status.hidden=true;
    }catch(caught){const error = failure(caught);if(serial===state.reviewSerial){status.textContent=`${error.message} `;const retry=button('重新加载','shop-text-button');retry.addEventListener('click',loadReviews);status.append(retry);}}
    finally{if(serial===state.reviewSerial)$('[data-shop-review-list]').setAttribute('aria-busy','false');}
  }
  async function loadOrderReview(o: ShopOrder) {
    const host=$('[data-shop-order-review]'), owner=state.owner, id=o.id, epoch=generation;
    // Keep a buyer's draft when an unrelated order refresh finishes.
    if(host.dataset.order===id && host.querySelector('form') && o.status==='completed')return;
    const serial=state.orderReviewSerial=(state.orderReviewSerial||0)+1;
    host.dataset.order=id;host.replaceChildren(node('p','shop-note','正在读取评价信息…'));
    try{
      const result=shopOrderReview(await api(`/orders/${encodeURIComponent(id)}/review`), o);
      if(!active(epoch)||state.order?.id!==id||state.owner!==owner||serial!==state.orderReviewSerial)return;
      host.replaceChildren(node('h3','','商品评价'));
      if(result.review){host.append(reviewCard(result.review));if(result.note)host.append(node('p','shop-note',result.note));return;}
      if(!result.eligible){host.append(node('p','shop-note',result.note));return;}
      const form=node('form','shop-review-form'),fieldset=node('fieldset'),legend=node('legend','','商品评分'),rating=node('div','shop-rating-input'),hint=node('output','shop-rating-hint','请选择评分');fieldset.append(legend);
      for(let n=1;n<=5;n++){const label=node('label'),input=node('input');input.type='radio';input.name='rating';input.value=String(n);input.required=true;input.setAttribute('aria-label',`${n} 星`);label.append(input,icon('star'));rating.append(label);}
      rating.addEventListener('change',()=>{const value=Number(new FormData(form).get('rating'));rating.querySelectorAll('label').forEach((el,index)=>el.dataset.filled=String(index<value));hint.textContent=['','很不满意','不满意','一般','满意','很满意'][value] || '';});
      fieldset.append(rating,hint);const label=node('label','','使用感受'),textarea=node('textarea');textarea.name='content';textarea.required=true;textarea.minLength=1;textarea.maxLength=2000;textarea.rows=4;textarea.placeholder='写下商品、交付或使用体验，帮助其他买家了解商品。';label.append(textarea);fieldset.append(label);
      const footer=node('div','shop-review-form-footer'),anonymous=node('label','shop-review-anonymous'),check=node('input'),count=node('output','','0 / 2000');check.type='checkbox';check.name='anonymous';anonymous.append(check,document.createTextNode('匿名评价'));footer.append(anonymous,count);textarea.addEventListener('input',()=>count.textContent=`${textarea.value.length} / 2000`);fieldset.append(footer);
      const submit=node('button','ui-button ui-button-primary','提交评价');submit.type='submit';fieldset.append(submit);const status=node('p','shop-note');status.setAttribute('role','status');form.append(fieldset,status);host.append(form,node('p','shop-note','每笔订单可评价一次。公开昵称会作部分隐藏处理，请勿填写联系方式或订单密钥。'));
      form.addEventListener('submit',async event=>{event.preventDefault();if(fieldset.disabled||state.busy)return;const input={rating:Number(new FormData(form).get('rating')),content:textarea.value.trim(),anonymous:check.checked};if(!input.content){status.textContent='请填写评价内容。';return;}fieldset.disabled=true;status.textContent='正在提交评价…';
        try{const result=await api(`/orders/${encodeURIComponent(id)}/review`,'POST',input);if(!active(epoch)||state.order?.id!==id||state.owner!==owner)return;const review=ownShopReview(result.review,o);if(review.rating!==input.rating||review.content!==input.content||review.anonymous!==input.anonymous)throw new Error('评价回执未匹配，请先刷新核对已有评价');host.replaceChildren(node('h3','','商品评价'),reviewCard(review),node('p','shop-note','评价已提交'));if(state.product?.key===o.product)void loadReviews();void loadCatalog().catch(()=>{});}
        catch(caught){if(!active(epoch)||state.order?.id!==id||state.owner!==owner)return;status.textContent=failure(caught).message;fieldset.disabled=false;}
      });
    }catch(caught){const error = failure(caught);if(state.order?.id!==id||state.owner!==owner||serial!==state.orderReviewSerial)return;host.replaceChildren(node('p','shop-note',error.message));const retry=button('重新读取评价');retry.addEventListener('click',()=>loadOrderReview(o));host.append(retry);}
  }
  async function loadOrders() {
    const owner = await authenticated(), epoch = generation, serial = ++ordersSerial; const result = await api('/orders'); if (!active(epoch) || serial !== ordersSerial || owner !== state.owner) return; const orders = shopOrderList(result, owner); if (result.csrf_token !== expectedToken) throw new Error('订单会话已变化，请刷新后核对');
    $('[data-shop-order-count]').textContent = orders.length ? ` (${orders.length})` : '';
    $('[data-shop-order-list]').replaceChildren(...(orders.length ? orders.map(o => { const card = node('article', 'shop-order-card'), copy = node('div'); copy.append(node('h3', '', o.title), tag(statuses[o.status] || o.status, true), node('p', '', `${date(o.created_at)} · ${o.id}`)); const open = button('查看订单'); open.dataset.shopOrder = o.id; const actions=node('div','shop-order-card-actions');actions.append(open);if(o.status==='completed'){const review=button('评价与回复','ui-button ui-button-primary');review.dataset.shopOrder=o.id;review.dataset.shopOpenReview='';actions.append(review);}card.append(copy, node('strong', '', money(o.total,o.currency,o.precision)), actions); return card; }) : [empty('还没有购买记录', '创建订单后可在此查看付款与交付记录。')]));
  }
  function updateConfirmButton() {
    const pendingRetry = state.draft && readPending()?.request_id === state.draft.request_id;
    const closed = raffleClosed(state.product) && !pendingRetry;
    const q = state.quote;
    const quotaExceeded = state.product?.kind === 'raffle' && q && !pendingRetry && q.quantity > raffleCapacity(state.product).max;
    $button('[data-shop-confirm-button]').disabled = state.busy || closed || quotaExceeded || Boolean(q && !paymentOptions(q.currency, q.pay_now).length);
    if (closed && checkout.open && !state.busy) message(cutoffMessage, '[data-shop-checkout-message]');
    else if (quotaExceeded && checkout.open && !state.busy) message('剩余额度已变化，请关闭确认框后重新选择数量。', '[data-shop-checkout-message]');
  }
  function applyRaffleCutoff() {
    if (!raffleClosed(state.product)) return;
    // A previously submitted request can still be replayed to recover its order.
    const retry = hasRecovery();
    required(purchase.querySelector('fieldset')).disabled = true;
    const buy = $button('[data-shop-buy]');
    buy.disabled = state.busy || !retry;
    buy.textContent = retry ? '查看上次下单结果' : '参与已截止';
  }
  function scheduleRaffleClock() {
    clearTimeout(raffleTimer);
    if (retired || !state.catalog || document.hidden) return;
    const now = shopNow();
    let delay = Infinity;
    if (state.product) {
      const deadline = raffleDeadline(state.product);
      if (!Number.isFinite(deadline)) return;
      const phase = root.querySelector<HTMLElement>('[data-shop-raffle-phase]');
      if (phase) phase.textContent = rafflePhase(deadline, now, state.viewRound, state.viewRoundAt);
      updateRaffleCountdown(now);
      if (deadline <= now) { applyRaffleCutoff(); updateConfirmButton(); return; }
      delay = (deadline - now) % 1000 || 1000;
    } else if (root.dataset.shopView === 'catalog') {
      for (const p of catalog().products) {
        const remaining = raffleDeadline(p) - now;
        if (remaining > 0) delay = Math.min(delay, remaining);
      }
    }
    if (Number.isFinite(delay)) raffleTimer = setTimeout(() => {
      if (root.dataset.shopView === 'catalog') renderProducts();
      scheduleRaffleClock();
    }, Math.min(Math.ceil(delay), 2147483647));
  }
  async function refreshRaffle(button: HTMLButtonElement) {
    const product = state.product, epoch = generation; if (!product) return;
    button.disabled = true;
    button.textContent = '正在刷新…';
    try {
      // Refresh availability while retaining the price/version that was quoted.
      const result = shopCatalogResponse(await api('/catalog', 'GET', undefined, true));
      if (!active(epoch) || state.product !== product || !button.isConnected) return;
      const focused = document.activeElement === button || document.activeElement === document.body;
      const current = result.catalog.products.find(p => p.key === product.key);
      const oldCampaign = campaignFor(product), newCampaign = result.catalog.raffle_campaigns?.find(c => c.key === required(product.raffle).campaign);
      if (!current || JSON.stringify(current.raffle) !== JSON.stringify(product.raffle) || JSON.stringify(oldCampaign) !== JSON.stringify(newCampaign)) {
        const link = node('a', 'shop-raffle-refresh', current ? '查看新一期奖池' : '返回商城');
        link.href = current ? productURL(product.key) : '/shop/';
        button.replaceWith(link);
        if (focused && !checkout.open && !orderDialog.open) link.focus({ preventScroll: true });
        message(current ? '商家已更新奖池，请查看新一期的规则。' : '商品已下架。', '[data-shop-detail-message]');
        return;
      }
      const sampledAt = Date.parse(result.server_time);
      if (Number.isFinite(sampledAt)) serverEpoch = sampledAt - performance.now();
      required(catalog().products.find(p => p.key === product.key)).stock = current.stock;
      state.raffleQuotas = result.raffle_quotas || state.raffleQuotas;
      state.personalRaffleQuotas = {};
      state.personalSerial = (state.personalSerial || 0) + 1;
      renderRafflePool(product.raffle, result.raffle_rounds?.[product.key], sampledAt);
      updateSelection();
      if (state.owner) await loadPersonal({ authenticated: true, id: state.owner });
      updateConfirmButton();
      scheduleRaffleClock();
      if (focused && !checkout.open && !orderDialog.open) $('[data-shop-raffle-refresh]')?.focus({ preventScroll: true });
    } catch (caught) { const error = failure(caught);
      if (active(epoch) && error.name !== 'AbortError') message(error.message, '[data-shop-detail-message]', true);
    } finally {
      if (active(epoch) && button.isConnected) { button.disabled = false; button.textContent = '刷新公示'; }
    }
  }
  function renderRafflePool(plan: ShopRafflePlan | null, counts = state.raffleRounds[state.product?.key || ''], sampledAt = state.catalogReadAt) {
    const pool = $('[data-shop-raffle-pool]');
    if (!pool) return;
    pool.replaceChildren();
    pool.hidden = !plan;
    if (!plan) return;
    const instant = plan.mode === 'instant';
    const tiers = plan.tiers?.length ? plan.tiers : [plan];
    state.viewRound = counts;
    state.viewRoundAt = sampledAt;
    const header = node('div', 'shop-raffle-heading');
    header.append(node('h2', '', '本期奖池'));
    pool.append(header);
    if (instant) {
      header.append(node('span', 'shop-note', '付款即开 · 每码最多中一项'));
    } else {
      const phase = node('strong', 'shop-raffle-phase', rafflePhase(Date.parse(plan.draw_at), shopNow(), counts, state.viewRoundAt));
      phase.dataset.shopRafflePhase = '';
      header.append(phase);
      const countdown = node('section', 'shop-raffle-countdown');
      countdown.setAttribute('aria-label', '活动倒计时');
      countdown.setAttribute('role', 'timer');
      countdown.setAttribute('aria-live', 'off');
      const caption = node('div', 'shop-raffle-countdown-title');
      caption.append(icon('clock-3'), node('span', '', '活动倒计时'));
      const digits = node('div', 'shop-raffle-countdown-digits');
      for (const [key, label] of [['days', '天'], ['hours', '时'], ['minutes', '分'], ['seconds', '秒']]) {
        const unit = node('span', 'shop-raffle-countdown-unit');
        const value = node('b', '', '00'); value.dataset.raffleUnit = key;
        unit.append(value, node('small', '', label)); digits.append(unit);
        if (key !== 'seconds') { const separator = node('span', 'shop-raffle-countdown-separator', ':'); separator.setAttribute('aria-hidden', 'true'); digits.append(separator); }
      }
      const gift = node('span', 'shop-raffle-gift'); gift.setAttribute('aria-hidden', 'true');
      gift.append(node('i', 'shop-raffle-gift-bow'), node('i', 'shop-raffle-gift-lid'), node('i', 'shop-raffle-gift-box'));
      countdown.append(caption, digits, gift);
      countdown.dataset.raffleDeadline = String(Date.parse(plan.draw_at));
      countdown.dataset.shopRaffleCountdown = '';
      pool.append(countdown);
    }
    if (!instant) updateRaffleCountdown(shopNow());
    if (!instant) {
      pool.append(node('p', 'shop-note', `${date(plan.draw_at)} 截止并开奖 · 共 ${plan.winners} 个名额`));
      if (counts) {
        const progress = node('div', 'shop-raffle-progress');
        const totals = `有效参与 ${counts.entries} 张${counts.drawn ? ` · 已中奖 ${counts.winners} 张` : ''}`;
        progress.append(node('strong', '', totals));
        pool.append(progress);
      }
    }
    const rows = tiers.map<ReceiptRow>(tier => {
      if (instant) return [tier.prize, `${(tier.win_bps || 0) / 100}%`];
      const quota = `${tier.winners} 名`;
      return [tier.prize, counts?.drawn ? `已中 ${Object.hasOwn(counts.by_prize || {}, tier.prize) ? counts.by_prize[tier.prize] : 0} / ${quota}` : quota];
    });
    if (instant && plan.win_bps < 10000) rows.push(['未中奖', `${(10000 - plan.win_bps) / 100}%`]);
    pool.append(receipt(rows));
    if (!instant && tiers.length > 1) pool.append(node('p', 'shop-note', '参与码不足时，按上方奖项顺序分配名额。'));
    const refresh = button('刷新公示', 'shop-raffle-refresh');
    refresh.dataset.shopRaffleRefresh = '';
    refresh.addEventListener('click', () => refreshRaffle(refresh));
    const product = state.product, personalQuota = state.personalRaffleQuotas[product?.key || ''], publicQuota = personalQuota || state.raffleQuotas[product?.key || ''];
    if (product && (product.raffle?.campaign || [product.raffle?.ticket_limit, product.raffle?.user_limit, product.raffle?.order_limit].some(value => Number(value) > 0))) {
      const campaign = campaignFor(product);
      const line = (left: number | undefined, limit: number | undefined) => Number(limit) > 0 ? `共 ${limit} 张${Number(left) >= 0 ? ` · 剩余 ${left} 张` : ''}` : '不限量';
      const personalLine = (left: number | undefined, limit: number | undefined) => `每人 ${limit} 张${personalQuota ? ` · 剩余 ${left} 张` : ' · 登录后查看剩余'}`;
      const quotaRows: ReceiptRow[] = [['本商品券码', line(publicQuota?.product_left, required(product.raffle).ticket_limit)]];
      if (campaign) quotaRows.push([`${campaign.title} · 总量`, line(publicQuota?.campaign_left, campaign.ticket_limit)]);
      if (Number(required(product.raffle).user_limit) > 0) quotaRows.push(['本商品每人限购', personalLine(personalQuota?.user_product_left, required(product.raffle).user_limit)]);
      if (Number(campaign?.user_limit) > 0) quotaRows.push(['活动每人限购', personalLine(personalQuota?.user_campaign_left, campaign?.user_limit)]);
      if (Number(required(product.raffle).order_limit) > 0) quotaRows.push(['每单上限', `${required(product.raffle).order_limit} 张`]);
      const capacity = raffleCapacity(product);
      quotaRows.push([state.owner ? '本次可购' : '登录前单次上限', raffleClosed(product) ? '参与已截止' : capacity.max ? `${capacity.max} 张` : capacity.personalLimit ? '个人额度已用完' : '抽奖码已售罄']);
      const box = node('section', 'shop-raffle-quota'), heading = node('div', 'shop-raffle-heading');
      heading.append(node('h3', '', '券码数量与限购'), refresh);
      box.append(heading, receipt(quotaRows), node('p', 'shop-note', '待付款订单占用额度；取消或超时未付款后释放。已发放抽奖码仍计入限额。'));
      pool.append(box);
    } else header.append(refresh);
  }
  function updateRaffleCountdown(now: number) {
    const countdown = root.querySelector<HTMLElement>('[data-shop-raffle-countdown]');
    if (!countdown) return;
    const values = raffleCountdown(Number(countdown.dataset.raffleDeadline), now);
    countdown.dataset.ended = String(now >= Number(countdown.dataset.raffleDeadline));
    for (const [key, value] of Object.entries(values)) {
      const digit = countdown.querySelector(`[data-raffle-unit="${key}"]`);
      if (digit) digit.textContent = String(value).padStart(2, '0');
    }
  }
  function renderTickets(order: ShopOrder, content: DocumentFragment) {
    const tickets = order.tickets;
    const winners = tickets.filter(t => t.status === 'won');
    const pending = tickets.filter(t => t.status === 'pending').length;
    content.append(node('h3', '', '我的抽奖码'), node('p', 'shop-note', `共 ${tickets.length} 张 · 中奖 ${winners.length} 张 · 未中奖 ${tickets.length - winners.length - pending} 张 · 待开 ${pending} 张`));
    const list = node('div');
    if (winners.length && winners.length < tickets.length) {
      const label = node('label', 'shop-consent');
      const filter = node('input'); filter.type = 'checkbox';
      filter.addEventListener('change', () => {
        for (const card of list.children) if (card instanceof HTMLElement) card.hidden = filter.checked && card.dataset.status !== 'won';
      });
      label.append(filter, document.createTextNode('只看中奖码')); content.append(label);
    }
    for (const ticket of tickets) {
      const card = node('section', 'shop-delivery');
      card.dataset.status = ticket.status;
      const title = { won: '已中奖', lost: '本次未中奖', pending: '等待开奖', cancelled: '已取消' }[ticket.status];
      card.append(node('h3', '', title), node('p', '', `抽奖码：${ticket.id}`));
      if (ticket.status === 'won') card.append(node('p', '', `所得奖品：${ticket.prize}`));
      card.append(node('p', 'shop-note', `开奖时间：${date(isDate(ticket.drawn_at) ? ticket.drawn_at : ticket.draw_at)}`));
      if (ticket.delivery) card.append(node('h3', '', '奖品交付'), node('pre', '', ticket.delivery), node('p', 'shop-note', `交付时间：${date(ticket.delivered_at)}`));
      else if (ticket.status === 'won' && order.status === 'completed') card.append(node('p', 'shop-note', '等待商家交付奖品，交付信息会显示在此处。'));
      list.append(card);
    }
    content.append(list);
  }
  function renderOrder(o: ShopOrder) {
    state.order = o; const content = document.createDocumentFragment();
    content.append(node('h3', '', o.title), tag(statuses[o.status] || o.status, true), receipt([['订单编号', o.id], ['购买方案', `${modes[o.mode]} · ${o.quantity} 件`], ['订单总额', money(o.total,o.currency,o.precision)], ['已支付', money(o.paid_amount,o.currency,o.precision)], ['创建时间', date(o.created_at)]]));
    if (o.status === 'pending') content.append(node('p', 'shop-note', `请于 ${date(o.expires_at)} 前完成付款，过期将释放库存。`));
    if (o.group_id) { const box = node('div', 'shop-delivery'); box.append(node('h3', '', '邀请朋友参团'), node('p', '', `团号：${o.group_id}`)); const share = button('复制参团链接'); share.addEventListener('click', async () => { try { await navigator.clipboard.writeText(`${location.origin}/shop/?product=${encodeURIComponent(o.product)}&group=${encodeURIComponent(o.group_id)}`); share.textContent = '已复制'; } catch { message('复制不可用，请手动复制上方团号。', '[data-shop-order-message]'); } }); box.append(share); content.append(box); }
    content.append(schedule(o.schedule || [], o));
    const orderChannels=paymentOptions(o.currency,o.schedule.find(part=>!isDate(part.paid_at))?.amount||'0');
    if (['pending', 'partially_paid'].includes(o.status)) { const field=node('fieldset','shop-choice-field shop-order-channel-field'),choices=node('div','shop-choice-list');field.append(node('legend','','付款方式'));choices.dataset.shopOrderChannel='';field.append(choices);renderChoices(choices,'order_channel',orderChannels,orderChannels[0]?.[0]||'');content.append(field); }
    if (o.address?.name) content.append(receipt([['收货人', `${o.address.name} · ${o.address.phone}`], ['收货地址', `${o.address.region || ''} ${o.address.detail}`]]));
    if (o.carrier) content.append(receipt([['物流公司', o.carrier], ['运单号', o.tracking]]));
    for (const d of o.deliveries || []) { const el = node('section', 'shop-delivery'); el.append(node('h3', '', `${d.title} × ${d.quantity}`), node('pre', '', d.content)); const url = d.kind === 'virtual' && safeURL(d.content); if (url && /^https:\/\//.test(d.content)) { const a = node('a', 'ui-button ui-button-secondary', '打开交付链接'); a.href = url; a.target = '_blank'; a.rel = 'noopener noreferrer'; el.append(a); } content.append(el); }
    if (o.tickets?.length) renderTickets(o, content);
    if (o.status === 'refund_requested') content.append(node('p', 'shop-message', '退款申请已提交，商家审核期间订单暂停后续付款与交付。'));
    if (o.status === 'refund_required') content.append(node('p', 'shop-message', '此订单已进入退款处理，请联系商家核对。实际退款到账以支付渠道为准。'));
    if (o.refund_reference) content.append(receipt([['退款凭证', o.refund_reference], ['处理说明', o.refund_note]]));
    const actions = node('div', 'shop-order-actions');
    if (o.status === 'shipped') { const received = button('确认已收货'); received.dataset.shopReceived = ''; actions.append(received); }
    if (BigInt(o.paid_amount || '0') > 0n && !['refund_requested','refund_required', 'refunded'].includes(o.status)) { const refund = button('申请售后退款'); refund.dataset.shopRefund = ''; actions.append(refund); }
    content.append(actions); $('[data-shop-order-detail]').replaceChildren(content); $button('[data-shop-order-cancel]').hidden = o.status !== 'pending'; loadOrderReview(o);
  }
  function renderPayment(payment?: PaymentOrder | null): void {
    const container = $('[data-shop-payment]'); container.replaceChildren(); const serial = ++paymentSerial;
    const order = state.order;
    if (!order || !payment || payment.status !== 'pending' || !['pending', 'partially_paid'].includes(order.status) || Date.parse(payment.expires_at) <= shopNow()) return;
    const part = order.schedule.find(v => !isDate(v.paid_at)); if (!part) return;
    shopPayment(payment, order, part.index);
    const precision = order.precision, amount = paymentUnits(payment.amount, precision), data = payment.checkout;
    const url = safeURL(data.url || ''), owner = state.owner, epoch = generation;
    const usable = () => active(epoch) && serial === paymentSerial && state.owner === owner && state.order?.id === order.id && ['pending', 'partially_paid'].includes(state.order.status) && !isDate(state.order.schedule[part.index]?.paid_at) && Date.parse(payment.expires_at) > shopNow();
    container.append(node('p', 'shop-note', '本次付款：' + money(amount, payment.currency, precision) + '。付款完成后点击“同步支付与进度”。'));
    if (payment.qr_url === '/api/v1/social/billing/orders/' + payment.id + '/qr') { const img = node('img'); img.src = payment.qr_url; img.alt = '本订单支付二维码'; container.append(img); }
    if (url && data.method === 'POST') {
      const form = node('form'); form.method = 'POST'; form.action = url;
      for (const [name, value] of Object.entries(data.fields || {})) { const input = node('input'); input.type = 'hidden'; input.name = name; input.value = value; form.append(input); }
      const submit = node('button', 'ui-button ui-button-primary', '前往支付平台'); submit.type = 'submit'; form.append(submit);
      form.addEventListener('submit', event => { if (!usable()) { event.preventDefault(); message('支付链接已失效，请重新读取订单。', '[data-shop-order-message]', true); } }); container.append(form);
    } else if (url && (!data.method || data.method === 'GET')) {
      const link = node('a', 'ui-button ui-button-primary', '前往支付平台'); link.href = url; link.target = '_blank'; link.rel = 'noopener noreferrer';
      link.addEventListener('click', event => { if (!usable()) { event.preventDefault(); message('支付链接已失效，请重新读取订单。', '[data-shop-order-message]', true); } }); container.append(link);
    }
  }
  function currentPayment(order: ShopOrder, payments: (PaymentOrder & { index: number })[]): PaymentOrder | undefined {
    const part = order.schedule.find(v => !isDate(v.paid_at)); if (!part || !['pending', 'partially_paid'].includes(order.status)) return;
    return payments.filter(p => p.index === part.index && p.status === 'pending' && Date.parse(p.expires_at) > shopNow()).sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at))[0];
  }
  async function openOrder(id: string): Promise<void> {
    shopOrderID(id); const owner = await authenticated(), epoch = generation, serial = ++state.orderSerial;
    const result = shopOrderResponse(await api('/orders/' + encodeURIComponent(id)), owner, id);
    if (!active(epoch) || owner !== state.owner || serial !== state.orderSerial) return;
    renderOrder(result.order); renderPayment(currentPayment(result.order, result.payments)); message('', '[data-shop-order-message]'); show(orderDialog); state.polls = 0; schedulePoll();
  }
  async function payOrder(index: number, channel?: string): Promise<void> {
    const order = state.order; if (!order || retired) return;
    const epoch = generation, owner = state.owner, serial = ++state.orderSerial;
    if (await authenticated() !== owner || !active(epoch) || state.order?.id !== order.id) return;
    const part = order.schedule[index], first = order.schedule.find(v => !isDate(v.paid_at));
    if (!part || part !== first || !['pending', 'partially_paid'].includes(order.status) || isDate(part.available_at) && Date.parse(part.available_at) > shopNow()) throw new Error('本期付款状态已变化，请同步订单后核对');
    channel ||= root.querySelector<HTMLInputElement>('[data-shop-order-channel] input:checked')?.value;
    if (!channel || !paymentOptions(order.currency, part.amount).some(v => v[0] === channel)) throw new Error('暂时没有可用的支付渠道或余额，请重新核对。');
    const result = await api('/orders/' + encodeURIComponent(order.id) + '/pay', 'POST', { index, channel, accepted: true });
    if (!active(epoch) || serial !== state.orderSerial || state.order?.id !== order.id) return;
    const next = shopOrderUpdate(result.order, order), nextPart = required(next.schedule[index]);
    const payment = result.payment === undefined ? undefined : shopPayment(result.payment, next, index);
    if (channel === 'wallet' && (!isDate(nextPart.paid_at) || BigInt(next.paid_amount) < BigInt(order.paid_amount) + BigInt(part.amount)) || !payment && !isDate(nextPart.paid_at)) throw new Error('付款回执尚未确认，请同步当前订单，勿重复付款');
    renderOrder(next); renderPayment(payment);
    message(isDate(nextPart.paid_at) ? '本期付款已确认。' : '支付单已生成，请使用当前订单的支付入口完成付款。', '[data-shop-order-message]');
    const refreshed = await loadPersonal({ authenticated: true, id: owner });
    if (!active(epoch) || state.order?.id !== order.id) return;
    if (!refreshed) message((isDate(nextPart.paid_at) ? '本期付款已确认。' : '支付单已生成。') + '账户余额暂时无法刷新，请稍后核对。', '[data-shop-order-message]');
    state.polls = 0; schedulePoll();
  }
  async function refreshOrder(sync = false): Promise<void> {
    const previous = state.order; if (!previous || retired) return;
    const epoch = generation, serial = ++state.orderSerial;
    const raw = await api('/orders/' + encodeURIComponent(previous.id) + (sync ? '/sync' : ''), sync ? 'POST' : 'GET', sync ? {} : undefined);
    if (!active(epoch) || serial !== state.orderSerial || state.order?.id !== previous.id) return;
    const result = sync ? { order: shopOrderUpdate(raw.order, previous), payments: [] } : shopOrderResponse(raw, previous.user_id, previous.id);
    shopOrderUpdate(result.order, previous);
    if (sync && typeof raw.sync_pending !== 'boolean') throw new Error('同步回执未确认，请重新读取当前订单');
    renderOrder(result.order); renderPayment(sync ? null : currentPayment(result.order, result.payments));
    if (sync) message(raw.sync_pending ? '支付渠道仍在确认，订单已保留，请稍后再同步。' : '订单同步已完成。', '[data-shop-order-message]');
  }
  function schedulePoll(): void {
    clearTimeout(state.poll);
    if (retired || !orderDialog.open || !state.order || document.hidden || state.polls >= 20 || !['pending', 'partially_paid', 'awaiting_group', 'awaiting_draw', 'awaiting_release'].includes(state.order.status)) return;
    const epoch = generation;
    state.poll = window.setTimeout(async () => { if (!active(epoch)) return; if (!state.busy) { state.polls++; try { await refreshOrder(); } catch {} } if (active(epoch)) schedulePoll(); }, 6000);
  }
  async function mutateOrder(operation: 'cancel' | 'received' | 'refund', body: Record<string, string> = {}): Promise<void> {
    const previous = state.order; if (!previous || retired) return;
    const epoch = generation, serial = ++state.orderSerial;
    if (await authenticated() !== previous.user_id || !active(epoch) || state.order?.id !== previous.id) return;
    const result = await api('/orders/' + encodeURIComponent(previous.id) + '/' + operation, 'POST', body);
    if (!active(epoch) || serial !== state.orderSerial || state.order?.id !== previous.id) return;
    const next = shopOrderUpdate(result.order, previous), expected = operation === 'cancel' ? 'cancelled' : operation === 'received' ? 'completed' : 'refund_requested';
    if (next.status !== expected) throw new Error('订单操作回执未确认，请同步原订单核对');
    renderOrder(next); renderPayment(null); message(operation === 'cancel' ? '订单已取消。' : operation === 'received' ? '收货已确认。' : '退款申请已提交。', '[data-shop-order-message]');
    void loadCatalog().catch(() => {});
  }
  async function action(buttonEl: HTMLButtonElement, fn: () => Promise<unknown>): Promise<void> {
    if (state.busy || retired) return; const epoch = generation;
    setBusy(true); buttonEl.disabled = true; message('', '[data-shop-order-message]');
    try { await fn(); }
    catch (caught) { const error = failure(caught); if (active(epoch) && error.name !== 'AbortError') message(error.message, '[data-shop-order-message]', true); }
    finally { if (active(epoch)) { setBusy(false); buttonEl.disabled = false; } }
  }
  root.addEventListener('click', event => {
    const target = event.target; if (retired || !(target instanceof Element)) return;
    const category=target.closest<HTMLElement>('[data-shop-category]');if(category){state.category=(category.dataset.shopCategory || '');renderProducts();}
    const currency=target.closest<HTMLElement>('[data-shop-currency]');if(currency){state.currency=(currency.dataset.shopCurrency || '');renderProducts();}
    const shortcut=target.closest<HTMLElement>('[data-shop-shortcut]');if(shortcut){if((shortcut.dataset.shopShortcut || '')==='coupon')showMarketCoupons();else{state.filter=(shortcut.dataset.shopShortcut || '')==='kind'?(shortcut.dataset.value || ''):'all';state.modeFilter=(shortcut.dataset.shopShortcut || '')==='mode'?(shortcut.dataset.value || ''):'';renderProducts();}}
    const featured=target.closest<HTMLElement>('[data-shop-featured] button[data-shop-featured]');if(featured){state.featuredIndex=Number((featured.dataset.shopFeatured || ''));renderFeatured();}
    if(target.closest<HTMLElement>('[data-shop-featured-prev]')){state.featuredIndex--;renderFeatured();}if(target.closest<HTMLElement>('[data-shop-featured-next]')){state.featuredIndex++;renderFeatured();}
    const filters=target.closest<HTMLElement>('[data-shop-filter-toggle]');if(filters){const panel=$('#shop-market-filter-panel');panel.hidden=!panel.hidden;filters.setAttribute('aria-expanded',String(!panel.hidden));}
    const density=target.closest<HTMLElement>('[data-shop-density]');if(density){const compact=density.getAttribute('aria-pressed')!=='true';density.setAttribute('aria-pressed',String(compact));density.setAttribute('aria-label',compact?'切换为大图商品网格':'切换为紧凑商品网格');$('[data-shop-products]').dataset.compact=String(compact);}
    if(target.closest<HTMLElement>('[data-shop-rail-coupons]'))showMarketCoupons();if(target.closest<HTMLElement>('[data-shop-coupons-close]'))($('[data-shop-market-coupons]') as HTMLDialogElement).close();
    const couponTabButton = target.closest<HTMLElement>('[data-shop-coupon-tab]'); if (couponTabButton) { message('', '[data-shop-coupon-message]'); couponTab = couponTabButton.dataset.shopCouponTab || 'available'; renderMarketCoupons(); }
    if(target.closest<HTMLElement>('[data-shop-top]'))window.scrollTo({top:0,behavior:matchMedia('(prefers-reduced-motion: reduce)').matches?'instant':'smooth'});
    const claim=target.closest<HTMLElement>('[data-shop-claim]');if(claim instanceof HTMLButtonElement){state.pendingCouponClaim=(claim.dataset.shopClaim || '');claim.disabled=true;claimCoupon((claim.dataset.shopClaim || '')).catch(error=>couponMessage(error.message,true)).finally(()=>{if(claim.isConnected)claim.disabled=false;});}
    const reviewRating=target.closest<HTMLElement>('[data-shop-review-rating]');if(reviewRating){state.reviewRating=Number((reviewRating.dataset.shopReviewRating || ''));state.reviewPage=1;loadReviews();}
    if(target.closest<HTMLElement>('[data-shop-review-replied]')){state.reviewReplied=!state.reviewReplied;state.reviewPage=1;loadReviews();}
    const reviewSort=target.closest<HTMLElement>('[data-shop-review-sort]');if(reviewSort){state.reviewSort=(reviewSort.dataset.shopReviewSort || '');state.reviewPage=1;loadReviews();}
    const reviewPage=target.closest<HTMLElement>('[data-shop-review-page]');if(reviewPage){state.reviewPage=Number((reviewPage.dataset.shopReviewPage || ''));loadReviews();}
    const thumbnail = target.closest<HTMLElement>('[data-shop-gallery-image]'); if (thumbnail) selectImage(Number((thumbnail.dataset.shopGalleryImage || '')));
    if (target.closest<HTMLElement>('[data-shop-gallery-next]')) selectImage(state.galleryIndex + 1, true);
    if (target.closest<HTMLElement>('[data-shop-gallery-share]')) shareProduct();
    const offer = target.closest<HTMLElement>('[data-shop-offer-coupon]'); if (offer && !state.busy) {
      const code = (offer.dataset.shopOfferCoupon || ''), coupon = state.coupons.find(c => c.code === code);
      (async () => { try { if (!coupon?.claimed) { state.pendingCouponClaim = code; await claimCoupon(code); } if (chooseValue($('[data-shop-coupon-choices]'), 'coupon', code)) { $('[data-shop-coupon-field]').scrollIntoView({ block: 'center', behavior: 'instant' }); message('已选择此优惠券', '[data-shop-detail-message]'); } } catch (caught) { const error = failure(caught); message(error.message, '[data-shop-detail-message]', true); } })();
    }
    const infoLink = target.closest<HTMLElement>('a[href^="#shop-product-"]');
    if (infoLink instanceof HTMLAnchorElement && !event.defaultPrevented && event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && selectInfoPanel(infoLink.hash, { updateHistory: true, scroll: true, focus: true })) {
      event.preventDefault(); infoLink.closest('.shop-gallery-menu')?.removeAttribute('open');
    }
    const filter = target.closest<HTMLElement>('[data-shop-filter]'); if (filter) { state.filter = (filter.dataset.shopFilter || ''); renderProducts(); }
    const mode = target.closest<HTMLElement>('[data-shop-mode-filter]'); if (mode) { state.modeFilter = (mode.dataset.shopModeFilter || ''); renderProducts(); }
    if (target.closest<HTMLElement>('[data-shop-reset]')) { state.filter = 'all'; state.modeFilter = ''; state.query = ''; state.category='';state.currency='';state.inStock=false;$input('[data-shop-instock]').checked=false; $input('#shop-search').value = ''; renderProducts(); }
    const sort = target.closest<HTMLElement>('[data-shop-sort]'); if (sort) { state.sort = (sort.dataset.shopSort || ''); renderProducts(); }
    const quantityStep = target.closest<HTMLElement>('[data-shop-quantity-step]'); if (quantityStep && !state.busy) { const input = inputField(purchase, 'quantity'), next = Number(input.value || 1) + Number((quantityStep.dataset.shopQuantityStep || '')); input.value = String(Math.max(Number(input.min), Math.min(Number(input.max), next))); updateSelection(); }
    if (target.closest<HTMLElement>('[data-shop-checkout-close]') && !state.busy) close(checkout);
    if (target.closest<HTMLElement>('[data-shop-order-close]')) close(orderDialog);
    const order = target.closest<HTMLElement>('[data-shop-order]'); if (order) { opener = order; openOrder((order.dataset.shopOrder || '')).then(()=>{if(order.hasAttribute('data-shop-open-review'))$('[data-shop-order-review]').scrollIntoView({block:'start',behavior:'instant'});}).catch(e => message(e.message, '[data-shop-message]', true)); }
    const pay = target.closest<HTMLElement>('[data-shop-pay]'); if (pay instanceof HTMLButtonElement) action(pay, () => payOrder(Number((pay.dataset.shopPay || ''))));
    const received = target.closest<HTMLElement>('[data-shop-received]'); if (received instanceof HTMLButtonElement) action(received, () => mutateOrder('received'));
    const refund = target.closest<HTMLElement>('[data-shop-refund]'); if (refund instanceof HTMLButtonElement) { const note = window.prompt('请填写退款原因，商家会按购买规则审核并核对支付流水。'); if (note?.trim()) action(refund, () => mutateOrder('refund', { note: note.trim() })); }
  });
  $button('[data-shop-order-sync]').addEventListener('click', () => action($button('[data-shop-order-sync]'), async () => { await refreshOrder(true); try { await refreshOrder(); } catch { message('同步已完成，后续读取暂时失败，请稍后刷新当前订单。', '[data-shop-order-message]'); } schedulePoll(); }));
  $button('[data-shop-order-cancel]').addEventListener('click', () => action($button('[data-shop-order-cancel]'), () => mutateOrder('cancel')));
  $form('[data-shop-search]').addEventListener('submit', event => { event.preventDefault(); state.query = $input('#shop-search').value.trim(); renderProducts(); });
  $input('[data-shop-instock]').addEventListener('change',()=>{state.inStock=$input('[data-shop-instock]').checked;renderProducts();});
  $input('#shop-search').addEventListener('input',()=>{if(!$input('#shop-search').value){state.query='';renderProducts();}});
  $('.shop-product-info-tabs').addEventListener('keydown', event => {
    if (!(event.target instanceof HTMLElement)) return;
    const tabs = $$('.shop-product-info-tabs [role="tab"]'), index = tabs.indexOf(event.target);
    if (index < 0 || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    let next = index;
    if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
    else if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = tabs.length - 1;
    else if (event.key !== ' ') return;
    event.preventDefault(); selectInfoPanel(required(tabs[next]).getAttribute('href') || '', { updateHistory: true, scroll: true, focus: true });
  });
  window.addEventListener('hashchange', syncInfoPanel);
  $('[data-shop-gallery-stage]').addEventListener('keydown', event => { if (event.target !== event.currentTarget) return; if (event.key === 'ArrowRight' || event.key === 'ArrowLeft') { event.preventDefault(); selectImage(state.galleryIndex + (event.key === 'ArrowRight' ? 1 : -1), true); } });
  let swipeStart: { x: number; y: number } | null = null;
  $('[data-shop-detail-visual]').addEventListener('pointerdown', event => { if (event.pointerType === 'touch') swipeStart = { x: event.clientX, y: event.clientY }; });
  $('[data-shop-detail-visual]').addEventListener('pointerup', event => { if (!swipeStart) return; const dx = event.clientX - swipeStart.x, dy = event.clientY - swipeStart.y; swipeStart = null; if (Math.abs(dx) > 40 && Math.abs(dx) > Math.abs(dy) * 1.5) selectImage(state.galleryIndex + (dx < 0 ? 1 : -1), true); });
  $('[data-shop-detail-visual]').addEventListener('pointercancel', () => { swipeStart = null; });
  $('[data-shop-orders-refresh]').addEventListener('click', () => loadOrders().catch(e => message(e.message, '[data-shop-message]', true)));
  inputField(purchase, 'quantity').addEventListener('input', event => { const input=inputField(purchase, 'quantity');if(input.value!==''&&Number.isFinite(Number(input.value)))updateSelection(); });
  $input('[data-shop-quantity-range]').addEventListener('input',event=>{inputField(purchase, 'quantity').value=$input('[data-shop-quantity-range]').value;updateSelection();});
  checkout.addEventListener('cancel', event => { if (state.busy) event.preventDefault(); });
  orderDialog.addEventListener('close', () => { clearTimeout(state.poll); state.orderSerial++;state.orderReviewSerial=(state.orderReviewSerial||0)+1; $('[data-shop-payment]').replaceChildren(); $('[data-shop-order-review]').replaceChildren();state.order = null; });
  document.addEventListener('chunnel:session', event => {
    if (retired || !event.detail || typeof event.detail.authenticated !== 'boolean') return;
    const detail = event.detail;
    identity(detail.authenticated && Number.isSafeInteger(detail.id) && Number(detail.id) > 0 && typeof detail.csrf_token === 'string' ? detail : { authenticated: false });
    if (state.owner) {
      void loadPersonal({ authenticated: true, id: state.owner });
      if (state.pendingCouponClaim) void claimCoupon(state.pendingCouponClaim).catch(error => { if (!retired) message(failure(error).message); });
    }
  });
  function resumeRaffleClock() {
    if (!retired && !document.hidden && state.catalog && root.dataset.shopView === 'catalog') renderProducts();
    scheduleRaffleClock();
  }
  document.addEventListener('visibilitychange', () => { schedulePoll(); resumeRaffleClock(); });
  window.addEventListener('pageshow', event => {
    if (event.persisted) {
      retired = true;
      message('页面已恢复，请刷新后重新确认账户与订单状态。', '[data-shop-message]');
      const refresh = button('刷新商城页面'); refresh.addEventListener('click', () => location.reload()); $('[data-shop-message]').append(refresh);
    } else resumeRaffleClock();
  });
  window.addEventListener('pagehide', () => {
    retired = true; generation++; state.serial++; state.orderSerial++; state.orderReviewSerial++; state.reviewSerial++; paymentSerial++;
    client.cancel(); clearTimeout(state.poll); clearTimeout(raffleTimer); clearTimeout(state.shareTimer);
    state.order = null; state.quote = null; state.draft = null; clearPersonal(); checkout.close(); orderDialog.close();
    $('[data-shop-payment]').replaceChildren(); $('[data-shop-order-detail]').replaceChildren(); $('[data-shop-order-review]').replaceChildren(); $('[data-shop-order-list]').replaceChildren();
    setBusy(true);
  });
  (async () => {
    const ordersPage = location.pathname.startsWith('/shop/orders');
    const productMatch = location.pathname.match(/^\/shop\/product\/([^/]+)\/?$/); let routeProduct = '';
    try { routeProduct = productMatch ? decodeURIComponent(productMatch[1] || '') : ''; } catch {}
    const productRoute = Boolean(productMatch);
    root.dataset.shopView = productRoute ? 'product' : ordersPage ? 'orders' : 'catalog';
    $$('[data-shop-rail]').forEach(el=>{if((el.dataset.shopRail==='orders')===ordersPage)el.setAttribute('aria-current','page');});
    if(new URLSearchParams(location.search).has('coupons')&&!ordersPage&&!productRoute)showMarketCoupons();
    $('[data-shop-catalog]').hidden = ordersPage || productRoute; $('[data-shop-orders]').hidden = !ordersPage; productPage.hidden = !productRoute; productContent.hidden = true; productLoading.hidden = !productRoute;
    $('[data-shop-store-crumb]').hidden = productRoute; $('[data-shop-store-heading]').hidden = productRoute; $('[data-shop-store-tabs]').hidden = productRoute;
    $$('[data-shop-tab]').forEach(el => { if ((el.dataset.shopTab === 'orders') === ordersPage) el.setAttribute('aria-current', 'page'); });
    try { const session = await client.getSession(); if (retired) return; identity(session); await loadCatalog(); if (retired) return; if(session.authenticated)await loadPersonal(session); const query = new URLSearchParams(location.search);if(query.has('claim')){const clean = new URL(location.href); clean.searchParams.delete('claim'); history.replaceState(history.state, '', clean.pathname + clean.search + clean.hash);state.pendingCouponClaim=(query.get('claim') || '');if(session.authenticated)await claimCoupon(state.pendingCouponClaim);else{document.querySelector<HTMLElement>('[data-account-login]')?.click();message('登录后即可领取这张优惠券。');}} if (ordersPage && session.authenticated) await loadOrders(); if (query.has('order') && session.authenticated) await openOrder((query.get('order') || '')); const requestedProduct = productRoute ? routeProduct : query.get('product'); if (requestedProduct && !productRoute) { location.replace(`${productURL(requestedProduct)}${location.search}`); return; } if (requestedProduct) { const opened = openProduct(requestedProduct); if (opened && state.product?.mode === 'group' && query.has('group')) inputField(purchase, 'group_id').value = (query.get('group') || ''); if (opened && query.has('coupon') && chooseValue($('[data-shop-coupon-choices]'),'coupon',(query.get('coupon') || ''))) updateSelection(); if (opened && !productRoute) history.replaceState(null, '', `${productURL(requestedProduct)}${location.search}`); } }
    catch (caught) { const error = failure(caught); message(error.message, '[data-shop-message]', true); $('[data-shop-products]').setAttribute('aria-busy', 'false'); if (productRoute && !state.catalog) productLoading.textContent = '商品信息暂时无法读取，请稍后刷新页面。'; if (!state.catalog) $('[data-shop-products]').replaceChildren(empty('商城暂时无法加载', '请稍后刷新页面，已有订单会继续保留。')); }
  })();
})();
