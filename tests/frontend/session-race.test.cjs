const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { compile, loadModule } = require('./helpers/typescript.cjs');
function fixture(initial = null) {
  const requests = [], applied = [];
  const state = {session: initial, epoch: 0};
  const applySession = session => { if ((state.session?.id||0)!==(session.id||0)) state.epoch++; state.session=session;applied.push(session); };
  const get = loadModule('frontend/shared/interaction-session.ts').createSocialSession(state,()=>new Promise(resolve=>requests.push(resolve)),applySession);
  return {state,requests,applied,get,change:applySession};
}

test('a login refresh supersedes a guest read already in flight', async () => {
  const f = fixture();
  const old = f.get(); const login = f.get(true);
  assert.equal(f.requests.length, 2);
  const owner = {id: 1, authenticated: true};
  f.requests[1](owner); await login;
  f.requests[0]({authenticated: false}); await old;
  assert.deepEqual(JSON.parse(JSON.stringify(f.state.session)), {...owner,configured:true});
  assert.deepEqual(JSON.parse(JSON.stringify(f.applied)), [{...owner,configured:true}]);
});

test('a delayed authenticated read cannot restore an identity after logout', async () => {
  const f = fixture({id: 1, authenticated: true});
  const old = f.get(true);
  const guest = {authenticated: false};
  f.change(guest);
  f.requests[0]({id: 1, authenticated: true});
  assert.equal(await old, guest);
  assert.equal(f.state.session, guest);
});

test('ordinary session discovery remains coalesced', async () => {
  const f = fixture();
  const first = f.get(); const second = f.get();
  assert.equal(f.requests.length, 1);
  f.requests[0]({id: 2, authenticated: true});
  await Promise.all([first, second]);
  assert.equal(f.applied.length, 1);
});

function logoutFixture() {
  let restore, current;
  const writes = [], applied = [], removed = [], broadcast = [];
  const menu = { open: true }, state = { epoch: 0 };
  const restored = new Promise(resolve => { restore = session => { current = session; resolve(session); }; });
  const logout = loadModule('frontend/shared/interaction-session.ts').createSocialLogout({
    state,
    getSession: () => restored,
    api: async (url, options) => { writes.push({ url, method: options.method, token: current.csrf_token }); return {ok:true}; },
    apply: session => applied.push(session),
    clearDrafts: user => { if (user === 7) removed.push('chunnel.private.7.draft'); },
    completed: () => { menu.open = false; broadcast.push({type:'session'}); },
  });
  const pending = logout();
  return { restore, writes, applied, removed, broadcast, menu, done: () => pending };
}

test('first-interaction logout waits for the restored session and its CSRF token', async () => {
  const f = logoutFixture();
  await Promise.resolve();
  assert.equal(f.writes.length, 0);
  assert.equal(f.applied.length, 0);
  f.restore({ id: 7, authenticated: true, csrf_token: 'restored-token' });
  await f.done();
  assert.deepEqual(f.writes, [{ url: '/api/v1/social/logout', method: 'POST', token: 'restored-token' }]);
  assert.equal(f.applied[0].authenticated, false);
  assert.equal(f.menu.open, false);
  assert.deepEqual(f.removed, ['chunnel.private.7.draft']);
  assert.equal(f.broadcast.length, 1);
});

test('a session that expired during enhancement completes logout without a rejected write', async () => {
  const f = logoutFixture();
  f.restore({ authenticated: false });
  await f.done();
  assert.equal(f.writes.length, 0);
  assert.equal(f.applied[0].authenticated, false);
  assert.equal(f.menu.open, false);
});

test('completion of an obsolete read cannot clear the replacement request', async () => {
  const f = fixture();
  const old = f.get(); const replacement = f.get(true);
  f.requests[0]({authenticated: false}); await old;
  const follower = f.get();
  assert.equal(f.requests.length, 2);
  f.requests[1]({id: 3, authenticated: true});
  await Promise.all([replacement, follower]);
  assert.equal(f.state.session.id, 3);
});

for (const [entry, clientCode] of [
  ['TypeScript', compile('frontend/shared/newpress-client-global.ts')],
  ['generated browser', fs.readFileSync(path.join(__dirname, '../../internal/coreui/assets/js/newpress-client.js'), 'utf8')],
]) {
  function apiFixture(status = 200) {
    let releaseBody;
    let bodyStarted;
    const ready = new Promise(resolve => { bodyStarted = resolve; });
    const state = {epoch: 0, session: {id: 1}};
    const applied = [], timers = new Set();
    const context = vm.createContext({
      state, AbortController, DOMException, navigator: {onLine: true},
      setTimeout(fn) { timers.add(fn); return fn; }, clearTimeout(fn) { timers.delete(fn); },
      applySession(session) { applied.push(session); state.session = session; }, announce() {},
      fetch: async () => ({ok: status === 200, status, headers: {get: () => null},
        json: () => new Promise((resolve, reject) => { releaseBody = {resolve, reject}; bodyStarted(); })}),
    });
    vm.runInContext(clientCode, context);
    vm.runInContext(`const client = NewPressClient.create({ session: () => state.session, epoch: () => state.epoch, onUnauthorized: () => applySession({authenticated:false, configured:true}) });`, context);
    return {state, applied, timers, ready, read: () => vm.runInContext("client.request('/test')", context), body: () => releaseBody};
  }

  test(`${entry}: a delayed 401 body cannot sign out a newer identity`, async () => {
    const f = apiFixture(401), pending = f.read();
    await f.ready;
    f.state.epoch++; f.state.session = {id: 2};
    f.body().resolve({error: {message: 'Expired'}});
    await assert.rejects(pending, {name: 'AbortError'});
    assert.equal(f.state.session.id, 2); assert.equal(f.applied.length, 0);
    assert.equal(f.timers.size, 0);
  });

  test(`${entry}: an aborted JSON body cannot become a successful empty API response`, async () => {
    const f = apiFixture(), pending = f.read();
    await f.ready;
    for (const timer of f.timers) timer();
    f.body().reject(new DOMException('Aborted', 'AbortError'));
    await assert.rejects(pending, /超时/);
    assert.equal(f.timers.size, 0);
  });

  test(`${entry}: an HTML gateway response cannot confirm a social API write`, async () => {
    const f = apiFixture(), pending = f.read();
    await f.ready;
    f.body().reject(new SyntaxError('Invalid JSON'));
    await assert.rejects(pending, /结果未确认/);
  });

  test(`${entry}: a current 401 still expires its own session`, async () => {
    const f = apiFixture(401), pending = f.read();
    await f.ready; f.body().resolve({error:{message:'Expired'}});
    await assert.rejects(pending, {status: 401});
    assert.equal(f.applied.length, 1); assert.equal(f.state.session.authenticated, false);
  });

  test(`${entry}: failures preserve offline guidance and remove caller listeners and timers`, async () => {
    for (const online of [true, false]) {
      const timers = new Set(), controller = new AbortController();
      let added = 0, removed = 0;
      const signal = {
        aborted: false,
        addEventListener(...args) { added++; controller.signal.addEventListener(...args); },
        removeEventListener(...args) { removed++; controller.signal.removeEventListener(...args); },
      };
      const context = vm.createContext({ AbortController, DOMException, TypeError, navigator: { onLine: online },
        setTimeout(fn) { timers.add(fn); return fn; }, clearTimeout(fn) { timers.delete(fn); },
        fetch: async () => { throw new TypeError('Failed to fetch'); }, signal,
      });
      vm.runInContext(clientCode, context);
      await assert.rejects(vm.runInContext("NewPressClient.create().request('/test', { signal })", context), online ? /网络连接失败/ : /离线状态/);
      assert.equal(timers.size, 0);
      assert.equal(added, 1); assert.equal(removed, 1);
    }
  });
}
