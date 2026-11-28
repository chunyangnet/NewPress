import type { RequestRecord } from './ai-usage-data';
export interface ChartDay { day: string; requests: number; tokens?: number; usage_known?: number; cost_known?: number; costs?: Record<string,number> }
export const numeric = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value);
export const purpose = (record: Pick<RequestRecord,'purpose'|'kind'>) => (({operations:'运维诊断',briefing:'运营简报',summary:'内容总结',seo:'SEO 检查',planning:'选题规划',costs:'AI 成本分析',comment_reply_draft:'评论回复草稿',content_queue:'内容生成队列',topic_suggestions:'分类选题建议'} as Record<string,string>))[record.purpose || ''] || (record.kind==='image'?'图片生成':'文本写作');
export const count = (value: unknown) => numeric(value) ? value.toLocaleString('zh-CN') : '—';
export const compact = (value: unknown) => !numeric(value) ? '—' : Math.abs(value) >= 1e6 ? `${(value/1e6).toFixed(2)}M` : Math.abs(value) >= 1e3 ? `${(value/1e3).toFixed(1)}K` : count(value);
export const duration = (ms: unknown) => !numeric(ms) ? '—' : ms >= 60000 ? `${Math.floor(ms/60000)}分 ${Math.round(ms%60000/1000)}秒` : `${(ms/1000).toFixed(2)}s`;
export const tps = (tokens: unknown, ms: unknown) => numeric(tokens) && numeric(ms) && ms >= 250 ? `${(tokens*1000/ms).toFixed(1)} tps` : '—';
export const money = (value: unknown, currency?: string) => numeric(value) ? `${currency || ''} ${value.toLocaleString('zh-CN',{minimumFractionDigits:2,maximumFractionDigits:6})}` : '未计价';
export function dayOffset(day: string, offset: number) { const date=new Date(`${day}T12:00:00Z`); date.setUTCDate(date.getUTCDate()+offset); return date.toISOString().slice(0,10); }
export function fillDays(start: string,end: string,trend: ChartDay[]) { const map=new Map(trend.map(day=>[day.day,day])); const days=[]; for(let day=start;day<=end && days.length<91;day=dayOffset(day,1)) days.push(map.get(day)||{day,requests:0,tokens:0,usage_known:0,cost_known:0,costs:{}}); return days; }
export function chartValue(day: ChartDay,metric: string,currency='') {
    if(metric==='costs') return day.requests && !day.cost_known ? null : (day.costs?.[currency] ?? 0);
    if(metric==='tokens') return day.requests && !day.usage_known ? null : day.tokens;
    return day.requests;
  }
