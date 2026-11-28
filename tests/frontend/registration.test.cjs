'use strict';
const nodeTest = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
for (const variant of ['TypeScript', 'generated']) {
const source = variant === 'TypeScript' ? require('./helpers/typescript.cjs').compile('frontend/shared/registration.ts') : fs.readFileSync(path.join(__dirname, '../../themes/chunnel/assets/js/chunnel-registration.js'), 'utf8');
const test = (name, fn) => nodeTest(variant + ': ' + name, fn);
const tick = () => new Promise(resolve => setImmediate(resolve));

class Element {
  constructor() { Object.assign(this, { dataset: {}, attributes: new Map(), listeners: {}, selectors: new Map(), value: '', textContent: '', hidden: false, disabled: false, required: false, valid: true }); }
  addEventListener(type, fn) { this.listeners[type] = fn; }
  emit(type, extra = {}) { return this.listeners[type]?.({ target: this, preventDefault() {}, ...extra }); }
  setAttribute(k, v) { this.attributes.set(k, v); }
  removeAttribute(k) { this.attributes.delete(k); }
  hasAttribute(k) { return this.attributes.has(k); }
  querySelector(key) { return this.selectors.get(key) || null; }
  querySelectorAll(key) { return key === 'input' ? Object.values(this.elements || {}) : this.groups?.[key] || []; }
  reportValidity() { return this.valid; }
  closest() { return null; }
  focus() { this.focused = true; }
  setCustomValidity(message) { this.validationMessage = message; this.valid = !message; }
  replaceChildren(...children) { this.children = children; }
  reset() { for (const input of Object.values(this.elements)) input.value = ''; }
}
const defaultPolicy = () => ({ configured: true, registration: 'open', email_available: true, sms_available: true, recovery_available: true,
  fields: { display_name: 'required', username: 'required', email: 'optional', phone: 'hidden', password: 'required', invitation_code: 'hidden', registration_code: 'hidden' },
  email_verification: false, phone_verification: false, password_confirmation: true, oauth_bindings: [] });

function fixture({ search = '', hash = '', verification, modal = false, sessionStorage } = {}) {
  const nodes = new Map(), requests = [], events = new Map(), historyCalls = [];
  const get = key => { if (!nodes.has(key)) nodes.set(key, new Element()); return nodes.get(key); };
  const root = new Element(); root.querySelector = get;
  if (modal) root.setAttribute('data-access-modal', '');
  const form = get('[data-access-register]'), recovery = get('[data-access-request]'), reset = get('[data-access-complete]');
  for (const owner of [form, recovery, reset]) {
    owner.hidden = true;
    owner.elements = Object.fromEntries([...Object.keys(defaultPolicy().fields), 'email_code', 'phone_code', 'confirm_password'].map(name => [name, Object.assign(new Element(), { name })]));
  }
  for (const owner of [form, recovery, reset]) Object.defineProperty(owner.elements, 'namedItem', {value(name) { return this[name] || null; }});
  const rows = Object.keys(defaultPolicy().fields).map(key => { const row = new Element(); row.dataset.registrationField = key; row.selectors.set('[data-field-requirement]', new Element()); return row; });
  form.groups = { '[data-registration-field]': rows };
  const flows = ['register', 'recover', 'login'].map(accessFlow => Object.assign(new Element(), { dataset: { accessFlow } }));
  const buttons = [form, recovery, reset].map((owner, i) => Object.assign(get(['[data-registration-submit]', '[data-access-send]', '[data-access-finish]'][i]), { form: owner }));
  const sends = ['email', 'phone'].map(channel => {
    const button = new Element(); button.dataset.sendCode = channel;
    const panel = get(`[data-contact-code="${channel}"]`);
    panel.selectors.set('[data-send-code]', button); panel.selectors.set('[data-code-unavailable]', new Element());
    return button;
  });
  root.groups = { '[data-access-flow]': flows, 'button[type="submit"]': buttons, '[data-send-code]': sends };
  get('[data-code-login-channel]').value = 'email';
  const location = { search, hash, pathname: '/account/access/', origin: 'https://example.test' };
  const win = { ChunnelVerification: verification, addEventListener: (name, callback) => events.set(name, callback) };
  const documentEvents = [];
  vm.runInNewContext(source, { window: win, document: { querySelectorAll: () => [root], createElement: () => new Element(), dispatchEvent: event => documentEvents.push(event) },
    HTMLInputElement: Element, HTMLSelectElement: Element, Element, Error, URL, URLSearchParams, location, history: { replaceState(...args) { historyCalls.push(args); location.hash = ''; } },
    crypto: require('node:crypto').webcrypto, btoa, Uint8Array, AbortController, Date, sessionStorage, CustomEvent: class { constructor(type, init) { this.type = type; this.detail = init.detail; } },
    setTimeout: () => 1, clearTimeout() {}, setInterval() {},
    fetch: (url, options) => new Promise((resolve, reject) => requests.push({ url, options, resolve, reject })),
  });
  return { root, form, recovery, reset, flows, sends, buttons, get, requests, events, historyCalls, documentEvents, window: win,
    respond(index, data, status = 200, headers = {}) { requests[index].resolve({ ok: status < 400, status, headers: { get: key => headers[key] || null }, json: async () => data }); },
    async ready(policy = defaultPolicy()) { this.respond(0, policy); await tick(); },
    fill() { Object.assign(form.elements.username, { value: 'reader' }); form.elements.display_name.value = '读者'; form.elements.password.value = form.elements.confirm_password.value = 'Registration password 2026'; },
    submit(owner = form) { owner.emit('submit'); },
    body(i = requests.length - 1) { return JSON.parse(requests[i].options.body); },
    hash(value) { location.hash = value; events.get('hashchange')(); },
  };
}

test('a URL invitation remains available when referral storage rejects access', async () => {
  const storage = { getItem() { throw new Error('storage blocked'); }, setItem() { throw new Error('storage blocked'); } };
  const invited = fixture({ search: '?invite=referral-code', sessionStorage: storage });
  await invited.ready();
  assert.equal(invited.form.elements.invitation_code.value, 'referral-code');
  assert.equal(invited.form.hidden, false);
  const normal = fixture({ sessionStorage: storage }); await normal.ready();
  assert.equal(normal.form.hidden, false);
  assert.equal(normal.form.elements.invitation_code.value, '');
});

test('registration paints hidden, optional, required and conditional confirmation from the server policy', async () => {
  const f = fixture(); const p = defaultPolicy(); p.fields.password = 'optional'; p.fields.email = 'required'; p.email_verification = true;
  await f.ready(p);
  assert.equal(f.form.hidden, false);
  assert.equal(f.form.elements.email.required, true);
  assert.equal(f.form.elements.phone.disabled, true);
  assert.equal(f.form.elements.confirm_password.required, false);
  f.form.elements.password.value = 'some password'; f.form.emit('input', { target: f.form.elements.password });
  assert.equal(f.form.elements.confirm_password.required, true);
  f.form.valid = false; f.submit(); await tick(); assert.equal(f.requests.length, 1);
});

test('one accepted submission owns verification and the request; verification cancellation releases it', async () => {
  let resolveProof, rejectProof, proofs = 0;
  const f = fixture({ verification: { getToken: () => { proofs++; return new Promise((resolve, reject) => { resolveProof = resolve; rejectProof = reject; }); }, cancel() {} } });
  await f.ready(); f.fill(); f.submit(); f.submit();
  assert.equal(proofs, 1); assert.equal(f.buttons[0].disabled, true); assert.equal(f.form.elements.password.readOnly, true);
  rejectProof(new Error('验证取消')); await tick();
  assert.equal(f.requests.length, 1); assert.equal(f.buttons[0].disabled, false);
  f.submit(); resolveProof('verified'); await tick(); f.submit();
  assert.equal(f.requests.length, 2); assert.equal(f.body().verification_token, 'verified');
  f.respond(1, { ok: true, username: 'reader' }); await tick(); f.submit();
  assert.equal(f.requests.length, 2); assert.equal(f.form.hidden, true); assert.equal(f.form.elements.password.value, '');
});

test('a transport failure preserves the exact original request and reconciles it without duplicate registration', async () => {
  const f = fixture(); await f.ready(); f.fill(); f.submit();
  const first = f.body(); f.requests[1].reject(new Error('connection lost')); await tick();
  assert.equal(f.buttons[0].disabled, false); assert.equal(f.form.elements.username.readOnly, true);
  assert.equal(f.get('[data-registration-retry-note]').hidden, false);
  f.flows[2].emit('click'); assert.equal(f.get('[data-access-heading]').textContent, '创建账号');
  f.form.elements.username.value = 'changed-by-script'; f.submit();
  assert.deepEqual(f.body(), first);
  f.respond(2, { ok: true, username: 'reader' }); await tick();
  assert.equal(f.get('[data-access-success]').hidden, false);
});

test('a definitive validation error unlocks editing and the corrected submission gets a new request id', async () => {
  const f = fixture(); await f.ready(); f.fill(); f.submit(); const first = f.body();
  f.respond(1, { error: { message: '用户名已存在' } }, 409); await tick();
  assert.equal(f.form.elements.username.readOnly, false);
  f.form.elements.username.value = 'available-name'; f.submit();
  assert.equal(f.body().username, 'available-name'); assert.notEqual(f.body().request_id, first.request_id);
});

test('contact challenges are invalidated by changing the destination or switching registration to code login', async () => {
  const f = fixture(); const p = defaultPolicy(); p.email_verification = true; await f.ready(p); f.fill();
  f.form.elements.email.value = 'first@example.test'; f.form.emit('input', { target: f.form.elements.email });
  f.sends[0].emit('click'); f.sends[0].emit('click'); await tick();
  assert.equal(f.requests.length, 2); assert.equal(f.body().purpose, 'register');
  f.respond(1, { ok: true, challenge: 'email-challenge', retry_after: 60 }); await tick();
  assert.equal(f.sends[0].disabled, true);
  f.form.elements.email_code.value = '123456'; f.form.elements.email.value = 'second@example.test'; f.form.emit('input', { target: f.form.elements.email });
  assert.equal(f.form.elements.email_code.value, ''); f.submit(); assert.equal(f.body().email_challenge, '');
  f.respond(2, { error: { message: '验证码无效' } }, 422); await tick();
  f.flows[2].emit('click'); f.submit(); assert.equal(f.body().challenge, '');
});

test('new fragments ignore old replies and browser restoration releases the lock while retaining uncertain content', async () => {
  const f = fixture(); await f.ready(); f.fill(); f.submit(); const first = f.body();
  f.events.get('pageshow')({ persisted: true });
  assert.equal(f.buttons[0].disabled, false); assert.equal(f.form.elements.username.readOnly, true);
  f.respond(1, { ok: true }); await tick(); assert.equal(f.form.hidden, false);
  f.submit(); assert.deepEqual(f.body(), first);
  f.hash('#kind=recover&token=new-link');
  assert.equal(f.historyCalls.at(-1)[2], '/account/access/');
  f.respond(2, { ok: true }); await tick();
  assert.equal(f.reset.hidden, false); assert.equal(f.get('[data-access-success]').hidden, true);
});

test('restoring during the initial config request retries configuration and discards the old response', async () => {
  const f = fixture(); f.events.get('pageshow')({ persisted: true });
  assert.equal(f.requests.length, 2);
  f.respond(0, { ...defaultPolicy(), registration: 'closed' }); await tick(); assert.equal(f.form.hidden, true);
  f.respond(1, defaultPolicy()); await tick(); assert.equal(f.form.hidden, false);
});

test('an existing OAuth account is asked only for its missing binding fields', async () => {
  const f = fixture({ search: '?flow=bind' }); f.respond(0, defaultPolicy()); await tick();
  assert.equal(f.requests[1].url, '/api/v1/social/access/pending');
  f.respond(1, { provider: 'GitHub', binding_id: 'current-binding', existing: true, required: ['email'], username: 'existing', display_name: '读者' }); await tick();
  assert.equal(f.form.elements.email.required, true); assert.equal(f.form.elements.password.disabled, true); assert.equal(f.form.elements.username.disabled, true);
  assert.equal(f.get('[data-access-switch]').hidden, true);
  f.form.elements.email.value = 'bound@example.test'; f.submit();
  assert.equal(f.body().binding_id, 'current-binding');
  f.respond(2, { error: { code: 'auth.binding_expired', message: '请重新授权' } }, 401); await tick();
  assert.equal(f.form.hidden, true); assert.equal(f.get('[data-oauth-login]').hidden, false);
});

for (const variant of ['TypeScript', 'generated']) test(`${variant}: native admin login locks only uncancelled submissions and recovers on browser return`, () => {
  const form = new Element(), button = new Element(), listeners = {};
  const microtasks = [], timers = new Map(); let nextTimer = 0;
  button.innerHTML = '登录'; form.selectors.set('button[type="submit"]', button);
  const source = variant === 'TypeScript' ? require('./helpers/typescript.cjs').compile('frontend/core/admin-login.ts') : fs.readFileSync(path.join(__dirname, '../../internal/coreui/assets/js/chunnel-admin-login.js'), 'utf8');
  vm.runInNewContext(source, {
    document: { querySelector: selector => selector === '.admin-login-form' ? form : null }, window: { addEventListener: (key, fn) => listeners[key] = fn },
    queueMicrotask: fn => microtasks.push(fn),
    setTimeout: fn => { const id = ++nextTimer; timers.set(id, fn); return id; }, clearTimeout: id => timers.delete(id),
  });
  const submit = (defaultPrevented = false) => {
    const event = { target: form, defaultPrevented, preventDefault() { this.defaultPrevented = true; } };
    form.listeners.submit(event); return event;
  };
  const flushMicrotasks = () => { while (microtasks.length) microtasks.shift()(); };
  const flushTasks = () => { const callbacks = [...timers.values()]; timers.clear(); callbacks.forEach(fn => fn()); };
  submit(true); flushMicrotasks(); assert.equal(form.hasAttribute('aria-busy'), false);
  const cancelledLater = submit(); flushMicrotasks(); cancelledLater.preventDefault(); flushTasks();
  assert.equal(form.hasAttribute('aria-busy'), false); assert.equal(timers.size, 0);
  assert.equal(submit().defaultPrevented, false);
  assert.equal(submit().defaultPrevented, true, 'duplicates are blocked before deferred locking');
  flushMicrotasks(); assert.equal(form.hasAttribute('aria-busy'), false); assert.equal(button.disabled, false);
  assert.equal(button.dataset.submitState, undefined, 'native submission keeps its submitter until serialization');
  flushTasks(); assert.equal(form.attributes.get('aria-busy'), 'true');
  assert.equal(button.dataset.submitState, 'pending'); assert.equal(button.textContent, '正在登录…');
  listeners.pageshow({ persisted: false }); assert.equal(submit().defaultPrevented, true);
  form.dataset.verificationSubmitting = 'true'; form.dataset.verificationPending = 'true';
  listeners.pageshow({ persisted: true });
  assert.equal(form.attributes.get('aria-busy'), 'false'); assert.equal(button.innerHTML, '登录');
  assert.equal(button.hasAttribute('aria-disabled'), false); assert.equal(button.dataset.submitState, undefined);
  assert.equal(form.dataset.verificationSubmitting, undefined); assert.equal(form.dataset.verificationPending, undefined);
  assert.equal(submit().defaultPrevented, false); flushMicrotasks();
  listeners.pageshow({ persisted: true }); assert.equal(timers.size, 0, 'restoration retires a pending visual lock');
});

test('phone prefix and local number produce the same canonical target for codes and registration', async () => {
  const f = fixture(); const p = defaultPolicy(); p.fields.phone = 'required'; p.phone_verification = true;
  p.phone_region = { selectable: true, default_code: '+86', codes: ['+86', '+1'] };
  await f.ready(p); f.fill();
  assert.equal(f.get('[data-phone-region]').value, '+86');
  f.form.elements.phone.value = '138 0013 8000'; f.form.emit('input', { target: f.form.elements.phone });
  f.sends[1].emit('click'); assert.equal(f.body().target, '+8613800138000');
  f.respond(1, { ok: true, challenge: 'phone-challenge', retry_after: 60 }); await tick();
  f.form.elements.phone_code.value = '123456'; f.submit();
  assert.equal(f.body().phone, '+8613800138000'); assert.equal(f.body().phone_challenge, 'phone-challenge');
  f.respond(2, { error: { message: '示例错误' } }, 422); await tick();
  f.get('[data-phone-region]').value = '+1'; f.get('[data-phone-region]').emit('change');
  assert.equal(f.form.elements.phone_code.value, '');
  f.submit(); assert.equal(f.body().phone_challenge, ''); assert.equal(f.body().phone, '+113800138000');
});

test('fixed phone region rejects a foreign prefix but does not lock out existing verified code-login users', async () => {
  const f = fixture(); const p = defaultPolicy(); p.fields.phone = 'required';
  p.phone_region = { selectable: false, default_code: '+86', codes: ['+86', '+1'] };
  await f.ready(p); f.fill(); f.form.elements.phone.value = '+12025550123';
  f.form.emit('input', { target: f.form.elements.phone });
  assert.equal(f.get('[data-phone-region]').hidden, true);
  assert.equal(f.form.elements.phone.valid, false);
  f.sends[1].emit('click'); assert.equal(f.requests.length, 1);
  f.flows[2].emit('click'); f.get('[data-code-login-channel]').value = 'phone'; f.get('[data-code-login-channel]').emit('change');
  assert.equal(f.form.elements.phone.valid, true);
  f.sends[1].emit('click'); assert.equal(f.body().target, '+12025550123'); assert.equal(f.body().purpose, 'login');
});

test('modal flow changes keep page location intact and preserve uncertain submissions when reopened', async () => {
  const f = fixture({ modal: true, search: '?filter=mine', hash: '#reading' }); await f.ready(); f.fill();
  f.flows[1].emit('click'); assert.equal(f.recovery.hidden, false); assert.equal(f.historyCalls.length, 0);
  await f.root.chunnelRegistration.open('register'); f.submit(); const first = f.body();
  f.requests[1].reject(new Error('lost response')); await tick();
  assert.equal(f.root.chunnelRegistration.canLeave(), false);
  await f.root.chunnelRegistration.open('login');
  assert.equal(f.get('[data-access-heading]').textContent, '创建账号');
  f.submit(); assert.deepEqual(f.body(), first);
  f.respond(2, { ok: true, username: 'reader' }); await tick();
  assert.equal(f.root.chunnelRegistration.canLeave(), true);
  assert.equal(f.documentEvents.at(-1).type, 'chunnel:account-authenticated');
  assert.equal(f.documentEvents.at(-1).detail.root, f.root);
});

test('mounting a second form keeps controllers, payloads and busy state independent', async () => {
  const first = fixture(); await first.ready(); first.fill();
  const second = fixture({ modal: true }); await second.ready();
  delete second.root.chunnelRegistration;
  const controller = first.window.ChunnelRegistration.mount(second.root);
  first.respond(1, defaultPolicy()); await tick();
  assert.notEqual(controller, first.root.chunnelRegistration);
  assert.equal(first.window.ChunnelRegistration.mount(second.root), controller);
  second.fill(); second.form.elements.username.value = 'second'; second.submit();
  assert.equal(first.body().username, 'second'); assert.equal(first.buttons[0].disabled, false);
  first.submit(); assert.equal(first.body().username, 'reader'); assert.equal(second.buttons[0].disabled, true);
});

test('disabled code login hides its tab and rejects direct URLs and programmatic switching', async () => {
  const f = fixture({ search: '?flow=login' });
  await f.ready({ ...defaultPolicy(), code_login_enabled: false });
  assert.equal(f.flows[2].hidden, true);
  assert.equal(f.get('[data-access-heading]').textContent, '创建账号');
  assert.match(f.get('[data-access-result]').textContent, /已关闭验证码登录/);
  assert.equal(f.historyCalls.at(-1)[2], '/account/access/');
  await f.root.chunnelRegistration.open('login');
  f.flows[2].emit('click');
  assert.equal(f.get('[data-access-heading]').textContent, '创建账号');
  assert.equal(f.requests.length, 1);
});

test('turning code login off while its form is open hides the stale flow and releases controls', async () => {
  const f = fixture({ search: '?flow=login' }); await f.ready();
  f.form.elements.email.value = 'verified@example.test';
  f.form.emit('input', { target: f.form.elements.email });
  f.sends[0].emit('click');
  assert.equal(f.body().purpose, 'login');
  f.respond(1, { error: { code: 'auth.code_login_disabled', message: '本站已关闭验证码登录' } }, 403);
  await tick();
  assert.equal(f.flows[2].hidden, true);
  assert.equal(f.get('[data-access-heading]').textContent, '创建账号');
  assert.equal(f.root.chunnelRegistration.canLeave(), true);
  assert.equal(f.form.elements.password.disabled, false);
});

test('malformed policy leaves registration unavailable and sends no write', async () => {
  const f=fixture(); await f.ready({...defaultPolicy(),fields:{}}); assert.equal(f.form.hidden,true);
  f.submit(); assert.equal(f.requests.length,1); assert.match(f.get('[data-access-result]').textContent,/规则格式错误/);
});

test('unconfirmed code delivery cannot create a usable challenge', async () => {
  const f=fixture({search:'?flow=login'}); await f.ready(); f.form.elements.email.value='one@example.test'; f.form.emit('input',{target:f.form.elements.email});
  f.sends[0].emit('click'); f.respond(1,{ok:true,challenge:'',retry_after:60}); await tick();
  assert.match(f.get('[data-access-result]').textContent,/尚未确认/); f.form.elements.email_code.value='123456'; f.submit(); assert.equal(f.body().challenge,'');
});

}
