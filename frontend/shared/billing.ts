import type {} from './account-types';
import type {} from './currency-types';
import type { ClientSession, RequestOptions } from './newpress-client';
import { failure, node } from './ledger-data';
import { billingConfig, paymentOrder, paymentReceipt, billingPending, orderID, exponent, paymentStatuses as statuses } from './payment-data';
import type { BillingConfig, BillingDraft, BillingPending, PaymentProduct, PaymentOrder } from './payment-data';
(() => {
  'use strict';
  const host = document.querySelector<HTMLElement>('[data-billing-room]'), interactions = window.ChunnelInteractions, shared = window.ChunnelCurrency;
  if (!host || !interactions || !shared) return;
  const root = host, C = shared, services = interactions, { getSession } = interactions;
  const $ = <T extends HTMLElement = HTMLElement>(selector: string): T => { const el = root.querySelector<T>(selector); if (!el) throw new Error('页面缺少控件：' + selector); return el; };
  const input = (form: HTMLFormElement, name: string): HTMLInputElement => { const el = form.elements.namedItem(name); if (!(el instanceof HTMLInputElement)) throw new Error('页面缺少输入字段：' + name); return el; };
  const rule = $<HTMLSelectElement>('[data-billing-exchange-rule]');
  const endpoint = '/api/v1/social/billing';
  const purchase = $<HTMLFormElement>('[data-billing-purchase]'), exchange = $<HTMLFormElement>('[data-billing-exchange]');
  const confirmDialog = $<HTMLDialogElement>('[data-billing-confirm]'), orderDialog = $<HTMLDialogElement>('[data-billing-order-dialog]');
  const params = new URLSearchParams(location.search);
  const requestedPlan = params.get('plan') || '', requestedOrder = params.get('order') || '';
  const providers: Record<string, [string, string]> = { alipay: ['支', '支付宝'], alipay_f2f: ['支', '支付宝当面付'], wechat: ['微', '微信支付'], epay: ['付', '聚合支付'], linuxdo: ['LD', 'Linux DO Credit'], stripe: ['S', 'Stripe'], paypal: ['P', 'PayPal'], twocheckout: ['2', '2Checkout'] };
  let config: BillingConfig | null = null, owner = '', csrf = '', initialized = false, epoch = 0, serial = 0, orderSerial = 0, retired = false;
  let loading = false, sending = false, syncing = false;
  let pending: BillingPending | null = null, draft: BillingDraft | null = null, pendingBroken = false;
  let kind = requestedPlan || params.get('kind') === 'membership' ? 'membership' : 'recharge', productKey = '', channelKey = '', orders = new Map<string, PaymentOrder>();
  let currentOrder: PaymentOrder | null = null, paySignature = '', pollTimer = 0, polls = 0, deepLinkOpened = false;
  let orderRevision = 0, loadController: AbortController | undefined, orderController: AbortController | undefined;
  const revisions = new Map<string, number>(), controllers = new Set<AbortController>();
  function storeOrder(order: PaymentOrder) { orders.set(order.id, order); revisions.set(order.id, ++orderRevision); }
  async function api(path: string, options: RequestOptions = {}) {
    if (retired || !owner || !root.isConnected) throw new DOMException('页面已变化', 'AbortError');
    const active = epoch, controller = new AbortController(); controllers.add(controller);
    const abort = () => controller.abort(); options.signal?.addEventListener('abort', abort, { once: true }); if (options.signal?.aborted) abort();
    try { const data = await services.api(path, { ...options, signal: controller.signal }); if (active !== epoch || retired || controller.signal.aborted || !root.isConnected) throw new DOMException('页面已变化', 'AbortError'); return data; }
    catch (caught) { const error = failure(caught); if (active === epoch && [401, 403].includes(error.status || 0) && error.code?.startsWith('auth.')) identity({ authenticated: false }); throw error; }
    finally { controllers.delete(controller); options.signal?.removeEventListener('abort', abort); }
  }

  const set = (selector: string, text: string) => { $(selector).textContent = text; };
  const message = (selector: string, text = '', tone = '') => C.message($(selector), text, tone);
  const date = (value: string) => value && Number.isFinite(Date.parse(value)) && !value.startsWith('0001-') ? (window.ChunnelTime?.formatDateTime(value) || new Date(value).toLocaleString()) : '—';
  const coin = (key: string) => config?.currencies.find(item => item.key === key);
  const coinName = (key: string) => coin(key)?.name || key;
  const money = (amount: string, currency: string) => C.format(BigInt(amount), exponent(currency));
  const creditText = (p: PaymentProduct) => p.kind === 'membership' ? '按所选方案开通会员权益' : `到账 ${C.format(BigInt(p.credit), coin(p.target_currency)?.precision ?? 0)} ${coinName(p.target_currency)}`;
  const orderPath = (id: string) => endpoint + '/orders/' + encodeURIComponent(id);
  const storageKey = () => 'chunnel.billing.v1.pending.' + owner;
  function readPending(): BillingPending | null {
    pendingBroken = false;
    try { const raw = sessionStorage.getItem(storageKey()); if (!raw) return null; return billingPending(JSON.parse(raw), owner); }
    catch { pendingBroken = true; return null; }
  }
  function savePending(value: BillingDraft): BillingPending {
    const saved = billingPending({ ...value, owner, body: { ...value.body, request_id: value.body.request_id || C.nonce(), accepted: true } }, owner);
    try { sessionStorage.setItem(storageKey(), JSON.stringify(saved)); }
    catch { throw new Error('浏览器无法保存订单恢复信息，请允许本站使用会话存储后再试。'); }
    return saved;
  }
  function clearPending() { try { sessionStorage.removeItem(storageKey()); } catch { /* The confirmed order remains on the server. */ } pending = null; }
  function controls() {
    const blocked = retired || loading || sending || !!pending || pendingBroken || !owner || !config?.enabled;
    $<HTMLFieldSetElement>('[data-billing-purchase-fields]').disabled = blocked;
    $<HTMLFieldSetElement>('[data-billing-exchange-fields]').disabled = blocked;
    $<HTMLButtonElement>('[data-billing-purchase-submit]').disabled = blocked || !selectedProduct() || !selectedChannel();
    $<HTMLButtonElement>('[data-billing-exchange-submit]').disabled = blocked || !selectedRule();
    $<HTMLButtonElement>('[data-billing-refresh]').disabled = loading || sending;
    $<HTMLButtonElement>('[data-billing-reload]').disabled = loading || sending;
    $<HTMLButtonElement>('[data-billing-resume]').disabled = sending || loading;
    for (const el of root.querySelectorAll<HTMLButtonElement>('[data-billing-kind]')) el.disabled = sending;
    for (const el of root.querySelectorAll<HTMLButtonElement>('[data-billing-confirm-close], [data-billing-confirm-cancel]')) el.disabled = sending;
    $<HTMLButtonElement>('[data-billing-confirm-send]').disabled = sending || !draft || !owner;
    $<HTMLButtonElement>('[data-billing-confirm-send]').textContent = sending ? '正在确认…' : pending ? '重试原请求' : draft?.kind === 'exchange' ? '确认兑换并扣款' : '确认创建订单';
    $<HTMLButtonElement>('[data-billing-order-sync]').disabled = syncing || !currentOrder || !owner;
    $<HTMLButtonElement>('[data-billing-order-refresh]').disabled = syncing || !currentOrder || !owner;
    $('[data-billing-pending]').hidden = !pending;
    if (pending) set('[data-billing-pending-copy]', pending.title + ' · ' + pending.expected.amount + ' ' + coinName(pending.expected.currency));
  }
  function selectedProduct() { return config?.products.find(p => p.key === productKey && p.kind === kind); }
  function selectedChannel() { const p = selectedProduct(); return p && config?.channels.find(ch => ch.key === channelKey && ch.currency === p.currency); }
  function selectedRule() { return config?.exchanges.find(r => r.key === rule.value); }
  function availableProduct(p: PaymentProduct) { return p.kind === 'membership' || !!coin(p.target_currency)?.enabled; }
  function renderProducts() {
    if (!config) return;
    const products = config.products.filter(p => p.kind === kind);
    if (!products.some(p => p.key === productKey && availableProduct(p))) {
      productKey = (products.find(p => requestedPlan && p.plan === requestedPlan && availableProduct(p)) || products.find(p => availableProduct(p)))?.key || '';
    }
    set('[data-billing-products-title]', kind === 'membership' ? '选择会员方案' : '选择充值面额');
    set('[data-billing-products-count]', products.length ? products.length + ' 个可选方案' : '');
    const items = products.map(p => {
      const label = node('label', 'billing-product'), top = node('span', 'billing-product-top'), input = node('input');
      input.type = 'radio'; input.name = 'product'; input.value = p.key; input.checked = p.key === productKey; input.disabled = !availableProduct(p);
      top.append(node('span', 'billing-product-title', p.title), input);
      const price = node('span', 'billing-product-price'); price.append(node('strong', '', money(p.price, p.currency)), node('small', '', p.currency));
      label.append(top, price, node('span', 'billing-product-description', availableProduct(p) ? creditText(p) : '目标钱包当前不可用'));
      return label;
    });
    $('[data-billing-products]').replaceChildren(...(items.length ? items : [node('p', 'billing-empty', kind === 'membership' ? '会员支付方案还未开放，稍后再来看看。' : '充值面额还未开放，已有订单仍可在下方查看。')]));
    set('[data-billing-catalog-note]', kind === 'membership' ? '会员权益以站点公布的方案为准。购买前可在个人中心查看当前会员状态。' : '充值到账的是站内余额，可在个人中心查看收支明细。');
    renderCheckout();
  }
  function renderCheckout() {
    if (!config) return;
    const p = selectedProduct();
    set('[data-billing-selected-title]', p?.title || '请选择一项商品');
    set('[data-billing-total]', p ? money(p.price, p.currency) : '—');
    set('[data-billing-total-currency]', p?.currency || '');
    set('[data-billing-benefit]', p ? creditText(p) : '');
    const channels = p ? config.channels.filter(ch => ch.currency === p.currency) : [];
    if (!channels.some(ch => ch.key === channelKey)) channelKey = channels[0]?.key || '';
    $('[data-billing-channels]').replaceChildren(...channels.map(ch => {
      const label = node('label', 'billing-channel'), brand = providers[ch.provider] || ['付', '在线支付'];
      const copy = node('span', 'billing-channel-copy'); copy.append(node('strong', '', ch.name), node('small', '', brand[1]));
      const input = node('input'); input.type = 'radio'; input.name = 'channel'; input.value = ch.key; input.checked = ch.key === channelKey;
      label.append(node('span', 'billing-channel-mark', brand[0]), copy, input); return label;
    }));
    const channel = selectedChannel();
    set('[data-billing-channel-note]', !p ? '选定商品后显示可用渠道。' : !channels.length ? '此币种暂时没有可用的支付渠道，请联系管理员。' : channel?.provider === 'linuxdo' ? 'LDC 为 Linux DO 社区积分，请在渠道页面核对授权。' : '确认订单后，可打开渠道页面或扫描订单二维码付款。');
    input(purchase, 'accepted').checked = false;
    controls();
  }
  function renderExchange() {
    if (!config) return;
    const previous = rule.value;
    const rules = config.exchanges.filter(r => coin(r.from)?.enabled && coin(r.to)?.enabled);
    rule.replaceChildren(...rules.map(r => { const option = node('option', '', r.title); option.value = r.key; return option; }));
    if (rules.some(r => r.key === previous)) rule.value = previous;
    $('[data-billing-exchange-empty]').hidden = !!rules.length;
    $('[data-billing-exchange-body]').hidden = !rules.length;
    input(exchange, 'accepted').checked = false;
    renderRule();
  }
  function renderRule() {
    const r = selectedRule();
    if (!r) return;
    const from = coin(r.from), to = coin(r.to); if (!from || !to) return;
    set('[data-billing-rate]', `${C.format(BigInt(r.from_units), from.precision)} ${from.name} → ${C.format(BigInt(r.to_units), to.precision)} ${to.name}（扣费前）`);
    set('[data-billing-from-name]', from.name); set('[data-billing-to-name]', to.name);
    set('[data-billing-exchange-limit]', `单笔 ${C.format(BigInt(r.minimum), from.precision)} – ${C.format(BigInt(r.maximum), from.precision)} ${from.name}`);
    set('[data-billing-exchange-fee]', C.format(BigInt(r.fee_bps), 2) + '%');
    set('[data-billing-exchange-precision]', to.precision + ' 位小数，向下取整');
    input(exchange, 'accepted').checked = false;
    previewExchange(); controls();
  }
  function quoteExchange() {
    const r = selectedRule();
    if (!r) throw new Error('请选择可用的兑换方案。');
    const from = coin(r.from), to = coin(r.to); if (!from || !to) throw new Error('兑换币种未确认');
    const amount = C.units(input(exchange, 'amount').value.trim(), from.precision);
    if (amount < BigInt(r.minimum) || amount > BigInt(r.maximum)) throw new Error('转出数量不在当前方案的单笔范围内。');
    const credit = amount * BigInt(r.to_units) * (10000n - BigInt(r.fee_bps)) / (BigInt(r.from_units) * 10000n);
    if (credit <= 0n || credit > 9223372036854775807n) throw new Error('预计到账数量无效，请调整转出数量。');
    return { r, from, to, amount: C.format(amount, from.precision), credit: C.format(credit, to.precision) };
  }
  function previewExchange() {
    set('[data-billing-exchange-credit]', '—');
    set('[data-billing-exchange-preview]', '输入数量后查看预计到账。');
    if (!input(exchange, 'amount').value.trim()) return;
    try { const q = quoteExchange(); set('[data-billing-exchange-credit]', q.credit); set('[data-billing-exchange-preview]', '已计入兑换手续费，最终以订单记录为准。'); }
    catch (caught) { const error = failure(caught); set('[data-billing-exchange-preview]', error.message); }
  }
  function chooseKind(next: string) {
    if (!['recharge', 'membership', 'exchange', 'resource'].includes(next) || sending) return;
    kind = next;
    for (const el of root.querySelectorAll<HTMLButtonElement>('[data-billing-kind]')) el.setAttribute('aria-pressed', String(el.dataset.billingKind === kind));
    purchase.hidden = kind === 'exchange'; exchange.hidden = kind !== 'exchange';
    if (config) { if (kind !== 'exchange') renderProducts(); controls(); }
  }
  function statusNode(order: PaymentOrder) { const badge = node('span', 'billing-status', statuses[order.status]); badge.dataset.status = order.status; return badge; }
  function renderOrders() {
    const list = [...orders.values()].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
    set('[data-billing-orders-count]', String(list.length));
    $('[data-billing-orders]').replaceChildren(...(list.length ? list.map(order => {
      const row = node('article', 'billing-order-row'), description = node('div'), time = node('time', '', date(order.created_at)); time.dateTime = order.created_at;
      description.append(node('h3', '', order.title), time, node('span', 'billing-order-meta', order.kind === 'exchange' ? '币种兑换' : order.kind === 'membership' ? '会员购买' : '余额充值'));
      const value = node('div', 'billing-order-value'); value.append(node('strong', '', order.amount), node('small', '', coinName(order.currency)));
      const button = node('button', 'billing-order-view', '查看详情'); button.type = 'button'; button.dataset.billingViewOrder = order.id; button.setAttribute('aria-label', '查看订单：' + order.title);
      row.append(description, value, statusNode(order), button); return row;
    }) : [node('p', 'billing-empty', '还没有订单。完成充值、会员购买或兑换后，记录会显示在这里。')]));
  }
  function renderConfig() {
    if (!config) return;
    set('[data-billing-availability]', config.enabled ? '账户服务已开放' : '服务暂未开放 · 历史订单可查看');
    $('[data-billing-availability]').dataset.enabled = String(config.enabled);
    set('[data-billing-disclaimer]', config.disclaimer); set('[data-billing-refund]', config.refund_policy);
    set('[data-billing-verification]', config.require_verified_email ? '本站要求完成邮箱验证后再购买或兑换。' : '使用当前登录账号创建订单。');
    renderExchange(); chooseKind(kind); renderOrders(); controls();
  }
  async function load() {
    if (!owner || sending) return;
    const request = ++serial, activeEpoch = epoch, previousRevision = orderRevision;
    loadController?.abort(); const controller = new AbortController(); loadController = controller;
    loading = true; controls(); $<HTMLButtonElement>('[data-billing-reload]').hidden = true;
    try {
      const response = await api(endpoint, { signal: controller.signal });
      if (request !== serial || activeEpoch !== epoch) return;
      if (response.user_id !== owner || csrf && response.csrf_token !== csrf) { identity({ authenticated: false }); message('[data-billing-message]', '登录状态已变化，请重新载入账户服务。', 'error'); return; }
      config = billingConfig(response, owner);
      const next = new Map(config.orders.map(o => [o.id, o])); for (const [id, order] of orders) if ((revisions.get(id) || 0) > previousRevision) next.set(id, order); orders = next;
      $('[data-billing-content]').hidden = false;
      renderConfig(); message('[data-billing-message]', pendingBroken ? '上次订单的恢复信息无法读取。请先在订单列表或联系管理员核对结果，再重新购买。' : '', pendingBroken ? 'error' : '');
      if (requestedOrder && !deepLinkOpened) {
        deepLinkOpened = true;
        if (orderID(requestedOrder)) openOrder(requestedOrder); else message('[data-billing-message]', '订单链接格式无效，可在下方查看自己的订单。', 'error');
      }
    } catch (caught) { const error = failure(caught);
      if (request !== serial || activeEpoch !== epoch || error.name === 'AbortError') return;
      message('[data-billing-message]', error.message || '账户服务暂时无法载入。', 'error'); $<HTMLButtonElement>('[data-billing-reload]').hidden = false;
    } finally { if (request === serial && activeEpoch === epoch) { loading = false; controls(); } }
  }
  function reviewRows(container: HTMLElement, rows: string[][], total = false) {
    container.replaceChildren(...rows.map(([label, value], index) => { const row = node('div', total && index === rows.length - 1 ? 'billing-review-total' : ''); row.append(node('dt', '', label), node('dd', '', value)); return row; }));
  }
  function confirm(value: BillingDraft) {
    draft = value;
    set('[data-billing-confirm-title]', pending ? '继续确认原请求' : value.kind === 'exchange' ? '核对这次兑换' : '核对这笔订单');
    set('[data-billing-confirm-copy]', value.title);
    reviewRows($('[data-billing-confirm-review]'), value.review, true);
    message('[data-billing-confirm-message]', pending ? '这次操作会复用原请求编号，请勿另行重复支付。' : '');
    controls(); confirmDialog.showModal();
  }
  purchase.addEventListener('change', event => {
    if (!(event.target instanceof HTMLInputElement) || retired) return;
    if (event.target.name === 'product') { productKey = event.target.value; renderCheckout(); }
    else if (event.target.name === 'channel') { channelKey = event.target.value; renderCheckout(); }
  });
  purchase.addEventListener('submit', event => {
    event.preventDefault();
    if (retired || sending || pending || pendingBroken || !config?.enabled || !purchase.reportValidity() || !input(purchase, 'accepted').checked) return;
    const p = selectedProduct(), ch = selectedChannel();
    if (!p || !ch) return;
    const amount = money(p.price, p.currency);
    confirm({ kind: 'purchase', title: p.title, body: { version: config.version, product: p.key, channel: ch.key, accepted: true }, expected: { kind: p.kind, amount, currency: p.currency, ...(p.kind === 'recharge' ? { credit: C.format(BigInt(p.credit), coin(p.target_currency)?.precision ?? 0), target_currency: p.target_currency } : { plan: p.plan }) }, review: [['商品', p.title], ['到账权益', creditText(p)], ['支付渠道', ch.name], ['应付金额', amount + ' ' + p.currency]] });
  });
  rule.addEventListener('change', renderRule);
  input(exchange, 'amount').addEventListener('input', () => { input(exchange, 'accepted').checked = false; previewExchange(); });
  exchange.addEventListener('submit', event => {
    event.preventDefault();
    if (retired || sending || pending || pendingBroken || !config?.enabled || !exchange.reportValidity() || !input(exchange, 'accepted').checked) return;
    try {
      const q = quoteExchange();
      confirm({ kind: 'exchange', title: q.r.title, body: { version: config.version, rule: q.r.key, amount: q.amount, accepted: true }, expected: { kind: 'exchange', amount: q.amount, currency: q.r.from, credit: q.credit, target_currency: q.r.to }, review: [['转出数量', q.amount + ' ' + q.from.name], ['兑换手续费', C.format(BigInt(q.r.fee_bps), 2) + '%'], ['实际到账', q.credit + ' ' + q.to.name]] });
    } catch (caught) { const error = failure(caught); message('[data-billing-message]', error.message, 'error'); }
  });
  for (const el of root.querySelectorAll<HTMLButtonElement>('[data-billing-kind]')) el.addEventListener('click', () => chooseKind(el.dataset.billingKind || ''));
  $<HTMLButtonElement>('[data-billing-resume]').addEventListener('click', () => { if (pending && !sending) confirm(pending); });
  const closeConfirm = () => { if (!sending) { confirmDialog.close(); draft = null; } };
  for (const el of root.querySelectorAll<HTMLButtonElement>('[data-billing-confirm-close], [data-billing-confirm-cancel]')) el.addEventListener('click', closeConfirm);
  confirmDialog.addEventListener('cancel', event => { if (sending) event.preventDefault(); else draft = null; });
  $<HTMLButtonElement>('[data-billing-confirm-send]').addEventListener('click', async () => {
    if (retired || sending || !draft || !owner) return;
    const activeEpoch = epoch;
    try { if (!pending) pending = savePending(draft); }
    catch (caught) { const error = failure(caught); message('[data-billing-confirm-message]', error.message, 'error'); return; }
    const original = pending; if (!original) return;
    sending = true; serial++; loading = false; loadController?.abort(); controls(); message('[data-billing-confirm-message]', '正在确认，请稍候…');
    try {
      const value = await api(endpoint + (original.kind === 'exchange' ? '/exchange' : '/orders'), { method: 'POST', body: original.body });
      if (activeEpoch !== epoch) return;
      const order = paymentReceipt(value, original);
      clearPending(); draft = null; input(purchase, 'accepted').checked = false; input(exchange, 'accepted').checked = false; confirmDialog.close(); storeOrder(order); renderOrders();
      message('[data-billing-message]'); showOrder(order);
    } catch (caught) { const error = failure(caught);
      if (activeEpoch !== epoch || error.name === 'AbortError') return;
      if (error.code === 'payment.conflict') {
        clearPending(); draft = null; confirmDialog.close(); sending = false;
        await load(); message('[data-billing-message]', '方案已更新，请重新选择并核对价格后再提交。', 'error');
      } else message('[data-billing-confirm-message]', (error.message || '处理结果暂未确认。') + ' 原请求已保留，可稍后继续确认。', 'error');
    } finally { if (activeEpoch === epoch) { sending = false; controls(); } }
  });
  function safeCheckout(value: unknown) {
    if (typeof value !== 'string') return '';
    try { const url = new URL(value); return url.protocol === 'https:' && url.hostname && !url.username && !url.password ? url.href : ''; } catch { return ''; }
  }
  function expired(order: PaymentOrder) { return Date.parse(order.expires_at) <= Date.now(); }
  function renderPay(order: PaymentOrder) {
    const signature = JSON.stringify([order.id, order.status, expired(order), order.checkout, order.qr_url]);
    if (signature === paySignature) return;
    paySignature = signature;
    const container = $('[data-billing-order-pay]'); container.replaceChildren();
    if(order.kind==='resource'&&order.status==='completed'&&typeof order.resource_url==='string'&&/^\/(posts|pages)\/[^\s\\]+$/.test(order.resource_url)){const link=node('a','ui-button ui-button-primary','查看已购内容与资源');link.href=order.resource_url;container.append(link);return;}
    if (order.kind === 'exchange' || order.status !== 'pending' || expired(order)) return;
    const checkout = order.checkout || {}, href = safeCheckout(checkout.url);
    if (order.qr_url) {
      let qr;
      try { qr = new URL(order.qr_url, location.origin); } catch { qr = null; }
      if (qr && qr.origin === location.origin && qr.pathname === orderPath(order.id) + '/qr' && !qr.search && !qr.hash) {
        const img = node('img'); img.src = qr.href; img.alt = '本次订单的支付二维码'; img.width = 216; img.height = 216;
        img.addEventListener('error', () => { img.hidden = true; if (currentOrder?.id === order.id) message('[data-billing-order-message]', '二维码暂时无法加载，请刷新订单或使用支付入口。', 'error'); });
        container.append(img, node('p', '', '请使用对应的支付应用扫码，并核对金额。'));
      }
    }
    if (href) {
      if ((checkout.method || '').toUpperCase() === 'POST') {
        const form = node('form'); form.method = 'POST'; form.action = href; form.target = '_blank'; form.rel = 'noopener noreferrer';
        const fields = checkout.fields;
        if (fields && typeof fields === 'object' && !Array.isArray(fields) && Object.keys(fields).length <= 100 && Object.entries(fields).every(([key, value]) => key.length <= 200 && typeof value === 'string' && value.length <= 32768)) {
          for (const [name, value] of Object.entries(fields)) { const input = node('input'); input.type = 'hidden'; input.name = name; input.value = value; form.append(input); }
          const submit = node('button', 'ui-button ui-button-primary', '打开支付页面'); submit.type = 'submit'; form.append(submit);
          form.addEventListener('submit', event => { if (!owner || currentOrder?.id !== order.id || currentOrder.status !== 'pending' || expired(currentOrder)) { event.preventDefault(); message('[data-billing-order-message]', '请先刷新并核对当前订单状态。', 'error'); } });
          container.append(form);
        }
      } else if (!checkout.method || checkout.method.toUpperCase() === 'GET') {
        const link = node('a', 'ui-button ui-button-primary', '打开支付页面'); link.href = href; link.target = '_blank'; link.rel = 'noopener noreferrer';
        link.addEventListener('click', event => { if (!owner || currentOrder?.id !== order.id || currentOrder.status !== 'pending' || expired(currentOrder)) event.preventDefault(); }); container.append(link);
      }
    }
    if (!container.children.length) container.append(node('p', '', order.error_code === 'checkout_unknown' ? '渠道创建结果尚未确认。请等待支付通知，或联系管理员核对原订单。' : '支付入口暂未就绪，请同步状态后再试。'));
  }
  function renderOrder() {
    const o = currentOrder;
    if (!o) return;
    set('[data-billing-order-status]', statuses[o.status]); $('[data-billing-order-status]').dataset.status = o.status;
    set('[data-billing-order-title]', o.title); set('[data-billing-order-amount]', o.amount); set('[data-billing-order-currency]', coinName(o.currency));
    const copy = o.status === 'completed' ? (o.kind === 'membership' ? '会员权益已开通，可前往个人中心查看。' : o.kind === 'resource' ? '资源购买已完成，返回文章核验其他条件后即可查看。' : '已到账，钱包收支明细中可以查到本次记录。') : o.status === 'paid' ? '支付已确认，正在发放权益。请勿再次付款，可同步状态继续确认。' : o.status === 'cancelled' ? (o.kind==='resource' ? '订单已取消。如已付款，请同步渠道结果；确认渠道关闭后可返回文章重新购买。' : '此订单已取消，未完成的兑换不会扣款。') : o.status === 'review' ? (o.error_code==='resource_delivery_requires_reconciliation' ? '收款已记录，但资源目前无法交付。请保留订单编号，联系管理员核对交付或退款。' : o.error_code==='resource_duplicate_payment' ? '已记录重复收款，需要管理员核对原订单与退款。请保留订单编号。' : '此订单需要管理员核对。请保留订单编号，暂勿重复支付。') : o.status === 'refunded' ? '订单已标记为退款，实际退回进度以支付渠道记录为准。' : o.kind === 'exchange' ? '兑换正在处理，可同步状态继续确认。' : o.error_code === 'checkout_unknown' ? '支付渠道的创建结果尚未确认，请保留本订单，等待通知或联系管理员核对。暂勿新建或重复付款。' : expired(o) ? '支付时限已到。如你已经付款，请同步状态或联系管理员核对。' : '请在订单有效期内付款。支付后点击「同步状态」确认到账。';
    set('[data-billing-order-state-copy]', copy);
    const rows = [['订单编号', o.id], ['创建时间', date(o.created_at)]];
    if (o.status === 'pending') rows.push(['支付截止', date(o.expires_at)]);
    if (o.kind === 'membership') rows.push(['会员有效期', o.days > 0 ? o.days + ' 天' : '按方案配置']); else if (o.kind === 'resource') { if (Date.parse(o.resource_ends_at)>0) rows.push(['资源访问截止',date(o.resource_ends_at)]); } else rows.push(['到账数量', o.credit + ' ' + coinName(o.target_currency)]);
    if (o.status === 'completed') rows.push(['完成时间', date(o.completed_at)]);
    reviewRows($('[data-billing-order-review]'), rows);
    set('[data-billing-order-disclaimer]', o.disclaimer || '—'); set('[data-billing-order-refund]', o.refund_policy || '—');
    $<HTMLButtonElement>('[data-billing-order-sync]').hidden = !(['pending','paid'].includes(o.status) || o.kind==='resource' && o.status==='cancelled');
    $<HTMLButtonElement>('[data-billing-order-sync]').textContent = o.kind === 'exchange' || o.status === 'paid' ? '继续确认到账' : '我已支付，同步状态';
    $<HTMLAnchorElement>('[data-billing-order-account]').hidden = o.status !== 'completed' || o.kind==='resource' && !(typeof o.resource_url==='string' && /^\/(posts|pages)\/[^\s\\]+$/.test(o.resource_url));
    $<HTMLAnchorElement>('[data-billing-order-account]').href = o.kind==='resource' ? o.resource_url || '/downloads/' : o.kind === 'membership' ? '/account/?view=progression' : '/account/?view=wallet&currency=' + encodeURIComponent(o.target_currency || 'main');
    $<HTMLAnchorElement>('[data-billing-order-account]').textContent = o.kind==='resource' ? '返回文章核验解锁条件' : o.kind === 'membership' ? '查看会员权益' : '查看我的钱包';
    set('[data-billing-order-auto]', ['pending', 'paid'].includes(o.status) ? '打开此窗口时会定时读取本站订单状态，支付结果以服务器确认为准。' : '此记录已保存，可随时回到订单列表查看。');
    renderPay(o); controls();
  }
  function stopPoll() { clearTimeout(pollTimer); pollTimer = 0; }
  function schedulePoll() {
    stopPoll();
    if (!owner || !orderDialog.open || document.hidden || !currentOrder || !['pending', 'paid'].includes(currentOrder.status) || polls >= 60) return;
    pollTimer = setTimeout(async () => { polls++; await refreshOrder(false, true); schedulePoll(); }, 10000);
  }
  function showOrder(order: PaymentOrder) {
    currentOrder = order; paySignature = ''; polls = 0; message('[data-billing-order-message]'); renderOrder();
    if (!orderDialog.open) orderDialog.showModal();
    const url = new URL(location.href); url.searchParams.set('order', order.id); history.replaceState(null, '', url.pathname + url.search + url.hash);
    schedulePoll();
  }
  async function openOrder(id: string) {
    if (!owner || !orderID(id)) return;
    const activeEpoch = epoch, request = ++orderSerial;
    orderController?.abort(); const controller = new AbortController(); orderController = controller; syncing = false;
    const cached = orders.get(id); if (cached) showOrder(cached);
    try {
      const response = await api(orderPath(id), { signal: controller.signal });
      if (activeEpoch !== epoch || request !== orderSerial) return;
      const order = paymentOrder(response, owner, id); storeOrder(order); renderOrders(); showOrder(order);
    } catch (caught) { const error = failure(caught); if (activeEpoch === epoch && request === orderSerial && error.name !== 'AbortError') message(orderDialog.open ? '[data-billing-order-message]' : '[data-billing-message]', error.message, 'error'); }
  }
  async function refreshOrder(sync = false, quiet = false) {
    if (!owner || !currentOrder || syncing) return;
    const activeEpoch = epoch, id = currentOrder.id, request = ++orderSerial;
    orderController?.abort(); const controller = new AbortController(); orderController = controller;
    syncing = true; controls(); if (!quiet) message('[data-billing-order-message]', sync ? '正在向渠道核对支付结果…' : '正在刷新订单…');
    try {
      const response = await api(orderPath(id) + (sync ? '/sync' : ''), { ...(sync ? { method: 'POST', body: {} } : {}), signal: controller.signal });
      if (activeEpoch !== epoch || request !== orderSerial || currentOrder?.id !== id) return;
      currentOrder = paymentOrder(response, owner, id); storeOrder(currentOrder); renderOrders(); renderOrder();
      message('[data-billing-order-message]', quiet ? '' : sync ? '已同步。' : '已刷新。');
    } catch (caught) { const error = failure(caught); if (activeEpoch === epoch && request === orderSerial && currentOrder?.id === id && error.name !== 'AbortError') message('[data-billing-order-message]', error.message || '订单状态暂时未确认，请稍后重试。', 'error'); }
    finally { if (activeEpoch === epoch && request === orderSerial) { syncing = false; controls(); } }
  }
  $('[data-billing-orders]').addEventListener('click', event => { const button = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-billing-view-order]') : null; if (button?.dataset.billingViewOrder) openOrder(button.dataset.billingViewOrder); });
  $<HTMLButtonElement>('[data-billing-order-sync]').addEventListener('click', async () => { await refreshOrder(true); schedulePoll(); });
  $<HTMLButtonElement>('[data-billing-order-refresh]').addEventListener('click', async () => { polls = 0; await refreshOrder(); schedulePoll(); });
  $('[data-billing-order-close]').addEventListener('click', () => orderDialog.close());
  orderDialog.addEventListener('close', () => { if (orderDialog.open) return; stopPoll(); orderSerial++; orderController?.abort(); syncing = false; currentOrder = null; paySignature = ''; const url = new URL(location.href); url.searchParams.delete('order'); history.replaceState(null, '', url.pathname + url.search + url.hash); });
  document.addEventListener('visibilitychange', schedulePoll);
  window.addEventListener('pagehide', () => { retired = true; identity({ authenticated: false }); });
  window.addEventListener('pageshow', event => { if (event.persisted) { retired = false; initialized = false; refreshIdentity(true).catch(error => message('[data-billing-message]', failure(error).message, 'error')); } });
  $<HTMLButtonElement>('[data-billing-reload]').addEventListener('click', () => { if (owner) load(); else refreshIdentity(true).catch(error => message('[data-billing-message]', error.message, 'error')); });
  $<HTMLButtonElement>('[data-billing-refresh]').addEventListener('click', load);
  async function refreshIdentity(force = false) { const active = epoch; const session = await getSession(force); if (active === epoch && !retired) identity(session); }
  function identity(session: ClientSession) {
    if (retired && session?.authenticated) return;
    const next = session?.authenticated && C.id(String(session.id)) ? String(session.id) : '';
    const token = session?.csrf_token || '';
    if (initialized && next === owner && token === csrf) return;
    controllers.forEach(controller => controller.abort()); revisions.clear(); csrf = token;
    initialized = true; epoch++; serial++; orderSerial++; owner = next; config = null; orders = new Map(); pending = owner ? readPending() : null;
    loading = false; sending = false; syncing = false; draft = null; currentOrder = null; stopPoll(); confirmDialog.close(); orderDialog.close();
    purchase.reset(); exchange.reset();
    $('[data-billing-content]').hidden = true; $('[data-billing-guest]').hidden = !!owner;
    $<HTMLButtonElement>('[data-billing-reload]').hidden = true; $('[data-billing-products]').replaceChildren(); $('[data-billing-orders]').replaceChildren(); $('[data-billing-order-pay]').replaceChildren();
    for (const el of root.querySelectorAll('[data-billing-order-review], [data-billing-confirm-review]')) el.replaceChildren();
    for (const selector of ['order-title', 'order-status', 'order-state-copy', 'order-amount', 'order-currency', 'order-disclaimer', 'order-refund', 'order-message', 'confirm-copy', 'pending-copy', 'orders-count']) set('[data-billing-' + selector + ']', '');
    const account = $<HTMLAnchorElement>('[data-billing-order-account]'); account.hidden = true; account.href = '/account/';
    message('[data-billing-message]', owner ? '正在载入账户服务…' : ''); controls();
    if (owner && !retired) load();
  }
  document.addEventListener('chunnel:session', event => identity(event.detail));
  refreshIdentity().catch(error => { message('[data-billing-message]', error.message || '登录状态暂时无法确认，请刷新页面。', 'error'); $<HTMLButtonElement>('[data-billing-reload]').hidden = false; });
})();
