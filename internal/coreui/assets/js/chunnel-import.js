(() => {
  'use strict';
  const root=document.querySelector('[data-content-import]');if(!root)return;
  const form=root.querySelector('[data-import-form]'),status=root.querySelector('[data-import-status]');
  const results=root.querySelector('[data-import-results]'),summary=root.querySelector('[data-import-summary]'),actions=root.querySelector('[data-import-actions]');
  const apply=root.querySelector('[data-import-apply]'),download=root.querySelector('[data-import-download]'),mappings=root.querySelector('[data-import-mappings]');
  const readonly=root.dataset.readonly==='true';let input=null,report=null,token='',busy=false,dirty=false;
  const make=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n;};
  const message=(text,tone='')=>{status.textContent=text;status.className='import-status'+(tone?' is-'+tone:'');};
  function invalidate(){if(busy)return;token='';dirty=true;apply.disabled=true;if(report)message('输入或映射已修改，请重新生成预览。');}
  form.addEventListener('input',invalidate);mappings.addEventListener('input',invalidate);
  form.elements.format.addEventListener('change',()=>{const wp=form.elements.format.value==='wordpress';form.elements.files.accept=wp?'.xml':'.md,.markdown';form.elements.files.multiple=!wp;form.elements.files.value='';root.querySelector('[data-import-files]').textContent=wp?'选择源站导出的 WXR 1.2 XML 文件。':'支持 Markdown 常用格式及 YAML 元信息。';mappings.hidden=true;input=null;report=null;invalidate();});
  form.elements.files.addEventListener('change',()=>{input=null;mappings.hidden=true;root.querySelector('[data-import-authors]').replaceChildren();root.querySelector('[data-import-media]').replaceChildren();});
  function lock(value){busy=value;form.querySelector('fieldset').disabled=value||readonly;mappings.querySelectorAll('input').forEach(n=>n.disabled=value);apply.disabled=value||!token||readonly;download.disabled=value;root.setAttribute('aria-busy',String(value));}
  async function request(endpoint,body){const controller=new AbortController(),timeout=setTimeout(()=>controller.abort(),45000);try{const r=await fetch(endpoint,{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':root.dataset.csrf},credentials:'same-origin',cache:'no-store',body:JSON.stringify(body),signal:controller.signal});let data;try{data=await r.json();}catch(error){if(controller.signal.aborted)throw error;throw new Error('响应无法读取，请保留文件后重试。');}if(!r.ok){const fields=data.error?.details?.fields;throw new Error(data.error?.message+(fields?'：'+Object.values(fields).join('；'):''));}return data;}finally{clearTimeout(timeout);}}
  function show(data){
    report=data;token=data.token||'';dirty=false;summary.replaceChildren();results.replaceChildren();actions.hidden=false;
    for(const [label,count] of [['新建',data.plan.new],['保留已有',data.plan.existing],['冲突',data.plan.conflicts],['跳过类型',data.document.skipped.length]])summary.append(make('span',`${label} ${count}`));
    const labels={create:'待迁入',created:'已迁入',skip:'已存在',changed:'来源有变化',review:'需人工核对',conflict:'待解决'};
    data.plan.items.forEach((row,index)=>{
      const item=data.document.items[index],card=make('article',undefined,'import-item');card.dataset.action=row.action;
      const head=make('header'),title=make('div');title.append(make('h3',row.title),make('code',row.key));head.append(title,make('span',labels[row.action]||row.action,'import-state'));card.append(head);
      const states={draft:'草稿',pending:'待审核',publish:'已发布',future:'定时发布',private:'私密',trash:'回收站'};
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
      mappings.hidden=false;const options=root.querySelector('#import-account-options');options.replaceChildren();data.targets.forEach(u=>{const o=make('option');o.value=String(u.id);o.label=u.name;options.append(o);});
      const authors=root.querySelector('[data-import-authors]');authors.replaceChildren();
      data.document.authors.forEach(author=>{const label=make('label',`作者 ${author||'（未署名）'} → 本站账号编号`),field=make('input');field.type='number';field.min='1';field.setAttribute('list','import-account-options');field.dataset.author=author;field.value=String(input.authors[author]||data.default_author);label.append(field);authors.append(label);});
      const media=root.querySelector('[data-import-media]');media.replaceChildren();const sources=new Set(data.document.items.flatMap(i=>i.images));for(const source of Object.keys(input.media))sources.add(source);
      for(const source of sources){const label=make('label','图片 → 本站媒体路径'),field=make('input');label.append(make('small',source||'未识别的图片地址'));field.dataset.media=source;field.type='text';field.maxLength=2048;field.placeholder='/media/编号/文件名';field.value=input.media[source]||'';label.append(field);media.append(label);}
    }
    apply.hidden=data.applied;download.textContent=data.applied?'下载结果报告':'下载核对报告';apply.textContent=data.plan.new?`确认迁入 ${data.plan.new} 篇`:'确认并生成结果报告';
    message(data.applied?'迁入已完成。请保存结果报告，并核对需要人工处理的项目。':data.plan.conflicts?'有地址冲突，本批尚未写入。修正源文件或目标地址后重新预览。':'预览已生成，尚未写入。核对正文和映射后确认迁入。',data.applied?'success':data.plan.conflicts?'error':'');
  }
  form.addEventListener('submit',async event=>{
    event.preventDefault();if(busy||readonly)return;lock(true);message('正在读取文件并生成预览…');
    try{
      const files=Array.from(form.elements.files.files);if(!files.length||files.length>100||files.reduce((n,f)=>n+f.size,0)>8*1024*1024)throw new Error('请选择 1–100 个文件，总大小不超过 8 MiB。');
      const authors=Object.create(null),media=Object.create(null);mappings.querySelectorAll('[data-author]').forEach(n=>{if(n.value)authors[n.dataset.author]=Number(n.value);});mappings.querySelectorAll('[data-media]').forEach(n=>{if(n.value.trim())media[n.dataset.media]=n.value.trim();});
      const payload=[];for(const file of files){const text=new TextDecoder('utf-8',{fatal:true}).decode(await file.arrayBuffer());payload.push({name:file.name,text});}
      input={source:form.elements.source.value.trim(),format:form.elements.format.value,state:form.elements.state.value,files:payload,authors,media};
      show(await request('/api/v1/admin/imports/preview',{input}));
    }catch(error){token='';message(error.name==='AbortError'?'预览超时，文件仍保留，可重新生成。':error.message,'error');}finally{lock(false);}
  });
  apply.addEventListener('click',async()=>{if(busy||!token||dirty||readonly)return;lock(true);message('正在保存本批内容…');try{show(await request('/api/v1/admin/imports/apply',{input,token}));}catch(error){message(error.name==='AbortError'?'未收到完整结果。可使用相同文件重试，已迁入内容会被识别。':error.message,'error');}finally{lock(false);}});
  download.addEventListener('click',()=>{if(!report)return;const data={schema:1,kind:'newpress.content-import-report',applied:report.applied,generated_at:new Date().toISOString(),plan:report.plan,warnings:report.document.warnings,skipped:report.document.skipped,items:report.document.items.map(({key,author,source_url,original_status,warnings,images})=>({key,author,source_url,original_status,warnings,images}))};const blob=new Blob([JSON.stringify(data,null,2)],{type:'application/json'}),url=URL.createObjectURL(blob),a=make('a');a.href=url;a.download=`newpress-import-${report.plan.source}.json`;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);});
})();
