export interface MemberLevel { name: string; threshold: string }
export interface MemberData {
  account: { user_id: string; experience: string; membership: { plan: string; until: string } };
  settings: {
    experience: { name: string };
    membership: { enabled: boolean; plans: { key: string; name: string; description: string }[] };
    check_in: { enabled: boolean; amount: string; mode: string; timezone: string; interval_hours: number };
  };
  level: MemberLevel | null;
  next_level: MemberLevel | null;
  membership_active: boolean;
  next_check_in: string;
}

const record = (value: unknown): Record<string, unknown> => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('会员状态格式错误');
  return value as Record<string, unknown>;
};
const text = (value: unknown): string => { if (value == null) return ''; if (typeof value !== 'string') throw new Error('会员文字格式错误'); return value; };
const decimal = (value: unknown): string => { const result = text(value); if (!/^(0|[1-9]\d*)$/.test(result)) throw new Error('会员数值格式错误'); return result; };
const flag = (value: unknown): boolean => { if (typeof value !== 'boolean') throw new Error('会员开关格式错误'); return value; };
const level = (value: unknown): MemberLevel | null => { if (value == null) return null; const row = record(value); return { name: text(row.name), threshold: decimal(row.threshold) }; };
const date = (value: unknown): string => { const result = text(value); if (result && !Number.isFinite(Date.parse(result))) throw new Error('会员时间格式错误'); return result; };

export function parseMember(value: unknown): MemberData {
  const data = record(value), account = record(data.account), settings = record(data.settings);
  const membership = record(account.membership), rules = record(settings.membership), experience = record(settings.experience), check = record(settings.check_in);
  if (!Array.isArray(rules.plans) || typeof check.interval_hours !== 'number' || !Number.isFinite(check.interval_hours)) throw new Error('会员配置格式错误');
  return {
    account: { user_id: decimal(account.user_id), experience: decimal(account.experience), membership: { plan: text(membership.plan), until: date(membership.until) } },
    settings: {
      experience: { name: text(experience.name) },
      membership: { enabled: flag(rules.enabled), plans: rules.plans.map(value => { const plan = record(value); return { key: text(plan.key), name: text(plan.name), description: text(plan.description) }; }) },
      check_in: { enabled: flag(check.enabled), amount: decimal(check.amount), mode: text(check.mode), timezone: text(check.timezone), interval_hours: check.interval_hours },
    },
    level: level(data.level), next_level: level(data.next_level), membership_active: flag(data.membership_active), next_check_in: date(data.next_check_in),
  };
}
