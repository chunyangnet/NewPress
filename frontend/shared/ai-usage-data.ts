import { record, string, boolean, number, list, date } from './ledger-data';

export const statuses = ['success', 'error', 'timeout', 'cancelled'] as const;
export type AIStatus = typeof statuses[number];
export interface TelemetryConfig {
  enabled: boolean; details: boolean; costs: boolean; balances: boolean; charts: boolean;
  retention_days: number; max_records: number;
}
export interface ModelPrice {
  model: string; enabled: boolean; currency: string; mode: 'tokens' | 'image' | 'request'; multiplier: number;
  input?: number; output?: number; cache_read?: number; cache_write?: number; flat?: number;
}
export const tokenKeys = ['prompt_tokens','completion_tokens','total_tokens','reasoning_tokens','cached_tokens','cache_creation_tokens','tool_prompt_tokens'] as const;
export type AIUsage = Partial<Record<typeof tokenKeys[number], number>> & { total_source?: string };
export interface AICost { complete: boolean; amount?: number; currency?: string; pricing?: ModelPrice }
export interface RequestRecord {
  sequence: number; id: string; at: string; day: string; provider_id: string; provider: string; model: string; protocol: string;
  kind: 'text' | 'image'; purpose?: string; user_id: number; status: AIStatus; error_code?: string; upstream_status?: number;
  duration_ms: number; first_token_ms?: number; usage: AIUsage; images: number; cost: AICost;
}
export const totalKeys = ['input_known','output_known','speed_output','requests','success','errors','cancelled','images','input','output','tokens','cached','cache_write','reasoning','usage_known','cost_known','cost_partial','duration_ms','first_token_ms','first_token_known','output_duration_ms'] as const;
export type AITotals = Record<typeof totalKeys[number], number> & { saturated: boolean; costs: Record<string, number> };
export type UsageBucket = AITotals & { day: string; provider_id: string; provider: string; model: string; kind: string; status: string };
export interface UsageProvider { id: string; name: string; models: string[]; balance: boolean }
export interface UsageReport {
  today: string; config: TelemetryConfig; totals: AITotals; trend: UsageBucket[]; models: UsageBucket[]; records: RequestRecord[];
  next: number; retained: number; collapsed: number; persist_error: boolean; persisted_at: string;
  start: string; end: string; timezone: string; providers: UsageProvider[];
}
export interface AIBalance { remaining?: number; used?: number; total?: number; currency: string; checked_at: string; expires_at: string; cached: boolean }
export function finite(value: unknown, min = 0, max = Number.MAX_VALUE): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < min || value > max) throw new Error('AI 数值格式未确认');
  return value;
}
export function choice<const T extends readonly string[]>(value: unknown, values: T): T[number] {
  const result = string(value); if (!values.includes(result)) throw new Error('AI 状态格式未确认'); return result;
}
export function calendarDay(value: unknown): string {
  const result = string(value);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(result) || !Number.isFinite(Date.parse(result+'T12:00:00Z')) || new Date(result+'T12:00:00Z').toISOString().slice(0,10) !== result) throw new Error('AI 统计日期格式未确认');
  return result;
}
export function telemetry(value: unknown): TelemetryConfig {
  const row = record(value), result = { enabled: boolean(row.enabled), details: boolean(row.details), costs: boolean(row.costs), balances: boolean(row.balances), charts: boolean(row.charts), retention_days: number(row.retention_days), max_records: number(row.max_records,100,10000) };
  if (![7,30,90].includes(result.retention_days)) throw new Error('AI 统计保留期未确认');
  return result;
}
export function modelPrice(value: unknown): ModelPrice {
  const row=record(value), result: ModelPrice={model:string(row.model),enabled:boolean(row.enabled),currency:choice(row.currency,['USD','CNY','EUR','GBP','JPY']),mode:choice(row.mode,['tokens','image','request']),multiplier:finite(row.multiplier,.001,1000)};
  for(const key of ['input','output','cache_read','cache_write','flat'] as const)if(row[key]!==undefined)result[key]=finite(row[key],0,1000000);
  return result;
}
export function usage(value: unknown): AIUsage {
  const row=record(value), result: AIUsage={};
  for(const key of tokenKeys)if(row[key]!==undefined)result[key]=number(row[key]);
  if(row.total_source!==undefined)result.total_source=choice(row.total_source,['reported','calculated']);
  return result;
}
export function cost(value: unknown): AICost {
  const row=record(value),result: AICost={complete:boolean(row.complete)};
  if(row.amount!==undefined)result.amount=finite(row.amount);
  if(row.currency!==undefined)result.currency=choice(row.currency,['USD','CNY','EUR','GBP','JPY']);
  if(row.pricing!==undefined)result.pricing=modelPrice(row.pricing);
  if(result.amount!==undefined&&!result.currency)throw new Error('AI 费用币种未确认');
  return result;
}
export function requestRecord(value: unknown): RequestRecord {
  const row=record(value);
  const result: RequestRecord={sequence:number(row.sequence,1),id:string(row.id),at:date(row.at),day:calendarDay(row.day),provider_id:string(row.provider_id),provider:string(row.provider),model:string(row.model),protocol:string(row.protocol),kind:choice(row.kind,['text','image']),user_id:number(row.user_id),status:choice(row.status,statuses),duration_ms:number(row.duration_ms),usage:usage(row.usage),images:number(row.images),cost:cost(row.cost)};
  if(!result.id||!result.at)throw new Error('AI 请求记录未确认');
  for(const key of ['purpose','error_code'] as const)if(row[key]!==undefined)result[key]=string(row[key]);
  if(row.upstream_status!==undefined)result.upstream_status=number(row.upstream_status,0,599);
  if(row.first_token_ms!==undefined)result.first_token_ms=number(row.first_token_ms);
  return result;
}
export function totals(value: unknown): AITotals {
  const row=record(value), result={saturated:boolean(row.saturated),costs:{}} as AITotals;
  for(const key of totalKeys)result[key]=number(row[key]);
  for(const [key,amount] of Object.entries(record(row.costs===null?{}:row.costs)))result.costs[key]=finite(amount);
  if(result.success+result.errors+result.cancelled!==result.requests || ['input_known','output_known','usage_known','cost_known','first_token_known'].some(key=>result[key as typeof totalKeys[number]]>result.requests)||result.cost_partial>result.cost_known)throw new Error('AI 统计口径未确认');
  return result;
}
export function bucket(value: unknown): UsageBucket {
  const row=record(value);return {...totals(row),day:string(row.day),provider_id:string(row.provider_id),provider:string(row.provider),model:string(row.model),kind:string(row.kind),status:string(row.status)};
}
export function usageReport(value: unknown,filter?:URLSearchParams): UsageReport {
  const row=record(value),result: UsageReport={today:calendarDay(row.today),config:telemetry(row.config),totals:totals(row.totals),trend:list(row.trend,bucket),models:list(row.models,bucket),records:list(row.records,requestRecord),next:number(row.next),retained:number(row.retained),collapsed:number(row.collapsed),persist_error:boolean(row.persist_error),persisted_at:date(row.persisted_at),start:calendarDay(row.start),end:calendarDay(row.end),timezone:string(row.timezone),providers:list(row.providers,value=>{const p=record(value);return {id:string(p.id),name:string(p.name),models:list(p.models,string),balance:boolean(p.balance)};})};
  const days=(Date.parse(result.end)-Date.parse(result.start))/86400000;
  if(days<0||days>89||result.end>result.today)throw new Error('AI 统计范围未确认');
  try{new Intl.DateTimeFormat('zh-CN',{timeZone:result.timezone}).format();}catch{throw new Error('AI 统计时区未确认');}
  const seen=new Set<string>(),sequences=new Set<number>();let prior=Number.MAX_SAFE_INTEGER;
  for(const item of result.records){if(seen.has(item.id)||sequences.has(item.sequence)||item.sequence>=prior||item.day<result.start||item.day>result.end)throw new Error('AI 请求分页未确认');seen.add(item.id);sequences.add(item.sequence);prior=item.sequence;}
  if(result.next && (!result.records.length||result.next!==result.records.at(-1)?.sequence))throw new Error('AI 分页游标未确认');
  const trendDays=new Set<string>();for(const item of result.trend){calendarDay(item.day);if(trendDays.has(item.day)||item.day<result.start||item.day>result.end)throw new Error('AI 趋势日期未确认');trendDays.add(item.day);}
  if(new Set(result.providers.map(p=>p.id)).size!==result.providers.length)throw new Error('AI 接口列表重复');
  if(filter){
    for(const key of ['start','end'] as const)if(filter.get(key)&&filter.get(key)!==result[key])throw new Error('AI 统计范围与本次查询不一致');
    for(const item of result.records){
      for(const [key,field] of [['provider','provider_id'],['model','model'],['kind','kind'],['status','status']] as const)if(filter.get(key)&&filter.get(key)!==item[field])throw new Error('AI 请求记录与本次筛选不一致');
      if(filter.get('before')&&item.sequence>=Number(filter.get('before')))throw new Error('AI 请求分页与本次查询不一致');
    }
  }
  return result;
}
export function balanceResult(value: unknown): AIBalance {
  const row=record(value),result: AIBalance={currency:choice(row.currency,['USD','CNY','EUR','GBP','JPY']),checked_at:date(row.checked_at),expires_at:date(row.expires_at),cached:boolean(row.cached)};
  for(const key of ['remaining','used','total'] as const)if(row[key]!==undefined)result[key]=finite(row[key],-Number.MAX_VALUE);
  if(!result.checked_at||!result.expires_at||Date.parse(result.expires_at)<Date.parse(result.checked_at))throw new Error('AI 余额时间未确认');
  return result;
}
