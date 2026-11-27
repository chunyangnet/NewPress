import { create, APIError } from '../shared/newpress-client';
import { node, failure } from '../shared/ledger-data';
import { createSiteTime } from '../shared/site-time';
import { couponCatalog, couponSession, couponClaim, pendingCoupon, couponAmount } from '../shared/shop-coupon-data';
import type { CouponCatalog, CouponSession, CouponOption, PendingCoupon } from '../shared/shop-coupon-data';

(() => {
  const target = document.querySelector<HTMLElement>('[data-home-coupons]');
  const targetList = target?.querySelector<HTMLElement>('[data-home-coupon-list]');
  if (!target || !targetList) return;
  const root = target, list = targetList;
  const status = node('p', 'home-coupon-status');
  status.dataset.homeCouponStatus = ''; status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite'); status.hidden = true;
  const retry = node('button', '', '重新读取优惠券'); retry.type = 'button'; retry.dataset.homeCouponRefresh = ''; retry.hidden = true;
  root.append(status, retry);
  const time = createSiteTime(() => document.documentElement.dataset.siteTimezone);
  let catalog: CouponCatalog | null = null, session: CouponSession | null = null;
  let generation = 0, retired = false, busyCode = '', sessionFailed = false;
  let expectedOwner: string | null | undefined, expectedToken = '';
  const attempts = new Map<string, PendingCoupon>();
  const client = create({ session: () => session ? { authenticated: true, csrf_token: session.csrf_token } : null, epoch: () => generation });
  const active = (ticket: number) => !retired && ticket === generation && root.isConnected;
  const notice = (text: string) => { status.textContent = text; status.hidden = !text; };
  const storageKey = (owner: string, code: string) => `newpress.shop.coupon.pending.${owner}.${code}`;
  function attempt(owner: string, code: string): PendingCoupon | null {
    const key = storageKey(owner, code), memory = attempts.get(key);
    if (memory) return memory;
    const raw = sessionStorage.getItem(key);
    if (raw === null) return null;
    const result = pendingCoupon(JSON.parse(raw) as unknown, owner, code);
    attempts.set(key, result); return result;
  }
  function remember(value: PendingCoupon): void {
    const key = storageKey(value.owner, value.code);
    // Persist before a non-idempotent claim so reloads cannot silently repeat it.
    sessionStorage.setItem(key, JSON.stringify(value)); attempts.set(key, value);
  }
  function forget(value: PendingCoupon): void {
    const key = storageKey(value.owner, value.code);
    sessionStorage.removeItem(key); attempts.delete(key);
  }
  async function readSession(owner = expectedOwner): Promise<CouponSession | null> {
    if (owner === null) return null;
    try {
      const result = couponSession(await client.request('/api/v1/shop/session'), owner);
      if (expectedToken && result.csrf_token !== expectedToken) throw new Error('登录状态已变化，请重新读取优惠券');
      return result;
    } catch (error) {
      if (error instanceof APIError && error.status === 401 && owner === undefined) return null;
      throw error;
    }
  }
  function reconcile(next: CouponSession): void {
    for (const option of next.coupons) {
      const pending = attempt(next.user_id, option.code);
      if (pending && (option.user_claim_count > pending.count || pending.rejected)) forget(pending);
    }
  }
  function card(c: CouponOption): HTMLElement {
    const currencies = session?.currencies || catalog?.currencies || [];
    const article = node('article', 'home-coupon-ticket'); article.dataset.couponCode = c.code;
    const value = node('strong', 'home-coupon-value', c.kind === 'percent' ? `${(Number(c.value) / 100).toFixed(Number(c.value) % 100 ? 2 : 0)}%` : couponAmount(c.value, c.currency, currencies));
    const detail = node('div', 'home-coupon-detail'); detail.append(value, node('h3', '', c.title));
    const conditions: string[] = [];
    if (BigInt(c.minimum) > 0n) conditions.push(`满 ${couponAmount(c.minimum, c.currency, currencies)} 可用`);
    if (c.kinds.length) conditions.push('指定商品类型');
    if (c.modes.length) conditions.push('指定购买方案');
    if (c.products.length) conditions.push('指定商品');
    if (c.categories.length) conditions.push(`分类：${c.categories.join('、')}`);
    if (c.levels.length) conditions.push('指定等级');
    if (c.memberships.length) conditions.push('指定会员');
    if (c.currency) conditions.push(`限 ${currencies.find(item => item.key === c.currency)?.name || c.currency}`);
    if (BigInt(c.maximum_discount) > 0n) conditions.push(`最多减 ${couponAmount(c.maximum_discount, c.currency, currencies)}`);
    conditions.push(`${time.formatDate(c.ends_at)} 到期`);
    detail.append(node('p', '', conditions.join(' · ')));
    const side = node('div', 'home-coupon-action');
    let pending: PendingCoupon | null = null, invalid = false;
    if (session) {
      try { pending = attempt(session.user_id, c.code); }
      catch { invalid = true; }
    }
    if (invalid) {
      side.append(node('span', 'home-coupon-claimed', '领取记录待核对'));
      const link = node('a', '', '查看优惠券'); link.href = '/shop/?coupons=1'; side.append(link);
    } else if (session && (pending || c.can_claim)) {
      if (pending?.confirmed) side.append(node('span', 'home-coupon-claimed', '已领取'));
      const button = node('button', '', pending ? pending.confirmed ? '刷新领取状态' : '核对领取结果' : c.user_claim_count ? '再领一张' : '领取');
      button.type = 'button'; button.dataset.code = c.code; button.disabled = !!busyCode;
      button.addEventListener('click', () => { void claim(c.code); }); side.append(button);
    } else if (sessionFailed) {
      side.append(node('span', 'home-coupon-claimed', '账户状态待读取'));
    } else if (c.claimed) {
      side.append(node('span', 'home-coupon-claimed', '已领取'));
    } else if (session) {
      const label = node('span', 'home-coupon-claimed', '暂不可领取'); label.setAttribute('aria-label', '已达到领取上限或优惠券暂不可领取'); side.append(label);
    } else {
      const link = node('a', '', '领取'); link.href = `/shop/?claim=${encodeURIComponent(c.code)}`; side.append(link);
    }
    article.append(detail, side); return article;
  }
  function render(): void {
    if (retired || !catalog) return;
    const own = new Map(session?.coupons.map(item => [item.code, item]));
    // Public eligibility must never substitute for missing current-account data.
    const coupons = catalog.coupons.filter(c => c.home).map(c => own.get(c.code) || { ...c, can_claim: false, claimed: false, user_claim_count: 0, available_count: 0 });
    list.replaceChildren(...coupons.map(card));
    root.hidden = coupons.length === 0 && !status.textContent;
    root.setAttribute('aria-busy', String(!!busyCode));
  }
  async function load(): Promise<void> {
    if (retired) return;
    const ticket = ++generation; client.cancel(); busyCode = ''; session = null; sessionFailed = false;
    list.replaceChildren(); root.hidden = true; retry.hidden = true; notice('');
    try {
      const nextCatalog = couponCatalog(await client.request('/api/v1/shop/catalog'));
      if (!active(ticket)) return;
      catalog = nextCatalog;
      try {
        const next = await readSession();
        if (!active(ticket)) return;
        session = next;
        if (next) reconcile(next);
      } catch (error) {
        if (!active(ticket)) return;
        sessionFailed = true; retry.hidden = false; notice(failure(error).message);
      }
      if (active(ticket)) render();
    } catch (error) {
      if (!active(ticket)) return;
      catalog = null; session = null; root.hidden = false; retry.hidden = false;
      notice('优惠券暂时无法读取：' + failure(error).message);
    }
  }
  async function claim(code: string): Promise<void> {
    if (busyCode || retired || !session) return;
    const ticket = generation, owner = session.user_id;
    busyCode = code; retry.hidden = true; notice('正在核对领取状态…'); render();
    let pending: PendingCoupon | null = null, submitted = false, confirmed = false;
    try {
      pending = attempt(owner, code);
      const next = await readSession(owner);
      if (!active(ticket)) return;
      if (!next) throw new Error('请登录后领取优惠券');
      session = next;
      const option = next.coupons.find(item => item.code === code);
      if (pending) {
        if (option && (option.user_claim_count > pending.count || pending.rejected)) {
          forget(pending); notice(option.user_claim_count > pending.count ? '优惠券已领取，领取记录已更新。' : '领取记录已核对，可重新选择领取。');
        } else notice(pending.confirmed ? '优惠券已领取，列表暂未更新，请稍后刷新领取状态。' : '领取结果暂未确认，请稍后核对或到商城查看我的优惠券。');
        return;
      }
      if (!option?.can_claim) { notice('当前优惠券暂不可领取，请查看最新领取状态。'); return; }
      pending = { owner, code, count: option.user_claim_count };
      remember(pending);
      submitted = true;
      const receipt = couponClaim(await client.request('/api/v1/shop/coupons/claim', { method: 'POST', body: { code } }), owner, code);
      if (!active(ticket)) return;
      confirmed = true; pending = { ...pending, confirmed: receipt.id };
      attempts.set(storageKey(owner, code), pending);
      try { remember(pending); } catch { /* Original uncertain record still prevents another claim after reload. */ }
      notice('优惠券已领取。');
      try {
        const refreshed = await readSession(owner);
        if (!active(ticket)) return;
        if (refreshed) { session = refreshed; reconcile(refreshed); }
        if (attempt(owner, code)) notice('优惠券已领取，列表暂未更新，请刷新领取状态。');
      } catch (error) {
        if (active(ticket)) notice('优惠券已领取，列表刷新失败，请稍后刷新领取状态。');
      }
    } catch (error) {
      if (!active(ticket)) return;
      if (submitted && pending && !confirmed) {
        if (error instanceof APIError && [400, 401, 403, 404, 409, 422, 429].includes(error.status)) {
          pending = { ...pending, rejected: true }; attempts.set(storageKey(owner, code), pending);
          try { remember(pending); } catch { /* Keep the conservative original record if storage is unavailable. */ }
        }
        notice(failure(error).message + '。请先核对领取结果。');
      } else notice(failure(error).message);
      if (error instanceof APIError && (error.status === 401 || error.status === 403)) { session = null; sessionFailed = true; retry.hidden = false; }
    } finally {
      if (active(ticket)) { busyCode = ''; render(); }
    }
  }
  retry.addEventListener('click', () => { void load(); });
  document.addEventListener('chunnel:session', event => {
    const identity = event.detail;
    if (!identity || typeof identity.authenticated !== 'boolean') return;
    const owner = identity.authenticated && Number.isSafeInteger(identity.id) && Number(identity.id) > 0 ? String(identity.id) : null;
    const token = typeof identity.csrf_token === 'string' ? identity.csrf_token : '';
    if (owner === session?.user_id && (!token || token === session.csrf_token) && !sessionFailed) { expectedOwner = owner; expectedToken = token; return; }
    if (owner === expectedOwner && token === expectedToken) return;
    expectedOwner = owner; expectedToken = token;
    if (!retired) void load();
  });
  window.addEventListener('pagehide', () => {
    retired = true; generation++; client.cancel(); session = null; busyCode = '';
    list.replaceChildren(); notice(''); retry.hidden = true; root.hidden = true;
  });
  window.addEventListener('pageshow', event => {
    if (event.persisted) { retired = false; expectedOwner = undefined; expectedToken = ''; void load(); }
  });
  void load();
})();
