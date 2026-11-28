// Public projections of internal/cdn. Decode every network/import boundary;
// keep provider credentials out of snapshots and strip unknown properties.
import {object, integer, text, date} from './plugin-data-common';
export const cdnInvalid = () => new Error('CDN 响应不完整或与提交不一致，请刷新记录核对结果。');
type Decoder<T> = (value:unknown) => T;
type Shape = Record<string,Decoder<unknown>>;
type Fields<S extends Shape> = {[K in keyof S as undefined extends ReturnType<S[K]> ? never : K]:ReturnType<S[K]>} & {[K in keyof S as undefined extends ReturnType<S[K]> ? K : never]?:ReturnType<S[K]>};
const str = (max=16384):Decoder<string> => value => {if(!text(value,max))throw cdnInvalid();return value;};
const num = (min=0,max=Number.MAX_SAFE_INTEGER):Decoder<number> => value => {if(!integer(value,min,max))throw cdnInvalid();return value;};
const real:Decoder<number> = value => {if(typeof value!=='number'||!Number.isFinite(value)||value<0)throw cdnInvalid();return value;};
const bool:Decoder<boolean> = value => {if(typeof value!=='boolean')throw cdnInvalid();return value;};
const time:Decoder<string> = value => {if(!date(value))throw cdnInvalid();return value;};
const empty:Decoder<string> = value => {if(value!=='')throw cdnInvalid();return value;};
const optional = <T>(decode:Decoder<T>):Decoder<T|undefined> => value => value===undefined?undefined:decode(value);
const choice = <const T extends readonly string[]>(...options:T):Decoder<T[number]> => value => {if(typeof value!=='string'||!options.includes(value))throw cdnInvalid();return value as T[number];};
const list = <T>(decode:Decoder<T>,max=1000):Decoder<T[]> => value => {if(value===null)return [];if(!Array.isArray(value)||value.length>max)throw cdnInvalid();return value.map(decode);};
const map = <T>(decode:Decoder<T>,max=1000):Decoder<Record<string,T>> => value => {if(value===null)return {};if(!object(value)||Object.keys(value).length>max)throw cdnInvalid();return Object.fromEntries(Object.entries(value).map(([key,item])=>[key,decode(item)]));};
const shape = <S extends Shape>(fields:S):Decoder<Fields<S>> => value => {
 if(!object(value))throw cdnInvalid();
 const result:Record<string,unknown>={};
 for(const key of Object.keys(fields)){const item=fields[key]!(value[key]);if(item!==undefined)result[key]=item;}
 return result as Fields<S>;
};
const unique = <T extends {id:string}>(decode:Decoder<T>,max:number):Decoder<T[]> => value => {const result=list(decode,max)(value);if(new Set(result.map(item=>item.id)).size!==result.length)throw cdnInvalid();return result;};
const id:Decoder<string> = value => {if(!text(value)||!/^[a-z0-9][a-z0-9-]{0,39}$/.test(value))throw cdnInvalid();return value;};
const jobID:Decoder<string> = value => {if(!text(value)||!/^[a-f0-9]{32}$/.test(value))throw cdnInvalid();return value;};
const provider=choice('cloudflare','tencent','edgeone','aliyun','qiniu','cloudfront');
const operation=choice('purge_url','purge_directory','purge_prefix','purge_host','purge_tag','purge_all','warm','test','diagnose','benchmark');
const jobStatus=choice('queued','running','retrying','accepted','succeeded','failed','interrupted','cancelled');
const profileFields={id,name:str(160),provider,resource_id:str(80),domains:list(str(253),20),access_key:str(256),auto_purge:bool};
const profile=shape({...profileFields,secret:optional(empty),token:optional(empty)});
const profileView=shape({...profileFields,secret:optional(empty),token:optional(empty),secret_saved:bool,token_saved:bool});
const settingFields={version:num(1),paused:bool,workers:num(1,4),max_attempts:num(1,6),interval_seconds:num(2,300),auto_content:bool,auto_comments:bool,auto_storage:bool,asset_base:str(2048),static_ttl:num(0,86400),core_assets:bool};
const settingsView=shape({...settingFields,profiles:unique(profile,8),profile_views:unique(profileView,8)});
export type CDNProfile = ReturnType<typeof profile>;
export type CDNSettings = ReturnType<typeof settingsView>;
export type CDNDraft = Omit<CDNSettings,'profile_views'>;
export function cdnSettings(value:unknown):CDNSettings {
 const result=settingsView(value);
 if(result.profiles.length!==result.profile_views.length||result.profiles.some(p=>!result.profile_views.some(v=>v.id===p.id&&equal(p,withoutViewFlags(v)))))throw cdnInvalid();
 return result;
}
function withoutViewFlags(value:CDNSettings['profile_views'][number]):CDNProfile {const {secret_saved,token_saved,...p}=value;return p;}
export function cdnDraft(value:CDNSettings):CDNDraft {const {profile_views,...draft}=value;return structuredClone(draft);}
export const cdnImport = shape({...settingFields,profiles:unique(shape({...profileFields,secret:optional(str(8192)),token:optional(str(8192))}),8)});
export const benchmarkRequest=shape({url:str(2048),candidates:list(str(128),64),sample_count:num(1,64),ipv6:bool,rounds:num(1,5),concurrency:num(1,4),timeout_seconds:num(2,15),download_bytes:num(0,8<<20),minimum_success:num(50,100)});
export type BenchmarkRequest=ReturnType<typeof benchmarkRequest>;
const nodeFields=shape({ip:str(128),family:str(16),colo:str(128),success:num(0,5),rounds:num(0,5),success_rate:real,tcp_ms:real,tls_ms:real,ttfb_p50:real,ttfb_p95:real,mbps:real,bytes:num(),status_code:num(0,599),eligible:bool,error:optional(str())});
const nodeResult=(value:unknown)=>{const result=nodeFields(value);if(result.success>result.rounds||result.success_rate>100)throw cdnInvalid();return result;};
const benchmark=shape({started:time,finished:time,url:str(2048),scope:str(),ip_list_etag:str(1024),ip_list_fetched:time,bytes:num(),results:list(nodeResult,64)});
const diagnostic=shape({url:str(2048),status_code:num(0,599),duration_ms:num(),tls_expires:time,headers:map(str()),findings:list(str())});
export const cdnJob=shape({id:jobID,profile_id:id,identity:empty,operation,targets:list(str(2048),100),status:jobStatus,source:str(128),actor:str(256),attempts:num(),created:time,updated:time,next:time,run_token:optional(empty),error:optional(str()),receipt:optional(shape({id:optional(str()),message:str(),details:optional(map(str()))})),benchmark_request:optional(benchmarkRequest),benchmark:optional(benchmark),diagnostic:optional(diagnostic)});
export type CDNJob=ReturnType<typeof cdnJob>;
export const cdnStatus=shape({settings:cdnSettings,enabled:bool,jobs:unique(cdnJob,200),counts:map(num(),16),capabilities:unique(shape({id:provider,name:str(160),operations:list(operation,10),note:str()}),6),last_error:optional(str())});
export type CDNStatus=ReturnType<typeof cdnStatus>;

const dnsFields={id,name:str(160),provider:choice('aliyun','dnspod'),zone:str(253),access_key:str(256),secret:optional(empty),token:optional(empty),secret_saved:optional(bool),token_saved:optional(bool)};
const dns=shape(dnsFields);
const route=shape({id,name:str(160),cf_profile_id:id,dns_profile_id:id,hostname:str(253),fallback_origin:str(253),target:str(253),preferred_target:str(253),preferred_lines:list(str(128),8),ttl:num(60,86400),manage_existing:bool,health_path:str(1024),expected_body:str(256),monitor:bool,interval:num(60,86400),failure_threshold:num(2,10),recovery_threshold:num(2,10),cooldown:num(300,86400),auto_failover:bool,auto_recover:bool});
export const routingConfig=shape({version:num(1),dns:unique(dns,8),routes:unique(route,20)});
export type RoutingConfig=ReturnType<typeof routingConfig>;
export type DNSProfile=RoutingConfig['dns'][number];
export type CDNRoute=RoutingConfig['routes'][number];
const dnsRecord=shape({id:optional(str(256)),name:str(1024),type:str(32),value:str(16384),line:str(128),ttl:num(),status:str(128),weight:optional(num()),locked:optional(bool)});
export type DNSRecord=ReturnType<typeof dnsRecord>;
const validation=shape({txt_name:optional(str()),txt_value:optional(str()),cname:optional(str()),cname_target:optional(str()),http_url:optional(str()),http_body:optional(str())});
const health=shape({healthy:bool,checked:time,samples:num(),success:num(),latency_ms:num(),colos:list(str(128)),tls_expires:time,error:optional(str())});
const report=shape({route_id:id,checked:time,zone_name:str(253),fallback:shape({origin:str(253),status:str(128)}),hostname:shape({id:str(256),hostname:str(253),status:str(128),custom_origin_server:optional(str(253)),ownership_verification:shape({name:str(),type:str(32),value:str()}),ssl:shape({status:str(128),method:str(128),expires_on:optional(str(128)),validation_records:list(validation),dcv_delegation_records:list(validation)})}),dns:shape({name:str(253),lines:list(shape({id:str(128),name:str(256)})),nameservers:list(str(253)),min_ttl:num(),default_line:str(128)}),actual_ns:list(str(253)),delegated:bool,records:list(dnsRecord),validation:list(dnsRecord),preferred:health,normal:health,warnings:list(str()),mode:choice('unmanaged','mixed','preferred','fallback')});
const step=choice('pending','attempting','done','unknown');
export const routingPlan=shape({id:jobID,route_id:id,kind:choice('setup','validation','route','fallback','restore','rollback'),identity:optional(empty),created:time,expires:time,status:choice('ready','applying','applied','partial','unknown','rolled_back'),actor:str(256),changes:list(shape({kind:choice('create','update','delete','adopt'),before:optional(dnsRecord),after:optional(dnsRecord),state:step}),32),snapshots:map(list(dnsRecord),20),fallback_before:optional(str(253)),fallback_after:optional(str(253)),fallback_state:optional(step),create_hostname:bool,hostname_state:optional(step),hostname_id:optional(str(256)),rollback_of:optional(jobID),error:optional(str())});
export type RoutingPlan=ReturnType<typeof routingPlan>;
export const routingTask=shape({id:jobID,route_id:id,action:choice('inspect','preview_setup','preview_validation','preview_route','preview_fallback','preview_restore','preview_rollback','apply','monitor'),plan_id:optional(jobID),identity:optional(empty),actor:str(256),status:choice('queued','running','succeeded','failed','interrupted'),created:time,updated:time,error:optional(str())});
export type RoutingTask=ReturnType<typeof routingTask>;
const routingView=shape({config:routingConfig,reports:map(report,20),plans:unique(routingPlan,60),tasks:unique(routingTask,100)});
export function routingState(value:unknown):ReturnType<typeof routingView> {
 const state=routingView(value);
 if(Object.entries(state.reports).some(([key,r])=>key!==r.route_id)||state.config.routes.some(r=>!state.config.dns.some(p=>p.id===r.dns_profile_id)))throw cdnInvalid();
 return state;
}
export type RoutingState=ReturnType<typeof routingState>;

export function equal(a:unknown,b:unknown):boolean {
 if(a===b)return true;
 if(Array.isArray(a)&&Array.isArray(b))return a.length===b.length&&a.every((v,i)=>equal(v,b[i]));
 if(object(a)&&object(b)){const keys=Object.keys(a).filter(k=>a[k]!==undefined),other=Object.keys(b).filter(k=>b[k]!==undefined);return keys.length===other.length&&keys.every(k=>Object.hasOwn(b,k)&&equal(a[k],b[k]));}
 return false;
}
export function cdnOK(value:unknown):void {if(!object(value)||value.ok!==true)throw cdnInvalid();}
export function enqueueReceipt(value:unknown,request:{profile_id:string;operation:string;targets:string[];benchmark?:BenchmarkRequest}):CDNJob {
 cdnOK(value);const job=cdnJob((value as Record<string,unknown>).job);
 const canonical=(v:string)=>request.operation==='purge_host'?v.trim().toLowerCase():request.operation==='purge_tag'?v.trim():new URL(v.trim()).href;
 const targets=[...new Set(request.targets.map(canonical))];
 if(job.profile_id!==request.profile_id||job.operation!==request.operation||!equal(job.targets.map(canonical),targets)||!equal(job.benchmark_request,request.benchmark))throw cdnInvalid();
 return job;
}
export function routingReceipt(value:unknown,request:{route_id:string;action:string;plan_id:string}):RoutingTask {
 if(!object(value))throw cdnInvalid();const task=routingTask(value.task);
 if(task.route_id!==request.route_id||task.action!==request.action||(task.plan_id||'')!==request.plan_id)throw cdnInvalid();
 return task;
}

const host=(v:string)=>v.trim().toLowerCase().replace(/\.$/,'');
export function normalizedCDN(draft:CDNDraft):CDNDraft {
 const result=structuredClone(draft);result.version++;
 result.asset_base=result.asset_base.trim().replace(/\/+$/,'');if(result.asset_base)result.asset_base='https://'+new URL(result.asset_base).host.toLowerCase();
 result.profiles=result.profiles.map(p=>{const {secret,token,...rest}=p;return {...rest,name:p.name.trim(),access_key:p.access_key.trim(),resource_id:p.resource_id.trim(),domains:[...new Set(p.domains.map(d=>d.trim().toLowerCase()))].sort()};});return result;
}
export function normalizedRouting(draft:RoutingConfig):RoutingConfig {
 const result=structuredClone(draft);result.version++;
 result.dns=result.dns.map(p=>{const {secret,token,secret_saved,token_saved,...rest}=p;return {...rest,name:p.name.trim(),zone:host(p.zone),access_key:p.access_key.trim()};});
 result.routes=result.routes.map(r=>({...r,name:r.name.trim(),hostname:host(r.hostname),fallback_origin:host(r.fallback_origin),target:host(r.target),preferred_target:host(r.preferred_target),preferred_lines:[...new Set(r.preferred_lines)].sort()}));return result;
}
function plainCredentials<T extends {secret?:string;token?:string;secret_saved?:boolean;token_saved?:boolean}>(p:T) {const {secret,token,secret_saved,token_saved,...rest}=p;return rest;}
export function cdnSaved(value:unknown,sent:CDNDraft,cleared:ReadonlySet<string>=new Set()):CDNSettings {
 cdnOK(value);const result=cdnSettings((value as Record<string,unknown>).settings),actual=cdnDraft(result);actual.profiles=actual.profiles.map(plainCredentials);
 if(!equal(actual,normalizedCDN(sent))||result.profile_views.some(p=>!p.secret_saved||cleared.has(p.id)&&p.token_saved||!cleared.has(p.id)&&sent.profiles.some(s=>s.id===p.id&&!!s.token)&&!p.token_saved))throw cdnInvalid();return result;
}
export function routingSaved(value:unknown,sent:RoutingConfig,cleared:ReadonlySet<string>=new Set()):RoutingState {
 const result=routingState(value),actual=structuredClone(result.config);actual.dns=actual.dns.map(plainCredentials);
 if(!equal(actual,normalizedRouting(sent))||result.config.dns.some(p=>!p.secret_saved||cleared.has(p.id)&&p.token_saved||!cleared.has(p.id)&&sent.dns.some(s=>s.id===p.id&&!!s.token)&&!p.token_saved))throw cdnInvalid();return result;
}
// A successful save only replaces fields that still equal the submitted value.
// Collections are matched by stable ID so deletions/new entries stay local.
export function mergeSaved<T extends object>(sent:T,current:T,saved:T):T {
 const result={...current};
 for(const key of Object.keys(saved) as (keyof T)[])if(equal(current[key],sent[key]))result[key]=structuredClone(saved[key]);
 return result;
}
export function mergeProfiles<T extends {id:string;secret?:string;token?:string}>(sent:T[],current:T[],saved:T[],identity:(p:T)=>unknown):T[] {
 return current.map(p=>{const before=sent.find(s=>s.id===p.id),after=saved.find(s=>s.id===p.id);if(!before||!after)return p;const merged=mergeSaved(before,p,after);
  for(const key of ['secret','token'] as const)if(equal(identity(before),identity(p))&&(p[key]||'')===(before[key]||''))delete merged[key];
  return merged;
 });
}
export const cdnIdentity=(p:CDNProfile)=>[p.provider,p.resource_id.trim(),p.access_key.trim()];
export const dnsIdentity=(p:DNSProfile)=>[p.provider,host(p.zone),p.access_key];
export function mergeCDN(sent:CDNDraft,current:CDNDraft,saved:CDNSettings):CDNDraft {return {...mergeSaved(sent,current,cdnDraft(saved)),version:saved.version,profiles:mergeProfiles(sent.profiles,current.profiles,saved.profiles,cdnIdentity)};}
export function mergeRouting(sent:RoutingConfig,current:RoutingConfig,saved:RoutingConfig):RoutingConfig {return {...mergeSaved(sent,current,saved),version:saved.version,dns:mergeProfiles(sent.dns,current.dns,saved.dns,dnsIdentity),routes:mergeProfiles(sent.routes,current.routes,saved.routes,p=>p.id)};}
