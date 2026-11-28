const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const editor = require('./helpers/editor-save.cjs');
const script = name => name === 'chunnel-admin-editor.source.js' ? editor.source() : fs.readFileSync(path.join(__dirname, '../../internal/coreui/assets/js', name), 'utf8');
const autosaveMeta = {post_id: 1, source_version: 1, expires_at: '2026-11-10T05:00:00Z', recovery_url: '/admin/content/1/edit/?recovery=autosave'};
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return {promise, resolve}; };

function autosave(fetch) {
  const source = script('chunnel-admin-editor.source.js');
  const start = source.indexOf('    const performAutosave =');
  const end = source.indexOf('    async function saveAutosave', start);
  assert.ok(start >= 0 && end > start);
  const timers = new Map(), details = [], toasts = [];
  const context = vm.createContext({
    ...editor.context,
    fetch, AbortController, DOMException, URLSearchParams, URL,
    checkEditorSession: async () => true, editorPageHidden: false,
    autosaveURL: '/admin/content/1/autosave/', submittingEditor: false, pendingEditorSubmission: null, browserRecovery: null,
    serializedEditor: () => 'new draft', editorNeedsAutosave: () => true,
    lastAutosaveSignature: '', autosaveController: null, autosaveStopped: false, timezoneNeedsReview: false,
    editorSavebar: {classList: {add() {}, remove() {}}}, editorForm: {}, autosaveRetention: 24,
    reviewPublicationTimezone() {},
    setAutosaveDetail: (message, tone) => details.push({message, tone}),
    autosaveErrorMessage: (_, status) => `HTTP ${status}`,
    showToast: (message, tone) => toasts.push({message, tone}),
    FormData: class { forEach(callback) { callback('new draft', 'content_source'); callback('1', 'version'); } },
    window: {location: {href: 'https://example.test/admin/content/1/edit/', origin: 'https://example.test'}, setTimeout(fn, ms) { timers.set(1, {fn, ms}); return 1; }, clearTimeout(id) { timers.delete(id); }},
  });
  vm.runInContext(source.slice(start, end) + '\nglobalThis.perform = performAutosave;', context);
  return {context, timers, details, toasts, save: () => context.perform(true)};
}

test('timezone review pauses automatic and manual recovery writes without consuming the draft', async () => {
  const fixture = autosave(() => assert.fail('unreviewed wall clock cannot be saved'));
  fixture.context.timezoneNeedsReview = true;
  assert.equal(await fixture.save(), null);
  assert.equal(await fixture.context.perform(false), null);
  assert.equal(fixture.context.lastAutosaveSignature, '');
  assert.equal(fixture.timers.size, 0);
  assert.match(fixture.details.at(-1).message, /核对/);
});

test('autosave aborts a hung request and keeps the draft eligible for retry', async () => {
  let signal;
  const started = deferred();
  const fixture = autosave((_, options) => new Promise((_, reject) => {
    signal = options.signal;
    started.resolve();
    signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
  }));
  const pending = fixture.save();
  await started.promise;
  assert.equal(fixture.timers.get(1).ms, 15000);
  fixture.timers.get(1).fn();
  assert.equal(await pending, null);
  assert.equal(signal.aborted, true);
  assert.equal(fixture.context.lastAutosaveSignature, '');
  assert.equal(fixture.context.autosaveStopped, false);
  assert.match(fixture.details.at(-1).message, /超时/);
  assert.equal(fixture.timers.size, 0);
});

test('autosave cannot acknowledge an HTML login response or invalid JSON as saved', async () => {
  const fixture = autosave(async () => ({ok: true, json: async () => { throw new SyntaxError('HTML'); }}));
  assert.equal(await fixture.save(), null);
  assert.equal(fixture.context.lastAutosaveSignature, '');
  assert.equal(fixture.details.at(-1).tone, 'error');
});

test('an incomplete success response does not consume the autosave signature', async () => {
  const fixture = autosave(async () => ({ok: true, json: async () => ({available: false})}));
  assert.equal(await fixture.save(), null);
  assert.equal(fixture.context.lastAutosaveSignature, '');
});

test('confirmed autosave acknowledges the captured draft and clears its deadline', async () => {
  const payload = {...autosaveMeta, available: true, updated_at: '2026-11-07T05:00:00Z', preview_url: '/admin/content/1/preview/?token=test'};
  const fixture = autosave(async () => ({ok: true, json: async () => payload}));
  assert.deepEqual(await fixture.save(), payload);
  assert.equal(fixture.context.lastAutosaveSignature, 'new draft');
  assert.equal(fixture.details.at(-1).tone, 'success');
  assert.equal(fixture.timers.size, 0);
});

test('autosave rejects malformed acknowledgements and preview addresses outside this draft', async () => {
  const valid = {...autosaveMeta, available: true, updated_at: '2026-11-07T05:00:00Z', preview_url: '/admin/content/1/preview/?token=test'};
  for (const change of [
    {available: 'true'}, {updated_at: 'invalid date'}, {preview_url: {}},
    {preview_url: 'javascript:alert(1)'}, {preview_url: '//external.test/admin/content/1/preview/?token=test'},
    {preview_url: '/admin/content/2/preview/?token=test'}, {preview_url: '/admin/content/1/preview/'},
  ]) {
    const fixture = autosave(async () => ({ok: true, json: async () => ({...valid, ...change})}));
    assert.equal(await fixture.save(), null);
    assert.equal(fixture.context.lastAutosaveSignature, '');
    assert.equal(fixture.details.at(-1).tone, 'error');
    assert.equal(fixture.timers.size, 0);
  }
});

test('conflicting autosave stops retrying instead of overwriting the saved version', async () => {
  const fixture = autosave(async () => ({ok: false, status: 409, json: async () => ({})}));
  assert.equal(await fixture.save(), null);
  assert.equal(fixture.context.autosaveStopped, true);
  assert.equal(fixture.context.lastAutosaveSignature, '');
});

test('a confirmed manual recovery resumes automatic saving after a session pause', async () => {
  const payload = {...autosaveMeta, available: true, updated_at: '2026-11-09T05:00:00Z', preview_url: '/admin/content/1/preview/?token=test'};
  const fixture = autosave(async () => ({ok: true, json: async () => payload}));
  fixture.context.autosaveStopped = true;
  assert.deepEqual(await fixture.save(), payload);
  assert.equal(fixture.context.autosaveStopped, false);
});

test('automatic recovery refuses an unconfirmed owner and a newer draft during session refresh', async () => {
  const fixture = autosave(() => assert.fail('unconfirmed draft must not be written'));
  fixture.context.checkEditorSession = async () => false;
  assert.equal(await fixture.save(), null);
  assert.equal(fixture.timers.size, 0);
  const started = deferred();
  fixture.context.checkEditorSession = () => started.promise;
  const saving = fixture.save();
  fixture.context.serializedEditor = () => 'later input';
  started.resolve(true);
  assert.equal(await saving, null);
  assert.equal(fixture.context.lastAutosaveSignature, '');
});

test('autosave verifies the article identity, source version and recovery metadata', async () => {
  const valid = {...autosaveMeta, available: true, updated_at: '2026-11-09T05:00:00Z', preview_url: '/admin/content/1/preview/?token=test'};
  for (const change of [{post_id: 2}, {post_id: '1'}, {source_version: 2}, {source_version: undefined},
    {expires_at: 'bad'}, {expires_at: valid.updated_at}, {recovery_url: '/admin/content/2/edit/?recovery=autosave'}]) {
    const fixture = autosave(async () => ({ok: true, json: async () => ({...valid, ...change})}));
    assert.equal(await fixture.save(), null);
    assert.equal(fixture.context.lastAutosaveSignature, '');
  }
});

test('timeout while reading the autosave response body cannot report success', async () => {
  const started = deferred();
  const fixture = autosave(async (_, {signal}) => ({ok: true, json: () => {
    started.resolve();
    return new Promise((_, reject) => signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError'))));
  }}));
  const pending = fixture.save(); await started.promise;
  fixture.timers.get(1).fn();
  assert.equal(await pending, null);
  assert.equal(fixture.context.lastAutosaveSignature, '');
  assert.match(fixture.details.at(-1).message, /超时/);
});

function discussion() {
  const {loadModule}=require('./helpers/typescript.cjs');
  const gate = deferred(), requests = [], errors = {hidden: true}, input = {value: 'Original text'};
  const state={owner:1,generation:0,busy:false,pending:null};
  const context = vm.createContext({
    ...editor.context,
    state, input, errorBox: errors,
    getSession: () => gate.promise, run: action => action(),
    signIn: action => action(), showToast() {}, update() {},
    api: (...args) => { requests.push(args); return Promise.reject(new Error('Injected server error')); },
    reveal() {}, key:id=>`community.${id||state.owner}`, storage:{get(){},set(){},remove(){}}, submit: {replaceChildren() {}},
    createIcon() {}, document: {createTextNode: text => text}, crypto: {randomUUID: () => 'test-nonce'},
  });
  const submit=loadModule('frontend/shared/community-submit.ts',context).createCommunitySubmission(context);
  return {context:state, input, errors, requests, submit:()=>submit('publish'), ready: () => gate.resolve({id:1, authenticated:true, can_publish:true})};
}

test('community submit preserves typing during the session refresh and sends nothing stale', async () => {
  const fixture = discussion();
  const pending = fixture.submit();
  fixture.input.value = 'Newer text while checking session';
  fixture.ready(); await pending;
  assert.equal(fixture.input.value, 'Newer text while checking session');
  assert.equal(fixture.requests.length, 0);
  assert.match(fixture.errors.textContent, /输入内容已更新/);
});

test('community submit retains an unchanged draft when its write fails', async () => {
  const fixture = discussion();
  const pending = fixture.submit(); fixture.ready(); await pending;
  assert.equal(fixture.requests.length, 1);
  assert.equal(fixture.input.value, 'Original text');
  assert.equal(fixture.input.readOnly, false);
  assert.equal(fixture.context.busy, false);
});
