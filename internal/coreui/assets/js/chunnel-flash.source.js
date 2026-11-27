(() => {
  const status = document.querySelector('[data-flash-status]');
  const tell = text => { if (status) status.textContent = text; };
  const request = async (url, options) => {
    const response = await fetch(url, { credentials: 'same-origin', ...options });
    const data = await response.json();
    if (!response.ok) {
      const error = new Error(data.message || data.error?.message || '操作未完成，请稍后重试');
      error.details = data.error?.details;
      throw error;
    }
    return data;
  };
  let session;
  const identity = async () => {
    session = await request('/api/v1/social/session');
    if (!session.authenticated) throw new Error('请先登录后操作，可从个人中心进入登录页面。');
    return session;
  };
  const form = document.querySelector('[data-flash-editor]');
  if (form) {
    let pending = null;
    const publishedZone = form.querySelector('[name="published_timezone"]');
    const timezoneReview = form.querySelector('[data-flash-timezone-review]');
    let reviewZone = '';
    timezoneReview?.querySelector('button').addEventListener('click', () => {
      if (!publishedZone || !reviewZone) return;
      publishedZone.value = reviewZone;
      timezoneReview.hidden = true;
      tell('已确认当前时区，请核对发布时间后重新保存。');
    });
    form.addEventListener('submit', async event => {
      event.preventDefault();
      if (form.dataset.busy) return;
      const action = event.submitter?.value || 'save';
      if (['trash', 'unpublish', 'reject'].includes(action) && !window.confirm('确认执行此操作？')) return;
      const f = new FormData(form);
      const body = {expected_user_id: Number(form.dataset.userId), id: Number(form.dataset.id), version: Number(form.dataset.version), action,
        title: f.get('title'), body: f.get('body'), source_name: f.get('source_name'), source_url: f.get('source_url'), comments: f.has('comments'),
        important: f.has('important'), review_note: action === 'reject' ? f.get('review_note') || '' : '', published_at: f.get('published_at') || '',
        published_timezone: f.get('published_timezone') || ''};
      const encoded = JSON.stringify(body);
      if (!pending || pending.body !== encoded) pending = {body: encoded, key: crypto.randomUUID()};
      form.dataset.busy = 'true';
      const buttons = [...form.querySelectorAll('button')]; buttons.forEach(b => b.disabled = true);
      tell('正在保存…');
      try {
        const auth = await identity();
        const result = await request(form.dataset.endpoint, {method: 'POST', headers: {'Content-Type':'application/json', 'X-CSRF-Token':auth.csrf_token, 'Idempotency-Key':pending.key}, body:pending.body});
        form.dataset.id = result.id; form.dataset.version = result.version;
        pending = null; tell('已保存');
        window.location.assign(`${location.pathname}?edit=${result.id}`);
      } catch (error) {
        const zone = error.details?.published_timezone;
        if (publishedZone && timezoneReview && typeof zone === 'string' && zone) {
          reviewZone = zone;
          for (const label of form.querySelectorAll('[data-flash-timezone-label]')) label.textContent = zone;
          timezoneReview.hidden = false;
        }
        tell(error.message);
      }
      finally { delete form.dataset.busy; buttons.forEach(b => b.disabled = false); }
    });
  }
  const items = [...document.querySelectorAll('[data-flash-id]')];
  if (items.length) {
    const ids = items.map(e => e.dataset.flashId).join(',');
    request(`/api/v1/social/engagement?posts=${encodeURIComponent(ids)}`).then(data => {
      for (const kind of ['like','bookmark']) for (const state of data[kind] || []) {
        const button = document.querySelector(`[data-flash-id="${state.target_id}"] [data-flash-engagement="${kind}"]`);
        if (button) {button.setAttribute('aria-pressed', String(state.active)); const count=button.querySelector('span'); if(count) count.textContent=state.count;}
      }
    }).catch(() => tell('互动状态暂时无法加载，请刷新后重试。'));
  }
  document.addEventListener('click', async event => {
    const button = event.target.closest('[data-flash-engagement], [data-flash-share]'); if (!button || button.disabled) return;
    button.disabled=true;
    try {
      if (button.hasAttribute('data-flash-share')) {
        const url=new URL(button.dataset.flashShare, location.origin).href;
        if (navigator.share) { try {await navigator.share({url});tell('分享已完成');return;} catch(error) {if(error.name==='AbortError')return;} }
        try { await navigator.clipboard.writeText(url);tell('链接已复制'); } catch { tell(`复制此链接分享：${url}`); }
      } else {
        const auth=await identity(); const active=button.getAttribute('aria-pressed')!=='true';
        await request('/api/v1/social/engagement', {method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':auth.csrf_token},body:JSON.stringify({kind:button.dataset.flashEngagement,target_id:Number(button.closest('[data-flash-id]').dataset.flashId),active})});
        button.setAttribute('aria-pressed',String(active)); const count=button.querySelector('span');if(count) count.textContent=Math.max(0,Number(count.textContent||0)+(active?1:-1));tell(active?'已保存互动':'已取消互动');
      }
    } catch(error){tell(error.message);} finally{button.disabled=false;}
  });
  for (const body of document.querySelectorAll('.flash-timeline .flash-body')) {
    if (body.textContent.length < 240) continue;
    const button=document.createElement('button');button.type='button';button.className='flash-more';button.textContent='展开正文';button.setAttribute('aria-expanded','false');
    const collapse=()=>{body.style.maxHeight='9.25em';body.style.overflow='hidden';};collapse();
    button.addEventListener('click',()=>{const open=button.getAttribute('aria-expanded')==='true';button.setAttribute('aria-expanded',String(!open));button.textContent=open?'展开正文':'收起正文';if(open)collapse();else{body.style.maxHeight='none';body.style.overflow='visible';}});body.after(button);
  }
})();
