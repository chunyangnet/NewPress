import type { CurrencyKind, CurrencyPending, CurrencyPendingMap } from './currency-types';
import { record, id, key as currencyKey, requestID, number, failure } from './ledger-data';
import { units } from './currency-amount';
const key = (kind: CurrencyKind, owner: string, currency: string) => kind === 'create' ? `chunnel.currency.v2.create.${owner}` : `chunnel.currency.v2.${currency}.${kind}.${owner}`;
function validPending<K extends CurrencyKind>(kind: K, owner: string, value: unknown, currency: string): value is CurrencyPendingMap[K] {
  try {
    const row = record(value);
    if (row.owner !== owner || !id(owner) || !requestID(row.request_id) || !currencyKey(currency) || row.currency_key !== currency) return false;
    if (kind === 'create') return currencyKey(row.key) && typeof row.name === 'string' && [...row.name].length >= 1 && [...row.name].length <= 40 && typeof row.symbol === 'string' && [...row.symbol].length >= 1 && [...row.symbol].length <= 12 && number(row.precision, 0, 6) >= 0;
    if (!id(row.version)) return false;
    if (kind === 'check-in') return true;
    const precision = number(row.precision, 0, 6);
    if (kind === 'transfer') return id(row.recipient_id) && typeof row.recipient === 'string' && row.recipient.length <= 320 && (row.symbol === undefined || typeof row.symbol === 'string') && units(row.amount, precision) > 0n;
    if (kind === 'adjust') return id(row.user_id) && typeof row.reason === 'string' && [...row.reason].length <= 200 && units(row.delta, precision, true) !== 0n;
  } catch { return false; }
  return false;
}
export function readPending<K extends CurrencyKind>(kind: K, owner: string, currency = 'main'): CurrencyPending<K> | null {
  try { const value: unknown = JSON.parse(sessionStorage.getItem(key(kind, owner, currency)) || 'null'); return validPending(kind, owner, value, currency) ? Object.freeze(value) : null; } catch { return null; }
}
export function savePending<K extends CurrencyKind>(kind: K, owner: string, value: unknown, currency = 'main'): CurrencyPending<K> {
  const row = record(value);
  if (row.currency_key !== undefined && row.currency_key !== currency) throw new Error('操作货币不一致，请重新核对');
  const pending = { ...row, owner, currency_key: currency };
  if (!validPending(kind, owner, pending, currency)) throw new Error('操作信息无效，请重新核对');
  try { sessionStorage.setItem(key(kind, owner, currency), JSON.stringify(pending)); } catch { /* The immutable request remains in memory. */ }
  return Object.freeze(pending);
}
export function clearPending(kind: CurrencyKind, owner: string, currency = 'main'): void { try { sessionStorage.removeItem(key(kind, owner, currency)); } catch { /* Storage may be disabled. */ } }
export function definiteRejection(error: unknown): boolean {
  return ['currency.invalid', 'currency.settings_conflict', 'currency.precision_locked', 'currency.insufficient', 'currency.balance_limit', 'currency.daily_limit', 'currency.disabled', 'currency.transfer_disabled', 'currency.recipient_unavailable', 'currency.reward_ineligible', 'currency.reward_limit', 'currency.key_exists', 'currency.catalog_limit', 'currency.not_found'].includes(failure(error).code || '');
}
