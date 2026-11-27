import {record, responseError, errorMessage} from './editor-tool-data';

type Selection = {id:number;version:number};
type Update = {snippet_id:number;snippet_version:number;items:Selection[];digest?:string;request_key?:string};
type Snippet = {id:number;version:number;current_version:number;visibility:string;title:string};
const integer=(v:unknown):v is number=>Number.isSafeInteger(v)&&Number(v)>0;
export function snippet(value:unknown):Snippet {
  if(!record(value)||!integer(value.id)||!integer(value.version)||!integer(value.current_version)||typeof value.title!=='string'||!['private','shared','withdrawn'].includes(String(value.visibility)))throw Error('片段响应无效');
  return value as Snippet;
}
export function requestKey():string {return Array.from(crypto.getRandomValues(new Uint8Array(32)),v=>v.toString(16).padStart(2,'0')).join('');}

(() => {
  const root=document.querySelector<HTMLElement>('[data-snippet-workspace]');
  if(!root)return;
  const form=root.querySelector<HTMLFormElement>('[data-snippet-editor]');
  if(!form)return;
  const status=root.querySelector<HTMLElement>('[data-snippet-status]')!;
  const list=root.querySelector<HTMLElement>('[data-snippet-impacts-list]');
  const planBox=root.querySelector<HTMLElement>('[data-snippet-plan]');
  const preview=root.querySelector<HTMLButtonElement>('[data-snippet-preview]');
  const apply=root.querySelector<HTMLButtonElement>('[data-snippet-apply]');
  let id=Number(root.dataset.id),version=Number(root.dataset.version),busy=false,page=1,pages=1,hasImpacts=false;
  let pending:Update|null=null,uncertain=false,saveUncertain=false;
  const selected=new Map<number,Selection>();
  const enabled=root.dataset.enabled==='true';
  function announce(message:string){status.textContent=message;}
  function resetPlan(){if(uncertain)return;pending=null;if(planBox)planBox.hidden=true;}
  function controls(){
    root!.setAttribute('aria-busy',String(busy));
    root!.querySelectorAll<HTMLButtonElement>('button').forEach(b=>{b.disabled=busy||saveUncertain||(!enabled && !b.matches('[data-snippet-copy]'))||uncertain&&!b.matches('[data-snippet-apply]');});
    form!.querySelectorAll<HTMLInputElement|HTMLTextAreaElement|HTMLSelectElement>('input:not([type=hidden]),textarea,select').forEach(e=>{e.disabled=busy||uncertain||saveUncertain||form!.querySelector('fieldset')!.disabled;});
    list?.querySelectorAll<HTMLInputElement>('input').forEach(e=>{e.disabled=busy||uncertain||saveUncertain||e.dataset.eligible!=='true';});
    const previous=root!.querySelector<HTMLButtonElement>('[data-snippet-prev]'),next=root!.querySelector<HTMLButtonElement>('[data-snippet-next]');
    if(previous){previous.hidden=!hasImpacts||page<=1;previous.disabled=busy||uncertain||saveUncertain;}
    if(next){next.hidden=!hasImpacts||page>=pages;next.disabled=busy||uncertain||saveUncertain;}
    if(preview){preview.hidden=!selected.size;preview.disabled=busy||uncertain||saveUncertain||!enabled;}
    if(apply)apply.textContent=uncertain?'核对这次提交结果':'确认更新';
    const selection=root!.querySelector<HTMLElement>('[data-snippet-selection]');if(selection)selection.textContent=hasImpacts?`已选择 ${selected.size} 篇（跨页保留）`:'';
  }
  async function api(url:string,method='GET',body?:unknown,etag?:string):Promise<unknown>{
    const controller=new AbortController();const timeout=setTimeout(()=>controller.abort(),15000);
    try{
      const response=await fetch(url,{method,credentials:'same-origin',cache:'no-store',redirect:'error',signal:controller.signal,headers:{Accept:'application/json',...(method!=='GET'?{'Content-Type':'application/json','X-CSRF-Token':form!.querySelector<HTMLInputElement>('[name=csrf_token]')!.value}:{}),...(etag?{'If-Match':etag}:{})},...(body===undefined?{}:{body:JSON.stringify(body)})});
      const result:unknown=await response.json();
      if(!response.ok){const error=Error(responseError(result,'操作失败，输入仍保留'));Object.assign(error,{status:response.status});throw error;}
      return result;
    }finally{clearTimeout(timeout);}
  }
  async function run(action:()=>Promise<void>){if(busy||saveUncertain)return;busy=true;controls();try{await action();}catch(e){announce(errorMessage(e,'请求未完成，输入仍保留，请重试'));}finally{busy=false;controls();}}
  async function saveMutation(action:()=>Promise<void>){
    try{await action();}catch(error){
      const code=record(error)?error.status:undefined;
      if(typeof code==='number'&&[400,401,403,404,409,412,415,422,428,429].includes(code))throw error;
      saveUncertain=true;resetPlan();
      root!.querySelector<HTMLElement>('[data-snippet-save-review]')!.hidden=false;
      announce('尚未取得保存或撤回结果。输入已保留，本页暂停提交；请在新标签页核对片段列表和版本，再决定后续修改。');
    }
  }
  async function impacts(number:number){
    const value=await api(`/api/v1/admin/snippets/${id}/impacts?page=${number}`);
    if(!record(value)||!Array.isArray(value.items)||!integer(value.page)||!integer(value.pages))throw Error('影响清单响应无效');
    const current=snippet(value.snippet);if(current.version!==version){selected.clear();resetPlan();announce('片段已变化，请重新载入页面后核对');throw Error('片段版本已经变化，当前输入保留');}
    const fragment=document.createDocumentFragment();
    for(const item of value.items){
      if(!record(item)||!integer(item.id)||!integer(item.version)||typeof item.title!=='string'||typeof item.status!=='string'||!Array.isArray(item.reference_versions)||!item.reference_versions.every(integer)||!Number.isSafeInteger(item.changes))throw Error('引用记录无效');
      const label=document.createElement('label'),check=document.createElement('input'),text=document.createElement('span'),meta=document.createElement('small');
      check.type='checkbox';check.dataset.eligible=String(current.visibility==='shared'&&Number(item.changes)>0);check.checked=selected.has(item.id);check.disabled=check.dataset.eligible!=='true';
      text.textContent=item.title;meta.textContent=`#${item.id} · 文章 v${item.version} · ${item.status} · 引用 ${item.reference_versions.map(v=>'v'+v).join('、')} → v${current.current_version}`;text.append(meta);
      const selection={id:item.id,version:item.version};
      check.addEventListener('change',()=>{if(check.checked){if(selected.size>=100){check.checked=false;announce('每次最多选择 100 篇');return;}selected.set(selection.id,selection);}else selected.delete(selection.id);resetPlan();controls();});
      label.append(check,text);fragment.append(label);
    }
    list!.replaceChildren(fragment);page=value.page;pages=value.pages;hasImpacts=true;
    if(!value.items.length)list!.textContent='当前没有你能编辑的引用文章。';
  }
  form.addEventListener('input',()=>{resetPlan();});
  form.addEventListener('submit',event=>{event.preventDefault();if(!enabled||uncertain||saveUncertain)return;const data=new FormData(form);void run(()=>saveMutation(async()=>{
    const visibility=String(data.get('visibility'));
    if(id>0&&visibility==='private'&&!confirm('转为私密将隐藏此片段的所有旧引用。确认保存？'))return;
    const value=snippet(await api(id?`/api/v1/admin/snippets/${id}`:'/api/v1/admin/snippets',id?'PUT':'POST',{title:data.get('title'),content_source:data.get('content_source'),visibility},id?`"snippet-${id}-v${version}"`:undefined));
    window.location.assign(`/admin/snippets/?edit=${value.id}`);
  }));});
  root.querySelector('[data-snippet-withdraw]')?.addEventListener('click',()=>{if(!enabled||uncertain||saveUncertain||!confirm('撤回后，后续请求会隐藏全部旧引用。确认撤回？'))return;void run(()=>saveMutation(async()=>{await api(`/api/v1/admin/snippets/${id}/withdraw`,'POST',undefined,`"snippet-${id}-v${version}"`);window.location.reload();}));});
  root.querySelector('[data-snippet-copy]')?.addEventListener('click',()=>{void run(async()=>{const field=root.querySelector<HTMLTextAreaElement>('[data-snippet-reference]')!;try{await navigator.clipboard.writeText(field.value);announce('已复制固定版本引用');}catch{field.focus();field.select();announce('已选中引用，请复制后插入文章 HTML 正文');}});});
  root.querySelector('[data-snippet-impacts]')?.addEventListener('click',()=>{if(!enabled||uncertain)return;void run(()=>impacts(1));});
  root.querySelector('[data-snippet-prev]')?.addEventListener('click',()=>{void run(()=>impacts(page-1));});
  root.querySelector('[data-snippet-next]')?.addEventListener('click',()=>{void run(()=>impacts(page+1));});
  preview?.addEventListener('click',()=>{if(!enabled||uncertain||!selected.size)return;void run(async()=>{
    const update:Update={snippet_id:id,snippet_version:version,items:[...selected.values()]};
    const value=await api(`/api/v1/admin/snippets/${id}/preview`,'POST',update);
    if(!record(value)||typeof value.digest!=='string'||!/^[a-f0-9]{64}$/.test(value.digest)||!Array.isArray(value.items)||value.items.length!==update.items.length)throw Error('预览响应无效');
    const target=snippet(value.snippet);pending={...update,digest:value.digest,request_key:requestKey()};
    root.querySelector<HTMLElement>('[data-snippet-plan-summary]')!.textContent=`将 ${update.items.length} 篇选中文章更新到「${target.title}」正文 v${target.current_version}。每篇文章新增一个修订版本。`;
    planBox!.hidden=false;announce('请核对预览后确认更新');apply!.focus();
  });});
  apply?.addEventListener('click',()=>{if(!enabled||!pending)return;void run(async()=>{
    const request=pending!;
    try{
      const value=await api(`/api/v1/admin/snippets/${id}/apply`,'POST',request);
      if(!record(value)||!record(value.receipt)||value.receipt.request_key!==request.request_key||!Array.isArray(value.receipt.items))throw Error('未取得有效操作回执');
      uncertain=false;pending=null;selected.clear();planBox!.hidden=true;list!.replaceChildren();hasImpacts=false;
      announce(`${value.replayed?'已核对原提交':'更新已提交'}：${value.receipt.items.length} 篇文章。可重新查看引用清单。`);
    }catch(error){
      const code=record(error)?error.status:undefined;
      if(typeof code==='number'&&[400,403,404,409,412,422,428].includes(code)){uncertain=false;pending=null;planBox!.hidden=true;throw error;}
      uncertain=true;announce('尚未取得提交结果。请留在本页，点击“核对这次提交结果”使用同一请求确认，避免重复修改。');
    }
  });});
  window.addEventListener('beforeunload',event=>{if(uncertain||saveUncertain){event.preventDefault();event.returnValue='';}});
  controls();
})();
