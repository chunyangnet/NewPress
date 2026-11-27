import type { Action, Message, MessageThread, SocialAPI, SocialDOM, SocialState, ThreadMode } from './interaction-types';
import { parseMessages, parseSentMessage } from './interaction-data';
import { confirmed, errorMessage } from './account-data';

interface Dependencies {
  state: SocialState; api: SocialAPI; dom: Pick<SocialDOM, '$' | 'empty'>;
  activeThread(thread: MessageThread): boolean;
  renderThread(thread: MessageThread, mode?: ThreadMode): void;
  saveThread(thread: MessageThread): void;
  announce(message: string): void;
  loadInbox(): Promise<void>;
  loadNotices(append?: boolean, quiet?: boolean): Promise<void>;
  run(action: Action): Promise<unknown>;
  connection(message: string): void;
  showToast(message: string, tone?: string): void;
  signIn(action?: Action): Promise<unknown>;
  openPeer(peer: number): Promise<unknown>;
  nonce(): string;
  atBottom(element: HTMLElement | null): boolean;
  mobile: Pick<MediaQueryList, 'matches'>;
  messageDrawer: HTMLDialogElement | null;
  channel?: Pick<BroadcastChannel, 'postMessage'>;
}
export function createMessageSync(deps: Dependencies) {
  const { state, api, dom: { $, empty }, activeThread, renderThread, saveThread, announce, loadInbox, loadNotices, run, connection, showToast, signIn, openPeer, nonce, atBottom, mobile, messageDrawer, channel } = deps;
  function mergeMessages(t: MessageThread, data: { items: Message[]; read_through?: number; contact?: MessageThread['contact'] }): void {
    for (const m of data.items) {
      t.messages.set(m.id, m);
      if (m.nonce && m.sender_id === t.owner) t.outbox = t.outbox.filter(item => item.nonce !== m.nonce || item.content !== m.content);
    }
    t.readThrough = Math.max(t.readThrough, data.read_through || 0);
    if (data.contact) t.contact = data.contact;
    saveThread(t);
  }
  async function loadThread(t: MessageThread, mode: ThreadMode = 'refresh'): Promise<void> {
    if (!messageDrawer || t.loading || state.session?.id !== t.owner) return;
    t.loading = true;
    const epoch = state.epoch, history = mode === 'older';
    const before = history && t.messages.size ? Math.min(...t.messages.keys()) : 0;
    let cursor = !history && t.loaded ? t.syncedThrough : 0;
    try {
      let more: boolean;
      do {
        const query = before ? `?before=${before}` : cursor ? `?after=${cursor}` : '';
        const value = await api(`/api/v1/social/messages/${t.peer}${query}`);
        if (epoch !== state.epoch) return;
        const data = parseMessages(value, t.owner, t.peer, cursor, before);
        mergeMessages(t, data);
        // Only forward reads advance this boundary; send acknowledgements may jump over unread incoming messages.
        if (!history) for (const m of data.items) t.syncedThrough = Math.max(t.syncedThrough, m.id);
        if (history || !cursor) t.hasMore = data.has_more;
        more = !!cursor && data.has_more;
        cursor = data.next_cursor;
        t.loaded = true; renderThread(t, mode); mode = 'refresh';
      } while (more && activeThread(t));
      if (activeThread(t)) $('[data-message-error]').hidden = true;
    } catch (error) {
      if (epoch !== state.epoch || error instanceof Error && error.name === 'AbortError') return;
      if (activeThread(t)) {
        if (!t.loaded) empty($('[data-message-log]'), '会话暂时无法加载', errorMessage(error), () => loadThread(t, 'initial'));
        else { const box = $('[data-message-error]'); box.textContent = errorMessage(error); box.hidden = false; }
      }
      throw error;
    } finally {
      t.loading = false;
      if (activeThread(t)) { $<HTMLButtonElement>('[data-message-history]').disabled = false; void run(() => markRead(t)); }
    }
  }
  async function markRead(t: MessageThread): Promise<void> {
    if (!messageDrawer || !activeThread(t) || document.hidden || t.loading || t.reading || !atBottom($('[data-message-scroll]')) || mobile.matches && !messageDrawer.classList.contains('has-thread') || document.querySelector('dialog[open]:not(#message-drawer)')) return;
    const unread = [...t.messages.values()].filter(m => m.recipient_id === t.owner && !m.read_at && m.id > t.readSent);
    if (!unread.length) return;
    const through = Math.max(...unread.map(m => m.id)), epoch = state.epoch;
    t.reading = true;
    try {
      const data = await api(`/api/v1/social/messages/${t.peer}/read`, { method: 'POST', body: { through } });
      if (epoch !== state.epoch) return;
      confirmed(data);
      t.readSent = Math.max(t.readSent, through);
      for (const m of t.messages.values()) if (m.recipient_id === t.owner && m.id <= through) m.read_at ||= new Date().toISOString();
      channel?.postMessage({ type: 'read', user: t.owner });
      await Promise.all([loadInbox(), loadNotices(false, true)]);
    } catch (error) { if (activeThread(t) && !(error instanceof Error && error.name === 'AbortError')) connection('已读状态尚未同步，重连后自动重试'); }
    finally { t.reading = false; }
  }
  async function sendMessage(t: MessageThread, content: string, retryNonce?: string): Promise<unknown> {
    if (!messageDrawer) return;
    if (state.session?.id !== t.owner || !state.session.authenticated) return signIn(() => openPeer(t.peer));
    content = content.replace(/\r\n/g, '\n').trim();
    if (!content || [...content].length > 2000) { showToast('请输入 1 至 2000 字符', 'warning'); return; }
    if (t.outbox.some(item => item.status === 'sending')) return;
    if (!retryNonce && t.outbox.length >= 20) { showToast('请先重试尚未发送成功的消息', 'warning'); return; }
    const epoch = state.epoch;
    let pending = retryNonce ? t.outbox.find(item => item.nonce === retryNonce) : undefined;
    if (retryNonce && !pending) return;
    if (!pending) { pending = { nonce: nonce(), content, created_at: new Date().toISOString(), status: 'sending', error: '', retryAt: 0 }; t.outbox.push(pending); }
    if (pending.retryAt > Date.now()) { showToast(`请在 ${Math.ceil((pending.retryAt - Date.now()) / 1000)} 秒后重试`, 'warning'); return; }
    pending.status = 'sending'; pending.error = '';
    if (!retryNonce && activeThread(t)) { const input = $<HTMLTextAreaElement>('[data-message-input]'); input.value = ''; input.dispatchEvent(new Event('input', { bubbles: true })); }
    saveThread(t); renderThread(t, 'send');
    let acknowledged = false;
    try {
      const data = await api(`/api/v1/social/messages/${t.peer}`, { method: 'POST', body: { content: pending.content, nonce: pending.nonce } });
      if (epoch !== state.epoch) return;
      mergeMessages(t, { items: [parseSentMessage(data, t.owner, t.peer, pending)] }); acknowledged = true;
      renderThread(t, 'send'); announce('消息已发送');
      await loadInbox(); channel?.postMessage({ type: 'messages', user: t.owner });
    } catch (error) {
      if (epoch !== state.epoch) return;
      if (acknowledged) { connection('消息已发送，会话列表尚未刷新，重连后自动重试'); return; }
      pending.status = 'failed'; pending.error = errorMessage(error);
      const retryAfter = error && typeof error === 'object' && 'retryAfter' in error && typeof error.retryAfter === 'number' && Number.isFinite(error.retryAfter) ? Math.max(0, error.retryAfter) : 0;
      pending.retryAt = Date.now() + retryAfter * 1000;
      saveThread(t); renderThread(t, 'send'); announce('发送失败，消息已保留，可点击重试');
    }
  }
  return { loadThread, markRead, sendMessage };
}
