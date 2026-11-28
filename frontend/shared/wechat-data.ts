import {record,string,boolean,number,list,date} from './ledger-data';
import {configFields,configChecks,jobStatuses} from './wechat-types';
import type {WeChatConfig,ConfigInput,WeChatJob,WeChatPost,Workspace,CollectedArticle,CollectRow,CollectPlan,CollectPreview,CollectImport,SyncInput,SyncCheck} from './wechat-types';
const optionalText=(value:unknown)=>value===undefined?'':string(value);
const identity=(value:unknown)=>{const text=string(value);if(!/^[A-Za-z0-9._:-]{1,128}$/.test(text))throw new Error('记录标识无效');return text;};
const timestamp=(value:unknown)=>{const text=date(value);if(!text)throw new Error('记录时间缺失');return text;};
function choice<T extends string>(value:unknown,allowed:readonly T[]):T {if(typeof value!=='string'||!allowed.some(item=>item===value))throw new Error('记录状态无效');return value as T;}
function unique<T>(items:T[],key:(item:T)=>string|number):T[]{if(new Set(items.map(key)).size!==items.length)throw new Error('返回记录重复');return items;}
export function wechatConfig(value:unknown):WeChatConfig {
  const row=record(value),result:WeChatConfig={version:number(row.version,1),app_id:string(row.app_id),author:string(row.author),thumb_media_id:string(row.thumb_media_id),enabled:boolean(row.enabled),collect_enabled:boolean(row.collect_enabled),collect_drafts:row.collect_drafts===undefined?false:boolean(row.collect_drafts),auto_sync:boolean(row.auto_sync),auto_publish:boolean(row.auto_publish),include_source:boolean(row.include_source),open_comment:boolean(row.open_comment),fans_comment:boolean(row.fans_comment),has_secret:row.has_secret===undefined?false:boolean(row.has_secret),since:timestamp(row.since)};
  if(row.app_secret!==undefined&&row.app_secret!==''||result.app_id&&!/^wx[0-9a-fA-F]{16}$/.test(result.app_id))throw new Error('公众号配置格式无效');
  return result;
}
export function savedConfig(value:unknown,input:ConfigInput,previous:WeChatConfig,clear:boolean):WeChatConfig {
  const config=wechatConfig(value),hasSecret=!clear&&(!!input.app_secret||input.app_id===previous.app_id&&previous.has_secret);
  if(config.version!==input.version+1||configFields.some(key=>config[key]!==input[key])||configChecks.some(key=>config[key]!==input[key])||config.has_secret!==hasSecret)throw new Error('保存回执不完整，输入仍保留。请先刷新配置核对结果，不要重复开启发表。');
  return config;
}
export function wechatPost(value:unknown):WeChatPost {const row=record(value);return {id:number(row.id,1),version:number(row.version,1),title:string(row.title),status:choice(row.status,['draft','publish','future']),cover:string(row.cover)};}
export function wechatJob(value:unknown):WeChatJob {
  const row=record(value),job:WeChatJob={id:identity(row.id),app_id:string(row.app_id),post_id:number(row.post_id,1),version:number(row.version,1),title:string(row.title),actor:string(row.actor),auto_publish:boolean(row.auto_publish),status:choice(row.status,jobStatuses),stage:choice(row.stage,['prepare','draft','publish','poll']),attempts:number(row.attempts),created_at:timestamp(row.created_at),updated_at:timestamp(row.updated_at),next_at:timestamp(row.next_at),lease_until:timestamp(row.lease_until)};
  if(!/^wx[0-9a-fA-F]{16}$/.test(job.app_id))throw new Error('任务公众号无效');
  for(const key of ['media_id','publish_id','article_id','article_url','error'] as const)if(row[key]!==undefined)job[key]=string(row[key]);
  return job;
}
export function wechatWorkspace(value:unknown):Workspace {
  const row=record(value),result:Workspace={user_id:number(row.user_id,1),config:wechatConfig(row.config),can_sync:boolean(row.can_sync),can_collect:boolean(row.can_collect),posts:unique(list(row.posts,wechatPost),post=>post.id),selected_post:row.selected_post==null?null:wechatPost(row.selected_post),jobs:unique(list(row.jobs,wechatJob),job=>job.id),job_page:number(row.job_page,1),job_pages:number(row.job_pages,1),total_jobs:number(row.total_jobs),filtered_jobs:number(row.filtered_jobs),active_jobs:number(row.active_jobs),attention_jobs:number(row.attention_jobs)};
  if(result.posts.length>50||result.jobs.length>20||result.job_page>result.job_pages||result.filtered_jobs>result.total_jobs)throw new Error('工作区分页格式无效');
  return result;
}
export function syncedJob(value:unknown,post:WeChatPost,config:WeChatConfig):WeChatJob {
  const job=wechatJob(record(value).job);if(job.post_id!==post.id||job.version!==post.version||job.app_id!==config.app_id)throw new Error('同步回执与当前文章或公众号不一致，请先刷新记录核对。');return job;
}
export function syncCheck(value:unknown,input:SyncInput):SyncCheck {
  const row=record(value),result:SyncCheck={post_id:number(row.post_id,1),version:number(row.version,1),config_version:number(row.config_version,1),title:string(row.title),auto_publish:boolean(row.auto_publish),ready:boolean(row.ready),title_chars:number(row.title_chars),content_chars:number(row.content_chars),content_bytes:number(row.content_bytes),images:number(row.images),checked_images:number(row.checked_images),upload_bytes:number(row.upload_bytes),issues:list(row.issues,value=>{const issue=record(value);return {field:string(issue.field),message:string(issue.message)};}),warnings:list(row.warnings,string),checked_at:timestamp(row.checked_at)};
  if(result.post_id!==input.post_id||result.version!==input.version||result.config_version!==input.config_version||result.checked_images>result.images||result.issues.length>50||result.warnings.length>10||result.ready!==(result.issues.length===0&&result.checked_images===result.images))throw new Error('检查结果不完整，上次结果已保留，请重新检查。');return result;
}
function article(value:unknown):CollectedArticle {
  const row=record(value),result:CollectedArticle={key:identity(row.key),url:string(row.url),title:string(row.title),author:string(row.author),excerpt:string(row.excerpt),body:string(row.body),cover:string(row.cover),preview:string(row.preview),images:list(row.images,string),warnings:list(row.warnings,string)};
  const url=new URL(result.url);if(url.origin!=='https://mp.weixin.qq.com'||url.username||url.password)throw new Error('预览来源无效');return result;
}
function planRow(value:unknown):CollectRow {const row=record(value);return {key:identity(row.key),action:choice(row.action,['create','created','skip','changed','conflict']),...(row.title===undefined?{}:{title:string(row.title)}),...(row.post_id===undefined?{}:{post_id:number(row.post_id)}),...(row.message===undefined?{}:{message:string(row.message)})};}
function plan(value:unknown):CollectPlan {const row=record(value);return {items:unique(list(row.items,planRow),item=>item.key),new:number(row.new),existing:number(row.existing),conflicts:number(row.conflicts)};}
export function collectPreview(value:unknown):CollectPreview {
  const row=record(value),articles=unique(list(row.articles,article),a=>a.key),failures=list(row.failures,value=>{const item=record(value);return {url:string(item.url),message:string(item.message)};});
  if(articles.length>10||failures.length>10)throw new Error('采集结果超过单批范围');
  if(!articles.length)return {articles,failures,receipts:{},expires:'',plan:{items:[],new:0,existing:0,conflicts:0}};
  const result={articles,failures,receipts:Object.fromEntries(Object.entries(record(row.receipts)).map(([key,value])=>[key,string(value)])),expires:timestamp(row.expires),plan:plan(row.plan)},rows=new Map(result.plan.items.map(item=>[item.key,item]));
  if(rows.size!==articles.length||result.plan.items.some(item=>item.action==='created')||articles.some(a=>!rows.has(a.key)||rows.get(a.key)?.action==='create'&&!result.receipts[a.key])||Object.keys(result.receipts).some(key=>!rows.has(key)))throw new Error('预览响应不完整，上次内容和选择仍保留，请重新采集。');
  if(result.plan.new!==result.plan.items.filter(item=>item.action==='create').length||result.plan.existing!==result.plan.items.filter(item=>['skip','changed'].includes(item.action)).length||result.plan.conflicts!==result.plan.items.filter(item=>item.action==='conflict').length)throw new Error('预览计数未确认');
  return result;
}
export function collectImport(value:unknown,keys:Set<string>):CollectImport {
  const row=record(value),result=plan(row.plan),items=result.items.map(item=>({...item,post_id:number(item.post_id,1),title:string(item.title)}));
  if(row.applied!==true||items.length!==keys.size||result.conflicts!==0||items.some(item=>!keys.has(item.key)||!['created','skip','changed'].includes(item.action))||result.new!==items.filter(item=>item.action==='created').length||result.existing!==items.length-result.new)throw new Error('导入回执不完整，请重新预览核对；已导入文章会自动跳过。');
  return {applied:true,plan:{...result,items}};
}
export function wechatMessage(value:unknown):string {return string(record(value).message);}
export function jobAcknowledgement(value:unknown):void {if(record(value).ok!==true)throw new Error('任务回执未确认，请先刷新同步记录核对。');}
export function wechatErrorMessage(value:unknown):string {const row=record(value);return row.error&&typeof row.error==='object'?optionalText(record(row.error).message):optionalText(row.message);}
