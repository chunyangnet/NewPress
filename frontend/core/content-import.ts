import {importReport,validateImportInput,type ImportInput,type ImportReport} from '../shared/content-import-data';
import {TransferSession,TransferError} from '../shared/transfer-session';

(() => {
  'use strict';
  const element=document.querySelector<HTMLElement>('[data-content-import]');if(!element)return;
  const root=element;
  const $=<T extends Element=HTMLElement>(selector:string)=>{const node=root.querySelector<T>(selector);if(!node)throw new Error('导入界面不完整');return node;};
  const form=$<HTMLFormElement>('[data-import-form]'),status=$('[data-import-status]');
  const field=<T extends HTMLInputElement|HTMLSelectElement=HTMLInputElement>(name:string)=>form.elements.namedItem(name) as T;
  const results=$('[data-import-results]'),summary=$('[data-import-summary]'),actions=$('[data-import-actions]');
  const apply=$<HTMLButtonElement>('[data-import-apply]'),download=$<HTMLButtonElement>('[data-import-download]'),mappings=$('[data-import-mappings]');
  let input:ImportInput|null=null,report:ImportReport|null=null,preview:ImportReport|null=null,token='',dirty=false,draftGeneration=0;
  let pending:{input:ImportInput;preview:ImportReport;token:string}|null=null;
  const session=new TransferSession(root,'/api/v1/admin/imports',lost=>{
    draftGeneration++;
    if(lost){input=null;report=null;preview=null;pending=null;token='';form.reset();mappings.replaceChildren();results.replaceChildren();summary.replaceChildren();actions.hidden=true;message('登录或权限已变化，请重新打开页面。','error');}
    lock(false);
  },()=>{if(!session.uncertain){token='';dirty=true;message('页面已恢复，请重新生成预览。');}else message('上次迁入结果待核对，请重试原批次。','error');lock(false);},false);
  const readonly=session.readonly;
  const make=<K extends keyof HTMLElementTagNameMap>(tag:K,text?:string,cls?:string):HTMLElementTagNameMap[K]=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n;};
  const message=(text:string,tone='')=>{status.textContent=text;status.className='import-status'+(tone?' is-'+tone:'');};
  function invalidate(){draftGeneration++;dirty=true;if(!session.uncertain){token='';apply.disabled=true;}if(report)message('输入或映射已修改，请重新生成预览。');}
  form.addEventListener('input',invalidate);mappings.addEventListener('input',invalidate);
  field<HTMLSelectElement>('format').addEventListener('change',()=>{const wp=field<HTMLSelectElement>('format').value==='wordpress';field('files').accept=wp?'.xml':'.md,.markdown';field('files').multiple=!wp;field('files').value='';$('[data-import-files]').textContent=wp?'选择源站导出的 WXR 1.2 XML 文件。':'支持 Markdown 常用格式及 YAML 元信息。';mappings.hidden=true;input=null;report=null;invalidate();});
  field('files').addEventListener('change',()=>{input=null;mappings.hidden=true;$('[data-import-authors]').replaceChildren();$('[data-import-media]').replaceChildren();});
  function lock(value:boolean){
    const inaccessible=readonly||session.lost||session.disposed||!session.active;
    form.querySelector('fieldset')!.disabled=value||inaccessible||session.uncertain;
    mappings.querySelectorAll('input').forEach(n=>n.disabled=value||inaccessible||session.uncertain);
    apply.disabled=value||inaccessible||(!token&&!session.uncertain)||dirty&&!session.uncertain;
    download.disabled=value||inaccessible||!report;root.setAttribute('aria-busy',String(value));
    if(session.uncertain)apply.textContent='核对并重试原迁入';
  }
  function show(data:ImportReport){
    if(!input)return;const shownInput=input;
    report=data;token=data.token||'';dirty=false;summary.replaceChildren();results.replaceChildren();actions.hidden=false;
    for(const [label,count] of [['新建',data.plan.new],['保留已有',data.plan.existing],['冲突',data.plan.conflicts],['跳过类型',data.document.skipped.length]])summary.append(make('span',`${label} ${count}`));
    const labels:Record<string,string>={create:'待迁入',created:'已迁入',skip:'已存在',changed:'来源有变化',review:'需人工核对',conflict:'待解决'};
    data.plan.items.forEach(row=>{
      const item=data.document.items.find(item=>item.key===row.key)!,card=make('article',undefined,'import-item');card.dataset.action=row.action;
      const head=make('header'),title=make('div');title.append(make('h3',row.title),make('code',row.key));head.append(title,make('span',labels[row.action]||row.action,'import-state'));card.append(head);
      const states:Record<string,string>={draft:'草稿',pending:'待审核',publish:'已发布',future:'定时发布',private:'私密',trash:'回收站'};
      card.append(make('p',`${row.type==='page'?'页面':'文章'} · ${states[row.status]||row.status} · ${row.url}`));if(row.old_path)card.append(make('p',`旧路径：${row.old_path}`));if(row.message)card.append(make('p',row.message));
      if(item.categories.length)card.append(make('p',`分类：${item.categories.join('、')}`));if(item.tags.length)card.append(make('p',`标签：${item.tags.join('、')}`));
      if(item.warnings.length){const list=make('ul');item.warnings.forEach(w=>list.append(make('li',w)));card.append(list);}
      const detail=make('details');detail.append(make('summary','查看正文预览'));const prose=make('div',undefined,'import-prose');
      // Only the server's sanitized HTML projection is used here; uploaded
      // source is never mounted. Unmapped images are converted to links.
      prose.innerHTML=item.preview;prose.querySelectorAll('a').forEach(a=>{a.target='_blank';a.rel='noopener noreferrer';});detail.append(prose);card.append(detail);
      if(row.post_id){const link=make('a',`打开内容 #${row.post_id}`);link.href=`/admin/content/${row.post_id}/edit/`;link.target='_blank';link.rel='noopener';card.append(link);}results.append(card);
    });
    for(const warning of data.document.warnings)results.append(make('p',warning,'import-status'));
    if(data.document.skipped.length){const detail=make('details');detail.append(make('summary',`${data.document.skipped.length} 项未迁入`));for(const item of data.document.skipped)detail.append(make('p',`#${item.key} · ${item.kind}：${item.reason}`));results.append(detail);}
    if(!data.applied){
      mappings.hidden=false;const options=$('#import-account-options');options.replaceChildren();data.targets.forEach(u=>{const o=make('option');o.value=String(u.id);o.label=u.name;options.append(o);});
      const authors=$('[data-import-authors]');authors.replaceChildren();
      data.document.authors.forEach(author=>{const label=make('label',`作者 ${author||'（未署名）'} → 本站账号编号`),field=make('input');field.type='number';field.min='1';field.setAttribute('list','import-account-options');field.dataset.author=author;field.value=String(shownInput.authors[author]||data.default_author);label.append(field);authors.append(label);});
      const media=$('[data-import-media]');media.replaceChildren();const sources=new Set(data.document.items.flatMap(i=>i.images));for(const source of Object.keys(shownInput.media))sources.add(source);
      for(const source of sources){const label=make('label','图片 → 本站媒体路径'),field=make('input');label.append(make('small',source||'未识别的图片地址'));field.dataset.media=source;field.type='text';field.maxLength=2048;field.placeholder='/media/编号/文件名';field.value=shownInput.media[source]||'';label.append(field);media.append(label);}
    }
    apply.hidden=data.applied;download.textContent=data.applied?'下载结果报告':'下载核对报告';apply.textContent=data.plan.new?`确认迁入 ${data.plan.new} 篇`:'确认并生成结果报告';
    message(data.applied?'迁入已完成。请保存结果报告，并核对需要人工处理的项目。':data.plan.conflicts?'有地址冲突，本批尚未写入。修正源文件或目标地址后重新预览。':'预览已生成，尚未写入。核对正文和映射后确认迁入。',data.applied?'success':data.plan.conflicts?'error':'');
  }
  form.addEventListener('submit',async event=>{
    event.preventDefault();if(session.locked)return;
    const generation=session.begin(false);if(generation===undefined)return;
    const draft=draftGeneration;lock(true);message('正在读取文件并生成预览…');
    try{
      const files=Array.from(field('files').files||[]);
      if(!files.length||files.length>100||files.reduce((n,f)=>n+f.size,0)>8*1024*1024)throw new Error('请选择 1–100 个文件，总大小不超过 8 MiB。');
      const authors:Record<string,number>=Object.create(null),media:Record<string,string>=Object.create(null);
      mappings.querySelectorAll<HTMLInputElement>('[data-author]').forEach(n=>{if(n.value)authors[n.dataset.author!]=Number(n.value);});
      mappings.querySelectorAll<HTMLInputElement>('[data-media]').forEach(n=>{if(n.value.trim())media[n.dataset.media!]=n.value.trim();});
      const source=field('source').value.trim(),format=field<HTMLSelectElement>('format').value,state=field<HTMLSelectElement>('state').value;
      if(format!=='markdown'&&format!=='wordpress'||state!=='draft'&&state!=='preserve')throw new Error('请选择有效的来源格式和内容状态。');
      const payload:ImportInput['files']=[];
      for(const file of files){const text=new TextDecoder('utf-8',{fatal:true}).decode(await file.arrayBuffer());if(!session.current(generation)||draft!==draftGeneration)return;payload.push({name:file.name,text});}
      const sent:ImportInput={source,format,state,files:payload,authors,media};validateImportInput(sent);
      const data=importReport(await session.request('/preview','POST',{input:sent},45000),sent,session.identity);
      if(!session.current(generation)||draft!==draftGeneration)return;
      input=sent;preview=data;pending=null;show(data);
    }catch(error){if(session.current(generation)){token='';message(session.fail(error,false),'error');}}
    finally{if(session.current(generation)){session.finish(generation);lock(false);if(draft!==draftGeneration)message('输入已变化，请重新生成预览。');}}
  });
  apply.addEventListener('click',async()=>{
    if(session.busy||!pending&&(!input||!preview||dirty||!token))return;
    const generation=session.begin(true,true);if(generation===undefined)return;
    if(!pending)pending={input:structuredClone(input!),preview:preview!,token};
    const sent=pending.input,prior=pending.preview,sentToken=pending.token,draft=draftGeneration;
    lock(true);message(session.uncertain?'正在核对原批次结果…':'正在保存本批内容…');
    try{
      if(Date.parse(prior.expires)<=Date.now()){
        const checked=importReport(await session.request('/preview','POST',{input:sent},45000),sent,session.identity);
        if(!session.current(generation))return;
        session.uncertain=false;pending=null;input=sent;preview=checked;show(checked);message('原批次已重新预览，请核对后再次确认。');return;
      }
      const data=importReport(await session.request('/apply','POST',{input:sent,token:sentToken},45000),sent,session.identity,prior);
      if(!session.current(generation))return;
      session.uncertain=false;pending=null;input=sent;show(data);preview=null;
      if(draft!==draftGeneration){dirty=true;message('原批次已迁入；期间的新输入需要重新预览。','success');}
    }catch(error){if(session.current(generation)){
      if(error instanceof TransferError&&error.code==='import.preview_required'){pending={input:sent,preview:{...prior,expires:new Date(0).toISOString()},token:sentToken};session.uncertain=true;message('原预览已失效，请核对并重新预览原批次。','error');}
      else {message(session.fail(error,true),'error');if(!session.uncertain)pending=null;}
    }}finally{if(session.current(generation)){session.finish(generation);lock(false);}}
  });
  download.addEventListener('click',()=>{if(!report||session.lost||!session.active)return;const data={schema:1,kind:'newpress.content-import-report',applied:report.applied,generated_at:new Date().toISOString(),plan:report.plan,warnings:report.document.warnings,skipped:report.document.skipped,items:report.document.items.map(({key,author,source_url,original_status,warnings,images})=>({key,author,source_url,original_status,warnings,images}))};const blob=new Blob([JSON.stringify(data,null,2)],{type:'application/json'}),url=URL.createObjectURL(blob),a=make('a');a.href=url;a.download=`newpress-import-${report.plan.source}.json`;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);});
  lock(false);
})();
