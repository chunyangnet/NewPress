import { errorMessage, errorName, responseError, linkReport, seoSources } from './editor-tool-data';

(() => {
  'use strict';
  const make = <K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, className?: string) => {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = text;
    if (className) node.className = className;
    return node;
  };
  async function readJSON(url: string, signal?: AbortSignal): Promise<unknown> {
    const controller=new AbortController();
    const abort=()=>controller.abort();
    if(signal?.aborted)abort();else signal?.addEventListener('abort',abort,{once:true});
    const timeout=setTimeout(abort,15000);
    try {
      const response = await fetch(url, {credentials:'same-origin',cache:'no-store',headers:{Accept:'application/json'},signal:controller.signal});
      let body: unknown;
      try {body=await response.json();} catch(error) {if(controller.signal.aborted)throw error;throw new Error('响应格式不正确，请重新登录后重试。');}
      if (controller.signal.aborted) throw new DOMException('Request interrupted', 'AbortError');
      if (!response.ok) throw new Error(responseError(body, '暂时无法读取，请重试。'));
      return body;
    } finally {clearTimeout(timeout);signal?.removeEventListener('abort',abort);}
  }
  const preview = document.querySelector<HTMLElement>('[data-seo-preview]');
  const previewForm = preview?.closest('form');
  if (preview && previewForm) {
    const form = previewForm;
    const value = (name: string) => form.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[name="${name}"]`)?.value.trim() || '';
    const data = preview.dataset;
    const base = data.publicUrl || location.origin;
    const imageBox = preview.querySelector<HTMLElement>('[data-seo-preview-image]')!;
    let imageURL: string | null = null, timer: ReturnType<typeof setTimeout> | undefined;
    function plainHTML(source: string) {
      // Template contents stay inert and are never inserted into the document.
      const inert = document.createElement('template');
      inert.innerHTML = source;
      inert.content.querySelectorAll('script,style,template,noscript').forEach(node => node.remove());
      inert.content.querySelectorAll('address,article,aside,blockquote,br,dd,div,dl,dt,figcaption,figure,footer,h1,h2,h3,h4,h5,h6,header,hr,li,main,nav,ol,p,pre,section,table,td,th,tr,ul').forEach(node=>{node.before(' ');node.after(' ');});
      return (inert.content.textContent || '').replace(/\s+/g,' ').trim();
    }
    function normalizeSlug(raw: string) {
      let slug='',separator=false;
      for(const ch of raw.toLowerCase()) {
        if(/[a-z0-9]/.test(ch)){if(separator&&slug)slug+='-';separator=false;if(slug.length<128)slug+=ch;}
        else if(/[-_\s]/u.test(ch))separator=slug.length>0;
        if(slug.length>=128)break;
      }
      return slug.replace(/-$/,'');
    }
    function update() {
      const title = value('title') || '文章标题';
      const fullTitle = `${title} ${data.separator} ${data.siteTitle}`;
      const plain=Array.from(plainHTML(value('content_source')));
      const excerpt = value('excerpt') || (plain.slice(0,180).join('').trim()+(plain.length>180?'…':'')) || data.defaultDescription || '添加摘要，让读者更快理解这篇内容。';
      const slug = normalizeSlug(value('slug')) || normalizeSlug(title) || 'your-article';
      const prefix = value('type') === 'page' ? 'pages' : 'posts';
      const style = value('type') === 'page' ? form.dataset.pagePermalink : form.dataset.postPermalink;
      const date = value('published_at').slice(0,10).replaceAll('-','/') || form.dataset.permalinkDate || '';
      const part = style === 'id' ? `id/${Number(form.dataset.permalinkId) > 0 ? form.dataset.permalinkId : '保存后分配'}` : style === 'month' ? `${date.slice(0,7)}/${slug}` : style === 'date' ? `${date}/${slug}` : slug;
      const currentURL = `${base.replace(/\/$/,'')}/${prefix}/${part}`;
      preview!.querySelectorAll('[data-seo-preview-title]').forEach(node => {node.textContent=fullTitle;});
      preview!.querySelectorAll('[data-seo-preview-description]').forEach(node => {node.textContent=excerpt;});
      preview!.querySelector('[data-seo-preview-url]')!.textContent=currentURL;
      preview!.querySelector('[data-seo-preview-count]')!.textContent=`标题 ${Array.from(fullTitle).length} 字符 · 摘要 ${Array.from(excerpt).length} 字符`;
      const cover=value('cover_url');
      let src=(cover===data.initialCover ? data.initialImage : (cover || data.defaultImage)) || '';
      try { const u=new URL(src,currentURL); src=src && u.origin===location.origin && ['http:','https:'].includes(u.protocol) && !u.username && !u.password ? u.href : ''; } catch {src='';}
      if (src!==imageURL) {
        imageURL=src;
        imageBox.replaceChildren();
        if (src) {const img=make('img');img.alt='分享封面预览';img.loading='lazy';img.src=src;img.addEventListener('error',()=>{if(imageURL===src)imageBox.replaceChildren(make('span','封面暂不可用'));},{once:true});imageBox.append(img);}
        else imageBox.append(make('span',data.siteTitle));
      }
    }
    const schedule = () => {clearTimeout(timer);timer=setTimeout(update,150);};
    form.addEventListener('input',schedule);form.addEventListener('change',schedule);form.addEventListener('reset', () => queueMicrotask(schedule));update();
  }
  document.querySelectorAll<HTMLElement>('[data-seo-link-check]').forEach(panel => {
    const run=panel.querySelector<HTMLButtonElement>('[data-seo-check-run]')!;
    const status=panel.querySelector<HTMLElement>('[data-seo-check-status]')!;
    const summary=panel.querySelector<HTMLElement>('[data-seo-check-summary]')!;
    const results=panel.querySelector<HTMLElement>('[data-seo-check-results]')!;
    const labels: Record<string, string>={ok:'可用',redirect:'需更新',broken:'需修复',unchecked:'未检查'};
    let pending: AbortController | null = null, sequence = 0;
    run.addEventListener('click',async()=>{
      if (pending || run.matches(':disabled')) return;
      const id=panel.dataset.postId || panel.querySelector<HTMLInputElement>('[data-seo-check-id]')?.value;
      if (!/^[1-9]\d*$/.test(id || '')) {status.textContent='请输入有效的内容编号。';status.classList.add('is-error');return;}
      run.disabled=true;panel.setAttribute('aria-busy','true');status.classList.remove('is-error');status.textContent='正在检查已保存正文中的引用…';summary.replaceChildren();results.replaceChildren();
      const controller=new AbortController();pending=controller;const current=++sequence;
      const timeout=setTimeout(()=>controller.abort(),15000);
      try {
        const payload=await readJSON(`/api/v1/admin/contents/${id}/links`,controller.signal);
        if (current !== sequence) return;
        const report=linkReport(payload);
        if (String(report.post_id) !== id) throw new Error('链接检查内容编号不一致，请重试。');
        status.textContent=`内容 #${report.post_id} · 正式版本 v${report.version} · 已检查 ${report.items.length} 个不同链接${report.truncated ? '，达到 200 项上限，后续链接尚未检查' : ''}。${report.items.length ? '' : '正文和封面没有可检查的链接。'}`;
        Object.entries(labels).forEach(([key,label])=>summary.append(make('span',`${label} ${report.counts[key] || 0}`)));
        for (const item of report.items) {
          const row=make('div',undefined,'seo-link-result');row.dataset.status=item.status;
          row.append(make('span',labels[item.status] || '未检查'));
          const body=make('div');body.append(make('code',item.url),make('small',item.message));
          if(item.target)body.append(make('small',`当前地址：${item.target}`));
          row.append(body);results.append(row);
        }
      } catch(error) {if(current===sequence){status.textContent=errorName(error)==='AbortError' ? '检查超时，请稍后重试。' : errorMessage(error,'检查失败，请重试。');status.classList.add('is-error');}}
      finally {clearTimeout(timeout);if(current===sequence){pending=null;run.disabled=false;panel.removeAttribute('aria-busy');}}
    });
    window.addEventListener('pagehide', () => { sequence++; if(pending){pending.abort();pending=null;run.disabled=false;panel.removeAttribute('aria-busy');status.textContent='页面切换中断了检查，请重试。';} });
  });
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
