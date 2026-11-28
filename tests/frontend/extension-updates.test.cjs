const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source=process.env.CHUNNEL_EXTENSION_UPDATES_SOURCE?fs.readFileSync(process.env.CHUNNEL_EXTENSION_UPDATES_SOURCE,'utf8'):require('./helpers/typescript.cjs').compile('frontend/core/extension-updates.ts');
const tick = () => new Promise(resolve => setImmediate(resolve));
const job = (phase, extra = {}) => ({ id: phase === 'idle' ? '' : 'task-1', phase, message: phase, kind: 'theme', action: 'update', slug: 'chunnel', target: '0.43.0', downloaded: 0, total: 0, started_at:'2026-11-11T00:00:00Z',review_required:false,updated_at: '2026-11-11T00:00:00Z', ...extra });

function fixture(options = {}) {
  const nodes = new Map(), calls = [], timers = new Map(), events = {};
  let reloads = 0, clock = 0;
  const node = () => ({ dataset: {}, events: {}, attributes: {}, disabled: false, hidden: true, textContent: '',
    addEventListener(type, fn) { this.events[type] = fn; },
    setAttribute(name, value) { this.attributes[name] = value; },
    removeAttribute(name) { delete this.attributes[name]; }, focus() {} });
  const get = name => nodes.get('[data-' + name + ']');
  for (const name of ['extension-dialog', 'extension-check', 'extension-action', 'extension-confirm', 'extension-error', 'extension-job', 'extension-cancel', 'extension-reconnect', 'confirm-error', 'confirm-title', 'confirm-versions', 'confirm-detail', 'job-message', 'job-detail', 'job-progress']) nodes.set('[data-' + name + ']', node());
  const cancelDialog = node(), dialog = get('extension-dialog');
  dialog.open = false;
  dialog.showModal = () => { dialog.open = true; };
  dialog.close = () => { dialog.open = false; dialog.events.close?.(); };
  dialog.querySelectorAll = () => [cancelDialog, get('extension-confirm')];
  dialog.querySelector = () => cancelDialog;
  get('extension-action').dataset = { extensionAction: 'update', slug: 'chunnel', name: 'Chunnel', current: '0.42.0', revision: 'revision-1', version: '0.43.0', digest: 'digest-1' };
  get('extension-action').closest = () => options.impact ? {querySelector: () => ({textContent: options.impact})} : null;
  const root = { dataset: { kind: options.kind || 'theme', userId: '7', csrf: 'csrf-token', activeTheme: 'starter', channel: 'stable', autoCheck: String(Boolean(options.autoCheck)), readonly: String(Boolean(options.readOnly)), busy: String(Boolean(options.busy)) },
    setAttribute() {}, querySelector: selector => nodes.get(selector),
    querySelectorAll: selector => selector === '[data-extension-check], [data-extension-action]' ? [get('extension-check'), get('extension-action')] : selector === '[data-extension-action]' ? [get('extension-action')] : [] };
  vm.runInNewContext(source, { document: {querySelector:()=>root,addEventListener(type,fn){events[type]=fn;}}, window: { addEventListener(type, fn) { events[type] = fn; } }, location: { reload() { reloads++; } }, AbortController,
    setTimeout(fn, ms) { const id = ++clock; timers.set(id, { fn, ms }); return id; }, clearTimeout(id) { timers.delete(id); },
    fetch(url, init) { return new Promise((resolve, reject) => calls.push({ url, init, resolve, reject })); } });
  return { get, calls, timers, dialog, events, reloads: () => reloads,
    fire(name) { return get(name).events.click?.(); },
    respond(value, status = 200) { calls.at(-1).resolve({ status, ok: status < 400, json: async () => value }); },
    fail() { calls.at(-1).reject(new Error('Connection lost')); },
    poll() { const [id, timer] = [...timers].find(([, timer]) => timer.ms === 1000) || []; assert.ok(timer, 'poll is scheduled'); timers.delete(id); return timer.fn(); },
    posts: () => calls.filter(call => call.init.method === 'POST') };
}

test('theme update and rollback disclose dependents without changing plugin confirmation', async () => {
  for (const kind of ['theme', 'plugin']) {
    const f = fixture({kind, impact: '受影响子主题：child-theme。不兼容则不切换。'});
    f.respond(job('idle')); await tick();
    for (const action of ['update', 'rollback']) {
      f.get('extension-action').dataset.extensionAction = action;
      f.fire('extension-action');
      assert.equal(f.get('confirm-detail').textContent.includes('child-theme'), kind === 'theme');
      f.dialog.close();
    }
  }
});

test('entering a stale theme market reconciles before automatically checking once', async () => {
  const f = fixture({autoCheck: true});
  assert.equal(f.posts().length, 0);
  f.respond(job('idle')); await tick();
  assert.equal(f.posts().length, 1);
  assert.match(f.posts()[0].url, /\/theme\/check$/);
  assert.deepEqual(JSON.parse(f.posts()[0].init.body), {channel: 'stable', expected_user_id: 7});
  f.fire('extension-check'); assert.equal(f.posts().length, 1);
  f.respond(job('queued', {action: 'check'}), 202); await tick();
  f.poll(); f.respond(job('succeeded', {action: 'check'})); await tick();
  assert.equal(f.reloads(), 1);
  assert.equal(f.posts().length, 1);
});

test('fresh, readonly, or running theme markets never submit an automatic job', async () => {
  for (const options of [{}, {autoCheck: true, readOnly: true}, {autoCheck: true, busy: true}]) {
    const f = fixture(options); f.respond(job(options.busy ? 'checking' : 'idle')); await tick();
    assert.equal(f.posts().length, 0);
  }
  const concurrent = fixture({autoCheck: true});
  concurrent.respond(job('checking')); await tick();
  assert.equal(concurrent.posts().length, 0);
});

test('failed automatic checks refresh diagnostics once without retrying the POST', async () => {
  const f = fixture({autoCheck: true}); f.respond(job('idle')); await tick();
  f.respond(job('checking', {action: 'check'}), 202); await tick();
  f.poll(); f.respond(job('failed', {action: 'check'})); await tick();
  assert.equal(f.reloads(), 1);
  assert.equal(f.posts().length, 1);
});

test('one submission reports real progress and reloads once after completion', async () => {
  const f = fixture(); f.respond(job('idle')); await tick();
  f.fire('extension-check'); f.fire('extension-check');
  assert.equal(f.posts().length, 1);
  assert.deepEqual(JSON.parse(f.posts()[0].init.body), { channel: 'stable', expected_user_id: 7 });
  assert.equal(f.posts()[0].init.headers['X-CSRF-Token'], 'csrf-token');
  f.respond(job('queued', {action:'check'}), 202); await tick();
  f.poll(); f.respond(job('downloading', { downloaded: 2048, total: 4096 })); await tick();
  assert.equal(f.get('job-progress').value, 2048);
  assert.equal(f.get('job-progress').max, 4096);
  assert.match(f.get('job-detail').textContent, /2.0 KiB \/ 4.0 KiB/);
  f.poll(); f.respond(job('succeeded')); await tick();
  assert.equal(f.reloads(), 1);
  f.fire('extension-reconnect'); await tick();
  assert.equal(f.reloads(), 1);
  assert.equal(f.posts().length, 1);
});

test('an unknown submission stays locked until reconciliation without a second POST', async () => {
  const f = fixture(); f.respond(job('idle')); await tick();
  f.fire('extension-action'); assert.equal(f.dialog.open, true);
  f.fire('extension-confirm');
  assert.deepEqual(JSON.parse(f.posts()[0].init.body), { channel: 'stable', version: '0.43.0', digest: 'digest-1', expected_version: '0.42.0', revision: 'revision-1', expected_user_id: 7 });
  f.fail(); await tick();
  assert.match(f.calls.at(-1).url, /\/job$/);
  f.fail(); await tick();
  assert.equal(f.get('extension-confirm').disabled, true);
  assert.equal(f.get('extension-action').disabled, true);
  f.fire('extension-confirm'); assert.equal(f.posts().length, 1);
  f.fire('extension-reconnect'); f.respond(job('succeeded')); await tick();
  assert.equal(f.reloads(), 1);
  assert.equal(f.posts().length, 1);
});

test('a failed poll keeps actions locked and manual reconnect observes the original job', async () => {
  const f = fixture({ busy: true }); f.respond(job('downloading')); await tick();
  f.poll(); f.fail(); await tick();
  assert.equal(f.get('extension-check').disabled, true);
  assert.equal(f.get('extension-reconnect').hidden, false);
  assert.equal([...f.timers.values()].filter(t => t.ms === 1000).length, 0);
  f.fire('extension-check'); assert.equal(f.posts().length, 0);
  f.fire('extension-reconnect'); f.respond(job('failed')); await tick();
  assert.equal(f.reloads(), 1);
});

test('cancellation is submitted once and completion is read from the server', async () => {
  const f = fixture({ busy: true }); f.respond(job('downloading')); await tick();
  f.fire('extension-cancel'); f.fire('extension-cancel');
  assert.equal(f.posts().length, 1);
  assert.deepEqual(JSON.parse(f.posts()[0].init.body), { id: 'task-1', expected_user_id: 7 });
  f.respond(job('cancelling'), 202); await tick();
  assert.equal(f.get('extension-cancel').hidden, true);
  assert.match(f.calls.at(-1).url, /\/job$/);
  f.respond(job('cancelled')); await tick();
  assert.equal(f.reloads(), 1);
});

test('permission loss stops polling and further mutations, including cancellation reconciliation', async () => {
  const f = fixture({ busy: true }); f.respond(job('downloading')); await tick();
  f.fire('extension-cancel'); f.respond({ error: { message: '权限已变化' } }, 403); await tick();
  const count = f.calls.length;
  assert.equal(f.get('extension-action').disabled, true);
  assert.equal(f.get('extension-reconnect').hidden, true);
  assert.equal(f.get('extension-cancel').hidden, true);
  f.fire('extension-reconnect'); f.fire('extension-confirm'); f.fire('extension-check'); await tick();
  assert.equal(f.calls.length, count);
  assert.equal([...f.timers.values()].filter(t => t.ms === 1000).length, 0);
});

test('malformed status and read-only pages never unlock update controls', async () => {
  const f = fixture(); f.respond({ phase: 'succeeded' }); await tick();
  assert.equal(f.get('extension-check').disabled, true);
  f.fire('extension-check'); assert.equal(f.posts().length, 0);
  const readonly = fixture({ readOnly: true }); readonly.respond(job('idle')); await tick();
  assert.equal(readonly.get('extension-action').disabled, true);
  readonly.fire('extension-check'); readonly.fire('extension-action');
  assert.equal(readonly.posts().length, 0);
});

for (const action of ['activate', 'uninstall']) test('theme ' + action + ' submits the observed active theme and revision', async () => {
  const f = fixture(); f.respond(job('idle')); await tick();
  f.get('extension-action').dataset = { extensionAction: action, slug: 'sample-theme', name: '示例主题', current: '1.0.1', revision: 'current-revision', version: '' };
  f.fire('extension-action');
  assert.equal(f.dialog.open, true);
  assert.match(f.get('confirm-detail').textContent, action === 'activate' ? /验证通过后/ : /删除此主题/);
  f.fire('extension-confirm');
  const body = JSON.parse(f.posts()[0].init.body);
  assert.equal(body.expected_active, 'starter');
  assert.equal(body.expected_version, '1.0.1');
  assert.equal(body.revision, 'current-revision');
  assert.equal(body.version, '');
  assert.match(f.posts()[0].url, new RegExp('/sample-theme/' + action + '$'));
  f.respond(job('queued',{action,slug:'sample-theme',target:''}), 202); await tick();
  f.poll(); f.respond(job('succeeded',{action,slug:'sample-theme',target:''})); await tick();
  assert.equal(f.reloads(), 1);
});

test('unknown acceptance cannot be cleared by reading the old idle job',async()=>{
  const f=fixture();f.respond(job('idle'));await tick();f.fire('extension-action');f.fire('extension-confirm');f.fail();await tick();f.respond(job('idle'));await tick();
  assert.equal(f.get('extension-confirm').disabled,true);assert.equal(f.get('extension-action').disabled,true);
  f.fire('extension-confirm');assert.equal(f.posts().length,1);f.fire('extension-reconnect');f.respond(job('succeeded'));await tick();assert.equal(f.reloads(),1);
});

test('a late pre-mutation poll cannot hide the accepted running job',async()=>{
  const f=fixture();f.respond(job('idle'));await tick();f.fire('extension-reconnect');const old=f.calls.at(-1);
  f.fire('extension-check');f.respond(job('queued',{action:'check'}));await tick();
  assert.equal(old.init.signal.aborted,true);old.resolve({ok:true,status:200,json:async()=>job('idle')});await tick();
  assert.equal(f.get('extension-job').dataset.phase,'queued');assert.equal(f.get('extension-check').disabled,true);assert.equal(f.reloads(),0);
});

test('session changes abort mutation and ignore a late receipt without reloading',async()=>{
  const f=fixture();f.respond(job('idle'));await tick();f.fire('extension-check');const write=f.calls.at(-1);f.events['chunnel:session']();
  assert.equal(write.init.signal.aborted,true);f.respond(job('queued',{action:'check'}));await tick();f.fire('extension-check');
  assert.equal(f.posts().length,1);assert.equal(f.reloads(),0);assert.equal(f.get('extension-confirm').disabled,true);
});

test('BFCache resumes with a fresh job read and ignores the frozen response',async()=>{
  const f=fixture({busy:true});f.respond(job('downloading'));await tick();f.poll();const old=f.calls.at(-1);f.events.pagehide();assert.equal(old.init.signal.aborted,true);
  f.events.pageshow({persisted:true});f.respond(job('verifying'));await tick();old.resolve({ok:true,status:200,json:async()=>job('succeeded')});await tick();
  assert.equal(f.reloads(),0);assert.equal(f.get('extension-job').dataset.phase,'verifying');
});
