import {required,input} from './ledger-admin-dom';
import {PluginRequests} from '../shared/plugin-requests';
import {PluginRequestError,pluginError} from '../shared/plugin-data-common';
import {apiError} from '../shared/plugin-runtime-data';
import {phases,busyPhases,terminal,updatePolicy,updateSnapshot,updateState,updateAcknowledgment,updateTicket} from '../shared/update-data';
import type {UpdatePolicy,UpdateSnapshot,UpdateState} from '../shared/update-data';
type PolicyDraft=Pick<UpdatePolicy,'auto_check'|'check_interval_minutes'|'observation_seconds'>;
(() => {
  'use strict';
  const found = document.querySelector<HTMLElement>('[data-update-center]');
  if (!found) return;
  const root=found,requests=new PluginRequests(),eventScope=new AbortController();
  let disposed=false,lifecycle=0,readEpoch=0,pollTimer=0,polling=false;
  let unconfirmedStart:{id:string;target:string}|null=null;
  const $=<T extends HTMLElement=HTMLElement>(name:string):T=>required<T>(root,`[data-update-${name}]`);
  const readOnly = root.dataset.readonly === 'true';
  const ticketKey = 'newpress-update-ticket';
  let ticket = '', snapshot:UpdateSnapshot|null = null, state:UpdateState = {id:'',phase:'idle',percent:0,downloaded:0,total:0,target:'',started_at:'0001-01-01T00:00:00Z',updated_at:'0001-01-01T00:00:00Z',logs:[]}, sending = false, previousBytes = 0, previousTime = 0, logSignature = '', reconnects = 0, loginRequired = false;
  const settingsController = window.ChunnelAdminUI?.savebar($<HTMLFormElement>('source'));
  const policyForm = $<HTMLFormElement>('policy-form');
  const policyInputs={auto_check:input(policyForm,'auto_check'),check_interval_minutes:input(policyForm,'check_interval_minutes'),observation_seconds:input(policyForm,'observation_seconds')};
  const policyBar = required(policyForm,'[data-admin-savebar]');
  let policyBase:UpdatePolicy|null = null, latestPolicy:UpdatePolicy|null = null, policyEpoch = 0, policySaving = false, policyConflict = false;
  let policyMessage = '', policyError = false;
  function policyDraft():PolicyDraft {
    return { auto_check: policyInputs.auto_check.checked,
      check_interval_minutes: policyInputs.check_interval_minutes.valueAsNumber,
      observation_seconds: policyInputs.observation_seconds.valueAsNumber };
  }
  function policyDirty() {
    const base=policyBase,draft=policyDraft();return Boolean(base && (Object.keys(draft) as (keyof PolicyDraft)[]).some(key=>draft[key]!==base[key]));
  }
  function loadPolicy(value:UpdatePolicy) {
    policyBase = latestPolicy = value;
    policyInputs.auto_check.checked = value.auto_check;
    policyInputs.check_interval_minutes.value = String(value.check_interval_minutes);
    policyInputs.observation_seconds.value = String(value.observation_seconds);
    policyConflict = policyError = false;
    policyMessage = '';
  }
  function syncPolicy(value:UpdatePolicy) {
    if (!value?.revision || policySaving) return;
    latestPolicy = value;
    if (value.revision === policyBase?.revision) return;
    if (policyDirty()) policyConflict = true;
    else loadPolicy(value);
  }
  function policyControls(busy:boolean) {
    const disabled = busy || readOnly || !policyBase;
    $<HTMLFieldSetElement>('policy-fields').disabled = disabled;
    if (!policyBase || !latestPolicy) return;
    policyInputs.check_interval_minutes.disabled = !policyInputs.auto_check.checked;
    $<HTMLButtonElement>('policy-defaults').hidden = !policyBase.customized;
    $<HTMLButtonElement>('policy-defaults').disabled = policyConflict;
    $('policy-conflict').hidden = !policyConflict;
    $('policy-origin').textContent = latestPolicy.customized ? '站点自定义' : '部署默认';
    $('policy-launcher').hidden = latestPolicy.launcher_applies_window;
    window.ChunnelAdminUI?.paintSavebar(policyBar, { dirty: policyDirty(), busy: policySaving,
      readOnly, disabled: disabled || policyConflict, message: policyMessage, error: policyError });
    policyForm.setAttribute('aria-busy', String(policySaving));
  }
  try { ticket = sessionStorage.getItem(ticketKey) || ''; } catch (_) { /* Private mode may block storage. */ }
  function storeTicket(value:string) { ticket = value; try { if (value) sessionStorage.setItem(ticketKey, value); else sessionStorage.removeItem(ticketKey); } catch (_) {} }
  function feedback(message:string, error = false) { $('feedback').textContent = message; $('feedback').hidden = !message; $('feedback').className = `np-update-feedback ${error ? 'is-error' : 'is-success'}`; }
  function requireLogin() {
    loginRequired = true;
    $('login').hidden = false;
    $('connection').textContent = '需要重新登录';
    feedback('登录会话已结束，请重新登录查看更新结果。', true);
    controls();
  }
  function bytes(n:number) { if (!n) return '0 B'; const units = ['B', 'KiB', 'MiB', 'GiB']; const k = Math.min(3, Math.floor(Math.log(n) / Math.log(1024))); return `${(n / 1024 ** k).toFixed(k ? 1 : 0)} ${units[k]}`; }
  async function api(path = '', method = 'GET', body?:Record<string,unknown>, progress = false):Promise<unknown> {
    if(!progress&&loginRequired)throw new Error('登录会话已结束，请重新登录。');
    const headers:Record<string,string> = {Accept:'application/json'};
    if(progress)headers['X-Update-Ticket']=ticket;
    if(method!=='GET')headers['X-CSRF-Token']=root.dataset.csrf||'';
    if(body!==undefined)headers['Content-Type']='application/json';
    const result=await requests.json('/api/v1/admin/updates'+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
    if(!result.ok){const detail=apiError(result.data),error=new PluginRequestError(detail.message);error.status=result.status;error.code=detail.code;error.confirmedRejection=[400,401,403,409,422].includes(result.status);if(!progress&&(result.status===401||result.status===403))requireLogin();throw error;}
    if(!progress&&loginRequired)throw new Error('登录状态已变化，请重新打开后核对。');
    return result.data;
  }
  function protectedRelease() { return state.phase === 'completed' && Date.parse(state.protected_until || '') > Date.now(); }
  function controls() {
    const busy = busyPhases.has(state.phase) || protectedRelease() || sending || loginRequired || !!unconfirmedStart || disposed || !requests.active;
    $<HTMLFieldSetElement>('fields').disabled = readOnly || busy;
    settingsController?.refresh(true);
    policyControls(busy);
    const save = $<HTMLFormElement>('source').querySelector<HTMLButtonElement>('[data-admin-save]');
    if (save && busy) save.disabled = true;
    $<HTMLButtonElement>('check').disabled = readOnly || busy || !snapshot?.configured || !snapshot?.trusted_publisher;
    $('install').hidden = !snapshot?.available || terminal.has(state.phase) && ticket !== '';
    $<HTMLButtonElement>('start').disabled = readOnly || busy || !snapshot?.managed || !snapshot?.revision || !$<HTMLInputElement>('backup').checked;
    $<HTMLButtonElement>('cancel').hidden = !['checking', 'downloading', 'verifying', 'staging'].includes(state.phase) || readOnly;
    $<HTMLButtonElement>('cancel').disabled = sending || loginRequired;
  }
  function renderState(next:UpdateState) {
    if(next.id && next.id===state.id && Date.parse(next.updated_at)<Date.parse(state.updated_at))return;
    if(unconfirmedStart && next.id && next.id!==unconfirmedStart.id && next.target===unconfirmedStart.target)unconfirmedStart=null;
    const changed = state.phase !== next.phase;
    state = next; root.dataset.phase = state.phase;
    $('phase').textContent = protectedRelease() ? '更新完成，重启保护中' : phases[state.phase] || '等待任务';
    $('badge').textContent = phases[state.phase] || '等待检查';
    $('percent').textContent = String(state.percent);
    $<HTMLProgressElement>('meter').value = state.percent || 0;
    const logs = state.logs || [];
    $('detail').textContent = logs.at(-1)?.message || '配置更新源，然后检查可用版本。';
    if (changed) $('announcement').textContent = phases[state.phase] || '';
    $('bytes').textContent = state.total ? `${bytes(state.downloaded)} / ${bytes(state.total)}` : '等待下载';
    const now = Date.now();
    if (state.phase === 'downloading' && previousTime && now>previousTime && state.downloaded >= previousBytes) $('speed').textContent = `${bytes(Math.round((state.downloaded - previousBytes) * 1000 / (now - previousTime)))}/s`;
    else $('speed').textContent = '';
    previousTime = now; previousBytes = state.downloaded || 0;
    const end = busyPhases.has(state.phase) ? now : Date.parse(state.updated_at);
    const seconds = Math.max(0, Math.floor((end - Date.parse(state.started_at)) / 1000));
    $('elapsed').textContent = state.id && Number.isFinite(seconds) ? `用时 ${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}` : '尚未开始';
    const steps:Partial<Record<UpdateState['phase'],number>> = { downloading: 0, verifying: 1, staging: 2, queued: 3, stopping: 3, backup: 3, starting: 4, observing: 4, completed: 5 };const step = steps[state.phase] ?? -1;
    root.querySelectorAll<HTMLElement>('[data-step]').forEach(el => { const n = Number(el.dataset.step); el.classList.toggle('is-done', n < step); el.classList.toggle('is-current', n === step); });
    const signature = JSON.stringify(logs);
    if (signature !== logSignature) {
      logSignature = signature; $('logs').replaceChildren();
      for (const log of logs) {
        const row = document.createElement('div'); row.className = `np-update-log is-${['error', 'warning'].includes(log.level) ? log.level : 'info'}`;
        const time = document.createElement('time'); time.textContent = window.ChunnelTime?.formatClock(log.time) || ''; time.dateTime = log.time;
        const level = document.createElement('span'); level.textContent = (log.level || 'info').toUpperCase();
        const message = document.createElement('p'); message.textContent = log.message;
        row.append(time, level, message); $('logs').append(row);
      }
      if (!logs.length) { const empty = document.createElement('p'); empty.className = 'np-update-log-empty'; empty.textContent = '暂无更新任务。每一个步骤，都会记录在这里。'; $('logs').append(empty); }
      $('log-count').textContent = String(logs.length);
      if ($<HTMLInputElement>('follow').checked) $('logs').scrollTop = $('logs').scrollHeight;
    }
    if (state.phase === 'completed') $('current').textContent = state.target;
    if (state.phase === 'rolled_back' && state.previous) $('current').textContent = state.previous;
    if ((ticket || busyPhases.has(state.phase)) && state.target) $('target').textContent = state.target;
    $('login').hidden = !(loginRequired || ticket && ['completed', 'rolled_back'].includes(state.phase));
    controls();
  }
  function sourceControls() {
    const form = $<HTMLFormElement>('source'), official = Boolean(input(form,'official').checked);
    for (const key of ['owner', 'repo', 'ref', 'manifest']) {
      if (!input(form,key)) continue;
      input(form,key).disabled = official || readOnly;
      input(form,key).required = !official;
    }
    input(form,'token').disabled = official || Boolean(snapshot?.environment_token) || readOnly;
    input(form,'clear_token').disabled = official || Boolean(snapshot?.environment_token) || readOnly;
  }
  function renderSnapshot(value:UpdateSnapshot) {
    if (ticket && terminal.has(value.state.phase) && !(value.state.phase === 'completed' && Date.parse(value.state.protected_until || '') > Date.now())) storeTicket('');
    if (snapshot?.revision !== value.revision) $<HTMLInputElement>('backup').checked = false;
    const discovery = value.discovery;
    $('discovery').textContent = [value.trusted_publisher ? '' : '请由运维配置核心发布公钥', discovery?.interval && discovery.interval !== '0s' ? `自动检查间隔 ${discovery.interval}` : '定时检查已关闭', discovery?.message].filter(Boolean).join(' · ');
    snapshot = value; $('current').textContent = value.current;
    $('target').textContent = value.available?.version || '—';
    $('notes-wrap').hidden = !value.available?.notes; $('notes').textContent = value.available?.notes || '';
    $('token-status').textContent = value.environment_token ? '由环境配置' : value.token_set ? '已保存' : '未配置';
    sourceControls();
    $('source-label').textContent = value.source?.official ? 'GitHub / Gitee 公开双源' : 'Gitee 私有发布源';
    renderState(value.state);
  }
  async function loadSnapshot() {
    const at=policyEpoch,read=readEpoch;
    const value=updateSnapshot(await api());
    if(at!==policyEpoch||read!==readEpoch||loginRequired)return;
    syncPolicy(value.policy);
    renderSnapshot(value);
  }
  async function refresh() {
    const at=lifecycle,read=readEpoch;
    try {
      if (ticket) {const read=readEpoch,value=updateState(await api('/progress','GET',undefined,true));if(read!==readEpoch)return;renderState(value);}
      else await loadSnapshot();
      reconnects = 0;
      if (!loginRequired) $('connection').textContent = '● 实时同步';
    } catch (err) {
      if(disposed||!requests.active||at!==lifecycle||read!==readEpoch)return;
      if (pluginError(err).status === 401 && ticket) { storeTicket(''); if(loginRequired)return;return refresh(); }
      if (loginRequired || pluginError(err).status === 401 || pluginError(err).status === 403) { requireLogin(); return; }
      reconnects++; $('connection').textContent = '重新连接中…';
      if (reconnects > 20) feedback('连接仍未恢复。更新任务在服务端继续执行，请检查服务状态；不要重复提交安装。', true);
    }
  }
  async function action(fn:()=>Promise<void>) {
    if(sending||readOnly||loginRequired||disposed||!requests.active)return;
    const at=lifecycle;readEpoch++;clearTimeout(pollTimer);sending=true;controls();
    try{feedback('');await fn();if(at===lifecycle&&!disposed&&requests.active&&(!loginRequired||ticket))await refresh();}
    catch(err){if(at===lifecycle&&!disposed&&requests.active)feedback(pluginError(err).message,true);}
    finally{if(at===lifecycle){sending=false;controls();schedule();}}
  }
  const policyBusy = () => readOnly || sending || loginRequired || disposed || !requests.active || !!unconfirmedStart || busyPhases.has(state.phase) || protectedRelease() || !policyBase;
  function savePolicy(useDefaults:boolean) {
    if (policyBusy() || !policyBase || policyConflict || (!useDefaults && (!policyDirty() || !policyForm.checkValidity()))) return;
    const submitted=policyDraft(),body = { revision: policyBase.revision, ...(useDefaults ? { use_defaults: true } : submitted) };
    action(async () => {
      const at=lifecycle;
      policySaving = true;
      policyEpoch++;
      controls();
      try {
        const saved=updatePolicy(await api('/policy','PUT',body));
        const unchanged=JSON.stringify(policyDraft())===JSON.stringify(submitted);
        if(unchanged)loadPolicy(saved);else{policyBase=latestPolicy=saved;policyConflict=policyError=false;}
        policyMessage = useDefaults ? '已恢复部署默认值' : '策略已保存';
      } catch (err) {
        if(disposed||at!==lifecycle||!requests.active)return;
        policyError = true;
        policyMessage = pluginError(err).message;
        if (pluginError(err).code === 'updates.policy_changed') policyConflict = true;
      } finally {
        if(at===lifecycle){policyEpoch++;policySaving=false;}
      }
    });
  }
  policyForm.addEventListener('input', event => {
    const interval = policyInputs.check_interval_minutes;
    if (event.target === policyInputs.auto_check && !policyInputs.auto_check.checked && !interval.validity.valid && policyBase) {
      interval.value = String(policyBase.check_interval_minutes);
    }
    policyMessage = ''; policyError = false; controls();
  });
  policyForm.addEventListener('submit', event => { event.preventDefault(); savePolicy(false); });
  window.addEventListener('beforeunload', event => {
    if (!readOnly && (policyDirty() || policySaving)) { event.preventDefault(); event.returnValue = ''; }
  },{signal:eventScope.signal});
  required(policyBar,'[data-admin-discard]').addEventListener('click', () => {
    if (!policyBusy() && latestPolicy) { loadPolicy(latestPolicy); controls(); }
  });
  $<HTMLButtonElement>('policy-defaults').addEventListener('click', () => savePolicy(true));
  $('policy-reload').addEventListener('click', () => {
    if (!policyBusy()) action(async () => { const draft=JSON.stringify(policyDraft()),value = updateSnapshot(await api()); policyEpoch++; if(JSON.stringify(policyDraft())===draft)loadPolicy(value.policy);else{latestPolicy=value.policy;policyConflict=true;} });
  });
  input($<HTMLFormElement>('source'),'official').addEventListener('change', sourceControls);
  $<HTMLFormElement>('source').addEventListener('submit', event => {
    event.preventDefault(); if (policyBusy()) return;
    if (settingsController && !settingsController.start()) return;
    action(async () => {
      const at=lifecycle;
      const form = $<HTMLFormElement>('source'),submitted=settingsController?.snapshot(),token=input(form,'token'),clear=input(form,'clear_token'),tokenValue=token.value,clearValue=clear.checked;
      const body:Record<string,unknown> = input(form,'official').checked ? { official: true } :
        Object.fromEntries(['owner', 'repo', 'ref', 'manifest', 'token'].map(key => [key, input(form,key).value.trim()]));
      if (!body.official) body.clear_token = input(form,'clear_token').checked;
      try {
        updateAcknowledgment(await api('/source','PUT',body),'saved');
        if(token.value===tokenValue)token.value='';if(clear.checked===clearValue)clear.checked=false;storeTicket('');
        submitted?.forEach(field=>{if(field.input===token)field.value='';if(field.input===clear)field.checked=false;});
        settingsController?.success(submitted);
        if (!settingsController) feedback('已保存');
      } catch (error) { if(disposed||at!==lifecycle||!requests.active)return;if (settingsController) settingsController.fail(pluginError(error).message); else throw error; }
    });
  });
  $<HTMLButtonElement>('check').addEventListener('click', () => {if(!$<HTMLButtonElement>('check').disabled)void action(async () => {updateAcknowledgment(await api('/check','POST'),'accepted');storeTicket('');});});
  $<HTMLInputElement>('backup').addEventListener('change', controls);
  $<HTMLButtonElement>('start').addEventListener('click', () => {
    if ($<HTMLButtonElement>('start').disabled || !snapshot?.available) return;
    const selected={version:snapshot.available.version,revision:snapshot.revision,backup_confirmed:$<HTMLInputElement>('backup').checked};
    void action(async()=>{
      const at=lifecycle;unconfirmedStart={id:state.id,target:selected.version};$<HTMLInputElement>('backup').checked=false;
      try{storeTicket(updateTicket(await api('/start','POST',selected)));unconfirmedStart=null;}
      catch(error){if(at===lifecycle&&pluginError(error).confirmedRejection)unconfirmedStart=null;throw error;}
    });
  });
  $<HTMLButtonElement>('cancel').addEventListener('click', () => {if(!$<HTMLButtonElement>('cancel').disabled&&!$<HTMLButtonElement>('cancel').hidden)void action(async()=>updateAcknowledgment(await api('/cancel','POST'),'accepted'));});
  $('export').addEventListener('click', () => {
    const text = (state.logs || []).map(log => `${log.time} [${log.level.toUpperCase()}] ${log.message}`).join('\n');
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }));
    const link = document.createElement('a'); link.href = url; link.download = `newpress-update-${state.id || 'logs'}.txt`; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  });
  function schedule(){clearTimeout(pollTimer);if(!disposed&&requests.active&&!sending&&(!loginRequired||ticket))pollTimer=setTimeout(poll,busyPhases.has(state.phase)||reconnects||unconfirmedStart?1000:5000);}
  async function poll(){if(disposed||!requests.active||sending||polling||loginRequired&&!ticket)return;polling=true;const at=lifecycle;try{await refresh();}finally{if(at===lifecycle){polling=false;schedule();}}}
  const retire=()=>{lifecycle++;readEpoch++;clearTimeout(pollTimer);requests.suspend();polling=false;sending=false;policySaving=false;};
  window.addEventListener('pagehide',retire,{signal:eventScope.signal});
  window.addEventListener('pageshow',event=>{if(event.persisted&&!disposed){requests.resume();settingsController?.fail('页面已恢复，请核对上次操作。');controls();void poll();}},{signal:eventScope.signal});
  const session=()=>{retire();requests.resume();input($<HTMLFormElement>('source'),'token').value='';requireLogin();void poll();};
  document.addEventListener('chunnel:session',session,{signal:eventScope.signal});
  let channel:BroadcastChannel|undefined;try{channel=new BroadcastChannel('chunnel.social');channel.addEventListener('message',event=>{if(event.data?.type==='session')session();});}catch{}
  document.addEventListener('chunnel:admin-before-replace',event=>{if(event.detail?.regions.some(region=>region===root||region.contains(root))){disposed=true;retire();settingsController?.dispose();eventScope.abort();channel?.close();}},{signal:eventScope.signal});
  // Get source/version metadata once even when resuming a saved progress receipt.
  controls();
  loadSnapshot().catch(() => {}).finally(()=>{if(!disposed&&requests.active)void poll();});
})();
