import {required,field as valueField,pageRequests,failure} from './collection-runtime';
import {seriesStatuses,seriesSources,seriesReceipt} from './collection-data';
import type {SeriesChapter,SeriesInput} from './collection-data';
(() => {
  'use strict';
  const page = document.querySelector<HTMLFormElement>('[data-series-editor]');
  if (!page) return;
  const form=page;
  const $ = <T extends Element = HTMLElement>(selector: string): T => required<T>(form,selector);
  const field = (name: string) => valueField(form,name);
  const list = $('[data-series-chapters]');
  const state = form.querySelector<HTMLElement>('[data-series-save-state]') || $('[data-save-state]');
  const status = $('[data-series-status]');
  const labels = seriesStatuses;
  let chapters: SeriesChapter[] = [...list.querySelectorAll<HTMLElement>('[data-series-chapter]')].map(node => ({id:Number(node.dataset.id), title:required(node,'[data-series-chapter-title]').textContent || '', status:node.dataset.status || 'draft'}));
  let id = Number(form.dataset.id), version = Number(form.dataset.version), dirty = false, busy = false;
  let searchSequence = 0, searchController: AbortController | undefined, sourcePage = 1, sourceQuery = '', sources: SeriesChapter[] = [], dragID = 0;
  const requests=pageRequests(Number(form.dataset.userId),()=>{busy=true;searchSequence++;searchController?.abort();$<HTMLFieldSetElement>('.series-editor-fields').disabled=true;status.textContent='登录状态已变化，当前输入已保留，请重新打开页面。';});
  const readOnly = form.dataset.readOnly === 'true';
  const el = <K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, className?: string): HTMLElementTagNameMap[K] => { const node=document.createElement(tag); if(text!==undefined)node.textContent=text; if(className)node.className=className; return node; };
  function message(text: string, error=false) { status.textContent=text; status.classList.toggle('is-error',error); }
  let chapterBaseline = chapters.map(chapter => ({...chapter}));
  const selection = $<HTMLInputElement>('[data-series-selection]');
  if (selection) selection.value = JSON.stringify(chapters.map(chapter => chapter.id));
  const controller = window.ChunnelAdminUI?.savebar(form, {allowClean: !id, onDiscard: () => { chapters = chapterBaseline.map(chapter => ({...chapter})); dirty = false; renderChapters(); renderSources(); }});
  function changed() { if (selection) selection.value = JSON.stringify(chapters.map(chapter => chapter.id)); dirty = controller ? controller.dirty() : true; if (controller) controller.refresh(); else state.textContent = '未保存'; }
  function setBusy(value: boolean) { busy=value||requests.lost; $<HTMLFieldSetElement>('.series-editor-fields').disabled=busy||readOnly; form.setAttribute('aria-busy',String(value)); controller?.refresh(true); const save = form.querySelector<HTMLButtonElement>('[data-admin-save]'); if (save && value) save.disabled = true; }
  function etag() { return `"series-${id}-v${version}"`; }
  const api = requests.request;
  function move(chapterID: number, index: number) {
    if(busy||readOnly)return;
    const from=chapters.findIndex(item=>item.id===chapterID);
    if(from<0||index<0||index>=chapters.length||from===index)return;
    const item=chapters.splice(from,1)[0];chapters.splice(index,0,item!);changed();renderChapters();
    list.querySelector<HTMLElement>(`[data-series-chapter="${chapterID}"]`)?.focus();
  }
  function renderChapters() {
    list.replaceChildren();
    if(!chapters.length)list.append(el('li','还没有文章。在下方查找并添加第一篇。','series-empty'));
    chapters.forEach((item,index)=>{
      const row=el('li');row.dataset.seriesChapter=String(item.id);row.draggable=!readOnly;row.tabIndex=0;
      row.append(el('span',String(index+1).padStart(2,'0'),'series-chapter-number'));
      const description=el('div');description.append(el('strong',item.title),el('small',labels[item.status]||'文章'));row.append(description);
      const actions=el('div',undefined,'series-chapter-actions');
      for(const [label,text,action,disabled] of [
        ['上移','↑',()=>move(item.id,index-1),index===0],['下移','↓',()=>move(item.id,index+1),index===chapters.length-1],
        ['移除','×',()=>{if(busy||readOnly)return;chapters=chapters.filter(entry=>entry.id!==item.id);changed();renderChapters();renderSources();},false]
      ] as [string,string,()=>void,boolean][]) { const button=el('button',text);button.type='button';button.setAttribute('aria-label',`${label}：${item.title}`);button.disabled=disabled||readOnly;button.addEventListener('click',action);actions.append(button); }
      row.append(actions);
      row.addEventListener('keydown',event=>{if(event.altKey&&['ArrowUp','ArrowDown'].includes(event.key)){event.preventDefault();move(item.id,index+(event.key==='ArrowUp'?-1:1));}});
      row.addEventListener('dragstart',event=>{if(busy||readOnly){event.preventDefault();return;}dragID=item.id;event.dataTransfer!.effectAllowed='move';event.dataTransfer!.setData('text/plain',String(item.id));row.classList.add('is-dragging');});
      row.addEventListener('dragover',event=>{if(dragID&&!busy&&!readOnly)event.preventDefault();});
      row.addEventListener('drop',event=>{if(dragID){event.preventDefault();const source=dragID;dragID=0;move(source,index);}});
      row.addEventListener('dragend',()=>{dragID=0;row.classList.remove('is-dragging');});
      list.append(row);
    });
    $('[data-series-count]').textContent=`${chapters.length} / 200 篇`;
  }
  function renderSources() {
    const container=$('[data-series-sources]');container.replaceChildren();
    for(const item of sources) {
      const row=el('div'),copy=el('div');copy.append(el('strong',item.title),el('small',labels[item.status]||'文章'));row.append(copy);
      const selected=chapters.some(chapter=>chapter.id===item.id);
      const button=el('button',selected?'已添加':'添加','admin-button');button.type='button';button.disabled=selected||chapters.length>=200||readOnly;
      button.setAttribute('aria-label',`${selected?'已添加':'添加'}：${item.title}`);
      button.addEventListener('click',()=>{if(busy||readOnly||chapters.length>=200||chapters.some(chapter=>chapter.id===item.id))return;chapters.push({...item});changed();renderChapters();renderSources();message(`已添加《${item.title}》。`);});
      row.append(button);container.append(row);
    }
  }
  async function search(page=1) {
    if(busy||readOnly)return;
    const sequence=++searchSequence;searchController?.abort();searchController=new AbortController();
    if(page===1)sourceQuery=$<HTMLInputElement>('[data-series-search]').value.trim();
    const pickerStatus=$('[data-series-picker-status]');pickerStatus.textContent='正在查找文章…';
    try {
      const result=seriesSources(await api(`/api/v1/admin/series/sources?q=${encodeURIComponent(sourceQuery)}&page=${page}`,{signal:searchController.signal}),page);
      if(sequence!==searchSequence)return;
      sourcePage=page;sources=result.items;renderSources();pickerStatus.textContent=sources.length?`共 ${result.total} 篇 · 第 ${page} 页`:'没有找到文章，换一个关键词试试。';
      $('[data-series-source-prev]').hidden=page<=1;$('[data-series-source-next]').hidden=!result.has_more;
    } catch(problem) { const error=failure(problem);if(requests.lost)return; if(sequence===searchSequence&&error.name!=='AbortError')pickerStatus.textContent=error.message; }
  }
  form.addEventListener('input',event=>{if(!busy&&(event.target instanceof Element && event.target.matches('[name]:not([type=hidden])')))changed();});
  form.addEventListener('change',event=>{if(!busy&&(event.target instanceof Element && event.target.matches('select[name]')))changed();});
  $('[data-series-find]').addEventListener('click',()=>search());
  $('[data-series-search]').addEventListener('keydown',event=>{if(event.key==='Enter'&&!event.isComposing&&event.keyCode!==229){event.preventDefault();search();}});
  $('[data-series-source-prev]').addEventListener('click',()=>search(sourcePage-1));
  $('[data-series-source-next]').addEventListener('click',()=>search(sourcePage+1));
  form.querySelector<HTMLButtonElement>('[data-series-reload]')?.addEventListener('click',()=>{if(!dirty||confirm('重新载入会放弃当前尚未保存的更改，继续吗？')){dirty=false;location.reload();}});
  window.addEventListener('beforeunload',event=>{if(dirty){event.preventDefault();event.returnValue='';}});
  form.addEventListener('submit',async event=>{
    event.preventDefault();if(busy||readOnly)return;
    const input: SeriesInput={post_ids:chapters.map(item=>item.id),kind:'',status:'',title:'',slug:'',description:'',cover_url:''};
    for(const name of ['kind','status','title','slug','description','cover_url'] as const)input[name]=field(name).value;
    if (controller && !controller.start()) return;
    setBusy(true);if (!controller) message('正在保存…');
    try {
      const raw=await api(`/api/v1/admin/series${id?'/'+id:''}`,{method:id?'PUT':'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':field('csrf_token').value,...(id?{'If-Match':etag()}:{})},body:JSON.stringify(input)});
      const saved=seriesReceipt(raw,id,version,input);
      const created=!id;id=saved.id;version=saved.version;dirty=false;chapterBaseline = chapters.map(chapter => ({...chapter}));controller?.success();if (!controller) { state.textContent='已保存';message('已保存'); }
      if(created)location.assign(`/admin/series/?edit=${id}`);
    } catch(problem) { const error=failure(problem);if(requests.lost)return;if (controller) controller.fail(error.message); else message(error.message,true);}
    finally {setBusy(false);}
  });
  form.querySelector<HTMLButtonElement>('[data-series-delete]')?.addEventListener('click',async()=>{
    if(busy||readOnly||!confirm('删除这份选集和目录？其中的文章会保留。'))return;
    setBusy(true);message('正在删除…');
    try {const result=await api(`/api/v1/admin/series/${id}`,{method:'DELETE',headers:{'X-CSRF-Token':field('csrf_token').value,'If-Match':etag()}});if(result!==null)throw new Error('删除结果未确认，请重新载入核对。');dirty=false;location.assign('/admin/series/');}
    catch(problem){const error=failure(problem);if(requests.lost)return;message(error.message,true);setBusy(false);}
  });
  renderChapters();if(!readOnly)search();
})();
