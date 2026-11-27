const test=require('node:test');
const assert=require('node:assert/strict');
const {loadModule}=require('./helpers/typescript.cjs');
function fixture(){
  const requests=[],timers=new Map();let id=0;
  const api=loadModule('frontend/core/appearance-preview-support.ts',{AbortController,DOMException,URLSearchParams,
    setTimeout(callback){const key=++id;timers.set(key,callback);return key;},clearTimeout(key){timers.delete(key);},
    fetch(url,options){return new Promise((resolve,reject)=>requests.push({url,options,reject,reply(value,status=200){resolve({status,json:async()=>value});},body(promise){resolve({status:200,json:()=>promise});}}));}
  });
  return {api,requests,timers};
}
test('preview response validates successful HTML/CSS and narrows error metadata',()=>{
  const {api}=fixture();
  for(const value of [null,[],{}, {html:[],css:''},{html:'',css:1}])assert.throws(()=>api.previewResponse(value,200));
  assert.deepEqual(JSON.parse(JSON.stringify(api.previewResponse({html:'<p>原文</p>',css:':root{color:red}'},200))),{ok:true,html:'<p>原文</p>',css:':root{color:red}'});
  const invalid=api.previewResponse({error:'invalid_fields',fields:{site_title:'必填'}},422);assert.equal(invalid.field,'site_title');assert.equal(invalid.ok,false);
  assert.equal(api.previewResponse({error:[],fields:[]},403).field,'');
});
test('an earlier timeout cannot abort the current request, even if its callback was queued',async()=>{
  const f=fixture(), a=new AbortController(),b=new AbortController();
  const first=f.api.fetchPreview('/preview?page=home',undefined,a.signal), obsoleteTimeout=[...f.timers.values()][0];
  const second=f.api.fetchPreview('/preview?page=article',undefined,b.signal);
  obsoleteTimeout();
  assert.equal(f.requests[0].options.signal.aborted,true);assert.equal(f.requests[1].options.signal.aborted,false);
  const rejected=assert.rejects(first,{name:'AbortError'});f.requests[0].reply({html:'old',css:''});await rejected;
  f.requests[1].reply({html:'new',css:''});assert.equal((await second).html,'new');assert.equal(f.timers.size,0);
});
test('closing a preview invalidates JSON still arriving after a successful fetch',async()=>{
  const f=fixture(), controller=new AbortController();let resolve;
  const pending=f.api.fetchPreview('/preview',new URLSearchParams('csrf_token=test&color=blue'),controller.signal);
  f.requests[0].body(new Promise(done=>resolve=done));await Promise.resolve();
  controller.abort();const rejected=assert.rejects(pending,{name:'AbortError'});resolve({html:'late',css:''});await rejected;
  assert.equal(f.requests[0].options.method,'POST');assert.equal(f.requests[0].options.credentials,'same-origin');
  assert.equal(f.requests[0].options.cache,'no-store');assert.equal(f.requests[0].options.redirect,'error');assert.equal(f.timers.size,0);
});
test('preview fetch failure and malformed success clear request timers and permit retry',async()=>{
  const f=fixture(), controller=new AbortController();
  const first=f.api.fetchPreview('/preview',undefined,controller.signal), rejected=assert.rejects(first,/offline/);
  f.requests[0].reject(new Error('offline'));await rejected;assert.equal(f.timers.size,0);
  const second=f.api.fetchPreview('/preview',undefined,controller.signal), invalid=assert.rejects(second,/预览响应/);
  f.requests[1].reply({html:null,css:''});await invalid;assert.equal(f.timers.size,0);
  const third=f.api.fetchPreview('/preview',undefined,controller.signal);f.requests[2].reply({error:'preview_article_missing'},404);
  assert.equal((await third).error,'preview_article_missing');assert.equal(f.requests[2].options.method,'GET');assert.equal(f.timers.size,0);
});
