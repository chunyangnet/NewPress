(() => {
  'use strict';
  const iconNames = ['award','sparkles','star','heart','diamond','shield-check','badge-check','zap','pen-line','newspaper','x'];
  const node = (tag, text, cls) => { const el = document.createElement(tag); if (text !== undefined) el.textContent = text; if (cls) el.className = cls; return el; };
  const icon = name => { const svg = document.createElementNS('http://www.w3.org/2000/svg','svg'); svg.setAttribute('class','icon'); svg.setAttribute('fill','none'); svg.setAttribute('stroke','currentColor'); svg.setAttribute('stroke-width','1.75'); svg.setAttribute('stroke-linecap','round'); svg.setAttribute('stroke-linejoin','round'); svg.setAttribute('viewBox','0 0 24 24'); svg.setAttribute('aria-hidden','true'); const use = document.createElementNS(svg.namespaceURI,'use'); use.setAttribute('href','#icon-' + (iconNames.includes(name) ? name : 'award')); svg.append(use); return svg; };
  function colors(el) {
    const color = el.dataset.identityColor, bg = el.dataset.identityBackground;
    if (/^#[0-9a-f]{6}$/.test(color || '')) {
      el.style.setProperty('--identity-accent',color);
      const rgb = [1,3,5].map(i => parseInt(color.slice(i,i+2),16)/255).map(v => v <= .04045 ? v/12.92 : ((v+.055)/1.055)**2.4);
      el.style.setProperty('--identity-on',rgb[0]*.2126+rgb[1]*.7152+rgb[2]*.0722 > .179 ? '#17253d' : '#ffffff');
    }
    if (/^#[0-9a-f]{6}$/.test(bg || '')) el.style.setProperty('--identity-bg',bg);
  }
  const localImage = value => typeof value === 'string' && /^\/media\/[^\s\\?#]+\.(png|jpe?g|gif|webp|avif)$/i.test(value) && !value.includes('..');
  function badge(b, options = {}) {
    const medal = b.kind === 'medal', interactive = medal && options.interactive !== false;
    const level = b.kind === 'level' && Number.isInteger(b.level) && b.level > 0 && b.level <= 50;
    const el = node(interactive ? 'button' : 'span', undefined, 'identity-badge' + (medal ? ' identity-medal' : level ? ' identity-level' : ''));
    el.dataset.identityKind=b.kind; el.dataset.identityStyle=b.style; el.dataset.identityColor=b.color; el.dataset.identityBackground=b.background || '';
    if (medal) {
      el.dataset.identityName=b.name || ''; el.dataset.identityDescription=b.description || '';
      el.dataset.identityGranted=b.granted_at || ''; el.dataset.identityExpires=b.expires_at || '';
      el.dataset.identityImage=localImage(b.image) ? b.image : ''; el.dataset.identityIcon=iconNames.includes(b.icon) ? b.icon : 'award';
      if (interactive) { el.type='button'; el.dataset.medalDetails=''; el.setAttribute('aria-label',b.name+' · 查看勋章详情'); el.setAttribute('aria-haspopup','dialog'); el.setAttribute('aria-expanded','false'); }
      else if (options.decorative) el.setAttribute('aria-hidden','true');
      else { el.setAttribute('role','img'); el.setAttribute('aria-label',b.name); el.title=b.name; }
    } else el.title = [level ? 'LV' + b.level : '', b.name,b.description].filter(Boolean).join(' · ');
    if (level) {
      el.setAttribute('aria-label',`等级 ${b.level} · ${b.name}`);
      const art=node('span',undefined,'identity-level-art'); art.setAttribute('aria-hidden','true'); art.append(node('span','LV','identity-level-prefix'),node('span',String(b.level),'identity-level-number')); el.append(art);
    } else {
      const art=medal ? node('span',undefined,'identity-medal-art') : el;
      if (localImage(b.image)) { const img=node('img'); img.src=b.image; img.alt=''; img.width=img.height=medal ? 28 : 20; art.append(img); } else art.append(icon(b.icon));
      if (medal) { art.setAttribute('aria-hidden','true'); el.append(art); } else el.append(node('span',b.name));
    }
    colors(el); return el;
  }
  let activeMedal = null, detailPanel = null, detailSequence = 0;
  const medalDate = raw => {
    if (!raw || raw.startsWith('0001-')) return '';
    const value = new Date(raw); if (Number.isNaN(value.getTime())) return '';
    return window.ChunnelTime?.formatDateTime(raw) || value.toLocaleString('zh-CN', { year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit' });
  };
  function closeMedal(restoreFocus = false) {
    if (!activeMedal) return;
    const trigger=activeMedal; activeMedal=null;
    trigger.setAttribute('aria-expanded','false'); trigger.removeAttribute('aria-controls');
    detailPanel?.remove(); detailPanel=null;
    if (restoreFocus && trigger.isConnected) trigger.focus({preventScroll:true});
  }
  function positionMedal() {
    if (!activeMedal || !detailPanel) return;
    if (!activeMedal.isConnected || !activeMedal.getClientRects().length) { closeMedal(); return; }
    const viewport=window.visualViewport, leftEdge=viewport?.offsetLeft || 0, topEdge=viewport?.offsetTop || 0;
    const width=viewport?.width || document.documentElement.clientWidth, height=viewport?.height || window.innerHeight;
    const rect=activeMedal.getBoundingClientRect(), gap=10, margin=12;
    if (rect.bottom < topEdge || rect.top > topEdge+height || rect.right < leftEdge || rect.left > leftEdge+width) { closeMedal(); return; }
    detailPanel.style.maxWidth=Math.max(0,width-margin*2)+'px';
    detailPanel.style.maxHeight=Math.max(0,height-margin*2)+'px';
    const size=detailPanel.getBoundingClientRect();
    const below=topEdge+height-rect.bottom-gap-margin, above=rect.top-topEdge-gap-margin;
    const side=below >= size.height || below >= above ? 'bottom' : 'top';
    const left=Math.max(leftEdge+margin,Math.min(rect.left+(rect.width-size.width)/2,leftEdge+width-margin-size.width));
    const top=Math.max(topEdge+margin,Math.min(side==='bottom' ? rect.bottom+gap : rect.top-gap-size.height,topEdge+height-margin-size.height));
    detailPanel.dataset.side=side; detailPanel.style.left=left+'px'; detailPanel.style.top=top+'px';
    detailPanel.style.setProperty('--medal-arrow',Math.max(22,Math.min(size.width-22,rect.left+rect.width/2-left))+'px');
  }
  function openMedal(trigger) {
    if (activeMedal===trigger) { closeMedal(true); return; }
    closeMedal(); activeMedal=trigger;
    const d=trigger.dataset, id='medal-detail-'+(++detailSequence);
    const panel=node('section',undefined,'identity-popover'); detailPanel=panel; panel.id=id; panel.tabIndex=-1;
    panel.setAttribute('role','dialog'); panel.setAttribute('aria-label',d.identityName+' · 勋章详情'); panel.setAttribute('aria-describedby',id+'-description');
    panel.dataset.identityColor=d.identityColor || ''; panel.dataset.identityBackground=d.identityBackground || ''; colors(panel);
    const surface=node('div',undefined,'identity-popover-surface'), header=node('div',undefined,'identity-popover-heading');
    const emblem=badge({kind:'medal',name:d.identityName,icon:d.identityIcon,image:d.identityImage,color:d.identityColor,background:d.identityBackground,style:d.identityStyle},{interactive:false});
    const heading=node('div',undefined,'identity-popover-title'); heading.append(node('span','社区勋章','recognition-eyebrow'),node('h3',d.identityName));
    const close=node('button',undefined,'identity-popover-close'); close.type='button'; close.setAttribute('aria-label','关闭勋章详情'); close.append(icon('x')); close.addEventListener('click',()=>closeMedal(true));
    header.append(emblem,heading,close);
    const description=node('p',d.identityDescription || '每一份投入，都值得被看见。','identity-popover-description'); description.id=id+'-description';
    const dates=node('dl',undefined,'identity-popover-dates'), granted=medalDate(d.identityGranted), expires=medalDate(d.identityExpires);
    if (granted) { const row=node('div'); row.append(node('dt','获得时间'),node('dd',granted)); dates.append(row); }
    const expiry=node('div'); expiry.append(node('dt','有效期'),node('dd',expires ? '至 '+expires : '长期有效')); dates.append(expiry);
    surface.append(header,description,dates); panel.append(surface); document.body.append(panel);
    if (typeof panel.showPopover === 'function') { panel.setAttribute('popover','manual'); panel.showPopover(); }
    trigger.setAttribute('aria-expanded','true'); trigger.setAttribute('aria-controls',id);
    positionMedal(); panel.focus({preventScroll:true});
  }
  // Delegation covers both server-rendered identities and newly posted comments.
  document.addEventListener('click',event=>{const trigger=event.target.closest?.('[data-medal-details]'); if(trigger && !trigger.disabled){event.preventDefault();openMedal(trigger);}else if(activeMedal && !detailPanel?.contains(event.target))closeMedal();});
  document.addEventListener('keydown',event=>{
    if (!activeMedal) return;
    if (event.key==='Escape') { event.preventDefault(); event.stopPropagation(); closeMedal(true); }
    else if (event.key==='Tab' && detailPanel?.contains(document.activeElement) && (event.shiftKey || document.activeElement!==detailPanel)) {
      // Continue from the original badge instead of tabbing out of the document
      // from the floating panel appended at the end of the body.
      closeMedal(true); if(event.shiftKey) event.preventDefault();
    }
  });
  document.addEventListener('focusin',event=>{if(activeMedal && event.target!==activeMedal && !detailPanel?.contains(event.target))closeMedal();});
  document.addEventListener('scroll',event=>{if(activeMedal && !detailPanel?.contains(event.target))positionMedal();},true);
  window.addEventListener('resize',positionMedal);
  window.addEventListener('pagehide',()=>closeMedal());
  window.visualViewport?.addEventListener('resize',positionMedal);
  window.visualViewport?.addEventListener('scroll',positionMedal);
  function decorateComment(meta, item) {
    if (Array.isArray(item.badges) && item.badges.length) { const group=node('span',undefined,'identity-badges'); item.badges.forEach(b=>group.append(badge(b))); meta.append(group); }
    if (item.support?.supported) { const el=node('span',undefined,'comment-support-badge'); el.append(icon('heart'),node('span',item.support.label || '已打赏')); meta.append(el); }
  }
  window.NewPressRecognition = Object.freeze({badge,decorateComment,colors});
  document.querySelectorAll('[data-identity-color]').forEach(colors);
  const report = (el,text,error=false) => { el.textContent=text; el.dataset.error=String(error); };
  const busy = (form,on) => { form.setAttribute('aria-busy',String(on)); form.querySelectorAll('button[type=submit], button:not([type])').forEach(b=>b.disabled=on); };
  const makeClient = () => { const client=window.NewPressClient?.create(); if(client)document.addEventListener('chunnel:session',e=>client.setSession(e.detail)); return client; };
  const tipRoot = document.querySelector('[data-article-tips]');
  if (tipRoot) {
    const client=makeClient(), dialog=tipRoot.querySelector('[data-tip-dialog]'), form=dialog.querySelector('form'), status=dialog.querySelector('[data-tip-status]'), opener=tipRoot.querySelector('[data-tip-open]');
    document.body.append(dialog);
    let data, pending, storageKey, submitting=false, generation=0;
    document.addEventListener('chunnel:session', e=>{ if(data && String(e.detail?.id)!==String(data.user_id)){generation++;data=null;pending=null;form.hidden=true;dialog.close();} });
    const field = key => form.elements.namedItem(key);
    const savePending=()=>{ try { if(pending) sessionStorage.setItem(storageKey,JSON.stringify(pending)); else sessionStorage.removeItem(storageKey); } catch {} };
    function selectCurrency() {
      const c=data?.currencies.find(c=>c.key===field('currency').value); if (!c) return;
      report(dialog.querySelector('[data-tip-balance]'),`可用 ${c.balance} ${c.symbol} · 单次 ${c.min}–${c.max} ${c.symbol}`);
      const group=dialog.querySelector('[data-tip-presets]'); group.replaceChildren();
      c.presets.forEach(amount=>{ const b=node('button',amount+' '+c.symbol); b.type='button'; b.setAttribute('aria-pressed',String(field('amount').value===amount)); b.addEventListener('click',()=>{ field('amount').value=amount; group.querySelectorAll('button').forEach(x=>x.setAttribute('aria-pressed',String(x===b))); }); group.append(b); });
    }
    function pendingUI() {
      if(!data)return;
      form.querySelector('fieldset').disabled=!!pending;
      form.querySelector('[type=submit]').textContent=pending?'核对并重试原打赏':'确认打赏';
      if(pending) report(dialog.querySelector('[data-tip-confirm]'),`待确认：${pending.amount} ${data.currencies.find(c=>c.key===pending.currency)?.symbol||pending.currency}。重试使用同一笔记录。`);
      else report(dialog.querySelector('[data-tip-confirm]'),'确认后从当前钱包扣除，直接转给文章作者。');
    }
    opener.addEventListener('click',async()=>{
      if(submitting) { dialog.showModal(); return; }
      const epoch=++generation; dialog.showModal(); form.hidden=true; dialog.querySelector('[data-tip-login]').hidden=true; report(status,'正在读取钱包…');
      try {
        if(!client) throw new Error('页面资源未加载，请刷新后重试');
        const session=await client.getSession(true);
        if(epoch!==generation)return;
        if(!session.authenticated){report(status,'登录后可以使用积分或余额支持作者。');dialog.querySelector('[data-tip-login]').hidden=false;return;}
        data=await client.request(tipRoot.dataset.tipApi); if(epoch!==generation)return;
        storageKey=`newpress:tip:${data.user_id}:${tipRoot.dataset.tipPost}`; pending=null;
        try { const saved=JSON.parse(sessionStorage.getItem(storageKey)); if(saved && saved.post_id===Number(tipRoot.dataset.tipPost) && typeof saved.request_id==='string' && /^[a-zA-Z0-9_-]{16,80}$/.test(saved.request_id) && typeof saved.amount==='string') pending=saved; } catch {}
        if(!data.enabled && !pending){report(status,'该文章暂不支持打赏，作者不能给自己的文章打赏。');return;}
        if(!data.currencies.length && !pending){report(status,'暂无可用于打赏的钱包，请稍后再试。');return;}
        field('currency').replaceChildren(...data.currencies.map(c=>{const o=node('option',c.name);o.value=c.key;return o;}));
        if(pending && !data.currencies.some(c=>c.key===pending.currency)){const o=node('option',pending.currency);o.value=pending.currency;field('currency').append(o);}
        field('currency').value=pending?.currency || data.currencies[0]?.key || '';
        field('amount').value=pending?.amount || data.currencies[0]?.presets[0] || '';
        field('hidden').checked=pending?.hidden || false;
        dialog.querySelector('[data-tip-hide-row]').hidden=!data.allow_hide_amount || data.hide_amounts;
        form.hidden=false;selectCurrency();pendingUI();report(status,pending?'上次打赏结果待确认，点击下方按钮核对。':data.hide_amounts?'评论中仅显示“已打赏”，不公开数值。':'选择一份心意，支持这篇文章。');
      } catch(e){if(epoch===generation)report(status,e.message,true);}
    });
    dialog.querySelector('[data-tip-close]').addEventListener('click',()=>dialog.close());
    dialog.addEventListener('close',()=>{opener.focus();});
    field('currency').addEventListener('change',()=>{field('amount').value=data.currencies.find(c=>c.key===field('currency').value)?.presets[0]||'';selectCurrency();});
    field('amount').addEventListener('input',()=>dialog.querySelectorAll('[data-tip-presets] button').forEach(b=>b.setAttribute('aria-pressed','false')));
    form.addEventListener('submit',async e=>{
      e.preventDefault();if(submitting)return;
      submitting=true;busy(form,true);
      try {
        const session=await client.getSession(true); if(!session.authenticated || String(session.id)!==String(data.user_id)) throw new Error('登录账号已变化，请关闭弹窗后重新打开');
        if(!pending){const c=data.currencies.find(c=>c.key===field('currency').value);const amount=field('amount').value.trim();if(!c || !new RegExp('^[0-9]+'+(c.precision?'(?:\\.[0-9]{1,'+c.precision+'})?':'')+'$').test(amount))throw new Error('请输入符合当前币种精度的正数');
          pending={post_id:Number(tipRoot.dataset.tipPost),request_id:crypto.randomUUID(),currency:c.key,amount,hidden:field('hidden').checked && data.allow_hide_amount,settings_version:data.settings_version,currency_version:c.version};savePending();pendingUI();}
        report(status,'正在确认打赏，请稍候…');
        const result=await client.request(tipRoot.dataset.tipApi,{method:'POST',body:pending});if(!result.completed || result.request_id!==pending.request_id)throw new Error('打赏结果暂未确认，请重试原操作');
        pending=null;savePending();report(tipRoot.querySelector('[data-tip-result]'),result.message+'；评论标注刷新后可见。');dialog.close();
      }catch(error){report(status,error.message,true);if(['recognition.invalid','recognition.conflict','recognition.disabled','recognition.not_found','currency.invalid','currency.insufficient','currency.balance_limit','currency.transfer_disabled','currency.daily_limit','currency.settings_conflict','currency.disabled'].includes(error.code)){pending=null;savePending();}pendingUI();}
      finally{submitting=false;busy(form,false);}
    });
  }
  document.querySelectorAll('[data-identity-panel]').forEach(root=>{
    const client=makeClient(), status=root.querySelector('[data-identity-status]'), details=root.querySelector('details');let data,loading=false,writing=false;
    async function load(){if(loading)return;loading=true;report(status,'正在读取身份…');try{
      if(!client)throw new Error('页面资源未加载，请刷新后重试');const session=await client.getSession(true);if(!session.authenticated || String(session.id)!==root.dataset.identityUser)throw new Error('登录状态已变化，请刷新页面');
      data=await client.request('/api/v1/social/recognition');root.querySelector('[data-identity-content]').hidden=false;
      const list=root.querySelector('[data-identity-medals]');list.replaceChildren();
      data.identity.grants.forEach(g=>{const b=data.catalog.find(b=>b.key===g.key);if(!b || !b.enabled || b.kind!=='medal')return;const expired=g.expires_at && !g.expires_at.startsWith('0001-') && new Date(g.expires_at)<=new Date();const label=node('label'),box=node('input');box.type='checkbox';box.name='medal';box.value=g.key;box.checked=data.identity.equipped?.includes(g.key);box.disabled=!!expired;const text=node('span');text.append(badge(b,{interactive:false,decorative:true}),node('span',b.name,'identity-medal-name'),node('small',expired?'已到期':g.expires_at&&!g.expires_at.startsWith('0001-')?'有效期至 '+new Date(g.expires_at).toLocaleDateString():'长期有效'));label.append(box,text);list.append(label);});
      if(!list.children.length)list.append(node('p','暂时还没有勋章，继续创作和交流吧。','recognition-help'));
      report(root.querySelector('[data-identity-limit]'),`最多展示 ${data.max_medals} 枚勋章，取消勾选后保留已获得的勋章。`);
      const p=data.identity.application, states={pending:'申请审核中',approved:'已通过认证',rejected:'申请未通过',revoked:'认证已撤销'};
      const verified=data.identity.grants.some(g=>data.catalog.find(b=>b.key===g.key)?.kind==='verified' && (!g.expires_at || g.expires_at.startsWith('0001-') || new Date(g.expires_at)>new Date()));
      report(root.querySelector('[data-identity-application-state]'),p?(p.status==='approved'&&!verified?'认证已到期，可重新申请':states[p.status])+(p.reason?' · '+p.reason:''):verified?'已获得站点认证，认证标记会显示在作者页与评论中。':'完成审核后，认证标记将显示在作者页与评论中。');
      report(root.querySelector('[data-identity-help]'),data.verification_enabled?data.verification_help:'站点暂未开放认证申请。');
      const apply=root.querySelector('[data-identity-apply]');apply.hidden=!data.verification_enabled||p?.status==='pending'||verified;
      apply.elements.key.replaceChildren(...data.catalog.filter(b=>b.kind==='verified'&&b.enabled).map(b=>{const o=node('option',b.name);o.value=b.key;return o;}));if(!apply.elements.key.options.length)apply.hidden=true;
      const visible=root.querySelector(':scope > .identity-badges');if(visible)visible.replaceChildren(...data.badges.map(badge));else if(data.badges.length){const group=node('span',undefined,'identity-badges');group.append(...data.badges.map(badge));root.querySelector('header').after(group);}
      document.querySelectorAll('[data-account-identity-badges]').forEach(group=>group.replaceChildren(...data.badges.map(badge)));
      report(status,'');
    }catch(e){report(status,e.message,true);}finally{loading=false;}}
    window.NewPressIdentity = Object.freeze({load});
    details.addEventListener('toggle',()=>{if(details.open&&!root.hidden&&!data)load();});root.querySelector('[data-identity-refresh]').addEventListener('click',load);
    async function submit(form,payload){if(writing)return;writing=true;busy(form,true);try{const session=await client.getSession(true);if(!session.authenticated||String(session.id)!==String(data.user_id))throw new Error('登录状态已变化，请刷新页面');await client.request('/api/v1/social/recognition',{method:'POST',body:{...payload,user_id:data.user_id,version:data.identity.version}});await load();report(status,payload.action==='equip'?'佩戴设置已保存':'认证申请已提交，请等待审核。');}catch(e){report(status,e.message,true);}finally{writing=false;busy(form,false);}}
    root.querySelector('[data-identity-equip]').addEventListener('submit',e=>{e.preventDefault();if(!data)return;const equipped=[...root.querySelectorAll('input[name=medal]:checked:not(:disabled)')].map(b=>b.value);if(equipped.length>data.max_medals){report(status,`最多佩戴 ${data.max_medals} 枚勋章`,true);return;}submit(e.target,{action:'equip',equipped});});
    root.querySelector('[data-identity-apply]').addEventListener('submit',e=>{e.preventDefault();if(!data)return;submit(e.target,{action:'apply',key:e.target.elements.key.value,label:e.target.elements.label.value.trim(),evidence:e.target.elements.evidence.value.trim().replace(/\r?\n/g,' ')});});
  });
})();
