import type { CurrencySettings, CurrencyEntry, WalletSnapshot } from './currency-types';
import { record, string, boolean, number, identifier, key, date, list, id } from './ledger-data';
import { units } from './currency-amount';
const amount = (value: unknown, precision: number, signed = false) => { units(value, precision, signed); return string(value); };
export function parseCurrencySettings(value: unknown): CurrencySettings {
  const row = record(value), precision = number(row.precision, 0, 6);
  return { version: identifier(row.version), enabled: boolean(row.enabled), name: string(row.name), symbol: string(row.symbol), precision,
    allow_transfers: boolean(row.allow_transfers), min_transfer: amount(row.min_transfer, precision), max_transfer: amount(row.max_transfer, precision),
    daily_transfer_limit: amount(row.daily_transfer_limit, precision), daily_reward_limit: amount(row.daily_reward_limit, precision),
    check_in_enabled: boolean(row.check_in_enabled), check_in_reward: amount(row.check_in_reward, precision),
    check_in_mode: row.check_in_mode === undefined ? 'interval' : string(row.check_in_mode), check_in_timezone: row.check_in_timezone === undefined ? '' : string(row.check_in_timezone), check_in_interval_hours: number(row.check_in_interval_hours),
    registration_enabled: boolean(row.registration_enabled), registration_reward: amount(row.registration_reward, precision),
    publication_enabled: boolean(row.publication_enabled), publication_reward: amount(row.publication_reward, precision),
    publication_min_length: number(row.publication_min_length), publication_daily_limit: number(row.publication_daily_limit) };
}
export function parseCurrencyEntry(value: unknown, precision: number): CurrencyEntry {
  const row = record(value), delta = amount(row.delta, precision, true), incoming = boolean(row.incoming);
  if (incoming !== (units(delta, precision, true) > 0n)) throw new Error('流水方向未确认');
  return { id: identifier(row.id), kind: string(row.kind), label: string(row.label), delta, balance: amount(row.balance, precision), incoming, created_at: date(row.created_at),
    reason: row.reason === undefined ? undefined : string(row.reason), peer: row.peer === undefined ? undefined : string(row.peer) };
}
export function parseWallet(value: unknown, owner: string, currency: string): WalletSnapshot {
  const row = record(value); if (row.currency_key !== currency || row.user_id !== owner || !id(owner)) throw new Error('钱包返回结果未确认，请重新载入');
  const settings = parseCurrencySettings(row.settings), precision = settings.precision;
  const entries = list(row.entries, value => parseCurrencyEntry(value, precision)), next = string(row.next_before);
  if (next && !id(next) || new Set(entries.map(entry => entry.id)).size !== entries.length) throw new Error('钱包分页数据未确认');
  return { currency_key: currency, user_id: owner, settings, entries, next_before: next,
    balance: amount(row.balance, precision), transferred_today: amount(row.transferred_today, precision), transfer_remaining: amount(row.transfer_remaining, precision),
    rewarded_today: amount(row.rewarded_today, precision), reward_remaining: amount(row.reward_remaining, precision), verification_required: boolean(row.verification_required),
    check_in_ready: boolean(row.check_in_ready), next_check_in_at: row.next_check_in_at == null ? undefined : date(row.next_check_in_at) };
}
export function parseCurrencies(value: unknown): { key: string; name: string; enabled: boolean }[] {
  const values = list(record(value).currencies, value => { const row = record(value); if (!key(row.key)) throw new Error('货币编号未确认'); return { key: row.key, name: string(row.name), enabled: boolean(row.enabled) }; });
  if (!values.length || values.length > 32 || new Set(values.map(value => value.key)).size !== values.length) throw new Error('货币列表未确认，请刷新重试');
  return values;
}
export function parseRecipient(value: unknown, currency: string): { id: string; username: string } {
  const row = record(value); if (row.currency_key !== currency) throw new Error('收款账号未确认');
  const username = string(row.username); if (!username.trim()) throw new Error('收款账号未确认');
  return { id: identifier(row.id), username };
}
export function parseCurrencyReceipt(value: unknown, currency: string, precision: number) {
  const row = record(value); if (row.currency_key !== currency) throw new Error('回执货币未确认');
  return { entry: parseCurrencyEntry(row.entry, precision), replayed: boolean(row.replayed), recipient_id: row.recipient_id === undefined ? undefined : identifier(row.recipient_id) };
}
