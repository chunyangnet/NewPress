const test=require('node:test'),assert=require('node:assert/strict');
const {loadNativeModule}=require('./helpers/typescript.cjs'),f=require('./fixtures/publishing.cjs');
const d=loadNativeModule('frontend/shared/flash-data.ts');
test('flash pending whitelists actual Go fields and binds actor, endpoint and manuscript',()=>{
  const value=f.pending({csrf:'secret',body:{...f.body(),author_id:99,csrf:'secret'}});
  assert.deepEqual(d.flashPending(value,value.endpoint,7,4),f.pending());
  for(const patch of [{schema:2},{key:'short'},{endpoint:'https://evil.test/api'},{body:f.body({id:5})},{body:f.body({expected_user_id:8})},{body:f.body({version:'2'})},{body:f.body({id:0,version:2})}])assert.throws(()=>d.flashPending(f.pending(patch),value.endpoint,7,4));
  assert.throws(()=>d.flashBody(f.body(),7,false));
  assert.throws(()=>d.flashPending(f.pending({draft:f.draft({body:'unrelated draft'})}),value.endpoint,7,4));
  assert.equal(d.flashBody(f.body({published_at:'',published_timezone:''}),7,false).action,'save');
});
test('flash receipt uses numeric Go IDs, next version, explicit replay flag and message',()=>{
  assert.deepEqual(d.flashReceipt(f.receipt(f.body()),f.body()),f.receipt(f.body()));
  for(const patch of [{id:5},{id:'4'},{version:2},{version:4},{replayed:undefined},{replayed:1},{message:undefined},{message:''}])assert.throws(()=>d.flashReceipt(f.receipt(f.body(),patch),f.body()));
  assert.equal(d.flashReceipt({id:12,version:1,replayed:true,message:'快报已保存'},f.body({id:0,version:0})).id,12);
});
test('flash local minutes and timezone survive recovery without UTC conversion',()=>{
  const draft=d.flashDraft(f.draft());assert.equal(draft.published_at,'2026-11-01T02:30');assert.equal(draft.published_timezone,'America/New_York');
  for(const zone of ['UTC','Asia/Shanghai','Etc/GMT+8'])assert.equal(d.flashZone(zone),zone);
  for(const zone of ['',null,'../UTC','Asia/Shanghai\n','<script>'])assert.throws(()=>d.flashZone(zone));
  assert.throws(()=>d.flashDraft(f.draft({published_at:'2026-11-01T02:30Z'})));
  assert.throws(()=>d.flashDraft(f.draft({comments:'true'})));
});
test('flash state-only transitions are distinct from saved content',()=>{
  for(const action of ['withdraw','reject','unpublish','trash','restore'])assert.equal(d.stateOnly(d.flashAction(action)),true);
  for(const action of ['save','submit','publish'])assert.equal(d.stateOnly(d.flashAction(action)),false);
  assert.throws(()=>d.flashAction('delete'));assert.equal(d.sameFlashDraft(f.draft(),f.draft({body:'new'})),false);
});
test('flash engagement rejects unrelated, duplicated and malformed states but preserves missing targets',()=>{
  assert.equal(d.flashEngagement(f.engagement(),[4]).like[0].count,5);
  for(const patch of [{like:[{target_id:5,count:5,active:false}]},{like:[...f.engagement().like,...f.engagement().like]},{like:[{target_id:4,count:-1,active:false}]},{follow:[{target_id:7,count:1,active:true}]},{bookmark:undefined}])assert.throws(()=>d.flashEngagement(f.engagement(patch),[4]));
  assert.deepEqual(d.flashEngagement({like:[],bookmark:[],follow:[]},[4]).like,[]);
});
test('flash engagement receipt binds original target, desired state and collection',()=>{
  const body={kind:'bookmark',target_id:4,active:true,collection:'研读'};
  d.flashEngagementReceipt({ok:true,target_id:4,active:true,collection:'研读'},body);
  for(const patch of [{ok:false},{target_id:5},{active:false},{collection:''}])assert.throws(()=>d.flashEngagementReceipt({ok:true,target_id:4,active:true,collection:'研读',...patch},body));
});
test('flash sharing stays on the same site and keeps canonical paths',()=>{
  assert.equal(d.flashShare('/flash/test/','https://example.test'),'https://example.test/flash/test/');
  for(const value of ['https://evil.test/flash/test/','javascript:alert(1)','//evil.test/flash/test/','/flash/../admin/','/flash/\\evil.test','/account/'])assert.throws(()=>d.flashShare(value,'https://example.test'));
});
