import { nonce, failure } from '../shared/ledger-data';
import { tipState, tipPending, tipReceipt, tipUnits } from '../shared/recognition-data';
import type { TipMutation } from '../shared/recognition-data';
import { node } from './recognition-view';
import { required, field, check, report, busy, errorMessage, ownerKey, sessionClient } from './recognition-dom';

export function mountTips(root: HTMLElement): void {
  const post = Number(root.dataset.tipPost), path = `/api/v1/social/tips/${post}`;
  if (!Number.isSafeInteger(post) || post < 1 || root.dataset.tipApi !== path) return;
  const dialog = required<HTMLDialogElement>(root, '[data-tip-dialog]'), form = required<HTMLFormElement>(dialog, 'form');
  const status = required(dialog, '[data-tip-status]'), opener = required<HTMLButtonElement>(root, '[data-tip-open]');
  const result = required(root, '[data-tip-result]'), currency = field(form, 'currency'), amount = field(form, 'amount');
  const privacy = check(form, 'hidden'), group = required(dialog, '[data-tip-presets]');
  document.body.append(dialog);
  let data: ReturnType<typeof tipState> | undefined, pending: TipMutation | undefined, serial = 0, submitting = false, storageKey = '';
  const memory = new Map<string, TipMutation>();
  const client = sessionClient(() => {
    serial++; data = undefined; pending = undefined; submitting = false; storageKey = ''; form.hidden = true; busy(form, false);
    result.textContent = ''; if (dialog.open) dialog.close();
  });
  const active = (ticket: number, epoch: number) => ticket === serial && client.current(epoch) && root.isConnected && dialog.open;
  function save(body: TipMutation, key: string): void { memory.set(key, body); try { sessionStorage.setItem(key, JSON.stringify(body)); } catch { /* In-memory retry remains available. */ } }
  function clear(body: TipMutation, key: string): void {
    if (memory.get(key)?.request_id === body.request_id) memory.delete(key);
    try { const stored: unknown = JSON.parse(sessionStorage.getItem(key) || 'null'); if (tipPending(stored, post).request_id === body.request_id) sessionStorage.removeItem(key); } catch { /* Do not delete another operation. */ }
  }
  function selectCurrency(): void {
    const c = data?.currencies.find(c => c.key === currency.value); group.replaceChildren();
    if (!c) { report(required(dialog, '[data-tip-balance]'), '钱包配置已变化，将核对原打赏记录。'); return; }
    report(required(dialog, '[data-tip-balance]'), `可用 ${c.balance} ${c.symbol} · 单次 ${c.min}–${c.max} ${c.symbol}`);
    c.presets.forEach(value => {
      const button = node('button', value + ' ' + c.symbol); button.type = 'button'; button.setAttribute('aria-pressed', String(amount.value === value));
      try { const units = tipUnits(value, c.precision); button.disabled = units < tipUnits(c.min, c.precision) || units > tipUnits(c.max, c.precision) || units > tipUnits(c.balance, c.precision); } catch { button.disabled = true; }
      button.addEventListener('click', () => { amount.value = value; group.querySelectorAll('button').forEach(b => b.setAttribute('aria-pressed', String(b === button))); });
      group.append(button);
    });
  }
  function pendingUI(): void {
    required<HTMLFieldSetElement>(form, 'fieldset').disabled = !!pending;
    required(form, '[type=submit]').textContent = pending ? '核对并重试原打赏' : '确认打赏';
    report(required(dialog, '[data-tip-confirm]'), pending ? `待确认：${pending.amount} ${pending.currency}，${pending.hidden ? '隐藏数值' : '按原设置展示'}。重试使用同一笔记录。` : '确认后从当前钱包扣除，直接转给文章作者。');
  }
  opener.addEventListener('click', async () => {
    if (submitting) { if (!dialog.open) dialog.showModal(); return; }
    const ticket = ++serial, epoch = client.epoch(); if (!dialog.open) dialog.showModal();
    data = undefined; pending = undefined; form.hidden = true; required(dialog, '[data-tip-login]').hidden = true; report(status, '正在读取钱包…');
    try {
      const session = await client.session(); if (!active(ticket, epoch)) return;
      if (!session.authenticated || !session.id) { report(status, '登录后可以使用积分或余额支持作者。'); required(dialog, '[data-tip-login]').hidden = false; return; }
      const fresh = tipState(await client.request(path), session.id); if (!active(ticket, epoch)) return;
      data = fresh; storageKey = `newpress:tip:${fresh.user_id}:${post}`;
      let saved: string | null = null; try { saved = sessionStorage.getItem(storageKey); } catch { /* Storage can be unavailable. */ }
      pending = memory.get(storageKey);
      if (!pending && saved !== null) { try { pending = tipPending(JSON.parse(saved), post); memory.set(storageKey, pending); } catch { throw new Error('上次打赏记录损坏，无法安全重试，请先在钱包中核对。'); } }
      if (!data.enabled && !pending) { report(status, '该文章暂不支持打赏，作者不能给自己的文章打赏。'); return; }
      if (!data.currencies.length && !pending) { report(status, '暂无可用于打赏的钱包，请稍后再试。'); return; }
      currency.replaceChildren(...data.currencies.map(c => { const o = node('option', c.name); o.value = c.key; return o; }));
      if (pending && !data.currencies.some(c => c.key === pending?.currency)) { const o = node('option', pending.currency); o.value = pending.currency; currency.append(o); }
      currency.value = pending?.currency || data.currencies[0]?.key || ''; amount.value = pending?.amount || data.currencies[0]?.presets[0] || '';
      privacy.checked = pending?.hidden || false;
      required(dialog, '[data-tip-hide-row]').hidden = !pending && (!data.allow_hide_amount || data.hide_amounts);
      form.hidden = false; selectCurrency(); pendingUI(); report(status, pending ? '上次打赏结果待确认，点击下方按钮核对。' : data.hide_amounts ? '评论中仅显示“已打赏”，不公开数值。' : '选择一份心意，支持这篇文章。');
    } catch (e) { if (active(ticket, epoch)) report(status, errorMessage(e), true); }
  });
  required(dialog, '[data-tip-close]').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => { serial++; client.client?.cancel(); submitting = false; busy(form, false); opener.focus(); });
  currency.addEventListener('change', () => { amount.value = data?.currencies.find(c => c.key === currency.value)?.presets[0] || ''; selectCurrency(); });
  amount.addEventListener('input', () => group.querySelectorAll('button').forEach(b => b.setAttribute('aria-pressed', 'false')));
  form.addEventListener('submit', async e => {
    // The privacy input is named "hidden" and masks HTMLFormElement.hidden.
    e.preventDefault(); if (submitting || !data || !dialog.open || form.hasAttribute('hidden')) return;
    const snapshot = data, ticket = serial, epoch = client.epoch(), credentials = ownerKey(client.client?.snapshot() || {}), key = storageKey, retry = !!pending;
    let body: TipMutation;
    try {
      if (pending) body = { ...pending };
      else {
        const c = snapshot.currencies.find(c => c.key === currency.value), value = amount.value.trim(); if (!snapshot.enabled || !c) throw new Error('请重新读取打赏钱包');
        const units = tipUnits(value, c.precision);
        if (units <= 0n || units < tipUnits(c.min, c.precision) || units > tipUnits(c.max, c.precision)) throw new Error('打赏数值超出单次允许范围');
        if (units > tipUnits(c.balance, c.precision)) throw new Error('当前钱包余额不足');
        body = { post_id: post, request_id: nonce(), currency: c.key, amount: value, hidden: privacy.checked && snapshot.allow_hide_amount, settings_version: snapshot.settings_version, currency_version: c.version };
      }
    } catch (e) { report(status, errorMessage(e), true); return; }
    submitting = true; busy(form, true);
    try {
      const session = await client.session(); if (!active(ticket, epoch)) return;
      if (!session.authenticated || session.id !== snapshot.user_id || credentials !== ownerKey(session)) throw new Error('登录状态已变化，请重新打开打赏窗口');
      pending = body; save(body, key); pendingUI(); report(status, '正在确认打赏，请稍候…');
      const response = await client.request(path, body); if (!active(ticket, epoch)) return;
      const message = tipReceipt(response, body); clear(body, key); pending = undefined;
      report(result, message + '；评论标注刷新后可见。'); dialog.close();
    } catch (e) {
      if (!active(ticket, epoch)) return;
      const error = failure(e); report(status, error.message, true);
      // A retry may already have debited the wallet. Only an initial explicit
      // business rejection can release the original request for editing.
      if (!retry && ['recognition.invalid', 'recognition.conflict', 'recognition.disabled', 'recognition.not_found', 'currency.invalid', 'currency.insufficient', 'currency.balance_limit', 'currency.transfer_disabled', 'currency.daily_limit', 'currency.settings_conflict', 'currency.disabled'].includes(error.code || '')) {
        clear(body, key); pending = undefined;
      }
      pendingUI();
    } finally { if (active(ticket, epoch)) { submitting = false; busy(form, false); } }
  });
}
