import { object, text, flag, integer } from './account-data';
export const registrationFields = ['display_name', 'username', 'email', 'phone', 'password', 'invitation_code', 'registration_code'] as const;
export type RegistrationField = typeof registrationFields[number];
export type FieldMode = 'hidden' | 'optional' | 'required';
export type ContactChannel = 'email' | 'phone';
export type AccessFlow = 'register' | 'recover' | 'login' | 'bind';
export interface RegistrationPolicy {
  configured: boolean; registration: 'open' | 'invite' | 'closed';
  fields: Record<RegistrationField, FieldMode>;
  email_available: boolean; sms_available: boolean; recovery_available: boolean;
  email_verification: boolean; phone_verification: boolean; password_confirmation: boolean;
  code_login_enabled: boolean; registration_code_url: string; oauth_bindings: RegistrationField[];
  phone_region: { selectable: boolean; default_code: string; codes: string[] };
}
export interface PendingBinding { provider: string; binding_id: string; display_name: string; existing: boolean; required: RegistrationField[]; email: string; phone: string; username: string }
const fields = (value: unknown): RegistrationField[] => {
  if (!Array.isArray(value) || value.some(value => !registrationFields.includes(value))) throw new Error('账号绑定字段格式错误');
  return value;
};
export const isChannel = (value: unknown): value is ContactChannel => value === 'email' || value === 'phone';
export const isField = (value: unknown): value is RegistrationField => typeof value === 'string' && registrationFields.some(key => value === key);
export const isFlow = (value: unknown): value is AccessFlow => ['register', 'recover', 'login', 'bind'].some(key => value === key);
export function parsePolicy(value: unknown): RegistrationPolicy {
  const row = object(value), modes = object(row.fields), parsed = {} as Record<RegistrationField, FieldMode>;
  for (const key of registrationFields) {
    const mode = modes[key]; if (mode !== 'hidden' && mode !== 'optional' && mode !== 'required') throw new Error('注册字段规则格式错误'); parsed[key] = mode;
  }
  if (row.registration !== 'open' && row.registration !== 'invite' && row.registration !== 'closed') throw new Error('注册开放状态格式错误');
  // Older compatible policy responses omit these optional settings.
  const region = row.phone_region === undefined ? { selectable: true, default_code: '+86', codes: ['+86'] } : object(row.phone_region);
  const code = (value: unknown) => { const result = text(value); if (!/^\+[1-9]\d{0,3}$/.test(result)) throw new Error('电话区号格式错误'); return result; };
  if (!Array.isArray(region.codes) || !region.codes.length) throw new Error('电话区号列表格式错误');
  const codes = region.codes.map(code), defaultCode = code(region.default_code);
  if (!codes.includes(defaultCode)) throw new Error('默认电话区号不在列表中');
  return { configured: flag(row.configured), registration: row.registration, fields: parsed,
    email_available: flag(row.email_available), sms_available: flag(row.sms_available), recovery_available: flag(row.recovery_available),
    email_verification: flag(row.email_verification), phone_verification: flag(row.phone_verification), password_confirmation: flag(row.password_confirmation),
    code_login_enabled: row.code_login_enabled === undefined ? true : flag(row.code_login_enabled),
    registration_code_url: row.registration_code_url === undefined ? '' : text(row.registration_code_url),
    oauth_bindings: row.oauth_bindings == null ? [] : fields(row.oauth_bindings),
    phone_region: { selectable: flag(region.selectable), default_code: defaultCode, codes },
  };
}
export function parseBinding(value: unknown): PendingBinding {
  const row = object(value);
  return { provider: text(row.provider), binding_id: text(row.binding_id), display_name: text(row.display_name),
    existing: row.existing === undefined ? false : flag(row.existing), required: row.required == null ? [] : fields(row.required),
    email: row.email === undefined ? '' : text(row.email), phone: row.phone === undefined ? '' : text(row.phone), username: row.username === undefined ? '' : text(row.username) };
}
export function parseCode(value: unknown): { challenge: string; retry_after: number } {
  const row = object(value), challenge = text(row.challenge);
  if (!challenge) throw new Error('验证码发送结果尚未确认，请重试');
  return { challenge, retry_after: integer(row.retry_after) };
}
export interface AccessFailure extends Error { fields?: Record<string, string>; code?: string; definitive?: boolean; retryAfter?: number }
export function accessFailure(value: unknown): AccessFailure {
  if (value instanceof Error) return value;
  const row = value && typeof value === 'object' ? object(value) : {};
  const error = new Error(typeof row.message === 'string' ? row.message : '操作失败，请重试');
  return Object.assign(error, { fields: row.fields && typeof row.fields === 'object' ? Object.fromEntries(Object.entries(object(row.fields)).filter((entry): entry is [string, string] => typeof entry[1] === 'string')) : undefined });
}
