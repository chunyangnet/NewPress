/** Service cards share the product preview shape used by the catalog editor. */
export type ShopKind = 'virtual' | 'physical' | 'cdk' | 'donation' | 'raffle' | 'bundle';
export type ServiceMode = 'auto' | 'custom' | 'hidden';
export interface ServiceModule { template?: string; icon: string; title: string; description: string; link?: string }
export interface ServiceConfig { mode: ServiceMode; items?: ServiceModule[] }
export interface ServiceTemplate extends ServiceModule { template: string; category: string }
export interface ServiceLibrary { templates: ServiceTemplate[]; icons: { key: string; title: string }[]; limit: number }
export interface ServiceCurrency { key: string; name: string; symbol: string; precision: number }
export interface ServiceProduct {
  key?: string; kind: ShopKind; currency: string; precision?: number; shipping_fee?: string;
  release_at?: string; balance_due_at?: string; services?: ServiceConfig | null;
  group?: { size: number; hours: number } | null;
  installment?: { periods: (number | string)[]; interval_days: number; interest_bps: number } | null;
  raffle?: { mode: string; draw_at?: string; win_bps: number; winners: number } | null;
  bundle?: { product: string }[] | null;
}
export interface ServiceContext { products?: ServiceProduct[]; currencies?: ServiceCurrency[] }
export interface ServiceEditorOptions {
  root: HTMLElement;
  getLibrary(): unknown;
  getProduct(): ServiceProduct | null;
  getContext(): ServiceContext;
  isLocked(): boolean;
  onChange(): void;
}
