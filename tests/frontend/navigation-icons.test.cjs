const test = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const {compile} = require('./helpers/typescript.cjs');

for (const variant of ['typescript', 'generated']) {
const source = variant === 'typescript' ? compile('frontend/core/admin-menu.ts') : readFileSync('internal/coreui/assets/js/chunnel-admin-menu.js', 'utf8');

function fixture({ modal = true } = {}) {
  class Element {
    constructor(tag) { this.tag = tag; this.children = []; this.dataset = {}; this.attributes = {}; this.listeners = {}; this.hidden = false; this.disabled = false; this.value = ''; this.textContent = ''; }
    append(...nodes) { for (const node of nodes) { if (node.tag === 'fragment') this.append(...node.children); else { node.parentElement = this; this.children.push(node); } } }
    replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
    after(node) { const siblings = this.parentElement.children; const previous = siblings.indexOf(node); if (previous >= 0) siblings.splice(previous, 1); siblings.splice(siblings.indexOf(this) + 1, 0, node); node.parentElement = this.parentElement; }
    setAttribute(key, value) { this.attributes[key] = value; }
    getAttribute(key) { return this.attributes[key]; }
    addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
    dispatchEvent(event) { event.target ||= this; (this.listeners[event.type] || []).forEach(fn => fn(event)); }
    querySelectorAll(selector) { return this.children.flatMap(child => [...(selector === 'select[data-icon-select]' && child.tag === 'select' ? [child] : []), ...child.querySelectorAll(selector)]); }
    querySelector(selector) { return selector === '[data-icon-picker-trigger]' ? this.children.find(child => 'iconPickerTrigger' in child.dataset) : null; }
    closest(selector) { return selector === '[data-icon-option]' && 'iconOption' in this.dataset ? this : this.parentElement?.closest(selector); }
    matches() { return this.disabled || this.inDisabledFieldset; }
    focus() { document.activeElement = this; }
    showModal() { this.open = true; }
    close() { this.open = false; this.dispatchEvent({ type: 'close' }); }
    get selectedOptions() { return this.options.filter(option => option.value === this.value); }
    get isConnected() { return true; }
  }
  const body = new Element('body');
  const document = { body, createElement: tag => new Element(tag), createDocumentFragment: () => new Element('fragment'), querySelector: () => null,
    getElementById: id => ['icon-house', 'icon-compass'].includes(id) ? {} : null };
  const window = { ChunnelAdmin: { icon(value) { const icon = new Element('svg'); icon.name = value; return icon; } } };
  vm.runInNewContext(source, { document, window, Element, HTMLDialogElement: modal ? Element : undefined, Event: class { constructor(type) { this.type = type; } }, getComputedStyle: () => ({ gridTemplateColumns: '1fr 1fr 1fr' }) });
  const makeSelect = (value = '') => {
    const label = new Element('label'), select = new Element('select');
    select.value = value; select.options = [['', '无图标'], ['house', '首页'], ['compass', '发现'], ['theme-custom', '自定义 · theme-custom']].map(([value, textContent]) => ({ value, textContent }));
    label.append(select); body.append(label); return { label, select };
  };
  const attach = label => window.NewPressIconPicker.attach(label);
  const event = (element, type, properties = {}) => { const event = { type, preventDefault() { this.defaultPrevented = true; }, ...properties }; element.dispatchEvent(event); return event; };
  const dialog = () => body.children.find(child => child.tag === 'dialog');
  return { body, document, makeSelect, attach, event, dialog };
}

test(`${variant}: icon picker is lazy, shared, searchable and updates the native field once`, () => {
  const f = fixture(), first = f.makeSelect('house'), second = f.makeSelect('compass');
  f.attach(first.label); f.attach(second.label); f.attach(first.label);
  assert.equal(f.dialog(), undefined);
  assert.equal(first.label.children.length, 2);
  const trigger = first.label.children[1];
  let changes = 0; first.select.addEventListener('change', () => changes++);
  f.event(trigger, 'click'); const dialog = f.dialog();
  assert.equal(dialog.open, true);
  const search = dialog.children[1].children[1], grid = dialog.children[3];
  assert.equal(grid.children.find(button => button.dataset.iconOption === 'house').attributes['aria-pressed'], 'true');
  search.value = 'COMPASS'; f.event(search, 'input');
  const visible = grid.children.filter(button => !button.hidden);
  assert.deepEqual(visible.map(button => button.dataset.iconOption), ['compass']);
  f.event(grid, 'click', { target: visible[0] });
  assert.equal(first.select.value, 'compass'); assert.equal(changes, 1); assert.equal(dialog.open, false);
  assert.equal(trigger.children[0].name, 'compass'); assert.equal(f.document.activeElement, trigger);
  f.event(second.label.children[1], 'click');
  assert.equal(f.dialog(), dialog); assert.equal(f.body.children.filter(child => child.tag === 'dialog').length, 1);
  assert.equal(search.value, '');
});

test(`${variant}: Chinese search, keyboard focus and no-icon choice work without changing unrelated fields`, () => {
  const f = fixture(), first = f.makeSelect('theme-custom'), second = f.makeSelect('house'); f.attach(first.label); f.attach(second.label);
  assert.equal(first.select.value, 'theme-custom'); assert.equal(first.label.children[1].children[0].name, 'circle-help');
  f.event(first.label.children[1], 'click'); const dialog = f.dialog(), search = dialog.children[1].children[1], grid = dialog.children[3];
  search.value = '首页'; f.event(search, 'input'); f.event(search, 'keydown', { key: 'ArrowDown' });
  assert.equal(f.document.activeElement.dataset.iconOption, 'house');
  search.value = 'missing'; f.event(search, 'input'); assert.match(dialog.children[2].textContent, /没有找到/);
  search.value = ''; f.event(search, 'input'); f.event(grid, 'keydown', { key: 'Home', target: grid.children[2] });
  assert.equal(f.document.activeElement.dataset.iconOption, '');
  f.event(grid, 'click', { target: grid.children[0] });
  assert.equal(first.select.value, ''); assert.equal(second.select.value, 'house');
});

test(`${variant}: disabled controls and browsers without modal support retain their expected behavior`, () => {
  const f = fixture(), field = f.makeSelect('house'); field.select.inDisabledFieldset = true; f.attach(field.label);
  f.event(field.label.children[1], 'click'); assert.equal(f.dialog(), undefined);
  const legacy = fixture({ modal: false }), native = legacy.makeSelect('house'); legacy.attach(native.label);
  assert.equal(native.select.hidden, false); assert.equal(native.label.children.length, 1);
});

test(`${variant}: changed catalogs rebuild once and keyboard navigation skips disabled options`, () => {
  const f = fixture(), first = f.makeSelect('house'), second = f.makeSelect('compass');
  second.select.options[1].disabled = true;
  f.attach(first.label); f.attach(second.label);
  f.event(first.label.children[1], 'click'); const dialog = f.dialog(), grid = dialog.children[3];
  const oldHouse = grid.children[1]; dialog.close();
  f.event(second.label.children[1], 'click');
  assert.notEqual(grid.children[1], oldHouse); assert.equal(grid.children[1].disabled, true);
  f.event(grid, 'keydown', { key: 'ArrowRight', target: grid.children[0] });
  assert.equal(f.document.activeElement.dataset.iconOption, 'compass');
  f.event(grid, 'click', { target: grid.children[1] });
  assert.equal(second.select.value, 'compass'); assert.equal(dialog.open, true);
});
}
