export const needsPublicationReview = (before: string, after: string, previousTime: string, nextTime: string) => before === after
    ? after === 'future' && previousTime !== nextTime
    : [before, after].some(status => ['publish', 'future', 'private'].includes(status));

(() => {
  if (typeof document === 'undefined') return;
  const pageNode = document.querySelector<HTMLElement>('.admin-editor-page');
  const formNode = pageNode?.querySelector<HTMLFormElement>('[data-content-editor]');
  if (!pageNode || !formNode) return;
  const page = pageNode, form = formNode;
  const sidebar = form.querySelector<HTMLElement>('#editor-settings')!;
  const toggle = form.querySelector<HTMLButtonElement>('[data-editor-settings-toggle]')!;
  const focus = form.querySelector<HTMLButtonElement>('[data-editor-focus]')!;
  const savebar = form.querySelector<HTMLElement>('[data-editor-savebar]')!;
  const toolbar = form.querySelector<HTMLElement>('[data-composer-toolbar]')!;
  const actions = form.querySelector<HTMLElement>('.editor-primary-actions')!;
  const status = form.querySelector<HTMLSelectElement>('[data-content-status]')!;
  const schedule = form.querySelector<HTMLInputElement>('[name="published_at"]');
  const save = form.querySelector<HTMLButtonElement>('button[value="save"]')!;
  const publish = form.querySelector<HTMLButtonElement>('[data-editor-publish]');
  let canonical = form.dataset.editorCanonicalStatus || status.value;
  let canonicalTime = schedule?.value || '';
  let narrow = page.clientWidth < 1120, settingsOpen = !narrow, beforeFocus = settingsOpen;
  let selectedPanel = 'article';

  const preview = page.querySelector<HTMLButtonElement>('[data-editor-preview]');
  if (preview) actions.insertBefore(preview, actions.firstChild);
  const publicLink = page.querySelector<HTMLElement>('.admin-editor-heading a[target="_blank"]');
  if (publicLink) form.querySelector<HTMLElement>('.editor-page-more .editor-more-menu')!.append(publicLink);
  const wechat = page.querySelector<HTMLElement>('.admin-callout a[href^="/admin/wechat/"]');
  if (wechat) {
    const callout = wechat.closest('.admin-callout');
    wechat.textContent = '同步到微信公众号';
    form.querySelector<HTMLElement>('.editor-page-more .editor-more-menu')!.append(wechat); callout?.remove();
  }
  if (form.dataset.editorForceDirty !== 'true') page.querySelectorAll<HTMLElement>('.admin-content-notice-success').forEach(notice => { notice.hidden = true; });
  const canonicalBadge = page.querySelector<HTMLElement>('[data-editor-canonical-state]');
  if (canonicalBadge) form.querySelector<HTMLElement>('.admin-editor-save-copy')!.prepend(canonicalBadge);
  const aiPolicy = savebar.querySelector<HTMLElement>('[name="ai_pro_max"]')?.closest('label');
  if (aiPolicy) form.querySelector<HTMLElement>('#editor-publication-panel')!.append(aiPolicy);
  const topics = savebar.querySelector<HTMLElement>('.aipm-text-link');
  if (topics) form.querySelector<HTMLElement>('#editor-assistant-panel')!.append(topics);
  if (toolbar) form.insertBefore(toolbar, form.querySelector<HTMLElement>('.admin-editor-layout'));
  const insert = form.querySelector<HTMLElement>('.editor-insert-menu');
  if (insert && toolbar) toolbar.querySelector<HTMLElement>('.editor-insert-slot')!.append(insert);
  page.querySelector<HTMLElement>('.admin-editor-heading')!.hidden = true;
  page.classList.add('editor-enhanced');
  toggle.hidden = false; focus.hidden = false;
  form.querySelector<HTMLElement>('[data-document-insert]')!.hidden = false;

  const sidebarHome = document.createComment('settings position');
  sidebar.before(sidebarHome);
  const drawer = document.createElement('dialog');
  drawer.className = 'editor-settings-drawer'; drawer.setAttribute('aria-label', '文章设置');
  form.append(drawer);
  const modalState = () => document.body.classList.toggle('editor-modal-open', !!document.querySelector<HTMLElement>('dialog[open]'));
  function showSettings(open: boolean, returnFocus = false) {
    settingsOpen = open;
    page.classList.toggle('is-settings-hidden', !open || narrow);
    toggle.setAttribute('aria-expanded', String(open));
    if (narrow && open) {
      if (sidebar.parentElement !== drawer) drawer.append(sidebar);
      if (!drawer.open) drawer.showModal();
    } else {
      if (drawer.open) drawer.close();
      if (sidebar.previousSibling !== sidebarHome) sidebarHome.after(sidebar);
    }
    modalState();
    if (returnFocus) toggle.focus();
  }
  drawer.addEventListener('cancel', event => { event.preventDefault(); showSettings(false, true); });
  drawer.addEventListener('click', event => { if (event.target === drawer) showSettings(false, true); });
  sidebar.querySelector<HTMLElement>('[data-editor-settings-close]')!.addEventListener('click', () => showSettings(false, true));
  toggle.addEventListener('click', () => {
    if (page.classList.contains('is-focus')) { setFocus(false); showSettings(true); }
    else showSettings(!settingsOpen);
  });
  function setFocus(active: boolean) {
    if (active) beforeFocus = settingsOpen;
    page.classList.toggle('is-focus', active); document.body.classList.toggle('is-editor-focused', active);
    focus.setAttribute('aria-pressed', String(active));
    focus.querySelector<HTMLElement>('span')!.textContent = active ? '退出专注' : '专注';
    form.querySelector<HTMLElement>('[data-editor-mobile-focus]')!.textContent = active ? '退出专注写作' : '专注写作';
    focus.title = active ? '退出专注写作' : '专注写作';
    showSettings(active ? false : beforeFocus);
  }
  focus.addEventListener('click', () => setFocus(!page.classList.contains('is-focus')));
  form.querySelector<HTMLElement>('[data-editor-mobile-focus]')!.addEventListener('click', () => setFocus(!page.classList.contains('is-focus')));
  function showPanel(name: string) {
    selectedPanel = name;
    sidebar.querySelectorAll<HTMLElement>('[data-editor-settings-panel]').forEach(panel => {
      panel.hidden = panel.dataset.editorSettingsPanel !== name;
      if (panel instanceof HTMLDetailsElement && !panel.hidden) panel.open = true;
    });
    sidebar.querySelectorAll<HTMLElement>('[data-editor-settings-tab]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.editorSettingsTab === name)));
  }
  sidebar.querySelectorAll<HTMLElement>('[data-editor-settings-tab]').forEach(button => button.addEventListener('click', () => showPanel(button.dataset.editorSettingsTab || 'article')));
  form.querySelector<HTMLElement>('[data-editor-open-assistant]')?.addEventListener('click', () => {
    const panel = form.querySelector<HTMLDetailsElement>('[data-ai-paragraph]');
    if (panel) { panel.open = true; panel.scrollIntoView({block: 'center', behavior: 'smooth'}); panel.querySelector<HTMLElement>('select, button')?.focus({preventScroll: true}); }
  });
  showPanel(selectedPanel); showSettings(settingsOpen);

  // Settings stay in their original form even when displayed in a native modal.
  const wide = document.createElement('dialog');
  wide.className = 'editor-wide-dialog'; wide.setAttribute('aria-labelledby', 'editor-wide-title');
  wide.innerHTML = '<header><div><h2 id="editor-wide-title"></h2><p>设置会随正文一起保存</p></div><button type="button" data-wide-close aria-label="关闭设置">×</button></header><div class="editor-wide-body"></div><footer><button type="button" class="admin-button admin-button-primary" data-wide-done>完成设置</button></footer>';
  form.append(wide);
  let activeGroup: HTMLDetailsElement | null = null, groupHome: Comment | null = null, groupLauncher: HTMLElement | null = null, reopenDrawer = false;
  const existingHistory = form.querySelector<HTMLDetailsElement>('[data-editor-wide="history"]');
  const history = existingHistory || document.createElement('details');
  if (!existingHistory) { history.className = 'editor-settings-group'; history.dataset.editorWide = 'history';
    history.innerHTML = '<summary>版本与恢复</summary><p class="editor-history-hint">首次保存后可查看正式版本历史。</p>';
    form.querySelector<HTMLElement>('.editor-below-grid')!.append(history);
  }
  const recovery = page.querySelector<HTMLElement>('[data-browser-recovery]');
  if (recovery) history.append(recovery);
  function restoreWide() {
    if (!activeGroup) return;
    activeGroup.open = false;
    activeGroup.querySelectorAll<HTMLDetailsElement>(':scope > details').forEach(panel => { panel.open = false; });
    groupHome?.replaceWith(activeGroup); activeGroup = null; groupHome = null;
    if (reopenDrawer && narrow && settingsOpen && !drawer.open) drawer.showModal();
    reopenDrawer = false; groupLauncher?.focus(); modalState();
  }
  function closeWide() { if (wide.open) wide.close(); restoreWide(); }
  function openWide(group: HTMLDetailsElement, launcher: HTMLElement | null) {
    if (activeGroup) closeWide();
    activeGroup = group; groupLauncher = launcher; groupHome = document.createComment('settings group position');
    group.before(groupHome); group.open = true;
    group.querySelectorAll<HTMLDetailsElement>(':scope > details').forEach(panel => { panel.open = true; });
    wide.querySelector<HTMLElement>('h2')!.textContent = group.querySelector<HTMLElement>('summary')?.textContent?.trim() || '文章设置';
    wide.querySelector<HTMLElement>('.editor-wide-body')!.append(group);
    reopenDrawer = drawer.open;
    if (drawer.open) drawer.close();
    wide.showModal(); modalState();
  }
  wide.addEventListener('close', () => { if (!wide.open) restoreWide(); });
  wide.querySelector<HTMLElement>('[data-wide-close]')!.addEventListener('click', closeWide);
  wide.querySelector<HTMLElement>('[data-wide-done]')!.addEventListener('click', closeWide);
  form.querySelectorAll<HTMLDetailsElement>('[data-editor-wide]').forEach(group => {
    const summary = group.querySelector<HTMLElement>(':scope > summary');
    if (!summary) return;
    summary.setAttribute('aria-haspopup', 'dialog');
    summary.addEventListener('click', event => { event.preventDefault(); openWide(group, summary); });
    group.open = false;
  });
  form.querySelector<HTMLElement>('[data-editor-open-history]')!.addEventListener('click', event => openWide(history, event.currentTarget instanceof HTMLElement ? event.currentTarget : null));

  function reveal(target: EventTarget | null) {
    if (!(target instanceof Element)) return;
    if (target.matches('[data-content-source]')) window.ChunnelBlockEditor?.setMode?.('source');
    const group = target.closest<HTMLDetailsElement>('[data-editor-wide]');
    if (group && activeGroup !== group) openWide(group, group.querySelector<HTMLElement>('summary'));
    else if (sidebar.contains(target)) {
      const panel = target.closest<HTMLElement>('[data-editor-settings-panel]');
      if (panel) showPanel(panel.dataset.editorSettingsPanel || 'article');
      showSettings(true);
    }
    for (let node = target.parentElement; node && node !== form; node = node.parentElement) if (node instanceof HTMLDetailsElement) node.open = true;
  }
  form.addEventListener('invalid', event => reveal(event.target), true);
  form.addEventListener('newpress:editor-reveal', event => {
    const target: unknown = (event as CustomEvent<{target?: unknown}>).detail?.target;
    if (target instanceof Element && form.contains(target)) reveal(target);
  });
  form.addEventListener('focusin', event => { if (event.target instanceof Element && !event.target.closest('summary')) reveal(event.target); });
  const submitErrors = form.querySelector<HTMLElement>('[data-editor-submit-errors]');
  if (submitErrors) new MutationObserver(() => {
    if (submitErrors.hidden) return;
    const invalid = form.querySelector<HTMLElement>('[data-editor-invalid]');
    if (invalid) { reveal(invalid); invalid.focus(); }
  }).observe(submitErrors, {childList:true, attributes:true, attributeFilter:['hidden']});
  const firstError = form.querySelector<HTMLElement>('.has-error input, .has-error textarea, .has-error select, [aria-invalid="true"]');
  if (firstError) reveal(firstError);
  form.querySelector<HTMLElement>('[data-editor-category-search]')?.addEventListener('input', event => {
    const term = event.target instanceof HTMLInputElement ? event.target.value.trim().toLocaleLowerCase() : '';
    form.querySelectorAll<HTMLElement>('.admin-category-checklist > label').forEach(label => { label.hidden = !label.textContent.toLocaleLowerCase().includes(term); });
  });
  insert?.querySelector<HTMLElement>('[data-insert-search]')!.addEventListener('input', event => {
    const term = event.target instanceof HTMLInputElement ? event.target.value.trim().toLocaleLowerCase() : ''; let found = 0;
    insert.querySelectorAll<HTMLElement>('.admin-composer-inserter > button').forEach(button => { button.hidden = !button.textContent.toLocaleLowerCase().includes(term); if (!button.hidden) found++; });
    insert.querySelector<HTMLElement>('[data-insert-empty]')!.hidden = found > 0;
  });
  const menus = [...page.querySelectorAll<HTMLDetailsElement>('.editor-more, .editor-insert-menu')];
  menus.forEach(menu => {
    menu.addEventListener('toggle', () => { if (menu.open) menus.forEach(other => { if (other !== menu) other.open = false; }); });
    menu.addEventListener('click', event => { if (event.target instanceof Element && event.target.closest('button, a')) menu.open = false; });
  });
  document.addEventListener('pointerdown', event => menus.forEach(menu => { if (!(event.target instanceof Node) || !menu.contains(event.target)) menu.open = false; }));
  page.addEventListener('keydown', event => {
    if (event.key !== 'Escape' || document.querySelector<HTMLElement>('dialog[open]')) return;
    const menu = menus.find(item => item.open);
    if (menu) { event.preventDefault(); menu.open = false; if (menu.dataset.returnToDocument === 'true') { menu.dataset.returnToDocument = ''; window.ChunnelBlockEditor?.focus?.(); } else menu.querySelector<HTMLElement>('summary')!.focus(); }
    else if (page.classList.contains('is-focus')) setFocus(false);
  });
  const title = form.querySelector<HTMLTextAreaElement>('[name="title"]')!;
  let lastTitleWidth = 0;
  const fitTitle = () => { title.style.height = 'auto'; title.style.height = title.scrollHeight + 'px'; };
  title.addEventListener('input', fitTitle);
  const measureWorkspace = () => {
    const next = page.clientWidth < 1120;
    if (next !== narrow) { narrow = next; showSettings(!narrow && !page.classList.contains('is-focus')); }
    page.classList.toggle('is-narrow', narrow);
    page.style.setProperty('--editor-savebar-height', savebar.offsetHeight + 'px');
    page.style.setProperty('--editor-toolbar-height', toolbar.offsetHeight + 'px');
    if (title.clientWidth !== lastTitleWidth) { lastTitleWidth = title.clientWidth; fitTitle(); }
  };
  // Layout changes can resize these same observed elements. Apply them in the
  // next frame so WebKit does not drop notifications from a resize loop.
  let resizeFrame = 0;
  const resize = new ResizeObserver(() => {
    if (resizeFrame) return;
    resizeFrame = requestAnimationFrame(() => { resizeFrame = 0; measureWorkspace(); });
  });
  [page, savebar, toolbar, title].forEach(element => resize.observe(element)); fitTitle();
  window.addEventListener('pagehide', () => {
    resize.disconnect();
    cancelAnimationFrame(resizeFrame); resizeFrame = 0;
  });
  window.addEventListener('pageshow', event => {
    if (event.persisted) [page, savebar, toolbar, title].forEach(element => resize.observe(element));
  });

  const review = document.createElement('dialog');
  review.className = 'editor-publish-dialog'; review.setAttribute('aria-labelledby', 'editor-publish-title');
  review.innerHTML = '<header><h2 id="editor-publish-title">确认发布</h2></header><p data-publish-intro></p><dl data-publish-summary></dl><footer><button type="button" class="admin-button admin-button-quiet" data-publish-cancel>继续编辑</button><button type="button" class="admin-button admin-button-primary" data-publish-confirm>确认发布</button></footer>';
  form.append(review);
  let proposedStatus = '', confirmed = false, reviewedSnapshot = '';
  const publicationSnapshot = () => JSON.stringify([...new FormData(form)].filter(([name]) => name !== 'csrf_token'));
  const statusLabels: Record<string, string> = {publish:'公开发布', future:'定时发布', private:'设为私密', pending:'提交审核', draft:'存为草稿', trash:'移入回收站'};
  function openReview(next: string) {
    proposedStatus = next;
    const label = statusLabels[next] || '保存内容';
    review.querySelector<HTMLElement>('h2')!.textContent = '确认' + label;
    review.querySelector<HTMLElement>('[data-publish-intro]')!.textContent = next === 'publish' ? '当前正文和设置将一起保存，并对读者公开。' : '当前正文和设置将一起保存，并按以下状态处理。';
    const categories = [...form.querySelectorAll<HTMLElement>('[name="category_ids"]:checked')].map(input => input.closest('label')?.querySelector<HTMLElement>('b')?.textContent).filter(Boolean).join('、');
    const rows: [string, string][] = [['标题', title.value || '未填写'], ['状态', label], ['分类', categories || (form.querySelector<HTMLSelectElement>('[name="type"]')?.value === 'page' ? '页面无需分类' : '使用默认分类')], ['封面', form.querySelector<HTMLInputElement>('[name="cover_url"]')?.value ? '已设置' : '未设置']];
    if (next === 'future') rows.push(['发布时间', (schedule?.value.replace('T', ' ') || '尚未选择') + ' · ' + (form.querySelector<HTMLElement>('[data-published-timezone-label]')?.textContent || '')]);
    const summary = review.querySelector<HTMLElement>('[data-publish-summary]')!; summary.replaceChildren();
    rows.forEach(([label, value]) => { const row = document.createElement('div'), dt = document.createElement('dt'), dd = document.createElement('dd'); dt.textContent = label; dd.textContent = value; row.append(dt, dd); summary.append(row); });
    review.querySelector<HTMLElement>('[data-publish-confirm]')!.textContent = '确认' + label;
    reviewedSnapshot = publicationSnapshot();
    if (!review.open) review.showModal();
    modalState();
  }
  form.addEventListener('submit', event => {
    const action = (event.submitter instanceof HTMLButtonElement || event.submitter instanceof HTMLInputElement ? event.submitter.value : '') || 'save';
    if (confirmed || form.dataset.editorPendingSubmission === 'true' || action !== 'save' || !needsPublicationReview(canonical, status.value, canonicalTime, schedule?.value || '')) return;
    event.preventDefault(); event.stopImmediatePropagation(); openReview(status.value);
  }, true);
  publish?.addEventListener('click', () => { if (!save.disabled && form.dataset.editorPendingSubmission !== 'true') openReview(['future', 'private'].includes(status.value) ? status.value : 'publish'); });
  review.querySelector<HTMLElement>('[data-publish-cancel]')!.addEventListener('click', () => review.close());
  review.addEventListener('close', modalState);
  review.querySelector<HTMLElement>('[data-publish-confirm]')!.addEventListener('click', () => {
    if (save.disabled) return;
    if (reviewedSnapshot !== publicationSnapshot()) {
      openReview(proposedStatus);
      review.querySelector<HTMLElement>('[data-publish-intro]')!.textContent = '内容或设置已有变化，请核对更新后的信息，再次确认。';
      return;
    }
    review.close(); status.value = proposedStatus; status.dispatchEvent(new Event('change', {bubbles:true}));
    confirmed = true;
    try { form.requestSubmit(save); } finally { confirmed = false; }
  });
  function syncActions() {
    if (publish) { publish.hidden = canonical === 'publish' || canonical === 'trash'; publish.disabled = save.disabled || form.dataset.editorPendingSubmission === 'true'; }
    save.classList.toggle('admin-button-primary', canonical === 'publish' || !publish || Boolean(publish.hidden));
    save.classList.toggle('admin-button-quiet', canonical !== 'publish' && !!publish && !publish.hidden);
  }
  new MutationObserver(syncActions).observe(save, {attributes:true, attributeFilter:['disabled']}); syncActions();
  form.addEventListener('newpress:editor-saved', event => {
    const detail: unknown = (event as CustomEvent<unknown>).detail;
    if (!detail || typeof detail !== 'object') return;
    const saved = detail as Record<string, unknown>;
    if (typeof saved.status !== 'string' || !Object.hasOwn(statusLabels, saved.status) || typeof saved.publishedAt !== 'string' || typeof saved.version !== 'number' || !Number.isSafeInteger(saved.version) || saved.version < 1 || typeof saved.url !== 'string' || !/^\/admin\/content\/[1-9]\d*\/edit\/\?notice=saved$/.test(saved.url)) return;
    canonical = saved.status; canonicalTime = saved.publishedAt || ''; form.dataset.editorCanonicalStatus = canonical;
    if (canonicalBadge) { canonicalBadge.hidden = false; canonicalBadge.textContent = ({publish:'已发布', future:'等待定时', private:'私密内容', pending:'等待审核', draft:'草稿'} as Record<string, string>)[canonical] || canonical; canonicalBadge.className = 'admin-editor-state admin-editor-state-' + canonical; }
    const timeline = history.querySelector<HTMLElement>('.admin-revision-list');
    if (timeline) {
      const note = document.createElement('p'), link = document.createElement('a');
      note.textContent = `已保存到 v${saved.version}。`;
      link.href = saved.url; link.textContent = '重新载入最新版本历史';
      note.append(link); timeline.replaceChildren(note);
    }
    syncActions();
  });
})();
