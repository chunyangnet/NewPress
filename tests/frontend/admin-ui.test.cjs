'use strict';
const nodeTest = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');
for (const entry of ['TypeScript', 'generated']) {
const test = (name, fn) => nodeTest(entry + ': ' + name, fn);
const source = entry === 'TypeScript' ? compile('frontend/core/admin-ui.ts') : fs.readFileSync('internal/coreui/assets/js/chunnel-admin-ui.js', 'utf8');

class Element {
  constructor() {
    this.listeners = new Map(); this.attributes = new Map(); this.dataset = {}; this.children = [];
    this.value = ''; this.type = 'text'; this.disabled = false; this.hidden = false; this.textContent = '';
    const classes = new Set(); this.classList = {toggle(name, enabled) { enabled ? classes.add(name) : classes.delete(name); }, contains: name => classes.has(name)};
    this.selectors = new Map(); this.groups = new Map();
  }
  addEventListener(name, fn) { this.listeners.set(name, [...(this.listeners.get(name) || []), fn]); }
  removeEventListener(name, fn) { this.listeners.set(name, (this.listeners.get(name) || []).filter(item => item !== fn)); }
  dispatchEvent(event) { event.target ||= this; for (const fn of this.listeners.get(event.type) || []) fn(event); return !event.defaultPrevented; }
  emit(type, values = {}) { const event = {type, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, ...values}; this.dispatchEvent(event); return event; }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  hasAttribute(name) { return this.attributes.has(name); }
  removeAttribute(name) { this.attributes.delete(name); }
  querySelector(selector) { return this.selectors.get(selector) || null; }
  querySelectorAll(selector) { return this.groups.get(selector) || []; }
  closest() { return null; }
  append(...children) { this.children.push(...children); }
}
function fixture({readOnly = false, native = false, initialNotice = '', initialTone = '', attachSavebar = true} = {}) {
  const form = new Element(); form.id = 'settings'; form.setAttribute('id', 'settings');
  const bar = new Element(), save = new Element(), discard = new Element(), output = new Element();
  save.textContent = '保存更改'; bar.dataset.readonly = String(readOnly);
  output.dataset.initialMessage = initialNotice; output.dataset.tone = initialTone;
  bar.selectors.set('[data-admin-save]', save); bar.selectors.set('[data-admin-discard]', discard); bar.selectors.set('[data-save-state]', output);
  const field = (value, type = 'text', attributes = []) => { const input = new Element(); input.value = value; input.type = type; attributes.forEach(attr => input.setAttribute(attr, '')); return input; };
  const title = field('原值'), secret = field('', 'password'), toggle = field('true', 'checkbox'); toggle.checked = false;
  const hidden = field('csrf', 'hidden'), payload = field('[]', 'hidden', ['data-admin-value']), transient = field('preview', 'text', ['data-admin-transient']);
  const multi = field('one', 'select-multiple'); multi.multiple = true; multi.options = [{selected: true}, {selected: false}];
  const fields = [title, secret, toggle, hidden, payload, transient, multi]; form.groups.set('input,select,textarea', fields);
  const document = new Element(), window = new Element(), timers = new Map(); let nextTimer = 0;
  window.scrollTo = ({top}) => { window.scrollY = top; };
  document.selectors.set('[data-admin-savebar][data-form-id="settings"]', bar);
  document.createElement = () => new Element();
  const context = {document, window, Element, HTMLInputElement: Element, HTMLFormElement: Element, location: {host: 'example.test', hash: ''}, history: {replaceState() {}}, localStorage: {getItem: () => '{}'},
    Event: class { constructor(type, values) {this.type = type; Object.assign(this, values);} },
    CustomEvent: class { constructor(type, values) {this.type = type; Object.assign(this, values);} },
    queueMicrotask, setTimeout(fn) { const id = ++nextTimer; timers.set(id, fn); return id; }, clearTimeout(id) { timers.delete(id); }};
  vm.runInNewContext(source, context);
  const api = attachSavebar ? window.ChunnelAdminUI.savebar(form, {native}) : null;
  return {api, form, bar, save, discard, output, title, secret, toggle, hidden, payload, transient, multi, fields, window, document, ui: window.ChunnelAdminUI, context,
    flushTimers() { const tasks = [...timers.values()]; timers.clear(); tasks.forEach(fn => fn()); }};
}

test('save dock remains quiet until real changes, and reverting clears dirty state', () => {
  const f = fixture(); assert.equal(f.save.disabled, true); assert.equal(f.output.textContent, ''); assert.equal(f.discard.hidden, true);
  f.hidden.value = 'rotated-csrf'; f.transient.value = 'new preview'; f.form.emit('input'); assert.equal(f.api.dirty(), false);
  f.toggle.checked = true; f.form.emit('change'); assert.equal(f.save.disabled, false); assert.equal(f.output.textContent, '未保存');
  f.toggle.checked = false; f.form.emit('change'); assert.equal(f.save.disabled, true); assert.equal(f.discard.hidden, true);
  f.payload.value = '[4,1]'; f.form.emit('input'); assert.equal(f.api.dirty(), true);
});

test('discard restores typed values, switches and multiple selections, retaining fresh CSRF', () => {
  const f = fixture(); f.title.value = '新标题'; f.toggle.checked = true; f.multi.options[1].selected = true; f.hidden.value = 'new-token';
  f.form.emit('input'); f.discard.emit('click');
  assert.equal(f.title.value, '原值'); assert.equal(f.toggle.checked, false); assert.equal(f.multi.options[1].selected, false);
  assert.equal(f.hidden.value, 'new-token'); assert.equal(f.api.dirty(), false);
});

test('failed asynchronous save retains secrets, edits and retry availability', () => {
  const f = fixture(); f.secret.value = 'unsaved-secret'; f.form.emit('input'); assert.equal(f.api.start(), true);
  assert.equal(f.api.start(), false); assert.equal(f.save.disabled, true);
  assert.equal(f.window.emit('beforeunload').defaultPrevented, true);
  f.api.fail('版本冲突'); assert.equal(f.secret.value, 'unsaved-secret'); assert.equal(f.save.disabled, false); assert.equal(f.output.dataset.tone, 'error');
  assert.equal(f.output.textContent, '版本冲突'); assert.equal(f.api.dirty(), true);
});

test('save acknowledgement does not mark edits made during a request as saved', () => {
  const f = fixture(); f.title.value = '已提交'; f.form.emit('input'); f.api.start();
  f.title.value = '后续输入'; f.form.emit('input'); f.api.success();
  assert.equal(f.title.value, '后续输入'); assert.equal(f.api.dirty(), true); assert.equal(f.output.textContent, '未保存');
  f.api.discard(); assert.equal(f.title.value, '已提交'); assert.equal(f.api.dirty(), false);
});

test('successful save confirms briefly and allows secret-cleared server baseline', () => {
  const f = fixture(); f.secret.value = 'secret'; f.form.emit('input'); f.api.start(); f.secret.value = '';
  f.api.success(f.api.snapshot()); assert.equal(f.api.dirty(), false); assert.equal(f.output.textContent, '已保存');
  f.flushTimers(); assert.equal(f.output.textContent, ''); assert.equal(f.window.emit('beforeunload').defaultPrevented, false);
});

test('native submission keeps the submitter enabled for serialization and recovers from bfcache', async () => {
  const f = fixture({native: true}); f.title.value = '更改'; f.form.emit('input'); f.form.emit('submit'); await Promise.resolve();
  assert.equal(f.save.disabled, false, 'do not omit a named action before browser serialization');
  assert.equal(f.window.emit('beforeunload').defaultPrevented, false); f.flushTimers(); assert.equal(f.save.disabled, true);
  f.window.emit('pageshow', {persisted: true}); assert.equal(f.save.disabled, false); assert.equal(f.api.dirty(), true);
  assert.equal(f.window.emit('beforeunload').defaultPrevented, true);
});

test('cancelled native submission and readonly forms never enter saving state', async () => {
  const f = fixture({native: true}); f.form.addEventListener('submit', event => event.preventDefault()); f.title.value = '更改'; f.form.emit('input'); f.form.emit('submit'); await Promise.resolve();
  assert.equal(f.save.disabled, false); assert.equal(f.window.emit('beforeunload').defaultPrevented, true);
  const read = fixture({readOnly: true}); read.title.value = '更改'; read.form.emit('input'); assert.equal(read.api.start(), false);
  read.api.discard(); assert.equal(read.title.value, '更改'); assert.equal(read.output.textContent, '只读');
});

test('server feedback retains its tone and switch factory keeps native keyboard semantics', () => {
  const f = fixture({initialNotice: '保存失败', initialTone: 'error'}); assert.equal(f.output.dataset.tone, 'error'); assert.equal(f.output.textContent, '保存失败');
  const input = new Element(), label = f.ui.switchControl(input, '开放页面');
  assert.equal(input.type, 'checkbox'); assert.equal(input.getAttribute('role'), 'switch'); assert.equal(label.children[0], input);
  assert.equal(label.children[2].children[0].textContent, '开放页面');
});

test('shared grouping respects hashes, reveals invalid fields and preserves scroll on clicks', () => {
  const f = fixture(); const sections = ['general', 'permissions'].map(id => { const section = Object.assign(new Element(), {id}); section.setAttribute('id', id); return section; });
  const links = sections.map(section => { const e = new Element(); e.dataset.settingsTarget = section.id; e.closest = () => e; return e; });
  f.document.groups.set('[data-settings-section]', sections); f.document.groups.set('[data-settings-target]', links);
  f.context.location.hash = '#permissions'; let kept = 0; f.window.ChunnelScroll = {keep(fn) { kept++; fn(); }};
  f.ui.settingsNavigation(f.document); assert.equal(sections[0].hidden, true); assert.equal(links[1].getAttribute('aria-current'), 'true');
  f.document.emit('click', {target: links[0]}); assert.equal(kept, 1); assert.equal(sections[0].hidden, false);
  f.document.emit('invalid', {target: Object.assign(new Element(), {closest: () => sections[1]})}); assert.equal(sections[1].hidden, false);
});

test('named id controls do not break navigation sections or their save dock', () => {
  const f = fixture({attachSavebar: false});
  const properties = new Element(); properties.setAttribute('id', 'properties');
  f.form.id = new Element(); f.form.id.value = 'menu-1';
  const sections = [properties, f.form];
  const links = sections.map(section => { const link = new Element(); link.dataset.settingsTarget = section.getAttribute('id'); link.closest = () => link; return link; });
  f.document.groups.set('[data-settings-section]', sections); f.document.groups.set('[data-settings-target]', links);
  f.context.location.hash = '#settings';
  f.ui.settingsNavigation(f.document);
  assert.equal(properties.hidden, true); assert.equal(f.form.hidden, false);
  f.document.emit('click', {target: links[0]});
  assert.equal(properties.hidden, false); assert.equal(f.form.hidden, true);
  f.document.emit('invalid', {target: Object.assign(new Element(), {closest: () => f.form})});
  assert.equal(properties.hidden, true); assert.equal(f.form.hidden, false);
  const api = f.ui.savebar(f.form);
  assert.ok(api); f.title.value = 'updated'; f.form.emit('input');
  assert.equal(f.save.disabled, false); assert.equal(api.dirty(), true);
});

function navigationFixture({scrollHelper = true} = {}) {
  const f = fixture();
  const sections = ['long', 'short', 'medium'].map(id => { const node = new Element(); node.setAttribute('id', id); return node; });
  const links = sections.map(section => { const link = new Element(); link.dataset.settingsTarget = section.getAttribute('id'); link.closest = () => link; return link; });
  f.document.groups.set('[data-settings-section]', sections); f.document.groups.set('[data-settings-target]', links);
  const heights = [2400, 400, 1400], surface = new Element();
  surface.style = {minHeight: ''}; surface.getBoundingClientRect = () => ({top: -f.window.scrollY});
  f.document.selectors.set('.admin-main', surface);
  f.document.documentElement = {style: {overflowAnchor: '', scrollBehavior: ''}, get scrollHeight() {
    return Math.max(f.window.innerHeight, ...sections.map((section, i) => section.hidden ? 0 : heights[i]), parseFloat(surface.style.minHeight) || 0);
  }};
  f.window.innerHeight = 800; f.window.scrollY = 0; f.window.scrollX = 0;
  const scrolls = [];
  f.window.scrollTo = position => {
    scrolls.push(position);
    f.window.scrollY = Math.min(Math.max(0, f.document.documentElement.scrollHeight - f.window.innerHeight), Math.max(0, position.top));
  };
  if (scrollHelper) vm.runInNewContext(entry === 'TypeScript' ? compile('frontend/shared/scroll.ts') : fs.readFileSync('internal/coreui/assets/js/chunnel-scroll.js', 'utf8'), f.context);
  const navigation = f.ui.settingsNavigation(f.document);
  return {...f, sections, links, heights, surface, scrolls, navigation, click: index => f.document.emit('click', {target: links[index]})};
}

test('new settings tabs start at the top without inflating a shorter destination', () => {
  const f = navigationFixture();
  f.window.scrollY = 1600;
  f.click(1);
  assert.equal(f.window.scrollY, 0);
  assert.equal(f.surface.style.minHeight, '');
  assert.equal(f.document.documentElement.scrollHeight, 800);
  assert.equal(f.sections[1].hidden, false);
  f.click(0); assert.equal(f.window.scrollY, 1600, 'returning restores only that tab\'s position');
  f.click(2); assert.equal(f.window.scrollY, 0, 'a new scrollable tab also starts at the top');
  f.window.scrollY = 300;
  f.click(0); assert.equal(f.window.scrollY, 1600);
  f.click(2); assert.equal(f.window.scrollY, 300);
});

test('restored settings positions clamp to the destination after its content shrinks', () => {
  const f = navigationFixture();
  f.window.scrollY = 1600; f.click(1);
  f.heights[0] = 1000;
  f.click(0);
  assert.equal(f.window.scrollY, 200);
  assert.equal(f.surface.style.minHeight, '');
  assert.equal(f.scrolls.at(-1).behavior, 'instant');
  assert.equal(f.document.documentElement.style.overflowAnchor, '');
});

test('active tab clicks and navigation refresh retain the current position', () => {
  const f = navigationFixture();
  f.window.scrollY = 700; f.click(1); f.click(0);
  f.window.scrollY = 350;
  const scrollCount = f.scrolls.length;
  f.click(0); f.navigation.refresh(); f.ui.settingsNavigation(f.document);
  assert.equal(f.window.scrollY, 350);
  assert.equal(f.scrolls.length, scrollCount);
  f.click(1); f.click(0); assert.equal(f.window.scrollY, 350);
});

test('hash navigation and validation keep independent settings positions', () => {
  const f = navigationFixture();
  f.window.scrollY = 900;
  f.context.location.hash = '#short'; f.window.emit('hashchange');
  assert.equal(f.window.scrollY, 0);
  f.context.location.hash = '#long'; f.window.emit('hashchange');
  assert.equal(f.window.scrollY, 900);
  f.window.scrollY = 600;
  f.document.emit('invalid', {target: Object.assign(new Element(), {closest: () => f.sections[2]})});
  assert.equal(f.sections[2].hidden, false);
  f.window.scrollY = 180;
  f.click(0); assert.equal(f.window.scrollY, 600, 'validation also records the section being left');
  f.click(2); assert.equal(f.window.scrollY, 180);
});

test('settings navigation also resets and restores without the optional scroll helper', () => {
  const f = navigationFixture({scrollHelper: false});
  f.window.scrollY = 1100; f.click(1);
  assert.equal(f.window.scrollY, 0);
  assert.equal(f.surface.style.minHeight, '');
  f.click(0); assert.equal(f.window.scrollY, 1100);
});

test('replaced native forms release guards while pending or dirty forms prevent local replacement', () => {
  const f = fixture({native: true});
  const region = {contains: node => node === f.form};
  assert.equal(f.ui.savebar(f.form), f.api, 'initialization is idempotent');
  f.title.value = 'unsaved'; f.form.emit('input');
  assert.equal(f.document.emit('chunnel:admin-before-update', {detail:{regions:[region]}}).defaultPrevented, true);
  f.api.discard();
  assert.equal(f.document.emit('chunnel:admin-before-update', {detail:{regions:[region]}}).defaultPrevented, false);
  f.api.start();
  assert.equal(f.document.emit('chunnel:admin-before-update', {detail:{regions:[region]}}).defaultPrevented, true);
  f.api.fail(); f.document.emit('chunnel:admin-before-replace', {detail:{regions:[region]}});
  f.title.value = 'detached';
  assert.equal(f.window.emit('beforeunload').defaultPrevented, false);
});

test('disposing a savebar detaches local handlers and ignores a late save acknowledgement', () => {
  const f = fixture();
  f.title.value = 'submitted'; f.form.emit('input'); f.api.start();
  f.api.dispose();
  const message = f.output.textContent;
  f.api.success(); f.api.fail('late failure'); f.title.value = 'detached'; f.form.emit('input');
  assert.equal(f.output.textContent, message);
  assert.equal(f.api.start(), false);
  for (const event of ['input', 'change', 'invalid', 'submit']) assert.equal((f.form.listeners.get(event) || []).length, 0);
  assert.equal((f.discard.listeners.get('click') || []).length, 0);
  assert.equal(f.window.emit('beforeunload').defaultPrevented, false);
});

test('disposing before the native submit microtask cannot revive an abandoned savebar', async () => {
  const f = fixture({native: true});
  f.title.value = 'changed'; f.form.emit('input'); f.form.emit('submit'); f.api.dispose();
  await Promise.resolve(); f.flushTimers();
  assert.equal(f.api.pending(), false);
  assert.equal(f.api.start(), false);
  assert.equal(f.window.emit('beforeunload').defaultPrevented, false);
  const replacement = f.ui.savebar(f.form, {native: true});
  assert.notEqual(replacement, f.api);
  assert.equal((f.form.listeners.get('input') || []).length, 1);
});

test('incomplete savebar markup leaves the native form usable', () => {
  const f = fixture({attachSavebar: false});
  f.bar.selectors.delete('[data-admin-save]');
  assert.equal(f.ui.savebar(f.form), null);
  assert.equal(f.form.emit('submit').defaultPrevented, false);
});

test('comment editor binds the shared dock, preserves busy state and recovers on back navigation', async () => {
  const f = fixture({attachSavebar: false});
  const editor = new Element(), content = new Element(); content.value = '原始评论';
  f.title.dataset.commentEditField = 'author_name'; content.dataset.commentEditField = 'content_source';
  f.document.selectors.set('[data-comment-editor]', editor);
  editor.selectors.set('[data-comment-edit-form]', f.form);
  f.form.groups.set('[data-comment-edit-field]', [f.title, content]);
  f.form.selectors.set('[data-comment-edit-source]', content);
  f.form.selectors.set('[data-admin-savebar]', f.bar);
  f.form.selectors.set('[data-save-state]', f.output);
  f.form.selectors.set('[data-admin-save]', f.save);
  f.form.checkValidity = () => true;
  f.document.createTextNode = text => Object.assign(new Element(), {textContent: text});
  f.save.replaceChildren = (...nodes) => { f.save.textContent = nodes.map(node => node.textContent).join(''); };
  f.window.ChunnelAdmin = {icon: () => new Element()};
  const admin = entry === 'TypeScript' ? compile('frontend/core/admin-comments.ts') : fs.readFileSync('internal/coreui/assets/js/chunnel-admin-comments.js', 'utf8');
  vm.runInNewContext(admin, {...f.context, TextEncoder});
  assert.equal(f.save.disabled, true);
  content.value = '修改后的评论'; content.emit('input');
  assert.equal(f.save.disabled, false); assert.equal(f.output.textContent, '未保存');
  assert.equal(f.window.emit('beforeunload').defaultPrevented, true);
  const cancelled = f.form.emit('submit'); cancelled.preventDefault(); await Promise.resolve();
  assert.equal(f.save.disabled, false, 'a cancelled submit keeps the editor available');
  assert.equal(f.window.emit('beforeunload').defaultPrevented, true);
  f.form.emit('submit'); await Promise.resolve();
  assert.equal(f.form.emit('submit').defaultPrevented, true, 'pending native submission cannot be repeated');
  content.value = '提交期间的输入'; content.emit('input');
  assert.equal(f.save.disabled, true, 'typing must not unlock a pending native submission');
  f.window.emit('pageshow', {persisted: true});
  assert.equal(f.form.getAttribute('aria-busy'), 'false'); assert.equal(f.save.disabled, false);
  assert.equal(f.save.textContent, '保存更改');
  f.discard.emit('click');
  assert.equal(content.value, '原始评论'); assert.equal(f.save.disabled, true);
  assert.equal(f.window.emit('beforeunload').defaultPrevented, false);
});

}
