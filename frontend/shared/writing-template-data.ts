import { record, string, boolean, number, list, date } from './ledger-data';
import { byteLength, writingFailure } from './ai-writing-data';
export { byteLength as bytes } from './ai-writing-data';
export const MAX_IMPORT = 128 * 1024;
export interface Definition {base_prompt: string; title: string; description: string; instruction: string; example: string}
export interface TemplateSource {kind: string; template_id?: string; version?: number; sha256?: string}
export interface Revision {version: number; definition: Definition; archived: boolean; actor: string; at: string; source: TemplateSource}
export interface Template {id: string; revisions: Revision[]}
export interface TemplateSummary {id: string; version: number; title: string; description: string; base_prompt: string; archived: boolean; updated_at: string}
export interface TemplateExport {schema: 1; kind: 'newpress.writing-template'; template_id: string; version: number; definition: Definition}
export interface TemplateResult {id: string; version: number; replayed: boolean}
export interface TemplateCommand {action: 'save'|'archive'|'activate'|'restore'|'import'; id?: string; expected_version?: number; definition?: Definition; restore_version?: number; document?: string; import_sha256?: string; confirm_import?: boolean}
const identity = (value: unknown) => {const id = string(value); if (!/^[0-9a-f]{32}$/.test(id)) throw new Error('模板标识未确认'); return id;};
const sha256 = (value: unknown) => {const hash = string(value); if (!/^[0-9a-f]{64}$/.test(hash)) throw new Error('模板摘要未确认'); return hash;};
const timestamp = (value: unknown) => {const text = date(value); if (!text) throw new Error('模板时间缺失'); return text;};
export function definition(value: unknown): Definition {
  const row = record(value), result = {base_prompt:string(row.base_prompt), title:string(row.title), description:string(row.description), instruction:string(row.instruction), example:string(row.example)};
  if (!result.title.trim() || !result.base_prompt || result.base_prompt.startsWith('team:') || !result.instruction.trim()) throw new Error('模板内容未确认');
  return result;
}
export function templateList(value: unknown): TemplateSummary[] {
  const row = record(value), items = list(row.items, value => {
    const row = record(value); return {id:identity(row.id), version:number(row.version,1), title:string(row.title), description:string(row.description), base_prompt:string(row.base_prompt), archived:boolean(row.archived), updated_at:timestamp(row.updated_at)};
  });
  sha256(row.snapshot_sha256);
  if (row.next_offset !== -1 || number(row.total,0,100) !== items.length || new Set(items.map(item=>item.id)).size !== items.length) throw new Error('模板列表不完整，请重新加载。');
  return items;
}
export function templateDetail(value: unknown, expectedID: string): Template {
  const row = record(value), id = identity(row.id);
  if (id !== expectedID) throw new Error('载入了另一份模板，请重试。');
  const revisions = list(row.revisions, value => {
    const row = record(value), raw = record(row.source), source: TemplateSource = {kind:string(raw.kind)};
    if (!['manual','archive','activate','history','import'].includes(source.kind)) throw new Error('模板版本来源未确认');
    if (source.kind === 'history' || source.kind === 'import') {source.template_id=identity(raw.template_id);source.version=number(raw.version,1);}
    if (source.kind === 'import') source.sha256=sha256(raw.sha256);
    return {version:number(row.version,1), definition:definition(row.definition), archived:boolean(row.archived), actor:string(row.actor), at:timestamp(row.at), source};
  });
  if (!revisions.length || revisions.length > 2000 || revisions.some((revision,index)=>revision.version!==index+1 || revision.source.kind==='history' && (revision.source.template_id!==id || (revision.source.version ?? 0)>=revision.version))) throw new Error('模板版本历史不完整');
  return {id,revisions};
}
export function templateExport(value: unknown, expectedID?: string, expectedVersion?: number): TemplateExport {
  const row = record(value), id = identity(row.template_id), version = number(row.version,1);
  if (row.schema !== 1 || row.kind !== 'newpress.writing-template' || expectedID !== undefined && id !== expectedID || expectedVersion !== undefined && version !== expectedVersion) throw new Error('模板导出版本未确认');
  return {schema:1,kind:'newpress.writing-template',template_id:id,version,definition:definition(row.definition)};
}
export async function importPreview(value: unknown, raw: string) {
  const row = record(value), hash = sha256(row.sha256);
  if (byteLength(raw) > MAX_IMPORT) throw new Error('模板文件超过 128 KiB');
  const digest = await crypto.subtle.digest('SHA-256',new TextEncoder().encode(raw));
  const actual = Array.from(new Uint8Array(digest),byte=>byte.toString(16).padStart(2,'0')).join('');
  const document = templateExport(row.document), supplied = templateExport(JSON.parse(raw) as unknown);
  if (hash !== actual || JSON.stringify(document) !== JSON.stringify(supplied)) throw new Error('导入预览与所选文件不一致，请重新预览。');
  return {raw,document,sha256:hash};
}
export function isResult(value: unknown): value is TemplateResult {
  try {const row=record(value);identity(row.id);number(row.version,1);boolean(row.replayed);return true;} catch{return false;}
}
export function templateReceipt(value: unknown, command: TemplateCommand): TemplateResult {
  if (!isResult(value) || command.id && value.id!==command.id || value.version !== (command.action==='import' || !command.id ? 1 : (command.expected_version ?? 0)+1)) throw new Error('保存回执与原模板或版本不一致，请重试原请求核对。');
  return value;
}
export function savedTemplate(value: unknown, result: TemplateResult, command: TemplateCommand): Template {
  const item=templateDetail(value,result.id),revision=item.revisions.find(row=>row.version===result.version);
  if(!revision)throw new Error('保存的版本不在历史中');
  const kind=command.action==='save'?'manual':command.action==='restore'?'history':command.action;
  let expected:Definition|undefined;
  if(command.action==='save')expected=command.definition;
  else if(command.action==='import')expected=templateExport(JSON.parse(command.document||'') as unknown).definition;
  else expected=item.revisions.find(row=>row.version===(command.action==='restore'?command.restore_version:command.expected_version))?.definition;
  if(revision.source.kind!==kind || revision.archived!==(command.action==='archive') || !expected || JSON.stringify(revision.definition)!==JSON.stringify(definition(expected)) || command.action==='restore'&&revision.source.version!==command.restore_version || command.action==='import'&&revision.source.sha256!==command.import_sha256)throw new Error('保存版本的内容与原操作不一致');
  return item;
}
export class Mutation {
  readonly body: string;
  uncertain = false;
  constructor(body: unknown, readonly key: string) {this.body=JSON.stringify(body);}
  retain(value: unknown) {const error=writingFailure(value);return this.uncertain=this.uncertain||(!error.definite&&!error.beforeSend);}
}
