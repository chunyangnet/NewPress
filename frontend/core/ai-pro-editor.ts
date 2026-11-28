import type {} from './composer-types';
import type {} from '../shared/account-types';
import type { EditorTextSelection } from './composer-types';
import { control, required } from './ledger-admin-dom';
import { escapeText } from '../shared/ai-writing-helpers';
import { metadataResult, paragraphResult, writingJSON, writingFailure, writingSession } from '../shared/ai-writing-data';

type SuggestionField = 'excerpt'|'slug'|'tags'|'categoryID';
type Selection = {kind:'field'; field:HTMLTextAreaElement; start:number; end:number; value:string; body:string} | {kind:'document'; selection:EditorTextSelection; body:string};
(() => {
  if (typeof document === 'undefined') return;
  const formNode = document.querySelector<HTMLFormElement>('[data-content-editor]');
  const sourceNode = formNode?.querySelector<HTMLTextAreaElement>('[data-content-source]');
  if (!formNode || !sourceNode) return;
  const form = formNode, source = sourceNode;
  const owner = form.dataset.editorUserId || '', login = form.dataset.editorLogin || '', csrf = control(form,'csrf_token').value;
  let retired = false;
  const requests = new Set<AbortController>(), cleanups: (()=>void)[] = [];
  const alive = () => !retired && form.isConnected;
  const readOnly = () => !alive() || source.disabled || source.readOnly || form.getAttribute('aria-busy') === 'true';
  function retire() {
    if (retired) return; retired = true;
    for (const request of requests) request.abort();
    for (const clear of cleanups) clear();
    form.querySelectorAll<HTMLButtonElement>('[data-pro-preview],[data-pro-apply],[data-paragraph-generate],[data-paragraph-apply]').forEach(button=>{button.disabled=true;});
  }
  document.addEventListener('chunnel:session',event=>{const value=event.detail;if(value?.authenticated!==true||String(value.id)!==owner||value.csrf_token!==csrf)retire();});
  document.addEventListener('chunnel:admin-before-replace',event=>{if(event.detail.regions.some(region=>region.contains(form)))retire();});
  window.addEventListener('pagehide',retire);
  window.addEventListener('pageshow',event=>{if(event.persisted)retire();});
  async function request(path:string,body:unknown,controller:AbortController):Promise<unknown> {
    if(readOnly())throw new Error('当前编辑器不可写，请重新载入。');
    requests.add(controller);const timer=setTimeout(()=>controller.abort(),100000);
    const options:RequestInit={credentials:'same-origin',redirect:'error',cache:'no-store',signal:controller.signal};
    try {
      const session=await writingJSON(await fetch('/api/v1/admin/session',options));
      try{writingSession(session,owner,login,csrf);}catch(error){retire();throw error;}
      if(readOnly()||controller.signal.aborted)throw new DOMException('已取消','AbortError');
      const response=await fetch('/api/v1/admin/plugins/ai-pro-max/'+path,{...options,method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:JSON.stringify(body)});
      if(response.status===401||response.status===403)retire();
      const data=await writingJSON(response);
      if(!alive()||controller.signal.aborted)throw new DOMException('已取消','AbortError');
      return data;
    } finally {clearTimeout(timer);requests.delete(controller);}
  }
  const metadata = form.querySelector<HTMLElement>('[data-pro-metadata]');
  if (metadata) {
    const status=required(metadata,'[data-pro-status]'),results=required(metadata,'[data-pro-results]'),apply=required<HTMLButtonElement>(metadata,'[data-pro-apply]');
    const categoryInputs=()=>[...form.querySelectorAll<HTMLInputElement>('[name="category_ids"]')];
    const current=()=>JSON.stringify([source.value,...['title','slug','tags','excerpt','type'].map(name=>form.elements.namedItem(name)?control(form,name).value:''),categoryInputs().filter(input=>input.checked).map(input=>input.value)]);
    const names:Record<SuggestionField,string>={excerpt:'摘要',slug:'英文别名',tags:'标签',categoryID:'分类'};
    let before='',pending:AbortController|null=null;
    const clear=()=>{pending?.abort();pending=null;results.replaceChildren();apply.hidden=true;};
    cleanups.push(()=>{clear();status.textContent='登录状态或页面已变化，请重新载入。';});
    metadata.addEventListener('change',event=>{if(event.target instanceof Element&&event.target.matches('[data-pro-field],[data-pro-language],[data-pro-model]'))clear();});
    required<HTMLButtonElement>(metadata,'[data-pro-preview]').addEventListener('click',async()=>{
      if(readOnly())return;
      clear();const fields=([...metadata.querySelectorAll<HTMLInputElement>('[data-pro-field]:checked')].map(input=>input.dataset.proField)).filter((key):key is SuggestionField=>key!==undefined&&Object.hasOwn(names,key));
      if(!fields.length||!source.value.trim()){status.textContent='请勾选字段并填写正文。';return;}
      const active=new AbortController();pending=active;before=current();status.textContent='正在生成建议…';
      try {
        const data=metadataResult(await request('metadata',{title:control(form,'title').value,body:source.value,fields,language:required<HTMLSelectElement>(metadata,'[data-pro-language]').value,model:required<HTMLSelectElement>(metadata,'[data-pro-model]').value},active));
        if(pending!==active)return;
        if(before!==current())throw new Error('文章已修改，请重新生成。');
        const values:Record<SuggestionField,string>={excerpt:data.excerpt,slug:data.slug,tags:data.tags.join(', '),categoryID:String(data.categoryID)};
        if(fields.includes('categoryID')&&data.categoryID!==0&&!categoryInputs().some(input=>input.value===values.categoryID))throw new Error('建议分类已不可用，请重新生成。');
        for(const key of fields){
          const label=document.createElement('label');label.textContent=names[key];
          const input=key==='categoryID'?document.createElement('select'):document.createElement('textarea');input.dataset.suggestion=key;
          if(input instanceof HTMLSelectElement){input.append(new Option('保留当前分类',''));for(const category of categoryInputs())input.append(new Option(category.closest('label')?.querySelector('b')?.textContent||category.value,category.value));}
          input.value=key==='categoryID'&&data.categoryID===0?'':values[key];label.append(input);results.append(label);
        }
        status.textContent='可编辑这些建议，再填入文章。';apply.hidden=false;
      }catch(error){if(pending===active&&alive())status.textContent=writingFailure(error).message;}
    });
    apply.addEventListener('click',()=>{
      if(readOnly())return;
      if(before!==current()){clear();status.textContent='文章已修改，请重新生成。';return;}
      for(const item of results.querySelectorAll<HTMLTextAreaElement|HTMLSelectElement>('[data-suggestion]')){
        if(!item.value.trim())continue;
        const key=item.dataset.suggestion;
        if(key==='categoryID'){
          if(!categoryInputs().some(input=>input.value===item.value)){status.textContent='分类已不可用，请重新生成。';return;}
          for(const input of categoryInputs()){input.checked=input.value===item.value;input.dispatchEvent(new Event('change',{bubbles:true}));}
        }else if(key==='excerpt'||key==='slug'||key==='tags'){
          const input=control(form,key);input.value=item.value;input.dispatchEvent(new Event('input',{bubbles:true}));
        }
      }
      clear();status.textContent='字段已填入，保存文章后生效。';
    });
  }
  const paragraph = form.querySelector<HTMLElement>('[data-ai-paragraph]');
  if (paragraph) {
    const status=required(paragraph,'[data-paragraph-status]'),result=required<HTMLTextAreaElement>(paragraph,'[data-paragraph-result]'),apply=required<HTMLButtonElement>(paragraph,'[data-paragraph-apply]'),preview=required(paragraph,'[data-paragraph-preview]');
    const mode=required<HTMLSelectElement>(paragraph,'[data-paragraph-mode]'),language=required<HTMLSelectElement>(paragraph,'[data-paragraph-lang]'),model=required<HTMLSelectElement>(paragraph,'[data-pro-model]');
    let selection:Selection|null=null,pending:AbortController|null=null,suggestion:Selection|null=null;
    const clear=()=>{pending?.abort();pending=null;suggestion=null;apply.hidden=true;preview.hidden=true;result.value='';};
    const capture=(event:Event)=>{
      const target=event.target;let captured:Selection|null;
      if(target instanceof Element&&target.closest('.newpress-document')){
        const text=window.ChunnelBlockEditor?.captureText?.();captured=text?{kind:'document',selection:text,body:text.body}:null;
      }else{
        if(!(target instanceof HTMLTextAreaElement)||!(target===source||target.closest('.admin-block-paragraph,.admin-block-quote,.admin-block-callout,.admin-block-h2,.admin-block-h3')))return;
        captured=target.selectionStart===target.selectionEnd?null:{kind:'field',field:target,start:target.selectionStart,end:target.selectionEnd,value:target.value,body:source.value};
      }
      const position=(value:Selection|null)=>value?.kind==='field'?[value.field,value.start,value.end,value.value,value.body]:value?.kind==='document'?[value.selection.from,value.selection.to,value.selection.text,value.body]:[];
      const a=position(captured),b=position(selection);if(a.length!==b.length||a.some((value,index)=>value!==b[index]))clear();selection=captured;
    };
    for(const event of ['select','keyup','pointerup'])form.addEventListener(event,capture);
    for(const field of [mode,language,model])field.addEventListener('change',clear);
    const stillCurrent=(value:Selection|null):value is Selection=>!!value&&!readOnly()&&source.value===value.body&&(value.kind==='field'?value.field.isConnected&&!value.field.disabled&&!value.field.readOnly&&value.field.value===value.value:!!window.ChunnelBlockEditor?.replaceText);
    required<HTMLButtonElement>(paragraph,'[data-paragraph-generate]').addEventListener('click',async()=>{
      const saved=selection;if(!stillCurrent(saved)){status.textContent='请重新选中一段正文。';return;}
      const text=saved.kind==='field'?saved.value.slice(saved.start,saved.end):saved.selection.text;
      if(saved.kind==='field'&&saved.field===source&&/[<>]/.test(text)){status.textContent='请只选择文字，保留两侧 HTML 标签。';return;}
      clear();const active=new AbortController();pending=active;status.textContent='正在优化选区…';
      try{
        const replacement=paragraphResult(await request('paragraph',{text,mode:mode.value,language:language.value,model:model.value},active));
        if(pending!==active)return;
        if(!stillCurrent(saved))throw new Error('原文已更改，请重新选择。');
        suggestion=saved;result.value=replacement;preview.hidden=false;apply.hidden=false;status.textContent='可先编辑建议，再替换选中的文字。';
      }catch(error){if(pending===active&&alive())status.textContent=writingFailure(error).message;}
    });
    apply.addEventListener('click',()=>{
      const saved=suggestion;
      if(!stillCurrent(saved)){status.textContent='原文已更改，请重新选择。';apply.hidden=true;return;}
      if(saved.kind==='field'){
        saved.field.setRangeText(saved.field===source?escapeText(result.value):result.value,saved.start,saved.end,'end');saved.field.dispatchEvent(new Event('input',{bubbles:true}));
      }else if(!window.ChunnelBlockEditor?.replaceText?.(result.value,saved.selection)){
        status.textContent='原文已更改或建议过长，请重新选择。';apply.hidden=true;return;
      }
      clear();selection=null;status.textContent='已替换选区，保存文章后生效。';
    });
    cleanups.push(()=>{clear();selection=null;status.textContent='登录状态或页面已变化，请重新载入。';});
  }
})();
