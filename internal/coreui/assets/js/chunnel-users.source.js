(() => {
  'use strict';
  const page = document.querySelector('.admin-users-page');
  let dialog = page?.querySelector('[data-user-dialog]');
  if (!dialog) return;
  let opener = null, loading = null, busy = false, generation = 0;
  const baselines = new Map(), operations = new Map(), loaded = new Set();
  const node = (tag, text, cls) => { const el = document.createElement(tag); if (text !== undefined) el.textContent = text; if (cls) el.className = cls; return el; };
  const signature = form => JSON.stringify([...form.querySelectorAll('input,select,textarea')].filter(el => el.type !== 'hidden' || el.hasAttribute('data-user-value')).map(el => [el.name, el.value, el.checked, el.multiple ? [...el.selectedOptions].map(o => o.value) : null]));
  const track = form => { baselines.set(form, signature(form)); return form; };
  const dirty = () => [...baselines].some(([form, value]) => form.isConnected && signature(form) !== value);
  const feedback = (message = '', error = false) => { const el = dialog.querySelector('[data-user-feedback]'); el.textContent = message; el.dataset.tone = error ? 'error' : 'success'; };
  const dateLabel = value => window.ChunnelTime?.formatDateTime(value) || '—';
  const userQuery = () => '?username=' + encodeURIComponent(dialog.dataset.username);
  const button = (text, type = 'submit') => { const el = node('button', text, 'ui-button ui-button-primary'); el.type = type; return el; };
  const input = (name, value = '', type = 'text') => { const el = node('input'); Object.assign(el, { name, value, type }); return el; };
  const label = (text, control, help = '') => { const el = node('label'); el.append(node('span', text), control); if (help) el.append(node('small', help)); return el; };
  const select = (name, items, value) => { const el = node('select'); el.name = name; items.forEach(([key, text]) => el.add(new Option(text, key, false, key === value))); return el; };
  const reason = () => { const el = input('reason'); el.required = true; el.maxLength = 200; el.placeholder = '填写本次调整的原因'; return label('调整原因', el, '将保存在账户操作记录中。'); };
  const card = title => { const el = node('article', undefined, 'admin-user-account-card'); el.append(node('h3', title)); return el; };
  const footer = (form, text) => { const el = node('footer', undefined, 'admin-user-form-footer'); el.append(button(text)); form.append(el); track(form); return form; };
  const facts = items => { const el = node('dl', undefined, 'admin-user-facts'); items.forEach(([key, value]) => { const row = node('div'); row.append(node('dt', key), node('dd', value)); el.append(row); }); return el; };
  async function request(url, body) {
    let response;
    try { response = await fetch(url, { method: body ? 'POST' : 'GET', credentials: 'same-origin', cache: 'no-store', signal: AbortSignal.timeout(15000), headers: { Accept: 'application/json', ...(body ? { 'Content-Type': 'application/json', 'X-CSRF-Token': dialog.dataset.csrf } : {}) }, body: body ? JSON.stringify(body) : undefined }); }
    catch { throw new Error('连接中断，尚未确认操作结果。请重试原操作。'); }
    let result;
    try { result = await response.json(); } catch { throw new Error('未收到可确认的结果，请重试。'); }
    if (!response.ok) { const error = new Error(result.error?.message || '操作未完成，请重试'); error.status = response.status; throw error; }
    return result;
  }
  function checkAccount(result, id) {
    if (String(id) !== dialog.dataset.userId) throw new Error('账户信息已变化，请重新打开编辑窗口。');
    return result;
  }
  // Keep one receipt for an uncertain write. Retrying a confirmed write only
  // refreshes its view, so a lost response cannot issue a second credit.
  async function perform(form, url, body, refresh) {
    if (busy) return;
    if (operations.size && !operations.has(form)) { feedback('请先重试并确认上一项调整的结果。', true); return; }
    const prior = operations.get(form);
    const operation = prior || { url, body: { ...body, ...(body.request_id !== undefined ? { request_id: crypto.randomUUID() } : {}) }, saved: false, controls: [...form.elements].map(el => [el, el.disabled]) };
    operations.set(form, operation); busy = true;
    const controls = operation.controls;
    controls.forEach(([el]) => { el.disabled = true; });
    form.setAttribute('aria-busy', 'true'); feedback(operation.saved ? '正在更新账户信息…' : '正在保存…');
    try {
      if (!operation.saved) { await request(operation.url, operation.body); operation.saved = true; }
      await refresh();
      operations.delete(form); baselines.delete(form); if (form.isConnected) track(form);
      feedback('已保存，账户信息已更新。');
    } catch (error) {
      if (error.status && error.status < 500 && !operation.saved) operations.delete(form);
      feedback(operation.saved ? '修改已保存，显示信息暂未更新。点击重试刷新即可。' : error.message, true);
    } finally {
      busy = false; form.removeAttribute('aria-busy');
      controls.forEach(([el, disabled]) => { el.disabled = disabled; });
      if (operations.has(form)) {
        for (const el of form.elements) el.disabled = el.type !== 'submit';
        form.querySelector('[type="submit"]').textContent = '重试确认结果';
      }
    }
  }
  function activate(name, focus = false) {
    dialog.querySelectorAll('[data-user-tab]').forEach(tab => { const active = tab.dataset.userTab === name; tab.setAttribute('aria-selected', String(active)); tab.tabIndex = active ? 0 : -1; if (active && focus) tab.focus(); });
    dialog.querySelectorAll('[data-user-panel]').forEach(panel => { panel.hidden = panel.dataset.userPanel !== name; });
    dialog.querySelector('.admin-user-dialog-body').scrollTop = 0;
    if (['membership', 'wallets', 'recognition'].includes(name)) loadPanel(name);
  }
  async function loadPanel(name) {
    if (loaded.has(name)) return;
    const target = dialog.querySelector(`[data-user-${name}]`), serial = generation;
    if (dialog.dataset.assets !== 'true') { target.replaceChildren(node('p', '此账户的会员、资产和身份调整需要用户管理及站点设置权限。', 'admin-user-empty')); return; }
    loaded.add(name); target.replaceChildren(node('p', '正在读取账户信息…', 'admin-user-empty'));
    try {
      if (name === 'membership') {
        const data = checkAccount(await request('/api/v1/admin/progression/account' + userQuery()), dialog.dataset.userId);
        checkAccount(data, data.account.user_id); if (serial !== generation) return;
        const member = node('div'), experience = node('div'); target.replaceChildren(member, experience);
        paintMembership(member, data); paintExperience(experience, data);
      } else if (name === 'wallets') {
        const data = await request('/api/v1/admin/currencies');
        const wallets = await Promise.all(data.currencies.map(async item => {
          const url = '/api/v1/admin/currencies/' + encodeURIComponent(item.key);
          try { const wallet = await request(url + '/wallet' + userQuery()); return { url, wallet: checkAccount(wallet, wallet.user_id) }; }
          catch (error) { return { name: item.settings.name, error }; }
        }));
        if (serial !== generation) return; target.replaceChildren();
        for (const item of wallets) { const holder = node('div'); target.append(holder); if (item.error) holder.append(node('p', item.name + '：' + item.error.message, 'admin-user-empty')); else paintWallet(holder, item.url, item.wallet); }
        if (!wallets.length) target.append(node('p', '尚未配置积分或其他虚拟货币。', 'admin-user-empty'));
      } else {
        const [config, account] = await Promise.all([request('/api/v1/admin/recognition'), request('/api/v1/admin/recognition/account' + userQuery())]);
        checkAccount(account, account.user_id); if (serial !== generation) return;
        paintRecognition(target, config.settings, account);
      }
    } catch (error) {
      if (serial !== generation) return;
      loaded.delete(name); target.replaceChildren(node('p', error.message, 'admin-user-empty'));
      const retry = button('重新载入', 'button'); retry.addEventListener('click', () => loadPanel(name)); target.append(retry);
    }
  }
  function localTime(value) { return window.ChunnelTime.formatDateTime(value).replace(' ', 'T'); }
  function instant(value) {
    if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) throw new Error('请填写完整的会员起止时间。');
    const wanted = Date.parse(value + ':00Z'); let guess = wanted;
    for (let i = 0; i < 4; i++) {
      const shown = localTime(new Date(guess)); if (shown === value) return new Date(guess).toISOString();
      guess += wanted - Date.parse(shown + ':00Z');
    }
    throw new Error('该时间在站点时区中不存在，请选择其他时间。');
  }
  async function progression() { const data = await request('/api/v1/admin/progression/account' + userQuery()); return checkAccount(data, data.account.user_id); }
  function paintMembership(holder, data) {
    const member = data.account.membership, cfg = data.settings;
    const panel = card('会员权益');
    const planName = cfg.membership.plans.find(plan => plan.key === member.plan)?.name || '暂无会员';
    panel.append(facts([['当前会员', planName], ['状态', member.revoked ? '已撤销' : data.membership_active ? '生效中' : member.plan ? '未生效或已到期' : '未开通'], ['生效时间', dateLabel(member.since)], ['到期时间', dateLabel(member.until)]]));
    const form = node('form'), fields = node('fieldset', undefined, 'admin-user-fields');
    const mode = select('mode', [['set', '设置套餐与有效期'], ...(member.plan && !member.revoked ? [['revoke', '撤销会员']] : [])], 'set');
    const plans = select('plan', cfg.membership.plans.map(p => [p.key, p.name + (p.enabled ? '' : '（已停用）')]), member.plan || cfg.membership.plans[0]?.key);
    const since = input('since', member.plan ? localTime(member.since) : localTime(new Date()), 'datetime-local');
    const until = input('until', member.plan ? localTime(member.until) : localTime(new Date(Date.now() + 30 * 86400000)), 'datetime-local');
    since.required = until.required = true;
    fields.append(label('操作', mode), label('会员套餐', plans), label('生效时间', since), label('到期时间', until), reason());
    mode.addEventListener('change', () => { plans.disabled = since.disabled = until.disabled = mode.value === 'revoke'; });
    form.append(fields, node('p', '时间按站点时区 ' + window.ChunnelTime.zone() + ' 保存。', 'admin-user-help'));
    footer(form, '保存会员'); panel.append(form); holder.replaceChildren(panel);
    if (data.pending_order) { fields.disabled = true; form.querySelector('[type="submit"]').disabled = true; panel.append(node('p', '有尚未处理完成的会员订单，请先在会员管理中完成或取消该订单。', 'admin-user-help')); }
    form.addEventListener('submit', event => {
      event.preventDefault();
      try {
        const body = { user_id: dialog.dataset.userId, version: cfg.version, member_version: member.version, action: mode.value, plan: mode.value === 'revoke' ? '' : plans.value, days: 0, reason: form.elements.reason.value.trim(), request_id: '' };
        if (mode.value === 'set') { body.since = instant(since.value); body.until = instant(until.value); if (body.until <= body.since) throw new Error('到期时间必须晚于生效时间。'); }
        perform(form, '/api/v1/admin/progression/membership', body, async () => paintMembership(holder, await progression()));
      } catch (error) { feedback(error.message, true); }
    });
  }
  function paintExperience(holder, data) {
    const panel = card('经验与等级');
    panel.append(facts([['当前经验', data.account.experience], ['当前等级', data.level?.name || '未启用等级'], ['下一级', data.next_level ? `${data.next_level.name} · ${data.next_level.threshold} 经验` : '—']]));
    const form = node('form'), fields = node('fieldset', undefined, 'admin-user-fields');
    const delta = input('delta'); delta.required = true; delta.pattern = '-?[0-9]+'; delta.placeholder = '例如 100 或 -50';
    fields.append(label('调整经验', delta, '正数增加，负数扣减；等级随经验自动计算。'), reason()); form.append(fields); footer(form, '保存经验调整'); panel.append(form); holder.replaceChildren(panel);
    form.addEventListener('submit', event => { event.preventDefault(); if (!/^-?[0-9]+$/.test(delta.value) || BigInt(delta.value) === 0n) return feedback('请输入非零整数经验值。', true); perform(form, '/api/v1/admin/progression/experience', { user_id: dialog.dataset.userId, version: data.settings.version, delta: BigInt(delta.value).toString(), reason: form.elements.reason.value.trim(), request_id: '' }, async () => paintExperience(holder, await progression())); });
  }
  function paintWallet(holder, url, wallet) {
    const panel = card(wallet.settings.name);
    panel.append(facts([['当前余额', wallet.balance + ' ' + wallet.settings.symbol], ['币种状态', wallet.settings.enabled ? '已启用' : '已停用']]));
    const form = node('form'), fields = node('fieldset', undefined, 'admin-user-fields');
    const delta = input('delta'); delta.required = true; delta.placeholder = '例如 100 或 -20'; delta.inputMode = 'decimal';
    fields.append(label('调整数量', delta, `正数增加，负数扣减；最多 ${wallet.settings.precision} 位小数。`), reason()); form.append(fields); footer(form, '保存' + wallet.settings.name + '调整'); panel.append(form);
    if (wallet.entries.length) {
      const details = node('details', undefined, 'admin-user-history'); details.append(node('summary', '最近流水'));
      const list = node('ul'); for (const e of wallet.entries.slice(0, 8)) list.append(node('li', `${e.delta} ${wallet.settings.symbol} · ${e.reason || e.label} · ${dateLabel(e.created_at)}`)); details.append(list); panel.append(details);
    }
    holder.replaceChildren(panel);
    form.addEventListener('submit', event => { event.preventDefault(); perform(form, url + '/adjust', { user_id: dialog.dataset.userId, version: wallet.settings.version, delta: delta.value.trim(), reason: form.elements.reason.value.trim(), request_id: '' }, async () => { const fresh = await request(url + '/wallet' + userQuery()); paintWallet(holder, url, checkAccount(fresh, fresh.user_id)); }); });
  }
  function paintRecognition(holder, settings, account) {
    let identity = account.identity;
    const panel = card('头衔、勋章与认证'), summary = node('div');
    const form = node('form'), fields = node('fieldset', undefined, 'admin-user-fields');
    const action = select('action', [['grant', '授予或更新'], ['revoke', '撤销'], ['approve', '通过认证申请'], ['reject', '拒绝认证申请']], 'grant');
    const key = select('key', settings.badges.map(b => [b.key, ({ title: '头衔', medal: '勋章', verified: '认证' })[b.kind] + ' · ' + b.name + (b.enabled ? '' : '（已停用）')]), '');
    const title = input('label'), days = input('days', '0', 'number'); title.maxLength = 30; days.min = '0'; days.max = '3650'; days.required = true;
    fields.append(label('操作', action), label('身份标识', key), label('显示名称', title, '留空使用默认名称。'), label('有效天数', days, '0 表示永久有效。'), reason()); form.append(fields); footer(form, '保存身份调整');
    const equipped = node('form'), choices = node('fieldset', undefined, 'admin-user-medal-options'); choices.append(node('legend', '展示的勋章'));
    equipped.append(choices); footer(equipped, '保存勋章展示');
    const paintSummary = () => {
      summary.replaceChildren();
      for (const grant of identity.grants) { const badge = settings.badges.find(b => b.key === grant.key); summary.append(node('p', `${badge?.name || grant.key}${grant.label ? ' · ' + grant.label : ''} · ${grant.expires_at.startsWith('0001-') ? '永久有效' : '到期 ' + dateLabel(grant.expires_at)}`, 'admin-user-grant')); }
      if (!identity.grants.length) summary.append(node('p', '尚未授予头衔、勋章或认证。', 'admin-user-help'));
      if (identity.application) summary.append(node('p', `认证申请：${identity.application.label} · ${identity.application.status} · ${identity.application.evidence || ''}`, 'admin-user-help'));
      const selected = new Set([...choices.querySelectorAll('input:checked')].map(el => el.value));
      const preserve = baselines.has(equipped) && signature(equipped) !== baselines.get(equipped);
      choices.replaceChildren(node('legend', '展示的勋章（最多 ' + settings.max_medals + ' 枚）'));
      for (const grant of identity.grants) {
        const badge = settings.badges.find(b => b.key === grant.key);
        if (badge?.kind !== 'medal' || !badge.enabled || (!grant.expires_at.startsWith('0001-') && Date.parse(grant.expires_at) <= Date.now())) continue;
        const el = input('equipped', grant.key, 'checkbox'); el.checked = preserve ? selected.has(grant.key) : identity.equipped.includes(grant.key); choices.append(label(badge.name, el));
      }
      if (!preserve) track(equipped);
    };
    paintSummary(); panel.append(summary, form, equipped); holder.replaceChildren(panel);
    const refresh = async savedForm => { const fresh = await request('/api/v1/admin/recognition/account' + userQuery()); checkAccount(fresh, fresh.user_id); identity = fresh.identity; if (savedForm === form) form.reset(); else baselines.delete(equipped); paintSummary(); };
    form.addEventListener('submit', event => { event.preventDefault(); perform(form, '/api/v1/admin/recognition/account', { user_id: Number(dialog.dataset.userId), version: identity.version, action: action.value, key: key.value, label: title.value.trim(), evidence: '', reason: form.elements.reason.value.trim(), days: Number(days.value) }, () => refresh(form)); });
    equipped.addEventListener('submit', event => { event.preventDefault(); perform(equipped, '/api/v1/admin/recognition/account', { user_id: Number(dialog.dataset.userId), version: identity.version, action: 'equip', key: '', label: '', evidence: '', reason: '', days: 0, equipped: [...choices.querySelectorAll('input:checked')].map(el => el.value) }, () => refresh(equipped)); });
  }
  function close(force = false) {
    if (busy || operations.size) { feedback('正在确认保存结果，请完成或重试当前操作后再关闭。', true); return; }
    if (!force && dirty()) { dialog.querySelector('[data-user-discard]').hidden = false; dialog.querySelector('[data-user-keep]').focus(); return; }
    dialog.close(); document.body.classList.remove('admin-user-dialog-open');
    const target = opener?.isConnected ? opener : [...page.querySelectorAll('[data-user-edit]')].find(link => new URL(link.href).searchParams.get('edit') === dialog.dataset.userId) || page.querySelector('[data-user-create]');
    target?.focus();
  }
  function mount(next) {
    if (dialog !== next) { dialog.replaceWith(next); dialog = next; }
    generation++; baselines.clear(); operations.clear(); loaded.clear();
    dialog.querySelectorAll('form[data-user-native]').forEach(track);
    dialog.addEventListener('cancel', event => { event.preventDefault(); close(); });
    dialog.addEventListener('close', () => { document.body.classList.remove('admin-user-dialog-open'); });
    dialog.addEventListener('click', event => {
      if (event.target.closest('[data-user-close]')) close();
      if (event.target.closest('[data-user-discard-close]')) close(true);
      if (event.target.closest('[data-user-keep]')) dialog.querySelector('[data-user-discard]').hidden = true;
      const tab = event.target.closest('[data-user-tab]'); if (tab) activate(tab.dataset.userTab);
      const choose = event.target.closest('[data-user-avatar-choose]'), remove = event.target.closest('[data-user-avatar-remove]');
      if (choose || remove) {
        const field = dialog.querySelector('[data-user-avatar]'), value = field.querySelector('[name="avatar_media_id"]'), preview = field.querySelector('[data-user-avatar-preview]');
        const update = (id, url) => { value.value = String(id); if (url) { const img = node('img'); Object.assign(img, { src: url, alt: '所选头像', width: 48, height: 48 }); preview.replaceChildren(img); } else preview.replaceChildren(node('span', '无', 'admin-user-avatar')); value.dispatchEvent(new Event('input', { bubbles: true })); };
        if (remove) update(0, '');
        else if (window.NewPressMediaPicker) window.NewPressMediaPicker.open(choose, { title: '选择账户头像', uploadSource: 'avatar', disabled: () => busy || value.matches(':disabled') || !value.isConnected, onSelect: (url, item) => update(item.id, url) });
        else feedback('图片选择器正在载入，请稍后重试。', true);
      }
      if (event.target === dialog) { const rect = dialog.getBoundingClientRect(); if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) close(); }
    });
    dialog.addEventListener('keydown', event => {
      if (!event.target.matches('[data-user-tab]') || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
      const tabs = [...dialog.querySelectorAll('[data-user-tab]')], index = tabs.indexOf(event.target);
      const nextIndex = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
      event.preventDefault(); activate(tabs[nextIndex].dataset.userTab, true);
    });
    dialog.addEventListener('submit', nativeSubmit);
    dialog.showModal(); document.body.classList.add('admin-user-dialog-open');
    if (dialog.querySelector('#admin-user-password .has-error')) activate('security');
    dialog.querySelector('.has-error input, .has-error textarea, [name="username"]')?.focus();
  }
  async function openEditor(link) {
    if (busy || dialog.open) return;
    loading?.abort(); const controller = new AbortController(); loading = controller; opener = link;
    link.setAttribute('aria-busy', 'true');
    try {
      const response = await fetch(link.href, { credentials: 'same-origin', cache: 'no-store', signal: controller.signal });
      const doc = new DOMParser().parseFromString(await response.text(), 'text/html');
      const next = doc.querySelector('[data-user-dialog]');
      if (!response.ok || !next) throw new Error('用户资料暂时无法载入，请刷新页面后重试。');
      if (loading === controller) mount(next);
    } catch (error) { if (!controller.signal.aborted) window.ChunnelAdmin?.showToast(error.message, 'error'); }
    finally { link.removeAttribute('aria-busy'); if (loading === controller) loading = null; }
  }
  async function refreshList() {
    const url = new URL(location.href); ['edit', 'create', 'notice'].forEach(key => url.searchParams.delete(key));
    const response = await fetch(url, { credentials: 'same-origin', cache: 'no-store', signal: AbortSignal.timeout(15000) });
    if (!response.ok) return;
    const doc = new DOMParser().parseFromString(await response.text(), 'text/html'), index = doc.querySelector('.admin-users-index');
    if (index) { page.querySelector('.admin-users-index').replaceWith(index); document.dispatchEvent(new CustomEvent('chunnel:admin-updated')); }
  }
  async function nativeSubmit(event) {
    const form = event.target; if (!form.matches('[data-user-native]')) return;
    event.preventDefault(); if (busy || operations.size) return;
    if (form.getAttribute('id') === 'admin-user-form' && !form.querySelector('[name="role"]:checked')) return feedback('至少选择一个账户角色。', true);
    const data = new URLSearchParams(new FormData(form));
    const submitter = event.submitter || form.querySelector('button[type="submit"][name="action"]');
    if (submitter?.name) data.set(submitter.name, submitter.value);
    const oldBaseline = baselines.get(form), formID = form.getAttribute('id'), creating = data.get('action') === 'create';
    const controls = [...form.elements].map(el => [el, el.disabled]); controls.forEach(([el]) => { el.disabled = true; });
    busy = true; feedback('正在保存…');
    try {
      const response = await fetch(form.getAttribute('action'), { method: 'POST', body: data.toString(), credentials: 'same-origin', cache: 'no-store', signal: AbortSignal.timeout(15000), headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Accept': 'text/html' } });
      const doc = new DOMParser().parseFromString(await response.text(), 'text/html'), next = doc.querySelector('[data-user-dialog]');
      if (!next) throw new Error(response.status === 403 ? '会话已失效，请刷新页面后重新登录。' : '未能确认保存结果，请重新载入账户核对。');
      if (creating && response.ok) { closeAfterCreate(doc); return; }
      const replacement = next.querySelector('#' + formID);
      if (!replacement) throw new Error('未能读取保存后的账户资料。');
      form.replaceWith(replacement); baselines.delete(form); track(replacement);
      if (!response.ok) { baselines.set(replacement, oldBaseline); feedback(next.querySelector('[data-user-feedback]').textContent || '请检查输入后重试。', true); replacement.querySelector('.has-error input, .has-error textarea')?.focus(); return; }
      dialog.dataset.csrf = next.dataset.csrf; dialog.dataset.username = next.dataset.username;
      for (const el of dialog.querySelectorAll('[name="csrf_token"]')) el.value = next.dataset.csrf;
      const picker = document.querySelector('[data-editor-media-dialog]'); if (picker) picker.dataset.csrf = next.dataset.csrf;
      if (formID === 'admin-user-form') {
        const password = dialog.querySelector('#admin-user-password'), incoming = next.querySelector('#admin-user-password');
        if (password && incoming) {
          const secrets = [...password.querySelectorAll('[type="password"]')].map(el => el.value);
          password.replaceWith(incoming); baselines.delete(password); track(incoming);
          incoming.querySelectorAll('[type="password"]').forEach((el, i) => { el.value = secrets[i]; });
        }
        dialog.querySelector('.admin-user-dialog-heading').replaceWith(next.querySelector('.admin-user-dialog-heading'));
      } else {
        const version = next.querySelector('#admin-user-form [name="version"]')?.value;
        if (version) dialog.querySelector('#admin-user-form [name="version"]').value = version;
      }
      feedback('已保存，账户信息已更新。'); await refreshList();
    } catch (error) { feedback(error.message, true); }
    finally { busy = false; controls.forEach(([el, disabled]) => { el.disabled = disabled; }); }
  }
  function closeAfterCreate(doc) {
    baselines.clear(); busy = false; close(true);
    const index = doc.querySelector('.admin-users-index'); if (index) page.querySelector('.admin-users-index').replaceWith(index);
    window.ChunnelAdmin?.showToast('用户已创建，可再次打开编辑会员、积分和其他资料。', 'success');
    refreshList().catch(() => {});
  }
  page.addEventListener('click', event => { const link = event.target.closest('[data-user-edit], [data-user-create]'); if (!link || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button !== 0) return; event.preventDefault(); openEditor(link); });
  window.addEventListener('beforeunload', event => { if (dialog.open && (busy || operations.size || dirty())) { event.preventDefault(); event.returnValue = ''; } });
  if (dialog.dataset.autoOpen === 'true') mount(dialog);
})();
