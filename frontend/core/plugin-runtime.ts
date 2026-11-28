import {PluginRequests} from '../shared/plugin-requests';
import {required} from './ledger-admin-dom';
import {integer,pluginError,PluginRequestError} from '../shared/plugin-data-common';
import {responseRecord,apiError,taskLabels,validReissue,validTask as validateTask,validTaskPage,validTaskReview,runtimeResult} from '../shared/plugin-runtime-data';
import type {PluginTask,PendingReissue} from '../shared/plugin-runtime-data';
(() => {
  'use strict';
  const found = document.querySelector<HTMLElement>('[data-runtime-admin]');
  if (!found) return;
  const root=found, identity=Number(root.dataset.userId), slug=root.dataset.slug || ''; 
  const form = required<HTMLFormElement>(root,'[data-runtime-form]');
  const status = required(root,'[data-runtime-status]');
  const savebar = window.ChunnelAdminUI?.savebar(form);
  if (!savebar) return;
  const controls=savebar;
  const requests=new PluginRequests();
  let busy = false, accessLost=false, disposed=false;
  const operationMessage = (error:unknown) => pluginError(error).name === 'TypeError' && /fetch|network|load failed/i.test(pluginError(error).message || '') ? '网络连接中断，操作结果尚未确认；输入已保留，请恢复连接后重试或刷新核对。' : pluginError(error).message || '操作尚未完成，输入已保留。';
  const show = (message:string) => { status.hidden = false; status.textContent = message; };
  const sendTo = async (url:string, body:Record<string,unknown>):Promise<Record<string,unknown>> => {
    const response = await requests.json(url, {
      method: 'POST', credentials: 'same-origin', cache: 'no-store',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': root.dataset.csrf || '' },
      body: JSON.stringify({ expected_user_id: identity, ...body })
    });
    const data=response.data;
    if (!response.ok || !data) {
      const error = new PluginRequestError(apiError(data).message);
      if(response.status===401||response.status===403||apiError(data).code==='plugins.identity_changed')retire(true);
      error.confirmedRejection = [400, 401, 403, 409, 422, 429].includes(response.status) && !!apiError(data).code;
      throw error;
    }
    return responseRecord(data);
  };
  const send = (suffix:string, body:Record<string,unknown>) => sendTo('/api/v1/admin/plugins/' + encodeURIComponent(slug) + suffix, body);
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (!requests.active || busy || !form.reportValidity() || !controls.start()) return;
    busy = true;
    const version = Number(root.dataset.version), values:Record<string,string|boolean|number> = {};
    form.querySelectorAll<HTMLInputElement|HTMLTextAreaElement>('[data-setting-type]').forEach(field => {
      values[field.name] = field.dataset.settingType === 'boolean' ? ('checked' in field && field.checked) : field.dataset.settingType === 'number' ? Number(field.value) : field.value;
    });
    const grants = [...form.querySelectorAll<HTMLInputElement>('[data-runtime-grant]:checked')].map(field => field.dataset.runtimeGrant);
    const submitted=controls.snapshot();
    try {
      const data = await send('/configuration', { configuration: { version, values, grants }, subscribe_events: form.querySelector<HTMLInputElement>('[data-event-subscription]')?.checked === true });
      if (data.ok !== true || !Number.isSafeInteger(data.version) || data.version !== version + 1) throw new Error('未收到有效保存结果，请刷新核对；输入已保留。');
      root.dataset.version = String(data.version);
      controls.success(submitted);
    } catch (error) { if(requests.active)controls.fail(pluginError(error).message); }
    finally { busy = false; }
  });
  const run=root.querySelector<HTMLButtonElement>('[data-runtime-run]');
  run?.addEventListener('click', async () => {
    if (!requests.active || busy) return;
    if (controls.dirty()) { show('请先保存设置再试运行。'); return; }
    let payload;
    try { payload = JSON.parse(required<HTMLTextAreaElement>(root,'[data-runtime-payload]').value); }
    catch { show('操作参数不是有效 JSON。'); return; }
    busy = true; const button = run; button.disabled = true;
    try {
      const result = runtimeResult(await send('/execute', { payload }));
      const output = required(root,'[data-runtime-result]'); output.hidden = false; output.textContent = JSON.stringify(result, null, 2);
      const preview = required(root,'[data-runtime-preview]'); preview.hidden = !result.fragment;
      if (result.fragment) { required(preview,'strong').textContent = result.fragment.title; required(preview,'p').textContent = result.fragment.text; preview.dataset.tone = result.fragment.tone; }
      show('试运行已完成');
    } catch (error) { if(requests.active)show(pluginError(error).message); }
    finally { busy = false; button.disabled = accessLost || disposed; }
  });
  const tasks = root.querySelector<HTMLElement>('[data-plugin-tasks]');
  if (tasks) {
    const note=required(tasks,'[data-task-status]'), filter=required<HTMLSelectElement>(tasks,'[data-task-filter]');
    const scope=tasks.querySelector<HTMLSelectElement>('[data-task-scope]'), archiveAge=tasks.querySelector<HTMLSelectElement>('[data-task-archive-age]');
    const previous=required<HTMLButtonElement>(tasks,'[data-task-prev]'),next=required<HTMLButtonElement>(tasks,'[data-task-next]');
    const panel=tasks.querySelector<HTMLElement>('[data-task-review-panel]');
    const review=panel?{panel,fields:required(tasks,'[data-task-reissue-fields]'),payload:required<HTMLTextAreaElement>(tasks,'[data-task-reissue-payload]'),reason:required<HTMLTextAreaElement>(tasks,'[data-task-reissue-reason]'),confirmed:required<HTMLInputElement>(tasks,'[data-task-reissue-confirm]'),submit:required<HTMLButtonElement>(tasks,'[data-task-reissue-submit]'),close:required<HTMLButtonElement>(tasks,'[data-task-review-close]')}:null;
    let reviewedSource:PluginTask|null = null, pendingReissue:PendingReissue|null = null;
    let offset = 0;
    let pendingEnqueue:{body:{key:string;payload:unknown;expected_user_id:number};uncertain:boolean}|null=null;
    const labels=taskLabels, validTask=(value:unknown):value is PluginTask=>validateTask(value,slug);
    const updateReviewControls = () => {
      if (!review) return;
      review.close.disabled = Boolean(pendingReissue);
      review.submit.textContent = pendingReissue ? '重试原重新投递' : '核对后加入队列';
      review.submit.disabled = !requests.active || !reviewedSource || Boolean(review.fields.hidden);
    };
    const refresh = async () => {
      const archived = scope?.value === 'archived';
      const response = await requests.json('/api/v1/admin/plugins/' + encodeURIComponent(slug) + '/tasks?' + new URLSearchParams({ status: filter.value, offset: String(offset), limit: '20', archived: String(archived) }), { credentials: 'same-origin', cache: 'no-store' });
      const page=response.data;
      if (!response.ok || !validTaskPage(page,slug,filter.value,offset)) throw new Error(apiError(page).message || '任务读取失败');
      const list = required(tasks,'[data-task-list]');
      list.replaceChildren();
      for (const task of page.items) {
        const row = document.createElement('li');
        row.textContent = task.id + ' · ' + task.kind + ' · ' + (labels[task.status] || task.status) + ' · 尝试 ' + task.attempts + ' 次' + (archived ? ' · 已归档' : '');
        const action = archived || task.archived_at ? null : task.status === 'failed' ? 'retry' : ['pending', 'running', 'retry'].includes(task.status) ? 'cancel' : null;
        if (action) {
          const button = document.createElement('button');
          button.type = 'button'; button.className = 'admin-button admin-button-quiet';
          button.textContent = action === 'retry' ? '重试' : '取消';
          button.dataset.taskAction = action; button.dataset.taskId = task.id; button.dataset.taskVersion = String(task.version);
          row.append(button);
        }
        if (task.reissue) {
          const origin = document.createElement('p'); origin.className = 'plugin-task-origin';
          origin.textContent = '重新投递来源：' + task.reissue.source_id + ' · 原任务版本 ' + task.reissue.source_version;
          row.append(origin);
        }
        if (review && (['failed', 'cancelled'].includes(task.status) || task.reissue)) {
          const button = document.createElement('button'); button.type = 'button'; button.className = 'admin-button admin-button-quiet';
          button.textContent = ['failed', 'cancelled'].includes(task.status) ? '核对后重新投递' : '投递来源';
          button.dataset.taskReviewOpen = ''; button.dataset.taskId = task.id;
          row.append(button);
        }
        list.append(row);
      }
      if (!page.items.length) { const row = document.createElement('li'); row.textContent = '暂无任务记录'; list.append(row); }
      previous.disabled = offset === 0; next.disabled = offset + 20 >= page.total;
      note.textContent = (archived ? '归档历史' : '队列保留记录') + '共 ' + page.total + ' 条';
    };
    const act = async (operation:()=>Promise<void>) => {
      if (busy) return;
      busy = true;
      const disabled = [...tasks.querySelectorAll<HTMLInputElement|HTMLSelectElement|HTMLButtonElement>('[data-task-scope], [data-task-filter], [data-task-archive-age], [data-task-refresh], [data-task-create], [data-task-archive], [data-fanout-archive], [data-task-reissue-submit], [data-task-review-close]')].map(node => [node, node.disabled] as const);
      disabled.forEach(([node]) => { node.disabled = true; });
      tasks.setAttribute('aria-busy', 'true');
      try { await operation(); } catch (error) { if(requests.active)note.textContent = operationMessage(error); }
      finally { busy = false; disabled.forEach(([node, value]) => { node.disabled = value || accessLost || disposed; }); tasks.removeAttribute('aria-busy'); updateReviewControls(); }
    };
    required<HTMLButtonElement>(tasks,'[data-task-refresh]').addEventListener('click', () => act(refresh));
    filter.addEventListener('change', () => act(async () => { offset = 0; await refresh(); }));
    scope?.addEventListener('change', () => act(async () => { offset = 0; await refresh(); }));
    previous.addEventListener('click', () => act(async () => { offset = Math.max(0, offset - 20); await refresh(); }));
    next.addEventListener('click', () => act(async () => { offset += 20; await refresh(); }));
    const archive = async (fanouts:boolean) => {
      const days = Number(archiveAge?.value ?? 30);
      if (![0, 7, 30, 90].includes(days)) throw new Error('归档范围无效。');
      const body = { before: new Date(Date.now() - days * 86400000).toISOString(), limit: 100 };
      const result = await (fanouts ? sendTo('/api/v1/admin/plugins/fanouts/archive', body) : send('/tasks/archive', body));
      if (!integer(result.tasks,0,100) || !integer(result.fanouts,0,100)) throw new Error('归档结果不完整，请刷新核对。');
      const message = fanouts ? '已归档站点事件回执 ' + result.fanouts + ' 条。' : '已归档本插件终态任务 ' + result.tasks + ' 条。';
      offset = 0;
      try { await refresh(); } catch { throw new Error(message + '任务列表暂未刷新，请再次刷新核对。'); }
      note.textContent = message + note.textContent;
    };
    tasks.querySelector('[data-task-archive]')?.addEventListener('click', () => act(() => archive(false)));
    tasks.querySelector('[data-fanout-archive]')?.addEventListener('click', () => act(() => archive(true)));
    tasks.addEventListener('click', event => {
      const target=event.target; if(!target||!('closest' in target)||typeof target.closest!=='function')return;
      const reviewButton = target.closest('[data-task-review-open]') as HTMLButtonElement|null;
      if (reviewButton && review) {
        act(async () => {
          if (pendingReissue) throw new Error('此前重新投递的结果尚未确认，请先重试原重新投递；新输入会保留。');
          const response = await requests.json('/api/v1/admin/plugins/' + encodeURIComponent(slug) + '/tasks/' + encodeURIComponent(reviewButton.dataset.taskId || '') + '/review', { credentials: 'same-origin', cache: 'no-store' });
          const details=response.data;
          if (!response.ok || !details) throw new Error(apiError(details).message || '任务核对读取失败。');
          if(!validTaskReview(details,slug,reviewButton.dataset.taskId || '',identity))throw new Error('任务核对信息不完整或当前账户已变化，请刷新核对。');
          reviewedSource = details.task;
          required(tasks,'[data-task-review-source]').textContent = reviewedSource.id + ' · ' + reviewedSource.kind + ' · ' + labels[reviewedSource.status] + ' · 版本 ' + reviewedSource.version + (reviewedSource.archived_at ? ' · 已归档，原参数已移除' : '');
          const audit = required(tasks,'[data-task-review-audit]'); audit.hidden = !details.provenance;
          if (details.provenance) audit.textContent = '来源任务：' + details.provenance.source_id + ' · 原版本 ' + details.provenance.source_version + ' · 核对账户 #' + details.provenance.reviewer_id + ' · ' + details.provenance.reason;
          review.panel.hidden = false; review.fields.hidden = !details.reissuable;
          // New parameters remain a local draft when the source is refreshed
          // or changed. Every fresh source read requires deliberate confirmation.
          review.confirmed.checked = false;
          note.textContent = details.reissuable ? '已读取当前任务记录，请填写并核对新参数后重新投递。' : '当前任务可核对来源，暂不能重新投递。';
          review.panel.scrollIntoView?.({ block: 'nearest' });
        });
        return;
      }
      const button = target.closest('[data-task-action]') as HTMLButtonElement|null;
      if (!button) return;
      act(async () => {
        const receipt=await send('/tasks/' + encodeURIComponent(button.dataset.taskId || ''), { action: button.dataset.taskAction, version: Number(button.dataset.taskVersion) });
        if(receipt.ok!==true)throw new Error('任务操作结果未确认，请刷新核对。');
        try{await refresh();}catch{throw new Error('任务操作已确认，列表暂未刷新，请再次刷新核对。');}
      });
    });
    review?.close.addEventListener('click', () => {
      if (!review || !requests.active || busy || pendingReissue) return;
      reviewedSource = null; review.panel.hidden = true; review.fields.hidden = true;
      updateReviewControls();
    });
    review?.submit.addEventListener('click', () => act(async () => {
      if(!review)return;
      if (!pendingReissue) {
        if (controls.dirty()) throw new Error('请先保存配置，再核对重新投递。');
        if (!reviewedSource || review.fields.hidden || !review.confirmed.checked) throw new Error('请填写新参数和原因，并明确确认重新投递。');
        let payload;
        try { payload = JSON.parse(review.payload.value); } catch { throw new Error('重新投递参数不是有效 JSON，输入已保留。'); }
        const reason = review.reason.value.trim();
        if (!reason || new TextEncoder().encode(reason).length > 512) throw new Error('请简短说明本次重新投递的原因。');
        pendingReissue = { sourceID: reviewedSource.id, sourceStatus: reviewedSource.status, uncertain: false, body: { expected_user_id: identity, source_version: reviewedSource.version, kind: reviewedSource.kind, key: crypto.randomUUID(), payload, reason, confirmed: true } };
      }
      const request = pendingReissue;
      let accepted;
      try {
        accepted = await send('/tasks/' + encodeURIComponent(request.sourceID) + '/reissues', request.body);
        if (!validTask(accepted) || accepted.id === request.sourceID || accepted.kind !== request.body.kind || !validReissue(accepted.reissue) || accepted.reissue.source_id !== request.sourceID || accepted.reissue.source_version !== request.body.source_version || accepted.reissue.source_status !== request.sourceStatus) throw new Error('重新投递结果不完整，请重试原重新投递；输入已保留。');
      } catch (error) {
        if (!request.uncertain && pluginError(error).confirmedRejection) {
          pendingReissue = null; reviewedSource = null; review.confirmed.checked = false;
          throw new Error(pluginError(error).message + ' 本次请求已被拒绝，输入已保留，请重新读取来源任务并核对后提交。');
        }
        request.uncertain = true;
        throw error;
      }
      pendingReissue = null; review.confirmed.checked = false; offset = 0;
      if (scope) scope.value = 'live';
      const message = '重新投递已确认，任务 ' + accepted.id + ' · ' + labels[accepted.status] + (accepted.archived_at ? ' · 已归档。' : '。');
      try { await refresh(); } catch { throw new Error(message + '任务列表暂未刷新，请再次刷新核对。'); }
      note.textContent = message + note.textContent;
    }));
    tasks.querySelector('[data-task-create]')?.addEventListener('click', () => act(async () => {
      if (!pendingEnqueue && controls.dirty()) throw new Error('请先保存配置，再提交任务。');
      if(!pendingEnqueue){const payload:unknown=JSON.parse(required<HTMLTextAreaElement>(tasks,'[data-task-payload]').value);pendingEnqueue={body:{key:crypto.randomUUID(),payload,expected_user_id:identity},uncertain:false};}
      const operation=pendingEnqueue;
      let accepted:unknown;
      try {accepted=await send('/tasks',operation.body);if(!validTask(accepted)||accepted.kind!=='task.execute.v1')throw new Error('任务结果不完整，请重试原请求或刷新核对。');}
      catch(error){if(!operation.uncertain&&pluginError(error).confirmedRejection)pendingEnqueue=null;else operation.uncertain=true;throw error;}
      pendingEnqueue=null;offset=0;
      if (scope) scope.value = 'live';
      const message = accepted.archived_at ? '原请求已归档，未重复入队。' : '任务已接受，当前状态：' + labels[accepted.status] + '。';
      try { await refresh(); } catch { throw new Error(message + '任务列表暂未刷新，请再次刷新核对。'); }
      note.textContent = message + note.textContent;
    }));
  }
  function retire(permanent:boolean) {
    accessLost ||= permanent; requests.suspend();
    root.querySelector('[data-runtime-result]')?.replaceChildren();
    root.querySelector('[data-runtime-preview]')?.setAttribute('hidden','');
    root.querySelector('[data-task-list]')?.replaceChildren();
    const confirmation=root.querySelector<HTMLInputElement>('[data-task-reissue-confirm]');if(confirmation)confirmation.checked=false;
    if(permanent)root.querySelectorAll<HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement|HTMLButtonElement>('input,select,textarea,button').forEach(control=>{control.disabled=true;});
    show(permanent?'登录状态已变化，请重新打开插件页面核对。未确认操作的输入已保留。':'页面已离开，返回后请手动核对操作结果。');
  }
  const onSession=()=>retire(true),onHide=()=>retire(false),onShow=(event:PageTransitionEvent)=>{if(event.persisted&&!accessLost&&!disposed){requests.resume();show('请手动刷新任务并核对配置和操作结果。');}};
  document.addEventListener('chunnel:session',onSession);
  window.addEventListener('pagehide',onHide);window.addEventListener('pageshow',onShow);
  let channel:BroadcastChannel|undefined;
  try{channel=new BroadcastChannel('chunnel.social');channel.addEventListener('message',event=>{if(event.data?.type==='session')onSession();});}catch{/* Session events still guard the page. */}
  const beforeReplace=(event:Event)=>{const detail=(event as CustomEvent<import('./admin-types').AdminRegionChange>).detail;if(!detail?.regions?.some(region=>region===root||region.contains(root)))return;disposed=true;retire(false);channel?.close();document.removeEventListener('chunnel:session',onSession);document.removeEventListener('chunnel:admin-before-replace',beforeReplace);window.removeEventListener('pagehide',onHide);window.removeEventListener('pageshow',onShow);};
  document.addEventListener('chunnel:admin-before-replace',beforeReplace);
})();
