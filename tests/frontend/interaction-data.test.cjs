'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { loadModule } = require('./helpers/typescript.cjs');
const tick = () => new Promise(resolve => setImmediate(resolve));
const data = loadModule('frontend/shared/interaction-data.ts');
const contact = {id:2,name:'读者',username:'reader',available:true};
const message = (id=2) => ({id,sender_id:2,recipient_id:1,content:'你好',created_at:'2026-11-26T00:00:00Z',read_at:null});
const page = () => ({items:[message()],contact,has_more:false,next_cursor:2,read_through:0});

test('message pages preserve descending history and ascending forward reads',()=>{
  assert.equal(data.parseMessages(page(),1,2).items[0].id,2);
  assert.equal(data.parseMessages({...page(),items:[message(4),message(3)],next_cursor:3},1,2,0,5).items.length,2);
  assert.equal(data.parseMessages({...page(),items:[message(3),message(4)],next_cursor:4},1,2,2).next_cursor,4);
});
for (const [name,mutate] of [
  ['missing list',p=>delete p.items], ['unsafe ID',p=>p.items[0].id=Number.MAX_SAFE_INTEGER+1],
  ['foreign recipient',p=>p.items[0].recipient_id=3], ['wrong contact',p=>p.contact={...contact,id:3}],
  ['invalid date',p=>p.items[0].created_at='yesterday'], ['missing read state',p=>delete p.items[0].read_at],
  ['wrong cursor',p=>p.next_cursor=9], ['repeated message',p=>p.items.push({...p.items[0]})],
  ['string boolean',p=>p.has_more='false'], ['empty advancing page',p=>{p.items=[];p.has_more=true;}],
]) test(`invalid message page: ${name}`,()=>{const p=page();mutate(p);assert.throws(()=>data.parseMessages(p,1,2));});
test('forward cursor validation rejects out-of-order or already seen items',()=>{
  assert.throws(()=>data.parseMessages({...page(),items:[message(3),message(2)],next_cursor:2},1,2,1));
  assert.throws(()=>data.parseMessages(page(),1,2,2));
  assert.throws(()=>data.parseMessages(page(),1,2,0,2));
});
test('sending must confirm the original content and nonce',()=>{
  const pending={nonce:'original-request-nonce',content:'hello'}, sent={...message(),sender_id:1,recipient_id:2,...pending};
  assert.equal(data.parseSentMessage({message:sent},1,2,pending).content,'hello');
  for(const change of [{nonce:'other-request-nonce'},{content:'different'},{sender_id:2,recipient_id:1}])assert.throws(()=>data.parseSentMessage({message:{...sent,...change}},1,2,pending));
});
test('corrupt or expired drafts cannot become outgoing requests',()=>{
  const now=Date.now(),valid={nonce:'saved-request-nonce',content:'草稿消息',created_at:'2026-11-26T00:00:00Z'};
  const restored=data.parseDraft({at:now,draft:'未发出的内容',outbox:[valid,valid,{...valid,nonce:'bad'},{...valid,nonce:'another-valid-nonce',content:{}}]},now);
  assert.equal(restored.draft,'未发出的内容');assert.equal(restored.outbox.length,1);assert.equal(restored.outbox[0].status,'failed');
  for(const at of [now+1,now-86400001,'now',NaN])assert.equal(data.parseDraft({at,draft:'expired',outbox:[valid]},now).outbox.length,0);
});
test('sessions and contacts reject ambiguous identities and pagination',()=>{
  for(const value of [{authenticated:'true'},{authenticated:true,id:0},{authenticated:true,id:1,can_admin:'true'}])assert.throws(()=>data.parseSocialSession(value));
  assert.equal(data.parseSocialSession({authenticated:false,id:3}).id,undefined);
  assert.throws(()=>data.parseContacts({items:[],page:1,has_more:true}));
  assert.throws(()=>data.parseContacts({items:[contact,contact],page:1,has_more:false}));
});
const notices = () => ({items:[{id:'a1',kind:'publication',title:'更新',url:'/posts/article',read:false,created_at:'2026-11-26T00:00:00Z'}],unread:1,has_more:false,next_cursor:'-1.-1',through:{message_id:0,activity_id:1}});
for(const url of ['https://other.test','//other.test','/\\other.test','/posts/\nmalformed'])test(`notification links reject ${JSON.stringify(url)}`,()=>{const p=notices();p.items[0].url=url;assert.throws(()=>data.parseNotices(p));});
test('notice IDs, counts and cursors must match the server contract',()=>{
  assert.equal(data.parseNotices(notices()).items.length,1);
  for(const change of [{next_cursor:'0.0.0'},{next_cursor:'99999999999999999999999.0'},{unread:-1},{has_more:true,items:[]}])assert.throws(()=>data.parseNotices({...notices(),...change}));
  const p=notices();p.items[0].id='a0';assert.throws(()=>data.parseNotices(p));
});

function inboxFixture(){
  const state={session:{id:1,authenticated:true},epoch:0,inbox:new Map(),threads:new Map(),inboxCursor:0,inboxMore:false,messageUnread:0},calls=[];
  const controller=loadModule('frontend/shared/interaction-inbox.ts').createInbox({state,render(){},badges(){},api:(url)=>new Promise((resolve,reject)=>calls.push({url,resolve,reject}))});
  return {state,calls,...controller};
}
const inboxPage = (id=10,peer=2,more=false) => ({items:[{peer_id:peer,contact:{...contact,id:peer},last:{...message(id),sender_id:peer},unread:1}],unread:1,has_more:more,next_cursor:id});
test('inbox pagination excludes duplicate refreshes and retains the oldest cursor',async()=>{
  const f=inboxFixture(),first=f.loadInbox();f.calls[0].resolve(inboxPage(10,2,true));await first;
  const next=f.loadInbox(true);await Promise.all([f.loadInbox(),f.loadInbox(true)]);assert.equal(f.calls.length,2);
  f.calls[1].resolve(inboxPage(8,3));await next;assert.equal(f.state.inbox.size,2);assert.equal(f.state.inboxCursor,8);
  const refresh=f.loadInbox();f.calls[2].resolve(inboxPage(11,2,true));await refresh;assert.equal(f.state.inboxCursor,8);
});
test('malformed inbox pages are atomic and retry from the same boundary',async()=>{
  const f=inboxFixture(),first=f.loadInbox();f.calls[0].resolve(inboxPage(10,2,true));await first;
  const next=f.loadInbox(true);f.calls[1].resolve(inboxPage(12,3,true));await assert.rejects(next);
  assert.equal(f.state.inbox.size,1);assert.equal(f.state.inboxCursor,10);
  const retry=f.loadInbox(true);assert.match(f.calls[2].url,/before=10/);f.calls[2].resolve(inboxPage(8,3));await retry;
});
test('inbox identity reset excludes stale data and obsolete unlocks',async()=>{
  const f=inboxFixture(),old=f.loadInbox();f.state.epoch++;f.state.session={id:4,authenticated:true};f.reset();const fresh=f.loadInbox();
  f.calls[0].resolve(inboxPage());await old;await f.loadInbox();assert.equal(f.calls.length,2);assert.equal(f.state.inbox.size,0);
  f.calls[1].resolve({items:[],unread:0,has_more:false,next_cursor:0});await fresh;
});

test('logout requires confirmation, coalesces clicks and ignores obsolete identity',async()=>{
  const state={epoch:0},calls=[],applied=[],cleared=[];
  const logout=loadModule('frontend/shared/interaction-session.ts').createSocialLogout({state,getSession:async()=>({authenticated:true,id:1}),api:()=>new Promise(resolve=>calls.push(resolve)),apply:value=>applied.push(value),clearDrafts:id=>cleared.push(id),completed(){}});
  const first=logout();assert.equal(logout(),first);await tick();calls[0]({ok:false});await assert.rejects(first);assert.equal(applied.length,0);
  const old=logout();await tick();state.epoch++;calls[1]({ok:true});await old;assert.equal(applied.length,0);assert.equal(cleared.length,0);
  const retry=logout();await tick();calls[2]({ok:true});await retry;assert.equal(applied.length,1);assert.deepEqual(cleared,[1]);
});
