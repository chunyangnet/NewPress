(() => {
  'use strict';
  const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const icons = {
    direct: '<path d="M12 3v12m-5-5 5 5 5-5M4 16v5h16v-5"/>',
    baidu: '<ellipse cx="5" cy="8" rx="2" ry="3"/><ellipse cx="10" cy="5" rx="2" ry="3"/><ellipse cx="16" cy="5" rx="2" ry="3"/><ellipse cx="20" cy="10" rx="2" ry="3"/><path d="M6 18c0-4 4-8 7-8s6 4 6 8c0 4-4 1-6 1s-7 3-7-1Z"/>',
    aliyun: '<path d="M9 5C3 4 1 9 5 14l4 5m6-14c6-1 8 4 4 9l-4 5M8 10l4 5 4-5"/>',
    quark: '<circle cx="11" cy="11" r="8"/><path d="m15 15 7 7"/>',
    onedrive: '<path d="M8 9a6 6 0 0 1 11 2 4 4 0 0 1-1 8H6a5 5 0 0 1-1-10Z"/>',
    google: '<path d="m8 3 8 0 8 14-4 5H4l-4-5Zm0 0 8 14H0m24 0H8L16 3"/>',
    dropbox: '<path d="m6 3 6 4-6 4-6-4Zm12 0 6 4-6 4-6-4ZM6 11l6 4-6 4-6-4Zm12 0 6 4-6 4-6-4ZM6 20l6 3 6-3"/>',
    lanzou: '<path d="M6 18h12a4 4 0 0 0 0-8 6 6 0 0 0-11-2 5 5 0 0 0-1 10Z"/>',
  };
  const logo = provider => `<span class="npr-provider npr-provider-${esc(provider)}" aria-hidden="true">${icons[provider] ? `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">${icons[provider]}</svg>` : esc(provider === '123pan' ? '123' : '115')}</span>`;
  const request = async (url, data, csrf) => {
    const response = await fetch(url, {credentials:'same-origin', cache:'no-store', headers: data ? {'Content-Type':'application/json','X-CSRF-Token':csrf} : {}, ...(data ? {method:'POST',body:JSON.stringify(data)} : {})});
    const result = await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(result.error?.message || result.message || '操作未完成，请刷新后重试');
      error.code = result.error?.code; error.details = result.error?.details; throw error;
    }
    return result;
  };
  const login = () => `/admin/login/?next=${encodeURIComponent(location.pathname + location.hash)}`;
  const orderID = value => typeof value === 'string' && /^np_[a-f0-9]{28}$/.test(value);
  const orderLink = condition => orderID(condition.order_id) && ['pending','paid','review'].includes(condition.order_status) ? `<a href="/billing/?order=${encodeURIComponent(condition.order_id)}" target="_blank" rel="noopener">查看原订单 · ${esc(condition.order_amount)} ${esc(condition.order_currency)} →</a>` : '';
  const date = (value, zone) => { if (!value) return ''; try { return new Date(value).toLocaleString('zh-CN', {timeZone:zone || undefined}); } catch { return ''; } };
  const roots = [...document.querySelectorAll('[data-npr-resource]')];
  roots.forEach(root => {
    const preview = root.hasAttribute('data-npr-preview');
    // Appearance previews are static; missing or unknown preview data must
    // never fall back to a live article's purchase endpoints.
    if (preview && root.dataset.nprPreview !== 'snapshot') return;
    const path = `/api/v1/article-resources/${root.dataset.nprPost}/${root.dataset.nprResource}`;
    const area = root.querySelector('.npr-content');
    let state, selected = '', busy = false, serial = 0, dirty = false, lastLoad = 0, epoch = 0;
    let previewUnlocked = false;
    const downloads = new Map();
    const dialogs = new Set();
    const status = message => { const node = area.querySelector('.npr-status'); if (node) node.textContent = message; };
    const send = (data, snapshot = {revision:state.revision,variant:selected,csrf:state.csrf}) => {
      if (preview) throw new Error('预览模式不会执行真实解锁操作。');
      return request(path, {revision:snapshot.revision,variant:snapshot.variant,...data}, snapshot.csrf);
    };
    function render() {
      if (!state.variants.some(v => v.id === selected)) selected = (!preview && state.variants.find(v => v.allowed)?.id) || state.variants[0]?.id;
      const v = state.variants.find(v => v.id === selected); if (!v) return;
      const allowed = preview ? previewUnlocked : v.allowed;
      const tabs = state.variants.length > 1 ? `<div class="npr-tabs" role="tablist" aria-label="内容版本">${state.variants.map(item => `<button type="button" role="tab" aria-selected="${item.id===selected}" data-version="${esc(item.id)}">${esc(item.name)}<span>${preview ? '预览版本' : item.allowed ? '已解锁' : '待解锁'}</span></button>`).join('')}</div>` : '';
      const actions = v.groups.map((g, gi) => `<section class="npr-rule"><div class="npr-rule-heading"><b>${esc(g.name || `解锁方式 ${gi+1}`)}</b><span>${g.passed ? '✓ 已满足' : '满足以下全部条件'}</span></div><ul>${g.conditions.map(c => {
        const attr = `data-condition="${esc(c.id)}" data-group="${gi}"`;
        let action = '';
        if (preview && !c.passed) {
          const label = ({points:'积分解锁',payment:'立即支付',follow:'关注作者',like:'点赞文章',membership:'查看会员',comment:'去评论',tip:'去打赏'})[c.kind];
          if (label) action = `<button type="button" disabled>${label}（预览）</button>`;
        } else if (!c.passed && state.logged_in && !v.blocked) {
          if (c.kind==='points' || c.kind==='payment') action = orderLink(c) || (c.purchasable === true ? `<button type="button" data-buy="${c.kind}" ${attr}>${c.kind==='points' ? '积分解锁' : '立即支付'} →</button>` : `<small>${esc(({prerequisite:'请先完成本组其他条件',limit:'下载次数已用完',already_unlocked:'已有可用的解锁方式'})[c.purchase_reason] || '当前不可购买')}</small>`);
          if (c.kind==='follow' || c.kind==='like') action = `<button type="button" data-engage="${c.kind}">${c.kind==='follow' ? '关注作者' : '点赞文章'}</button>`;
          if (c.kind==='membership') action = '<a href="/billing/?kind=membership">查看会员 →</a>';
          if (c.kind==='comment') action = '<a href="#comments">去评论 →</a>';
          if (c.kind==='tip') action = '<button type="button" data-tip>去打赏 →</button>';
        }
        return `<li class="${c.passed ? 'npr-met' : 'npr-unmet'}"><span>${c.passed?'✓':'○'}</span><span>${esc(c.label)}</span>${action}</li>`;
      }).join('')}</ul></section>`).join('<div class="npr-or"><span>或选择另一种方式</span></div>');
      const questions = v.questions?.length && !v.quiz_passed ? `<fieldset class="npr-questions" ${preview?'disabled':''}><legend>回答问题 · 达到 ${v.pass_percent}% 即可解锁</legend>${v.questions.map(q => `<label>${esc(q.title)}${q.options?.length ? `<select data-answer="${esc(q.id)}" required><option value="">请选择答案</option>${q.options.map(o=>`<option>${esc(o)}</option>`).join('')}</select>` : `<input data-answer="${esc(q.id)}" maxlength="500" required autocomplete="off">`}</label>`).join('')}<small>${preview?'每日最多尝试':'今日还可尝试'} ${v.attempts_remaining} 次</small></fieldset>` : '';
      const fields = v.fields?.length && !v.form_complete ? `<fieldset ${preview?'disabled':''}><legend>填写资料</legend>${v.fields.map(f => `<label>${esc(f.label)}${f.required ? '<span class="npr-required"> *</span>' : ''}${f.type==='textarea' ? `<textarea data-field="${esc(f.id)}" maxlength="4000" ${f.required?'required':''}></textarea>` : f.type==='select' ? `<select data-field="${esc(f.id)}" ${f.required?'required':''}><option value="">请选择</option>${f.options.map(o=>`<option>${esc(o)}</option>`).join('')}</select>` : `<input type="${esc(f.type)}" data-field="${esc(f.id)}" maxlength="4000" ${f.required?'required':''}>`}</label>`).join('')}</fieldset>` : '';
      const submit = !v.blocked && (questions || fields) ? `<form class="npr-form">${questions}${fields}<button class="npr-primary" type="submit" ${preview?'disabled':''}>${preview?'提交核验（预览）':'提交并核验条件 →'}</button></form>` : '';
      const priced = c => c.kind==='payment' && (preview || (!c.passed && c.purchasable===true && !orderLink(c)));
      const priceGroup=!v.blocked?v.groups.findIndex(g=>g.conditions.some(priced)):-1;
      const price=priceGroup>=0?v.groups[priceGroup].conditions.find(priced):null;
      const primary=price?`<div class="npr-pricebar"><div><small>解锁此版本${preview?'（预览）':''}</small><strong><i>${esc(({CNY:'¥',USD:'$',EUR:'€',GBP:'£',JPY:'¥'})[price.currency]||price.currency)}</i>${(Number(price.amount)/(price.currency==='JPY'?1:100)).toFixed(price.currency==='JPY'?0:2)}</strong></div>${preview?'<button type="button" class="npr-primary" data-preview-view="unlocked">预览解锁效果</button>':state.logged_in?`<button type="button" class="npr-primary" data-buy="payment" data-condition="${esc(price.id)}" data-group="${priceGroup}">立即解锁</button>`:`<a class="npr-primary" href="${login()}">登录后解锁</a>`}</div>`:'';
      const moreConditions=v.groups.length>1||v.groups.some(group=>group.conditions.length>1);
      const conditions=price?`${primary}${moreConditions?`<details class="npr-alternatives"><summary>查看解锁条件与其他方式</summary>${actions}</details>`:''}`:actions;
      const delivery = link => preview ? `<button type="button" class="npr-primary" disabled>获取下载地址（预览）</button><div class="npr-delivery npr-preview-delivery"><span>下载地址 <b>${esc(link.url)}</b></span>${link.code?`<span>提取码 <b>${esc(link.code)}</b></span>`:''}${link.password?`<span>解压密码 <b>${esc(link.password)}</b></span>`:''}</div>` : `<button type="button" class="npr-primary" data-download="${esc(link.id)}">获取下载地址</button><div class="npr-delivery" data-delivery="${esc(link.id)}"></div>`;
      const content = allowed ? `<div class="npr-unlocked"><p class="npr-success">${preview?'解锁后效果预览 · ':'✓ '}${esc(v.name)}${preview?'':'已解锁'}</p>${v.body || ''}${v.kind==='image' ? `<img class="npr-private-image" src="${esc(v.media_url)}" alt="${esc(v.name)}" loading="lazy">` : ''}${v.kind==='video' ? `<video controls preload="metadata" src="${esc(v.media_url)}" aria-label="${esc(v.name)}"></video>` : ''}${v.downloads.map(link=>`<div class="npr-download">${logo(link.provider)}<span><b>${esc(link.name || link.provider_name)}</b><small>${esc(link.provider_name)}</small></span>${delivery(link)}</div>`).join('')}</div>` : `<p class="npr-intro">${!preview && state.selection==='first' && state.variants.some(x=>x.allowed) ? '此版本未被选中，当前展示首个符合条件的版本。' : '完成任意一组条件，即可解锁这个版本。'}</p>${!preview && !state.logged_in && !price ? `<a class="npr-login" href="${login()}">登录后使用积分、支付与会员权益 →</a>`:''}${conditions}${submit}`;
      const windowCopy = [state.starts_at ? `开放时间：${date(state.starts_at,state.timezone)}` : '', state.ends_at ? `访问截止：${date(state.ends_at,state.timezone)}` : ''].filter(Boolean).join(' · ');
      const recovery = !preview && !v.available && state.logged_in ? v.groups.flatMap(g=>g.conditions.map(orderLink)).filter(Boolean).join('') : '';
      const previewControls = preview ? `<div class="npr-preview-controls" role="group" aria-label="资源预览状态"><span>查看效果</span><button type="button" data-preview-view="locked" aria-pressed="${!previewUnlocked}">解锁前</button><button type="button" data-preview-view="unlocked" aria-pressed="${previewUnlocked}">解锁后</button></div><p class="npr-intro">使用本次草稿快照展示全部版本。${state.selection==='first'?'正式访问仅展示首个符合条件的版本。':'正式访问按各版本条件核验。'}</p>` : '';
      const unavailable = !v.available ? `<p class="npr-unavailable">此资源尚未开放或已结束访问。${preview?'下方仅预览内容效果。':''}</p>` : '';
      area.innerHTML = `${previewControls}${tabs}${windowCopy?`<p class="npr-intro">${esc(windowCopy)}</p>`:''}${unavailable}${preview || v.available ? content : recovery}<footer class="npr-controls"><span>${preview?'预览额度 · ':''}${state.remaining<0 ? '下载次数不限' : `${preview?'可下载':'剩余下载'} ${state.remaining} 次`}</span>${preview?'':'<button type="button" data-refresh>重新核验</button>'}</footer><p class="npr-status" role="status" aria-live="polite"></p>`;
    }
    async function refresh() {
      if (preview) { if (state) render(); return; }
      const ticket = ++serial;
      try { const next = await request(path); if (ticket!==serial) return; state=next;lastLoad=Date.now();render(); }
      catch(error) { if (ticket===serial) area.innerHTML=`<p class="npr-status" role="status">${esc(error.message)}</p><button type="button" data-refresh>重试加载</button>`; }
    }
    async function purchase(button) {
      if (preview) return;
      const v = state.variants.find(item => item.id===selected), group=Number(button.dataset.group);
      const condition = v?.groups[group]?.conditions.find(c=>c.id===button.dataset.condition);
      if (!condition || condition.passed || condition.purchasable!==true || !state.logged_in || v.blocked || !v.available) return;
      const snapshot = {revision:state.revision,variant:v.id,csrf:state.csrf,user:state.user_id,paymentVersion:state.payment_version,epoch};
      const current = () => snapshot.epoch===epoch && snapshot.user===state?.user_id;
      const channels = state.channels.filter(ch=>ch.currency===condition.currency);
      const dialog=document.createElement('dialog');dialog.className='npr-checkout';
      dialog.innerHTML=`<form method="dialog"><button class="npr-close" aria-label="关闭">×</button></form><p class="npr-eyebrow">UNLOCK YOUR ACCESS</p><h2>${esc(state.title)}</h2><p>${esc(v.name)} · ${esc(condition.label)}</p><p>购买只完成本组的付费条件，其他条件仍需保持满足。</p>${state.ends_at?`<p>资源访问截止：${esc(date(state.ends_at,state.timezone))}</p>`:''}${condition.kind==='payment' ? `<label>支付方式<select data-channel>${channels.map(ch=>`<option value="${esc(ch.key)}">${esc(ch.name)}</option>`).join('')}</select></label><details><summary>支付与退款说明</summary><p>${esc(state.refund_policy)}</p><p>${esc(state.disclaimer)}</p></details>` : '<p>确认后从站内钱包扣除相应积分，重复提交不会重复扣除。</p>'}<label class="npr-agree"><input type="checkbox" data-agree>我已核对解锁条件、金额及${condition.kind==='payment'?'退款说明':'积分扣除说明'}</label><button type="button" class="npr-primary" data-confirm ${condition.kind==='payment'&&!channels.length?'disabled':''}>${condition.kind==='payment'&&!channels.length?'暂未配置可用支付渠道':'确认并继续 →'}</button><div class="npr-payment-result" role="status"></div>`;
      document.body.append(dialog);dialogs.add(dialog);dialog.showModal();dialog.addEventListener('close',()=>{dialogs.delete(dialog);dialog.remove();},{once:true});
      const result=dialog.querySelector('.npr-payment-result');
      const completed = async kind => {
        await refresh(); if (!current()) return;
        const allowed=state.variants.find(item=>item.id===snapshot.variant)?.allowed;
        dialog.close();status(allowed ? `${kind}已完成，对应版本已解锁。` : `${kind}已完成，购买凭据已保留。请核验其余条件、版本选择与访问时限。`);
      };
      const showOrder = async (order, message='') => {
        if (!current()) return;
        if (!orderID(order?.id) || order.kind!=='resource' || String(order.user_id)!==String(snapshot.user)) throw new Error('返回订单与当前资源账户不一致，请保留原请求并核验。');
        if (order.status==='completed') {await completed('支付');return;}
        const copy = order.status==='review' ? '支付事实已记录，订单需要管理员核对资源交付或退款。请保留订单编号。' : order.status==='paid' ? '已确认收款，正在核对资源权益，请勿再次付款。' : order.status==='cancelled' ? '订单已取消，请先在原订单中核对渠道结果，再返回文章重新购买。' : order.status==='refunded' ? '订单已退款，解锁权益已停止。' : Date.parse(order.expires_at)<=Date.now() ? '支付时限已到，请核对原订单。确认渠道关闭前不会创建第二笔支付。' : '请在原订单中核对金额和支付时限后继续。';
        result.innerHTML=`<p>${esc(message || copy)}</p><p>订单金额 ${esc(order.amount)} ${esc(order.currency)}</p><a class="npr-primary" href="/billing/?order=${encodeURIComponent(order.id)}" target="_blank" rel="noopener">打开原订单与安全收银台 ↗</a>${['pending','paid','cancelled'].includes(order.status)?'<button type="button" data-sync>核验原订单</button>':''}<p data-order-status></p>`;
        result.querySelector('[data-sync]')?.addEventListener('click',async ev=>{
          if (!current()) return; ev.currentTarget.disabled=true;
          try {
            const paid=await request(`/api/v1/social/billing/orders/${order.id}/sync`,{},snapshot.csrf);
            if (!current()) return;
            if(paid.status==='completed') await completed('支付'); else {await refresh();await showOrder(paid);}
          } catch(error) {if(current()) result.querySelector('[data-order-status]').textContent=error.message;}
          finally {if(current()) ev.currentTarget.disabled=false;}
        });
      };
      dialog.querySelector('[data-confirm]').addEventListener('click',async event=>{
        const btn=event.currentTarget;
        if (!current() || btn.disabled) return;
        if(!dialog.querySelector('[data-agree]').checked){result.textContent='请先核对并勾选确认。';return;}
        btn.disabled=true;
        try {
          const order=await send({action:condition.kind==='points'?'points':'pay',condition:condition.id,group,channel:dialog.querySelector('[data-channel]')?.value||'',accepted:true,...(condition.kind==='payment'?{payment_version:snapshot.paymentVersion}: {})},snapshot);
          if (!current()) return;
          if(condition.kind==='points'){if(!order.paid)throw new Error('积分扣款尚未确认，请保留原请求后核验。');await completed('积分支付');return;}
          await showOrder(order);
        } catch(error){
          if (!current()) return;
          if(error.code==='payment.pending'&&error.details?.order){try{await showOrder(error.details.order,error.message);}catch(e){result.textContent=e.message;}return;}
          result.textContent=error.message;
          if(error.code==='resource.changed'||error.code==='payment.conflict'){await refresh();if(current())result.textContent='条件或支付说明已更新，请关闭本弹窗，重新核对后确认购买。';}
          else btn.disabled=false;
        }
      });
    }
    area.addEventListener('click',async event=>{
      const button=event.target.closest('button');if(!button||busy||!state)return;
      if(button.dataset.version){selected=button.dataset.version;render();return;}
      if(preview){
        if(['locked','unlocked'].includes(button.dataset.previewView)){
          previewUnlocked=button.dataset.previewView==='unlocked';render();
          area.querySelector(`.npr-preview-controls [data-preview-view="${button.dataset.previewView}"]`)?.focus?.();
        } else status('预览模式不会执行真实解锁操作。');
        return;
      }
      busy=true;button.disabled=true;
      try {
        if(button.hasAttribute('data-refresh')) await refresh();
        if(button.hasAttribute('data-buy')) await purchase(button);
        if(button.dataset.engage){await request('/api/v1/social/engagement',{kind:button.dataset.engage,target_id:button.dataset.engage==='follow'?state.author:state.post,active:true,collection:''},state.csrf);await refresh();}
        if(button.hasAttribute('data-tip')){const trigger=document.querySelector('[data-recognition-tip], [data-tip-open], [data-support-open]');if(trigger)trigger.click();else status('请使用文章下方的打赏入口，完成后点击重新核验。');}
        if(button.dataset.download){
          const key=`${selected}:${button.dataset.download}`;
          if(!downloads.has(key))downloads.set(key,crypto.randomUUID().replaceAll('-',''));
          const file=await send({action:'download',link:button.dataset.download,request:downloads.get(key)});
          const output=[...area.querySelectorAll('[data-delivery]')].find(n=>n.dataset.delivery===button.dataset.download);
          output.innerHTML=`<a href="${esc(file.url)}" target="_blank" rel="noopener noreferrer">打开下载地址 ↗</a>${file.code?`<span>提取码 <b>${esc(file.code)}</b><button type="button" data-copy="${esc(file.code)}">复制</button></span>`:''}${file.password?`<span>解压密码 <b>${esc(file.password)}</b><button type="button" data-copy="${esc(file.password)}">复制</button></span>`:''}`;
          state.remaining=file.remaining;area.querySelector('.npr-controls>span').textContent=file.remaining<0?'下载次数不限':`剩余下载 ${file.remaining} 次`;
          status('下载地址已准备好。本次获取已记录，重复点击不会再次计数。');
        }
        if(button.dataset.copy){await navigator.clipboard.writeText(button.dataset.copy);status('已复制');}
      }catch(error){status(error.message);}finally{busy=false;button.disabled=false;}
    });
    area.addEventListener('input',event=>{if(event.target.closest('.npr-form'))dirty=true;});
    area.addEventListener('submit',async event=>{
      if(!event.target.matches('.npr-form'))return;event.preventDefault();if(preview){status('预览模式不会提交答题或表单资料。');return;}if(busy)return;busy=true;
      const fields={},answers={};event.target.querySelectorAll('[data-field]').forEach(n=>fields[n.dataset.field]=n.type==='checkbox'?String(n.checked):n.value);event.target.querySelectorAll('[data-answer]').forEach(n=>answers[n.dataset.answer]=n.value);
      try {const result=await send({action:'submit',...(Object.keys(fields).length?{fields}:{}),...(Object.keys(answers).length?{answers}:{})});dirty=false;await refresh();status(Object.keys(answers).length&&!result.quiz_passed?`答案尚未达到通过要求，今日还可尝试 ${result.attempts_remaining} 次。`:'提交成功，解锁条件已更新。');}catch(error){status(error.message);}finally{busy=false;}
    });
    if(preview){
      try {
        const encoded=root.dataset.nprPreviewState || '';
        const data=JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(encoded.replace(/-/g,'+').replace(/_/g,'/')),c=>c.charCodeAt(0))));
        if(data.preview!==true || String(data.post)!==root.dataset.nprPost || data.id!==root.dataset.nprResource || !Array.isArray(data.variants) || !data.variants.length)throw new Error();
        state=data;render();delete root.dataset.nprPreviewState;
      } catch {state=undefined;area.innerHTML='<p class="npr-status" role="status">预览配置无法读取，请返回编辑器重新打开预览。</p>';}
    } else {
      document.addEventListener('chunnel:session',()=>{epoch++;serial++;downloads.clear();dialogs.forEach(dialog=>dialog.close());dirty=false;state=undefined;area.innerHTML='<p class="npr-status" role="status">正在重新核验账户与访问权限…</p>';refresh();});
      window.addEventListener('focus',()=>{if(!busy&&!dirty&&!document.querySelector('.npr-checkout[open]')&&Date.now()-lastLoad>60000){downloads.clear();refresh();}});
      refresh();
    }
  });
  const library=document.querySelector('[data-npr-library]');
  if(library) request('/api/v1/social/downloads').then(data=>{library.innerHTML=data.items.length?data.items.map(item=>`<a class="npr-library-item" href="${esc(item.url)}"><span>${logo('direct')}<b>${esc(item.title)}</b></span><small>${esc(new Date(item.created_at).toLocaleString())} →</small></a>`).join(''):'<p>还没有下载记录，解锁资源后会显示在这里。</p>';}).catch(error=>{library.innerHTML=`<p>${esc(error.message)}</p><a href="${login()}">登录查看我的下载</a>`;});
})();
