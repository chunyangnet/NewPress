(() => {
  'use strict';
  const needsPublicationReview = (before, after, previousTime, nextTime) => before === after
    ? after === 'future' && previousTime !== nextTime
    : [before, after].some(status => ['publish', 'future', 'private'].includes(status));
  if (typeof module !== 'undefined') module.exports = {needsPublicationReview};
  if (typeof document === 'undefined') return;
  const page = document.querySelector('.admin-editor-page');
  const form = page?.querySelector('[data-content-editor]');
  if (!form) return;
  const sidebar = form.querySelector('#editor-settings');
  const toggle = form.querySelector('[data-editor-settings-toggle]');
  const focus = form.querySelector('[data-editor-focus]');
  const savebar = form.querySelector('[data-editor-savebar]');
  const toolbar = form.querySelector('[data-composer-toolbar]');
  const actions = form.querySelector('.editor-primary-actions');
  const status = form.querySelector('[data-content-status]');
  const schedule = form.querySelector('[name="published_at"]');
  const save = form.querySelector('button[value="save"]');
  const publish = form.querySelector('[data-editor-publish]');
  let canonical = form.dataset.editorCanonicalStatus || status.value;
  let canonicalTime = schedule?.value || '';
  let narrow = page.clientWidth < 1120, settingsOpen = !narrow, beforeFocus = settingsOpen;
  let selectedPanel = 'article';

  const preview = page.querySelector('[data-editor-preview]');
  if (preview) actions.insertBefore(preview, actions.firstChild);
  const publicLink = page.querySelector('.admin-editor-heading a[target="_blank"]');
  if (publicLink) form.querySelector('.editor-page-more .editor-more-menu').append(publicLink);
  const wechat = page.querySelector('.admin-callout a[href^="/admin/wechat/"]');
  if (wechat) {
    const callout = wechat.closest('.admin-callout');
    wechat.textContent = '同步到微信公众号';
    form.querySelector('.editor-page-more .editor-more-menu').append(wechat); callout.remove();
  }
  if (form.dataset.editorForceDirty !== 'true') page.querySelectorAll('.admin-content-notice-success').forEach(notice => { notice.hidden = true; });
  const canonicalBadge = page.querySelector('[data-editor-canonical-state]');
  if (canonicalBadge) form.querySelector('.admin-editor-save-copy').prepend(canonicalBadge);
  const aiPolicy = savebar.querySelector('[name="ai_pro_max"]')?.closest('label');
  if (aiPolicy) form.querySelector('#editor-publication-panel').append(aiPolicy);
  const topics = savebar.querySelector('.aipm-text-link');
  if (topics) form.querySelector('#editor-assistant-panel').append(topics);
  if (toolbar) form.insertBefore(toolbar, form.querySelector('.admin-editor-layout'));
  const insert = form.querySelector('.editor-insert-menu');
  if (insert && toolbar) toolbar.querySelector('.editor-insert-slot').append(insert);
  page.querySelector('.admin-editor-heading').hidden = true;
  page.classList.add('editor-enhanced');
  toggle.hidden = false; focus.hidden = false;
  form.querySelector('[data-document-insert]').hidden = false;

  const sidebarHome = document.createComment('settings position');
  sidebar.before(sidebarHome);
  const drawer = document.createElement('dialog');
  drawer.className = 'editor-settings-drawer'; drawer.setAttribute('aria-label', '文章设置');
  form.append(drawer);
  const modalState = () => document.body.classList.toggle('editor-modal-open', !!document.querySelector('dialog[open]'));
  function showSettings(open, returnFocus = false) {
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
  sidebar.querySelector('[data-editor-settings-close]').addEventListener('click', () => showSettings(false, true));
  toggle.addEventListener('click', () => {
    if (page.classList.contains('is-focus')) { setFocus(false); showSettings(true); }
    else showSettings(!settingsOpen);
  });
  function setFocus(active) {
    if (active) beforeFocus = settingsOpen;
    page.classList.toggle('is-focus', active); document.body.classList.toggle('is-editor-focused', active);
    focus.setAttribute('aria-pressed', String(active));
    focus.querySelector('span').textContent = active ? '退出专注' : '专注';
    form.querySelector('[data-editor-mobile-focus]').textContent = active ? '退出专注写作' : '专注写作';
    focus.title = active ? '退出专注写作' : '专注写作';
    showSettings(active ? false : beforeFocus);
  }
  focus.addEventListener('click', () => setFocus(!page.classList.contains('is-focus')));
  form.querySelector('[data-editor-mobile-focus]').addEventListener('click', () => setFocus(!page.classList.contains('is-focus')));
  function showPanel(name) {
    selectedPanel = name;
    sidebar.querySelectorAll('[data-editor-settings-panel]').forEach(panel => {
      panel.hidden = panel.dataset.editorSettingsPanel !== name;
      if (panel.tagName === 'DETAILS' && !panel.hidden) panel.open = true;
    });
    sidebar.querySelectorAll('[data-editor-settings-tab]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.editorSettingsTab === name)));
  }
  sidebar.querySelectorAll('[data-editor-settings-tab]').forEach(button => button.addEventListener('click', () => showPanel(button.dataset.editorSettingsTab)));
  form.querySelector('[data-editor-open-assistant]')?.addEventListener('click', () => {
    const panel = form.querySelector('[data-ai-paragraph]');
    if (panel) { panel.open = true; panel.scrollIntoView({block: 'center', behavior: 'smooth'}); panel.querySelector('select, button')?.focus({preventScroll: true}); }
  });
  showPanel(selectedPanel); showSettings(settingsOpen);

  // Settings stay in their original form even when displayed in a native modal.
  const wide = document.createElement('dialog');
  wide.className = 'editor-wide-dialog'; wide.setAttribute('aria-labelledby', 'editor-wide-title');
  wide.innerHTML = '<header><div><h2 id="editor-wide-title"></h2><p>设置会随正文一起保存</p></div><button type="button" data-wide-close aria-label="关闭设置">×</button></header><div class="editor-wide-body"></div><footer><button type="button" class="admin-button admin-button-primary" data-wide-done>完成设置</button></footer>';
  form.append(wide);
  let activeGroup = null, groupHome = null, groupLauncher = null, reopenDrawer = false;
  let history = form.querySelector('[data-editor-wide="history"]');
  if (!history) {
    history = document.createElement('details'); history.className = 'editor-settings-group'; history.dataset.editorWide = 'history';
    history.innerHTML = '<summary>版本与恢复</summary><p class="editor-history-hint">首次保存后可查看正式版本历史。</p>';
    form.querySelector('.editor-below-grid').append(history);
  }
  const recovery = page.querySelector('[data-browser-recovery]');
  if (recovery) history.append(recovery);
  function restoreWide() {
    if (!activeGroup) return;
    activeGroup.open = false;
    activeGroup.querySelectorAll(':scope > details').forEach(panel => { panel.open = false; });
    groupHome.replaceWith(activeGroup); activeGroup = null;
    if (reopenDrawer && narrow && settingsOpen && !drawer.open) drawer.showModal();
    reopenDrawer = false; groupLauncher?.focus(); modalState();
  }
  function closeWide() { if (wide.open) wide.close(); restoreWide(); }
  function openWide(group, launcher) {
    if (activeGroup) closeWide();
    activeGroup = group; groupLauncher = launcher; groupHome = document.createComment('settings group position');
    group.before(groupHome); group.open = true;
    group.querySelectorAll(':scope > details').forEach(panel => { panel.open = true; });
    wide.querySelector('h2').textContent = group.querySelector('summary').textContent.trim();
    wide.querySelector('.editor-wide-body').append(group);
    reopenDrawer = drawer.open;
    if (drawer.open) drawer.close();
    wide.showModal(); modalState();
  }
  wide.addEventListener('close', () => { if (!wide.open) restoreWide(); });
  wide.querySelector('[data-wide-close]').addEventListener('click', closeWide);
  wide.querySelector('[data-wide-done]').addEventListener('click', closeWide);
  form.querySelectorAll('[data-editor-wide]').forEach(group => {
    const summary = group.querySelector(':scope > summary');
    summary.setAttribute('aria-haspopup', 'dialog');
    summary.addEventListener('click', event => { event.preventDefault(); openWide(group, summary); });
    group.open = false;
  });
  form.querySelector('[data-editor-open-history]').addEventListener('click', event => openWide(history, event.currentTarget));

  function reveal(target) {
    if (!(target instanceof Element)) return;
    if (target.matches('[data-content-source]')) window.ChunnelBlockEditor?.setMode?.('source');
    const group = target.closest('[data-editor-wide]');
    if (group && activeGroup !== group) openWide(group, group.querySelector('summary'));
    else if (sidebar.contains(target)) {
      const panel = target.closest('[data-editor-settings-panel]');
      if (panel) showPanel(panel.dataset.editorSettingsPanel);
      showSettings(true);
    }
    for (let node = target.parentElement; node && node !== form; node = node.parentElement) if (node.tagName === 'DETAILS') node.open = true;
  }
  form.addEventListener('invalid', event => reveal(event.target), true);
  form.addEventListener('newpress:editor-reveal', event => { if (form.contains(event.detail?.target)) reveal(event.detail.target); });
  form.addEventListener('focusin', event => { if (!event.target.closest('summary')) reveal(event.target); });
  const submitErrors = form.querySelector('[data-editor-submit-errors]');
  if (submitErrors) new MutationObserver(() => {
    if (submitErrors.hidden) return;
    const invalid = form.querySelector('[data-editor-invalid]');
    if (invalid) { reveal(invalid); invalid.focus(); }
  }).observe(submitErrors, {childList:true, attributes:true, attributeFilter:['hidden']});
  const firstError = form.querySelector('.has-error input, .has-error textarea, .has-error select, [aria-invalid="true"]');
  if (firstError) reveal(firstError);
  form.querySelector('[data-editor-category-search]')?.addEventListener('input', event => {
    const term = event.target.value.trim().toLocaleLowerCase();
    form.querySelectorAll('.admin-category-checklist > label').forEach(label => { label.hidden = !label.textContent.toLocaleLowerCase().includes(term); });
  });
  insert?.querySelector('[data-insert-search]').addEventListener('input', event => {
    const term = event.target.value.trim().toLocaleLowerCase(); let found = 0;
    insert.querySelectorAll('.admin-composer-inserter > button').forEach(button => { button.hidden = !button.textContent.toLocaleLowerCase().includes(term); if (!button.hidden) found++; });
    insert.querySelector('[data-insert-empty]').hidden = found > 0;
  });
  const menus = [...page.querySelectorAll('.editor-more, .editor-insert-menu')];
  menus.forEach(menu => {
    menu.addEventListener('toggle', () => { if (menu.open) menus.forEach(other => { if (other !== menu) other.open = false; }); });
    menu.addEventListener('click', event => { if (event.target.closest('button, a')) menu.open = false; });
  });
  document.addEventListener('pointerdown', event => menus.forEach(menu => { if (!menu.contains(event.target)) menu.open = false; }));
  page.addEventListener('keydown', event => {
    if (event.key !== 'Escape' || document.querySelector('dialog[open]')) return;
    const menu = menus.find(item => item.open);
    if (menu) { event.preventDefault(); menu.open = false; if (menu.dataset.returnToDocument === 'true') { menu.dataset.returnToDocument = ''; window.ChunnelBlockEditor?.focus?.(); } else menu.querySelector('summary').focus(); }
    else if (page.classList.contains('is-focus')) setFocus(false);
  });
  const title = form.querySelector('[name="title"]');
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

  const review = document.createElement('dialog');
  review.className = 'editor-publish-dialog'; review.setAttribute('aria-labelledby', 'editor-publish-title');
  review.innerHTML = '<header><h2 id="editor-publish-title">确认发布</h2></header><p data-publish-intro></p><dl data-publish-summary></dl><footer><button type="button" class="admin-button admin-button-quiet" data-publish-cancel>继续编辑</button><button type="button" class="admin-button admin-button-primary" data-publish-confirm>确认发布</button></footer>';
  form.append(review);
  let proposedStatus = null, confirmed = false;
  const statusLabels = {publish:'公开发布', future:'定时发布', private:'设为私密', pending:'提交审核', draft:'存为草稿', trash:'移入回收站'};
  function openReview(next) {
    proposedStatus = next;
    const label = statusLabels[next] || '保存内容';
    review.querySelector('h2').textContent = '确认' + label;
    review.querySelector('[data-publish-intro]').textContent = next === 'publish' ? '当前正文和设置将一起保存，并对读者公开。' : '当前正文和设置将一起保存，并按以下状态处理。';
    const categories = [...form.querySelectorAll('[name="category_ids"]:checked')].map(input => input.closest('label').querySelector('b')?.textContent).filter(Boolean).join('、');
    const rows = [['标题', title.value || '未填写'], ['状态', label], ['分类', categories || (form.querySelector('[name="type"]')?.value === 'page' ? '页面无需分类' : '使用默认分类')], ['封面', form.querySelector('[name="cover_url"]')?.value ? '已设置' : '未设置']];
    if (next === 'future') rows.push(['发布时间', (schedule?.value.replace('T', ' ') || '尚未选择') + ' · ' + (form.querySelector('[data-published-timezone-label]')?.textContent || '')]);
    const summary = review.querySelector('[data-publish-summary]'); summary.replaceChildren();
    rows.forEach(([label, value]) => { const row = document.createElement('div'), dt = document.createElement('dt'), dd = document.createElement('dd'); dt.textContent = label; dd.textContent = value; row.append(dt, dd); summary.append(row); });
    review.querySelector('[data-publish-confirm]').textContent = '确认' + label;
    review.showModal(); modalState();
  }
  form.addEventListener('submit', event => {
    const action = event.submitter?.value || 'save';
    if (confirmed || form.dataset.editorPendingSubmission === 'true' || action !== 'save' || !needsPublicationReview(canonical, status.value, canonicalTime, schedule?.value || '')) return;
    event.preventDefault(); event.stopImmediatePropagation(); openReview(status.value);
  }, true);
  publish?.addEventListener('click', () => { if (!save.disabled && form.dataset.editorPendingSubmission !== 'true') openReview(['future', 'private'].includes(status.value) ? status.value : 'publish'); });
  review.querySelector('[data-publish-cancel]').addEventListener('click', () => review.close());
  review.addEventListener('close', modalState);
  review.querySelector('[data-publish-confirm]').addEventListener('click', () => {
    if (save.disabled) return;
    review.close(); status.value = proposedStatus; status.dispatchEvent(new Event('change', {bubbles:true}));
    confirmed = true;
    try { form.requestSubmit(save); } finally { confirmed = false; }
  });
  function syncActions() {
    if (publish) { publish.hidden = canonical === 'publish' || canonical === 'trash'; publish.disabled = save.disabled || form.dataset.editorPendingSubmission === 'true'; }
    save.classList.toggle('admin-button-primary', canonical === 'publish' || !publish || publish.hidden);
    save.classList.toggle('admin-button-quiet', canonical !== 'publish' && !!publish && !publish.hidden);
  }
  new MutationObserver(syncActions).observe(save, {attributes:true, attributeFilter:['disabled']}); syncActions();
  form.addEventListener('newpress:editor-saved', event => {
    canonical = event.detail.status; canonicalTime = event.detail.publishedAt || ''; form.dataset.editorCanonicalStatus = canonical;
    if (canonicalBadge) { canonicalBadge.hidden = false; canonicalBadge.textContent = ({publish:'已发布', future:'等待定时', private:'私密内容', pending:'等待审核', draft:'草稿'})[canonical] || canonical; canonicalBadge.className = 'admin-editor-state admin-editor-state-' + canonical; }
    const timeline = history.querySelector('.admin-revision-list');
    if (timeline) {
      const note = document.createElement('p'), link = document.createElement('a');
      note.textContent = `已保存到 v${event.detail.version}。`;
      link.href = event.detail.url; link.textContent = '重新载入最新版本历史';
      note.append(link); timeline.replaceChildren(note);
    }
    syncActions();
  });
})();
