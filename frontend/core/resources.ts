import { resourceState, resourcePreview, downloadReceipt, submissionReceipt, pointsReceipt, resourceOrder, resourceHistory, resourceError, ResourceError } from './resource-public-data';
import type { ResourceView, ResourceState, ResourceDraft } from './resource-public-data';
import type { PaymentOrder } from '../shared/payment-data';
import { esc, logo, login, date, renderResource } from './resource-public-view';
import { record, nonce } from '../shared/ledger-data';

type Snapshot = { epoch: number; user: string; revision: string; variant: string; csrf: string; paymentVersion: string };
const message = (error: unknown) => error instanceof Error ? error.message : '操作结果未确认，请重新核验。';
const required = <T extends Element = HTMLElement>(root: ParentNode, selector: string): T => {
  const element = root.querySelector<T>(selector); if (!element) throw new Error('资源控件不可用，请刷新页面'); return element;
};
const controllers = new Set<AbortController>();
let epoch = 0, suspended = false, firstReads: Promise<unknown> = Promise.resolve();
const request = async (url: string, data?: object, csrf = ''): Promise<unknown> => {
  const controller = new AbortController(), timer = setTimeout(() => controller.abort(), 15000);
  controllers.add(controller);
  try {
    const response = await fetch(url, { credentials: 'same-origin', cache: 'no-store', signal: controller.signal, redirect: 'error', headers: { Accept: 'application/json', ...(data ? { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf } : {}) }, ...(data ? { method: 'POST', body: JSON.stringify(data) } : {}) });
    const value: unknown = await response.json();
    if (!response.ok) throw resourceError(value, response.status);
    return value;
  } finally { clearTimeout(timer); controllers.delete(controller); }
};
const owners: { reset: (session: boolean) => void; resume: () => void; matchesSession?: (authenticated: boolean, id: string, csrf: string) => boolean }[] = [];
const actor = (user: string, csrf: string) => user === '0' ? `guest:${csrf}` : user;

for (const root of document.querySelectorAll<HTMLElement>('[data-npr-resource]')) {
  const preview = root.hasAttribute('data-npr-preview');
  if (preview && root.dataset.nprPreview !== 'snapshot') continue;
  const found = root.querySelector<HTMLElement>('.npr-content'); if (!found) continue;
  const area: HTMLElement = found;
  const target = { post: root.dataset.nprPost || '', id: root.dataset.nprResource || '' };
  if (!/^[1-9][0-9]*$/.test(target.post) || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(target.id)) continue;
  const path = `/api/v1/article-resources/${target.post}/${target.id}`;
  let state: ResourceView | undefined, selected = '', previewUnlocked = false, identity: string | undefined, lastCSRF = '';
  let serial = 0, operation = 0, busy: number | undefined, lastLoad = 0;
  const drafts = new Map<string, ResourceDraft>(), downloads = new Map<string, string>();
  const dialogs = new Set<HTMLDialogElement>();
  // A submit endpoint consumes quiz attempts and has no idempotency key.
  // Unknown outcomes require a successful read before another deliberate attempt.
  const uncertain = new Map<string, ResourceDraft>();
  const active = (ticket: number) => ticket === epoch && !suspended && root.isConnected;
  const current = (s: Snapshot) => active(s.epoch) && identity === actor(s.user, s.csrf);
  const key = (s: Snapshot) => `${actor(s.user, s.csrf)}:${s.revision}:${s.variant}`;
  const snapshot = (): Snapshot => {
    if (!state || state.preview) throw new Error('请先重新核验资源');
    return { epoch, user: state.user_id, revision: state.revision, variant: selected, csrf: state.csrf, paymentVersion: state.payment_version };
  };
  const status = (copy: string) => { const node = area.querySelector<HTMLElement>('.npr-status'); if (node) node.textContent = copy; };
  const send = (data: object, s: Snapshot) => {
    if (preview || !current(s)) throw new Error('账户或页面已变化，请重新核验');
    return request(path, { revision: s.revision, variant: s.variant, ...data }, s.csrf);
  };
  function capture(): ResourceDraft | undefined {
    if (!state || state.preview) return;
    const form = area.querySelector<HTMLFormElement>('.npr-form'); if (!form) return;
    const draft: ResourceDraft = { fields: {}, answers: {} };
    for (const field of form.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>('[data-field]')) {
      if (field.dataset.field) draft.fields[field.dataset.field] = field instanceof HTMLInputElement && field.type === 'checkbox' ? String(field.checked) : field.value;
    }
    for (const field of form.querySelectorAll<HTMLInputElement | HTMLSelectElement>('[data-answer]')) if (field.dataset.answer) draft.answers[field.dataset.answer] = field.value;
    return draft;
  }
  function saveDraft(force = false): void {
    const draft = capture();
    if (draft && (force || drafts.has(key(snapshot())) || Object.values({ ...draft.fields, ...draft.answers }).some(v => v !== '' && v !== 'false'))) drafts.set(key(snapshot()), draft);
  }
  function render(): void {
    if (!state) return;
    if (!state.variants.some(v => v.id === selected)) selected = state.variants.find(v => v.allowed)?.id || state.variants[0]?.id || '';
    const draft = !state.preview ? drafts.get(key(snapshot())) : undefined;
    const view = draft ? { ...state, variants: state.variants.map(v => v.id === selected ? { ...v, quiz_passed: Object.keys(draft.answers).length ? false : v.quiz_passed, form_complete: Object.keys(draft.fields).length ? false : v.form_complete } : v) } : state;
    area.innerHTML = renderResource(view, selected, previewUnlocked, !!draft);
    if (draft) {
      for (const n of area.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>('[data-field], [data-answer]')) {
        const value = n.dataset.field ? draft.fields[n.dataset.field] : draft.answers[n.dataset.answer || ''];
        if (value === undefined) continue;
        if (n instanceof HTMLInputElement && n.type === 'checkbox') n.checked = value === 'true'; else n.value = value;
      }
    }
  }
  function clearOwner(): void {
    serial++; busy = undefined; drafts.clear(); downloads.clear(); uncertain.clear();
    for (const dialog of dialogs) dialog.close();
    dialogs.clear(); state = undefined; identity = undefined; lastCSRF = '';
  }
  async function refresh(): Promise<boolean> {
    if (preview) return false;
    saveDraft();
    const ticket = ++serial, ownerEpoch = epoch;
    try {
      // Serialize initial reads so resources on one anonymous page share the
      // server-created visitor cookie instead of creating competing actors.
      const read = () => { if (!active(ownerEpoch) || ticket !== serial) throw new Error('旧读取已取消'); return request(path); };
      const pending = identity === undefined ? firstReads.then(read, read) : read();
      if (identity === undefined) firstReads = pending.catch(() => undefined);
      const next = resourceState(await pending, target);
      if (!active(ownerEpoch) || ticket !== serial) return false;
      if (identity !== undefined && identity !== actor(next.user_id, next.csrf)) { clearOwner(); selected = ''; }
      else saveDraft();
      identity = actor(next.user_id, next.csrf); lastCSRF = next.csrf; state = next; lastLoad = Date.now(); render(); return true;
    } catch (error) {
      if (active(ownerEpoch) && ticket === serial) {
        state = undefined;
        if (error instanceof ResourceError && [401, 403].includes(error.status)) clearOwner();
        area.innerHTML = `<p class="npr-status" role="status">${esc(message(error))}</p><button type="button" data-refresh>重试加载</button>`;
      }
      return false;
    }
  }
  async function purchase(button: HTMLButtonElement): Promise<void> {
    if (!state || state.preview) return;
    const view: ResourceState = state, v = view.variants.find(v => v.id === selected), group = Number(button.dataset.group);
    const condition = v?.groups[group]?.conditions.find(c => c.id === button.dataset.condition);
    if (!v || !Number.isInteger(group) || !condition || !['points', 'payment'].includes(condition.kind) || condition.passed || !condition.purchasable || !view.logged_in || v.blocked || !v.available || dialogs.size) return;
    const s = snapshot(), channels = view.channels.filter(c => c.currency === condition.currency);
    const dialog = document.createElement('dialog'); dialog.className = 'npr-checkout';
    dialog.innerHTML = `<form method="dialog"><button class="npr-close" aria-label="关闭">×</button></form><p class="npr-eyebrow">UNLOCK YOUR ACCESS</p><h2>${esc(view.title)}</h2><p>${esc(v.name)} · ${esc(condition.label)}</p><p>购买只完成本组的付费条件，其他条件仍需保持满足。</p>${view.ends_at ? `<p>资源访问截止：${esc(date(view.ends_at, view.timezone))}</p>` : ''}${condition.kind === 'payment' ? `<label>支付方式<select data-channel>${channels.map(c => `<option value="${esc(c.key)}">${esc(c.name)}</option>`).join('')}</select></label><details><summary>支付与退款说明</summary><p>${esc(view.refund_policy)}</p><p>${esc(view.disclaimer)}</p></details>` : '<p>确认后从站内钱包扣除相应积分，重复提交不会重复扣除。</p>'}<label class="npr-agree"><input type="checkbox" data-agree>我已核对解锁条件、金额及${condition.kind === 'payment' ? '退款说明' : '积分扣除说明'}</label><button type="button" class="npr-primary" data-confirm ${condition.kind === 'payment' && !channels.length ? 'disabled' : ''}>${condition.kind === 'payment' && !channels.length ? '暂未配置可用支付渠道' : '确认并继续 →'}</button><div class="npr-payment-result" role="status"></div>`;
    document.body.append(dialog); dialogs.add(dialog); dialog.showModal();
    dialog.addEventListener('close', () => { dialogs.delete(dialog); dialog.remove(); }, { once: true });
    const valid = () => current(s) && dialogs.has(dialog) && dialog.open && dialog.isConnected;
    const result = required(dialog, '.npr-payment-result'), confirm = required<HTMLButtonElement>(dialog, '[data-confirm]');
    let original: object | undefined, order: PaymentOrder | undefined;
    const completed = async (kind: string) => {
      const loaded = await refresh(); if (!valid()) return;
      const allowed = state?.variants.find(v => v.id === s.variant)?.allowed;
      dialog.close(); status(!loaded ? `${kind}已确认，资源状态刷新失败，请重新核验。` : allowed ? `${kind}已完成，对应版本已解锁。` : `${kind}已完成，购买凭据已保留。请核验其余条件、版本选择与访问时限。`);
    };
    const showOrder = async (value: unknown, resumed = false, copy = '') => {
      if (!valid()) return;
      const parsed = resourceOrder(value, s.user, order ? { id: order.id } : resumed ? undefined : { amount: condition.amount, currency: condition.currency });
      if (order && (parsed.amount !== order.amount || parsed.currency !== order.currency)) throw new Error('原订单金额发生变化，请保留订单编号并核验');
      order = parsed;
      if (parsed.status === 'completed') { await completed('支付'); return; }
      const note = parsed.status === 'review' ? '支付事实已记录，订单需要管理员核对资源交付或退款。请保留订单编号。' : parsed.status === 'paid' ? '已确认收款，正在核对资源权益，请勿再次付款。' : parsed.status === 'cancelled' ? '订单已取消，请先在原订单中核对渠道结果，再返回文章重新购买。' : parsed.status === 'refunded' ? '订单已退款，解锁权益已停止。' : Date.parse(parsed.expires_at) <= Date.now() ? '支付时限已到，请核对原订单。确认渠道关闭前不会创建第二笔支付。' : '请在原订单中核对金额和支付时限后继续。';
      result.innerHTML = `<p>${esc(copy || note)}</p><p>订单金额 ${esc(parsed.amount)} ${esc(parsed.currency)}</p><a class="npr-primary" href="/billing/?order=${encodeURIComponent(parsed.id)}" target="_blank" rel="noopener">打开原订单与安全收银台 ↗</a>${['pending', 'paid', 'cancelled'].includes(parsed.status) ? '<button type="button" data-sync>核验原订单</button>' : ''}<p data-order-status></p>`;
      const sync = result.querySelector<HTMLButtonElement>('[data-sync]');
      sync?.addEventListener('click', async () => {
        if (!valid() || sync.disabled) return; sync.disabled = true;
        try { const receipt = await request(`/api/v1/social/billing/orders/${parsed.id}/sync`, {}, s.csrf); if (valid()) await showOrder(receipt); }
        catch (error) { if (valid()) required(result, '[data-order-status]').textContent = message(error); }
        finally { if (valid()) sync.disabled = false; }
      });
    };
    confirm.addEventListener('click', async () => {
      if (!valid() || confirm.disabled) return;
      if (!required<HTMLInputElement>(dialog, '[data-agree]').checked) { result.textContent = '请先核对并勾选确认。'; return; }
      const channel = dialog.querySelector<HTMLSelectElement>('[data-channel]');
      if (condition.kind === 'payment' && !channels.some(c => c.key === channel?.value)) { result.textContent = '请选择已核验的支付渠道'; return; }
      original ??= { action: condition.kind === 'points' ? 'points' : 'pay', condition: condition.id, group, channel: channel?.value || '', accepted: true, ...(condition.kind === 'payment' ? { payment_version: s.paymentVersion } : {}) };
      if (channel) channel.disabled = true;
      confirm.disabled = true;
      try {
        const value = await send(original, s); if (!valid()) return;
        if (condition.kind === 'points') { pointsReceipt(value); await completed('积分支付'); }
        else await showOrder(value);
      } catch (error) {
        if (!valid()) return;
        if (error instanceof ResourceError && error.code === 'payment.pending') {
          try { await showOrder(record(error.details).order, true, error.message); }
          catch (failure) { if (valid()) result.textContent = message(failure); }
          return;
        }
        result.textContent = message(error);
        if (error instanceof ResourceError && ['resource.changed', 'payment.conflict'].includes(error.code)) {
          await refresh(); if (valid()) result.textContent = '条件或支付说明已更新，请关闭本弹窗，重新核对后确认购买。';
        } else { confirm.disabled = false; confirm.textContent = '核验并继续原操作'; }
      }
    });
  }
  area.addEventListener('click', async event => {
    const button = event.target instanceof Element ? event.target.closest<HTMLButtonElement>('button') : null;
    if (!button || busy !== undefined || button.disabled || suspended) return;
    if (preview) {
      if (state && button.dataset.version) { selected = button.dataset.version; render(); }
      else if (state && ['locked', 'unlocked'].includes(button.dataset.previewView || '')) {
        previewUnlocked = button.dataset.previewView === 'unlocked'; render();
        area.querySelector<HTMLButtonElement>(`.npr-preview-controls [data-preview-view="${button.dataset.previewView}"]`)?.focus();
      } else status('预览模式不会执行真实解锁操作。');
      return;
    }
    if (button.hasAttribute('data-refresh')) { await refresh(); return; }
    if (!state || state.preview) return;
    if (button.dataset.version) { saveDraft(); selected = button.dataset.version; render(); return; }
    const s = snapshot(), token = ++operation;
    busy = token; button.disabled = true;
    try {
      if (button.hasAttribute('data-buy')) await purchase(button);
      if (button.dataset.engage === 'follow' || button.dataset.engage === 'like') {
        const kind = button.dataset.engage, target_id = kind === 'follow' ? state.author : state.post;
        const value = record(await request('/api/v1/social/engagement', { kind, target_id, active: true, collection: '' }, s.csrf));
        if (!current(s)) return;
        // Engagement response contracts are shared with the account controls.
        if (value.ok !== true || value.target_id !== target_id || value.active !== true || value.collection !== '') throw new Error('互动结果未确认，请重新核验');
        await refresh();
      }
      if (button.hasAttribute('data-tip')) {
        const trigger = document.querySelector<HTMLElement>('[data-recognition-tip], [data-tip-open], [data-support-open]');
        if (trigger) trigger.click(); else status('请使用文章下方的打赏入口，完成后点击重新核验。');
      }
      if (button.dataset.download) {
        const link = button.dataset.download, variant = state.variants.find(v => v.id === s.variant);
        if (!variant?.allowed || !variant.downloads.some(d => d.id === link)) return;
        const downloadKey = `${key(s)}:${link}`;
        if (!downloads.has(downloadKey)) downloads.set(downloadKey, nonce().replaceAll('-', ''));
        const file = downloadReceipt(await send({ action: 'download', link, request: downloads.get(downloadKey) }, s));
        if (!current(s) || selected !== s.variant || !state || state.preview || state.revision !== s.revision) return;
        const output = [...area.querySelectorAll<HTMLElement>('[data-delivery]')].find(n => n.dataset.delivery === link);
        if (!output) return;
        output.innerHTML = `<a href="${esc(file.url)}" target="_blank" rel="noopener noreferrer">打开下载地址 ↗</a>${file.code ? `<span>提取码 <b>${esc(file.code)}</b><button type="button" data-copy="${esc(file.code)}">复制</button></span>` : ''}${file.password ? `<span>解压密码 <b>${esc(file.password)}</b><button type="button" data-copy="${esc(file.password)}">复制</button></span>` : ''}`;
        state.remaining = file.remaining;
        const remaining = area.querySelector<HTMLElement>('.npr-controls>span'); if (remaining) remaining.textContent = file.remaining < 0 ? '下载次数不限' : `剩余下载 ${file.remaining} 次`;
        status('下载地址已准备好。本次获取已记录，重复点击不会再次计数。');
      }
      if (button.dataset.copy) { await navigator.clipboard.writeText(button.dataset.copy); if (current(s)) status('已复制'); }
    } catch (error) { if (current(s)) status(message(error)); }
    finally { if (busy === token) busy = undefined; if (current(s)) button.disabled = false; }
  });
  area.addEventListener('input', event => { if (event.target instanceof Element && event.target.closest('.npr-form')) saveDraft(true); });
  area.addEventListener('change', event => { if (event.target instanceof Element && event.target.closest('.npr-form')) saveDraft(true); });
  area.addEventListener('submit', async event => {
    const form = event.target; if (!(form instanceof HTMLFormElement) || !form.matches('.npr-form')) return;
    event.preventDefault(); if (preview) { status('预览模式不会提交答题或表单资料。'); return; }
    if (busy !== undefined || !state || state.preview || suspended) return;
    const s = snapshot(), draft = capture(); if (!draft) return;
    const draftKey = key(s), token = ++operation;
    if (uncertain.has(draftKey)) {
      const original = uncertain.get(draftKey);
      busy = token;
      try {
        const loaded = await refresh();
        if (loaded && current(s)) {
          uncertain.delete(draftKey);
          const v = state?.variants.find(v => v.id === s.variant);
          const satisfied = (!Object.keys(draft.answers).length || v?.quiz_passed) && (!Object.keys(draft.fields).length || v?.form_complete);
          if (satisfied && JSON.stringify(drafts.get(draftKey)) === JSON.stringify(original)) { drafts.delete(draftKey); render(); }
          status(satisfied ? '当前条件已满足。上次提交无需重复，请核验解锁状态。' : `已核验当前状态，剩余答题次数 ${v?.attempts_remaining ?? 0}。再次提交会进行一次新的核验，请确认后操作。`);
        }
      } finally { if (busy === token) busy = undefined; }
      return;
    }
    drafts.set(draftKey, draft); uncertain.set(draftKey, draft); busy = token;
    try {
      const receipt = submissionReceipt(await send({ action: 'submit', ...(Object.keys(draft.fields).length ? { fields: draft.fields } : {}), ...(Object.keys(draft.answers).length ? { answers: draft.answers } : {}) }, s));
      if (!current(s)) return;
      uncertain.delete(draftKey);
      const latest = capture(), changed = latest && JSON.stringify(latest) !== JSON.stringify(draft);
      if (changed && latest) drafts.set(draftKey, latest); else drafts.delete(draftKey);
      // refresh normally captures fields; remove the submitted form first only
      // when no new input exists, so it cannot restore an already accepted draft.
      if (!changed) form.remove();
      const loaded = await refresh(); if (!current(s)) return;
      status(!loaded ? '提交已确认，资源状态刷新失败，请重新核验。' : changed ? '提交已确认，提交期间的新输入已保留。' : Object.keys(draft.answers).length && !receipt.quiz_passed ? `答案尚未达到通过要求，今日还可尝试 ${receipt.attempts_remaining} 次。` : '提交成功，解锁条件已更新。');
    } catch (error) {
      if (current(s)) status(`${message(error)} 下次提交将先核验当前状态，不会立即重复答题。`);
    } finally { if (busy === token) busy = undefined; }
  });
  if (preview) {
    try {
      const encoded = root.dataset.nprPreviewState || ''; if (encoded.length > 2 << 20) throw new Error('预览过大');
      const raw: unknown = JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(encoded.replace(/-/g, '+').replace(/_/g, '/')), c => c.charCodeAt(0))));
      state = resourcePreview(raw, target); render();
    } catch { state = undefined; area.innerHTML = '<p class="npr-status" role="status">预览配置无法读取，请返回编辑器重新打开预览。</p>'; }
    finally { delete root.dataset.nprPreviewState; }
  } else {
    owners.push({ reset: session => {
      if (session) clearOwner();
      else { saveDraft(); serial++; busy = undefined; for (const dialog of dialogs) dialog.close(); state = undefined; }
      area.innerHTML = '<p class="npr-status" role="status">正在重新核验账户与访问权限…</p>';
    }, resume: () => { void refresh(); }, matchesSession: (authenticated, id, csrf) => authenticated ? identity === id && lastCSRF === csrf : !!identity?.startsWith('guest:') });
    window.addEventListener('focus', () => { if (!suspended && busy === undefined && !dialogs.size && !drafts.size && Date.now() - lastLoad > 60000) void refresh(); });
    void refresh();
  }
}

const library = document.querySelector<HTMLElement>('[data-npr-library]');
if (library) {
  let ticket = 0;
  const refresh = async () => {
    const expected = ++ticket, ownerEpoch = epoch;
    try {
      const items = resourceHistory(await request('/api/v1/social/downloads'));
      if (expected !== ticket || ownerEpoch !== epoch || suspended || !library.isConnected) return;
      library.innerHTML = items.length ? items.map(item => `<a class="npr-library-item" href="${esc(item.url)}"><span>${logo('direct')}<b>${esc(item.title)}</b></span><small>${esc(new Date(item.created_at).toLocaleString())} →</small></a>`).join('') : '<p>还没有下载记录，解锁资源后会显示在这里。</p>';
    } catch (error) { if (expected === ticket && ownerEpoch === epoch && !suspended && library.isConnected) library.innerHTML = `<p>${esc(message(error))}</p><a href="${login()}">登录查看我的下载</a>`; }
  };
  owners.push({ reset: () => { ticket++; library.innerHTML = '<p>正在重新核验下载记录…</p>'; }, resume: () => { void refresh(); } });
  void refresh();
}
function reset(session: boolean): void { epoch++; for (const c of controllers) c.abort(); firstReads = Promise.resolve(); for (const owner of owners) owner.reset(session); }
let sessionIdentity: string | undefined;
document.addEventListener('chunnel:session', event => {
  const session = event.detail;
  if (session && typeof session.authenticated === 'boolean') {
    const id = String(session.id || ''), csrf = String(session.csrf_token || ''), next = session.authenticated ? `${id}:${csrf}` : 'guest';
    const known = owners.filter(owner => owner.matchesSession);
    if (known.length ? known.every(owner => owner.matchesSession?.(session.authenticated, id, csrf)) : next === sessionIdentity) { sessionIdentity = next; return; }
    sessionIdentity = next;
  } else sessionIdentity = undefined;
  reset(true); if (!suspended) for (const owner of owners) owner.resume();
});
window.addEventListener('pagehide', () => { suspended = true; reset(false); });
window.addEventListener('pageshow', event => { if (event.persisted || suspended) { suspended = false; for (const owner of owners) owner.resume(); } });
