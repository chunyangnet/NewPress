import type { ProgressionKind, ProgressionPayloadMap, ProgressionPending, ProgressionQuote } from './progression-types';
import {record, id, key, requestID, version, integer, number, string, failure} from './ledger-data';
export function validPayload<K extends ProgressionKind>(kind: K, value: unknown): value is ProgressionPayloadMap[K] {
  try {
    const payload = record(value), count = Object.keys(payload).length;
    if (!requestID(payload.request_id) || !id(payload.version)) return false;
    if (kind === 'check-in') return count === 2;
    if (kind === 'purchase') return key(payload.plan) && (payload.mode === 'grant' || payload.mode === 'renew') && version(payload.member_version) && count === 5;
    if (!id(payload.user_id) || typeof payload.reason !== 'string' || !payload.reason.trim() || payload.reason.length > 400) return false;
    if (kind === 'experience') return integer(payload.delta, true) !== 0n && count === 5;
    return kind === 'membership' && version(payload.member_version) && ['grant', 'renew', 'revoke'].includes(string(payload.action)) &&
      (payload.action === 'revoke' ? payload.days === 0 && payload.plan === '' : key(payload.plan) && number(payload.days, 1, 3650) > 0) && count === 8;
  } catch { return false; }
}
const storageKey = (kind: ProgressionKind, owner: string) => `chunnel.progression.v1.${kind}.${owner}`;
function quote(value: unknown): ProgressionQuote {
  const row = record(value), result: ProgressionQuote = {};
  for (const field of ['plan', 'amount', 'symbol', 'currency', 'until', 'username'] as const) if (row[field] !== undefined) result[field] = string(row[field]);
  if (row.days !== undefined) result.days = number(row.days, 0, 3650);
  return result;
}
function freeze<K extends ProgressionKind>(kind: K, owner: string, payload: ProgressionPayloadMap[K], display: ProgressionQuote): ProgressionPending<K> {
  return Object.freeze({ kind, owner, payload: Object.freeze(Object.assign({}, payload)), quote: Object.freeze({ ...display }) });
}
export function read<K extends ProgressionKind>(kind: K, owner: string): ProgressionPending<K> | null {
  try {
    const value = record(JSON.parse(sessionStorage.getItem(storageKey(kind, owner)) || 'null'));
    return value.owner === owner && value.kind === kind && id(owner) && validPayload(kind, value.payload) ? freeze(kind, owner, value.payload, quote(value.quote ?? {})) : null;
  } catch { return null; }
}
export function save<K extends ProgressionKind>(kind: K, owner: string, payload: unknown, display: unknown = {}): ProgressionPending<K> {
  if (!id(owner) || !validPayload(kind, payload)) throw new Error('操作数据无效，请重新核对');
  const value = freeze(kind, owner, payload, quote(display));
  try { sessionStorage.setItem(storageKey(kind, owner), JSON.stringify(value)); } catch { /* Keep the immutable request in memory until confirmed. */ }
  return value;
}
export function clear(kind: ProgressionKind, owner: string): void { try { sessionStorage.removeItem(storageKey(kind, owner)); } catch { /* Storage can be disabled. */ } }
export function rejected(error: unknown): boolean { return ['progression.invalid', 'progression.settings_changed', 'progression.disabled', 'progression.ineligible', 'progression.limit', 'progression.membership_changed', 'progression.pending', 'progression.user_not_found', 'currency.invalid', 'currency.insufficient', 'currency.settings_conflict', 'currency.disabled', 'currency.not_found', 'request.invalid_json'].includes(failure(error).code || ''); }
