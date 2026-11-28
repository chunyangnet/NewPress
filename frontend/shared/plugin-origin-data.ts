import {object,integer,text} from './plugin-data-common';
export interface OriginInput {version:number;provider:string;key:string;endpoint:string;country_path:string;country_code_path:string;region_path:string;city_path:string;success_path:string;success_value:string;flag_mode:string;location:boolean;device:boolean;browser:boolean;fallback:boolean;retention_days:number;cache_days:number;daily_limit:number;rules:{role:string;location:boolean;device:boolean}[]}
export interface OriginLocation {country:string;region:string;city:string;precision:string;provider:string;country_code?:string}
export interface OriginRecord {id:number;status:string;location:OriginLocation;source_label:string;ip?:string;ua?:string;source_issue_label?:string;error?:string}
export function originSave(data:unknown,sent:OriginInput,clearKey:boolean) {
 if(!object(data)||!object(data.settings)||!integer(data.settings.version,1)||data.settings.version!==sent.version+1||typeof data.key_saved!=='boolean')throw new Error('未收到完整的保存确认，输入已保留，请刷新核对后重试。');
 const s=data.settings;
 for(const key of ["provider","endpoint","country_path","country_code_path","region_path","city_path","success_path","success_value","flag_mode","location","device","browser","fallback","retention_days","cache_days","daily_limit"] as const)if(s[key]!==sent[key])throw new Error('保存回执与提交设置不一致，输入已保留，请刷新核对。');
 if(s.key!==undefined&&s.key!==''||clearKey&&data.key_saved||!clearKey&&!!sent.key&&!data.key_saved||!Array.isArray(s.rules)||s.rules.length!==sent.rules.length)throw new Error('保存回执的密钥或角色状态不一致，请刷新核对。');
 if(!s.rules.every((rule,index)=>{const expected=sent.rules[index];return object(rule)&&expected&&rule.role===expected.role&&rule.location===expected.location&&rule.device===expected.device;}))throw new Error('保存回执的角色设置不一致，请刷新核对。');
 return {version:s.version,keySaved:data.key_saved};
}
export function originLocation(data:unknown):OriginLocation {
 if(!object(data)||!text(data.country)||!text(data.region)||!text(data.city)||!text(data.precision)||!text(data.provider)||data.country_code!==undefined&&!text(data.country_code))throw new Error('归属地响应不完整，请重新查询。');
 return {country:data.country,region:data.region,city:data.city,precision:data.precision,provider:data.provider,...(text(data.country_code)?{country_code:data.country_code}:{})};
}
export function originRecord(data:unknown,id:number):OriginRecord {
 if(!object(data)||data.id!==id||!integer(data.id,1)||!text(data.status)||!['pending','resolved','failed','local','expired','unavailable'].includes(data.status)||!text(data.source_label))throw new Error('评论记录不完整或编号不匹配，请重新查询。');
 for(const key of ['ip','ua','source_issue_label','error'])if(data[key]!==undefined&&!text(data[key]))throw new Error('评论记录字段无效，请重新查询。');
 return {...data,id:data.id,status:data.status,source_label:data.source_label,location:originLocation(data.location)};
}
