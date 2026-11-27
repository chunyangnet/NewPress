const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { loadModule } = require('./helpers/typescript.cjs');
const notice = id => ({ id: 'a' + (id + 1), kind:'publication', url:'/posts/test', title: '通知 ' + id, created_at: new Date(1700000000000 + id * 1000).toISOString(), read: false });
const response = (items, more = false) => ({ items, next_cursor: more ? '0.1' : '-1.-1', has_more: more, unread: items.length, through: { message_id: 0, activity_id: 99 } });

function fixture() {
  const document = { activeElement: null }, nodes = new Map(), calls = [];
  class Node {
    constructor(cls = '') { this.className = cls; this.dataset = {}; this.children = []; this.scrollTop = 0; }
    contains(other) { return other === this || this.children.some(child => child.contains?.(other)); }
    matches(selector) { return selector === '[data-notification-id]' ? !!this.dataset.notificationId : this.className.split(' ').includes(selector.slice(1)); }
    closest(selector) { return this.matches(selector) ? this : this.parent?.closest(selector); }
    append(...items) { for (const child of items) { child.parent = this; this.children.push(child); } }
    replaceChildren(...items) { if (this.contains(document.activeElement)) document.activeElement = null; this.children = []; this.append(...items); }
    setAttribute(name, value) { this[name] = value; }
    querySelectorAll(selector) { return this.children.flatMap(child => [...(child.matches(selector) ? [child] : []), ...child.querySelectorAll(selector)]); }
    querySelector(selector) { return this.querySelectorAll(selector)[0]; }
    focus(options) { document.activeElement = this; this.focusOptions = options; }
  }
  const get = (selector, root) => root ? root.querySelector(selector) : nodes.get(selector);
  for (const key of ['[data-notification-list]', '[data-notification-more]', '[data-confirm-notification-clear]', '[data-notification-read-all]', '#interaction-confirm']) nodes.set(key, new Node());
  const filters = ['all', 'unread'].map(value => { const n = new Node(); n.dataset.notificationFilter = value; return n; });
  const state = { session: { authenticated: true, id: 1 }, epoch: 0, notices: [notice(3), notice(2), notice(1)], noticeFilter: 'all', noticeCursor: '0.2', noticeMore: true };
  const {createNoticeCenter} = loadModule('frontend/shared/interaction-notices.ts', {document, location:{assign(){}}, Error});
  const context = createNoticeCenter({state, notificationDrawer:{open:true}, badges(){}, announce(){}, openPeer: async()=>{}, setOverlay(){},
    dom:{ $:get, optional:get, $$:(selector,root)=>root?root.querySelectorAll(selector):filters,
      node:(_tag,cls='')=>new Node(cls), button:(_text,cls,action)=>{const n=new Node(cls);n.action=action;return n;},
      createIcon:()=>new Node(), dateLabel:value=>value, fullDate:value=>value, empty:container=>container.replaceChildren()},
    api:(url,options)=>new Promise((resolve,reject)=>calls.push({url,options,resolve,reject})),
  });
  context.renderNotices();
  return { state, document, calls, get, list: get('[data-notification-list]'), filters, reset:context.reset, update:context.updateNotices, render: () => context.renderNotices(), load: (...args) => context.loadNotices(...args) };
}

test('notification refresh restores the same keyboard action without scrolling', () => {
  const f = fixture(), row = f.list.children[1]; row.children[1].focus(); f.list.scrollTop = 120;
  f.state.notices.unshift(notice(4)); f.render();
  assert.equal(f.document.activeElement?.parent.dataset.notificationId, 'a3');
  assert.equal(f.document.activeElement.className, 'notification-mark-read');
  assert.equal(f.document.activeElement.focusOptions.preventScroll, true); assert.equal(f.list.scrollTop, 120);
});

test('removing a focused unread notice moves focus to its neighbor or current filter', () => {
  const f = fixture(); f.state.noticeFilter = 'unread'; f.list.children[1].children[1].focus();
  f.state.notices = f.state.notices.filter(item => item.id !== 'a3'); f.render();
  assert.equal(f.document.activeElement?.parent.dataset.notificationId, 'a2');
  assert.equal(f.document.activeElement.className, 'notification-open');
  f.state.notices = []; f.render(); assert.equal(f.document.activeElement, f.filters[1]);
});

test('background polling cannot supersede a pending next page or duplicate its request', async () => {
  const f = fixture(), pending = f.load(true);
  const quiet = f.load(false, true), duplicate = f.load(true);
  assert.equal(f.calls.length, 1); assert.match(f.calls[0].url, /before=0.2/);
  f.calls[0].resolve(response([notice(0)])); await Promise.all([pending, quiet, duplicate]);
  assert.deepEqual(Array.from(f.state.notices, item => item.id), ['a4', 'a3', 'a2', 'a1']);
});

test('an obsolete filter failure cannot replace a newer result with an error', async () => {
  const f = fixture(), old = f.load(); f.state.noticeFilter = 'unread'; const next = f.load();
  f.calls[1].resolve(response([notice(8)])); await next;
  f.calls[0].reject(new Error('old connection failed')); await assert.doesNotReject(old);
  assert.equal(f.state.notices[0].id, 'a9');
});

test('failed pagination preserves its boundary and allows retry; quiet reads resume afterwards', async () => {
  const f = fixture(), pending = f.load(true); f.calls[0].reject(new Error('offline')); await assert.rejects(pending, /offline/);
  assert.equal(f.state.noticeCursor, '0.2');
  const retry = f.load(true); assert.match(f.calls[1].url, /before=0.2/); f.calls[1].resolve(response([notice(0)])); await retry;
  const quiet = f.load(false, true); assert.equal(f.calls.length, 3); f.calls[2].resolve(response([notice(9)])); await quiet;
});

test('invalid notice pages preserve rendered rows and cursor before retry',async()=>{
  const f=fixture(),before=JSON.stringify(f.state.notices),pending=f.load(true);
  f.calls[0].resolve({...response([notice(0)],true),next_cursor:'0.2'});await assert.rejects(pending);
  assert.equal(JSON.stringify(f.state.notices),before);assert.equal(f.state.noticeCursor,'0.2');assert.equal(f.list.children.length,3);
});
test('bulk notice actions require acknowledgement before changing the visible list',async()=>{
  const f=fixture();f.state.noticeThrough={message_id:1,activity_id:3};const before=JSON.stringify(f.state.notices),pending=f.update('clear');
  await f.update('clear');assert.equal(f.calls.length,1);f.calls[0].resolve({ok:false});await assert.rejects(pending);
  assert.equal(JSON.stringify(f.state.notices),before);assert.equal(f.get('[data-confirm-notification-clear]').disabled,false);assert.equal(f.calls.length,1);
});
test('old notice writes cannot refresh or unlock a new identity operation',async()=>{
  const f=fixture();f.state.noticeThrough={message_id:1,activity_id:3};const old=f.update('clear');
  f.state.epoch++;f.state.session={authenticated:true,id:2};f.reset();const next=f.update('read_all');
  f.calls[0].resolve({ok:true});await old;assert.equal(f.calls.length,2);assert.equal(f.get('[data-confirm-notification-clear]').disabled,true);
  f.calls[1].resolve({ok:false});await assert.rejects(next);assert.equal(f.get('[data-confirm-notification-clear]').disabled,false);
});
