const test=require('node:test'),assert=require('node:assert/strict');
const {loadNativeModule,loadModule}=require('./helpers/typescript.cjs');
const data=loadNativeModule('frontend/shared/plugin-runtime-data.ts');
const task={id:'a'.repeat(64),plugin:'task-fixture',kind:'task.execute.v1',status:'cancelled',version:2,attempts:0};
test('runtime fragments match Go Unicode limits and optional title semantics',()=>{
 const fragment={title:'😀'.repeat(120),text:'😀'.repeat(2000),tone:'info'};
 assert.deepEqual(data.runtimeResult({fragment}).fragment,fragment);
 assert.equal(data.runtimeResult({fragment:{...fragment,title:''}}).fragment.title,'');
 for(const patch of [{title:'😀'.repeat(121)},{text:'😀'.repeat(2001)},{text:''},{text:' padded '},{text:'a\0b'},{tone:'danger'}])assert.throws(()=>data.runtimeResult({fragment:{...fragment,...patch}}));
});
test('task pagination rejects missing rows, duplicates and another plugin or filter',()=>{
 assert(data.validTaskPage({items:[task],total:1},'task-fixture','cancelled',0));
 for(const page of [{items:[task],total:20},{items:[task,task],total:2},{items:[{...task,plugin:'other-plugin'}],total:1},{items:[{...task,status:'pending'}],total:1}])assert(!data.validTaskPage(page,'task-fixture','cancelled',0));
 assert(data.validTaskPage({items:[],total:1},'task-fixture','',20));
});
test('task review binds source, owner and provenance to the displayed task',()=>{
 const reissue={source_id:'b'.repeat(64),source_version:1,source_status:'failed',reviewed_at:'2026-11-27T10:00:00Z'};
 const review={task:{...task,reissue},current_user_id:7,reissuable:true,provenance:{...reissue,reviewer_id:7,reason:'已核对'}};
 assert(data.validTaskReview(review,'task-fixture',task.id,7));
 for(const patch of [{current_user_id:8},{provenance:{...review.provenance,source_id:'c'.repeat(64)}},{task:{...review.task,status:'pending'}},{provenance:undefined}])assert(!data.validTaskReview({...review,...patch},'task-fixture',task.id,7));
});
test('suspend and resume cannot consume an old request even if transport ignores abort',async()=>{
 let finish,options;
 const {PluginRequests}=loadModule('frontend/shared/plugin-requests.ts',{AbortController,setTimeout,clearTimeout,fetch:async(_url,init)=>{options=init;return{ok:true,status:200,json:()=>new Promise(resolve=>{finish=resolve;})};}});
 const requests=new PluginRequests(),old=requests.json('/fixture');
 await new Promise(resolve=>setImmediate(resolve));requests.suspend();requests.resume();finish({ok:true});
 await assert.rejects(old,/请求已取消/);assert.equal(options.signal.aborted,true);assert.equal(options.redirect,'error');assert.equal(options.cache,'no-store');assert.equal(options.credentials,'same-origin');
});
test('request timeout after headers cannot apply a late JSON body',async()=>{
 let timeout,finish,cleared=false;
 const {PluginRequests}=loadModule('frontend/shared/plugin-requests.ts',{AbortController,setTimeout:fn=>{timeout=fn;return 1;},clearTimeout:()=>{cleared=true;},fetch:async()=>({ok:true,status:200,json:()=>new Promise(resolve=>{finish=resolve;})})});
 const pending=new PluginRequests().json('/fixture');await new Promise(resolve=>setImmediate(resolve));timeout();finish({ok:true});await assert.rejects(pending,/请求已取消/);assert(cleared);
});
