'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const {loadModule}=require('./helpers/typescript.cjs');
const key = 'chunnel.comment./posts/closed', pendingKey = `${key}.submission`;
const submission = { version: 1, nonce: 'original-request', owner: 0, uncertain: true, payload: { content: '原评论\n\n保留空行 🙂', parent_id: 10, author_name: '读者', email: 'reader@example.test', website: '' } };
const draft = () => ({ content: '继续编辑的新内容\n尚未发送', parent: '20', reply: '回复 #20', at: Date.now() });
const receipt = { status: 'approved', notice: '已确认此前提交', submission_key: submission.nonce, replayed: true, comment: { id: 31, post_id: 7, parent_id: 10, author_name: '审核后称呼' } };

class Element {
  constructor(tagName = 'div', document) { Object.assign(this, { tagName, ownerDocument: document, children: [], dataset: {}, listeners: new Map(), attributes: new Map(), hidden: false, disabled: false, textContent: '' }); }
  addEventListener(type, handler) { if (!this.listeners.has(type)) this.listeners.set(type, []); this.listeners.get(type).push(handler); }
  dispatchEvent(event) { return Promise.all((this.listeners.get(event.type) || []).map(handler => handler(event))); }
  click() { return this.dispatchEvent({ type: 'click' }); }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  setAttribute(name, value) { this.attributes.set(name, value); }
  contains(element) { return this === element || this.children.some(child => child.contains?.(element)); }
  focus(options) { this.ownerDocument.activeElement = this; this.focusOptions = options; }
}

function fixture({ savedDraft = draft(), pending = submission, api = true, drafts = true, session = { authenticated: false }, unavailable = false, loginFailure = false } = {}) {
  const document = new Element(), window = new Element(); document.body = new Element('body', document); document.activeElement = document.body;
  const closed = new Element('div', document); closed.dataset = { commentPostId: '7', commentDrafts: String(drafts), ...(api ? { commentApi: '/api/v1/posts/closed/comments', commentToken: 'current-token' } : {}) };
  const storage = new Map([[key, savedDraft], [pendingKey, pending]].filter(([, value]) => value).map(([name, value]) => [name, JSON.parse(JSON.stringify(value))]));
  let currentSession = session, sessions = 0, signins = 0, allow = false;
  const requests = [];
  window.confirm = () => allow;
  const context = {
    $: selector => selector === '[data-comment-closed]' ? closed : null,
    node: (tag, className = '', text = '') => Object.assign(new Element(tag, document), { className, textContent: text }),
    document, window, location: { pathname: '/posts/closed' },
    storage: { get: name => unavailable ? null : storage.get(name), remove: name => storage.delete(name) },
    getSession: async force => { assert.equal(force, true); sessions++; return currentSession; },
    signIn: () => { signins++; if (loginFailure) throw new Error('登录连接失败'); }, createIcon: () => new Element('svg', document),
    api: (url, options) => new Promise((resolve, reject) => requests.push({ url, ...options, resolve, reject })),
  };
  loadModule('frontend/shared/comments.ts',context).mountComments({...context,dom:{$:context.$,optional:context.$,node:context.node}});
  const all = (root = closed) => [root, ...root.children.flatMap(all)];
  const find = name => all().find(node => Object.hasOwn(node.dataset, name));
  return {
    closed, storage, requests, document, window, all, find,
    draftPanel: () => find('commentSavedDraft'), recovery: () => find('commentRecovery'),
    query: () => find('commentReceiptQuery'), end: () => find('commentRetryEnd'),
    textarea: () => all().find(node => node.tagName === 'textarea'),
    discard: () => all().find(node => node.textContent === '清除草稿'),
    login: () => all().find(node => node.textContent === '登录原账号'),
    result: () => all().find(node => node.className === 'comment-notice comment-notice-success'),
    error: () => all().find(node => node.className === 'interaction-error'),
    complete: result => requests.at(-1).resolve(result), fail: error => requests.at(-1).reject(error),
    session: value => { currentSession = value; return document.dispatchEvent({ type: 'chunnel:session', detail: value }); },
    sessions: () => sessions, signins: () => signins,
    confirm: value => { allow = value; },
    async lookup() { const task = this.query().click(); await new Promise(resolve => setImmediate(resolve)); return { task }; },
  };
}

test('closed pages restore separate readable input and original submission without automatic requests', async () => {
  const f = fixture();
  assert.equal(f.draftPanel().hidden, false); assert.equal(f.recovery().hidden, false);
  assert.equal(f.textarea().value, draft().content); assert.equal(f.textarea().readOnly, true);
  assert.equal(f.find('commentOriginal').textContent, submission.payload.content);
  assert.match(f.recovery().children[0].textContent, /不会重新发送/);
  assert.equal(f.all().some(node => node.tagName === 'form'), false);
  await f.session({ authenticated: true, id: 2 }); await f.window.dispatchEvent({ type: 'pagehide' });
  assert.equal(f.requests.length, 0); assert.equal(f.sessions(), 0); assert.equal(f.textarea().readOnly, true);
});

test('explicit lookup uses the original identity and receipt-only route, preserving newer input and timestamp', async () => {
  const savedDraft = draft(), f = fixture({ savedDraft }); f.query().focus();
  const { task } = await f.lookup();
  assert.equal(f.requests[0].url, '/api/v1/posts/closed/comments/receipt');
  assert.equal(f.requests[0].method, 'POST'); assert.equal(f.requests[0].headers['Idempotency-Key'], submission.nonce);
  assert.equal(f.requests[0].body.content, submission.payload.content); assert.equal(f.requests[0].body.parent_id, 10);
  assert.equal(f.requests[0].body.expected_user_id, 0); assert.equal(f.requests[0].body.form_token, 'current-token');
  assert.equal(f.query().disabled, true); assert.equal(f.end().disabled, true); assert.equal(f.discard().disabled, true);
  await f.query().click(); assert.equal(f.requests.length, 1);
  f.complete(receipt); await task;
  assert.equal(f.storage.has(pendingKey), false); assert.deepEqual(f.storage.get(key), savedDraft);
  assert.equal(f.recovery().hidden, true); assert.equal(f.draftPanel().hidden, false);
  assert.equal(f.document.activeElement, f.result()); assert.equal(f.result().focusOptions.preventScroll, true);
  assert.equal(f.result().children.at(-1).href, '/posts/closed?comments_page=1#comments');
});

for (const [name, result] of [
  ['approved', receipt],
  ['pending', { status: 'pending', replayed: true, submission_key: submission.nonce }],
  ['deleted', { status: 'received', replayed: true, submission_key: submission.nonce }],
]) test(`confirmed ${name} receipt consumes only the matching input`, async () => {
  const f = fixture({ savedDraft: { ...draft(), content: submission.payload.content, parent: '10' } });
  const { task } = await f.lookup(); f.complete(result); await task;
  assert.equal(f.storage.has(pendingKey), false); assert.equal(f.storage.has(key), false); assert.equal(f.draftPanel().hidden, true);
});

test('identical text with a different reply target remains a separate draft', async () => {
  const f = fixture({ savedDraft: { ...draft(), content: submission.payload.content, parent: '20' } });
  const { task } = await f.lookup(); f.complete(receipt); await task;
  assert.equal(f.storage.get(key).parent, '20'); assert.equal(f.draftPanel().hidden, false);
});

for (const switchBack of [false,true]) test(`late closed-page receipt retains both records after identity changes (return=${switchBack})`,async()=>{
  const f=fixture({savedDraft:{...draft(),content:submission.payload.content,parent:'10'}});
  const {task}=await f.lookup();await f.session({authenticated:true,id:2});
  if(switchBack)await f.session({authenticated:false});
  f.complete(receipt);await task;
  assert.equal(f.storage.has(key),true);assert.equal(f.storage.has(pendingKey),true);
  assert.equal(f.result().hidden,true);assert.match(f.error().textContent,/账号已变化/);
  await f.session({authenticated:false});const next=await f.lookup();f.complete(receipt);await next.task;
  assert.equal(f.storage.has(pendingKey),false);assert.equal(f.storage.has(key),false);
});

for (const [name, result] of [
  ['missing key', { ...receipt, submission_key: undefined }],
  ['wrong key', { ...receipt, submission_key: 'another-original' }],
  ['fresh creation', { ...receipt, replayed: false }],
  ['missing replay flag', { ...receipt, replayed: undefined }],
  ['wrong article', { ...receipt, comment: { ...receipt.comment, post_id: 8 } }],
  ['wrong reply', { ...receipt, comment: { ...receipt.comment, parent_id: 0 } }],
  ['hidden data leak', { ...receipt, status: 'received' }],
  ['unrecognized status', { ...receipt, status: 'missing' }],
]) test(`invalid lookup receipt (${name}) keeps both saved records`, async () => {
  const f = fixture(); const { task } = await f.lookup(); f.complete(result); await task;
  assert.equal(f.storage.has(pendingKey), true); assert.equal(f.storage.has(key), true);
  assert.equal(f.error().hidden, false); assert.equal(f.result().hidden, true);
});

test('missing receipt and transport failure retain original key for further explicit queries', async () => {
  const f = fixture();
  for (const error of [Object.assign(new Error('暂未查到回执'), { code: 'comments.submission_not_found' }), new Error('网络中断')]) {
    const { task } = await f.lookup(); f.fail(error); await task;
    assert.equal(f.storage.get(pendingKey).nonce, submission.nonce); assert.equal(f.storage.has(key), true);
    assert.equal(f.requests.at(-1).url.endsWith('/receipt'), true);
  }
  assert.equal(f.requests.length, 2);
});

test('expired token refreshes through GET without an automatic query or write', async () => {
  const f = fixture(); const { task } = await f.lookup();
  f.fail(Object.assign(new Error('过期'), { code: 'comments.form_token_invalid' })); await new Promise(resolve => setImmediate(resolve));
  assert.equal(f.requests.length, 2); assert.equal(f.requests[1].method, undefined); assert.equal(f.requests[1].anonymous, true);
  f.complete({ submission: { form_token: 'renewed-token', open: true } }); await task;
  assert.equal(f.requests.length, 2); assert.equal(f.textarea().readOnly, true); assert.match(f.error().textContent, /再次查询/);
  const retry = await f.lookup(); assert.equal(f.requests.at(-1).body.form_token, 'renewed-token'); assert.equal(f.requests.at(-1).headers['Idempotency-Key'], submission.nonce);
  f.complete(receipt); await retry.task;
});

test('original member identity requires explicit login and never queries as a guest', async () => {
  const f = fixture({ pending: { ...submission, owner: 2 } }); f.query().focus();
  const { task } = await f.lookup(); await task;
  assert.equal(f.requests.length, 0); assert.equal(f.login().hidden, false); assert.equal(f.document.activeElement, f.login());
  await f.login().click(); assert.equal(f.signins(), 1);
  await f.session({ authenticated: true, id: 2 }); assert.equal(f.requests.length, 0);
  const retry = await f.lookup(); assert.equal(f.requests[0].body.expected_user_id, 2); f.complete(receipt); await retry.task;
});

test('guest identity cannot be replaced by a signed-in account', async () => {
  const f = fixture({ session: { authenticated: true, id: 2 } });
  const { task } = await f.lookup(); await task;
  assert.equal(f.requests.length, 0); assert.match(f.error().textContent, /退出当前账号/); assert.equal(f.storage.has(pendingKey), true);
});

test('login connection failure remains local to recovery and preserves both records', async () => {
  const f = fixture({ pending: { ...submission, owner: 2 }, loginFailure: true });
  const { task } = await f.lookup(); await task; await f.login().click();
  assert.equal(f.error().hidden, false); assert.match(f.error().textContent, /登录连接失败/);
  assert.equal(f.storage.has(pendingKey), true); assert.equal(f.storage.has(key), true); assert.equal(f.requests.length, 0);
});

test('server identity change and cancellation preserve records and do not steal outside focus', async () => {
  const f = fixture(); f.query().focus(); const { task } = await f.lookup();
  const outside = new Element('button', f.document); outside.focus();
  f.fail(Object.assign(new Error('会话已变化'), { name: 'AbortError' })); await task;
  assert.equal(f.document.activeElement, outside); assert.equal(f.storage.has(pendingKey), true); assert.equal(f.storage.has(key), true);
});

test('disabled public API leaves saved input readable but exposes no query action', async () => {
  const f = fixture({ api: false });
  assert.equal(f.query().hidden, true); assert.match(f.recovery().children[0].textContent, /暂不可用/);
  await f.query().click(); assert.equal(f.requests.length, 0); assert.equal(f.sessions(), 0);
  assert.equal(f.textarea().value, draft().content); assert.equal(f.storage.has(pendingKey), true);
});

test('disabled draft module and unavailable storage expose no stale recovery', () => {
  for (const options of [{ drafts: false }, { unavailable: true }]) {
    const f = fixture(options); assert.equal(f.draftPanel().hidden, true); assert.equal(f.recovery().hidden, true);
    assert.equal(f.storage.has(key), false); assert.equal(f.storage.has(pendingKey), false); assert.equal(f.requests.length, 0);
  }
});

test('expired or malformed records are discarded without affecting the other valid record', () => {
  const expired = fixture({ savedDraft: { ...draft(), at: Date.now() - 86400001 } });
  assert.equal(expired.draftPanel().hidden, true); assert.equal(expired.recovery().hidden, false);
  const invalid = fixture({ pending: { ...submission, owner: -1 } });
  assert.equal(invalid.recovery().hidden, true); assert.equal(invalid.draftPanel().hidden, false);
});

test('ending recovery requires explicit confirmation and keeps the independent draft', async () => {
  const f = fixture(); await f.end().click(); assert.equal(f.storage.has(pendingKey), true);
  f.confirm(true); await f.end().click(); assert.equal(f.storage.has(pendingKey), false); assert.equal(f.storage.has(key), true);
  assert.equal(f.requests.length, 0); assert.equal(f.document.activeElement, f.result());
});

test('clearing the saved input leaves the original submission available for lookup', async () => {
  const f = fixture(); await f.discard().click(); assert.equal(f.storage.has(key), true);
  f.confirm(true); await f.discard().click(); assert.equal(f.storage.has(key), false); assert.equal(f.storage.has(pendingKey), true);
  assert.equal(f.document.activeElement, f.query()); assert.equal(f.find('commentOriginal').textContent, submission.payload.content);
});
