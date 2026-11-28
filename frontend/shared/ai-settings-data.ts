import { record, string, boolean, number, list } from './ledger-data';
import { finite, choice, modelPrice, telemetry } from './ai-usage-data';
import type { ModelPrice, TelemetryConfig } from './ai-usage-data';

export const protocolIDs = ['', 'openai_responses', 'anthropic', 'gemini', 'azure_openai', 'azure_responses', 'azure_openai_legacy', 'openai_images', 'gemini_images', 'gemini_interactions', 'seedream'] as const;
export type Protocol = typeof protocolIDs[number];
export const imageProtocols: readonly string[] = ['openai_images', 'gemini_images', 'gemini_interactions', 'seedream'];
export const isImageProtocol = (protocol: string) => imageProtocols.includes(protocol);
export const parseModels = (text: unknown) => [...new Set(String(text || '').split(/[\n,，]+/).map(value=>value.trim()).filter(Boolean))];
export const telemetryDefaults: TelemetryConfig={enabled:true,details:true,costs:true,balances:false,charts:true,retention_days:30,max_records:5000};
export interface BalanceConfig {
  enabled:boolean;format:string;path:string;auth:string;token:string;has_token:boolean;clear_token:boolean;
  user_id:string;remaining_path:string;used_path:string;total_path:string;currency:string;divisor:number;cache_seconds:number;
}
export interface AIProvider {
  id:string;protocol:Protocol;api_version:string;name:string;base_url:string;allow_insecure:boolean;api_key:string;has_key:boolean;
  models:string[];default_model:string;pricing:ModelPrice[];balance:BalanceConfig;
}
export interface AISettings { revision:string;default_provider:string;default_image_provider:string;providers:AIProvider[];telemetry:TelemetryConfig }
export const defaultBalance:BalanceConfig={enabled:false,format:'custom',path:'',auth:'bearer',token:'',has_token:false,clear_token:false,user_id:'',remaining_path:'',used_path:'',total_path:'',currency:'USD',divisor:1,cache_seconds:300};
const optionalString=(value:unknown)=>value===undefined?'':string(value);
const optionalFlag=(value:unknown)=>value===undefined?false:boolean(value);
const nonempty=(value:unknown)=>{const text=string(value);if(!text)throw new Error('AI 设置标识缺失');return text;};
export function normalizeEndpoint(raw:string,protocol:string):string {
  raw=raw.trim();const url=new URL(raw);
  if(!['https:','http:'].includes(url.protocol)||url.username||url.password||url.search||url.hash||!url.hostname)throw new Error('AI 接口地址无效');
  // Go retains the supplied host spelling and explicit default port.
  const parts=/^https?:\/\/([^/?#]+)([^?#]*)$/.exec(raw);if(!parts)throw new Error('AI 接口地址无效');
  let path=parts[2]!.replace(/\/+$/,'');
  for(const suffix of ['/chat/completions','/responses','/messages','/images/generations','/interactions','/models'])if(path.endsWith(suffix))path=path.slice(0,-suffix.length);
  if(!path)path=['gemini','gemini_images','gemini_interactions'].includes(protocol)?'/v1beta':protocol==='seedream'?'/api/v3':['azure_openai','azure_responses'].includes(protocol)?'/openai/v1':protocol==='azure_openai_legacy'?'/openai':'/v1';
  if(['azure_openai','azure_responses'].includes(protocol)&&path==='/openai')path+='/v1';
  return url.protocol+'//'+parts[1]+path.replace(/\/+$/,'');
}
export function balanceConfig(value:unknown):BalanceConfig {
  const row=record(value);
  if(row.token!==undefined&&row.token!=='')throw new Error('AI 余额令牌不应返回浏览器');
  const result:BalanceConfig={enabled:boolean(row.enabled),format:choice(row.format,['custom','deepseek','siliconflow','openrouter','sub2api','newapi']),path:optionalString(row.path),auth:choice(row.auth??'bearer',['bearer','x-api-key','api-key','x-goog-api-key','none']),token:'',has_token:optionalFlag(row.has_token),clear_token:false,user_id:optionalString(row.user_id),remaining_path:optionalString(row.remaining_path),used_path:optionalString(row.used_path),total_path:optionalString(row.total_path),currency:choice(row.currency??'USD',['USD','CNY','EUR','GBP','JPY']),divisor:finite(row.divisor??1,.000001,1e12),cache_seconds:number(row.cache_seconds,60,86400)};
  return result;
}
export function aiSettings(value:unknown):AISettings {
  const row=record(value);
  const result:AISettings={revision:nonempty(row.revision),default_provider:string(row.default_provider),default_image_provider:optionalString(row.default_image_provider),telemetry:row.telemetry===undefined?{...telemetryDefaults}:telemetry(row.telemetry),providers:list(row.providers,value=>{
    const p=record(value);
    if(p.api_key!==undefined&&p.api_key!=='')throw new Error('AI Key 不应返回浏览器');
    const models=list(p.models,string),prices=p.pricing===undefined||p.pricing===null?[]:list(p.pricing,modelPrice),protocol=choice(p.protocol??'',protocolIDs);
    const provider:AIProvider={id:nonempty(p.id),name:nonempty(p.name),protocol,api_version:optionalString(p.api_version),base_url:nonempty(p.base_url),allow_insecure:optionalFlag(p.allow_insecure),api_key:'',has_key:optionalFlag(p.has_key),models,default_model:nonempty(p.default_model),pricing:prices,balance:balanceConfig(p.balance)};
    if(!/^[a-zA-Z0-9_-]{1,64}$/.test(provider.id)||models.length<1||models.length>100||models.some(m=>!m||/\s/.test(m))||new Set(models).size!==models.length||!models.includes(provider.default_model)||prices.length>100||prices.some(p=>!models.includes(p.model))||new Set(prices.map(p=>p.model)).size!==prices.length)throw new Error('AI 模型设置未确认');
    const endpoint=new URL(provider.base_url);if(!['https:','http:'].includes(endpoint.protocol)||endpoint.username||endpoint.password||endpoint.search||endpoint.hash||!endpoint.hostname)throw new Error('AI 接口地址未确认');
    return provider;
  })};
  if(result.providers.length>20||new Set(result.providers.map(p=>p.id)).size!==result.providers.length)throw new Error('AI 接口目录未确认');
  for(const image of [false,true]){const items=result.providers.filter(p=>isImageProtocol(p.protocol)===image),selected=image?result.default_image_provider:result.default_provider;if(items.length?!items.some(p=>p.id===selected):selected!=='')throw new Error('AI 默认接口未确认');}
  return result;
}
function publicProvider(sent:AIProvider,prior?:AIProvider):AIProvider {
  const base_url=normalizeEndpoint(sent.base_url,sent.protocol),same=prior?.base_url===base_url&&prior.protocol===sent.protocol;
  if(!sent.api_key.trim()&&prior?.has_key&&!same)throw new Error('接口地址或协议已修改，原 Key 不能继续使用。');
  if(!sent.balance.clear_token&&!sent.balance.token.trim()&&prior?.balance.has_token&&prior.base_url!==base_url)throw new Error('接口地址已修改，原余额令牌不能继续使用。');
  const balance={...sent.balance,token:'',clear_token:false,has_token:!sent.balance.clear_token&&!!(sent.balance.token.trim()||(prior?.base_url===base_url&&prior.balance.has_token)),format:sent.balance.format||'custom',auth:sent.balance.auth||'bearer',currency:sent.balance.currency||'USD',cache_seconds:sent.balance.cache_seconds||300,divisor:sent.balance.divisor||(sent.balance.format==='newapi'?500000:1)};
  return {...sent,name:sent.name.trim(),base_url,api_version:sent.protocol==='azure_openai_legacy'?sent.api_version.trim()||'2024-10-21':'',models:parseModels(sent.models.join('\n')),default_model:sent.default_model.trim()||sent.models[0]||'',api_key:'',has_key:!!(sent.api_key.trim()||same&&prior?.has_key),balance,pricing:sent.pricing.map(p=>({...p,currency:p.currency||'USD',mode:p.mode||'tokens',multiplier:p.multiplier||1}))};
}
export function savedAISettings(value:unknown,sent:AISettings,baseline:AISettings):AISettings {
  const result=aiSettings(value),expected={...sent,revision:result.revision,providers:sent.providers.map(p=>publicProvider(p,baseline.providers.find(old=>old.id===p.id)))};
  if(!expected.providers.some(p=>!isImageProtocol(p.protocol)))expected.default_provider='';
  const images=expected.providers.filter(p=>isImageProtocol(p.protocol));if(!images.length)expected.default_image_provider='';else if(!expected.default_image_provider)expected.default_image_provider=images[0]!.id;
  const canonical=(settings:AISettings)=>JSON.stringify(aiSettings({...settings,providers:settings.providers.map(p=>({...p,pricing:p.pricing.map(price=>Object.fromEntries(Object.entries(price).filter(([,v])=>v!==undefined)))}))}));
  if(result.revision===sent.revision||canonical(result)!==canonical(expected))throw new Error('AI 保存回执与本次设置不一致，请重新读取核对。');
  return result;
}
const same=(a:unknown,b:unknown)=>JSON.stringify(a)===JSON.stringify(b);
export function mergeAISettings(current:AISettings,sent:AISettings,saved:AISettings):AISettings {
  const result:AISettings={...current,revision:saved.revision,default_provider:current.default_provider===sent.default_provider?saved.default_provider:current.default_provider,default_image_provider:current.default_image_provider===sent.default_image_provider?saved.default_image_provider:current.default_image_provider,telemetry:{...current.telemetry},providers:[]};
  for(const key of Object.keys(current.telemetry) as (keyof TelemetryConfig)[])if(current.telemetry[key]===sent.telemetry[key])Object.assign(result.telemetry,{[key]:saved.telemetry[key]});
  result.providers=current.providers.map(p=>{const prior=sent.providers.find(q=>q.id===p.id),next=saved.providers.find(q=>q.id===p.id);if(!prior||!next)return p;const merged={...p,balance:{...p.balance},has_key:next.has_key};
    for(const key of ['name','protocol','api_version','base_url','allow_insecure','models','default_model','pricing','api_key'] as const)if(same(p[key],prior[key]))Object.assign(merged,{[key]:next[key]});
    for(const key of Object.keys(p.balance) as (keyof BalanceConfig)[])if(same(p.balance[key],prior.balance[key]))Object.assign(merged.balance,{[key]:next.balance[key]});
    merged.balance.has_token=next.balance.has_token;return merged;
  });
  return result;
}
export function discoveredModels(value:unknown):{models:{id:string}[]} {
  const row=record(value),models=list(row.models,value=>{const id=nonempty(record(value).id);if(id.length>200||/\s/.test(id))throw new Error('AI 模型 ID 无效');return {id};});
  if(models.length>5000||new Set(models.map(m=>m.id)).size!==models.length)throw new Error('AI 模型列表重复或过大');return {models};
}
