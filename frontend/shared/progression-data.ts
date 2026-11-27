import type { Membership, MembershipPlan, ProgressionLevel, ProgressionSettings, ProgressionEntry, ProgressionOrder, ProgressionSnapshot } from './progression-types';
import { record, string, boolean, number, identifier, decimal, date, list, key, requestID, version, id } from './ledger-data';
import { planBenefits } from './progression-benefits';
import { units } from './currency-amount';
const currencyKey = (value: unknown) => { if (!key(value)) throw new Error('方案或币种编号未确认'); return value; };
const optionalText = (value: unknown) => value === undefined ? undefined : string(value);
const currencyAmount = (value: unknown) => { const raw = string(value); units(raw, raw.split('.')[1]?.length || 0); return raw; };
export function parseMembership(value: unknown): Membership {
  const row = record(value);
  return { version: decimal(row.version), plan: string(row.plan), revoked: boolean(row.revoked), since: date(row.since), until: date(row.until) };
}
const level = (value: unknown): ProgressionLevel => { const row = record(value); return { key: currencyKey(row.key), name: string(row.name), color: string(row.color), threshold: decimal(row.threshold) }; };
function plan(value: unknown): MembershipPlan {
  const row = record(value), price = currencyAmount(row.price);
  return { key: currencyKey(row.key), name: string(row.name), description: string(row.description), color: string(row.color), enabled: boolean(row.enabled), days: number(row.days, 1, 3650), experience_percent: number(row.experience_percent),
    purchase_enabled: boolean(row.purchase_enabled), currency: currencyKey(row.currency), price, benefits: planBenefits(row) };
}
export function parseProgressionSettings(value: unknown): ProgressionSettings {
  const row = record(value), xp = record(row.experience), check = record(row.check_in), member = record(row.membership);
  const levels = row.levels === undefined ? null : record(row.levels), plans = list(member.plans, plan);
  if (new Set(plans.map(item => item.key)).size !== plans.length) throw new Error('会员方案重复');
  return { version: identifier(row.version), experience: { enabled: boolean(xp.enabled), name: string(xp.name), maximum: decimal(xp.maximum), daily_limit: decimal(xp.daily_limit), require_verified_email: boolean(xp.require_verified_email) },
    check_in: { enabled: boolean(check.enabled), amount: decimal(check.amount), mode: optionalText(check.mode), timezone: optionalText(check.timezone), interval_hours: check.interval_hours === undefined ? undefined : number(check.interval_hours) },
    levels: levels ? { enabled: boolean(levels.enabled), public_badges: levels.public_badges === undefined ? undefined : boolean(levels.public_badges), items: list(levels.items, level) } : undefined,
    membership: { enabled: boolean(member.enabled), public_badges: member.public_badges === undefined ? undefined : boolean(member.public_badges), plans } };
}
export function parseProgressionEntry(value: unknown, owner: string): ProgressionEntry {
  const row = record(value); if (row.user_id !== owner) throw new Error('成长记录归属未确认');
  return { id: identifier(row.id), user_id: owner, actor_id: decimal(row.actor_id), kind: string(row.kind), delta: decimal(row.delta, true), balance: decimal(row.balance), reason: string(row.reason), created_at: date(row.created_at), membership: row.membership == null ? undefined : parseMembership(row.membership) };
}
export function parseProgressionOrder(value: unknown, owner: string): ProgressionOrder {
  const row = record(value); if (row.user_id !== owner || !requestID(row.request_id) || row.mode !== 'grant' && row.mode !== 'renew') throw new Error('订单归属未确认');
  return { user_id: owner, request_id: row.request_id, status: string(row.status), version: identifier(row.version), member_version: decimal(row.member_version), plan: currencyKey(row.plan), mode: row.mode,
    plan_name: string(row.plan_name), days: number(row.days, 1, 3650), amount: currencyAmount(row.amount), symbol: string(row.symbol), currency: currencyKey(row.currency) };
}
export function validateSnapshot(value: unknown, owner: string): ProgressionSnapshot {
  const row = record(value), account = record(row.account);
  if (account.user_id !== owner || !id(owner)) throw new Error('账号数据未确认，请重新载入');
  const order = row.pending_order == null ? null : parseProgressionOrder(row.pending_order, owner);
  if (order && order.status !== 'pending') throw new Error('订单归属未确认');
  const entries = list(row.entries, value => parseProgressionEntry(value, owner));
  if (new Set(entries.map(item => item.id)).size !== entries.length || !version(row.next_before)) throw new Error('成长分页数据未确认');
  return { account: { user_id: owner, experience: decimal(account.experience), membership: parseMembership(account.membership) }, settings: parseProgressionSettings(row.settings),
    level: row.level == null ? null : level(row.level), next_level: row.next_level == null ? null : level(row.next_level), membership_active: boolean(row.membership_active), earned_today: decimal(row.earned_today),
    next_check_in: date(row.next_check_in), pending_order: order, entries, next_before: row.next_before,
    currencies: list(row.currencies, value => { const row = record(value); return { key: currencyKey(row.key), name: string(row.name), symbol: string(row.symbol), enabled: boolean(row.enabled), precision: number(row.precision, 0, 6) }; }), verified_email: boolean(row.verified_email) };
}
