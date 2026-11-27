const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(require('node:path').join(__dirname, '../../internal/coreui/assets/js/chunnel-plugins.js'), 'utf8');
const tick = () => new Promise(resolve => setImmediate(resolve));

function fixture({configured = true, readOnly = false, entries} = {}) {
  const calls = [], timers = new Map(), events = {}, documentEvents = {}, pages = new Map();
  let reloads = 0, nextTimer = 0;
  const camel = text => text.replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
  const matches = (node, selector) => selector.split(',').some(part => {
    part = part.trim();
    if (part === 'button:not(:disabled)') return node.tag === 'button' && !node.disabled;
    if (part === 'button' || part === 'a') return node.tag === part;
    if (part === '.admin-plugin-source') return node.dataset.pluginSource !== undefined;
    if (part === 'button[value="cancel"]') return node.cancel;
    const match = /^\[data-([\w-]+)(?:="([^"]*)")?\]$/.exec(part);
    return !!match && Object.hasOwn(node.dataset, camel(match[1])) && (match[2] === undefined || node.dataset[camel(match[1])] === match[2]);
  });
  const node = (tag = 'div', dataset = {}) => ({tag, dataset, events: {}, attributes: {}, childNodes: [], disabled: false, hidden: false, open: false, textContent: '',
    append(...nodes) { nodes.forEach(n => { n.parent = this; this.childNodes.push(n); }); },
    replaceChildren(...nodes) { this.childNodes = []; this.append(...nodes); },
    querySelectorAll(selector) { return this.childNodes.flatMap(n => [...(matches(n, selector) ? [n] : []), ...n.querySelectorAll(selector)]); },
    querySelector(selector) { return this.querySelectorAll(selector)[0] || null; },
    addEventListener(type, fn) { this.events[type] = fn; },
    setAttribute(name, value) { this.attributes[name] = value; }, removeAttribute(name) { delete this.attributes[name]; }, hasAttribute(name) { return Object.hasOwn(this.attributes, name); },
    closest(selector) { return matches(this, selector) ? this : this.parent?.closest(selector); },
    focus() { document.activeElement = this; }, scrollIntoView() {},
    showModal() { this.open = true; }, close() { this.open = false; this.events.close?.(); }
  });
  const makePage = (name, {userId = '7', readOnly: ro = false, error = '', action = 'install', entries = [{slug: name}]} = {}) => {
    const root = node('div', {pluginWorkspace: '', userId, csrf: 'csrf-'+name, marketConfigured: String(configured), readonly: String(ro)});
    const market = node('div', {pluginMarketContent: '', marketError: error});
    const categories = node('div', {pluginCategories: ''});
    for (const category of ['all', ...new Set(entries.map(entry => entry.category || 'other'))]) {
      const button = node('button', {pluginCategory: category}); button.append(node('span', {categoryCount: ''})); categories.append(button);
    }
    market.append(categories);
    const cards = entries.map(entry => {
      const card = node('article', {pluginCard: entry.slug, category: entry.category || 'other', search: entry.search || entry.slug,
        installed: String(!!entry.installed), enabled: String(!!entry.enabled), update: String(!!entry.update)});
      const button = node('button', {pluginAction: action, slug: entry.slug, digest: entry.slug+'-digest'}); button.disabled = ro;
      const remove = node('button', {pluginRemove: entry.slug, name: entry.slug}); remove.disabled = ro;
      const drawer = node('dialog', {pluginDetails: ''}), open = node('button', {pluginDetailsOpen: ''}), close = node('button', {pluginDetailsClose: ''});
      open.isConnected = true; drawer.append(close);
      card.append(button, remove, open, drawer); market.append(card);
      return {card, button, remove};
    });
    const result = node('p', {pluginResultCount: ''}), empty = node('div', {pluginEmpty: ''}), clear = node('button', {pluginClear: ''});
    empty.append(clear); market.append(result, empty, node('details', {pluginSource: ''}));
    const refresh = node('button', {pluginAction: 'refresh'}); refresh.disabled = ro;
    root.append(market, refresh);
    pages.set(name, root);
    return {root, market, ...cards[0], cards, categories, result, empty, clear, refresh};
  };
  const initial = makePage('cached', {readOnly, entries});
  const root = initial.root, market = initial.market;
  const search = node('input', {pluginSearch: ''}), statusFilter = node('select', {pluginStatusFilter: ''});
  search.value = ''; statusFilter.value = 'all';
  const filterTools = node('div', {pluginTools: ''}); filterTools.append(search, statusFilter); root.append(filterTools);
  const notice = node('p', {pluginStatus: ''}); notice.hidden = true;
  const loading = node('div', {pluginMarketLoading: ''}); loading.hidden = true;
  loading.append(node('strong', {pluginLoadingTitle: ''}), node('p', {pluginLoadingDetail: ''})); root.append(loading);
  const dialog = node('dialog', {pluginUninstall: ''}), cancel = node('button'); cancel.cancel = true;
  const confirm = node('button', {uninstallConfirm: ''}), uninstallName = node('span', {uninstallName: ''}), uninstallError = node('p', {uninstallError: ''});
  dialog.append(confirm, cancel, uninstallName, uninstallError); root.append(notice, dialog);
  const document = {hidden: false, activeElement: null, querySelector: selector => matches(root, selector) ? root : root.querySelector(selector), addEventListener(type, fn) {documentEvents[type] = fn;}};
  vm.runInNewContext(source, {document, window: {addEventListener(type, fn) {events[type] = fn;}}, location: {reload() {reloads++;}}, AbortController,
    DOMParser: class { parseFromString(html) {return {querySelector: () => pages.get(html) || null};} },
    setTimeout(fn, ms) {const id = ++nextTimer; timers.set(id, {fn, ms}); return id;}, clearTimeout(id) {timers.delete(id);},
    fetch(path, options) {return new Promise((resolve,reject) => calls.push({path, options, resolve, reject}));}
  });
  const respond = (call, value, status = 200) => call.resolve({ok: status < 400, status, json: async () => value, text: async () => value});
  return {root, market, notice, loading, dialog, document, events, documentEvents, calls, timers, initial, confirm, makePage, respond, search, statusFilter, reloads: () => reloads,
    fire(node, type = 'click') {return node.events[type]?.({currentTarget: node, preventDefault() {}});},
    posts() {return calls.filter(call => call.options.method === 'POST');}
  };
}

test('entering the market checks its source and renders fresh cards without navigation', async () => {
  const f = fixture(); assert.equal(f.calls[0].path, '/admin/plugins/?market=1');
  assert.equal(f.loading.hidden, false); assert.equal(f.market.attributes['aria-busy'], 'true');
  assert.equal(f.root.dataset.marketLoading, 'true');
  assert.match(f.loading.querySelector('[data-plugin-loading-title]').textContent, /加载插件市场/);
  assert.equal(f.calls[0].options.cache, 'no-store');
  const fresh = f.makePage('fresh'); f.respond(f.calls[0], 'fresh'); await tick();
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'fresh');
  assert.equal(f.root.dataset.csrf, 'csrf-fresh'); assert.equal(f.reloads(), 0);
  assert.equal(f.loading.hidden, true); assert.equal(f.market.attributes['aria-busy'], undefined);
  f.fire(fresh.button); assert.equal(f.posts().length, 1);
  assert.equal(f.posts()[0].options.headers['X-CSRF-Token'], 'csrf-fresh');
  f.respond(f.posts()[0], {ok: true}); await tick();
  const installed = f.makePage('installed', {action: 'enable'});
  assert.equal(f.calls.at(-1).path, '/admin/plugins/');
  f.respond(f.calls.at(-1), 'installed'); await tick();
  assert.equal(f.market.querySelector('[data-plugin-action]').dataset.pluginAction, 'enable');
  assert.equal(typeof installed.remove.events.click, 'function'); assert.equal(f.reloads(), 0);
  assert.equal(f.initial.refresh.disabled, false);
});

const discoveryEntries = [
  {slug: 'comment-origin', category: 'comments', search: '评论来源 IP 设备 NewPress', installed: true, enabled: true},
  {slug: 'content-age', category: 'content', search: '文章时效提醒 写作 NewPress', installed: true, update: true},
  {slug: 'seo-helper', category: 'seo', search: '搜索优化 SEO 站点地图', installed: false}
];

test('category, search and status filters intersect and update counts without requests', () => {
  const f = fixture({entries: discoveryEntries});
  const category = value => f.market.querySelector(`[data-plugin-category="${value}"]`);
  const visible = () => f.market.querySelectorAll('[data-plugin-card]').filter(card => !card.hidden).map(card => card.dataset.pluginCard);
  f.fire(category('comments')); assert.deepEqual(visible(), ['comment-origin']);
  f.statusFilter.value = 'available'; f.fire(f.statusFilter, 'change');
  assert.deepEqual(visible(), []); assert.equal(f.initial.empty.hidden, false);
  assert.equal(category('comments').querySelector('[data-category-count]').textContent, 0);
  f.fire(f.initial.clear); assert.deepEqual(visible(), discoveryEntries.map(entry => entry.slug));
  f.search.value = 'ＳＥＯ   地图'; f.fire(f.search, 'input');
  assert.deepEqual(visible(), ['seo-helper']);
  assert.equal(f.initial.result.textContent, '1 / 3 个插件');
  f.fire(f.initial.clear); f.statusFilter.value = 'updates'; f.fire(f.statusFilter, 'change');
  assert.deepEqual(visible(), ['content-age']);
  f.statusFilter.value = 'enabled'; f.fire(f.statusFilter, 'change');
  assert.deepEqual(visible(), ['comment-origin']);
  assert.equal(f.calls.length, 1); assert.equal(f.posts().length, 0);
});

test('automatic refresh preserves active filters, focus and expanded plugin details', async () => {
  const f = fixture({entries: discoveryEntries});
  f.fire(f.market.querySelector('[data-plugin-category="content"]'));
  f.search.value = '写作'; f.fire(f.search, 'input');
  f.statusFilter.value = 'installed'; f.fire(f.statusFilter, 'change');
  f.market.querySelector('[data-plugin-card="content-age"]').querySelector('[data-plugin-details]').open = true;
  f.search.focus();
  const fresh = f.makePage('fresh', {entries: discoveryEntries});
  f.respond(f.calls[0], 'fresh'); await tick();
  assert.equal(f.search.value, '写作'); assert.equal(f.document.activeElement, f.search);
  assert.equal(f.market.querySelector('[data-plugin-category="content"]').attributes['aria-pressed'], 'true');
  assert.deepEqual(fresh.cards.filter(item => !item.card.hidden).map(item => item.card.dataset.pluginCard), ['content-age']);
  assert.equal(fresh.cards[1].card.querySelector('[data-plugin-details]').open, true);
});

test('a removed category falls back to all while retaining search and status', async () => {
  const f = fixture({entries: discoveryEntries});
  f.fire(f.market.querySelector('[data-plugin-category="content"]'));
  f.search.value = 'NewPress'; f.fire(f.search, 'input');
  f.statusFilter.value = 'installed'; f.fire(f.statusFilter, 'change');
  const fresh = f.makePage('fresh', {entries: [discoveryEntries[0], discoveryEntries[2]]});
  f.respond(f.calls[0], 'fresh'); await tick();
  assert.equal(f.market.querySelector('[data-plugin-category="all"]').attributes['aria-pressed'], 'true');
  assert.equal(fresh.cards[0].card.hidden, false); assert.equal(fresh.cards[1].card.hidden, true);
  f.search.value = '<script>'; f.fire(f.search, 'input');
  assert.equal(fresh.empty.hidden, false);
  f.fire(fresh.clear); assert.equal(f.search.value, ''); assert.equal(fresh.empty.hidden, true);
});

test('a pre-operation market response cannot overwrite completed installation state', async () => {
  const f = fixture(), stale = f.calls[0];
  f.fire(f.initial.button); assert.equal(stale.options.signal.aborted, true);
  f.respond(f.posts()[0], {ok: true}); await tick();
  f.makePage('installed', {action: 'enable'}); f.respond(f.calls.at(-1), 'installed'); await tick();
  f.makePage('outdated'); f.respond(stale, 'outdated'); await tick();
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'installed');
  assert.equal(f.reloads(), 0);
});

test('manual refresh bypasses the automatic check and replaces source diagnostics', async () => {
  const f = fixture(); f.makePage('initial'); f.respond(f.calls[0], 'initial'); await tick();
  f.fire(f.initial.refresh); f.fire(f.initial.refresh); assert.equal(f.posts().length, 1);
  assert.equal(f.loading.hidden, false);
  assert.equal(JSON.parse(f.posts()[0].options.body).action, 'refresh');
  f.respond(f.posts()[0], {ok: true}); await tick();
  assert.equal(f.loading.hidden, false);
  f.makePage('new-release'); f.respond(f.calls.at(-1), 'new-release'); await tick();
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'new-release');
  assert.equal(f.reloads(), 0);
  assert.equal(f.loading.hidden, true);
});

test('failed automatic checks retain the visible list and can recover when refocused', async () => {
  const f = fixture(); f.calls[0].reject(new Error('offline')); await tick();
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'cached');
  assert.equal(f.notice.dataset.tone, 'error');
  assert.equal(f.loading.hidden, true);
  f.events.focus(); f.makePage('recovered'); f.respond(f.calls.at(-1), 'recovered'); await tick();
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'recovered'); assert.equal(f.notice.hidden, true);
});

test('failed manual refresh loads the source diagnostics without probing again', async () => {
  const f = fixture(); f.makePage('initial'); f.respond(f.calls[0], 'initial'); await tick();
  f.fire(f.initial.refresh);
  const message = 'GitHub：连接超时；Gitee：目录签名校验失败';
  f.respond(f.posts()[0], {error: {message}}, 422); await tick();
  assert.equal(f.calls.at(-1).path, '/admin/plugins/');
  f.makePage('diagnostics', {error: message}); f.respond(f.calls.at(-1), 'diagnostics'); await tick();
  assert.equal(f.market.dataset.marketError, message);
  assert.equal(f.notice.textContent, message); assert.equal(f.notice.dataset.tone, 'error');
  assert.equal(f.initial.refresh.disabled, false); assert.equal(f.posts().length, 1);
  assert.equal(f.market.querySelector('.admin-plugin-source').open, true);
  assert.equal(f.loading.hidden, true);
  assert.equal(f.calls.filter(call => call.path.includes('?market=1')).length, 1);
});

test('failed diagnostic reload preserves the original manual refresh error', async () => {
  const f = fixture(); f.makePage('initial'); f.respond(f.calls[0], 'initial'); await tick();
  f.fire(f.initial.refresh);
  f.respond(f.posts()[0], {error: {message: 'GitHub 和 Gitee 均不可用'}}, 422); await tick();
  assert.equal(f.calls.at(-1).path, '/admin/plugins/');
  f.calls.at(-1).reject(new Error('snapshot offline')); await tick();
  assert.equal(f.notice.textContent, 'GitHub 和 Gitee 均不可用');
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'initial');
});

test('denied manual refresh does not reload diagnostics or re-enable controls', async () => {
  const f = fixture(); f.makePage('initial'); f.respond(f.calls[0], 'initial'); await tick();
  f.fire(f.initial.refresh);
  f.respond(f.posts()[0], {error: {message: '权限已变化'}}, 403); await tick();
  assert.equal(f.calls.length, 2); assert.equal(f.initial.refresh.disabled, true);
  assert.equal(f.notice.textContent, '权限已变化');
});

test('a browser timeout reports that source detection did not finish', async () => {
  const f = fixture(), pending = f.calls[0];
  [...f.timers.values()].find(timer => timer.ms === 12000).fn();
  assert.equal(pending.options.signal.aborted, true);
  pending.reject(Object.assign(new Error('aborted'), {name: 'AbortError'})); await tick();
  assert.equal(f.notice.hidden, false); assert.match(f.notice.textContent, /超时/);
  assert.equal(f.loading.hidden, true);
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'cached');
});

test('identity change halts updates and locks stale mutation controls', async () => {
  const f = fixture(); f.makePage('other', {userId: '8'}); f.respond(f.calls[0], 'other'); await tick();
  assert.equal(f.initial.button.disabled, true); assert.equal(f.confirm.disabled, true);
  assert.match(f.notice.textContent, /账号已变化/);
  f.fire(f.initial.button); f.events.focus(); assert.equal(f.calls.length, 1);
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'cached');
});

test('read-only viewers update cards without enabling writes or posting a refresh', async () => {
  const f = fixture({readOnly: true}); const fresh = f.makePage('fresh', {readOnly: true});
  f.respond(f.calls[0], 'fresh'); await tick(); f.fire(fresh.button); f.fire(f.initial.refresh);
  assert.equal(f.posts().length, 0); assert.equal(fresh.button.disabled, true);
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'fresh');
});

test('background pages stop scheduling checks and discarded requests cannot repaint', async () => {
  const f = fixture(), pending = f.calls[0]; f.events.pagehide();
  assert.equal(pending.options.signal.aborted, true);
  assert.equal(f.loading.hidden, true);
  f.makePage('obsolete'); f.respond(pending, 'obsolete'); await tick();
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'cached');
  assert.equal([...f.timers.values()].filter(t => t.ms === 60000).length, 0);
  f.events.pageshow({persisted: true}); f.makePage('restored'); f.respond(f.calls.at(-1), 'restored'); await tick();
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'restored');
  f.document.hidden = true; f.documentEvents.visibilitychange();
  assert.equal([...f.timers.values()].filter(t => t.ms === 60000).length, 0);
});

test('market refresh does not replace cards during an uninstall confirmation', async () => {
  const f = fixture(); const current = f.makePage('current'); f.respond(f.calls[0], 'current'); await tick();
  f.fire(current.remove); assert.equal(f.dialog.open, true);
  const count = f.calls.length; f.events.focus(); assert.equal(f.calls.length, count);
  f.fire(f.confirm); f.respond(f.posts()[0], {ok: true}); await tick();
  f.makePage('removed'); f.respond(f.calls.at(-1), 'removed'); await tick();
  assert.equal(f.dialog.open, false); assert.equal(f.reloads(), 0);
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'removed');
});

test('opening uninstall confirmation cancels an in-flight automatic snapshot', async () => {
  const f = fixture(), pending = f.calls[0];
  f.fire(f.initial.remove); assert.equal(f.dialog.open, true);
  assert.equal(pending.options.signal.aborted, true);
  f.makePage('late'); f.respond(pending, 'late'); await tick();
  assert.equal(f.market.querySelector('[data-plugin-card]').dataset.pluginCard, 'cached');
  assert.equal(f.dialog.open, true);
});


test('details use a modal and restore focus without refreshing while reading', async () => {
  const f = fixture(); f.respond(f.calls[0], 'cached'); await tick();
  const card = f.market.querySelector('[data-plugin-card]');
  const open = card.querySelector('[data-plugin-details-open]');
  const drawer = card.querySelector('[data-plugin-details]');
  f.fire(open); assert.equal(drawer.open, true);
  const count = f.calls.length; f.events.focus(); assert.equal(f.calls.length, count);
  f.fire(drawer.querySelector('[data-plugin-details-close]'));
  assert.equal(drawer.open, false); assert.equal(f.document.activeElement, open);
});
