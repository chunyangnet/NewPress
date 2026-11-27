import { node } from '../shared/dom';

interface Action { label: string; url?: string; target?: string; module?: string; allowed: boolean; restriction?: string }
interface Check { id: string; label: string; state: string; reason: string; action?: Action }
interface Item { id: string; title: string; scope: string; state: string; label: string; summary: string; mode?: string; effective: boolean; checks: Check[]; action?: Action }
interface Overview { state: string; label: string; title: string; summary: string; additional: number; global_block: boolean; action?: Action }
interface Status { feature: string; object?: string; revision: string; checked_at: string; user_id: number; read_only: boolean; recovery: boolean; items: Item[]; overview: Overview }

// The server owns readiness. This component only presents its explanation and
// invokes the existing revision-checked module API; it never guesses success.
const mountFeatureStatus = (): (() => void) | undefined => {
  const root = document.querySelector<HTMLElement>('[data-feature-status]');
  if (!root) return;
  const items = root.querySelector<HTMLElement>('[data-feature-items]');
  const feedback = root.querySelector<HTMLElement>('[data-feature-feedback]');
  const draft = root.querySelector<HTMLElement>('[data-feature-draft]');
  const refreshButton = root.querySelector<HTMLButtonElement>('[data-feature-refresh]');
  const brief = root.querySelector<HTMLElement>('[data-feature-brief]');
  const overview = root.querySelector<HTMLDetailsElement>('[data-feature-overview]');
  const more = root.querySelector<HTMLElement>('[data-feature-more]');
  const stale = root.querySelector<HTMLElement>('[data-feature-stale]');
  const toggle = overview?.querySelector<HTMLElement>('summary');
  const editorAlert = document.querySelector<HTMLElement>('[data-feature-editor-alert]');
  if (!items || !feedback || !draft || !refreshButton || !brief || !overview || !more || !stale || !toggle) return;
  const lifetime = new AbortController();
  const listen = { signal: lifetime.signal };
  let current: Status;
  try { current = JSON.parse(root.dataset.status || ''); } catch { return; }
  const owner = current.user_id;
  let endpoint = root.dataset.endpoint || '';
  if (!endpoint.startsWith('/api/v1/admin/feature-status?')) return;
  let generation = 0, controller: AbortController | null = null, changing = false, suspended = false, lastRead = 0;
  let manualDirty = false;
  const draftHome = document.createComment('feature status draft hint');
  draft.before(draftHome);
  const updateDraft = () => {
    const bars = [...document.querySelectorAll<HTMLElement>('[data-admin-savebar], [data-editor-savebar], .recognition-save')];
    const dirtyBar = bars.find(bar => bar.classList.contains('is-dirty'));
    const bar = dirtyBar || bars.find(bar => bar.getClientRects().length > 0);
    const destination = bar?.querySelector('[data-editor-save-state]')?.parentElement || bar;
    if (destination && draft.parentElement !== destination) {
      destination.insertBefore(draft, destination.querySelector(':scope > .admin-savebar-actions'));
    } else if (!destination && draft.parentElement !== root) draftHome.after(draft);
    draft.dataset.location = destination ? 'savebar' : 'status';
    const message = destination ? '生效状态仍按已保存配置显示' : '未保存修改 · 生效状态仍按已保存配置显示';
    if (draft.textContent !== message) draft.textContent = message;
    draft.hidden = !manualDirty && !dirtyBar;
  };
  const safeURL = (value: string | undefined): string | null => {
    if (!value || !value.startsWith('/') || value.startsWith('//')) return null;
    try { const url = new URL(value, location.href); return url.origin === location.origin ? url.href : null; } catch { return null; }
  };
  const validAction = (value: unknown): value is Action => {
    if (!value || typeof value !== 'object') return false;
    const a = value as Action;
    return typeof a.label === 'string' && typeof a.allowed === 'boolean' && (!a.url || !!safeURL(a.url)) && (!a.module || /^[a-z][a-z0-9-]{0,63}$/.test(a.module)) && (!a.target || typeof a.target === 'string');
  };
  const valid = (value: unknown): value is Status => {
    if (!value || typeof value !== 'object') return false;
    const v = value as Status;
    return v.feature === current.feature && (v.object || '') === (current.object || '') && v.user_id === owner &&
      typeof v.revision === 'string' && !!v.revision && typeof v.checked_at === 'string' && typeof v.read_only === 'boolean' &&
      !!v.overview && ['state', 'label', 'title', 'summary'].every(k => typeof (v.overview as unknown as Record<string, unknown>)[k] === 'string') &&
      Number.isSafeInteger(v.overview.additional) && v.overview.additional >= 0 && typeof v.overview.global_block === 'boolean' && (!v.overview.action || validAction(v.overview.action)) &&
      Array.isArray(v.items) && v.items.every(item => item && ['id', 'title', 'scope', 'state', 'label', 'summary'].every(k => typeof (item as unknown as Record<string, unknown>)[k] === 'string') &&
        typeof item.effective === 'boolean' && (!item.mode || typeof item.mode === 'string') && (!item.action || validAction(item.action)) && Array.isArray(item.checks) && item.checks.every(c => c &&
          ['id', 'label', 'state', 'reason'].every(k => typeof (c as unknown as Record<string, unknown>)[k] === 'string') && (!c.action || validAction(c.action))));
  };
  const actionNode = (a: Action): HTMLElement => {
    if (!a.allowed) return node('span', 'feature-status-restriction', a.restriction || '需要相应管理权限');
    if (a.module) {
      const button = node('button', 'admin-button admin-button-secondary', a.label);
      button.type = 'button'; button.dataset.featureModule = a.module; button.title = '影响全站，立即保存'; return button;
    }
    const link = node('a', 'admin-button admin-button-quiet', a.label);
    link.href = safeURL(a.url) || '#'; if (a.target) link.dataset.featureTarget = a.target;
    return link;
  };
  const render = (status: Status) => {
    const focused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const inBrief = brief.contains(focused), inItems = items.contains(focused);
    const focusedID = focused?.closest<HTMLElement>('[data-feature-id]')?.dataset.featureId;
    const expanded = new Set([...items.querySelectorAll<HTMLDetailsElement>('details[open]')].map(el => el.closest<HTMLElement>('[data-feature-id]')?.dataset.featureId));
    const o = status.overview;
    const copy = node('div', 'feature-status-brief-copy'), title = node('div', 'feature-status-title');
    title.append(node('span', 'feature-status-badge', o.label));
    if (o.title) title.append(node('strong', '', o.title));
    copy.append(title); if (o.summary) copy.append(node('p', '', o.summary));
    brief.replaceChildren(copy); if (o.action) brief.append(actionNode(o.action));
    brief.dataset.state = o.state; root.dataset.state = o.state;
    more.textContent = o.additional ? `还有 ${o.additional} 项待处理` : '查看详情';
    if (editorAlert) {
      editorAlert.hidden = !o.global_block;
      const reason = editorAlert.querySelector('[data-feature-editor-reason]');
      if (reason) reason.textContent = o.global_block ? o.summary : '';
    }
    const fragment = document.createDocumentFragment();
    for (const item of status.items) {
      const card = node('article', 'feature-status-item'); card.dataset.featureId = item.id; card.dataset.state = item.state;
      const summary = node('div', 'feature-status-summary'), copy = node('div', 'feature-status-copy'), title = node('div', 'feature-status-title');
      title.append(node('strong', '', item.title), node('span', 'feature-status-badge', item.label), node('small', '', item.scope));
      copy.append(title, node('p', '', item.summary)); if (item.mode) copy.append(node('span', 'feature-status-mode', item.mode));
      summary.append(copy); if (item.action) summary.append(actionNode(item.action)); card.append(summary);
      if (item.checks.length) {
        const details = node('details', 'feature-status-details'); details.open = expanded.has(item.id);
        const heading = node('summary', '', '查看全部条件与处理步骤 '); heading.append(node('span', '', `${item.checks.length} 项`));
        const list = node('ol');
        for (const check of item.checks) {
          const row = node('li'); row.dataset.checkState = check.state;
          const text = node('div'); text.append(node('strong', '', check.label), node('p', '', check.reason)); row.append(text);
          if (check.action) row.append(actionNode(check.action)); list.append(row);
        }
        details.append(heading, list); card.append(details);
      }
      fragment.append(card);
    }
    items.replaceChildren(fragment); current = status; root.dataset.stale = 'false'; stale.hidden = true;
    if (focused && (inBrief || inItems)) {
      const scope = inBrief ? brief : [...items.querySelectorAll<HTMLElement>('[data-feature-id]')].find(el => el.dataset.featureId === focusedID);
      const replacement = [...scope?.querySelectorAll<HTMLElement>('button,a,summary') || []].find(el =>
        el.tagName === focused.tagName && el.textContent === focused.textContent &&
        ['data-feature-module', 'data-feature-target', 'href'].every(attr => el.getAttribute(attr) === focused.getAttribute(attr)));
      // The refresh button is disabled while rendering. The disclosure remains
      // focusable even when the formerly focused repair action has disappeared.
      (replacement || toggle).focus({ preventScroll: true });
    }
    updateDraft();
  };
  const say = (text: string, error = false, visible = false) => {
    feedback.textContent = text; feedback.dataset.error = String(error); feedback.dataset.visible = String(error || visible);
  };
  const setBusy = (busy: boolean) => {
    root.setAttribute('aria-busy', String(busy)); refreshButton.disabled = busy;
    root.querySelectorAll<HTMLButtonElement>('[data-feature-module]').forEach(button => button.disabled = changing);
  };
  const refresh = async (announce = true) => {
    if (suspended || changing) return;
    const mine = ++generation; controller?.abort(); controller = new AbortController();
    const activeController = controller, timeout = setTimeout(() => activeController.abort(), 10000);
    setBusy(true);
    try {
      const response = await fetch(endpoint, { credentials: 'same-origin', cache: 'no-store', signal: controller.signal, headers: { Accept: 'application/json' } });
      if (!response.ok || response.redirected) throw new Error(response.status === 401 || response.status === 403 ? '登录或权限已变化，请核对原账号后重新检测。' : '暂时无法检测，以下保留上次状态。');
      const data: unknown = await response.json();
      if (mine !== generation || suspended) return;
      if (!valid(data)) throw new Error('账号或状态数据已变化，请重新载入页面；当前输入仍保留。');
      render(data); lastRead = Date.now();
      if (announce) say('已重新检测。'+data.items.map(i => `${i.title}：${i.label}`).join('；'));
      else if (feedback.dataset.error === 'true') say('状态已重新检测。');
    } catch (error) {
      if (mine !== generation || suspended) return;
      root.dataset.stale = 'true'; stale.hidden = false;
      const message = (error as Error).name === 'AbortError' ? '检测超时，以下保留上次状态；可重新检测。' :
        error instanceof TypeError ? '网络连接中断，以下保留上次状态；请检查网络后重新检测。' :
        error instanceof SyntaxError ? '状态响应格式异常，以下保留上次状态；可重新检测。' :
        error instanceof Error ? error.message : '状态检测失败，当前输入仍保留。';
      say(message, true);
    } finally {
      clearTimeout(timeout);
      if (mine === generation) setBusy(false);
    }
  };
  const enableModule = async (button: HTMLButtonElement) => {
    if (changing || current.read_only || suspended) return;
    const id = button.dataset.featureModule;
    if (!id) return;
    const allowed = current.items.some(i => [i.action, ...i.checks.map(c => c.action)].some(a => a && a.module === id && a.allowed));
    if (!allowed) return;
    const restoreFocus = root.contains(document.activeElement);
    changing = true; const mine = ++generation; controller?.abort(); controller = new AbortController();
    const activeController = controller, timeout = setTimeout(() => activeController.abort(), 15000);
    setBusy(true);
    let message = '';
    try {
      const response = await fetch('/api/v1/admin/modules', { method: 'PATCH', credentials: 'same-origin', signal: controller.signal,
        headers: { 'Content-Type': 'application/json', Accept: 'application/json', 'X-CSRF-Token': root.dataset.csrf || '' },
        body: JSON.stringify({ revision: current.revision, modules: { [id]: true } }) });
      if (response.redirected || !response.ok) {
        if (response.status === 409) message = '设置已被其他操作修改，本次没有覆盖。已重新读取生效状态，请核对后再操作。';
        else if (response.status === 401 || response.status === 403) message = '登录或权限已变化，本次操作未完成；当前输入保留。';
        else message = '未能确认启用结果，已重新检测；请核对状态后再操作。';
      } else message = '模块启用意图已保存，影响全站。实际生效情况见下方状态。';
    } catch { message = '未能确认启用结果，已重新检测；请核对状态后再操作。'; }
    finally {
      clearTimeout(timeout);
      if (mine === generation) { changing = false; setBusy(false); }
    }
    if (mine !== generation || suspended) return;
    say(message, false, true); await refresh(false);
    if (!suspended && !changing && restoreFocus && document.activeElement === document.body) toggle.focus({ preventScroll: true });
  };
  root.addEventListener('click', event => {
    if (event.defaultPrevented || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    const target = event.target instanceof Element ? event.target : null;
    const moduleButton = target?.closest<HTMLButtonElement>('[data-feature-module]');
    if (moduleButton) { event.preventDefault(); void enableModule(moduleButton); return; }
    const link = target?.closest<HTMLAnchorElement>('[data-feature-target]');
    if (!link || link.hasAttribute('download') || (link.target && link.target !== '_self') || !safeURL(link.href.startsWith(location.origin) ? link.href.slice(location.origin.length) : link.getAttribute('href') || '')) return;
    const url = new URL(link.href, location.href);
    if (url.pathname !== location.pathname) return;
    const sameObject = current.feature === 'currency' ? url.searchParams.get('currency') === current.object :
      ['feedback', 'links'].includes(current.feature) ? url.searchParams.get('edit') === current.object :
      ['article-comments', 'wechat', 'ai-queue', 'layout', 'progression', 'recognition'].includes(current.feature);
    if (url.search !== location.search && !sameObject) return;
    let control: HTMLElement | null = null;
    try { control = document.querySelector<HTMLElement>(link.dataset.featureTarget || ''); } catch { return; }
    if (!control) return;
    event.preventDefault();
    control.closest('[data-content-editor]')?.dispatchEvent(new CustomEvent('newpress:editor-reveal', { detail: { target: control } }));
    const panel = control.closest<HTMLElement>('[data-wechat-panel]');
    if (panel) document.querySelector<HTMLButtonElement>(`[data-wechat-tab="${CSS.escape(panel.dataset.wechatPanel || '')}"]`)?.click();
    const section = control.closest<HTMLElement>('[data-settings-section]');
    if (section?.id) window.ChunnelAdminUI?.settingsNavigation().activate(section.id, true);
    let parent: HTMLElement | null = control.parentElement;
    while (parent && parent !== document.body) { if (parent instanceof HTMLDetailsElement) parent.open = true; parent = parent.parentElement; }
    if (control instanceof HTMLSelectElement && control.getAttribute('aria-hidden') === 'true') {
      control = control.nextElementSibling?.querySelector<HTMLElement>('.np-choice-trigger') || control;
    }
    if (!control.matches('input,select,textarea,button,a,[tabindex]')) control.tabIndex = -1;
    control.scrollIntoView({ block: 'center', behavior: 'auto' }); control.focus({ preventScroll: true });
    say('已定位到对应设置，修改后按本页保存规则生效。');
  }, listen);
  refreshButton.addEventListener('click', () => void refresh(), listen);
  editorAlert?.querySelector('[data-feature-open-settings]')?.addEventListener('click', event => {
    if (event.defaultPrevented || event instanceof MouseEvent && (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey)) return;
    event.preventDefault();
    root.closest('[data-content-editor]')?.dispatchEvent(new CustomEvent('newpress:editor-reveal', { detail: { target: root } }));
    overview.open = true; toggle.focus({ preventScroll: true }); root.scrollIntoView({ block: 'nearest' });
  }, listen);
  let timer: ReturnType<typeof setTimeout> | undefined;
  document.addEventListener('newpress:feature-status-refresh', () => { clearTimeout(timer); timer = setTimeout(() => void refresh(), 150); }, listen);
  document.addEventListener('newpress:feature-status-draft', event => { manualDirty = !!(event as CustomEvent<{ dirty: boolean }>).detail?.dirty; updateDraft(); }, listen);
  document.addEventListener('newpress:feature-status-article-saved', event => {
    if (current.feature !== 'article-comments') return;
    const id = (event as CustomEvent<{ id: number }>).detail?.id;
    if (!Number.isSafeInteger(id) || id < 1) return;
    current.object = String(id); endpoint = `/api/v1/admin/feature-status?feature=article-comments&object=${id}`;
    void refresh();
  }, listen);
  const dirtyObserver = new MutationObserver(updateDraft);
  dirtyObserver.observe(document.querySelector('.admin-main') || document.body, { subtree: true, attributes: true, attributeFilter: ['class'] });
  window.addEventListener('focus', () => { if (Date.now() - lastRead > 15000) void refresh(false); }, listen);
  const suspend = () => {
    suspended = true; ++generation; changing = false; controller?.abort(); clearTimeout(timer); setBusy(false);
  };
  window.addEventListener('pagehide', suspend, listen);
  window.addEventListener('pageshow', event => { suspended = false; if (event.persisted) void refresh(false); }, listen);
  updateDraft();
  return () => { suspend(); lifetime.abort(); dirtyObserver.disconnect(); draftHome.replaceWith(draft); };
};

let mountedRoot = document.querySelector('[data-feature-status]');
let disposeFeatureStatus = mountFeatureStatus();
document.addEventListener('chunnel:admin-updated', () => {
  const next = document.querySelector('[data-feature-status]');
  if (next === mountedRoot) return;
  disposeFeatureStatus?.(); mountedRoot = next; disposeFeatureStatus = mountFeatureStatus();
});
