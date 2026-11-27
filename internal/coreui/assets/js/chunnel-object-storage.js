(() => {
  'use strict';
  const root = document.querySelector('[data-storage-admin]');
  if (!root) return;
  const $ = selector => root.querySelector(selector);
  const all = selector => [...root.querySelectorAll(selector)];
  const form = $('[data-storage-form]'), bar = $('[data-admin-savebar]');
  const readOnly = root.dataset.readonly === 'true';
  const api = '/api/v1/admin/plugins/object-storage';
  const providers = {
    s3: { name: 'Amazon S3', region: 'us-east-1', endpoint: 'https://s3.us-east-1.amazonaws.com', note: '使用存储桶所在区域。支持长期密钥和带 Session Token 的临时凭据。' },
    r2: { name: 'Cloudflare R2', region: 'auto', endpoint: 'https://<account-id>.r2.cloudflarestorage.com', note: '填写 R2 的 S3 API 地址，区域固定为 auto。公开访问请另外填写自定义下载域名。' },
    cos: { name: '腾讯云 COS', region: 'ap-guangzhou', endpoint: 'https://cos.ap-guangzhou.myqcloud.com', note: '桶名需要包含 APPID 后缀。采用虚拟主机地址方式，可使用 SecretId / SecretKey 和临时令牌。' },
    oss: { name: '阿里云 OSS', region: 'cn-hangzhou', endpoint: 'https://oss-cn-hangzhou.aliyuncs.com', note: '通过 OSS 原生接口连接，无需开通 S3 兼容签名。Endpoint 不包含桶名。' },
    qiniu: { name: '七牛云 Kodo', region: 'cn-east-1', endpoint: 'https://s3.cn-east-1.qiniucs.com', note: '填写控制台提供的 S3 空间名，它可能不同于普通空间名。S3 API 地址不能用作匿名下载域名。' },
    's3-compatible': { name: 'S3 兼容存储', region: 'us-east-1', endpoint: 'https://storage.example.com', note: '填写服务提供方给出的 S3 Endpoint、区域和桶名。自建服务可在高级设置中选择路径式地址。' },
    cloudreve: { name: 'Cloudreve', region: '', endpoint: 'https://cloud.example.com/dav', note: '在 Cloudreve 中创建 WebDAV 账号，填写账号邮箱和独立 WebDAV 密码。地址通常以 /dav 结尾。' },
    webdav: { name: 'WebDAV', region: '', endpoint: 'https://dav.example.com/remote.php/dav/files/user', note: '填写可以读写的 WebDAV 根目录地址。支持创建子目录、上传、下载和删除。' }
  };
  const labels = { pending: '待处理', syncing: '处理中', synced: '已同步', failed: '失败', orphaned: '待清理', removed: '已清理' };
  const settingKeys = ['version', 'active_profile', 'mode', 'auto_sync', 'verify_download', 'paused', 'workers', 'scan_seconds', 'max_attempts', 'signed_url_seconds', 'profiles', 'upload_routes'];
  const clone = value => JSON.parse(JSON.stringify(value));
  const el = (tag, text, className) => { const element = document.createElement(tag); if (text !== undefined) element.textContent = text; if (className) element.className = className; return element; };
  let state;
  try { state = JSON.parse(root.dataset.storageState); } catch { return; }
  root.removeAttribute('data-storage-state');
  let draft, baseline, secretFlags = new Map(), clearTokens = new Set();
  let selected = '', busy = false, dirty = false, saveMessage = '', saveError = false, pollTimer = 0, lastSavedVersion = 0;
  let requestEpoch = 0, refreshing = false;
  let migrationVersion = 0, recordPage = 1, recordPages = 1, recordEpoch = 0, recordResults = null;
  const activeRequests = new Set();

  function bytes(value) {
    const n = Number(value) || 0;
    if (n < 1024) return n + ' B';
    const units = ['KiB', 'MiB', 'GiB', 'TiB']; let size = n / 1024, i = 0;
    while (size >= 1024 && i < units.length - 1) { size /= 1024; i++; }
    return size.toLocaleString('zh-CN', { maximumFractionDigits: 1 }) + ' ' + units[i];
  }
  function when(value) {
    if (!value || value.startsWith('0001-')) return '—';
    if (window.ChunnelTime) return window.ChunnelTime.formatDateTimeSeconds(value) || '—';
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString('zh-CN', { timeZone: 'UTC', hour12: false });
  }
  function currentProfile() { return draft.profiles.find(profile => profile.id === selected); }
  function profileName(id) { return state.settings.profiles?.find(profile => profile.id === id)?.name || id; }
  function message(text, error = false) {
    const output = $('[data-storage-action-status]');
    output.hidden = !text; output.textContent = text; output.dataset.tone = error ? 'error' : '';
  }
  function fieldValue(input) { return input.type === 'checkbox' ? input.checked : input.type === 'number' ? Number(input.value) : input.value; }
  function captureEditor() {
    const profile = currentProfile();
    if (!profile) return;
    const priorID = profile.id;
    all('[data-profile-field]').forEach(input => { profile[input.dataset.profileField] = fieldValue(input); });
    if (profile.id !== priorID) {
      if (draft.active_profile === priorID) draft.active_profile = profile.id;
      if (clearTokens.delete(priorID)) clearTokens.add(profile.id);
      selected = profile.id;
    }
    const dav = profile.provider === 'cloudreve' || profile.provider === 'webdav';
    if (dav) { profile.bucket = ''; profile.region = ''; profile.access_key = ''; profile.secret_key = ''; profile.session_token = ''; profile.path_style = false; }
    else { profile.username = ''; profile.password = ''; }
    if (profile.provider === 'r2') profile.region = 'auto';
    if (profile.provider === 'cos') profile.path_style = false;
  }
  function capture() {
    captureEditor();
    all('[data-setting-field]').forEach(input => { draft[input.dataset.settingField] = fieldValue(input); });
    all('[data-route-source]').forEach(row => {
      const target = row.querySelector('[data-route-target]').value, mode = row.querySelector('[data-route-mode]').value;
      if (target || mode) draft.upload_routes[row.dataset.routeSource] = { target, mode };
      else delete draft.upload_routes[row.dataset.routeSource];
    });
  }
  function signature() { return JSON.stringify({ settings: draft, clear: [...clearTokens].sort() }); }
  function markDirty() {
    capture(); dirty = signature() !== baseline; saveMessage = ''; saveError = false; paint();
  }
  function paint() {
    form.setAttribute('aria-busy', String(busy));
    const button = bar.querySelector('[data-admin-save]'), discard = bar.querySelector('[data-admin-discard]'), output = bar.querySelector('[data-save-state]');
    button.disabled = readOnly || busy || !dirty; button.setAttribute('aria-busy', String(busy));
    discard.hidden = readOnly || !dirty; discard.disabled = busy;
    output.textContent = saveMessage || (readOnly ? '只读预览' : dirty ? '有未保存的更改' : ''); output.dataset.tone = saveError ? 'error' : '';
    bar.classList.toggle('is-dirty', dirty); bar.classList.toggle('is-saving', busy);
    all('[data-storage-action], [data-storage-test], [data-storage-add], [data-storage-remove]').forEach(control => { control.disabled = readOnly || busy; });
    $('[data-storage-add]').disabled = readOnly || busy || draft.profiles.length >= 8;
    $('[data-storage-copy]').disabled = readOnly || busy || !currentProfile() || draft.profiles.length >= 8;
    $('[data-storage-action-profile]').disabled = readOnly || busy || !state.settings.profiles?.length;
    $('[data-storage-refresh]').disabled = readOnly || busy;
    const editor = $('[data-storage-editor]');
    editor.disabled = readOnly || busy || !currentProfile();
    form.querySelector('.storage-form-fieldset').disabled = readOnly || busy;
    all('[data-profile-select]').forEach(button => { button.disabled = busy; });
    all('[data-migration-source], [data-migration-target], [data-migration-preview], [data-record-search-button], [data-record-export]').forEach(button => { button.disabled = readOnly || busy; });
    $('[data-migration-apply]').disabled = readOnly || busy || dirty || migrationVersion !== lastSavedVersion;
  }
  function profileLists() {
    const list = $('[data-storage-profiles]'); list.replaceChildren();
    draft.profiles.forEach(profile => {
      const button = el('button', undefined, 'storage-profile-card'); button.type = 'button'; button.dataset.profileSelect = profile.id;
      button.setAttribute('aria-pressed', String(profile.id === selected));
      button.append(el('b', profile.name || '未命名存储'), el('span', providers[profile.provider]?.name || profile.provider));
      if (profile.id === draft.active_profile) button.append(el('small', '当前存储', 'storage-current-tag'));
      button.append(el('small', `${state.profile_counts?.[profile.id]?.synced || 0} 个已同步 · ${bytes(state.profile_bytes?.[profile.id])}`));
      button.addEventListener('click', () => { capture(); selected = profile.id; profileLists(); renderEditor(); paint(); });
      list.append(button);
    });
    const active = $('[data-storage-active]'); active.replaceChildren(new Option('暂不选择', ''));
    draft.profiles.forEach(profile => active.add(new Option(profile.name || '未命名存储', profile.id)));
    active.value = draft.active_profile;
    const actions = $('[data-storage-action-profile]'), previous = actions.value;
    actions.replaceChildren(new Option('请选择存储', ''));
    (state.settings.profiles || []).forEach(profile => actions.add(new Option(profile.name, profile.id)));
    actions.value = state.settings.profiles?.some(profile => profile.id === previous) ? previous : state.settings.active_profile || state.settings.profiles?.[0]?.id || '';
    renderRoutes();
    for (const selector of ['[data-migration-source]', '[data-migration-target]', '[data-record-profile]', '[data-cloud-profile]', '[data-cloud-target-profile]']) {
      const control = $(selector), value = control.value;
      control.replaceChildren(new Option(selector === '[data-record-profile]' ? '全部存储' : '请选择存储', ''));
      (state.settings.profiles || []).forEach(profile => control.add(new Option(profile.name, profile.id)));
      control.value = value;
    }
  }
  function renderRoutes() {
    const container = $('[data-storage-routes]'); container.replaceChildren();
    (state.upload_entries || []).forEach(entry => {
      const row = el('div', undefined, 'storage-field'); row.dataset.routeSource = entry.id;
      const targetLabel = el('label', entry.name), target = el('select'); target.dataset.routeTarget = '';
      target.setAttribute('aria-label', entry.name + '存储目标');
      target.add(new Option(entry.id === 'circle-attachment' ? '默认（仅本地）' : '继承默认存储', ''));
      target.add(new Option('仅本地 · 不同步云端', 'local'));
      draft.profiles.forEach(profile => target.add(new Option(profile.name || profile.id, profile.id)));
      const route = draft.upload_routes[entry.id] || {};
      if (route.target && ![...target.options].some(option => option.value === route.target)) target.add(new Option('已移除配置 · 请重新选择', route.target));
      target.value = route.target || ''; targetLabel.append(target);
      const mode = el('select'); mode.dataset.routeMode = ''; mode.setAttribute('aria-label', entry.name + '访问方式');
      mode.add(new Option('继承默认访问方式', '')); mode.add(new Option('本地读取 · 云端保存副本', 'mirror'));
      mode.add(new Option(entry.id === 'circle-attachment' ? '云端读取 · 本站鉴权代理' : '云端加速 · 已同步后跳转', 'redirect'));
      mode.value = route.mode || ''; mode.disabled = target.value === 'local';
      target.addEventListener('change', () => { mode.disabled = target.value === 'local'; markDirty(); });
      mode.addEventListener('change', markDirty);
      row.append(targetLabel, mode); container.append(row);
    });
  }
  function providerFields() {
    const profile = currentProfile(); if (!profile) return;
    const info = providers[profile.provider] || providers['s3-compatible'];
    const dav = profile.provider === 'cloudreve' || profile.provider === 'webdav';
    all('[data-storage-cloud-field]').forEach(label => { label.hidden = dav; label.querySelectorAll('input').forEach(input => { input.disabled = dav; }); });
    all('[data-storage-dav-field]').forEach(label => { label.hidden = !dav; label.querySelectorAll('input').forEach(input => { input.disabled = !dav; input.required = dav && input.type !== 'password'; }); });
    const pathStyle = $('[data-storage-path-style]'); pathStyle.hidden = dav || profile.provider === 'cos'; pathStyle.querySelector('input').disabled = pathStyle.hidden;
    $('[data-storage-provider-note]').textContent = info.note + (dav ? ' 未设置公开下载域名时，媒体继续从本地读取。' : '');
    $('[data-profile-field="endpoint"]').placeholder = info.endpoint;
    $('[data-profile-field="endpoint"]').required = dav || ['r2', 's3-compatible'].includes(profile.provider);
    $('[data-storage-endpoint-help]').textContent = dav ? '使用完整的 WebDAV 根目录地址，可包含路径。' : ['r2', 's3-compatible'].includes(profile.provider) ? '填写完整的 API 服务地址，不包含桶名和目录前缀。' : '可留空，按区域使用标准服务地址；不包含存储桶名称。';
    $('[data-profile-field="region"]').placeholder = info.region;
    $('[data-profile-field="region"]').readOnly = profile.provider === 'r2';
    $('[data-storage-region-help]').textContent = profile.provider === 'r2' ? 'Cloudflare R2 固定使用 auto。' : '填写存储桶所在区域。';
    $('[data-storage-bucket-label]').textContent = profile.provider === 'qiniu' ? 'S3 空间名' : '存储桶 / Bucket';
    $('[data-storage-bucket-help]').textContent = profile.provider === 'cos' ? '例如 site-media-1250000000，必须包含 APPID。' : profile.provider === 'qiniu' ? '使用七牛控制台显示的 S3 空间名。' : '仅填写桶名，不包含地址或目录。';
  }
  function renderEditor() {
    const profile = currentProfile(), editor = $('[data-storage-editor]');
    editor.hidden = !profile; $('[data-storage-empty]').hidden = !!profile;
    if (!profile) return;
    $('[data-storage-profile-heading]').textContent = profile.name || '新存储配置';
    all('[data-profile-field]').forEach(input => { const value = profile[input.dataset.profileField]; if (input.type === 'checkbox') input.checked = !!value; else input.value = value ?? ''; });
    $('[data-profile-field="id"]').readOnly = true;
    const flags = secretFlags.get(profile.id) || {};
    for (const [field, flag, help] of [['secret_key', 'secret_saved', 'secret'], ['password', 'password_saved', 'password'], ['session_token', 'token_saved', 'token']]) {
      const input = $(`[data-profile-field="${field}"]`); input.placeholder = flags[flag] ? '已保存；留空保留原值' : '';
      $(`[data-storage-${help}-help]`).textContent = flags[flag] ? '已保存；地址与账号保持不变时，留空保留。' : field === 'password' ? '使用 WebDAV 账号密码，不是网页登录密码。' : field === 'session_token' ? '使用长期密钥时留空。' : '凭据只保存在服务端，保存后不再显示。';
    }
    $('[data-storage-clear-token]').checked = clearTokens.has(profile.id);
    providerFields();
  }
  function adopt(settings) {
    draft = {}; settingKeys.forEach(key => { draft[key] = clone(settings[key] ?? (key === 'profiles' ? [] : null)); });
    draft.upload_routes ||= {};
    // The API omits empty secrets; normalize every profile before taking the
    // baseline so simply opening an editor never creates a phantom change.
    draft.profiles.forEach(profile => {
      for (const key of ['access_key', 'secret_key', 'session_token', 'username', 'password']) profile[key] ??= '';
    });
    secretFlags = new Map((settings.profile_views || []).map(profile => [profile.id, profile]));
    clearTokens = new Set(); lastSavedVersion = draft.version;
    if (!draft.profiles.some(profile => profile.id === selected)) selected = draft.active_profile || draft.profiles[0]?.id || '';
    all('[data-setting-field]').forEach(input => { const value = draft[input.dataset.settingField]; if (input.type === 'checkbox') input.checked = !!value; else input.value = value ?? ''; });
    profileLists(); renderEditor(); baseline = signature(); dirty = false; paint();
  }
  function renderRecords() {
    const body = $('[data-storage-records]'), filter = $('[data-storage-record-filter]').value;
    body.replaceChildren();
    const records = (recordResults || state.records || []).filter(record => !filter || record.status === filter);
    if (!records.length) { const tr = el('tr'), td = el('td', filter ? '当前状态没有记录。' : '尚无同步记录。保存连接后，可以同步媒体库。', 'storage-empty-row'); td.colSpan = 5; tr.append(td); body.append(tr); return; }
    records.forEach(record => {
      const tr = el('tr'), file = el('td'), details = el('details'), summary = el('summary', record.filename || '媒体 ' + record.item_id);
      details.append(summary, el('code', record.remote_key));
      details.append(el('p', '尝试 ' + record.attempts + ' 次 · 最近校验：' + when(record.verified)));
      if (record.next && !record.next.startsWith('0001-')) details.append(el('p', '下次尝试：' + when(record.next)));
      if (record.error) details.append(el('p', record.error, 'storage-record-error-text'));
      file.append(details);
      const statusCell = el('td'), badge = el('span', labels[record.status] || record.status, 'storage-record-badge'); badge.dataset.status = record.status; statusCell.append(badge);
      tr.append(file, el('td', profileName(record.profile_id)), statusCell, el('td', bytes(record.size)), el('td', when(record.updated)));
      body.append(tr);
    });
  }
  function renderStatus() {
    renderProfileSummary();
    const counts = state.counts || {};
    all('[data-storage-count]').forEach(node => { const key = node.dataset.storageCount; node.textContent = String((counts[key] || 0) + (key === 'pending' ? counts.syncing || 0 : 0)); });
    $('[data-storage-bytes]').textContent = bytes(state.synced_bytes);
    $('[data-storage-enabled]').textContent = !state.enabled ? '插件已停用' : state.settings.paused ? '任务已暂停' : state.running ? '正在同步' : '已启用';
    root.dataset.storageEnabled = String(!!state.enabled);
    $('[data-storage-current]').textContent = state.settings.active_profile ? profileName(state.settings.active_profile) : '尚未选择当前存储';
    $('[data-storage-disabled-note]').hidden = state.enabled || readOnly;
    const last = $('[data-storage-last-error]'); last.hidden = !state.last_error; last.textContent = state.last_error || '';
    renderRecords();
  }
  async function request(suffix, method = 'GET', body, timeout = 30000) {
    const controller = new AbortController(); activeRequests.add(controller);
    const timer = setTimeout(() => controller.abort(), timeout);
    try {
      const response = await fetch(api + suffix, { method, credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
        headers: body ? { 'Content-Type': 'application/json', 'X-CSRF-Token': root.dataset.csrf } : {},
        body: body ? JSON.stringify({ ...body, expected_user_id: Number(root.dataset.userId) }) : undefined });
      const data = await response.json().catch(() => null);
      if (!response.ok || !data) { const error = new Error(data?.error?.message || '请求未完成，请重试。'); error.details = data?.error?.details; error.code = response.status; throw error; }
      return data;
    } catch (error) { if (error.name === 'AbortError') throw new Error('请求已取消或超时，请刷新状态核对结果。'); throw error; }
    finally { clearTimeout(timer); activeRequests.delete(controller); }
  }
  function renderProfileSummary() {
    const id = $('[data-storage-action-profile]').value, counts = state.profile_counts?.[id] || {};
    const total = Object.entries(counts).reduce((sum, [status, count]) => sum + (status === 'removed' ? 0 : count), 0);
    const done = counts.synced || 0;
    $('[data-storage-profile-summary]').textContent = id ? `${profileName(id)}：已同步 ${done} / ${total} 个对象（${total ? Math.round(done / total * 100) : 0}%），容量 ${bytes(state.profile_bytes?.[id])}；待处理 ${(counts.pending || 0) + (counts.syncing || 0)}，失败 ${counts.failed || 0}，待清理 ${counts.orphaned || 0}。` : '选择存储查看用量与任务进度。';
  }
  async function refresh(silent = false) {
    if (busy || readOnly || refreshing || cloudBusy || $('[data-cloud-dialog]').open) return;
    const epoch = requestEpoch; refreshing = true;
    try {
      const next = await request('/status');
      if (busy || epoch !== requestEpoch) return;
      if (!next.settings || !Number.isSafeInteger(next.settings.version)) throw new Error('状态响应不完整，请稍后重试。');
      state = next; renderStatus();
      await loadRecords();
      if (!dirty) adopt(state.settings);
      else if (state.settings.version !== lastSavedVersion) { saveMessage = '配置已在其他页面更新，当前输入仍保留。请刷新页面后核对。'; saveError = true; paint(); }
      $('[data-storage-refreshed]').textContent = '更新于 ' + (window.ChunnelTime?.formatClock(new Date()) || new Date().toLocaleTimeString('zh-CN', { timeZone: 'UTC', hour12: false }));
    } catch (error) { if (!silent) message(error.message, true); }
    finally { refreshing = false; schedulePoll(); }
  }
  function schedulePoll() {
    clearTimeout(pollTimer);
    if (!readOnly && state.enabled && !document.hidden) pollTimer = setTimeout(() => refresh(true), state.running || state.counts?.pending ? 15000 : 45000);
  }
  function renderProbe(probe, error) {
    const panel = $('[data-storage-probe]'); panel.hidden = false;
    $('[data-storage-probe-title]').textContent = error ? '连接测试未完成' : '连接测试通过';
    const stages = $('[data-storage-probe-stages]'); stages.replaceChildren();
    (probe?.stages || []).forEach(stage => stages.append(el('li', stage)));
    $('[data-storage-probe-detail]').textContent = error || ('耗时 ' + (probe?.duration_ms || 0) + ' ms；测试文件已清理。');
    panel.dataset.error = String(!!error);
  }
  async function loadRecords() {
    if (readOnly) return;
    const epoch = ++recordEpoch;
    const params = new URLSearchParams({ profile: $('[data-record-profile]').value, status: $('[data-storage-record-filter]').value, q: $('[data-record-search]').value, page: String(recordPage) });
    try {
      const data = await request('/records?' + params);
      if (epoch !== recordEpoch) return;
      recordResults = data.records; recordPage = data.page; recordPages = data.pages;
      $('[data-storage-records-subtitle]').textContent = `符合条件 ${data.total} 条，每页最多 100 条。`;
      $('[data-record-page]').textContent = `${recordPage} / ${recordPages} 页`;
      $('[data-record-prev]').disabled = recordPage <= 1;
      $('[data-record-next]').disabled = recordPage >= recordPages;
      renderRecords();
    } catch (error) { message(error.message, true); }
  }
  async function migrate(apply) {
    if (busy || readOnly) return;
    capture();
    if (signature() !== baseline) { message('请先保存更改，再迁移。', true); return; }
    if (apply && migrationVersion !== lastSavedVersion) return;
    busy = true; requestEpoch++; paint();
    try {
      const data = await request('/action', 'POST', { action: apply ? 'migrate' : 'preview_migration', profile_id: $('[data-migration-source]').value, target_profile_id: $('[data-migration-target]').value, expected_version: lastSavedVersion }, 120000);
      const m = data.migration;
      migrationVersion = apply ? 0 : lastSavedVersion;
      $('[data-migration-summary]').textContent = `${apply ? '迁移已安排' : '迁移预览'}：${m.total} 个对象，共 ${bytes(m.bytes)}；目标已就绪 ${m.ready} 个。${m.default ? '切换默认存储。' : ''}关联入口：${m.routes.join('、') || '无'}。${apply ? '可在同步任务中重试、校验，在运行记录中按目标存储查看进度。' : ''}`;
    } catch (error) { migrationVersion = 0; $('[data-migration-summary]').textContent = error.message; }
    finally { busy = false; paint(); await refresh(true); }
  }
  async function runAction(action, profileID, confirmation = false) {
    if (busy || readOnly) return;
    capture(); if (signature() !== baseline) { message('请先保存更改，再执行操作。', true); return; }
    if (!profileID) { message('请选择已保存的存储配置。', true); return; }
    requestEpoch++; busy = true; paint(); message(action === 'test' ? '正在测试上传、读取校验和清理…' : '正在准备任务…');
    if (action === 'test') { $('[data-storage-probe]').hidden = true; $('[data-storage-probe-stages]').replaceChildren(); }
    try {
      const profile = state.settings.profiles.find(item => item.id === profileID);
      const data = await request('/action', 'POST', { action, profile_id: profileID, expected_version: lastSavedVersion, confirm_cleanup: confirmation }, action === 'test' ? ((profile?.timeout_seconds || 120) + 30) * 1000 : 120000);
      if (data.ok !== true) throw new Error('未收到有效操作结果，请刷新状态核对。');
      if (action === 'test') { renderProbe(data.probe); message('连接测试通过。'); }
      else { const suffix = state.settings.paused || !state.enabled ? '任务已保存，启用插件并取消暂停后处理。' : '后台将按并发和重试设置处理。'; message('已安排 ' + data.scheduled + ' 个对象。' + suffix); }
    } catch (error) { message(error.message, true); if (action === 'test') renderProbe(error.details?.probe, error.message); }
    finally { busy = false; paint(); await refresh(true); }
  }

  form.addEventListener('input', event => { if (event.target.matches('[data-profile-field], [data-setting-field]')) markDirty(); });
  $('[data-storage-routes-local]').addEventListener('click', () => {
    if (busy || readOnly) return;
    capture(); (state.upload_entries || []).forEach(entry => { draft.upload_routes[entry.id] = { target: 'local', mode: '' }; });
    renderRoutes(); markDirty();
  });
  $('[data-storage-routes-reset]').addEventListener('click', () => {
    if (busy || readOnly) return;
    capture(); draft.upload_routes = {}; renderRoutes(); markDirty();
  });
  form.addEventListener('change', event => {
    const input = event.target;
    if (input.matches('[data-profile-field="provider"]')) {
      captureEditor(); const profile = currentProfile(), info = providers[profile.provider];
      Object.assign(profile, { region: info.region, endpoint: '', bucket: '', access_key: '', secret_key: '', session_token: '', username: '', password: '', public_url: '', path_style: profile.provider === 's3-compatible', allow_http: false, allow_private: false });
      secretFlags.delete(profile.id); renderEditor(); profileLists(); dirty = true; paint();
    } else if (input.matches('[data-storage-clear-token]')) { if (input.checked) clearTokens.add(selected); else clearTokens.delete(selected); markDirty(); }
    else if (input.matches('[data-setting-field="active_profile"]')) { markDirty(); profileLists(); }
    else if (input.matches('[data-profile-field="name"], [data-profile-field="id"]')) { markDirty(); profileLists(); }
    else if (input.matches('[data-profile-field], [data-setting-field]')) markDirty();
  });
  $('[data-storage-add]').addEventListener('click', () => {
    if (busy || readOnly || draft.profiles.length >= 8) return;
    capture(); const id = 'storage-' + crypto.randomUUID().slice(0, 8);
    draft.profiles.push({ id, name: '新存储', provider: 's3', endpoint: '', region: 'us-east-1', bucket: '', prefix: 'media', public_url: '', access_key: '', secret_key: '', session_token: '', username: '', password: '', path_style: false, allow_private: false, allow_http: false, timeout_seconds: 120 });
    selected = id; if (!draft.active_profile) draft.active_profile = id;
    dirty = true; profileLists(); renderEditor(); paint(); $('[data-profile-field="name"]').focus();
  });
  $('[data-storage-remove]').addEventListener('click', () => {
    if (busy || readOnly || !currentProfile()) return;
    capture(); draft.profiles = draft.profiles.filter(profile => profile.id !== selected); clearTokens.delete(selected);
    if (draft.active_profile === selected) draft.active_profile = draft.profiles[0]?.id || '';
    selected = draft.profiles[0]?.id || ''; dirty = true; profileLists(); renderEditor(); paint();
    message('配置已从草稿中移除。保存时会核对现有同步记录；不会删除云端文件。');
  });
  $('[data-storage-copy]').addEventListener('click', () => {
    if (busy || readOnly || !currentProfile() || draft.profiles.length >= 8) return;
    capture(); const profile = clone(currentProfile());
    profile.id = 'storage-' + crypto.randomUUID().slice(0, 8); profile.name = (profile.name + ' 副本').slice(0, 80);
    profile.secret_key = ''; profile.password = ''; profile.session_token = '';
    draft.profiles.push(profile); selected = profile.id; dirty = true;
    profileLists(); renderEditor(); paint();
    message('连接参数已复制。填写目标桶或目录及凭据，保存后可用于迁移。');
    $('[data-profile-field="name"]').focus();
  });
  form.addEventListener('submit', async event => {
    event.preventDefault(); if (busy || readOnly) return;
    capture(); if (!form.reportValidity()) return;
    requestEpoch++; busy = true; saveMessage = '正在保存…'; saveError = false; paint();
    try {
      const data = await request('/settings', 'PUT', { settings: draft, clear_tokens: [...clearTokens] });
      if (data.ok !== true || !data.settings || data.settings.version !== lastSavedVersion + 1) throw new Error('保存响应不完整，请刷新核对；输入仍保留。');
      state.settings = data.settings; adopt(data.settings); saveMessage = '设置已保存'; saveError = false;
    } catch (error) { saveMessage = error.message; saveError = true; }
    finally { busy = false; paint(); renderStatus(); schedulePoll(); }
  });
  bar.querySelector('[data-admin-discard]').addEventListener('click', () => { if (busy) return; adopt(state.settings); saveMessage = ''; saveError = false; paint(); });
  $('[data-storage-test]').addEventListener('click', () => runAction('test', selected));
  all('[data-storage-action]').forEach(button => button.addEventListener('click', async () => {
    if (busy || readOnly) return;
    const action = button.dataset.storageAction, profileID = $('[data-storage-action-profile]').value;
    if (action !== 'cleanup') { runAction(action, profileID); return; }
    capture(); if (signature() !== baseline) { message('请先保存更改，再执行操作。', true); return; }
    if (!profileID) { message('请选择已保存的存储配置。', true); return; }
    const count = state.profile_counts?.[profileID]?.orphaned;
    if (count === 0) { message('此存储没有待清理副本。'); return; }
    const dialog = $('[data-storage-confirm]');
    $('[data-storage-confirm-copy]').textContent = '存储「' + profileName(profileID) + '」：' + (Number.isInteger(count) ? '将安排删除 ' + count + ' 个待清理对象。' : '将安排删除全部待清理对象。') + '请先核对下方运行记录。';
    dialog.returnValue = 'cancel'; dialog.showModal();
    dialog.addEventListener('close', () => { button.focus(); if (dialog.returnValue === 'confirm') runAction('cleanup', profileID, true); }, { once: true });
  }));
  $('[data-storage-refresh]').addEventListener('click', () => refresh());
  $('[data-storage-action-profile]').addEventListener('change', renderProfileSummary);
  $('[data-storage-record-filter]').addEventListener('change', () => { recordPage = 1; loadRecords(); });
  $('[data-record-profile]').addEventListener('change', () => { recordPage = 1; loadRecords(); });
  $('[data-record-search-button]').addEventListener('click', () => { recordPage = 1; loadRecords(); });
  $('[data-record-search]').addEventListener('keydown', event => { if (event.key === 'Enter') { event.preventDefault(); recordPage = 1; loadRecords(); } });
  $('[data-record-prev]').addEventListener('click', () => { if (recordPage > 1) { recordPage--; loadRecords(); } });
  $('[data-record-next]').addEventListener('click', () => { if (recordPage < recordPages) { recordPage++; loadRecords(); } });
  $('[data-record-export]').addEventListener('click', () => {
    if (readOnly) return;
    const url = URL.createObjectURL(new Blob([JSON.stringify(recordResults || [], null, 2)], { type: 'application/json' }));
    const a = el('a'); a.href = url; a.download = `storage-records-page-${recordPage}.json`; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  });
  for (const selector of ['[data-migration-source]', '[data-migration-target]']) $(selector).addEventListener('change', () => { migrationVersion = 0; paint(); });
  $('[data-migration-preview]').addEventListener('click', () => migrate(false));
  $('[data-migration-apply]').addEventListener('click', () => migrate(true));
  let cloudBusy = false, cloudNext = '', cloudEpoch = 0;
  const cloudStatus = text => { $('[data-cloud-status]').textContent = text; };
  function cloudPaint() {
    all('[data-cloud-open], [data-cloud-up], [data-cloud-upload], [data-cloud-profile], [data-cloud-path], [data-cloud-operation]').forEach(control => { control.disabled = readOnly || cloudBusy; });
    $('[data-cloud-next]').disabled = readOnly || cloudBusy || !cloudNext;
  }
  function cloudRoot() { const p = state.settings.profiles.find(p => p.id === $('[data-cloud-profile]').value); return p?.prefix ? p.prefix + '/' : ''; }
  function cloudPath() { const value = $('[data-cloud-path]').value.trim() || cloudRoot(); return value && !value.endsWith('/') ? value + '/' : value; }
  async function cloudList(next = false) {
    if (readOnly || cloudBusy) return;
    const profile = $('[data-cloud-profile]').value;
    if (!profile) { cloudStatus('请选择已保存的存储连接。'); return; }
    const epoch = ++cloudEpoch, directory = cloudPath(); cloudBusy = true; cloudPaint(); cloudStatus('正在读取云端目录…');
    try {
      const data = await request('/files?' + new URLSearchParams({ profile, key: directory, version: String(lastSavedVersion), token: next ? cloudNext : '' }), 'GET', undefined, 120000);
      if (epoch !== cloudEpoch) return;
      cloudNext = data.next; $('[data-cloud-path]').value = directory;
      const body = $('[data-cloud-files]'); body.replaceChildren();
      for (const file of data.files) {
        const row = el('tr'), name = el('td'), actions = el('td');
        const label = file.key.slice(directory.length) || file.key;
        if (file.directory) {
          const open = el('button', label, 'admin-button admin-button-quiet'); open.type = 'button'; open.dataset.cloudOperation = '';
          open.addEventListener('click', () => { $('[data-cloud-path]').value = file.key; cloudList(); }); name.append(open);
        } else {
          name.textContent = label;
          const download = el('a', '下载', 'admin-button admin-button-quiet');
          download.href = api + '/files?' + new URLSearchParams({ profile, key: file.key, version: String(lastSavedVersion), download: '1' }); actions.append(download);
          for (const [action, text] of [['copy', '复制'], ['move', '移动 / 重命名'], ['delete', '删除']]) {
            if (file.managed && action !== 'copy') continue;
            const button = el('button', text, 'admin-button admin-button-quiet'); button.type = 'button'; button.dataset.cloudOperation = '';
            button.addEventListener('click', () => cloudAction(action, profile, file.key)); actions.append(button);
          }
        }
        row.append(name, el('td', file.directory ? '—' : bytes(file.size)), el('td', file.managed ? '本站同步管理' : '独立文件'), actions); body.append(row);
      }
      cloudStatus(`当前页 ${data.files.length} 项${cloudNext ? '，可继续下一页' : ''}。`);
    } catch (error) { cloudNext = ''; cloudStatus(error.message); }
    finally { cloudBusy = false; cloudPaint(); }
  }
  async function cloudAction(action, profile, key) {
    if (cloudBusy || readOnly) return;
    const version = lastSavedVersion;
    let target = '', targetProfile = profile;
    if (action === 'delete') { if (!window.confirm('删除云端文件「' + key + '」？')) return; }
    else {
      const dialog = $('[data-cloud-dialog]'); $('[data-cloud-dialog-title]').textContent = action === 'copy' ? '复制文件' : '移动 / 重命名';
      $('[data-cloud-target-profile]').value = profile; $('[data-cloud-target]').value = key + '.copy';
      dialog.returnValue = 'cancel'; dialog.showModal();
      const confirmed = await new Promise(resolve => dialog.addEventListener('close', () => resolve(dialog.returnValue === 'confirm'), { once: true }));
      if (!confirmed) return;
      target = $('[data-cloud-target]').value; targetProfile = $('[data-cloud-target-profile]').value;
    }
    cloudBusy = true; cloudPaint(); cloudStatus('正在执行文件操作…');
    let outcome;
    try { await request('/files/action', 'POST', { action, profile, key, target, target_profile: targetProfile, expected_version: version }, 120000); outcome = '文件操作完成。'; }
    catch (error) { outcome = error.message; }
    finally { cloudBusy = false; cloudPaint(); await cloudList(); cloudStatus(outcome); }
  }
  $('[data-cloud-open]').addEventListener('click', () => cloudList());
  $('[data-cloud-next]').addEventListener('click', () => cloudList(true));
  $('[data-cloud-profile]').addEventListener('change', () => { cloudNext = ''; $('[data-cloud-path]').value = cloudRoot(); $('[data-cloud-files]').replaceChildren(); cloudList(); });
  $('[data-cloud-up]').addEventListener('click', () => { const parent = cloudPath().replace(/[^/]+\/$/, ''); $('[data-cloud-path]').value = parent.length >= cloudRoot().length ? parent : cloudRoot(); cloudList(); });
  $('[data-cloud-upload]').addEventListener('change', async event => {
    const files = [...event.target.files]; event.target.value = '';
    if (cloudBusy || readOnly || !files.length) return;
    const profile = $('[data-cloud-profile]').value, directory = cloudPath(), version = lastSavedVersion;
    if (!profile) { cloudStatus('请先选择存储。'); return; }
    cloudBusy = true; cloudPaint(); let completed = 0, errorText = '';
    try {
      for (const file of files) {
        if (file.size > 64 * 1024 * 1024) throw new Error(file.name + ' 超过 64 MiB。');
        cloudStatus(`正在上传 ${completed + 1} / ${files.length}：${file.name}`);
        const controller = new AbortController(); activeRequests.add(controller); const timer = setTimeout(() => controller.abort(), 120000);
        try {
          const response = await fetch(api + '/files/upload?' + new URLSearchParams({ profile, key: directory + file.name, version: String(version), expected_user_id: root.dataset.userId }), { method: 'POST', credentials: 'same-origin', headers: { 'X-CSRF-Token': root.dataset.csrf, 'Content-Type': 'application/octet-stream' }, body: file, signal: controller.signal });
          const data = await response.json(); if (!response.ok) throw new Error(data?.error?.message || '上传失败'); completed++;
        } finally { clearTimeout(timer); activeRequests.delete(controller); }
      }
    } catch (error) { errorText = error.message; }
    finally { cloudBusy = false; cloudPaint(); await cloudList(); cloudStatus(`已上传 ${completed} / ${files.length} 个文件。${errorText}`); }
  });
  cloudPaint();
  document.addEventListener('visibilitychange', () => { if (document.hidden) clearTimeout(pollTimer); else refresh(true); });
  window.addEventListener('pagehide', () => { clearTimeout(pollTimer); activeRequests.forEach(controller => controller.abort()); });
  window.addEventListener('beforeunload', event => { if (dirty) { event.preventDefault(); event.returnValue = ''; } });
  adopt(state.settings); renderStatus(); loadRecords(); schedulePoll();
})();
