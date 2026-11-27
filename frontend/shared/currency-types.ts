interface PendingBase { owner: string; currency_key: string; request_id: string }
export interface TransferDraft { currency_key: string; version: string; recipient_id: string; recipient: string; amount: string; precision: number; symbol?: string }
export interface CurrencyPendingMap {
  'check-in': PendingBase & { version: string };
  transfer: PendingBase & TransferDraft;
  adjust: PendingBase & { version: string; user_id: string; delta: string; reason: string; precision: number };
  create: PendingBase & { key: string; name: string; symbol: string; precision: number };
}
export type CurrencyKind = keyof CurrencyPendingMap;
export type CurrencyPending<K extends CurrencyKind> = Readonly<CurrencyPendingMap[K]>;
export interface CurrencySettings {
  version: string; enabled: boolean; name: string; symbol: string; precision: number; allow_transfers: boolean;
  min_transfer: string; max_transfer: string; daily_transfer_limit: string; daily_reward_limit: string;
  check_in_enabled: boolean; check_in_reward: string; check_in_mode: string; check_in_timezone: string; check_in_interval_hours: number;
  registration_enabled: boolean; registration_reward: string; publication_enabled: boolean; publication_reward: string;
  publication_min_length: number; publication_daily_limit: number;
}
export interface CurrencyEntry { id: string; kind: string; label: string; delta: string; balance: string; incoming: boolean; created_at: string; reason?: string; peer?: string }
export interface WalletSnapshot {
  currency_key: string; user_id: string; settings: CurrencySettings; balance: string; transferred_today: string; transfer_remaining: string;
  rewarded_today: string; reward_remaining: string; verification_required: boolean; check_in_ready: boolean; next_check_in_at?: string;
  entries: CurrencyEntry[]; next_before: string;
}
declare global { interface Window { ChunnelCurrency?: typeof import('./currency-shared').currency } }
