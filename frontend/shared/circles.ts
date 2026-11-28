import {required,field,pageRequests,failure} from './collection-runtime';
import {record,circleReceipt,circleLatest,circleUpload} from './collection-data';
import type {CircleCommand,CircleFile} from './collection-data';
import {composeSnapshot,composeSaved} from './community-draft';
(() => {
  'use strict';
  const page=document.querySelector<HTMLElement>('[data-circle-page]');if(!page)return;
  const root=page;
  const status=required(root,'[data-circle-status]'),conflict=required(root,'[data-circle-conflict]');
  const owner=Number(root.dataset.userId);
  const requests=pageRequests(owner,()=>{status.hidden=false;status.textContent='登录状态已变化，请重新打开页面。';root.querySelectorAll<HTMLButtonElement|HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement>('button,input,select,textarea').forEach(node=>node.disabled=true);});
  let busy=false,pending: {version:number;csrf:string;node?:HTMLElement;retry?:()=>Promise<void>} | null=null;
  const attempts=new WeakMap<HTMLElement,{signature:string;key:string;version?:string;file?:File}>();
  const revealSection=()=>{const id=location.hash.slice(1);if(!id)return;const section=document.getElementById(id);if(section&&root.contains(section)&&section.tagName==='DETAILS')(section as HTMLDetailsElement).open=true;};
  revealSection();window.addEventListener('hashchange',revealSection);
  document.addEventListener('chunnel:session',event=>{
    const id=event.detail?.authenticated?Number(event.detail.id):0;
    if(id!==Number(root.dataset.userId))location.reload();
  });
  const endpoint=(slug: string)=>'/api/v1/community/circles/'+encodeURIComponent(slug);
  const parse=async(value:unknown)=>record(value);
  const notify=(message: string,node: HTMLElement)=>{
    const local=node.closest('form')?.querySelector<HTMLElement>('[data-form-status]');
    status.textContent=message;status.hidden=!!local;
    // A second page-top banner shifts the form and can move a retry button
    // between pointerdown and pointerup while WebKit applies scroll anchoring.
    if(local){local.hidden=false;local.textContent=message;}
  };
  const inputFor=(node: HTMLElement)=>{
    const form=node instanceof HTMLFormElement?node:null;
    const action=form?.dataset.circleCommand||node.dataset.circleAction||'';
    const cmd: CircleCommand={action,version:action==='create'?0:Number(root.dataset.version)};
    if(form)for(const [key,value] of new FormData(form)){if(key==='slug'||value instanceof File)continue;cmd[key]=key==='owner_id'?Number(value):['join_reason','enabled'].includes(key)?value==='true':String(value).trim();}
    if(form?.querySelector('[data-circle-editor]'))cmd.attachments=selectedAttachments(form);
    for(const [attribute,field] of [['topic','topic_id'],['reply','reply_id'],['user','user_id'],['report','report_id']] as const)if(node.dataset[attribute]!==undefined)cmd[field]=Number(node.dataset[attribute]);
    if(node.dataset.attachment!==undefined)cmd.attachment_id=node.dataset.attachment;
    if(node.dataset.enabled!==undefined)cmd.enabled=node.dataset.enabled==='true';
    const slug=action==='create'?form ? field(form,'slug').value.trim() : '' : root.dataset.slug || '';
    return {slug,cmd};
  };
  const execute=async(node: HTMLElement)=>{
    if(busy||requests.lost||node.matches(':disabled'))return;
    if(node.dataset.confirm&&!window.confirm(node.dataset.confirm))return;
    const {slug,cmd}=inputFor(node);
    const draft=node instanceof HTMLFormElement?composeSnapshot(node):undefined;
    if(cmd.action==='settings'&&cmd.visibility==='public'&&!window.confirm('公开阅读会让所有人看到圈内话题和回复，确认保存这些设置？'))return;
    const signature=JSON.stringify({slug,cmd});let attempt=attempts.get(node);
    if(!attempt||attempt.signature!==signature){attempt={signature,key:crypto.randomUUID()};attempts.set(node,attempt);}cmd.key=attempt.key;
    busy=true;root.setAttribute('aria-busy','true');const controls=[...root.querySelectorAll<HTMLButtonElement|HTMLInputElement|HTMLTextAreaElement|HTMLSelectElement>('button,input,textarea,select')].map(control=>({control,disabled:control.disabled}));controls.forEach(({control})=>control.disabled=true);notify('正在保存…',node);conflict.hidden=true;pending=null;
    try{
      const response=await requests.request(endpoint(slug),{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':root.dataset.csrf || ''},body:JSON.stringify(cmd)});
      const result=circleReceipt(response,slug,cmd);
      attempts.delete(node);if(node instanceof HTMLFormElement&&draft)composeSaved(node,owner,draft);notify('已保存，正在打开最新内容…',node);
      const workspace=root.hasAttribute('data-circle-workspace');
      let target=(workspace?'/admin/circles/':'/community/')+encodeURIComponent(slug)+'/';
      if(result.topic_id){target+='topics/'+result.topic_id;if(result.reply_id)target+='?page='+Math.max(1,Number(result.reply_page)||1)+'#reply-'+result.reply_id;}
      else if(['report','review_report','clear_reports'].includes(cmd.action))target+='#circle-reports';
      else if(workspace&&['member','remove_member','transfer'].includes(cmd.action))target+='#circle-members';
      else if(!workspace&&cmd.action!=='create'&&cmd.action!=='delete_topic')target=location.pathname+location.search+(['member','remove_member','transfer'].includes(cmd.action)?'#circle-members':'');
      if(target.split('#')[0]===location.pathname+location.search){history.replaceState(null,'',target);location.reload();}else location.assign(target);
    }catch(problem){const error=failure(problem);if(requests.lost)return;
      notify(error.name==='TimeoutError'?'响应超时。输入已保留，原样重试会识别同一请求。':error.message,node);
      if(error.status===409&&cmd.action!=='create'){
        try{const latest=circleLatest(await requests.request(endpoint(slug)),slug,owner);pending={node,version:latest.circle.version,csrf:latest.csrf};required(conflict,'[data-circle-latest]').textContent=`${latest.circle.name} · 版本 ${latest.circle.version}\n${latest.circle.visibility_label} · ${latest.circle.join_label} · 你的身份：${latest.circle.role_label}\n介绍：${latest.circle.description||'未填写'}\n规则：${latest.circle.rules||'未填写'}\n当前输入仍在表单中。确认以上状态后，可使用新版本重试。`;conflict.hidden=false;conflict.scrollIntoView({block:'center',behavior:'smooth'});}catch{if(!requests.lost)notify('最新状态暂时无法读取，输入已保留，请稍后重试。',node);}
      }
    }finally{busy=false;root.removeAttribute('aria-busy');if(!requests.lost)controls.forEach(({control,disabled})=>control.disabled=disabled);}
  };
  root.addEventListener('submit',event=>{const form=event.target instanceof Element?event.target.closest<HTMLFormElement>('[data-circle-command]'):null;if(!form)return;event.preventDefault();if(form.reportValidity())execute(form);});
  root.addEventListener('click',event=>{
    if(!(event.target instanceof Element))return;
    const memberTrigger=event.target.closest<HTMLElement>('[data-circle-members-open],[data-settings-target="circle-members"]');
    if(memberTrigger){membersOpener=memberTrigger;queueMicrotask(()=>openMembers());return;}
    if(event.target.closest('[data-circle-members-close]')){if(typeof membersDialog?.close==='function')membersDialog.close();else{membersDialog?.removeAttribute('open');membersOpener?.focus();}return;}
    const button=event.target.closest<HTMLElement>('[data-circle-action]');if(button)execute(button);
  });
  root.querySelector('[data-circle-retry]')?.addEventListener('click',()=>{if(!pending||busy||requests.lost)return;root.dataset.version=String(pending.version);root.dataset.csrf=pending.csrf;const next=pending;pending=null;if(next.retry)next.retry();else if(next.node)execute(next.node);});
  const membersDialog=root.querySelector<HTMLDialogElement>('[data-circle-members-dialog]');
  let membersOpener=root.querySelector<HTMLElement>('[data-circle-members-open]');
  const openMembers=(updateHash=true)=>{
    if(!membersDialog)return;
    if(updateHash&&location.hash!=='#circle-members')history.replaceState(history.state,'',location.pathname+location.search+'#circle-members');
    if(!membersDialog.open){if(typeof membersDialog.showModal==='function'){membersDialog.showModal();document.body.classList.add('circle-members-open');queueMicrotask(()=>root.querySelector<HTMLInputElement>('[data-member-search]')?.focus());}else membersDialog.setAttribute('open','');}
  };
  if(membersDialog?.hasAttribute('open')&&typeof membersDialog.showModal==='function')membersDialog.removeAttribute('open');
  membersDialog?.addEventListener('close',()=>{
    document.body.classList.remove('circle-members-open');
    if(location.hash==='#circle-members')history.replaceState(history.state,'',location.pathname+location.search);
    membersOpener?.focus();
  });
  membersDialog?.addEventListener('click',event=>{if(event.target===membersDialog)membersDialog.close();});
  function openLinkedPanel(){
    if(location.hash==='#circle-members'){openMembers(false);return;}
    if(['#circle-settings','#circle-reports','#circle-history','#circle-files','#circle-archive'].includes(location.hash)){const panel=root.querySelector(location.hash);if(panel?.tagName==='DETAILS')(panel as HTMLDetailsElement).open=true;}
  }
  openLinkedPanel();window.addEventListener('hashchange',openLinkedPanel);

  const memberList=root.querySelector('[data-circle-member-list]');
  if(memberList){
    const rows=[...memberList.querySelectorAll<HTMLElement>('[data-circle-member-row]')];
    const filters=[...root.querySelectorAll<HTMLElement>('[data-member-filter]')];
    const search=root.querySelector<HTMLInputElement>('[data-member-search]');
    const empty=memberList.querySelector<HTMLElement>('.circle-empty');
    const noMatches=memberList.querySelector<HTMLElement>('[data-member-filter-empty]');
    let activeFilter='all';
    const updateMembers=()=>{
      const query=search?.value.trim().toLocaleLowerCase()||'';
      const matchesSearch=(row: HTMLElement)=>!query||(row.dataset.memberQuery||'').toLocaleLowerCase().includes(query);
      const counts=new Map<string | undefined,number>([['all',0]]);
      for(const row of rows)if(matchesSearch(row))counts.set(row.dataset.memberRole,(counts.get(row.dataset.memberRole)||0)+1);
      counts.set('all',rows.filter(matchesSearch).length);
      filters.forEach(button=>{
        const filter=button.dataset.memberFilter;
        const badge=button.querySelector('[data-member-filter-count]');
        if(badge)badge.textContent=String(counts.get(filter)||0);
        button.setAttribute('aria-pressed',String(filter===activeFilter));
      });
      let visible=0;
      rows.forEach(row=>{const show=matchesSearch(row)&&(activeFilter==='all'||row.dataset.memberRole===activeFilter);row.hidden=!show;if(show)visible++;});
      if(empty)empty.hidden=rows.length>0;
      if(noMatches)noMatches.hidden=rows.length===0||visible>0;
    };
    filters.forEach(button=>button.addEventListener('click',()=>{activeFilter=button.dataset.memberFilter || 'all';updateMembers();}));
    search?.addEventListener('input',updateMembers);
    updateMembers();
  }

  const editorIcon=(name: string)=>{
    const svg=document.createElementNS('http://www.w3.org/2000/svg','svg'),use=document.createElementNS('http://www.w3.org/2000/svg','use');
    svg.setAttribute('class','icon');svg.setAttribute('aria-hidden','true');svg.setAttribute('focusable','false');use.setAttribute('href','#icon-'+name);svg.append(use);return svg;
  };
  for(const size of root.querySelectorAll<HTMLElement>('[data-circle-file-size]')){
    const bytes=Number(size.dataset.circleFileSize);
    if(Number.isFinite(bytes)&&bytes>=0)size.textContent=bytes>=1048576?(bytes/1048576).toFixed(1)+' MB':bytes>=1024?Math.ceil(bytes/1024)+' KB':bytes+' B';
  }

  function selectedAttachments(form: HTMLFormElement){return [...form.querySelectorAll<HTMLInputElement>('[data-attachment-id]:checked')].map(input=>input.dataset.attachmentId || '');}
  function freezeEditor(){
    busy=true;root.setAttribute('aria-busy','true');
    const controls=[...root.querySelectorAll<HTMLButtonElement|HTMLInputElement|HTMLTextAreaElement|HTMLSelectElement>('button,input,textarea,select')].map(control=>({control,disabled:control.disabled}));
    controls.forEach(({control})=>control.disabled=true);
    return ()=>{busy=false;root.removeAttribute('aria-busy');if(!requests.lost)controls.forEach(({control,disabled})=>control.disabled=disabled);};
  }
  function appendAttachment(form: HTMLFormElement,file: CircleFile){
    const list=required(form,'[data-editor-attachments]');
    if([...list.querySelectorAll<HTMLInputElement>('[data-attachment-id]')].some(input=>input.dataset.attachmentId===file.id))return;
    const label=document.createElement('label'),check=document.createElement('input'),text=document.createElement('span');
    check.type='checkbox';check.checked=true;check.dataset.attachmentId=file.id;
    text.textContent=file.name+' · '+Math.ceil(file.size/1024)+' KiB';label.append(check,text);list.append(label);
    if(file.image&&field(form,'format').value==='markdown')field(form,'body').value+=`\n\n![图片](attachment:${file.id})`;
  }
  async function uploadAttachment(form: HTMLFormElement,input: HTMLInputElement,retrying=false){
    const file=input.files?.[0];if(!file||busy||requests.lost)return;
    if(selectedAttachments(form).length>=8){notify('每条内容最多关联 8 个附件。',form);return;}
    if(file.size>10*1024*1024){notify('文件上限为 10 MiB。',form);return;}
    const signature=[file.name,file.size,file.lastModified].join(':');
    let attempt=attempts.get(input);
    if(retrying||!attempt||attempt.file!==file){attempt={signature,key:crypto.randomUUID(),version:root.dataset.version,file};attempts.set(input,attempt);}
    const body=new FormData();body.set('file',file);body.set('key',attempt.key);body.set('version',attempt.version || '');
    const unfreeze=freezeEditor();notify('正在上传附件…',form);conflict.hidden=true;pending=null;
    try{
      const result=circleUpload(await requests.request(endpoint(root.dataset.slug || '')+'/attachments',{method:'POST',headers:{'X-CSRF-Token':root.dataset.csrf || ''},body},30000),root.dataset.slug || '',Number(attempt.version));
      root.dataset.version=String(result.version);appendAttachment(form,result.attachment);attempts.delete(input);input.value='';notify('附件已上传，发布内容后会随正文展示。',form);
    }catch(problem){const error=failure(problem);if(requests.lost)return;
      notify(error.name==='TimeoutError'?'上传响应超时，请点击上传重试，已选择的文件仍保留。':error.message,form);
      if(error.status===409){try{
        const latest=circleLatest(await requests.request(endpoint(root.dataset.slug || '')),root.dataset.slug || '',owner);
        pending={version:latest.circle.version,csrf:latest.csrf,retry:()=>uploadAttachment(form,input,true)};
        required(conflict,'[data-circle-latest]').textContent=`${latest.circle.name} · 版本 ${latest.circle.version}\n${latest.circle.visibility_label} · ${latest.circle.join_label} · ${latest.circle.role_label}\n当前输入与所选文件已保留。核对后使用新版本上传。`;
        conflict.hidden=false;conflict.scrollIntoView({block:'center',behavior:'smooth'});
      }catch{if(!requests.lost)notify('最新状态暂时无法读取，请稍后重试。',form);}}
    }finally{unfreeze();}
  }
  for(const form of root.querySelectorAll<HTMLFormElement>('[data-circle-command="topic"],[data-circle-command="reply"],[data-circle-command="edit_topic"],[data-circle-command="edit_reply"]')){
    const toolbar=document.createElement('div');toolbar.className='circle-editor-tools';toolbar.dataset.circleEditor='';
    const formatLabel=document.createElement('label');formatLabel.textContent='正文格式';
    const format=document.createElement('select');format.name='format';
    for(const [value,label]of [['markdown','Markdown 图文'],['plain','纯文本']] as const){const option=document.createElement('option');option.value=value;option.textContent=label;format.append(option);}
    format.value=form.dataset.circleCommand?.startsWith('edit_')?(form.dataset.format||'plain'):'markdown';formatLabel.append(format);
    const uploadLabel=document.createElement('label');uploadLabel.className='circle-upload-field';uploadLabel.textContent='图片或附件（10 MiB 内）';
    const input=document.createElement('input');input.type='file';input.accept='.jpg,.jpeg,.png,.gif,.webp,.pdf,.txt,.zip';uploadLabel.append(input);
    const uploadButton=document.createElement('button');uploadButton.type='button';uploadButton.append(editorIcon('paperclip'),document.createTextNode('上传附件'));uploadButton.addEventListener('click',()=>uploadAttachment(form,input));
    const previewButton=document.createElement('button');previewButton.type='button';previewButton.append(editorIcon('eye'),document.createTextNode('预览正文'));
    const preview=document.createElement('div');preview.className='circle-rich-body circle-preview';preview.setAttribute('role','region');preview.setAttribute('aria-label','正文预览');preview.hidden=true;
    previewButton.addEventListener('click',async()=>{
      if(busy||requests.lost)return;const body={body:field(form,'body').value,format:format.value,attachments:selectedAttachments(form)};const unfreeze=freezeEditor();
      try{const result=await parse(await requests.request(endpoint(root.dataset.slug || '')+'/preview',{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':root.dataset.csrf || ''},body:JSON.stringify(body)}));if(typeof result.html!=='string')throw new Error('预览暂时不可用');preview.innerHTML=result.html;preview.hidden=false;}catch(problem){const error=failure(problem);if(requests.lost)return;notify(error.message,form);}finally{unfreeze();}
    });
    toolbar.append(formatLabel,uploadLabel,uploadButton,previewButton);required(form,'[name=body]').closest('label')!.before(toolbar);
    let attachments=form.querySelector<HTMLElement>('[data-editor-attachments]');if(!attachments){attachments=document.createElement('div');attachments.dataset.editorAttachments='';attachments.className='circle-editor-attachments';toolbar.after(attachments);}
    const help=document.createElement('p');help.className='circle-help';help.textContent='支持标题、列表、引用、代码和链接。图片须先上传；取消勾选的文件不会附在正文后。';toolbar.after(help);attachments.after(preview);
  }
})();
