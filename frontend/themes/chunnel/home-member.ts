import '../../shared/site-time-global';
import { parseMember } from './home-member-data';
import type { MemberData } from './home-member-data';
import type {} from './home-types';
// Loaded only for an authenticated homepage with a progression widget.
(() => {
  const $ = (selector: string) => document.querySelector<HTMLElement>(selector);
  const memberText = (root: HTMLElement | null, selector: string, text: string) => {
    const element = root?.querySelector<HTMLElement>(selector);
    if (!element) return;
    element.textContent = text;
    element.hidden = !text;
  };
  // Go emits int64 quantities as decimal strings. Keep them exact through display.
  const memberNumber = (value: string | bigint) => BigInt(value).toLocaleString('zh-CN');
  const memberDate = (value: string) => {
    const label = window.ChunnelTime?.formatDateDot(value) || '';
    return label ? `有效期至 ${label}` : '';
  };
  const paintMember = (data: MemberData, owner: number) => {
    const card = $('[data-home-member-card]');
    if (!card || !Number.isSafeInteger(owner) || owner <= 0 || data.account.user_id !== String(owner)) return false;
    const account = data.account, membership = account.membership, cfg = data.settings;
    const level = data.level, next = data.next_level, active = data.membership_active;
    const plan = active ? cfg.membership.plans.find((item) => item.key === membership.plan) : null;
    const state = card.querySelector('[data-home-member-state]');
    if (state) state.textContent = active ? (plan?.name || '会员有效') : (cfg.membership?.enabled ? '社区成员 · 会员未开通' : '社区成员');
    const planNode = card.querySelector<HTMLElement>('[data-home-member-plan]');
    if (planNode) { planNode.textContent = active && plan?.name ? plan.name : ''; planNode.hidden = !(active && plan?.name); }
    const unit = cfg.experience?.name || '经验', experience = memberNumber(account.experience);
    const block = card.querySelector<HTMLElement>('[data-home-level-block]');
    if (level) {
      memberText(card, '[data-home-level]', level.name || '成长等级');
      memberText(card, '[data-home-xp]', `${experience} ${unit}`);
      const earned = BigInt(account.experience), start = BigInt(level.threshold), end = next ? BigInt(next.threshold) : start;
      const remaining = end > earned ? end - earned : 0n;
      memberText(card, '[data-home-level-next]', next ? `再获得 ${memberNumber(remaining)} ${unit}升级为「${next.name}」` : '已达到当前最高等级');
      const rounded = end > start ? ((earned - start) * 100n + (end - start) / 2n) / (end - start) : 100n;
      const percent = Number(rounded < 0n ? 0n : rounded > 100n ? 100n : rounded);
      const meter = card.querySelector('[data-home-level-meter]');
      if (meter) meter.setAttribute('aria-valuenow', String(percent));
      const fill = card.querySelector<HTMLElement>('[data-home-level-fill]');
      if (fill) fill.style.width = `${percent}%`;
      if (block) block.hidden = false;
    } else if (block) block.hidden = true;
    const description = card.querySelector<HTMLElement>('[data-home-member-description]');
    if (description) { description.textContent = active ? (plan?.description || '') : ''; description.hidden = !description.textContent; }
    const until = card.querySelector<HTMLElement>('[data-home-membership-until]');
    if (until) { const label = active ? memberDate(membership.until) : (cfg.membership?.enabled ? '会员尚未开通' : '账号状态已同步'); until.textContent = label; until.hidden = !label; }
    return true;
  };
  const paintCheckIn = (data: MemberData) => {
    // Once the reward form takes over, its receipt and pending state own these controls.
    if (window.ChunnelHomeFeaturesReady) return;
    const card = $('[data-home-checkin-card]');
    if (!card || card.dataset.checkinSource !== 'progression') return;
    const cfg = data.settings.check_in, unit = data.settings.experience?.name || '经验';
    memberText(card, '[data-home-checkin-amount]', String(cfg.amount || '0'));
    memberText(card, '[data-home-checkin-unit]', unit);
    const button = card.querySelector<HTMLButtonElement>('[data-home-checkin]'), label = card.querySelector('[data-home-checkin-label]');
    if (!cfg.enabled) {
      if (button) button.disabled = true;
      if (label) label.textContent = '签到暂未开放';
      memberText(card, '[data-home-checkin-meta]', '');
      memberText(card, '[data-home-checkin-note]', '站点暂时没有开放签到。');
      return;
    }
    const next = new Date(data.next_check_in || '');
    const ready = !data.next_check_in || String(data.next_check_in).startsWith('0001-') || Number.isNaN(next.getTime()) || next.getTime() <= Date.now();
    if (button) button.disabled = !ready;
    if (label) label.textContent = ready ? '领取签到奖励' : '本轮已领取';
    memberText(card, '[data-home-checkin-meta]', ready ? (cfg.mode === 'calendar_day' ? `每天零点重置（${cfg.timezone === 'Asia/Shanghai' ? '北京时间' : cfg.timezone}）` : `每 ${cfg.interval_hours || 24} 小时可签到一次`) : `下次可签：${window.ChunnelTime?.formatDateTime(data.next_check_in) || ''}`);
    memberText(card, '[data-home-checkin-note]', ready ? '现在可以领取今日奖励。' : '本轮奖励已经入账。');
  };
  window.ChunnelHomeMember = { paint: (value, owner) => { try { const data = parseMember(value); const painted = paintMember(data, owner); if (painted) paintCheckIn(data); return painted; } catch { return false; } } };
})();
