import { record, string, boolean, number, list, date, identifier, decimal, requestID, key } from './ledger-data';
import { choice } from './admin-config-data';
import { parseProgressionEntry } from './progression-data';
import { currencyAdjustment, adminCurrencySettings } from '../core/ledger-admin-data';

export interface RecognitionBadge { key: string; kind: 'title' | 'medal' | 'verified'; name: string; enabled: boolean }
export interface RecognitionSettings { max_medals: number; badges: RecognitionBadge[] }
export interface RecognitionIdentity {
  version: number; grants: { key: string; label: string; granted_at: string; expires_at: string; actor_id: number }[];
  equipped: string[]; application?: { key: string; label: string; evidence: string; status: string; reason: string; updated_at: string };
  history: { action: string; key: string; reason: string; actor_id: number; created_at: string }[];
}
export interface RecognitionAccount { user_id: number; username: string; name: string; identity: RecognitionIdentity }
export interface UserMutation {
  user_id: string | number; version: string | number; reason: string; request_id?: string; delta?: string;
  member_version?: string; action?: 'set' | 'revoke' | 'grant' | 'approve' | 'reject' | 'equip'; plan?: string; days?: number; since?: string; until?: string;
  key?: string; label?: string; evidence?: string; equipped?: string[];
}
export interface UserOperation { url: string; body: UserMutation; saved: boolean; actor: string; target: string; precision: number; uncertain: boolean }
export function recognitionSettings(value: unknown): RecognitionSettings {
  const row = record(record(value).settings), badges = list(row.badges, value => { const b = record(value); if (!key(b.key)) throw new Error('身份标识编号未确认'); return { key: b.key, kind: choice(b.kind, ['title', 'medal', 'verified']), name: string(b.name), enabled: boolean(b.enabled) }; });
  if (badges.length > 100 || new Set(badges.map(b => b.key)).size !== badges.length) throw new Error('身份标识清单重复或过长');
  return { max_medals: number(row.max_medals, 1, 8), badges };
}
export function recognitionIdentity(value: unknown): RecognitionIdentity {
  const row = record(value), grants = list(row.grants ?? [], value => { const g = record(value); return { key: string(g.key), label: string(g.label), granted_at: date(g.granted_at), expires_at: date(g.expires_at), actor_id: number(g.actor_id, 1) }; });
  const equipped = list(row.equipped ?? [], string), history = list(row.history ?? [], value => { const h = record(value); return { action: string(h.action), key: string(h.key), reason: string(h.reason), actor_id: number(h.actor_id, 1), created_at: date(h.created_at) }; });
  const result: RecognitionIdentity = { version: number(row.version, 1), grants, equipped, history };
  if (new Set(grants.map(g => g.key)).size !== grants.length || new Set(equipped).size !== equipped.length || history.length > 100) throw new Error('身份记录未确认');
  if (row.application != null) { const a = record(row.application); result.application = { key: string(a.key), label: string(a.label), evidence: string(a.evidence), status: choice(a.status, ['pending', 'approved', 'rejected', 'revoked']), reason: string(a.reason), updated_at: date(a.updated_at) }; }
  return result;
}
export function recognitionAccount(value: unknown, target: string, username: string): RecognitionAccount {
  const row = record(value), id = number(row.user_id, 1), name = string(row.username);
  if (String(id) !== target || name.toLowerCase() !== username.trim().toLowerCase()) throw new Error('返回的身份账户不一致');
  return { user_id: id, username: name, name: string(row.name), identity: recognitionIdentity(row.identity) };
}
export function userCurrencies(value: unknown) {
  const currencies = list(record(value).currencies, value => { const row = record(value); if (!key(row.key)) throw new Error('货币编号未确认'); return { key: row.key, settings: adminCurrencySettings(row.settings) }; });
  if (currencies.length > 32 || new Set(currencies.map(c => c.key)).size !== currencies.length) throw new Error('货币清单重复或过长'); return currencies;
}
function mismatch(): never { throw new Error('操作回执未确认，请核对或重试原操作'); }
export function userOperation(value: unknown, actor: string, target: string): UserOperation {
  const row = record(value), b = record(row.body), url = string(row.url);
  if (row.actor !== actor || row.target !== target || String(b.user_id) !== target) mismatch();
  const body: UserMutation = { user_id: target, version: url === '/api/v1/admin/recognition/account' ? number(b.version, 1) : decimal(b.version), reason: string(b.reason) };
  if (url === '/api/v1/admin/recognition/account') {
    body.user_id = number(b.user_id, 1); body.version = number(b.version, 1); body.action = choice(b.action, ['grant', 'revoke', 'approve', 'reject', 'equip'] as const); body.key = string(b.key); body.label = string(b.label); body.evidence = string(b.evidence); body.days = number(b.days, 0, 3650);
    if (b.equipped !== undefined) body.equipped = list(b.equipped, string);
  } else {
    if (!requestID(b.request_id)) mismatch(); body.request_id = b.request_id; identifier(b.user_id);
    if (url === '/api/v1/admin/progression/membership') {
      body.action = choice(b.action, ['set', 'revoke'] as const); body.member_version = decimal(b.member_version); body.plan = string(b.plan); body.days = number(b.days, 0, 0);
      if (body.action === 'set') { body.since = date(b.since); body.until = date(b.until); if (!body.since || !body.until || Date.parse(body.since) >= Date.parse(body.until)) mismatch(); }
    } else if (url === '/api/v1/admin/progression/experience') { body.delta = decimal(b.delta, true); if (body.delta === '0') mismatch(); }
    else if (/^\/api\/v1\/admin\/currencies\/[a-z][a-z0-9-]*\/adjust$/.test(url)) body.delta = string(b.delta);
    else mismatch();
  }
  return { url, body, actor, target, saved: boolean(row.saved), uncertain: boolean(row.uncertain), precision: number(row.precision, 0, 6) };
}
export function recognitionReceipt(value: unknown, operation: UserOperation) {
  const identity = recognitionIdentity(record(value).identity), body = operation.body;
  if (identity.version !== Number(body.version) + 1) mismatch();
  const event = identity.history.at(-1);
  if (!event || event.action !== body.action || event.key !== body.key || event.reason !== body.reason || String(event.actor_id) !== operation.actor || !event.created_at) mismatch();
  if (body.action === 'equip' && JSON.stringify(identity.equipped) !== JSON.stringify(body.equipped || [])) mismatch();
  if (body.action === 'revoke' && (identity.grants.some(g => g.key === body.key) || identity.equipped.includes(body.key || ''))) mismatch();
  if (body.action === 'reject' || body.action === 'approve') if (identity.application?.key !== body.key || identity.application.status !== (body.action === 'reject' ? 'rejected' : 'approved') || identity.application.reason !== body.reason) mismatch();
  if (body.action === 'grant' || body.action === 'approve') {
    const grant = identity.grants.find(g => g.key === body.key), label = body.action === 'approve' ? identity.application?.label : body.label;
    if (!grant || String(grant.actor_id) !== operation.actor || grant.label !== label || grant.granted_at !== event.created_at) mismatch();
    if (body.days === 0 ? !grant.expires_at.startsWith('0001-') : Date.parse(grant.expires_at) - Date.parse(grant.granted_at) !== (body.days || 0) * 86400000) mismatch();
  }
  return identity;
}
export function userReceipt(value: unknown, operation: UserOperation) {
  const body = operation.body;
  if (operation.url.endsWith('/recognition/account')) { recognitionReceipt(value, operation); return; }
  if (operation.url.includes('/currencies/')) { currencyAdjustment(value, { owner: operation.actor, currency_key: operation.url.split('/')[5] || '', request_id: body.request_id || '', version: String(body.version), user_id: String(body.user_id), delta: body.delta || '', reason: body.reason, precision: operation.precision }); return; }
  const row = record(value), raw = record(row.entry), entry = parseProgressionEntry(raw, operation.target); boolean(row.replayed);
  if (entry.actor_id !== operation.actor || entry.reason !== body.reason || !entry.created_at || number(raw.days) !== 0) mismatch();
  if (operation.url.endsWith('/experience')) { if (entry.kind !== 'experience_adjust' || entry.delta !== body.delta) mismatch(); }
  else {
    const m = entry.membership;
    if (!m || entry.kind !== 'membership_' + body.action || entry.delta !== '0' || BigInt(m.version) !== BigInt(body.member_version || '-1') + 1n || m.revoked !== (body.action === 'revoke')) mismatch();
    if (body.action === 'set' && (m.plan !== body.plan || Date.parse(m.since) !== Date.parse(body.since || '') || Date.parse(m.until) !== Date.parse(body.until || ''))) mismatch();
  }
}
