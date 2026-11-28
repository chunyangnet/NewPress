import type {CollectDraft,DraftChoice,DraftStore,CollectedArticle} from './wechat-types';
const limit=65536,lifetime=86400000;
function valid(value:unknown,owner:number,now:number):value is CollectDraft {
  if(!value||typeof value!=='object'||Array.isArray(value))return false;
  const row=value as Record<string,unknown>;
  if(row.version!==1||row.owner!==owner||!Number.isSafeInteger(owner)||owner<=0||typeof row.at!=='number'||!Number.isFinite(row.at)||row.at>now+60000||now-row.at>=lifetime||typeof row.input!=='string'||row.input.length>22000||typeof row.baseline!=='string'||row.baseline.length>22000||typeof row.uncertain!=='boolean'||!Array.isArray(row.choices)||row.choices.length>10)return false;
  const keys=new Set<string>();
  return row.choices.every((value:unknown)=>{if(!value||typeof value!=='object')return false;const item=value as Record<string,unknown>;if(typeof item.key!=='string'||!/^[A-Za-z0-9._:-]{1,128}$/.test(item.key)||keys.has(item.key)||typeof item.digest!=='string'||!/^[a-f0-9]{64}$/.test(item.digest)||typeof item.selected!=='boolean')return false;keys.add(item.key);return true;});
}
const copy=(value:CollectDraft):CollectDraft=>({version:1,owner:value.owner,at:value.at,input:value.input,baseline:value.baseline,choices:value.choices.map(({key,digest,selected})=>({key,digest,selected})),uncertain:value.uncertain});
export function create(owner:number):DraftStore {
  const key=`newpress.wechat.collect.${owner}`;let previous:CollectDraft|null=null;
  const payload=(value:CollectDraft)=>JSON.stringify([value.input,value.baseline,value.choices,value.uncertain]);
  return {
    read(){const raw=window.sessionStorage.getItem(key);if(!raw)return null;let value:unknown;try{value=raw.length<=limit?JSON.parse(raw):null;}catch{value=null;}if(!valid(value,owner,Date.now())){this.clear();return null;}previous=copy(value);return copy(previous);},
    save(input:string,baseline:string,choices:DraftChoice[],uncertain:boolean){const value:CollectDraft={version:1,owner,at:Date.now(),input,baseline,choices:choices.map(({key,digest,selected})=>({key,digest,selected})),uncertain:!!uncertain};if(previous&&payload(previous)===payload(value)){if(Date.now()-previous.at>=lifetime){this.clear();return null;}value.at=previous.at;}if(!valid(value,owner,Date.now()))throw new Error('采集进度超出暂存范围，当前输入仍保留；请减少链接或重新开启暂存。');const raw=JSON.stringify(value);if(raw.length>limit)throw new Error('采集进度超过暂存容量，当前输入与上次副本仍保留。');window.sessionStorage.setItem(key,raw);previous=copy(value);return copy(value);},
    clear(){window.sessionStorage.removeItem(key);previous=null;}
  };
}
export async function fingerprints(articles:CollectedArticle[]):Promise<Map<string,string>> {
  const result=new Map<string,string>();if(!window.crypto?.subtle||typeof TextEncoder!=='function')return result;
  for(const article of articles){const value=JSON.stringify((['url','title','author','excerpt','body','cover','preview','images'] as const).map(name=>article[name]));const digest=await window.crypto.subtle.digest('SHA-256',new TextEncoder().encode(value));result.set(article.key,[...new Uint8Array(digest)].map(byte=>byte.toString(16).padStart(2,'0')).join(''));}return result;
}
