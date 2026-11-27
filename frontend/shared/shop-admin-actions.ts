import { adminOrder, adminOrders, adminOrderReceipt } from './shop-admin-data';
import { record, string, boolean, identifier } from './ledger-data';
import { shopOrderUpdate } from './shop-data';
import { equal } from './shop-admin-helpers';
import type { ShopAdminOrder, ShopOrderAction } from './shop-admin-types';

interface Storage { getItem(key: string): string | null; setItem(key: string, value: string): void; removeItem(key: string): void }
interface Pending { owner: string; order: ShopAdminOrder; action: ShopOrderAction }
const zero = '0001-01-01T00:00:00Z';
function text(value: unknown, limit: number, required = true): string {
  const v = string(value); if ((required && !v.trim()) || [...v].length > limit || /[\0<>]/.test(v)) throw new Error('售后操作内容未确认'); return v;
}
function name(value: unknown, limit: number): string {
  const result = text(value, limit); if (/[\r\n]/.test(result)) throw new Error('请将此项填写为单行文字'); return result;
}
export function orderAction(value: unknown): ShopOrderAction {
  const v = record(value);
  switch (v.kind) {
    case 'ship': return { kind: 'ship', carrier: name(v.carrier, 80), tracking: name(v.tracking, 120) };
    case 'deliver': { const ticket = string(v.ticket); if (!/^st_[a-f0-9]{28}$/.test(ticket)) throw new Error('奖品编号未确认'); return { kind: 'deliver', ticket, content: text(v.content, 4000) }; }
    case 'review-refund': return { kind: 'review-refund', approved: boolean(v.approved), note: text(v.note, 1000, false) };
    case 'refund': return { kind: 'refund', ...(v.reference === undefined ? {} : { reference: name(v.reference, 200) }), ...(v.note === undefined ? {} : { note: name(v.note, 1000) }) };
    default: throw new Error('售后操作类型未确认');
  }
}
function payload(action: ShopOrderAction): { path: string; body: unknown } {
  switch (action.kind) {
    case 'ship': return { path: 'ship', body: { carrier: action.carrier, tracking: action.tracking } };
    case 'deliver': return { path: 'tickets/' + encodeURIComponent(action.ticket) + '/deliver', body: { content: action.content } };
    case 'review-refund': return { path: 'refund-review', body: { approved: action.approved, note: action.note } };
    case 'refund': return { path: 'refund', body: { ...(action.reference === undefined ? {} : { reference: action.reference }), ...(action.note === undefined ? {} : { note: action.note }) } };
  }
}
function canApply(order: ShopAdminOrder, action: ShopOrderAction): boolean {
  switch (action.kind) {
    case 'ship': return order.status === 'awaiting_shipment';
    case 'deliver': return order.status === 'completed' && order.tickets.some(t => t.id === action.ticket && t.status === 'won' && t.delivered_at === zero);
    case 'review-refund': return order.status === 'refund_requested';
    case 'refund': return order.status === 'refund_required' || order.currency.startsWith('wallet:') && order.status === 'refunded';
  }
}
export function createAdminActions(options: {
  owner: string; readOnly?: boolean; storage: Storage; alive(): boolean;
  api(path: string, method?: string, body?: unknown): Promise<unknown>;
}) {
  if (!options.readOnly || options.owner !== '0') identifier(options.owner);
  const key = (id: string) => 'newpress.shop.admin.pending.' + options.owner + '.' + id;
  const active = () => { if (options.readOnly || !options.alive()) throw new DOMException('商城后台会话已变化', 'AbortError'); };
  const read = (orderID: string): Pending | null => {
    if (options.readOnly) return null;
    try {
      const raw = options.storage.getItem(key(orderID)); if (raw === null) return null;
      const v = record(JSON.parse(raw) as unknown), order = adminOrder(v.order);
      if (v.owner !== options.owner || order.id !== orderID) throw new Error('不匹配');
      return { owner: options.owner, order, action: orderAction(v.action) };
    } catch { throw new Error('售后恢复记录无法读取，请先核对已有处理结果'); }
  };
  const forget = (pending: Pending) => {
    const current = read(pending.order.id);
    if (!equal(current, pending)) throw new Error('售后恢复记录已变化，请先核对');
    options.storage.removeItem(key(pending.order.id));
  };
  const running = new Set<string>();
  return {
    read,
    async perform(before: ShopAdminOrder, value: ShopOrderAction): Promise<{ order: ShopAdminOrder; recoveryPending: boolean }> {
      active(); if (running.has(before.id)) throw new Error('此订单正在处理，请稍候');
      running.add(before.id);
      try {
        const action = orderAction(value), pending = read(before.id);
        if (pending && !equal(action, pending.action)) throw new Error('输入与上次未确认的操作不同，请先按原内容核对结果');
        const original = pending?.order || before;
        const current = (adminOrders(await options.api('/orders'))).find(o => o.id === before.id); active();
        if (!current) throw new Error('订单已不可读取，请先确认权限与处理结果');
        shopOrderUpdate(current, original);
        if (pending) {
          let confirmed: ShopAdminOrder | undefined;
          try { confirmed = adminOrderReceipt({ order: current }, original, pending.action, true); } catch { /* Only a matching result clears the original action. */ }
          if (confirmed) { let recoveryPending = false; try { forget(pending); } catch { recoveryPending = true; } return { order: confirmed, recoveryPending }; }
        }
        if (!canApply(current, action)) throw new Error('订单状态已变化，原操作结果尚未匹配，请重新读取核对');
        if (action.kind === 'refund' && !current.currency.startsWith('wallet:') && (!action.reference || !action.note)) throw new Error('请填写渠道退款凭证与核对说明');
        if (current.paid_amount !== original.paid_amount) throw new Error('订单付款记录已变化，请重新读取核对');
        const attempt: Pending = pending || { owner: options.owner, order: current, action };
        options.storage.setItem(key(before.id), JSON.stringify(attempt));
        if (!equal(read(before.id), attempt)) throw new Error('售后恢复记录未保存，暂未提交');
        const request = payload(action), response = await options.api('/orders/' + encodeURIComponent(before.id) + '/' + request.path, 'POST', request.body); active();
        const result = adminOrderReceipt(response, attempt.order, action);
        let recoveryPending = false; try { forget(attempt); } catch { recoveryPending = true; }
        return { order: result, recoveryPending };
      } finally { running.delete(before.id); }
    },
  };
}
