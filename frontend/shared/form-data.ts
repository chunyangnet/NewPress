import {record, text, integer, flag} from './collection-data';
import {instant} from './site-time';

export interface FeedbackGrant { role: string; read: boolean; submit: boolean; vote: boolean; comment: boolean; moderate: boolean }
export type FeedbackSettings = Record<string, string | boolean | number | string[] | FeedbackGrant[]>;
export interface FeedbackCommand {
  action: string; version: number; key?: string; settings?: FeedbackSettings; item_id?: number; reply_id?: number; enabled?: boolean;
  [key: string]: string | number | boolean | FeedbackSettings | undefined;
}
export function feedbackReceipt(value: unknown, slug: string, command: FeedbackCommand) {
  const row=record(value), version=integer(row.version,1);
  const item_id=row.item_id===undefined?0:integer(row.item_id,1), reply_id=row.reply_id===undefined?0:integer(row.reply_id,1), reply_page=row.reply_page===undefined?0:integer(row.reply_page,1);
  if(row.replay!==undefined)flag(row.replay);
  const append=['submit','reply','vote'].includes(command.action);
  // Appends accept a reviewed older version; configuration and edits do not.
  if(text(row.slug)!==slug || version<=command.version || (!append&&version!==command.version+1) || (command.item_id&&item_id!==command.item_id)
    || (['submit','reply'].includes(command.action)&&!item_id) || (command.action==='reply'&&(!reply_id||!reply_page))) throw new Error('未收到有效的保存回执。输入已保留，请重试原请求。');
  // Validate the entire vote before consuming its request key or changing the UI.
  const vote_count=command.action==='vote'?integer(row.vote_count):0;
  const voted=command.action==='vote'?flag(row.voted):false;
  return {slug,version,item_id,reply_id,reply_page,vote_count,voted};
}

export interface Invitation {id:string;email:string;expires_at:string;state:'pending'|'closed'|'expired'}
export function invitations(value: unknown) {
  const row=record(value), available=flag(row.available), registration=text(row.registration);
  if(!['open','invite','closed'].includes(registration)||!Array.isArray(row.items))throw new Error('邀请列表格式不正确，请刷新重试。');
  const ids=new Set<string>();
  const items: Invitation[]=row.items.map(value=>{
    const item=record(value),id=text(item.id),email=text(item.email),expires_at=text(item.expires_at),state=item.state;
    if(!/^[a-f0-9]{64}$/.test(id)||ids.has(id)||!email||!instant(expires_at)||!['pending','closed','expired'].includes(String(state)))throw new Error('邀请列表存在无效记录，请刷新重试。');
    ids.add(id);return {id,email,expires_at,state:state as Invitation['state']};
  });
  return {available,registration,items};
}
export function invitationReceipt(value:unknown):void {
  if(record(value).ok!==true)throw new Error('未收到有效确认，请刷新邀请列表后核对，避免重复发送。');
}

export const verificationProviders=['local','turnstile','geetest','tencent','hcaptcha','recaptcha_v2','recaptcha_v3'] as const;
export type VerificationProvider=typeof verificationProviders[number];
export type VerificationConfig={enabled:false}|{enabled:true;provider:VerificationProvider;site_key:string;theme:'auto'|'light'|'dark';language:string;recaptcha_net:boolean};
export function verificationConfig(value:unknown,scene:string):VerificationConfig {
  const row=record(value);
  if(row.scene!==scene||!text(row.revision))throw new Error('验证场景或配置已变化，请重新加载。');
  if(!flag(row.enabled))return {enabled:false};
  const provider=text(row.provider),theme=text(row.theme),language=text(row.language),site_key=row.site_key===undefined?'':text(row.site_key);
  if(!verificationProviders.includes(provider as VerificationProvider)||!['auto','light','dark'].includes(theme)||!language||(provider!=='local'&&!site_key))throw new Error('验证配置不完整，请重新加载。');
  return {enabled:true,provider:provider as VerificationProvider,site_key,theme:theme as 'auto'|'light'|'dark',language,recaptcha_net:row.recaptcha_net===undefined?false:flag(row.recaptcha_net)};
}
export function verificationChallenge(value:unknown) {
  const row=record(value),id=text(row.id),image=text(row.image);
  if(!id||!/^[A-Za-z0-9_-]+$/.test(id)||!/^data:image\/png;base64,[A-Za-z0-9+/]+={0,2}$/.test(image)||image.length>250000||integer(row.expires_in,1)>600)throw new Error('验证码图片格式不正确，请重新加载。');
  return {id,image};
}
export function verificationReceipt(value:unknown,preview:boolean):string {
  const row=record(value);
  if(preview){if(row.ok!==true||row.preview!==true)throw new Error('验证回执无效，请重试');return 'preview-passed';}
  if(typeof row.token!=='string'||!row.token||integer(row.expires_in,1)>120)throw new Error('验证回执无效，请重试');
  return row.token;
}
