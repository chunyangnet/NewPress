import {required} from './ledger-admin-dom';
import {CDNSession} from '../shared/cdn-session';
import {pluginError,own} from '../shared/plugin-data-common';
import {cdnStatus,cdnDraft,cdnImport,cdnSaved,mergeCDN,cdnIdentity,equal,cdnOK,enqueueReceipt} from '../shared/cdn-data';
import type {CDNStatus,CDNSettings,CDNDraft,CDNProfile,CDNJob,BenchmarkRequest} from '../shared/cdn-data';
import {fieldValue,writeField} from '../shared/cdn-fields';
(() => {
  'use strict';
  const found = document.querySelector<HTMLElement>('[data-cdn-admin]');
  if (!found) return;const root=found;
  const $=<T extends HTMLElement=HTMLElement>(s:string):T=>required<T>(root,s), $$=<T extends HTMLElement=HTMLElement>(s:string):T[]=>[...root.querySelectorAll<T>(s)];
  let state:CDNStatus;
  try { state = cdnStatus(JSON.parse(root.dataset.state||'')); } catch { $('[data-message]').textContent='CDN 初始配置无效，请重新打开页面。';$('[data-message]').hidden=false;root.querySelectorAll<HTMLButtonElement>('button,input,select,textarea').forEach(n=>n.disabled=true);return; }
  const session=new CDNSession(root,root,'/api/v1/admin/plugins/cdn',lost=>{
 clearTimeout(timer);polling=false;cancelConfirmation?.();
 if(lost){draft.profiles=[];state.settings.profiles=[];state.settings.profile_views=[];state.jobs=[];state.counts={};state.last_error='';state.settings.asset_base='';draft.asset_base='';root.dataset.state='';clearTokens.clear();historyID='';selected='';$$<HTMLInputElement|HTMLTextAreaElement>('[data-profile],[data-targets],[name=url],[name=candidates]').forEach(n=>{n.value='';});$$('[data-result-format]').forEach(n=>n.removeAttribute('href'));$('[data-confirm-text]').textContent='';$('[data-confirm-targets]').textContent='';renderDraft();renderStatus();say('登录或权限已变化，请重新打开 CDN 页面。',true);}
 paintButtons();
 },()=>{paintButtons();void refresh();});
 const readonly=session.readonly;
  
  const labels:Record<string,string> = { purge_url: 'URL 刷新', purge_directory: '目录刷新', purge_prefix: '前缀刷新', purge_host: '域名刷新', purge_tag: '标签刷新', purge_all: '全站刷新', warm: '云端预热', test: '验证连接', diagnose: 'URL 诊断', benchmark: 'Cloudflare 优选', queued: '待处理', running: '进行中', retrying: '等待重试', accepted: '云端已受理', succeeded: '检查已完成', failed: '失败', interrupted: '已中断', cancelled: '已取消' };
  let draft:CDNDraft, selected = '', dirty = false, polling = false, timer=0, historyID = '';const clearTokens=new Set<string>();let edits=0;

  let lastJobsRender = '';
  let cancelConfirmation:(()=>void)|null=null;
  function el<K extends keyof HTMLElementTagNameMap>(tag:K, text?:unknown, cls?:string) { const n = document.createElement(tag); if (text != null) n.textContent = String(text); if (cls) n.className = cls; return n; }
  function lines(v:unknown) { return String(v || '').split(/\r?\n/).map(x => x.trim()).filter(Boolean); }
  function bytes(n:number) { n = Number(n) || 0; return n >= 1048576 ? (n / 1048576).toFixed(1) + ' MiB' : (n / 1024).toFixed(0) + ' KiB'; }
  function when(v:string) {
    if (!v||v.startsWith('0001-')) return '—';
    if (window.ChunnelTime) return window.ChunnelTime.formatDateTime(v) || '—';
    try { return new Date(v).toLocaleString('sv-SE',{timeZone:document.documentElement.dataset.siteTimezone||'UTC',year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'}); } catch { return '—'; }
  }
  function capability(id:string|undefined) { return (state.capabilities || []).find(p => p.id === id); }
  function current() { return draft.profiles.find(p => p.id === selected); }
  function profileName(id:string) { return state.settings.profiles.find(p => p.id === id)?.name || id; }
  function say(text:string, error = false) { const n = $('[data-message]'); n.textContent = text; n.hidden = !text; n.dataset.error = String(error); }
  function markDirty() { edits++;dirty = true; paintButtons(); }
  function adopt(v:CDNSettings) {draft=cdnDraft(v);dirty=false;clearTokens.clear();renderDraft();}
  function renderDraft() {
    if(!draft.profiles.some(p=>p.id===selected))selected=draft.profiles[0]?.id||'';
    $$<HTMLInputElement>('[data-setting]').forEach(input=>{const key=input.dataset.setting;if(!own(draft,key))return;const value=draft[key];if(input.type==='checkbox')input.checked=!!value;else input.value=String(value??'');});
    renderProfiles();paintButtons();
  }
  function paintButtons() {
	root.dataset.cdnDirty = String(dirty);root.dataset.cdnLocked=String(session.locked);root.dispatchEvent(new Event('cdn:controls'));
    $<HTMLButtonElement>("[data-save]").disabled = session.locked || !dirty;
    $<HTMLButtonElement>("[data-reset]").disabled = session.locked || !dirty;
    $<HTMLButtonElement>("[data-add]").disabled = session.locked || draft.profiles.length >= 8;
    $<HTMLButtonElement>("[data-remove]").disabled = session.locked;
    $<HTMLButtonElement>("[data-test]").disabled = session.locked || dirty || !state.enabled || !selected;
    $<HTMLButtonElement>("[data-submit-job]").disabled = session.locked || dirty || !state.enabled || !state.settings.profiles.length;
    $<HTMLButtonElement>("[data-benchmark-start]").disabled = session.locked || dirty || !state.enabled || !$<HTMLSelectElement>("[data-benchmark-profile]").value;
    $<HTMLButtonElement>("[data-import]").disabled = session.locked;
    $$<HTMLButtonElement>('[data-profiles] button').forEach(button=>button.disabled=session.locked);
    $('[data-save-status]').textContent = session.accessLost ? '登录或权限已变化' : session.uncertain ? '操作结果待核对，请检查记录后重新打开页面' : readonly ? '只读预览 · 请登录后配置' : session.busy ? '正在处理…' : dirty ? '有未保存的更改；操作前请先保存' : '配置已保存';
    $('.cdn-savebar').dataset.dirty = String(dirty);
    $$('[data-export]').forEach(a => { a.setAttribute('aria-disabled', String(readonly||session.accessLost)); if (readonly||session.accessLost) a.removeAttribute('href'); });
  }
  function renderProfiles() {
    const list = $('[data-profiles]'); list.replaceChildren();
    for (const p of draft.profiles) {
      const b = el('button', null, 'cdn-profile'); b.type = 'button'; b.setAttribute('aria-pressed', String(p.id === selected)); b.disabled = session.locked;
      b.append(el('b', p.name || '未命名连接'), el('span', capability(p.provider)?.name || p.provider));
      b.addEventListener('click', () => { selected = p.id; renderProfiles(); }); list.append(b);
    }
    const p = current(); $('[data-empty]').hidden = !!p; $<HTMLFieldSetElement>("[data-editor]").hidden = !p; $<HTMLFieldSetElement>("[data-editor]").disabled = !p || readonly || session.accessLost;
    if (!p) return;
    $('[data-editor-title]').textContent = p.name || '连接配置';
    $('[data-provider-note]').textContent = capability(p.provider)?.note || '';
    $$<HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement>('[data-profile]').forEach(input=>{const key=input.dataset.profile;if(key!=='secret'&&key!=='token'&&!own(p,key))return;const value=p[key];if(input instanceof HTMLInputElement&&input.type==='checkbox')input.checked=!!value;else input.value=key==='domains'?p.domains.join('\n'):String(value??'');});
    $<HTMLInputElement>("[data-clear-token]").checked = clearTokens.has(p.id);
    const cf = p.provider === 'cloudflare', resource = ['cloudflare','edgeone','cloudfront'].includes(p.provider), token = ['tencent','edgeone','aliyun','cloudfront'].includes(p.provider);
    $('[data-resource-field]').hidden = !resource; $('[data-access-field]').hidden = cf;
    $('[data-token-field]').hidden = !token; $('[data-clear-token-field]').hidden = !token;
    $('[data-resource-label]').textContent = p.provider === 'cloudfront' ? 'Distribution ID' : 'Zone ID';
    $('[data-secret-label]').textContent = cf ? 'API Token' : 'Secret Key';
    const saved = state.settings.profile_views?.find(v => v.id === p.id && v.provider === p.provider && v.access_key === p.access_key && v.resource_id === p.resource_id);
    $('[data-secret-hint]').textContent = saved?.secret_saved ? '已保存，留空保留；填写可替换。' : '保存后不再回显。';
    $('[data-token-hint]').textContent = saved?.token_saved ? '已保存临时令牌，留空保留；勾选下方可清除。' : '长期密钥无需填写；临时令牌到期后需更新。';
    const caps = $('[data-capabilities]'); caps.replaceChildren();
    for (const op of capability(p.provider)?.operations || []) caps.append(el('span', labels[op], 'cdn-chip'));
    paintButtons();
  }
  function fillProfiles(select:HTMLSelectElement, filter:(p:CDNProfile)=>boolean = () => true) {
    const previous = select.value; select.replaceChildren();
    const profiles = state.settings.profiles.filter(filter);
    if (!profiles.length) select.append(new Option('请先保存连接', ''));
    for (const p of profiles) select.append(new Option(p.name, p.id));
    if (profiles.some(p => p.id === previous)) select.value = previous;
  }
  function operationChanged() {
    const op = $<HTMLSelectElement>("[data-operation]").value, broad = ['purge_all','purge_host','purge_prefix','purge_directory','purge_tag'].includes(op);
    $('[data-target-field]').hidden = op === 'purge_all';
    $('[data-operation-note]').textContent = op === 'purge_all' ? '会清除整个 Zone / Distribution 的缓存，包括该资源下未列入连接的域名。提交前将再次显示范围。' : op === 'diagnose' ? '一次有限 GET：检查 HTTPS、缓存头、状态码和证书有效期。不跟随重定向、不携带登录凭据。' : op === 'warm' ? '调用服务商预热接口，可能产生流量费用和回源请求；受服务商配额限制。' : broad ? '此操作影响多个缓存对象。确认域名和目录范围后再提交。' : '已受理表示服务商接收任务；全球节点完成时间以服务商控制台为准。';
    $('[data-target-help]').textContent = op === 'purge_host' ? '每行一个已配置域名，不含协议。' : op === 'purge_tag' ? '每行一个缓存标签，实际权限取决于套餐。' : ['purge_directory','purge_prefix'].includes(op) ? '每行一个以 / 结尾的 HTTPS 目录，不带查询；七牛每批最多 10 个。' : op === 'diagnose' ? '填写一个无查询参数的公开资源 URL。' : '每行一个 HTTPS URL，每批最多 100 条。';
    $<HTMLTextAreaElement>("[data-targets]").required = op !== 'purge_all';
  }
  function updateOperations() {
    const p = state.settings.profiles.find(p => p.id === $<HTMLSelectElement>("[data-action-profile]").value), select = $<HTMLSelectElement>("[data-operation]"), old = select.value; select.replaceChildren();
    for (const op of capability(p?.provider)?.operations || []) if (!['test','benchmark'].includes(op)) select.append(new Option(labels[op], op));
    if ([...select.options].some(o => o.value === old)) select.value = old;
    operationChanged(); paintButtons();
  }
  function renderResults() {
    const jobs = (state.jobs || []).filter(j => j.benchmark), history = $<HTMLSelectElement>("[data-benchmark-history]");
    if (!jobs.some(j => j.id === historyID)) historyID = jobs[0]?.id || '';
    history.replaceChildren();
    if (!jobs.length) history.append(new Option('尚无测速结果', ''));
    jobs.forEach(j => history.append(new Option(when(j.updated) + ' · ' + profileName(j.profile_id), j.id)));
    history.value = historyID;
    const job = jobs.find(j => j.id === historyID), table = $('[data-node-results]'); table.replaceChildren();
    $('[data-benchmark-exports]').hidden = !job || readonly;
    if (!job) { const tr = el('tr'), td = el('td', '运行第一次测量，了解当前出口可用的 Cloudflare 线路。', 'cdn-table-empty'); td.colSpan = 6; tr.append(td); table.append(tr); $('[data-benchmark-summary]').textContent = ''; return; }
    const result = job.benchmark!;
    $('[data-benchmark-summary]').textContent = `${when(result.finished)} · ${result.results.length} 个地址 · 已读取 ${bytes(result.bytes)} · 网段获取于 ${when(result.ip_list_fetched)}`;
    for (const n of result.results) {
      const tr = el('tr'), ip = el('td'), latency = el('td'), status = el('td');
      ip.append(el('b', n.ip), el('small', `${n.family} · ${n.colo || '未确认机房'}`)); if (n.error) ip.append(el('small', n.error));
      latency.append(el('b', n.success ? `${n.ttfb_p50.toFixed(1)} / ${n.ttfb_p95.toFixed(1)} ms` : '—'));
      const badge = el('span', n.eligible ? '符合阈值' : '未达阈值', 'cdn-badge'); badge.dataset.status = n.eligible ? 'succeeded' : 'failed'; status.append(badge);
      tr.append(ip, el('td', `${n.success_rate.toFixed(0)}% (${n.success}/${n.rounds})`), latency, el('td', n.success ? `${n.tcp_ms.toFixed(1)} / ${n.tls_ms.toFixed(1)} ms` : '—'), el('td', n.mbps > 0 ? `${n.mbps.toFixed(2)} Mbps` : '—'), status); table.append(tr);
    }
    $$<HTMLAnchorElement>('[data-result-format]').forEach(a => a.href = `/api/v1/admin/plugins/cdn/export?kind=benchmark&job=${encodeURIComponent(job.id)}&format=${a.dataset.resultFormat}`);
  }
  function renderJobs() {
    const body = $('[data-jobs]'), filter = $<HTMLSelectElement>("[data-filter]").value, search = $<HTMLInputElement>("[data-search]").value.toLowerCase().trim();
    const signature = JSON.stringify([filter,search,readonly,session.busy,dirty,state.jobs,state.settings.profiles.map(p=>[p.id,p.name])]);
    if(signature===lastJobsRender)return;
    const openDetails=new Set([...body.querySelectorAll<HTMLDetailsElement>('details[open]')].map(n=>n.dataset.job));
    lastJobsRender=signature;body.replaceChildren();
    const jobs = (state.jobs || []).filter(j => (!filter || j.status === filter) && (!search || [j.id,profileName(j.profile_id),...(j.targets || [])].join(' ').toLowerCase().includes(search)));
    if (!jobs.length) { const tr = el('tr'), td = el('td', '没有符合条件的任务。', 'cdn-table-empty'); td.colSpan = 5; tr.append(td); body.append(tr); }
    for (const j of jobs) {
      const tr = el('tr'), task = el('td'), status = el('td'), time = el('td'), detail = el('td'), actions = el('td');
      task.append(el('b', labels[j.operation] || j.operation),el('small',profileName(j.profile_id)),el('small',j.source === 'manual' ? j.actor : '自动联动 · ' + j.source));
      const badge = el('span',labels[j.status] || j.status,'cdn-badge');badge.dataset.status=j.status;status.append(badge);
      time.append(el('span',when(j.updated)),el('small',`尝试 ${j.attempts} 次`)); if (j.status === 'retrying') time.append(el('small','下次：'+when(j.next)));
      if (j.error) detail.append(el('span',j.error)); else if (j.receipt) detail.append(el('span',j.receipt.message)); else detail.append(el('span',j.operation === 'benchmark' ? '限量测速 · 可取消本地请求' : `${j.targets?.length || 0} 个目标`));
      const disclosure = el('details'), summary = el('summary','查看详情');disclosure.dataset.job=j.id;disclosure.open=openDetails.has(j.id);disclosure.append(summary,el('pre','任务 ID：'+j.id));
      if (j.receipt?.id) disclosure.append(el('pre','云端回执：'+j.receipt.id)); if (j.targets?.length) disclosure.append(el('pre',j.targets.join('\n')));
      if (j.diagnostic) { disclosure.append(el('pre',JSON.stringify(j.diagnostic,null,2))); } if (j.receipt?.details) disclosure.append(el('pre',JSON.stringify(j.receipt.details,null,2)));detail.append(disclosure);
      const action = ['queued','running','retrying'].includes(j.status) ? 'cancel' : ['failed','interrupted','cancelled'].includes(j.status) ? 'retry' : '';
      if (action) {const b=el('button',action==='cancel'?'取消':'重试','admin-button admin-button-quiet');b.type='button';b.disabled=session.locked||dirty; b.addEventListener('click',()=>jobAction(j,action));actions.append(b);}
      if(j.benchmark){const b=el('button','查看测量','admin-button admin-button-quiet');b.type='button';b.addEventListener('click',()=>{historyID=j.id;renderResults();$('#cdn-optimizer').scrollIntoView({behavior:'auto'});});actions.append(b);}
      tr.append(task,status,time,detail,actions);body.append(tr);
    }
  }
  function renderStatus() {
	root.dispatchEvent(new CustomEvent('cdn:state', {detail: state}));
    root.dataset.cdnActive=String(state.enabled);$('[data-cdn-enabled]').textContent=state.enabled?(state.settings.paused?'队列已暂停':'CDN 插件已启用'):'CDN 插件未启用';$('[data-cdn-disabled]').hidden=state.enabled||readonly;
    $('[data-cdn-connection-count]').textContent=`${state.settings.profiles.length} 个连接 · ${state.settings.profiles.reduce((sum,p)=>sum+p.domains.length,0)} 个域名`;
    const c=state.counts||{};$$('[data-count]').forEach(n=>n.textContent=String(n.dataset.count==='queued'?(c.queued||0)+(c.retrying||0):n.dataset.count==='failed'?(c.failed||0)+(c.interrupted||0):c[n.dataset.count||'']||0));
    $('[data-last-error]').textContent=state.last_error||'';$('[data-last-error]').hidden=!state.last_error;
    fillProfiles($<HTMLSelectElement>("[data-action-profile]"));fillProfiles($<HTMLSelectElement>("[data-benchmark-profile]"),p=>p.provider==='cloudflare');updateOperations();renderResults();renderJobs();paintButtons();
  }
  const api=(path:string,method='GET',body?:object)=>session.api(path,method,body);
  function schedule(){clearTimeout(timer);if(readonly||!session.active||session.disposed||session.accessLost||document.hidden)return;const active=state.jobs.some(j=>['queued','running','retrying'].includes(j.status));timer=setTimeout(()=>void refresh(true),active?4000:15000);}
  async function refresh(silent=false){
    if(readonly||!session.active||session.disposed||session.accessLost)return;
    if(session.busy||polling){schedule();return;}
    polling=true;const token=session.epoch,generation=session.generation;
    try{const next=cdnStatus(await api('/status'));if(token!==session.epoch||!session.current(generation)||session.busy)return;
      if(next.settings.version<state.settings.version)return;
      state=next;if(!dirty)adopt(state.settings);else if(draft.version!==state.settings.version)say('配置已被其他页面更新。当前输入仍保留，撤销更改可载入新版本。',true);
      renderStatus();$('[data-last-refresh]').textContent='更新于 '+when(new Date().toISOString());
    }catch(error){if(token===session.epoch&&session.current(generation)&&!silent)say(pluginError(error).message,true);}
    finally{if(token===session.epoch&&session.current(generation)){polling=false;schedule();}}
  }
  async function mutation(fn:()=>Promise<void>){
    const generation=session.start();if(generation===undefined)return;polling=false;clearTimeout(timer);paintButtons();renderJobs();
    try{await fn();}catch(error){if(session.current(generation))say(session.fail(error),true);}
    finally{if(generation===session.generation){session.busy=false;if(session.current(generation)){paintButtons();renderJobs();void refresh(true);}}}
  }
  function confirmation(text:string,targets:string[]=[]):Promise<boolean>{if(session.locked||$<HTMLDialogElement>('[data-confirm]').open)return Promise.resolve(false);return new Promise(resolve=>{const d=$<HTMLDialogElement>("[data-confirm]");$('[data-confirm-text]').textContent=text;$('[data-confirm-targets]').textContent=targets.join('\n');$('[data-confirm-targets]').hidden=!targets.length;d.returnValue='';cancelConfirmation=()=>{d.returnValue='cancel';d.close();};d.addEventListener('close',()=>{cancelConfirmation=null;resolve(session.active&&d.returnValue==='confirm');},{once:true});d.showModal();});}
  async function enqueue(operation:string,profileID:string,targets:string[]=[],benchmark?:BenchmarkRequest){
    if(dirty){say('请先保存连接和策略配置。',true);return;}
    const version=state.settings.version,p=state.settings.profiles.find(p=>p.id===profileID);if(!p){say('请先保存并选择连接。',true);return;}
    const broad=['purge_all','purge_host','purge_prefix','purge_directory','purge_tag'].includes(operation);
    if(broad&&!await confirmation(operation==='purge_all'?`清除「${p.name}」整个 Zone / Distribution（${p.resource_id}）的缓存，会影响该云端资源下全部域名并增加回源流量。`:`在「${p.name}」执行${labels[operation]}，请核对下面的范围。`,targets))return;
    if(dirty||state.settings.version!==version||session.locked||!state.enabled){say('配置或页面状态已变化，请重新核对操作范围。',true);return;}
    const request={action:'enqueue',profile_id:profileID,operation,targets,benchmark,expected_version:version,confirm_broad:broad};
    await mutation(async()=>{const job=enqueueReceipt(await api('/action','POST',request),request);say('任务已入队：'+job.id+'。可在运行记录查看结果。');if(operation==='benchmark')historyID='';});
  }
  async function jobAction(j:CDNJob,action:'cancel'|'retry'){if(dirty){say('请先保存配置。',true);return;}const version=state.settings.version;if(action==='retry'&&!await confirmation('重试将重新执行此任务。已提交到云端的操作可能仍在进行，请先核对回执；预热可能再次产生流量。',[labels[j.operation]||j.operation,profileName(j.profile_id)]))return;if(dirty||version!==state.settings.version||!state.jobs.some(next=>next.id===j.id&&next.status===j.status))return;await mutation(async()=>{cdnOK(await api('/action','POST',{action,job_id:j.id,expected_version:version}));say(action==='cancel'?'已取消本地任务。云端已受理的任务可能继续执行。':'任务已重新排队。');});}
  $<HTMLFormElement>("#cdn-settings").addEventListener('submit',e=>{e.preventDefault();if(!$<HTMLFormElement>("#cdn-settings").reportValidity())return;mutation(async()=>{
 const sent=structuredClone(draft),cleared=new Set(clearTokens);
 const saved=cdnSaved(await api('/settings','PUT',{settings:sent,clear_tokens:[...cleared]}),sent,cleared);
 draft=mergeCDN(sent,draft,saved);state.settings=saved;
 for(const id of cleared){const before=sent.profiles.find(p=>p.id===id),current=draft.profiles.find(p=>p.id===id);if(before&&current&&equal(cdnIdentity(before),cdnIdentity(current)))clearTokens.delete(id);}
 dirty=!equal(draft,cdnDraft(saved))||clearTokens.size>0;renderDraft();renderStatus();say(dirty?'配置已保存；等待期间的新输入仍未保存。':'配置已保存。');
 });});
  $$<HTMLInputElement>('[data-setting]').forEach(input=>input.addEventListener('input',()=>{if(readonly||session.accessLost)return;writeField(draft,input.dataset.setting,fieldValue(input));markDirty();}));
  $$<HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement>('[data-profile]').forEach(input=>input.addEventListener('input',()=>{const p=current();if(!p||readonly||session.accessLost)return;const key=input.dataset.profile;writeField(p,key,key==='domains'?lines(input.value):fieldValue(input));if(key==='provider'){p.secret='';p.token='';p.access_key='';p.resource_id='';clearTokens.delete(p.id);renderProfiles();}markDirty();}));
  $<HTMLInputElement>("[data-clear-token]").addEventListener('change',e=>{if((e.currentTarget as HTMLInputElement).checked)clearTokens.add(selected);else clearTokens.delete(selected);markDirty();});
  $<HTMLButtonElement>("[data-add]").addEventListener('click',()=>{if(session.locked||draft.profiles.length>=8)return;selected='cdn-'+crypto.randomUUID().slice(0,8);draft.profiles.push({id:selected,name:'新的 CDN 连接',provider:'cloudflare',resource_id:'',domains:[],access_key:'',secret:'',token:'',auto_purge:true});markDirty();renderProfiles();$<HTMLInputElement>("[data-profile=\"name\"]").focus();});
  $<HTMLButtonElement>("[data-remove]").addEventListener('click',async()=>{const p=current();if(!p||session.locked)return;if(!await confirmation(`移除「${p.name}」后，该连接的待处理任务会在保存配置时取消。`))return;if(selected!==p.id||session.locked)return;draft.profiles=draft.profiles.filter(item=>item.id!==p.id);selected=draft.profiles[0]?.id||'';markDirty();renderProfiles();});
  $<HTMLButtonElement>("[data-test]").addEventListener('click',()=>enqueue('test',selected));
  $<HTMLButtonElement>("[data-reset]").addEventListener('click',()=>{adopt(state.settings);renderStatus();say('已载入最近读取的配置。');});
  $<HTMLSelectElement>("[data-action-profile]").addEventListener('change',updateOperations);$<HTMLSelectElement>("[data-operation]").addEventListener('change',operationChanged);
  $<HTMLTextAreaElement>("[data-targets]").addEventListener('input',()=>{$('[data-target-count]').textContent=lines($<HTMLTextAreaElement>("[data-targets]").value).length+' 个目标';});
  $<HTMLFormElement>("[data-operation-form]").addEventListener('submit',e=>{e.preventDefault();if(!(e.currentTarget as HTMLFormElement).reportValidity())return;const op=$<HTMLSelectElement>("[data-operation]").value;enqueue(op,$<HTMLSelectElement>("[data-action-profile]").value,op==='purge_all'?[]:lines($<HTMLTextAreaElement>("[data-targets]").value));});
  const bench=$<HTMLFormElement>("[data-benchmark-form]");
  function budget(){const f=new FormData(bench),count=lines(f.get('candidates')).length||Number(f.get('sample_count'));$('[data-benchmark-budget]').textContent=`读取上限约 ${bytes(count*(Number(f.get('rounds'))*8192+Number(f.get('download_bytes'))))}，另含协议开销`;}
  bench.addEventListener('input',budget);$<HTMLSelectElement>("[data-benchmark-profile]").addEventListener('change',()=>{const p=state.settings.profiles.find(p=>p.id===$<HTMLSelectElement>("[data-benchmark-profile]").value);if(p)(bench.elements.namedItem('url') as HTMLInputElement).value='https://'+p.domains[0]+'/cdn-cgi/trace';paintButtons();});
  bench.addEventListener('submit',e=>{e.preventDefault();if(!bench.reportValidity())return;const f=new FormData(bench),b:BenchmarkRequest={url:String(f.get('url')).trim(),candidates:lines(f.get('candidates')),ipv6:f.get('family')==='6',sample_count:1,rounds:1,concurrency:1,timeout_seconds:2,download_bytes:0,minimum_success:50};for(const k of ['sample_count','rounds','concurrency','timeout_seconds','download_bytes','minimum_success'] as const)b[k]=Number(f.get(k));enqueue('benchmark',$<HTMLSelectElement>("[data-benchmark-profile]").value,[],b);});
  $<HTMLSelectElement>("[data-benchmark-history]").addEventListener('change',e=>{historyID=(e.currentTarget as HTMLSelectElement).value;renderResults();});
  $<HTMLSelectElement>("[data-filter]").addEventListener('change',renderJobs);$<HTMLInputElement>("[data-search]").addEventListener('input',renderJobs);$<HTMLButtonElement>("[data-refresh]").addEventListener('click',()=>refresh());
  $<HTMLButtonElement>("[data-import]").addEventListener('click',()=>{if(!readonly&&!session.busy)$<HTMLInputElement>("[data-import-file]").click();});
  $<HTMLInputElement>("[data-import-file]").addEventListener('change',async e=>{
 const input=e.currentTarget as HTMLInputElement,file=input.files?.[0];if(!file||session.locked)return;
 const generation=session.generation,at=edits,version=state.settings.version;
 try{if(file.size>131072)throw new Error('配置文件不能超过 128 KiB');
  const raw:unknown=JSON.parse(await file.text());const next=cdnImport(raw);next.version=version;
  if(!session.current(generation)||at!==edits||session.locked)return;
  if(!await confirmation('导入内容会替换当前编辑中的连接和策略。导出文件不包含密钥，新连接需要重新填写密钥。保存后才会生效。'))return;
  if(!session.current(generation)||at!==edits||version!==state.settings.version||session.locked)return;
  draft=next;selected=next.profiles[0]?.id||'';clearTokens.clear();markDirty();renderDraft();say('配置已导入编辑区，请检查域名并补齐密钥后保存。');
 }catch(error){if(session.current(generation))say(pluginError(error).message,true);}finally{input.value='';}
 });
  document.addEventListener('visibilitychange',()=>{if(document.hidden)clearTimeout(timer);else void refresh(true);},{signal:session.events.signal});
  window.addEventListener('beforeunload',e=>{if(dirty||session.busy){e.preventDefault();e.returnValue='';}},{signal:session.events.signal});
  document.addEventListener('chunnel:admin-before-update',e=>{if(dirty||session.busy||session.uncertain||$<HTMLDialogElement>('[data-confirm]').open)e.preventDefault();},{signal:session.events.signal});
  adopt(state.settings);renderStatus();budget();schedule();
})();
