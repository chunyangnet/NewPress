(() => {
  'use strict';
  const root = document.querySelector('[data-plugin-workspace]');
  if (!root) return;
  const $ = (selector, scope = root) => scope.querySelector(selector);
  const $$ = (selector, scope = root) => [...scope.querySelectorAll(selector)];
  const notice = $('[data-plugin-status]');
  const identity = Number(root.dataset.userId);
  const market = $('[data-plugin-market-content]');
  const marketLoading = $('[data-plugin-market-loading]');
  const search = $('[data-plugin-search]'), statusFilter = $('[data-plugin-status-filter]');
  const filters = { category: 'all', query: '', status: 'all' };
  const normalize = value => String(value || '').normalize('NFKC').toLocaleLowerCase();
  const bindDetails = () => {
    if (!market) return;
    $$('[data-plugin-card]', market).forEach(card => {
      const drawer = $('[data-plugin-details]', card);
      let detailOpener = $('[data-plugin-details-open]', card);
      const labels = $('.plugin-card-labels', card);
      if (labels) {
        const seen = new Set(); let count = 0;
        [...labels.children].forEach(label => {
          const text = normalize(label.textContent).trim();
          label.hidden = seen.has(text) || count >= 3;
          if (!label.hidden) { seen.add(text); count++; }
        });
      }
      if (!drawer?.showModal) return;
      $$('[data-plugin-details-open]', card).forEach(button => button.addEventListener('click', () => {
        detailOpener = button;
        drawer.showModal();
      }));
      $('[data-plugin-details-close]', drawer)?.addEventListener('click', () => drawer.close());
      drawer.addEventListener('click', event => {
        if (event.target !== drawer) return;
        const box = drawer.getBoundingClientRect();
        if (event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom) drawer.close();
      });
      drawer.addEventListener('close', () => { if (drawer.dataset.replacing !== 'true' && detailOpener?.isConnected) detailOpener.focus({preventScroll:true}); });
    });
  };
  bindDetails();
  const applyFilters = () => {
    if (!market) return;
    const cards = $$('[data-plugin-card]', market);
    const categories = $$('[data-plugin-category]', market);
    if (!categories.some(button => button.dataset.pluginCategory === filters.category)) filters.category = 'all';
    const words = normalize(filters.query).trim().split(/\s+/).filter(Boolean);
    const counts = new Map();
    let matching = 0, visible = 0;
    cards.forEach(card => {
      const text = normalize(card.dataset.search);
      const stateMatches = filters.status === 'all' ||
        filters.status === 'installed' && card.dataset.installed === 'true' ||
        filters.status === 'enabled' && card.dataset.enabled === 'true' ||
        filters.status === 'available' && card.dataset.installed !== 'true' ||
        filters.status === 'updates' && card.dataset.update === 'true';
      const matches = stateMatches && words.every(word => text.includes(word));
      const category = card.dataset.category || 'other';
      if (matches) { matching++; counts.set(category, (counts.get(category) || 0) + 1); }
      card.hidden = !(matches && (filters.category === 'all' || filters.category === category));
      if (!card.hidden) visible++;
    });
    categories.forEach(button => {
      const category = button.dataset.pluginCategory;
      button.setAttribute('aria-pressed', String(category === filters.category));
      const count = $('[data-category-count]', button);
      if (count) count.textContent = category === 'all' ? matching : counts.get(category) || 0;
    });
    const result = $('[data-plugin-result-count]'), empty = $('[data-plugin-empty]');
    if (result) result.textContent = `${visible} / ${cards.length} 个插件`;
    if (empty) empty.hidden = visible !== 0;
  };
  const bindFilters = () => {
    if (!market) return;
    const categories = $('[data-plugin-categories]', market);
    if (categories) categories.hidden = false;
    $$('[data-plugin-category]', market).forEach(button => button.addEventListener('click', () => {
      filters.category = button.dataset.pluginCategory; applyFilters();
    }));
    $('[data-plugin-clear]', market)?.addEventListener('click', () => {
      filters.category = 'all'; filters.query = ''; filters.status = 'all';
      if (search) search.value = '';
      if (statusFilter) statusFilter.value = 'all';
      applyFilters(); search?.focus();
    });
    applyFilters();
  };
  if (market) {
    const tools = $('[data-plugin-tools]'); if (tools) tools.hidden = false;
    search?.addEventListener('input', () => { filters.query = search.value; applyFilters(); });
    statusFilter?.addEventListener('change', () => { filters.status = statusFilter.value; applyFilters(); });
    bindFilters();
  }
  let busy = false, dirty = false, removing = false, accessLost = false, disposed = false;
  let marketRequest, marketGeneration = 0, marketTimer;
  const show = (message, error = false, scroll = error) => { notice.hidden = false; notice.textContent = message; notice.dataset.tone = error ? 'error' : 'success'; if (scroll) notice.scrollIntoView({ block: 'nearest' }); };
  const resultMessage = (target, message, tone = 'success') => { target.hidden = false; target.textContent = message; target.dataset.tone = tone; };
  const setMarketLoading = (loading, refresh = true) => {
    if (!market) return;
    root.dataset.marketLoading = String(loading);
    if (loading) market.setAttribute('aria-busy', 'true'); else market.removeAttribute('aria-busy');
    if (marketLoading) {
      marketLoading.hidden = !loading;
      $('[data-plugin-loading-title]', marketLoading).textContent = refresh ? '正在加载插件市场' : '正在更新插件列表';
      $('[data-plugin-loading-detail]', marketLoading).textContent = refresh ? '正在检测官方源并获取目录，请稍候…' : '正在同步最新插件状态，请稍候…';
    }
    if (loading) notice.hidden = true;
  };
  const stopMarketRequest = () => { marketGeneration++; marketRequest?.abort(); marketRequest = undefined; setMarketLoading(false); };
  const loseAccess = () => {
    accessLost = true; stopMarketRequest(); clearTimeout(marketTimer);
    $$('[data-plugin-action], [data-plugin-remove], [data-uninstall-confirm]').forEach(button => { button.disabled = true; });
  };
  const request = async (path, method, body) => {
    const response = await fetch(path, { method, credentials: 'same-origin', cache: 'no-store', headers: { Accept: 'application/json', ...(body ? { 'Content-Type': 'application/json', 'X-CSRF-Token': root.dataset.csrf } : {}) }, body: body ? JSON.stringify({ ...body, expected_user_id: identity }) : undefined });
    const result = await response.json().catch(() => null);
    if (!response.ok) {
      if (response.status === 401 || response.status === 403 || result?.error?.code === 'plugins.identity_changed') loseAccess();
      throw new Error(result?.error?.message || result?.message || (response.status === 401 || response.status === 403 ? '登录状态或权限已变化，请保留输入并刷新后重试。' : '操作未完成，请核对当前状态后重试。'));
    }
    if (!result || typeof result !== 'object' || Array.isArray(result)) throw new Error('未收到有效结果，输入已保留，请刷新核对登录状态后重试。');
    return result;
  };
  const perform = async (button, work, feedback) => {
    if (accessLost || disposed || root.dataset.readonly === 'true') return;
    if (busy) { show('另一项操作正在处理，请完成后重试。'); return; }
    busy = true;
    stopMarketRequest();
    const refreshing = button.dataset.pluginAction === 'refresh';
    if (refreshing) setMarketLoading(true);
    const wasDisabled = button.disabled;
    button.disabled = true; button.setAttribute('aria-busy', 'true');
    try { await work(); } catch (error) { const message = error.message || '连接中断，请核对操作结果后重试。'; const detailFeedback = button.closest?.('[data-plugin-details]')?.querySelector('[data-plugin-detail-status]'); if (dialog?.open) { const errorBox = $('[data-uninstall-error]'); errorBox.hidden = false; errorBox.textContent = message; } else if (detailFeedback) resultMessage(detailFeedback, message, 'error'); else if (feedback) resultMessage(feedback, message, 'error'); else show(message, true); }
    finally { busy = false; if (refreshing) setMarketLoading(false); if (!button.hasAttribute?.('data-admin-save')) button.disabled = wasDisabled || accessLost || root.dataset.readonly === 'true'; button.removeAttribute('aria-busy'); }
  };
  const bindActions = scope => $$('[data-plugin-action]', scope).forEach(button => button.addEventListener('click', () => { if (button.disabled) return; return perform(button, async () => {
    let result;
    try {
      result = await request('/api/v1/admin/plugins', 'POST', { action: button.dataset.pluginAction, slug: button.dataset.slug || '', digest: button.dataset.digest || '' });
    } catch (error) {
      if (button.dataset.pluginAction === 'refresh' && market && !accessLost && !disposed) {
        // Refresh can fail after recording useful per-source diagnostics.
        // Read that snapshot without starting another round of network probes.
        try { await syncMarket(false); } catch (snapshotError) { if (accessLost) throw snapshotError; }
      }
      throw error;
    }
    if (result.ok !== true) throw new Error('未收到完成确认，请核对当前状态后重试。');
    if (button.dataset.pluginAction === 'retry') {
      resultMessage($('[data-retry-result]'), '已重新安排未完成查询。配置草稿已保留，运行统计在下次打开页面时更新。');
      return;
    }
    if (market) { await syncMarket(false); if (!market.dataset.marketError) show('插件状态已更新。'); }
    else location.reload();
  }); }));
  bindActions(root);
  const dialog = $('[data-plugin-uninstall]');
  let removeTarget = '', opener;
  const bindRemoval = scope => $$('[data-plugin-remove]', scope).forEach(button => button.addEventListener('click', () => {
    if (busy || accessLost || button.disabled) return;
    stopMarketRequest();
    opener = button; removeTarget = button.dataset.pluginRemove;
    $('[data-uninstall-name]').textContent = button.dataset.name;
    $('[data-uninstall-error]').hidden = true;
    dialog.showModal(); $('button[value="cancel"]', dialog).focus();
  }));
  bindRemoval(root);
  async function syncMarket(refresh = true) {
    if (!market || accessLost || disposed) return;
    stopMarketRequest();
    const generation = marketGeneration;
    const controller = new AbortController(); marketRequest = controller;
    let timedOut = false;
    const timeout = setTimeout(() => { timedOut = true; controller.abort(); }, 12000);
    setMarketLoading(true, refresh);
    try {
      const response = await fetch('/admin/plugins/' + (refresh ? '?market=1' : ''), { credentials: 'same-origin', cache: 'no-store', signal: controller.signal, headers: { Accept: 'text/html' } });
      const html = await response.text();
      if (generation !== marketGeneration || disposed) return;
      if (response.status === 401 || response.status === 403) { loseAccess(); throw new Error('登录状态或插件管理权限已变化，请重新登录。'); }
      if (!response.ok) throw new Error('市场更新暂时不可用，已保留当前插件列表。');
      const parsed = new DOMParser().parseFromString(html, 'text/html');
      const workspace = parsed.querySelector('[data-plugin-workspace]');
      if (!workspace || Number(workspace.dataset.userId) !== identity) { loseAccess(); throw new Error('登录账号已变化，请重新登录后查看插件市场。'); }
      const content = workspace.querySelector('[data-plugin-market-content]');
      if (!content) throw new Error('市场响应不完整，已保留当前插件列表。');
      const focused = document.activeElement?.closest?.('[data-plugin-card]')?.dataset.pluginCard;
      const focusedCategory = document.activeElement?.dataset?.pluginCategory;
      const openDetails = $$('[data-plugin-card]', market).filter(card => $('[data-plugin-details]', card)?.open).map(card => card.dataset.pluginCard);
      $$('[data-plugin-details]', market).forEach(drawer => { if (drawer.open && drawer.close) { drawer.dataset.replacing = 'true'; drawer.close(); } });
      const expanded = $('.admin-plugin-source', market)?.open;
      market.replaceChildren(...content.childNodes);
      market.dataset.marketError = content.dataset.marketError || '';
      root.dataset.csrf = workspace.dataset.csrf;
      root.dataset.readonly = workspace.dataset.readonly;
      const refreshButton = $('[data-plugin-action="refresh"]'), nextRefresh = workspace.querySelector('[data-plugin-action="refresh"]');
      if (refreshButton && nextRefresh) refreshButton.disabled = busy && refreshButton.hasAttribute('aria-busy') || nextRefresh.disabled;
      bindActions(market); bindRemoval(market); bindFilters(); bindDetails();
      $$('[data-plugin-card]', market).forEach(card => {
        const details = $('[data-plugin-details]', card);
        if (details && openDetails.includes(card.dataset.pluginCard)) {
          if (details.showModal) details.showModal(); else details.open = true;
        }
      });
      const source = $('.admin-plugin-source', market); if (source && (expanded || market.dataset.marketError)) source.open = true;
      if (focused) $$('[data-plugin-card]', market).find(card => !card.hidden && card.dataset.pluginCard === focused)?.querySelector('button:not(:disabled),a')?.focus();
      if (focusedCategory) $$('[data-plugin-category]', market).find(button => button.dataset.pluginCategory === focusedCategory || button.dataset.pluginCategory === filters.category)?.focus();
      if (market.dataset.marketError) show('官方目录检查失败，已保留当前插件列表。' + market.dataset.marketError, true, false);
      else notice.hidden = true;
    } catch (error) {
      if (disposed || generation !== marketGeneration && !accessLost) return;
      if (timedOut) throw new Error('插件市场加载超时，已保留当前列表，请点击刷新重试。');
      throw error;
    } finally {
      clearTimeout(timeout);
      if (generation === marketGeneration) { marketRequest = undefined; setMarketLoading(false); }
    }
  }
  const scheduleMarket = () => {
    clearTimeout(marketTimer);
    if (!disposed && !accessLost && !document.hidden) marketTimer = setTimeout(autoMarket, 60000);
  };
  async function autoMarket() {
    if (disposed || accessLost || document.hidden || !market || busy || dialog?.open || $$('[data-plugin-details]', market).some(drawer => drawer.open) || marketRequest) { scheduleMarket(); return; }
    try { await syncMarket(root.dataset.marketConfigured === 'true'); }
    catch (error) { if (!disposed && error.name !== 'AbortError') show(error.message || '市场连接中断，已保留当前插件列表。', true, false); }
    finally { scheduleMarket(); }
  }
  if (market) {
    window.addEventListener('focus', autoMarket);
    window.addEventListener('pageshow', event => { if (event.persisted) { disposed = false; autoMarket(); } });
    document.addEventListener('visibilitychange', () => { if (!document.hidden) autoMarket(); else clearTimeout(marketTimer); });
    window.addEventListener('pagehide', () => { disposed = true; stopMarketRequest(); clearTimeout(marketTimer); });
    autoMarket();
  }
  dialog?.addEventListener('close', () => opener?.focus());
  dialog?.addEventListener('cancel', event => { if (removing) event.preventDefault(); });
  $('[data-uninstall-confirm]')?.addEventListener('click', event => perform(event.currentTarget, async () => {
    removing = true;
    const cancelButtons = $$('button[value="cancel"]', dialog);
    cancelButtons.forEach(button => { button.disabled = true; });
    try {
      const result = await request('/api/v1/admin/plugins', 'POST', { action: 'uninstall', slug: removeTarget });
      if (result.ok !== true) throw new Error('未收到卸载确认，请刷新核对插件状态。');
      dialog.close();
      if (market) { await syncMarket(false); show('插件已卸载，列表已更新。'); }
      else location.reload();
    } finally {
      removing = false;
      cancelButtons.forEach(button => { button.disabled = false; });
    }
  }));
  const form = $('[data-origin-settings]');
  if (form) {
    const sharedSavebar = window.ChunnelAdminUI?.savebar(form, { onDiscard: () => updateDraft() });
    const field = name => form.elements.namedItem(name);
    const isCheckedField = input => input.type === 'checkbox' || input.type === 'radio';
    const formState = () => JSON.stringify([...form.querySelectorAll('input,select')].map(input => [input.name || input.dataset.rule || '', isCheckedField(input) ? input.checked : input.value]));
    const state = $('[data-settings-state]') || $('[data-save-state]');
    let baseline = formState();
    const providerHelp = {
      ipwhois: '免费免密钥，支持 IPv4 / IPv6。每天最多 900 次以预留服务额度；可能返回英文地名，不保证每个 IP 都能定位到城市。',
      amap: '高德普通 IP 定位仅支持国内 IPv4；海外／局域网可能返回空结果。请填写 Web 服务 Key。',
      baidu: '百度普通 IP 定位主要面向国内，默认使用 IPv4。IPv6 需向百度单独申请权限；AK 请配置服务端 IP 白名单。',
      custom: '适配返回 JSON 的 HTTPS GET 接口。字段使用点路径（例如 data.city），成功状态必须匹配；密钥使用 {key} 占位符。'
    };
    const preview = () => {
      const provider = field('provider').value;
      $('[data-provider-help]').textContent = providerHelp[provider];
      $('[data-provider-key]').hidden = provider === 'ipwhois';
      $('[data-provider-clear]').hidden = provider === 'ipwhois';
      $('[data-provider-custom]').hidden = provider !== 'custom';
      const selected = $('[data-preview-role]').value;
      const row = $$('[data-origin-role]').find(item => item.dataset.originRole === selected);
      const locationShown = field('location').checked && Boolean(row && $('[data-rule="location"]', row)?.checked);
      const deviceShown = field('device').checked && Boolean(row && $('[data-rule="device"]', row)?.checked);
      $('[data-preview-location]').hidden = !locationShown;
      $('[data-preview-device]').hidden = !deviceShown;
      $('[data-preview-hidden]').hidden = locationShown || deviceShown;
      const place = $('[data-preview-country]').value;
      const sample = { domestic: ['浙江 · 杭州', 'cn'], foreign: ['日本 · 东京', 'jp'], unknown: ['浙江 · 杭州', ''] }[place];
      const mode = field('flag_mode').value;
      const flagShown = locationShown && Boolean(sample[1]) && (mode === 'all' || mode === 'foreign' && place === 'foreign');
      const flag = $('[data-preview-flag]');
      flag.hidden = !flagShown;
      if (flagShown) flag.src = `/plugin-assets/comment-origin/flags/${sample[1]}.svg`;
      $('[data-preview-globe]').hidden = flagShown;
      $('[data-preview-location-label]').textContent = sample[0];
      $('[data-flag-options]').disabled = !field('location').checked || root.dataset.readonly === 'true' || accessLost;
      $('[data-flag-hint]').textContent = !field('location').checked ? '已关闭归属地，国旗也会隐藏。显示模式会保留。' : place === 'unknown' ? '示例未提供国家代码，使用地球图标。' : '国旗遵循归属地的用户组权限；“仅国外”下，中国内地及港澳台不显示旗帜。';
      const kind = $('[data-preview-device-select]').value;
      const device = { mobile: ['iOS', 'Safari'], desktop: ['Windows', 'Edge'], tablet: ['iPadOS', 'Safari'] }[kind];
      $('[data-preview-device-label]').textContent = field('browser').checked ? device.join(' · ') : device[0];
      $$('[data-device-icon]').forEach(icon => { icon.hidden = icon.dataset.deviceIcon !== kind; });
    };
    const updateDraft = () => { dirty = formState() !== baseline; if (sharedSavebar) sharedSavebar.refresh(); else state.textContent = dirty ? '未保存' : ''; preview(); };
    form.addEventListener('input', updateDraft);
    form.addEventListener('change', updateDraft);
    $('[data-preview-role]').addEventListener('change', preview);
    $('[data-preview-device-select]').addEventListener('change', preview);
    $('[data-preview-country]').addEventListener('change', preview);
    preview();
    window.addEventListener('beforeunload', event => { if (dirty) { event.preventDefault(); event.returnValue = ''; } });
    form.addEventListener('submit', event => {
      event.preventDefault();
      if (busy) return;
      const settings = { version: Number(form.dataset.version) };
      ['provider','key','endpoint','country_path','country_code_path','region_path','city_path','success_path','success_value','flag_mode'].forEach(name => settings[name] = field(name).value.trim());
      ['location','device','browser','fallback'].forEach(name => settings[name] = field(name).checked);
      ['retention_days','cache_days','daily_limit'].forEach(name => settings[name] = Number(field(name).value));
      settings.rules = $$('[data-origin-role]').map(row => ({ role: row.dataset.originRole, location: $('[data-rule="location"]', row).checked, device: $('[data-rule="device"]', row).checked }));
      const snapshot = formState(), clearKey = field('clear_key').checked;
      if (sharedSavebar && !sharedSavebar.start()) return;
      perform($('button[type="submit"]', form), async () => {
        try {
        const result = await request('/api/v1/admin/plugins/comment-origin/settings', 'PUT', { settings, clear_key: clearKey });
        if (!Number.isSafeInteger(result.settings?.version) || result.settings.version <= settings.version || typeof result.key_saved !== 'boolean') throw new Error('未收到完整的保存确认，输入已保留，请刷新核对后重试。');
        form.dataset.version = result.settings.version;
        // Keep edits typed while a save was in flight; only clear the submitted secret.
        dirty = snapshot !== formState();
        if (field('key').value.trim() === settings.key) field('key').value = '';
        field('key').placeholder = result.key_saved ? '已保存，留空保留现有密钥' : '填写服务提供方的密钥';
        if (field('clear_key').checked === clearKey) field('clear_key').checked = false;
        baseline = JSON.stringify(JSON.parse(snapshot).map(([name, value]) => [name, name === 'key' ? '' : name === 'clear_key' ? false : value]));
        if (sharedSavebar) {
          const submitted = sharedSavebar.snapshot();
          const snapshotValues = JSON.parse(baseline);
          submitted.forEach((item, i) => { const stored = snapshotValues[i]; if (!stored) return; if (isCheckedField(item.input)) item.checked = stored[1]; else item.value = stored[1]; });
          sharedSavebar.success(submitted);
        } else { state.textContent = dirty ? '未保存' : '已保存'; show(state.textContent); }
        } catch (error) { if (sharedSavebar) { sharedSavebar.fail(error.message); return; } throw error; }
      });
    });
  }
  const tester = $('[data-origin-test]');
  tester?.addEventListener('submit', event => { event.preventDefault(); perform($('button', tester), async () => {
    const ip = tester.elements.ip.value.trim();
    resultMessage($('[data-test-result]'), `正在查询 ${ip}…`, 'pending');
    const result = await request('/api/v1/admin/plugins/comment-origin/test', 'POST', { ip });
    const precision = { city:'城市级', region:'省／地区级', country:'国家级' }[result.precision] || '未知精度';
    resultMessage($('[data-test-result]'), `${ip}\n${[result.country,result.region,result.city].filter(Boolean).join(' · ')}\n${precision} · ${result.provider}${dirty ? '\n当前测试使用已保存配置，表单中仍有未保存修改。' : ''}`);
  }, $('[data-test-result]')); });
  const record = $('[data-origin-record]');
  record?.addEventListener('submit', event => { event.preventDefault(); perform($('button', record), async () => {
    const id = Number(record.elements.comment_id.value);
    resultMessage($('[data-record-result]'), '正在读取私有记录…', 'pending');
    if (!Number.isSafeInteger(id) || id < 1) throw new Error('请输入有效评论编号');
    const result = await request(`/api/v1/admin/plugins/comment-origin/records/${id}`, 'GET');
    const status = { pending: '等待查询', resolved: '已定位', failed: '查询失败', local: '非公网地址', expired: '原始记录已到期', unavailable: '来源未确认' }[result.status] || '暂无状态';
    const missingIP = result.status === 'unavailable' ? '未采集（来源未通过验证）' : '已到期清除或未采集';
    resultMessage($('[data-record-result]'), `评论 #${result.id} · ${status}\n来源方式：${result.source_label || '旧记录未标记'}\n来源 IP：${result.ip || missingIP}\n归属地：${[result.location.country,result.location.region,result.location.city].filter(Boolean).join(' · ') || '暂无结果'}\nUA：${result.ua || '已到期清除或未采集'}${result.source_issue_label ? '\n'+result.source_issue_label : ''}${result.error ? '\n'+result.error : ''}`);
  }, $('[data-record-result]')); });
})();
