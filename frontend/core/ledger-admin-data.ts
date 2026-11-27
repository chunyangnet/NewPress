import type { CurrencyPending, CurrencySettings, WalletSnapshot } from '../shared/currency-types';
import type { MembershipPlan, ProgressionSettings, ProgressionSnapshot, ProgressionPending, ProgressionOrder } from '../shared/progression-types';
import { record, string, boolean, number, identifier, decimal, date, list, key } from '../shared/ledger-data';
import { units } from '../shared/currency-amount';
import { parseCurrencySettings, parseCurrencyEntry, parseWallet } from '../shared/wallet-data';
import { parseProgressionSettings, parseProgressionEntry, parseProgressionOrder, validateSnapshot } from '../shared/progression-data';

export interface AdminCurrencySettings extends CurrencySettings {
  precision_locked: boolean; max_balance: string; require_verified_email: boolean; daily_issuance_limit: string;
  registration_since: string; publication_since: string;
}
export interface AdminWallet extends WalletSnapshot { settings: AdminCurrencySettings; username: string; display_name: string; status: string }
export interface AdminProgressionSettings extends ProgressionSettings {
  levels: { enabled: boolean; public_badges: boolean; items: NonNullable<ProgressionSettings['levels']>['items'] };
  check_in: { enabled: boolean; amount: string; mode: string; timezone: string; interval_hours: number };
  registration: { enabled: boolean; amount: string; since: string };
  publication: { enabled: boolean; amount: string; since: string; min_length: number; daily_limit: number };
  membership: { enabled: boolean; public_badges: boolean; plans: (MembershipPlan & { tier: number })[] };
}
export interface AdminProgressionSnapshot extends ProgressionSnapshot { settings: AdminProgressionSettings; username: string; display_name: string; status: string }
export type AdminOperation = ProgressionPending<'experience'> | ProgressionPending<'membership'>;
export type AdminCurrency = ProgressionSnapshot['currencies'][number];
const schedule = (value: string) => { if (value !== 'interval' && value !== 'calendar_day') throw new Error('签到周期未确认'); return value; };
const amount = (value: unknown, precision: number) => { units(value, precision); return string(value); };
const identity = (row: Record<string, unknown>, username: string) => {
  const name = string(row.username), status = string(row.status);
  if (!name || name.toLowerCase() !== username.trim().toLowerCase() || !['active', 'suspended'].includes(status)) throw new Error('查询账号未确认');
  return { username: name, display_name: string(row.display_name), status };
};
export function nextVersion(actual: string, previous: string): void {
  if (BigInt(actual) !== BigInt(previous) + 1n) throw new Error('保存版本未确认，请重新载入核对');
}
export function adminCurrencySettings(value: unknown): AdminCurrencySettings {
  const row = record(value), cfg = parseCurrencySettings(row);
  return { ...cfg, check_in_mode: schedule(cfg.check_in_mode), precision_locked: boolean(row.precision_locked),
    max_balance: amount(row.max_balance, cfg.precision), require_verified_email: boolean(row.require_verified_email),
    daily_issuance_limit: amount(row.daily_issuance_limit, cfg.precision), registration_since: date(row.registration_since), publication_since: date(row.publication_since) };
}
export function currencySettingsResult(value: unknown, currency: string, previous?: string, module?: string): AdminCurrencySettings {
  const row = record(value);
  if (row.currency_key !== currency || module && row.module !== module) throw new Error('返回的货币或模块不一致，请重新载入确认');
  const cfg = adminCurrencySettings(row.settings); if (previous) nextVersion(cfg.version, previous); return cfg;
}
export function currencyCreation(value: unknown, original: CurrencyPending<'create'>): void {
  const row = record(value); currencySettingsResult(row, original.key); boolean(row.replayed);
  // A replay returns current settings, which may have changed since creation.
}
export function currencyAdjustment(value: unknown, original: CurrencyPending<'adjust'>) {
  const row = record(value), entry = parseCurrencyEntry(row, original.precision), delta = units(original.delta, original.precision, true);
  if (row.currency_key !== original.currency_key || units(entry.delta, original.precision, true) !== delta || entry.reason !== original.reason || entry.kind !== (delta > 0n ? 'admin_credit' : 'admin_debit') || !entry.created_at) throw new Error('调整回执未确认，请重试原请求');
  // This endpoint does not echo user_id or request_id; never infer them from a receipt.
  return entry;
}
export function adminWallet(value: unknown, currency: string, username: string, expected = ''): AdminWallet {
  const row = record(value), user = expected || identifier(row.user_id);
  return { ...parseWallet(row, user, currency), ...identity(row, username), settings: adminCurrencySettings(row.settings) };
}
export function adminProgressionSettings(value: unknown): AdminProgressionSettings {
  const row = record(value), cfg = parseProgressionSettings(row), levels = record(row.levels), member = record(row.membership), check = record(row.check_in), registration = record(row.registration), publication = record(row.publication);
  if (!cfg.levels || cfg.levels.items.length > 50 || cfg.membership.plans.length > 16 || new Set(cfg.levels.items.map(item => item.key)).size !== cfg.levels.items.length) throw new Error('等级或会员规则未确认');
  const tiers = list(member.plans, value => { const row = record(value); return row.tier === undefined ? 1 : number(row.tier, 1, 100); });
  return { ...cfg, levels: { ...cfg.levels, public_badges: boolean(levels.public_badges) },
    check_in: { ...cfg.check_in, mode: schedule(cfg.check_in.mode || 'interval'), timezone: cfg.check_in.timezone || 'Asia/Shanghai', interval_hours: number(check.interval_hours, 1, 720) },
    registration: { enabled: boolean(registration.enabled), amount: decimal(registration.amount), since: date(registration.since) },
    publication: { enabled: boolean(publication.enabled), amount: decimal(publication.amount), since: date(publication.since), min_length: number(publication.min_length, 1, 100000), daily_limit: number(publication.daily_limit, 1, 1000) },
    membership: { ...cfg.membership, public_badges: boolean(member.public_badges), plans: cfg.membership.plans.map((plan, index) => ({ ...plan, tier: tiers[index] ?? 1 })) } };
}
export function progressionSettingsResult(value: unknown, previous?: string) {
  const row = record(value), settings = adminProgressionSettings(row.settings), currencies = list(row.currencies, value => {
    const coin = record(value); if (!key(coin.key)) throw new Error('货币编号未确认');
    return { key: coin.key, name: string(coin.name), symbol: string(coin.symbol), precision: number(coin.precision, 0, 6), enabled: boolean(coin.enabled) };
  });
  if (currencies.length > 32 || new Set(currencies.map(coin => coin.key)).size !== currencies.length) throw new Error('货币列表未确认');
  if (previous) nextVersion(settings.version, previous); return { settings, currencies };
}
export function adminProgressionAccount(value: unknown, username: string, expected = ''): AdminProgressionSnapshot {
  const row = record(value), user = expected || identifier(record(row.account).user_id);
  return { ...validateSnapshot(row, user), ...identity(row, username), settings: adminProgressionSettings(row.settings) };
}
export function progressionAdjustment(value: unknown, original: AdminOperation, actor: string) {
  const row = record(value), raw = record(row.entry), entry = parseProgressionEntry(raw, original.payload.user_id), replayed = boolean(row.replayed);
  if (entry.actor_id !== actor || entry.reason !== original.payload.reason || !entry.created_at || !entry.membership) throw new Error('操作回执未确认，请重试原请求');
  if (original.kind === 'experience') {
    if (entry.kind !== 'experience_adjust' || entry.delta !== original.payload.delta || number(raw.days) !== 0) throw new Error('经验回执未确认，请重试原请求');
  } else {
    const p = original.payload, m = entry.membership;
    if (entry.kind !== 'membership_' + p.action || entry.delta !== '0' || number(raw.days) !== p.days || BigInt(m.version) !== BigInt(p.member_version) + 1n || m.revoked !== (p.action === 'revoke') || p.action !== 'revoke' && m.plan !== p.plan) throw new Error('会员回执未确认，请重试原请求');
  }
  decimal(raw.source_id); decimal(raw.payment_entry); string(raw.order_request); string(raw.payment_currency);
  return { entry, replayed };
}
export function progressionOrderResult(value: unknown, original: ProgressionOrder, action: 'recover' | 'cancel') {
  const row = record(value), order = parseProgressionOrder(row, original.user_id);
  for (const field of ['request_id', 'version', 'member_version', 'plan', 'mode', 'days', 'amount', 'currency'] as const) if (order[field] !== original[field]) throw new Error('订单回执未确认，请重试原订单');
  if (!(action === 'recover' ? ['pending', 'completed', 'cancelled'] : ['completed', 'cancelled']).includes(order.status)) throw new Error('订单状态未确认');
  if (!date(row.created_at) || !date(row.completed_at)) throw new Error('订单时间未确认');
  return order;
}

const text = (value: unknown, max = 500) => { const result = string(value); if (result.length > max) throw new Error('检查文字格式未确认'); return result; };
const state = (value: unknown): 'ok' | 'warning' | 'error' => { if (value !== 'ok' && value !== 'warning' && value !== 'error') throw new Error('检查状态未确认'); return value; };
const whole = (value: unknown) => number(value, Number.MIN_SAFE_INTEGER);
// Preview is also returned for invalid settings, so negative values and invalid prices are diagnostic text.
export function progressionPreview(value: unknown) {
  const row = record(value), valid = boolean(row.valid);
  const issues = list(row.issues, value => { const item = record(value); if (item.severity !== 'error' && item.severity !== 'warning') throw new Error('检查问题未确认'); return { severity: item.severity, field: text(item.field, 120), code: text(item.code, 120), message: text(item.message) }; });
  const metrics = list(row.metrics, value => { const item = record(value); return { key: text(item.key, 80), label: text(item.label, 80), value: text(item.value, 80) }; });
  const plans = list(row.plans, value => { const item = record(value); return { index: whole(item.index), key: text(item.key, 32), name: text(item.name, 40), currency: text(item.currency, 32), currency_name: text(item.currency_name, 80), symbol: text(item.symbol, 80), input_price: text(item.input_price, 2000), price: text(item.price, 2000), amount: text(item.amount, 19), precision: whole(item.precision), days: whole(item.days), experience_percent: whole(item.experience_percent), enabled: boolean(item.enabled), purchase_enabled: boolean(item.purchase_enabled), status: state(item.status) }; });
  const levels = list(row.levels, value => { const item = record(value); return { index: whole(item.index), key: text(item.key, 32), name: text(item.name, 40), threshold: text(item.threshold, 19), gap: text(item.gap, 19), status: state(item.status) }; });
  if (issues.length > 300 || metrics.length > 32 || plans.length > 16 || levels.length > 50 || valid && issues.some(item => item.severity === 'error')) throw new Error('数值检查结果未确认');
  return { valid, issues, metrics, plans, levels };
}
