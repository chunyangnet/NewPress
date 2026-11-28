import type {ClientSession} from '../../shared/newpress-client';
import {parseWalletReward, parseProgressionReward} from '../../shared/reward-data';
import {record, string, decimal, boolean} from '../../shared/ledger-data';
// Home pages are publicly cacheable. Resolve account state through core session APIs.
export function initHomeAccount() {
  if (document.documentElement.dataset.appearancePreview === 'true') return;
  const welcome = document.querySelector<HTMLElement>('[data-docs-home-welcome]');
  const checkin = document.querySelector<HTMLElement>('[data-home-checkin-card]');
  const service = window.ChunnelInteractions;
  if ((!welcome && !checkin) || !service) return;
  let owner = '', revision = 0, refreshed = 0;
  const text = (root: HTMLElement | null, selector: string, value: string) => { const node = root?.querySelector(selector); if (node) node.textContent = value; };
  const sync = async (session: ClientSession, force = false) => {
    const user = session?.authenticated ? String(session.id) : '';
    if (welcome) {
      const guest = welcome.querySelector<HTMLElement>('[data-docs-home-guest]'), member = welcome.querySelector<HTMLElement>('[data-docs-home-member]');
      if (guest) guest.hidden = !!user;
      if (member) member.hidden = !user;
      text(welcome, '[data-docs-home-name]', user ? session.name || '' : '');
    }
    if (user === owner && !force && Date.now() - refreshed < 30000) return;
    owner = user; refreshed = Date.now();
    const current = ++revision;
    text(welcome, '[data-docs-home-progress]', '在个人中心管理收藏、关注与学习进度。');
    if (checkin && !checkin.querySelector('[aria-busy="true"]')) {
      const button = checkin.querySelector<HTMLButtonElement>('[data-home-checkin]'); if (button) button.disabled = false;
      text(checkin, '[data-home-checkin-label]', user ? '领取签到奖励' : '登录后签到');
      const status = checkin.querySelector<HTMLElement>('[data-home-checkin-status]');
      if (!user && status) { status.textContent = ''; status.hidden = true; }
    }
    if (!user || !checkin) return;
    const source = checkin?.dataset.checkinSource === 'wallet' ? 'wallet' : 'progression';
    try {
      const state = record(await service.api('/api/v1/social/' + (source === 'wallet' ? 'wallets/main' : source)));
      if (current !== revision) return;
      const reward = source === 'wallet' ? parseWalletReward(state, user) : parseProgressionReward(state, user);
      if (source === 'progression' && welcome) {
        const parts = [state.level == null ? '' : string(record(state.level).name), `${decimal(record(state.account).experience)} ${reward.name}`];
        if (boolean(state.membership_active)) parts.push('会员有效');
        text(welcome, '[data-docs-home-progress]', parts.filter(Boolean).join(' · '));
      }
      if (!checkin || checkin.querySelector('[aria-busy="true"]')) return;
      const {enabled, next, ready} = reward;
      const button = checkin.querySelector<HTMLButtonElement>('[data-home-checkin]'); if (button) button.disabled = !enabled || !ready;
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
