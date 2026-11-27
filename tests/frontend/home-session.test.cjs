const test = require('node:test');
const assert = require('node:assert/strict');

const {loadModule} = require('./helpers/typescript.cjs');
const flush = () => new Promise(resolve => setImmediate(resolve));

function fixture({ progression = false, messaging = true } = {}) {
  const nodes = new Map(), requests = [], timers = new Map(), paints = [], loads = [];
  let nextTimer = 0, channel, ready = false;
  class Element extends EventTarget {
    constructor() { super(); this.hidden = false; this.textContent = ''; this.attributes = new Map(); }
    setAttribute(key, value) { this.attributes.set(key, value); }
  }
  const document = new Element(), window = new Element();
  const node = (selector) => {
    if (!nodes.has(selector)) nodes.set(selector, new Element());
    return nodes.get(selector);
  };
  const many = (selector) => ['[data-favorite]', '[data-follow]'].includes(selector) ? [] : selector.split(',').map(part => node(part.trim()));
  document.querySelector = $; document.querySelectorAll = $$;
  const {initHomeSession} = loadModule('frontend/themes/chunnel/home-session.ts', {
    $, $$, document, window, AbortController, URL, location: { origin: 'https://example.test' },
    localStorage: { getItem: () => null },
    BroadcastChannel: class { constructor() { channel = this; } close() { this.closed = true; } },
    setTimeout: (callback, delay) => { timers.set(++nextTimer, { callback, delay }); return nextTimer; },
    clearTimeout: id => timers.delete(id),
    fetch: (url, options) => new Promise(resolve => {
      requests.push({ url, signal: options.signal, resolve: value => resolve({ ok: true, json: async () => value }) });
    }),
  });
  initHomeSession(name => {
    loads.push(name);
    window.ChunnelHomeMember = { paint(data, owner) { paints.push({ data, owner }); return true; } };
    return Promise.resolve();
  }, () => ready);
  // The member card is only present when its module is enabled; this fixture
  // models the shell, so skipping progression keeps the unread-index contract.
  function $(selector) {
    if (selector === '[data-messaging-enabled="false"]') return messaging ? null : node(selector);
    return selector === '[data-home-progression]' && !progression ? null : node(selector);
  }
  function $$(selector) { return many(selector); }
  return { document, window, node, requests, timers, channel, paints, loads, featuresReady:()=>{ready=true;} };
}

test('read-only homepage session restores identity and unread counts without an enhancement script', async () => {
  const f = fixture();
  f.requests[0].resolve({ authenticated: true, id: 7, name: 'Reader', can_admin: false });
  await flush();
  assert.equal(f.node('[data-account-menu]').hidden, false);
  assert.equal(f.node('[data-account-name]').textContent, 'Reader');
  assert.equal(f.node('[data-account-admin]').hidden, true);
  f.requests[1].resolve({ unread: 2 }); f.requests[2].resolve({ unread: 101 });
  await flush();
  assert.equal(f.node('[data-message-unread-count]').textContent, '2');
  assert.equal(f.node('[data-unread-count]').textContent, '99+');
  assert.ok([...f.timers.values()].some(timer => timer.delay === 30000));
});

test('anonymous reading does not start background session polling', async () => {
  const f = fixture(); f.requests[0].resolve({ authenticated: false }); await flush();
  assert.equal(f.node('[data-account-menu]').hidden, true);
  assert.equal(f.requests.length, 1); assert.equal(f.timers.size, 0);
});

test('disabled messaging skips private APIs while keeping notifications and membership in sync', async () => {
  const f = fixture({ messaging: false, progression: true });
  f.requests[0].resolve({ authenticated: true, id: 7, name: 'Reader' }); await flush();
  assert.deepEqual(f.requests.map(request => request.url), [
    '/api/v1/social/session', '/api/v1/social/notice-center', '/api/v1/social/progression',
  ]);
  f.requests[1].resolve({ unread: 3 });
  f.requests[2].resolve({ account: { user_id: '7' } }); await flush();
  assert.equal(f.node('[data-message-unread-count]').hidden, true);
  assert.equal(f.node('[data-unread-count]').textContent, '3');
  assert.equal(f.node('[data-menu-unread]').hidden, false);
  assert.deepEqual(f.paints.map(paint => paint.owner), [7]);
});

test('management and writing links require authenticated capabilities and disappear after logout', async () => {
  const f = fixture();
  f.requests[0].resolve({ authenticated: true, id: 7, name: 'Editor', can_admin: true, can_publish: true });
  await flush();
  assert.equal(f.node('[data-account-admin]').hidden, false);
  assert.equal(f.node('[data-account-publish]').hidden, false);
  f.requests[1].resolve({ unread: 0 }); f.requests[2].resolve({ unread: 0 }); await flush();
  f.channel.onmessage({ data: { type: 'session' } });
  f.requests[3].resolve({ authenticated: false, can_admin: true, can_publish: true }); await flush();
  assert.equal(f.node('[data-account-menu]').hidden, true);
  assert.equal(f.node('[data-account-admin]').hidden, true);
  assert.equal(f.node('[data-account-publish]').hidden, true);
});

test('a role downgrade hides privileged links while retaining the reader account menu', async () => {
  const f = fixture();
  f.requests[0].resolve({ authenticated: true, id: 7, name: 'Editor', can_admin: true, can_publish: true });
  await flush();
  f.requests[1].resolve({ unread: 0 }); f.requests[2].resolve({ unread: 0 }); await flush();
  f.channel.onmessage({ data: { type: 'session' } });
  f.requests[3].resolve({ authenticated: true, id: 7, name: 'Reader', can_admin: false, can_publish: false }); await flush();
  assert.equal(f.node('[data-account-menu]').hidden, false);
  assert.equal(f.node('[data-account-admin]').hidden, true);
  assert.equal(f.node('[data-account-publish]').hidden, true);
});

test('the collapsed mobile menu exposes unread messages even without notifications', async () => {
  const f = fixture(); f.requests[0].resolve({ authenticated: true, id: 7, name: 'Reader' }); await flush();
  f.requests[1].resolve({ unread: 2 }); f.requests[2].resolve({ unread: 0 }); await flush();
  assert.equal(f.node('[data-menu-unread]').hidden, false);
  assert.equal(f.node('[data-unread-dot]').hidden, true);
});

test('cross-tab identity refresh cannot be overwritten by an earlier session body', async () => {
  const f = fixture(), older = f.requests[0];
  f.channel.onmessage({ data: { type: 'session' } });
  assert.equal(older.signal.aborted, true);
  f.requests[1].resolve({ authenticated: true, id: 8, name: 'New reader' }); await flush();
  older.resolve({ authenticated: false }); await flush();
  assert.equal(f.node('[data-account-name]').textContent, 'New reader');
  assert.equal(f.node('[data-account-menu]').hidden, false);
});

test('full module handoff aborts pending reads, stops polling and closes the lightweight channel', async () => {
  const f = fixture(); f.node('[data-account-name]').textContent = 'Full module identity';
  f.window.ChunnelHome.stop();
  assert.equal(f.requests[0].signal.aborted, true);
  assert.equal(f.channel.closed, true);
  f.requests[0].resolve({ authenticated: true, id: 9, name: 'Stale bootstrap' }); await flush();
  assert.equal(f.node('[data-account-name]').textContent, 'Full module identity');
  assert.equal(f.requests.length, 1); assert.equal(f.timers.size, 0);
});

test('timeout covers the session response body and suppresses late identity changes', async () => {
  const f = fixture(); f.node('[data-account-menu]').hidden = true;
  [...f.timers.values()].find(timer => timer.delay === 8000).callback();
  assert.equal(f.requests[0].signal.aborted, true);
  f.requests[0].resolve({ authenticated: true, id: 10, name: 'Late reader' }); await flush();
  assert.equal(f.node('[data-account-menu]').hidden, true);
});

for (const value of [null, [], 'bad response', {}, { authenticated: 'true', id: 9 },
  ...[0, -1, 1.5, '9', Number.MAX_SAFE_INTEGER + 1].map(id => ({ authenticated: true, id }))]) {
  test(`malformed session ${JSON.stringify(value)} preserves the last valid identity and schedules a retry`, async () => {
    const f = fixture();
    f.requests[0].resolve({ authenticated: true, id: 7, name: 'Reader' }); await flush();
    f.requests[1].resolve({ unread: 2 }); f.requests[2].resolve({ unread: 3 }); await flush();
    f.channel.onmessage({ data: { type: 'session' } });
    f.requests[3].resolve(value); await flush();
    assert.equal(f.node('[data-account-name]').textContent, 'Reader');
    assert.equal(f.node('[data-home-name]').textContent, 'Reader');
    assert.equal(f.node('[data-account-menu]').hidden, false);
    assert.equal(f.node('[data-message-unread-count]').textContent, '2');
    assert.equal(f.requests.length, 4);
    assert.ok([...f.timers.values()].some(timer => timer.delay === 30000));
  });
}

test('anonymous responses clear all identity text even when unrelated identity fields remain', async () => {
  const f = fixture();
  f.requests[0].resolve({ authenticated: false, id: 7, name: 'Old user', can_admin: true }); await flush();
  for (const selector of ['[data-home-name]', '[data-account-name]', '[data-home-member-avatar]', '[data-account-avatar]']) {
    assert.equal(f.node(selector).textContent, '');
  }
  assert.equal(f.node('[data-account-admin]').hidden, true);
  assert.equal(f.loads.length, 0); assert.equal(f.timers.size, 0);
});

test('an identity switch suppresses the previous member response before its delayed body resolves', async () => {
  const f = fixture({ progression: true });
  f.requests[0].resolve({ authenticated: true, id: 7, name: 'Old reader' }); await flush();
  f.requests[1].resolve({ unread: 2 }); f.requests[2].resolve({ unread: 3 });
  const oldMember = f.requests[3];
  f.channel.onmessage({ data: { type: 'session' } });
  f.requests[4].resolve({ authenticated: true, id: 8, name: 'New reader' }); await flush();
  f.requests[5].resolve({ unread: 1 }); f.requests[6].resolve({ unread: 0 });
  f.requests[7].resolve({ account: { user_id: '8' } }); await flush();
  oldMember.resolve({ account: { user_id: '7' } }); await flush();
  assert.equal(oldMember.signal.aborted, true);
  assert.deepEqual(f.paints.map(paint => paint.owner), [8]);
  assert.equal(f.node('[data-home-name]').textContent, 'New reader');
});

test('a repeated session read preserves the already synchronized check-in label', async () => {
  const f = fixture({ progression: true });
  f.requests[0].resolve({ authenticated: true, id: 7, name: 'Reader' }); await flush();
  f.requests[1].resolve({ unread: 0 }); f.requests[2].resolve({ unread: 0 });
  f.requests[3].resolve({ account: { user_id: '7' } }); await flush();
  f.node('[data-home-checkin-label]').textContent = '本轮已领取';
  f.channel.onmessage({ data: { type: 'session' } });
  f.requests[4].resolve({ authenticated: true, id: 7, name: 'Reader' }); await flush();
  assert.equal(f.node('[data-home-checkin-label]').textContent, '本轮已领取');
  assert.equal(f.loads.length, 1);
});

test('full module handoff still permits a check-in event to refresh member display without restarting polling', async () => {
  const f = fixture({ progression: true });
  f.window.ChunnelHome.stop();
  f.document.dispatchEvent(new Event('chunnel:checkin'));
  f.requests[1].resolve({ authenticated: true, id: 7, name: 'Reader' }); await flush();
  f.requests[2].resolve({ unread: 0 }); f.requests[3].resolve({ unread: 0 });
  f.requests[4].resolve({ account: { user_id: '7' } }); await flush();
  f.requests[0].resolve({ authenticated: false }); await flush();
  assert.deepEqual(f.paints.map(paint => paint.owner), [7]);
  assert.equal(f.timers.size, 0);
  assert.equal(f.channel.closed, true);
});

test('membership refresh after handoff cannot replace a confirmed reward label when the read fails',async()=>{
  const f=fixture({progression:true});f.featuresReady();f.window.ChunnelHome.stop();
  f.node('[data-home-checkin-label]').textContent='本轮已领取';
  f.document.dispatchEvent(new Event('chunnel:checkin'));
  f.requests[1].resolve({authenticated:true,id:7,name:'Reader'});await flush();
  f.requests[2].resolve({unread:0});f.requests[3].resolve({unread:0});f.requests[4].resolve(null);await flush();
  f.requests[0].resolve({authenticated:false});await flush();
  assert.equal(f.node('[data-home-checkin-label]').textContent,'本轮已领取');assert.equal(f.paints.length,0);assert.equal(f.timers.size,0);
});

test('leaving the page after handoff aborts reward-driven reads and ignores their late bodies', async () => {
  const f = fixture({ progression: true });
  f.featuresReady(); f.window.ChunnelHome.stop();
  f.document.dispatchEvent(new Event('chunnel:checkin'));
  f.requests[1].resolve({ authenticated: true, id: 7, name: 'Reader' }); await flush();
  const pending = f.requests.slice(2);
  assert.equal(pending.length, 3);
  f.window.dispatchEvent(new Event('pagehide'));
  assert.ok(pending.every(request => request.signal.aborted));
  pending[0].resolve({ unread: 6 }); pending[1].resolve({ unread: 8 });
  pending[2].resolve({ account: { user_id: '7' } }); await flush();
  f.requests[0].resolve({ authenticated: false }); await flush();
  assert.equal(f.paints.length, 0);
  assert.equal(f.node('[data-unread-count]').textContent, '');
  assert.equal(f.timers.size, 0);
});
