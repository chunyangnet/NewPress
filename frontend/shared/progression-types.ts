export interface ProgressionLevel { key: string; name: string; color: string; threshold: string }
export interface Membership { version: string; plan: string; since: string; until: string; revoked: boolean }
export interface MembershipPlan { key: string; name: string; description: string; benefits?: string[]; color: string; enabled: boolean; days: number; experience_percent: number; purchase_enabled: boolean; currency: string; price: string }
export interface ProgressionSettings {
  version: string; experience: { enabled: boolean; name: string; maximum: string; daily_limit: string; require_verified_email: boolean };
  levels?: { enabled: boolean; public_badges?: boolean; items: ProgressionLevel[] };
  check_in: { enabled: boolean; amount: string; mode?: string; timezone?: string; interval_hours?: number };
  membership: { enabled: boolean; public_badges?: boolean; plans: MembershipPlan[] };
}
export interface ProgressionEntry { id: string; user_id: string; actor_id: string; kind: string; delta: string; balance: string; reason: string; created_at: string; membership?: Membership }
export interface ProgressionOrder { user_id: string; request_id: string; status: string; version: string; member_version: string; plan: string; mode: 'grant' | 'renew'; plan_name: string; days: number; amount: string; symbol: string; currency: string }
export interface ProgressionSnapshot {
  account: { user_id: string; experience: string; membership: Membership }; settings: ProgressionSettings;
  level: ProgressionLevel | null; next_level: ProgressionLevel | null; membership_active: boolean; earned_today: string;
  next_check_in: string; pending_order: ProgressionOrder | null; entries: ProgressionEntry[]; next_before: string;
  currencies: { key: string; name: string; symbol: string; precision: number; enabled: boolean }[]; verified_email: boolean;
}
interface ProgressionPayloadBase { version: string; request_id: string }
export interface ProgressionPayloadMap {
  'check-in': ProgressionPayloadBase;
  purchase: ProgressionPayloadBase & { member_version: string; plan: string; mode: 'grant' | 'renew' };
  experience: ProgressionPayloadBase & { user_id: string; reason: string; delta: string };
  membership: ProgressionPayloadBase & { user_id: string; reason: string; member_version: string; action: 'grant' | 'renew' | 'revoke'; days: number; plan: string };
}
export type ProgressionKind = keyof ProgressionPayloadMap;
export interface ProgressionQuote { plan?: string; days?: number; amount?: string; symbol?: string; currency?: string; until?: string; username?: string }
export interface ProgressionPending<K extends ProgressionKind> { readonly owner: string; readonly kind: K; readonly payload: Readonly<ProgressionPayloadMap[K]>; readonly quote: Readonly<ProgressionQuote> }
declare global { interface Window { ChunnelProgressionShared?: typeof import('./progression-shared').progression } }
