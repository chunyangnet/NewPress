(() => {
  'use strict';
  const form = document.querySelector('[data-content-editor]');
  const source = form?.querySelector('[data-content-source]');
  if (!source) return;
  const escapeText = value => String(value ?? '').replace(/[&<>"']/g, char => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[char]));
  const metadata = form.querySelector('[data-pro-metadata]');
  if (metadata) {
    const get = name => metadata.querySelector('[data-pro-' + name + ']');
    const categoryInputs = () => [...form.querySelectorAll('[name="category_ids"]')];
    const current = () => JSON.stringify([source.value,...['title','slug','tags','excerpt','type'].map(k => form.elements[k]?.value),categoryInputs().filter(x=>x.checked).map(x=>x.value)]);
    const names={excerpt:'摘要',slug:'英文别名',tags:'标签',categoryID:'分类'};
    let before = '', request = null;
    const clear = () => { request?.abort();request=null;get('results').replaceChildren();get('apply').hidden=true; };
    metadata.addEventListener('change',e=>{if(e.target.matches('[data-pro-field],[data-pro-language],[data-pro-model]'))clear();});
    get('preview').addEventListener('click',async()=>{
      clear();const fields=[...metadata.querySelectorAll('[data-pro-field]:checked')].map(x=>x.dataset.proField);
      if(!fields.length||!source.value.trim()){get('status').textContent='请勾选字段并填写正文。';return;}
      const active=new AbortController();request=active;before=current();get('status').textContent='正在生成建议…';
      try {
        const response=await fetch('/api/v1/admin/plugins/ai-pro-max/metadata',{method:'POST',credentials:'same-origin',signal:active.signal,headers:{'Content-Type':'application/json','X-CSRF-Token':form.elements.csrf_token.value},body:JSON.stringify({title:form.elements.title.value,body:source.value,fields,language:get('language').value,model:get('model').value})});const result=await response.json();if(request!==active)return;
        if(!response.ok)throw new Error(result.error?.message||'无法生成建议。');
        const values={excerpt:result.Excerpt,slug:result.Slug,tags:(result.Tags||[]).join(', '),categoryID:String(result.CategoryID)};
        for(const f of fields){const label=document.createElement('label');label.textContent=names[f];const input=document.createElement(f==='categoryID'?'select':'textarea');input.dataset.suggestion=f;
          if(f==='categoryID'){input.append(new Option('保留当前分类',''));for(const c of categoryInputs())input.append(new Option(c.closest('label')?.querySelector('b')?.textContent||c.value,c.value));}
          input.value=values[f]||'';label.append(input);get('results').append(label);}
        get('status').textContent='可编辑这些建议，再填入文章。';get('apply').hidden=false;
      }catch(error){if(request===active)get('status').textContent=error.message;}
    });
    get('apply').addEventListener('click',()=>{
      if(before!==current()){clear();get('status').textContent='文章已修改，请重新生成。';return;}
      for(const item of get('results').querySelectorAll('[data-suggestion]')){if(!item.value.trim())continue;const key=item.dataset.suggestion;if(key==='categoryID'){for(const c of categoryInputs()){c.checked=c.value===item.value;c.dispatchEvent(new Event('change',{bubbles:true}));}}else{const input=form.elements[key];if(input){input.value=item.value;input.dispatchEvent(new Event('input',{bubbles:true}));}}}
      clear();get('status').textContent='字段已填入，保存文章后生效。';
    });window.addEventListener('pagehide',clear);
  }
  const paragraph = form.querySelector('[data-ai-paragraph]');
  if (paragraph) {
    const find = name => paragraph.querySelector('[data-paragraph-' + name + ']');
    const status = find('status'), result = find('result'), apply = find('apply');
    let selection = null, pending = null, suggestion = null;
    const capture = event => {
      const field = event.target;
      if (field instanceof Element && field.closest('.newpress-document')) {
        selection = window.ChunnelBlockEditor?.captureText?.() || null;
        return;
      }
      if (!(field instanceof HTMLTextAreaElement) || !(field === source || field.closest('.admin-block-paragraph,.admin-block-quote,.admin-block-callout,.admin-block-h2,.admin-block-h3'))) return;
      const start = field.selectionStart, end = field.selectionEnd;
      if (start === end) { selection = null; return; }
      selection = {field, start, end, value:field.value, body:source.value};
    };
    for (const event of ['select', 'keyup', 'pointerup']) form.addEventListener(event, capture);
    const stillCurrent = s => s && source.value === s.body && !source.disabled &&
      (s.field ? s.field.isConnected && s.field.value === s.value : typeof s.text === 'string' && !!window.ChunnelBlockEditor?.replaceText);
    find('generate').addEventListener('click', async () => {
      const s = selection;
      if (!stillCurrent(s)) { status.textContent = '请重新选中一段正文。'; return; }
      const input = s.field ? s.value.slice(s.start,s.end) : s.text;
      if (s.field === source && /[<>]/.test(input)) { status.textContent = '请只选择文字，保留两侧 HTML 标签。'; return; }
      pending?.abort(); const request = new AbortController(); pending=request;
      apply.hidden=true; find('preview').hidden=true; status.textContent='正在优化选区…';
      try {
        const response=await fetch('/api/v1/admin/plugins/ai-pro-max/paragraph',{method:'POST',credentials:'same-origin',signal:request.signal,headers:{'Content-Type':'application/json','X-CSRF-Token':form.elements.csrf_token.value},body:JSON.stringify({text:input,mode:find('mode').value,language:find('lang').value,model:paragraph.querySelector('[data-pro-model]').value})});
        const data=await response.json(); if(pending!==request)return;
        if(!response.ok)throw new Error(data.error?.message||'生成失败，请重试。');
        if(!stillCurrent(s))throw new Error('原文已更改，请重新选择。');
        suggestion=s;result.value=data.text;find('preview').hidden=false;apply.hidden=false;status.textContent='可先编辑建议，再替换选中的文字。';
      } catch(error) { if(pending===request)status.textContent=error.message; }
    });
    apply.addEventListener('click',() => {
      if (!stillCurrent(suggestion)) { status.textContent='原文已更改，请重新选择。';apply.hidden=true;return; }
      if (suggestion.field) {
        const {field,start,end}=suggestion;
        field.setRangeText(field===source?escapeText(result.value):result.value,start,end,'end');field.dispatchEvent(new Event('input',{bubbles:true}));
      } else if (!window.ChunnelBlockEditor.replaceText(result.value, suggestion)) {
        status.textContent='原文已更改或建议过长，请重新选择。';apply.hidden=true;return;
      }
      status.textContent='已替换选区，保存文章后生效。';apply.hidden=true;find('preview').hidden=true;suggestion=selection=null;
    });
    window.addEventListener('pagehide',()=>pending?.abort());
  }
})();
