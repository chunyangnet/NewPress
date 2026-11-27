// Run the whole picker through its public API and DOM events, without slicing source.
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { compile } = require('./typescript.cjs');
const code = new Map();
const imageItem = (overrides = {}) => ({ id: 1, status: 'active', kind: 'image', public_url: '/media/1/image.png',
  filename: 'image.png', title: 'Image', alt_text: 'Library alt', caption: 'Library caption', size_bytes: 128, width: 100, height: 50, ...overrides });
const mediaList = (items = [imageItem()], overrides = {}) => ({ items, pagination: { page: 1, pages: 1, total: items.length }, can_upload: true, max_upload_bytes: 1048576, ...overrides });
const escapeHTML = value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;');

class Element {
  constructor(tag = 'div') {
    this.tagName = tag.toUpperCase(); this.listeners = new Map(); this.selectors = new Map(); this.groups = new Map();
    this.attributes = new Map(); this.dataset = {}; this.children = []; this.value = ''; this.textContent = '';
    this.disabled = false; this.readOnly = false; this.hidden = false; this.checked = false; this.isConnected = true;
    this.naturalWidth = 100; this.selectionStart = 0; this.selectionEnd = 0; this.valid = true; this.open = false;
  }
  addEventListener(type, callback) { this.listeners.set(type, [...(this.listeners.get(type) || []), callback]); }
  emit(type, values = {}) {
    const event = { type, target: this, currentTarget: this, defaultPrevented: false, immediateStopped: false,
      preventDefault() { this.defaultPrevented = true; }, stopImmediatePropagation() { this.immediateStopped = true; }, ...values };
    event.completion = Promise.all((this.listeners.get(type) || []).map(fn => fn(event)));
    return event;
  }
  querySelector(selector) { return this.selectors.get(selector) || this.querySelectorAll(selector)[0] || null; }
  querySelectorAll(selector) {
    if (this.groups.has(selector)) return this.groups.get(selector);
    if (selector === 'button') return this.children.filter(child => child.tagName === 'BUTTON');
    if (selector === '[data-editor-image-id]') return this.children.filter(child => child.dataset.editorImageId);
    return [];
  }
  matches(selector) { return selector === ':disabled' && this.disabled; }
  closest(selector) { return this.parents?.get(selector) || null; }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  removeAttribute(name) { this.attributes.delete(name); if (name === 'href') delete this.href; }
  append(...children) { for (const child of children) this.children.push(...(child.tagName === 'FRAGMENT' ? child.children : [child])); }
  replaceChildren(...children) { this.children = []; this.append(...children); }
  get firstElementChild() { return this.children[0] || null; }
  cloneNode() { return new Element(this.tagName); }
  focus() { this.focused = true; }
  click() { if (!this.disabled) return this.emit('click'); }
  reset() { for (const input of Object.values(this.elements || {})) { input.value = ''; input.checked = false; } }
  reportValidity() { this.validityChecks = (this.validityChecks || 0) + 1; return this.valid; }
  setCustomValidity(message) { this.validationMessage = message; }
  showModal() { this.open = true; }
  close() { this.open = false; this.emit('close'); }
  dispatchEvent(event) { this.emit(event.type); return true; }
  get outerHTML() {
    const tag = this.tagName.toLowerCase(), attrs = [...this.attributes].map(([key, value]) => ` ${key}="${escapeHTML(value)}"`).join('');
    return `<${tag}${attrs}>` + (tag === 'img' ? '' : escapeHTML(this.textContent) + this.children.map(child => child.outerHTML).join('') + `</${tag}>`);
  }
}

function fixture(variant = 'TypeScript', options = {}) {
  const document = new Element(), window = new Element(), dialog = new Element('dialog'), editor = new Element('form');
  const requests = [], timers = new Map(), selections = [], insertions = [], controls = new Map(); let timerID = 0;
  document.body = new Element('body'); document.createElement = tag => new Element(tag); document.createDocumentFragment = () => new Element('fragment');
  document.selectors.set('[data-editor-media-dialog]', dialog); document.selectors.set('[data-content-editor]', options.editor === false ? null : editor);
  dialog.dataset = { canUpload: options.canUpload === false ? 'false' : 'true', csrf: options.csrf === false ? '' : 'test-csrf' };
  function add(name, tag = 'div') { const item = new Element(tag); controls.set(name, item); dialog.selectors.set(`[data-editor-media-${name}]`, item); return item; }
  const search = add('search', 'form'), query = new Element('input'), searchButton = new Element('button'); search.elements = { q: query };
  dialog.selectors.set('[data-editor-media-search] [name="q"]', query); dialog.selectors.set('[data-editor-media-search] button', searchButton);
  const details = add('insert', 'form'); details.elements = {};
  for (const name of ['alt_text', 'decorative', 'caption', 'width']) {
    const input = new Element(name === 'caption' ? 'textarea' : 'input'); details.elements[name] = input; dialog.selectors.set(`[name="${name}"]`, input);
  }
  const fields = new Element('fieldset'); dialog.selectors.set('[data-editor-image-fields]', fields);
  const grid = add('grid'), preview = add('preview'); preview.append(new Element('svg'));
  for (const name of ['apply', 'upload', 'previous', 'next', 'retry', 'close']) add(name, 'button');
  for (const name of ['status', 'error', 'library', 'progress', 'name', 'dimensions', 'total', 'page']) add(name);
  const file = add('file', 'input'), recovery = add('recover', 'a'), errorText = new Element('span');
  if (options.canUpload === false) { dialog.selectors.delete('[data-editor-media-upload]'); dialog.selectors.delete('[data-editor-media-file]'); }
  dialog.selectors.set('[data-editor-media-error] span', errorText);
  dialog.selectors.set('[data-editor-media-apply] span', new Element('span'));
  dialog.selectors.set('#editor-media-title', new Element('h2'));
  const source = new Element('textarea'), cover = new Element('input'), bodyLauncher = new Element('button'), coverLauncher = new Element('button'), launcher = new Element('button');
  source.value = 'draft'; source.selectionStart = source.selectionEnd = 5;
  bodyLauncher.dataset.editorMediaOpen = 'body'; coverLauncher.dataset.editorMediaOpen = 'cover';
  editor.selectors.set('[data-content-source]', source); editor.selectors.set('[name="cover_url"]', cover);
  editor.selectors.set('[data-editor-media-open="body"]', bodyLauncher); editor.groups.set('[data-editor-media-open]', [bodyLauncher, coverLauncher]);
  window.ChunnelBlockEditor = { capture: () => ({ value: source.value, start: source.selectionStart, end: source.selectionEnd }),
    insert: (markup, selection) => insertions.push({ markup, selection }) };
  const globals = { document, window, Element, HTMLInputElement: Element, HTMLTextAreaElement: Element, URL, Headers, FormData, File, Blob, AbortController, DOMException, Event, Error,
    location: { origin: 'https://newpress.test' }, MutationObserver: class { observe() {} },
    setTimeout(callback, delay) { const id = ++timerID; timers.set(id, { callback, delay }); return id; }, clearTimeout: id => timers.delete(id),
    fetch(url, init) { return new Promise((resolve, reject) => requests.push({ url: String(url), init, reject,
      respond(payload, status = 200) { resolve({ ok: status >= 200 && status < 300, status, json: () => Promise.resolve(payload) }); },
      respondBody(body) { resolve({ ok: true, status: 200, json: () => body }); } })); } };
  if (!code.has(variant)) code.set(variant, variant === 'TypeScript' ? compile('frontend/core/media-picker.ts')
    : fs.readFileSync(path.resolve(__dirname, '../../../internal/coreui/assets/js/chunnel-admin-media.js'), 'utf8'));
  vm.runInNewContext(code.get(variant), globals, { filename: 'media-picker-' + variant });
  const settle = async () => { for (let i = 0; i < 18; i++) await Promise.resolve(); };
  return { document, window, dialog, editor, source, cover, bodyLauncher, coverLauncher, launcher, query, search, searchButton, details, fields,
    grid, preview, file, recovery, errorText, controls, requests, timers, selections, insertions, settle,
    open(extra = {}) { return window.NewPressMediaPicker.open(launcher, { title: 'Test image', uploadSource: 'settings-image', disabled: () => false,
      onSelect: (url, item) => selections.push({ url, item }), ...extra }); },
    async reply(index, payload, status = 200) { requests[index].respond(payload, status); await settle(); },
    select(index = 0) { grid.children[index].click(); },
    ready() { preview.firstElementChild.emit('load'); },
    submit() { return details.emit('submit').completion; },
    paste(files) { return editor.emit('paste', { target: source, clipboardData: { files } }); },
    drop(files) { return editor.emit('drop', { target: source, dataTransfer: { files, types: ['Files'] } }); },
    close() { dialog.close(); }, hide() { window.emit('pagehide'); },
  };
}

module.exports = { fixture, mediaList, imageItem, Element };
