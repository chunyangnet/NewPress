import type { ShopAdminCatalog } from './shop-admin-types';

export function moneyUnits(value: unknown, places = 2): string {
  if (!Number.isInteger(places) || places < 0 || places > 6) throw new Error('货币精度无效。');
  const text = String(value ?? '').trim();
  if (!/^\d+(?:\.\d+)?$/.test(text) || text.length > 64) throw new Error('金额请输入非负十进制数字。');
  const [whole = '', raw = ''] = text.split('.');
  if (raw.replace(/0+$/, '').length > places) throw new Error(`金额最多支持 ${places} 位小数。`);
  const units = BigInt(whole) * 10n ** BigInt(places) + BigInt((raw + '0'.repeat(places)).slice(0, places) || '0');
  if (units > 1000000000000n) throw new Error('金额超出允许范围。');
  return units.toString();
}
export function moneyText(value: string | number | bigint = '0', places = 2): string {
  if (!Number.isInteger(places) || places < 0 || places > 6) throw new Error('货币精度无效。');
  const n = BigInt(value), scale = 10n ** BigInt(places);
  if (n < 0n || n > 1000000000000n) throw new Error('金额超出允许范围。');
  return places ? `${n / scale}.${String(n % scale).padStart(places, '0')}` : String(n);
}
export function equal(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (!a || !b || typeof a !== 'object' || typeof b !== 'object') return false;
  if (Array.isArray(a) || Array.isArray(b)) return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((value, i) => equal(value, b[i]));
  const left = a as Record<string, unknown>, right = b as Record<string, unknown>;
  const keys = Object.keys(left);
  return keys.length === Object.keys(right).length && keys.every(key => Object.hasOwn(right, key) && equal(left[key], right[key]));
}
export function mergeCatalog(base: ShopAdminCatalog, desired: ShopAdminCatalog, latest: ShopAdminCatalog): { catalog: ShopAdminCatalog; conflicts: string[] } {
  const merged = structuredClone(latest), conflicts: string[] = [];
  const labels: Record<string, string> = { stock: '库存', codes: '卡密', variants: '规格', price: '价格', enabled: '上架状态', terms: '购买须知', refund_policy: '退款说明' };
  const mergeFields = <T extends Record<string, unknown>>(before: T, next: T, current: T, label: string, omit: string[] = []): T => {
    const out = structuredClone(current);
    for (const key of Object.keys(next) as (keyof T & string)[]) {
      if (omit.includes(key) || equal(before[key], next[key])) continue;
      if (!equal(current[key], before[key]) && !equal(current[key], next[key])) conflicts.push(label + (labels[key] || '配置'));
      else out[key] = structuredClone(next[key]);
    }
    return out;
  };
  Object.assign(merged, mergeFields(base, desired, latest, '商城：', ['version', 'products']));
  for (const next of desired.products) {
    const before = base.products.find(p => p.key === next.key), index = merged.products.findIndex(p => p.key === next.key);
    if (!before) {
      const current = merged.products[index];
      // A newly created product can already be present after a lost reply.
      // Only the server-owned author may differ from the original creation.
      if (current && !equal({ ...next, description_author_id: current.description_author_id }, current)) conflicts.push(`${next.title}：商品编号`);
      else if (!current) merged.products.push(structuredClone(next));
      continue;
    }
    if (equal(before, next)) continue;
    const current = merged.products[index];
    if (!current) { conflicts.push(`${next.title}：商品已移除`); continue; }
    merged.products[index] = mergeFields(before, next, current, `${next.title}：`);
  }
  return { catalog: merged, conflicts };
}
export function dateInput(value: string | undefined): string {
  if (!value || value.startsWith('0001-')) return '';
  const date = new Date(value); if (!Number.isFinite(date.getTime())) return '';
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}
export function raffleDrawTime(input: string, original?: string): string {
  if (input && original && input === dateInput(original)) return original;
  if (!input || !Number.isFinite(Date.parse(input))) throw new Error('请填写有效的时间。');
  return new Date(input).toISOString();
}
export function raffleAllocation(mode: string, values: string[], tiered: boolean) {
  if (mode !== 'instant' && mode !== 'scheduled') throw new Error('开奖模式无效。');
  const instant = mode === 'instant', limit = instant ? 10000 : 1000;
  const amounts = values.map(value => {
    if (instant) {
      let bps: number;
      try { bps = Number(moneyUnits(value)); } catch { throw new Error('中奖概率请输入最多两位小数的非负数。'); }
      if (tiered && bps < 1) throw new Error('每个奖项至少需要 0.01% 的中奖概率。');
      return bps;
    }
    if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value)) || Number(value) < 1 || Number(value) > 1000) throw new Error('每个奖项的名额应为 1 至 1000 的整数。');
    return Number(value);
  });
  const total = amounts.reduce((sum, amount) => sum + amount, 0), remaining = limit - total;
  const message = instant ? `总中奖概率 ${total / 100}% · ${remaining < 0 ? `超出 ${-remaining / 100}%` : `未中奖 ${remaining / 100}%`}` : `合计 ${total} 个中奖名额${remaining < 0 ? ` · 超出 ${-remaining} 个` : ''}`;
  return { amounts, total, message, valid: remaining >= 0 };
}
