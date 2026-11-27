import type {} from './account-types';
import { mountReferrals } from './referrals';
import { parseLibrary, parseProfile, errorMessage } from './account-data';
import type { LibraryItem, AccountProfile } from './account-data';
import { required, input, selection, valueField } from './account-dom';

export function mountAccount(): void {
  const element = document.querySelector<HTMLElement>('[data-account-room]');
  if (!element || !window.ChunnelInteractions || !window.ChunnelEngagement) return;
  const root = element;
  const { api, getSession, run } = window.ChunnelInteractions;
  const engagement = window.ChunnelEngagement;
  const $ = <T extends Element = HTMLElement>(s: string): T => required<T>(root, s);
  const keepScroll = <T>(change: () => T): T => window.ChunnelScroll ? window.ChunnelScroll.keep(change) : change();
  const node = <K extends keyof HTMLElementTagNameMap>(tag: K, cls = '', text = ''): HTMLElementTagNameMap[K] => { const el = document.createElement(tag); el.className = cls; el.textContent = text; return el; };
  const icon = (name: string) => {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'icon'); svg.setAttribute('aria-hidden', 'true'); svg.setAttribute('focusable', 'false');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use'); use.setAttribute('href', '#icon-' + name); svg.append(use); return svg;
  };
  const link = (text: string, href: string, cls = '') => { const el = node('a', cls, text); el.href = href; return el; };
  const button = (text: string, action: () => unknown | Promise<unknown>, cls = '') => { const el = node('button', cls, text); el.type = 'button'; el.addEventListener('click', () => run(async () => { el.disabled = true; try { await action(); } finally { el.disabled = false; } })); return el; };
  const renderedUser = Number(root.dataset.accountUser);
  let accountChanged = false;
  document.addEventListener('chunnel:session', (event) => {
    if (!event.detail?.authenticated || (Number(event.detail.id) || 0) !== renderedUser) {
      accountChanged = true; serial++; root.hidden = true;
      $('[data-library-list]')?.replaceChildren();
      location.reload();
    }
  });
  if (!renderedUser) return;
  mountReferrals();
  const titles: Record<'security' | 'bookmark' | 'feed' | 'follow' | 'like' | 'identity' | 'profile' | 'posts' | 'comments' | 'notifications' | 'wallet' | 'progression' | 'import', [string, string, string, string, string]> = {
    security: ['账号安全', '管理第三方平台登录与绑定', '', '', ''],
    bookmark: ['我的收藏', '收藏的文章与收藏夹', '还没有收藏的文章', '浏览文章时点击收藏，之后可以在这里找到。', 'bookmark'],
    feed: ['关注动态', '你关注的作者最近发布的文章', '暂时没有新的文章', '关注作者后，他们发布的文章会出现在这里。', 'rss'],
    follow: ['关注的人', '你关注的创作者', '还没有关注作者', '在作者主页点击关注，随时查看他们的更新。', 'users'],
    like: ['我赞过的', '你点过赞的文章', '还没有赞过的文章', '为喜欢的文章点赞后，可以在这里回顾。', 'heart'],
    identity: ['勋章与认证', '管理你的社区身份、勋章佩戴与创作者认证', '', '', ''],
    profile: ['个人资料', '管理公开昵称、头像和个人简介', '', '', ''],
    posts: ['我的文章', '查看文章与草稿的发布状态', '还没有自己的文章', '获得作者权限后，可以开始撰写和发布文章。', 'newspaper'],
    comments: ['我的评论', '你发表的评论与审核状态', '还没有发表过评论', '登录后发表的评论会显示在这里。', 'message-square'],
    notifications: ['通知与订阅', '管理站内提醒和邮件订阅', '', '', ''],
    wallet: ['我的钱包', '查看余额、奖励与收支记录', '', '', ''],
    progression: ['成长与会员', '查看经验等级、签到奖励与会员权益', '', '', ''],
    import: ['导入本机记录', '将当前浏览器的记录保存到账号', '', '', ''],
  };
  type AccountView = keyof typeof titles;
  const isView = (value: unknown): value is AccountView => typeof value === 'string' && Object.hasOwn(titles, value) && (value !== 'identity' || !!root.querySelector('[data-identity-panel]'));
  const initial = new URL(location.href).searchParams.get('view');
  let kind: AccountView = isView(initial) ? initial : 'bookmark';
  let page = 1, pages = 1, serial = 0, editing: LibraryItem | null = null;
  const form = $<HTMLFormElement>('[data-library-search]'), status = $('[data-library-status]'), list = $('[data-library-list]');
  const navigation = $('.account-navigation'), navToggle = $('[data-account-nav-toggle]'), rail = $('.account-rail');
  const securityTab = node('button'); securityTab.type = 'button'; securityTab.dataset.libraryTab = 'security'; securityTab.setAttribute('aria-pressed', 'false');
  securityTab.append(icon('user-round'), node('span', '', '账号安全'), icon('chevron-right'));
  navigation.querySelector('[data-library-tab="profile"]')?.before(securityTab);
  let profileBusy = false;
  const statusFilter = node('label', 'account-status-filter'), statusSelect = node('select');
  statusSelect.name = 'status'; statusSelect.setAttribute('aria-label', '内容状态'); statusFilter.append(statusSelect); form.insertBefore(statusFilter, form.querySelector('[type=submit]'));
  const labels: Record<string, string> = { draft: '草稿', publish: '已发布', future: '定时发布', pending: '待审核', private: '私密', trash: '回收站', approved: '已发布' };
  function select() {
    root.dataset.accountView = kind;
    root.querySelectorAll<HTMLElement>('[data-library-tab]').forEach((el) => el.setAttribute('aria-pressed', String(el.dataset.libraryTab === kind)));
    $('[data-library-title]').textContent = titles[kind][0]; $('[data-library-description]').textContent = titles[kind][1];
    $('[data-account-folders]').hidden = kind !== 'bookmark' || !$('[data-collection-list]').children.length;
    form.hidden = ['security', 'profile', 'import', 'notifications', 'wallet', 'progression', 'identity'].includes(kind); $('[data-notification-settings]').hidden = kind !== 'notifications'; $<HTMLFormElement>('[data-profile-form]').hidden = kind !== 'profile';
    $('[data-currency-wallet]').hidden = kind !== 'wallet';
    $('[data-progression-panel]').hidden = kind !== 'progression';
    const identityPanel = root.querySelector<HTMLElement>('[data-identity-panel]'); if (identityPanel) identityPanel.hidden = kind !== 'identity';
    $<HTMLFormElement>('[data-avatar-form]').hidden = kind !== 'profile'; $('[data-account-import]').hidden = kind !== 'import';
    $('.account-collection-filter').hidden = kind !== 'bookmark';
    statusFilter.hidden = !['posts', 'comments'].includes(kind);
    const chosen = statusSelect.value;
    statusSelect.replaceChildren(...['', ...(kind === 'comments' ? ['pending', 'approved'] : ['draft', 'publish', 'future', 'pending', 'private', 'trash'])].map(value => { const option = node('option', '', value ? labels[value] : '全部状态'); option.value = value; return option; })); statusSelect.value = chosen || '';
    const placeholders: Partial<Record<AccountView, string>> = { bookmark: '搜索收藏的文章', feed: '搜索关注的文章', follow: '搜索昵称或用户名', like: '搜索赞过的文章', comments: '搜索我的评论', posts: '搜索我的文章' };
    input(form, 'q').placeholder = placeholders[kind] || '搜索个人内容';
    const url = new URL(location.href); url.searchParams.set('view', kind); history.replaceState(history.state, '', url.pathname + url.search);
    window.ChunnelAccountNav?.();
    revealSelectedTab();
  }
  function revealSelectedTab() {
    if (navToggle.getAttribute('aria-expanded') === 'true') return;
    const active = navigation.querySelector<HTMLElement>('[aria-pressed="true"]');
    if (active && navigation.scrollWidth > navigation.clientWidth) {
      const frame = navigation.getBoundingClientRect(), tab = active.getBoundingClientRect();
      if (tab.left < frame.left) navigation.scrollLeft += tab.left - frame.left - 6;
      else if (tab.right > frame.right) navigation.scrollLeft += tab.right - frame.right + 6;
    }
  }
  function expandNavigation(expanded: boolean) {
    rail.dataset.expanded = String(expanded);
    navToggle.setAttribute('aria-expanded', String(expanded));
    navToggle.setAttribute('aria-label', expanded ? '收起全部栏目' : '展开全部栏目');
    if (!expanded) revealSelectedTab();
  }
  navToggle.addEventListener('click', () => expandNavigation(navToggle.getAttribute('aria-expanded') !== 'true'));
  rail.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape' || navToggle.getAttribute('aria-expanded') !== 'true') return;
    event.preventDefault(); event.stopPropagation(); expandNavigation(false); navToggle.focus({ preventScroll: true });
  });
  window.addEventListener('resize', () => {
    if (window.innerWidth > 760) expandNavigation(false);
    else revealSelectedTab();
  }, { passive: true });
  function paintCollections(collections: Record<string, number>) {
    const navigation = $('[data-collection-list]'), restoreFocus = navigation.contains(document.activeElement);
    const current = selection(form, 'collection').value;
    const folders: [string, string, number][] = [['', '全部收藏', Object.values(collections).reduce((total, count) => total + count, 0)], ...Object.keys(collections).filter(Boolean).sort((a, b) => a.localeCompare(b, 'zh-CN')).map((name): [string, string, number] => [name, name, collections[name] ?? 0])];
    navigation.replaceChildren(...folders.map(([value, label, count]) => {
      const entry = button('', () => { selection(form, 'collection').value = value; page = 1; return keepScroll(() => load()); });
      entry.dataset.collectionName = value; entry.setAttribute('aria-pressed', String(value === current));
      entry.append(node('span', '', label), node('small', '', String(count))); return entry;
    }));
    $('[data-account-folders]').hidden = false;
    if (restoreFocus) navigation.querySelector<HTMLElement>('[aria-pressed="true"]')?.focus({ preventScroll: true });
  }
  async function load(): Promise<void> {
    if (accountChanged) return;
    const request = ++serial;
    const query = new URLSearchParams({ kind, page: String(page), q: valueField(form, 'q').value });
    if (kind === 'bookmark' && selection(form, 'collection').value) query.set('collection', selection(form, 'collection').value);
    const own = ['posts', 'comments'].includes(kind);
    if (own && statusSelect.value) query.set('status', statusSelect.value);
    const utility = ['security', 'profile', 'import', 'notifications', 'wallet', 'progression', 'identity'].includes(kind);
    const retain = !utility && list.children.length > 0;
    keepScroll(() => {
      select();
      if (!retain) { list.replaceChildren(); $('[data-library-pagination]').hidden = true; }
      // Stale rows remain visible to preserve geometry, but cannot be acted on
      // under another tab's label while the new response is still pending.
      list.inert = retain;
      $('[data-library-pagination]').inert = retain;
      list.removeAttribute('aria-busy');
      status.dataset.state = retain ? 'refreshing' : 'loading';
      status.textContent = retain ? '正在加载新内容，下方暂时保留上次结果…' : '正在加载…';
      $('[data-library-total]').textContent = retain ? '正在更新…' : '';
    });
    if (kind === 'profile') { await loadProfile(request); return; }
    if (kind === 'security') { await loadSecurity(request); return; }
    if (kind === 'import') { status.textContent = ''; return; }
    if (kind === 'notifications') { status.textContent = ''; await window.ChunnelNotificationSettings?.load(); return; }
    if (kind === 'wallet') { status.textContent = ''; await window.ChunnelWallet?.load(); return; }
    if (kind === 'identity') { status.textContent = ''; await window.NewPressIdentity?.load(); return; }
    if (kind === 'progression') { status.textContent = ''; await window.ChunnelProgression?.load(); return; }
    list.setAttribute('aria-busy', 'true');
    try {
      const rawData = await api(`/api/v1/social/${own ? 'own-content' : 'library'}?${query}`);
      if (request !== serial || accountChanged) return;
      const data = parseLibrary(rawData, kind);
      return await keepScroll(async () => {
        pages = data.pages;
        if (page > pages) { page = pages; await load(); return; }
        status.textContent = ''; status.dataset.state = 'ready'; $('[data-library-total]').textContent = `共 ${data.total} ${kind === 'follow' ? '人' : kind === 'comments' ? '条' : '篇'}`;
        if (kind === 'bookmark') {
          const selectedCollection = selection(form, 'collection').value;
          const first = node('option', '', '全部收藏夹'); first.value = ''; const options = [first];
          const datalist = $('#account-collection-options'); datalist.replaceChildren();
          Object.keys(data.collections).filter(Boolean).sort((a, b) => a.localeCompare(b, 'zh-CN')).forEach((name) => { const option = node('option', '', `${name} · ${data.collections[name]}`); option.value = name; options.push(option); const suggestion = node('option'); suggestion.value = name; datalist.append(suggestion); });
          selection(form, 'collection').replaceChildren(...options); selection(form, 'collection').value = options.some(option => option.value === selectedCollection) ? selectedCollection : '';
          // Removing or moving the final article in a folder invalidates that filter.
          if (selectedCollection && selection(form, 'collection').value !== selectedCollection) { page = 1; await load(); return; }
          paintCollections(data.collections);
          if (selectedCollection) $('[data-library-description]').textContent = '收藏夹 · ' + selectedCollection;
        }
        if (!data.items.length) {
          const filtered = valueField(form, 'q').value.trim() || kind === 'bookmark' && selection(form, 'collection').value || own && statusSelect.value;
          const canWrite = kind === 'posts' && root.dataset.accountCanWrite === 'true';
          const action = filtered ? button('清除筛选', () => { form.reset(); page = 1; return load(); }, 'ui-button ui-button-secondary ui-button-small') : link(canWrite ? '撰写文章' : '发现文章', canWrite ? '/admin/content/new/' : '/explore/', 'ui-button ui-button-secondary ui-button-small');
          status.dataset.state = 'empty';
          status.append(icon(filtered ? 'search' : titles[kind][4]), node('h3', '', filtered ? '没有找到相关内容' : titles[kind][2]), node('p', '', filtered ? '试试其他关键词，或清除筛选查看全部内容。' : canWrite ? '开始写一篇文章，草稿会保存在这里。' : titles[kind][3]), action);
        }
        const entries = data.items.map(item => kind === 'follow' ? person(item) : own ? ownEntry(item) : post(item));
        list.replaceChildren(...entries);
        list.inert = false;
        $('[data-library-pagination]').inert = false;
        $('[data-library-pagination]').hidden = pages <= 1;
        $('[data-library-page-label]').textContent = `${page} / ${pages}`;
        $<HTMLButtonElement>('[data-library-page="-1"]').disabled = page <= 1; $<HTMLButtonElement>('[data-library-page="1"]').disabled = page >= pages;
      });
    } catch (error) {
      if (request !== serial || error instanceof Error && error.name === 'AbortError') return;
      status.dataset.state = 'error';
      $('[data-library-total]').textContent = retain ? '更新失败，保留上次内容' : '';
      status.replaceChildren(icon('circle-help'), node('p', '', errorMessage(error, '暂时无法加载，请稍后重试。')), button('重新加载', load, 'ui-button ui-button-secondary ui-button-small'));
    } finally { if (request === serial) list.removeAttribute('aria-busy'); }
  }
  async function loadSecurity(request: number) {
    try {
      const data = await api('/api/v1/social/oauth-bindings');
      if (request !== serial || accountChanged) return;
      if (!Array.isArray(data.items) || !Number.isSafeInteger(data.version)) throw new Error('绑定信息格式错误，请刷新重试');
      const entries = data.items.map((item: unknown) => {
        if (!item || typeof item !== 'object') throw new Error('绑定信息格式错误');
        const row = item as Record<string, unknown>;
        if (typeof row.id !== 'string' || !['github', 'google', 'microsoft', 'linuxdo', 'qq', 'wechat'].includes(row.id) || typeof row.name !== 'string' || typeof row.bound !== 'boolean' || typeof row.enabled !== 'boolean') throw new Error('绑定信息格式错误');
        const id = row.id, bound = row.bound;
        const entry = node('article', 'library-entry'), copy = node('div', 'library-entry-copy');
        copy.append(node('h3', '', row.name), node('p', '', bound ? '已绑定' + (row.enabled ? '' : ' · 平台登录已停用') : '未绑定'));
        const action = button(bound ? '解除绑定' : '绑定账号', async () => {
          if (accountChanged || kind !== 'security') return;
          if (bound) {
            if (!window.confirm('确认解除此平台绑定？解绑后将无法通过该平台登录当前账号。')) return;
            await api(`/api/v1/social/oauth-bindings/${id}/remove`, { method: 'POST', body: { version: data.version } });
            if (kind === 'security' && !accountChanged) await load();
          } else {
            const result = await api(`/api/v1/social/oauth-bindings/${id}/start`, { method: 'POST', body: {} });
            if (typeof result.url !== 'string') throw new Error('授权地址无效');
            const target = new URL(result.url);
            if (target.protocol !== 'https:' || target.username || target.password) throw new Error('授权地址无效');
            if (!accountChanged && kind === 'security') location.assign(target.href);
          }
        }, 'ui-button ui-button-secondary ui-button-small');
        action.disabled = !bound && !row.enabled;
        copy.append(action); entry.append(copy); return entry;
      });
      status.textContent = location.hash === '#binding_success' ? '第三方账号绑定成功。' : location.hash === '#binding_error' ? '绑定失败，授权可能已过期或该平台账号已被绑定，请重试。' : entries.length ? '' : '暂无可绑定的第三方平台。';
      if (location.hash.startsWith('#binding_')) history.replaceState(history.state, '', location.pathname + location.search);
      list.replaceChildren(...entries); list.inert = false; status.dataset.state = 'ready';
    } catch (error) {
      if (request !== serial || accountChanged) return;
      status.replaceChildren(node('p', '', errorMessage(error)), button('重新加载', load, 'ui-button ui-button-secondary ui-button-small'));
    }
  }
  function post(item: LibraryItem) {
    const entryKind = kind;
    const el = node('article', 'library-entry'), copy = node('div', 'library-entry-copy');
    copy.append(node('small', '', item.collection || (kind === 'bookmark' ? '默认收藏夹' : kind === 'feed' ? '来自你的关注' : '我赞过的内容')));
    const h = node('h3'); h.append(link(item.title, item.url)); copy.append(h, node('p', '', item.excerpt));
    const footer = node('footer'); footer.append(link(item.author_name, item.author_url), node('time', '', window.ChunnelTime?.formatDate(item.published_at) || ''));
    const actions = node('span', 'library-entry-actions');
    if (kind === 'bookmark') actions.append(button('整理', () => {
      editing = item; $('[data-collection-post]').textContent = item.title;
      valueField($<HTMLFormElement>('[data-collection-form]'), 'collection').value = item.collection || ''; $('[data-collection-error]').textContent = '';
      $<HTMLDialogElement>('[data-collection-dialog]').showModal();
    }));
    if (kind === 'bookmark' || kind === 'like') actions.append(button(kind === 'bookmark' ? '取消收藏' : '取消点赞', async () => { if (entryKind === 'bookmark' || entryKind === 'like') await engagement.set(entryKind, item.id, false); await load(); }));
    if (actions.children.length) footer.append(actions);
    if (item.cover_url && item.cover_url.startsWith('/') && !item.cover_url.startsWith('//')) { const cover = link('', item.url, 'library-entry-cover'), img = node('img'); cover.setAttribute('aria-label', '阅读：' + item.title); img.src = item.cover_url; img.alt = ''; img.width = 136; img.height = 106; img.loading = 'lazy'; cover.append(img); el.append(cover); }
    copy.append(footer); el.append(copy);
    return el;
  }
  function person(item: LibraryItem) {
    const el = node('article', 'account-person'), copy = node('div');
    const name = node('b'); name.append(link(item.name, `/authors/${item.id}/`)); copy.append(name, node('small', '', '@' + item.username));
    el.append(node('span', 'account-person-avatar', [...item.name][0] || 'N'), copy, button('取消关注', async () => { await engagement.set('follow', item.id, false); await load(); }, 'ui-button ui-button-secondary ui-button-small'));
    return el;
  }
  function ownEntry(item: LibraryItem) {
    const el = node('article', 'library-entry'), copy = node('div', 'library-entry-copy');
    copy.append(node('small', 'account-content-state', labels[item.status] || item.status));
    const heading = node('h3'); heading.append(item.url ? link(item.title, item.url) : node('span', '', item.title));
    const body = node('p', kind === 'comments' ? 'account-comment-text' : '', kind === 'comments' ? item.content : item.excerpt);
    const footer = node('footer'); footer.append(node('time', '', window.ChunnelTime?.formatDateTime(item.created_at || item.updated_at) || ''));
    if (item.edit_url) footer.append(link('进入编辑器 →', item.edit_url));
    copy.append(heading, body, footer); el.append(copy); return el;
  }
  function paintAvatar(data: AccountProfile) {
    $('[data-profile-heading]').textContent = data.display_name;
    $('[data-profile-bio]').textContent = data.bio || '还没有填写个人简介';
    const img = $<HTMLImageElement>('[data-avatar-preview]'), monogram = $('[data-profile-monogram]');
    const valid = typeof data.avatar_url === 'string' && data.avatar_url.startsWith('/media/');
    img.hidden = !valid; $('[data-avatar-fallback]').hidden = valid;
    if (valid) { img.src = data.avatar_url; const thumb = node('img'); thumb.src = data.avatar_url; thumb.alt = ''; monogram.replaceChildren(thumb); }
    else { img.removeAttribute('src'); monogram.textContent = [...data.display_name][0] || 'N'; }
    input($<HTMLFormElement>('[data-profile-form]'), 'remove_avatar').checked = false;
    input($<HTMLFormElement>('[data-profile-form]'), 'remove_avatar').disabled = !valid;
  }
  function profileLock(locked: boolean) {
    profileBusy = locked;
    for (const selector of ['[data-profile-form] [type=submit]', '[data-avatar-form] [type=submit]', '[data-profile-refresh]']) $<HTMLButtonElement>(selector).disabled = locked;
  }
  async function loadProfile(request = ++serial) {
    if (profileBusy) return;
    const profile = $<HTMLFormElement>('[data-profile-form]'); profileLock(true);
    try {
      const rawData = await api('/api/v1/social/profile'); if (request !== serial || accountChanged) return; const data = parseProfile(rawData, renderedUser);
      for (const key of ['username', 'display_name', 'locale', 'version', 'bio'] as const) valueField(profile, key).value = String(data[key]);
      paintAvatar(data); status.textContent = ''; $('[data-profile-result]').textContent = '';
    } catch (error) { if (request === serial) status.textContent = errorMessage(error); }
    finally { profileLock(false); if (!valueField(profile, 'version').value) { required<HTMLButtonElement>(profile, '[type=submit]').disabled = true; $<HTMLButtonElement>('[data-avatar-form] [type=submit]').disabled = true; } if (kind === 'profile' && request !== serial) run(load); }
  }
  root.querySelectorAll<HTMLElement>('[data-library-tab], [data-account-open]').forEach((el) => el.addEventListener('click', () => {
    const next = el.dataset.libraryTab || el.dataset.accountOpen;
    expandNavigation(false);
    if (!isView(next) || next === kind) return;
    keepScroll(() => { kind = next; page = 1; form.reset(); run(load); });
  }));
  form.addEventListener('submit', (event) => { event.preventDefault(); keepScroll(() => { page = 1; run(load); }); });
  selection(form, 'collection').addEventListener('change', () => keepScroll(() => { page = 1; run(load); }));
  statusSelect.addEventListener('change', () => keepScroll(() => { page = 1; run(load); }));
  root.querySelectorAll<HTMLElement>('[data-library-page]').forEach((el) => el.addEventListener('click', () => keepScroll(() => { page = Math.max(1, Math.min(pages, page + Number(el.dataset.libraryPage))); run(load); })));
  $('[data-collection-cancel]').addEventListener('click', () => $<HTMLDialogElement>('[data-collection-dialog]').close());
  $<HTMLFormElement>('[data-collection-form]').addEventListener('submit', (event) => {
    event.preventDefault(); const collection = $<HTMLFormElement>('[data-collection-form]'), submit = required<HTMLButtonElement>(collection, '[type=submit]');
    if (submit.disabled || !editing || accountChanged) return; const item = editing; submit.disabled = true;
    run(async () => {
      try { await engagement.set('bookmark', item.id, true, input(collection, 'collection').value.trim()); $<HTMLDialogElement>('[data-collection-dialog]').close(); await load(); }
      catch (error) { $('[data-collection-error]').textContent = errorMessage(error); }
      finally { submit.disabled = false; }
    });
  });
  $('[data-profile-refresh]').addEventListener('click', () => run(() => loadProfile()));
  $<HTMLFormElement>('[data-profile-form]').addEventListener('submit', (event) => {
    event.preventDefault(); const profile = $<HTMLFormElement>('[data-profile-form]'), submit = required<HTMLButtonElement>(profile, '[type=submit]');
    if (submit.disabled || profileBusy || accountChanged) return; profileLock(true);
    run(async () => {
      try {
        const rawData = await api('/api/v1/social/profile', { method: 'POST', body: { display_name: valueField(profile, 'display_name').value, locale: valueField(profile, 'locale').value, bio: valueField(profile, 'bio').value, remove_avatar: input(profile, 'remove_avatar').checked, version: Number(valueField(profile, 'version').value) } });
        if (accountChanged) return; const data = parseProfile(rawData, renderedUser);
        valueField(profile, 'version').value = String(data.version); $('[data-profile-heading]').textContent = data.display_name;
        paintAvatar(data);
        $('[data-profile-result]').textContent = '资料已保存。'; await getSession(true);
      } catch (error) { $('[data-profile-result]').textContent = errorMessage(error); }
      finally { profileLock(false); }
    });
  });
  $<HTMLFormElement>('[data-avatar-form]').addEventListener('submit', (event) => {
    event.preventDefault(); const upload = $<HTMLFormElement>('[data-avatar-form]'), profile = $<HTMLFormElement>('[data-profile-form]'), file = input(upload, 'file').files?.[0];
    if (accountChanged || profileBusy || !file || !valueField(profile, 'version').value) return;
    if (file.size > 2 * 1024 * 1024) { $('[data-avatar-result]').textContent = '头像不能超过 2 MiB。'; return; }
    profileLock(true); $('[data-avatar-result]').textContent = '正在处理头像…';
    run(async () => {
      try {
        const body = new FormData(); body.set('file', file); body.set('version', valueField(profile, 'version').value);
        const rawData = await api('/api/v1/social/profile/avatar', { method: 'POST', body });
        if (accountChanged) return; const data = parseProfile(rawData, renderedUser);
        valueField(profile, 'version').value = String(data.version); paintAvatar(data); upload.reset(); $('[data-avatar-result]').textContent = '头像已更新。';
      } catch (error) { $('[data-avatar-result]').textContent = errorMessage(error); }
      finally { profileLock(false); }
    });
  });
  getSession().then(current => { if (!current.authenticated || current.id !== renderedUser) { accountChanged = true; serial++; root.hidden = true; location.reload(); return; } return load(); }).catch((error) => { status.textContent = errorMessage(error); });
}
mountAccount();
