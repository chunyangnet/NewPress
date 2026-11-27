import {record, responseError, errorMessage} from './editor-tool-data';

type DraftRequest = {post_id:number;version:number;digest:string;request_key:string};
type DraftPlan = {source:{id:number;version:number};type:string;title:string;excerpt:string;cover_url:string;content_html:string;digest:string;occurrences:number;references:{id:number;version:number;snippet_version:number;title:string}[]};
const positive=(v:unknown):v is number=>Number.isSafeInteger(v)&&Number(v)>0;
const digest=(v:unknown):v is string=>typeof v==='string'&&/^[a-f0-9]{64}$/.test(v);

function draftError(value:unknown):string {
  const message=responseError(value,'操作暂时无法完成');
  if(!record(value)||!record(value.error)||!record(value.error.details)||!record(value.error.details.fields))return message;
  const reasons=[...new Set(Object.values(value.error.details.fields).filter((v):v is string=>typeof v==='string'&&!!v.trim()).map(v=>v.trim().slice(0,240)))].slice(0,4);
  return reasons.length?message+'：'+reasons.join('；'):message;
}

export function draftPlan(v:unknown,id:number,version:number):DraftPlan {
  if(!record(v)||!record(v.source)||v.source.id!==id||v.source.version!==version||!['post','page'].includes(String(v.type))||
    !['title','excerpt','cover_url','content_html'].every(k=>typeof v[k]==='string')||!digest(v.digest)||!positive(v.occurrences)||v.occurrences>200||
    !Array.isArray(v.references)||!v.references.length||v.references.length>100||!v.references.every(r=>record(r)&&positive(r.id)&&positive(r.version)&&positive(r.snippet_version)&&typeof r.title==='string'))throw Error('草稿预览响应无效，请重新预览');
  return v as DraftPlan;
}

export function draftRequest(v:unknown,id:number):v is DraftRequest {
  return record(v)&&v.post_id===id&&positive(v.version)&&digest(v.digest)&&digest(v.request_key)&&Object.keys(v).length===4;
}

(() => {
  const candidate=document.querySelector<HTMLFormElement>('[data-content-editor]');
  const trigger=candidate?.querySelector<HTMLButtonElement>('[data-snippet-draft-open]');
  const box=document.querySelector<HTMLDialogElement>('[data-snippet-draft-dialog]');
  if(!candidate||!trigger||!box)return;
  const form=candidate,launcher=trigger,dialog=box;
  const id=Number(launcher.dataset.post),user=Number(form.dataset.editorUserId||0),login=form.dataset.editorLogin||'';
  if(!positive(id)||!login)return;
  const status=dialog.querySelector<HTMLElement>('[data-snippet-draft-status]')!;
  const preview=dialog.querySelector<HTMLButtonElement>('[data-snippet-draft-preview]')!;
  const confirm=dialog.querySelector<HTMLButtonElement>('[data-snippet-draft-confirm]')!;
  const consent=dialog.querySelector<HTMLInputElement>('[data-snippet-draft-consent]')!;
  const content=dialog.querySelector<HTMLElement>('[data-snippet-draft-content]')!;
  const result=dialog.querySelector<HTMLAnchorElement>('[data-snippet-draft-result]')!;
  const frame=dialog.querySelector<HTMLIFrameElement>('[data-snippet-draft-frame]')!;
  const storageKey=`newpress:snippet-draft:${user>0?'user:'+user:'legacy:'+login}:${id}`;
  let pending:DraftRequest|null=null,busy=false,uncertain=false,serial=0,controller:AbortController|null=null;
  // Store only confirmation metadata, never article HTML or credentials. The
  // same tab can recover a lost response after refresh without creating twice.
  try {const saved=JSON.parse(sessionStorage.getItem(storageKey)||'null');if(draftRequest(saved,id)){pending=saved;uncertain=true;}}catch{/* Keep the in-page request if browser storage is unavailable. */}
  function remember():boolean{try{if(pending)sessionStorage.setItem(storageKey,JSON.stringify(pending));else sessionStorage.removeItem(storageKey);return true;}catch{return false;}}
  function controls(){
    dialog.setAttribute('aria-busy',String(busy));preview.disabled=busy||uncertain;
    confirm.disabled=busy||!pending||(!uncertain&&!consent.checked);consent.disabled=busy||uncertain||!pending;
    confirm.textContent=uncertain?'核对这次生成结果':'确认生成独立草稿';
  }
  function reset(){pending=null;consent.checked=false;content.hidden=true;frame.srcdoc='';remember();controls();}
  async function api(url:string,body?:unknown,csrf?:string):Promise<unknown>{
    const c=new AbortController();controller=c;const timer=setTimeout(()=>c.abort(),15000);
    try{
      const response=await fetch(url,{method:body===undefined?'GET':'POST',credentials:'same-origin',cache:'no-store',redirect:'error',signal:c.signal,
        headers:{Accept:'application/json',...(body===undefined?{}:{'Content-Type':'application/json','X-CSRF-Token':csrf||''})},...(body===undefined?{}:{body:JSON.stringify(body)})});
      const value:unknown=await response.json();if(c.signal.aborted)throw Error('请求中断');
      if(!response.ok){const error=Error(draftError(value));Object.assign(error,{status:response.status,code:record(value)&&record(value.error)?value.error.code:''});throw error;}
      return value;
    }finally{clearTimeout(timer);}
  }
  async function session():Promise<string>{
    const v=await api('/api/v1/admin/session');
    if(!record(v)||v.authenticated!==true||v.read_only!==false||Number(v.user_id||0)!==user||(user===0&&v.username!==login)||
      typeof v.csrf_token!=='string'||!v.csrf_token||!Array.isArray(v.capabilities)||!v.capabilities.includes('content.create')||!v.capabilities.includes('content.read'))throw Error('登录账号或权限已变化，请在新窗口重新登录原账号后核对');
    return v.csrf_token;
  }
  async function run(action:(n:number)=>Promise<void>){if(busy)return;busy=true;controls();const n=++serial;
    try{await action(n);}catch(e){if(n===serial)status.textContent=errorMessage(e,'请求未完成，请重试');}
    finally{if(n===serial){busy=false;controls();}}
  }
  launcher.addEventListener('click',()=>{if(dialog.open)return;dialog.showModal();launcher.setAttribute('aria-expanded','true');
    status.textContent=uncertain?'上次生成结果尚未确认，请使用保留的原请求核对。':'使用正式保存的版本。本页未保存的修改不会复制，请先保存需要的修改。';controls();(uncertain?confirm:preview).focus();});
  dialog.querySelector('[data-snippet-draft-close]')!.addEventListener('click',()=>{if(!busy)dialog.close();});
  dialog.addEventListener('cancel',event=>{if(busy)event.preventDefault();});
  dialog.addEventListener('close',()=>{if(dialog.open)return;launcher.setAttribute('aria-expanded','false');const menu=launcher.closest('details');if(menu)menu.open=true;launcher.focus({preventScroll:true});});
  consent.addEventListener('change',controls);
  preview.addEventListener('click',()=>{if(uncertain)return;void run(async n=>{
    reset();result.hidden=true;const version=Number(form.querySelector<HTMLInputElement>('[name="version"]')?.value);
    if(!positive(version))throw Error('请先保存文章并重新打开编辑器');
    status.textContent='正在核对保存版本和共享片段…';
    const csrf=await session();if(n!==serial)return;
    const p=draftPlan(await api(`/api/v1/admin/contents/${id}/snippet-draft/preview`,{post_id:id,version},csrf),id,version);
    if(n!==serial)return;
    pending={post_id:id,version,digest:p.digest,request_key:Array.from(crypto.getRandomValues(new Uint8Array(32)),b=>b.toString(16).padStart(2,'0')).join('')};
    dialog.querySelector<HTMLElement>('[data-snippet-draft-title]')!.textContent=p.title;
    dialog.querySelector<HTMLElement>('[data-snippet-draft-details]')!.textContent=`来源 #${id} · v${version}；${p.occurrences} 处引用。摘要：${p.excerpt||'（空）'}；封面：${p.cover_url||'（无）'}`;
    const refs=dialog.querySelector<HTMLElement>('[data-snippet-draft-references]')!;refs.replaceChildren();
    for(const ref of p.references){const li=document.createElement('li');li.textContent=`${ref.title} · #${ref.id} 正文 v${ref.version}（对象 v${ref.snippet_version}）`;refs.append(li);}
    // Opaque sandbox + restrictive CSP: a preview cannot execute or contact
    // original resource URLs. Exact copied markup is also available as text.
    frame.srcdoc='<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src \'none\'; style-src \'unsafe-inline\'; form-action \'none\'"><style>body{font:16px/1.7 system-ui;margin:16px;overflow-wrap:anywhere}pre{white-space:pre-wrap}img{max-width:100%}table{max-width:100%}</style></head><body>'+p.content_html+'</body></html>';
    dialog.querySelector<HTMLElement>('[data-snippet-draft-source]')!.textContent=p.content_html;
    content.hidden=false;status.textContent='请核对来源、正文和独立副本说明，再确认生成。';
  });});
  confirm.addEventListener('click',()=>{if(!pending||(!uncertain&&!consent.checked))return;void run(async n=>{
    const request=pending!;let sent=false;
    try{
      const csrf=await session();if(n!==serial)return;
      if(!remember())throw Error('浏览器无法保留请求标识，请允许此站点的会话存储后重试；当前输入已保留');
      sent=true;status.textContent='正在生成或核对同一份草稿…';
      const v=await api(`/api/v1/admin/contents/${id}/snippet-draft`,request,csrf);
      if(n!==serial)return;
      if(!record(v)||!positive(v.id)||v.version!==1||v.request_key!==request.request_key||typeof v.replayed!=='boolean'||v.editor_url!==`/admin/content/${v.id}/edit/`)throw Error('生成响应无效，请核对原请求');
      uncertain=false;reset();result.href=v.editor_url;result.hidden=false;result.textContent=`打开独立草稿 #${v.id}（新窗口）`;
      status.textContent=v.replayed?'已核对原请求，草稿只生成一次。原页面输入已保留。':'独立草稿已生成。原页面输入已保留。';result.focus();
    }catch(e){
      if(n!==serial)return;
      const code=record(e)?e.code:undefined;
      if(code==='snippet_draft.result_unavailable'){uncertain=false;reset();status.textContent='原请求已完成，但原草稿已不可读取；本次没有重新生成。';return;}
      // Once any response is uncertain, later permission/version errors do
      // not prove that the original request failed. Keep its identity.
      if(sent&&(uncertain||!['snippet_draft.source_unavailable','snippet_draft.version_conflict','snippet_draft.capacity','snippet_draft.invalid_input','snippet_draft.slug_conflict'].includes(String(code)))){
        uncertain=true;remember();status.textContent='生成结果尚未确认；请保留本页并核对同一次请求。'+errorMessage(e,'');
      }else if(sent){reset();throw e;}else{throw e;}
    }
  });});
  window.addEventListener('beforeunload',event=>{if(uncertain||busy&&pending){event.preventDefault();event.returnValue='';}});
  window.addEventListener('pagehide',()=>{serial++;controller?.abort();if(busy&&pending){uncertain=true;remember();}busy=false;controls();});
})();
