// Home pages are publicly cacheable. Resolve account state through core session APIs.
export function initHomeAccount() {
  if (document.documentElement.dataset.appearancePreview === 'true') return;
  const welcome = document.querySelector('[data-docs-home-welcome]');
  const checkin = document.querySelector('[data-home-checkin-card]');
  const service = window.ChunnelInteractions;
  if ((!welcome && !checkin) || !service) return;
  let owner = '', revision = 0, refreshed = 0;
  const text = (root, selector, value) => { const node = root?.querySelector(selector); if (node) node.textContent = value; };
  const sync = async (session, force = false) => {
    const user = session?.authenticated ? String(session.id) : '';
    if (welcome) {
      welcome.querySelector('[data-docs-home-guest]').hidden = !!user;
      welcome.querySelector('[data-docs-home-member]').hidden = !user;
      text(welcome, '[data-docs-home-name]', user ? session.name : '');
    }
    if (user === owner && !force && Date.now() - refreshed < 30000) return;
    owner = user; refreshed = Date.now();
    const current = ++revision;
    text(welcome, '[data-docs-home-progress]', '在个人中心管理收藏、关注与学习进度。');
    if (checkin && !checkin.querySelector('[aria-busy="true"]')) {
      checkin.querySelector('[data-home-checkin]').disabled = false;
      text(checkin, '[data-home-checkin-label]', user ? '领取签到奖励' : '登录后签到');
      const status = checkin.querySelector('[data-home-checkin-status]');
      if (!user) { status.textContent = ''; status.hidden = true; }
    }
    if (!user || !checkin) return;
    const source = checkin?.dataset.checkinSource === 'wallet' ? 'wallet' : 'progression';
    try {
      const state = await service.api('/api/v1/social/' + source);
      if (current !== revision || String(state.account?.user_id || state.user_id || '') !== user) return;
      if (source === 'progression' && welcome) {
        const unit = state.settings?.experience?.name || '经验';
        const parts = [state.level?.name, `${state.account.experience || 0} ${unit}`];
        if (state.membership_active) parts.push('会员有效');
        text(welcome, '[data-docs-home-progress]', parts.filter(Boolean).join(' · '));
      }
      if (!checkin || checkin.querySelector('[aria-busy="true"]')) return;
      const enabled = source === 'wallet' ? state.settings?.enabled && state.settings?.check_in_enabled : state.settings?.check_in?.enabled;
      const next = source === 'wallet' ? state.next_check_in_at : state.next_check_in;
      const ready = source === 'wallet' ? state.check_in_ready : !next || String(next).startsWith('0001-') || new Date(next).getTime() <= Date.now();
      checkin.querySelector('[data-home-checkin]').disabled = !enabled || !ready;
      text(checkin, '[data-home-checkin-label]', !enabled ? '签到暂未开放' : ready ? '领取签到奖励' : '本轮已领取');
      text(checkin, '[data-home-checkin-meta]', !enabled ? '可在个人中心查看账户状态' : ready ? '现在可以领取签到奖励' : `下次可签：${window.ChunnelTime?.formatDateTime(next) || next}`);
    } catch {
      if (current === revision) text(welcome, '[data-docs-home-progress]', '账户状态暂未同步，可进入个人中心查看。');
    }
  };
  document.addEventListener('chunnel:session', event => { void sync(event.detail); });
  document.addEventListener('chunnel:checkin', () => { service.getSession().then(session => sync(session, true)).catch(() => {}); });
  service.getSession().then(session => sync(session)).catch(() => {});
}
