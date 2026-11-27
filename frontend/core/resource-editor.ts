import { amountUnits, amountText, decodeResourceConfig, decodeResourceOptions, emptyResourceOptions, resourcePath, setResourcePath, resourceCondition, resourceConditions, resourceSubmissions } from './resource-data';
import type { ResourceConfig, ResourceOptions, ResourceItem, ResourceVariant, ResourceCondition, ResourceFieldValue } from './resource-data';
import { errorMessage } from './editor-tool-data';
import { editorToolJSON } from './editor-tool-request';

type FieldSettings = { choices?: Record<string, string>; multiple?: boolean; moneyKey?: string; precision?: number | null; rows?: number; list?: boolean; max?: number; placeholder?: string; required?: boolean; wide?: boolean; hint?: string };

(() => {
 const section=document.querySelector<HTMLElement>('[data-resource-editor]');if(!section)return;
 const root=section;
 const input=root.querySelector<HTMLInputElement>('[data-resource-config]')!,workspace=root.querySelector<HTMLElement>('[data-resource-workspace]')!,message=root.querySelector<HTMLElement>('[data-resource-editor-status]')!;
 const e=(v: unknown)=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c] || c));
 const uid=(p: string)=>p+'_'+crypto.randomUUID().replaceAll('-','').slice(0,12);
 const kinds={public:'所有人',guest:'仅游客',login:'登录用户',points:'消耗积分',payment:'在线支付',level:'用户等级',membership:'会员方案 / 级别 / 周期',group:'用户组',follow:'关注作者',comment:'评论后解锁',tip:'打赏后解锁',like:'点赞后解锁',quiz:'答题后解锁',form:'提交表单'};
 const providers={direct:'直接下载',baidu:'百度网盘',aliyun:'阿里云盘',quark:'夸克网盘',lanzou:'蓝奏云',115:'115 网盘','123pan':'123 云盘',onedrive:'OneDrive',google:'Google Drive',dropbox:'Dropbox'};
 let config: ResourceConfig,index=0,version=0,options: ResourceOptions=emptyResourceOptions(),optionsReady=false;
 const drafts=new Map<string, string>(),invalidAmounts=new Map<string, string>();
 const expanded=new Map<string, boolean>();
 try{
  const initial: unknown=JSON.parse(input.value);config=decodeResourceConfig(initial);
 }catch{message.textContent='已有资源配置无法读取，请检查文章恢复点。';return;}
 const coins=()=>Object.fromEntries(options.currencies.map(c=>[c.key,c.settings]));
 const currencyPrecision=(kind: string,key: string): number | null=>kind==='payment'?key==='JPY'?0:['CNY','USD','EUR','GBP','HKD','LDC'].includes(key)?2:null:optionsReady?coins()[key]?.precision??null:null;
 const freshCondition=(kind: string): ResourceCondition=>({id:uid('rule'),kind,label:'',values:[],amount:['payment','points','tip'].includes(kind)?kind==='payment'?990:100*10**(currencyPrecision(kind,'main')??1):0,currency:kind==='payment'?'CNY':'main',minimum:kind==='level'?1:0,term:'any'});
 const freshVariant=(): ResourceVariant=>({id:uid('version'),name:'标准版',kind:'download',body:'',media_url:'',downloads:[{id:uid('file'),name:'资源文件',provider:'baidu',url:'',code:'',password:''}],groups:[{name:'支付解锁',conditions:[freshCondition('payment')]},{name:'会员专享',conditions:[freshCondition('membership')]}],questions:[],fields:[],pass_percent:100,attempts_per_day:3});
 const freshResource=(): ResourceItem=>({id:uid('resource'),title:'专属资源包',description:'',cover:'',size:'',updated:new Date().toLocaleDateString('sv-SE'),original_price:'',badge:'',features:['专属资源','持续更新'],member_cta:true,selection:'all',daily_limit:0,total_limit:0,starts_at:null,ends_at:null,variants:[freshVariant()]});
 let serialized=input.value,invalidConfig=false;
 const changed=()=>{serialized=input.value=JSON.stringify(config);input.dispatchEvent(new Event('input',{bubbles:true}));};
 function syncConfiguration(){
  if(input.value===serialized)return;
  serialized=input.value;
  try{const restored: unknown=JSON.parse(serialized);config=decodeResourceConfig(restored);invalidConfig=false;drafts.clear();invalidAmounts.clear();index=version=0;message.textContent='';render();}
  catch{invalidConfig=true;message.textContent='恢复的资源配置无法读取，请检查文章恢复点。';}
 }
 const pathValue=(path: string)=>resourcePath(config,path);
 function field(label: string,path: string,type='text',settings: FieldSettings={}){
  const value=pathValue(path),attr=`data-path="${path}"`;
  let control;
  if(settings.choices)control=`<select ${attr}${settings.multiple?' multiple data-list="selected"':''}>${Object.entries(settings.choices).map(([k,title])=>`<option value="${e(k)}" ${(settings.multiple?Array.isArray(value)&&value.includes(k):String(value??'')===k)?'selected':''}>${e(title)}</option>`).join('')}</select>`;
  else if(settings.moneyKey){
   const places=settings.precision??null,shown=drafts.get(settings.moneyKey)??(places!==null?amountText(value,places):''),known=places!==null;
   control=`<input type="text" inputmode="decimal" ${attr} data-money-key="${e(settings.moneyKey)}" value="${e(shown)}" maxlength="27" required ${known?'':`disabled placeholder="加载货币精度…"`}>`;
  }
  else if(type==='checkbox')control=`<input type="checkbox" ${attr} ${value?'checked':''}>`;
  else if(type==='textarea')control=`<textarea ${attr} rows="${settings.rows||4}" ${settings.list?'data-list="lines"':''} maxlength="${settings.max||262144}">${e(settings.list?(Array.isArray(value)?value:[]).join('\n'):value)}</textarea>`;
  else control=`<input type="${type}" ${attr} value="${e(type==='datetime-local'?(typeof value==='string'&&value?new Date(new Date(value).getTime()-new Date(value).getTimezoneOffset()*60000).toISOString().slice(0,16):''):value)}" ${type==='number'?`min="0" max="${settings.max||100000000000}" step="1"`:''} ${settings.placeholder?`placeholder="${e(settings.placeholder)}"`:''} ${settings.required?'required':''}>`;
  return `<label class="${settings.wide?'npre-wide':''} ${type==='checkbox'?'npre-check':''}"><span>${label}</span>${control}${settings.hint?`<small>${settings.hint}</small>`:''}</label>`;
 }
 const button=(text: string,action: string,path='',extra='')=>`<button type="button" data-action="${action}" data-path-action="${path}" ${extra}>${text}</button>`;
 function condition(c: ResourceCondition,path: string){
  let extra='';
  if(['points','payment','tip'].includes(c.kind)){
   const places=currencyPrecision(c.kind,c.currency),choices=c.kind==='payment'?Object.fromEntries(['CNY','USD','EUR','GBP','HKD','JPY','LDC'].map(k=>[k,k])):Object.fromEntries(Object.entries(coins()).map(([k,s])=>[k,s.name+' · '+k]));
   choices[c.currency] ||= c.currency;
   extra=field(c.kind==='payment'?'支付金额':'积分数量',path+'.amount','text',{moneyKey:c.id,precision:places,hint:places===null?'货币精度加载后即可编辑。':places===0?'此货币只支持整数。':`支持 ${places} 位小数，例如 ${amountText(1,places)}。`})+field('货币',path+'.currency','text',{choices});
  }
  if(['level','comment'].includes(c.kind))extra+=field(c.kind==='level'?'最低用户等级（1 起）':'评论至少字数',path+'.minimum','number',{max:c.kind==='level'?50:10000,hint:c.kind==='comment'?'仅计入本人审核通过的评论':'按后台等级配置的先后顺序'});
  if(c.kind==='membership'){
   const plans=Object.fromEntries((options.plans||[]).map(p=>[p.key,p.name+' · '+p.days+' 天']));(c.values||[]).forEach(x=>{plans[x] ||= x;});
   extra+=field('允许的会员方案',path+'.values','text',{choices:plans,multiple:true,wide:true,hint:'留空允许任意有效会员；可多选。普通、高级、年度高级等方案可在「等级与会员」中配置。'});
   extra+=field('最低会员级别（0 = 不限制）',path+'.minimum','number',{max:100})+field('会员周期',path+'.term','text',{choices:{any:'不限制',monthly:'月度会员（28–31 天方案）',annual:'年费会员（365 天及以上方案）'}});
  }
  if(c.kind==='group'){
   const roles=Object.fromEntries((options.roles||[]).map(r=>[r.slug,r.name]));(c.values||[]).forEach(x=>{roles[x] ||= x;});
   extra+=field('允许的用户组（可多选）',path+'.values','text',{choices:roles,multiple:true,wide:true});
  }
  if(c.kind==='quiz')extra+=`<div class="npre-inline npre-wide">${button('编辑题目与答案','configure-quiz')}<small>设置通过分数与答题次数。</small></div>`;
  if(c.kind==='form')extra+=`<div class="npre-inline npre-wide">${button('编辑表单字段','configure-form')}<small>设置读者需要填写的资料。</small></div>`;
  return `<div class="npre-condition"><div class="npre-condition-top">${field('解锁条件',path+'.kind','text',{choices:kinds})}${button('移除','remove',path)}</div>${extra?`<div class="npre-grid">${extra}</div>`:''}<details class="npre-custom-label" data-detail-key="${e(c.id)}:label" ${c.label?'open':''}><summary>自定义条件说明</summary>${field('前台显示的说明',path+'.label','text',{placeholder:'留空时自动生成'})}</details></div>`;
 }
 function render(request: {open?: string; focus?: string}={}){
  workspace.querySelectorAll<HTMLDetailsElement>('details[data-detail-key]').forEach(node=>expanded.set(node.dataset.detailKey!,node.open));
  if(request.open)expanded.set(request.open,true);
  const active=document.activeElement instanceof Element&&workspace.contains(document.activeElement)?document.activeElement as HTMLInputElement|HTMLTextAreaElement|HTMLSelectElement:null;
  const focus=request.focus||active?.dataset.path;
  const caret=active&&(active instanceof HTMLTextAreaElement||active instanceof HTMLInputElement&&active.type==='text')?[active.selectionStart,active.selectionEnd] as const:null;
  const ids=new Set(config.items.flatMap(r=>r.variants.flatMap(v=>v.groups.flatMap(g=>g.conditions.map(c=>c.id)))));
  for(const id of drafts.keys())if(!ids.has(id)){drafts.delete(id);invalidAmounts.delete(id);}
  index=Math.min(index,Math.max(0,config.items.length-1));
  const nav=`<div class="npre-nav" role="group" aria-label="选择资源">${config.items.map((r,i)=>button(e(r.title||`资源 ${i+1}`),'select','',`data-index="${i}" aria-pressed="${index===i}"`)).join('')}${button('＋ 添加资源','add-resource')}</div>`;
  if(!config.items.length){workspace.innerHTML=`<div class="npre-empty"><h3>添加一份专属内容</h3><p>选择一个起点，之后可以自由调整内容和解锁条件。</p><div>${button('<b>付费资源</b><small>网盘、文件与下载链接</small>','preset-paid')}${button('<b>会员内容</b><small>仅对会员开放的正文</small>','preset-member')}${button('<b>答题解锁</b><small>回答问题后查看内容</small>','preset-quiz')}${button('<b>表单收集</b><small>填写资料后获取资源</small>','preset-form')}</div></div>`;return;}
  const r=config.items[index]!,rp=`items.${index}`;version=Math.max(0,Math.min(version,r.variants.length-1));const v=r.variants[version]!,vp=rp+`.variants.${version}`;
  const detail=(key: string)=>`data-detail-key="${e(r.id+':'+v.id+':'+key)}"`;
  const variantTabs=`<div class="npre-variant-tabs" role="group" aria-label="选择内容版本">${r.variants.map((x,i)=>button(`${i+1} · ${e(x.name)}`,'version','',`data-index="${i}" aria-pressed="${version===i}"`)).join('')}${button('＋ 增加版本','add-version')}</div>`;
  const groups=v.groups.map((g,gi)=>`<section class="npre-group"><header><b>方式 ${gi+1} <span>满足本组全部条件</span></b>${button('删除此组','remove',vp+`.groups.${gi}`,v.groups.length===1?'disabled':'')}</header>${field('方式名称',vp+`.groups.${gi}.name`)}${g.conditions.map((c,ci)=>condition(c,vp+`.groups.${gi}.conditions.${ci}`)).join('<div class="npre-and">同时满足</div>')}<div>${button('＋ 添加条件','add-condition',vp+`.groups.${gi}.conditions`)}</div></section>`).join('<p class="npre-or">或</p>');
  const downloads=v.downloads.map((d,di)=>{const p=vp+`.downloads.${di}`;return `<div class="npre-link"><header><b>下载线路 ${di+1}</b>${button('移除','remove',p)}</header><div class="npre-grid">${field('网盘类型',p+'.provider','text',{choices:providers})}${field('线路 / 文件名称',p+'.name')}${field('下载地址',p+'.url','url',{wide:true,required:true,placeholder:'https://pan.baidu.com/s/...',hint:'仅在读者解锁后提供。'})}${field('提取码',p+'.code')}${field('解压密码',p+'.password')}</div></div>`;}).join('');
  const questions=v.questions.map((q,qi)=>{const p=vp+`.questions.${qi}`;return `<div class="npre-question"><header><b>题目 ${qi+1}</b>${button('移除','remove',p)}</header>${field('问题',p+'.title')}${field('选项（每行一项，留空为填空题）',p+'.options','textarea',{list:true,rows:3})}${field('正确答案',p+'.answer','text',{hint:'选择题答案必须与某一个选项完全一致。答案不会向读者公开。'})}</div>`;}).join('');
  const fields=v.fields.map((f,fi)=>{const p=vp+`.fields.${fi}`;return `<div class="npre-question"><header><b>表单字段 ${fi+1}</b>${button('移除','remove',p)}</header><div class="npre-grid">${field('字段标题',p+'.label')}${field('字段类型',p+'.type','text',{choices:{text:'单行文字',textarea:'多行文字',email:'电子邮箱',select:'下拉选择',checkbox:'确认勾选'}})}${field('必填',p+'.required','checkbox')}${f.type==='select'?field('选项（每行一项）',p+'.options','textarea',{list:true,wide:true,rows:3}):''}</div></div>`;}).join('');
  const interactive=v.groups.some(g=>g.conditions.some(c=>c.kind==='quiz'||c.kind==='form'));
  workspace.innerHTML=`${nav}
   <div class="npre-toolbar"><span>${r.variants.length} 个内容版本</span><div>${button('插入正文位置','insert')}${button('删除资源','remove',rp)}</div></div>
   <section class="npre-basics"><div class="npre-section-heading"><h3>基本信息</h3><span>读者解锁前可见</span></div><div class="npre-grid">${field('资源标题',rp+'.title','text',{required:true,wide:true})}${field('资源简介（可选）',rp+'.description','textarea',{wide:true,rows:2,max:2000})}</div></section>
   <details class="npre-details" data-detail-key="${e(r.id)}:appearance"><summary>外观与访问设置 <span>封面、限额、开放时间</span></summary><div class="npre-grid">${field('封面地址',rp+'.cover','url')}${field('文件大小',rp+'.size','text',{placeholder:'248.6 MB'})}${field('更新日期 / 版本',rp+'.updated')}${field('原价展示（可选）',rp+'.original_price','text',{placeholder:'¥29.90'})}${field('角标文案',rp+'.badge','text',{placeholder:'限时特惠'})}${field('权益亮点（每行一项）',rp+'.features','textarea',{list:true,rows:3})}${field('每人每日下载次数',rp+'.daily_limit','number',{hint:'0 为不限；按站点时区每日重置'})}${field('每人累计下载次数',rp+'.total_limit','number',{hint:'0 为不限；所有版本共享额度'})}${field('开始开放时间',rp+'.starts_at','datetime-local',{hint:'按当前浏览器本地时区填写'})}${field('结束访问时间',rp+'.ends_at','datetime-local')}${field('显示会员引导',rp+'.member_cta','checkbox')}${field('版本展示规则',rp+'.selection','text',{choices:{all:'展示所有符合条件的版本',first:'仅展示首个符合条件的版本'}})}</div></details>
   <div class="npre-section-heading"><h3>版本内容</h3><span>读者解锁后可见</span></div>${variantTabs}
   <div class="npre-version"><div class="npre-grid">${field('版本名称',vp+'.name','text',{required:true})}${field('内容类型',vp+'.kind','text',{choices:{download:'网盘 / 资源下载',content:'隐藏正文 / 后续内容',image:'隐藏图片',video:'隐藏视频'}})}</div>
    ${r.variants.length>1?`<div class="npre-version-actions">${button('↑ 提高优先级','move-version','',version===0?'disabled':'')}${button('删除版本','remove',vp)}</div>`:''}
    ${v.kind==='download'?`${downloads}${button('＋ 添加下载线路','add-download')}`:''}
    ${['image','video'].includes(v.kind)?field(v.kind==='image'?'私密图片 URL':'私密视频 URL',vp+'.media_url','url',{wide:true,required:true,hint:'支持 HTTPS 或站内路径。请使用未在正文中公开的地址。'}):''}
    ${v.kind==='content'?`${field('隐藏正文（HTML）',vp+'.body','textarea',{wide:true,rows:6})}<div class="npre-inline">${button('移入正文选区','hide-selection')}${button('隐藏光标后的正文','hide-tail')}<small>原文将替换为资源位置标记。</small></div>`:`<details class="npre-details npre-description-settings" ${detail('body')} ${v.body?'open':''}><summary>解锁后说明 <span>可选</span></summary>${field('说明内容（HTML）',vp+'.body','textarea',{wide:true,rows:4})}</details>`}
   </div>
   <div class="npre-section-heading npre-rules-heading"><h3>解锁条件</h3><span>满足任意一种方式即可</span></div>${groups}${button('＋ 添加解锁方式','add-group')}
   <details class="npre-details" ${detail('questions')} ${interactive?'open':''}><summary>答题与表单 <span>${v.questions.length} 个问题 · ${v.fields.length} 个字段</span></summary><div class="npre-grid">${field('答题通过比例（%）',vp+'.pass_percent','number',{max:100})}${field('每日可答题次数',vp+'.attempts_per_day','number',{max:100})}</div>${questions}${button('＋ 添加题目','add-question')}<hr>${fields}${button('＋ 添加表单字段','add-field')}<p class="npre-help">请在解锁条件中选择「答题后解锁」或「提交表单」。仅收集必要资料。</p></details>
   ${root.dataset.post!=='0'?`<details class="npre-details" ${detail('submissions')}><summary>提交记录</summary>${button('加载最近 200 条记录','submissions')}<div data-submissions></div></details>`:''}`;
  workspace.querySelectorAll<HTMLDetailsElement>('details[data-detail-key]').forEach(node=>{const saved=expanded.get(node.dataset.detailKey!);if(saved!==undefined)node.open=saved;});
  workspace.querySelectorAll<HTMLInputElement>('[data-money-key]').forEach(node=>node.setCustomValidity(invalidAmounts.get(node.dataset.moneyKey||'')||''));
  if(focus){
   const node=[...workspace.querySelectorAll<HTMLInputElement|HTMLTextAreaElement|HTMLSelectElement>('[data-path]')].find(node=>node.dataset.path===focus);
   if(node&&!node.disabled){
    node.focus({preventScroll:!request.focus});
    if(!request.focus&&caret&&(node instanceof HTMLTextAreaElement||node instanceof HTMLInputElement&&node.type==='text'))node.setSelectionRange(caret[0],caret[1]);
   }
  }else if(active?.dataset.action){
   const node=[...workspace.querySelectorAll<HTMLButtonElement>('[data-action]')].find(node=>node.dataset.action===active.dataset.action&&node.dataset.index===active.dataset.index&&node.dataset.pathAction===active.dataset.pathAction);
   if(node&&!node.disabled)node.focus({preventScroll:true});
  }
 }
 const editable=()=>!invalidConfig && !root.querySelector<HTMLFieldSetElement>('fieldset')?.disabled && !input.disabled;
 function update(event: Event){
  const node=event.target instanceof Element?event.target.closest<HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement>('[data-path]'):null;
  const path=node?.dataset.path;if(!node||!path||!editable())return;
  const parts=path.split('.'),key=parts.at(-1),target=resourceCondition(config,path);
  let value: ResourceFieldValue=node instanceof HTMLInputElement&&node.type==='checkbox'?node.checked:node.type==='number'?Number(node.value):node.value;
  if(node.dataset.moneyKey){
   if(!target)return;drafts.set(target.id,node.value);
   try{value=amountUnits(node.value,currencyPrecision(target.kind,target.currency));invalidAmounts.delete(target.id);node.setCustomValidity('');message.textContent='';}
   catch(error){const text=errorMessage(error,'金额无效');invalidAmounts.set(target.id,text);node.setCustomValidity(text);message.textContent=text;return;}
  }
  if(key==='currency'&&target&&['points','payment','tip'].includes(target.kind)){
   const old=target.currency,places=currencyPrecision(target.kind,old),shown=drafts.get(target.id)??(places!==null?amountText(target.amount,places):null);
   try{if(shown===null||typeof value!=='string')throw Error('请等待货币精度加载后再切换。');target.amount=amountUnits(shown,currencyPrecision(target.kind,value));drafts.delete(target.id);invalidAmounts.delete(target.id);}
   catch(error){node.value=old;message.textContent='无法切换货币：'+errorMessage(error,'金额无效');return;}
  }
  if(node.type==='datetime-local'){
   if(node.value&&!Number.isFinite(new Date(node.value).getTime())){message.textContent='日期格式不正确。';return;}
   value=node.value?new Date(node.value).toISOString():null;
  }
  if(node.dataset.list==='lines')value=node.value.split('\n').map(x=>x.trim()).filter(Boolean);
  if(node.dataset.list==='selected'&&node instanceof HTMLSelectElement)value=[...node.selectedOptions].map(x=>x.value);
  if(!setResourcePath(config,path,value))return;
  if(key==='kind'&&target&&typeof value==='string'){drafts.delete(target.id);invalidAmounts.delete(target.id);Object.assign(target,{...freshCondition(value),id:target.id});}
  changed();
  // Renaming must not replace the focused field or swallow the next click on blur.
  if(key==='name'||key==='title'){
   workspace.querySelectorAll<HTMLButtonElement>('[data-action="select"]').forEach(button=>{const i=Number(button.dataset.index),item=config.items[i];if(item)button.textContent=item.title||`资源 ${i+1}`;});
   workspace.querySelectorAll<HTMLButtonElement>('[data-action="version"]').forEach(button=>{const i=Number(button.dataset.index),variant=config.items[index]?.variants[i];if(variant)button.textContent=`${i+1} · ${variant.name}`;});
  }
  if(event.type==='change'&&(key==='kind'||key==='type'||key==='currency')){
   const r=config.items[index],v=r?.variants[version];
   render({open:target&&['quiz','form'].includes(target.kind)?`${r?.id}:${v?.id}:questions`:undefined});
  }
 }
 root.closest('form')?.addEventListener('submit',event=>{
  syncConfiguration();
  if(invalidConfig){event.preventDefault();event.stopImmediatePropagation();return;}
  if(!invalidAmounts.size)return;
  event.preventDefault();event.stopImmediatePropagation();message.textContent=[...invalidAmounts.values()][0] || '请检查金额';
  const id=invalidAmounts.keys().next().value;
  for(let ri=0;ri<config.items.length;ri++){
   const vi=config.items[ri]!.variants.findIndex(v=>v.groups.some(g=>g.conditions.some(c=>c.id===id)));
   if(vi>=0){index=ri;version=vi;render();break;}
  }
  const node=[...workspace.querySelectorAll<HTMLInputElement>('[data-money-key]')].find(n=>invalidAmounts.has(n.dataset.moneyKey||''));node?.reportValidity();
 },true);
 // Browser recovery restores all form fields, then emits source input/type change.
 // Follow the canonical hidden field so editing cannot overwrite a restored resource.
 const form=root.closest('form');
 form?.addEventListener('input',syncConfiguration);form?.addEventListener('change',syncConfiguration);
 form?.addEventListener('reset',()=>queueMicrotask(syncConfiguration));
 workspace.addEventListener('input',event=>{if(event.target instanceof Element&&!event.target.matches('select,input[type=checkbox]'))update(event);});workspace.addEventListener('change',update);
 workspace.addEventListener('invalid',event=>{
  if(!(event.target instanceof Element))return;
  let details=event.target.closest('details');
  while(details&&workspace.contains(details)){details.open=true;details=details.parentElement?.closest('details')||null;}
 },true);
 workspace.addEventListener('pointerdown',event=>{if(event.target instanceof Element&&event.target.closest('[data-action="insert"],[data-action="hide-selection"],[data-action="hide-tail"]'))event.preventDefault();});
 workspace.addEventListener('click',async event=>{
  const node=event.target instanceof Element?event.target.closest<HTMLButtonElement>('[data-action]'):null;if(!node||!editable())return;
  const action=node.dataset.action||'',r=config.items[index],v=r?.variants[version];message.textContent='';
  if(action==='select'){const next=Number(node.dataset.index);if(Number.isInteger(next)&&config.items[next]){index=next;version=0;render();}return;}if(action==='version'){const next=Number(node.dataset.index);if(Number.isInteger(next)&&r?.variants[next]){version=next;render();}return;}
  if(action==='add-resource'||action.startsWith('preset-')){
   if(config.items.length>=24){message.textContent='最多添加 24 个资源。';return;}
   const next=freshResource();if(action==='preset-member'){next.title='会员专属内容';next.variants[0]!.kind='content';next.variants[0]!.downloads=[];next.variants[0]!.groups=[{name:'会员专享',conditions:[freshCondition('membership')]}];}
   if(action==='preset-quiz'){next.title='完成挑战，解锁完整内容';const n=next.variants[0]!;n.kind='content';n.downloads=[];n.groups=[{name:'答题解锁',conditions:[freshCondition('quiz')]}];n.questions=[{id:uid('question'),title:'请填写题目',options:['选项 A','选项 B'],answer:'选项 A'}];}
   if(action==='preset-form'){next.title='填写资料，领取资源';const n=next.variants[0]!;n.groups=[{name:'提交资料',conditions:[freshCondition('form')]}];n.fields=[{id:uid('field'),label:'称呼',type:'text',required:true,options:[]}];}
   config.items.push(next);index=config.items.length-1;version=0;
   changed();render({focus:`items.${index}.title`});return;
  }
  if(!r||!v)return;
  if(action==='configure-quiz'||action==='configure-form'){
   render({open:`${r.id}:${v.id}:questions`,focus:action==='configure-quiz'&&v.questions.length?`items.${index}.variants.${version}.questions.0.title`:action==='configure-form'&&v.fields.length?`items.${index}.variants.${version}.fields.0.label`:undefined});
   const add=workspace.querySelector<HTMLButtonElement>(`[data-action="${action==='configure-quiz'?'add-question':'add-field'}"]`);
   if(action==='configure-quiz'&&!v.questions.length||action==='configure-form'&&!v.fields.length)add?.focus();
   return;
  }
  if(action==='remove'){
   const path=node.dataset.pathAction||'';
   if(!/^items\.\d+(?:\.variants\.\d+(?:\.(?:downloads|questions|fields|groups)\.\d+(?:\.conditions\.\d+)?)?)?$/.test(path))return;
   const parts=path.split('.'),at=Number(parts.pop()),list=pathValue(parts.join('.'));
   if(!Array.isArray(list)||!Number.isInteger(at)||at<0||at>=list.length)return;
   if(['groups','conditions','variants'].includes(parts.at(-1)||'')&&list.length===1){message.textContent='至少保留一个版本、一组条件和一个条件。公开资源可选择「所有人」。';return;}
   list.splice(at,1);
  }
  if(action==='add-version'){if(r.variants.length>=12)return;const n=freshVariant();n.name=`版本 ${r.variants.length+1}`;r.variants.push(n);version=r.variants.length-1;}
  if(action==='move-version'&&version>0){[r.variants[version-1],r.variants[version]]=[r.variants[version]!,r.variants[version-1]!];version--;}
  if(action==='add-group'){if(v.groups.length>=12)return;v.groups.push({name:'另一种解锁方式',conditions:[freshCondition('login')]});}
  if(action==='add-condition'){const list=resourceConditions(config,node.dataset.pathAction||'');if(!list||list.length>=16)return;list.push(freshCondition('login'));}
  if(action==='add-download'){if(v.downloads.length>=12)return;v.downloads.push({id:uid('file'),name:'备用下载',provider:'quark',url:'',code:'',password:''});}
  if(action==='add-question'){if(v.questions.length>=20)return;v.questions.push({id:uid('question'),title:'',options:[],answer:''});}
  if(action==='add-field'){if(v.fields.length>=20)return;v.fields.push({id:uid('field'),label:'',type:'text',required:true,options:[]});}
  if(action==='insert'||action==='hide-selection'||action==='hide-tail'){
   const editor=window.ChunnelBlockEditor,source=document.querySelector<HTMLTextAreaElement>('[name=content_source]');if(!source||source.disabled||source.readOnly){message.textContent='正文编辑器尚未就绪或不可编辑。';return;}
   const hiding=action==='hide-selection'||action==='hide-tail';
   const marker=`\n<p>[resource:${r.id}]</p>\n`;
   if(hiding&&editor?.replaceSelectedHTML){
    try{const hidden=editor.replaceSelectedHTML(marker,action==='hide-tail');if(hidden!==null){v.body+=(v.body?'\n':'')+hidden;changed();render();return;}}
    catch(error){message.textContent=errorMessage(error,'正文插入失败，请重新选择位置。');return;}
   }
   const selection=editor?.capture()||{start:source.selectionStart,end:source.selectionEnd,value:source.value};
   if(action==='hide-tail')selection.end=source.value.length;
   if(selection.value!==source.value){message.textContent='正文已变化，请重新选择位置。';return;}
   if(hiding&&selection.end<=selection.start){message.textContent='请先在正文中选择需要隐藏的内容。';return;}
   const hidden=hiding?source.value.slice(selection.start,selection.end):'';
   try{if(editor)editor.insert(marker,selection);else{source.setRangeText(marker,selection.start,selection.end,'end');source.dispatchEvent(new Event('input',{bubbles:true}));}}
   catch(error){message.textContent=errorMessage(error,'正文插入失败，请重新选择位置。');return;}
   if(hiding)v.body+=(v.body?'\n':'')+hidden;
  }
  if(action==='submissions'){
   const target=workspace.querySelector<HTMLElement>('[data-submissions]');if(!target||node.disabled)return;node.disabled=true;
   try{const data=await editorToolJSON(`/api/v1/admin/resource-submissions/${root.dataset.post}`);
    const items=resourceSubmissions(data);if(!target.isConnected)return;target.innerHTML=items.length?`<div class="npre-table"><table><thead><tr><th>身份</th><th>版本</th><th>状态</th><th>资料</th><th>时间</th></tr></thead><tbody>${items.map(s=>`<tr><td>${e(s.actor)}</td><td>${e(s.resource)} / ${e(s.variant)}</td><td>${s.passed?'答题通过':'未通过'} · ${s.form_complete?'已提交':'未填写'}</td><td>${Object.entries(s.fields).map(([k,val])=>`${e(k)}: ${e(val)}`).join('<br>')}</td><td>${e(new Date(s.updated_at).toLocaleString())}</td></tr>`).join('')}</tbody></table></div>`:'<p>暂无提交记录。</p>';
   }catch(error){if(target.isConnected)target.textContent=errorMessage(error,'提交记录暂时无法读取，请重试。');}finally{node.disabled=false;}return;
  }
  const vp=`items.${index}.variants.${version}`;
  const focus=action==='add-question'?`${vp}.questions.${v.questions.length-1}.title`:action==='add-field'?`${vp}.fields.${v.fields.length-1}.label`:action==='add-download'?`${vp}.downloads.${v.downloads.length-1}.url`:action==='add-version'?`${vp}.name`:undefined;
  changed();render({focus,open:action==='add-question'||action==='add-field'?`${r.id}:${v.id}:questions`:undefined});
 });
 void editorToolJSON('/api/v1/admin/resource-options').then(decodeResourceOptions).then(data=>{options=data;optionsReady=true;render();}).catch(()=>{message.textContent='货币、会员或用户组选项暂未加载，请刷新页面重试。';});
 render();
})();
