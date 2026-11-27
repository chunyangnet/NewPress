import type {ProgressionSnapshot, ProgressionEntry, ProgressionOrder, ProgressionPending} from './progression-types';
import {required} from './account-dom';
import {failure} from './ledger-data';
import {parseProgressionOrder} from './progression-data';
import {progressionRewardReceipt} from './reward-data';
export function mountProgression(): void {
  const element = document.querySelector<HTMLElement>('[data-progression-panel]'); if (!element || !window.ChunnelProgressionShared) return;
  const root: HTMLElement = element;
  const P = window.ChunnelProgressionShared, owner = root.dataset.userId || '', $ = <T extends Element = HTMLElement>(selector: string): T => required<T>(root, selector), base = '/api/v1/social/progression';
  if (!P.id(owner)) return;
  const status = $('[data-progression-status]'), dialog = $<HTMLDialogElement>('[data-progression-dialog]');
  let snapshot: ProgressionSnapshot | null = null, entries: ProgressionEntry[] = [], busy = false, loading = false, serial = 0, draft: ProgressionPending<'purchase'> | null = null, purchase = P.read('purchase', owner), checkIn = P.read('check-in', owner);
  const connection = P.transport(owner, '', () => { serial++; snapshot = null; entries = []; dialog.close(); $('[data-progression-summary]').replaceChildren(); $('[data-progression-plans]').replaceChildren(); $('[data-progression-history]').replaceChildren(); document.querySelector('[data-progression-badges]')?.replaceChildren(); controls(); P.message(status, '登录状态已变化，请重新打开个人中心。', 'error'); });
  const currentOrder = () => purchase || (snapshot?.pending_order ? fromOrder(snapshot.pending_order) : null);
  function fromOrder(o: ProgressionOrder): ProgressionPending<'purchase'> { return { kind: 'purchase', owner, payload: { version: o.version, member_version: o.member_version, plan: o.plan, mode: o.mode, request_id: o.request_id }, quote: { plan: o.plan_name, days: o.days, amount: o.amount, symbol: o.symbol, currency: o.currency } }; }
  function description(value: ProgressionPending<'purchase'>) { const q = value.quote; return `${value.payload.mode === 'renew' ? '续费' : '开通'}「${q.plan || value.payload.plan}」${q.days || ''} 天\n支付 ${q.amount || '原订单金额'} ${q.symbol || ''}（${q.currency || '原币种'}）${q.until ? `\n预计到期 ${P.date(q.until)}（以实际开通为准）` : ''}`; }
  function controls() {
    const locked = busy || loading || connection.lost, pending = currentOrder();
    root.querySelectorAll('button').forEach(button => { if (button.dataset.allowed !== undefined) button.disabled = locked || button.dataset.allowed !== 'true'; });
    $<HTMLButtonElement>('[data-progression-refresh]').disabled = locked;
    $<HTMLButtonElement>('[data-progression-more]').disabled = locked || !snapshot || snapshot.next_before === '0';
    $<HTMLButtonElement>('[data-progression-retry]').disabled = locked || !pending;
    $<HTMLButtonElement>('[data-progression-cancel]').disabled = locked || !pending;
    $<HTMLButtonElement>('[data-progression-confirm-submit]').disabled = locked || !draft;
    $('[data-progression-pending]').hidden = !pending || connection.lost;
    if (pending) $('[data-progression-pending-description]').textContent = description(pending);
    root.querySelectorAll<HTMLButtonElement>('[data-progression-buy]').forEach(button => { button.disabled = locked || !snapshot || !!pending || button.dataset.allowed !== 'true'; });
    const check = root.querySelector<HTMLButtonElement>('[data-progression-check-in]'); if (check) { if (checkIn) check.textContent = '重试原签到'; check.disabled = locked || !snapshot || (!checkIn && check.dataset.allowed !== 'true'); }
  }
  function paint(data: ProgressionSnapshot) {
    P.summary($('[data-progression-summary]'), data, claim);
    const check = root.querySelector<HTMLButtonElement>('[data-progression-check-in]'); if (check) check.dataset.allowed = String(!check.disabled);
    const planNodes: HTMLElement[] = [], cfg = data.settings, m = data.account.membership, active = P.membershipActive(data);
    for (const plan of cfg.membership.plans.filter(p => p.enabled)) {
      const card = P.node('article', 'progression-plan'); card.style.setProperty('--progression-accent', P.color(plan.color)); card.append(P.node('h4', '', plan.name), P.node('p', '', plan.description));
      if (active && m.plan === plan.key) { card.dataset.current = 'true'; card.append(P.node('span', 'progression-plan-current', '当前方案')); }
      const coin = data.currencies.find(c => c.key === plan.currency), price = P.node('div', 'progression-plan-price', plan.purchase_enabled ? plan.price : '在线开通');
      if (plan.purchase_enabled) price.append(P.node('small', '', `${coin?.symbol || plan.currency} · ${plan.currency}`)); card.append(price);
      const terms = P.node('div', 'progression-plan-terms'); terms.append(P.node('span', '', `${plan.days} 天有效期`), P.node('span', '', cfg.experience.enabled ? `经验 × ${(plan.experience_percent / 100).toFixed(2)}` : '自动经验已暂停')); card.append(terms);
      const benefits = P.planBenefits(plan); if (cfg.membership.public_badges) benefits.unshift('公开主页展示会员徽章'); if (benefits.length) card.append(P.benefitsList(benefits));
      const mode = m.plan === plan.key && !m.revoked && P.integer(m.version) > 0n ? 'renew' : 'grant';
      const allowedPlan = cfg.membership.enabled && (!active || m.plan === plan.key), enabled = allowedPlan && plan.purchase_enabled && coin?.enabled;
      const estimatedUntil = P.membershipEnd(m, plan);
      if (mode === 'renew' && allowedPlan) card.append(P.node('p', 'progression-plan-renewal', estimatedUntil ? `续费后预计至 ${P.date(estimatedUntil)}` : '有效期已接近上限，请稍后续费。'));
      const actions = P.node('div', 'progression-plan-actions');
      const button = P.node('button', 'ui-button ui-button-secondary ui-button-small', !cfg.membership.enabled ? '会员模块已关闭' : !plan.purchase_enabled ? '站内币购买未开放' : !coin?.enabled ? '支付货币已停用' : active && m.plan !== plan.key ? '请待当前会员到期' : mode === 'renew' ? '站内币续费' : '站内币开通');
      button.type = 'button'; button.dataset.progressionBuy = plan.key; button.dataset.allowed = String(!!enabled); button.addEventListener('click', () => {
        if (!enabled || busy || loading || connection.lost || currentOrder() || snapshot !== data) return;
        draft = { kind: 'purchase', owner, payload: { version: cfg.version, member_version: m.version, plan: plan.key, mode, request_id: P.nonce() }, quote: { plan: plan.name, days: plan.days, amount: plan.price, symbol: coin?.symbol || '', currency: plan.currency, until: P.membershipEnd(m, plan) } }; showConfirm();
      }); actions.append(button);
      if (allowedPlan) { const billing = P.node('a', 'progression-billing-link', mode === 'renew' ? '在线续费 →' : '在线开通 →'); billing.href = '/billing/?plan=' + encodeURIComponent(plan.key); actions.append(billing); }
      card.append(actions); planNodes.push(card);
    }
    $('[data-progression-plans]').replaceChildren(...(planNodes.length ? planNodes : [P.node('p', 'progression-empty', '暂时没有可开通的会员方案。')]));
    P.history($('[data-progression-history]'), entries, cfg); $<HTMLButtonElement>('[data-progression-more]').hidden = data.next_before === '0';
    const badges = document.querySelector('[data-progression-badges]'); if (badges) { const marks = []; if (data.level) marks.push(P.badge(data.level.name, data.level.color, P.levelNumber(data))); if (active) { const plan = cfg.membership.plans.find(p => p.key === m.plan); if (plan) marks.push(P.badge(plan.name, plan.color)); } badges.replaceChildren(...marks); }
    controls();
  }
  async function refresh(more = false) {
    const request = ++serial; loading = true; controls();
    try {
      await connection.check();
      const data = P.validateSnapshot(await connection.request(base + (more ? '?before=' + (snapshot?.next_before || '0') : '')), owner);
      if (request !== serial || connection.lost) return;
      const seen = new Set(more ? entries.map(entry => entry.id) : []);
      entries = more ? [...entries, ...data.entries.filter(entry => !seen.has(entry.id))] : data.entries; snapshot = data; paint(data);
    }
    finally { if (request === serial) { loading = false; controls(); } }
  }
  function invalidateSnapshot() {
    snapshot = null; entries = [];
    for (const selector of ['[data-progression-summary]', '[data-progression-plans]', '[data-progression-history]']) $(selector).replaceChildren();
    document.querySelector('[data-progression-badges]')?.replaceChildren();
    $<HTMLButtonElement>('[data-progression-more]').hidden = true; controls();
  }
  async function refreshConfirmed(text: string) {
    // The write is final. A failed read cannot restore a consumed order or its old purchase controls.
    invalidateSnapshot();
    try { await refresh(); if (!connection.lost) P.message(status, text); }
    catch { if (!connection.lost) P.message(status, text + ' 最新状态暂未载入，请刷新查看。'); }
  }
  function acknowledgedOrder(value: unknown, original: ProgressionPending<'purchase'>) {
    const order = parseProgressionOrder(value, owner), payload = original.payload;
    if (order.request_id !== payload.request_id || order.version !== payload.version || order.member_version !== payload.member_version || order.plan !== payload.plan || order.mode !== payload.mode || order.status !== 'completed' && order.status !== 'cancelled') throw new Error('订单结果未确认，请重试原购买');
    return order;
  }
  async function load() { if (busy || loading || connection.lost) return; P.message(status, '正在加载成长记录…'); try { await refresh(); if (!connection.lost) P.message(status, currentOrder() ? '请先核对待确认的购买，再进行新的购买。' : ''); } catch (problem) { const error = failure(problem); if (!connection.lost) P.message(status, error.message, 'error'); } }
  function showConfirm() { if (!draft) return; $('[data-progression-confirm]').textContent = description(draft); P.message($('[data-progression-confirm-status]'), ''); controls(); dialog.showModal(); }
  async function performPurchase() {
    if (busy || loading || connection.lost || !draft) return;
    purchase = P.save('purchase', owner, draft.payload, draft.quote); busy = true; controls(); P.message(status, '正在确认会员支付…');
    try {
      const original = purchase, order = acknowledgedOrder(await connection.request(base + '/purchase', { method: 'POST', body: original.payload }), original);
      if (connection.lost) return;
      P.clear('purchase', owner); purchase = null; draft = null; dialog.close(); await refreshConfirmed(order.status === 'completed' ? '会员已生效，支付与有效期已更新。' : '该订单已取消，未重复扣款。');
    } catch (problem) { const error = failure(problem); if (connection.lost) return; if (P.rejected(error)) { P.clear('purchase', owner); purchase = null; draft = null; dialog.close(); invalidateSnapshot(); await refresh().catch(() => {}); } if (!connection.lost) { P.message(status, error.message, 'error'); P.message($('[data-progression-confirm-status]'), error.message, 'error'); } }
    finally { busy = false; controls(); }
  }
  async function claim() {
    if (busy || loading || connection.lost || !snapshot) return; busy = true;
    const name = snapshot.settings.experience.name;
    try {
      checkIn = checkIn || P.save('check-in', owner, { version: snapshot.settings.version, request_id: P.nonce() }); controls();
      const result = progressionRewardReceipt(await connection.request(base + '/check-in', { method: 'POST', body: checkIn.payload }), owner);
      if (connection.lost) return;
      P.clear('check-in', owner); checkIn = null; await refreshConfirmed(result.replayed ? '这次签到已领取，未重复发放。' : `签到成功，获得 ${result.delta} ${name}。`);
    } catch (problem) { const error = failure(problem); if (connection.lost) return; if (P.rejected(error)) { P.clear('check-in', owner); checkIn = null; invalidateSnapshot(); await refresh().catch(() => {}); } if (!connection.lost) P.message(status, error.message, 'error'); }
    finally { busy = false; controls(); }
  }
  $<HTMLButtonElement>('[data-progression-refresh]').addEventListener('click', async () => { if (busy || loading || connection.lost) return; busy = true; controls(); try { const result = await connection.request(base + '/sync', { method: 'POST', body: {} }); if (result.ok !== true) throw new Error('同步结果未确认，请刷新重试'); await refreshConfirmed('已同步经验和会员状态。'); } catch (problem) { const error = failure(problem); if (!connection.lost) P.message(status, error.message, 'error'); } finally { busy = false; controls(); } });
  $<HTMLButtonElement>('[data-progression-more]').addEventListener('click', async () => { if (busy || loading || connection.lost || !snapshot || snapshot.next_before === '0') return; try { await refresh(true); } catch (problem) { const error = failure(problem); if (!connection.lost) P.message(status, error.message, 'error'); } });
  $<HTMLButtonElement>('[data-progression-retry]').addEventListener('click', () => { if (busy || loading || connection.lost) return; draft = currentOrder(); if (draft) showConfirm(); });
  $<HTMLButtonElement>('[data-progression-cancel]').addEventListener('click', async () => {
    const original = currentOrder(); if (!original || busy || loading || connection.lost) return; busy = true; controls();
    try { const order = acknowledgedOrder(await connection.request(base + '/purchase/cancel', { method: 'POST', body: { request_id: original.payload.request_id } }), original); if (connection.lost) return; P.clear('purchase', owner); purchase = null; draft = null; dialog.close(); await refreshConfirmed(order.status === 'completed' ? '订单已付款，会员已恢复生效。' : '订单已取消，不会再为这笔订单扣款。'); } catch (problem) { const error = failure(problem); if (!connection.lost) P.message(status, error.message, 'error'); } finally { busy = false; controls(); }
  });
  $<HTMLButtonElement>('[data-progression-confirm-submit]').addEventListener('click', performPurchase);
  dialog.addEventListener('cancel', event => { if (busy) event.preventDefault(); });
  required<HTMLFormElement>(dialog, 'form').addEventListener('submit', event => { if (busy) event.preventDefault(); });
  window.ChunnelProgression = Object.freeze({ load }); controls();
}
mountProgression();
