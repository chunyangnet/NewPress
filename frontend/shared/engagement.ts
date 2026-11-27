import type { EngagementKind, EngagementStates } from './account-types';
import type { ClientSession } from './newpress-client';
import { confirmed, parseEngagement } from './account-data';

export function mountEngagement(): void {
  const interactions = window.ChunnelInteractions, ui = window.Chunnel;
  if (!interactions || !ui) return;
  const { api, getSession, run } = interactions, { createIcon, showToast } = ui;
  let session: ClientSession | null = null, sequence = 0, identity = 0;
  let states: EngagementStates = { bookmark: [], like: [], follow: [] };
  const busy = new Map<string, symbol>();
  const all = (selector: string): HTMLButtonElement[] => [...document.querySelectorAll<HTMLButtonElement>(selector)];
  const idFor = (el: HTMLElement, kind: EngagementKind): number => Number(kind === 'follow' ? el.dataset.followUser : kind === 'like' ? el.dataset.reaction : el.closest<HTMLElement>('[data-post-id]')?.dataset.postId);
  function adopt(next: ClientSession): boolean {
    const changed = session?.id !== next.id || session?.authenticated !== next.authenticated;
    session = next;
    if (changed) { identity++; sequence++; states = { bookmark: [], like: [], follow: [] }; busy.clear(); }
    return changed;
  }
  function paint(): void {
    if (!session?.authenticated) return;
    for (const [kind, selector] of [['bookmark', '[data-favorite]'], ['like', '[data-reaction]'], ['follow', '[data-follow]']] as const) {
      const entries = new Map(states[kind].map(s => [s.target_id, s]));
      all(selector).forEach(button => {
        const id = idFor(button, kind), item = entries.get(id), active = !!item?.active;
        button.setAttribute('aria-pressed', String(active));
        button.classList.toggle('is-favorite', kind === 'bookmark' && active);
        button.classList.toggle('is-following', kind === 'follow' && active);
        button.classList.toggle('is-active', kind === 'like' && active);
        if (kind === 'bookmark') {
          button.title = active ? '已保存到账号收藏' : '收藏到账号';
          button.setAttribute('aria-label', active ? '取消账号收藏' : '收藏到账号');
          button.querySelector('use')?.setAttribute('href', `#icon-${active ? 'heart-fill' : 'heart'}`);
          const label = button.querySelector('span'); if (label) label.textContent = active ? '已收藏' : '收藏';
        } else {
          button.title = kind === 'follow' ? (!id ? '该署名尚未关联账号' : id === session?.id ? '这是你自己的主页' : active ? '取消关注' : '关注创作者') : '点赞保存在你的账号中';
          const label = kind === 'follow' ? (active ? '已关注' : '关注') : (active ? '已赞' : '赞');
          const text = document.createElement('span'); text.textContent = label + (kind === 'like' && item?.count ? ` ${item.count}` : '');
          button.replaceChildren(createIcon(kind === 'follow' ? active ? 'check' : 'plus' : active ? 'heart-fill' : 'heart'), text);
        }
      });
    }
    document.dispatchEvent(new CustomEvent('chunnel:preferences'));
  }
  async function refresh(): Promise<void> {
    const request = ++sequence;
    const current = await getSession();
    if (request !== sequence) return;
    adopt(current);
    const activeRequest = sequence, owner = identity;
    if (!current.authenticated) return;
    const ids = (selector: string, kind: EngagementKind): number[] => [...new Set(all(selector).map(el => idFor(el, kind)).filter(id => Number.isSafeInteger(id) && id > 0))].slice(0, 100);
    const posts = [...new Set([...ids('[data-favorite]', 'bookmark'), ...ids('[data-reaction]', 'like')])].slice(0, 100), authors = ids('[data-follow]', 'follow');
    const data = await api(`/api/v1/social/engagement?posts=${posts.join(',')}&authors=${authors.join(',')}`);
    if (activeRequest !== sequence || owner !== identity) return;
    states = parseEngagement(data);
    paint();
  }
  async function set(kind: EngagementKind, targetID: number, active: boolean, collection = ''): Promise<boolean> {
    const before = identity, current = await getSession();
    if (!current.authenticated) throw new Error('请登录后继续');
    if (before !== identity || !session?.authenticated || session.id !== current.id) return false;
    if (!Number.isSafeInteger(targetID) || targetID < 1) throw new Error('内容或账号当前不可用，请刷新页面');
    const owner = identity, key = `${kind}:${targetID}`, operation = Symbol();
    if (busy.has(key)) return false;
    busy.set(key, operation); sequence++;
    try {
      const data = await api('/api/v1/social/engagement', { method: 'POST', body: { kind, target_id: targetID, active, collection } });
      if (owner !== identity) return false;
      confirmed(data); sequence++;
      const item = states[kind].find(s => s.target_id === targetID);
      if (item) { item.count = Math.max(0, item.count + (active === item.active ? 0 : active ? 1 : -1)); item.active = active; item.collection = collection; }
      paint();
      document.dispatchEvent(new CustomEvent('chunnel:engagement', { detail: { kind, targetID, active } }));
      return true;
    } finally { if (busy.get(key) === operation) busy.delete(key); }
  }
  async function toggle(button: HTMLButtonElement, kind: EngagementKind): Promise<void> {
    const id = idFor(button, kind);
    if (!id) throw new Error(kind === 'follow' ? '这个署名还没有关联可关注的账号' : '这项内容暂不支持账号收藏');
    if (kind === 'follow' && id === session?.id) { showToast('这是你自己的主页', 'info'); return; }
    button.disabled = true;
    const owner = identity;
    try {
      await refresh();
      if (owner !== identity) return;
      const item = states[kind].find(s => s.target_id === id);
      if (!item) throw new Error('内容或账号当前不可用，请刷新页面');
      const active = !item.active;
      if (await set(kind, id, active, item.collection)) showToast({ bookmark: active ? '已加入账号收藏' : '已取消收藏', like: active ? '已点赞' : '已取消点赞', follow: active ? '已关注，动态将在个人中心更新' : '已取消关注' }[kind], 'success');
    } finally { button.disabled = false; }
  }
  document.addEventListener('chunnel:session', event => {
    if (!adopt(event.detail)) return;
    if (event.detail.authenticated) { paint(); void run(refresh); }
    else document.dispatchEvent(new CustomEvent('chunnel:guest-preferences'));
  });
  document.addEventListener('visibilitychange', () => { if (!document.hidden && session?.authenticated) void run(refresh); });
  window.ChunnelEngagement = {
    get authenticated() { return !!session?.authenticated; }, refresh, paint, toggle, set,
    preferences: () => ({ saved: [], following: states.follow.filter(s => s.active).map(s => String(s.target_id)), likes: states.like.filter(s => s.active).map(s => `post:${s.target_id}`) }),
  };
  void run(refresh);
}
mountEngagement();
