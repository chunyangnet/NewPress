(() => {
  'use strict';
  const statuses={active:'可用',scheduled:'未生效',exhausted:'已用完',expired:'已过期',disabled:'已停用'};
  function statusOf(item,now=Date.now()) {
    if(!item.enabled)return 'disabled';
    if(item.expires_at&&Date.parse(item.expires_at)<=now)return 'expired';
    if(item.max_uses>0&&item.used>=item.max_uses)return 'exhausted';
    if(item.starts_at&&Date.parse(item.starts_at)>now)return 'scheduled';
    return 'active';
  }
  function csvRows(text) {
    const rows=[];let row=[],cell='',quoted=false,closed=false;
    for(let i=0;i<text.length;i++){
      const c=text[i];
      if(quoted){if(c==='"'){if(text[i+1]==='"'){cell+='"';i++;}else{quoted=false;closed=true;}}else cell+=c;continue;}
      if(c==='"'){if(cell||closed)throw new Error('CSV 引号格式不正确');quoted=true;continue;}
      if(c===','||c==='\n'||c==='\r'){row.push(cell);cell='';closed=false;if(c!==','){if(row.some(v=>v.trim()))rows.push(row);row=[];if(c==='\r'&&text[i+1]==='\n')i++;}continue;}
      if(closed){if(c===' '||c==='\t')continue;throw new Error('CSV 引号后存在多余内容');}cell+=c;
    }
    if(quoted)throw new Error('CSV 引号未闭合');
    row.push(cell);if(row.some(v=>v.trim()))rows.push(row);return rows;
  }
  function parseImport(text,format='auto') {
    text=String(text).replace(/^\uFEFF/,'').trim();if(!text)return [];
    let codes;
    if(format==='auto')format=text.startsWith('[')||text.startsWith('{')?'json':/^(?:"注册码"|注册码)(?:,|\r?\n|$)/.test(text)||/^[^\r\n]*,\s*"?注册码"?(?:,|\r?\n|$)/.test(text)?'csv':'text';
    if(format==='json'){
      let value;try{value=JSON.parse(text);}catch{throw new Error('JSON 格式不正确');}
      const items=Array.isArray(value)?value:(value.codes||value.items);
      if(!Array.isArray(items))throw new Error('JSON 须为注册码数组，或含 codes / items 数组的对象');
      codes=items.map(item=>typeof item==='string'?item:item?.code);
    }else if(format==='csv'){
      const rows=csvRows(text),header=rows[0]||[],column=header.findIndex(v=>['注册码','code'].includes(v.trim().toLowerCase()));
      if(column<0)throw new Error('CSV 首行必须包含「注册码」或「code」列名');
      if(header.filter(v=>['注册码','code'].includes(v.trim().toLowerCase())).length!==1)throw new Error('CSV 只能包含一个注册码列');
      codes=rows.slice(1).map(row=>{const value=row[column];return typeof value==='string'&&/^'-[A-Za-z0-9_-]+$/.test(value)?value.slice(1):value;});
    }else if(format==='text')codes=text.split(/[\s,，;；]+/);
    else throw new Error('请选择有效的导入格式');
    if(codes.length>500)throw new Error('每批最多导入 500 个注册码');
    const seen=new Set();return codes.map((value,i)=>{
      if(typeof value!=='string'||!/^[A-Za-z0-9_-]{4,128}$/.test(value.trim()))throw new Error(`第 ${i+1} 个注册码格式不正确`);
      const code=value.trim();if(seen.has(code))throw new Error(`第 ${i+1} 个注册码与前面的内容重复`);seen.add(code);return code;
    });
  }
  function maxUses(value) {
    if(String(value).trim()==='')throw new Error('请填写有效总次数，0 表示不限次数');
    const count=Number(value);if(!Number.isInteger(count)||count<0||count>1000000)throw new Error('有效总次数须为 0 至 1000000 的整数');return count;
  }
  function csvCell(value) {
    let text=String(value??'');if(/^[\s]*[=+\-@\t\r]/.test(text))text="'"+text;
    return '"'+text.replace(/"/g,'""')+'"';
  }
  function exportCSV(items,now=Date.now()) {
    const rows=[['注册码','状态','已使用次数','有效总次数（0不限）','生效时间','到期时间','备注','创建时间','最近使用时间'],...items.map(c=>[c.code,statuses[statusOf(c,now)],c.used,c.max_uses,c.starts_at||'',c.expires_at||'',c.note,c.created_at,c.last_used_at||''])];
    return '\uFEFF'+rows.map(row=>row.map(csvCell).join(',')).join('\r\n')+'\r\n';
  }
  function formatPreview(g) {
    const alphabet={friendly:'ABCD2345EFGH6789',mixed:'aB3dE7fG2hJ9kL4m',digits:'2385169470',hex:'A1B2C3D4E5F60789'}[g.charset]||'';
    const length=Math.max(0,Math.min(64,Number(g.length)||0)),group=Number(g.group)||0;
    let random='';for(let i=0;i<length;i++){if(group&&i&&i%group===0)random+='-';random+=alphabet[i%alphabet.length]||'A';}
    return String(g.prefix||'')+random;
  }
  if(typeof module!=='undefined'&&module.exports)module.exports={statusOf,csvRows,parseImport,csvCell,exportCSV,formatPreview,maxUses};
  if(typeof document==='undefined')return;
  const root=document.querySelector('[data-registration-codes]');if(!root)return;
  const $=name=>root.querySelector(`[data-rc-${name}]`),$$=selector=>root.querySelectorAll(selector);
  const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n;};
  const createForm=$('create-form'),editForm=$('edit-form'),policyForm=$('policy-form');
  const readonly=root.dataset.readOnly==='true',pageSize=50;
  let items=[],selected=new Set(),policy=null,page=1,busy=false,loaded=false,createMode='generate',targets=[],clockOffset=0;
  const now=()=>Date.now()+clockOffset;
  const filtered=()=>{const search=$('search').value.trim().toLowerCase(),status=$('filter').value;return items.filter(c=>(!search||(c.code+' '+c.note).toLowerCase().includes(search))&&(!status||statusOf(c,now())===status));};
  const picked=()=>items.filter(c=>selected.has(c.id));
  const exportItems=()=>selected.size?picked():filtered();
  const targetList=list=>list.map(c=>({id:c.id,version:c.version}));
  const notice=(message,error=false)=>{const n=$('notice');n.textContent=message;n.hidden=!message;n.classList.toggle('is-error',error);};
  const formError=(name,message='')=>{const n=$(name+'-error');n.textContent=message;n.hidden=!message;};
  const fmt=value=>value?new Date(value).toLocaleString('zh-CN',{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false}):'—';
  const localTime=value=>{if(!value)return '';const d=new Date(value);return new Date(d.getTime()-d.getTimezoneOffset()*60000).toISOString().slice(0,16);};
  const utcTime=value=>{if(!value)return '';const d=new Date(value);if(!Number.isFinite(d.getTime()))throw new Error('请选择有效的日期和时间');return d.toISOString();};
  async function request(body,path='') {
    const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),20000);
    try{
      const response=await fetch('/api/v1/admin/registration-codes'+path,{credentials:'same-origin',cache:'no-store',signal:controller.signal,...(body?{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':root.dataset.csrf},body:JSON.stringify(body)}:{})});
      const data=await response.json();
      if(!response.ok){const fields=Object.values(data.error?.details?.fields||{});throw new Error(fields.length?fields.join('\n'):(data.error?.message||'操作失败，请重试'));}
      return data;
    }catch(error){if(controller.signal.aborted)throw new Error('请求超时，请关闭窗口并刷新列表确认结果后再操作。');throw error;}finally{clearTimeout(timer);}
  }
  function controls() {
    const blocked=readonly||busy||!loaded;
    $$('button').forEach(button=>{button.disabled=blocked;});
    $$('[data-rc-close]').forEach(b=>{b.disabled=busy;});
    $('refresh').disabled=readonly||busy;
    for(const key of ['edit','clear','delete'])$(key).disabled=blocked||!selected.size;
    $$('[data-rc-toggle]').forEach(b=>{b.disabled=blocked||!selected.size;});
    for(const key of ['copy','export'])$(key).disabled=blocked||!exportItems().length;
    const count=filtered().length,pages=Math.max(1,Math.ceil(count/pageSize));
    $('prev').disabled=blocked||page<=1;$('next').disabled=blocked||page>=pages;
    $('select-filtered').disabled=blocked||!count;
    $$('[data-rc-rows] input,[data-rc-select-page]').forEach(n=>{n.disabled=blocked;});
    for(const form of [createForm,editForm,policyForm])form.querySelectorAll('input,select,textarea').forEach(n=>{n.disabled=blocked;});
    for(const key of ['count','prefix','length','charset','group'])createForm.elements.namedItem(key).disabled=blocked||createMode==='import';
    createForm.elements.codes.disabled=blocked||createMode!=='import';
    createForm.elements.import_format.disabled=blocked||createMode!=='import';
    for(const key of ['max_uses','enabled','starts_at','expires_at','note'])editForm.elements[key].disabled=blocked||!editForm.elements['apply_'+key].checked;
    root.setAttribute('aria-busy',String(busy));
  }
  function render() {
    const counts={total:items.length,active:0,used:0,finished:0};
    items.forEach(c=>{const s=statusOf(c,now());if(s==='active')counts.active++;if(s==='expired'||s==='exhausted')counts.finished++;counts.used+=c.used;});
    Object.entries(counts).forEach(([k,v])=>{root.querySelector(`[data-rc-stat="${k}"]`).textContent=v.toLocaleString('zh-CN');});
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
    $('empty').querySelector('h3').textContent=items.length?'没有匹配的注册码':'还没有注册码';
    $('empty').querySelector('p').textContent=items.length?'试试其他关键词或状态。':'批量生成或导入自定义注册码，集中管理注册名额。';
    $('total').textContent=`${matches.length} / ${items.length}`;
    $('selection').textContent=selected.size?`已选择 ${selected.size} 个`:'未选择';
    $('export-scope').textContent=selected.size?`所选 ${selected.size} 个`:`筛选结果 ${matches.length} 个`;
    $('page-label').textContent=`第 ${page} / ${pages} 页 · 每页 50 个 · 每批最多 500 个`;
    const checked=visible.filter(c=>selected.has(c.id)).length;$('select-page').checked=visible.length>0&&checked===visible.length;$('select-page').indeterminate=checked>0&&checked<visible.length;
    controls();
  }
  function renderPolicy() {
    policyForm.elements.mode.value=policy.mode;policyForm.elements.code_url.value=policy.code_url||'';
    const modes={hidden:'隐藏 · 无需注册码',optional:'选填 · 填写时校验',required:'必填 · 凭码注册'};
    $('policy-summary').textContent=`工作台码库 · ${modes[policy.mode]}`;
    $('registration-state').textContent={closed:'本站当前关闭注册，开放后接入规则才会生效。',invite:'本站采用邀请注册，新用户还需满足邀请要求。',open:'本站开放注册，按右侧所选规则校验注册码。'}[policy.registration]||'';
  }
  async function load() {
    if(busy||readonly)return;busy=true;controls();
    try{const data=await request();items=data.items||[];policy=data.policy;clockOffset=Date.parse(data.now)-Date.now();selected=new Set([...selected].filter(id=>items.some(c=>c.id===id)));loaded=true;renderPolicy();notice('');}
    catch(error){notice(error.message,true);$('empty').querySelector('h3').textContent='读取失败';}
    finally{busy=false;if(loaded)render();else controls();}
  }
  function updateInventory(action,result) {
    const affected=new Map(result.items.map(c=>[c.id,c]));
    if(action==='delete'){items=items.filter(c=>!affected.has(c.id));selected.clear();}
    else{items=items.map(c=>affected.get(c.id)||c);const known=new Set(items.map(c=>c.id));items.unshift(...result.items.filter(c=>!known.has(c.id)));if(action==='create'||action==='generate'){selected=new Set(result.items.map(c=>c.id));$('search').value='';$('filter').value='';page=1;}}
  }
  async function mutate(payload,errorName,dialog,message) {
    if(busy)return;busy=true;controls();if(errorName)formError(errorName);
    try{const result=await request(payload);updateInventory(payload.action,result);dialog?.close();notice(typeof message==='function'?message(result.count):message);}
    catch(error){if(errorName)formError(errorName,error.message);else notice(error.message,true);}
    finally{busy=false;render();}
  }
  function preview() {
    const g=Object.fromEntries(['prefix','length','charset','group'].map(k=>[k,createForm.elements.namedItem(k).value])),value=formatPreview(g);$('preview').textContent=value;$('preview-length').textContent=`含前缀和分隔符共 ${value.length} 位 · 实际码由服务器随机生成`;
  }
  function importCount() {try{const codes=parseImport(createForm.elements.codes.value,createForm.elements.import_format.value);$('import-count').textContent=`已识别 ${codes.length} 个注册码`;}catch(error){$('import-count').textContent=error.message;}}
  function openCreate(mode) {
    createMode=mode;formError('create');$('generator').hidden=mode!=='generate';$('import').hidden=mode!=='import';
    $('create-dialog').querySelector('h2').textContent=mode==='generate'?'批量生成注册码':'导入自定义注册码';
    $('create-submit').textContent=mode==='generate'?'生成注册码':'校验并导入';$('create-subtitle').textContent='每批最多 500 个，保存后可直接复制或导出。';
    controls();preview();importCount();$('create-dialog').showModal();
  }
  function openEdit(list) {
    targets=targetList(list);editForm.reset();formError('edit');$('edit-count').textContent=`正在设置 ${list.length} 个注册码`;
    if(list.length===1){const c=list[0];for(const k of ['max_uses','enabled','note'])editForm.elements[k].value=String(c[k]);for(const k of ['starts_at','expires_at'])editForm.elements[k].value=localTime(c[k]);}
    controls();$('edit-dialog').showModal();
  }
  async function copyCodes(list) {
    if(!list.length)return;const text=list.map(c=>c.code).join('\n');
    try{await navigator.clipboard.writeText(text);notice(`已复制 ${list.length} 个注册码`);}
    catch{$('copy-text').value=text;$('copy-dialog').showModal();$('copy-text').focus();$('copy-text').select();}
  }
  $$('[data-rc-open]').forEach(b=>b.addEventListener('click',()=>openCreate(b.dataset.rcOpen)));
  $$('[data-rc-close]').forEach(b=>b.addEventListener('click',()=>b.closest('dialog').close()));
  $$('dialog').forEach(d=>d.addEventListener('cancel',e=>{if(busy)e.preventDefault();}));
  $('refresh').addEventListener('click',load);
  $('search').addEventListener('input',()=>{page=1;render();});$('filter').addEventListener('change',()=>{page=1;render();});
  $('prev').addEventListener('click',()=>{page--;render();});$('next').addEventListener('click',()=>{page++;render();});
  $('select-page').addEventListener('change',e=>{const rows=filtered().slice((page-1)*pageSize,page*pageSize);if(e.target.checked){if(new Set([...selected,...rows.map(c=>c.id)]).size>500){notice('每批最多选择 500 个注册码',true);render();return;}rows.forEach(c=>selected.add(c.id));}else rows.forEach(c=>selected.delete(c.id));render();});
  $('select-filtered').addEventListener('click',()=>{const list=filtered();if(list.length>500){notice('筛选结果超过 500 个，请缩小范围或按页选择',true);return;}selected=new Set(list.map(c=>c.id));render();});
  $('clear').addEventListener('click',()=>{selected.clear();render();});$('edit').addEventListener('click',()=>openEdit(picked()));
  $$('[data-rc-toggle]').forEach(b=>b.addEventListener('click',()=>mutate({action:'update',targets:targetList(picked()),patch:{enabled:b.dataset.rcToggle==='true'}},null,null,b.dataset.rcToggle==='true'?'所选注册码已启用':'所选注册码已停用')));
  $('delete').addEventListener('click',()=>{targets=targetList(picked());formError('delete');$('delete-description').textContent=`即将删除 ${targets.length} 个注册码。`;$('delete-dialog').showModal();});
  $('delete-confirm').addEventListener('click',()=>mutate({action:'delete',targets},'delete',$('delete-dialog'),'所选注册码已删除'));
  $('copy').addEventListener('click',()=>copyCodes(exportItems()));
  $('export').addEventListener('click',()=>{const list=exportItems(),format=$('export-format').value,text=format==='csv'?exportCSV(list,now()):list.map(c=>c.code).join('\r\n')+'\r\n';const blob=new Blob([text],{type:format==='csv'?'text/csv;charset=utf-8':'text/plain;charset=utf-8'}),url=URL.createObjectURL(blob),link=el('a');link.href=url;link.download=`registration-codes-${new Date().toISOString().slice(0,10)}.${format}`;document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1000);notice(`已导出 ${list.length} 个注册码`);});
  createForm.addEventListener('input',()=>{preview();importCount();});
  createForm.elements.import_format.addEventListener('change',importCount);
  $('import-file').addEventListener('change',async e=>{const file=e.target.files[0];if(!file)return;formError('create');if(file.size>256*1024){formError('create','文件最大为 256 KiB');return;}try{const text=await file.text(),extension=file.name.split('.').pop().toLowerCase(),format={txt:'text',csv:'csv',json:'json'}[extension]||'auto';createForm.elements.import_format.value=format;createForm.elements.codes.value=text;importCount();parseImport(text,format);}catch(error){formError('create',error.message);}finally{e.target.value='';}});
  createForm.addEventListener('submit',e=>{
    e.preventDefault();formError('create');try{
      const f=createForm.elements,rules={max_uses:maxUses(f.max_uses.value),enabled:f.enabled.value==='true',note:f.note.value,starts_at:utcTime(f.starts_at.value)||null,expires_at:utcTime(f.expires_at.value)||null};
      const payload={action:createMode==='generate'?'generate':'create',rules};
      if(createMode==='generate')payload.generator={count:Number(f.count.value),prefix:f.prefix.value,length:Number(f.namedItem('length').value),charset:f.charset.value,group:Number(f.group.value)};
      else{payload.codes=parseImport(f.codes.value,f.import_format.value);if(!payload.codes.length)throw new Error('请输入至少一个注册码');}
      if(rules.starts_at&&rules.expires_at&&Date.parse(rules.expires_at)<=Date.parse(rules.starts_at))throw new Error('到期时间必须晚于生效时间');
      mutate(payload,'create',$('create-dialog'),n=>`已${createMode==='generate'?'生成':'导入'} ${n} 个注册码并选中，可直接复制或导出。`);
    }catch(error){formError('create',error.message);}
  });
  editForm.addEventListener('change',controls);
  editForm.addEventListener('submit',e=>{e.preventDefault();formError('edit');try{const f=editForm.elements,patch={};for(const k of ['max_uses','enabled','starts_at','expires_at','note'])if(f['apply_'+k].checked)patch[k]=k==='max_uses'?maxUses(f[k].value):k==='enabled'?f[k].value==='true':k.endsWith('_at')?utcTime(f[k].value):f[k].value;if(!Object.keys(patch).length)throw new Error('请勾选至少一项要修改的设置');mutate({action:'update',targets,patch},'edit',$('edit-dialog'),'注册码设置已保存');}catch(error){formError('edit',error.message);}});
  policyForm.addEventListener('submit',async e=>{e.preventDefault();if(busy)return;busy=true;controls();try{const f=policyForm.elements;policy=await request({revision:policy.revision,mode:f.mode.value,code_url:f.code_url.value},'/policy');renderPolicy();notice('注册接入设置已保存');}catch(error){notice(error.message,true);}finally{busy=false;controls();}});
  const timezone=Intl.DateTimeFormat().resolvedOptions().timeZone;
  $$('[data-rc-timezone]').forEach(n=>{n.textContent=`按当前浏览器时区 ${timezone} 输入和显示时间。`;});
  controls();load();
})();
