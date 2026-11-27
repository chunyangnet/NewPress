// Monetary and experience values cross the API as decimal strings, never floats.
export const maximum = 9223372036854775807n;
export function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('返回数据格式未确认，请重新载入');
  return value as Record<string, unknown>;
}
export function string(value: unknown): string { if (typeof value !== 'string') throw new Error('文字数据格式未确认'); return value; }
export function boolean(value: unknown): boolean { if (typeof value !== 'boolean') throw new Error('状态数据格式未确认'); return value; }
export function number(value: unknown, min = 0, max = Number.MAX_SAFE_INTEGER): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < min || value > max) throw new Error('数值数据格式未确认'); return value;
}
export function integer(raw: unknown, signed = false): bigint {
  if (typeof raw !== 'string' || !(signed ? /^-?(0|[1-9][0-9]*)$/ : /^(0|[1-9][0-9]*)$/).test(raw) || raw === '-0' || raw.length > 20) throw new Error('请输入普通整数');
  const value = BigInt(raw); if (value > maximum || value < -maximum) throw new Error('整数超出允许范围'); return value;
}
export const id = (raw: unknown): raw is string => { try { return integer(raw) > 0n; } catch { return false; } };
export const version = (raw: unknown): raw is string => { try { return integer(raw) >= 0n; } catch { return false; } };
export const requestID = (raw: unknown): raw is string => typeof raw === 'string' && /^[A-Za-z0-9_-]{16,80}$/.test(raw);
export const key = (raw: unknown): raw is string => typeof raw === 'string' && /^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$/.test(raw);
export function identifier(value: unknown): string { if (!id(value)) throw new Error('账号或记录编号未确认'); return value; }
export function decimal(value: unknown, signed = false): string { integer(value, signed); return string(value); }
export function date(value: unknown): string { const result = string(value); if (result && !Number.isFinite(Date.parse(result))) throw new Error('时间数据格式未确认'); return result; }
export function list<T>(value: unknown, parse: (item: unknown) => T): T[] { if (!Array.isArray(value)) throw new Error('列表数据格式未确认'); return value.map(parse); }
export function failure(value: unknown): Error & { code?: string; status?: number } {
  const row = value && typeof value === 'object' ? value as Record<string, unknown> : {};
  const error = new Error(typeof row.message === 'string' && row.message ? row.message : '操作未完成，请重试');
  if (typeof row.name === 'string') error.name = row.name;
  return Object.assign(error, { code: typeof row.code === 'string' ? row.code : undefined, status: typeof row.status === 'number' ? row.status : undefined });
}
export const nonce = () => crypto.randomUUID ? crypto.randomUUID() : [...crypto.getRandomValues(new Uint8Array(20))].map(value => value.toString(16).padStart(2, '0')).join('');
export function node<K extends keyof HTMLElementTagNameMap>(tag: K, className = '', text = ''): HTMLElementTagNameMap[K] {
  const element = document.createElement(tag); element.className = className; element.textContent = text; return element;
}
export function message(element: HTMLElement, text: string, tone = ''): void { element.textContent = text; element.hidden = !text; element.dataset.tone = tone; }
