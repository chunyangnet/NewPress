const fs = require('node:fs');
const vm = require('node:vm');
const { webcrypto: crypto } = require('node:crypto');
const { compile } = require('./typescript.cjs');
const { Element } = require('./media-picker.cjs');
const cache = new Map();

function environment() {
  const document = new Element(), window = new Element(), requests = [], timers = new Map(); let timerID = 0;
  document.head = new Element('head'); document.body = new Element('body'); document.createElement = tag => new Element(tag); document.createDocumentFragment = () => new Element('fragment');
  document.getElementById = id => document.querySelector('#' + id);
  Element.prototype.classList = { add() {}, remove() {}, contains() { return false; } };
  Element.prototype.remove = function () { this.isConnected = false; };
  const globals = { document, window, Element, HTMLInputElement: Element, HTMLTextAreaElement: Element, HTMLSelectElement: Element,
    crypto, URL, AbortController, DOMException, Error, Event, queueMicrotask, location: { origin: 'https://newpress.test', href: 'https://newpress.test/admin/content/1/edit/' },
    setTimeout(callback, delay) { const id = ++timerID; timers.set(id, { callback, delay }); return id; }, clearTimeout: id => timers.delete(id),
    fetch(url, init = {}) { return new Promise((resolve, reject) => requests.push({ url: String(url), init, reject,
      reply(payload, status = 200) { resolve({ ok: status >= 200 && status < 300, status, json: async () => payload }); },
      body(promise) { resolve({ ok: true, status: 200, json: () => promise }); } })); },
  };
  return { document, window, globals, requests, timers,
    load(name, variant = 'TypeScript') {
      const key = name + '-' + variant;
      if (!cache.has(key)) cache.set(key, variant === 'TypeScript' ? compile('frontend/core/' + name + '.ts')
        : fs.readFileSync('internal/coreui/assets/js/' + (name.startsWith('resource-') ? 'newpress-' : 'chunnel-') + name + '.js', 'utf8'));
      vm.runInNewContext(cache.get(key), globals);
    },
    async settle() { for (let i = 0; i < 18; i++) await Promise.resolve(); },
    async reply(index, data, status) { requests[index].reply(data, status); await this.settle(); },
    flush() { const pending = [...timers.values()]; timers.clear(); pending.forEach(item => item.callback()); },
  };
}

function dialogFixture(name, variant) {
  const f = environment(), kind = name === 'editor-reuse' ? 'reuse' : 'widget';
  const form = new Element('form'), dialog = new Element('dialog'), source = new Element('textarea'), launcher = new Element('button');
  source.value = 'draft'; source.selectionStart = source.selectionEnd = 5;
  f.document.selectors.set('[data-content-editor]', form); f.document.selectors.set(`[data-editor-${kind}-dialog]`, dialog);
  form.selectors.set('[data-content-source]', source); form.selectors.set(`[data-editor-${kind}-open]`, launcher); form.groups.set(`[data-editor-${kind}-open]`, [launcher]);
  const nodes = {}; for (const key of ['search', 'results', 'status', 'page', 'previous', 'next', 'preview', 'insert', 'close']) {
    const node = new Element(key === 'search' ? 'form' : ['previous', 'next', 'insert', 'close'].includes(key) ? 'button' : 'div');
    nodes[key] = node; dialog.selectors.set(`[data-editor-${kind}-${key}]`, node);
  }
  nodes.query = new Element('input'); nodes.filter = new Element('select'); nodes.filter.value = 'draft'; nodes.submit = new Element('button');
  nodes.search.selectors.set('[name="q"]', nodes.query); nodes.search.selectors.set('[name="status"]', nodes.filter); nodes.search.selectors.set('button', nodes.submit); nodes.search.selectors.set('button[type="submit"]', nodes.submit);
  const layouts = ['card', 'compact'].map(value => Object.assign(new Element('input'), { value, checked: value === 'card' })); dialog.groups.set('[name="article_widget_layout"]', layouts);
  const insertions = [], renderings = [];
  f.window.ChunnelBlockEditor = { capture: () => ({ value: source.value, start: source.selectionStart, end: source.selectionEnd }),
    insert: (markup, selection) => { insertions.push({ markup, selection }); }, localPostURL: url => /^\/posts\/[a-z-]+$/.test(url),
    renderBlock: block => { renderings.push(block); return '<aside data-np-post="' + block.postID + '"></aside>'; } };
  f.load(name, variant);
  return { ...f, form, dialog, source, launcher, nodes, layouts, insertions, renderings,
    open() { launcher.click(); }, close() { dialog.close(); }, hide() { f.window.emit('pagehide'); },
    insert() { (kind === 'reuse' ? nodes.results.children[0].children[1] : nodes.insert).click(); },
    select() { nodes.results.children[0].click(); },
  };
}

const originalConfig = () => ({ items: [{ id: 'bundle', title: '已有隐藏内容', variants: [{ id: 'full', name: '标准版', kind: 'content', body: '私密正文', downloads: null, questions: null, fields: null,
  groups: [{ name: '支付', conditions: [{ id: 'pay', kind: 'payment', amount: 990, currency: 'CNY' }] }] }] }] });

function resourceFixture(variant, config = originalConfig()) {
  const f = environment(), root = new Element(), form = new Element('form'), input = new Element('input'), workspace = new Element(), message = new Element(), fieldset = new Element('fieldset');
  root.dataset.post = '1'; root.parents = new Map([['form', form]]); input.value = JSON.stringify(config);
  for (const [selector, value] of [['[data-resource-config]', input], ['[data-resource-workspace]', workspace], ['[data-resource-editor-status]', message], ['fieldset', fieldset]]) root.selectors.set(selector, value);
  f.document.selectors.set('[data-resource-editor]', root); f.load('resource-editor', variant);
  const control = (dataset, value = '', type = 'text') => { const node = new Element('input'); Object.assign(node, { dataset, value, type }); node.closest = () => node; return node; };
  return { ...f, root, form, input, workspace, message, fieldset,
    options(data = { currencies: [], roles: [], plans: [] }) { return f.reply(0, data); },
    update(path, value, { type = 'text', event = 'input', ...extra } = {}) { const node = control({ path, ...extra }, value, type); node.checked = value === true; workspace.emit(event, { target: node }); return node; },
    action(action, pathAction = '', index) { const node = control({ action, pathAction, index }); const event = workspace.emit('click', { target: node }); return { node, completion: event.completion }; },
    config() { return JSON.parse(input.value); },
  };
}

module.exports = { environment, dialogFixture, resourceFixture, originalConfig, Element };
