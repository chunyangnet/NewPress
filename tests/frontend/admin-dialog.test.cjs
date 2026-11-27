const nodeTest = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');
for (const variant of ['TypeScript', 'generated']) {
const test = (name, fn) => nodeTest(`${variant}: ${name}`, fn);
const source = variant === 'TypeScript' ? compile('frontend/core/admin-settings.ts') : fs.readFileSync('internal/coreui/assets/js/chunnel-admin-settings.js', 'utf8');

function fixture() {
  const events = {}, pending = [], classes = new Set();
  const close = { focus() {} }, open = { addEventListener(_, fn) { this.click = fn; } };
  const dialog = {
    open: false,
    showModal() { this.open = true; },
    close() { this.open = false; pending.push(() => events.close()); },
    addEventListener(type, fn) { events[type] = fn; },
    querySelector(selector) { return selector === '.admin-dialog-close' ? close : null; },
    querySelectorAll() { return []; },
  };
  const document = {
    querySelector: selector => selector === '[data-import-dialog]' ? dialog : selector === '[data-import-open]' ? open : null,
    querySelectorAll: () => [],
    body: { classList: { toggle(name, active) { active ? classes.add(name) : classes.delete(name); }, remove: name => classes.delete(name) } },
  };
  vm.runInNewContext(source, { document, window: { addEventListener() {} } });
  return { dialog, open: () => open.click(), locked: () => classes.has('admin-dialog-open'), flush: () => { while (pending.length) pending.shift()(); } };
}

test('native import close releases its scroll lock', () => {
  const f = fixture(); f.open(); assert.equal(f.locked(), true);
  f.dialog.close(); f.flush(); assert.equal(f.locked(), false);
});

test('a queued native close cannot unlock a reopened import dialog', () => {
  const f = fixture(); f.open(); f.dialog.close(); f.open(); f.flush();
  assert.equal(f.dialog.open, true); assert.equal(f.locked(), true);
  f.dialog.close(); f.flush(); assert.equal(f.locked(), false);
});
}
