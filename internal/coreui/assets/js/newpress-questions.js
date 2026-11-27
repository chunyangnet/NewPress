(() => {
 'use strict';
 const root=document.querySelector('[data-questions]');if(!root)return;
 const status=root.querySelector('[data-qa-status]');let busy=false;const attempts=new WeakMap();
 document.addEventListener('chunnel:session',event=>{if(Number(event.detail?.authenticated?event.detail.id:0)!==Number(root.dataset.userId))location.reload();});
 const tell=message=>{status.hidden=false;status.textContent=message;status.scrollIntoView({block:'nearest'});};
 async function execute(node){
  if(busy)return;const form=node.matches('form')?node:null,action=form?.dataset.qaCommand||node.dataset.qaAction;
  if(form&&!form.reportValidity())return;
  const cmd={action,version:action==='create'?0:Number(root.dataset.version)};
  if(form)for(const[key,value]of new FormData(form))cmd[key]=key==='tags'?String(value).split(/[,，]/).map(s=>s.trim()).filter(Boolean):String(value).trim();
  if(node.dataset.answer!==undefined)cmd.answer_id=Number(node.dataset.answer);if(node.dataset.report!==undefined)cmd.report_id=Number(node.dataset.report);if(node.dataset.enabled!==undefined)cmd.enabled=node.dataset.enabled==='true';
  if(['hide','hide_answer','lock'].includes(action)&&!confirm('确认修改此内容的展示或互动状态？'))return;
  const signature=JSON.stringify(cmd);let attempt=attempts.get(node);if(!attempt||attempt.signature!==signature){attempt={signature,key:crypto.randomUUID()};attempts.set(node,attempt)}cmd.key=attempt.key;
  busy=true;root.setAttribute('aria-busy','true');const controls=[...root.querySelectorAll('button,input,select,textarea')].map(el=>[el,el.disabled]);controls.forEach(([el])=>el.disabled=true);tell('正在保存…');
  try{
   const response=await fetch('/api/v1/questions/'+(action==='create'?'':root.dataset.id+'/'),{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-CSRF-Token':root.dataset.csrf},body:JSON.stringify(cmd),signal:AbortSignal.timeout(15000)});
   let result;try{result=await response.json()}catch{throw new Error('返回内容异常，输入已保留，请重试。')}
   if(response.status===409){
    const latest=await fetch(location.href,{credentials:'same-origin',cache:'no-store',signal:AbortSignal.timeout(15000)});
    if(!latest.ok)throw new Error('无法读取最新内容，输入已保留，请稍后重试。');
    const doc=new DOMParser().parseFromString(await latest.text(),'text/html'),fresh=doc.querySelector('[data-questions]');
    if(!fresh||fresh.dataset.id!==root.dataset.id||fresh.dataset.userId!==root.dataset.userId)throw new Error('登录身份或内容状态已变化，输入已保留。');
    tell('内容已更新，输入已保留。请在新窗口核对后，再点击确认按钮更新提交版本。');
    const link=document.createElement('a');link.href=location.href;link.target='_blank';link.rel='noopener';link.textContent='查看最新内容';status.append(' ',link);
    const retry=document.createElement('button');retry.type='button';retry.className='ui-button';retry.textContent='已核对，保留输入继续编辑';retry.addEventListener('click',()=>{root.dataset.version=fresh.dataset.version;attempts.delete(node);tell('已更新版本。请核对当前输入，然后重新提交。')},{once:true});status.append(' ',retry);return;
   }
   if(!response.ok)throw new Error(result.error?.message||'操作未完成，输入已保留。');
   if(!Number.isSafeInteger(result.id)||result.id<1||!Number.isSafeInteger(result.version)||result.version<1||typeof result.url!=='string'||!result.url.startsWith('/questions/'+result.id+'/'))throw new Error('保存回执异常，输入已保留。原样重试会识别同一请求。');
   attempts.delete(node);form?.dispatchEvent(new Event('community:saved'));tell('已保存。');if(root.hasAttribute('data-management'))location.assign('/admin/questions/'+result.id+'/');else location.assign(result.url);
  }catch(error){tell(error.name==='TimeoutError'?'响应超时，输入已保留。原样重试不会重复发布。':error.message)}finally{busy=false;root.removeAttribute('aria-busy');controls.forEach(([el,disabled])=>el.disabled=disabled)}
 }
 root.addEventListener('submit',event=>{const form=event.target.closest('[data-qa-command]');if(form){event.preventDefault();execute(form)}});
 root.addEventListener('click',event=>{const button=event.target.closest('[data-qa-action]');if(button)execute(button)});
})();
