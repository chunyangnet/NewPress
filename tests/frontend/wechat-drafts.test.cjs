'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs'), path = require('node:path'), vm = require('node:vm');
const {compile}=require('./helpers/typescript.cjs');
const code=process.env.CHUNNEL_WECHAT_DRAFTS_SOURCE?fs.readFileSync(process.env.CHUNNEL_WECHAT_DRAFTS_SOURCE,'utf8'):compile('frontend/core/wechat-drafts.ts');
const key = 'newpress.wechat.collect.42';
const choices = [{ key:'article-one', digest:'a'.repeat(64), selected:true }];
function setup() {
  const memory = new Map(); let now = 1800000000000, fail = false;
  const window = { crypto:require('node:crypto').webcrypto, sessionStorage:{getItem:k=>memory.get(k)||null,setItem:(k,v)=>{if(fail)throw Error('quota');memory.set(k,v);},removeItem:k=>memory.delete(k)} };
  vm.runInNewContext(code, {window,TextEncoder,Uint8Array,Date:{now:()=>now}});
  return { memory, window, store: window.ChunnelWeChatDrafts.create(42), clock:delta=>{now+=delta;},fail:()=>{fail=true;} };
}
test('collection snapshots round-trip only whitelisted input and fingerprints', () => {
  const f=setup(); f.store.save('https://mp.weixin.qq.com/s/one','baseline',[{...choices[0],body:'PRIVATE BODY',receipt:'SIGNED TOKEN',app_secret:'SECRET'}],true);
  const raw=f.memory.get(key); assert.doesNotMatch(raw,/PRIVATE BODY|SIGNED TOKEN|SECRET|receipt|app_secret/);
  const record=f.window.ChunnelWeChatDrafts.create(42).read();assert.equal(record.choices[0].selected,true);assert.equal(record.uncertain,true);
  assert.equal(f.window.ChunnelWeChatDrafts.create(43).read(),null);
});
test('viewing and identical writes do not extend a saved collection expiry', () => {
  const f=setup(),first=f.store.save('link','baseline',choices,false);f.clock(10000);f.store.read();
  assert.equal(f.store.save('link','baseline',choices,false).at,first.at);
  f.clock(86400000);assert.equal(f.store.read(),null);assert.equal(f.memory.has(key),false);
});
test('failed replacement keeps the last successfully saved snapshot', () => {
  const f=setup();f.store.save('original','baseline',choices,false);const raw=f.memory.get(key);f.fail();
  assert.throws(()=>f.store.save('new input','baseline',choices,false),/quota/);assert.equal(f.memory.get(key),raw);
});

test('an unchanged save expires the old snapshot without silently renewing it', () => {
  const f=setup();f.store.save('link','baseline',choices,false);f.clock(86400000);
  assert.equal(f.store.save('link','baseline',choices,false),null);assert.equal(f.memory.has(key),false);
  assert.ok(f.store.save('new link','baseline',choices,false));
});
test('malformed, oversized, future and foreign-account records are removed', () => {
  const f=setup();const valid=f.store.save('link','baseline',choices,false);
  for(const record of [{...valid,owner:43},{...valid,at:valid.at+60001},{...valid,input:'x'.repeat(22001)},{...valid,choices:[...choices,...choices]},{...valid,choices:[{...choices[0],digest:'bad'}]}]){
    f.memory.set(key,JSON.stringify(record));assert.equal(f.store.read(),null);assert.equal(f.memory.has(key),false);
  }
  for(const raw of ['{broken','x'.repeat(65537)]){f.memory.set(key,raw);assert.equal(f.store.read(),null);}
});
test('fingerprints detect every preview-visible change and ignore import receipts', async () => {
  const f=setup(),a={key:'one',url:'https://mp.weixin.qq.com/s/firstx',title:'标题',author:'作者',excerpt:'摘要',body:'正文',cover:'封面',preview:'预览',images:['image']};
  const hash=async a=>(await f.window.ChunnelWeChatDrafts.fingerprints([a])).get('one');const first=await hash(a);
  assert.match(first,/^[a-f0-9]{64}$/);assert.equal(await hash({...a,receipt:'new-token'}),first);
  for(const field of ['url','title','author','excerpt','body','cover','preview','images'])assert.notEqual(await hash({...a,[field]:field==='images'?['different']:a[field]+'changed'}),first,field);
  f.window.crypto={};assert.equal((await f.window.ChunnelWeChatDrafts.fingerprints([a])).size,0);
});
