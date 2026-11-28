import {object,text,integer,date} from './plugin-data-common';
export interface SystemMetric {id:string;value:string;unit:string;detail:string;note:string;tone:string;percent:number}
export interface SystemCheck {id:string;detail:string;status_label:string;latency:string;status:string}
export interface SystemSnapshot {collected_at:string;collected_label:string;status:string;status_label:string;status_detail:string;uptime:string;started_at:string;version:string;hostname:string;platform:string;timezone:string;healthy:number;total:number;metrics:SystemMetric[];checks:SystemCheck[];groups:{rows:{id:string;value:string}[]}[]}
const fields=(value:unknown,keys:string[])=>object(value)&&keys.every(key=>text(value[key],16384));
const unique=(items:Record<string,unknown>[])=>new Set(items.map(item=>item.id)).size===items.length;
export function validSnapshot(value:unknown):value is SystemSnapshot {
 if(!object(value)||!date(value.collected_at)||!fields(value,['collected_label','status_label','status_detail','uptime','started_at','version','hostname','platform','timezone'])||!['pass','error'].includes(String(value.status))||!integer(value.healthy)||!integer(value.total)||value.healthy>value.total)return false;
 if(!Array.isArray(value.metrics)||value.metrics.length!==4||!value.metrics.every((item:unknown)=>object(item)&&fields(item,['id','value','unit','detail','note','tone'])&&['normal','muted','warning','error'].includes(String(item.tone))&&typeof item.percent==='number'&&Number.isFinite(item.percent)&&item.percent>=0&&item.percent<=100)||!unique(value.metrics))return false;
 if(!Array.isArray(value.checks)||value.checks.length>100||!value.checks.every((item:unknown)=>object(item)&&fields(item,['id','detail','status_label','latency'])&&['pass','error','disabled'].includes(String(item.status)))||!unique(value.checks))return false;
 if(!Array.isArray(value.groups)||value.groups.length>100||!value.groups.every((group:unknown)=>object(group)&&Array.isArray(group.rows)&&group.rows.length<=100&&group.rows.every((row:unknown)=>fields(row,['id','value']))))return false;
 return unique(value.groups.flatMap(group=>group.rows));
}
