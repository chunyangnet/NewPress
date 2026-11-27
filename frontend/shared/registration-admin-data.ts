import { record, string, boolean, number, list, date } from './ledger-data';
import { choice } from './admin-config-data';
export interface CodeRules { enabled: boolean; max_uses: number; starts_at?: string | null; expires_at?: string | null; note: string }
export interface RegistrationCode extends CodeRules { id: string; code: string; used: number; version: number; created_at: string; updated_at: string; last_used_at?: string }
export interface CodePolicy { revision: string; mode: 'hidden' | 'optional' | 'required'; registration: 'open' | 'closed' | 'invite'; code_url: string }
export interface CodeGenerator { count: number; prefix: string; length: number; charset: string; group: number }
export interface CodeTarget { id: string; version: number }
export type CodeMutation = { action: 'create'; rules: CodeRules; codes?: string[]; generator?: CodeGenerator } | { action: 'generate'; rules: CodeRules; codes?: string[]; generator?: CodeGenerator } | { action: 'update'; targets: CodeTarget[]; patch?: Partial<CodeRules> } | { action: 'delete'; targets: CodeTarget[]; patch?: Partial<CodeRules> };
export const ruleKeys = ['enabled', 'max_uses', 'starts_at', 'expires_at', 'note'] as const;
const stamp = (value: unknown) => { const result = date(value); if (!result) throw new Error('注册码时间缺失'); return result; };
export function registrationCode(value: unknown): RegistrationCode {
  const row = record(value), id = string(row.id), code = string(row.code), note = string(row.note);
  if (!/^[a-f0-9]{32}$/.test(id) || !/^[A-Za-z0-9_-]{4,128}$/.test(code) || [...note].length > 200) throw new Error('注册码数据未确认');
  const result = { id, code, note, enabled: boolean(row.enabled), max_uses: number(row.max_uses, 0, 1000000), used: number(row.used), version: number(row.version, 1), created_at: stamp(row.created_at), updated_at: stamp(row.updated_at),
    ...(row.starts_at !== undefined ? { starts_at: stamp(row.starts_at) } : {}), ...(row.expires_at !== undefined ? { expires_at: stamp(row.expires_at) } : {}), ...(row.last_used_at !== undefined ? { last_used_at: stamp(row.last_used_at) } : {}) };
  if (Date.parse(result.updated_at) < Date.parse(result.created_at) || result.max_uses && result.used > result.max_uses || result.starts_at && result.expires_at && Date.parse(result.starts_at) >= Date.parse(result.expires_at) || (result.used > 0) !== !!result.last_used_at) throw new Error('注册码状态不一致');
  return result;
}
export function codeItems(value: unknown, max = 5000): RegistrationCode[] {
  const items = list(value, registrationCode);
  if (items.length > max || new Set(items.map(c => c.id)).size !== items.length || new Set(items.map(c => c.code)).size !== items.length) throw new Error('注册码清单重复或过长'); return items;
}
export function codePolicy(value: unknown): CodePolicy {
  const row = record(value), revision = string(row.revision); if (!revision) throw new Error('注册配置版本未确认');
  return { revision, mode: choice(row.mode, ['hidden', 'optional', 'required']), registration: choice(row.registration, ['open', 'closed', 'invite']), code_url: string(row.code_url) };
}
export function codeInventory(value: unknown) {
  const row = record(value); return { items: codeItems(row.items), policy: codePolicy(row.policy), now: stamp(row.now), max_codes: number(row.max_codes, 1, 5000), max_batch: number(row.max_batch, 1, 500) };
}
const seconds = (value: string | null | undefined) => value ? Math.floor(Date.parse(value) / 1000) : null;
function matchingRules(actual: RegistrationCode, wanted: CodeRules) {
  if (actual.enabled !== wanted.enabled || actual.max_uses !== wanted.max_uses || actual.note !== wanted.note.trim() || seconds(actual.starts_at) !== seconds(wanted.starts_at) || seconds(actual.expires_at) !== seconds(wanted.expires_at)) throw new Error('注册码回执与提交规则不一致');
}
export function codeReceipt(value: unknown, mutation: CodeMutation, before: RegistrationCode[]) {
  const row = record(value), items = codeItems(row.items, 500), count = number(row.count, 1, 500);
  if (items.length !== count) throw new Error('注册码回执数量不一致');
  if (mutation.action === 'create' || mutation.action === 'generate') {
    const expected = mutation.action === 'create' ? mutation.codes?.length : mutation.generator?.count;
    if (count !== expected) throw new Error('注册码创建数量不一致');
    for (const item of items) {
      if (item.version !== 1 || item.used || before.some(old => old.id === item.id || old.code === item.code) || mutation.action === 'create' && !mutation.codes?.includes(item.code.trim())) throw new Error('注册码创建回执未确认');
      if (mutation.generator) {
        const g = mutation.generator, body = item.code.slice(g.prefix.length), alphabet: Record<string, RegExp> = { friendly: /^[23456789ABCDEFGHJKLMNPQRSTUVWXYZ]+$/, mixed: /^[A-Za-z0-9]+$/, digits: /^[0-9]+$/, hex: /^[0-9A-F]+$/ };
        const groups = g.group ? body.split('-') : [body];
        if (!item.code.startsWith(g.prefix) || groups.join('').length !== g.length || !alphabet[g.charset]?.test(groups.join('')) || g.group && groups.some((group, i) => group.length !== (i < groups.length - 1 ? g.group : g.length % g.group || g.group))) throw new Error('注册码生成格式未确认');
      }
      matchingRules(item, mutation.rules);
    }
  } else {
    if (count !== mutation.targets.length) throw new Error('注册码操作范围不一致');
    for (const item of items) {
      const target = mutation.targets.find(t => t.id === item.id), old = before.find(c => c.id === item.id);
      if (!old || !target || target.version !== old.version || item.version !== old.version + (mutation.action === 'update' ? 1 : 0) || item.code !== old.code || item.used !== old.used || item.created_at !== old.created_at || item.last_used_at !== old.last_used_at) throw new Error('注册码操作版本或对象未确认');
      matchingRules(item, mutation.action === 'update' ? { ...old, ...mutation.patch } : old);
    }
  }
  return { items, count };
}
export function policyReceipt(value: unknown, sent: Pick<CodePolicy, 'revision' | 'mode' | 'code_url'>): CodePolicy {
  const saved = codePolicy(value); if (saved.revision === sent.revision || saved.mode !== sent.mode || saved.code_url !== sent.code_url.trim()) throw new Error('注册接入设置回执未确认'); return saved;
}
