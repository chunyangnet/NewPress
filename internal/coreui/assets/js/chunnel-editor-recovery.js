(() => {
  'use strict';
  const ROOT = 'newpress:editor-recovery:v1:';
  const MAX_LENGTH = 256 * 1024, MAX_COPIES = 8;
  const fields = new Set(['pinned', 'ai_pro_max', 'title', 'slug', 'content_source', 'excerpt', 'status', 'published_at', 'published_timezone', 'published_source', 'type', 'comment_status', 'category', 'category_ids', 'tags', 'cover_url', 'version', 'article_badge_kind', 'article_badge_text', 'article_badge_style', 'article_badge_tone', 'article_badge_icon', 'article_badge_placement', 'article_badge_hide_mobile']);
  const routeID = route => route === '/admin/content/new/' ? 0 : /^\/admin\/content\/([1-9]\d*)\/edit\/$/.test(route) ? Number(route.split('/')[3]) : -1;
  const signature = data => {
    const values = {};
    data.forEach((value, key) => {
      if (key === 'action' || key === 'csrf_token') return;
      values[key] = Object.hasOwn(values, key) ? [].concat(values[key], value) : value;
    });
    return JSON.stringify(Object.fromEntries(Object.keys(values).sort().map(key => [key, values[key]])));
  };
  const pairs = (data, action = false) => {
    if (!Array.isArray(data) || data.length > 40) throw new Error('invalid fields');
    const counts = new Map();
    for (const pair of data) {
      if (!Array.isArray(pair) || pair.length !== 2 || pair.some(value => typeof value !== 'string')) throw new Error('invalid field');
      const [name] = pair, count = (counts.get(name) || 0) + 1;
      if ((!fields.has(name) && !(action && name === 'action')) || count > (name === 'category_ids' ? 8 : 1)) throw new Error('unexpected field');
      counts.set(name, count);
    }
    if (!counts.has('title') || !counts.has('content_source') || !counts.has('type') || !counts.has('status')) throw new Error('incomplete draft');
    const values = new URLSearchParams(data), zone = values.get('published_timezone');
    if (!zone || zone.length > 100 || !['canonical', 'autosave'].includes(values.get('published_source'))) throw new Error('incomplete publication metadata');
    return values;
  };
  const versionOf = (body, id) => {
    const raw = body.get('version');
    if (id === 0 && raw === null) return 0;
    if (id <= 0 || !/^[1-9]\d*$/.test(raw || '') || !Number.isSafeInteger(Number(raw))) throw new Error('invalid version');
    return Number(raw);
  };
  function createStore(storage, owner, hours, now = Date.now) {
    const prefix = ROOT + 'draft:' + encodeURIComponent(owner) + ':';
    const preference = ROOT + 'enabled:' + encodeURIComponent(owner);
    const retention = Math.min(144, Math.max(1, Number(hours) || 72)) * 3600000;
    const keys = () => {
      if (storage.length > 2048) throw new Error('storage too large');
      return Array.from({length: storage.length}, (_, index) => storage.key(index)).filter(Boolean);
    };
    function decode(raw, key) {
      if (!raw || raw.length > MAX_LENGTH) throw new Error('invalid size');
      const value = JSON.parse(raw), time = now();
      if (value.schema !== 1 || value.owner !== owner || key !== prefix + value.id || !/^[a-f0-9]{32}$/.test(value.id) ||
          !Number.isSafeInteger(value.updated) || value.updated > time + 60000 || !Number.isSafeInteger(value.expires) ||
          value.expires <= time || value.expires > value.updated + retention || !Number.isSafeInteger(routeID(value.route)) || routeID(value.route) < 0) throw new Error('invalid scope or expiry');
      const draft = pairs(value.draft), id = routeID(value.route), version = versionOf(draft, id);
      if (value.pending !== null) {
        const p = value.pending;
        if (!p || typeof p.body !== 'string' || p.route !== value.route || !/^editor-[a-f0-9]{32}$/.test(p.key) || typeof p.wasDirty !== 'boolean' ||
            !Number.isSafeInteger(p.createdAt) || p.createdAt > value.updated || value.expires > p.createdAt + retention) throw new Error('invalid pending write');
        const body = pairs([...new URLSearchParams(p.body)], true), action = body.get('action');
        if (!/^(save|publish|trash|restore|restore_revision-[1-9]\d*)$/.test(action || '') || versionOf(body, id) !== version || (!id && action !== 'save')) throw new Error('invalid pending action');
        value.pending = {...p, id, version, action: id ? action.replace(/^restore_revision-\d+$/, 'restore_revision') : 'create', signature: signature(body), uncertain: true};
      }
      return {...value, raw};
    }
    function list(route) {
      const entries = [];
      for (const key of keys().filter(key => key.startsWith(prefix))) {
        try {
          const value = decode(storage.getItem(key), key);
          if (!route || value.route === route) entries.push(value);
        } catch { storage.removeItem(key); }
      }
      return entries.sort((a, b) => b.updated - a.updated);
    }
    return {
      list,
      enabled: () => storage.getItem(preference) === '1',
      enable: value => value ? storage.setItem(preference, '1') : storage.removeItem(preference),
      clear() { for (const key of keys().filter(key => key.startsWith(prefix))) storage.removeItem(key); storage.removeItem(preference); },
      read(id) { return decode(storage.getItem(prefix + id), prefix + id); },
      remove(id, raw) { if (raw === undefined || storage.getItem(prefix + id) === raw) storage.removeItem(prefix + id); },
      write(id, snapshot, previousID) {
        const time = now(), p = snapshot.pending;
        const body = p ? new URLSearchParams(p.body) : null;
        body?.delete('csrf_token');
        const value = {schema: 1, owner, id, route: snapshot.route, updated: time,
          expires: Math.min(time + retention, p ? p.createdAt + retention : Infinity),
          draft: snapshot.draft.filter(([name]) => name !== 'csrf_token' && name !== 'action'),
          pending: p ? {key: p.key, route: p.route, body: body.toString(), wasDirty: p.wasDirty, createdAt: p.createdAt} : null};
        const raw = JSON.stringify(value);
        if (raw.length > MAX_LENGTH) throw new Error('size');
        decode(raw, prefix + id);
        list(); // Remove expired or invalid copies belonging to this identity only.
        // Snapshot keys are immutable. A concurrent writer gets a new key, so
        // restoring/deleting an older copy cannot remove that writer's new text.
        if (storage.getItem(prefix + id)) throw new Error('collision');
        if (!(previousID && storage.getItem(prefix + previousID)) && keys().filter(key => key.startsWith(ROOT + 'draft:')).length >= MAX_COPIES) throw new Error('capacity');
        storage.setItem(prefix + id, raw);
        if (previousID && previousID !== id) storage.removeItem(prefix + previousID);
        return decode(raw, prefix + id);
      }
    };
  }
  function mount(options) {
    const panel = document.querySelector('[data-browser-recovery]');
    let store;
    try {
      store = createStore(window.localStorage, options.owner, options.hours);
      if (!options.allowed) { store.clear(); return null; }
    } catch { /* The editor itself remains usable when storage is blocked. */ }
    if (!panel) return null;
    panel.hidden = false;
    const toggle = panel.querySelector('[data-browser-recovery-toggle]'), list = panel.querySelector('[data-browser-recovery-list]');
    const status = panel.querySelector('[data-browser-recovery-status]'), count = panel.querySelector('[data-browser-recovery-count]');
    const clear = panel.querySelector('[data-browser-recovery-clear]');
    let timer = 0, records = [], busy = false, paused = false, restored = null;
    const newID = () => { const bytes = new Uint8Array(16); window.crypto.getRandomValues(bytes); return Array.from(bytes, value => value.toString(16).padStart(2, '0')).join(''); };
    let id = newID();
    const say = (message, error = false) => { status.textContent = message; status.dataset.tone = error ? 'error' : ''; if (error) panel.open = true; };
    const failed = error => say(error?.message === 'size' ? '内容超过本机副本上限，本次未更新；请保留页面并正式保存。' : error?.message === 'capacity' ? '本机副本已满（最多 8 份）；清理不需要的副本后再试。' : '浏览器未能更新本机副本；当前输入仍在页面，请保留页面并正式保存。', true);
    const blocked = () => records.some(record => record.id !== id && record.pending && record.pending.key !== options.pending()?.key);
    function render() {
      try {
        records = store.list(options.route());
        const others = records.filter(record => record.id !== id);
        count.textContent = others.length ? `${others.length} 份可恢复` : toggle.checked ? '已开启' : '可选开启';
        list.replaceChildren();
        if (others.length) panel.open = true;
        for (const record of others) {
          const item = document.createElement('li'), text = document.createElement('div'), title = document.createElement('b'), detail = document.createElement('small'), actions = document.createElement('div');
          const fields = new URLSearchParams(record.draft);
          title.textContent = fields.get('title') || '未命名内容';
          detail.textContent = `${window.ChunnelTime?.formatDateTime(record.updated) || ''} · ${fields.has('version') ? '基于 v' + fields.get('version') : '新内容'}${record.pending ? ' · 保存结果待确认' : ''} · ${fields.get('published_timezone') || '旧副本：需核对发布时间时区'}`;
          text.append(title, detail); item.append(text, actions);
          const button = (label, action) => {
            const element = document.createElement('button'); element.type = 'button'; element.className = 'admin-button admin-button-quiet'; element.textContent = label; element.disabled = busy;
            element.addEventListener('click', action); actions.append(element); return element;
          };
          button('恢复这份编辑', async () => {
            if (busy || !options.idle()) return;
            if (options.dirty() && !window.confirm('用这份副本替换当前表单输入？尚未保存的当前输入会被替换。')) return;
            busy = true;
            const before = options.signature(); render();
            try {
              if (!await options.check()) { say('尚未确认原编辑账号，请完成登录检查后重试。', true); return; }
              if (!options.idle() || options.signature() !== before || store.read(record.id).raw !== record.raw) { say('检查期间输入或副本已变化，请重新选择。', true); return; }
              options.restore(record);
              restored = record;
              paused = false;
              // Copy to this tab before removing the exact snapshot selected by the user.
              const saved = toggle.checked && flush();
              if (saved) { store.remove(record.id, record.raw); restored = null; }
              if (!toggle.checked || saved) say(record.pending ? '编辑已载入，请点击「重试原保存」确认之前的操作；后续输入已保留。' : '编辑已载入，尚未正式保存；原版本号已保留。');
            } catch (error) { failed(error); }
            finally { busy = false; render(); options.settled(); }
          });
          button('删除副本', () => {
            if (busy || !window.confirm(record.pending ? '原保存可能已完成，请先核对正式版本。删除这份本机副本及其重试信息？' : '删除这份本机副本？正式内容不会改变。')) return;
            try { store.remove(record.id, record.raw); render(); say('所选副本已删除。'); } catch (error) { failed(error); }
          });
          list.append(item);
        }
        options.changed();
      } catch (error) { failed(error); }
    }
    function flush() {
      window.clearTimeout(timer);
      if (!toggle.checked || paused || !options.persistable()) return false;
      try {
        if (!store.enabled()) { toggle.checked = false; paused = true; say('其他标签页已关闭本机副本。'); return false; }
        if (!options.dirty() && !options.pending()) { store.remove(id); return true; }
        const next = newID();
        store.write(next, options.snapshot(), id); id = next;
        say('本机副本已更新于 ' + (window.ChunnelTime?.formatClock(new Date()) || '') + '；正式内容尚未改变。');
        return true;
      } catch (error) { failed(error); return false; }
    }
    const schedule = () => { window.clearTimeout(timer); if (toggle.checked && !paused) timer = window.setTimeout(flush, 1200); };
    function disable() {
      try { store.clear(); toggle.checked = false; paused = true; window.clearTimeout(timer); render(); say('已关闭并清除当前账号的全部本机副本，当前页面输入保留。'); }
      catch (error) { failed(error); }
    }
    toggle.addEventListener('change', () => {
      if (!toggle.checked) {
        if (!window.confirm('关闭并清除当前账号在此浏览器中的全部编辑副本？')) { toggle.checked = true; return; }
        disable(); return;
      }
      try { store.enable(true); paused = false; const saved = flush(); render(); if (saved) say('已开启本机副本；停止输入后保存，刷新返回时可手动恢复。'); } catch (error) { toggle.checked = false; failed(error); }
    });
    clear.addEventListener('click', () => { if (window.confirm('清除当前账号在此浏览器中的所有副本及重试信息，并关闭本机恢复？请先核对待确认的正式保存。')) disable(); });
    window.addEventListener('storage', event => {
      if (event.key && !event.key.startsWith(ROOT)) return;
      try {
        if (!store.enabled()) { store.clear(); toggle.checked = false; paused = true; window.clearTimeout(timer); say('本机副本已在其他标签页关闭或清除。'); }
        render();
      } catch (error) { failed(error); }
    });
    document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'hidden') flush(); });
    try { toggle.checked = store.enabled(); render(); if (toggle.checked) { say('本机恢复已开启；修改后更新副本。'); schedule(); } }
    catch (error) { toggle.disabled = true; failed(error); }
    return {schedule, flush, blocked, busy: () => busy,
      confirmed() { window.clearTimeout(timer); try { store.remove(id); if (restored) store.remove(restored.id, restored.raw); restored = null; render(); } catch (error) { failed(error); } },
      warn() { panel.open = true; say('有一份保存结果待确认的副本。请先恢复并重试原保存，或核对正式版本后删除副本。', true); panel.scrollIntoView({block: 'nearest'}); }
    };
  }
  window.ChunnelEditorRecovery = {createStore, mount};
})();
