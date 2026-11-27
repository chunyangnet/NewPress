// DocsHub homepage check-in uses the shared core social API and idempotent requests.
(() => {
  const card = document.querySelector('[data-home-checkin-card]');
  if (!card) return;
  const button = card.querySelector('[data-home-checkin]');
  const status = card.querySelector('[data-home-checkin-status]');
  const label = card.querySelector('[data-home-checkin-label]');
  const note = card.querySelector('[data-home-checkin-note]');
  const meta = card.querySelector('[data-home-checkin-meta]');
  const { api, getSession, signIn } = window.ChunnelInteractions || {};
  if (!button || !status || !label || !note || !api || !getSession || !signIn) return;
  const source = card.dataset.checkinSource === 'wallet' ? 'wallet' : 'progression';
  const C = window.ChunnelCurrency;
  let busy = false, retry = null;
  const message = (text, tone = '') => { status.textContent = text; status.hidden = !text; status.dataset.tone = tone; };
  const state = (text, done = false, detail = '') => {
    label.textContent = text; button.disabled = done;
    if (detail) note.textContent = detail;
  };
  const nextLabel = (at) => window.ChunnelTime?.formatDateTime(at) || '';
  const setMeta = (text) => { if (meta) meta.textContent = text; };
  const nextText = (at) => { const value = nextLabel(at); return value ? `下次可签：${value}` : ''; };
  const nonce = () => crypto.randomUUID ? crypto.randomUUID() : [...crypto.getRandomValues(new Uint8Array(20))].map((value) => value.toString(16).padStart(2, '0')).join('');
  const storageKey = (owner) => `chunnel.progression.v1.check-in.${owner}`;
  const readPending = (owner) => {
    try {
      const value = JSON.parse(sessionStorage.getItem(storageKey(owner)));
      return value?.owner === owner && value.kind === 'check-in' && /^(0|[1-9][0-9]*)$/.test(value.payload?.version) && /^[A-Za-z0-9_-]{16,80}$/.test(value.payload?.request_id) ? value : null;
    } catch { return null; }
  };
  const savePending = (owner, payload) => {
    const value = { owner, kind: 'check-in', payload, quote: {} };
    try { sessionStorage.setItem(storageKey(owner), JSON.stringify(value)); } catch { /* Keep the request in memory for this attempt. */ }
    return value;
  };
  const clearPending = (owner) => { try { sessionStorage.removeItem(storageKey(owner)); } catch { /* Storage can be disabled. */ } };
  const rejected = new Set(['progression.invalid', 'progression.settings_changed', 'progression.disabled', 'progression.ineligible', 'progression.limit', 'progression.membership_changed', 'progression.pending', 'progression.user_not_found', 'request.invalid_json']);
  async function claimProgression() {
    if (busy || button.disabled) return;
    busy = true; button.setAttribute('aria-busy', 'true'); message('正在确认签到状态…');
    let owner = '';
    try {
      const session = await getSession(true);
      if (!session?.authenticated) { message('登录后即可领取签到奖励。'); await signIn(claimProgression); return; }
      owner = String(session.id);
      const snapshot = await api('/api/v1/social/progression');
      if (!snapshot || String(snapshot.account?.user_id || '') !== owner || !snapshot.settings?.check_in) throw new Error('账号数据未确认，请重新载入');
      const cfg = snapshot.settings.check_in, unit = snapshot.settings.experience?.name || '经验';
      if (!cfg.enabled) { state('签到暂未开放', true, '站点暂时没有开放签到。'); setMeta('签到功能当前未开启'); message('站点暂未开放签到奖励。'); return; }
      const next = new Date(snapshot.next_check_in || '');
      const ready = !snapshot.next_check_in || String(snapshot.next_check_in).startsWith('0001-') || Number.isNaN(next.getTime()) || next.getTime() <= Date.now();
      let request = readPending(owner);
      if (!ready && !request) { state('本轮已领取', true, '本轮奖励已经入账。'); setMeta(nextText(snapshot.next_check_in)); message('本轮签到已经领取。'); return; }
      request = request || savePending(owner, { version: String(snapshot.settings.version), request_id: nonce() });
      message('正在领取奖励…');
      const result = await api('/api/v1/social/progression/check-in', { method: 'POST', body: request.payload });
      if (!result.entry || String(result.entry.user_id || '') !== owner || result.entry.kind !== 'reward_check_in' || !result.entry.delta) throw new Error('签到结果未确认，可以安全重试。');
      clearPending(owner);
      state('本轮已领取', true, `本轮已领取 ${result.entry.delta} ${unit}。`);
      try { setMeta(nextText((await api('/api/v1/social/progression')).next_check_in)); }
      catch { setMeta('下次可签到时间暂未更新，请刷新查看。'); }
      document.dispatchEvent(new Event('chunnel:checkin'));
      message(result.replayed ? '这次签到已领取，未重复发放。' : `签到成功，获得 ${result.entry.delta} ${unit}。`);
    } catch (error) {
      if (owner && rejected.has(error?.code)) clearPending(owner);
      state('重试签到');
      message(error?.message || '签到暂时失败，请重试。', 'error');
    } finally {
      busy = false; button.removeAttribute('aria-busy');
    }
  }
  async function claimWallet() {
    if (busy || button.disabled) return;
    busy = true; button.setAttribute('aria-busy', 'true'); message('正在确认签到状态…');
    let owner = '';
    try {
      if (!C) throw new Error('签到组件未载入，请刷新后重试。');
      const session = await getSession(true);
      if (!session?.authenticated) { message('登录后即可领取签到奖励。'); await signIn(claimWallet); return; }
      owner = String(session.id);
      if (!C.id(owner)) throw new Error('账号状态已变化，请刷新后重试。');
      const wallet = await api('/api/v1/social/wallet');
      if (String(wallet.user_id || '') !== owner || !wallet.settings || !C.id(wallet.settings.version)) throw new Error('签到状态未能确认，请重试。');
      if (!wallet.settings.enabled || !wallet.settings.check_in_enabled) { state('签到暂未开放', true, '站点暂时没有开放签到。'); setMeta('签到功能当前未开启'); message('站点暂未开放签到奖励。'); return; }
      if (wallet.verification_required) { message('请先到个人中心验证邮箱，再领取奖励。'); return; }
      let request = retry?.owner === owner ? retry : C.readPending('check-in', owner);
      if (!wallet.check_in_ready && !request) {
        const next = nextLabel(wallet.next_check_in_at);
        state('本轮已领取', true, next ? '本轮奖励已经入账。' : '当前暂不可领取，请到钱包查看详情。');
        setMeta(next ? `下次可签：${next}` : '当前暂不可领取');
        message('本轮签到已经领取。'); return;
      }
      request = request || C.savePending('check-in', owner, { version: wallet.settings.version, request_id: C.nonce() });
      retry = request; message('正在领取奖励…');
      const result = await api('/api/v1/social/wallet/check-in', { method: 'POST', body: { version: request.version, request_id: request.request_id } });
      if (!result.entry || !C.id(result.entry.id)) throw new Error('领取结果未能确认，可以安全重试。');
      const current = await getSession(true);
      if (!current?.authenticated || String(current.id) !== owner) { message(''); return; }
      C.clearPending('check-in', owner); retry = null;
      state('本轮已领取', true, `已领取 ${result.entry.delta} ${wallet.settings.name}。`);
      try { setMeta(nextText((await api('/api/v1/social/wallet')).next_check_in_at)); }
      catch { setMeta('下次可签到时间暂未更新，请刷新查看。'); }
      message(`签到成功，已领取 ${result.entry.delta} ${wallet.settings.name}。`);
    } catch (error) {
      if (owner && C?.definiteRejection(error)) { C.clearPending('check-in', owner); retry = null; }
      state('重试签到');
      message(error?.message || '签到暂时失败，请重试。', 'error');
    } finally {
      busy = false; button.removeAttribute('aria-busy');
    }
  }
  button.addEventListener('click', source === 'wallet' ? claimWallet : claimProgression);
})();
