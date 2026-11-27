import type { GuestRecords } from './account-types';
import { errorMessage, parseImport } from './account-data';
import { required } from './account-dom';

export function normalize(raw: unknown, origin: string): GuestRecords {
  const value = raw && typeof raw === 'object' && !Array.isArray(raw) ? raw as Record<string, unknown> : {};
  const saved: unknown[] = Array.isArray(value.saved) ? value.saved : [], likes: unknown[] = Array.isArray(value.likes) ? value.likes : [], following: unknown[] = Array.isArray(value.following_ids) ? value.following_ids : [];
  const bookmarks: string[] = [], validLikes: number[] = [], validFollowing: number[] = [];
  let skipped = Math.max(0, saved.length - 200) + Math.max(0, likes.length - 500) + Math.max(0, following.length - 200);
  for (const item of saved.slice(0, 200)) {
    try {
      if (!item || typeof item !== 'object' || !('url' in item) || typeof item.url !== 'string') throw new Error();
      const url = new URL(item.url, origin);
      if (url.origin !== origin || url.username || url.password || !/^\/posts\/[^/]+\/?$/.test(url.pathname) || url.pathname.length > 1024) throw new Error();
      bookmarks.push(url.pathname.replace(/\/$/, ''));
    } catch { skipped++; }
  }
  for (const item of likes.slice(0, 500)) {
    const match = typeof item === 'string' && /^(?:.*:)?([1-9][0-9]*)$/.exec(item), id = match ? Number(match[1]) : 0;
    if (Number.isSafeInteger(id) && id > 0) validLikes.push(id); else skipped++;
  }
  for (const id of following.slice(0, 200)) { if (typeof id === 'number' && Number.isSafeInteger(id) && id > 0) validFollowing.push(id); else skipped++; }
  const legacy = Math.max(0, (Array.isArray(value.following) ? value.following.length : 0) - validFollowing.length);
  return { bookmarks: [...new Set(bookmarks)], likes: [...new Set(validLikes)], following: [...new Set(validFollowing)], skipped: skipped + legacy };
}

export function mountGuestImport(): void {
  window.ChunnelGuestImport = { normalize };
  const root = document.querySelector<HTMLElement>('[data-account-import]'), interactions = window.ChunnelInteractions;
  if (!root || !interactions) return;
  const { api, getSession } = interactions;
  const check = required<HTMLButtonElement>(root, '[data-import-preview]'), confirm = required<HTMLButtonElement>(root, '[data-import-confirm]'), result = required(root, '[data-import-result]');
  const accountID = Number(root.dataset.importAccount);
  let pending: GuestRecords | null = null, busy = false, sequence = 0;
  function reset(): void { sequence++; pending = null; confirm.hidden = true; }
  function lock(value: boolean): void { busy = value; check.disabled = confirm.disabled = value; }
  async function perform(preview: boolean): Promise<void> {
    if (busy) return;
    lock(true); const request = ++sequence;
    try {
      const session = await getSession(true);
      if (request !== sequence) return;
      if (!session.authenticated || session.id !== accountID) throw new Error('登录账号已变化，请刷新后重新检查。');
      if (preview) {
        pending = null; confirm.hidden = true;
        let raw: unknown;
        try { raw = JSON.parse(localStorage.getItem('chunnel.preferences') || '{}'); } catch { throw new Error('无法读取本机记录，请检查浏览器存储权限。'); }
        // Only a confirmed preview may become an importable snapshot.
        const candidate = normalize(raw, location.origin);
        result.textContent = '正在检查本机记录…';
        const rawData = await api('/api/v1/social/import', { method: 'POST', body: { account_id: accountID, bookmarks: candidate.bookmarks, likes: candidate.likes, following: candidate.following, preview: true } });
        if (request !== sequence) return;
        const data = parseImport(rawData, true);
        pending = candidate;
        result.textContent = `可新增 ${data.added} 项，账号已有 ${data.existing} 项，跳过 ${candidate.skipped + data.skipped} 项。确认后导入到当前账号。`;
        confirm.hidden = data.added === 0;
      } else {
        if (!pending || confirm.hidden) return;
        const candidate = pending;
        result.textContent = '正在导入…';
        const rawData = await api('/api/v1/social/import', { method: 'POST', body: { account_id: accountID, bookmarks: candidate.bookmarks, likes: candidate.likes, following: candidate.following, preview: false } });
        if (request !== sequence) return;
        const data = parseImport(rawData, false);
        result.textContent = `已新增 ${data.added} 项，账号已有 ${data.existing} 项，跳过 ${candidate.skipped + data.skipped} 项。本机记录已保留，你现在可以在收藏和关注中查看。`;
        confirm.hidden = true; pending = null;
        await window.ChunnelEngagement?.refresh();
      }
    } catch (error) {
      if (request === sequence && !(error instanceof Error && error.name === 'AbortError')) result.textContent = errorMessage(error, '导入未完成，请重试。');
    } finally { lock(false); }
  }
  check.addEventListener('click', () => { void perform(true); });
  confirm.addEventListener('click', () => { void perform(false); });
  document.addEventListener('chunnel:session', event => { if (!event.detail?.authenticated || event.detail.id !== accountID) { reset(); result.textContent = ''; } });
  window.addEventListener('storage', event => { if (!event.key || event.key === 'chunnel.preferences') { reset(); result.textContent = '本机记录有变化，请重新检查。'; } });
}
mountGuestImport();
