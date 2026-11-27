import { create } from '../shared/newpress-client';
import { failure } from '../shared/ledger-data';
import { moneyUnits, moneyText, mergeCatalog, dateInput, raffleDrawTime, equal } from '../shared/shop-admin-helpers';
import { createAdminActions } from '../shared/shop-admin-actions';
import { coupon as parseCoupon } from '../shared/shop-coupon-data';
import { safeURL } from '../shared/shop-helpers';
import { raffleAllocation } from '../shared/shop-admin-helpers';
import { adminConfig, adminCatalog, savedAdminCatalog, adminCoupons, savedAdminCoupons, adminOrders, adminReviews, adminOrderReceipt, savedAdminReview } from '../shared/shop-admin-data';
import type { ShopAdminCatalog, ShopAdminProduct, ShopAdminOrder, ShopAdminCoupons, ShopOrderAction } from '../shared/shop-admin-types';
import type { ShopCurrency, ShopCoupon } from '../shared/shop-coupon-data';
import type { ShopOwnReview, ShopVariant, ShopRaffleTier, ShopRaffleCampaign, ShopRafflePlan, ShopMode } from '../shared/shop-types';
import type { ServiceLibrary, ServiceProduct, ShopKind } from '../shared/shop-service-types';

interface AdminState {
  catalog: ShopAdminCatalog | null; currencies: ShopCurrency[]; coupons: ShopCoupon[]; couponVersion: number; editingCoupon: string;
  orders: ShopAdminOrder[]; busy: boolean; editing: number; product: ShopAdminProduct | null; order: ShopAdminOrder | null; dirty: boolean; sequence: number;
  reviews: ShopOwnReview[]; reviewStatus: string; reviewRating: number; reviewPage: number; reviewSerial: number; serviceLibrary: ServiceLibrary | undefined;
}
interface FieldOptions { type?: string; min?: number; max?: number; step?: number; maxlength?: number; required?: boolean; readonly?: boolean; multiline?: boolean; rows?: number; wide?: boolean; hint?: string; options?: Record<string, string> }
function required<T>(value: T | null | undefined): T { if (value === null || value === undefined) throw new Error('商城后台页面状态未确认，请重新读取'); return value; }
function query<T extends Element>(root: ParentNode, selector: string, type: { new(): T }): T { const el = root.querySelector(selector); if (!(el instanceof type)) throw new Error('商城后台控件缺失：' + selector); return el; }
function inputControl(value: Element): HTMLInputElement { if (!(value instanceof HTMLInputElement)) throw new Error('商城输入控件类型未确认'); return value; }
function select(value: Element): HTMLSelectElement { if (!(value instanceof HTMLSelectElement)) throw new Error('商城选择控件类型未确认'); return value; }
function queryControl(root: ParentNode, selector: string) { const el = root.querySelector(selector); if (!(el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement)) throw new Error('商城输入控件缺失'); return el; }
function selectedKind(value: string): ShopKind { if (!['virtual', 'physical', 'cdk', 'donation', 'raffle', 'bundle'].includes(value)) throw new Error('商品类型未确认'); return value as ShopKind; }
function selectedMode(value: string): ShopMode { if (!['direct', 'presale', 'deposit', 'installment', 'group'].includes(value)) throw new Error('购买方案未确认'); return value as ShopMode; }
function selectedRaffle(value: string): 'instant' | 'scheduled' { if (value !== 'instant' && value !== 'scheduled') throw new Error('开奖方式未确认'); return value; }
function couponKind(value: string): 'fixed' | 'percent' { if (value !== 'fixed' && value !== 'percent') throw new Error('优惠券方式未确认'); return value; }
function controls(form: HTMLFormElement) {
  return (name: string) => {
    const el = form.elements.namedItem(name);
    if (!(el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement)) throw new Error('商城后台输入控件未确认：' + name);
    return el;
  };
}
(() => {
  const target = document.querySelector('[data-shop-admin]'); if (!(target instanceof HTMLElement)) return;
  const root = target;
  const $ = (selector: string) => query(root, selector, HTMLElement);
  const $input = (selector: string) => query(root, selector, HTMLInputElement);
  const $select = (selector: string) => query(root, selector, HTMLSelectElement);
  const $form = (selector: string) => query(root, selector, HTMLFormElement);
  const $dialog = (selector: string) => query(root, selector, HTMLDialogElement);
  const $fieldset = (selector: string) => query(root, selector, HTMLFieldSetElement);
  const $textarea = (selector: string) => query(root, selector, HTMLTextAreaElement);
  const $button = (selector: string) => query(root, selector, HTMLButtonElement);
  const readOnly = root.dataset.readOnly === 'true';
  const owner = root.dataset.userId || '', csrf = root.dataset.csrf || '';
  let retired = false, catalogSequence = 0, couponSequence = 0;
  const client = create({ session: () => ({ authenticated: !retired, csrf_token: csrf }), onUnauthorized: () => retire() });
  const api = async (path = '', method = 'GET', body?: unknown) => {
    if (retired || method !== 'GET' && readOnly) throw new DOMException('商城后台操作已停用', 'AbortError');
    const result = await client.request(`/api/v1/admin/shop${path}`, { method, body });
    if (retired) throw new DOMException('商城后台会话已变化', 'AbortError');
    return result;
  };
  const esc = (value: unknown) => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c] || c));
  const kinds: Record<string, string> = { virtual: '虚拟商品', physical: '实物商品', cdk: 'CDK 卡密', donation: '捐赠', raffle: '抽奖码', bundle: '捆绑包' };
  const modes: Record<string, string> = { direct: '直接购买', presale: '全款预售', deposit: '定金 / 尾款', installment: '分期付款', group: '拼团购买' };
  const statuses: Record<string, string> = { pending: '待付款', paid: '已付款', partially_paid: '待付尾款 / 分期', awaiting_shipment: '待发货', shipped: '已发货', completed: '已完成', awaiting_group: '待成团', awaiting_draw: '待开奖', awaiting_release: '等待预售交付', refund_requested: '退款待审核', refund_required: '需处理退款', refunded: '已退款', cancelled: '已取消', expired: '已过期', fulfilled: '已交付' };
  const state: AdminState = { catalog: null, currencies: [], coupons: [], couponVersion: 0, editingCoupon: '', orders: [], busy: false, editing: -1, product: null, order: null, dirty: false, sequence: 0,
    reviews: [], reviewStatus: '', reviewRating: 0, reviewPage: 1, reviewSerial: 0, serviceLibrary: undefined };
  const catalog = () => required(state.catalog), product = () => required(state.product);
  const drafts = new Set<string>(), revisions = new Map<string, number>();
  const touch = (scope: string) => { drafts.add(scope); revisions.set(scope, (revisions.get(scope) || 0) + 1); state.dirty = true; };
  const clearDraft = (scope: string, revision = revisions.get(scope)) => { if (revisions.get(scope) === revision) drafts.delete(scope); state.dirty = drafts.size > 0; };
  let productBaseline: ShopAdminCatalog | null = null, settingsBaseline: ShopAdminCatalog | null = null, couponBaseline: ShopAdminCoupons | null = null;
  let couponCreatedAt = '';
  const actions = createAdminActions({ owner, readOnly, alive: () => !retired && !readOnly && root.isConnected,
    storage: { getItem: key => sessionStorage.getItem(key), setItem: (key, value) => sessionStorage.setItem(key, value), removeItem: key => sessionStorage.removeItem(key) }, api });
  const productForm = $form('[data-shop-product-form]'), editor = $dialog('[data-shop-editor]'), orderDialog = $dialog('[data-shop-order-dialog]');
  const serviceEditor = window.NewPressShopServiceEditor.create({
    root: $fieldset('[data-shop-services-fields]'), getLibrary: () => state.serviceLibrary,
    getProduct: servicePreviewProduct, getContext: () => ({ products: state.catalog?.products || [], currencies: state.currencies }),
    isLocked: () => readOnly || retired || state.busy, onChange: () => { touch('product'); }
  });
  let editorOpener: HTMLElement | null = null, orderOpener: HTMLElement | null = null;
  function message(text = '', error = false, target = $('[data-shop-status]')) { if (retired) return; target.textContent = text; target.dataset.error = String(error); }
  function busy(value: boolean) {
    value = value || retired;
    state.busy = value; root.setAttribute('aria-busy', String(value));
    $fieldset('[data-shop-product-fields]').disabled = value || readOnly;
    $fieldset('[data-shop-description-fields]').disabled = value || readOnly;
    $fieldset('[data-shop-services-fields]').disabled = value || readOnly;
    $textarea('[data-content-source]').readOnly = value || readOnly;
    $button('[data-shop-product-save]').disabled = value || readOnly;
    $fieldset('[data-shop-settings] fieldset').disabled = value || readOnly;
    $fieldset('[data-shop-coupon-form] fieldset').disabled = value || readOnly;
    $button('[data-shop-add]').disabled = value || readOnly;
  }
  const precision = (currency: string) => currency === 'CNY' ? 2 : state.currencies.find(item => item.key === currency)?.precision ?? 2;
  const price = (value: string, currency = 'CNY') => { const item = state.currencies.find(entry => entry.key === currency); return `${item?.symbol || (currency === 'CNY' ? '¥' : `${item?.name || currency} `)}${moneyText(value, precision(currency))}`; };
  const date = (value: string) => value && !String(value).startsWith('0001-') && Number.isFinite(Date.parse(value)) ? window.ChunnelTime?.formatDateTime(value) || new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
  const tone = (status: string) => ['completed', 'fulfilled', 'shipped'].includes(status) ? 'success' : status === 'refund_required' ? 'danger' : ['cancelled', 'expired', 'refunded'].includes(status) ? '' : 'warning';
  const empty = (title: string, description: string) => `<div class="sa-empty"><strong>${esc(title)}</strong><p>${esc(description)}</p></div>`;
  function safeImage(value: string) { return safeURL(value); }
  function switchTab(tab: string, focus = false) {
    for (const button of root.querySelectorAll<HTMLElement>('[data-shop-tab]')) {
      if (button.dataset.shopTab === tab) { button.setAttribute('aria-current', 'page'); if (focus) button.focus(); }
      else button.removeAttribute('aria-current');
    }
    for (const panel of root.querySelectorAll<HTMLElement>('[data-shop-panel]')) panel.hidden = panel.dataset.shopPanel !== tab;
    if(tab==='reviews')loadReviews().catch(error=>message(error.message,true));
  }
  async function loadReviews(request: { page?: number; status?: string; rating?: number } = {}): Promise<void> {
    if ([...drafts].some(key => key.startsWith('review:'))) throw new Error('评价有未保存的修改，请先保存后重新读取');
    const serial = ++state.reviewSerial, page = request.page ?? state.reviewPage, status = request.status ?? state.reviewStatus, rating = request.rating ?? state.reviewRating;
    const q = new URLSearchParams({ page: String(page), rating: String(rating), status, q: controls($form('[data-shop-review-search]'))('q').value.trim() });
    const response = await api(`/reviews?${q}`);
    if (serial !== state.reviewSerial || [...drafts].some(key => key.startsWith('review:'))) return;
    const result = adminReviews(response, page), pagesAvailable = Math.max(1, Math.ceil(result.total / result.page_size));
    if (result.page > pagesAvailable) return loadReviews({ page: pagesAvailable, status, rating });
    state.reviewPage = page; state.reviewStatus = status; state.reviewRating = rating;
    state.reviews=result.reviews;
    root.querySelectorAll<HTMLElement>('[data-shop-review-status]').forEach(el=>el.setAttribute('aria-pressed',String(el.dataset.shopReviewStatus===state.reviewStatus)));
    root.querySelectorAll<HTMLElement>('[data-shop-review-rating]').forEach(el=>el.setAttribute('aria-pressed',String(Number(el.dataset.shopReviewRating)===state.reviewRating)));
    $('[data-shop-review-count]').textContent=`共 ${result.total} 条评价`;
    $('[data-shop-reviews]').innerHTML=state.reviews.map(r=>`<article class="sa-review" data-hidden="${r.hidden}"><header><div><a href="/shop/product/${encodeURIComponent(r.product)}/#shop-product-reviews">${esc(r.title)}</a><span class="sa-review-stars" aria-label="${r.rating} 星">${'★'.repeat(r.rating)}${'☆'.repeat(5-r.rating)}</span></div><span class="sa-badge">${r.hidden?'已隐藏':'公开'}</span></header><p class="sa-review-content">${esc(r.content)}</p><p class="sa-note">${esc(r.anonymous?'匿名买家':r.author)} · 用户 ${esc(r.user_id)} · ${esc(date(r.created_at))}${r.variant?` · ${esc(r.variant)}`:''}</p><p class="sa-note">订单 ${esc(r.order_id)}</p><form data-shop-review-edit="${esc(r.id)}"><fieldset ${readOnly?'disabled':''}><label class="sa-field"><span>商家回复${r.reply?` · ${esc(date(r.replied_at))}`:''}</span><textarea name="reply" rows="3" maxlength="2000" placeholder="回复买家的问题或使用反馈">${esc(r.reply)}</textarea></label><label class="sa-switch"><input type="checkbox" name="hidden" ${r.hidden?'checked':''}><span>隐藏此评价</span></label><label class="sa-field sa-review-reason" ${r.hidden?'':'hidden'}><span>隐藏原因（买家可见）</span><input name="reason" maxlength="500" value="${esc(r.hidden_reason)}" ${r.hidden?'required':''} placeholder="请填写具体原因"></label><footer><p role="status" class="sa-note"></p><button type="submit" class="admin-button admin-button-primary">保存处理</button></footer></fieldset></form></article>`).join('')||empty('没有符合条件的评价','可以切换筛选条件，或等待买家完成订单后提交评价。');
    const pages=Math.max(1,Math.ceil(result.total/result.page_size));$('[data-shop-review-pagination]').innerHTML=pages>1?`<button type="button" class="admin-button admin-button-secondary" data-shop-review-page="${result.page-1}" ${result.page<=1?'disabled':''}>上一页</button><span>${result.page} / ${pages}</span><button type="button" class="admin-button admin-button-secondary" data-shop-review-page="${result.page+1}" ${result.page>=pages?'disabled':''}>下一页</button>`:'';
  }
  root.addEventListener('change',event=>{if(!(event.target instanceof HTMLInputElement))return;const target=event.target;const form=target.closest<HTMLFormElement>('[data-shop-review-edit]');if(form&&target.name==='hidden'){query(form, '.sa-review-reason', HTMLElement).hidden=!target.checked;controls(form)('reason').required=target.checked;}});
  root.addEventListener('input', event => {
    if (!(event.target instanceof Element)) return;
    const form = event.target.closest<HTMLFormElement>('[data-shop-review-edit]');
    if (form?.dataset.shopReviewEdit) touch('review:' + form.dataset.shopReviewEdit);
  });
  root.addEventListener('submit', async event => {
    if (!(event.target instanceof HTMLFormElement) || !event.target.matches('[data-shop-review-edit]')) return;
    event.preventDefault(); if (readOnly || retired || state.busy) return;
    const form = event.target, previous = state.reviews.find(r => r.id === form.dataset.shopReviewEdit); if (!previous) return;
    const fields = controls(form), scope = 'review:' + previous.id, revision = revisions.get(scope);
    const input = { version: previous.version, reply: fields('reply').value.trim(), hidden: inputControl(fields('hidden')).checked, reason: fields('reason').value.trim() };
    const status = query(form, '[role=status]', HTMLElement), fieldset = query(form, 'fieldset', HTMLFieldSetElement);
    busy(true); fieldset.disabled = true;
    try {
      const current = adminReviews(await api('/reviews?page=1&q=' + encodeURIComponent(previous.order_id)), 1).reviews.find(r => r.id === previous.id);
      if (!current) throw new Error('评价已不可读取，请重新核对');
      // A versioned read recognizes a previously committed reply after a lost response.
      const result = current.version !== previous.version
        ? savedAdminReview({ review: current }, previous, input, owner)
        : savedAdminReview(await api('/reviews/' + encodeURIComponent(previous.id), 'PUT', input), previous, input, owner);
      state.reviews = state.reviews.map(r => r.id === result.id ? result : r);
      clearDraft(scope, revision);
      message(drafts.has(scope) ? '处理已保存；保存期间的新输入尚未提交。' : '评价回复与显示状态已保存。', false, status);
      const article = form.closest<HTMLElement>('.sa-review'); if (article) article.dataset.hidden = String(result.hidden);
      const badge = article?.querySelector('.sa-badge'); if (badge) badge.textContent = result.hidden ? '已隐藏' : '公开';
    } catch (error) { message(failure(error).message, true, status); }
    finally { busy(false); fieldset.disabled = readOnly || retired; }
  });
  $form('[data-shop-review-search]').addEventListener('submit',event=>{event.preventDefault();loadReviews({ page: 1 }).catch(error=>message(error.message,true));});
  function updateOverview() {
    $('[data-shop-stat="products"]').textContent = String((state.catalog?.products || []).filter(p => p.enabled).length);
    $('[data-shop-stat="shipping"]').textContent = String(state.orders.filter(o => o.status === 'awaiting_shipment').length);
    $('[data-shop-stat="refunds"]').textContent = String(state.orders.filter(o => ['refund_required','refund_requested'].includes(o.status)).length);
    $('[data-shop-stat="orders"]').textContent = String(state.orders.length);
  }
  function renderProducts() {
    const query = $input('[data-shop-search]').value.trim().toLocaleLowerCase(), kind = $select('[data-shop-kind-filter]').value;
    const all = state.catalog?.products || [], filtered = all.map((product, index) => ({ product, index })).filter(({ product }) => (!kind || product.kind === kind) && (!query || `${product.title} ${product.key}`.toLocaleLowerCase().includes(query)));
    $('[data-shop-products]').innerHTML = filtered.length ? filtered.map(({ product: p, index }) => {
      const image = safeImage(p.image || '');
      return `<article class="sa-product-row">${image ? `<img class="sa-product-thumb" src="${esc(image)}" alt="" loading="lazy">` : `<span class="sa-product-thumb" aria-hidden="true">${esc((p.title || '商').slice(0, 1))}</span>`}<div class="sa-product-copy"><h3>${esc(p.title)}</h3><div class="sa-product-meta"><span class="sa-tag">${esc(kinds[p.kind] || p.kind)}</span><span class="sa-tag" data-tone="${p.enabled ? 'success' : ''}">${p.enabled ? '已上架' : '未上架'}</span><span>${esc(p.key)}</span></div></div><div class="sa-product-price">${price(p.price, p.currency)}<small>库存 ${String(p.stock) === '-1' ? '不限量' : esc(p.stock)}</small></div><div class="sa-product-actions"><button type="button" class="admin-button admin-button-secondary" data-shop-edit="${index}">${readOnly ? '查看' : '编辑'}</button><button type="button" class="admin-button admin-button-quiet" data-shop-toggle="${index}" ${readOnly ? 'disabled' : ''}>${p.enabled ? '下架' : '上架'}</button></div></article>`;
    }).join('') : empty(all.length ? '没有匹配的商品' : '先添加第一件商品', all.length ? '换个关键词或交付类型继续查找。' : '从交付类型和购买方案开始，配置完成后上架。');
    updateOverview();
  }
  const capField = (name: string, label: string, value: number | undefined, max = 1_000_000_000) => field(name, label, value ?? 0, { type: 'number', min: 0, max, step: 1, required: true });
  function campaignRow(campaign: ShopRaffleCampaign) {
    const products = catalog().products.filter(p => p.kind === 'raffle' && p.raffle?.campaign === campaign.key);
    return `<div class="sa-campaign-row" data-shop-campaign="${esc(campaign.key)}">
      <div class="sa-grid">${field('campaign_title', '活动名称', campaign.title, { required: true, maxlength: 80, wide: true })}
        ${capField('campaign_tickets', '活动券码总量（张）', campaign.ticket_limit)}${capField('campaign_user', '活动每人限购（张）', campaign.user_limit)}</div>
      <div class="sa-campaign-foot"><small>${products.length ? `已关联 ${esc(products.map(p => p.title).join('、'))}，解除关联后可移除。` : '尚未关联商品'}</small>
      <button type="button" class="admin-button admin-button-secondary" data-shop-remove-campaign ${products.length ? 'disabled' : ''}>移除活动</button></div></div>`;
  }
  function syncCampaignList() {
    const count = $('[data-shop-campaigns]').childElementCount;
    $button('[data-shop-add-campaign]').disabled = readOnly || count >= 100;
  }
  function renderSettings() {
    const form = $form('[data-shop-settings]'), catalog = required(state.catalog);
    settingsBaseline = structuredClone(catalog);
    inputControl(controls(form)('enabled')).checked = !!catalog.enabled;
    controls(form)('order_minutes').value = String(catalog.order_minutes);
    controls(form)('terms').value = catalog.terms || '';
    controls(form)('refund_policy').value = catalog.refund_policy || '';
    $('[data-shop-campaigns]').innerHTML = (catalog.raffle_campaigns || []).map(campaignRow).join('');
    syncCampaignList();
    $('[data-shop-version]').textContent = `配置版本 ${catalog.version}`;
  }
  function renderOrders() {
    const query = $input('[data-shop-order-search]').value.trim().toLocaleLowerCase(), status = $select('[data-shop-order-filter]').value;
    const rows = state.orders.filter(o => (!status || o.status === status) && (!query || `${o.id} ${o.title} ${o.user_id}`.toLocaleLowerCase().includes(query)));
    $('[data-shop-orders]').innerHTML = rows.length ? `<div class="sa-table-wrap"><table class="sa-table"><thead><tr><th scope="col">订单 / 商品</th><th scope="col">状态</th><th scope="col">金额</th><th scope="col">用户 / 创建时间</th><th scope="col">操作</th></tr></thead><tbody>${rows.map(o => `<tr><td><strong>${esc(o.title)}</strong><code>${esc(o.id)}</code><small>${esc(kinds[o.kind] || o.kind)} · ${esc(modes[o.mode] || o.mode)} · ${esc(o.quantity || 1)} 件</small></td><td><span class="sa-tag" data-tone="${tone(o.status)}">${esc(statuses[o.status] || o.status)}</span></td><td class="sa-money">${price(o.total, o.currency)}<small>已付 ${price(o.paid_amount, o.currency)}</small></td><td>${esc(o.user_id)}<small>${date(o.created_at)}</small></td><td><button type="button" class="admin-button admin-button-secondary" data-shop-order="${esc(o.id)}">${o.status === 'awaiting_shipment' ? '查看并发货' : '查看详情'}</button></td></tr>`).join('')}</tbody></table></div>` : empty(state.orders.length ? '没有匹配的订单' : '暂无商城订单', state.orders.length ? '更改筛选条件后继续查询。' : '用户创建订单后，会在这里显示付款与交付进度。');
    updateOverview();
  }
  function couponCurrencies() {
    const select = $select('[data-shop-coupon-currency]'), keep = select.value;
    select.replaceChildren(new Option('不限货币', ''), ...state.currencies.map(item => new Option(`${item.name}（${item.key}）`, item.key)));
    if ([...select.options].some(option => option.value === keep)) select.value = keep;
  }
  function renderCoupons() {
    const host = $('[data-shop-coupons]');
    host.innerHTML = state.coupons.length ? state.coupons.map((coupon,index) => {
      const condition = [coupon.kinds?.length ? coupon.kinds.map(k => kinds[k] || k).join('、') : '全部商品类型', coupon.products?.length ? `商品 ${coupon.products.join('、')}` : '', coupon.levels?.length ? `等级 ${coupon.levels.join('、')}` : '', coupon.memberships?.length ? `会员 ${coupon.memberships.join('、')}` : ''].filter(Boolean).join(' · ');
      const value = coupon.kind === 'percent' ? `${moneyText(coupon.value,2)}% 折扣` : `${price(coupon.value,coupon.currency)} 立减`;
      return `<article class="sa-coupon-row"><div class="sa-coupon-amount">${esc(value)}</div><div class="sa-coupon-copy"><h3>${esc(coupon.title)}</h3><code>${esc(coupon.code)}</code><p>${esc(condition || '适用于全场商品')} · ${date(coupon.starts_at)} 至 ${date(coupon.ends_at)}</p><small>${coupon.home ? '首页展示' : '不在首页展示'} · ${coupon.enabled ? '已启用' : '已停用'} · 最多领取 ${coupon.claim_limit || '不限'} 张 · 核销 ${coupon.use_limit || '不限'} 次</small></div><div class="sa-product-actions"><button type="button" class="admin-button admin-button-secondary" data-shop-coupon-edit="${index}" ${readOnly?'disabled':''}>编辑</button><button type="button" class="admin-button admin-button-quiet" data-shop-coupon-toggle="${index}" ${readOnly?'disabled':''}>${coupon.enabled?'停用':'启用'}</button><button type="button" class="admin-button admin-button-quiet" data-shop-coupon-delete="${index}" ${readOnly?'disabled':''}>删除</button></div></article>`;
    }).join('') : empty('还没有优惠券', '创建优惠券后，可以设置领取条件并选择是否在首页展示。');
  }
  function setCouponForm(coupon: ShopCoupon | null = null) {
    if (drafts.has('coupon')) { message('优惠券有未保存修改，请先保存或取消。', true); return; }
    couponBaseline = { version: state.couponVersion, coupons: structuredClone(state.coupons) };
    const form = $form('[data-shop-coupon-form]'); form.reset(); state.editingCoupon = coupon?.code || '';
    couponCurrencies();
    const fields = controls(form), now = new Date(), later = new Date(now.getTime() + 30 * 86400000);
    couponCreatedAt = coupon?.created_at || now.toISOString();
    fields('code').value = coupon?.code || ''; inputControl(fields('code')).readOnly = !!coupon;
    fields('title').value = coupon?.title || ''; fields('kind').value = coupon?.kind || 'fixed';
    fields('currency').value = coupon ? coupon.currency : 'CNY';
    fields('value').value = coupon ? moneyText(coupon.value, coupon.kind === 'percent' ? 2 : precision(coupon.currency)) : '';
    fields('minimum').value = coupon ? moneyText(coupon.minimum, precision(coupon.currency || 'CNY')) : '0';
    fields('maximum_discount').value = coupon ? moneyText(coupon.maximum_discount, precision(coupon.currency || 'CNY')) : '0';
    fields('starts_at').value = dateInput(coupon?.starts_at) || dateInput(now.toISOString());
    fields('ends_at').value = dateInput(coupon?.ends_at) || dateInput(later.toISOString());
    for (const key of ['claim_limit','use_limit'] as const) fields(key).value = String(coupon?.[key] || 0);
    fields('per_user_limit').value = String(coupon?.per_user_limit || 1);
    for (const key of ['kinds','modes','products','categories','levels','memberships'] as const) fields(key).value = (coupon?.[key] || []).join(', ');
    inputControl(fields('home')).checked = !!coupon?.home; inputControl(fields('enabled')).checked = coupon ? !!coupon.enabled : true;
    form.hidden = false; $('[data-shop-coupon-value-hint]').textContent = fields('kind').value === 'percent' ? '填写折扣百分比，如 8.5 表示优惠 8.5%。' : `按 ${select(fields('currency')).selectedOptions[0]?.textContent || '所选货币'} 输入立减金额。`;
    fields('kind').disabled = false; message('',false,$('[data-shop-coupon-status]')); fields('code').focus();
  }
  function collectCoupon(): ShopCoupon {
    const f=controls($form('[data-shop-coupon-form]')), code=f('code').value.trim().toUpperCase(),currency=f('currency').value,places=precision(currency||'CNY');
    const list=(name: string)=>[...new Set(f(name).value.split(/[,，]+/).map(v=>v.trim()).filter(Boolean))];
    const timestamp=(name: 'starts_at' | 'ends_at') => raffleDrawTime(f(name).value, couponBaseline?.coupons.find(c => c.code === state.editingCoupon)?.[name]);
    const numeric=(name: string)=>{const value=Number(f(name).value);if(!Number.isSafeInteger(value)||value<0)throw new Error('领取和核销上限请输入非负整数。');return value;};
    if(!/^[A-Z0-9][A-Z0-9_-]{2,39}$/.test(code))throw new Error('优惠码请使用 3 至 40 位字母、数字、下划线或连字符。');
    const kind=couponKind(f('kind').value),value=kind==='percent'?moneyUnits(f('value').value,2):moneyUnits(f('value').value,places);
    const minimum=moneyUnits(f('minimum').value||'0',places),maximum_discount=moneyUnits(f('maximum_discount').value||'0',places);
    if(!currency&&(minimum!=='0'||maximum_discount!=='0'))throw new Error('不限货币的折扣券请将最低金额和最高优惠金额设为 0。');
    return parseCoupon({code,title:f('title').value.trim(),kind,value,minimum,maximum_discount,currency,kinds:list('kinds'),modes:list('modes'),products:list('products'),categories:list('categories'),levels:list('levels'),memberships:list('memberships'),starts_at:timestamp('starts_at'),ends_at:timestamp('ends_at'),claim_limit:numeric('claim_limit'),use_limit:numeric('use_limit'),per_user_limit:numeric('per_user_limit'),home:inputControl(f('home')).checked,enabled:inputControl(f('enabled')).checked,created_at:couponCreatedAt});
  }
  async function loadCoupons() {
    const serial = ++couponSequence, result = adminCoupons(await api('/coupons'));
    if (serial !== couponSequence) return;
    state.coupons = result.coupons; state.couponVersion = result.version; renderCoupons();
  }
  async function loadOrders() {
    const seq = ++state.sequence, orders = adminOrders(await api('/orders'));
    if (seq !== state.sequence) return;
    state.orders = orders; renderOrders();
  }
  async function saveCatalog(next: ShopAdminCatalog, base = catalog()) {
    const latestConfig = adminConfig(await api());
    if (latestConfig.csrf_token !== csrf) { retire(); throw new Error('登录状态已变化'); }
    const latest = latestConfig.catalog, merged = mergeCatalog(base, next, latest);
    if (merged.conflicts.length) throw new Error('这些内容已被其他操作修改：' + merged.conflicts.join('、') + '。输入已保留，请先核对最新配置。');
    // A lost successful reply can be recognized on the next read without saving again.
    state.catalog = equal(merged.catalog, latest) ? latest : savedAdminCatalog(await api('', 'PUT', merged.catalog), merged.catalog);
    renderProducts(); if (!drafts.has('settings')) renderSettings();
  }
  async function saveCoupons(next: ShopCoupon[], version: number) {
    const latest = adminCoupons(await api('/coupons'));
    if (equal(latest.coupons, next)) { state.coupons = latest.coupons; state.couponVersion = latest.version; renderCoupons(); return; }
    if (latest.version !== version) throw new Error('优惠券已被其他操作修改，输入已保留，请重新读取核对');
    const result = savedAdminCoupons(await api('/coupons', 'PUT', { version, coupons: next }), version, next);
    state.coupons = result.coupons; state.couponVersion = result.version; renderCoupons();
  }
  async function runOrder(form: HTMLFormElement, order: ShopAdminOrder, action: ShopOrderAction, success: string): Promise<void> {
    if (readOnly || retired || state.busy) return;
    const status = query(form, '[role=status]', HTMLElement), fieldset = query(form, 'fieldset', HTMLFieldSetElement);
    busy(true); fieldset.disabled = true;
    try {
      const result = await actions.perform(order, action);
      state.sequence++; state.orders = state.orders.map(o => o.id === result.order.id ? result.order : o); renderOrders();
      if (state.order?.id === order.id) { orderDialog.close(); state.order = null; orderOpener?.focus(); }
      const confirmed = success + (result.recoveryPending ? '恢复记录清理失败，下次操作会先核对已确认结果。' : '');
      message(confirmed);
      try { await loadOrders(); } catch { message(confirmed + '订单列表暂未刷新，请稍后重新读取。'); }
    } catch (error) {
      message(failure(error).message, true, status);
      // Refund approval can commit before wallet credit fails. A read discovers
      // the persisted state and exposes its original wallet refund retry.
      if (!retired) try {
        await loadOrders(); const current = state.orders.find(o => o.id === order.id);
        if (current && current.status !== order.status && state.order?.id === order.id) {
          let confirmed = false;
          try { adminOrderReceipt({ order: current }, order, action, true); confirmed = true; } catch {}
          if (confirmed) {
            const result = await actions.perform(order, action);
            orderDialog.close(); state.order = null; showOrder(result.order.id, orderOpener);
            message('原处理结果已核对；请查看订单中的后续操作。');
          }
        }
      } catch { /* Keep the pending action and its original input. */ }
    } finally { busy(false); fieldset.disabled = readOnly || retired; }
  }
  function field(name: string, label: string, value: unknown = '', options: FieldOptions = {}) {
    const media = ['image', 'variant_image', 'images', 'detail_images'].includes(name);
    const attrs = `${media ? ` data-media-input data-media-title="${esc(label)}"${options.multiline ? ' data-media-multiple="12"' : ''}` : ''}${options.required ? ' required' : ''}${options.maxlength ? ` maxlength="${options.maxlength}"` : ''}${options.min !== undefined ? ` min="${esc(options.min)}"` : ''}${options.max !== undefined ? ` max="${esc(options.max)}"` : ''}${options.step ? ` step="${esc(options.step)}"` : ''}${options.readonly ? ' readonly' : ''}`;
    const input = options.options ? `<select name="${esc(name)}"${attrs}>${Object.entries(options.options).map(([key, text]) => `<option value="${esc(key)}" ${String(value) === key ? 'selected' : ''}>${esc(text)}</option>`).join('')}</select>` : options.multiline ? `<textarea name="${esc(name)}" rows="${options.rows || 3}"${attrs}>${esc(value)}</textarea>` : `<input name="${esc(name)}" type="${options.type || 'text'}" value="${esc(value)}"${attrs}>`;
    return `<label class="sa-field${options.wide ? ' sa-wide' : ''}"><span>${esc(label)}</span>${input}${options.hint ? `<small>${esc(options.hint)}</small>` : ''}</label>`;
  }
  function closeEditor() { editor.close(); clearDraft('product'); editorOpener?.focus(); }
  function editorTab(tab: string) {
    $fieldset('[data-shop-product-fields]').hidden = tab !== 'settings';
    $fieldset('[data-shop-description-fields]').hidden = tab !== 'description';
    $fieldset('[data-shop-services-fields]').hidden = tab !== 'services';
    if (tab === 'services') serviceEditor.refreshPreview();
    editor.querySelectorAll<HTMLElement>('[data-shop-editor-tab]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.shopEditorTab === tab)));
    editor.scrollTop = 0;
  }
  editor.querySelectorAll<HTMLElement>('[data-shop-editor-tab]').forEach(button => button.addEventListener('click', () => editorTab(button.dataset.shopEditorTab || 'settings')));
  productForm.addEventListener('invalid', event => { if (!(event.target instanceof Element)) return; const target=event.target; editorTab(target.closest<HTMLElement>('[data-shop-description-fields]') ? 'description' : 'settings'); }, true);
  function openEditor(index: number, opener: HTMLElement) {
    if (state.busy) return;
    if (drafts.has('product')) { message('商品有未保存修改，请先保存或取消。', true); return; }
    productBaseline = structuredClone(catalog()); state.editing = index; editorOpener = opener; clearDraft('product');
    state.product = structuredClone(index >= 0 ? required(catalog().products[index]) : newProduct());
    $('#shop-editor-title').textContent = index >= 0 ? '编辑商品' : '添加商品';
    editorTab('settings');
    renderEditor(product());
    serviceEditor.reset(product());
    const source = product().description_source || (product().description ? '<p>' + esc(product().description).replace(/\n/g, '<br>') + '</p>' : '');
    $fieldset('[data-shop-description-fields]').disabled = readOnly;
    $fieldset('[data-shop-services-fields]').disabled = readOnly;
    $textarea('[data-content-source]').readOnly = readOnly;
    required(window.ChunnelBlockEditor).reset(source);
    $fieldset('[data-shop-product-fields]').disabled = readOnly;
    $button('[data-shop-product-save]').hidden = readOnly;
    message('', false, $('[data-shop-editor-status]'));
    editor.showModal();
  }
  // Product-specific fields are collected below; untouched fields stay on the
  // server snapshot so future versions are not silently erased by this editor.
  const zeroDate = '0001-01-01T00:00:00Z';
  function newProduct(): ShopAdminProduct {
    return { key: `product-${crypto.randomUUID().slice(0, 8)}`, title: '', summary: '', image: '', images: [], detail_images: [], description: '', description_source: '', description_author_id: '0', services: null, precision: 2, category: '', kind: 'virtual', mode: 'direct', enabled: false, currency: 'CNY', price: '1000', compare_at: '0', stock: '-1', shipping_fee: '0', variants: [], delivery: '', codes: [], bundle: [], release_at: zeroDate, deposit: '0', balance_due_at: zeroDate, max_donation: '0', installment: null, group: null, raffle: null };
  }
  function variantRow(variant: Partial<ShopVariant> = { key: '', title: '', price: '1000', stock: '-1' }, places?: number) {
    const selectedCurrency = productForm.elements.namedItem('currency');
    places ??= precision(selectedCurrency instanceof HTMLSelectElement ? selectedCurrency.value : product().currency);
    return `<div class="sa-variant-row" data-shop-variant>${field('variant_key', '规格编号', variant.key, { required: true, maxlength: 64 })}${field('variant_title', '规格名称', variant.title, { required: true, maxlength: 80 })}${field('variant_price', '规格售价', moneyText(variant.price, places), { required: true })}${field('variant_stock', '规格库存', variant.stock, { type: 'number', required: true, min: -1, max: 1000000000 })}${field('variant_image', '规格图片地址', variant.image || '', { maxlength: 2000, hint: '可选，选中规格时切换到此图。' })}<button type="button" class="sa-remove" data-shop-remove-row aria-label="移除此规格">移除</button></div>`;
  }
  function tierRow(tier: ShopRaffleTier | Partial<ShopRaffleTier> = {}) {
    return `<div class="sa-tier-row" data-shop-tier>
      ${field('tier_prize', '奖项名称', tier.prize || '', { required: true, maxlength: 120 })}
      <div data-shop-raffle="instant">${field('tier_rate', '中奖概率（%）', moneyText(tier.win_bps ?? 100), { type: 'number', min: 0.01, max: 100, step: 0.01, required: true })}</div>
      <div data-shop-raffle="scheduled">${field('tier_count', '中奖名额', tier.winners || 1, { type: 'number', min: 1, max: 1000, required: true })}</div>
      <div class="sa-tier-actions"><button type="button" data-tier-move="up">上移</button><button type="button" data-tier-move="down">下移</button>
      <button type="button" class="sa-remove" data-shop-remove-row aria-label="移除此奖项">移除</button></div></div>`;
  }
  function editorAllocation(mode: string) {
    const rows = [...productForm.querySelectorAll<HTMLElement>('[data-shop-tier]')];
    const instant = mode === 'instant';
    const values = rows.length ? rows.map(row => queryControl(row, instant ? '[name=tier_rate]' : '[name=tier_count]').value)
      : [controls(productForm)(instant ? 'win_rate' : 'winners').value];
    return { ...raffleAllocation(mode, values, rows.length > 0), rows };
  }
  function refreshRaffleSummary() {
    if (controls(productForm)('kind').value !== 'raffle') return;
    const summary = $('[data-shop-tier-summary]');
    try {
      const allocation = editorAllocation(controls(productForm)('raffle_mode').value);
      summary.textContent = allocation.message;
      summary.dataset.error = String(!allocation.valid);
    } catch (caught) { const error = failure(caught);
      summary.textContent = error.message;
      summary.dataset.error = 'true';
    }
  }
  function bundleRow(item = { product: '', quantity: 1 }) {
    const choices: Record<string, string> = { '': '选择子商品' };
    for (const p of catalog().products) if (p.key !== product().key && p.kind !== 'bundle' && p.kind !== 'raffle' && p.kind !== 'donation' && p.mode === 'direct' && !p.variants?.length) choices[p.key] = `${p.title}（${kinds[p.kind]}）`;
    return `<div class="sa-bundle-row" data-shop-bundle-item>${field('bundle_product', '包含的商品', item.product, { options: choices, required: true })}${field('bundle_quantity', '数量', item.quantity, { type: 'number', required: true, min: 1, max: 100 })}<button type="button" class="sa-remove" data-shop-remove-row aria-label="移除此组合商品">移除</button></div>`;
  }
  function raffleLimitFields(plan: Partial<ShopRafflePlan>) {
    const activities = Object.fromEntries([['', '独立商品'], ...(catalog().raffle_campaigns || []).map(c => [c.key, c.title])]);
    return `<div class="sa-subsection"><h4>券码数量与限购</h4>
      <p class="sa-hint">每购买 1 份发放 1 张码。以下各项可任意组合，填 0 表示此项不限。实际可买数量取库存与各项剩余额度中的最小值。</p>
      <div class="sa-grid">${field('raffle_campaign', '所属抽奖活动', plan.campaign || '', { options: activities, hint: '在「商城设置」管理共享活动。切换活动后，新订单单独计数。' })}
        ${capField('raffle_tickets', '本商品券码总量（张）', plan.ticket_limit)}${capField('raffle_user', '本商品每人限购（张）', plan.user_limit)}${capField('raffle_order', '每单限购（张）', plan.order_limit, 99)}</div></div>`;
  }
  function renderEditor(p: ShopAdminProduct) {
    const places = precision(p.currency), installment = p.installment || { periods: [3, 6, 12], interval_days: 30, interest_bps: 0 }, group = p.group || { size: 2, price: p.price, hours: 24 }, raffle = p.raffle || { mode: 'instant', draw_at: zeroDate, winners: 1, win_bps: 100, prize: '', tiers: [] };
    const currencies = Object.fromEntries(state.currencies.map(item => [item.key, `${item.name}（精度 ${item.precision}）`]));
    $fieldset('[data-shop-product-fields]').innerHTML = `<section class="sa-form-section"><h3>基本信息</h3><div class="sa-grid">${field('title', '商品名称', p.title, { required: true, maxlength: 80 })}${field('key', '商品编号', p.key, { required: true, maxlength: 64, readonly: state.editing >= 0, hint: '使用小写字母、数字和连字符；保存后保持稳定。' })}${field('kind', '交付类型', p.kind, { options: kinds })}${field('mode', '购买方案', p.mode, { options: modes })}${field('category', '分类', p.category, { maxlength: 80 })}${field('currency', '计价货币', p.currency, { options: currencies })}${field('summary', '商品介绍', p.summary, { multiline: true, wide: true, maxlength: 4000 })}${field('image', '商品封面地址', p.image, { wide: true, maxlength: 2000, hint: '使用媒体库图片地址或 HTTPS 图片地址。' })}${field('images', '商品图组', (p.images || []).join('\n'), { multiline: true, wide: true, rows: 4, maxlength: 24012, hint: '每行一个图片地址，含封面最多 12 张；重复地址会自动合并。' })}${field('detail_images', '详情长图', (p.detail_images || []).join('\n'), { multiline: true, wide: true, rows: 4, maxlength: 24012, hint: '每行一个图片地址，最多 12 张，按顺序显示在商品详情中。' })}<label class="sa-switch sa-wide"><input type="checkbox" name="enabled" ${p.enabled ? 'checked' : ''}><span>上架此商品</span></label></div></section>
      <section class="sa-form-section"><h3>价格与库存</h3><div class="sa-grid">${field('price', '售价', moneyText(p.price, places), { required: true, hint: '输入所选币种的金额，如 19.90。' })}${field('compare_at', '划线参考价', moneyText(p.compare_at, places), { required: true, hint: '不展示参考价时填 0。' })}${field('stock', '库存', p.stock, { type: 'number', min: -1, max: 1000000000, required: true, hint: '-1 表示不限量；卡密还受可用卡密数量限制。' })}<div data-shop-kinds="physical,bundle">${field('shipping_fee', '每单运费', moneyText(p.shipping_fee, places), { required: true })}</div></div><div class="sa-subsection" data-shop-kinds="virtual,physical,cdk"><h4>商品规格</h4><p class="sa-hint">可为每个规格单独设置价格和库存。不添加规格时使用商品价格。</p><div data-shop-variants>${(p.variants || []).map(v => variantRow(v, places)).join('')}</div><button type="button" class="admin-button admin-button-secondary sa-inline-add" data-shop-add-variant>添加规格</button></div></section>
      <section class="sa-form-section" data-shop-kinds="virtual"><h3>虚拟内容交付</h3>${field('delivery', '付款后显示的内容', p.delivery || '', { multiline: true, rows: 5, maxlength: 8000, hint: '填写下载链接、使用说明或授权信息，仅付款完成后向买家展示。' })}</section>
      <section class="sa-form-section" data-shop-kinds="cdk"><h3>卡密库存</h3>${field('codes', '可用卡密（每行一个）', (p.codes || []).join('\n'), { multiline: true, rows: 7, maxlength: 500000, hint: '保存剩余可用卡密。每个卡密只发放一次；已售出的卡密不会显示在这里。' })}</section>
      <section class="sa-form-section" data-shop-kinds="donation"><h3>捐赠金额</h3>${field('max_donation', '单笔捐赠金额上限', moneyText(p.max_donation || p.price, places), { required: true, hint: '商品售价为最低捐赠金额，买家可在上下限之间自定义金额。' })}</section>
      <section class="sa-form-section" data-shop-kinds="bundle"><h3>捆绑包内容</h3><p class="sa-hint">一次购买包含多个商品，订单会分别预留库存并交付。</p><div data-shop-bundle>${(p.bundle || []).map(bundleRow).join('')}</div><button class="admin-button admin-button-secondary sa-inline-add" type="button" data-shop-add-bundle>添加组合商品</button></section>
      <section class="sa-form-section" data-shop-kinds="raffle"><h3>抽奖规则</h3><div class="sa-grid">${field('raffle_mode', '开奖方式', raffle.mode, { options: { instant: '付款后即时开奖', scheduled: '到指定时间统一开奖' } })}${field('raffle_prize', '奖品说明', raffle.prize, { maxlength: 1000, required: true })}<div data-shop-single data-shop-raffle="instant">${field('win_rate', '中奖概率（%）', moneyText(raffle.win_bps), { required: true, hint: '例如 1.50 表示每张抽奖码 1.5% 的中奖概率。' })}</div><div data-shop-raffle="scheduled">${field('draw_at', '统一开奖时间', dateInput(raffle.draw_at), { type: 'datetime-local', required: true, hint: '按当前浏览器所在时区输入。' })}</div><div data-shop-single data-shop-raffle="scheduled">${field('winners', '中奖名额', raffle.winners || 1, { type: 'number', min: 1, max: 1000, required: true })}</div></div>
        <div class="sa-subsection"><h4>奖池明细</h4><p class="sa-hint">可添加最多 12 个奖项。即时开奖的剩余概率为未中奖；定时开奖参与码不足时优先分配靠前的奖项。不添加奖项时使用上方的单奖品规则。</p>
        <p class="sa-hint" data-shop-raffle="scheduled">更改开奖时间、奖品或名额顺序后，新订单按新一轮开奖，已有订单按购买时的规则开奖。</p>
        <div data-shop-tiers>${(raffle.tiers || []).map(tierRow).join('')}</div><button type="button" class="admin-button admin-button-secondary sa-inline-add" data-shop-add-tier>添加奖项</button><p class="sa-tier-summary" role="status" data-shop-tier-summary></p></div>${raffleLimitFields(raffle)}</section>
      <section class="sa-form-section" data-shop-modes="presale,deposit"><h3>预售与定金</h3><div class="sa-grid">${field('release_at', '预计交付时间', dateInput(p.release_at), { type: 'datetime-local', required: true, hint: '使用浏览器本地时间；到期且全部付清后开始交付。' })}<div data-shop-modes="deposit">${field('deposit', '每件商品定金', moneyText(p.deposit, places), { required: true })}</div><div data-shop-modes="deposit">${field('balance_due_at', '尾款到期时间', dateInput(p.balance_due_at), { type: 'datetime-local', required: true })}</div></div></section>
      <section class="sa-form-section" data-shop-modes="installment"><h3>分期方案</h3><div class="sa-grid">${field('periods', '可选期数', installment.periods.join(', '), { required: true, hint: '用逗号分隔，例如 3, 6, 12。' })}${field('interval_days', '每期间隔（天）', installment.interval_days, { type: 'number', required: true, min: 1, max: 365 })}${field('interest_rate', '每期平息费率（%）', moneyText(installment.interest_bps), { required: true, hint: '按原始商品金额逐期计息；这不是年化利率。设为 0 则免息。' })}</div><p class="sa-note-inline">买家下单前可查看每期本金、利息与总额。全部期款付清后交付商品。</p></section>
      <section class="sa-form-section" data-shop-modes="group"><h3>拼团规则</h3><div class="sa-grid">${field('group_price', '拼团价', moneyText(group.price, places), { required: true })}${field('group_size', '成团人数', group.size, { type: 'number', required: true, min: 2, max: 100 })}${field('group_hours', '成团时限（小时）', group.hours, { type: 'number', required: true, min: 1, max: 720 })}</div><p class="sa-note-inline">成团后交付；未按时成团的已付款订单进入退款核对流程。</p></section>`;
    syncEditorVisibility();
  }
  function syncEditorVisibility() {
    const fields = controls(productForm), kind = fields('kind').value;
    if (['raffle', 'donation'].includes(kind)) fields('mode').value = 'direct';
    const mode = fields('mode').value;
    for (const section of productForm.querySelectorAll<HTMLElement>('[data-shop-kinds]')) section.hidden = !(section.dataset.shopKinds || '').split(',').includes(kind);
    for (const section of productForm.querySelectorAll<HTMLElement>('[data-shop-modes]')) section.hidden = !(section.dataset.shopModes || '').split(',').includes(mode);
    const rows = [...productForm.querySelectorAll<HTMLElement>('[data-shop-tier]')], tierCount = rows.length;
    rows.forEach((row, index) => {
      query(row, '[data-tier-move=up]', HTMLButtonElement).disabled = index === 0;
      query(row, '[data-tier-move=down]', HTMLButtonElement).disabled = index === tierCount - 1;
    });
    $button('[data-shop-add-tier]').disabled = readOnly || tierCount >= 12;
    for (const section of productForm.querySelectorAll<HTMLElement>('[data-shop-raffle]')) {
      section.hidden = section.dataset.shopRaffle !== fields('raffle_mode').value || (section.hasAttribute('data-shop-single') && tierCount > 0);
    }
    for (const control of $fieldset('[data-shop-product-fields]').querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>('input,select,textarea')) control.disabled = [...$fieldset('[data-shop-product-fields]').querySelectorAll<HTMLElement>('[data-shop-kinds],[data-shop-modes],[data-shop-raffle]')].some(section => section.hidden && section.contains(control));
    fields('mode').disabled = ['raffle', 'donation'].includes(kind);
    refreshRaffleSummary();
  }
  function servicePreviewProduct(): ServiceProduct | null {
    if (!state.product) return null;
    const p: ServiceProduct & { mode: string } = { ...state.product }, fields = controls(productForm), value = (name: string) => fields(name)?.value || '';
    p.kind = selectedKind(value('kind')); p.mode = value('mode') || p.mode; p.currency = value('currency') || p.currency; p.precision = precision(p.currency);
    try { p.shipping_fee = moneyUnits(value('shipping_fee') || '0', p.precision); } catch { p.shipping_fee = '0'; }
    for (const key of ['release_at', 'balance_due_at'] as const) { const input = value(key); p[key] = input && Number.isFinite(Date.parse(input)) ? new Date(input).toISOString() : ''; }
    p.group = p.mode === 'group' ? { size: Number(value('group_size')), hours: Number(value('group_hours')) } : null;
    p.installment = p.mode === 'installment' ? { periods: value('periods').split(/[,，\s]+/).filter(Boolean), interval_days: Number(value('interval_days')), interest_bps: Number(value('interest_rate')) * 100 } : null;
    p.raffle = p.kind === 'raffle' ? { mode: value('raffle_mode'), draw_at: value('draw_at'), winners: Number(value('winners')), win_bps: Number(value('win_rate')) * 100 } : null;
    if (p.raffle) {
      const key = p.raffle.mode === 'instant' ? 'win_bps' : 'winners';
      try { p.raffle[key] = editorAllocation(p.raffle.mode).total; } catch { p.raffle[key] = 0; }
    }
    return p;
  }
  function collectProduct(): ShopAdminProduct {
    const p = structuredClone(product()), fields = controls(productForm), text = (name: string) => fields(name).value.trim(), amount = (name: string) => moneyUnits(text(name), precision(fields('currency').value));
    const integer = (name: string, min: number, max: number) => { const value = text(name); if (!/^-?\d+$/.test(value) || !Number.isSafeInteger(Number(value)) || Number(value) < min || Number(value) > max) throw new Error('数量或库存超出允许范围。'); return Number(value); };
    const timestamp = (name: 'release_at' | 'balance_due_at') => raffleDrawTime(text(name), product()[name]);
    p.key = text('key'); if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(p.key)) throw new Error('商品编号请使用小写字母、数字、连字符或下划线。');
    p.title = text('title'); p.summary = text('summary'); p.category = text('category'); p.image = text('image'); p.kind = selectedKind(text('kind')); p.mode = selectedMode(text('mode')); p.currency = text('currency'); p.precision = precision(p.currency); p.enabled = inputControl(fields('enabled')).checked;
    const imageList = (name: string) => [...new Set(text(name).split(/\r?\n/).map(value => value.trim()).filter(Boolean))];
    p.images = imageList('images').filter(value => value !== p.image); p.detail_images = imageList('detail_images'); p.description_source = text('description_source'); p.description = '';
    p.services = serviceEditor.collect();
    if (new TextEncoder().encode(p.description_source).length > 262144) throw new Error('商品详细介绍超过 256 KiB，请缩短后保存。');
    if (p.images.length + (p.image ? 1 : 0) > 12 || p.detail_images.length > 12) throw new Error('商品图组和详情长图分别最多支持 12 张。');
    p.price = amount('price'); p.compare_at = amount('compare_at'); p.stock = String(integer('stock', -1, 1000000000)); p.shipping_fee = ['physical', 'bundle'].includes(p.kind) ? amount('shipping_fee') : '0';
    p.variants = ['virtual', 'physical', 'cdk'].includes(p.kind) ? [...productForm.querySelectorAll<HTMLElement>('[data-shop-variant]')].map(row => {
      const get = (name: string) => queryControl(row, `[name="${name}"]`).value.trim();
      const stock = get('variant_stock'); if (!/^-?\d+$/.test(stock) || Number(stock) < -1 || Number(stock) > 1000000000) throw new Error('规格库存格式无效。');
      return { key: get('variant_key'), title: get('variant_title'), image: get('variant_image'), price: moneyUnits(get('variant_price'), precision(p.currency)), stock };
    }) : [];
    p.delivery = p.kind === 'virtual' ? text('delivery') : '';
    p.codes = p.kind === 'cdk' ? text('codes').split(/\r?\n/).map(code => code.trim()).filter(Boolean) : [];
    if (new Set(p.codes).size !== p.codes.length) throw new Error('卡密列表中存在重复值，请移除重复卡密。');
    p.bundle = p.kind === 'bundle' ? [...productForm.querySelectorAll<HTMLElement>('[data-shop-bundle-item]')].map(row => ({ product: queryControl(row, '[name="bundle_product"]').value, quantity: Number(queryControl(row, '[name="bundle_quantity"]').value) })) : [];
    if (p.kind === 'bundle' && p.bundle.length === 0) throw new Error('捆绑包至少需要包含一个商品。');
    p.max_donation = p.kind === 'donation' ? amount('max_donation') : '0';
    p.release_at = ['presale', 'deposit'].includes(p.mode) ? timestamp('release_at') : zeroDate;
    p.deposit = p.mode === 'deposit' ? amount('deposit') : '0'; p.balance_due_at = p.mode === 'deposit' ? timestamp('balance_due_at') : zeroDate;
    p.installment = null; p.group = null; p.raffle = null;
    if (p.mode === 'installment') {
      const periods = text('periods').split(/[,，\s]+/).filter(Boolean).map(Number);
      if (!periods.length || periods.some(n => !Number.isInteger(n) || n < 2 || n > 36) || new Set(periods).size !== periods.length) throw new Error('分期期数请输入 2 至 36 之间的不重复整数。');
      const interestBPS = Number(moneyUnits(text('interest_rate'))); if (interestBPS > 10000) throw new Error('每期费率不能超过 100%。');
      p.installment = { periods: periods.sort((a, b) => a - b), interval_days: integer('interval_days', 1, 365), interest_bps: interestBPS };
    }
    if (p.mode === 'group') p.group = { size: integer('group_size', 2, 100), price: amount('group_price'), hours: integer('group_hours', 1, 720) };
    if (p.kind === 'raffle') {
      const mode = selectedRaffle(text('raffle_mode')), instant = mode === 'instant';
      const allocation = editorAllocation(mode);
      if (!allocation.valid) throw new Error(allocation.message);
      const tiers = allocation.rows.map((row, i) => ({
        prize: queryControl(row, '[name=tier_prize]').value.trim(),
        [instant ? 'win_bps' : 'winners']: required(allocation.amounts[i])
      }));
      if (new Set(tiers.map(t => t.prize)).size !== tiers.length) throw new Error('请为每个奖项填写不同的名称。');
      const previous = product().raffle;
      p.raffle = { mode, draw_at: instant ? zeroDate : raffleDrawTime(text('draw_at'), previous?.draw_at),
        winners: instant ? (previous?.mode === mode ? previous.winners : 1) : allocation.total,
        win_bps: instant ? allocation.total : (previous?.mode === mode ? previous.win_bps : 0), prize: text('raffle_prize'), tiers: [], campaign: '', ticket_limit: 0, user_limit: 0, order_limit: 0 };
      p.raffle.campaign = text('raffle_campaign');
      p.raffle.ticket_limit = integer('raffle_tickets', 0, 1_000_000_000);
      p.raffle.user_limit = integer('raffle_user', 0, 1_000_000_000);
      p.raffle.order_limit = integer('raffle_order', 0, 99);
      if (tiers.length) p.raffle.tiers = tiers;
    }
    return p;
  }
  function showOrder(id: string, opener: HTMLElement | null) {
    const order = state.orders.find(o => o.id === id); if (!order) return;
    state.order = order; orderOpener = opener;
    const schedule = order.schedule || [], address = order.address || {}, walletRefund = String(order.currency || '').startsWith('wallet:');
    const facts = [['订单编号', order.id], ['商品', order.title], ['交付类型', kinds[order.kind] || order.kind], ['购买方案', modes[order.mode] || order.mode], ['订单总额', price(order.total, order.currency)], ['已付款', price(order.paid_amount, order.currency)], ['创建时间', date(order.created_at)], ['状态', statuses[order.status] || order.status]];
    $('[data-shop-order-detail]').innerHTML = `<dl class="sa-order-facts">${facts.map(([label, value]) => `<div><dt>${esc(label)}</dt><dd>${esc(value)}</dd></div>`).join('')}</dl>${schedule.length ? `<h3>付款计划</h3><div class="sa-table-wrap"><table class="sa-table"><thead><tr><th>期次</th><th>到期日</th><th>应付</th><th>状态</th></tr></thead><tbody>${schedule.map((part, i) => `<tr><td>第 ${i + 1} 期</td><td>${date(part.due_at)}</td><td>${price(part.amount, order.currency)}</td><td>${part.paid_at && !part.paid_at.startsWith('0001-') ? '已付' : '待付'}</td></tr>`).join('')}</tbody></table></div>` : ''}${address.name ? `<h3>收货信息</h3><div class="sa-delivery">${esc(address.name)} · ${esc(address.phone)}<br>${esc(address.detail)}</div>` : ''}${order.carrier || order.tracking ? `<h3>物流信息</h3><div class="sa-delivery">${esc(order.carrier)} · ${esc(order.tracking)}</div>` : ''}${order.deliveries.length ? `<h3>交付记录</h3>${order.deliveries.map(item => `<div class="sa-delivery">${esc(item.content || item.title || item.kind)}</div>`).join('')}` : ''}${order.status === 'refund_requested' ? `<h3>买家退款申请</h3><div class="sa-delivery">${esc(order.refund_note)}</div>` : ''}${order.status === 'refund_required' ? `<p class="sa-error-note">${walletRefund ? '此订单的站内钱包退款待完成。系统会按原付款记录重试退款，也可在下方手动重试。' : '此订单需要退款核对。请在支付渠道处理退款，并核对原始支付流水；此状态不表示渠道已经完成退款。'}</p>` : ''}${order.refund_review ? `<p class="sa-note-inline">审核记录：${esc(order.refund_review)}</p>` : ''}`;
    const shipping = $form('[data-shop-shipment]'); shipping.hidden = order.status !== 'awaiting_shipment'; shipping.reset();
    if (order.tickets?.length) $('[data-shop-order-detail]').insertAdjacentHTML('beforeend', `<h3>抽奖结果</h3>${order.tickets.map(ticket => `<div class="sa-delivery"><strong>${ticket.status === 'won' ? '已中奖' : ticket.status === 'lost' ? '未中奖' : '待开奖'}</strong><code>${esc(ticket.id)}</code><p>${esc(ticket.prize)}</p><small>${date(ticket.drawn_at && !ticket.drawn_at.startsWith('0001-') ? ticket.drawn_at : ticket.draw_at)}</small></div>`).join('')}`);
    for (const ticket of order.tickets || []) {
      if (ticket.delivery) { $('[data-shop-order-detail]').insertAdjacentHTML('beforeend', `<div class="sa-delivery"><h3>奖品交付记录</h3><code>${esc(ticket.id)}</code><p>${esc(ticket.delivery)}</p><small>${date(ticket.delivered_at)}</small></div>`); continue; }
      if (ticket.status !== 'won' || order.status !== 'completed') continue;
      const form = document.createElement('form'); form.className = 'sa-shipment';
      form.innerHTML = `<h3>交付奖品</h3><p class="sa-note-inline">抽奖码：${esc(ticket.id)}</p><fieldset ${readOnly ? 'disabled' : ''}>${field('content', '奖品内容或物流说明', '', { required: true, multiline: true, maxlength: 4000, hint: '填写下载地址、兑换方式或物流单号；保存后买家可在订单中查看。' })}<p class="sa-editor-status" role="status"></p><button type="submit" class="admin-button admin-button-primary">确认交付</button></fieldset>`;
      form.addEventListener('submit', event => {
        event.preventDefault(); void runOrder(form, order, { kind: 'deliver', ticket: ticket.id, content: controls(form)('content').value.trim() }, '奖品交付信息已保存。');
      });
      $('[data-shop-order-detail]').append(form);
    }
    if (order.refund_reference) $('[data-shop-order-detail]').insertAdjacentHTML('beforeend', `<h3>退款记录</h3><div class="sa-delivery">${esc(order.refund_reference)}<p>${esc(order.refund_note)}</p></div>`);
    root.querySelector('[data-shop-refund-review]')?.remove();
    if(order.status==='refund_requested'){
      const form=document.createElement('form');form.className='sa-shipment';form.dataset.shopRefundReview='';
      form.innerHTML=`<fieldset ${readOnly?'disabled':''}>${field('note','审核说明','',{multiline:true,maxlength:1000})}<p class="sa-editor-status" role="status"></p><div class="sa-actions"><button type="submit" name="approved" value="true" class="admin-button admin-button-primary">批准退款</button><button type="submit" name="approved" value="false" class="admin-button admin-button-secondary">驳回申请</button></div></fieldset>`;
      form.addEventListener('submit', event => {
        event.preventDefault(); if (!(event.submitter instanceof HTMLButtonElement)) return;
        const approved = event.submitter.value === 'true';
        void runOrder(form, order, { kind: 'review-refund', approved, note: controls(form)('note').value.trim() }, approved ? '退款申请已批准，请按订单状态继续核对退款。' : '退款申请已驳回，订单状态已更新。');
      });
      orderDialog.append(form);
    }
    root.querySelector('[data-shop-refund-form]')?.remove();
    if (order.status === 'refund_required') {
      const form = document.createElement('form'); form.className = 'sa-shipment'; form.dataset.shopRefundForm = '';
      form.innerHTML = walletRefund ? `<h3>退回站内钱包</h3><p class="sa-note-inline">按原付款记录退回对应站内钱包。重复重试不会重复退款，到账后订单才会显示已退款。</p><fieldset ${readOnly ? 'disabled' : ''}><p data-shop-refund-status class="sa-editor-status" role="status"></p><button class="admin-button admin-button-primary" type="submit">重试钱包退款</button></fieldset>` : `<h3>登记渠道退款结果</h3><p class="sa-note-inline">请先在原支付渠道完成退款并核对金额，再登记可追溯凭证。此操作不会发起自动退款。</p><fieldset ${readOnly ? 'disabled' : ''}>${field('reference','退款流水 / 凭证编号','',{required:true,maxlength:200})}${field('note','核对说明',order.refund_note || '',{required:true,multiline:true,maxlength:1000})}<p data-shop-refund-status class="sa-editor-status" role="status"></p><button class="admin-button admin-button-primary" type="submit">已核对渠道退款，登记完成</button></fieldset>`;
      form.addEventListener('submit', event => {
        event.preventDefault(); const action: ShopOrderAction = walletRefund ? { kind: 'refund' } : { kind: 'refund', reference: controls(form)('reference').value.trim(), note: controls(form)('note').value.trim() };
        void runOrder(form, order, action, walletRefund ? '退款已退回站内钱包。' : '退款核对结果已保存。');
      });
      orderDialog.append(form);
    }
    root.querySelector('[data-shop-pending]')?.remove();
    try {
      const pending = actions.read(order.id);
      if (pending) {
        const form = document.createElement('form'); form.dataset.shopPending = ''; form.className = 'sa-shipment';
        const action = pending.action, detail = action.kind === 'ship' ? action.carrier + ' · ' + action.tracking : action.kind === 'deliver' ? action.content : action.kind === 'review-refund' ? (action.approved ? '批准退款：' : '驳回申请：') + action.note : (action.reference || '站内钱包退款') + ' ' + (action.note || '');
        form.innerHTML = '<h3>上次处理尚待核对</h3><p class="sa-note-inline">' + esc(detail) + '</p><fieldset' + (readOnly ? ' disabled' : '') + '><p role="status" class="sa-editor-status"></p><button type="submit" class="admin-button admin-button-primary">核对并重试原操作</button></fieldset>';
        form.addEventListener('submit', event => { event.preventDefault(); void runOrder(form, pending.order, action, '上次处理结果已核对。'); });
        orderDialog.append(form);
      }
    } catch (error) {
      message(failure(error).message, true);
      orderDialog.querySelectorAll('fieldset').forEach(fieldset => { fieldset.disabled = true; });
    }
    message('', false, $('[data-shop-shipment-status]')); orderDialog.showModal();
  }
  root.addEventListener('click', async event => {
    if (retired || !(event.target instanceof Element)) return; const target = event.target;
    const reviewStatus=target.closest<HTMLElement>('[data-shop-review-status]'),reviewRating=target.closest<HTMLElement>('[data-shop-review-rating]'),reviewPage=target.closest<HTMLElement>('[data-shop-review-page]');
    if ((reviewStatus || reviewRating || reviewPage || target.closest('[data-shop-review-refresh]')) && !state.busy) {
      try { await loadReviews({ page: reviewPage ? Number(reviewPage.dataset.shopReviewPage) : 1, status: reviewStatus?.dataset.shopReviewStatus, rating: reviewRating ? Number(reviewRating.dataset.shopReviewRating) : undefined }); }
      catch (error) { message(failure(error).message, true); }
    }
    if (target.closest<HTMLElement>('[data-shop-reload]') && !state.busy) { if (state.dirty) { message('有未保存的修改，请先保存或取消编辑后重新读取。', true); return; } await load(); }
    const move = target.closest<HTMLButtonElement>('[data-tier-move]');
    if (move && !readOnly && !state.busy) {
      const row = required(move.closest<HTMLElement>('[data-shop-tier]')), up = move.dataset.tierMove === 'up';
      const neighbor = up ? row.previousElementSibling : row.nextElementSibling;
      if (neighbor) {
        required(row.parentElement).insertBefore(up ? row : neighbor, up ? neighbor : row);
        syncEditorVisibility(); touch('product');
        (move.disabled ? query(row, '[data-tier-move]:not(:disabled)', HTMLButtonElement) : move)?.focus();
      }
    }
    const addCampaign = target.closest<HTMLElement>('[data-shop-add-campaign]');
    if (addCampaign && !readOnly && !state.busy && $('[data-shop-campaigns]').childElementCount < 100) {
      const list = $('[data-shop-campaigns]');
      list.insertAdjacentHTML('beforeend', campaignRow({ key: `draw-${crypto.randomUUID()}`, title: '', ticket_limit: 0, user_limit: 0 }));
      query(required(list.lastElementChild), 'input', HTMLInputElement).focus();
      syncCampaignList(); touch('settings');
    }
    const removeCampaign = target.closest<HTMLButtonElement>('[data-shop-remove-campaign]');
    if (removeCampaign && !removeCampaign.disabled && !readOnly && !state.busy) {
      removeCampaign.closest('[data-shop-campaign]')?.remove();
      syncCampaignList(); $button('[data-shop-add-campaign]').focus(); touch('settings');
    }
    if (target.closest<HTMLElement>('[data-shop-add-tier]') && !readOnly && !state.busy && productForm.querySelectorAll<HTMLElement>('[data-shop-tier]').length < 12) {
      $('[data-shop-tiers]').insertAdjacentHTML('beforeend', tierRow());
      syncEditorVisibility(); touch('product'); serviceEditor.refreshPreview();
      query(required($('[data-shop-tiers]').lastElementChild), 'input', HTMLInputElement).focus();
    }
    if (target.closest<HTMLElement>('[data-shop-add-variant]') && !readOnly && !state.busy) { $('[data-shop-variants]').insertAdjacentHTML('beforeend', variantRow()); touch('product'); }
    if (target.closest<HTMLElement>('[data-shop-add-bundle]') && !readOnly && !state.busy) { $('[data-shop-bundle]').insertAdjacentHTML('beforeend', bundleRow()); touch('product'); }
    const remove = target.closest<HTMLElement>('[data-shop-remove-row]'); if (remove && !readOnly && !state.busy) { remove.closest('[data-shop-variant],[data-shop-bundle-item],[data-shop-tier]')?.remove(); syncEditorVisibility(); touch('product'); serviceEditor.refreshPreview(); }
    const tab = target.closest<HTMLElement>('[data-shop-tab]'); if (tab) switchTab(tab.dataset.shopTab || 'products');
    const couponNew=target.closest<HTMLElement>('[data-shop-coupon-new]');if(couponNew&&!readOnly&&!state.busy)setCouponForm();
    const couponEdit=target.closest<HTMLElement>('[data-shop-coupon-edit]');if(couponEdit&&!readOnly&&!state.busy)setCouponForm(state.coupons[Number(couponEdit.dataset.shopCouponEdit)]);
    const couponCancel=target.closest<HTMLElement>('[data-shop-coupon-cancel]');if(couponCancel&&!state.busy){$form('[data-shop-coupon-form]').hidden=true;state.editingCoupon='';clearDraft('coupon');}
    const couponToggle=target.closest<HTMLElement>('[data-shop-coupon-toggle]');if(couponToggle&&!readOnly&&!state.busy){const next=structuredClone(state.coupons);required(next[Number(couponToggle.dataset.shopCouponToggle)]).enabled=!required(next[Number(couponToggle.dataset.shopCouponToggle)]).enabled;busy(true);try{await saveCoupons(next,state.couponVersion);message('优惠券状态已更新。')}catch (caught) { const error = failure(caught);message(error.message,true)}finally{busy(false)}}
    const couponDelete=target.closest<HTMLElement>('[data-shop-coupon-delete]');if(couponDelete&&!readOnly&&!state.busy&&window.confirm('删除这张优惠券？已领取的优惠券无法删除。')){const next=structuredClone(state.coupons);next.splice(Number(couponDelete.dataset.shopCouponDelete),1);busy(true);try{await saveCoupons(next,state.couponVersion);message('优惠券已删除。')}catch (caught) { const error = failure(caught);message(error.message,true)}finally{busy(false)}}
    const edit = target.closest<HTMLElement>('[data-shop-edit]'); if (edit) openEditor(Number(edit.dataset.shopEdit), edit);
    const add = target.closest<HTMLElement>('[data-shop-add]'); if (add && !readOnly) openEditor(-1, add);
    if (target.closest<HTMLElement>('[data-shop-close]') && !state.busy) closeEditor();
    if (target.closest<HTMLElement>('[data-shop-order-close]') && !state.busy) { orderDialog.close(); orderOpener?.focus(); }
    const order = target.closest<HTMLElement>('[data-shop-order]'); if (order && !state.busy) showOrder(order.dataset.shopOrder || '', order);
    const toggle = target.closest<HTMLElement>('[data-shop-toggle]');
    if (toggle && !readOnly && !state.busy) {
      const next = structuredClone(catalog()), product = required(next.products[Number(toggle.dataset.shopToggle)]); product.enabled = !product.enabled;
      busy(true); try { await saveCatalog(next); message(`“${product.title}”已${product.enabled ? '上架' : '下架'}。`); } catch (caught) { const error = failure(caught); message(error.message, true); } finally { busy(false); }
    }
    if (target.closest<HTMLElement>('[data-shop-refresh-orders]') && !state.busy) { try { await loadOrders(); message('订单已更新。'); } catch (caught) { const error = failure(caught); message(error.message, true); } }
    const advance = target.closest<HTMLButtonElement>('[data-shop-advance]');
    if (advance && !readOnly && !state.busy) { busy(true); advance.disabled = true; try { const result = await api('/advance', 'POST', {}); if (result.ok !== true) throw new Error('到期任务结果未确认，请先重新读取订单'); message('到期任务已更新。'); try { await loadOrders(); } catch { message('到期任务已更新；订单列表暂未刷新。'); } } catch (caught) { const error = failure(caught); message(error.message, true); } finally { busy(false); advance.disabled = readOnly || retired; } }
  });
  for (const selector of ['[data-shop-search]', '[data-shop-kind-filter]']) $(selector).addEventListener('input', renderProducts);
  for (const selector of ['[data-shop-order-search]', '[data-shop-order-filter]']) $(selector).addEventListener('input', renderOrders);
  productForm.addEventListener('input', event => { if (!(event.target instanceof Element)) return; const target=event.target; if (target.matches('[data-service-search]')) return; touch('product'); if (target.closest<HTMLElement>('[data-shop-kinds=raffle]')) refreshRaffleSummary(); if (target.closest<HTMLElement>('[data-shop-product-fields]')) serviceEditor.refreshPreview(); });
  productForm.addEventListener('change', event => { if (!(event.target instanceof HTMLInputElement || event.target instanceof HTMLSelectElement)) return; const target=event.target; if (['kind','mode','raffle_mode'].includes(target.name)) syncEditorVisibility(); });
  $form('[data-shop-coupon-form]').addEventListener('input',()=>{touch('coupon')});
  $form('[data-shop-coupon-form]').addEventListener('change',event=>{if(!(event.target instanceof HTMLInputElement || event.target instanceof HTMLSelectElement))return;const target=event.target;if(target.name==='kind'||target.name==='currency'){const f=controls($form('[data-shop-coupon-form]'));const pct=f('kind').value==='percent';$('[data-shop-coupon-value-hint]').textContent=pct?'填写折扣百分比，如 8.5 表示优惠 8.5%。':`按 ${select(f('currency')).selectedOptions[0]?.textContent||'所选货币'} 输入立减金额。`;}});
  $form('[data-shop-coupon-form]').addEventListener('submit', async event => {
    event.preventDefault(); if (readOnly || retired || state.busy) return;
    const revision = revisions.get('coupon');
    try {
      const coupon = collectCoupon(), base = required(couponBaseline), next = structuredClone(base.coupons), index = next.findIndex(c => c.code === state.editingCoupon);
      if (index < 0) next.push(coupon); else next[index] = coupon;
      busy(true); await saveCoupons(next, base.version); clearDraft('coupon', revision);
      couponBaseline = { version: state.couponVersion, coupons: structuredClone(state.coupons) };
      if (!drafts.has('coupon')) { state.editingCoupon = ''; $form('[data-shop-coupon-form]').hidden = true; }
      else { state.editingCoupon = coupon.code; inputControl(controls($form('[data-shop-coupon-form]'))('code')).readOnly = true; }
      message(drafts.has('coupon') ? '优惠券已保存，新的输入尚未提交。' : '优惠券规则已保存。');
    } catch (error) { message(failure(error).message, true, $('[data-shop-coupon-status]')); }
    finally { busy(false); }
  });
  editor.addEventListener('cancel', event => { if (state.busy) event.preventDefault(); else clearDraft('product'); });
  productForm.addEventListener('submit', async event => {
    event.preventDefault(); if (readOnly || state.busy) return;
    let product; try { product = collectProduct(); } catch (caught) { const error = failure(caught); if (error.message.includes('详细介绍')) editorTab('description'); if (error.message.includes('服务说明')) editorTab('services'); message(error.message, true, $('[data-shop-editor-status]')); return; }
    const revision = revisions.get('product'), base = required(productBaseline), next = structuredClone(base); if (state.editing < 0) next.products.push(product); else next.products[state.editing] = product;
    busy(true); message('正在保存商品…', false, $('[data-shop-editor-status]'));
    try {
      await saveCatalog(next, base); clearDraft('product', revision); productBaseline = structuredClone(catalog());
      if (!drafts.has('product')) closeEditor();
      else {
        state.editing = catalog().products.findIndex(p => p.key === product.key);
        const acknowledged = { ...product, description_author_id: required(catalog().products[state.editing]).description_author_id };
        // Controls still show the submitted values plus any newer typing. Keep
        // that baseline so an untouched stock field cannot undo a reservation.
        state.product = structuredClone(acknowledged); productBaseline.products[state.editing] = structuredClone(acknowledged);
      }
      message('“' + product.title + '”已保存。' + (drafts.has('product') ? '保存期间的新输入尚未提交。' : ''));
    }
    catch (caught) { const error = failure(caught); message(error.message, true, $('[data-shop-editor-status]')); }
    finally { busy(false); }
  });
  $form('[data-shop-settings]').addEventListener('submit', async event => {
    event.preventDefault(); if (readOnly || state.busy) return;
    const fields = controls($form('[data-shop-settings]')), revision = revisions.get('settings'), base = required(settingsBaseline), next = structuredClone(base);
    try {
      next.raffle_campaigns = [...$form('[data-shop-settings]').querySelectorAll<HTMLElement>('[data-shop-campaign]')].map(row => {
        const cap = (name: string) => {
          const value = queryControl(row, `[name=${name}]`).value;
          if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value)) || Number(value) > 1_000_000_000) throw new Error('活动额度请输入 0 至 1000000000 的整数；0 表示不限量。');
          return Number(value);
        };
        return { key: required(row.dataset.shopCampaign), title: queryControl(row, '[name=campaign_title]').value.trim(), ticket_limit: cap('campaign_tickets'), user_limit: cap('campaign_user') };
      });
    } catch (caught) { const error = failure(caught); message(error.message, true); return; }
    if (next.raffle_campaigns.some(c => !c.title)) { message('请填写抽奖活动名称。', true); return; }
    next.enabled = inputControl(fields('enabled')).checked; next.order_minutes = Number(fields('order_minutes').value); next.terms = fields('terms').value.trim(); next.refund_policy = fields('refund_policy').value.trim();
    busy(true); try { await saveCatalog(next, base); clearDraft('settings', revision); settingsBaseline = { ...structuredClone(next), version: catalog().version }; if (!drafts.has('settings')) renderSettings(); message(drafts.has('settings') ? '商城设置已保存；新的输入尚未提交。' : '商城设置已保存。'); } catch (caught) { const error = failure(caught); message(error.message, true); } finally { busy(false); }
  });
  $form('[data-shop-settings]').addEventListener('input', () => { touch('settings'); });
  $form('[data-shop-shipment]').addEventListener('submit', event => {
    event.preventDefault(); if (!state.order) return;
    const form = $form('[data-shop-shipment]'), fields = controls(form);
    void runOrder(form, state.order, { kind: 'ship', carrier: fields('carrier').value.trim(), tracking: fields('tracking').value.trim() }, '物流信息已保存，订单已发货。');
  });
  orderDialog.addEventListener('cancel', event => { if (state.busy) event.preventDefault(); });
  window.addEventListener('beforeunload', event => { if (state.dirty) { event.preventDefault(); event.returnValue = ''; } });
  function retire() {
    if (retired) return;
    retired = true; catalogSequence++; couponSequence++; state.sequence++; state.reviewSerial++; client.cancel(); busy(true); serviceEditor.destroy();
    editor.close(); orderDialog.close(); root.querySelectorAll('fieldset').forEach(el => { el.disabled = true; }); drafts.clear(); state.dirty = false; state.catalog = null; state.product = null; state.order = null; state.orders = []; state.reviews = [];
    for (const selector of ['[data-shop-products]', '[data-shop-orders]', '[data-shop-reviews]', '[data-shop-order-detail]']) $(selector).replaceChildren();
    for (const control of root.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>('input,textarea')) if (control.name === 'codes' || control.name === 'delivery' || control.name === 'description_source') control.value = '';
    $('[data-shop-workspace]').hidden = true;
    $('[data-shop-status]').textContent = '页面或登录状态已变化，请刷新后重新读取商城管理。';
  }
  document.addEventListener('chunnel:session', event => { if (event.detail?.authenticated !== true || String(event.detail.id || '') !== owner || event.detail.csrf_token !== csrf) retire(); });
  window.addEventListener('pagehide', retire);
  window.addEventListener('pageshow', event => { if (event.persisted) retire(); });
  async function load() {
    const serial = ++catalogSequence; busy(true);
    try {
      const result = adminConfig(await api(), readOnly); if (serial !== catalogSequence) return; if (result.csrf_token !== csrf) { retire(); return; } state.catalog = result.catalog; state.currencies = result.currencies; state.serviceLibrary = result.service_library; if (result.csrf_token) root.dataset.csrf = result.csrf_token;
      await loadCoupons();
      renderSettings(); renderProducts(); $('[data-shop-workspace]').hidden = false;
      message(result.payment_enabled === false ? '请在“支付渠道”启用支付服务并配置可用渠道，用户才能在线付款。' : '');
      await loadOrders();
    } catch (caught) { const error = failure(caught); message(error.message, true); }
    finally { busy(false); }
  }
  load();
})();
