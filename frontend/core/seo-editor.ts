import { errorMessage, errorName, linkReport } from './editor-tool-data';
import { make, readJSON } from './seo-dom';

(() => {
  'use strict';
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
})();
