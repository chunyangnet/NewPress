const test=require('node:test'),assert=require('node:assert/strict');
const {loadModules}=require('./helpers/typescript.cjs');
const F=require('./fixtures/ai-admin-contracts.json');
const [D,H]=loadModules(['frontend/shared/ai-usage-data.ts','frontend/shared/ai-usage-helpers.ts']);
const [S]=loadModules(['frontend/shared/ai-settings-data.ts'],{URL});
const W=require('./helpers/typescript.cjs').loadNativeModule('frontend/shared/ai-workbench-data.ts');
const clone=value=>structuredClone(value);

test('actual Go AI report contracts preserve missing counters and reported zero',()=>{
 const empty=D.usageReport(F.usage_empty),first=D.usageReport(F.usage_first),second=D.usageReport(F.usage_second);
 assert.equal(empty.totals.requests,0);assert.equal(first.records.length,2);assert.equal(first.next,2);
 assert.equal(first.records[0].usage.total_tokens,undefined);assert.equal(first.records[0].cost.amount,undefined);
 assert.equal(second.records[0].usage.total_tokens,0);assert.equal(second.records[0].cost.amount,0);
 assert.equal(second.next,0);assert.equal(first.totals.success+first.totals.errors+first.totals.cancelled,first.totals.requests);
});
test('AI report rejects unsafe, fractional and mismatched pagination instead of rounding it',()=>{
 for(const change of [v=>v.next=2**53,v=>v.next=1.5,v=>v.next=1,v=>v.records[0].sequence=2**53,v=>v.records.reverse(),v=>v.records[1].id=v.records[0].id,v=>v.records[1].sequence=v.records[0].sequence]){const value=clone(F.usage_first);change(value);assert.throws(()=>D.usageReport(value));}
});
test('AI report checks calendar, time zone, range and duplicate days',()=>{
 for(const change of [v=>v.start='2026-02-30',v=>v.end='9999-12-31',v=>v.start='2000-01-01',v=>v.timezone='unknown/zone',v=>v.trend.push(v.trend[0]),v=>v.records[0].day='2000-01-01']){const value=clone(F.usage_first);change(value);assert.throws(()=>D.usageReport(value));}
 assert.equal(D.calendarDay('2024-02-29'),'2024-02-29');assert.throws(()=>D.calendarDay('2025-02-29'));
});
test('AI totals reject impossible counts and non-finite costs without inventing zero',()=>{
 for(const change of [v=>v.totals.success++,v=>v.totals.cost_known=100,v=>v.totals.cost_partial=100,v=>v.totals.tokens='0',v=>v.totals.costs.USD=Infinity,v=>v.totals.costs.USD=-1,v=>v.totals.saturated='false',v=>v.totals.costs=false,v=>delete v.totals.costs]){const value=clone(F.usage_first);change(value);assert.throws(()=>D.usageReport(value));}
});

const draft=raw=>({...raw,default_image_provider:raw.default_image_provider||'',telemetry:raw.telemetry||{...S.telemetryDefaults},providers:raw.providers.map(p=>({protocol:'',api_version:'',allow_insecure:false,has_key:false,api_key:'',default_model:'',pricing:[],...p,balance:{...S.defaultBalance,...p.balance}}))});
test('actual Go AI settings include empty defaults and never return secrets',()=>{
 assert.equal(S.aiSettings(F.empty_settings).telemetry.retention_days,30);
 const settings=S.aiSettings(F.settings);assert.equal(settings.providers[0].has_key,true);assert.equal(settings.providers[0].balance.token,'');
 for(const change of [v=>v.providers[0].api_key='secret',v=>v.providers[0].balance.token='secret',v=>v.providers[0].protocol='future',v=>v.providers[0].models.push('fixture-model'),v=>v.providers[0].default_model='other',v=>v.default_provider='other',v=>v.providers[0].balance.cache_seconds=0]){const value=clone(F.settings);change(value);assert.throws(()=>S.aiSettings(value));}
});
test('AI settings receipts match Go normalization and independent key/token retention',()=>{
 for(const sample of F.settings_receipts){const result=S.savedAISettings(sample.after,draft(sample.sent),S.aiSettings(sample.before));assert.equal(result.revision,sample.after.revision);}
 const changedProtocol=F.settings_receipts[1];assert.equal(changedProtocol.after.providers[0].balance.has_token,true);
 const changedURL=F.settings_receipts[2];assert.equal(changedURL.after.providers[0].balance.has_token,undefined);assert.equal(changedURL.after.providers[0].base_url,'https://API.example.test:443/v1');
});
test('AI save rejects a different receipt, reused revision, and unsafe retained credentials',()=>{
 const sample=F.settings_receipts[0],sent=draft(sample.sent),baseline=S.aiSettings(sample.before);
 for(const change of [v=>v.revision=sent.revision,v=>v.providers[0].name='other',v=>v.providers[0].has_key=false,v=>v.providers[0].balance.has_token=false,v=>v.providers[0].pricing[0].flat=1]){const value=clone(sample.after);change(value);assert.throws(()=>S.savedAISettings(value,sent,baseline));}
 const target=draft(F.settings_receipts[2].sent);target.providers[0].api_key='';assert.throws(()=>S.savedAISettings(F.settings_receipts[2].after,target,S.aiSettings(F.settings_receipts[2].before)));
 const token=draft(F.settings_receipts[2].sent);token.providers[0].balance.clear_token=false;assert.throws(()=>S.savedAISettings(F.settings_receipts[2].after,token,S.aiSettings(F.settings_receipts[2].before)));
});
test('AI settings merge retains later edits, removals, new providers and new secrets',()=>{
 const sample=F.settings_receipts[0],sent=draft(sample.sent),saved=S.aiSettings(sample.after),current=clone(sent);
 current.providers[0].name='later';current.providers[0].api_key='later-key';current.telemetry.max_records=2000;
 const merged=S.mergeAISettings(current,sent,saved);assert.equal(merged.providers[0].name,'later');assert.equal(merged.providers[0].api_key,'later-key');assert.equal(merged.providers[0].balance.token,'');assert.equal(merged.providers[0].balance.has_token,true);assert.equal(merged.telemetry.max_records,2000);assert.equal(merged.revision,saved.revision);
 current.providers[0].balance.token='later-token';assert.equal(S.mergeAISettings(current,sent,saved).providers[0].balance.token,'later-token');
 current.providers=[];assert.equal(S.mergeAISettings(current,sent,saved).providers.length,0);
 current.providers=[{...clone(sent.providers[0]),id:'added'}];assert.equal(S.mergeAISettings(current,sent,saved).providers[0].id,'added');
});
test('AI model discovery rejects ambiguous IDs and malformed payloads',()=>{
 assert.equal(S.discoveredModels({models:[{id:'m',name:'Model'}]}).models[0].id,'m');
 for(const value of [{models:[{id:1}]},{models:[{id:'m'},{id:'m'}]},{models:[{id:'bad model'}]},{models:null}])assert.throws(()=>S.discoveredModels(value));
});
test('AI telemetry settings require real flags and supported retention',()=>{
 const value=clone(F.usage_first.config);assert.equal(D.telemetry(value).retention_days,30);
 for(const change of [v=>delete v.charts,v=>v.costs='true',v=>v.retention_days=14,v=>v.max_records=10001]){const candidate=clone(value);change(candidate);assert.throws(()=>D.telemetry(candidate));}
});
test('AI usage counters distinguish omitted values, zero and invalid values',()=>{
 assert.equal(D.usage({}).total_tokens,undefined);assert.equal(D.usage({total_tokens:0,total_source:'reported'}).total_tokens,0);
 for(const usage of [{total_tokens:null},{total_tokens:-1},{total_tokens:1.5},{total_tokens:2**53},{total_source:'inferred'}])assert.throws(()=>D.usage(usage));
});
test('AI cost snapshots preserve unpriced records and validate currency and price scale',()=>{
 assert.equal(D.cost({complete:false}).amount,undefined);
 assert.equal(D.cost({complete:true,amount:0,currency:'USD'}).amount,0);
 for(const cost of [{complete:false,amount:0},{complete:false,amount:NaN,currency:'USD'},{complete:'true'},{complete:true,amount:1,currency:'unknown'}])assert.throws(()=>D.cost(cost));
 const price=clone(F.usage_second.records[0].cost.pricing);assert.equal(D.modelPrice(price).flat,0);price.multiplier=0;assert.throws(()=>D.modelPrice(price));
});
test('AI balances accept debt while rejecting invalid timestamps and missing flags',()=>{
 const row={remaining:-1,used:2,total:1,currency:'USD',checked_at:'2026-11-27T00:00:00Z',expires_at:'2026-11-27T00:05:00Z',cached:false};
 assert.equal(D.balanceResult(row).remaining,-1);
 for(const patch of [{cached:undefined},{remaining:'0'},{remaining:Infinity},{expires_at:'2026-11-26T00:00:00Z'},{currency:'bad'}])assert.throws(()=>D.balanceResult({...row,...patch}));
});
test('typed AI chart helpers preserve unknown values and fill calendar gaps',()=>{
 assert.equal(H.compact(undefined),'—');assert.equal(H.count(null),'—');assert.equal(H.money(undefined,'USD'),'未计价');assert.equal(H.money(0,'USD'),'USD 0.00');
 assert.equal(H.chartValue({requests:1,usage_known:0,tokens:0},'tokens'),null);assert.equal(H.chartValue({requests:1,usage_known:1,tokens:0},'tokens'),0);
 assert.equal(H.chartValue({requests:1,cost_known:0,costs:{}},'costs','USD'),null);assert.equal(H.chartValue({requests:1,cost_known:1,costs:{USD:0}},'costs','USD'),0);
 assert.equal(H.dayOffset('2024-03-01',-1),'2024-02-29');assert.equal(H.fillDays('2026-10-30','2026-11-02',[]).length,4);
});
test('actual Go workbench configuration, private history and complete reports validate',()=>{
 assert.equal(W.configuration(F.workbench).tasks.length,6);assert.equal(W.reportList(F.reports,'1').length,1);assert.equal(W.reportDetail(F.report,'1',F.report.id).content,F.report.content);
 const input={task:'operations',days:7,focus:' 核对状态 '};assert.equal(W.previewReceipt(F.preview,input).token,F.preview.token);
 assert.throws(()=>W.reportDetail(F.report,'2'));assert.throws(()=>W.reportDetail(F.report,'1','a'.repeat(32)));
});
test('AI workbench rejects partial contracts and mismatched preview criteria',()=>{
 for(const change of [v=>v.providers[0].default_model='other',v=>v.tasks.push(v.tasks[0]),v=>v.default_provider='other',v=>v.timezone='bad']){const value=clone(F.workbench);change(value);assert.throws(()=>W.configuration(value));}
 for(const patch of [{task:'briefing',days:7,focus:'核对状态'},{task:'operations',days:1,focus:'other'}])assert.throws(()=>W.previewReceipt(F.preview,patch));
 const value=clone(F.preview);value.task='summary';value.context=JSON.stringify({material:'test'});assert.throws(()=>W.previewReceipt(value,{task:'summary',days:1,focus:value.focus,material:'different'}));
});
test('AI completion validates provider, model, owner and exact source before replacing partial text',()=>{
 const done={report:F.report,saved:true,usage:{total_tokens:0,total_source:'reported'}};
 assert.equal(W.completedReport(done,'1',F.preview,F.workbench.providers[0],'fixture-model').saved,true);
 for(const change of [v=>v.report.owner=2,v=>v.report.model='other',v=>v.report.source.context='{}',v=>v.report.source.token='secret',v=>v.report.content='',v=>v.saved='true']){const value=clone(done);change(value);assert.throws(()=>W.completedReport(value,'1',F.preview,F.workbench.providers[0],'fixture-model'));}
 assert.equal(W.completedReport({...done,saved:false},'1',F.preview,F.workbench.providers[0],'fixture-model').saved,false);
});
test('AI history and delete receipts reject duplicate, unrelated or uncertain records',()=>{
 assert.throws(()=>W.reportList({reports:[...F.reports.reports,...F.reports.reports]},'1'));
 assert.throws(()=>W.deletedReport({ok:true}));assert.throws(()=>W.deletedReport({deleted:'true'}));assert.doesNotThrow(()=>W.deletedReport({deleted:true}));
 assert.throws(()=>W.articleList({items:[{id:2,title:'a'}],total:0}));assert.throws(()=>W.articleList({items:[{id:2,title:'a'},{id:2,title:'b'}],total:2}));
});
test('AI usage report must match the submitted range, filters and pagination cursor',()=>{
 for(const params of ['start=2020-01-01','provider=other','model=other','kind=image','status=success','before=2'])assert.throws(()=>D.usageReport(F.usage_first,new URLSearchParams(params)));
});
