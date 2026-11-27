const test = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = readFileSync(path.join(__dirname, '../../internal/coreui/assets/js/chunnel-updates.js'), 'utf8');
const tick = () => new Promise(resolve => setImmediate(resolve));
const defaults = { auto_check: true, check_interval_minutes: 360, observation_seconds: 120,
  customized: false, launcher_applies_window: true, revision: '1'.repeat(64) };
const checked = revision => ({ current: '1.0.0', managed: true, configured: true, trusted_publisher: true,
  policy: defaults, revision, available: revision ? { version: '1.1.0' } : undefined, state: { phase: 'idle' } });

function fixture(ticket = '', readOnly = false) {
  const calls = [], timers = [], nodes = new Map();
  const node = () => ({ dataset: {}, checked: false, events: {}, querySelector: () => null,
    setAttribute(key, value) { this[key] = value; },
    addEventListener(type, fn) { this.events[type] = fn; }, append() {}, replaceChildren() {} });
  const number = (min, max) => ({ value: '', get valueAsNumber() { return this.value === '' ? NaN : Number(this.value); },
    get validity() { return { valid: this.valueAsNumber >= min && this.valueAsNumber <= max }; } });
  function get(name) { if (!nodes.has(name)) nodes.set(name, node()); return nodes.get(name); }
  get('source').elements = { official: node(), token: {}, clear_token: {},
    owner: { value: 'private-owner' }, repo: { value: 'private-repo' }, ref: { value: 'main' }, manifest: { value: 'updates/stable.json' } };
  const form = get('policy-form'), bar = get('policy-bar');
  form.elements = { auto_check: node(), check_interval_minutes: number(1, 10080), observation_seconds: number(2, 600) };
  form.querySelector = selector => selector === '[data-admin-savebar]' ? bar : null;
  bar.querySelector = selector => selector === '[data-admin-discard]' ? get('discard') : null;
  const window = { events: {}, addEventListener(name, fn) { this.events[name] = fn; },
    ChunnelAdminUI: { savebar: () => null, paintSavebar: (node, state) => { node.state = state; } } };
  const root = { dataset: { readonly: String(readOnly), csrf: 'test-csrf' }, querySelectorAll: () => [], querySelector(selector) {
    const name = /^\[data-update-([\w-]+)\]$/.exec(selector)?.[1]; return name ? get(name) : null;
  } };
  vm.runInNewContext(source, { window, document: { querySelector: () => root, createElement: node }, AbortSignal,
    sessionStorage: { getItem: () => ticket, setItem(_, value) { ticket = value; }, removeItem() { ticket = ''; } },
    setTimeout(fn) { timers.push(fn); }, fetch(url, init) { return new Promise(resolve => calls.push({ url, init, resolve })); } });
  const respond = async (data, status = 200, call = calls.at(-1)) => {
    call.resolve({ ok: status < 400, status, json: async () => data }); await tick();
  };
  return { get, calls, respond, form, bar, window, fields: form.elements,
    async ready(policy = defaults) { await respond({ ...checked(), policy }); await respond({ ...checked(), policy }); },
    input(name, value) { const target = form.elements[name]; target[name === 'auto_check' ? 'checked' : 'value'] = value; form.events.input({ target }); },
    submit() { form.events.submit({ preventDefault() {} }); },
    poll() { assert.ok(timers.length); timers.shift()(); },
    confirm() { get('backup').checked = true; get('backup').events.change(); } };
}

test('official public source submission excludes all private repository fields and credentials', async () => {
  const f = fixture(); await f.ready();
  const sourceForm = f.get('source');
  sourceForm.elements.token.value = 'private-token';
  sourceForm.elements.clear_token.checked = false;
  sourceForm.elements.official.checked = true;
  sourceForm.elements.official.events.change();
  assert.equal(sourceForm.elements.token.disabled, true);
  assert.equal(sourceForm.elements.owner.disabled, true);
  sourceForm.events.submit({ preventDefault() {} });
  const call = f.calls.at(-1);
  assert.ok(call.url.endsWith('/source'));
  assert.deepEqual(JSON.parse(call.init.body), { official: true });
  await f.respond({ saved: true });
  await f.respond({ ...checked(), source: { official: true } });
  assert.equal(f.get('source-label').textContent, 'GitHub / Gitee 公开双源');
});

test('backup confirmation follows the exact release revision, including withdrawal and republication', async () => {
  const a = 'a'.repeat(64), b = 'b'.repeat(64), f = fixture();
  await f.respond(checked(a));
  f.confirm();
  await f.respond(checked(a));
  assert.equal(f.get('start').disabled, false);
  f.poll(); await f.respond(checked(b));
  assert.equal(f.get('backup').checked, false);
  assert.equal(f.get('start').disabled, true);
  f.get('start').events.click();
  assert.ok(f.calls.every(call => call.init.method === 'GET'));
  f.confirm();
  f.poll(); await f.respond(checked(undefined));
  assert.equal(f.get('backup').checked, false);
  assert.equal(f.get('install').hidden, true);
  f.poll(); await f.respond(checked(b));
  assert.equal(f.get('start').disabled, true);
  f.confirm(); f.get('start').events.click();
  assert.deepEqual(JSON.parse(f.calls.at(-1).init.body), { version: '1.1.0', revision: b, backup_confirmed: true });
});

test('the progress receipt shows the restored version after a post-commit rollback', async () => {
  const f = fixture('t'.repeat(64));
  const completed = { phase: 'completed', previous: '1.0.0', target: '1.1.0', protected_until: new Date(Date.now() + 60000).toISOString() };
  await f.respond({ ...checked(undefined), current: '1.1.0', state: completed });
  assert.ok(f.calls.at(-1).url.endsWith('/progress'));
  await f.respond(completed);
  assert.equal(f.get('current').textContent, '1.1.0');
  assert.equal(f.get('check').disabled, true);
  f.poll(); await f.respond({ phase: 'rolled_back', previous: '1.0.0', target: '1.1.0' });
  assert.equal(f.get('current').textContent, '1.0.0');
  assert.equal(f.get('check').disabled, false);
});

test('a disabled schedule retains its interval and stale polling cannot undo a save', async () => {
  const f = fixture(); await f.ready();
  f.input('check_interval_minutes', 15); f.input('auto_check', false);
  assert.equal(f.fields.check_interval_minutes.disabled, true);
  f.poll(); const inFlight = f.calls.at(-1);
  f.submit(); const write = f.calls.at(-1);
  assert.ok(write.url.endsWith('/policy')); assert.equal(write.init.method, 'PUT');
  assert.equal(write.init.headers['X-CSRF-Token'], 'test-csrf');
  assert.deepEqual(JSON.parse(write.init.body), { revision: defaults.revision, auto_check: false, check_interval_minutes: 15, observation_seconds: 120 });
  const saved = { ...defaults, auto_check: false, check_interval_minutes: 15, customized: true, revision: '2'.repeat(64) };
  await f.respond(saved); await f.respond({ ...checked(), policy: saved }); await f.respond(checked(), 200, inFlight);
  assert.equal(f.get('policy-origin').textContent, '站点自定义');
  assert.equal(f.bar.state.dirty, false);
  f.input('auto_check', true);
  assert.equal(f.fields.check_interval_minutes.disabled, false); assert.equal(f.fields.check_interval_minutes.value, 15);
  f.input('check_interval_minutes', ''); f.input('auto_check', false);
  assert.equal(f.fields.check_interval_minutes.value, 15);
  f.input('observation_seconds', 28); f.get('discard').events.click();
  assert.equal(f.fields.observation_seconds.value, 120); assert.equal(f.bar.state.dirty, false);
  f.get('policy-defaults').events.click();
  assert.deepEqual(JSON.parse(f.calls.at(-1).init.body), { revision: saved.revision, use_defaults: true });
  await f.respond(defaults); await f.respond(checked());
  assert.equal(f.get('policy-defaults').hidden, true); assert.equal(f.get('policy-origin').textContent, '部署默认');
});

test('polls preserve an edited policy, and a conflicting writer requires an explicit reload', async () => {
  const f = fixture(); await f.ready();
  f.input('observation_seconds', 55);
  f.poll(); await f.respond(checked()); assert.equal(f.fields.observation_seconds.value, 55);
  f.submit(); await f.respond({ error: { code: 'updates.policy_changed', message: '策略已变更' } }, 409);
  const latest = { ...defaults, customized: true, observation_seconds: 80, revision: '3'.repeat(64) };
  await f.respond({ ...checked(), policy: latest });
  assert.equal(f.fields.observation_seconds.value, 55); assert.equal(f.get('policy-conflict').hidden, false);
  const writes = f.calls.length; f.submit(); f.get('policy-defaults').events.click(); assert.equal(f.calls.length, writes);
  f.get('policy-reload').events.click(); await f.respond({ ...checked(), policy: latest }); await f.respond({ ...checked(), policy: latest });
  assert.equal(f.fields.observation_seconds.value, 80); assert.equal(f.get('policy-conflict').hidden, true);
  assert.equal(f.bar.state.dirty, false);
});

test('read-only access and active protection disable policy mutations', async () => {
  for (const readOnly of [true, false]) {
    const f = fixture('', readOnly); await f.ready({ ...defaults, launcher_applies_window: false });
    assert.equal(f.get('policy-launcher').hidden, false);
    if (!readOnly) {
      f.poll(); await f.respond({ ...checked(), state: { phase: 'completed', protected_until: new Date(Date.now() + 60000).toISOString() } });
    }
    assert.equal(f.get('policy-fields').disabled, true);
    f.input('observation_seconds', 45); f.submit(); f.get('policy-defaults').events.click();
    assert.ok(f.calls.every(c => c.init.method === 'GET'));
  }
});

test('an expired session preserves the draft and disables writes', async () => {
  const f = fixture(); await f.ready();
  f.input('observation_seconds', 45);
  f.poll(); const inFlight = f.calls.at(-1);
  await f.respond({ error: { message: '请重新登录' } }, 401);
  assert.equal(f.get('policy-fields').disabled, true); assert.equal(f.get('login').hidden, false);
  f.submit(); f.get('check').events.click();
  assert.equal(f.calls.at(-1), inFlight); assert.equal(f.fields.observation_seconds.value, 45);
});

test('a valid progress receipt keeps polling after the admin session expires', async () => {
  const f = fixture('t'.repeat(64));
  await f.respond({ error: { message: '请重新登录' } }, 401);
  assert.ok(f.calls.at(-1).url.endsWith('/progress'));
  await f.respond({ phase: 'observing', target: '1.1.0' });
  f.poll(); assert.ok(f.calls.at(-1).url.endsWith('/progress'));
  await f.respond({ phase: 'rolled_back', previous: '1.0.0' });
  assert.equal(f.get('current').textContent, '1.0.0');
  assert.equal(f.get('login').hidden, false); assert.equal(f.get('policy-fields').disabled, true);
});

test('a policy write returning 401 keeps the session locked when its following refresh succeeds', async () => {
  const f = fixture(); await f.ready(); f.input('observation_seconds', 45); f.submit();
  await f.respond({ error: { message: '请重新登录' } }, 401);
  await f.respond(checked());
  assert.equal(f.get('policy-fields').disabled, true); assert.equal(f.get('login').hidden, false);
  assert.equal(f.fields.observation_seconds.value, 45); assert.equal(f.get('connection').textContent, '需要重新登录');
});

test('navigation warns about unsaved policy changes and a successful save clears the warning', async () => {
  const f = fixture(); await f.ready();
  const leaving = () => { let warned = false; f.window.events.beforeunload({ preventDefault() { warned = true; } }); return warned; };
  assert.equal(leaving(), false);
  f.input('observation_seconds', 33); assert.equal(leaving(), true);
  f.submit(); assert.equal(f.form['aria-busy'], 'true'); assert.equal(leaving(), true);
  const saved = { ...defaults, customized: true, observation_seconds: 33, revision: '4'.repeat(64) };
  await f.respond(saved); await f.respond({ ...checked(), policy: saved });
  assert.equal(f.form['aria-busy'], 'false'); assert.equal(leaving(), false);
});
