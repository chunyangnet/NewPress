export interface ComponentItem {
  title: string;
  text: string;
  image: string;
  url: string;
  value: number;
}
export interface ComponentConfig extends ComponentItem {
  v: number;
  id: string;
  kind: string;
  tone: string;
  style: string;
  width: string;
  density: string;
  caption: string;
  sender: string;
  amount: string;
  currency: string;
  mode: string;
  provider: string;
  uid: string;
  language: string;
  layout: string;
  unit: string;
  max: number;
  count: number;
  columns: number;
  loop: boolean;
  open: boolean;
  items: ComponentItem[];
  rows: string[][];
}
export const emptyItem = (): ComponentItem => ({title: '', text: '', image: '', url: '', value: 0});
export const emptyConfig = (): ComponentConfig => ({...emptyItem(), v: 1, id: '', kind: '', tone: 'theme', style: 'soft', width: 'full', density: 'normal', caption: '', sender: '', amount: '', currency: '', mode: '', provider: '', uid: '', language: '', layout: '', unit: '', max: 0, count: 0, columns: 0, loop: false, open: false, items: [], rows: []});
function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('组件格式无效');
  return value as Record<string, unknown>;
}
function scalarFields<T extends ComponentItem>(base: T, value: Record<string, unknown>): T {
  for (const key of Object.keys(base) as (keyof T)[]) {
    const field = value[String(key)];
    if (field === undefined || typeof base[key] === 'object') continue;
    if (typeof field !== typeof base[key] || (typeof field === 'number' && !Number.isFinite(field))) throw new Error('组件字段格式无效');
    // Each field is checked against its concrete default immediately above.
    base[key] = field as T[keyof T];
  }
  return base;
}
export function parseComponentConfig(input: unknown, kinds: Record<string, unknown>): ComponentConfig {
  const value = record(input);
  if (value.v !== 1 || typeof value.kind !== 'string' || !Object.hasOwn(kinds, value.kind) || typeof value.id !== 'string' || !/^[\w-]{12,64}$/.test(value.id)) throw new Error('组件格式无效');
  const config = scalarFields(emptyConfig(), value);
  if (value.items !== undefined) {
    if (!Array.isArray(value.items) || value.items.length > 30) throw new Error('组件条目格式无效');
    config.items = value.items.map(item => scalarFields(emptyItem(), record(item)));
  }
  if (value.rows !== undefined) {
    if (!Array.isArray(value.rows) || value.rows.length > 50) throw new Error('组件表格格式无效');
    config.rows = value.rows.map(row => {
      if (!Array.isArray(row) || row.length > 10 || row.some(cell => typeof cell !== 'string')) throw new Error('组件表格格式无效');
      return row as string[];
    });
  }
  return config;
}
