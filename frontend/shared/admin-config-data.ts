import { record, string, boolean, number, list } from './ledger-data';

export const loginIDs = ['qq', 'github', 'wechat', 'microsoft', 'google', 'linuxdo'] as const;
export const smsIDs = ['aliyun', 'tencent', 'huawei', 'yunpian', 'twilio', 'custom'] as const;
export const verificationIDs = ['local', 'geetest', 'tencent', 'turnstile', 'hcaptcha', 'recaptcha_v2', 'recaptcha_v3'] as const;
export const sceneIDs = ['comment', 'register', 'recover', 'login', 'admin_login', 'feedback'] as const;
export type LoginID = typeof loginIDs[number];
export type SMSID = typeof smsIDs[number];
export type VerificationID = typeof verificationIDs[number];
export type SceneID = typeof sceneIDs[number];
export interface Secret { secret: string; has_secret: boolean; clear_secret: boolean }
export interface LoginProvider extends Secret { id: LoginID; enabled: boolean; client_id: string }
export const smsStrings = ['access_key', 'region', 'app_id', 'sign_name', 'template_id', 'template_params', 'sender', 'message', 'endpoint', 'encoding', 'auth', 'auth_header', 'payload', 'success_path', 'success_value'] as const;
export type SMSString = typeof smsStrings[number];
export type SMSSettings = Secret & Record<SMSString, string> & { enabled: boolean; provider: SMSID; timeout_seconds: number };
export interface AccessSettings { revision: string; callback_base_url: string; providers: LoginProvider[]; sms: SMSSettings }
export interface LoginInfo { id: LoginID; name: string; callback: string; application_url: string }
export interface SMSInfo { id: SMSID; name: string; description: string; docs: string }
export interface AccessConfig { settings: AccessSettings; providers: LoginInfo[]; sms_providers: SMSInfo[]; default_callback_base_url: string; sms_ready: boolean }
export interface VerificationProvider extends Secret { id: Exclude<VerificationID, 'local'>; site_key: string; secret_id: string; api_secret: string; has_api_secret: boolean; clear_api_secret: boolean; recaptcha_net: boolean; min_score: number }
export interface VerificationRule { enabled: boolean; provider: VerificationID | ''; skip_members: boolean }
export interface VerificationSettings { revision: string; enabled: boolean; default_provider: VerificationID; theme: 'auto' | 'light' | 'dark'; language: 'auto' | 'zh-CN' | 'en'; timeout_seconds: number; scenes: Record<SceneID, VerificationRule>; providers: VerificationProvider[] }
export interface VerificationInfo { id: VerificationID; name: string; network: 'local' | 'domestic' | 'test' | 'restricted'; network_note: string; description: string; docs: string }
export interface VerificationConfig { settings: VerificationSettings; providers: VerificationInfo[] }

export function choice<T extends string>(value: unknown, choices: readonly T[]): T {
  if (typeof value !== 'string' || !choices.includes(value as T)) throw new Error('返回的配置选项未确认');
  return value as T;
}
export function text(value: unknown, max = 2048): string {
  const result = string(value); if (result.length > max) throw new Error('返回的配置文字过长'); return result;
}
function revision(value: unknown) { const result = text(value, 256); if (!result) throw new Error('配置版本未确认'); return result; }
function optionalBoolean(value: unknown) { return value === undefined ? false : boolean(value); }
function redacted(value: unknown) { if (value !== undefined && value !== '') throw new Error('服务端意外返回了密钥，请重新载入'); return ''; }
function secret(row: Record<string, unknown>): Secret { return { secret: redacted(row.secret), has_secret: optionalBoolean(row.has_secret), clear_secret: false }; }
function unique<T extends { id: string }>(items: T[], max: number): T[] {
  if (items.length > max || new Set(items.map(item => item.id)).size !== items.length) throw new Error('配置服务重复或数量异常'); return items;
}
export function docsURL(value: unknown): string {
  const raw = text(value); if (!raw) return '';
  const url = new URL(raw); if (url.protocol !== 'https:' || url.username || url.password) throw new Error('服务商链接未确认'); return raw;
}
export function accessSettings(value: unknown): AccessSettings {
  const row = record(value), sms = record(row.sms);
  const fields = Object.fromEntries(smsStrings.map(key => [key, text(sms[key], 16384)])) as Record<SMSString, string>;
  return { revision: revision(row.revision), callback_base_url: text(row.callback_base_url),
    providers: unique(list(row.providers, value => { const p = record(value); return { id: choice(p.id, loginIDs), enabled: boolean(p.enabled), client_id: text(p.client_id, 200), ...secret(p) }; }), 6),
    sms: { ...fields, ...secret(sms), enabled: boolean(sms.enabled), provider: choice(sms.provider, smsIDs), timeout_seconds: number(sms.timeout_seconds, 3, 20) } };
}
export function accessConfig(value: unknown): AccessConfig {
  const row = record(value);
  const result = { settings: accessSettings(row.settings), providers: unique(list(row.providers, value => { const p = record(value); return { id: choice(p.id, loginIDs), name: text(p.name), callback: text(p.callback), application_url: docsURL(p.application_url) }; }), 6),
    sms_providers: unique(list(row.sms_providers, value => { const p = record(value); return { id: choice(p.id, smsIDs), name: text(p.name), description: text(p.description), docs: docsURL(p.docs) }; }), 6),
    default_callback_base_url: text(row.default_callback_base_url), sms_ready: boolean(row.sms_ready) };
  if (result.providers.length !== 6 || result.sms_providers.length !== 6) throw new Error('登录或短信服务清单不完整'); return result;
}
export function verificationSettings(value: unknown): VerificationSettings {
  const row = record(value), rules = record(row.scenes);
  const scenes = Object.fromEntries(sceneIDs.map(id => { const r = record(rules[id]); return [id, { enabled: boolean(r.enabled), provider: choice(r.provider, ['', ...verificationIDs]), skip_members: boolean(r.skip_members) }]; })) as Record<SceneID, VerificationRule>;
  const providers = unique(list(row.providers, value => { const p = record(value), score = p.min_score;
    if (typeof score !== 'number' || !Number.isFinite(score) || score < 0.1 || score > 0.9) throw new Error('验证分数未确认');
    const id = choice(p.id, verificationIDs); if (id === 'local') throw new Error('本地验证不应含密钥配置');
    return { id, site_key: text(p.site_key), ...secret(p), secret_id: text(p.secret_id ?? ''), api_secret: redacted(p.api_secret), has_api_secret: optionalBoolean(p.has_api_secret), clear_api_secret: false, recaptcha_net: boolean(p.recaptcha_net), min_score: score };
  }), 6);
  return { revision: revision(row.revision), enabled: boolean(row.enabled), default_provider: choice(row.default_provider, verificationIDs), theme: choice(row.theme, ['auto', 'light', 'dark']), language: choice(row.language, ['auto', 'zh-CN', 'en']), timeout_seconds: number(row.timeout_seconds, 3, 15), scenes, providers };
}
export function verificationConfig(value: unknown): VerificationConfig {
  const row = record(value), providers = unique(list(row.providers, value => { const p = record(value); return { id: choice(p.id, verificationIDs), name: text(p.name), network: choice(p.network, ['local', 'domestic', 'test', 'restricted']), network_note: text(p.network_note), description: text(p.description), docs: docsURL(p.docs) }; }), 7);
  if (providers.length !== 7) throw new Error('验证服务清单不完整'); return { settings: verificationSettings(row.settings), providers };
}
const equal = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
function requireEqual(a: unknown, b: unknown) { if (!equal(a, b)) throw new Error('保存回执与提交内容不一致，请重新加载核对'); }
function secretReceipt(saved: Secret, sent: Secret) {
  requireEqual(saved.has_secret, !!sent.secret.trim() || !sent.clear_secret && sent.has_secret);
}
export function savedAccess(value: unknown, sent: AccessSettings, baseline = sent): AccessConfig {
  const result = accessConfig(value), saved = result.settings;
  if (saved.revision === sent.revision) throw new Error('保存版本没有更新，请重新加载核对');
  requireEqual(saved.callback_base_url, sent.callback_base_url.trim().replace(/\/+$/, ''));
  for (const p of sent.providers) {
    const next = saved.providers.find(item => item.id === p.id); if (!next) throw new Error('保存的登录服务缺失');
    requireEqual([next.enabled, next.client_id], [p.enabled, p.client_id.trim()]);
    const original = baseline.providers.find(item => item.id === p.id);
    secretReceipt(next, { ...p, has_secret: !!original?.has_secret && original.client_id === p.client_id.trim() });
  }
  const trim = new Set<SMSString>(['access_key', 'endpoint', 'sender', 'sign_name', 'template_id', 'app_id', 'region', 'success_path', 'auth_header']);
  for (const key of smsStrings) requireEqual(saved.sms[key], trim.has(key) ? sent.sms[key].trim() : sent.sms[key]);
  requireEqual([saved.sms.enabled, saved.sms.provider, saved.sms.timeout_seconds], [sent.sms.enabled, sent.sms.provider, sent.sms.timeout_seconds || 8]);
  const sameIdentity = (['provider', 'access_key', 'endpoint', 'auth', 'auth_header'] as const).every(key => sent.sms[key].trim() === baseline.sms[key]);
  secretReceipt(saved.sms, { ...sent.sms, has_secret: baseline.sms.has_secret && sameIdentity }); return result;
}
export function savedVerification(value: unknown, sent: VerificationSettings, baseline = sent): VerificationSettings {
  const saved = verificationSettings(value); if (saved.revision === sent.revision) throw new Error('保存版本没有更新，请重新加载核对');
  for (const key of ['enabled', 'default_provider', 'theme', 'language', 'timeout_seconds', 'scenes'] as const) requireEqual(saved[key], sent[key]);
  for (const p of sent.providers) {
    const next = saved.providers.find(item => item.id === p.id); if (!next) throw new Error('保存的验证服务缺失');
    requireEqual([next.site_key, next.secret_id, next.recaptcha_net, next.min_score], [p.site_key.trim(), p.secret_id.trim(), p.recaptcha_net, p.min_score || 0.5]);
    const original = baseline.providers.find(item => item.id === p.id);
    secretReceipt(next, { ...p, has_secret: !!original?.has_secret && original.site_key === p.site_key.trim() });
    requireEqual(next.has_api_secret, !!p.api_secret.trim() || !p.clear_api_secret && !!original?.has_api_secret && original.secret_id === p.secret_id.trim());
  }
  return saved;
}
/** Preserve edits made after submission; retire only credentials in that submission. */
function mergeSecret<T extends Secret>(current: T, sent: T | undefined, saved: T | undefined): void {
  if (!sent || !saved) return;
  current.has_secret = saved.has_secret;
  if (current.secret === sent.secret) current.secret = '';
  if (current.clear_secret === sent.clear_secret) current.clear_secret = false;
}
export function mergeAccess(current: AccessSettings, sent: AccessSettings, saved: AccessSettings): AccessSettings {
  const next = structuredClone(current); next.revision = saved.revision;
  for (const p of next.providers) { const submitted = sent.providers.find(item => item.id === p.id), receipt = saved.providers.find(item => item.id === p.id); if (submitted && p.client_id === submitted.client_id) mergeSecret(p, submitted, receipt); }
  const a = next.sms, b = sent.sms;
  if (['provider', 'access_key', 'endpoint', 'auth', 'auth_header'].every(key => a[key as keyof SMSSettings] === b[key as keyof SMSSettings])) mergeSecret(a, b, saved.sms);
  return next;
}
export function mergeVerification(current: VerificationSettings, sent: VerificationSettings, saved: VerificationSettings): VerificationSettings {
  const next = structuredClone(current); next.revision = saved.revision;
  for (const p of next.providers) {
    const before = sent.providers.find(item => item.id === p.id), after = saved.providers.find(item => item.id === p.id); if (!before || !after) continue;
    if (p.site_key === before.site_key) mergeSecret(p, before, after);
    if (p.secret_id === before.secret_id) { p.has_api_secret = after.has_api_secret; if (p.api_secret === before.api_secret) p.api_secret = ''; if (p.clear_api_secret === before.clear_api_secret) p.clear_api_secret = false; }
  }
  return next;
}
