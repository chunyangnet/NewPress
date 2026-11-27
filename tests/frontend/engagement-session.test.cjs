const nodeTest = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
for (const variant of ['TypeScript', 'generated']) {
const source = variant === 'TypeScript' ? require('./helpers/typescript.cjs').compile('frontend/shared/engagement.ts') : fs.readFileSync(require('node:path').join(__dirname, '../../themes/chunnel/assets/js/chunnel-engagement.js'), 'utf8');
const test = (name, fn) => nodeTest(variant + ': ' + name, fn);
const tick = () => new Promise(resolve => setImmediate(resolve));
function fixture() {
  let current = { id: 1, authenticated: true };
  const listeners = new Map(), requests = [], errors = [];
  const document = {
    querySelectorAll: () => [], hidden: false,
    addEventListener(name, callback) { listeners.set(name, [...(listeners.get(name) || []), callback]); },
    dispatchEvent(event) { for (const callback of listeners.get(event.type) || []) callback(event); },
  };
  const window = { Chunnel: { createIcon() {}, showToast() {} }, ChunnelInteractions: {
    getSession: async () => current,
    api: (path, options) => new Promise((resolve, reject) => requests.push({ path, options, resolve, reject })),
    run: action => Promise.resolve().then(action).catch(error => errors.push(error)),
  } };
  vm.runInNewContext(source, { window, document, CustomEvent: class { constructor(type, value) { this.type = type; this.detail = value?.detail; } } });
  return { client: window.ChunnelEngagement, requests, errors,
    session(next) { current = next; document.dispatchEvent({ type: 'chunnel:session', detail: next }); },
  };
}
function response(active = true) { return { bookmark: [], follow: [], like: [{ target_id: 7, count: 1, active }] }; }
test('an obsolete engagement read cannot restore another account preferences', async () => {
  const f = fixture(); await tick(); assert.equal(f.requests.length, 1);
  f.session({ id: 2, authenticated: true }); await tick(); assert.equal(f.requests.length, 2);
  f.requests[1].resolve(response(false)); await tick();
  f.requests[0].resolve(response(true)); await tick();
  assert.equal(f.client.preferences().likes.length, 0);
});
test('logout immediately clears account preferences and ignores a late response', async () => {
  const f = fixture(); await tick(); f.requests[0].resolve(response()); await tick();
  assert.equal(f.client.preferences().likes[0], 'post:7');
  const pending = f.client.refresh(); await tick(); f.session({ authenticated: false });
  assert.equal(f.client.authenticated, false); assert.equal(f.client.preferences().likes.length, 0);
  f.requests[1].resolve(response()); await pending;
  assert.equal(f.client.preferences().likes.length, 0);
});
test('failed engagement writes keep the last confirmed preference and allow retry', async () => {
  const f = fixture(); await tick(); f.requests[0].resolve(response()); await tick();
  const first = f.client.set('like', 7, false); await tick();
  f.requests[1].reject(new Error('storage unavailable')); await assert.rejects(first, /storage unavailable/);
  assert.equal(f.client.preferences().likes[0], 'post:7');
  const retry = f.client.set('like', 7, false); await tick();
  assert.equal(f.requests[2].options.body.active, false);
  f.requests[2].resolve({ ok: true }); await retry;
  assert.equal(f.client.preferences().likes.length, 0);
});

test('malformed reads and unconfirmed writes keep confirmed preferences', async () => {
  const f=fixture(); await tick(); f.requests[0].resolve(response()); await tick();
  const read=f.client.refresh(); await tick(); f.requests[1].resolve({like:[]}); await assert.rejects(read,/数据格式/);
  assert.equal(f.client.preferences().likes[0],'post:7');
  const write=f.client.set('like',7,false); await tick(); f.requests[2].resolve({ok:'true'}); await assert.rejects(write,/尚未确认/);
  assert.equal(f.client.preferences().likes[0],'post:7');
});

test('a slow read cannot undo a newer confirmed write', async () => {
  const f=fixture(); await tick(); f.requests[0].resolve(response()); await tick();
  const read=f.client.refresh(); await tick(); const write=f.client.set('like',7,false); await tick();
  f.requests[2].resolve({ok:true}); await write; f.requests[1].resolve(response()); await read;
  assert.equal(f.client.preferences().likes.length,0);
});

test('an old identity write cannot unlock a new identity operation on the same target', async () => {
  const f=fixture(); await tick(); f.requests[0].resolve(response()); await tick();
  const old=f.client.set('like',7,false); await tick(); f.session({id:2,authenticated:true}); await tick();
  f.requests[2].resolve(response()); await tick(); const current=f.client.set('like',7,false); await tick();
  f.requests[1].resolve({ok:true}); assert.equal(await old,false);
  assert.equal(await f.client.set('like',7,false),false); assert.equal(f.requests.length,4);
  f.requests[3].resolve({ok:true}); assert.equal(await current,true); assert.equal(f.client.preferences().likes.length,0);
});

}
