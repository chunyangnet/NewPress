const test=require('node:test'),assert=require('node:assert/strict');
const data=require('./helpers/typescript.cjs').loadNativeModule('frontend/shared/cdn-data.ts');
// Captured from TestCDNMigrationBrowserFixture's Go projections; credentials are redacted.
const sample=require('./fixtures/cdn-projections.json'),clone=value=>structuredClone(value);
const status=()=>({...clone(sample.snapshot),jobs:[clone(sample.job)]});
const routing=()=>({...clone(sample.routing),reports:{site:clone(sample.report)},plans:[clone(sample.plan)],tasks:[clone(sample.task)]});
const settingsSave=sent=>{const settings=data.normalizedCDN(sent);settings.profile_views=settings.profiles.map(p=>({...p,secret_saved:true,token_saved:!!sent.profiles.find(s=>s.id===p.id)?.token}));return{ok:true,settings};};
const routeSave=sent=>{const result=routing();result.config=data.normalizedRouting(sent);result.config.dns.forEach(p=>{p.secret_saved=true;if(sent.dns.find(s=>s.id===p.id)?.token)p.token_saved=true;});return result;};

test('CDN and routing decode complete Go settings, nullable collections, jobs, benchmark, plans and reports',()=>{
 const a=data.cdnStatus(status()),b=data.routingState(routing());assert.equal(a.jobs[0].benchmark.results[0].success_rate,100);assert.deepEqual(b.reports.site.records,[]);assert.deepEqual(b.reports.site.hostname.ssl.validation_records,[]);assert.equal(b.plans[0].changes[0].after.value,'customers.example.test');assert.equal(b.tasks[0].action,'inspect');
});
for(const [name,change] of [
 ['unsafe version',s=>s.settings.version=Number.MAX_SAFE_INTEGER+1],
 ['malformed profiles',s=>s.settings.profiles={}],
 ['duplicate profiles',s=>s.settings.profiles.push(clone(s.settings.profiles[0]))],
 ['mismatched views',s=>s.settings.profile_views[0].resource_id='other-zone'],
 ['leaked secret',s=>s.settings.profiles[0].secret='unexpected-token'],
 ['missing counts',s=>delete s.counts],
 ['nonfinite measurements',s=>s.jobs[0].benchmark.results[0].ttfb_p50=NaN],
 ['overstated success',s=>s.jobs[0].benchmark.results[0].success=2],
 ['invalid date',s=>s.jobs[0].updated='later'],
 ['wrong job identifier',s=>s.jobs[0].id='arbitrary'],
 ['private job fingerprint',s=>s.jobs[0].identity='a'.repeat(64)],
 ['unknown status',s=>s.jobs[0].status='done'],
 ['invalid provider operation',s=>s.capabilities[0].operations=['drop']],
])test('CDN rejects '+name,()=>{const s=status();change(s);assert.throws(()=>data.cdnStatus(s));});
for(const [name,change] of [
 ['missing DNS reference',s=>s.config.routes[0].dns_profile_id='absent'],
 ['report for a different route',s=>s.reports.site.route_id='elsewhere'],
 ['untyped DNS steps',s=>s.plans[0].changes[0].after.ttl='600'],
 ['unconfirmed step status',s=>s.plans[0].changes[0].state='successful'],
 ['private plan fingerprint',s=>s.plans[0].identity='a'.repeat(64)],
 ['duplicate tasks',s=>s.tasks.push(clone(s.tasks[0]))],
 ['wrong plan reference',s=>s.tasks[0].plan_id='not-an-id'],
 ['wrong boolean',s=>s.reports.site.preferred.healthy='false'],
 ['oversized routes',s=>s.config.routes=Array(21).fill(s.config.routes[0])],
])test('routing rejects '+name,()=>{const s=routing();change(s);assert.throws(()=>data.routingState(s));});
test('CDN import validates all policy types and strips extra fields',()=>{
 const sent=data.cdnDraft(data.cdnStatus(status()).settings);sent.profiles[0].secret='typed-secret';const imported=data.cdnImport({...sent,unexpected:'drop'});assert.equal(imported.profiles[0].secret,'typed-secret');assert.equal(imported.unexpected,undefined);for(const key of ['workers','paused','auto_content','static_ttl'])assert.throws(()=>data.cdnImport({...sent,[key]:'bad'}));
});
test('CDN save accepts actual Go normalization and rejects unrelated version or content',()=>{
 const sent=data.cdnDraft(sample.snapshot.settings);Object.assign(sent.profiles[0],{name:'  保存名称  ',domains:['STATIC.EXAMPLE.TEST','example.test','example.test'],secret:'replacement'});sent.asset_base='https://STATIC.EXAMPLE.TEST/';const response=settingsSave(sent);assert.equal(data.cdnSaved(response,sent).asset_base,'https://static.example.test');response.settings.workers++;assert.throws(()=>data.cdnSaved(response,sent));response.settings.workers--;response.settings.version++;assert.throws(()=>data.cdnSaved(response,sent));
});
test('CDN save requires credential saved/cleared flags to match the submission',()=>{
 const sent=data.cdnDraft(sample.snapshot.settings);sent.profiles[0].token='replacement';let response=settingsSave(sent);assert.doesNotThrow(()=>data.cdnSaved(response,sent));response.settings.profile_views[0].token_saved=false;assert.throws(()=>data.cdnSaved(response,sent));response=settingsSave(sent);assert.throws(()=>data.cdnSaved(response,sent,new Set(['cloudflare'])));response.settings.profile_views[0].token_saved=false;assert.doesNotThrow(()=>data.cdnSaved(response,sent,new Set(['cloudflare'])));
});
test('save reconciliation retains later per-field edits, new credentials and removed/new profiles',()=>{
 const sent=data.cdnDraft(sample.snapshot.settings);Object.assign(sent.profiles[0],{name:'  已提交  ',secret:'old-token'});const current=clone(sent);current.workers=4;current.profiles[0].secret='new-token';current.profiles.push({...current.profiles[0],id:'new-profile'});const saved=data.cdnSaved(settingsSave(sent),sent),result=data.mergeCDN(sent,current,saved);assert.equal(result.profiles[0].name,'已提交');assert.equal(result.profiles[0].secret,'new-token');assert.equal(result.workers,4);assert.equal(result.profiles[1].id,'new-profile');assert.equal(result.version,saved.version);assert.equal(data.mergeCDN(sent,{...current,profiles:[]},saved).profiles.length,0);
});
test('only submitted credentials are cleared, and changing account keeps the new credential',()=>{
 const sent=data.cdnDraft(sample.snapshot.settings);sent.profiles[0].secret='submitted';const saved=data.cdnSaved(settingsSave(sent),sent);assert.equal(data.mergeCDN(sent,clone(sent),saved).profiles[0].secret,undefined);const current=clone(sent);current.profiles[0].resource_id='new-zone';assert.equal(data.mergeCDN(sent,current,saved).profiles[0].secret,'submitted');
});
test('routing save validates normalization and retains later route and DNS edits',()=>{
 const sent=clone(sample.routing.config);sent.dns[0].name='  DNS  ';sent.routes[0].hostname='APP.CUSTOMER.TEST.';sent.routes[0].preferred_lines=['telecom','telecom'];sent.dns[0].secret='submitted';const current=clone(sent);current.routes[0].ttl=1200;current.dns[0].secret='new';const response=routeSave(sent),saved=data.routingSaved(response,sent),result=data.mergeRouting(sent,current,saved.config);assert.equal(result.dns[0].secret,'new');assert.equal(result.dns[0].name,'DNS');assert.equal(result.routes[0].ttl,1200);assert.equal(result.routes[0].hostname,'app.customer.test');assert.deepEqual(result.routes[0].preferred_lines,['telecom']);response.config.routes[0].fallback_origin='elsewhere.test';assert.throws(()=>data.routingSaved(response,sent));
});
test('enqueue acknowledgment binds profile, operation, normalized targets and exact benchmark parameters',()=>{
 const job={...clone(sample.job),operation:'purge_url',targets:['https://example.test/a']};delete job.benchmark;delete job.benchmark_request;const request={profile_id:'cloudflare',operation:'purge_url',targets:[' https://example.test/a ','https://example.test/a']};assert.equal(data.enqueueReceipt({ok:true,job},request).id,job.id);assert.throws(()=>data.enqueueReceipt({ok:true,job},{...request,profile_id:'other'}));assert.throws(()=>data.enqueueReceipt({ok:true,job},{...request,targets:['https://example.test/b']}));const benchmark=clone(sample.job),b={profile_id:'cloudflare',operation:'benchmark',targets:[],benchmark:clone(benchmark.benchmark_request)};assert.doesNotThrow(()=>data.enqueueReceipt({ok:true,job:benchmark},b));b.benchmark.rounds++;assert.throws(()=>data.enqueueReceipt({ok:true,job:benchmark},b));
});
test('routing task acknowledgment binds exact action, route and plan',()=>{
 const task={...clone(sample.task),action:'apply',plan_id:sample.plan.id};const request={action:'apply',route_id:'site',plan_id:sample.plan.id};assert.equal(data.routingReceipt({task},request).id,task.id);for(const key of ['action','route_id','plan_id'])assert.throws(()=>data.routingReceipt({task},{...request,[key]:'other'}));
});
