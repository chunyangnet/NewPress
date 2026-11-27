import {record,responseError,errorMessage} from './editor-tool-data';
import type {EditorSourceSelection} from './media-types';
import './editor-snippet-draft';

(() => {
  const form=document.querySelector<HTMLFormElement>('[data-content-editor]');
  const sourceField=form?.querySelector<HTMLTextAreaElement>('[data-content-source]');
  const launcher=form?.querySelector<HTMLButtonElement>('[data-editor-snippets-open]');
  const candidate=document.querySelector<HTMLDialogElement>('[data-editor-snippets-dialog]');
  const blockEditor=window.ChunnelBlockEditor;
  if(!sourceField||!launcher||!candidate||!blockEditor)return;
  const source=sourceField,trigger=launcher,dialog=candidate,editor=blockEditor;
  const status=dialog.querySelector<HTMLElement>('[data-snippet-picker-status]')!,list=dialog.querySelector<HTMLElement>('[data-snippet-picker-list]')!;
  const previous=dialog.querySelector<HTMLButtonElement>('[data-snippet-picker-prev]')!,next=dialog.querySelector<HTMLButtonElement>('[data-snippet-picker-next]')!;
  let selection:EditorSourceSelection|null=null,page=1,pages=1,busy=false,serial=0,returnFocus=true;
  let controller:AbortController|null=null;
  const positive=(v:unknown):v is number=>Number.isSafeInteger(v)&&Number(v)>0;
  const controls=()=>{dialog.setAttribute('aria-busy',String(busy));previous.disabled=busy||page<=1;next.disabled=busy||page>=pages;list.querySelectorAll('button').forEach(b=>{b.disabled=busy;});};
  async function request(url:string,handle:(v:unknown)=>void){
    if(busy)return;busy=true;controls();const n=++serial;controller=new AbortController();const c=controller,timer=setTimeout(()=>c.abort(),15000);
    try{const response=await fetch(url,{credentials:'same-origin',cache:'no-store',redirect:'error',signal:c.signal,headers:{Accept:'application/json'}});const payload:unknown=await response.json();if(n!==serial||!dialog.open)return;if(!response.ok)throw Error(responseError(payload,'片段暂时不可用'));handle(payload);}catch(e){if(n===serial&&dialog.open)status.textContent=errorMessage(e,'请求中断，请重试；正文保持不变');}finally{clearTimeout(timer);if(n===serial){busy=false;controls();}}
  }
  function load(number:number){
    status.textContent='正在读取共享片段…';
    void request(`/api/v1/admin/snippets?page=${number}`,v=>{
      if(!record(v)||!Array.isArray(v.items)||!positive(v.page)||!positive(v.pages))throw Error('片段列表无效');
      const fragment=document.createDocumentFragment();let count=0;
      for(const item of v.items){
        if(!record(item)||!positive(item.id)||!positive(item.version)||!positive(item.current_version)||typeof item.title!=='string')throw Error('片段记录无效');
        if(item.visibility!=='shared')continue;
        count++;const row=document.createElement('article'),text=document.createElement('div'),title=document.createElement('h3'),meta=document.createElement('small'),button=document.createElement('button');
        title.textContent=item.title;meta.textContent=`#${item.id} · 固定正文 v${item.current_version}`;text.append(meta,title);
        button.type='button';button.className='admin-button';button.textContent='插入引用';button.setAttribute('aria-label',`插入「${item.title}」的固定版本引用`);
        button.addEventListener('click',()=>{void request(`/api/v1/admin/snippets/${item.id}?view=summary`,current=>{
          if(!record(current)||current.id!==item.id||current.version!==item.version||current.current_version!==item.current_version||current.visibility!=='shared')throw Error('片段已经变化，请重新载入后插入');
          if(!selection||source.disabled||source.readOnly||source.value!==selection.value)throw Error('正文或编辑权限已变化，请重新选择插入位置');
          editor.insert(`\n<aside data-np-snippet="${item.id}" data-np-snippet-version="${item.current_version}"></aside>\n`,selection);returnFocus=false;dialog.close();
        });});row.append(text,button);fragment.append(row);
      }
      list.replaceChildren(fragment);page=v.page;pages=v.pages;dialog.querySelector<HTMLElement>('[data-snippet-picker-page]')!.textContent=`${page} / ${pages}`;
      status.textContent=count?'插入的是固定版本引用；来源撤回后，公开文章中的片段会隐藏。':'这一页没有已共享的片段。';
    });
  }
  trigger.addEventListener('pointerdown',e=>e.preventDefault());
  trigger.addEventListener('click',()=>{if(source.disabled||source.readOnly||dialog.open)return;selection=editor.capture();returnFocus=true;dialog.showModal();trigger.setAttribute('aria-expanded','true');load(1);});
  dialog.querySelector('[data-snippet-picker-close]')!.addEventListener('click',()=>dialog.close());
  dialog.addEventListener('close',()=>{serial++;controller?.abort();busy=false;selection=null;trigger.setAttribute('aria-expanded','false');controls();if(returnFocus)trigger.focus({preventScroll:true});});
  previous.addEventListener('click',()=>load(page-1));next.addEventListener('click',()=>load(page+1));
  window.addEventListener('pagehide',()=>{serial++;controller?.abort();busy=false;controls();if(dialog.open)status.textContent='页面切换中断了读取，请重新打开片段选择器。';});
})();
