(() => {
  'use strict';
  const page = document.querySelector('[data-cdn-admin]'), root = page?.querySelector('[data-routing]');
  if (!root) return;
  const $ = s => root.querySelector(s), $$ = s => [...root.querySelectorAll(s)];
  const readonly = page.dataset.readonly === 'true', userID = Number(page.dataset.userId), clone = v => JSON.parse(JSON.stringify(v));
  let cdn; try { cdn = JSON.parse(page.dataset.state); } catch { return; }
  let state, draft, selectedDNS = '', selectedRoute = '', dirty = false, busy = false, polling = false, alive = true, timer, renderKey = '';
  const clearTokens = new Set(), requests = new Set();
  const preferredService = $('[data-preferred-service]');
  let preferredServiceKey = '';
  const label = {setup:'SaaS 准备',validation:'验证解析',route:'地区分流',fallback:'回退常规线路',restore:'恢复优选',rollback:'DNS 回滚',inspect:'检查接入',monitor:'自动监测',apply:'执行计划',ready:'待执行',applying:'执行中',applied:'已执行',partial:'部分完成',unknown:'结果待核对',rolled_back:'已回滚',queued:'排队中',running:'执行中',succeeded:'已完成',failed:'失败',interrupted:'已中断',create:'新增',update:'修改',delete:'删除',adopt:'接管',pending:'待执行',attempting:'已发起',done:'有回执',preferred:'优选分流',mixed:'记录不一致',unmanaged:'尚未形成分流'};
  function node(tag,text,cls) { const n=document.createElement(tag); if(text!=null)n.textContent=String(text); if(cls)n.className=cls; return n; }
  function when(v) { if(!v||v.startsWith('0001-'))return '—'; return window.ChunnelTime?.formatDateTime(v)||new Date(v).toLocaleString(); }
  function say(text,error=false) { const n=$('[data-route-message]');n.textContent=text;n.hidden=!text;n.dataset.error=String(error); }
  const currentDNS = () => draft?.dns.find(p=>p.id===selectedDNS), currentRoute = () => draft?.routes.find(r=>r.id===selectedRoute);
  const active = () => (state?.tasks||[]).some(t=>['queued','running'].includes(t.status));
  const cname = value => String(value||'').trim().toLowerCase().replace(/\.$/,'');
  function preferredDetails() {
    const option=preferredService.selectedOptions[0], data=option?.dataset||{}, history=$('[data-preferred-history]'), source=$('[data-preferred-source]');
    $('[data-preferred-note]').textContent=data.note||'选择服务查看维护说明，再按“填入目标”使用；也可以直接填写自己的优选域名。';
    history.textContent=data.history?'维护记录：'+data.history:'';history.hidden=!data.history;
    source.hidden=!data.source;
    if(data.source){source.href=data.source;source.textContent='查看 '+data.name+' 的维护说明 ↗';}else source.removeAttribute('href');
  }
  function syncPreferredService() {
    const target=cname(currentRoute()?.preferred_target), key=selectedRoute+'\n'+target;
    if(key===preferredServiceKey)return;preferredServiceKey=key;
    const match=[...preferredService.options].find(option=>option.value&&(cname(option.value)===target||option.dataset.suffix&&(target===option.dataset.suffix.slice(1)||target.endsWith(option.dataset.suffix))));
    preferredService.value=match?.value||'';preferredDetails();
  }
  function controls() {
    const locked=readonly||!state||busy||active();
    $('[data-routing-save]').disabled=locked||!dirty||page.dataset.cdnDirty==='true';
    $('[data-routing-reset]').disabled=readonly||!state||busy||!dirty;
    $('[data-dns-add]').disabled=locked||(draft?.dns.length||0)>=8;
    $('[data-route-add]').disabled=locked||!draft?.dns.length||!(cdn.settings.profiles||[]).some(p=>p.provider==='cloudflare')||(draft?.routes.length||0)>=20;
    $('[data-dns-remove]').disabled=locked||!currentDNS();$('[data-route-remove]').disabled=locked||!currentRoute();
    preferredService.disabled=locked||!currentRoute();
    $('[data-preferred-fill]').disabled=locked||!currentRoute()||!preferredService.value||cname(currentRoute().preferred_target)===preferredService.value;
    $$('[data-route-action]').forEach(b=>b.disabled=locked||dirty||!selectedRoute||!cdn.enabled||page.dataset.cdnDirty==='true');
    $('[data-routing-dirty]').textContent=!state?'正在读取…':dirty?'接入配置尚未保存':active()?'接入任务处理中':'接入配置已保存';
    $$('[data-routing-export]').forEach(a=>{if(readonly)a.removeAttribute('href');a.setAttribute('aria-disabled',String(readonly));});
  }
  function selectOptions(select,items,value) {
    select.replaceChildren();if(!items.length)select.append(new Option('请先添加并保存',''));
    items.forEach(([id,name])=>select.append(new Option(name,id)));select.value=value||items[0]?.[0]||'';
  }
  function paintLines() {
    const r=currentRoute(), select=$('[data-route="preferred_lines"]');select.replaceChildren();if(!r)return;
    const report=state.reports?.[r.id], lines=report?.dns?.lines||[], def=report?.dns?.default_line;
    for(const line of lines)if(line.id!==def){const option=new Option(`${line.name} · ${line.id}`,line.id);option.selected=r.preferred_lines.includes(line.id);select.append(option);}
    for(const id of r.preferred_lines)if(!lines.some(l=>l.id===id)){const option=new Option(id+' · 待检查确认',id);option.selected=true;select.append(option);}
  }
  function editors() {
    if(!draft)return;
    selectOptions($('[data-dns-select]'),draft.dns.map(p=>[p.id,p.name]),selectedDNS);
    const d=currentDNS();$('[data-dns-editor]').hidden=!d;$('[data-dns-editor]').disabled=!d||readonly;
    if(d){$$('[data-dns]').forEach(n=>n.value=d[n.dataset.dns]||'');$('[data-dns-clear]').checked=clearTokens.has(d.id);$('[data-dns-secret]').textContent=d.secret_saved?'已保存；同一账号留空保留，填写可替换。':'请填写密钥，保存后不回显。';}
    selectOptions($('[data-route-select]'),draft.routes.map(r=>[r.id,r.name]),selectedRoute);
    const r=currentRoute();$('[data-route-editor]').hidden=!r;$('[data-route-editor]').disabled=!r||readonly;
    if(r){
      selectOptions($('[data-route="cf_profile_id"]'),cdn.settings.profiles.filter(p=>p.provider==='cloudflare').map(p=>[p.id,p.name]),r.cf_profile_id);
      selectOptions($('[data-route="dns_profile_id"]'),draft.dns.map(p=>[p.id,p.name]),r.dns_profile_id);
      $$('[data-route]').forEach(n=>{if(n.dataset.route==='preferred_lines')return;const v=r[n.dataset.route];if(n.type==='checkbox')n.checked=!!v;else n.value=v??'';});paintLines();
    }
    syncPreferredService();controls();render();
  }
  function adopt() {
    draft=clone(state.config);dirty=false;clearTokens.clear();
    if(!draft.dns.some(p=>p.id===selectedDNS))selectedDNS=draft.dns[0]?.id||'';
    if(!draft.routes.some(r=>r.id===selectedRoute))selectedRoute=draft.routes[0]?.id||'';
    editors();
  }
  function mark() { dirty=true;controls();render(); }
  function recordText(r) {return r?`${r.name}  ${r.type}  ${r.value}  [${r.line}]  TTL ${r.ttl}${r.id?' · ID '+r.id:''}`:'（无记录）';}
  function planText(p) {
    const parts=[`方案：${state.config.routes.find(r=>r.id===p.route_id)?.hostname||p.route_id}`,`计划：${p.id}`,`有效期：${when(p.expires)}`];
    if(p.kind==='setup'){
      if(p.fallback_state)parts.push(`整个 Cloudflare Zone 的 fallback origin：${p.fallback_before||'未配置'} → ${p.fallback_after}。影响此 Zone 下使用默认回源的所有自定义主机名。`);
      if(p.create_hostname)parts.push('新增自定义主机名，使用 TXT 验证和最低 TLS 1.2。服务商套餐和账单以控制台为准。');
    }
    for(const c of p.changes||[])parts.push(`${label[c.kind]||c.kind}：\n${recordText(c.before)}\n→ ${recordText(c.after)}\n步骤：${label[c.state]||c.state}`);
    if(!p.changes?.length&&!p.create_hostname&&!p.fallback_state)parts.push('无需更改云端记录。');
    if(p.error)parts.push(p.error);return parts.join('\n\n');
  }
  function badge(text,status) {const b=node('span',text,'cdn-badge');b.dataset.status=status;return b;}
  function render() {
    if(!state)return;
    const key=JSON.stringify([selectedRoute,dirty,busy,cdn.enabled,page.dataset.cdnDirty,state]);if(key===renderKey)return;renderKey=key;
    const box=$('[data-route-report]'), report=state.reports?.[selectedRoute];box.replaceChildren();
    if(!report)box.append(node('p','保存方案并检查接入，查看证书、NS 委派和可用解析线路。','cdn-muted'));
    else {
      box.append(node('h3',`${label[report.mode]||report.mode} · ${when(report.checked)}`));
      const indicators=node('div',null,'cdn-actions');
      for(const [name,value,ok] of [['主机名',report.hostname?.status,report.hostname?.status==='active'],['证书',report.hostname?.ssl?.status,report.hostname?.ssl?.status==='active'],['回源',report.fallback?.status,report.fallback?.status==='active'],['NS 委派',report.delegated?'匹配':'未匹配',report.delegated]])indicators.append(badge(name+'：'+(value||'尚无'),ok?'succeeded':'failed'));
      box.append(indicators);
      for(const [name,h] of [['常规线路',report.normal],['优选线路',report.preferred]]){
        const text=h?.checked&&!h.checked.startsWith('0001-')?`${h.healthy?'通过':'未通过'} · ${h.success}/${h.samples} 个地址 · ${h.latency_ms} ms · ${(h.colos||[]).join(', ')||'无机房回执'}${h.error?' · '+h.error:''}`:'尚未探测';box.append(node('p',name+'：'+text,'cdn-muted'));
      }
      box.append(node('p','分配 NS：'+(report.dns?.nameservers||[]).join(' / ')+'；当前 NS：'+(report.actual_ns||[]).join(' / '),'cdn-muted'));
      box.append(node('p',`套餐最低 TTL：${report.dns?.min_ttl||'由接口校验'} 秒。健康结果来自本站服务器，不代表各地区访客。`,'cdn-muted'));
      for(const warning of report.warnings||[])box.append(node('p',warning,'cdn-policy-note'));
      if(report.validation?.length){const details=node('details');details.append(node('summary','云端要求的验证记录'),node('pre',report.validation.map(recordText).join('\n')));box.append(details);}
    }
    const plans=$('[data-routing-plans]'), open=new Set([...plans.querySelectorAll('details[open]')].map(n=>n.dataset.plan));plans.replaceChildren();
    const selected=(state.plans||[]).filter(p=>p.route_id===selectedRoute).slice(-8).reverse();
    if(!selected.length)plans.append(node('p','还没有计划。上方操作先生成预览，不会立即更改 DNS。','cdn-muted'));
    for(const p of selected){
      const card=node('article',null,'cdn-routing-plan'), heading=node('div',null,'cdn-actions');heading.append(node('strong',label[p.kind]||p.kind),badge(label[p.status]||p.status,p.status==='applied'?'succeeded':['partial','unknown'].includes(p.status)?'failed':p.status),node('small',when(p.created),'cdn-muted'));card.append(heading);
      const details=node('details');details.dataset.plan=p.id;details.open=open.has(p.id)||p.status==='ready';details.append(node('summary',`${p.changes?.length||0} 个 DNS 步骤${p.kind==='setup'?' · SaaS 配置':''} · 查看具体变更`),node('pre',planText(p)));card.append(details);
      const actions=node('div',null,'cdn-actions');const locked=readonly||busy||dirty||active()||!cdn.enabled||page.dataset.cdnDirty==='true';
      if(p.status==='ready'){const b=node('button','执行此计划','admin-button');b.type='button';b.disabled=locked||new Date(p.expires)<=new Date();b.addEventListener('click',()=>apply(p));actions.append(b);}
      if(p.kind!=='setup'&&p.kind!=='rollback'&&['applied','partial','unknown'].includes(p.status)){const b=node('button','预览回滚','admin-button admin-button-secondary');b.type='button';b.disabled=locked;b.addEventListener('click',()=>action('preview_rollback',p.id));actions.append(b);}
      card.append(actions);plans.append(card);
    }
    const tasks=$('[data-routing-tasks]');tasks.replaceChildren();
    for(const t of (state.tasks||[]).filter(t=>t.route_id===selectedRoute).slice(-12).reverse()){
      const row=node('div',null,'cdn-routing-task');row.append(badge(label[t.status]||t.status,t.status),node('span',`${label[t.action.replace('preview_','')]||t.action} · ${t.actor} · ${when(t.updated)}`));if(t.error)row.append(node('p',t.error));tasks.append(row);
    }
  }
  async function api(path,method='GET',body) {
    const controller=new AbortController();requests.add(controller);const timeout=setTimeout(()=>controller.abort(),20000);
    try{const res=await fetch('/api/v1/admin/plugins/cdn/routing'+path,{method,credentials:'same-origin',cache:'no-store',signal:controller.signal,headers:body?{'Content-Type':'application/json','X-CSRF-Token':page.dataset.csrf}:{},body:body?JSON.stringify({...body,expected_user_id:userID}):undefined});const data=await res.json();if(!res.ok)throw new Error(data?.error?.message||'接入请求未完成');return data;}
    catch(e){if(e.name==='AbortError')throw new Error('请求超时或取消，请刷新任务状态确认提交结果。');throw e;}finally{clearTimeout(timeout);requests.delete(controller);}
  }
  function schedule() {clearTimeout(timer);if(!alive||readonly||document.hidden)return;timer=setTimeout(refresh,active()?4000:15000);}
  async function refresh() {
    if(readonly||busy||polling||!alive){schedule();return;}polling=true;
    try{const next=await api('');if(!alive||busy)return;state=next;if(!dirty)adopt();else if(draft.version!==state.config.version)say('接入配置已被其他页面更新；当前输入仍保留，请撤销后载入新版本。',true);controls();render();}
    catch(e){if(alive)say(e.message,true);}finally{polling=false;schedule();}
  }
  async function mutation(fn) {
    if(readonly||busy||!alive)return;busy=true;controls();render();
    try{await fn();}catch(e){if(alive)say(e.message,true);}finally{busy=false;if(alive){controls();render();refresh();}}
  }
  async function action(name,planID='') {
    if(dirty||page.dataset.cdnDirty==='true'){say('请先保存服务连接和接入配置。',true);return;}
    const routeID=selectedRoute,version=state.config.version,cdnVersion=cdn.settings.version;
    await mutation(async()=>{const result=await api('/action','POST',{action:name,route_id:routeID,plan_id:planID,expected_version:version,cdn_version:cdnVersion,confirm:name==='apply'});say('接入任务已提交：'+result.task.id+'。可在下方查看结果。');});
  }
  async function apply(plan) {
    if(readonly||busy||dirty||active())return;
    const dialog=$('[data-routing-confirm]');$('[data-routing-confirm-text]').textContent=planText(plan);dialog.returnValue='';dialog.showModal();
    const accepted=await new Promise(resolve=>dialog.addEventListener('close',()=>resolve(dialog.returnValue==='confirm'),{once:true}));
    if(accepted&&alive&&selectedRoute===plan.route_id)await action('apply',plan.id);
  }
  $('[data-routing-form]').addEventListener('submit',e=>{e.preventDefault();if(!e.currentTarget.reportValidity())return;mutation(async()=>{state=await api('/settings','PUT',{config:draft,clear_tokens:[...clearTokens],cdn_version:cdn.settings.version});adopt();say('接入配置已保存。');});});
  $$('[data-dns]').forEach(n=>n.addEventListener('input',()=>{const p=currentDNS();if(!p)return;p[n.dataset.dns]=n.value;if(n.dataset.dns==='provider'){p.secret='';p.token='';p.access_key='';p.secret_saved=false;editors();}mark();}));
  $$('[data-route]').forEach(n=>n.addEventListener('input',()=>{const r=currentRoute();if(!r)return;r[n.dataset.route]=n.multiple?[...n.selectedOptions].map(o=>o.value):n.type==='checkbox'?n.checked:n.type==='number'?Number(n.value):n.value;if(n.dataset.route==='preferred_target')syncPreferredService();mark();}));
  preferredService.addEventListener('change',()=>{preferredDetails();controls();});
  $('[data-preferred-fill]').addEventListener('click',()=>{
    const route=currentRoute();if(readonly||busy||active()||!route||!preferredService.value)return;
    route.preferred_target=preferredService.value;$('[data-route="preferred_target"]').value=route.preferred_target;
    syncPreferredService();mark();say('已填入 '+preferredService.selectedOptions[0].dataset.name+' 的优选目标。请保存接入配置，再检查接入并预览地区分流。');
  });
  $('[data-dns-clear]').addEventListener('change',e=>{if(e.target.checked)clearTokens.add(selectedDNS);else clearTokens.delete(selectedDNS);mark();});
  $('[data-dns-select]').addEventListener('change',e=>{selectedDNS=e.target.value;editors();});$('[data-route-select]').addEventListener('change',e=>{selectedRoute=e.target.value;editors();});
  $('[data-dns-add]').addEventListener('click',()=>{selectedDNS='dns-'+crypto.randomUUID().slice(0,8);draft.dns.push({id:selectedDNS,name:'新的 DNS 连接',provider:'aliyun',zone:'',access_key:'',secret:'',token:''});mark();editors();$('[data-dns="name"]').focus();});
  $('[data-route-add]').addEventListener('click',()=>{selectedRoute='route-'+crypto.randomUUID().slice(0,8);draft.routes.push({id:selectedRoute,name:'新的接入方案',cf_profile_id:cdn.settings.profiles.find(p=>p.provider==='cloudflare')?.id||'',dns_profile_id:selectedDNS||draft.dns[0]?.id||'',hostname:'',fallback_origin:'',target:'',preferred_target:'',preferred_lines:[],ttl:600,manage_existing:false,health_path:'/',expected_body:'',monitor:false,interval:300,failure_threshold:3,recovery_threshold:3,cooldown:900,auto_failover:false,auto_recover:false});mark();editors();$('[data-route="name"]').focus();});
  $('[data-dns-remove]').addEventListener('click',()=>{if(draft.routes.some(r=>r.dns_profile_id===selectedDNS)){say('请先移除或修改使用此 DNS 连接的方案。',true);return;}draft.dns=draft.dns.filter(p=>p.id!==selectedDNS);selectedDNS=draft.dns[0]?.id||'';mark();editors();});
  $('[data-route-remove]').addEventListener('click',()=>{draft.routes=draft.routes.filter(r=>r.id!==selectedRoute);selectedRoute=draft.routes[0]?.id||'';mark();editors();say('方案已从编辑区移除，保存后生效。云端记录仍保留；需要撤销解析时先使用计划回滚。');});
  $('[data-routing-reset]').addEventListener('click',()=>{adopt();say('已载入最近读取的配置。');});
  $$('[data-route-action]').forEach(b=>b.addEventListener('click',()=>action(b.dataset.routeAction)));
  $('[data-route-refresh]').addEventListener('click',refresh);
  page.addEventListener('cdn:state',e=>{cdn=e.detail;if(state&&!dirty&&!busy)editors();else controls();});
  document.addEventListener('visibilitychange',()=>{if(document.hidden)clearTimeout(timer);else refresh();});
  window.addEventListener('beforeunload',e=>{if(dirty){e.preventDefault();e.returnValue='';}});
  window.addEventListener('pagehide',()=>{alive=false;clearTimeout(timer);requests.forEach(c=>c.abort());const dialog=$('[data-routing-confirm]');if(dialog.open){dialog.returnValue='cancel';dialog.close();}});
  window.addEventListener('pageshow',()=>{alive=true;refresh();});
  controls();refresh();
})();
