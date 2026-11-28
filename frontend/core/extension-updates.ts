import {required} from './ledger-admin-dom';
import {PluginRequests} from '../shared/plugin-requests';
import {PluginRequestError,pluginError} from '../shared/plugin-data-common';
import {apiError} from '../shared/plugin-runtime-data';
import {extensionActive,extensionJob} from '../shared/update-data';
import type {ExtensionJob} from '../shared/update-data';
interface Target {extensionAction:string;slug:string;name:string;current:string;revision:string;version:string;digest:string}
(() => {
  'use strict';
  const found = document.querySelector<HTMLElement>('[data-extension-workspace]');
  if (!found) return;
  const root=found,eventScope=new AbortController(),requests=new PluginRequests(),reads=new PluginRequests();let disposed=false,readEpoch=0,lifecycle=0,polling=false;
  const $ = <T extends HTMLElement=HTMLButtonElement>(selector:string, scope:ParentNode = root):T => required<T>(scope,selector);
  const $$ = <T extends HTMLElement=HTMLButtonElement>(selector:string, scope:ParentNode = root):T[] => [...scope.querySelectorAll<T>(selector)];
  const kind = root.dataset.kind || '';
  if(kind!=='plugin'&&kind!=='theme')return;
  const base = '/api/v1/admin/extensions/' + encodeURIComponent(kind);
  const identity = Number(root.dataset.userId);
  const readOnly = root.dataset.readonly === 'true';
  const dialog = $<HTMLDialogElement>('[data-extension-dialog]');
  let controls = $$('[data-extension-check], [data-extension-action]');
  let disabled = new Map(controls.map(control => [control, control.disabled]));
  const active = extensionActive;
  let current:ExtensionJob|undefined, selected:Target|undefined, opener:HTMLButtonElement|undefined, timer=0, unresolvedStart:{id:string;action:string;slug:string;target:string}|undefined;
  let pending = false, uncertain = false, accessLost = false, watching = root.dataset.busy === 'true', reloading = false;
  let autoCheck = root.dataset.autoCheck === 'true' && !readOnly && !watching;
  const bytes = (value:number) => {
    if (!Number.isFinite(value) || value < 0) return '';
    if (value >= 1048576) return (value / 1048576).toFixed(1) + ' MiB';
    if (value >= 1024) return (value / 1024).toFixed(1) + ' KiB';
    return value + ' B';
  };
  $$('[data-package-bytes]').forEach(element => { element.textContent = bytes(Number(element.dataset.packageBytes)); });
  const showError = (message:string, inDialog = false) => {
    const target = inDialog && dialog.open ? $('[data-confirm-error]') : $('[data-extension-error]');
    target.hidden = false;
    target.textContent = message;
  };
  const busy = (value:boolean) => {
    controls.forEach(control => { control.disabled = Boolean(value || uncertain || accessLost || reloading || disabled.get(control) || readOnly); });
    root.setAttribute('aria-busy', String(value || uncertain));
  };
  const loseAccess = () => {
    accessLost = true;requests.suspend();reads.suspend();readEpoch++;
    clearTimeout(timer);
    busy(true);
    $('[data-extension-cancel]').hidden = true;
    $('[data-extension-reconnect]').hidden = true;
    $('[data-extension-confirm]').disabled = true;
    $('[data-job-message]').textContent='';$('[data-job-detail]').textContent='';
  };
  const request = async (path:string,body?:Record<string,unknown>):Promise<ExtensionJob> => {
    const response=await (body?requests:reads).json(base+path,{method:body?'POST':'GET',headers:{Accept:'application/json',...(body?{'Content-Type':'application/json','X-CSRF-Token':root.dataset.csrf||''}:{})},body:body?JSON.stringify({...body,expected_user_id:identity}):undefined});
    if(!response.ok){const error=new PluginRequestError(apiError(response.data).message);error.status=response.status;error.confirmedRejection=[400,401,403,409,422].includes(response.status);throw error;}
    return extensionJob(response.data,kind);
  };
  const render = (job:ExtensionJob) => {
    if (current?.id && job.id === current.id && Date.parse(job.updated_at) < Date.parse(current.updated_at)) return;
    current = job;
    const running = active.has(job.phase);
    const box = $('[data-extension-job]');
    box.hidden = job.phase === 'idle';
    box.dataset.phase = job.phase;
    $('[data-job-message]').textContent = job.message;
    const progress = $<HTMLProgressElement>('[data-job-progress]');
    progress.hidden = !running || job.phase === 'other';
    if (job.phase === 'downloading' && job.total > 0) {
      progress.max = job.total;
      progress.value = job.downloaded;
    } else {
      progress.removeAttribute('value');
    }
    const detail = [job.slug && (job.slug + (job.target ? ' · ' + job.target : '')), job.total > 0 && (bytes(job.downloaded) + ' / ' + bytes(job.total))].filter(Boolean);
    $('[data-job-detail]').textContent = detail.join('　');
    const cancel = $('[data-extension-cancel]');
    cancel.hidden = readOnly || accessLost || !running || !job.id || job.phase === 'switching' || job.phase === 'cancelling' || job.phase === 'other';
    cancel.disabled = pending;
    $('[data-extension-reconnect]').hidden = true;
    busy(running || pending);
    if (running) watching = true;
    else if (watching && !pending && !reloading) {
      // Reload once to use the new template/assets and server-authoritative
      // installation state. The persisted result remains visible after reload.
      reloading = true;
      location.reload();
    }
  };
  const schedule = () => {
    clearTimeout(timer);
    if (current && active.has(current.phase) && !reloading && !accessLost && !disposed && requests.active && !pending) timer = setTimeout(poll, 1000);
  };
  const reconcile = (job:ExtensionJob) => {
    uncertain = false;
    if (unresolvedStart) {
      if(job.id && job.id!==unresolvedStart.id && job.action===unresolvedStart.action && job.slug===unresolvedStart.slug && job.target===unresolvedStart.target){watching=true;unresolvedStart=undefined;}
      else uncertain=true;
    }
    render(job);
    schedule();
    $('[data-extension-reconnect]').hidden=!uncertain;
    $('[data-extension-confirm]').disabled = readOnly || uncertain || accessLost || reloading || pending || active.has(job.phase);
  };
  async function poll() {
    if (accessLost || reloading || disposed || !requests.active || pending || polling) return;
    polling=true;const epoch=readEpoch;
    try {
      const job = await request('/job');
      if(epoch!==readEpoch)return;
      $('[data-extension-error]').hidden = true;
      reconcile(job);
      if (autoCheck) {
        autoCheck = false;
        if (!active.has(job.phase) && !dialog.open && !reloading) await start('check');
      }
    } catch (error) {
      if(disposed||!requests.active||epoch!==readEpoch)return;
      // A transport failure is not a failed update. Do not repeat POST or
      // silently unlock actions while the job outcome remains unknown.
      showError(pluginError(error).message || '连接中断，服务器任务可能仍在继续，请重新连接查看。');
      $('[data-extension-reconnect]').hidden = false;
      $('[data-extension-job]').hidden = false;
      uncertain = true;
      busy(true);
      $('[data-extension-confirm]').disabled = true;
      if (pluginError(error).status === 401 || pluginError(error).status === 403) {
        loseAccess();
      }
    } finally {
      if(epoch===readEpoch)polling=false;
    }
  }
  const dialogBusy = (value:boolean) => {
    $$('button', dialog).forEach(button => { button.disabled = value; });
    $('[data-extension-confirm]').setAttribute('aria-busy', String(value));
  };
  async function start(action:string, target?:Target) {
    if (disposed || !requests.active || pending || uncertain || accessLost || reloading || current && active.has(current.phase) || readOnly) return;
    const at=lifecycle;
    pending = true;
    unresolvedStart={id:current?.id||'',action,slug:target?.slug||'',target:target?.version||''};
    clearTimeout(timer);reads.suspend();reads.resume();readEpoch++;polling=false;
    autoCheck = false;
    busy(true);
    dialogBusy(true);
    $('[data-extension-error]').hidden = true;
    $('[data-confirm-error]').hidden = true;
    const body:Record<string,unknown> = { channel: root.dataset.channel };
    let path = '/check';
    if (target) {
      path = '/' + encodeURIComponent(target.slug) + '/' + action;
      Object.assign(body, {
        version: target.version, digest: target.digest || '',
        expected_version: action === 'install' ? '' : target.current,
        revision: action === 'install' ? '' : target.revision
      });
      if (action === 'activate' || action === 'uninstall') body.expected_active = root.dataset.activeTheme;
    }
    try {
      const job = await request(path, body);
      if(job.action!==action || target && (job.slug!==target.slug || !['activate','uninstall'].includes(action) && job.target!==target.version))throw new Error('任务回执与本次操作不一致，请重新连接核对。');
      unresolvedStart=undefined;pending = false;
      watching = true;
      if (dialog.open) dialog.close();
      render(job);
      schedule();
    } catch (error) {
      if(disposed||!requests.active||at!==lifecycle)return;
      showError(pluginError(error).message || '未收到任务确认，请重新连接核对状态后再操作。', true);
      pending = false;
      if (pluginError(error).status === 401 || pluginError(error).status === 403) {
        loseAccess();
      } else {
        uncertain = true;
        if(pluginError(error).confirmedRejection)unresolvedStart=undefined;
        // Reconcile a possibly accepted request without submitting it again.
        try {
          const job = await request('/job');
          if (active.has(job.phase) && dialog.open) dialog.close();
          reconcile(job);
        } catch (readError) {
          if(disposed||!requests.active||at!==lifecycle)return;
          busy(true);
          $('[data-extension-job]').hidden = false;
          $('[data-extension-reconnect]').hidden = false;
          if (pluginError(readError).status === 401 || pluginError(readError).status === 403) {
            showError(pluginError(readError).message, true);
            loseAccess();
          }
        }
      }
    } finally {
      if(disposed||!requests.active||at!==lifecycle)return;
      pending = false;
      dialogBusy(false);
      $('[data-extension-confirm]').disabled = Boolean(readOnly || uncertain || accessLost || reloading || current && active.has(current.phase));
    }
  }
  $('[data-extension-check]').addEventListener('click', () => start('check'));
  const boundActions = new WeakSet();
  const bindActions = () => $$('[data-extension-action]').forEach(button => {
    if (boundActions.has(button)) return;
    boundActions.add(button);
    button.addEventListener('click', () => {
    if (disposed || !requests.active || pending || uncertain || accessLost || reloading || current && active.has(current.phase) || readOnly) return;
    selected = {extensionAction:button.dataset.extensionAction||'',slug:button.dataset.slug||'',name:button.dataset.name||'',current:button.dataset.current||'',revision:button.dataset.revision||'',version:button.dataset.version||'',digest:button.dataset.digest||''};
    opener = button;
    const action = selected.extensionAction;
    const labels:Record<string,string> = { install: '安装', update: '更新', rollback: '回退', activate: '使用', uninstall: '卸载' };const label=labels[action];if(!label)return;
    $('[data-confirm-title]').textContent = label + ' ' + selected.name;
    $('[data-confirm-versions]').textContent = action === 'activate' || action === 'uninstall' ? selected.name + ' · v' + selected.current : (selected.current || '未安装') + ' → ' + selected.version;
    $('[data-extension-confirm]').textContent = '确认' + label;
    $('[data-confirm-detail]').textContent = action === 'activate'
      ? '验证通过后，站点前台立即使用此主题。文章、导航与各主题设置保留，重启后继续使用。'
      : action === 'uninstall'
      ? '将删除此主题的安装文件与版本回退记录。文章、媒体和个性化设置保留；重新安装后可以继续使用原设置。'
      : action === 'rollback'
      ? '将切换到保留的旧代码。当前业务数据会保留；配置与权限如有变化，需重新确认。'
      : action === 'install'
        ? (kind === 'plugin' ? '安装后默认停用。请先设置配置与权限，再启用插件。' : '安装后保留为可用主题，不改变当前使用的主题。')
        : (kind === 'plugin' ? '插件数据与已有配置会保留。若权限或配置项有变化，更新后会停用并提示重新确认。' : '主题设置会保留，验证通过后立即生效。本地代码改动会保存在旧版本快照中。');
    if (kind === 'theme' && (action === 'update' || action === 'rollback')) {
      const impact = button.closest('[data-extension-card]')?.querySelector('[data-dependency-impact]');
      if (impact) $('[data-confirm-detail]').textContent += ' ' + (impact.textContent || '').trim();
    }
    $('[data-confirm-error]').hidden = true;
    dialog.showModal();
    $('button[value="cancel"]', dialog).focus();
    });
  });
  bindActions();
  document.addEventListener('chunnel:admin-before-update', event => {
    if (pending || uncertain || accessLost || reloading || dialog.open || current && active.has(current.phase)) event.preventDefault();
  },{signal:eventScope.signal});
  document.addEventListener('chunnel:admin-updated', event => {
    if (disposed || !event.detail?.regions.some(region => region.matches('.extension-main'))) return;
    root.dataset.channel = new URL(event.detail.url || location.href).searchParams.get('channel') === 'preview' ? 'preview' : 'stable';
    const checkDisabled = disabled.get($('[data-extension-check]'));
    controls = $$('[data-extension-check], [data-extension-action]');
    disabled = new Map(controls.map(control => [control, control === $('[data-extension-check]') ? Boolean(checkDisabled) : control.disabled]));
    bindActions();
    $$('[data-package-bytes]').forEach(element => { element.textContent = bytes(Number(element.dataset.packageBytes)); });
    busy(Boolean(pending || current && active.has(current.phase)));
  },{signal:eventScope.signal});
  $('[data-extension-confirm]').addEventListener('click', () => { if (selected) start(selected.extensionAction, selected); });
  dialog.addEventListener('cancel', event => { if (pending) event.preventDefault(); });
  dialog.addEventListener('close', () => { if (opener && !opener.disabled) opener.focus(); });
  $('[data-extension-cancel]').addEventListener('click', async () => {
    if (disposed || !requests.active || pending || uncertain || accessLost || reloading || !current?.id || readOnly) return;
    const at=lifecycle;
    clearTimeout(timer);reads.suspend();reads.resume();readEpoch++;polling=false;
    pending = true;
    $('[data-extension-cancel]').disabled = true;
    try { const id=current.id,job=await request('/cancel', { id });if(job.id!==id)throw new Error('取消回执与任务不一致，请重新核对。');render(job); }
    catch (error) {
      if(disposed||!requests.active||at!==lifecycle)return;
      showError(pluginError(error).message || '未收到取消确认，请核对任务结果。');
      uncertain = true;
      busy(true);
      if (pluginError(error).status === 401 || pluginError(error).status === 403) loseAccess();
    }
    finally {if(at===lifecycle){pending=false;if(!accessLost&&!disposed&&requests.active)await poll();}}
  });
  $('[data-extension-reconnect]').addEventListener('click', poll);
  const retire=()=>{clearTimeout(timer);requests.suspend();reads.suspend();lifecycle++;readEpoch++;polling=false;if(pending)uncertain=true;pending=false;};
  window.addEventListener('pagehide',retire,{signal:eventScope.signal});
  window.addEventListener('pageshow',event=>{if(event.persisted&&!accessLost&&!disposed){requests.resume();reads.resume();void poll();}},{signal:eventScope.signal});
  const session=()=>{loseAccess();showError('登录状态已变化，请重新打开扩展页面核对。');};
  document.addEventListener('chunnel:session',session,{signal:eventScope.signal});
  let channel:BroadcastChannel|undefined;try{channel=new BroadcastChannel('chunnel.social');channel.addEventListener('message',event=>{if(event.data?.type==='session')session();});}catch{}
  document.addEventListener('chunnel:admin-before-replace',event=>{if(event.detail?.regions.some(region=>region===root||region.contains(root))){disposed=true;retire();eventScope.abort();channel?.close();}},{signal:eventScope.signal});
  busy(watching);
  poll();
})();
