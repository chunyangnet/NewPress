import {object,text,integer,date,digest,own} from './plugin-data-common';
export const phases={idle:'准备就绪',checking:'正在检查版本',available:'发现新版本',current:'已是最新版本',downloading:'正在下载更新包',verifying:'正在校验完整性',staging:'正在准备新版本',queued:'等待切换',stopping:'正在停止旧进程',backup:'正在备份站点',starting:'正在检查服务',observing:'正在观察新版本',rollback:'正在恢复原版本',completed:'更新完成',rolled_back:'已恢复原版本',failed:'更新未完成',cancelled:'任务已取消'};
export const busyPhases=new Set<string>(['checking','downloading','verifying','staging','queued','stopping','backup','starting','observing','rollback']);
export const terminal=new Set<string>(['completed','rolled_back','failed','cancelled']);
export interface UpdatePolicy {auto_check:boolean;check_interval_minutes:number;observation_seconds:number;customized:boolean;revision:string;launcher_applies_window:boolean}
export interface UpdateState {id:string;phase:keyof typeof phases;percent:number;downloaded:number;total:number;target:string;previous?:string;protected_until?:string;started_at:string;updated_at:string;logs:{time:string;level:string;message:string}[]}
export interface UpdateSnapshot {policy:UpdatePolicy;trusted_publisher:boolean;revision?:string;discovery:{checked:string;message:string;interval?:string};current:string;platform:string;environment:string;managed:boolean;configured:boolean;token_set:boolean;environment_token:boolean;source:{official?:boolean;owner:string;repo:string;ref:string;manifest:string};available?:{version:string;notes:string};state:UpdateState}
const bad=()=>new Error('更新响应不完整，请重新连接核对；不会自动重试安装。');
export function updatePolicy(value:unknown):UpdatePolicy {
 if(!object(value)||typeof value.auto_check!=='boolean'||typeof value.customized!=='boolean'||typeof value.launcher_applies_window!=='boolean'||!digest(value.revision)||typeof value.check_interval_minutes!=='number'||!Number.isFinite(value.check_interval_minutes)||value.check_interval_minutes<1||value.check_interval_minutes>10080||typeof value.observation_seconds!=='number'||!Number.isFinite(value.observation_seconds)||value.observation_seconds<2||value.observation_seconds>600)throw bad();
 return {auto_check:value.auto_check,customized:value.customized,launcher_applies_window:value.launcher_applies_window,revision:value.revision,check_interval_minutes:value.check_interval_minutes,observation_seconds:value.observation_seconds};
}
export function updateState(value:unknown):UpdateState {
 if(!object(value)||!text(value.id,128)||!own(phases,value.phase)||!integer(value.percent,0,100)||!integer(value.downloaded)||!integer(value.total)||value.total>0&&value.downloaded>value.total||!text(value.target,64)||value.previous!==undefined&&!text(value.previous,64)||value.protected_until!==undefined&&!date(value.protected_until)||!date(value.started_at)||!date(value.updated_at))throw bad();
 const logs=value.logs===null?[]:value.logs;
 if(!Array.isArray(logs)||logs.length>200||!logs.every((log:unknown)=>object(log)&&date(log.time)&&text(log.level,32)&&['info','warning','error'].includes(log.level)&&text(log.message,16384)))throw bad();
 return {...value,id:value.id,phase:value.phase,percent:value.percent,downloaded:value.downloaded,total:value.total,target:value.target,started_at:value.started_at,updated_at:value.updated_at,logs:logs as UpdateState['logs']};
}
export function updateSnapshot(value:unknown):UpdateSnapshot {
 if(!object(value)||!text(value.current,64)||!text(value.platform,128)||!text(value.environment,128)||!['trusted_publisher','managed','configured','token_set','environment_token'].every(key=>typeof value[key]==='boolean')||value.revision!==undefined&&!digest(value.revision))throw bad();
 const source=value.source,discovery=value.discovery;
 if(!object(source)||!['owner','repo','ref','manifest'].every(key=>text(source[key],512))||source.official!==undefined&&typeof source.official!=='boolean'||source.token!==undefined||!object(discovery)||!date(discovery.checked)||!text(discovery.message,16384)||discovery.interval!==undefined&&!text(discovery.interval,128))throw bad();
 if(value.available!==undefined&&(!object(value.available)||!text(value.available.version,64)||!value.available.version||!text(value.available.notes,16000)||!digest(value.revision)))throw bad();
 return {...value,policy:updatePolicy(value.policy),state:updateState(value.state)} as UpdateSnapshot;
}
export function updateAcknowledgment(value:unknown,key:'saved'|'accepted'):void {if(!object(value)||value[key]!==true)throw bad();}
export function updateTicket(value:unknown):string {if(!object(value)||!digest(value.ticket))throw bad();return value.ticket;}

export const extensionActive=new Set(['queued','checking','downloading','verifying','validating','preparing','switching','cancelling','other']);
export interface ExtensionJob {id:string;kind:string;action:string;slug:string;target:string;phase:string;message:string;downloaded:number;total:number;started_at:string;updated_at:string;review_required:boolean}
export function extensionJob(value:unknown,kind:string):ExtensionJob {
 if(!object(value)||!text(value.phase)||![...extensionActive,'idle','succeeded','failed','cancelled','interrupted'].includes(value.phase)||!['id','kind','action','slug','target','message'].every(key=>text(value[key],key==='message'?4096:128))||!integer(value.downloaded)||!integer(value.total)||value.downloaded>value.total||!date(value.started_at)||!date(value.updated_at)||typeof value.review_required!=='boolean')throw new Error('任务响应不完整，请重新连接并核对当前版本。');
 if(!['idle','other'].includes(value.phase)&&(value.kind!==kind||!value.id))throw new Error('任务类型与当前页面不一致，请重新核对。');
 return value as unknown as ExtensionJob;
}
