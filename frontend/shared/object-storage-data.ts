import {array,boolean,choice,equal,hash,invalidTransfer,map,number,ok,optional,shape,string,timestamp,unique} from './transfer-data';

const id=(value:unknown)=>{const result=string(40)(value);if(!/^[a-z0-9][a-z0-9-]{0,39}$/.test(result))throw invalidTransfer();return result;};
const empty=(value:unknown)=>{if(value!==undefined&&value!=='')throw invalidTransfer();return '';};
const profileFields={id,name:string(320),provider:choice('s3','r2','cos','oss','qiniu','s3-compatible','cloudreve','webdav'),endpoint:string(8192),region:string(40),bucket:string(63),prefix:string(200),public_url:string(8192),access_key:optional(string(8192),''),username:optional(string(8192),''),secret_key:empty,session_token:empty,password:empty,path_style:boolean,allow_private:boolean,allow_http:boolean,timeout_seconds:number(10,600)};
const profile=shape(profileFields),view=shape({...profileFields,secret_saved:boolean,token_saved:boolean,password_saved:boolean});
const route=shape({target:string(40),mode:optional(choice('','mirror','redirect'),'')});
const settings=shape({version:number(1),active_profile:string(40),mode:choice('mirror','redirect'),auto_sync:boolean,verify_download:boolean,paused:boolean,workers:number(1,4),scan_seconds:number(30,3600),max_attempts:number(1,10),signed_url_seconds:number(60,3600),profiles:array(profile,8),profile_views:array(view,8),upload_routes:optional(map(route,100),{})});
export type StorageProfile=ReturnType<typeof profile>;
export type StorageSettings=ReturnType<typeof settings>;
export type StorageDraft=Omit<StorageSettings,'profile_views'>;
export function storageSettings(value:unknown):StorageSettings {
 const result=settings(value);unique(result.profiles,p=>p.id);unique(result.profile_views,p=>p.id);
 if(result.active_profile&&!result.profiles.some(p=>p.id===result.active_profile)||result.profiles.length!==result.profile_views.length)throw invalidTransfer();
 for(const p of result.profile_views){const {secret_saved,token_saved,password_saved,...fields}=p;if(!result.profiles.some(other=>equal(other,fields)))throw invalidTransfer();}
 if(Object.values(result.upload_routes).some(r=>r.target&&r.target!=='local'&&!result.profiles.some(p=>p.id===r.target)))throw invalidTransfer();return result;
}
export function storageDraft(value:StorageSettings):StorageDraft {const {profile_views,...draft}=value;return structuredClone(draft);}
const statusName=choice('pending','syncing','synced','failed','orphaned','removed');
const record=shape({id:hash,profile_id:id,identity:hash,item_id:number(1),filename:string(4096),object_key:string(1024),remote_key:string(1024),sha256:hash,size:number(1),content_type:string(1024),status:statusName,operation:choice('upload','verify','delete'),attempts:number(0,100),next:timestamp,updated:timestamp,verified:timestamp,error:optional(string(512),'')});
const status=shape({settings:storageSettings,upload_entries:array(shape({id,name:string(1024)}),100),enabled:boolean,running:boolean,namespace:string(128),counts:map(number(),6),profile_counts:map(map(number(),6),1000),profile_bytes:map(number(),1000),total_bytes:number(),synced_bytes:number(),records:array(record,100),last_error:optional(string(),'')});
export type StorageStatus=ReturnType<typeof status>;
export type StorageRecord=ReturnType<typeof record>;
export function storageStatus(value:unknown):StorageStatus {const result=status(value);unique(result.records,r=>r.id);unique(result.upload_entries,r=>r.id);if(result.synced_bytes>result.total_bytes)throw invalidTransfer();return result;}
const records=shape({records:array(record,100),total:number(),page:number(1),pages:number(1)});
export function storageRecords(value:unknown,request:{profile:string;status:string;q:string;page:number}):ReturnType<typeof records> {
 const result=records(value);unique(result.records,r=>r.id);
 if(result.pages!==Math.max(1,Math.ceil(result.total/100))||result.page!==Math.min(request.page,result.pages)||result.records.length!==Math.min(100,result.total-(result.page-1)*100))throw invalidTransfer();
 const query=request.q.trim().toLowerCase();if(result.records.some(r=>request.profile&&r.profile_id!==request.profile||request.status&&r.status!==request.status||query&&!(r.filename+' '+r.remote_key).toLowerCase().includes(query)))throw invalidTransfer();return result;
}
const probe=shape({ok:boolean,stages:array(string(),100),duration_ms:number(),cleanup_ok:boolean});
export function storageProbe(value:unknown,success=false):ReturnType<typeof probe>{const result=probe(value);if(success&&(!result.ok||!result.cleanup_ok))throw invalidTransfer();return result;}
const migration=shape({source:id,target:id,total:number(),ready:number(),bytes:number(),routes:array(string(),100),default:boolean});
export type StorageMigration=ReturnType<typeof migration>;
export function storageMigration(value:unknown,source:string,target:string):StorageMigration {const result=migration(ok(value).migration);if(result.source!==source||result.target!==target||source===target||result.ready>result.total)throw invalidTransfer();return result;}
const files=shape({files:array(shape({key:string(1024),size:number(),directory:boolean,managed:boolean}),1000),next:string(4096)});
export function storageFiles(value:unknown,directory:string):ReturnType<typeof files> {const result=files(value);unique(result.files,f=>f.key);if(result.files.some(f=>!f.key.startsWith(directory)||!f.key||f.key.startsWith('/')||/[\\\x00\r\n]/.test(f.key)||f.key.replace(/\/$/,'').split('/').some(s=>!s||s==='.'||s==='..')))throw invalidTransfer();return result;}
export function storageAction(value:unknown,action:string):number|ReturnType<typeof probe> {const response=ok(value);return action==='test'?storageProbe(response.probe,true):number()(response.scheduled);}
export function normalizedStorage(sent:StorageDraft):StorageDraft {
 const result=structuredClone(sent);result.version++;
 result.profiles=result.profiles.map(p=>{
  const v={...p,name:p.name.trim(),endpoint:p.endpoint.trim().replace(/\/+$/,''),public_url:p.public_url.trim().replace(/\/+$/,''),region:p.region.trim(),bucket:p.bucket.trim(),prefix:p.prefix.trim().replace(/^\/+|\/+$/g,''),timeout_seconds:p.timeout_seconds||120,secret_key:'',password:'',session_token:''};
  if(v.provider==='r2')v.region='auto';
  if(!v.endpoint){if(v.provider==='s3')v.endpoint=`https://s3.${v.region}.amazonaws.com${v.region.startsWith('cn-')?'.cn':''}`;else if(v.provider==='cos')v.endpoint=`https://cos.${v.region}.myqcloud.com`;else if(v.provider==='oss')v.endpoint=`https://oss-${v.region}.aliyuncs.com`;else if(v.provider==='qiniu')v.endpoint=`https://s3.${v.region}.qiniucs.com`;}
  return v;
 });return result;
}
export function storageSaved(value:unknown,sent:StorageDraft,cleared:ReadonlySet<string>):StorageSettings {
 const result=storageSettings(ok(value).settings);if(!equal(storageDraft(result),normalizedStorage(sent)))throw invalidTransfer();
 for(const p of result.profile_views){const submitted=sent.profiles.find(s=>s.id===p.id)!;const dav=p.provider==='cloudreve'||p.provider==='webdav';if(dav?!p.password_saved:!p.secret_saved)throw invalidTransfer();if(cleared.has(p.id)&&p.token_saved||submitted.session_token&&!cleared.has(p.id)&&!p.token_saved)throw invalidTransfer();}return result;
}
function mergeFields<T extends object>(sent:T,current:T,saved:T):T {const result={...current};for(const key of Object.keys(saved) as (keyof T)[])if(equal(sent[key],current[key]))result[key]=structuredClone(saved[key]);return result;}
export function mergeStorage(sent:StorageDraft,current:StorageDraft,saved:StorageSettings):StorageDraft {
 const accepted=storageDraft(saved),result=mergeFields(sent,current,accepted);result.version=saved.version;
 result.profiles=current.profiles.map(p=>{const before=sent.profiles.find(s=>s.id===p.id),after=accepted.profiles.find(s=>s.id===p.id);if(!before||!after)return p;const merged=mergeFields(before,p,after);
  const identity=(v:StorageProfile)=>[v.provider,v.endpoint.trim().replace(/\/+$/,''),v.region.trim(),v.bucket.trim(),v.prefix.trim(),v.username,v.access_key,v.path_style];
  if(!equal(identity(before),identity(p)))for(const key of ['secret_key','password','session_token'] as const)merged[key]=p[key];return merged;
 });
 const routes={...current.upload_routes};for(const key of new Set([...Object.keys(sent.upload_routes),...Object.keys(accepted.upload_routes)])){if(equal(current.upload_routes[key],sent.upload_routes[key])){if(accepted.upload_routes[key])routes[key]=structuredClone(accepted.upload_routes[key]);else delete routes[key];}}
 result.upload_routes=routes;return result;
}
