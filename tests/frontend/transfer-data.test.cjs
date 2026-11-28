const test=require('node:test'),assert=require('node:assert/strict');
const {loadNativeModule}=require('./helpers/typescript.cjs');
const storage=loadNativeModule('frontend/shared/object-storage-data.ts'),imports=loadNativeModule('frontend/shared/content-import-data.ts');
const sample=require('./fixtures/storage-import-projections.json'),clone=value=>structuredClone(value);
const status=()=>({...clone(sample.snapshot),records:[clone(sample.record)],total_bytes:128});
const preview=()=>clone(sample.preview),input=()=>({...clone(sample.input),authors:{},media:{}}),identity=sample.preview.default_author;
const saved=sent=>{const settings=storage.normalizedStorage(sent);settings.profile_views=settings.profiles.map(p=>({...p,secret_saved:true,password_saved:false,token_saved:false}));return{ok:true,settings};};
test('storage decodes actual Go projections including nullable collections and zero timestamps',()=>{assert.equal(storage.storageStatus(status()).records[0].filename,'fixture.png');const empty=clone(sample.snapshot);empty.records=null;empty.counts=null;empty.profile_counts=null;empty.profile_bytes=null;assert.deepEqual(storage.storageStatus(empty).records,[]);});
for(const [name,change] of [
 ['leaked credentials',s=>s.settings.profiles[0].secret_key='leak'],
 ['inconsistent flags',s=>s.settings.profile_views[0].name='different'],
 ['unsafe version',s=>s.settings.version=Number.MAX_SAFE_INTEGER+1],
 ['wrong policy types',s=>s.settings.paused='false'],
 ['invalid saved route',s=>s.settings.upload_routes={editor:{target:'missing'}}],
 ['duplicate profiles',s=>s.settings.profiles.push(clone(s.settings.profiles[0]))],
 ['unknown status',s=>s.records[0].status='done'],
 ['malformed record timestamp',s=>s.records[0].verified='tomorrow'],
 ['impossible byte totals',s=>s.synced_bytes=129],
])test('storage rejects '+name,()=>{const value=status();change(value);assert.throws(()=>storage.storageStatus(value));});
test('storage save binds normalized fields, returned version and secret flags',()=>{
 const sent=storage.storageDraft(storage.storageSettings(sample.snapshot.settings));sent.profiles[0].name='  saved name  ';sent.profiles[0].secret_key='new';const receipt=saved(sent);assert.equal(storage.storageSaved(receipt,sent,new Set()).profiles[0].name,'saved name');receipt.settings.workers++;assert.throws(()=>storage.storageSaved(receipt,sent,new Set()));receipt.settings.workers--;receipt.settings.profile_views[0].secret_saved=false;assert.throws(()=>storage.storageSaved(receipt,sent,new Set()));
});
test('storage reconciliation retains later input, destination credentials, additions and removals',()=>{
 const sent=storage.storageDraft(storage.storageSettings(sample.snapshot.settings));sent.profiles[0].name='  submitted  ';sent.profiles[0].secret_key='submitted-secret';const current=clone(sent);current.workers=4;current.profiles[0].secret_key='newer-secret';current.profiles[0].bucket='different-bucket';current.profiles.pop();current.upload_routes.editor={target:'local',mode:''};const result=storage.mergeStorage(sent,current,storage.storageSaved(saved(sent),sent,new Set()));assert.equal(result.workers,4);assert.equal(result.profiles.length,1);assert.equal(result.profiles[0].name,'submitted');assert.equal(result.profiles[0].secret_key,'newer-secret');assert.equal(result.profiles[0].bucket,'different-bucket');assert.equal(result.upload_routes.editor.target,'local');
});
test('storage clears only confirmed submitted secrets and validates token clearing',()=>{
 const sent=storage.storageDraft(storage.storageSettings(sample.snapshot.settings));sent.profiles[0].secret_key='submitted';const accepted=storage.storageSaved(saved(sent),sent,new Set());assert.equal(storage.mergeStorage(sent,clone(sent),accepted).profiles[0].secret_key,'');const receipt=saved(sent);receipt.settings.profile_views[0].token_saved=true;assert.throws(()=>storage.storageSaved(receipt,sent,new Set(['archive'])));
});
test('record pages bind query filters, counts and clamped page',()=>{
 const page={records:[clone(sample.record)],total:1,page:1,pages:1};const query={profile:'archive',status:'orphaned',q:'fixture',page:3};assert.equal(storage.storageRecords(page,query).page,1);assert.throws(()=>storage.storageRecords(page,{...query,profile:'backup'}));assert.throws(()=>storage.storageRecords({...page,total:2},query));
});
test('migration receipts bind source, target and valid totals',()=>{assert.equal(storage.storageMigration({ok:true,migration:sample.migration},'archive','backup').total,1);assert.throws(()=>storage.storageMigration({ok:true,migration:sample.migration},'backup','archive'));assert.throws(()=>storage.storageMigration({ok:true,migration:{...sample.migration,ready:2}},'archive','backup'));});
test('probe only reports success after its cleanup completes; queue receipts require numeric counts',()=>{assert.equal(storage.storageAction({ok:true,probe:sample.probe},'test').cleanup_ok,true);assert.throws(()=>storage.storageAction({ok:true,probe:{...sample.probe,cleanup_ok:false}},'test'));assert.throws(()=>storage.storageAction({ok:true,scheduled:'1'},'sync'));});
test('file listings reject foreign prefixes and traversal paths',()=>{assert.equal(storage.storageFiles(sample.files,'media/').files.length,3);for(const key of ['other/file','media/../private','/media/file','media//file'])assert.throws(()=>storage.storageFiles({files:[{key,size:1,directory:false,managed:false}],next:''},'media/'));});
test('import decodes real Go projections, reordered documents and exact apply receipts',()=>{
 const before=preview();before.document.items.reverse();assert.equal(imports.importReport(before,input(),identity).plan.items.length,2);assert.equal(imports.importReport(sample.applied,input(),identity,preview()).applied,true);
});
for(const [name,change] of [
 ['wrong source',r=>r.plan.source='other'],['wrong account',r=>r.default_author++],
 ['unrelated document key',r=>r.document.items[0].key='other.md'],['wrong markdown file key',r=>{r.document.items[0].key='other.md';r.plan.items[0].key='other.md';}],
 ['duplicate rows',r=>r.plan.items[1]=clone(r.plan.items[0])],['miscounted new items',r=>r.plan.new=1],
 ['unexpected applied flag',r=>r.applied=true],['expired token',r=>r.expires='2000-01-01T00:00:00Z'],
 ['missing token',r=>delete r.token],['nonlocal content URL',r=>r.plan.items[0].url='//foreign.test'],
 ['nonstring sanitized preview',r=>r.document.items[0].preview={}],
])test('import rejects '+name,()=>{const value=preview();change(value);assert.throws(()=>imports.importReport(value,input(),identity));});
test('import conflicts deliberately carry no apply token and no expiry',()=>{const value=preview();value.plan.items[0].action='conflict';value.plan.conflicts=1;value.plan.new=1;delete value.token;delete value.expires;assert.equal(imports.importReport(value,input(),identity).token,'');});
test('apply validates created targets and refuses a response for a different batch',()=>{for(const key of ['post_id','version']){const receipt=clone(sample.applied);receipt.plan.items[0][key]=0;assert.throws(()=>imports.importReport(receipt,input(),identity,preview()));}const receipt=clone(sample.applied);receipt.plan.items[0].url='/posts/different/';assert.throws(()=>imports.importReport(receipt,input(),identity,preview()));});
test('replaying an already applied import accepts existing mappings with preserved manual edits',()=>{
 const receipt=clone(sample.applied);receipt.plan.new=0;receipt.plan.existing=2;receipt.plan.items.forEach(row=>{row.action='skip';row.url='/posts/manually-edited-'+row.post_id+'/';row.version=3;});assert.equal(imports.importReport(receipt,input(),identity,preview()).plan.existing,2);
});
test('import validates file identity, size and authors before sending',()=>{
 assert.doesNotThrow(()=>imports.validateImportInput(input()));for(const mutate of [v=>v.authors.Writer=0,v=>v.authors.Writer=NaN,v=>v.files.push(clone(v.files[0])),v=>v.files[0].name='../file.md',v=>v.files[0].text='bad\0text',v=>v.files[0].text='x'.repeat(8*1024*1024+1),v=>v.format='wordpress',v=>v.media.source='https://remote.test/image']){const value=input();mutate(value);assert.throws(()=>imports.validateImportInput(value));}
});
