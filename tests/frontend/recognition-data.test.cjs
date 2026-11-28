const test = require('node:test');
const assert = require('node:assert/strict');
const {loadNativeModule} = require('./helpers/typescript.cjs');
const f = require('./fixtures/recognition.cjs');
const d = loadNativeModule('frontend/shared/recognition-data.ts');

test('recognition settings follow numeric Go versions and complete settings shape',()=>{
  assert.deepEqual(d.recognitionSettings(f.settings()),f.settings());
  for(const patch of [{version:'3'},{tips_enabled:1},{badges:undefined},{currencies:[]},{max_medals:9},{verification_help:'<unsafe>'}])assert.throws(()=>d.recognitionSettings({...f.settings(),...patch}));
  assert.deepEqual(d.recognitionSettings({...f.settings(),tips_enabled:false,badges:null,currencies:null}).badges,[]);
  const duplicated=f.settings();duplicated.badges.push(f.badge());assert.throws(()=>d.recognitionSettings(duplicated));
});
test('badge image validation covers decoded traversal and local media references',()=>{
  for(const image of ['/media/medal.png','/media/%E4%B8%AD%E6%96%87.webp'])assert.equal(d.localBadgeImage(image),true);
  for(const image of ['https://example.test/a.png','//evil.test/a.png','/media/%2e%2e/a.png','/media/a.png?q=1','/media/a.svg','/media/../a.png','/media/%5cevil.png'])assert.equal(d.localBadgeImage(image),false);
  for(const patch of [{image:'https://other.test/a.png'},{color:'#ABCDEF'},{icon:'script'},{key:'badge-'},{name:'x\u200by'}])assert.throws(()=>d.recognitionBadge({...f.badge(),...patch}));
});
test('identities accept Go nil slices but reject missing, duplicated and unrelated grants',()=>{
  assert.deepEqual(d.recognitionIdentity(f.identity()),f.identity());
  assert.deepEqual(d.recognitionIdentity({version:1,grants:null,equipped:null,history:null}).grants,[]);
  for(const patch of [{version:0},{grants:undefined},{grants:[...f.identity().grants,...f.identity().grants]},{equipped:['missing']},{history:[{...f.identity().history[0],action:'unknown'}]}])assert.throws(()=>d.recognitionIdentity({...f.identity(),...patch}));
});
test('social identity binds the owner, catalog and private application',()=>{
  const social={user_id:2,identity:f.identity(),catalog:f.settings().badges,badges:[],verification_enabled:true,verification_help:'',max_medals:3};
  assert.equal(d.recognitionSocial(social,2).user_id,2);
  assert.throws(()=>d.recognitionSocial(social,3));
  assert.throws(()=>d.recognitionSocial({...social,catalog:[]},2));
  assert.throws(()=>d.recognitionSocial({...social,verification_enabled:undefined},2));
  assert.throws(()=>d.recognitionAccount({user_id:2,username:'reader',name:'',identity:f.identity(),badges:[]},'other'));
});
test('identity receipts bind version, actor, action, exact grant and application labels',()=>{
  const body=d.identityMutation({user_id:2,version:1,action:'grant',key:'original',label:'原创',reason:'贡献',days:0});
  assert.equal(d.identityReceipt({identity:f.identity()},body,1).version,2);
  for(const patch of [{version:3},{history:[]},{grants:[]},{grants:[{...f.identity().grants[0],label:'其他'}]}])assert.throws(()=>d.identityReceipt({identity:{...f.identity(),...patch}},body,1));
  assert.throws(()=>d.identityReceipt({identity:f.identity()},body,3));
  const approved=f.identity(),app={...f.application(),status:'approved',reason:'通过',updated_at:'2026-11-28T00:00:00Z'};
  approved.application=app;approved.grants.push({key:'creator',label:app.label,actor_id:1,granted_at:app.updated_at,expires_at:'0001-01-01T00:00:00Z'});
  approved.history.push({action:'approve',key:'creator',reason:'通过',actor_id:1,created_at:app.updated_at});
  const approve={user_id:2,version:1,action:'approve',key:'creator',label:'ignored admin label',reason:'通过',days:0};
  d.identityReceipt({identity:approved},approve,1);
  const before={...f.identity(),application:f.application()};d.identityReceipt({identity:approved},approve,1,before);
  assert.throws(()=>d.identityReceipt({identity:approved},approve,1,{...before,application:{...before.application,evidence:'other private application'}}));
  approved.grants[1].label='ignored admin label';assert.throws(()=>d.identityReceipt({identity:approved},approve,1));
});
test('apply and equip receipts require the actual saved values',()=>{
  const a=f.identity();a.application=f.application();a.history.push({action:'apply',key:'creator',reason:'',actor_id:2,created_at:a.application.updated_at});
  const body={user_id:2,version:1,action:'apply',key:'creator',label:a.application.label,evidence:a.application.evidence};d.identityReceipt({identity:a},body,2);
  assert.throws(()=>d.identityReceipt({identity:a},{...body,evidence:'other'},2));
  a.history.at(-1).action='equip';a.history.at(-1).key='';d.identityReceipt({identity:a},{user_id:2,version:1,action:'equip',equipped:['original']},2);
  assert.throws(()=>d.identityReceipt({identity:a},{user_id:2,version:1,action:'equip',equipped:[]},2));
});
test('tip recovery whitelists request fields and preserves exact amounts and versions',()=>{
  assert.deepEqual(d.tipPending({...f.pending(),csrf:'must-not-send',actor_id:99},7),f.pending());
  for(const patch of [{post_id:8},{request_id:'short'},{currency:'bad-'},{hidden:'false'},{settings_version:'3'},{currency_version:0},{amount:'01'},{amount:'0'},{amount:'1e3'},{amount:'0.0000001'}])assert.throws(()=>d.tipPending({...f.pending(),...patch},7));
  assert.equal(d.tipPending({...f.pending(),amount:'9223372036854775807'},7).amount,'9223372036854775807');
});
test('tip monetary comparisons use integers and reject excess precision and overflow',()=>{
  assert.equal(d.tipUnits('1.25',2),125n);assert.equal(d.tipUnits('0.000001',6),1n);assert.equal(d.tipUnits('0.00',2),0n);
  for(const v of ['1.001','-1','+1','01','1.','1e2','92233720368547758.08'])assert.throws(()=>d.tipUnits(v,2));
});
test('tip read and completion cannot coerce owner, versions or receipt booleans',()=>{
  assert.equal(d.tipState(f.tips(),2).currencies.length,2);
  for(const patch of [{user_id:3},{settings_version:'3'},{enabled:undefined},{currencies:[{...f.tips().currencies[0],precision:7}]}])assert.throws(()=>d.tipState({...f.tips(),...patch},2));
  assert.equal(d.tipReceipt({completed:true,request_id:f.pending().request_id,message:'完成'},f.pending()),'完成');
  for(const patch of [{completed:'true'},{request_id:'another'},{message:undefined}])assert.throws(()=>d.tipReceipt({completed:true,request_id:f.pending().request_id,message:'完成',...patch},f.pending()));
});
