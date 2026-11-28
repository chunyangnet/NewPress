import {object,integer,text,digest,date,own} from './plugin-data-common';
export const taskLabels={pending:'等待',running:'执行中',retry:'等待重试',failed:'失败',succeeded:'成功',cancelled:'已取消'};
export interface ReissueTrace {source_id:string;source_version:number;source_status:'failed'|'cancelled';reviewed_at:string}
export interface PluginTask {id:string;plugin:string;kind:string;status:keyof typeof taskLabels;version:number;attempts:number;archived_at?:string;reissue?:ReissueTrace}
export interface TaskPage {items:PluginTask[];total:number}
export interface TaskReview {task:PluginTask;current_user_id:number;reissuable:boolean;provenance?:ReissueTrace & {reviewer_id:number;reason:string}}
export interface PendingReissue {sourceID:string;sourceStatus:keyof typeof taskLabels;uncertain:boolean;body:{expected_user_id:number;source_version:number;kind:string;key:string;payload:unknown;reason:string;confirmed:true}}
export function validReissue(trace:unknown):trace is ReissueTrace {return object(trace)&&digest(trace.source_id)&&integer(trace.source_version,1)&&(trace.source_status==='failed'||trace.source_status==='cancelled')&&date(trace.reviewed_at);}
export function validTask(task:unknown,plugin:string):task is PluginTask {return object(task)&&digest(task.id)&&task.plugin===plugin&&text(task.kind)&&/^[a-z][a-z0-9_.:-]{0,95}$/.test(task.kind)&&own(taskLabels,task.status)&&integer(task.version,1)&&integer(task.attempts,0,5)&&(task.archived_at===undefined||date(task.archived_at))&&(task.reissue===undefined||validReissue(task.reissue));}
export function validTaskPage(page:unknown,plugin:string,status:string,offset:number):page is TaskPage {
 if(!object(page)||!Array.isArray(page.items)||!integer(page.total)||page.items.length!==Math.min(20,Math.max(0,page.total-offset)))return false;
 const ids=new Set<string>();return page.items.every(task=>{if(!validTask(task,plugin)||ids.has(task.id)||status&&task.status!==status)return false;ids.add(task.id);return true;});
}
export function validTaskReview(details:unknown,plugin:string,id:string,owner:number):details is TaskReview {
 if(!object(details)||!validTask(details.task,plugin)||details.task.id!==id||details.current_user_id!==owner||typeof details.reissuable!=='boolean'||Boolean(details.task.reissue)!==Boolean(details.provenance))return false;
 if(details.reissuable&&!['failed','cancelled'].includes(details.task.status))return false;
 if(details.provenance!==undefined){const p=details.provenance,t=details.task.reissue;if(!object(p)||!validReissue(p)||!t||p.source_id!==t.source_id||p.source_version!==t.source_version||p.source_status!==t.source_status||p.reviewed_at!==t.reviewed_at||!integer(p.reviewer_id,1)||!text(p.reason,512))return false;}
 return true;
}
export function runtimeResult(data:unknown):{fragment?:{title:string;text:string;tone:string};data?:unknown} {
 if(!object(data))throw new Error('试运行结果不完整，请核对执行状态。');
 if(data.fragment===undefined)return data;
 // Go's validManifestText counts Unicode code points, permits an empty title,
 // and requires non-empty body text without surrounding whitespace or controls.
 const manifestText=(value:unknown,max:number,required:boolean):value is string=>text(value)&&value===value.trim()&&(!required||value!=='')&&[...value].length<=max&&!/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/.test(value);
 const f=data.fragment;if(!object(f)||!manifestText(f.title,120,false)||!manifestText(f.text,2000,true)||!text(f.tone)||!['info','warning','success'].includes(f.tone))throw new Error('试运行返回的预览不完整，请核对执行状态。');
 return {...data,fragment:{title:f.title,text:f.text,tone:f.tone}};
}
export function responseRecord(value:unknown):Record<string,unknown> {if(!object(value))throw new Error('未收到完整结果，请核对后重试。');return value;}
export function apiError(value:unknown):{message:string;code:string} {const e=object(value)&&object(value.error)?value.error:{};return{message:text(e.message)?e.message:'未完成操作，输入已保留。',code:text(e.code)?e.code:''};}
