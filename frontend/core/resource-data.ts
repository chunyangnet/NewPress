import { integer, record } from './editor-tool-data';

export interface ResourceCondition { id: string; kind: string; label: string; values: string[]; amount: number; currency: string; minimum: number; term: string }
export interface ResourceGroup { name: string; conditions: ResourceCondition[] }
export interface ResourceDownload { id: string; name: string; provider: string; url: string; code: string; password: string }
export interface ResourceQuestion { id: string; title: string; options: string[]; answer: string }
export interface ResourceField { id: string; label: string; type: string; required: boolean; options: string[] }
export interface ResourceVariant {
  id: string; name: string; kind: string; body: string; media_url: string;
  downloads: ResourceDownload[]; groups: ResourceGroup[]; questions: ResourceQuestion[]; fields: ResourceField[];
  pass_percent: number; attempts_per_day: number;
}
export interface ResourceItem {
  id: string; title: string; description: string; cover: string; size: string; updated: string; original_price: string; badge: string;
  features: string[]; member_cta: boolean; selection: string; daily_limit: number; total_limit: number;
  starts_at?: string | null; ends_at?: string | null; variants: ResourceVariant[];
}
export interface ResourceConfig { items: ResourceItem[] }
export interface ResourceOptions {
  currencies: { key: string; settings: { name: string; precision: number } }[];
  plans: { key: string; name: string; days: number }[];
  roles: { slug: string; name: string }[];
}
export const emptyResourceOptions = (): ResourceOptions => ({ currencies: [], plans: [], roles: [] });
const invalid = (): never => { throw new Error('资源配置响应不完整，请重新载入后重试。'); };
const object = (value: unknown): Record<string, unknown> => record(value) ? value : invalid();
const text = (value: unknown): string => value === undefined ? '' : typeof value === 'string' ? value : invalid();
const number = (value: unknown): number => value === undefined ? 0 : integer(value) ? value : invalid();
const bool = (value: unknown): boolean => value === undefined ? false : typeof value === 'boolean' ? value : invalid();
const array = (value: unknown): unknown[] => value == null ? [] : Array.isArray(value) ? value : invalid();
const strings = (value: unknown): string[] => array(value).map(value => typeof value === 'string' ? value : invalid());
const id = (value: unknown): string => typeof value === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(value) ? value : invalid();
const timestamp = (value: unknown): string | null | undefined => value == null ? value : typeof value === 'string' && Number.isFinite(Date.parse(value)) ? value : invalid();

export function decodeResourceConfig(value: unknown): ResourceConfig {
  const config = object(value);
  if (!Array.isArray(config.items)) invalid();
  return { items: array(config.items).map(value => {
    const r = object(value);
    const variants = array(r.variants).map(value => {
      const v = object(value);
      const groups = array(v.groups).map(value => {
        const g = object(value);
        const conditions = array(g.conditions).map(value => {
          const c = object(value);
          return { ...c, id: id(c.id), kind: text(c.kind), label: text(c.label), values: strings(c.values), amount: number(c.amount),
            currency: text(c.currency), minimum: number(c.minimum), term: text(c.term) };
        });
        if (!conditions.length) invalid();
        return { ...g, name: text(g.name), conditions };
      });
      if (!groups.length) invalid();
      return { ...v, id: id(v.id), name: text(v.name), kind: text(v.kind), body: text(v.body), media_url: text(v.media_url), groups,
        downloads: array(v.downloads).map(value => { const d = object(value); return { ...d, id: id(d.id), name: text(d.name), provider: text(d.provider), url: text(d.url), code: text(d.code), password: text(d.password) }; }),
        questions: array(v.questions).map(value => { const q = object(value); return { ...q, id: id(q.id), title: text(q.title), options: strings(q.options), answer: text(q.answer) }; }),
        fields: array(v.fields).map(value => { const f = object(value); return { ...f, id: id(f.id), label: text(f.label), type: text(f.type), required: bool(f.required), options: strings(f.options) }; }),
        pass_percent: number(v.pass_percent), attempts_per_day: number(v.attempts_per_day) };
    });
    if (!variants.length) invalid();
    return { ...r, id: id(r.id), title: text(r.title), description: text(r.description), cover: text(r.cover), size: text(r.size), updated: text(r.updated),
      original_price: text(r.original_price), badge: text(r.badge), features: strings(r.features), member_cta: bool(r.member_cta), selection: text(r.selection),
      daily_limit: number(r.daily_limit), total_limit: number(r.total_limit), starts_at: timestamp(r.starts_at), ends_at: timestamp(r.ends_at), variants };
  }) };
}

export function decodeResourceOptions(value: unknown): ResourceOptions {
  const data = object(value);
  return {
    currencies: array(data.currencies).map(value => { const c = object(value), settings = object(c.Settings ?? c.settings), precision = number(settings.precision);
      if (precision > 6 || settings.precision === undefined) invalid();
      return { key: text(c.Key ?? c.key), settings: { name: text(settings.name), precision } }; }),
    plans: array(data.plans).map(value => { const p = object(value); return { key: text(p.key), name: text(p.name), days: number(p.days) }; }),
    roles: array(data.roles).map(value => { const r = object(value); return { slug: text(r.slug), name: text(r.name) }; }),
  };
}

export interface ResourceSubmission { actor: string; resource: string; variant: string; passed: boolean; form_complete: boolean; fields: Record<string, string>; updated_at: string }
export function resourceSubmissions(value: unknown): ResourceSubmission[] {
  const data = object(value);
  if (!Array.isArray(data.items)) invalid();
  return array(data.items).map(value => { const s = object(value), fields: Record<string, string> = {};
    if (s.fields != null) for (const [key, value] of Object.entries(object(s.fields))) fields[key] = text(value);
    return { actor: text(s.actor), resource: text(s.resource), variant: text(s.variant), passed: bool(s.passed), form_complete: bool(s.form_complete), fields, updated_at: text(s.updated_at) };
  });
}

// Traverse only declared object members and numeric array offsets. No inherited
// properties or prototype keys may be used by the editor's data-path protocol.
export function resourcePath(config: ResourceConfig, path: string): unknown {
  let current: unknown = config;
  for (const part of path.split('.')) {
    if (Array.isArray(current)) { if (!/^(0|[1-9]\d*)$/.test(part)) return undefined; current = current[Number(part)]; }
    else if (record(current) && !['__proto__', 'constructor', 'prototype'].includes(part) && Object.hasOwn(current, part)) current = current[part];
    else return undefined;
  }
  return current;
}
export type ResourceFieldValue = string | number | boolean | string[] | null;
export function setResourcePath(config: ResourceConfig, path: string, value: ResourceFieldValue): boolean {
  const parts = path.split('.'), key = parts.pop();
  if (!key || ['id', '__proto__', 'constructor', 'prototype'].includes(key)) return false;
  const parent = resourcePath(config, parts.join('.'));
  if (!record(parent) || !Object.hasOwn(parent, key)) return false;
  const existing = parent[key];
  if (key === 'starts_at' || key === 'ends_at') {
    if (value !== null && (typeof value !== 'string' || !Number.isFinite(Date.parse(value)))) return false;
  } else if (Array.isArray(existing)) {
    if (!Array.isArray(value) || !value.every(item => typeof item === 'string') || !existing.every(item => typeof item === 'string')) return false;
  } else if (typeof existing !== typeof value || typeof value === 'number' && !Number.isSafeInteger(value)) return false;
  parent[key] = value;
  return true;
}
export function resourceCondition(config: ResourceConfig, path: string): ResourceCondition | undefined {
  const match = /^items\.(\d+)\.variants\.(\d+)\.groups\.(\d+)\.conditions\.(\d+)(?:\.[a-z_]+)?$/.exec(path);
  return match ? config.items[Number(match[1])]?.variants[Number(match[2])]?.groups[Number(match[3])]?.conditions[Number(match[4])] : undefined;
}
export function resourceConditions(config: ResourceConfig, path: string): ResourceCondition[] | undefined {
  const match = /^items\.(\d+)\.variants\.(\d+)\.groups\.(\d+)\.conditions$/.exec(path);
  return match ? config.items[Number(match[1])]?.variants[Number(match[2])]?.groups[Number(match[3])]?.conditions : undefined;
}

export function amountUnits(value: unknown, places: number | null): number {
  const text = String(value ?? '').trim();
  if (places === null || !Number.isInteger(places) || places < 0 || places > 6 || text.length > 27 || !/^\d+(?:\.\d+)?$/.test(text)) throw Error('请输入有效的十进制金额。');
  const [whole = '', raw = ''] = text.split('.');
  if (raw.replace(/0+$/, '').length > places) throw Error(`当前货币最多支持 ${places} 位小数。`);
  const units = BigInt(whole) * 10n ** BigInt(places) + BigInt((raw + '0'.repeat(places)).slice(0, places) || '0');
  if (units < 1n || units > 1000000000000n) throw Error('金额必须大于 0，且不能超出允许范围。');
  return Number(units);
}
export function amountText(value: unknown, places: number): string {
  if (!integer(value) || !Number.isInteger(places) || places < 0 || places > 6) throw Error('金额无法读取。');
  const n = BigInt(value), scale = 10n ** BigInt(places);
  return places ? `${n / scale}.${String(n % scale).padStart(places, '0')}` : String(n);
}
