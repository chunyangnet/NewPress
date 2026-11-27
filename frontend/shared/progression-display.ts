import type {ProgressionSnapshot, ProgressionLevel, ProgressionSettings, Membership, MembershipPlan, ProgressionEntry} from './progression-types';
import {integer, version, node} from './ledger-data';
import {planBenefits, benefitsList} from './progression-benefits';
export const color = (raw: unknown): string => typeof raw === 'string' && /^#[0-9a-f]{6}$/.test(raw) ? raw : '#64748b';
export const date = (value: string): string => window.ChunnelTime?.formatDateTime(value) || '—';
export function badge(name: string, accent: unknown, level = 0) {
    const numbered = Number.isInteger(level) && level > 0 && level <= 50;
    const el = node('span', numbered ? 'identity-badge identity-level' : 'progression-badge', numbered ? '' : name);
    el.style.setProperty('--progression-accent', color(accent));
    if (numbered) {
      el.dataset.identityKind='level'; el.dataset.identityColor=color(accent); el.style.setProperty('--identity-accent',color(accent));
      const rgb=[1,3,5].map(i=>parseInt(color(accent).slice(i,i+2),16)/255).map(v=>v<=.04045?v/12.92:((v+.055)/1.055)**2.4);
      el.style.setProperty('--identity-on',(rgb[0] || 0)*.2126+(rgb[1] || 0)*.7152+(rgb[2] || 0)*.0722>.179?'#17253d':'#ffffff');
      el.title=`LV${level} · ${name}`; el.setAttribute('aria-label',`等级 ${level} · ${name}`);
      const art=node('span','identity-level-art'); art.setAttribute('aria-hidden','true'); art.append(node('span','identity-level-prefix','LV'),node('span','identity-level-number',String(level))); el.append(art);
    }
    return el;
  }
export const levelNumber = (data: ProgressionSnapshot) => data.level && data.settings.levels?.enabled ? (data.settings.levels.items || []).findIndex(item=>item.key===data.level?.key)+1 : 0;
export function progress(experience: string, level: Pick<ProgressionLevel, 'threshold'> | null, next: Pick<ProgressionLevel, 'threshold'> | null) { if (!next) return 100; const start = integer(level?.threshold || '0'), end = integer(next.threshold), current = integer(experience); return end > start ? Number((current - start) * 10000n / (end - start)) / 100 : 100; }
export function levelFor(experience: string, levels: ProgressionSettings['levels']) {
    if (!levels?.enabled || !Array.isArray(levels.items)) return null;
    let found: ProgressionLevel | null = null, current: bigint;
    try { current = integer(experience); } catch { return null; }
    for (const level of levels.items) { try { if (integer(level.threshold) <= current) found = level; } catch { return null; } }
    return found;
  }
export function remainingDaily(limit: unknown, earned: unknown) { try { const left = integer(limit) - integer(earned); return left > 0n ? left : 0n; } catch { return 0n; } }
export function membershipDays(until: string, now = Date.now()) { const end = new Date(until); return Number.isNaN(end.getTime()) || end.getTime() <= now ? 0 : Math.ceil((end.getTime() - now) / 86400000); }
export function membershipActive(data: ProgressionSnapshot, now = Date.now()) {
    const m = data.account.membership;
    return data.membership_active === true && data.settings.membership.enabled && !m.revoked && data.settings.membership.plans.some(plan => plan.key === m.plan) && new Date(m.since).getTime() <= now && new Date(m.until).getTime() > now;
  }
export function membershipEnd(m: Membership, plan: Pick<MembershipPlan, 'key' | 'days'>, now = Date.now()) {
    const renewal = m.plan === plan.key && !m.revoked && version(m.version) && integer(m.version) > 0n;
    const currentEnd = new Date(m.until).getTime(), base = renewal && currentEnd > now ? currentEnd : now;
    if (!Number.isInteger(plan.days) || plan.days < 1 || plan.days > 3650 || base + plan.days * 86400000 > now + 3650 * 86400000) return '';
    return new Date(base + plan.days * 86400000).toISOString();
  }
export function summary(container: HTMLElement, data: ProgressionSnapshot, checkIn?: () => void | Promise<void>) {
    const cfg = data.settings, account = data.account, m = account.membership, plan = cfg.membership.plans.find(p => p.key === m.plan), active = membershipActive(data);
    const xp = node('section', 'progression-xp-card'), member = node('section', 'progression-member-card');
    xp.append(node('span', 'progression-card-kicker', cfg.experience.enabled ? '经验值' : '自动经验已暂停'));
    const value = node('div', 'progression-xp-value', account.experience); value.append(node('small', '', cfg.experience.name)); xp.append(value);
    const levelHeading=node('strong','progression-level-name'); if(data.level)levelHeading.append(badge(data.level.name,data.level.color,levelNumber(data))); levelHeading.append(node('span','',data.level?.name || '等级模块未开启')); xp.append(levelHeading);
    if (data.level) {
      const meter = node('div', 'progression-meter'), bar = node('span'); bar.style.width = Math.max(0, Math.min(100, progress(account.experience, data.level, data.next_level))) + '%'; meter.append(bar); xp.append(meter);
      const percent = Math.round(progress(account.experience, data.level, data.next_level));
      xp.append(node('p', '', data.next_level ? `距离「${data.next_level.name}」还需 ${integer(data.next_level.threshold) - integer(account.experience)} ${cfg.experience.name} · 本级进度 ${percent}%` : '已达到当前最高等级'));
    }
    xp.append(node('p', '', `近 24 小时获得 ${data.earned_today} / ${cfg.experience.daily_limit} · 今日还可获得 ${remainingDaily(cfg.experience.daily_limit, data.earned_today)} ${cfg.experience.name}`));
    if (checkIn) {
      xp.append(node('p', '', cfg.check_in.mode === 'calendar_day' ? `每天零点重置（${cfg.check_in.timezone === 'Asia/Shanghai' ? '北京时间' : cfg.check_in.timezone}）` : `距离上次签到满 ${cfg.check_in.interval_hours} 小时可领取`));
      const can = cfg.experience.enabled && cfg.check_in.enabled && (!cfg.experience.require_verified_email || data.verified_email);
      const next = new Date(data.next_check_in), ready = !Number.isNaN(next.getTime()) && next.getTime() > Date.now();
      const button = node('button', 'ui-button ui-button-secondary ui-button-small', ready ? '本次签到已领取' : `签到 · 基础 +${cfg.check_in.amount}`); button.type = 'button'; button.disabled = !can || ready; button.dataset.progressionCheckIn = ''; button.addEventListener('click', checkIn); xp.append(button);
      if (ready) xp.append(node('p', '', `下次领取：${date(data.next_check_in)}`));
      else if (cfg.experience.require_verified_email && !data.verified_email) xp.append(node('p', '', '验证邮箱后可以领取经验奖励'));
    }
    member.append(node('span', 'progression-card-kicker', '当前会员'));
    member.append(node('h3', '', plan?.name || '尚未开通会员'));
    member.append(node('p', 'progression-membership-state', !m.plan ? '选择下方方案，或前往支付中心开通。' : m.revoked ? '会员已撤销' : !cfg.membership.enabled ? '会员模块已关闭，权益暂停' : new Date(m.since).getTime() > Date.now() ? '会员尚未开始生效' : active ? '会员权益生效中' : '会员已到期或失效'));
    if (m.plan) {
      const days = membershipDays(m.until);
      member.append(node('p', 'progression-membership-dates', `开通于 ${date(m.since)}\n${active ? '有效期至' : '到期时间'} ${date(m.until)}`));
      if (active) { const remaining = node('span', 'progression-membership-remaining', `剩余 ${days} 天`); remaining.dataset.expiring = String(days <= 7); member.append(remaining); }
    }
    if (active && plan) {
      if (cfg.experience.enabled) member.append(node('p', '', `自动经验奖励 × ${(plan.experience_percent / 100).toFixed(2)}`));
      const benefits = planBenefits(plan); if (benefits.length) member.append(node('span', 'progression-benefits-title', '方案权益'), benefitsList(benefits));
      if (!plan.enabled) member.append(node('p', 'progression-help', '此方案已停止续费，现有权益保留至到期。'));
      else if (membershipDays(m.until) <= 7) member.append(node('p', 'progression-help', '会员即将到期，续费可累加当前有效期。'));
    }
    const marks = node('div', 'progression-badges'); if (data.level) marks.append(badge(data.level.name, data.level.color, levelNumber(data))); if (active && plan) marks.append(badge(plan.name, plan.color)); member.append(marks);
    container.replaceChildren(xp, member);
  }
  const labels: Record<string, string> = { experience_adjust: '管理员调整经验', reward_check_in: '签到经验', reward_registration: '注册经验', reward_publication: '发文经验', membership_grant: '开通会员', membership_renew: '续期会员', membership_revoke: '撤销会员', membership_purchase: '购买会员', membership_set: '调整会员' };
export function history(container: HTMLElement, entries: ProgressionEntry[], cfg: ProgressionSettings) {
    if (!entries.length) { container.replaceChildren(node('p', 'progression-empty', '还没有成长记录。')); return; }
    const wrap = node('div', 'progression-table-wrap'), table = node('table', 'progression-table'), header = node('thead'), title = node('tr'), body = node('tbody');
    ['变动事项', '经验变动', '当前经验'].forEach(text => title.append(node('th', '', text))); header.append(title); table.append(header, body); wrap.append(table);
    entries.forEach(entry => {
      const row = node('tr'), description = node('td'), delta = integer(entry.delta, true);
      description.append(node('strong', '', labels[entry.kind] || entry.kind), node('p', '', entry.reason), node('small', '', `${date(entry.created_at)} · #${entry.id}`));
      if (entry.membership?.plan) description.append(node('p', '', (cfg.membership.plans.find(p => p.key === entry.membership?.plan)?.name || entry.membership.plan) + (entry.membership.revoked ? ' · 已撤销' : ' · 到期 ' + date(entry.membership.until))));
      row.append(description, node('td', delta > 0n ? 'progression-positive' : delta < 0n ? 'progression-negative' : '', delta === 0n ? '—' : (delta > 0n ? '+' : '') + entry.delta), node('td', '', entry.balance)); body.append(row);
    }); container.replaceChildren(wrap);
  }
