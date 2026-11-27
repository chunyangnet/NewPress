const nodeTest = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const { compile, loadModule } = require('./helpers/typescript.cjs');
const runtime = require('../../internal/coreui/assets/js/newpress-client.js');
const source = loadModule('frontend/shared/newpress-client.ts', { AbortController, DOMException, FormData, setTimeout, clearTimeout, TypeError });

nodeTest('client global adapter preserves the browser and CommonJS v1 entry points', () => {
  const window = {};
  vm.runInNewContext(compile('frontend/shared/newpress-client-global.ts'), { window });
  assert.equal(window.NewPressClient.version, 1);
  assert.equal(typeof window.NewPressClient.create, 'function');
  assert.equal(typeof window.NewPressClient.APIError, 'function');
  assert.equal(runtime.version, 1);
  assert.equal(globalThis.NewPressClient, runtime);
  assert.equal(Object.isFrozen(window.NewPressClient), true);
  assert.equal(Object.isFrozen(runtime), true);
});

for (const [label, api] of [['TypeScript', source], ['generated CommonJS', runtime]]) {
  const test = (name, fn) => nodeTest(`${label}: ${name}`, fn);
  const { create, APIError } = api;
  const guest = () => ({ authenticated: false, configured: true });
  const member = (id = 7) => ({ authenticated: true, configured: true, id, name: `User ${id}`, csrf_token: `csrf-${id}`, can_admin: false, can_publish: true });

  function fixture(options = {}) {
    const calls = [];
    const client = create({ fetch(path, options) {
      return new Promise(resolve => calls.push({ path, options, finish(data, status = 200, headers = {}) {
        resolve({ ok: status >= 200 && status < 300, status, headers: { get: name => headers[name] ?? null }, json: async () => data });
      } }));
    }, ...options });
    return { client, calls };
  }

  test('client runs without a document and supplies same-origin credentials and current CSRF', async () => {
    const { client, calls } = fixture();
    client.setSession({ id: 1, authenticated: true, csrf_token: 'a' });
    const pending = client.request('/api/v1/test', { method: 'POST', body: { value: 1 } });
    assert.equal(calls[0].options.credentials, 'same-origin');
    assert.equal(calls[0].options.redirect, 'error');
    assert.equal(calls[0].options.headers['X-CSRF-Token'], 'a');
    assert.equal(calls[0].options.body, '{"value":1}');
    calls[0].finish({ ok: true }); assert.equal((await pending).ok, true);
    for (const target of ['https://outside.test/api', '//outside.test/api', '/\\outside.test/api', '/\n/outside.test/api', '/\r/outside.test/api', '/\t/outside.test/api']) {
      await assert.rejects(client.request(target), /same-origin/);
    }
    assert.equal(calls.length, 1);
  });

  test('session reads coalesce and an older guest response cannot replace a login', async () => {
    const { client, calls } = fixture();
    const old = client.getSession(), follower = client.getSession();
    assert.equal(calls.length, 1);
    const login = client.getSession(true);
    calls[1].finish(member()); await login;
    calls[0].finish(guest()); await Promise.all([old, follower]);
    assert.equal(client.snapshot().id, 7);
  });

  test('logout cancels owned requests and prevents stale response application', async () => {
    const { client, calls } = fixture();
    client.setSession({ id: 1, authenticated: true });
    const read = client.request('/api/v1/private');
    const oldSession = client.getSession(true);
    client.setSession({ authenticated: false });
    assert.equal(calls[0].options.signal.aborted, true);
    calls[0].finish({ private: true }); calls[1].finish({ id: 1, authenticated: true });
    await assert.rejects(read, { name: 'AbortError' }); await oldSession;
    assert.equal(client.snapshot().authenticated, false);
  });

  test('write coalescing and API conflict errors preserve caller recovery decisions', async () => {
    const { client, calls } = fixture();
    const action = () => client.request('/api/v1/test', { method: 'POST', body: { expected_version: 1 } });
    const first = client.once('save', action), second = client.once('save', action);
    assert.equal(first, second); await Promise.resolve();
    assert.equal(calls.length, 1);
    calls[0].finish({ error: { code: 'version_conflict', message: 'Changed', details: { version: 2 } } }, 409);
    await assert.rejects(first, error => error.status === 409 && error.code === 'version_conflict' && error.details.version === 2);
    const retry = client.once('save', action); await Promise.resolve();
    calls[1].finish({ saved: true }); assert.equal((await retry).saved, true);
  });

  test('caller cancellation survives completion of a delayed response', async () => {
    const { client, calls } = fixture(), controller = new AbortController();
    const pending = client.request('/api/v1/test', { signal: controller.signal });
    controller.abort(); calls[0].finish({ ok: true });
    await assert.rejects(pending, { name: 'AbortError' });
    client.dispose();
  });

  test('identity changes discard queued writes and release coalescing keys for the new owner', async () => {
    const { client, calls } = fixture();
    client.setSession({ id: 1, authenticated: true, csrf_token: 'old' });
    const old = client.once('save', () => client.request('/api/v1/test', { method: 'POST', body: { text: 'old draft' } }));
    client.setSession({ id: 2, authenticated: true, csrf_token: 'new' });
    const fresh = client.once('save', () => client.request('/api/v1/test', { method: 'POST', body: { text: 'new draft' } }));
    await assert.rejects(old, { name: 'AbortError' });
    assert.equal(calls.length, 1);
    assert.equal(calls[0].options.headers['X-CSRF-Token'], 'new');
    assert.equal(calls[0].options.body, '{"text":"new draft"}');
    calls[0].finish({ saved: true }); await fresh;
  });

  test('JSON, multipart, custom headers and anonymous writes keep their transport contracts', async () => {
    const { client, calls } = fixture();
    client.setSession(member());
    const form = new FormData(); form.append('name', 'example');
    const upload = client.request('/upload', { method: 'post', body: form, headers: { 'X-Request-ID': 'upload-1' } });
    assert.equal(calls[0].options.method, 'POST');
    assert.equal(calls[0].options.body, form);
    assert.equal(calls[0].options.headers['Content-Type'], undefined);
    assert.equal(calls[0].options.headers['X-CSRF-Token'], 'csrf-7');
    assert.equal(calls[0].options.headers['X-Request-ID'], 'upload-1');
    assert.equal(calls[0].options.cache, 'no-store');
    calls[0].finish({ uploaded: true }); await upload;
    for (const options of [{ method: 'GET' }, { method: 'HEAD' }, { method: 'POST', anonymous: true, body: { username: 'owner' } }]) {
      const pending = client.request('/test', options), call = calls.at(-1);
      assert.equal(call.options.headers['X-CSRF-Token'], undefined);
      assert.equal(call.options.headers.Accept, 'application/json');
      call.finish({ ok: true }); await pending;
    }
  });

  test('a verified session is cached and subscribers and snapshots cannot mutate its fields', async () => {
    const { client, calls } = fixture();
    let notifications = 0;
    const unsubscribe = client.subscribe(next => { notifications++; next.id = 99; });
    const pending = client.getSession(); calls[0].finish(member());
    const session = await pending;
    assert.deepEqual({ ...session }, member());
    assert.equal(await client.getSession(), session);
    const copy = client.snapshot(); copy.csrf_token = 'changed';
    assert.equal(client.snapshot().csrf_token, 'csrf-7');
    assert.equal(client.snapshot().id, 7);
    assert.equal(calls.length, 1);
    assert.equal(notifications, 1);
    unsubscribe(); client.setSession(guest());
    assert.equal(notifications, 1);
    assert.equal(Object.isFrozen(client), true);
  });

  test('invalid session responses preserve the current identity, pending writes and retry path', async () => {
    const invalid = [
      {}, { authenticated: false }, { configured: true },
      { ...guest(), authenticated: 'false' }, { ...guest(), configured: 1 },
      ...[0, -1, 1.5, '7', null, Number.MAX_SAFE_INTEGER + 1].map(id => ({ ...member(), id })),
      { ...member(), name: {} }, { ...member(), csrf_token: 7 },
      { ...member(), can_admin: 'true' }, { ...member(), can_publish: 1 },
    ];
    for (const value of invalid) {
      const { client, calls } = fixture();
      const previous = member(1); client.setSession(previous);
      let notifications = 0; client.subscribe(() => notifications++);
      const write = client.request('/save', { method: 'POST', body: { draft: 'keep' } });
      const pending = client.getSession(true); calls[1].finish(value);
      await assert.rejects(pending, /结果未确认/);
      assert.deepEqual({ ...client.snapshot() }, previous);
      assert.equal(notifications, 0);
      assert.equal(calls[0].options.signal.aborted, false);
      calls[0].finish({ saved: true }); await write;
      const retry = client.getSession(true); calls[2].finish(guest());
      assert.equal((await retry).authenticated, false);
    }
  });

  test('successful responses must be objects without inventing endpoint-specific field types', async () => {
    const { client, calls } = fixture();
    for (const value of [null, [], 'ok', 1, true]) {
      const pending = client.request('/save', { method: 'POST', body: { draft: 'keep' } });
      calls.at(-1).finish(value);
      await assert.rejects(pending, /结果未确认/);
    }
    const pending = client.request('/custom');
    const payload = { values: [1, null, { custom: true }] };
    calls.at(-1).finish(payload); assert.equal(await pending, payload);
  });

  test('malformed error fields cannot become a non-string message or code', async () => {
    const { client, calls } = fixture();
    for (const data of [null, [], { error: { message: {}, code: 7 } }, { error: ['bad'] }]) {
      const pending = client.request('/save'); calls.at(-1).finish(data, 503, { 'Retry-After': '8' });
      await assert.rejects(pending, error => error instanceof APIError && error.status === 503
        && error.message === '服务暂时不可用，请稍后重试' && error.code === '' && error.retryAfter === 8);
    }
    const pending = client.request('/save'); calls.at(-1).finish({ error: 'legacy_error' }, 400, { 'Retry-After': '-1' });
    await assert.rejects(pending, error => error.code === 'legacy_error' && error.retryAfter === 0);
  });

  test('a current 401 clears its own session and preserves the original APIError', async () => {
    let notified;
    const { client, calls } = fixture({ onUnauthorized: error => { notified = error; } });
    client.setSession(member());
    const pending = client.request('/private');
    calls[0].finish({ error: { code: 'auth.expired', message: 'Expired' } }, 401);
    await assert.rejects(pending, error => error === notified && error instanceof APIError && error.status === 401 && error.code === 'auth.expired');
    assert.equal(client.snapshot().authenticated, false);
    assert.equal(calls[0].options.signal.aborted, true);
  });

  test('an anonymous login failure does not clear an existing identity', async () => {
    let notified = false;
    const { client, calls } = fixture({ onUnauthorized: () => { notified = true; } });
    client.setSession(member());
    const pending = client.request('/login', { method: 'POST', anonymous: true, body: { username: 'owner' } });
    calls[0].finish({ error: 'invalid_credentials' }, 401);
    await assert.rejects(pending, { status: 401 });
    assert.equal(client.snapshot().id, 7);
    assert.equal(notified, false);
  });

  test('obsolete session completion cannot clear a replacement discovery request', async () => {
    const { client, calls } = fixture();
    const first = client.getSession(), second = client.getSession(true);
    calls[0].finish({ malformed: true }); await first;
    const follower = client.getSession(); assert.equal(calls.length, 2);
    calls[1].finish(member(2));
    const results = await Promise.all([second, follower]);
    assert.equal(results[0].id, 2); assert.equal(results[1].id, 2);
  });

  test('external session and epoch providers retain CSRF and stale-response ownership', async () => {
    let session = member(1), epoch = 0, notified = false;
    const { client, calls } = fixture({ session: () => session, epoch: () => epoch, onUnauthorized: () => { notified = true; } });
    const old = client.request('/write', { method: 'POST' });
    assert.equal(calls[0].options.headers['X-CSRF-Token'], 'csrf-1');
    session = member(2); epoch++;
    calls[0].finish({ error: { message: 'Expired' } }, 401);
    await assert.rejects(old, { name: 'AbortError' }); assert.equal(notified, false);
    const fresh = client.request('/write', { method: 'POST' });
    assert.equal(calls[1].options.headers['X-CSRF-Token'], 'csrf-2');
    calls[1].finish({ saved: true }); await fresh;
  });

  test('token rotation cancels owned reads and pre-aborted callers cannot apply responses', async () => {
    const { client, calls } = fixture();
    client.setSession(member());
    const read = client.request('/private');
    client.setSession({ ...member(), csrf_token: 'rotated' });
    calls[0].finish({ private: true }); await assert.rejects(read, { name: 'AbortError' });
    const controller = new AbortController(); controller.abort();
    const aborted = client.request('/private', { signal: controller.signal });
    assert.equal(calls[1].options.signal.aborted, true);
    calls[1].finish({ private: true }); await assert.rejects(aborted, { name: 'AbortError' });
  });

  test('dispose prevents delayed discovery, active reads and queued writes from applying', async () => {
    const { client, calls } = fixture();
    let notifications = 0, wrote = false;
    client.subscribe(() => notifications++);
    const session = client.getSession();
    const read = client.request('/private');
    const write = client.once('save', () => { wrote = true; });
    client.dispose();
    calls[0].finish(member()); calls[1].finish({ private: true });
    await assert.rejects(write, { name: 'AbortError' });
    await assert.rejects(read, { name: 'AbortError' });
    assert.equal((await session).authenticated, false);
    assert.equal(client.snapshot(), null);
    assert.equal(notifications, 0); assert.equal(wrote, false);
  });

  test('an older rejected write cannot evict the new owner\'s coalesced write', async () => {
    const { client } = fixture();
    client.setSession(member(1));
    const old = client.once('save', () => 'old');
    client.setSession(member(2));
    let release;
    const fresh = client.once('save', () => new Promise(resolve => { release = resolve; }));
    await assert.rejects(old, { name: 'AbortError' });
    assert.equal(client.once('save', () => 'wrong'), fresh);
    release('new'); assert.equal(await fresh, 'new');
    assert.equal(await client.once('save', () => 'next'), 'next');
  });
}
