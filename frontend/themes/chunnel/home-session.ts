import type { HomeLoad } from './home-types';
import { $, $$ } from './home-dom';

function object(value: unknown): Record<string, unknown> | null { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null; }
function integer(value: unknown): value is number { return Number.isSafeInteger(value); }

export function initHomeSession(load: HomeLoad, featuresReady: () => boolean): void {
  const show = (selector: string, visible: boolean) => { $$(selector).forEach(node => { node.hidden = !visible; }); };
  const text = (selector: string, value: string) => { $$(selector).forEach(node => { node.textContent = value; }); };
  // Read-only state until the full interaction module takes over. Never store credentials.
  const lifecycle = new AbortController();
  const options = { signal: lifecycle.signal };
  const preferences = () => {
    let value: Record<string, unknown>; try { value = object(JSON.parse(localStorage.getItem('chunnel.preferences') || '{}')) || {}; } catch { value = {}; }
    // Normalize stored URLs once per refresh, not once per visible card.
    const saved = (Array.isArray(value.saved) ? value.saved.slice(0, 200) : []).flatMap(candidate => {
      const item = object(candidate);
      if (!item || typeof item.title !== 'string' || typeof item.url !== 'string' || !item.url.trim()) return [];
      try {
        const url = new URL(item.url, location.origin);
        if (url.origin === location.origin && ['http:', 'https:'].includes(url.protocol))
          return [[url.pathname + url.hash, ['/', '/market/'].includes(item.url) && item.title]];
      } catch { /* Ignore invalid local preferences. */ }
      return [];
    });
    const following = Array.isArray(value.following) ? value.following.slice(0, 200) : [];
    $$('[data-favorite]').forEach((button) => {
      const card = button.closest('article'); if (!card) return;
      const title = card.querySelector('h3')?.textContent?.trim() || '';
      const active = saved.some(([url, legacy]) => url === card.dataset.contentUrl || legacy === title);
      button.setAttribute('aria-pressed', String(active)); button.classList.toggle('is-favorite', active);
      button.setAttribute('aria-label', `${active ? '取消本机收藏' : '收藏到本机'}：${title}`);
      button.querySelector('use')?.setAttribute('href', `#icon-${active ? 'heart-fill' : 'heart'}`);
    });
    $$('[data-follow]').forEach((button) => {
      const active = following.includes(button.dataset.follow);
      button.setAttribute('aria-pressed', String(active)); button.classList.toggle('is-following', active);
      button.querySelector('use')?.setAttribute('href', `#icon-${active ? 'check' : 'plus'}`);
      if (button.lastChild) button.lastChild.textContent = active ? '已关注' : '关注';
    });
  };
  preferences();
  window.addEventListener('storage', (event) => { if (!event.key || event.key === 'chunnel.preferences') preferences(); }, options);

  let polling: ReturnType<typeof setTimeout> | undefined, request: AbortController | null | undefined, identity = 0, progressionOwner = 0;
  const counts = (selector: string, count: unknown) => {
    const value = integer(count) && count > 0 ? count : 0;
    $$(selector).forEach((element) => { element.hidden = !value; element.textContent = value > 99 ? '99+' : String(value); });
    return value;
  };
  const session = async (force = false) => {
    if (request || (!force && lifecycle.signal.aborted) || document.hidden) return;
    clearTimeout(polling);
    const pending = new AbortController(); request = pending;
    const timer = setTimeout(() => pending.abort(), 8000);
    let failed = false;
    const json = async (path: string): Promise<Record<string, unknown>> => {
      const response = await fetch('/api/v1/social/' + path, { cache: 'no-store', headers: { Accept: 'application/json' }, signal: pending.signal });
      if (!response.ok) throw new Error();
      const data = object(await response.json());
      if (!data) throw new Error();
      return data;
    };
    try {
      const value = await json('session');
      if (pending.signal.aborted) return;
      if (typeof value.authenticated !== 'boolean') throw new Error();
      const nextID = value.authenticated ? value.id : 0;
      if (!integer(nextID) || (value.authenticated && nextID <= 0)) throw new Error();
      identity = nextID;
      if (!identity) progressionOwner = 0;
      const name = identity && typeof value.name === 'string' ? value.name : '';
      const avatar = [...name][0] || '';
      show('[data-home-guest], [data-account-login], [data-account-register]', !identity);
      show('[data-home-member], [data-account-menu]', !!identity);
      text('[data-home-name], [data-account-name]', name);
      text('[data-home-member-avatar], [data-account-avatar]', avatar);
      if (identity && progressionOwner !== identity && !featuresReady()) text('[data-home-checkin-label]', '签到领取');
      show('[data-account-admin]', !!identity && value.can_admin === true);
      show('[data-account-publish]', !!identity && value.can_publish === true);
      const memberRequest = identity && $('[data-home-progression]') && (force || progressionOwner !== identity);
      const messaging = !$('[data-messaging-enabled="false"]');
      const requests: (Promise<Record<string, unknown> | null> | null)[] = identity ? [messaging ? json('inbox') : null, json('notice-center')] : [];
      if (memberRequest) requests.push(Promise.all([json('progression'), load('chunnel-home-member.js', () => window.ChunnelHomeMember)]).then(([data]) => data).catch(() => null));
      const data = await Promise.all(requests);
      if (pending.signal.aborted) return;
      const messages = counts('[data-message-unread-count]', data[0]?.unread);
      const notices = counts('[data-unread-count]', data[1]?.unread);
      if (memberRequest) {
        if (data[2] && window.ChunnelHomeMember?.paint(data[2], identity)) { progressionOwner = identity; }
        else text('[data-home-member-state]', '会员状态暂不可用');
      }
      show('[data-unread-dot]', !!notices);
      show('[data-menu-unread]', !!(messages || notices));
      for (const [kind, count, label] of [['message', messages, '私信'], ['notification', notices, '通知']]) {
        $$(`[data-drawer-open="${kind}-drawer"]`).forEach((element) => element.setAttribute('aria-label', `打开${label}${count ? `，${count} 条未读` : ''}`));
      }
    } catch { failed = true; /* Retry interrupted reads without polling anonymous visitors. */ }
    finally {
      clearTimeout(timer);
      if (request === pending) {
        request = null;
        if (!lifecycle.signal.aborted && !document.hidden && (identity || failed)) polling = setTimeout(session, 30000);
      }
    }
  };
  let channel: BroadcastChannel | undefined;
  try { channel = new BroadcastChannel('chunnel.social'); channel.onmessage = () => { request?.abort(); request = null; session(); }; } catch { /* Polling covers browsers without channels. */ }
  const refresh = (event: Event) => {
    const next = Number(object((event as CustomEvent<unknown>).detail)?.id) || 0;
    if (event.type === 'chunnel:session' && next === identity && progressionOwner === identity) return;
    request?.abort(); request = null; session(true);
  };
  document.addEventListener('chunnel:session', refresh);
  document.addEventListener('chunnel:checkin', refresh);
  document.addEventListener('visibilitychange', () => { if (document.hidden) { clearTimeout(polling); request?.abort(); } else session(); }, options);
  window.addEventListener('online', () => session(), options);
  window.addEventListener('pageshow', () => { preferences(); session(); }, options);
  // Explicit reward refreshes remain active after handoff and must stop on navigation too.
  window.addEventListener('pagehide', () => { clearTimeout(polling); request?.abort(); });
  window.ChunnelHome = { refreshFeed: () => { if (featuresReady()) document.dispatchEvent(new CustomEvent('chunnel:guest-preferences')); else preferences(); }, stop: () => { lifecycle.abort(); request?.abort(); clearTimeout(polling); channel?.close(); } };
  session();
}
