import type {} from './account-types';
import type { RegistrationController } from './account-types';
import type { ClientSession, RequestOptions } from './newpress-client';
import type { Action, Contact, Conversation, MessageThread, NoticeBoundary, SocialState, SocialStorage, ThreadMode } from './interaction-types';
import { required, input } from './account-dom';
import { confirmed } from './account-data';
import { parseContacts, parseDraft, interactionFailure } from './interaction-data';
import { createSocialSession, createSocialLogout } from './interaction-session';
import { createMessageSync } from './interaction-messages';
import { createNoticeCenter } from './interaction-notices';
import { createInbox } from './interaction-inbox';

export function mountInteractions(): void {
  if (!window.Chunnel || !window.ChunnelEmoji || !window.NewPressClient) return;
  const { showToast, createIcon, setOverlay } = window.Chunnel;
  const emoji = window.ChunnelEmoji;
  const $ = <T extends Element = HTMLElement>(selector: string, root: ParentNode = document): T => required<T>(root, selector);
  const optional = <T extends Element = HTMLElement>(selector: string, root: ParentNode = document): T | null => root.querySelector<T>(selector);
  const $$ = <T extends Element = HTMLElement>(selector: string, root: ParentNode = document): T[] => [...root.querySelectorAll<T>(selector)];
  const messageDrawer = optional<HTMLDialogElement>('#message-drawer');
  const notificationDrawer = $<HTMLDialogElement>('#notification-drawer');
  const loginModal = $<HTMLDialogElement>('#account-login-modal');
  const registerModal = $<HTMLDialogElement>('#account-register-modal');
  let registrationLoad: Promise<RegistrationController> | null = null, registrationAssets: Promise<void[]> | null = null, accountSwitching = false;
  const threadPanel = optional('[data-message-thread]');
  const mobile = matchMedia('(max-width: 680px)');
  const state: SocialState = {
    session: null, epoch: 0, inbox: new Map(), inboxCursor: 0, inboxMore: false,
    peer: 0, threads: new Map(), notices: [], noticeFilter: 'all', noticeCursor: '',
    noticeMore: false, noticeThrough: null, noticeUnread: 0, messageUnread: 0,
  };
  const client = window.NewPressClient.create({
    fetch: (...args) => fetch(...args), session: () => state.session, epoch: () => state.epoch,
    onUnauthorized() { applySession({ authenticated: false, configured: true }); announce('登录已过期，请重新登录。'); },
  });
  let syncRequest: Promise<void> | null = null;
  let pollTimer: ReturnType<typeof setTimeout> | undefined;
  let failures = 0;
  let pendingLoginAction: Action | null = null;
  let contactRequest = 0;
  let contactTimer: ReturnType<typeof setTimeout> | undefined;
  let contactPage = 0;
  let contactQuery = '';
  let contactComposing = false;
  let contactLoading = false;
  let channel: BroadcastChannel | undefined;
  try { channel = new BroadcastChannel('chunnel.social'); } catch { /* Polling also synchronizes tabs. */ }

  function node<K extends keyof HTMLElementTagNameMap>(tag: K, className = '', text = ''): HTMLElementTagNameMap[K] {
    const el = document.createElement(tag);
    if (className) el.className = className;
    if (text) el.textContent = text;
    return el;
  }
  function button(text: string, className: string, action?: Action | null, icon?: string) {
    const el = node('button', className, text);
    el.type = 'button';
    if (icon) el.prepend(createIcon(icon));
    if (action) el.addEventListener('click', () => run(action));
    return el;
  }
  function announce(text: string) { $('[data-interaction-status]').textContent = text; }
  function run(action: Action): Promise<unknown> { return Promise.resolve().then(action).catch((failure) => { if (interactionFailure(failure).name !== 'AbortError') showToast(interactionFailure(failure).message || '操作未完成，请重试', 'error'); }); }
  const storage: SocialStorage = {
    get(key, fallback) { try { return JSON.parse(sessionStorage.getItem(key) || 'null') ?? fallback; } catch { return fallback; } },
    set(key, value) { try { sessionStorage.setItem(key, JSON.stringify(value)); } catch { /* The current input remains in memory. */ } },
    remove(key) { try { sessionStorage.removeItem(key); } catch { /* Storage can be disabled. */ } },
  };
  // Labels use the site's configured zone, not the visitor's browser zone.
  const dateLabel = (at: string) => window.ChunnelTime?.relative(at) || '';
  const fullDate = (at: string) => window.ChunnelTime?.formatDateTime(at) || '';
  const nonce = () => crypto.randomUUID ? crypto.randomUUID() : [...crypto.getRandomValues(new Uint8Array(20))].map((v) => v.toString(16).padStart(2, '0')).join('');

  async function api(path: string, options?: RequestOptions) { return client.request(path, options); }


  const getSession = createSocialSession(state, api, applySession);
  const inbox = createInbox({ state, api, render: renderInbox, badges });
  const { loadInbox } = inbox;
  const logout = createSocialLogout({ state, api, getSession, apply: applySession,
    clearDrafts(user) {
      try { Object.keys(sessionStorage).filter(key => key.startsWith(`chunnel.private.${user}.`) || key === `chunnel.community.${user}`).forEach(key => sessionStorage.removeItem(key)); } catch { /* Storage can be disabled. */ }
    },
    completed() {
      $<HTMLDetailsElement>('[data-account-menu]').open = false;
      channel?.postMessage({ type: 'session' }); announce('已退出登录'); showToast('已安全退出登录');
    },
  });
  const dom = { $, $$, optional, node, button, empty, dateLabel, fullDate, createIcon };
  const notices = createNoticeCenter({ state, api, dom, notificationDrawer, badges, announce, openPeer, setOverlay, channel });
  const { loadNotices, updateNotices } = notices;
  const { loadThread, markRead, sendMessage } = createMessageSync({ state, api, dom, activeThread, renderThread, saveThread, announce, loadInbox, loadNotices, run, connection, showToast, signIn, openPeer, nonce, atBottom, mobile, messageDrawer, channel });

  function empty(container: HTMLElement, title: string, description: string, action?: Action | null, label = '重试', icon = 'message-square') {
    delete container.dataset.signature;
    const box = node('div', 'interaction-empty');
    box.append(createIcon(icon), node('b', '', title), node('p', '', description));
    if (action) box.append(button(label, 'ui-button ui-button-primary ui-button-small', action));
    container.replaceChildren(box);
  }
  function loading(container: HTMLElement, text = '正在加载…') {
    delete container.dataset.signature;
    const box = node('div', 'interaction-loading', text);
    box.setAttribute('role', 'status'); box.prepend(createIcon('refresh-cw'));
    container.replaceChildren(box);
  }
  function info(title: string, description: string) {
    $('#interaction-info-title').textContent = title;
    $('[data-info-description]').textContent = description;
    setOverlay($('#interaction-info'), true);
  }
  function avatar(contact: Contact) {
    const el = node('span', `avatar conversation-avatar avatar-${['cyan', 'violet', 'orange', 'gold'][Math.abs(contact.id) % 4]}`, ([...(contact.name || '?')][0] || '?').toUpperCase());
    el.setAttribute('aria-hidden', 'true'); return el;
  }
  function badges() {
    const messageCount = state.session?.authenticated ? state.messageUnread : 0;
    const noticeCount = state.session?.authenticated ? state.noticeUnread : 0;
    $$('[data-message-unread-count]').forEach((el) => { el.hidden = !messageCount; el.textContent = messageCount > 99 ? '99+' : String(messageCount); });
    $$('[data-unread-count]').forEach((el) => { el.hidden = !noticeCount; el.textContent = noticeCount > 99 ? '99+' : String(noticeCount); });
    $$('[data-unread-dot]').forEach((el) => { el.hidden = !noticeCount; });
    $('[data-menu-unread]').hidden = !(messageCount || noticeCount);
    $$('[data-drawer-open="message-drawer"]').forEach((el) => el.setAttribute('aria-label', messageCount ? `打开私信，${messageCount} 条未读` : '打开私信'));
    $$('[data-drawer-open="notification-drawer"]').forEach((el) => el.setAttribute('aria-label', noticeCount ? `打开通知，${noticeCount} 条未读` : '打开通知'));
    const messageSummary = optional('[data-message-summary]');
    if (messageSummary) messageSummary.textContent = messageCount ? `${messageCount} 条未读` : '';
    $('[data-notification-summary]').textContent = noticeCount ? `${noticeCount} 条未读` : '';
    $<HTMLButtonElement>('[data-notification-read-all]').disabled = !noticeCount || !state.noticeThrough;
    $<HTMLButtonElement>('[data-notification-clear]').disabled = !state.noticeThrough;
  }
  function connection(message = '') {
    $$('[data-social-connection]').forEach((el) => {
      el.hidden = !message;
      if (el.dataset.message === message) return;
      el.dataset.message = message;
      el.replaceChildren(node('span', '', message));
      if (message) el.append(button('重试连接', 'text-button', () => sync()));
    });
  }
  function applySession(session: ClientSession) {
    const changed = (state.session?.id || 0) !== (session.id || 0) || state.session?.authenticated !== session.authenticated;
    if (changed) {
      saveActiveDraft();
      state.epoch++;
      client.cancel();
      state.inbox.clear(); state.threads.clear(); state.peer = 0;
      state.notices = []; state.noticeThrough = null; state.messageUnread = 0; state.noticeUnread = 0;
      state.inboxCursor = 0; state.inboxMore = false;
      notices.reset(); inbox.reset(); clearThrough = null; closeContactSearch();
      contactPage = 0; contactQuery = '';
      if (messageDrawer && threadPanel) {
        $('[data-contact-results]').replaceChildren();
        $<HTMLInputElement>('[data-contact-query]').value = '';
        $('[data-contact-search]').hidden = true;
        $<HTMLButtonElement>('[data-conversation-more]').hidden = true;
        messageDrawer.classList.remove('has-thread');
        empty(threadPanel, '选择一个会话', '和创作者保持联系，讨论下一步想法。');
        loading($('[data-conversation-list]'));
      }
      $<HTMLButtonElement>('[data-notification-more]').hidden = true;
      loading($('[data-notification-list]'));
    }
    state.session = session;
    $$('[data-home-guest]').forEach(el => { el.hidden = session.authenticated === true; });
    $$('[data-home-member]').forEach(el => { el.hidden = session.authenticated !== true; });
    $$('[data-home-name]').forEach(el => { el.textContent = session.authenticated === true ? session.name || '' : ''; });
    // Notify editors when login, logout, or session expiry changes their owner.
    document.dispatchEvent(new CustomEvent('chunnel:session', { detail: { ...session } }));
    $$('[data-account-login], [data-account-register]').forEach((el) => { el.hidden = !!session.authenticated; });
    $<HTMLDetailsElement>('[data-account-menu]').hidden = !session.authenticated;
    $('[data-account-name]').textContent = session.name || '';
    $('[data-account-avatar]').textContent = [...(session.name || '')][0] || '';
    $('[data-account-admin]').hidden = !(session.authenticated === true && session.can_admin === true);
    $$('[data-account-publish]').forEach(el => { el.hidden = !(session.authenticated === true && session.can_publish === true); });
    badges();
    if (!session.authenticated) {
      clearTimeout(pollTimer);
      guest($('[data-conversation-list]'), '登录后开始对话');
      guest($('[data-notification-list]'), '登录后查看通知');
    }
  }
  function guest(container: HTMLElement, title: string) {
    empty(container, title, state.session?.configured === false ? '站点暂未开放账号登录，请联系管理员。' : '使用站点账号登录，消息和未读状态会保存在你的账号中。', () => signIn(), '登录', 'lock-keyhole');
  }
  async function accountDialog(target: HTMLDialogElement) {
    if (accountSwitching) return false;
    accountSwitching = true;
    try {
      const previous = [loginModal, registerModal].find(dialog => dialog && dialog !== target && dialog.open);
      if (previous) {
        await new Promise<void>(resolve => { previous.addEventListener('close', () => resolve(), { once: true }); setOverlay(previous, false); });
      }
      setOverlay(target, true);
      return true;
    } finally { accountSwitching = false; }
  }
  function accountCanLeave() {
    return $$('[data-account-access]').every(root => !root.chunnelRegistration || root.chunnelRegistration.canLeave());
  }
  async function loadRegistration(): Promise<RegistrationController> {
    const panel = $(':scope > section', registerModal);
    if (panel.chunnelRegistration) return panel.chunnelRegistration;
    if (registrationLoad) return registrationLoad;
    registrationLoad = (async () => {
      $('[data-registration-loading-status]', registerModal).textContent = '正在准备注册表单…';
      $('[data-registration-reload]', registerModal).hidden = true;
      const loadAsset = (url: string | undefined, style: boolean, ready = !!window.ChunnelRegistration): Promise<void> => new Promise((resolve, reject) => {
        if (!url) { reject(new Error('注册资源地址缺失')); return; }
        if (style && $$<HTMLLinkElement>('link[rel="stylesheet"]').some(link => link.sheet && new URL(link.href).pathname === new URL(url, location.origin).pathname) || !style && ready) { resolve(); return; }
        const asset = document.createElement(style ? 'link' : 'script');
        const done = (error?: Error) => { clearTimeout(timer); asset.onload = asset.onerror = null; if (error) { asset.remove(); reject(error); } else resolve(); };
        const timer = setTimeout(() => done(new Error('注册资源加载超时')), 15000);
        if (asset instanceof HTMLLinkElement) { asset.rel = 'stylesheet'; asset.href = url; } else if (asset instanceof HTMLScriptElement) asset.src = url;
        asset.onload = () => done(); asset.onerror = () => done(new Error('注册资源加载失败'));
        document.head.append(asset);
      });
      registrationAssets ||= Promise.all([loadAsset(registerModal.dataset.registrationStyle, true), loadAsset(registerModal.dataset.registrationScript, false)]).catch(failure => { registrationAssets = null; throw failure; });
      // The homepage fetches the registration form on demand. Its native fields
      // remain usable if the optional dropdown enhancement cannot be loaded.
      const {choiceStyle, choiceScript} = registerModal.dataset;
      if (choiceStyle && !optional('link[href*="/chunnel-selects.css"]')) loadAsset(choiceStyle, true).catch(() => {});
      if (choiceScript && !optional('script[src*="/chunnel-selects.js"]')) loadAsset(choiceScript, false, !!window.NewPressSelects).catch(() => {});
      const request = new AbortController(), timeout = setTimeout(() => request.abort(), 15000);
      let html;
      try {
        const [response] = await Promise.all([fetch('/account/access/', { credentials: 'same-origin', signal: request.signal }), registrationAssets]);
        if (!response.ok) throw new Error('注册表单暂不可用');
        html = await response.text();
      } finally { clearTimeout(timeout); }
      const card = new DOMParser().parseFromString(html, 'text/html').querySelector('[data-account-access] .access-card');
      if (!card?.querySelector('[data-access-register]') || !window.ChunnelRegistration) throw new Error('注册表单加载失败');
      const close = $('.modal-close', panel);
      panel.replaceChildren(close, ...[...card.childNodes].map(child => document.importNode(child, true)));
      panel.setAttribute('data-account-access', ''); panel.setAttribute('data-access-modal', '');
      const heading = $('[data-access-heading]', panel); heading.id = 'account-register-title'; heading.tabIndex = -1;
      const keepReading = node('button', 'ui-button ui-button-outline', '继续浏览'); keepReading.type = 'button'; keepReading.setAttribute('data-modal-close', '');
      $('[data-access-success]', panel).append(keepReading);
      const mounted = window.ChunnelRegistration.mount(panel); if (!mounted) throw new Error('注册表单无法挂载'); return mounted;
    })().catch(failure => {
      const status = optional('[data-registration-loading-status]', registerModal);
      if (status) { status.textContent = '加载失败，请重试或打开注册页面。'; $('[data-registration-reload]', registerModal).hidden = false; }
      throw failure;
    }).finally(() => { registrationLoad = null; });
    return registrationLoad;
  }
  async function showRegistration(flow = 'register') {
    if (!registerModal || accountSwitching) return;
    if ($<HTMLFormElement>('[data-social-login]').getAttribute('aria-busy') === 'true') { showToast('请等待登录完成'); return; }
    if (!await accountDialog(registerModal)) return;
    const controller = await loadRegistration();
    await controller.open(flow);
    if (registerModal.open) $('[data-access-heading]', registerModal)?.focus({ preventScroll: true });
  }
  async function signIn(action?: Action): Promise<unknown> {
    if (accountSwitching) return;
    if (!accountCanLeave()) {
      if (optional('[data-account-access]', registerModal)) await accountDialog(registerModal);
      showToast('请先等待处理完成，或重试确认上次提交的结果'); return;
    }
    const session = await getSession();
    if (session.authenticated) { if (action) return action(); return openMessages(); }
    if (session.configured === false) { info('暂未开放登录', '站点管理员尚未配置账号。配置完成后，即可收藏文章、关注作者和查看通知。'); return; }
    if (action) pendingLoginAction = action;
    $('[data-login-error]').hidden = true;
    await accountDialog(loginModal);
  }
  $<HTMLFormElement>('[data-social-login]').addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = $<HTMLFormElement>('[data-social-login]');
    const submit = $<HTMLButtonElement>('button[type="submit"]', form);
    if (submit.disabled || !form.reportValidity()) return;
    const credentials = { username: input(form, 'username').value.trim(), password: input(form, 'password').value };
    submit.disabled = true; submit.dataset.submitState = 'pending'; form.setAttribute('aria-busy', 'true');
    const errorBox = $('[data-login-error]'); errorBox.hidden = true;
    try {
      const verificationToken = window.ChunnelVerification ? await window.ChunnelVerification.getToken(form, 'login') : '';
      confirmed(await api('/api/v1/social/login', { method: 'POST', anonymous: true, body: { ...credentials, ...(verificationToken ? { verification_token: verificationToken } : {}) } }));
      await getSession(true);
      if (!state.session?.authenticated) throw new Error('登录状态未建立，请检查浏览器 Cookie 设置');
      input(form, 'password').value = '';
      input(form, 'password').type = 'password';
      const action = pendingLoginAction;
      pendingLoginAction = null;
      loginModal.addEventListener('close', () => run(action || (() => sync())), { once: true });
      setOverlay(loginModal, false);
      channel?.postMessage({ type: 'session' });
      announce(`已登录，${state.session.name}`);
      sync();
    } catch (failure) { errorBox.textContent = interactionFailure(failure).message; errorBox.hidden = false; }
    finally { submit.disabled = false; submit.dataset.submitState = 'idle'; form.removeAttribute('aria-busy'); }
  });
  loginModal.addEventListener('close', () => { window.ChunnelVerification?.cancel($<HTMLFormElement>('[data-social-login]')); input($<HTMLFormElement>('[data-social-login]'), 'password').value = ''; if (!accountSwitching) pendingLoginAction = null; });
  registerModal?.addEventListener('close', () => { if (!accountSwitching) pendingLoginAction = null; });
  document.addEventListener('chunnel:account-authenticated', event => {
    event.preventDefault();
    run(async () => {
      await getSession(true);
      // If another refresh superseded this one, wait for the current read.
      const session = await getSession();
      if (!session.authenticated) return;
      const action = pendingLoginAction; pendingLoginAction = null;
      if (action && registerModal?.open && registerModal.contains(event.detail.root)) {
        registerModal.addEventListener('close', () => run(action), { once: true }); setOverlay(registerModal, false);
      }
      channel?.postMessage({ type: 'session' }); sync();
    });
  });

  function draftKey(peer: number, id = state.session?.id) { return `chunnel.private.${id}.${peer}`; }
  function thread(peer: number, contact?: Contact): MessageThread {
    const existing = state.threads.get(peer); if (existing) { if (contact) existing.contact = contact; return existing; }
    const owner = state.session?.id; if (!owner) throw new Error('请登录后继续');
    const saved = parseDraft(storage.get(draftKey(peer), null));
    const current: MessageThread = { peer, owner, contact: contact || {id:peer,name:'联系人',username:'',available:true}, messages:new Map(), loaded:false,hasMore:false,loading:false,readThrough:0,readSent:0,draft:saved.draft,outbox:saved.outbox,syncedThrough:0 };
    state.threads.set(peer,current); return current;
  }
  function saveThread(t: MessageThread) {
    storage.set(draftKey(t.peer, t.owner), { draft: t.draft, outbox: t.outbox.map(({ nonce, content, created_at }) => ({ nonce, content, created_at })), at: Date.now() });
  }
  function saveActiveDraft() {
    const t = state.threads.get(state.peer);
    const input = optional<HTMLTextAreaElement>('[data-message-input]');
    if (t && input && Number(input.dataset.peer) === t.peer) { t.draft = input.value; saveThread(t); }
  }
  function inboxRow(conversation: Conversation) {
    const contact = conversation.contact;
    const row = button('', `conversation-item${state.peer === contact.id ? ' is-active' : ''}`, () => openPeer(contact.id, contact));
    row.dataset.peerId = String(contact.id);
    row.setAttribute('aria-current', state.peer === contact.id ? 'true' : 'false');
    const copy = node('span', 'conversation-copy');
    const name = node('b', '', contact.name);
    const preview = node('small');
    const draft = state.threads.get(contact.id)?.draft;
    preview.textContent = draft ? `[草稿] ${draft}` : conversation.last?.content || '开始新的对话';
    copy.append(name, preview);
    const meta = node('span', 'conversation-meta');
    if (conversation.last) meta.append(node('time', '', dateLabel(conversation.last.created_at)));
    if (conversation.unread) meta.append(node('i', 'unread-badge', conversation.unread > 99 ? '99+' : String(conversation.unread)));
    row.append(avatar(contact), copy, meta);
    return row;
  }
  function renderInbox() {
    const list = $('[data-conversation-list]');
    const items = [...state.inbox.values()].sort((a, b) => (b.last?.id || 0) - (a.last?.id || 0));
    if (!items.length) { empty(list, '还没有会话', '找到一位联系人，从一句问候开始。', () => searchContacts(), '新建私信'); return; }
    const signature = JSON.stringify(items.map((item) => [item.peer_id, item.contact, item.last?.id, item.last?.content, item.unread, state.peer === item.peer_id, state.threads.get(item.peer_id)?.draft]));
    $<HTMLButtonElement>('[data-conversation-more]').hidden = !state.inboxMore;
    if (list.dataset.signature === signature && list.querySelector('.conversation-item')) return;
    const focused = document.activeElement?.closest<HTMLElement>('[data-peer-id]')?.dataset.peerId;
    list.replaceChildren(...items.map(inboxRow));
    list.dataset.signature = signature;
    if (focused) optional(`[data-peer-id="${Number(focused)}"]`, list)?.focus({ preventScroll: true });
    $<HTMLButtonElement>('[data-conversation-more]').hidden = !state.inboxMore;
  }
  async function openMessages() {
    if (!messageDrawer) return;
    setOverlay(messageDrawer, true);
  }
  function cancelContactSearch() {
    clearTimeout(contactTimer); contactRequest++;
    contactLoading = false;
    if (!messageDrawer) return;
    $<HTMLButtonElement>('[data-contact-more]').disabled = false;
    $('[data-contact-results]').removeAttribute('aria-busy');
  }
  function closeContactSearch() {
    cancelContactSearch(); contactComposing = false;
    if (!messageDrawer) return;
    $('[data-contact-search]').hidden = true;
  }
  async function searchContacts(query = '', append = false): Promise<unknown> {
    if (!messageDrawer) return;
    if (!state.session?.authenticated) return signIn(() => searchContacts(query));
    if (append && (contactLoading || contactComposing)) return;
    clearTimeout(contactTimer);
    const term = query.trim();
    if (append && (!contactPage || term !== contactQuery)) append = false;
    const page = append ? contactPage + 1 : 1;
    setOverlay(messageDrawer, true);
    messageDrawer.classList.remove('has-thread');
    const panel = $('[data-contact-search]'); panel.hidden = false;
    const input = $<HTMLInputElement>('[data-contact-query]');
    if (!append && input.value !== query) input.value = query;
    if (!append) { contactPage = 0; contactQuery = term; }
    const request = ++contactRequest;
    const epoch = state.epoch;
    const list = $('[data-contact-results]');
    const more = $<HTMLButtonElement>('[data-contact-more]');
    optional('[data-contact-error]', list)?.remove();
    if (!append) loading(list, '正在查找联系人…');
    contactLoading = true; list.setAttribute('aria-busy', 'true');
    more.hidden = !append; more.disabled = true; more.textContent = '正在加载…';
    try {
      const value = await api(`/api/v1/social/contacts?q=${encodeURIComponent(term)}&page=${page}`);
      if (epoch !== state.epoch || request !== contactRequest) return;
      const data = parseContacts(value); if (data.page !== page) throw new Error('联系人分页不匹配，请重试');
      if (!append) list.replaceChildren();
      data.items.forEach((contact) => {
        const row = button('', 'contact-result', () => { closeContactSearch(); return openPeer(contact.id, contact); });
        const copy = node('span'); copy.append(node('b', '', contact.name), node('small', '', `@${contact.username}`));
        row.append(avatar(contact), copy, createIcon('chevron-right')); list.append(row);
      });
      contactPage = page;
      if (!data.items.length && !append) empty(list, term ? '没有找到联系人' : '还没有其他联系人', term ? '试试完整用户名或昵称。' : '站点有其他活跃账号后，就可以开始私信。');
      more.hidden = !data.has_more; more.textContent = '更多联系人';
    } catch (failure) {
      if (epoch !== state.epoch || request !== contactRequest || interactionFailure(failure).name === 'AbortError') return;
      if (append) {
        const feedback = node('p', 'interaction-error', interactionFailure(failure).message);
        feedback.dataset.contactError = ''; feedback.setAttribute('role', 'status'); list.append(feedback);
        more.hidden = false; more.textContent = '重试加载更多';
      } else {
        empty(list, '联系人加载失败', interactionFailure(failure).message, () => searchContacts(query));
        more.hidden = true;
      }
    } finally {
      if (epoch === state.epoch && request === contactRequest) {
        contactLoading = false; more.disabled = false; list.removeAttribute('aria-busy');
      }
    }
  }
  function queueContactSearch(composing = contactComposing) {
    if (!messageDrawer) return;
    cancelContactSearch();
    $<HTMLButtonElement>('[data-contact-more]').hidden = true;
    if ($('[data-contact-search]').hidden || !messageDrawer.open) return;
    loading($('[data-contact-results]'), composing ? '输入完成后开始搜索' : '正在查找联系人…');
    if (composing) return;
    const query = $<HTMLInputElement>('[data-contact-query]').value;
    const request = contactRequest;
    contactTimer = setTimeout(() => {
      if (request === contactRequest && !$('[data-contact-search]').hidden && messageDrawer.open) run(() => searchContacts(query));
    }, 250);
  }
  optional<HTMLInputElement>('[data-contact-query]')?.addEventListener('input', (event) => queueContactSearch(contactComposing || event instanceof InputEvent && event.isComposing));
  optional<HTMLInputElement>('[data-contact-query]')?.addEventListener('compositionstart', () => { contactComposing = true; queueContactSearch(); });
  optional<HTMLInputElement>('[data-contact-query]')?.addEventListener('compositionend', () => { contactComposing = false; queueContactSearch(); });

  function ensureThread(t: MessageThread) {
    if (!messageDrawer || !threadPanel) return;
    const previous = optional<HTMLTextAreaElement>('[data-message-input]');
    if (previous && Number(previous.dataset.peer) === t.peer) return;
    emoji.close();
    const header = node('div', 'message-thread-header');
    const back = button('', 'message-back icon-button', () => { saveActiveDraft(); messageDrawer.classList.remove('has-thread'); renderInbox(); optional(`[data-peer-id="${t.peer}"]`)?.focus(); }, 'arrow-left');
    back.setAttribute('aria-label', '返回会话列表');
    const copy = node('div', 'message-peer');
    copy.append(node('b', '', t.contact.name), node('small', '', t.contact.available ? `@${t.contact.username || ''} · 私密会话` : '这个账号目前无法接收消息'));
    header.append(back, avatar(t.contact), copy);
    const refresh = button('', 'icon-button', () => loadThread(t), 'refresh-cw'); refresh.setAttribute('aria-label', '刷新会话'); header.append(refresh);
    const scroller = node('div', 'thread-messages'); scroller.dataset.messageScroll = ''; scroller.tabIndex = 0; scroller.setAttribute('role', 'region'); scroller.setAttribute('aria-label', `与${t.contact.name}的聊天记录`);
    const history = button('加载更早消息', 'message-history', () => loadThread(t, 'older')); history.dataset.messageHistory = ''; history.hidden = true;
    const log = node('div', 'thread-log'); log.dataset.messageLog = '';
    scroller.append(history, log);
    const jump = button('查看新消息', 'message-new-arrivals', () => { scroller.scrollTop = scroller.scrollHeight; jump.hidden = true; return markRead(t); }, 'arrow-down'); jump.dataset.messageJump = ''; jump.hidden = true;
    const form = node('form', 'message-composer');
    const label = node('label', 'sr-only', `发送消息给${t.contact.name}`); label.htmlFor = 'private-message-input';
    const input = node('textarea'); input.id = 'private-message-input'; input.dataset.messageInput = ''; input.dataset.peer = String(t.peer); input.dataset.emojiInput = ''; input.dataset.emojiMax = '2000'; input.maxLength = 4000; input.rows = 3;
    input.placeholder = '写下你想说的话…'; input.value = t.draft; input.disabled = !t.contact.available;
    const toolbar = node('div', 'message-composer-toolbar');
    const picker = button('', 'emoji-trigger', null, 'smile'); picker.dataset.emojiTrigger = ''; picker.dataset.emojiTarget = input.id; picker.setAttribute('aria-label', '添加表情'); picker.setAttribute('aria-haspopup', 'dialog'); picker.setAttribute('aria-expanded', 'false'); picker.disabled = input.disabled;
    const hint = node('span', 'message-input-hint', 'Enter 发送 · Shift + Enter 换行');
    const count = node('span', 'message-counter'); count.id = 'private-message-count'; input.setAttribute('aria-describedby', count.id);
    const send = node('button', 'ui-button ui-button-primary ui-button-small', '发送'); send.type = 'submit'; send.prepend(createIcon('send'));
    toolbar.append(picker, hint, count, send);
    const errorBox = node('p', 'interaction-error'); errorBox.dataset.messageError = ''; errorBox.setAttribute('role', 'alert'); errorBox.hidden = true;
    form.append(label, input, toolbar, errorBox);
    threadPanel.replaceChildren(header, scroller, jump, form);
    const update = () => {
      const length = [...input.value].length;
      count.textContent = `${length} / 2000`;
      count.classList.toggle('is-limit', length > 2000);
      send.disabled = !input.value.trim() || length > 2000 || !t.contact.available || t.outbox.some((item) => item.status === 'sending');
      input.setCustomValidity(length > 2000 ? '消息最多 2000 个字符' : '');
      t.draft = input.value; saveThread(t);
    };
    input.addEventListener('input', update);
    input.addEventListener('keydown', (event) => {
      if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && event.keyCode !== 229 && !mobile.matches) { event.preventDefault(); if (!send.disabled) form.requestSubmit(); }
    });
    form.addEventListener('submit', (event) => { event.preventDefault(); if (!send.disabled && form.reportValidity()) run(() => sendMessage(t, input.value)); });
    scroller.addEventListener('scroll', () => {
      if (atBottom(scroller)) { jump.hidden = true; run(() => markRead(t)); }
    }, { passive: true });
    emoji.enhance(form);
    update();
  }
  function atBottom(el: HTMLElement | null) { return !!el && el.scrollHeight - el.scrollTop - el.clientHeight < 48; }
  function activeThread(t: MessageThread) { return state.peer === t.peer && state.session?.id === t.owner && state.threads.get(t.peer) === t && messageDrawer?.open === true; }
  function renderThread(t: MessageThread, mode: ThreadMode = 'refresh') {
    if (state.peer !== t.peer || state.session?.id !== t.owner) return;
    ensureThread(t);
    const scroller = $('[data-message-scroll]');
    const bottom = atBottom(scroller);
    const oldHeight = scroller.scrollHeight;
    const top = scroller.scrollTop;
    const log = $('[data-message-log]');
    const data = [...t.messages.values()].sort((a, b) => a.id - b.id);
    const items = [...data, ...t.outbox.map((item) => ({ ...item, sender_id: t.owner }))];
    const signature = JSON.stringify(items.map((item) => [item.id || item.nonce, item.read_at, item.status, item.error, t.readThrough]));
    if (log.dataset.signature !== signature) {
      const fragment = document.createDocumentFragment();
      let previousDate = '';
      for (const m of items) {
        const day = window.ChunnelTime?.formatDate(m.created_at) || '';
        if (day !== previousDate) { fragment.append(node('div', 'message-date-divider', day)); previousDate = day; }
        const own = m.sender_id === t.owner;
        const row = node('article', `message-row${own ? ' is-own' : ''}${m.status === 'failed' ? ' is-failed' : ''}`);
        if (m.id) row.dataset.messageId = String(m.id);
        const bubble = node('div', 'message-bubble');
        const copy = node('div', 'message-copy'); copy.append(emoji.fragment(m.content));
        const meta = node('div', 'message-meta');
        const time = node('time', '', dateLabel(m.created_at)); time.title = fullDate(m.created_at); time.dateTime = m.created_at; meta.append(time);
        if (own) {
          if (m.status === 'failed') {
            const retry = button('发送失败 · 重试', 'message-retry', () => sendMessage(t, m.content, m.nonce), 'refresh-cw'); retry.title = m.error || '重试不会重复发送'; meta.append(retry);
          } else meta.append(node('span', m.read_at || m.id !== undefined && m.id <= t.readThrough ? 'message-read' : '', m.status === 'sending' ? '发送中…' : m.read_at || m.id !== undefined && m.id <= t.readThrough ? '已读' : '已送达'));
        }
        bubble.append(copy, meta); row.append(bubble); fragment.append(row);
      }
      if (!items.length) { const welcome = node('div', 'thread-welcome'); welcome.append(avatar(t.contact), node('b', '', `和${t.contact.name}打个招呼`), node('p', '', '友善交流，从这里开始。')); fragment.append(welcome); }
      log.replaceChildren(fragment); log.dataset.signature = signature;
    }
    $<HTMLButtonElement>('[data-message-history]').hidden = !t.hasMore;
    $<HTMLButtonElement>('[data-message-history]').disabled = t.loading;
    const input = $<HTMLTextAreaElement>('[data-message-input]');
    input.disabled = !t.contact.available;
    $('.message-peer b').textContent = t.contact.name;
    $('.message-thread-header > .conversation-avatar').replaceWith(avatar(t.contact));
    $('.message-composer > label').textContent = `发送消息给${t.contact.name}`;
    scroller.setAttribute('aria-label', `与${t.contact.name}的聊天记录`);
    $('.message-peer small').textContent = t.contact.available ? `@${t.contact.username || ''} · 私密会话` : '这个账号目前无法接收消息';
    const send = $<HTMLButtonElement>('.message-composer button[type="submit"]');
    send.disabled = !input.value.trim() || [...input.value].length > 2000 || !t.contact.available || t.outbox.some((item) => item.status === 'sending');
    if (mode === 'older') scroller.scrollTop = top + scroller.scrollHeight - oldHeight;
    else if (mode === 'initial' || mode === 'send' || bottom) scroller.scrollTop = scroller.scrollHeight;
    else if (scroller.scrollHeight > oldHeight) { $('[data-message-jump]').hidden = false; announce('当前会话有新消息'); }
  }
  async function openPeer(value: number | string, contact?: Contact): Promise<unknown> {
    if (!messageDrawer) return;
    closeContactSearch();
    const peer = Number(value);
    if (!Number.isSafeInteger(peer) || peer <= 0) return searchContacts(contact?.name || '');
    if (!state.session?.authenticated) return signIn(() => openPeer(peer, contact));
    if (peer === state.session.id) { showToast('这是你自己的账号，试着联系其他创作者吧', 'info'); return; }
    saveActiveDraft();
    const t = thread(peer, contact);
    state.peer = peer;
    if (!state.inbox.has(peer)) state.inbox.set(peer, { peer_id: peer, contact: t.contact, unread: 0 });
    if (notificationDrawer.open) setOverlay(notificationDrawer, false);
    setOverlay(messageDrawer, true);
    messageDrawer.classList.add('has-thread');
    $('[data-contact-search]').hidden = true;
    renderInbox(); ensureThread(t);
    if (!t.loaded) loading($('[data-message-log]'), '正在加载会话…');
    else renderThread(t, 'initial');
    await loadThread(t, t.loaded ? 'refresh' : 'initial');
    if (activeThread(t) && !mobile.matches) $<HTMLTextAreaElement>('[data-message-input]')?.focus({ preventScroll: true });
  }
  function schedule() {
    clearTimeout(pollTimer);
    if (!state.session?.authenticated || document.hidden) return;
    const delay = failures ? Math.min(60000, 10000 * 2 ** failures) : messageDrawer?.open || notificationDrawer.open ? 10000 : 30000;
    pollTimer = setTimeout(() => sync(), delay);
  }
  async function sync() {
    if (syncRequest) return syncRequest;
    if (document.hidden) return;
    if (!navigator.onLine) { connection('当前处于离线状态，输入内容会保留'); schedule(); return; }
    syncRequest = (async () => {
      try {
        const session = await getSession(true);
        if (!session.authenticated) return;
        const jobs = [loadNotices(false, true)];
        if (messageDrawer) jobs.push(loadInbox());
        const t = state.threads.get(state.peer);
        if (t && activeThread(t)) jobs.push(loadThread(t));
        const results = await Promise.allSettled(jobs);
        const rejected = results.find((result) => result.status === 'rejected' && interactionFailure(result.reason).name !== 'AbortError');
        if (rejected?.status === 'rejected') throw rejected.reason;
        failures = 0; connection();
      } catch (failure) { failures++; connection(interactionFailure(failure).status === 401 ? '登录已过期，请重新登录' : '连接暂时中断，正在等待重连'); }
      finally { syncRequest = null; schedule(); }
    })();
    return syncRequest;
  }

  let clearThrough: NoticeBoundary | null = null;

  document.addEventListener('chunnel:overlay', (event) => {
    const open = event.detail.open;
    if (event.target === messageDrawer) {
      if (!open) { closeContactSearch(); saveActiveDraft(); schedule(); return; }
      run(async () => {
        if (!state.session) loading($('[data-conversation-list]'));
        const session = await getSession();
        if (!session.authenticated) { guest($('[data-conversation-list]'), '登录后开始对话'); return; }
        const jobs = [loadInbox()];
        const active = state.threads.get(state.peer);
        if (active) jobs.push(loadThread(active, active.loaded ? 'refresh' : 'initial'));
        try { await Promise.all(jobs); } finally { schedule(); }
      });
    }
    if (event.target === notificationDrawer && open) {
      run(async () => {
        loading($('[data-notification-list]'));
        const session = await getSession();
        if (!session.authenticated) { guest($('[data-notification-list]'), '登录后查看通知'); return; }
        try { state.noticeCursor = ''; await loadNotices(); schedule(); }
        catch (failure) { empty($('[data-notification-list]'), '通知加载失败', interactionFailure(failure).message, () => loadNotices()); }
      });
    }
  });
  notificationDrawer.addEventListener('close', () => {
    const t = state.threads.get(state.peer);
    if (t) run(() => markRead(t));
  });
  document.addEventListener('click', (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (!target) return;
    if (target.closest('[data-account-login]')) { event.preventDefault(); run(() => signIn()); }
    const registration = target.closest<HTMLElement>('[data-account-register], [data-registration-reload]');
    if (registration && !(event.ctrlKey || event.metaKey || event.shiftKey || event.altKey)) { event.preventDefault(); run(() => showRegistration(registration.dataset.accountRegister || 'register')); }
    if (target.closest('[data-account-logout]')) run(logout);
    if (target.closest('[data-password-toggle]')) {
      const toggle = target.closest<HTMLElement>('[data-password-toggle]'); if (!toggle) return;
      const input = toggle.previousElementSibling; if (!(input instanceof HTMLInputElement)) return;
      const visible = input.type === 'password';
      input.type = visible ? 'text' : 'password';
      toggle.setAttribute('aria-label', visible ? '隐藏密码' : '显示密码');
      toggle.setAttribute('aria-pressed', String(visible)); toggle.title = visible ? '隐藏密码' : '显示密码';
    }
    if (target.closest('[data-new-conversation]')) run(async () => { await searchContacts(); $<HTMLInputElement>('[data-contact-query]').focus(); });
    if (target.closest('[data-contact-close]')) { closeContactSearch(); $('[data-new-conversation]').focus(); }
    if (target.closest('[data-contact-more]')) run(() => searchContacts($<HTMLInputElement>('[data-contact-query]').value, true));
    if (target.closest('[data-conversation-more]')) run(async () => { const b = $<HTMLButtonElement>('[data-conversation-more]'); b.disabled = true; try { await loadInbox(true); } finally { b.disabled = false; } });
    if (target.closest('[data-notification-more]')) run(async () => { const b = $<HTMLButtonElement>('[data-notification-more]'); b.disabled = true; try { await loadNotices(true); } finally { b.disabled = false; } });
    const filter = target.closest<HTMLElement>('[data-notification-filter]');
    if (filter) run(async () => {
      const nextFilter = filter.dataset.notificationFilter; if (nextFilter !== 'all' && nextFilter !== 'interaction' && nextFilter !== 'activity' && nextFilter !== 'unread') return; state.noticeFilter = nextFilter; state.noticeCursor = '';
      $$('[data-notification-filter]').forEach((b) => { b.classList.toggle('is-active', b === filter); b.setAttribute('aria-pressed', String(b === filter)); });
      if (!state.session?.authenticated) { guest($('[data-notification-list]'), '登录后查看通知'); return; }
      loading($('[data-notification-list]'));
      try { await loadNotices(); } catch (failure) { empty($('[data-notification-list]'), '通知加载失败', interactionFailure(failure).message, () => loadNotices()); }
    });
    if (target.closest('[data-notification-read-all]')) run(() => updateNotices('read_all'));
    if (target.closest('[data-notification-clear]') && state.noticeThrough) { clearThrough = state.noticeThrough; setOverlay($('#interaction-confirm'), true); }
    if (target.closest('[data-confirm-notification-clear]')) run(() => updateNotices('clear', clearThrough));
    const recipient = target.closest<HTMLElement>('[data-message-to], [data-message-user]');
    if (recipient && messageDrawer) run(() => signIn(() => recipient.dataset.messageUser ? openPeer(recipient.dataset.messageUser) : searchContacts(recipient.dataset.messageTo || '')));
    const accountMenu = $<HTMLDetailsElement>('[data-account-menu]');
    if (accountMenu.open && (!accountMenu.contains(target) || target.closest('button, a'))) accountMenu.open = false;
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && $<HTMLDetailsElement>('[data-account-menu]').open) { $<HTMLDetailsElement>('[data-account-menu]').open = false; $('summary', $<HTMLDetailsElement>('[data-account-menu]')).focus(); }
  });
  document.addEventListener('visibilitychange', () => { saveActiveDraft(); if (document.hidden) clearTimeout(pollTimer); else sync(); });
  window.addEventListener('online', () => sync());
  window.addEventListener('offline', () => { clearTimeout(pollTimer); connection('当前处于离线状态，输入内容会保留'); });
  window.addEventListener('pagehide', () => { saveActiveDraft(); clearTimeout(pollTimer); });
  window.addEventListener('pageshow', (event) => { if (event.persisted) sync(); });
  const fitViewport = () => document.documentElement.style.setProperty('--interaction-viewport-height', `${window.visualViewport?.height || innerHeight}px`);
  window.visualViewport?.addEventListener('resize', fitViewport);
  window.addEventListener('resize', fitViewport);
  fitViewport();
  if (channel) channel.onmessage = (event) => {
    if (event.data?.type === 'session' || event.data?.user === state.session?.id) sync();
  };
  getSession().then((session) => { if (session.authenticated) sync(); }).catch(() => {
    if (messageDrawer) empty($('[data-conversation-list]'), '暂时无法连接', '请检查网络后重试。', () => sync());
    empty($('[data-notification-list]'), '暂时无法连接', '请检查网络后重试。', () => sync());
  });
  window.ChunnelInteractions = { api, getSession, signIn, info, run, storage, announce, openPeer, searchContacts };
}
mountInteractions();
