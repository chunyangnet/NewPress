'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {loadModule}=require('./helpers/typescript.cjs');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(){
  const state={epoch:0,session:{id:1,authenticated:true}},calls=[],saved=[],notices=[],connections=[],controls=new Map();
  const thread={owner:1,peer:2,contact:{id:2,name:'读者',username:'reader',available:true},messages:new Map(),loaded:true,hasMore:false,loading:false,readThrough:0,readSent:0,syncedThrough:1,draft:'',outbox:[]};
  const get=selector=>{if(!controls.has(selector))controls.set(selector,{value:'',hidden:false,disabled:false,dispatchEvent(){}});return controls.get(selector);};
  let refreshFailure=false;
  const controller=loadModule('frontend/shared/interaction-messages.ts',{Error,Event:class{},document:{hidden:false,querySelector:()=>null}}).createMessageSync({
    state,api:(url,options)=>new Promise((resolve,reject)=>calls.push({url,options,resolve,reject})),dom:{$:get,empty(){}},
    activeThread:t=>state.session.id===t.owner,renderThread(){},saveThread:t=>saved.push(JSON.stringify(t.outbox)),announce:message=>notices.push(message),
    loadInbox:async()=>{if(refreshFailure)throw new Error('refresh failed');},loadNotices:async()=>{},run:action=>Promise.resolve().then(action),
    connection:text=>connections.push(text),showToast(){},signIn:async()=>{},openPeer:async()=>{},nonce:()=> 'fixed-original-message-nonce',atBottom:()=>true,mobile:{matches:false},messageDrawer:{classList:{contains:()=>true}},
  });
  return {state,calls,saved,notices,connections,thread,refreshFailure:()=>{refreshFailure=true;},...controller};
}
const incoming=(id=2)=>({id,sender_id:2,recipient_id:1,content:'收到的消息',created_at:'2026-11-26T00:00:00Z',read_at:null});
const sent=(f,changes={})=>({message:{...incoming(3),sender_id:1,recipient_id:2,...f.calls[0].options.body,...changes}});
test('uncertain send acknowledgements retain the exact retry payload',async()=>{
  const f=fixture(),first=f.sendMessage(f.thread,'  原始消息  ');f.calls[0].resolve(sent(f,{nonce:'unrelated-request-nonce'}));await first;
  assert.equal(f.thread.outbox[0].status,'failed');assert.equal(f.thread.messages.size,0);const payload=f.calls[0].options.body;
  const retry=f.sendMessage(f.thread,'原始消息',payload.nonce);assert.deepEqual(f.calls[1].options.body,payload);f.calls[1].resolve(sent(f));await retry;assert.equal(f.thread.outbox.length,0);
});
test('confirmed sends remain delivered when the inbox refresh fails',async()=>{
  const f=fixture();f.refreshFailure();const send=f.sendMessage(f.thread,'发送成功');f.calls[0].resolve(sent(f));await send;
  assert.equal(f.thread.outbox.length,0);assert.equal(f.thread.messages.size,1);assert.match(f.connections[0],/消息已发送/);assert.doesNotMatch(f.notices.join(' '),/发送失败/);
});
test('unconfirmed read state is not marked read locally and retries its boundary',async()=>{
  const f=fixture();f.thread.messages.set(2,incoming());const first=f.markRead(f.thread);f.calls[0].resolve({ok:false});await first;
  assert.equal(f.thread.readSent,0);assert.equal(f.thread.messages.get(2).read_at,null);
  const retry=f.markRead(f.thread);assert.equal(f.calls[1].options.body.through,2);f.calls[1].resolve({ok:true});await retry;assert.equal(f.thread.readSent,2);assert.ok(f.thread.messages.get(2).read_at);
});
test('identity changes discard delayed send failures without restoring private storage',async()=>{
  const f=fixture(),send=f.sendMessage(f.thread,'旧账号消息');const before=f.saved.length;f.state.epoch++;f.state.session={id:3,authenticated:true};
  f.calls[0].reject(new Error('late network failure'));await send;assert.equal(f.saved.length,before);assert.equal(f.notices.length,0);
});
test('malformed message pages preserve the current map and forward boundary',async()=>{
  const f=fixture();f.thread.messages.set(1,incoming(1));const load=f.loadThread(f.thread);
  f.calls[0].resolve({items:[incoming(),{...incoming(3),recipient_id:4}],contact:f.thread.contact,has_more:false,next_cursor:3,read_through:0});await assert.rejects(load);
  assert.equal(f.thread.messages.size,1);assert.equal(f.thread.syncedThrough,1);
  await tick();if(f.calls[1]){f.calls[1].resolve({ok:false});await tick();}
});
