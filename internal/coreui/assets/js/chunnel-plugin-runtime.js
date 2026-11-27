(() => {
  'use strict';
  const root = document.querySelector('[data-runtime-admin]');
  if (!root) return;
  const form = root.querySelector('[data-runtime-form]');
  const status = root.querySelector('[data-runtime-status]');
  const controls = window.ChunnelAdminUI.savebar(form);
  let busy = false;
  const operationMessage = error => error?.name === 'TypeError' && /fetch|network|load failed/i.test(error.message || '') ? '网络连接中断，操作结果尚未确认；输入已保留，请恢复连接后重试或刷新核对。' : error?.message || '操作尚未完成，输入已保留。';
  const show = message => { status.hidden = false; status.textContent = message; };
  const sendTo = async (url, body) => {
    const response = await fetch(url, {
      method: 'POST', credentials: 'same-origin', cache: 'no-store',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': root.dataset.csrf },
      body: JSON.stringify({ expected_user_id: Number(root.dataset.userId), ...body })
    });
    const data = await response.json().catch(() => null);
    if (!response.ok || !data) {
      const error = new Error(data?.error?.message || '未完成操作，输入已保留。');
      error.confirmedRejection = [400, 401, 403, 409, 422, 429].includes(response.status) && typeof data?.error?.code === 'string';
      throw error;
    }
    return data;
  };
  const send = (suffix, body) => sendTo('/api/v1/admin/plugins/' + encodeURIComponent(root.dataset.slug) + suffix, body);
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (busy || !form.reportValidity() || !controls.start()) return;
    busy = true;
    const version = Number(root.dataset.version), values = {};
    form.querySelectorAll('[data-setting-type]').forEach(field => {
      values[field.name] = field.dataset.settingType === 'boolean' ? field.checked : field.dataset.settingType === 'number' ? Number(field.value) : field.value;
    });
    const grants = [...form.querySelectorAll('[data-runtime-grant]:checked')].map(field => field.dataset.runtimeGrant);
    try {
      const data = await send('/configuration', { configuration: { version, values, grants }, subscribe_events: form.querySelector('[data-event-subscription]')?.checked === true });
      if (data.ok !== true || !Number.isSafeInteger(data.version) || data.version !== version + 1) throw new Error('未收到有效保存结果，请刷新核对；输入已保留。');
      root.dataset.version = String(data.version);
      controls.success();
    } catch (error) { controls.fail(error.message); }
    finally { busy = false; }
  });
  root.querySelector('[data-runtime-run]')?.addEventListener('click', async event => {
    if (busy) return;
    if (controls.dirty()) { show('请先保存设置再试运行。'); return; }
    let payload;
    try { payload = JSON.parse(root.querySelector('[data-runtime-payload]').value); }
    catch { show('操作参数不是有效 JSON。'); return; }
    busy = true; const button = event.currentTarget; button.disabled = true;
    try {
      const result = await send('/execute', { payload });
      const output = root.querySelector('[data-runtime-result]'); output.hidden = false; output.textContent = JSON.stringify(result, null, 2);
      const preview = root.querySelector('[data-runtime-preview]'); preview.hidden = !result.fragment;
      if (result.fragment) { preview.querySelector('strong').textContent = result.fragment.title; preview.querySelector('p').textContent = result.fragment.text; preview.dataset.tone = result.fragment.tone; }
      show('试运行已完成');
    } catch (error) { show(error.message); }
    finally { busy = false; button.disabled = false; }
  });
  const tasks = root.querySelector('[data-plugin-tasks]');
  if (tasks) {
    const note = tasks.querySelector('[data-task-status]');
    const filter = tasks.querySelector('[data-task-filter]');
    const scope = tasks.querySelector('[data-task-scope]');
    const archiveAge = tasks.querySelector('[data-task-archive-age]');
    const previous = tasks.querySelector('[data-task-prev]');
    const next = tasks.querySelector('[data-task-next]');
    const reviewPanel = tasks.querySelector('[data-task-review-panel]');
    const reviewFields = tasks.querySelector('[data-task-reissue-fields]');
    const reviewPayload = tasks.querySelector('[data-task-reissue-payload]');
    const reviewReason = tasks.querySelector('[data-task-reissue-reason]');
    const reviewConfirmed = tasks.querySelector('[data-task-reissue-confirm]');
    const reviewSubmit = tasks.querySelector('[data-task-reissue-submit]');
    const reviewClose = tasks.querySelector('[data-task-review-close]');
    let reviewedSource = null, pendingReissue = null;
    let offset = 0, requestKey = null, requestPayload = null;
    const labels = { pending: '等待', running: '执行中', retry: '等待重试', failed: '失败', succeeded: '成功', cancelled: '已取消' };
    const validReissue = trace => trace && /^[a-f0-9]{64}$/.test(trace.source_id) && Number.isSafeInteger(trace.source_version) && trace.source_version > 0 && ['failed', 'cancelled'].includes(trace.source_status) && typeof trace.reviewed_at === 'string' && Number.isFinite(Date.parse(trace.reviewed_at));
    const validTask = task => task && /^[a-f0-9]{64}$/.test(task.id) && task.plugin === root.dataset.slug &&
      typeof task.kind === 'string' && /^[a-z][a-z0-9_.:-]{0,95}$/.test(task.kind) && Object.hasOwn(labels, task.status) &&
      Number.isSafeInteger(task.version) && task.version > 0 && Number.isSafeInteger(task.attempts) && task.attempts >= 0 && task.attempts <= 5 && (!task.reissue || validReissue(task.reissue));
    const updateReviewControls = () => {
      if (!reviewPanel) return;
      reviewClose.disabled = Boolean(pendingReissue);
      reviewSubmit.textContent = pendingReissue ? '重试原重新投递' : '核对后加入队列';
      reviewSubmit.disabled = !reviewedSource || reviewFields.hidden;
    };
    const refresh = async () => {
      const archived = scope?.value === 'archived';
      const response = await fetch('/api/v1/admin/plugins/' + encodeURIComponent(root.dataset.slug) + '/tasks?' + new URLSearchParams({ status: filter.value, offset: String(offset), limit: '20', archived: String(archived) }), { credentials: 'same-origin', cache: 'no-store' });
      const page = await response.json();
      if (!response.ok || !Array.isArray(page.items) || !Number.isSafeInteger(page.total) || page.total < 0 || page.items.length > 20 || !page.items.every(validTask)) throw new Error(page?.error?.message || '任务读取失败');
      const list = tasks.querySelector('[data-task-list]');
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
        if (reviewPanel && (['failed', 'cancelled'].includes(task.status) || task.reissue)) {
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
    const act = async operation => {
      if (busy) return;
      busy = true;
      const disabled = [...tasks.querySelectorAll('[data-task-scope], [data-task-filter], [data-task-archive-age], [data-task-refresh], [data-task-create], [data-task-archive], [data-fanout-archive], [data-task-reissue-submit], [data-task-review-close]')].map(node => [node, node.disabled]);
      disabled.forEach(([node]) => { node.disabled = true; });
      tasks.setAttribute('aria-busy', 'true');
      try { await operation(); } catch (error) { note.textContent = operationMessage(error); }
      finally { busy = false; disabled.forEach(([node, value]) => { node.disabled = value; }); tasks.removeAttribute('aria-busy'); updateReviewControls(); }
    };
    tasks.querySelector('[data-task-refresh]').addEventListener('click', () => act(refresh));
    filter.addEventListener('change', () => act(async () => { offset = 0; await refresh(); }));
    scope?.addEventListener('change', () => act(async () => { offset = 0; await refresh(); }));
    previous.addEventListener('click', () => act(async () => { offset = Math.max(0, offset - 20); await refresh(); }));
    next.addEventListener('click', () => act(async () => { offset += 20; await refresh(); }));
    const archive = async fanouts => {
      const days = Number(archiveAge?.value ?? 30);
      if (![0, 7, 30, 90].includes(days)) throw new Error('归档范围无效。');
      const body = { before: new Date(Date.now() - days * 86400000).toISOString(), limit: 100 };
      const result = await (fanouts ? sendTo('/api/v1/admin/plugins/fanouts/archive', body) : send('/tasks/archive', body));
      if (!Number.isSafeInteger(result.tasks) || !Number.isSafeInteger(result.fanouts) || result.tasks < 0 || result.fanouts < 0 || result.tasks > 100 || result.fanouts > 100) throw new Error('归档结果不完整，请刷新核对。');
      const message = fanouts ? '已归档站点事件回执 ' + result.fanouts + ' 条。' : '已归档本插件终态任务 ' + result.tasks + ' 条。';
      offset = 0;
      try { await refresh(); } catch { throw new Error(message + '任务列表暂未刷新，请再次刷新核对。'); }
      note.textContent = message + note.textContent;
    };
    tasks.querySelector('[data-task-archive]')?.addEventListener('click', () => act(() => archive(false)));
    tasks.querySelector('[data-fanout-archive]')?.addEventListener('click', () => act(() => archive(true)));
    tasks.addEventListener('click', event => {
      const reviewButton = event.target.closest('[data-task-review-open]');
      if (reviewButton && reviewPanel) {
        act(async () => {
          if (pendingReissue) throw new Error('此前重新投递的结果尚未确认，请先重试原重新投递；新输入会保留。');
          const response = await fetch('/api/v1/admin/plugins/' + encodeURIComponent(root.dataset.slug) + '/tasks/' + encodeURIComponent(reviewButton.dataset.taskId) + '/review', { credentials: 'same-origin', cache: 'no-store' });
          const details = await response.json().catch(() => null);
          if (!response.ok || !details) throw new Error(details?.error?.message || '任务核对读取失败。');
          if (!validTask(details.task) || details.task.id !== reviewButton.dataset.taskId || details.current_user_id !== Number(root.dataset.userId) || typeof details.reissuable !== 'boolean' ||
            Boolean(details.task.reissue) !== Boolean(details.provenance) || (details.provenance && (!validReissue(details.provenance) || details.provenance.source_id !== details.task.reissue.source_id || details.provenance.source_version !== details.task.reissue.source_version || !Number.isSafeInteger(details.provenance.reviewer_id) || details.provenance.reviewer_id < 1 || typeof details.provenance.reason !== 'string'))) throw new Error('任务核对信息不完整或当前账户已变化，请刷新核对。');
          reviewedSource = details.task;
          tasks.querySelector('[data-task-review-source]').textContent = reviewedSource.id + ' · ' + reviewedSource.kind + ' · ' + labels[reviewedSource.status] + ' · 版本 ' + reviewedSource.version + (reviewedSource.archived_at ? ' · 已归档，原参数已移除' : '');
          const audit = tasks.querySelector('[data-task-review-audit]'); audit.hidden = !details.provenance;
          if (details.provenance) audit.textContent = '来源任务：' + details.provenance.source_id + ' · 原版本 ' + details.provenance.source_version + ' · 核对账户 #' + details.provenance.reviewer_id + ' · ' + details.provenance.reason;
          reviewPanel.hidden = false; reviewFields.hidden = !details.reissuable;
          // New parameters remain a local draft when the source is refreshed
          // or changed. Every fresh source read requires deliberate confirmation.
          reviewConfirmed.checked = false;
          note.textContent = details.reissuable ? '已读取当前任务记录，请填写并核对新参数后重新投递。' : '当前任务可核对来源，暂不能重新投递。';
          reviewPanel.scrollIntoView?.({ block: 'nearest' });
        });
        return;
      }
      const button = event.target.closest('[data-task-action]');
      if (!button) return;
      act(async () => {
        await send('/tasks/' + encodeURIComponent(button.dataset.taskId), { action: button.dataset.taskAction, version: Number(button.dataset.taskVersion) });
        await refresh();
      });
    });
    reviewClose?.addEventListener('click', () => {
      if (busy || pendingReissue) return;
      reviewedSource = null; reviewPanel.hidden = true; reviewFields.hidden = true;
      updateReviewControls();
    });
    reviewSubmit?.addEventListener('click', () => act(async () => {
      if (!pendingReissue) {
        if (controls.dirty()) throw new Error('请先保存配置，再核对重新投递。');
        if (!reviewedSource || reviewFields.hidden || !reviewConfirmed.checked) throw new Error('请填写新参数和原因，并明确确认重新投递。');
        let payload;
        try { payload = JSON.parse(reviewPayload.value); } catch { throw new Error('重新投递参数不是有效 JSON，输入已保留。'); }
        const reason = reviewReason.value.trim();
        if (!reason || new TextEncoder().encode(reason).length > 512) throw new Error('请简短说明本次重新投递的原因。');
        pendingReissue = { sourceID: reviewedSource.id, sourceStatus: reviewedSource.status, uncertain: false, body: { expected_user_id: Number(root.dataset.userId), source_version: reviewedSource.version, kind: reviewedSource.kind, key: crypto.randomUUID(), payload, reason, confirmed: true } };
      }
      const request = pendingReissue;
      let accepted;
      try {
        accepted = await send('/tasks/' + encodeURIComponent(request.sourceID) + '/reissues', request.body);
        if (!validTask(accepted) || accepted.id === request.sourceID || accepted.kind !== request.body.kind || !validReissue(accepted.reissue) || accepted.reissue.source_id !== request.sourceID || accepted.reissue.source_version !== request.body.source_version || accepted.reissue.source_status !== request.sourceStatus) throw new Error('重新投递结果不完整，请重试原重新投递；输入已保留。');
      } catch (error) {
        if (!request.uncertain && error.confirmedRejection) {
          pendingReissue = null; reviewedSource = null; reviewConfirmed.checked = false;
          throw new Error(error.message + ' 本次请求已被拒绝，输入已保留，请重新读取来源任务并核对后提交。');
        }
        request.uncertain = true;
        throw error;
      }
      pendingReissue = null; reviewConfirmed.checked = false; offset = 0;
      if (scope) scope.value = 'live';
      const message = '重新投递已确认，任务 ' + accepted.id + ' · ' + labels[accepted.status] + (accepted.archived_at ? ' · 已归档。' : '。');
      try { await refresh(); } catch { throw new Error(message + '任务列表暂未刷新，请再次刷新核对。'); }
      note.textContent = message + note.textContent;
    }));
    tasks.querySelector('[data-task-create]')?.addEventListener('click', () => act(async () => {
      if (controls.dirty()) throw new Error('请先保存配置，再提交任务。');
      const text = tasks.querySelector('[data-task-payload]').value;
      const payload = JSON.parse(text);
      // Retain the key after an ambiguous network failure. Changed input is a
      // different request; a confirmed success starts a fresh request next time.
      if (!requestKey || requestPayload !== text) { requestKey = crypto.randomUUID(); requestPayload = text; }
      const accepted = await send('/tasks', { key: requestKey, payload });
      if (!validTask(accepted) || accepted.kind !== 'task.execute.v1') throw new Error('任务结果不完整，请重试原请求或刷新核对。');
      requestKey = null; offset = 0;
      if (scope) scope.value = 'live';
      const message = accepted.archived_at ? '原请求已归档，未重复入队。' : '任务已接受，当前状态：' + labels[accepted.status] + '。';
      try { await refresh(); } catch { throw new Error(message + '任务列表暂未刷新，请再次刷新核对。'); }
      note.textContent = message + note.textContent;
    }));
  }
})();
