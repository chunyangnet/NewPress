import type { Notice, NoticeBoundary, SocialAPI, SocialDOM, SocialState } from './interaction-types';
import { parseNotices } from './interaction-data';
import { confirmed } from './account-data';

interface Dependencies {
  state: SocialState; api: SocialAPI; dom: SocialDOM; notificationDrawer: HTMLDialogElement;
  badges(): void; announce(message: string): void; openPeer(peer: number): Promise<unknown>;
  setOverlay(element: HTMLElement, open: boolean): void;
  channel?: Pick<BroadcastChannel, 'postMessage'>;
}
export function createNoticeCenter(deps: Dependencies) {
  const { state, api, dom: { $, $$, optional, node, button, empty, createIcon, dateLabel, fullDate }, notificationDrawer, badges, announce, openPeer, setOverlay, channel } = deps;
  let sequence = 0, foreground = 0, mutation: symbol | null = null;
  const compare = (a: Notice, b: Notice): number => Date.parse(b.created_at) - Date.parse(a.created_at) || (a.id[0] === b.id[0] ? Number(b.id.slice(1)) - Number(a.id.slice(1)) : b.id.localeCompare(a.id));
  async function markNotice(notice: Notice, open: boolean): Promise<void> {
    const epoch = state.epoch;
    const response = await api('/api/v1/social/notice-center', { method: 'POST', body: { action: 'read', id: notice.id } });
    if (epoch !== state.epoch) return;
    confirmed(response); sequence++; notice.read = true;
    if (open) {
      if (notice.peer_id) await openPeer(notice.peer_id);
      else if (notice.url) location.assign(notice.url);
    }
    if (epoch !== state.epoch) return;
    await loadNotices(false, open);
    if (epoch === state.epoch) channel?.postMessage({ type: 'read', user: state.session?.id });
  }
  function renderNotices(): void {
    const list = $('[data-notification-list]'), signature = JSON.stringify([state.noticeFilter, state.notices]);
    $('[data-notification-more]').hidden = !state.noticeMore;
    if (list.dataset.signature === signature && list.children.length) return;
    const focused = list.contains(document.activeElement) ? document.activeElement : null;
    const focusedRow = focused?.closest<HTMLElement>('[data-notification-id]');
    const focusID = focusedRow?.dataset.notificationId, focusIndex = focusedRow ? [...list.children].indexOf(focusedRow) : -1;
    const focusRead = !!focused?.closest('.notification-mark-read'), scroll = list.scrollTop;
    if (!state.notices.length) {
      empty(list, state.noticeFilter === 'unread' ? '未读已清零' : '还没有通知', state.noticeFilter === 'unread' ? '所有通知都已看过，新的消息会出现在这里。' : '收到回复、提及、作者更新' + (optional('#message-drawer') ? '、审核结果和私信' : '和审核结果') + '后，会在这里提醒你。', null, '', 'bell-ring');
    } else list.replaceChildren(...state.notices.map(notice => {
      const row = node('article', `notification-item${notice.read ? '' : ' is-unread'}`); row.dataset.notificationId = notice.id;
      const open = button('', 'notification-open', () => markNotice(notice, true));
      const icon = node('span', 'notification-icon'); icon.append(createIcon(notice.kind === 'publication' ? 'newspaper' : notice.kind === 'moderation' ? 'check-circle' : 'message-square'));
      const copy = node('span', 'notification-copy'), heading = node('b', '', notice.title);
      const detail = node('span', '', notice.peer_id ? '点击进入会话，继续这次交流' : notice.url ? '查看这次更新' : '内容已撤回或当前无法访问');
      const time = node('time', '', dateLabel(notice.created_at)); time.dateTime = notice.created_at; time.title = fullDate(notice.created_at);
      copy.append(heading, detail, time); open.append(icon, copy); row.append(open);
      if (!notice.read) {
        const read = button('', 'notification-mark-read', () => markNotice(notice, false), 'check');
        read.setAttribute('aria-label', `将「${notice.title}」标为已读`); read.title = '标为已读'; row.append(read);
      }
      return row;
    }));
    if (focusedRow) {
      const rows = $$('[data-notification-id]', list), same = rows.find(row => row.dataset.notificationId === focusID);
      const neighbor = same || rows[Math.min(focusIndex, rows.length - 1)];
      const target = neighbor && (same && focusRead && optional('.notification-mark-read', same) || optional('.notification-open', neighbor));
      (target || $$('[data-notification-filter]').find(button => button.dataset.notificationFilter === state.noticeFilter))?.focus({ preventScroll: true });
    }
    list.scrollTop = scroll; $('[data-notification-more]').hidden = !state.noticeMore; list.dataset.signature = signature;
  }
  async function loadNotices(append = false, quiet = false): Promise<void> {
    if (!state.session?.authenticated || (quiet || append) && foreground) return;
    const epoch = state.epoch, request = ++sequence;
    if (!quiet) foreground = request;
    const filter = state.noticeFilter, cursor = append ? state.noticeCursor : '';
    try {
      const response = await api(`/api/v1/social/notice-center?filter=${filter}${cursor ? `&before=${encodeURIComponent(cursor)}` : ''}`);
      if (epoch !== state.epoch || request !== sequence) return;
      const data = parseNotices(response);
      if (append && data.has_more && data.next_cursor === cursor) throw new Error('通知分页没有继续推进，请重试');
      state.noticeUnread = data.unread;
      state.noticeThrough = data.through.message_id || data.through.activity_id ? data.through : null;
      if (!quiet || notificationDrawer.open || !state.notices.length) {
        if (append) {
          const notices = new Map(state.notices.map(n => [n.id, n])); data.items.forEach(n => notices.set(n.id, n));
          state.notices = [...notices.values()].sort(compare);
        } else if (quiet && state.noticeCursor && state.notices.length > 50) {
          const oldest = data.items.at(-1); state.notices = [...data.items, ...state.notices.filter(n => oldest && compare(n, oldest) > 0)];
        } else state.notices = data.items;
        if (append || !quiet || !state.noticeCursor || state.notices.length <= 50) { state.noticeCursor = data.next_cursor; state.noticeMore = data.has_more; }
        renderNotices();
      }
      badges();
    } catch (error) { if (epoch === state.epoch && request === sequence) throw error; }
    finally { if (foreground === request) foreground = 0; }
  }
  async function updateNotices(action: 'read_all' | 'clear', through: NoticeBoundary | null = state.noticeThrough): Promise<void> {
    if (!through || mutation || !state.session?.authenticated) return;
    const token = Symbol(), epoch = state.epoch; mutation = token;
    const confirm = $<HTMLButtonElement>('[data-confirm-notification-clear]'); confirm.disabled = true;
    $<HTMLButtonElement>('[data-notification-read-all]').disabled = true;
    try {
      const response = await api('/api/v1/social/notice-center', { method: 'POST', body: { action, through } });
      if (epoch !== state.epoch) return;
      confirmed(response); sequence++;
      if (action === 'clear') setOverlay($('#interaction-confirm'), false);
      state.noticeCursor = ''; await loadNotices();
      if (epoch !== state.epoch) return;
      channel?.postMessage({ type: 'read', user: state.session?.id }); announce(action === 'clear' ? (optional('#message-drawer') ? '通知已清空，私信记录保留' : '通知已清空') : '通知已标为已读');
    } finally { if (mutation === token) { mutation = null; confirm.disabled = false; badges(); } }
  }
  function reset(): void { sequence++; foreground = 0; mutation = null; $<HTMLButtonElement>('[data-confirm-notification-clear]').disabled = false; }
  return { renderNotices, loadNotices, updateNotices, reset };
}
