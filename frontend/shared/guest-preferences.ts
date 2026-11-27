import type { ExperienceDependencies, GuestPreferences, SavedEntry } from './experience-types';
import type { ClientSession } from './newpress-client';
import { safeExperienceURL, savedEntry, parseGuestPreferences, experienceError } from './experience-data';
export function mountGuestPreferences(deps: ExperienceDependencies) {
  const {getSession,run,info,announce,showToast,createIcon,setOverlay,dom:{$,$$,optional,node}} = deps;
  const quickActions = optional<HTMLDialogElement>('#quick-actions');
  if (quickActions) {
    let observed = false;
    const syncQuickActions = (session: ClientSession) => {
      $$('[data-quick-member]', quickActions).forEach(el => { el.hidden = !session?.authenticated; });
      $('[data-quick-publish]', quickActions).hidden = !(session?.authenticated && session.can_publish);
    };
    document.addEventListener('chunnel:session', event => { observed = true; syncQuickActions(event.detail); });
    getSession().then(session => { if (!observed) syncQuickActions(session); }).catch(() => {});
    quickActions.addEventListener('click', event => {
      const action = event.target instanceof Element && event.target.closest('.quick-actions-grid > a, .quick-actions-grid > button');
      if (!action) return;
      quickActions.close();
      if (action.hasAttribute('data-quick-search')) {
        const input = optional<HTMLInputElement>('[data-search-input]');
        optional('[data-search-box]')?.classList.add('is-visible', 'is-focused');
        input?.focus({ preventScroll: true });
      }
    }, true);
  }
  let memoryPreferences: GuestPreferences = { saved: [], following: [], following_ids: [], likes: [] };
  let preferenceWriteFailed = false;
  const safeURL = (value: unknown) => safeExperienceURL(value, location.href);
  const entry = (title: string, value: unknown) => savedEntry(title, value, location.href);
  function preferences(): GuestPreferences {
    if (window.ChunnelEngagement?.authenticated) return parseGuestPreferences(window.ChunnelEngagement.preferences(), location.href);
    if (preferenceWriteFailed) return memoryPreferences;
    try { memoryPreferences = parseGuestPreferences(JSON.parse(localStorage.getItem('chunnel.preferences') || 'null'), location.href); } catch { /* Optional storage must not block reading. */ }
    return memoryPreferences;
  }
  function savePreferences(value: GuestPreferences) {
    memoryPreferences = value;
    try { localStorage.setItem('chunnel.preferences', JSON.stringify(value)); preferenceWriteFailed = false; return true; } catch { preferenceWriteFailed = true; return false; }
  }
  function bookmarkFor(button: HTMLElement) {
    const article = button.closest<HTMLElement>('article'), card = article || document;
    const link = optional<HTMLAnchorElement>('a[href^="/posts/"]', card);
    const title = article?.dataset.contentTitle || optional('h1, h2, h3', card)?.textContent?.trim() || button.getAttribute('aria-label')?.replace(/^收藏/, '') || document.title;
    const url = article?.dataset.contentUrl || (article?.classList.contains('article') ? location.pathname : link?.href) || location.pathname + location.search;
    return entry(title, url);
  }
  const favoriteEntries = new Map<HTMLElement, SavedEntry>();
  $$('[data-favorite]').forEach((button) => favoriteEntries.set(button, bookmarkFor(button)));
  $$('[data-toast]').forEach((button) => {
    if (/^已关注/.test(button.dataset.toast || '')) { button.dataset.follow = (button.dataset.toast || '').replace(/^已关注\s*/, '') || '作者'; delete button.dataset.toast; }
    if (button.dataset.toast === '已发送私信邀请') { button.dataset.messageTo = button.closest('article')?.querySelector('h3')?.textContent?.replace(/VIP/g, '').trim() || ''; delete button.dataset.toast; }
  });
  function refreshPreferences() {
	if (window.ChunnelEngagement?.authenticated) { window.ChunnelEngagement.paint(); return; }
    $$('[data-favorite]').forEach((button) => { if (!favoriteEntries.has(button)) favoriteEntries.set(button, bookmarkFor(button)); });
    const prefs = preferences();
    favoriteEntries.forEach((item, button) => {
      const active = prefs.saved.some((saved) => saved.key === item.key);
      button.classList.toggle('is-favorite', active); button.setAttribute('aria-pressed', String(active));
      button.setAttribute('aria-label', `${active ? '取消本机收藏' : '收藏到本机'}：${item.title}`);
      button.title = active ? '已在当前浏览器收藏' : '收藏到当前浏览器';
      optional('use', button)?.setAttribute('href', `#icon-${active ? 'heart-fill' : 'heart'}`);
      const label = optional('span', button); if (label && /收藏/.test(label.textContent || '')) label.textContent = active ? '已收藏' : '收藏';
    });
    $$('[data-follow]').forEach((button) => {
      const active = prefs.following.includes(button.dataset.follow || '');
      button.setAttribute('aria-pressed', String(active)); button.classList.toggle('is-following', active);
      button.title = '关注偏好保存在当前浏览器';
      button.replaceChildren(createIcon(active ? 'check' : 'plus'), document.createTextNode(active ? '已关注' : '关注'));
    });
    $$('[data-reaction]').forEach((button) => {
      const active = prefs.likes.includes(`post:${button.dataset.reaction}`);
      button.setAttribute('aria-pressed', String(active)); button.classList.toggle('is-active', active);
      button.title = '点赞偏好保存在当前浏览器';
      button.replaceChildren(createIcon(active ? 'heart-fill' : 'heart'), node('span', '', active ? '已赞' : '赞'));
    });
    document.dispatchEvent(new CustomEvent('chunnel:preferences'));
  }
  let savedFilter = 'all';
  let removedSaved: { item: SavedEntry; position: number } | null = null;
  const savedKind = (item: SavedEntry) => item.url.startsWith('/posts/') ? 'post' : 'link';
  function renderSaved() {
    const list = $('[data-saved-list]'); list.replaceChildren();
    const saved = preferences().saved;
    const term = $<HTMLInputElement>('[data-saved-query]').value.trim().toLocaleLowerCase();
    const items = saved.filter((item) => (savedFilter === 'all' || savedKind(item) === savedFilter) && item.title.toLocaleLowerCase().includes(term));
    $('[data-saved-count]').textContent = `显示 ${items.length} / ${saved.length} 项收藏`;
    if (!items.length) { const empty = node('div', 'interaction-empty'); empty.append(createIcon('bookmark'), node('b', '', saved.length ? '没有匹配的收藏' : '把喜欢的内容留在这里'), node('p', '', saved.length ? '换一个关键词，或切换到全部内容。' : '点击文章上的收藏按钮，下次继续阅读。')); list.append(empty); return; }
    items.forEach((item, index) => {
      const row = node('div', 'saved-item');
      const link = node('a'); link.href = safeURL(item.url) || location.pathname;
      link.append(node('small', '', { post: '文章 / ARTICLE', link: '链接 / LINK' }[savedKind(item)]), node('b', '', item.title));
      const remove = node('button'); remove.type = 'button'; remove.setAttribute('aria-label', `取消收藏：${item.title}`); remove.append(createIcon('x'));
      remove.addEventListener('click', () => {
        const prefs = preferences(); const position = prefs.saved.findIndex((saved) => saved.key === item.key);
        if (position < 0) { renderSaved(); $<HTMLInputElement>('[data-saved-query]').focus(); return; }
        removedSaved = { item, position }; prefs.saved.splice(position, 1);
        const stored = savePreferences(prefs); renderSaved(); refreshPreferences();
        $('[data-saved-undo]').hidden = false;
        $('[data-saved-undo-label]').textContent = stored ? '已移除一项收藏' : '仅在当前页面移除，浏览器未能保存';
        const buttons = $$('.saved-item > button', list);
        (buttons[Math.min(index, buttons.length - 1)] || $<HTMLInputElement>('[data-saved-query]')).focus({ preventScroll: true });
      });
      row.append(link, remove); list.append(row);
    });
  }
  $<HTMLInputElement>('[data-saved-query]').addEventListener('input', renderSaved);
  $$('[data-saved-filter]').forEach((button) => button.addEventListener('click', () => {
    savedFilter = button.dataset.savedFilter || 'all';
    $$('[data-saved-filter]').forEach((item) => item.setAttribute('aria-pressed', String(item === button)));
    renderSaved();
  }));
  $('[data-saved-restore]').addEventListener('click', () => {
    if (!removedSaved) return;
    const prefs = preferences();
    if (!prefs.saved.some((item) => item.key === removedSaved?.item.key)) {
      if (prefs.saved.length >= 200) { showToast('收藏已满，请先移除一项再撤销', 'warning'); return; }
      prefs.saved.splice(Math.min(removedSaved.position, prefs.saved.length), 0, removedSaved.item);
    }
    const stored = savePreferences(prefs); removedSaved = null;
    $('[data-saved-undo]').hidden = true; renderSaved(); refreshPreferences();
    $<HTMLInputElement>('[data-saved-query]').focus({ preventScroll: true });
    announce(stored ? '收藏已恢复' : '收藏已恢复到当前页面，浏览器未能保存');
  });
  async function copy(text: string) {
    try { if (navigator.clipboard?.writeText) { await navigator.clipboard.writeText(text); return true; } } catch { /* Fall back to a selection-based copy. */ }
    const active = document.activeElement;
    const input = node('textarea'); input.value = text; input.setAttribute('aria-label', '要复制的链接'); input.style.cssText = 'position:fixed;left:0;top:0;opacity:0;width:1px;height:1px;';
    (document.querySelector('dialog[open]:last-of-type') || document.body).append(input); input.focus(); input.select();
    let success = false;
    try { success = document.execCommand('copy'); } catch { /* Explicit fallback below. */ }
    input.remove(); if (active instanceof HTMLElement) active.focus({ preventScroll: true }); return success;
  }
  async function share(button: HTMLElement) {
    const item = bookmarkFor(button);
    const path = safeURL(button.dataset.shareUrl || item.url) || location.pathname;
    const url = new URL(path, location.origin).href;
    const title = item.title;
    if (navigator.share && matchMedia('(pointer: coarse)').matches) {
      try { await navigator.share({ title, url }); return; } catch (error) { if (experienceError(error).name === 'AbortError') return; }
    }
    if (await copy(url)) showToast('分享链接已复制，可以发给朋友了');
    else info('复制分享链接', `浏览器暂不允许自动复制，请手动选择这个链接：${url}`);
  }
  document.addEventListener('click', (event) => {
    const target = event.target instanceof Element ? event.target : null; if (!target) return;
    const favorite = target.closest<HTMLButtonElement>('[data-favorite]');
    if (favorite) run(async () => {
      if ((await getSession()).authenticated) { await window.ChunnelEngagement?.toggle(favorite, 'bookmark'); return; }
      const prefs = preferences(); const item = favoriteEntries.get(favorite) || bookmarkFor(favorite);
      const active = prefs.saved.some((saved) => saved.key === item.key);
      prefs.saved = active ? prefs.saved.filter((saved) => saved.key !== item.key) : [item, ...prefs.saved].slice(0, 200);
      const stored = savePreferences(prefs); refreshPreferences();
      showToast(stored ? active ? '已取消本机收藏' : '已加入本机收藏' : '浏览器禁止保存，收藏仅在当前页面保留', stored ? 'success' : 'warning');
    });
    const follow = target.closest<HTMLButtonElement>('[data-follow]');
    if (follow) run(async () => {
      if ((await getSession()).authenticated) { await window.ChunnelEngagement?.toggle(follow, 'follow'); return; }
      const prefs = preferences(); const key = follow.dataset.follow || ''; const active = prefs.following.includes(key);
      prefs.following = active ? prefs.following.filter((item) => item !== key) : [key, ...prefs.following].slice(0, 200);
      const id = Number(follow.dataset.followUser);
      if (Number.isSafeInteger(id) && id > 0) prefs.following_ids = active ? prefs.following_ids.filter((item) => item !== id) : [...new Set([id, ...prefs.following_ids])].slice(0, 200);
      const stored = savePreferences(prefs); refreshPreferences();
      showToast(stored ? active ? '已取消关注' : '已在当前浏览器关注' : '关注偏好仅在当前页面保留', stored ? 'success' : 'warning');
    });
    const reaction = target.closest<HTMLButtonElement>('[data-reaction]');
    if (reaction) run(async () => { if ((await getSession()).authenticated) { await window.ChunnelEngagement?.toggle(reaction, 'like'); return; } const prefs = preferences(); const key = `post:${reaction.dataset.reaction}`; const active = prefs.likes.includes(key); prefs.likes = active ? prefs.likes.filter((item) => item !== key) : [key, ...prefs.likes].slice(0, 500); savePreferences(prefs); refreshPreferences(); announce(active ? '已取消点赞' : '已赞'); });
    if (target.closest('[data-saved-open]')) run(async () => { if ((await getSession()).authenticated) { location.assign('/account/?view=bookmark'); return; } renderSaved(); setOverlay($('#saved-drawer'), true); });
    if (target.closest('#saved-drawer .saved-item > a')) setOverlay($('#saved-drawer'), false);
    const shareButton = target.closest<HTMLElement>('[data-share]'); if (shareButton) run(() => share(shareButton));
    const discussion = target.closest<HTMLElement>('[data-discussion-url]'); if (discussion) location.assign(safeURL(discussion.dataset.discussionUrl) || '/community/');
    if (target.closest('a[data-account-login], a[data-drawer-open]')) event.preventDefault();
  });
  document.addEventListener('chunnel:overlay', (event) => { if (event.target instanceof Element && event.target.id === 'saved-drawer' && event.detail.open) renderSaved(); });
  window.addEventListener('storage', (event) => { if (event.key === 'chunnel.preferences' || event.key === null) { preferenceWriteFailed = false; refreshPreferences(); if ($<HTMLDialogElement>('#saved-drawer').open) renderSaved(); } });
  $$('a.login-link, .online-card a[href="/admin/"], .login-buttons a[href="/admin/"]').forEach((link) => { link.dataset.accountLogin = ''; });
  refreshPreferences();
  document.addEventListener('chunnel:guest-preferences', refreshPreferences);

  // Content filtering, order and page counts come from the server. Keep the
  // current list and position until its replacement is ready to commit.
  document.addEventListener('change', (event) => {
    const select = event.target instanceof Element ? event.target.closest<HTMLSelectElement>('[data-content-sort]') : null;
    if (select?.form) select.form.requestSubmit();
  });


  return {preferences, refreshPreferences};
}
