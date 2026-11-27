(() => {
  'use strict';
  const root = document.querySelector('[data-wechat]');
  if (!root) return;
  const one = selector => root.querySelector(selector);
  const configForm = one('[data-wechat-config]'), syncForm = one('[data-wechat-sync]'), collectForm = one('[data-wechat-collect]');
  const status = one('[data-wechat-status]'), jobsNode = one('[data-wechat-jobs]'), collection = one('[data-wechat-collection]');
  const applyButton = one('[data-wechat-apply]'), applyWrap = one('[data-wechat-apply-wrap]'), dialog = one('[data-wechat-confirm]');
  const historyForm = one('[data-wechat-job-filters]'), prev = one('[data-wechat-prev]'), next = one('[data-wechat-next]');
  const selectAll = one('[data-wechat-select-all]'), resultsNode = one('[data-wechat-import-results]');
  const recovery = one('[data-wechat-recovery]'), recoverButton = one('[data-wechat-recover]'), discardButton = one('[data-wechat-discard]');
  const checkButton = one('[data-wechat-check]'), checkResult = one('[data-wechat-check-result]');
  const stopReadButton = one('[data-wechat-stop-read]');
  const ownerID = Number(root.dataset.userId || 0);
  const readonly = root.dataset.readonly === 'true', api = '/api/v1/admin/wechat';
  const checks = ['enabled', 'collect_enabled', 'collect_drafts', 'auto_sync', 'auto_publish', 'include_source', 'open_comment', 'fans_comment'];
  const fields = ['app_id', 'author', 'thumb_media_id'];
  const labels = { queued: '等待处理', working: '同步中', draft: '已存草稿', publishing: '发表中', published: '已发表', failed: '失败', uncertain: '结果待核对', skipped: '已停止' };
  const readActions = {
    preview: { label: '预览', stopped: '已停止预览。输入链接、上次预览和选择已保留，可继续核对或重新采集。' },
    check: { label: '检查', stopped: '已停止检查。上次检查结果已保留，可重新检查当前文章。' },
    connection: { label: '连接测试', stopped: '已停止连接测试。本页设置已保留，可以稍后重新测试。' },
  };
  let config = null, formVersion = 0, posts = [], jobs = [], report = null;
  let busy = false, dirty = false, canSync = false, canCollect = false, authLost = false, disposed = false;
  let activeTab = 'sync', timer = 0, readController = null, readSequence = 0, jobsSignature = '';
  let jobPage = 1, jobPages = 1, activeJobs = 0, jobQuery = '', jobStatus = '';
  let selectedKeys = new Set(), expiryTimer = 0;
  let reviewKeys = new Set(), previewing = false;
  let initialConfig = '', collectionBaseline = '', pageEpoch = 0, recovering = false, interruptedWrite = false;
  let syncCheck = null;
  let readOperation = null;
  let configSaving = false, configFeedback = '', configError = false;
  const maxCollectionImages = 50;
  const draftStore = ownerID > 0 ? window.ChunnelWeChatDrafts?.create(ownerID) : null;
  const draftPanel = one('[data-wechat-collection-draft]'), draftEnable = one('[data-wechat-draft-enable]');
  const draftStatus = one('[data-wechat-draft-status]'), draftRestore = one('[data-wechat-draft-restore]'), draftClear = one('[data-wechat-draft-clear]');
  let draftRead = false, draftOpted = false, draftTimer = 0, savedDraft = null, restoreCandidate = null, restoredChoices = null, draftUncertain = false;
  let collectionFingerprints = new Map();
  const requests = new Map();
  let initialPost = new URLSearchParams(location.search).get('post') || '';
  if (!/^[1-9]\d*$/.test(initialPost)) initialPost = '';
  const make = (tag, text, cls) => { const node = document.createElement(tag); if (text !== undefined) node.textContent = text; if (cls) node.className = cls; return node; };
  const message = (text, error = false) => { status.textContent = text; status.classList.toggle('is-error', error); if (activeTab === 'config') { configFeedback = text; configError = error; paintConfig(); } };
  function paintConfig() {
    window.ChunnelAdminUI?.paintSavebar(one('[data-admin-savebar]'), {dirty, busy: configSaving, disabled: busy || recovering || disposed || authLost || !config, readOnly: readonly, message: configFeedback, error: configError});
  }
  const date = value => window.ChunnelTime?.formatDateTime(value) || '';
  const configValues = () => Object.fromEntries([...fields, 'app_secret', ...checks, 'clear_secret'].map(name => [name,
    checks.includes(name) || name === 'clear_secret' ? configForm.elements[name].checked : configForm.elements[name].value]));
  function normalizeCollectionURL(raw) {
    if (raw.length > 2048 || raw.includes('\\')) return null;
    try {
      const url = new URL(raw);
      if (!['http:', 'https:'].includes(url.protocol) || url.hostname !== 'mp.weixin.qq.com' || url.username || url.password || url.port && url.port !== '443') return null;
      if (/^\/s\/[A-Za-z0-9_-]{5,100}$/.test(url.pathname)) return 'https://mp.weixin.qq.com' + url.pathname;
      if (url.pathname !== '/s' && url.pathname !== '/s/') return null;
      const query = new URLSearchParams();
      for (const name of ['__biz', 'idx', 'mid', 'sn']) {
        const values = url.searchParams.getAll(name);
        if (values.length !== 1 || !values[0] || values[0].length > 160) return null;
        query.set(name, values[0]);
      }
      return 'https://mp.weixin.qq.com/s?' + query.toString();
    } catch { return null; }
  }
  function parseCollectionInput(input) {
    // Keep the textarea untouched. Unsupported addresses stay in the batch so
    // the server can report each failure alongside successful articles.
    const text = input.replace(/&(?:amp|#0*38|#x0*26);/gi, '&');
    const matches = text.match(/https?:\/\/[^\s<>"'“”‘’()[\]{}（）【】《》，。；！？,]+/gi);
    const candidates = matches ? matches.map(value => value.replace(/[.;!]+$/, '')) : text.split(/\r?\n/).map(value => value.trim()).filter(Boolean);
    const seen = new Set(); let duplicates = 0, unchecked = 0;
    for (const raw of candidates) {
      const normalized = normalizeCollectionURL(raw), url = normalized || raw;
      if (seen.has(url)) { duplicates++; continue; }
      seen.add(url);
      if (!normalized) unchecked++;
    }
    return { urls: [...seen], duplicates, unchecked };
  }
  let parsedCollection = null;
  function collectionLinks() {
    const input = collectForm.elements.urls.value;
    if (!parsedCollection || parsedCollection.input !== input) parsedCollection = { input, ...parseCollectionInput(input) };
    return parsedCollection;
  }
  const collectionURLs = () => collectionLinks().urls;
  const collectionInput = () => collectionURLs().join('\n');
  const collectionStale = () => !!report && collectionInput() !== collectionBaseline;
  function paintCollectionInput() {
    const { urls, duplicates, unchecked } = collectionLinks(), over = urls.length > 10;
    one('[data-wechat-input-state]').dataset.state = over ? 'over' : urls.length ? 'ready' : 'empty';
    const count = one('[data-wechat-input-count]'), label = `${urls.length} / 10`;
    if (count.textContent !== label) count.textContent = label;
    const notes = [over ? `请减少 ${urls.length - 10} 个链接后分批采集。` : urls.length ? '先预览，再选择要导入的文章。' : '支持粘贴分享文案或多个链接，重复分享链接自动合并。'];
    if (duplicates) notes.push(`已合并 ${duplicates} 个重复链接。`);
    if (unchecked) notes.push(`${unchecked} 项地址需核对，预览时将逐条提示。`);
    const note = one('[data-wechat-input-note]'), copy = notes.join(' ');
    if (note.textContent !== copy) note.textContent = copy;
    collectForm.elements.urls.setAttribute('aria-invalid', String(over));
    return over;
  }
  function configState() {
    const conflict = dirty && config && formVersion !== config.version;
    one('[data-wechat-config-state]').textContent = conflict ? '服务器配置版本已变化，本页修改尚未保存。请核对或重新载入设置。' : dirty ? '未保存的设置尚未生效。' : config ? '' : '正在读取配置…';
    one('[data-wechat-dirty]').hidden = !dirty;
    discardButton.hidden = !dirty;
    paintConfig();
    updateCheckState();
  }
  function requireRecovery(copy) {
    authLost = true; clearTimeout(timer); recovery.hidden = false;
    one('[data-wechat-recovery-copy]').textContent = copy;
    message(copy, true); lock();
  }
  function validConfig(value) {
    return value && Number.isSafeInteger(value.version) && value.version > 0 && fields.every(name => typeof value[name] === 'string') &&
      checks.every(name => typeof value[name] === 'boolean' || name === 'collect_drafts' && value[name] === undefined) && (!value.app_secret) && (value.has_secret === undefined || typeof value.has_secret === 'boolean');
  }
  function draftMessage(text, error = false) { draftStatus.textContent = text; draftStatus.classList.toggle('is-error', error); }
  function paintDraft() {
    draftPanel.hidden = !draftStore || !config?.collect_drafts || !canCollect || readonly;
    draftEnable.checked = draftOpted;
    const blocked = busy || recovering || authLost || disposed || readonly;
    draftEnable.disabled = blocked; draftRestore.disabled = blocked || !config?.collect_enabled; draftClear.disabled = blocked;
    one('[data-wechat-draft-saved]').hidden = !savedDraft;
    draftRestore.hidden = !restoreCandidate;
    one('[data-wechat-draft-summary]').textContent = savedDraft ? `${restoreCandidate ? '可恢复' : '已暂存'} ${parseCollectionInput(savedDraft.input).urls.length} 个链接 · ${date(savedDraft.at)}${savedDraft.uncertain ? ' · 上次导入结果需重新预览核对' : ''}` : '';
  }
  function clearDraft() {
    clearTimeout(draftTimer); draftOpted = false; savedDraft = null; restoreCandidate = null; restoredChoices = null;
    try { draftStore?.clear(); draftMessage('已停止暂存并清除此标签页的采集记录，当前输入不变。'); }
    catch { draftMessage('已在当前页面停止暂存，但浏览器未能清除旧记录，请检查存储权限。', true); }
    paintDraft();
  }
  function syncDraftPolicy() {
    if (!draftStore) return;
    if (!config.collect_drafts) { clearDraft(); draftRead = true; return; }
    if (!canCollect || readonly || authLost) { paintDraft(); return; }
    if (!draftRead) {
      draftRead = true;
      try {
        savedDraft = draftStore.read(); draftOpted = !!savedDraft;
        restoreCandidate = savedDraft?.input ? savedDraft : null;
        if (restoreCandidate) draftMessage('有上次的采集进度。恢复不会读取远程文章或执行导入。');
      } catch { draftMessage('浏览器暂存不可用，当前输入仍可正常采集；请保持本页打开。', true); }
    }
    paintDraft();
  }
  function saveCollectionDraft() {
    clearTimeout(draftTimer);
    if (!draftStore || !draftOpted || !config?.collect_drafts || !canCollect || authLost || readonly) return;
    if (restoreCandidate) { draftMessage('上次进度尚待处理。请先恢复，或清除旧记录后开启本次暂存。'); return; }
    const baseline = report ? collectionBaseline : restoredChoices?.baseline ?? collectionBaseline;
    const choicesByKey = new Map((restoredChoices?.baseline === baseline ? restoredChoices.choices : []).map(choice => [choice.key, choice]));
    (report?.articles || []).forEach(article => {
      if (collectionFingerprints.has(article.key)) choicesByKey.set(article.key, { key: article.key, digest: collectionFingerprints.get(article.key), selected: selectedKeys.has(article.key) });
      else choicesByKey.delete(article.key);
    });
    const choices = [...choicesByKey.values()].slice(-10);
    try {
      savedDraft = draftStore.save(collectForm.elements.urls.value, baseline, choices, draftUncertain);
      if (!savedDraft) { draftOpted = false; draftMessage('暂存记录已超过 24 小时并清除，当前输入和预览仍保留；可重新开启暂存。'); }
      else draftMessage(report?.articles.length && !choices.length ? '链接已暂存；当前浏览器无法保留内容核对指纹，恢复后需重新勾选。' : '采集进度已暂存于此标签页，刷新后可手动恢复。');
    } catch (error) { draftMessage(error.message || '暂存失败，当前输入与上次可用副本仍保留。', true); }
    paintDraft();
  }
  function scheduleDraft() {
    clearTimeout(draftTimer);
    if (draftOpted && !disposed) draftTimer = setTimeout(saveCollectionDraft, 500);
  }
  function lock() {
    const blocked = busy || recovering || disposed || readonly || authLost || !config;
    stopReadButton.hidden = !busy || !readOperation || disposed;
    stopReadButton.disabled = !readOperation || readOperation.controller.signal.aborted;
    stopReadButton.textContent = readOperation ? '停止' + readActions[readOperation.kind].label : '停止读取';
    configForm.querySelector('fieldset').disabled = blocked;
    syncForm.querySelector('fieldset').disabled = blocked || !canSync;
    one('[data-wechat-send]').disabled = blocked || !canSync || !config?.enabled || !syncForm.elements.post_id.value;
    checkButton.disabled = blocked || !canSync || !config?.enabled || !syncForm.elements.post_id.value;
    collectForm.querySelector('fieldset').disabled = blocked || !canCollect || !config?.collect_enabled;
    const overLinkLimit = paintCollectionInput();
    one('[data-wechat-preview]').disabled = blocked || !canCollect || !config?.collect_enabled || overLinkLimit;
    const expired = report?.expires && Date.parse(report.expires) <= Date.now();
    const stale = collectionStale();
    applyButton.disabled = blocked || !canCollect || !config?.collect_enabled || !selectedKeys.size || !!expired || stale || selectedImageCount() > maxCollectionImages;
    selectAll.disabled = blocked || !canCollect || !config?.collect_enabled || !!expired || stale;
    collection.querySelectorAll('input[type=checkbox]').forEach(input => { input.disabled = blocked || !canCollect || !config?.collect_enabled || !!expired || stale; });
    collection.querySelectorAll('[data-wechat-retry]').forEach(button => { button.disabled = blocked || !canCollect || !config?.collect_enabled || stale; });
    historyForm.querySelectorAll('input,select,button').forEach(input => { input.disabled = busy || recovering || authLost || !config; });
    prev.disabled = busy || recovering || authLost || !config || jobPage <= 1; next.disabled = busy || recovering || authLost || !config || jobPage >= jobPages;
    one('[data-wechat-refresh]').disabled = busy || recovering || authLost;
    recoverButton.disabled = busy || recovering || disposed || readonly;
    discardButton.disabled = blocked;
    paintConfig();
    jobsNode.querySelectorAll('button').forEach(button => {
      const action = button.dataset.action;
      button.disabled = blocked || !canSync || (action !== 'cancel' &&
        (button.dataset.account !== config.app_id || (!['resolve', 'draft_only'].includes(action) && !config.enabled)));
    });
    root.setAttribute('aria-busy', String(busy || recovering));
    updateCheckState();
    paintDraft();
  }
  const request = (path, body, signal) => requestURL(api + path, body, signal);
  async function requestURL(url, body, signal) {
    if (disposed || signal?.aborted || authLost && body !== undefined) throw new DOMException('Cancelled', 'AbortError');
    const epoch = pageEpoch, controller = new AbortController(), timeout = setTimeout(() => controller.abort(), url === '/api/v1/admin/session' ? 15000 : 60000);
    requests.set(controller, body !== undefined && !['/sync/check', '/collect/preview', '/test'].some(path => url === api + path));
    const abort = () => controller.abort();
    if (signal?.aborted) controller.abort();
    else signal?.addEventListener('abort', abort, { once: true });
    try {
      const response = await fetch(url, {
        method: body === undefined ? 'GET' : 'POST', credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: controller.signal,
        headers: body === undefined ? { Accept: 'application/json' } : { Accept: 'application/json', 'Content-Type': 'application/json', 'X-CSRF-Token': root.dataset.csrf },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      });
      if (disposed || epoch !== pageEpoch || controller.signal.aborted) throw new DOMException('Cancelled', 'AbortError');
      if (response.status === 401) { requireRecovery('登录已失效。请在新窗口登录原账号，再回到这里恢复；本页输入和采集选择已保留。'); throw new Error('登录已失效，请重新登录后在本页恢复。'); }
      let data;
      try { data = await response.json(); } catch (error) { if (controller.signal.aborted) throw error; throw new Error('服务器响应不完整，请刷新同步记录或重新预览后核对结果。'); }
      if (disposed || epoch !== pageEpoch || controller.signal.aborted) throw new DOMException('Cancelled', 'AbortError');
      if (!response.ok) {
        if (response.status === 403) requireRecovery('权限或登录凭证已变化，请核对原账号后恢复。本页输入和采集选择已保留。');
        throw new Error(data.error?.message || data.message || '操作未完成，请稍后重试。');
      }
      return data;
    } catch (error) {
      if (disposed || epoch !== pageEpoch || signal?.aborted) throw new DOMException('Cancelled', 'AbortError');
      if (controller.signal.aborted) throw new Error(url === '/api/v1/admin/session' ? '登录检查超时，本页输入已保留，请重试。' : '请求超时。同步请先刷新记录核对；采集导入请重新预览，已导入文章会自动跳过。');
      if (error.name === 'TypeError') throw new Error(url === '/api/v1/admin/session' ? '登录检查未完成，请检查网络后重试。' : '请求未能完成，请检查网络。当前输入与预览已保留；同步或导入请先核对结果，再手动重试。');
      throw error;
    } finally { clearTimeout(timeout); requests.delete(controller); signal?.removeEventListener('abort', abort); }
  }
  async function checkSession(signal) {
    const session = await requestURL('/api/v1/admin/session', undefined, signal);
    if (session?.authenticated !== true || session.read_only !== false || !Array.isArray(session.role_capabilities) ||
        typeof session.csrf_token !== 'string' || session.csrf_token.length < 32 || session.csrf_token.length > 256) throw new Error('无法确认登录权限，当前输入已保留，请重试。');
    if (ownerID <= 0 || session.user_id !== ownerID) {
      requireRecovery('当前账号已切换。请在新窗口登录原编辑账号，再回到本页恢复；本页内容不会发送给新账号。'); return false;
    }
    const can = name => session.role_capabilities.some(value => typeof value === 'string' && (value === '*' || value === name || value.endsWith(':*') && name.startsWith(value.slice(0, -1))));
    if (!can('admin:access') || !can('settings:manage')) { requireRecovery('当前账号没有公众号设置权限，请联系管理员；本页输入已保留。'); return false; }
    canSync = can('content:write'); canCollect = canSync && can('taxonomy:manage') && can('media:write');
    root.dataset.csrf = session.csrf_token;
    authLost = false;
    return true;
  }
  async function recoverWorkspace() {
    if (busy || recovering || disposed || readonly) return;
    const epoch = pageEpoch; recovering = true; lock();
    try {
      if (!await checkSession()) return;
      if (!await load(true, true) || epoch !== pageEpoch || disposed) return;
      recovery.hidden = true; updateSelection();
      message(interruptedWrite ? '登录已确认，输入与选择已保留。上次操作结果需先核对同步记录、已保存设置或导入预览，再手动继续。' : '登录已确认，输入和采集选择已保留，请核对后手动继续。');
    } catch (error) { if (epoch === pageEpoch && !disposed) { recovery.hidden = false; message(error.message, true); } }
    finally { if (epoch === pageEpoch) { recovering = false; lock(); schedule(); } }
  }
  recoverButton.addEventListener('click', recoverWorkspace);
  function fillConfig(value) {
    fields.forEach(name => { configForm.elements[name].value = value[name] || ''; });
    checks.forEach(name => { configForm.elements[name].checked = !!value[name]; });
    configForm.elements.app_secret.value = '';
    configForm.elements.clear_secret.checked = false;
    one('[data-wechat-secret-hint]').textContent = value.has_secret ? '已保存密钥；留空保持原值，更换 AppID 时需重新填写。' : '密钥只保存在服务端。';
    formVersion = value.version; initialConfig = JSON.stringify(configValues()); dirty = false; configState();
  }
  function showConfig() {
    const connected = config.enabled && config.has_secret;
    const connection = one('[data-wechat-connection]');
    connection.textContent = connected ? `同步已开启 · ${config.auto_sync ? '新文章自动同步' : '仅手动同步'} · ${config.auto_publish ? '自动发表' : '草稿箱'}` : config.enabled ? '待配置：请在账号与设置中补全公众号密钥' : '同步已暂停 · 前往账号与设置开启';
    connection.classList.toggle('is-connected', connected);
    one('[data-wechat-sync-hint]').textContent = config.auto_publish ? '当前设置：同步后自动发表。提交前请确认文章已可公开。' : '默认同步到草稿箱，排版确认后再发表。';
    one('[data-wechat-send]').textContent = config.auto_publish ? '同步并自动发表' : '同步到草稿箱';
  }
  function renderPosts(selected) {
    const select = syncForm.elements.post_id;
    select.replaceChildren(make('option', posts.length ? '请选择文章' : '没有匹配文章，请更换关键词'));
    select.firstChild.value = '';
    posts.forEach(post => { const option = make('option', `${post.title} · ${post.status === 'publish' ? '已发布' : post.status === 'future' ? '等待定时' : '草稿'} · v${post.version}`); option.value = String(post.id); select.append(option); });
    select.value = posts.some(post => String(post.id) === selected) ? selected : '';
  }
  function renderJobs() {
    const signature = JSON.stringify([config.app_id, !!config.enabled, !!config.auto_publish, jobQuery, jobStatus, jobs]);
    if (signature === jobsSignature) return;
    jobsSignature = signature;
    const focused = jobsNode.contains(document.activeElement) ? document.activeElement : null;
    const focusID = focused?.dataset.id, focusAction = focused?.dataset.action;
    const fragment = document.createDocumentFragment();
    jobs.forEach(job => {
      const row = make('article', undefined, 'wechat-job'), info = make('div'), actions = make('div', undefined, 'wechat-job-actions');
      info.append(make('h3', job.title));
      const badge = make('span', labels[job.status] || job.status, 'wechat-badge'); badge.dataset.state = job.status;
      info.append(badge, make('p', `v${job.version} · ${job.actor} · ${date(job.updated_at)}`));
      info.append(make('p', '任务方式：' + (job.auto_publish ? '同步后发表' : '同步到草稿箱')));
      if (!job.publish_id && ['queued', 'failed', 'skipped'].includes(job.status) && !!job.auto_publish !== !!config.auto_publish) info.append(make('p', '此任务保留创建时确认的方式，当前默认设置仅用于新任务。'));
      if (job.status === 'queued' && !config.enabled && job.app_id === config.app_id) info.append(make('p', '同步模块已关闭，开启后继续处理。'));
      if (job.app_id !== config.app_id) info.append(make('p', '属于另一公众号；切换回对应账号后可继续操作。'));
      if (job.media_id) info.append(make('p', `草稿素材 ID：${job.media_id}`));
      if (job.publish_id) info.append(make('p', `发表任务 ID：${job.publish_id}`));
      if (job.error) info.append(make('p', job.error, 'wechat-job-error'));
      const action = (name, text) => { const button = make('button', text, 'admin-button'); button.type = 'button'; Object.assign(button.dataset, { action: name, id: job.id, account: job.app_id }); actions.append(button); };
      if (job.status === 'failed') action('retry', job.publish_id ? '重新查询结果' : '重试');
      if (job.status === 'draft') action('publish', '发表草稿');
      if (job.status === 'publishing') action('refresh', '查询发表结果');
      if (job.status === 'uncertain') action('resolve', '核对并补录');
      if (job.status === 'skipped' && !job.error) action('resume', job.publish_id ? '继续查询结果' : job.auto_publish ? '继续同步并发表' : '继续同步到草稿箱');
      if (job.auto_publish && !job.publish_id && ['queued', 'failed', 'skipped'].includes(job.status)) action('draft_only', '改为仅草稿');
      if (job.status === 'queued' || job.status === 'failed') action('cancel', '停止任务');
      if (job.article_url) {
        try { const url = new URL(job.article_url); if (url.protocol === 'https:' && url.hostname === 'mp.weixin.qq.com') { const link = make('a', '查看文章', 'admin-button'); link.href = url.href; link.target = '_blank'; link.rel = 'noopener noreferrer'; actions.append(link); } } catch (_) { /* No link for an invalid upstream URL. */ }
      }
      row.append(info, actions); fragment.append(row);
    });
    if (!jobs.length) fragment.append(make('div', jobQuery || jobStatus ? '没有匹配的同步记录，可更换标题或状态重新筛选。' : '还没有同步记录。连接账号后，选择一篇文章开始。', 'wechat-empty'));
    jobsNode.replaceChildren(fragment);
    if (focusID && focusAction) [...jobsNode.querySelectorAll('button')].find(button => button.dataset.id === focusID && button.dataset.action === focusAction)?.focus({ preventScroll: true });
  }
  function schedule() {
    clearTimeout(timer);
    if (disposed || authLost || recovering || document.hidden || activeTab !== 'sync' || !config?.enabled) return;
    if (activeJobs > 0) {
      timer = setTimeout(() => { if (busy) schedule(); else load(false, true); }, 6000);
    }
  }
  async function load(withPosts = true, quiet = false) {
    if (disposed || authLost) return;
    readController?.abort(); readController = new AbortController();
    const sequence = ++readSequence;
    try {
      const query = new URLSearchParams({ q: syncForm.elements.q.value.trim() });
      query.set('job_q', jobQuery); query.set('job_status', jobStatus); query.set('job_page', jobPage);
      const selected = syncForm.elements.post_id.value || initialPost;
      if (selected) query.set('post', selected);
      const data = await request('?' + query, undefined, readController.signal);
      if (sequence !== readSequence || disposed) return false;
      if (data.user_id !== ownerID || ownerID <= 0) { requireRecovery('当前账号已切换。请登录原编辑账号后恢复，本页输入已保留。'); return false; }
      if (!validConfig(data.config) || typeof data.can_sync !== 'boolean' || typeof data.can_collect !== 'boolean' || !Array.isArray(data.jobs) || !Array.isArray(data.posts)) throw new Error('工作区响应不完整，当前输入已保留，请重试读取。');
      // The selector stays usable during refreshes. Recheck a newer selection
      // before applying a response that only knows the previously chosen post.
      if ((syncForm.elements.post_id.value || initialPost) !== selected) return load(withPosts, quiet);
      config = { ...data.config, collect_drafts: data.config.collect_drafts === true }; canSync = data.can_sync; canCollect = data.can_collect; jobs = data.jobs || [];
      syncDraftPolicy();
      if (syncCheck && selected === String(syncCheck.post_id) && (!data.selected_post || data.selected_post.id !== syncCheck.post_id || data.selected_post.version !== syncCheck.version)) syncCheck.stale = true;
      jobPage = data.job_page || 1; jobPages = data.job_pages || 1; activeJobs = data.active_jobs || 0;
      if (!dirty) fillConfig(config);
      configState();
      showConfig();
      if (withPosts) {
        posts = data.posts || [];
        if (data.selected_post && !posts.some(post => post.id === data.selected_post.id)) posts.unshift(data.selected_post);
        renderPosts(selected); initialPost = '';
        one('[data-wechat-post-note]').textContent = selected && !data.selected_post ? '所选文章不存在或当前状态不可同步，请重新选择。' : '同步使用正式保存的版本；编辑页尚未保存的改动不会发送。';
      } else if (selected) {
        if (data.selected_post && String(data.selected_post.id) === selected) {
          posts = posts.map(post => String(post.id) === selected ? data.selected_post : post);
          const option = [...syncForm.elements.post_id.children].find(item => item.value === selected);
          if (option) option.textContent = `${data.selected_post.title} · ${data.selected_post.status === 'publish' ? '已发布' : data.selected_post.status === 'future' ? '等待定时' : '草稿'} · v${data.selected_post.version}`;
        } else {
          posts = posts.filter(post => String(post.id) !== selected);
          renderPosts('');
          one('[data-wechat-post-note]').textContent = '所选文章不存在或当前状态不可同步，请重新选择。';
        }
      }
      renderJobs();
      one('[data-wechat-total]').textContent = `筛选 ${data.filtered_jobs ?? jobs.length} / 共 ${data.total_jobs} 条`;
      one('[data-wechat-page]').textContent = `第 ${jobPage} / ${jobPages} 页`;
      one('[data-wechat-job-summary]').textContent = `当前公众号：${activeJobs} 条处理中 · ${data.attention_jobs || 0} 条需处理。记录按创建时间从新到旧排列。`;
      if (!quiet) message(dirty && formVersion !== config.version ? '服务器配置版本已变化。本页输入已保留，可在账号与设置中核对并载入已保存值。' : readonly ? '当前为只读预览；登录管理员账号后可操作。' : '工作区已就绪。');
      return true;
    } catch (error) { if (sequence === readSequence && error.name !== 'AbortError') { message(error.message, true); if (!config) recovery.hidden = false; } return false; }
    finally { if (sequence === readSequence) { lock(); schedule(); } }
  }
  async function run(callback, access = 'config', readKind = '') {
    if (busy || recovering || disposed || readonly || authLost || !config) return;
    const epoch = pageEpoch, trigger = document.activeElement, startedTab = activeTab;
    const keepCollectionFocus = access === 'collect' && trigger && (trigger === applyButton || collectForm.contains(trigger) || collection.contains(trigger));
    let focusMoved = false, focusTarget = null;
    const movedFocus = event => {
      if (trigger === event.target || trigger?.contains(event.target) || stopReadButton === event.target || stopReadButton.contains(event.target)) return;
      if (event.type !== 'focusin' || event.target !== document.body) focusMoved = true;
    };
    if (keepCollectionFocus) { document.addEventListener('focusin', movedFocus, true); document.addEventListener('pointerdown', movedFocus, true); }
    const operation = readKind ? { kind: readKind, controller: new AbortController(), trigger } : null;
    readOperation = operation;
    busy = true; clearTimeout(timer); readSequence++; readController?.abort(); lock();
    try {
      const signal = operation?.controller.signal;
      if (!await checkSession(signal)) return;
      if (signal?.aborted) throw new DOMException('Cancelled', 'AbortError');
      if (access === 'sync' && !canSync) throw new Error('当前账号缺少全站内容管理权限，操作未发送；仍可管理公众号设置。');
      if (access === 'collect' && !canCollect) throw new Error('当前账号缺少采集所需的内容、分类或媒体管理权限，操作未发送。');
      if (access === 'collect' && !config.collect_enabled) throw new Error('文章采集模块已关闭。');
      focusTarget = await callback(signal);
    } catch (error) {
      if (epoch === pageEpoch && !disposed) {
        if (operation?.controller.signal.aborted) message(readActions[operation.kind].stopped);
        else message(error.message, true);
      }
    } finally {
      if (keepCollectionFocus) { document.removeEventListener('focusin', movedFocus, true); document.removeEventListener('pointerdown', movedFocus, true); }
      if (epoch === pageEpoch) {
        const restoreFocus = document.activeElement === stopReadButton || keepCollectionFocus && !focusMoved && startedTab === activeTab &&
          (!document.activeElement || document.activeElement === document.body || document.activeElement === trigger);
        readOperation = null; busy = false; lock(); schedule();
        if (restoreFocus) {
          const available = node => node && typeof node.focus === 'function' && node.isConnected !== false && !node.disabled && !node.matches?.(':disabled') && !node.closest('[hidden]');
          let target = keepCollectionFocus ? focusTarget : null;
          if (!available(target) && keepCollectionFocus && trigger.dataset.wechatRetry !== undefined) {
            target = [...collection.querySelectorAll('[data-wechat-retry]')].find(node => node.dataset.wechatRetry === trigger.dataset.wechatRetry);
          }
          if (!available(target)) target = trigger;
          if (!available(target)) target = tabs.find(tab => tab.dataset.wechatTab === activeTab);
          const sameControl = target === trigger || trigger?.dataset.wechatRetry !== undefined && target?.dataset.wechatRetry === trigger.dataset.wechatRetry;
          target?.focus({ preventScroll: sameControl });
        }
      }
    }
  }
  stopReadButton.addEventListener('click', () => {
    if (!busy || !readOperation) return;
    readOperation.controller.abort(); lock();
  });
  async function confirm(copy, kind = '') {
    if (dialog.open || disposed || authLost) return null;
    const epoch = pageEpoch;
    one('[data-wechat-confirm-copy]').textContent = copy;
    one('[data-wechat-resolve]').hidden = !kind;
    const input = dialog.querySelector('input'); input.value = ''; input.required = !!kind;
    one('[data-wechat-resolve-hint]').textContent = kind === 'publish' ? '填写微信发表任务 ID（publish_id），系统只查询该任务。' : '填写已创建草稿的素材 ID（media_id），补录后保留在草稿箱。';
    dialog.returnValue = '';
    const answer = await new Promise(resolve => { dialog.addEventListener('close', () => resolve(dialog.returnValue === 'confirm' ? { value: input.value.trim() } : null), { once: true }); dialog.showModal(); });
    // Native close events may arrive after departure or a cached-page return.
    // Validate after resolution so stale confirmations cannot write or clear drafts.
    return !disposed && epoch === pageEpoch && !authLost ? answer : null;
  }
  const tabs = [...root.querySelectorAll('[data-wechat-tab]')];
  root.querySelector('.wechat-tabs').setAttribute('role', 'tablist');
  function selectTab(name, focus = false, preserve = false) {
    const change = () => {
      activeTab = name;
      tabs.forEach(tab => { const selected = tab.dataset.wechatTab === name; tab.classList.toggle('is-active', selected); tab.setAttribute('aria-selected', String(selected)); tab.tabIndex = selected ? 0 : -1; if (selected && focus) tab.focus({ preventScroll: true }); });
      const target = root.querySelector(`[data-wechat-panel="${name}"]`);
      if (target) target.hidden = false;
      root.querySelectorAll('[data-wechat-panel]').forEach(panel => { if (panel !== target) panel.hidden = true; });
      schedule();
    };
    if (preserve && window.ChunnelScroll) window.ChunnelScroll.keep(change);
    else change();
  }
  tabs.forEach((tab, index) => {
    const name = tab.dataset.wechatTab, panel = one(`[data-wechat-panel="${name}"]`);
    tab.id = `wechat-tab-${name}`; tab.setAttribute('role', 'tab'); tab.setAttribute('aria-controls', `wechat-panel-${name}`);
    panel.id = `wechat-panel-${name}`; panel.setAttribute('role', 'tabpanel'); panel.setAttribute('aria-labelledby', tab.id);
    tab.addEventListener('click', () => selectTab(name, false, true));
    tab.addEventListener('keydown', event => { const next = event.key === 'ArrowRight' ? (index + 1) % tabs.length : event.key === 'ArrowLeft' ? (index + tabs.length - 1) % tabs.length : event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : -1; if (next >= 0) { event.preventDefault(); selectTab(tabs[next].dataset.wechatTab, true, true); } });
  });
  const configChanged = () => { configFeedback = ''; configError = false; dirty = JSON.stringify(configValues()) !== initialConfig; configState(); };
  configForm.addEventListener('input', configChanged);
  configForm.addEventListener('change', configChanged);
  discardButton.addEventListener('click', () => run(async () => {
    if (!dirty || !(await confirm('放弃本页尚未保存的配置，并载入服务器当前设置？已输入但未保存的密钥也会清空。'))) return;
    if (!await load(false, true)) return;
    fillConfig(config); message('已载入服务器保存的设置。');
  }));
  configForm.addEventListener('submit', event => {
    event.preventDefault();
    if (busy || recovering || disposed || readonly || authLost || !config) return;
    configSaving = true;
    run(async () => {
      const input = { version: formVersion }; fields.forEach(name => { input[name] = configForm.elements[name].value.trim(); }); checks.forEach(name => { input[name] = configForm.elements[name].checked; });
      input.app_secret = configForm.elements.app_secret.value.trim();
      const clear = configForm.elements.clear_secret.checked;
      if (input.auto_publish && !config.auto_publish && !(await confirm('开启后，新同步的文章会自动提交微信发表。请确认内容已可公开。'))) return;
      message('正在保存设置…');
      const data = await request('/config', { config: input, clear_secret: clear });
      if (!validConfig(data.config) || data.config.version !== input.version + 1 || fields.some(name => data.config[name] !== input[name]) || checks.some(name => (data.config[name] === true) !== input[name])) throw new Error('保存回执不完整，输入仍保留。请先刷新配置核对结果，不要重复开启发表。');
      config = { ...data.config, collect_drafts: data.config.collect_drafts === true }; syncDraftPolicy(); fillConfig(config); showConfig();
      if (!await load(false, true)) { if (!authLost) message('设置保存已确认，但最新同步记录尚未读取成功，请稍后刷新。', true); return; }
      message('已保存');
    }).finally(() => { configSaving = false; paintConfig(); });
  });
  one('[data-wechat-test]').addEventListener('click', () => run(async signal => { message('正在测试已保存的连接…' + (dirty ? '本页未保存的配置不参与测试。' : '')); const data = await request('/test', {}, signal); message(data.message + (dirty ? ' 本次测试使用服务器已保存的设置。' : '')); }, 'config', 'connection'));
  one('[data-wechat-search]').addEventListener('click', () => { if (!busy) load(); });
  syncForm.elements.q.addEventListener('keydown', event => { if (event.key === 'Enter' && !event.isComposing && event.keyCode !== 229) { event.preventDefault(); if (!busy) load(); } });
  syncForm.elements.post_id.addEventListener('change', lock);
  function updateCheckState() {
    if (!syncCheck) return;
    const post = posts.find(item => String(item.id) === syncForm.elements.post_id.value);
    const stale = syncCheck.stale || !post || post.id !== syncCheck.post_id || post.version !== syncCheck.version || config?.version !== syncCheck.config_version || authLost;
    checkResult.dataset.state = stale ? 'stale' : syncCheck.ready ? 'ready' : 'issues';
    one('[data-wechat-check-badge]').textContent = stale ? '需要重新检查' : syncCheck.ready ? '本地检查通过' : `${syncCheck.issues.length} 项需调整`;
    one('[data-wechat-check-state]').textContent = stale ? '文章、已保存配置或登录状态已变化。下方保留上次结果，请重新检查当前文章。' : dirty ? '本次检查使用服务器已保存的设置；本页尚未保存的配置不参与检查。' : syncCheck.ready ? `已核对当前保存版本。当前方式：${syncCheck.auto_publish ? '同步后自动发表' : '同步到草稿箱'}。` : '先修正以下问题，再检查图片和正文。发送时也会执行本地检查。';
  }
  function validSyncCheck(value, input) {
    return value && value.post_id === input.post_id && value.version === input.version && value.config_version === input.config_version &&
      typeof value.title === 'string' && typeof value.auto_publish === 'boolean' && typeof value.ready === 'boolean' && Number.isFinite(Date.parse(value.checked_at)) &&
      ['title_chars','content_chars','content_bytes','images','checked_images','upload_bytes'].every(name => Number.isSafeInteger(value[name]) && value[name] >= 0) && value.checked_images <= value.images &&
      Array.isArray(value.issues) && value.issues.length <= 50 && value.issues.every(issue => issue && typeof issue.field === 'string' && typeof issue.message === 'string') &&
      Array.isArray(value.warnings) && value.warnings.length <= 10 && value.warnings.every(warning => typeof warning === 'string') &&
      value.ready === (value.issues.length === 0 && value.checked_images === value.images);
  }
  function renderSyncCheck(value) {
    syncCheck = value; checkResult.hidden = false;
    one('[data-wechat-check-title]').textContent = value.title;
    one('[data-wechat-check-title-count]').textContent = `${value.title_chars} / 32`;
    one('[data-wechat-check-content-count]').textContent = `${value.content_chars.toLocaleString('zh-CN')} 字符`;
    one('[data-wechat-check-images]').textContent = `${value.checked_images} / ${value.images} 项`;
    const issues = one('[data-wechat-check-issues]'); issues.replaceChildren(...value.issues.map(issue => make('li', issue.message))); issues.hidden = !value.issues.length;
    const warnings = one('[data-wechat-check-warnings]'); warnings.replaceChildren(...value.warnings.map(warning => make('p', warning)));
    if (value.upload_bytes > 0) warnings.append(make('p', `已准备图片约 ${(value.upload_bytes / 1024).toFixed(1)} KiB。`));
    one('[data-wechat-check-edit]').href = `/admin/content/${value.post_id}/edit/`;
    one('[data-wechat-check-time]').textContent = `v${value.version} · ${date(value.checked_at)}`;
    updateCheckState();
  }
  checkButton.addEventListener('click', () => run(async signal => {
    const post = posts.find(item => String(item.id) === syncForm.elements.post_id.value);
    if (!post || !config.enabled) return;
    const input = { post_id: post.id, version: post.version, config_version: config.version };
    message('正在检查标题、正文和本站图片…');
    try {
      const data = await request('/sync/check', input, signal);
      if (!validSyncCheck(data.check, input)) throw new Error('检查结果不完整，上次结果已保留，请重新检查。');
      if (syncForm.elements.post_id.value !== String(input.post_id)) { message('所选文章已变化，请检查当前文章。'); return; }
      renderSyncCheck(data.check);
      message(data.check.ready ? '本地检查通过，可以按当前设置同步。' : '检查已完成，请先调整下方列出的问题。', !data.check.ready);
    } catch (error) { if (syncCheck && !signal.aborted) { syncCheck.stale = true; updateCheckState(); } throw error; }
  }, 'sync', 'check'));
  one('[data-wechat-refresh]').addEventListener('click', () => { if (!busy) load(); });
  historyForm.addEventListener('submit', event => { event.preventDefault(); if (busy) return; jobQuery = historyForm.elements.job_q.value.trim(); jobStatus = historyForm.elements.job_status.value; jobPage = 1; load(false); });
  prev.addEventListener('click', () => { if (!busy && jobPage > 1) { jobPage--; load(false, true); } });
  next.addEventListener('click', () => { if (!busy && jobPage < jobPages) { jobPage++; load(false, true); } });
  syncForm.addEventListener('submit', event => {
    event.preventDefault();
    run(async () => {
      const post = posts.find(item => String(item.id) === syncForm.elements.post_id.value);
      if (!post || !canSync || !config.enabled) return;
      if (config.auto_publish && !(await confirm(`《${post.title}》将同步并自动提交微信发表。确认继续？`))) return;
      const data = await request('/sync', { post_id: post.id, version: post.version, config_version: config.version });
      jobPage = 1; jobQuery = ''; jobStatus = ''; historyForm.reset();
      if (await load(false, true)) message(`《${data.job.title}》：${labels[data.job.status] || '已入队'}。可在下方查看处理结果。`);
    }, 'sync');
  });
  jobsNode.addEventListener('click', event => {
    const button = event.target.closest('button[data-action]'); if (!button || button.disabled) return;
    run(async () => {
      const job = jobs.find(item => item.id === button.dataset.id); if (!job) return;
      const action = button.dataset.action, body = { action, updated_at: job.updated_at };
      if (action === 'publish' && !(await confirm(`将《${job.title}》提交微信发表。提交后会异步查询结果，确认继续？`))) return;
      if (['retry', 'resume'].includes(action) && job.auto_publish && !job.publish_id && !(await confirm(`《${job.title}》的任务方式为同步后发表，继续处理将按该方式提交。确认继续？也可取消并改为仅草稿。`))) return;
      if (action === 'cancel' && !(await confirm('停止此同步任务。已经创建的微信草稿将保留在公众号后台。'))) return;
      if (action === 'resolve') { const kind = job.stage === 'publish' ? 'publish' : 'draft'; const answer = await confirm('请先在微信公众平台核对操作结果，再填写已创建的标识。补录不会重复创建内容。', kind); if (!answer) return; body[kind === 'publish' ? 'publish_id' : 'media_id'] = answer.value; }
      await request('/jobs/' + encodeURIComponent(job.id), body); if (await load(false, true)) message('任务状态已更新。');
    }, 'sync');
  });
  function selectableArticles(value = report) {
    const rows = new Map((value?.plan?.items || []).map(row => [row.key, row]));
    return (value?.articles || []).filter(article => rows.get(article.key)?.action === 'create' && value.receipts?.[article.key]);
  }
  function selectedImageCount() {
    const images = new Set();
    selectableArticles().forEach(article => { if (selectedKeys.has(article.key)) article.images.forEach(url => images.add(url)); });
    return images.size;
  }
  function updateSelection() {
    const eligible = selectableArticles();
    selectedKeys = new Set([...selectedKeys].filter(key => eligible.some(article => article.key === key)));
    selectAll.checked = !!eligible.length && selectedKeys.size === eligible.length;
    selectAll.indeterminate = selectedKeys.size > 0 && selectedKeys.size < eligible.length;
    const expired = report?.expires && Date.parse(report.expires) <= Date.now();
    one('[data-wechat-apply-note]').textContent = expired ? '预览已过期，请重新采集后确认。' : `已选择 ${selectedKeys.size} / ${eligible.length} 篇可导入文章。${report?.expires ? '预览有效至 ' + date(report.expires) + '。' : ''}`;
    const images = selectedImageCount(), overBudget = images > maxCollectionImages;
    const budget = one('[data-wechat-image-budget]'); budget.hidden = !eligible.length;
    budget.dataset.state = overBudget ? 'over' : 'ready';
    one('[data-wechat-image-count]').textContent = `${images} / ${maxCollectionImages} 张`;
    one('[data-wechat-image-meter]').value = Math.min(images, maxCollectionImages);
    one('[data-wechat-image-meter]').max = maxCollectionImages;
    one('[data-wechat-image-budget-note]').textContent = overBudget ? `当前超出 ${images - maxCollectionImages} 张，请取消部分文章后分批导入。未选文章会保留在本次预览中。` : '按图片链接去重，包含封面。图片大小会在导入时继续检查。';
    applyButton.textContent = overBudget ? '请减少所选文章后导入' : selectedKeys.size ? `确认导入 ${selectedKeys.size} 篇草稿` : '请选择要导入的文章';
    const state = one('[data-wechat-preview-state]');
    state.hidden = !report || !previewing && !collectionStale() && !reviewKeys.size;
    state.textContent = previewing ? '正在刷新预览，上次内容和选择已保留；完成后请核对变化。' : collectionStale() ? '链接已修改，下方仍为上次预览。重新采集或恢复原链接后才能导入。' : reviewKeys.size ? `${reviewKeys.size} 篇文章内容有更新，已取消旧勾选；请核对后重新选择。` : '';
    collection.querySelectorAll('[data-wechat-review-change]').forEach(node => { node.hidden = !reviewKeys.has(node.dataset.key); });
    clearTimeout(expiryTimer);
    if (report?.expires && !expired && !disposed) expiryTimer = setTimeout(() => updateSelection(), Math.max(1, Date.parse(report.expires) - Date.now() + 50));
    lock();
    scheduleDraft();
  }
  function sameArticle(a, b) {
    return ['url', 'title', 'author', 'excerpt', 'body', 'cover', 'preview'].every(name => a[name] === b[name]) &&
      a.images.length === b.images.length && a.images.every((value, index) => value === b.images[index]);
  }
  function validPreview(value) {
    if (!value || !Array.isArray(value.articles) || value.articles.length > 10 || !Array.isArray(value.failures) || value.failures.length > 10 ||
        !value.failures.every(item => item && typeof item.url === 'string' && typeof item.message === 'string')) return false;
    if (!value.articles.length) return true;
    if (!Number.isFinite(Date.parse(value.expires)) || !value.receipts || typeof value.receipts !== 'object' || Array.isArray(value.receipts) ||
        !Array.isArray(value.plan?.items) || value.plan.items.length !== value.articles.length) return false;
    const keys = new Set(), rows = new Map();
    for (const row of value.plan.items) {
      if (!row || typeof row.key !== 'string' || rows.has(row.key) || !['create', 'skip', 'changed', 'conflict'].includes(row.action)) return false;
      rows.set(row.key, row);
    }
    for (const article of value.articles) {
      if (!article || !['key', 'url', 'title', 'author', 'excerpt', 'body', 'cover', 'preview'].every(name => typeof article[name] === 'string') ||
          !article.key || keys.has(article.key) || !rows.has(article.key) || !Array.isArray(article.images) || !article.images.every(url => typeof url === 'string') ||
          !Array.isArray(article.warnings) || !article.warnings.every(warning => typeof warning === 'string')) return false;
      try { const url = new URL(article.url); if (url.origin !== 'https://mp.weixin.qq.com' || url.username || url.password) return false; } catch (_) { return false; }
      if (rows.get(article.key).action === 'create' && (typeof value.receipts[article.key] !== 'string' || !value.receipts[article.key])) return false;
      keys.add(article.key);
    }
    return Object.keys(value.receipts).every(key => keys.has(key) && typeof value.receipts[key] === 'string');
  }
  function validImport(value, keys) {
    if (value?.applied !== true || !Array.isArray(value.plan?.items) || value.plan.items.length !== keys.size || value.plan.conflicts !== 0) return false;
    const seen = new Set(); let created = 0;
    for (const row of value.plan.items) {
      if (!row || !keys.has(row.key) || seen.has(row.key) || !Number.isSafeInteger(row.post_id) || row.post_id < 1 || typeof row.title !== 'string' ||
          !['created', 'skip', 'changed'].includes(row.action)) return false;
      seen.add(row.key); if (row.action === 'created') created++;
    }
    return value.plan.new === created && value.plan.existing === keys.size - created;
  }
  async function previewLinks(urls, merge = false, signal) {
    const epoch = pageEpoch, input = collectionInput();
    const previous = report, previousKeys = new Set(selectedKeys), previousArticles = new Map((previous?.articles || []).map(article => [article.key, article]));
    if (merge && collectionStale()) throw new Error('链接已修改，请重新采集全部链接或恢复原链接后重试。');
    previewing = true; updateSelection();
    message(`正在读取 ${urls.length} 个文章链接并生成预览…`);
    const slow = setTimeout(() => { if (!disposed && epoch === pageEpoch) message('文章读取较慢，本次会分别返回成功与失败结果，请稍候。'); }, 10000);
    try {
      const data = await request('/collect/preview', { urls }, signal);
      if (!validPreview(data)) throw new Error('预览响应不完整，上次内容和选择仍保留，请重新采集。');
      if (input !== collectionInput()) throw new Error('采集期间链接已修改，本次结果未替换上次预览，请重新采集。');
      if (!merge && previous && !data.articles.length) {
        if (input === collectionBaseline) { report = { ...previous, failures: data.failures }; renderCollection(report); }
        throw new Error('本次未能读取文章，上次预览和选择已保留，原有效期不变。' + (data.failures[0]?.message || '请稍后重新采集。'));
      }
      let candidate = data;
      if (merge && previous) {
        const articles = new Map(previous.articles.map(article => [article.key, article]));
        data.articles.forEach(article => articles.set(article.key, article));
        const previousArticleKeys = new Set(previous.articles.map(article => article.key));
        const rows = new Map((previous.plan?.items || []).filter(row => previousArticleKeys.has(row.key)).map(row => [row.key, row]));
        (data.plan?.items || []).forEach(row => rows.set(row.key, row));
        const items = [...rows.values()], expiries = [previous.articles.length ? previous.expires : '', data.expires].filter(value => Number.isFinite(Date.parse(value)));
        candidate = { articles: [...articles.values()], failures: [...(previous.failures || []).filter(item => !urls.includes(item.url)), ...data.failures], receipts: { ...previous.receipts, ...data.receipts }, expires: expiries.sort((a, b) => Date.parse(a) - Date.parse(b))[0], plan: { items, new: items.filter(row => row.action === 'create').length, conflicts: items.filter(row => row.action === 'conflict').length, existing: items.filter(row => ['skip', 'changed'].includes(row.action)).length } };
      }
      const eligible = selectableArticles(candidate);
      let fingerprints = new Map();
      if (draftOpted && config.collect_drafts) {
        try { fingerprints = await window.ChunnelWeChatDrafts.fingerprints(candidate.articles); } catch { /* Links remain recoverable without selection fingerprints. */ }
        if (disposed || epoch !== pageEpoch || signal?.aborted) throw new DOMException('Cancelled', 'AbortError');
        if (input !== collectionInput()) throw new Error('核对期间链接已修改，请重新采集；原内容仍保留。');
      }
      reviewKeys = new Set(eligible.filter(article => reviewKeys.has(article.key) || previousArticles.has(article.key) && !sameArticle(previousArticles.get(article.key), article)).map(article => article.key));
      selectedKeys = new Set(eligible.filter(article => !reviewKeys.has(article.key) && (!previousArticles.has(article.key) || previousKeys.has(article.key))).map(article => article.key));
      if (restoredChoices) {
        const choices = new Map(restoredChoices.choices.map(choice => [choice.key, choice]));
        const sameInput = restoredChoices.baseline === input;
        eligible.forEach(article => {
          if (previousArticles.has(article.key)) return;
          selectedKeys.delete(article.key);
          const old = choices.get(article.key);
          if (sameInput && old && old.digest === fingerprints.get(article.key)) { if (old.selected) selectedKeys.add(article.key); }
          else reviewKeys.add(article.key);
        });
      }
      collectionFingerprints = fingerprints;
      report = candidate; collectionBaseline = input;
      renderCollection(report);
      message(data.articles.length ? (merge ? '重试结果已加入预览，原有文章和选择已保留。' : '预览已生成。展开标题核对正文，勾选需要的文章后确认导入。') : '本次未读取到文章，可重试失败链接或修改后重新采集。', !data.articles.length);
      if (merge && data.articles.length) return [...collection.querySelectorAll('details')].find(node => node.dataset.previewKey === data.articles[0].key)?.querySelector('summary');
    } finally { clearTimeout(slow); if (epoch === pageEpoch && !disposed) { previewing = false; updateSelection(); } }
  }
  function renderCollection(data) {
    const focused = collection.contains(document.activeElement) ? document.activeElement : null;
    const focusedArticle = focused?.closest('details'), focusedTag = focused?.tagName?.toLowerCase();
    const focusedURL = focusedTag === 'a' ? focused.href : null;
    const expanded = new Set([...collection.querySelectorAll('details')].filter(node => node.open).map(node => node.dataset.previewKey));
    const fragment = document.createDocumentFragment();
    (data.failures || []).forEach(item => {
      const failure = make('div', undefined, 'wechat-collect-failure'), retry = make('button', '重试此链接', 'admin-button');
      retry.type = 'button'; retry.dataset.wechatRetry = item.url;
      retry.addEventListener('click', () => run(signal => previewLinks([item.url], true, signal), 'collect', 'preview'));
      failure.append(make('strong', '该链接未采集成功'), make('p', item.url), make('p', item.message), retry); fragment.append(failure);
    });
    const rows = new Map((data.plan?.items || []).map(row => [row.key, row]));
    (data.articles || []).forEach(article => {
      const row = rows.get(article.key), details = make('details', undefined, 'wechat-collect-item');
      details.dataset.previewKey = article.key; details.open = expanded.has(article.key);
      const card = make('article', undefined, 'wechat-collect-card');
      if (row?.action === 'create' && data.receipts?.[article.key]) {
        const label = make('label', undefined, 'wechat-collect-select'), input = make('input');
        input.type = 'checkbox'; input.checked = selectedKeys.has(article.key); input.dataset.key = article.key;
        input.setAttribute('aria-label', '导入：' + article.title);
        input.addEventListener('change', () => { if (input.checked) { selectedKeys.add(article.key); reviewKeys.delete(article.key); } else selectedKeys.delete(article.key); updateSelection(); });
        label.append(input, make('span', '选入本次导入')); card.append(label);
        if (reviewKeys.has(article.key)) { const note = make('p', '内容已更新，请核对正文后重新勾选。', 'wechat-collect-state'); note.dataset.wechatReviewChange = ''; note.dataset.key = article.key; card.append(note); }
      } else if (row?.message) {
        card.append(make('p', row.message, 'wechat-collect-state'));
      }
      details.append(make('summary', article.title), make('p', `${article.author || '未标注公众号'} · ${article.images.length} 张图片 · ${row?.action === 'create' ? '待导入' : row?.message || ''}`));
      if (article.excerpt) details.append(make('p', article.excerpt));
      (article.warnings || []).forEach(warning => details.append(make('p', warning)));
      const link = make('a', '打开公众号原文'); link.href = article.url; link.target = '_blank'; link.rel = 'noopener noreferrer'; details.append(link);
      const showProse = () => {
        if (!details.open || details.querySelector('.wechat-prose')) return;
        const prose = make('div', undefined, 'wechat-prose');
        // Only the server's sanitized projection is mounted. Remote pictures
        // are placeholders until the administrator confirms the import.
        prose.innerHTML = article.preview;
        prose.querySelectorAll('a').forEach(anchor => { anchor.target = '_blank'; anchor.rel = 'noopener noreferrer'; });
        details.append(prose);
      };
      details.addEventListener('toggle', showProse); showProse();
      if (row?.post_id) { const edit = make('a', '编辑已导入文章', 'wechat-imported-link'); edit.href = `/admin/content/${row.post_id}/edit/`; edit.target = '_blank'; edit.rel = 'noopener'; details.append(edit); }
      card.append(details); fragment.append(card);
    });
    if (!data.articles?.length && !data.failures?.length) fragment.append(make('p', '没有可导入的文章。', 'wechat-empty'));
    collection.replaceChildren(fragment);
    applyWrap.hidden = !data.articles?.length;
    one('[data-wechat-selection]').hidden = !selectableArticles().length;
    updateSelection();
    if (focusedArticle && ['summary', 'a'].includes(focusedTag) && !disposed) {
      const article = [...collection.querySelectorAll('details')].find(node => node.dataset.previewKey === focusedArticle.dataset.previewKey);
      if (article && !article.closest('[hidden]')) {
        const target = focusedURL && [...article.querySelectorAll('a')].find(link => link.href === focusedURL);
        (target || article.querySelector('summary'))?.focus({ preventScroll: true });
      }
    }
  }
  selectAll.addEventListener('change', () => {
    selectedKeys = new Set(selectAll.checked ? selectableArticles().map(article => article.key) : []);
    selectedKeys.forEach(key => reviewKeys.delete(key));
    collection.querySelectorAll('input[type=checkbox]').forEach(input => { input.checked = selectedKeys.has(input.dataset.key); });
    updateSelection();
  });
  draftEnable.addEventListener('change', async () => {
    if (!config?.collect_drafts || busy || recovering || authLost || readonly) { paintDraft(); return; }
    if (!draftEnable.checked) {
      draftEnable.checked = draftOpted;
      if (savedDraft && !(await confirm('停止暂存并清除本标签页的采集记录？当前页面输入和预览会继续保留。'))) return;
      clearDraft(); return;
    }
    draftOpted = true; saveCollectionDraft();
    if (report?.articles.length) {
      const current = report, epoch = pageEpoch;
      try { const hashes = await window.ChunnelWeChatDrafts.fingerprints(current.articles); if (!disposed && epoch === pageEpoch && current === report) { collectionFingerprints = hashes; saveCollectionDraft(); } } catch { /* Keep links and explain selection fallback on the next save. */ }
    }
  });
  draftClear.addEventListener('click', async () => {
    if (busy || recovering || authLost || readonly || !(await confirm('清除本标签页记录并停止暂存？当前输入与预览不变；已经发送的导入不会撤回。'))) return;
    clearDraft(); draftEnable.focus({ preventScroll: true });
  });
  draftRestore.addEventListener('click', async () => {
    const hadFocus = document.activeElement === draftRestore; let restored = false;
    await run(async () => {
      const record = restoreCandidate, input = collectForm.elements.urls.value, epoch = pageEpoch;
      if (!record || !await load(false, true)) return;
      const checkRestore = () => {
        if (!config.collect_drafts || !canCollect || !config.collect_enabled || restoreCandidate !== record) throw new Error('采集恢复已停用或权限发生变化，当前输入不变。');
        if (Date.now() - record.at >= 86400000) { clearDraft(); throw new Error('保存的采集进度已过期，当前输入不变。'); }
        if (collectForm.elements.urls.value !== input) throw new Error('检查期间输入已变化，请核对后再次恢复。');
      };
      checkRestore();
      if (input.trim() || report) {
        if (!(await confirm('用保存的链接与选择替换当前采集输入？预览会清空，之后需要重新采集核对。')) || disposed || epoch !== pageEpoch) return;
        if (!await checkSession() || !await load(false, true)) return;
      }
      if (disposed || epoch !== pageEpoch) return;
      checkRestore();
      collectForm.elements.urls.value = record.input;
      restoredChoices = { baseline: record.baseline, choices: record.choices }; draftUncertain = record.uncertain;
      restoreCandidate = null; report = null; selectedKeys.clear(); reviewKeys.clear(); collectionFingerprints.clear(); collectionBaseline = '';
      renderCollection({ articles: [], failures: [] });
      message(record.uncertain ? '已恢复链接。上次导入结果需重新预览核对，已导入文章会跳过；不会自动重发。' : '已恢复链接，请重新采集。内容一致的文章会恢复原勾选，新增或变化的文章需重新确认。');
      restored = true; saveCollectionDraft();
    }, 'collect');
    if (restored && hadFocus && (document.activeElement === draftRestore || document.activeElement === document.body)) collectForm.elements.urls.focus({ preventScroll: true });
  });
  collectForm.elements.urls.addEventListener('input', () => { if (report) updateSelection(); else lock(); scheduleDraft(); });
  collectForm.addEventListener('submit', event => {
    event.preventDefault();
    run(async signal => {
      const urls = collectionURLs();
      if (!urls.length || urls.length > 10) throw new Error('每批请输入 1–10 个不同的文章链接，可直接粘贴分享文案。');
      await previewLinks(urls, false, signal);
    }, 'collect', 'preview');
  });
  applyButton.addEventListener('click', () => run(async () => {
    const preview = report; if (!preview || !selectedKeys.size) return;
    if (collectionStale()) throw new Error('链接已修改，请重新采集预览或恢复原链接后再导入。');
    if (!preview.expires || Date.parse(preview.expires) <= Date.now()) { updateSelection(); throw new Error('预览已过期，请重新采集。'); }
    if (selectedImageCount() > maxCollectionImages) throw new Error(`所选图片超过 ${maxCollectionImages} 张，请取消部分文章后分批导入。`);
    const articles = selectableArticles().filter(article => selectedKeys.has(article.key));
    if (!articles.length) return;
    const keys = new Set(articles.map(article => article.key)), receipts = Object.fromEntries(articles.map(article => [article.key, preview.receipts[article.key]]));
    draftUncertain = true; saveCollectionDraft();
    message('正在保存图片并导入草稿…');
    const data = await request('/collect/apply', { articles, receipts });
    if (!validImport(data, keys)) throw new Error('导入回执不完整，请重新预览核对；已导入文章会自动跳过。');
    draftUncertain = false;
    if (restoredChoices) restoredChoices.choices = restoredChoices.choices.filter(choice => !keys.has(choice.key));
    report = { ...preview, articles: preview.articles.filter(article => !keys.has(article.key)) };
    selectedKeys.clear(); renderCollection(report); saveCollectionDraft();
    const result = make('section', undefined, 'wechat-import-result');
    result.append(make('p', `已导入 ${data.plan.new} 篇草稿，保留 ${data.plan.existing} 篇已导入文章。`));
    data.plan.items.forEach(item => {
      const entry = make('div'), link = make('a', '编辑：' + item.title, 'wechat-imported-link');
      link.href = `/admin/content/${item.post_id}/edit/`; link.target = '_blank'; link.rel = 'noopener'; entry.append(link);
      if (item.message) entry.append(make('p', item.message, 'wechat-collect-state'));
      result.append(entry);
    });
    resultsNode.replaceChildren(...[...resultsNode.children, result].slice(-5));
    message(selectableArticles().length ? '所选文章处理完成。未选文章仍保留在预览中，可以继续勾选导入。' : data.plan.new ? '导入完成。文章已保存为草稿，可继续编辑排版。' : '本次未新建草稿，已保留现有文章；请查看下方核对提示。');
    await load(true, true);
    return collection.querySelector('input[type=checkbox]') || result.querySelector('a');
  }, 'collect'));
  document.addEventListener('visibilitychange', () => { if (document.hidden) { clearTimeout(timer); saveCollectionDraft(); } else { schedule(); updateSelection(); } });
  window.addEventListener('beforeunload', event => {
    if (dirty || busy || recovering || collectionInput() !== collectionBaseline || selectableArticles().length || report?.failures?.length) { event.preventDefault(); event.returnValue = ''; }
  });
  window.addEventListener('pagehide', () => {
    saveCollectionDraft(); clearTimeout(draftTimer);
    interruptedWrite = interruptedWrite || [...requests.values()].some(Boolean);
    disposed = true; pageEpoch++; clearTimeout(timer); clearTimeout(expiryTimer); readSequence++; readController?.abort();
    requests.forEach((_, controller) => controller.abort());
    readOperation?.controller.abort(); readOperation = null;
    if (dialog.open) dialog.close('cancel');
    busy = false; recovering = false; previewing = false;
  });
  window.addEventListener('pageshow', event => { if (event.persisted) {
    disposed = false; updateSelection();
    requireRecovery(interruptedWrite ? '页面返回时，上次操作结果尚待核对。请先恢复登录检查，再核对记录；本页输入已保留。' : '页面已恢复，请核对当前登录状态后继续；本页输入和采集选择已保留。');
  } });
  selectTab(location.hash==='#config'?'config':'sync'); lock(); load();
})();
