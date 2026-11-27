const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function fixture(variant, replies) {
  const fields = Object.fromEntries(Object.entries({
    title: 'Unsent editorial changes', body: 'News body', source_name: '', source_url: '',
    published_at: '2026-11-01T02:30', published_timezone: 'America/New_York',
  }).map(([name, value]) => [name, {value}]));
  const status = {textContent: ''}, label = {textContent: 'America/New_York'};
  const confirm = {addEventListener(type, listener) {this[type] = listener;}};
  const review = {hidden: true, querySelector: () => confirm};
  const form = {
    dataset: {userId: '7', id: '4', version: '2', endpoint: '/api/v1/admin/flash', admin: 'true'},
    addEventListener(type, listener) {this[type] = listener;},
    querySelector: selector => selector === '[name="published_timezone"]' ? fields.published_timezone : selector === '[data-flash-timezone-review]' ? review : null,
    querySelectorAll: selector => selector === 'button' ? [confirm] : selector === '[data-flash-timezone-label]' ? [label] : [],
  };
  const requests = [], navigations = [];
  let keyNumber = 0;
  const context = vm.createContext({
    document: {
      querySelector: selector => selector === '[data-flash-editor]' ? form : selector === '[data-flash-status]' ? status : null,
      querySelectorAll: () => [], addEventListener() {},
    },
    window: {confirm: () => true, location: {assign: value => navigations.push(value)}},
    location: {pathname: '/admin/flash/'},
    crypto: {randomUUID: () => `retry-key-${++keyNumber}`},
    FormData: class {
      constructor() {this.values = Object.fromEntries(Object.entries(fields).map(([name, field]) => [name, field.value]));}
      get(name) {return this.values[name] ?? null;}
      has(name) {return Object.hasOwn(this.values, name);}
    },
    fetch: async (url, options) => {
      if (url === '/api/v1/social/session') return {ok: true, json: async () => ({authenticated: true, csrf_token: 'csrf'})};
      requests.push({url, ...options});
      const response = replies.shift();
      if (response instanceof Error) throw response;
      assert.ok(response, 'unexpected write request');
      return {ok: response.ok, json: async () => response.data};
    },
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, '../../internal/coreui/assets/js', `chunnel-flash${variant === 'source' ? '.source' : ''}.js`), 'utf8'), context);
  return {fields, status, label, review, requests, navigations, confirm: () => confirm.click(), submit: () => form.submit({preventDefault() {}, submitter: {value: 'save'}})};
}

for (const variant of ['source', 'built']) {
  test(`${variant}: flash retries preserve the original time, timezone, body and key after a lost response`, async () => {
    const f = fixture(variant, [new Error('Connection lost'), {ok: true, data: {id: 4, version: 3, replayed: true}}]);
    await f.submit();
    assert.equal(f.review.hidden, true);
    assert.equal(f.status.textContent, 'Connection lost');
    await f.submit();
    assert.equal(f.requests.length, 2);
    assert.equal(f.requests[0].body, f.requests[1].body);
    assert.equal(f.requests[0].headers['Idempotency-Key'], f.requests[1].headers['Idempotency-Key']);
    const submitted = JSON.parse(f.requests[0].body);
    assert.equal(submitted.published_at, '2026-11-01T02:30');
    assert.equal(submitted.published_timezone, 'America/New_York');
    assert.deepEqual(f.navigations, ['/admin/flash/?edit=4']);
  });

  test(`${variant}: a changed flash timezone requires explicit review while preserving the unsaved input`, async () => {
    const rejection = {ok: false, data: {error: {message: 'Please review the changed timezone', details: {published_timezone: 'Asia/Shanghai'}}}};
    const f = fixture(variant, [rejection, rejection, {ok: true, data: {id: 4, version: 3}}]);
    await f.submit();
    assert.equal(f.review.hidden, false);
    assert.equal(f.label.textContent, 'Asia/Shanghai');
    assert.equal(f.fields.published_timezone.value, 'America/New_York');
    assert.equal(f.fields.published_at.value, '2026-11-01T02:30');
    assert.equal(f.fields.title.value, 'Unsent editorial changes');
    assert.equal(f.navigations.length, 0);
    await f.submit();
    assert.equal(f.requests[1].body, f.requests[0].body);
    assert.equal(f.requests[1].headers['Idempotency-Key'], f.requests[0].headers['Idempotency-Key']);
    f.confirm();
    assert.equal(f.review.hidden, true);
    assert.equal(f.fields.published_timezone.value, 'Asia/Shanghai');
    assert.equal(f.fields.published_at.value, '2026-11-01T02:30');
    assert.equal(f.requests.length, 2, 'review alone cannot submit a write');
    await f.submit();
    assert.equal(JSON.parse(f.requests[2].body).published_timezone, 'Asia/Shanghai');
    assert.notEqual(f.requests[2].headers['Idempotency-Key'], f.requests[0].headers['Idempotency-Key']);
  });
}
