import { flashEngagement, flashEngagementReceipt, flashShare } from '../shared/flash-data';
import type { FlashEngagementBody } from '../shared/flash-data';
import type { EngagementStates } from '../shared/account-types';
import { flashRuntime } from '../shared/flash-runtime';

export function mountFlashEngagement(tell: (text: string) => void): void {
  const items = new Map<number, HTMLElement[]>();
  for (const item of document.querySelectorAll<HTMLElement>('[data-flash-id]')) {
    const id = Number(item.dataset.flashId); if (!Number.isSafeInteger(id) || id < 1) continue;
    items.set(id, [...(items.get(id) || []), item]);
  }
  const states = new Map<string, EngagementStates['like'][number]>(), pending = new Map<string, {body: FlashEngagementBody; owner: number}>(), busy = new Set<number>(), serials = new Map<number, number>();
  let identity: number | undefined, paused = false;
  const buttons = (id: number) => (items.get(id) || []).flatMap(item => [...item.querySelectorAll<HTMLButtonElement>('[data-flash-engagement]')]);
  const original = new WeakMap<HTMLButtonElement, boolean>();
  for (const id of items.keys()) buttons(id).forEach(button => original.set(button, button.disabled));
  const disable = (id: number) => buttons(id).forEach(button => { button.disabled = !!original.get(button) || paused || busy.has(id); });
  const key = (kind: string, id: number) => `${kind}:${id}`;
  const invalidate = () => {
    states.clear(); pending.clear(); busy.clear();
    for (const id of items.keys()) {
      serials.set(id, (serials.get(id) || 0) + 1); disable(id);
      buttons(id).forEach(button => { button.removeAttribute('aria-pressed'); const count = button.querySelector('span'); if (count) count.textContent = '—'; });
    }
  };
  const runtime = flashRuntime(session => {
    const owner = session.authenticated ? session.id || 0 : 0;
    if (identity !== undefined && identity !== owner) { invalidate(); if (!paused) queueMicrotask(() => void load()); }
    identity = owner;
  }, () => { paused = true; invalidate(); }, () => { paused = false; items.forEach((_, id) => disable(id)); void load(); });
  function paint(id: number, kind: 'like' | 'bookmark', active: boolean, count?: number): void {
    buttons(id).filter(button => button.dataset.flashEngagement === kind).forEach(button => {
      button.setAttribute('aria-pressed', String(active)); const label = button.querySelector('span'); if (label) label.textContent = count === undefined ? '—' : String(count);
    });
  }
  async function read(ids: number[], ticket: number): Promise<void> {
    const versions = new Map(ids.map(id => [id, serials.get(id) || 0]));
    const data = flashEngagement(await runtime.client.request(`/api/v1/social/engagement?posts=${encodeURIComponent(ids.join(','))}`), ids);
    if (!runtime.current(ticket)) return;
    for (const id of ids) {
      if ((serials.get(id) || 0) !== versions.get(id)) continue;
      for (const kind of ['like', 'bookmark'] as const) {
        const row = data[kind].find(row => row.target_id === id);
        if (row) { states.set(key(kind, id), row); paint(id, kind, row.active, row.count); }
        else { states.delete(key(kind, id)); buttons(id).filter(button => button.dataset.flashEngagement === kind).forEach(button => { button.removeAttribute('aria-pressed'); const count = button.querySelector('span'); if (count) count.textContent = '—'; }); }
      }
    }
  }
  async function load(): Promise<void> {
    const ticket = runtime.epoch();
    try {
      await runtime.session(); if (!runtime.current(ticket) || paused) return;
      const ids = [...items.keys()]; for (let offset = 0; offset < ids.length; offset += 100) await read(ids.slice(offset, offset + 100), ticket);
    } catch { if (runtime.current(ticket)) tell('互动状态暂时无法加载，点击互动按钮可重新核对。'); }
  }
  if (items.size) void load();
  document.addEventListener('click', async event => {
    const button = event.target instanceof Element ? event.target.closest<HTMLButtonElement>('[data-flash-engagement], [data-flash-share]') : null;
    if (!button || button.disabled || paused) return;
    if (button.hasAttribute('data-flash-share')) {
      const ticket = runtime.epoch(); button.disabled = true;
      try {
        const url = flashShare(button.dataset.flashShare || '', location.origin);
        if (navigator.share) { try { await navigator.share({url}); if (runtime.current(ticket)) tell('分享已完成'); return; } catch (error) { if (error instanceof Error && error.name === 'AbortError') return; } }
        if (!runtime.current(ticket)) return;
        try { await navigator.clipboard.writeText(url); if (runtime.current(ticket)) tell('链接已复制'); } catch { if (runtime.current(ticket)) tell(`复制此链接分享：${url}`); }
      } catch (error) { if (runtime.current(ticket)) tell(error instanceof Error ? error.message : '分享未完成'); }
      finally { button.disabled = false; }
      return;
    }
    const id = Number(button.closest<HTMLElement>('[data-flash-id]')?.dataset.flashId), kind = button.dataset.flashEngagement;
    if (!items.has(id) || busy.has(id) || (kind !== 'like' && kind !== 'bookmark')) return;
    const ticket = runtime.epoch(), serial = (serials.get(id) || 0) + 1; busy.add(id); serials.set(id, serial); disable(id);
    let confirmed = false;
    try {
      const session = await runtime.session(); if (!runtime.current(ticket)) return;
      if (!session.authenticated || !session.id || !session.csrf_token) throw new Error('请先登录后操作，可从个人中心进入登录页面。');
      const operationKey = key(kind, id); let operation = pending.get(operationKey);
      if (!operation) {
        if (!states.has(operationKey)) await read([id], ticket);
        if (!runtime.current(ticket)) return;
        const state = states.get(operationKey); if (!state) throw new Error('这条快报的互动状态尚未确认，请刷新后核对。');
        operation = {body: {kind, target_id: id, active: !state.active, collection: kind === 'bookmark' ? state.collection : ''}, owner: session.id};
      }
      if (operation.owner !== session.id) throw new Error('登录账号已变化，请重新核对互动状态。');
      pending.set(operationKey, operation);
      const value = await runtime.client.request('/api/v1/social/engagement', {method: 'POST', body: operation.body});
      if (!runtime.current(ticket)) return;
      flashEngagementReceipt(value, operation.body); pending.delete(operationKey); confirmed = true;
      const state = states.get(operationKey); if (state) state.active = operation.body.active;
      paint(id, kind, operation.body.active); tell(operation.body.active ? '已保存互动' : '已取消互动');
      await read([id], ticket);
    } catch (error) {
      if (runtime.current(ticket)) tell(confirmed ? '互动已保存，数量刷新未完成。' : `${error instanceof Error ? error.message : '互动结果未确认'}${pending.has(key(kind, id)) ? ' 再次点击会重试原操作。' : ''}`);
    } finally { if (serials.get(id) === serial) { busy.delete(id); disable(id); } }
  });
}
