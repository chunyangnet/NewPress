import {composeSnapshot,composeSaved} from '../shared/community-draft';
(() => {
 'use strict';
 const root=document.querySelector<HTMLElement>('[data-questions]');if(!root)return;
 const status=root.querySelector<HTMLElement>('[data-qa-status]');if(!status)return;
 let busy=false;const attempts=new WeakMap<HTMLElement,{signature:string;key:string}>();
 document.addEventListener('chunnel:session',event=>{if(Number(event.detail?.authenticated?event.detail.id:0)!==Number(root.dataset.userId))location.reload();});
 const tell=(message:string)=>{status.hidden=false;status.textContent=message;status.scrollIntoView({block:'nearest'});};
 const execute=async (node:HTMLElement)=>{
  if(busy)return;const form=node instanceof HTMLFormElement?node:null,action=form?.dataset.qaCommand||node.dataset.qaAction;
  if(!action)return;
  if(form&&!form.reportValidity())return;
  const draft=form?composeSnapshot(form):undefined;
  const cmd:Record<string,string|string[]|number|boolean>={action,version:action==='create'?0:Number(root.dataset.version)};
  if(form)for(const[key,value]of new FormData(form))cmd[key]=key==='tags'?String(value).split(/[,，]/).map(s=>s.trim()).filter(Boolean):String(value).trim();
  if(node.dataset.answer!==undefined)cmd.answer_id=Number(node.dataset.answer);if(node.dataset.report!==undefined)cmd.report_id=Number(node.dataset.report);if(node.dataset.enabled!==undefined)cmd.enabled=node.dataset.enabled==='true';
  if(['hide','hide_answer','lock'].includes(action)&&!confirm('确认修改此内容的展示或互动状态？'))return;
  const signature=JSON.stringify(cmd);let attempt=attempts.get(node);if(!attempt||attempt.signature!==signature){attempt={signature,key:crypto.randomUUID()};attempts.set(node,attempt)}cmd.key=attempt.key;
  busy=true;root.setAttribute('aria-busy','true');const controls=[...root.querySelectorAll<HTMLButtonElement|HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement>('button,input,select,textarea')].map(el=>[el,el.disabled] as const);controls.forEach(([el])=>el.disabled=true);tell('正在保存…');
  try{
   const response=await fetch('/api/v1/questions/'+(action==='create'?'':root.dataset.id+'/'),{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-CSRF-Token':root.dataset.csrf||''},body:JSON.stringify(cmd),signal:AbortSignal.timeout(15000)});
   let result:unknown;try{result=await response.json()}catch{throw new Error('返回内容异常，输入已保留，请重试。')}
   if(response.status===409){
    const latest=await fetch(location.href,{credentials:'same-origin',cache:'no-store',signal:AbortSignal.timeout(15000)});
    if(!latest.ok)throw new Error('无法读取最新内容，输入已保留，请稍后重试。');
    const doc=new DOMParser().parseFromString(await latest.text(),'text/html'),fresh=doc.querySelector<HTMLElement>('[data-questions]');
    if(!fresh||fresh.dataset.id!==root.dataset.id||fresh.dataset.userId!==root.dataset.userId)throw new Error('登录身份或内容状态已变化，输入已保留。');
    tell('内容已更新，输入已保留。请在新窗口核对后，再点击确认按钮更新提交版本。');
    const link=document.createElement('a');link.href=location.href;link.target='_blank';link.rel='noopener';link.textContent='查看最新内容';status.append(' ',link);
    const retry=document.createElement('button');retry.type='button';retry.className='ui-button';retry.textContent='已核对，保留输入继续编辑';retry.addEventListener('click',()=>{root.dataset.version=fresh.dataset.version;attempts.delete(node);tell('已更新版本。请核对当前输入，然后重新提交。')},{once:true});status.append(' ',retry);return;
   }
   if(!response.ok){
    const error=result && typeof result==='object' && 'error' in result ? result.error : null;
    throw new Error(error && typeof error==='object' && 'message' in error && typeof error.message==='string'?error.message:'操作未完成，输入已保留。');
   }
   if(!result||typeof result!=='object'||!('id' in result)||typeof result.id!=='number'||!Number.isSafeInteger(result.id)||result.id<1||!('version' in result)||typeof result.version!=='number'||!Number.isSafeInteger(result.version)||result.version<1||!('url' in result)||typeof result.url!=='string'||!result.url.startsWith('/questions/'+result.id+'/'))throw new Error('保存回执异常，输入已保留。原样重试会识别同一请求。');
   attempts.delete(node);if(form&&draft)composeSaved(form,Number(root.dataset.userId),draft);tell('已保存。');if(root.hasAttribute('data-management'))location.assign('/admin/questions/'+result.id+'/');else location.assign(result.url);
  }catch(error){tell(error instanceof Error ? (error.name==='TimeoutError'?'响应超时，输入已保留。原样重试不会重复发布。':error.message):'操作未完成，输入已保留。')}finally{busy=false;root.removeAttribute('aria-busy');controls.forEach(([el,disabled])=>el.disabled=disabled)}
 }
 root.addEventListener('submit',event=>{const form=event.target instanceof Element?event.target.closest<HTMLFormElement>('[data-qa-command]'):null;if(form){event.preventDefault();void execute(form)}});
 root.addEventListener('click',event=>{const button=event.target instanceof Element?event.target.closest<HTMLElement>('[data-qa-action]'):null;if(button)void execute(button)});
})();
