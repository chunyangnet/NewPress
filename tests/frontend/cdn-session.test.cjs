const test=require('node:test'),assert=require('node:assert/strict');
const {loadModule}=require('./helpers/typescript.cjs');
class DetailEvent extends Event {constructor(type,detail){super(type);this.detail=detail;}}
function fixture(id='0',readOnly=false){
 const page=new EventTarget(),window=new EventTarget(),document=new EventTarget(),calls=[],changes=[];let resumes=0;
 page.dataset={userId:id,csrf:'fixture-csrf',readonly:String(readOnly)};page.contains=region=>region===page;
 const {CDNSession}=loadModule('frontend/shared/cdn-session.ts',{window,document,AbortController,Event,structuredClone,setTimeout,clearTimeout,fetch:(url,options)=>new Promise((resolve,reject)=>calls.push({url,options,resolve,reject}))});
 const session=new CDNSession(page,page,'/cdn',lost=>changes.push(lost),()=>resumes++);
 const respond=(call,data={},status=200)=>call.resolve({ok:status<400,status,json:async()=>data});
 return{session,page,window,document,calls,changes,respond,get resumes(){return resumes;}};
}
test('environment administrator ID zero can write while preview or missing identity stays read-only',async()=>{
 const f=fixture();assert.equal(f.session.locked,false);const response=f.session.api('/settings','PUT',{settings:{version:1}});const call=f.calls[0];assert.equal(JSON.parse(call.options.body).expected_user_id,0);assert.equal(call.options.credentials,'same-origin');assert.equal(call.options.cache,'no-store');assert.equal(call.options.redirect,'error');assert.equal(call.options.headers['X-CSRF-Token'],'fixture-csrf');f.respond(call,{ok:true});await response;
 for(const [id,preview]of [['',false],['-1',false],['NaN',false],['1',true]])assert.equal(fixture(id,preview).session.locked,true);
});
test('starting a mutation retires older reads even if fetch ignores abort',async()=>{
 const f=fixture('12'),old=f.session.api('/status'),rejected=assert.rejects(old,/取消/);f.session.start();assert.equal(f.calls[0].options.signal.aborted,true);f.respond(f.calls[0]);await rejected;assert.equal(f.session.busy,true);
});
test('late mutation after BFCache restore cannot be consumed and stays uncertain',async()=>{
 const f=fixture(),generation=f.session.start(),pending=f.session.api('/action','POST',{}),rejected=assert.rejects(pending,/取消/);
 f.window.dispatchEvent(new Event('pagehide'));const restore=new Event('pageshow');restore.persisted=true;f.window.dispatchEvent(restore);assert.equal(f.resumes,1);assert.equal(f.session.current(generation),false);assert.equal(f.session.uncertain,true);assert.equal(f.session.locked,true);f.respond(f.calls[0],{ok:true});await rejected;
});
test('read permission failure retires both sessions sharing a page',async()=>{
 const f=fixture();const {CDNSession}=loadModule('frontend/shared/cdn-session.ts',{window:f.window,document:f.document,AbortController,Event,setTimeout,clearTimeout});const other=new CDNSession(f.page,f.page,'/routing',()=>{},()=>{});
 const pending=f.session.api('/status'),rejected=assert.rejects(pending,e=>e.status===403);f.respond(f.calls[0],{error:{code:'permission',message:'forbidden'}},403);await rejected;assert.equal(f.session.accessLost,true);assert.equal(other.accessLost,true);assert.equal(f.session.reads.active,false);assert.equal(other.writes.active,false);
});
test('server account conflict is an identity loss; version conflict preserves draft ownership',async()=>{
 for(const [code,lost]of [['plugins.identity_changed',true],['cdn.conflict',false]]){
  const f=fixture(),pending=f.session.api('/settings','PUT',{}),rejected=assert.rejects(pending,e=>e.code===code&&e.confirmedRejection);f.respond(f.calls[0],{error:{code,message:'conflict'}},409);await rejected;assert.equal(f.session.accessLost,lost);
 }
});
test('unknown results lock mutations but keep status reads available',async()=>{
 const f=fixture();f.session.fail(new Error('lost response'));assert.equal(f.session.locked,true);assert.equal(f.session.start(),undefined);const pending=f.session.api('/status');f.respond(f.calls[0],{ok:true});assert.deepEqual(JSON.parse(JSON.stringify(await pending)),{ok:true});assert.equal(f.session.uncertain,true);
});
test('partial replacement of a child permanently disposes the owning workspace',()=>{
 const f=fixture(),child={contains:()=>false};f.page.contains=region=>region===child;f.document.dispatchEvent(new DetailEvent('chunnel:admin-before-replace',{regions:[child]}));assert.equal(f.session.disposed,true);const restored=new Event('pageshow');restored.persisted=true;f.window.dispatchEvent(restored);assert.equal(f.resumes,0);assert.equal(f.session.active,false);
});
test('same-page session event suspends pending operations and clears private view once',()=>{
 const f=fixture();f.session.start();f.document.dispatchEvent(new Event('chunnel:session'));f.document.dispatchEvent(new Event('chunnel:session'));assert.equal(f.session.accessLost,true);assert.equal(f.changes.filter(Boolean).length,1);assert.equal(f.session.writes.active,false);
});
