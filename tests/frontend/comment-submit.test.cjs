'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

// Exercise the shipped comment form against a delayed response, allowing the
// visitor to continue typing or choose another reply target while it is pending.
const {loadModule}=require('./helpers/typescript.cjs');

class Element {
  constructor() {
    this.listeners = new Map();
    this.selectors = new Map();
    this.children = [];
    this.dataset = {};
    this.value = '';
    this.textContent = '';
    this.hidden = false;
    this.disabled = false;
    this.attributes = new Map();
  }
  addEventListener(type, handler) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(handler);
  }
  dispatchEvent(event) {
    return Promise.all((this.listeners.get(event.type) || []).map(handler => handler(event)));
  }
  click() { return this.dispatchEvent({ type: 'click' }); }
  querySelector(selector) { return this.selectors.get(selector) || null; }
  append(...children) { this.children.push(...children); }
  prepend(...children) { this.children.unshift(...children); }
  replaceChildren(...children) { this.children = children; }
  insertAdjacentElement(_position, child) { this.adjacent = child; }
  before(child) { this.previous = child; }
  after(child) { this.next = child; if (child.dataset?.commentRecovery !== undefined) this.recovery = child; }
  closest() { return null; }
  remove() {}
  setAttribute(key, value) { this.attributes.set(key, value); }
  removeAttribute(key) { this.attributes.delete(key); }
  getAttribute(key) { return this.attributes.get(key) ?? null; }
  contains(target) { return target === this || (this.controls || []).includes(target) || this.children.some(child => child.contains?.(target)); }
  querySelectorAll() { return this.controls || []; }
  setCustomValidity() {}
  reportValidity() { return true; }
  focus(options) { this.focusCount = (this.focusCount || 0) + 1; this.focusOptions = options; if (this.ownerDocument) this.ownerDocument.activeElement = this; }
  scrollIntoView() { this.scrollCount = (this.scrollCount || 0) + 1; }
}

function fixture({ requireLogin = false, draftsEnabled = true, retrySupported = false, stored = [], initialSession = { authenticated: false }, storageFailure = false } = {}) {
  const elements = new Map();
  const document = new Element(), window = new Element(), toasts = [];
  let currentSession = initialSession;
  const get = (selector) => {
    if (!elements.has(selector)) elements.set(selector, Object.assign(new Element(), { ownerDocument: document }));
    return elements.get(selector);
  };
  const form = get('[data-comment-form]');
  const input = get('[data-comment-content]');
  const parent = get('[data-comment-parent]');
  const replyLabel = get('[data-comment-reply-label]');
  const replyContext = get('[data-comment-reply-context]');
  const submitButton = get('button[type="submit"]');
  form.dataset = { commentPostId: '7', commentLoginRequired: String(requireLogin), commentLocked: String(requireLogin), commentDrafts: String(draftsEnabled), commentApi: '/api/v1/posts/test/comments', commentDepth: '5', commentReplies: 'true' };
  form.elements = Object.fromEntries(['form_token', 'author_name', 'email', 'website'].map(name => [name, Object.assign(new Element(),{value:name})]));
  form.controls = [input, submitButton, ...Object.values(form.elements)];
  form.elements.namedItem=name=>form.elements[name];
  parent.value = '0'; replyContext.hidden = true;
  get('[data-comment-reply-cancel]').addEventListener('click', () => {
    parent.value = '0'; replyLabel.textContent = ''; replyContext.hidden = true;
  });
  const parentComment = new Element();
  parentComment.className = 'comment comment-depth-1';
  parentComment.selectors.set('.comment-main', new Element());
  const drafts = new Map(stored);
  const requests = [];
  get('[data-comment-login-gate]').selectors.set('[data-account-login]', get('[data-account-login]'));
	document.getElementById = id => id === 'comment-10' ? parentComment : null;
	document.createTextNode = text => ({ textContent: text });
  const context = vm.createContext({
    $: (selector, root) => selector === '[data-comment-retry-supported]' ? (retrySupported ? get(selector) : null) : root === parentComment ? root.querySelector(selector) : selector === '.comment-notice' ? null : get(selector),
    node(tag, className = '', text = '') { const element = new Element(); element.tagName = tag; element.className = className; element.textContent = text; return element; },
    document,HTMLInputElement:Element,
	getSession: async () => currentSession,
    storage: { get: key => drafts.get(key), set: (key, value) => { if (!storageFailure) drafts.set(key, JSON.parse(JSON.stringify(value))); }, remove: key => drafts.delete(key) },
    location: { pathname: '/posts/test', search: '' },
    crypto: require('node:crypto').webcrypto, URLSearchParams, queueMicrotask, Event: class { constructor(type) { this.type = type; } },
    api: (url, options) => new Promise((resolve, reject) => { requests.push({ url, ...options, resolve, reject }); }),
    createIcon: () => new Element(), showToast(message) { toasts.push(message); }, emoji: { fragment: text => ({ textContent: text }) },
    matchMedia: () => ({ matches: true }), window,
  });
  loadModule('frontend/shared/comments.ts',context).mountComments({...context,dom:{$:context.$,optional:context.$,node:context.node}});
  const draftKey = 'chunnel.comment./posts/test';
  return {
    form, get, document, window, toasts, session: value => { currentSession = value; return document.dispatchEvent({type:'chunnel:session',detail:value}); },
    input, parent, replyLabel, replyContext, submitButton, requests, parentComment, drafts,
    retry: () => form.recovery.children[1].click(),
    recovery: () => form.recovery,
    complete: value => requests.at(-1).resolve(value), fail: error => requests.at(-1).reject(error),
    type(value) { input.value = value; input.dispatchEvent({ type: 'input' }); },
    reply(id, label) { parent.value = String(id); replyLabel.textContent = label; replyContext.hidden = false; form.dispatchEvent({ type: 'click' }); },
    submit: () => form.dispatchEvent({ type: 'submit', preventDefault() {} }),
    pagehide: () => window.dispatchEvent({ type: 'pagehide' }),
    draft: () => drafts.get(draftKey),
    note: () => get('.comment-body-field').adjacent.children[0].textContent,
    comments: () => get('.comment-list').children,
    feedback: () => get('.comment-form-panel').children.find(child => 'commentError' in child.dataset),
  };
}

test('signed-in comments use the account label without collecting email and participate in session cancellation', async () => {
  const f = fixture(); await Promise.resolve();
  await f.session({authenticated:true,id:2,name:'账号读者'});
  assert.equal(f.get('[data-comment-identity]').hidden,true);
  assert.equal(f.get('[data-comment-account-name]').textContent,'账号读者');
  assert.equal(f.form.elements.email.required,false); assert.equal(f.form.elements.email.value,'');
  f.type('Account-owned comment'); const pending = f.submit();
  assert.equal(f.requests[0].anonymous,undefined);
  f.complete({status:'pending'}); await pending;
  await f.session({authenticated:false});
  assert.equal(f.get('[data-comment-identity]').hidden,false); assert.equal(f.form.elements.email.required,true);
});

test('successful submission preserves text entered while the response was pending', async () => {
  const f = fixture();
  f.type('First comment');
  const pending = f.submit();
  assert.equal(f.submitButton.disabled, true);
  f.type('A second thought, still being written');
  f.complete({ status: 'pending' });
  await pending;
  assert.equal(f.requests[0].body.content, 'First comment');
  assert.equal(f.input.value, 'A second thought, still being written');
  assert.equal(f.draft().content, f.input.value);
  assert.match(f.note(), /已保留/);
  assert.equal(f.submitButton.disabled, false);
  await f.pagehide();
  assert.equal(f.draft().content, f.input.value);
});

for (const switchBack of [false, true]) test(`late receipt preserves the original request after an identity change (return=${switchBack})`, async () => {
  const f = fixture({retrySupported:true,initialSession:{authenticated:true,id:2,name:'Original'}});
  await Promise.resolve();
  f.type('Still composing the same text');
  const task = f.submit();
  await new Promise(resolve => setImmediate(resolve));
  const nonce = f.requests[0].headers['Idempotency-Key'];
  await f.session({authenticated:true,id:3,name:'Next'});
  if (switchBack) await f.session({authenticated:true,id:2,name:'Original'});
  f.complete({status:'pending',submission_key:nonce}); await task;
  assert.equal(f.input.value,'Still composing the same text');
  assert.equal(f.draft().content,f.input.value);
  assert.equal(f.drafts.get('chunnel.comment./posts/test.submission').nonce,nonce);
  assert.equal(f.recovery().hidden,false);
  assert.match(f.recovery().children.at(-1).textContent,/账号已变化/);
  assert.equal(f.submitButton.disabled,false);
  assert.deepEqual(f.toasts,[]);
  await f.session({authenticated:true,id:2,name:'Original'});
  const retry = f.retry(); await new Promise(resolve => setImmediate(resolve));
  assert.equal(f.requests[1].headers['Idempotency-Key'],nonce);
  f.complete({status:'pending',submission_key:nonce,replayed:true}); await retry;
  assert.equal(f.input.value,'');
});

test('a late rejection from another identity cannot discard the uncertain original request', async () => {
  const f=fixture({retrySupported:true,initialSession:{authenticated:true,id:2}});await Promise.resolve();
  f.type('Uncertain original');const task=f.submit();await new Promise(resolve=>setImmediate(resolve));
  const nonce=f.requests[0].headers['Idempotency-Key'];await f.session({authenticated:true,id:3});
  f.fail(Object.assign(new Error('Old rejection'),{code:'comments.validation_failed'}));await task;
  assert.equal(f.drafts.get('chunnel.comment./posts/test.submission').nonce,nonce);
  assert.match(f.recovery().children.at(-1).textContent,/账号已变化/);
});

test('successful submission clears an unchanged draft and reply target', async () => {
  const f = fixture();
  f.type('Reply to the original comment');
  f.reply(10, '正在回复 Original');
  const pending = f.submit();
  f.complete({ status: 'pending' });
  await pending;
  assert.equal(f.input.value, '');
  assert.equal(f.parent.value, '0');
  assert.equal(f.replyContext.hidden, true);
  assert.equal(f.draft(), undefined);
});

test('changing only the reply target retains the current text and target', async () => {
  const f = fixture();
  f.type('Same words, different recipient');
  f.reply(10, '正在回复 Original');
  const pending = f.submit();
  f.reply(20, '正在回复 Next');
  f.complete({ status: 'pending' });
  await pending;
  assert.equal(f.requests[0].body.parent_id, 10);
  assert.equal(f.parent.value, '20');
  assert.equal(f.replyLabel.textContent, '正在回复 Next');
  assert.equal(f.replyContext.hidden, false);
  assert.equal(f.draft().parent, '20');
  assert.equal(f.draft().content, 'Same words, different recipient');
});

test('approved comment renders its submitted text under its original parent', async () => {
  const f = fixture();
  f.type('Submitted reply');
  f.reply(10, '正在回复 Original');
  const pending = f.submit();
  f.type('Next reply');
  f.reply(20, '正在回复 Next');
  f.complete({ status: 'approved', comment: { id: 30, post_id: 7, parent_id: 10, author_name: 'Reader' } });
  await pending;
  const children = f.parentComment.querySelector('.comment-main').children[0];
  assert.equal(children.children[0].id, 'comment-30');
  const main = children.children[0].children[1];
  assert.equal(main.children[1].children[0].textContent, 'Submitted reply');
  assert.equal(f.comments().length, 0);
  assert.equal(f.input.value, 'Next reply');
  assert.equal(f.parent.value, '20');
});

test('login gate blocks guest submit, unlocks without losing draft, and relocks on logout', async () => {
  const f = fixture({requireLogin:true}); await Promise.resolve();
  f.type('Preserved draft');
  assert.equal(f.form.inert,true); assert.equal(f.input.disabled,true);
  await f.submit(); assert.equal(f.requests.length,0);
  await f.session({authenticated:true,id:2,name:'读者'});
  assert.equal(f.form.inert,false); assert.equal(f.input.disabled,false); assert.equal(f.input.value,'Preserved draft');
  await f.session({authenticated:false});
  assert.equal(f.form.inert,true); assert.equal(f.submitButton.disabled,true); assert.equal(f.draft().content,'Preserved draft');
});

test('disabled draft module never persists the composer content', async () => {
  const f=fixture({draftsEnabled:false}); f.type('Do not store'); await f.pagehide();
  assert.equal(f.draft(),undefined); assert.equal(f.get('.comment-body-field').adjacent.hidden,true);
});

test('an approved response preserves the viewport and offers an explicit jump to the posted comment', async () => {
  const f=fixture();f.type('正在提交的评论');const pending=f.submit();f.type('还在编辑的下一条');
  f.complete({status:'approved',comment:{id:88,post_id:7,author_name:'读者'}});await pending;
  assert.equal(f.comments()[0].scrollCount || 0,0);
  assert.equal(f.input.value,'还在编辑的下一条');
  const jump=(f.form.next || f.form.previous).children.find(node=>node.tagName==='a');assert.ok(jump);
  assert.equal(jump.href,'#comment-88');
});

test('session refresh cannot unlock an in-flight comment submit button', async () => {
  const f=fixture({requireLogin:true});await Promise.resolve();await f.session({authenticated:true,id:2,name:'读者'});
  f.type('仍在提交');const pending=f.submit();
  await f.session({authenticated:true,id:2,name:'读者'});
  assert.equal(f.submitButton.disabled,true);
  f.complete({status:'pending'});await pending;assert.equal(f.submitButton.disabled,false);
});

const approvedReceipt = { status: 'approved', comment: { id: 30, post_id: 7, parent_id: 10, author_name: 'Reader' } };
for (const [name, response] of [
  ['missing status', {}],
  ['unknown status', { status: 'success' }],
  ['approved without comment', { status: 'approved' }],
  ['approved with null comment', { status: 'approved', comment: null }],
  ['non-text notice', { ...approvedReceipt, notice: { text: 'Published' } }],
  ['string id', { ...approvedReceipt, comment: { ...approvedReceipt.comment, id: '30' } }],
  ['unsafe id', { ...approvedReceipt, comment: { ...approvedReceipt.comment, id: Number.MAX_SAFE_INTEGER + 1 } }],
  ['zero id', { ...approvedReceipt, comment: { ...approvedReceipt.comment, id: 0 } }],
  ['wrong article', { ...approvedReceipt, comment: { ...approvedReceipt.comment, post_id: 8 } }],
  ['missing article', { ...approvedReceipt, comment: { ...approvedReceipt.comment, post_id: undefined } }],
  ['wrong parent', { ...approvedReceipt, comment: { ...approvedReceipt.comment, parent_id: 20 } }],
  ['missing reply parent', { ...approvedReceipt, comment: { ...approvedReceipt.comment, parent_id: undefined } }],
  ['non-text author', { ...approvedReceipt, comment: { ...approvedReceipt.comment, author_name: { label: 'Reader' } } }],
  ['empty author', { ...approvedReceipt, comment: { ...approvedReceipt.comment, author_name: '  ' } }],
  ['invalid pending comment', { status: 'pending', comment: { ...approvedReceipt.comment, id: -1 } }],
]) {
  test(`an unconfirmed comment receipt (${name}) preserves the draft and reply without showing success`, async () => {
    const f = fixture(); f.type('保留原评论'); f.reply(10, '正在回复 Original'); const pending = f.submit();
    f.complete(response); await pending;
    assert.equal(f.input.value, '保留原评论'); assert.equal(f.parent.value, '10');
    assert.equal(f.draft().content, '保留原评论'); assert.equal(f.draft().parent, '10');
    assert.equal(f.comments().length, 0); assert.equal(f.toasts.length, 0);
    assert.equal(f.feedback().hidden, false); assert.match(f.feedback().textContent, /未确认/);
    assert.equal(f.submitButton.disabled, false);
  });
}

test('an invalid receipt preserves a newer draft and its changed reply target', async () => {
  const f = fixture(); f.type('正在提交'); const pending = f.submit(); f.type('新的内容'); f.reply(10, '新的回复对象');
  f.complete({ status: 'approved' }); await pending;
  assert.equal(f.input.value, '新的内容'); assert.equal(f.parent.value, '10');
  assert.equal(f.draft().content, '新的内容'); assert.equal(f.toasts.length, 0);
});

test('valid pending receipts with or without a comment confirm the unchanged draft', async () => {
  for (const comment of [undefined, null, { id: 31, post_id: 7, author_name: 'Reader' }]) {
    const f = fixture(); f.type('等待审核'); const pending = f.submit();
    f.complete({ status: 'pending', notice: '已收到，等待审核', comment }); await pending;
    assert.equal(f.input.value, ''); assert.equal(f.draft(), undefined); assert.equal(f.comments().length, 0);
    assert.equal(f.toasts.length, 1); assert.equal(f.feedback().hidden, true);
  }
});

test('a newly required login activates the existing mask, retains text and allows explicit signed-in resubmission', async () => {
  const f = fixture(); await Promise.resolve(); f.type('设置变化时仍然保留'); f.reply(10, '正在回复 Original'); f.input.focus();
  const pending = f.submit(); f.fail(Object.assign(new Error('请先登录'), { code: 'comments.login_required' })); await pending;
  assert.equal(f.form.dataset.commentLoginRequired, 'true'); assert.equal(f.form.inert, true);
  assert.equal(f.form.getAttribute('aria-hidden'), 'true'); assert.equal(f.get('[data-comment-login-gate]').hidden, false);
  assert.equal(f.input.value, '设置变化时仍然保留'); assert.equal(f.parent.value, '10'); assert.equal(f.submitButton.disabled, true);
  assert.equal(f.document.activeElement, f.get('[data-account-login]')); assert.equal(f.get('[data-account-login]').focusOptions.preventScroll, true);
  await f.submit(); assert.equal(f.requests.length, 1);
  await f.session({ authenticated: true, id: 2, name: '读者' });
  assert.equal(f.form.inert, false); assert.equal(f.get('[data-comment-login-gate]').hidden, true);
  assert.equal(f.input.value, '设置变化时仍然保留'); assert.equal(f.requests.length, 1);
  const retry = f.submit(); f.complete({ status: 'pending' }); await retry;
  assert.equal(f.requests.length, 2); assert.equal(f.input.value, '');
});

test('locking the comment form does not steal focus from another part of the page', async () => {
  const f = fixture(); await Promise.resolve(); f.type('正在提交'); const pending = f.submit();
  const other = f.get('outside-control'); other.focus();
  f.fail(Object.assign(new Error('请先登录'), { code: 'comments.login_required' })); await pending;
  assert.equal(f.form.inert, true); assert.equal(f.document.activeElement, other); assert.equal(f.input.value, '正在提交');
});

test('success notice follows the composer so a wrapped receipt cannot displace an active draft', async () => {
  const f = fixture(); f.type('原评论'); const pending = f.submit(); f.type('继续编辑的新评论');
  f.complete({ status: 'approved', comment: { id: 32, post_id: 7, author_name: 'Reader' } }); await pending;
  assert.equal(f.form.previous, undefined); assert.equal(f.form.next?.className, 'comment-notice comment-notice-success');
  assert.equal(f.input.value, '继续编辑的新评论');
});


const pendingStorageKey = 'chunnel.comment./posts/test.submission';
const tick = () => new Promise(resolve => setImmediate(resolve));
async function retryFixture(options = {}) {
  const f = fixture({ retrySupported: true, ...options });
  await tick(); f.form.elements.website.value = '';
  return f;
}
function receipt(f, value = { status: 'pending' }) {
  f.complete({ submission_key: f.requests.at(-1).headers['Idempotency-Key'], ...value });
}

test('lost response retries the original payload and preserves newer text and reply', async () => {
  const f = await retryFixture(); f.type('Original'); f.reply(10,'Original parent');
  let sending = f.submit(); const original = f.requests[0];
  assert.match(original.headers['Idempotency-Key'], /^[a-zA-Z0-9._:-]{8,128}$/);
  assert.equal(original.body.expected_user_id,0);
  f.type('New draft'); f.reply(20,'New parent'); f.fail(new Error('Network lost')); await sending;
  assert.equal(f.recovery().hidden,false); assert.equal(f.submitButton.disabled,false);
  await f.submit(); assert.equal(f.requests.length,1, 'a new draft cannot silently replace the unknown request');
  sending = f.retry(); assert.equal(f.requests.length,2);
  assert.equal(f.requests[1].headers['Idempotency-Key'],original.headers['Idempotency-Key']);
  assert.equal(f.requests[1].body.content,'Original'); assert.equal(f.requests[1].body.parent_id,10);
  receipt(f,{status:'pending',replayed:true}); await sending;
  assert.equal(f.input.value,'New draft'); assert.equal(f.parent.value,'20');
  assert.equal(f.recovery().hidden,true); assert.equal(f.drafts.has(pendingStorageKey),false);
});

test('refresh restores the same pending request without automatically sending', async () => {
  const original = await retryFixture(); original.type('Original');
  const sending = original.submit(); original.type('New draft'); await original.pagehide();
  const saved = [...original.drafts.entries()]; original.fail(new Error('Lost')); await sending;
  const f = await retryFixture({stored:saved});
  assert.equal(f.requests.length,0); assert.equal(f.input.value,'New draft');
  const retry = f.retry(); assert.equal(f.requests[0].headers['Idempotency-Key'],original.requests[0].headers['Idempotency-Key']);
  assert.equal(f.requests[0].body.content,'Original'); receipt(f); await retry;
  assert.equal(f.input.value,'New draft');
});

test('replaying a published receipt never appends stale submitted text or increments the list', async () => {
  const f = await retryFixture(); f.type('Before moderation');
  let sending=f.submit(); f.fail(new Error('Lost')); await sending;
  sending=f.retry(); receipt(f,{status:'approved',replayed:true,comment:{id:81,post_id:7,author_name:'Edited author',content_html:'<p>Edited body</p>'}}); await sending;
  assert.equal(f.comments().length,0); assert.equal(f.input.value,'');
  assert.equal(f.form.next.children.find(item=>item.tagName==='a').href,'/posts/test?comments_page=1#comments');
});

test('a deleted or hidden submission can be confirmed without recreating the comment', async () => {
  const f = await retryFixture(); f.type('Deleted later'); let sending=f.submit(); f.fail(new Error('Lost')); await sending;
  sending=f.retry(); receipt(f,{status:'received',replayed:true,notice:'已确认此前提交，评论当前未公开显示。'}); await sending;
  assert.equal(f.comments().length,0); assert.equal(f.input.value,''); assert.equal(f.recovery().hidden,true);
});

for (const invalid of [
  {status:'pending'},
  {status:'pending',submission_key:'wrong-key'},
  {status:'pending',replayed:'true'},
  {status:'received'},
  {status:'received',replayed:true,comment:{id:1}},
]) test('untrusted atomic receipt retains request: '+JSON.stringify(invalid), async () => {
  const f=await retryFixture();f.type('Keep this');const sending=f.submit();
  if (Object.keys(invalid).length===1 && invalid.status==='pending') f.complete(invalid); else receipt(f,invalid);
  await sending; assert.equal(f.input.value,'Keep this'); assert.equal(f.recovery().hidden,false); assert.ok(f.drafts.get(pendingStorageKey));
});

test('renewing a form token preserves the original key and request for explicit retry', async () => {
  const f=await retryFixture();f.type('Original');let sending=f.submit();const nonce=f.requests[0].headers['Idempotency-Key'];
  f.fail(Object.assign(new Error('Expired'),{code:'comments.form_token_invalid'}));await tick();
  assert.equal(f.requests[1].method,undefined);assert.equal(f.requests[1].anonymous,true);
  f.complete({submission:{form_token:'renewed'}});await sending;
  assert.equal(f.requests.length,2);assert.equal(f.form.elements.form_token.value,'renewed');
  sending=f.retry();assert.equal(f.requests[2].headers['Idempotency-Key'],nonce);assert.equal(f.requests[2].body.form_token,'renewed');receipt(f);await sending;
});

test('definitive rejection of the first attempt allows a corrected new submission', async () => {
  const f=await retryFixture();f.type('Invalid');let sending=f.submit();const nonce=f.requests[0].headers['Idempotency-Key'];
  f.fail(Object.assign(new Error('Invalid'),{code:'comments.validation_failed',details:{fields:{content:'Too long'}}}));await sending;
  assert.equal(f.recovery().hidden,true);f.type('Corrected');sending=f.submit();assert.notEqual(f.requests[1].headers['Idempotency-Key'],nonce);receipt(f);await sending;
});

test('a rejected retry cannot release an earlier uncertain identity', async () => {
  const f=await retryFixture();f.type('Unknown');let sending=f.submit();f.fail(new Error('Lost'));await sending;
  const nonce=f.requests[0].headers['Idempotency-Key'];sending=f.retry();f.fail(Object.assign(new Error('Parent gone'),{code:'comments.invalid_parent'}));await sending;
  assert.equal(f.drafts.get(pendingStorageKey).nonce,nonce);await f.submit();assert.equal(f.requests.length,2);
});

test('switching accounts blocks recovery until the original identity returns', async () => {
  const f=await retryFixture({initialSession:{authenticated:true,id:9,name:'Original'}});f.type('Owned');let sending=f.submit();f.fail(new Error('Lost'));await sending;
  await f.session({authenticated:true,id:10,name:'Other'});await f.retry();assert.equal(f.requests.length,1);assert.match(f.feedback().textContent,/原提交身份/);
  await f.session({authenticated:true,id:9,name:'Renamed'});sending=f.retry();assert.equal(f.requests[1].body.expected_user_id,9);receipt(f);await sending;
});

test('disabling drafts keeps retry only in memory and does not write original email or text', async () => {
  const f=await retryFixture({draftsEnabled:false});f.type('Transient');let sending=f.submit();f.fail(new Error('Lost'));await sending;
  assert.equal(f.drafts.size,0);assert.match(f.recovery().children[0].textContent,/仅保留在当前页面/);
  sending=f.retry();assert.equal(f.requests[1].headers['Idempotency-Key'],f.requests[0].headers['Idempotency-Key']);receipt(f);await sending;
});

test('unavailable session storage keeps an in-memory retry and explains its recovery scope', async () => {
  const f=await retryFixture({storageFailure:true});f.type('Transient');const sending=f.submit();f.fail(new Error('Lost'));await sending;
  assert.equal(f.drafts.size,0);assert.match(f.recovery().children[0].textContent,/不要刷新/);
});

test('ending an uncertain recovery requires an explicit acknowledgement and keeps the draft', async () => {
  const f=await retryFixture();f.type('Unknown');const sending=f.submit();f.fail(new Error('Lost'));await sending;
  f.window.confirm=()=>false;await f.recovery().children[2].click();assert.equal(f.recovery().hidden,false);
  let message='';f.window.confirm=text=>{message=text;return true;};await f.recovery().children[2].click();
  assert.match(message,/可能重复/);assert.equal(f.recovery().hidden,true);assert.equal(f.input.value,'Unknown');
});

test('repeat clicks during a pending confirmation do not create more requests', async () => {
  const f=await retryFixture();f.type('Unknown');let sending=f.submit();await f.retry();await f.submit();assert.equal(f.requests.length,1);f.fail(new Error('Lost'));await sending;
  sending=f.retry();await f.retry();await f.submit();assert.equal(f.requests.length,2);receipt(f);await sending;
});


test('confirmation restores focus after disabling its button but respects newer focus', async () => {
  for (const moved of [false,true]) {
    const f=await retryFixture();f.type('Unknown');let sending=f.submit();f.fail(new Error('Lost'));await sending;
    const button=f.recovery().children[1];button.ownerDocument=f.document;button.focus();
    f.document.body=new Element();sending=f.retry();f.document.activeElement=moved?f.get('outside-control'):f.document.body;
    receipt(f);await sending;
    assert.equal(f.document.activeElement,moved?f.get('outside-control'):f.input);
    if(!moved)assert.deepEqual({...f.input.focusOptions},{preventScroll:true});
  }
});


test('newly published comments render local flag chips and reject unsafe flag paths', async () => {
  for (const code of ['jp', '', '../us', 'https://evil.test/us', 'US']) {
    const f = fixture(); f.type('Flag example'); const pending = f.submit();
    f.complete({status:'approved', comment:{id:88, post_id:7, author_name:'读者', origin:{location:'东京', precision:'city', flag:code, device:'Windows · Edge', kind:'desktop'}}}); await pending;
    const all = element => [element, ...(element.children || []).flatMap(all)];
    const nodes = all(f.comments()[0]);
    const flags = nodes.filter(node => node.className === 'comment-origin-flag');
    assert.equal(flags.length, code === 'jp' ? 1 : 0);
    if (flags.length) { assert.equal(flags[0].src, '/plugin-assets/comment-origin/flags/jp.svg'); assert.equal(flags[0].alt, ''); }
    const location = nodes.find(node => node.className === 'comment-origin-location');
    assert.equal(location.dataset.precision, 'city');
    assert.ok(nodes.some(node => node.className === 'comment-origin-value' && node.textContent === '东京'));
    assert.ok(nodes.some(node => node.className === 'comment-origin-device'));
  }
});
