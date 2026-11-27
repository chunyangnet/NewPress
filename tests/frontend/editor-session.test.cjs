const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../../internal/coreui/assets/js/chunnel-admin-editor.source.js'), 'utf8');
const valid = {authenticated: true, read_only: false, user_id: 42, username: 'writer', csrf_token: 'new-session-token-'.repeat(3), role_capabilities: ['admin:access', 'content:write:own']};
const response = (payload = valid, status = 200) => ({ok: status === 200, status, json: async () => payload});

function session(fetch) {
  const start = source.indexOf('    const checkEditorSession ='), end = source.indexOf('    const performAutosave =', start);
  const timers = new Map(), messages = [], statuses = [], token = {value: 'old-token'};
  const context = vm.createContext({
    fetch, AbortController, DOMException, editorOwnerID: 42, editorOwnerLogin: 'writer',
    editorSessionPromise: null, editorSessionController: null, editorPageHidden: false, autosaveStopped: false, pendingEditorSubmission: null,
    editorForm: {querySelectorAll: () => [token]},
    setEditorRecovery: status => statuses.push(status), setAutosaveDetail: (message, tone) => messages.push({message, tone}),
    autosaveErrorMessage: (_, status) => `HTTP ${status}`,
    window: {setTimeout: (fn, ms) => { timers.set(1, {fn, ms}); return 1; }, clearTimeout: id => timers.delete(id)},
  });
  vm.runInContext(source.slice(start, end) + '\nglobalThis.check = checkEditorSession;', context);
  return {context, token, timers, messages, statuses, check: () => context.check()};
}

test('session expiry retains the token and stops autosave until an explicit retry', async () => {
  const f = session(async () => response({error: {}}, 401));
  assert.equal(await f.check(), false); assert.equal(f.token.value, 'old-token');
  assert.equal(f.context.autosaveStopped, true); assert.equal(f.messages.at(-1).message, 'HTTP 401');
});

test('same author login refreshes CSRF while preserving the paused autosave policy', async () => {
  const requests = [], f = session(async (...args) => { requests.push(args); return response(); });
  f.context.autosaveStopped = true;
  assert.equal(await f.check(), true); assert.equal(f.token.value, valid.csrf_token);
  assert.equal(f.context.autosaveStopped, true);
  assert.equal(requests[0][0], '/api/v1/admin/session');
  assert.equal(requests[0][1].cache, 'no-store'); assert.equal(requests[0][1].redirect, 'error');
  assert.equal(f.timers.size, 0);
});

test('administrator and scoped wildcard capabilities retain native save access', async () => {
  for (const role_capabilities of [['*'], ['admin:*', 'content:*']]) {
    const f = session(async () => response({...valid, role_capabilities}));
    assert.equal(await f.check(), true);
  }
});

test('another account cannot adopt a retained draft even with the same username', async () => {
  const f = session(async () => response({...valid, user_id: 99}));
  assert.equal(await f.check(), false); assert.equal(f.token.value, 'old-token');
  assert.match(f.messages.at(-1).message, /账号已切换/);
});

test('bootstrap sessions match their original nonempty username and cannot become a different persistent user', async () => {
  for (const [payload, allowed] of [[{...valid, user_id: 0}, true], [{...valid, user_id: 0, username: 'other'}, false], [valid, false]]) {
    const f = session(async () => response(payload)); f.context.editorOwnerID = 0;
    assert.equal(await f.check(), allowed);
  }
});

test('read-only, revoked write access and malformed sessions cannot enable saving', async () => {
  for (const change of [{read_only: true}, {role_capabilities: ['admin:access']}, {role_capabilities: ['content:write']}, {csrf_token: 'short'}, {role_capabilities: null}]) {
    const f = session(async () => response({...valid, ...change}));
    assert.equal(await f.check(), false); assert.equal(f.token.value, 'old-token');
  }
});

test('simultaneous editor actions share one session request and cancel safely on page hide', async () => {
  let release, calls = 0;
  const f = session(() => { calls++; return new Promise(resolve => { release = resolve; }); });
  const first = f.check(), second = f.check();
  assert.equal(first, second); assert.equal(calls, 1);
  f.context.editorSessionController.abort(); f.context.editorPageHidden = true;
  release(response()); assert.equal(await first, false);
  assert.equal(f.token.value, 'old-token'); assert.equal(f.messages.length, 0);
});

test('session deadline prevents an unbounded pending save and permits another attempt', async () => {
  const f = session((_, {signal}) => new Promise((_, reject) => { signal.addEventListener('abort', () => reject(signal.reason)); }));
  const pending = f.check(); assert.equal(f.timers.get(1).ms, 15000); f.timers.get(1).fn();
  assert.equal(await pending, false); assert.match(f.messages.at(-1).message, /超时/);
  f.context.fetch = async () => response(); assert.equal(await f.check(), true);
});

