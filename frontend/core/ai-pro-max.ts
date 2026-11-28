import type {} from '../shared/account-types';
import { control, required } from './ledger-admin-dom';
import { suggestedTopics, writingFailure, writingJSON, writingSession } from '../shared/ai-writing-data';

(() => {
  if(typeof document==='undefined')return;
  const root=document.querySelector<HTMLElement>('[data-ai-pro-max]');
  if(!root)return;
  let retired=false;
  root.addEventListener('submit',event=>{
    const message=event.submitter instanceof HTMLElement?event.submitter.dataset.confirmAction:undefined;
    if(retired||message&&!window.confirm(message))event.preventDefault();
  });
  const picker=root.querySelector<HTMLElement>('[data-topic-picker]'),form=picker?.closest('form');
  if(!picker||!form)return;
  const topics=control(form,'topics'),selectors=['target','model','language'].map(name=>control(form,name));
  const button=required<HTMLButtonElement>(picker,'[data-suggest-topics]'),status=required(picker,'[data-topic-status]'),list=required(picker,'[data-topic-options]');
  const owner=root.dataset.userId||'',login=root.dataset.username||'',csrf=control(form,'csrf_token').value;
  let current:AbortController|null=null;
  const alive=()=>!retired&&root.isConnected;
  picker.hidden=false;
  function clear(){current?.abort();current=null;button.disabled=retired;list.replaceChildren();status.textContent='';}
  function retire(){if(retired)return;retired=true;clear();topics.value='';status.textContent='登录状态或页面已变化，请重新载入。';}
  for(const select of selectors)select.addEventListener('change',clear);
  document.addEventListener('chunnel:session',event=>{const value=event.detail;if(value?.authenticated!==true||String(value.id)!==owner||value.csrf_token!==csrf)retire();});
  document.addEventListener('chunnel:admin-before-replace',event=>{if(event.detail.regions.some(region=>region.contains(root)))retire();});
  window.addEventListener('pagehide',retire);window.addEventListener('pageshow',event=>{if(event.persisted)retire();});
  function showOption(title:string){
    const item=document.createElement('li'),add=document.createElement('button');add.type='button';add.textContent='+ '+title;add.setAttribute('aria-label','采用选题：'+title);
    add.addEventListener('click',()=>{
      if(!alive()||topics.disabled)return;
      const existing=topics.value.split(/\r?\n/).map(line=>line.trim()).filter(Boolean);
      if(!existing.includes(title)){
        if(existing.length>=20){status.textContent='计划已有 20 个选题。';return;}
        topics.value+=(topics.value&&!topics.value.endsWith('\n')?'\n':'')+title;topics.dispatchEvent(new Event('input',{bubbles:true}));
      }
      item.remove();status.textContent='选题已填入，可继续编辑。';
    });item.append(add);list.append(item);
  }
  button.addEventListener('click',async()=>{
    if(current||!alive()||topics.disabled)return;
    const controller=new AbortController();current=controller;button.disabled=true;list.replaceChildren();status.textContent='正在生成选题…';
    const input=Object.fromEntries(selectors.map(select=>[select.name,select.value]));
    const timer=setTimeout(()=>controller.abort(),100000),options:RequestInit={credentials:'same-origin',redirect:'error',cache:'no-store',signal:controller.signal};
    try {
      const session=await writingJSON(await fetch('/api/v1/admin/session',options));
      try{writingSession(session,owner,login,csrf);}catch(error){retire();throw error;}
      if(!alive()||controller.signal.aborted)return;
      const response=await fetch('/api/v1/admin/plugins/ai-pro-max/topics',{...options,method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:JSON.stringify(input)});
      if(response.status===401||response.status===403)retire();
      const choices=suggestedTopics(await writingJSON(response));
      if(current!==controller||!alive()||controller.signal.aborted)return;
      choices.forEach(showOption);status.textContent='点击选题可加入计划。';
    }catch(error){if(current===controller&&alive())status.textContent=controller.signal.aborted?'生成超时，请核对后重试。':writingFailure(error).message;}
    finally{clearTimeout(timer);if(current===controller){current=null;button.disabled=retired;}}
  });
})();
