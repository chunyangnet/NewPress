'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');

for (const variant of ['TypeScript', 'generated']) {
  const source = variant === 'TypeScript' ? compile('frontend/core/admin-comments.ts') : fs.readFileSync('internal/coreui/assets/js/chunnel-admin-comments.js', 'utf8');
  function fixture({ csrf = 'fixture', endpoint = '/local-test' } = {}) {
    let click, complete, calls = 0, submit;
    const button = { dataset: { aiReplyUrl: endpoint }, addEventListener: (_, fn) => { click = fn; }, setAttribute() {}, removeAttribute() {}, replaceChildren() {} };
    const status = {}, textarea = { value: '', focus() {}, dispatchEvent() {}, addEventListener() {} };
    const controls = { '[data-ai-reply-draft]': button, '[data-ai-reply-status]': status, '[data-comment-reply-source]': textarea, 'input[name="csrf_token"]': { value: csrf } };
    const replyForm = { querySelector: s => controls[s] || null, checkValidity: () => true, setAttribute() {}, addEventListener: (name, fn) => { if (name === 'submit') submit = fn; } };
    const editor = { querySelector: s => s === '[data-comment-reply-form]' ? replyForm : null, querySelectorAll: () => [] };
    vm.runInNewContext(source, {
      window: { confirm: () => true, addEventListener() {}, ChunnelAdmin: { icon: s => s } },
      document: { querySelector: () => editor, addEventListener() {}, createTextNode: s => s }, Event, TextEncoder, queueMicrotask, setTimeout, clearTimeout,
      fetch: () => { calls++; return new Promise(r => { complete = r; }); },
    });
    return { click: () => click(), done: (payload = { draft: '模型建议' }, ok = true) => complete({ ok, json: async () => payload }), textarea, status, button, calls: () => calls,
      submit: async () => { submit({ defaultPrevented: false, preventDefault() {} }); await Promise.resolve(); } };
  }

  test(`${variant}: waiting for an AI draft preserves later edits and rejects duplicate generation`, async () => {
    const f = fixture(); const pending = f.click(); await f.click(); assert.equal(f.calls(), 1); assert.equal(f.button.disabled, true);
    f.textarea.value = '我在等待期间输入的回复'; f.done(); await pending;
    assert.equal(f.textarea.value, '我在等待期间输入的回复'); assert.match(f.status.textContent, /保留/); assert.equal(f.button.disabled, false);
  });
  test(`${variant}: unchanged reply receives the draft for manual submission`, async () => {
    const f = fixture(); const pending = f.click(); f.done(); await pending; assert.equal(f.textarea.value, '模型建议'); assert.match(f.status.textContent, /手动发送/);
  });
  test(`${variant}: a submitted reply rejects late AI drafts and further generation`, async () => {
    const f = fixture(); const pending = f.click(); await f.submit(); f.done(); await pending;
    assert.equal(f.textarea.value, ''); await f.click(); assert.equal(f.calls(), 1);
  });
  for (const payload of [null, [], 'text', { draft: 7 }, { draft: '  ' }, { error: { message: 9 } }]) {
    test(`${variant}: malformed AI payload ${JSON.stringify(payload)} leaves the reply usable`, async () => {
      const f = fixture(); const pending = f.click(); f.done(payload); await pending;
      assert.equal(f.textarea.value, ''); assert.equal(f.button.disabled, false); assert.match(f.status.textContent, /没有返回可用草稿/);
    });
  }
  test(`${variant}: API failure shows a validated message and permits retry`, async () => {
    const f = fixture(); const pending = f.click(); f.done({ error: { message: '服务暂不可用' } }, false); await pending;
    assert.equal(f.status.textContent, '服务暂不可用'); assert.equal(f.button.disabled, false);
    const retry = f.click(); assert.equal(f.calls(), 2); f.done(); await retry; assert.equal(f.textarea.value, '模型建议');
  });
  test(`${variant}: missing token or endpoint prevents a draft request`, async () => {
    for (const options of [{ csrf: '' }, { endpoint: '' }]) {
      const f = fixture(options); await f.click(); assert.equal(f.calls(), 0); assert.equal(f.textarea.value, ''); assert.match(f.status.textContent, /不可用/);
    }
  });
}
