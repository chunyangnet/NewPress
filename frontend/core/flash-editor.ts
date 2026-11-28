import { nonce, record } from '../shared/ledger-data';
import { APIError } from '../shared/newpress-client';
import { flashRuntime } from '../shared/flash-runtime';
import { flashAction, flashBody, flashDraft, flashPending, flashReceipt, flashZone, sameFlashDraft, stateOnly } from '../shared/flash-data';
import type { FlashDraft, FlashPending } from '../shared/flash-data';

export function mountFlashEditor(form: HTMLFormElement, tell: (text: string) => void): void {
  if (form.dataset.flashReady === 'true') return;
  const owner = Number(form.dataset.userId), admin = form.dataset.admin === 'true', endpoint = form.dataset.endpoint || '';
  if (!Number.isSafeInteger(owner) || owner < 1 || endpoint !== (admin ? '/api/v1/admin/flash' : '/api/v1/flash/submissions')) throw new Error('快报编辑页面的账号或地址未确认');
  const snapshot = (): FlashDraft => {
    const values = new FormData(form), text = (name: string) => String(values.get(name) || '');
    return flashDraft({ title: text('title'), body: text('body'), source_name: text('source_name'), source_url: text('source_url'), comments: values.has('comments'),
      important: values.has('important'), review_note: text('review_note'), published_at: text('published_at'), published_timezone: text('published_timezone') });
  };
  const mountedDraft = snapshot();
  let baseline = mountedDraft, pending: FlashPending | undefined, busy = false, lost = false, paused = false, corrupt = false, reviewZone = '', zoneRejected = false, recoveredKey = '', sending = 0;
  let storageKey = `newpress:flash:${owner}:${endpoint}:${Number(form.dataset.id)}`;
  const originalButtons = [...form.querySelectorAll<HTMLButtonElement>('button')].map(button => ({ button, disabled: button.disabled }));
  const actions = form.querySelector('.flash-actions');
  for (const [value, label] of [['save', '保存'], ['submit', '提交审核'], ['withdraw', '撤回投稿'], ['publish', '审核发布'], ['reject', '退回修改'], ['unpublish', '下架为草稿'], ['trash', '移入回收站'], ['restore', '恢复草稿']] as const) {
    if ((!admin && !['save', 'submit', 'withdraw'].includes(value)) || originalButtons.some(({button}) => button.type === 'submit' && button.value === value)) continue;
    const button = document.createElement('button'); button.type = 'submit'; button.className = 'ui-button'; button.value = value; button.textContent = label;
    button.formNoValidate = stateOnly(flashAction(value)); actions?.append(button); originalButtons.push({button, disabled: false});
  }
  function allowed(value: string): boolean {
    const state = form.dataset.state || 'draft', exists = Number(form.dataset.id) > 0;
    if (state === 'trash') return admin && value === 'restore';
    if (value === 'save') return admin || state === 'draft';
    if (value === 'submit') return state === 'draft';
    if (value === 'withdraw') return state === 'pending';
    if (value === 'reject') return admin && state === 'pending';
    if (value === 'unpublish') return admin && ['publish', 'future'].includes(state);
    return admin && (value === 'publish' || (value === 'trash' && exists));
  }
  const publishedZone = form.querySelector<HTMLInputElement>('[name=published_timezone]'), review = form.querySelector<HTMLElement>('[data-flash-timezone-review]');
  const controls = document.createElement('div'); controls.className = 'flash-actions';
  const retry = document.createElement('button'); retry.type = 'button'; retry.className = 'ui-button'; retry.textContent = '核对上次保存'; retry.dataset.flashRetry = '';
  const restore = document.createElement('button'); restore.type = 'button'; restore.className = 'ui-button'; restore.textContent = '恢复上次提交的输入'; restore.dataset.flashRestore = '';
  const note = document.createElement('p'); note.dataset.flashPending = ''; note.setAttribute('role', 'status');
  controls.append(retry, restore, note); form.append(controls);
  const current = (ticket: number) => !lost && !paused && form.isConnected && runtime.current(ticket);
  function update(): void {
    if (busy) { form.setAttribute('aria-busy', 'true'); form.dataset.busy = 'true'; } else { form.removeAttribute('aria-busy'); delete form.dataset.busy; }
    originalButtons.forEach(({button, disabled}) => { if (button.type === 'submit') button.hidden = !allowed(button.value); button.disabled = disabled || busy || lost || paused || corrupt || ((!!pending || zoneRejected) && button.type === 'submit'); });
    retry.hidden = restore.hidden = !pending;
    retry.disabled = restore.disabled = busy || lost || paused || corrupt;
    note.textContent = pending ? `有一笔${pending.body.id ? '编辑' : '新建'}快报的提交待核对。核对会使用原内容和发布时间，当前输入仍保留。` : corrupt ? '上次保存记录无法读取，请先在稿件列表中核对结果。' : '';
  }
  const runtime = flashRuntime(session => {
    if (!session.authenticated || session.id !== owner) {
      lost = true; busy = false;
      for (const input of form.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>('input, textarea')) { input.value = ''; if (input instanceof HTMLInputElement) input.checked = false; input.disabled = true; }
      tell('登录账号已变化，请重新打开快报页面。'); update();
    }
  }, () => { paused = true; busy = false; sending++; update(); }, () => { paused = false; update(); });
  function persist(): void {
    if (!pending) return;
    try { sessionStorage.setItem(storageKey, JSON.stringify(pending)); } catch { tell('当前标签页仍可核对保存；本机存储不可用，请保留此页面。'); }
  }
  function clear(operation: FlashPending): void {
    try {
      const stored = sessionStorage.getItem(storageKey);
      if (stored && record(JSON.parse(stored)).key === operation.key) sessionStorage.removeItem(storageKey);
    } catch { /* Never erase a different or unreadable operation. */ }
    if (pending?.key === operation.key) pending = undefined;
  }
  try {
    const stored = sessionStorage.getItem(storageKey);
    if (stored !== null) { pending = flashPending(JSON.parse(stored), endpoint, owner, Number(form.dataset.id)); recoveredKey = pending.key; }
  } catch { corrupt = true; }
  // A storage access failure before any write must not disable publishing.
  if (corrupt) { try { if (sessionStorage.getItem(storageKey) === null) corrupt = false; } catch { corrupt = false; } }
  form.dataset.flashReady = 'true'; update();
  function applyDraft(draft: FlashDraft): void {
    for (const [name, value] of Object.entries(draft)) {
      const input = form.elements.namedItem(name);
      if (input instanceof HTMLInputElement || input instanceof HTMLTextAreaElement) {
        if (typeof value === 'boolean' && input instanceof HTMLInputElement) input.checked = value; else input.value = String(value);
      }
    }
  }
  restore.addEventListener('click', () => {
    if (!pending || lost || paused || busy || !confirm('用上次提交的输入替换当前内容？')) return;
    applyDraft(pending.draft);
    tell('已恢复上次输入，请先核对上次保存结果。');
  });
  review?.querySelector('button')?.addEventListener('click', () => {
    if (!publishedZone || !reviewZone || !zoneRejected || busy || lost || paused) return;
    publishedZone.value = reviewZone; review.hidden = true; zoneRejected = false; reviewZone = ''; update();
    tell('已确认当前时区，请核对发布时间后重新保存。');
  });
  async function send(operation: FlashPending, retrying: boolean): Promise<void> {
    const ticket = runtime.epoch(), run = ++sending; busy = true; update(); tell(retrying ? '正在核对上次保存…' : '正在保存…');
    let sent = false;
    try {
      const session = await runtime.session(); if (!current(ticket)) return;
      if (!session.authenticated || session.id !== owner || !session.csrf_token) throw new Error('登录状态未确认，请重新登录后核对');
      pending = operation; persist(); update(); sent = true;
      const value = await runtime.client.request(endpoint, {method: 'POST', body: operation.body, headers: {'Idempotency-Key': operation.key}});
      if (!current(ticket)) return;
      const receipt = flashReceipt(value, operation.body), advanced = Number(form.dataset.version) > receipt.version;
      let now = snapshot();
      const recoveredUnchanged = operation.key === recoveredKey && sameFlashDraft(now, mountedDraft);
      if (recoveredUnchanged && (stateOnly(operation.body.action) || operation.draft.review_note)) { applyDraft(operation.draft); now = snapshot(); }
      clear(operation); zoneRejected = false; reviewZone = ''; if (review) review.hidden = true;
      form.dataset.id = String(receipt.id); form.dataset.version = String(Math.max(Number(form.dataset.version), receipt.version));
      storageKey = `newpress:flash:${owner}:${endpoint}:${receipt.id}`;
      const contentSaved = !stateOnly(operation.body.action);
      // Only a reject action consumes the review field. Other state changes
      // preserve that draft just like title, body, source and publication time.
      if (operation.body.action === 'reject' && now.review_note === operation.draft.review_note) {
        const field = form.elements.namedItem('review_note'); if (field instanceof HTMLTextAreaElement) field.value = '';
        now.review_note = '';
      }
      const preserve = contentSaved ? ((!recoveredUnchanged && !sameFlashDraft(now, operation.draft)) || !!now.review_note) : !sameFlashDraft(now, {...operation.baseline, review_note: operation.body.action === 'reject' ? '' : operation.baseline.review_note});
      if (!advanced) {
        if (contentSaved) baseline = {...operation.draft, review_note: ''};
        const next = {submit: 'pending', publish: 'publish', withdraw: 'draft', reject: 'draft', unpublish: 'draft', trash: 'trash', restore: 'draft'};
        if (operation.body.action !== 'save') form.dataset.state = next[operation.body.action];
      }
      if (preserve || advanced) {
        tell(contentSaved ? '提交时的内容已保存，表单中未提交的修改仍保留；请核对后再保存。' : '快报状态已更新，当前正文修改尚未保存，已保留在表单中。');
        let link = controls.querySelector<HTMLAnchorElement>('a');
        if (!link) { link = document.createElement('a'); link.target = '_blank'; link.rel = 'noopener'; link.textContent = '查看已保存版本'; controls.append(link); }
        link.href = `${location.pathname}?edit=${receipt.id}`;
        history.replaceState(history.state, '', `${location.pathname}?edit=${receipt.id}`);
      } else { tell(receipt.message); location.assign(`${location.pathname}?edit=${receipt.id}`); }
    } catch (error) {
      if (!current(ticket)) return;
      tell(error instanceof Error ? error.message : '快报结果未确认，请核对原请求。');
      if (sent && error instanceof APIError) {
        if (!retrying && error.code === 'flash.timezone_changed' && error.status === 422 && review && publishedZone) {
          try {
            reviewZone = flashZone(record(error.details).published_timezone); zoneRejected = true;
            clear(operation);
            form.querySelectorAll('[data-flash-timezone-label]').forEach(label => { label.textContent = reviewZone; }); review.hidden = false;
          } catch { /* Unverified details cannot reinterpret the stored local time. */ }
        } else if (!retrying && ['flash.invalid_action', 'flash.invalid_version', 'flash.forbidden', 'flash.editor_fields', 'flash.locked', 'flash.invalid_state', 'flash.review_required', 'flash.invalid_time', 'flash.validation', 'flash.rate_limited'].includes(error.code)) clear(operation);
      }
    } finally { if (!lost && form.isConnected && run === sending) { busy = false; update(); } }
  }
  retry.addEventListener('click', () => { if (pending && !busy && !lost && !paused && !corrupt) void send(pending, true); });
  form.addEventListener('submit', event => {
    event.preventDefault(); if (busy || lost || paused || corrupt || pending || zoneRejected) return;
    try {
      const action = flashAction(event.submitter instanceof HTMLButtonElement ? event.submitter.value || 'save' : 'save');
      if (!stateOnly(action) && !form.reportValidity()) return;
      if (['trash', 'unpublish', 'reject'].includes(action) && !confirm('确认执行此操作？')) return;
      const draft = snapshot();
      const body = flashBody({...draft, review_note: action === 'reject' ? draft.review_note : '', expected_user_id: owner, id: Number(form.dataset.id), version: Number(form.dataset.version), action}, owner, admin);
      void send({schema: 1, endpoint, key: nonce(), body, draft, baseline}, false);
    } catch (error) { tell(error instanceof Error ? error.message : '请核对快报内容'); }
  });
}
