'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const { loadModule } = require('./helpers/typescript.cjs');
function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}

function fixture(initialIDs = [100], messaging = true) {
  const owner = 1;
  const peer = 2;
  const contact = { id: peer, name: 'Peer', username: 'peer', available: true };
  const server = [];
  const requests = [];
  const controls = new Map();
  let active = true;
  let nextNonce = 0;
  let nextRead;
  const t = {
    peer, owner, contact, messages: new Map(), loaded: false, hasMore: false,
    loading: false, readThrough: 0, readSent: 0, draft: '', outbox: [], syncedThrough: 0,
  };
  function incoming(id) {
    server.push({ id, sender_id: peer, recipient_id: owner, content: `Incoming ${id}`, created_at: new Date(1700000000000 + id * 1000).toISOString(), read_at: null });
  }
  initialIDs.forEach(incoming);
  function control(selector) {
    if (!controls.has(selector)) controls.set(selector, { hidden: false, disabled: false, value: '', dispatchEvent() {} });
    return controls.get(selector);
  }
  async function api(url, options = {}) {
    const query = new URL(url, 'https://example.invalid').searchParams;
    requests.push({ url, method: options.method || 'GET' });
    if (options.method === 'POST') {
      const existing = server.find((item) => item.nonce === options.body.nonce);
      if (existing) return { message: { ...existing } };
      const id = Math.max(0, ...server.map((item) => item.id)) + 1;
      const message = { id, sender_id: owner, recipient_id: peer, ...options.body, created_at: new Date(1700000000000 + id * 1000).toISOString(), read_at: null };
      server.push(message);
      return { message: { ...message } };
    }
    const before = Number(query.get('before'));
    const after = Number(query.get('after'));
    const matches = server.filter((item) => (!before || item.id < before) && (!after || item.id > after))
      .sort((a, b) => after ? a.id - b.id : b.id - a.id);
    const items = matches.slice(0, 50).map((item) => ({ ...item }));
    const body = { items, contact, has_more: matches.length > 50, next_cursor: items.at(-1)?.id || 0, read_through: 0 };
    const hook = nextRead;
    nextRead = null;
    if (hook) await hook(url);
    return body;
  }
  const state = { session: {id:owner, authenticated:true}, epoch:0 };
  const module = loadModule('frontend/shared/interaction-messages.ts', { document: {hidden:true}, Event: class Event {}, Error });
  const context = module.createMessageSync({
    state, api, dom: {$: control}, activeThread: () => active, renderThread() {}, saveThread() {}, announce() {},
    loadInbox: async () => {}, loadNotices: async () => {}, showToast() {}, connection() {}, signIn: async () => {}, openPeer: async () => {},
    nonce: () => 'test-message-nonce-' + (++nextNonce), run: action => Promise.resolve().then(action),
    messageDrawer: messaging ? { open: true } : null,
  });
  return {
    t, server, requests, incoming,
    load: (mode) => context.loadThread(t, mode),
    send: (content, nonce) => context.sendMessage(t, content, nonce),
    active: (value) => { active = value; },
    onNextRead: (action) => { nextRead = action; },
    holdNextRead() {
      const started = deferred();
      const released = deferred();
      nextRead = async (url) => { started.resolve(url); await released.promise; };
      return { started: started.promise, release: released.resolve };
    },
    ids: () => [...t.messages.keys()].sort((a, b) => a - b),
  };
}

test('sending between polls cannot skip an unseen incoming message', async () => {
  const f = fixture();
  await f.load('initial');
  f.incoming(101);
  await f.send('Reply');
  assert.deepEqual(f.ids(), [100, 102], 'The acknowledgement is visible immediately');
  await f.load();
  assert.deepEqual(f.ids(), [100, 101, 102], 'The next read must include the incoming message before our reply');
  assert.equal(f.t.outbox.length, 0);
  await f.load();
  assert.deepEqual(f.ids(), [100, 101, 102], 'Acknowledgement and polling must not duplicate the reply');
});

test('a reply acknowledged during the initial read does not replace its snapshot boundary', async () => {
  const f = fixture();
  const gate = f.holdNextRead();
  const loading = f.load('initial');
  await gate.started;
  f.incoming(101);
  await f.send('Reply before initial GET completes');
  gate.release();
  await loading;
  await f.load();
  assert.deepEqual(f.ids(), [100, 101, 102]);
});

test('an empty initial read remains refreshable after a concurrent first send', async () => {
  const f = fixture([]);
  const gate = f.holdNextRead();
  const loading = f.load('initial');
  await gate.started;
  f.incoming(1);
  await f.send('First reply');
  gate.release();
  await loading;
  await f.load();
  assert.deepEqual(f.ids(), [1, 2]);
});

test('forward pagination catches every page when sending during an in-flight read', async () => {
  const f = fixture();
  await f.load('initial');
  for (let id = 101; id <= 201; id++) f.incoming(id);
  const gate = f.holdNextRead();
  const loading = f.load();
  assert.match(await gate.started, /after=100$/);
  await f.send('Reply while catching up');
  gate.release();
  await loading;
  assert.deepEqual(f.ids(), Array.from({ length: 103 }, (_, index) => index + 100));
  assert.deepEqual(f.requests.filter((r) => r.method === 'GET').slice(-3).map((r) => r.url), [
    '/api/v1/social/messages/2?after=100', '/api/v1/social/messages/2?after=150', '/api/v1/social/messages/2?after=200',
  ]);
});

test('loading older history and acknowledging a reply preserve unread forward gaps', async () => {
  const f = fixture(Array.from({ length: 100 }, (_, index) => index + 1));
  await f.load('initial');
  assert.equal(f.t.hasMore, true);
  f.incoming(101);
  await f.send('Reply before browsing history');
  await f.load('older');
  await f.load();
  assert.deepEqual(f.ids(), Array.from({ length: 102 }, (_, index) => index + 1));
  assert.equal(f.t.hasMore, false);
});

test('failed catch-up resumes after the last successful page even after another send', async () => {
  const f = fixture();
  await f.load('initial');
  for (let id = 101; id <= 201; id++) f.incoming(id);
  f.onNextRead(() => f.onNextRead(() => { throw new Error('Transient read failure'); }));
  await assert.rejects(f.load(), /Transient read failure/);
  assert.deepEqual(f.ids(), Array.from({ length: 51 }, (_, index) => index + 100));
  await f.send('Reply during reconnect');
  await f.load();
  assert.deepEqual(f.ids(), Array.from({ length: 103 }, (_, index) => index + 100));
});

test('disabled messaging makes no private reads or writes', async () => {
  const f = fixture([100], false);
  await f.load('initial');
  await f.send('Must not be sent');
  assert.deepEqual(f.requests, []);
  assert.deepEqual(f.t.outbox, []);
});

test('leaving a conversation between pages resumes from the completed page', async () => {
  const f = fixture();
  await f.load('initial');
  for (let id = 101; id <= 201; id++) f.incoming(id);
  f.onNextRead(() => f.active(false));
  await f.load();
  assert.deepEqual(f.ids(), Array.from({ length: 51 }, (_, index) => index + 100));
  await f.send('Reply while away');
  f.active(true);
  await f.load();
  assert.deepEqual(f.ids(), Array.from({ length: 103 }, (_, index) => index + 100));
});
