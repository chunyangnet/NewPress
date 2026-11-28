import { record, string, number, boolean, list, date } from './ledger-data';
import { calendarDay, choice, usage } from './ai-usage-data';
import type { AIUsage } from './ai-usage-data';

export const taskIDs=['operations','briefing','summary','seo','planning','costs'] as const;
export type TaskID=typeof taskIDs[number];
export interface Task {id:TaskID;title:string;description:string;scope:string;icon:string}
export interface Provider {id:string;name:string;models:string[];default_model:string}
export interface PreviewInput {task:TaskID;days:number;focus:string;post_id?:number;material?:string}
export interface Preview {token:string;task:TaskID;title:string;scope:string;start:string;end:string;timezone:string;created_at:string;expires_at:string;context:string;focus:string}
export interface ReportSummary {id:string;owner:number;task:TaskID;title:string;created_at:string;provider:string;model:string}
export interface Report extends ReportSummary {content:string;source:Preview}
export type ExportReport=Partial<ReportSummary>&{content?:string;source?:Preview|null};
const nonempty=(value:unknown)=>{const result=string(value);if(!result.trim())throw new Error('AI 报告字段缺失');return result;};
const identifier=(value:unknown)=>{const result=string(value);if(!/^[a-f0-9]{32}$/i.test(result))throw new Error('AI 报告标识未确认');return result;};
const timestamp=(value:unknown)=>{const result=date(value);if(!result)throw new Error('AI 报告时间缺失');return result;};
const zone=(value:unknown)=>{const result=nonempty(value);try{new Intl.DateTimeFormat('zh-CN',{timeZone:result}).format();}catch{throw new Error('AI 报告时区未确认');}return result;};
const bytes=(value:string)=>new TextEncoder().encode(value).length;
export function configuration(value:unknown){
  const row=record(value),tasks=list(row.tasks,value=>{const item=record(value);return {id:choice(item.id,taskIDs),title:nonempty(item.title),description:string(item.description),scope:nonempty(item.scope),icon:nonempty(item.icon)};}),providers=list(row.providers,value=>{const p=record(value),models=list(p.models,nonempty),default_model=nonempty(p.default_model);if(!models.includes(default_model)||models.length>100||new Set(models).size!==models.length)throw new Error('AI 工作台模型目录未确认');return {id:nonempty(p.id),name:nonempty(p.name),models,default_model};});
  const selected=string(row.default_provider);
  if(!tasks.length||new Set(tasks.map(t=>t.id)).size!==tasks.length||providers.length>20||new Set(providers.map(p=>p.id)).size!==providers.length||(providers.length?!providers.some(p=>p.id===selected):selected!==''))throw new Error('AI 工作台配置未确认');
  return {tasks,providers,default_provider:selected,timezone:zone(row.timezone),max_reports:number(row.max_reports,1),retention_days:number(row.retention_days,1)};
}
export function previewSource(value:unknown):Preview {
  const row=record(value),result:Preview={token:string(row.token),task:choice(row.task,taskIDs),title:nonempty(row.title),scope:nonempty(row.scope),start:calendarDay(row.start),end:calendarDay(row.end),timezone:zone(row.timezone),created_at:timestamp(row.created_at),expires_at:timestamp(row.expires_at),context:string(row.context),focus:string(row.focus)};
  const days=(Date.parse(result.end)-Date.parse(result.start))/86400000;
  if(days<0||days>89||Date.parse(result.expires_at)<=Date.parse(result.created_at)||bytes(result.context)>49152)throw new Error('AI 资料范围未确认');
  return result;
}
export function previewReceipt(value:unknown,input:PreviewInput):Preview {
  const result=previewSource(value);identifier(result.token);
  const days=['operations','summary','seo'].includes(input.task)?1:input.days;
  if(result.task!==input.task||result.focus!==input.focus.trim()||(Date.parse(result.end)-Date.parse(result.start))/86400000!==days-1)throw new Error('AI 预览与所选条件不一致');
  if(input.task==='summary'){
    const context=record(JSON.parse(result.context));
    if(input.post_id?record(context.article).id!==input.post_id:context.material!==input.material?.trim())throw new Error('AI 预览材料与本次选择不一致');
  }
  return result;
}
export function reportSummary(value:unknown,owner:string):ReportSummary {
  const row=record(value),result={id:identifier(row.id),owner:number(row.owner),task:choice(row.task,taskIDs),title:string(row.title),created_at:timestamp(row.created_at),provider:string(row.provider),model:string(row.model)};
  if(String(result.owner)!==owner)throw new Error('AI 报告所属账号未确认');return result;
}
export function reportDetail(value:unknown,owner:string,id?:string):Report {
  const row=record(value),result={...reportSummary(row,owner),content:nonempty(row.content),source:previewSource(row.source)};
  if(id!==undefined&&result.id!==id||result.source.task!==result.task||result.source.token!==''||bytes(result.content)>65536)throw new Error('AI 报告内容未确认');return result;
}
export function reportList(value:unknown,owner:string):ReportSummary[] {
  const items=list(record(value).reports,row=>reportSummary(row,owner));if(items.length>100||new Set(items.map(r=>r.id)).size!==items.length)throw new Error('AI 报告目录未确认');return items;
}
export function completedReport(value:unknown,owner:string,preview:Preview,provider:Provider,model:string):{report:Report;saved:boolean;warning:string;usage:AIUsage} {
  const row=record(value),report=reportDetail(row.report,owner),saved=boolean(row.saved),warning=row.warning===undefined?'':string(row.warning);
  if(report.task!==preview.task||report.title!==preview.title||report.model!==model||report.provider!==provider.name||JSON.stringify(report.source)!==JSON.stringify({...preview,token:''}))throw new Error('AI 完成回执与本次生成不一致，已保留收到的文字。');
  return {report,saved,warning,usage:usage(row.usage)};
}
export function articleList(value:unknown){const row=record(value),items=list(row.items,value=>{const p=record(value);return {id:number(p.id,1),title:string(p.title)};}),total=number(row.total);if(items.length>20||items.length>total||new Set(items.map(p=>p.id)).size!==items.length)throw new Error('AI 文章列表未确认');return {items,total};}
export function deletedReport(value:unknown){if(record(value).deleted!==true)throw new Error('AI 报告删除结果未确认，请刷新列表核对。');}
export function systemChecks(context:string):{service:string;status:string}[]{
  try{const data=record(JSON.parse(context)),system=record(data.system);return list(system.checks,item=>{const row=record(item);return {service:string(row.service),status:string(row.status)};});}catch{return [];}
}
