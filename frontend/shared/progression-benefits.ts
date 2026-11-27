import { record, string, node } from './ledger-data';
export function planBenefits(plan: unknown): string[] {
  const benefits = record(plan).benefits;
  if (benefits === undefined) return [];
  if (!Array.isArray(benefits) || benefits.length > 8 || benefits.some(item => typeof item !== 'string' || !item || item.trim() !== item || [...item].length > 80 || /[\p{Cc}\p{Cf}<>]/u.test(item)) || new Set(benefits).size !== benefits.length) throw new Error('权益说明最多 8 项，每项 1–80 个可见字符，且不能重复');
  return benefits.map(string);
}
export const benefitLines = (raw: string) => planBenefits({ benefits: raw.split(/\r\n|\r|\n/).map(item => item.trim()).filter(Boolean) });
export function benefitsList(items: string[]): HTMLUListElement { const list = node('ul', 'progression-benefits'); items.forEach(item => list.append(node('li', '', item))); return list; }
