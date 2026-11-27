import type {} from './progression-types';
import { integer, id, version, key, requestID, nonce, node, message } from './ledger-data';
import { color, date, badge, levelNumber, progress, levelFor, remainingDaily, membershipDays, membershipActive, membershipEnd, summary, history } from './progression-display';
import { planBenefits, benefitLines, benefitsList } from './progression-benefits';
import { validateSnapshot } from './progression-data';
import { validPayload, read, save, clear, rejected } from './progression-pending';
import { transport } from './progression-transport';
function canonical(value: unknown): unknown {
  return Array.isArray(value) ? value.map(canonical) : value && typeof value === 'object' ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0).map(([key, value]) => [key, canonical(value)])) : value;
}
const equal = (a: unknown, b: unknown) => JSON.stringify(canonical(a)) === JSON.stringify(canonical(b));
export const progression = Object.freeze({ integer, id, version, key, requestID, nonce, color, date, node, badge, levelNumber, message, progress, levelFor, remainingDaily, membershipDays, membershipActive, membershipEnd, planBenefits, benefitLines, benefitsList, summary, history, validateSnapshot, validPayload, read, save, clear, rejected, transport, equal });
window.ChunnelProgressionShared = progression;
