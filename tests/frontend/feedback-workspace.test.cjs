const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const {compile}=require('./helpers/typescript.cjs');
const source = process.env.NEWPRESS_FORMS_TS==='1'?compile('frontend/shared/feedback.ts'):fs.readFileSync(require('node:path').join(__dirname, '../../themes/chunnel/assets/js/chunnel-feedback.js'), 'utf8');
const tick = () => new Promise(resolve => setImmediate(resolve));
function fixture() {
  const events = {}, documentEvents = {}, windowEvents = {}, calls = [], navigations = [];
  const status = { hidden: true, textContent: '' }, latest = {}, strong = {};
  const conflict = { hidden: true, querySelector: selector => selector === 'strong' ? strong : latest };
  const form = { dataset: { feedbackForm: 'submit' }, fields: { title: '改进收藏功能', body: '支持分类和按日期筛选', kind: 'suggestion' }, disabled: false,
    matches: selector => selector === 'form', closest: selector => selector === '[data-feedback-form]' ? form : null,
    querySelector: () => status, reportValidity: () => true };
  const root = { dataset: { slug: 'ideas', version: '1', csrf: 'csrf', userId: '2' }, hasAttribute: () => false,
    querySelector: selector => ({ '[data-feedback-status]': status, '[data-feedback-conflict]': conflict, '[data-feedback-form]': form })[selector],
    querySelectorAll: () => [form], setAttribute() {}, removeAttribute() {}, addEventListener: (type, listener) => { events[type] = listener; } };
  let keys = 0;
  class Element {static [Symbol.hasInstance](value){return !!value&&typeof value.closest==='function';}}
  class Form {static [Symbol.hasInstance](value){return value?.matches?.('form')===true;}}
  vm.runInNewContext(source, { Element,HTMLFormElement:Form,AbortController,DOMException,setTimeout,clearTimeout,document: { querySelector: () => root, addEventListener: (type, listener) => { const previous=documentEvents[type];documentEvents[type]=event=>{previous?.(event);listener(event);}; } },
    window: { addEventListener: (type, listener) => { windowEvents[type] = listener; } },
    location: { pathname: '/feedback/ideas/', search: '', assign: value => navigations.push(value), reload: () => navigations.push('reload') },
    crypto: { randomUUID: () => 'test-request-key-' + (++keys) }, AbortSignal,
    FormData: class { constructor(node) { return Object.entries(node.fields); } },
    fetch: (url, options) => new Promise((resolve, reject) => calls.push({url, options, body:JSON.parse(options.body), resolve, reject})),
  });
  return { form, root, status, conflict, latest, events, documentEvents, calls, navigations,
    input: (node = form) => events.input({ target: node }),
    submit: (node = form) => events.submit({ target: node, preventDefault() {} }),
    resolve: (i, value, status = 200) => calls[i].resolve({ok:status<400, status, json:async()=>value}),
    session: id => documentEvents['chunnel:session']({detail:{authenticated:!!id,id}}), windowEvents };
}
test('feedback suppresses duplicate submit and retries uncertain writes with the original receipt and version', async () => {
  const f = fixture(); f.input(); f.submit(); f.submit();
  assert.equal(f.calls.length,1); assert.equal(f.form.disabled,true);
  f.calls[0].reject(new Error('network failed')); await tick();
  assert.equal(f.form.fields.body,'支持分类和按日期筛选'); assert.equal(f.form.disabled,false);
  f.root.dataset.version='9'; f.submit();
  assert.equal(f.calls[1].body.key,f.calls[0].body.key);
  assert.equal(f.calls[1].body.version,1);
  f.resolve(1,{slug:'ideas',version:2,item_id:7,replay:true}); await tick();
  assert.deepEqual(f.navigations,['/feedback/ideas/items/7']);
});
test('editing a failed feedback request produces a new receipt without erasing text', async () => {
  const f = fixture();f.submit();f.resolve(0,{error:{message:'字段不正确'}},422);await tick();
  f.form.fields.body='改过的完整描述内容';f.input();f.submit();
  assert.notEqual(f.calls[1].body.key,f.calls[0].body.key);assert.equal(f.calls[1].body.body,'改过的完整描述内容');
  f.resolve(1,{slug:'ideas',version:2,item_id:7});await tick();
});
test('configuration conflict retains input and opens latest version without silently overwriting', async () => {
  const f=fixture();f.input();f.submit();f.resolve(0,{error:{message:'内容已更新'}},409);await tick();
  assert.equal(f.conflict.hidden,false);assert.equal(f.latest.href,'/feedback/ideas/');assert.equal(f.navigations.length,0);assert.equal(f.form.fields.title,'改进收藏功能');
});
test('account change during feedback save ignores the late receipt and blocks subsequent writes', async () => {
  const f=fixture();f.input();f.submit();f.session(3);f.resolve(0,{slug:'ideas',version:2,item_id:1});await tick();
  assert.equal(f.navigations.length,0);assert.match(f.status.textContent,/账号已变化/);f.submit();assert.equal(f.calls.length,1);
});
test('malformed and mismatched reply receipts retain the form for safe retry', async () => {
  for(const result of [{slug:'other',version:2,item_id:4,reply_id:2,reply_page:1},{slug:'ideas',version:2,item_id:999,reply_id:2,reply_page:1},{slug:'ideas',version:2,item_id:4,reply_id:2}]){
    const f=fixture();f.form.dataset={feedbackForm:'reply',item:'4'};f.form.fields={body:'需要保留的回复'};f.input();f.submit();f.resolve(0,result);await tick();
    assert.equal(f.navigations.length,0);assert.equal(f.form.fields.body,'需要保留的回复');assert.match(f.status.textContent,/回执/);
  }
});
test('saving one feedback form does not navigate away from a different unsaved form', async () => {
  const f=fixture();const other={closest:()=>other};f.input(other);f.input();f.submit();f.resolve(0,{slug:'ideas',version:2,item_id:3});await tick();
  assert.equal(f.navigations.length,0);assert.equal(f.latest.href,'/feedback/ideas/items/3');assert.match(f.status.textContent,/另一处/);
});
