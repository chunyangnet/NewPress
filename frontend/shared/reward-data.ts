import {record, string, boolean, number, identifier, decimal, date} from './ledger-data';
import {parseCurrencyReceipt} from './wallet-data';
import {parseProgressionEntry} from './progression-data';
export function parseWalletReward(value: unknown, owner: string) {
  const row = record(value), settings = record(row.settings);
  if (row.user_id !== owner || row.currency_key !== 'main') throw new Error('签到状态未能确认，请重试');
  return { source: 'wallet' as const, version: identifier(settings.version), name: string(settings.name), enabled: boolean(settings.enabled) && boolean(settings.check_in_enabled),
    precision: number(settings.precision, 0, 6),
    ready: boolean(row.check_in_ready), verification: boolean(row.verification_required), next: row.next_check_in_at == null ? '' : date(row.next_check_in_at) };
}
export function parseProgressionReward(value: unknown, owner: string) {
  const row = record(value), account = record(row.account), settings = record(row.settings), xp = record(settings.experience), check = record(settings.check_in);
  if (account.user_id !== owner) throw new Error('账号数据未确认，请重新载入');
  const next = date(row.next_check_in);
  return { source: 'progression' as const, version: identifier(settings.version), name: string(xp.name), enabled: boolean(xp.enabled) && boolean(check.enabled),
    ready: !next || Date.parse(next) <= Date.now(), verification: boolean(xp.require_verified_email) && !boolean(row.verified_email), next };
}
export function walletRewardReceipt(value: unknown, precision: number) {
  const result = parseCurrencyReceipt(value, 'main', precision);
  if (result.entry.kind !== 'reward_check_in' || !result.entry.incoming) throw new Error('领取结果未能确认，可以安全重试。');
  return { delta: result.entry.delta, replayed: result.replayed };
}
export function progressionRewardReceipt(value: unknown, owner: string) {
  const row = record(value), entry = parseProgressionEntry(row.entry, owner);
  if (entry.kind !== 'reward_check_in' || BigInt(decimal(entry.delta)) <= 0n) throw new Error('签到结果未确认，可以安全重试。');
  return { delta: entry.delta, replayed: boolean(row.replayed) };
}
