import './seo-editor';
import { errorMessage, errorName, seoSources } from './editor-tool-data';
import { make, readJSON } from './seo-dom';

(() => {
  'use strict';
  const picker=document.querySelector<HTMLElement>('[data-seo-target-picker]');
  if (picker) {
    const form=picker.querySelector('form')!;
    const status=picker.querySelector<HTMLElement>('[data-seo-source-status]')!;
    const list=picker.querySelector<HTMLElement>('[data-seo-source-results]')!;
    const previous=picker.querySelector<HTMLButtonElement>('[data-seo-source-prev]')!;
    const next=picker.querySelector<HTMLButtonElement>('[data-seo-source-next]')!;
    const labels: Record<string, string>={publish:'已发布',future:'定时',draft:'草稿',pending:'待审核',private:'私密',trash:'回收站'};
    let page=1,query='',sequence=0,controller: AbortController | null=null;
    async function search() {
      const current=++sequence;controller?.abort();controller=new AbortController();
      status.textContent='正在查找内容…';list.replaceChildren();previous.hidden=true;next.hidden=true;
      try {
        const payload=await readJSON(`/api/v1/admin/seo/sources?q=${encodeURIComponent(query)}&page=${page}`,controller.signal);
        if(current!==sequence)return;
        const result=seoSources(payload);
        status.textContent=result.total ? `${result.total} 项 · 第 ${result.page} / ${result.pages} 页` : '没有匹配的内容，试试更短的关键词。';
        for(const item of result.items) {
          const button=make('button');button.type='button';button.disabled=picker!.dataset.readOnly==='true';button.append(make('strong',item.title),make('small',`#${item.id} · ${item.type==='page'?'页面':'文章'} · ${labels[item.status] || item.status}`));
          button.addEventListener('click',()=>{const target=document.querySelector<HTMLInputElement>('#seo-target-id');if(!target||target.disabled||target.readOnly||picker!.dataset.readOnly==='true')return;target.value=String(item.id);target.dispatchEvent(new Event('input',{bubbles:true}));document.querySelector('[data-seo-selected-target]')!.textContent=`已选择：${item.title}（#${item.id}）`;});
          list.append(button);
        }
        previous.hidden=page<=1;next.hidden=page>=result.pages;
      } catch(error) {if(current===sequence)status.textContent=errorName(error)==='AbortError'?'查找超时，请重新查找。':errorMessage(error,'查找失败，请重试。');}
      finally {if(current===sequence)controller=null;}
    }
    form.addEventListener('submit',event=>{event.preventDefault();query=form.querySelector<HTMLInputElement>('[name="q"]')!.value.trim();page=1;void search();});
    previous.addEventListener('click',()=>{if(page>1){page--;void search();}});next.addEventListener('click',()=>{page++;void search();});
    window.addEventListener('pagehide',()=>{sequence++;if(controller){controller.abort();controller=null;status.textContent='页面切换中断了查找，请重新查找。';}});
  }
})();
