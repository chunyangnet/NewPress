const nodeTest = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');

for (const [entry, source] of [
  ['TypeScript', compile('frontend/shared/selects.ts')],
  ['generated', fs.readFileSync(path.join(__dirname, '../../themes/chunnel/assets/js/chunnel-selects.js'), 'utf8')],
]) {
  const test = (name, fn) => nodeTest(`${entry}: ${name}`, fn);

  class Element {
    constructor(tag) { this.tagName = tag.toUpperCase(); this.children = []; this.attributes = new Map(); }
    get firstElementChild() { return this.children[0] || null; }
    setAttribute(key, value) { this.attributes.set(key, value); }
    removeAttribute(key) { this.attributes.delete(key); }
    hasAttribute(key) { return this.attributes.has(key); }
    toggleAttribute(key, force) { if (force) this.setAttribute(key, ''); else this.removeAttribute(key); }
    append(node) { node.remove(); node.parentElement = this; this.children.push(node); }
    prepend(node) { node.remove(); node.parentElement = this; this.children.unshift(node); }
    remove() { if (this.parentElement) this.parentElement.children = this.parentElement.children.filter(n => n !== this); this.parentElement = null; }
    querySelector(selector) {
      if (selector.startsWith(':scope > button')) return this.children.find(n => n.tagName === 'BUTTON' && (!selector.includes('[data-select-display]') || n.hasAttribute('data-select-display'))) || null;
      return this.querySelectorAll(selector)[0] || null;
    }
    querySelectorAll(tag) { return this.children.flatMap(n => [...(n.tagName.toLowerCase() === tag ? [n] : []), ...n.querySelectorAll(tag)]); }
  }
  class Select extends Element {
    constructor() { super('select'); this.multiple = false; this.size = 0; this.value = 'kept'; this.disabled = false; }
  }
  class Form extends Element {
    constructor() { super('form'); this.elements = []; }
  }
  function fixture(selects, {support = true, selectedContent = true} = {}) {
    const body = new Element('body');
    selects.forEach(s => body.append(s));
    const state = {body, observes: 0, events: {}, tasks: []};
    const window = {CSS: {supports: () => support}};
    if (selectedContent) window.HTMLSelectedContentElement = class {};
    vm.runInNewContext(source, {
      window, Element, HTMLSelectElement: Select, HTMLFormElement: Form, HTMLScriptElement: class {}, setTimeout: callback => state.tasks.push(callback),
      document: {body, querySelectorAll: tag => body.querySelectorAll(tag), createElement: tag => new Element(tag), addEventListener: (type, callback) => { state.events[type] = callback; }},
      MutationObserver: class { constructor(callback) { state.notify = callback; } observe(node, options) { state.observes++; state.options = options; } }
    });
    return state;
  }

  test('native select display preserves value, disabled state and the actual options', () => {
    const s = new Select(), option = new Element('option');
    s.disabled = true;
    s.append(option);
    fixture([s]);
    assert.equal(s.value, 'kept');
    assert.equal(s.disabled, true);
    assert.equal(s.firstElementChild.type, 'button');
    assert.equal(s.firstElementChild.firstElementChild.tagName, 'SELECTEDCONTENT');
    assert.equal(s.children[1], option);
    assert.equal(s.hasAttribute('data-native-select'), true);
  });

  test('replacing dynamic options repairs the display once without changing the value', () => {
    const s = new Select(), f = fixture([s]);
    s.firstElementChild.remove();
    const option = new Element('option');
    s.append(option);
    f.notify([{target: s, type: 'childList', addedNodes: [option]}]);
    const button = s.firstElementChild;
    f.notify([{target: s, type: 'childList', addedNodes: [button]}]);
    assert.equal(s.querySelectorAll('button').length, 1);
    assert.equal(s.children[1], option);
    assert.equal(s.value, 'kept');
  });

  test('inserted controls are discovered and switching to listbox or multiple removes only the owned display', () => {
    const f = fixture([]), container = new Element('div'), s = new Select();
    container.append(s);
    f.body.append(container);
    f.notify([{target: f.body, type: 'childList', addedNodes: [container]}]);
    assert.equal(s.hasAttribute('data-native-select'), true);
    for (const mode of ['multiple', 'size']) {
      s[mode] = mode === 'multiple' ? true : 4;
      f.notify([{target: s, type: 'attributes'}]);
      assert.equal(s.hasAttribute('data-native-select'), false);
      assert.equal(s.querySelectorAll('button').length, 0);
      s[mode] = mode === 'multiple' ? false : 1;
      f.notify([{target: s, type: 'attributes'}]);
      assert.equal(s.querySelectorAll('button').length, 1);
    }
    assert.equal(s.value, 'kept');
  });

  test('an author-provided button is preserved, including when multiple is enabled', () => {
    const s = new Select(), button = new Element('button');
    button.append(new Element('selectedcontent'));
    s.append(button);
    const f = fixture([s]);
    assert.equal(s.firstElementChild, button);
    assert.equal(s.querySelectorAll('button').length, 1);
    s.multiple = true;
    f.notify([{target: s, type: 'attributes'}]);
    assert.equal(s.firstElementChild, button);
    assert.equal(s.hasAttribute('data-native-select'), false);
  });

  test('unsupported engines retain untouched native selects and need no observer', () => {
    for (const options of [{support: false}, {selectedContent: false}]) {
      const s = new Select(), f = fixture([s], options);
      assert.equal(s.children.length, 0);
      assert.equal(s.value, 'kept');
      assert.equal(f.observes, 0);
    }
  });

  test('reset refreshes the live native index after the default action, including external form controls', () => {
    const s = new Select(), f = fixture([s]), form = new Form(), writes = [];
    let index = 1;
    Object.defineProperty(s, 'selectedIndex', {get: () => index, set: next => { index = next; writes.push(next); }});
    form.elements = [s];
    f.events.reset({target: form, defaultPrevented: false});
    assert.equal(writes.length, 0);
    index = 0; // The browser's reset algorithm runs after dispatching the event.
    f.tasks.shift()();
    assert.deepEqual(writes, [0]);
    assert.equal(s.value, 'kept');
  });

  test('cancelled resets and controls switched to multiple never rewrite selectedness', () => {
    const s = new Select(), f = fixture([s]), form = new Form(), writes = [];
    Object.defineProperty(s, 'selectedIndex', {get: () => 0, set: next => writes.push(next)});
    form.elements = [s];
    const event = {target: form, defaultPrevented: false};
    f.events.reset(event);
    event.defaultPrevented = true;
    f.tasks.shift()();
    f.events.reset({target: form, defaultPrevented: false});
    s.multiple = true;
    f.tasks.shift()();
    assert.deepEqual(writes, []);
  });
}
