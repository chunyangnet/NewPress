import type { ExperienceDependencies, GuestPreferences } from './experience-types';
import type { ClientSession } from './newpress-client';
import { object } from './account-data';

export function mountCommunityList(deps: ExperienceDependencies, preferences: () => GuestPreferences): void {
  const { getSession, signIn, run, storage, showToast, dom: { $, $$, optional, node } } = deps;
  function syncCommunityPagination() {
    $$<HTMLAnchorElement>('[data-community-page]').forEach(link => {
      const url = new URL(link.href), page = url.searchParams.get('page') || '1';
      url.search = location.search;
      if (page === '1') url.searchParams.delete('page'); else url.searchParams.set('page', page);
      url.hash = 'questions'; link.href = url.pathname + url.search + url.hash;
    });
  }
  const discussionList = optional('.discussion-list');
  if (!discussionList) return;
  const filter = $<HTMLSelectElement>('[data-discussion-filter]'), sort = $<HTMLSelectElement>('[data-discussion-sort]');
  const empty = node('p', 'interaction-inline-empty'); empty.hidden = true; empty.setAttribute('role', 'status'); discussionList.after(empty);
  const cards = $$('.discussion-post-card', discussionList);
  let userID = 0, sessionObserved = false;
  const render = () => {
    const url = new URL(location.href), scope = url.searchParams.get('discussion') || 'all';
    filter.value = ['all', 'liked', 'mine'].includes(scope) ? scope : 'all';
    sort.value = url.searchParams.get('discussion_sort') === 'oldest' ? 'oldest' : 'newest';
    const liked = new Set(preferences().likes);
    const focused = discussionList.contains(document.activeElement) && document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const ordered = sort.value === 'oldest' ? [...cards].reverse() : cards;
    ordered.forEach((card, index) => {
      card.hidden = filter.value === 'liked' ? !liked.has(`post:${card.dataset.postId}`) : filter.value === 'mine' && (!userID || Number(card.dataset.authorId) !== userID);
      if (card.hidden && card.contains(document.activeElement)) filter.focus({ preventScroll: true });
      if (discussionList.children[index] !== card) discussionList.insertBefore(card, discussionList.children[index] || null);
    });
    if (focused && !focused.closest('[hidden]') && document.activeElement !== focused) focused.focus({ preventScroll: true });
    const count = cards.filter(card => !card.hidden).length;
    $('[data-discussion-count]').textContent = `${count} / ${cards.length} 条讨论`;
    empty.textContent = filter.value === 'liked' ? '本页没有已赞的讨论' : filter.value === 'mine' ? userID ? '本页没有你发布的话题' : '登录后查看你的话题' : '还没有公开讨论';
    empty.hidden = count > 0; syncCommunityPagination();
  };
  const change = () => {
    const url = new URL(location.href);
    for (const [parameter, value, fallback] of [['discussion', filter.value, 'all'], ['discussion_sort', sort.value, 'newest']] as const) {
      if (value === fallback) url.searchParams.delete(parameter); else url.searchParams.set(parameter, value);
    }
    history.pushState({ ...history.state }, '', url); render();
    if (filter.value === 'mine' && !userID) run(() => signIn(render));
  };
  const syncIdentity = (session: ClientSession) => { userID = session.authenticated && session.id ? session.id : 0; render(); };
  filter.addEventListener('change', change); sort.addEventListener('change', change);
  window.addEventListener('popstate', render); document.addEventListener('chunnel:preferences', render);
  document.addEventListener('chunnel:session', event => { sessionObserved = true; syncIdentity(event.detail); });
  getSession().then(session => {
    if (!sessionObserved) syncIdentity(session);
    const value = storage.get('chunnel.community.published', null);
    if (!value || typeof value !== 'object' || Array.isArray(value)) return;
    const published = object(value);
    if (published.owner !== userID || !userID) return;
    storage.remove('chunnel.community.published');
    if (typeof published.id !== 'number' || !Number.isSafeInteger(published.id) || published.id <= 0 || typeof published.at !== 'number' || !Number.isFinite(published.at) || published.at > Date.now() || Date.now() - published.at >= 60000) return;
    const card = document.getElementById(`discussion-${published.id}`);
    if (card) { showToast('讨论已发布，大家现在可以阅读和回复了'); card.focus({ preventScroll: true }); }
  }).catch(() => {});
  render();
}
