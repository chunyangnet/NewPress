import type {} from '../shared/account-types';
import { required, control, input as namedInput, values } from './ledger-admin-dom';
import type { Control } from './ledger-admin-dom';
import { failure, node } from '../shared/ledger-data';
import { adminAccountRuntime } from './admin-account-runtime';
import { choice } from '../shared/admin-config-data';
import { statusOf, statuses, parseImport, maxUses, exportCSV, formatPreview } from '../shared/registration-code-helpers';
import { codeInventory, codeReceipt, policyReceipt, ruleKeys } from '../shared/registration-admin-data';
import type { RegistrationCode, CodeTarget, CodePolicy, CodeMutation, CodeRules } from '../shared/registration-admin-data';
(() => {
  const host=document.querySelector<HTMLElement>('[data-registration-codes]');if(!host)return;const root=host;
  const $=<T extends HTMLElement = HTMLElement>(name: string): T=>required<T>(root, `[data-rc-${name}]`),$$=<T extends HTMLElement = HTMLElement>(selector: string)=>root.querySelectorAll<T>(selector);
  const el=<K extends keyof HTMLElementTagNameMap>(tag: K,text='',cls='')=>node(tag,cls,text);
  const createForm=$<HTMLFormElement>('create-form'),editForm=$<HTMLFormElement>('edit-form'),policyForm=$<HTMLFormElement>('policy-form');
  const readonly=root.dataset.readOnly==='true',pageSize=50;
  let items: RegistrationCode[]=[],selected=new Set<string>(),policy: CodePolicy | null=null,page=1,busy=false,loaded=false,createMode: 'generate' | 'import'='generate',targets: CodeTarget[]=[],clockOffset=0, pending=false, checkedPending=false; let editOriginal: RegistrationCode | undefined; let policyEditing=0;
  const now=()=>Date.now()+clockOffset;
  const filtered=()=>{const search=$<HTMLInputElement>('search').value.trim().toLowerCase(),status=$<HTMLSelectElement>('filter').value;return items.filter(c=>(!search||(c.code+' '+c.note).toLowerCase().includes(search))&&(!status||statusOf(c,now())===status));};
  const picked=()=>items.filter(c=>selected.has(c.id));
  const exportItems=()=>selected.size?picked():filtered();
  const targetList=(list: RegistrationCode[])=>list.map(c=>({id:c.id,version:c.version}));
  const notice=(message: string,error=false)=>{const n=$('notice');n.textContent=message;n.hidden=!message;n.classList.toggle('is-error',error);};
  const formError=(name: string,message='')=>{const n=$(name+'-error');n.textContent=message;n.hidden=!message;};
  const fmt=(value?: string | null)=>value?new Date(value).toLocaleString('zh-CN',{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false}):'—';
  const localTime=(value?: string | null)=>{if(!value)return '';const d=new Date(value);return new Date(d.getTime()-d.getTimezoneOffset()*60000).toISOString().slice(0,16);};
  const utcTime=(value: string)=>{if(!value)return '';const d=new Date(value);if(!Number.isFinite(d.getTime()))throw new Error('请选择有效的日期和时间');return d.toISOString();};
  const pendingKey = 'newpress.registration.admin.pending.' + (root.dataset.userId || '');
  const runtime = adminAccountRuntime(root, root.dataset.userId || '', root.dataset.csrf || '', readonly, () => {
    items=[]; selected.clear(); loaded=false; $('rows').replaceChildren(); $<HTMLTextAreaElement>('copy-text').value='';
    controls(); notice('登录状态或页面已变化，请重新载入后继续。', true);
  });
  const request=(body?: unknown,path='')=>runtime.request('/api/v1/admin/registration-codes'+path,body,20000);
  const review=el('button','已核对，继续操作','admin-button admin-button-quiet'); review.type='button'; review.hidden=true; $('notice').after(review);
  const pendingNotice=()=>{review.hidden=!pending; review.disabled=!checkedPending || !runtime.alive() || busy; if(pending)notice('上一项操作尚未确认。请刷新码库，核对实际结果后再继续；不会自动重新生成或提交。',true);};
  function keepPending(action: string){sessionStorage.setItem(pendingKey,JSON.stringify({action,time:Date.now()}));pending=true;checkedPending=false;}
  function clearPending(){sessionStorage.removeItem(pendingKey);pending=false;checkedPending=false;review.hidden=true;}
  try{pending=sessionStorage.getItem(pendingKey)!==null;}catch{pending=true;}
  review.addEventListener('click',()=>{if(!pending||!checkedPending||busy||!runtime.alive())return;try{clearPending();notice('已结束结果核对。请按实际码库状态决定后续操作。');controls();}catch{notice('浏览器无法更新操作记录，请检查存储设置。',true);}});
  function controls() {
    const blocked=readonly||!runtime.alive()||busy||!loaded||pending;
    $$<HTMLButtonElement>('button').forEach(button=>{button.disabled=blocked;});
    $$<HTMLButtonElement>('[data-rc-close]').forEach(b=>{b.disabled=busy;});
    $<HTMLButtonElement>('refresh').disabled=readonly||!runtime.alive()||busy;
    for(const key of ['edit','clear','delete'])$<HTMLButtonElement>(key).disabled=blocked||!selected.size;
    $$<HTMLButtonElement>('[data-rc-toggle]').forEach(b=>{b.disabled=blocked||!selected.size;});
    const readBlocked=readonly||!runtime.alive()||busy||!loaded;
    for(const key of ['copy','export'])$<HTMLButtonElement>(key).disabled=readBlocked||!exportItems().length;
    const count=filtered().length,pages=Math.max(1,Math.ceil(count/pageSize));
    $<HTMLButtonElement>('prev').disabled=readBlocked||page<=1;$<HTMLButtonElement>('next').disabled=readBlocked||page>=pages;
    $<HTMLButtonElement>('select-filtered').disabled=readBlocked||!count;
    $$<HTMLInputElement>('[data-rc-rows] input,[data-rc-select-page]').forEach(n=>{n.disabled=readBlocked;});
    const inputBlocked=readonly||!runtime.alive()||!loaded;
    for(const form of [createForm,editForm,policyForm])form.querySelectorAll<Control>('input,select,textarea').forEach(n=>{n.disabled=inputBlocked;});
    for(const key of ['count','prefix','length','charset','group'])control(createForm,key).disabled=inputBlocked||createMode==='import';
    control(createForm,'codes').disabled=inputBlocked||createMode!=='import';
    control(createForm,'import_format').disabled=inputBlocked||createMode!=='import';
    for(const key of ['max_uses','enabled','starts_at','expires_at','note'])control(editForm,key).disabled=inputBlocked||!namedInput(editForm,'apply_'+key).checked;
    review.hidden=!pending;review.disabled=!checkedPending||!runtime.alive()||busy;root.setAttribute('aria-busy',String(busy));
  }
  function render() {
    const counts={total:items.length,active:0,used:0,finished:0};
    items.forEach(c=>{const s=statusOf(c,now());if(s==='active')counts.active++;if(s==='expired'||s==='exhausted')counts.finished++;counts.used+=c.used;});
    Object.entries(counts).forEach(([k,v])=>{required(root,`[data-rc-stat="${k}"]`).textContent=v.toLocaleString('zh-CN');});
    const matches=filtered(),pages=Math.max(1,Math.ceil(matches.length/pageSize));page=Math.min(page,pages);
    const visible=matches.slice((page-1)*pageSize,page*pageSize),body=$('rows');body.replaceChildren();
    visible.forEach(c=>{
      const row=el('tr');row.classList.toggle('is-selected',selected.has(c.id));
      const check=el('input');check.type='checkbox';check.checked=selected.has(c.id);check.setAttribute('aria-label','选择 '+c.code);
      check.addEventListener('change',()=>{if(check.checked){if(selected.size>=500){notice('每批最多选择 500 个注册码',true);check.checked=false;return;}selected.add(c.id);}else selected.delete(c.id);render();});
      const checkCell=el('td',undefined,'rc-check');checkCell.append(check);row.append(checkCell);
      const code=el('td');code.append(el('code',c.code));if(c.note)code.append(el('small',c.note));row.append(code);
      const status=el('td'),badge=el('span',statuses[statusOf(c,now())],'rc-badge');badge.dataset.status=statusOf(c,now());status.append(badge);row.append(status);
      row.append(el('td',`${c.used} / ${c.max_uses||'不限'}`,'rc-numeric'));
      const times=el('td');times.append(el('time',c.starts_at?'起 '+fmt(c.starts_at):'即刻生效'),el('time',c.expires_at?'至 '+fmt(c.expires_at):'永不过期'));row.append(times);
      row.append(el('td',fmt(c.last_used_at),'rc-numeric'));
      const actions=el('td'),copy=el('button','复制','rc-text-button'),edit=el('button','设置','rc-text-button');copy.type=edit.type='button';copy.addEventListener('click',()=>copyCodes([c]));edit.addEventListener('click',()=>openEdit([c]));actions.append(copy,edit);row.append(actions);body.append(row);
    });
    $('empty').hidden=!!matches.length;
    required($('empty'),'h3').textContent=items.length?'没有匹配的注册码':'还没有注册码';
    required($('empty'),'p').textContent=items.length?'试试其他关键词或状态。':'批量生成或导入自定义注册码，集中管理注册名额。';
    $('total').textContent=`${matches.length} / ${items.length}`;
    $('selection').textContent=selected.size?`已选择 ${selected.size} 个`:'未选择';
    $('export-scope').textContent=selected.size?`所选 ${selected.size} 个`:`筛选结果 ${matches.length} 个`;
    $('page-label').textContent=`第 ${page} / ${pages} 页 · 每页 50 个 · 每批最多 500 个`;
    const checked=visible.filter(c=>selected.has(c.id)).length;$<HTMLInputElement>('select-page').checked=visible.length>0&&checked===visible.length;$<HTMLInputElement>('select-page').indeterminate=checked>0&&checked<visible.length;
    controls();
  }
  function renderPolicy() { if(!policy)return;
    control(policyForm,'mode').value=policy.mode;control(policyForm,'code_url').value=policy.code_url||'';
    const modes={hidden:'隐藏 · 无需注册码',optional:'选填 · 填写时校验',required:'必填 · 凭码注册'};
    $('policy-summary').textContent=`工作台码库 · ${modes[policy.mode]}`;
    $('registration-state').textContent={closed:'本站当前关闭注册，开放后接入规则才会生效。',invite:'本站采用邀请注册，新用户还需满足邀请要求。',open:'本站开放注册，按右侧所选规则校验注册码。'}[policy.registration]||'';
  }
  async function load() {
    if(busy||readonly||!runtime.alive())return;
    if(policy&&(control(policyForm,'mode').value!==policy.mode||control(policyForm,'code_url').value!==policy.code_url)&&!window.confirm('刷新将重新读取注册接入设置，放弃尚未保存的接入修改，是否继续？'))return;
    const serial=policyEditing;busy=true;controls();
    try{const data=codeInventory(await request());items=data.items;if(serial===policyEditing){policy=data.policy;renderPolicy();}clockOffset=Date.parse(data.now)-Date.now();selected=new Set([...selected].filter(id=>items.some(c=>c.id===id)));loaded=true;checkedPending=pending;notice('');pendingNotice();}
    catch(error){if(runtime.alive()){notice(failure(error).message,true);required($('empty'),'h3').textContent='读取失败';}}
    finally{busy=false;if(loaded)render();else controls();}
  }
  function updateInventory(action: CodeMutation['action'],result: {items: RegistrationCode[];count:number}) {
    const affected=new Map(result.items.map(c=>[c.id,c]));
    if(action==='delete'){items=items.filter(c=>!affected.has(c.id));selected.clear();}
    else{items=items.map(c=>affected.get(c.id)||c);const known=new Set(items.map(c=>c.id));items.unshift(...result.items.filter(c=>!known.has(c.id)));if(action==='create'||action==='generate'){selected=new Set(result.items.map(c=>c.id));$<HTMLInputElement>('search').value='';$<HTMLSelectElement>('filter').value='';page=1;}}
  }
  async function mutate(payload: CodeMutation,errorName: string | null,dialog: HTMLDialogElement | null,message: string | ((count:number)=>string)) {
    if(busy||readonly||pending||!loaded||!runtime.alive())return;busy=true;controls();if(errorName)formError(errorName);
    const form=dialog?.querySelector('form'),before=form?JSON.stringify(values(form)):'';
    try{keepPending(payload.action);const result=codeReceipt(await request(payload),payload,items);clearPending();updateInventory(payload.action,result);const changed=form&&before!==JSON.stringify(values(form));if(changed&&payload.action==='update'){targets=targetList(result.items);editOriginal=result.items.length===1?result.items[0]:undefined;}if(!changed)dialog?.close();notice((typeof message==='function'?message(result.count):message)+(changed?'；后续输入仍待保存。':''));}
    catch(caught){if(!runtime.alive())return;const error=failure(caught);if(error.status&&error.status<500)try{clearPending();}catch{}if(errorName)formError(errorName,failure(error).message);else notice(failure(error).message,true);pendingNotice();}
    finally{busy=false;render();}
  }
  function preview() {
    const g={prefix:control(createForm,'prefix').value,length:control(createForm,'length').value,charset:control(createForm,'charset').value,group:control(createForm,'group').value},value=formatPreview(g);$('preview').textContent=value;$('preview-length').textContent=`含前缀和分隔符共 ${value.length} 位 · 实际码由服务器随机生成`;
  }
  function importCount() {try{const codes=parseImport(control(createForm,'codes').value,control(createForm,'import_format').value);$('import-count').textContent=`已识别 ${codes.length} 个注册码`;}catch(error){$('import-count').textContent=failure(error).message;}}
  function openCreate(mode: 'generate' | 'import') {
    if(readonly||pending||busy||!runtime.alive())return;createMode=mode;formError('create');$('generator').hidden=mode!=='generate';$('import').hidden=mode!=='import';
    required($<HTMLDialogElement>('create-dialog'),'h2').textContent=mode==='generate'?'批量生成注册码':'导入自定义注册码';
    $('create-submit').textContent=mode==='generate'?'生成注册码':'校验并导入';$('create-subtitle').textContent='每批最多 500 个，保存后可直接复制或导出。';
    controls();preview();importCount();$<HTMLDialogElement>('create-dialog').showModal();
  }
  function openEdit(list: RegistrationCode[]) {
    if(readonly||pending||busy||!runtime.alive()||!list.length)return;editOriginal=list.length===1?list[0]:undefined;targets=targetList(list);editForm.reset();formError('edit');$('edit-count').textContent=`正在设置 ${list.length} 个注册码`;
    if(editOriginal){const c=editOriginal;for(const k of ['max_uses','enabled','note'] as const)control(editForm,k).value=String(c[k]);for(const k of ['starts_at','expires_at'] as const)control(editForm,k).value=localTime(c[k]);}
    controls();$<HTMLDialogElement>('edit-dialog').showModal();
  }
  async function copyCodes(list: RegistrationCode[]) {
    if(!list.length)return;const text=list.map(c=>c.code).join('\n');
    try{await navigator.clipboard.writeText(text);notice(`已复制 ${list.length} 个注册码`);}
    catch{$<HTMLTextAreaElement>('copy-text').value=text;$<HTMLDialogElement>('copy-dialog').showModal();$<HTMLTextAreaElement>('copy-text').focus();$<HTMLTextAreaElement>('copy-text').select();}
  }
  $$('[data-rc-open]').forEach(b=>b.addEventListener('click',()=>openCreate(choice(b.dataset.rcOpen,['generate','import']))));
  $$('[data-rc-close]').forEach(b=>b.addEventListener('click',()=>b.closest('dialog')?.close()));
  $$<HTMLDialogElement>('dialog').forEach(d=>d.addEventListener('cancel',e=>{if(busy)e.preventDefault();}));
  $<HTMLButtonElement>('refresh').addEventListener('click',load);
  $<HTMLInputElement>('search').addEventListener('input',()=>{page=1;render();});$<HTMLSelectElement>('filter').addEventListener('change',()=>{page=1;render();});
  $<HTMLButtonElement>('prev').addEventListener('click',()=>{page--;render();});$<HTMLButtonElement>('next').addEventListener('click',()=>{page++;render();});
  $<HTMLInputElement>('select-page').addEventListener('change',e=>{const rows=filtered().slice((page-1)*pageSize,page*pageSize);if($<HTMLInputElement>('select-page').checked){if(new Set([...selected,...rows.map(c=>c.id)]).size>500){notice('每批最多选择 500 个注册码',true);render();return;}rows.forEach(c=>selected.add(c.id));}else rows.forEach(c=>selected.delete(c.id));render();});
  $<HTMLButtonElement>('select-filtered').addEventListener('click',()=>{const list=filtered();if(list.length>500){notice('筛选结果超过 500 个，请缩小范围或按页选择',true);return;}selected=new Set(list.map(c=>c.id));render();});
  $('clear').addEventListener('click',()=>{selected.clear();render();});$('edit').addEventListener('click',()=>openEdit(picked()));
  $$('[data-rc-toggle]').forEach(b=>b.addEventListener('click',()=>mutate({action:'update',targets:targetList(picked()),patch:{enabled:b.dataset.rcToggle==='true'}},null,null,b.dataset.rcToggle==='true'?'所选注册码已启用':'所选注册码已停用')));
  $('delete').addEventListener('click',()=>{targets=targetList(picked());formError('delete');$('delete-description').textContent=`即将删除 ${targets.length} 个注册码。`;$<HTMLDialogElement>('delete-dialog').showModal();});
  $('delete-confirm').addEventListener('click',()=>mutate({action:'delete',targets},'delete',$<HTMLDialogElement>('delete-dialog'),'所选注册码已删除'));
  $('copy').addEventListener('click',()=>copyCodes(exportItems()));
  $('export').addEventListener('click',()=>{const list=exportItems(),format=$<HTMLSelectElement>('export-format').value,text=format==='csv'?exportCSV(list,now()):list.map(c=>c.code).join('\r\n')+'\r\n';const blob=new Blob([text],{type:format==='csv'?'text/csv;charset=utf-8':'text/plain;charset=utf-8'}),url=URL.createObjectURL(blob),link=el('a');link.href=url;link.download=`registration-codes-${new Date().toISOString().slice(0,10)}.${format}`;document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1000);notice(`已导出 ${list.length} 个注册码`);});
  createForm.addEventListener('input',()=>{preview();importCount();});
  control(createForm,'import_format').addEventListener('change',importCount);
  $<HTMLInputElement>('import-file').addEventListener('change',async e=>{const field=$<HTMLInputElement>('import-file'),file=field.files?.[0];if(!file)return;formError('create');if(file.size>256*1024){formError('create','文件最大为 256 KiB');return;}try{const text=await file.text(),extension=file.name.split('.').pop()?.toLowerCase() || '',format=({txt:'text',csv:'csv',json:'json'} as Record<string,string>)[extension]||'auto';control(createForm,'import_format').value=format;control(createForm,'codes').value=text;importCount();parseImport(text,format);}catch(error){formError('create',failure(error).message);}finally{field.value='';}});
  createForm.addEventListener('submit',e=>{
    e.preventDefault();formError('create');try{
      const f=(name:string)=>control(createForm,name),rules={max_uses:maxUses(f('max_uses').value),enabled:f('enabled').value==='true',note:f('note').value,starts_at:utcTime(f('starts_at').value)||null,expires_at:utcTime(f('expires_at').value)||null};
      const payload: Extract<CodeMutation,{rules:CodeRules}>={action:createMode==='generate'?'generate':'create',rules};
      if(createMode==='generate')payload.generator={count:Number(f('count').value),prefix:f('prefix').value,length:Number(f('length').value),charset:f('charset').value,group:Number(f('group').value)};
      else{payload.codes=parseImport(f('codes').value,f('import_format').value);if(!payload.codes.length)throw new Error('请输入至少一个注册码');}
      if(rules.starts_at&&rules.expires_at&&Date.parse(rules.expires_at)<=Date.parse(rules.starts_at))throw new Error('到期时间必须晚于生效时间');
      mutate(payload,'create',$<HTMLDialogElement>('create-dialog'),n=>`已${createMode==='generate'?'生成':'导入'} ${n} 个注册码并选中，可直接复制或导出。`);
    }catch(error){formError('create',failure(error).message);}
  });
  editForm.addEventListener('change',controls);
  editForm.addEventListener('submit',e=>{
    e.preventDefault();formError('edit');try{
      const patch: Partial<CodeRules>={};
      for(const key of ruleKeys)if(namedInput(editForm,'apply_'+key).checked){
        const value=control(editForm,key).value;
        if(key==='enabled')patch.enabled=value==='true';else if(key==='max_uses')patch.max_uses=maxUses(value);
        else if(key==='note')patch.note=value;else patch[key]=editOriginal && value===localTime(editOriginal[key]) ? editOriginal[key] || '' : utcTime(value);
      }
      if(!Object.keys(patch).length)throw new Error('请勾选至少一项要修改的设置');
      mutate({action:'update',targets,patch},'edit',$<HTMLDialogElement>('edit-dialog'),'注册码设置已保存');
    }catch(error){formError('edit',failure(error).message);}
  });
  policyForm.addEventListener('input',()=>{policyEditing++;});
  policyForm.addEventListener('submit',async e=>{
    e.preventDefault();if(busy||readonly||pending||!policy||!runtime.alive())return;
    const sent={revision:policy.revision,mode:choice(control(policyForm,'mode').value,['hidden','optional','required']),code_url:control(policyForm,'code_url').value},serial=policyEditing;
    busy=true;controls();try{const result=policyReceipt(await request(sent,'/policy'),sent);policy=result;if(serial===policyEditing)renderPolicy();notice(serial===policyEditing?'注册接入设置已保存':'本次接入设置已保存，后续输入仍待保存。');}
    catch(error){if(runtime.alive())notice(failure(error).message+'；请刷新核对后再保存。',true);}finally{busy=false;controls();}
  });
  const timezone=Intl.DateTimeFormat().resolvedOptions().timeZone;
  $$('[data-rc-timezone]').forEach(n=>{n.textContent=`按当前浏览器时区 ${timezone} 输入和显示时间。`;});
  controls();load();
})();
