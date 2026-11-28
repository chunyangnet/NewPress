import {array,boolean,choice,hash,invalidTransfer,number,optional,shape,string,timestamp,unique} from './transfer-data';

export type ImportInput={source:string;format:'markdown'|'wordpress';state:'draft'|'preserve';files:{name:string;text:string}[];authors:Record<string,number>;media:Record<string,string>};
const localPath=(value:unknown)=>{const path=string(4096)(value);if(!path.startsWith('/')||/^\/[/\\]/.test(path)||/[\\\x00-\x1f]/.test(path))throw invalidTransfer();return path;};
const row=shape({key:string(256),title:string(),type:choice('post','page'),status:choice('draft','pending','publish','future','private','trash'),url:localPath,old_path:optional(string(4096),''),action:choice('create','created','skip','changed','review','conflict'),message:optional(string(),''),post_id:optional(number(1),0),version:optional(number(1),0)});
const item=shape({key:string(256),author:string(),source_url:optional(string(4096),''),original_status:string(128),warnings:array(string()),images:array(string(2048)),preview:string(16<<20),categories:array(string()),tags:array(string())});
const response=shape({plan:shape({source:string(80),format:choice('markdown','wordpress'),revision:hash,items:array(row,100),new:number(0,100),existing:number(0,100),conflicts:number(0,100)}),
 document:shape({items:array(item,100),skipped:array(shape({key:string(),kind:string(),reason:string()}),10000),warnings:array(string()),authors:array(string(),100)}),
 targets:array(shape({id:number(1),name:string()}),100),default_author:number(1),applied:boolean,token:optional(string(1500),''),expires:optional(timestamp,'')});
export type ImportReport=ReturnType<typeof response>;
export function importReport(value:unknown,input:ImportInput,identity:number,previous?:ImportReport):ImportReport {
 const result=response(value),plan=result.plan,document=result.document;
 if(plan.source!==input.source||plan.format!==input.format||result.default_author!==identity||result.applied!==!!previous||!plan.items.length||plan.items.length!==document.items.length)throw invalidTransfer();
 unique(plan.items,r=>r.key);unique(document.items,r=>r.key);unique(result.targets,r=>r.id);unique(document.authors,r=>r);
 if(input.format==='markdown'&&(plan.items.length!==input.files.length||plan.items.some(r=>!input.files.some(f=>f.name===r.key))))throw invalidTransfer();
 if(plan.items.some(r=>!r.key||!document.items.some(i=>i.key===r.key))||document.items.some(i=>!document.authors.includes(i.author)))throw invalidTransfer();
 if(plan.new!==plan.items.filter(r=>r.action===(result.applied?'created':'create')).length||plan.conflicts!==plan.items.filter(r=>r.action==='conflict').length||plan.new+plan.existing+plan.conflicts!==plan.items.length)throw invalidTransfer();
 if(plan.items.some(r=>(['created','skip','changed','review'].includes(r.action)&&(!r.post_id||!r.version))||result.applied&&r.action==='create'||!result.applied&&r.action==='created'))throw invalidTransfer();
 if(previous){
  if(result.token||result.expires||plan.conflicts||plan.items.length!==previous.plan.items.length||plan.items.some(r=>!previous.plan.items.some(p=>p.key===r.key&&p.type===r.type)))throw invalidTransfer();
  for(const r of plan.items){const prior=previous.plan.items.find(p=>p.key===r.key)!;if(prior.post_id&&r.post_id!==prior.post_id||r.action==='created'&&(prior.action!=='create'||r.url!==prior.url||r.status!==prior.status||r.title!==prior.title))throw invalidTransfer();}
 }else if(plan.conflicts?(!!result.token||!!result.expires):(!result.token||!result.expires||Date.parse(result.expires)<=Date.now()))throw invalidTransfer();
 return result;
}
export function validateImportInput(input:ImportInput):void {
 if(!/^[a-z0-9][a-z0-9._-]{0,79}$/.test(input.source)||!['markdown','wordpress'].includes(input.format)||!['draft','preserve'].includes(input.state)||input.files.length<1||input.files.length>100||input.format==='wordpress'&&input.files.length!==1)throw new Error('请检查来源标识、文件格式和文件数量。');
 unique(input.files,f=>f.name);
 const encoder=new TextEncoder();let bytes=0;
 for(const file of input.files){bytes+=encoder.encode(file.text).length;if(!file.name||encoder.encode(file.name).length>256||/[\/\\\x00-\x1f\x7f]/.test(file.name)||file.text.includes('\0'))throw new Error('文件名或 UTF-8 内容无效。');}
 if(bytes>8*1024*1024)throw new Error('正文源总计不能超过 8 MiB。');
 if(Object.keys(input.authors).length>100||Object.values(input.authors).some(id=>!Number.isSafeInteger(id)||id<1))throw new Error('作者映射须为有效的本站账号编号。');
 if(Object.keys(input.media).length>200||Object.entries(input.media).some(([from,to])=>!from||from.length>2048||to.length>2048||!/^\/media\/[1-9]\d*\//.test(to)))throw new Error('图片映射须为本站媒体库路径。');
}
