const test = require('node:test');
const assert = require('node:assert/strict');
const { loadNativeModule } = require('./helpers/typescript.cjs');
const { resourceState, order } = require('./fixtures/resource-public.cjs');
const d = loadNativeModule('frontend/core/resource-public-data.ts');
const target = {post:'1', id:'bundle'};
test('resource snapshot binds the exact article, resource, account and revision', () => {
  const parsed = d.resourceState(resourceState(), target);
  assert.equal(parsed.revision, 'a'.repeat(64));
  for (const patch of [{post:2},{id:'other'},{user_id:2},{logged_in:false},{revision:'old'},{csrf:''},{payment_version:-1},{daily_used:3,total_used:1},{timezone:'invalid'}]) assert.throws(()=>d.resourceState({...resourceState(),...patch},target));
});
test('resource parses anonymous identity and nullable Go lists without inventing success', () => {
  const raw=resourceState();Object.assign(raw,{user_id:'0',logged_in:false,payment_version:'0'});
  raw.variants[0].fields=null;raw.variants[0].questions=[{id:'q',title:'问题',options:null}];
  const parsed=d.resourceState(raw,target);assert.equal(parsed.logged_in,false);assert.deepEqual(parsed.variants[0].fields,[]);assert.deepEqual(parsed.variants[0].questions[0].options,[]);
  delete raw.variants[0].quiz_passed;assert.throws(()=>d.resourceState(raw,target));
});
test('anonymous quota resources accept only the server display-only login prerequisite',()=>{
  const raw=resourceState();Object.assign(raw,{user_id:'0',logged_in:false});
  const prerequisite={id:'',kind:'login',label:'请登录后使用有次数限制的资源',passed:false,amount:0,currency:'',purchasable:false};
  raw.variants[0].groups[0].conditions.push(prerequisite);
  raw.variants[0].groups.push({name:'其他方式',passed:false,conditions:[{...prerequisite}]});
  assert.equal(d.resourceState(raw,target).variants[0].groups.length,2);
  prerequisite.kind='payment';assert.throws(()=>d.resourceState(raw,target));prerequisite.kind='login';raw.user_id='2';raw.logged_in=true;assert.throws(()=>d.resourceState(raw,target));
});
test('resource rejects duplicate identities and contradictory unlock conditions', () => {
  for(const mutate of [s=>s.variants.push(structuredClone(s.variants[0])),s=>s.variants[0].groups[0].conditions.push(structuredClone(s.variants[0].groups[0].conditions[0])),s=>s.variants[0].groups[0].passed=true,s=>s.variants[0].allowed=true]) {
    const raw=resourceState();mutate(raw);assert.throws(()=>d.resourceState(raw,target));
  }
});
test('resource state never accepts answers or private download metadata in public projections',()=>{
  const raw=resourceState();raw.variants[0].downloads[0].url='https://example.test/private';assert.throws(()=>d.resourceState(raw,target));delete raw.variants[0].downloads[0].url;
  raw.variants[0].questions=[{id:'q',title:'问题',options:[],answer:'secret'}];assert.throws(()=>d.resourceState(raw,target));
});
test('resource preview uses a distinct contract without account, CSRF or payment state',()=>{
  const raw=resourceState();for(const field of ['csrf','user_id','logged_in','revision','payment_version','channels'])delete raw[field];raw.preview=true;
  Object.assign(raw.variants[0],{body:'<p>preview</p>',media_url:''});for(const field of ['blocked','quiz_passed','form_complete'])delete raw.variants[0][field];
  Object.assign(raw.variants[0].downloads[0],{url:'/example.zip',code:'code',password:'password'});
  const parsed=d.resourcePreview(raw,target);assert.equal(parsed.preview,true);assert.equal(parsed.variants[0].downloads[0].password,'password');assert.throws(()=>d.resourceState(raw,target));
});
test('resource URLs allow HTTP(S) and same-origin paths and reject active or credentialed URLs',()=>{
  for(const good of ['/file.zip','/file.zip?a=<b>','https://example.test/file','http://example.test/file'])assert.equal(d.resourceURL(good),good);
  for(const bad of ['javascript:alert(1)','data:text/html,test','//example.test/a','/\\example.test/a','https://name:pass@example.test/a','https://example.test/\nfoo','file:///test','relative.zip'])assert.throws(()=>d.resourceURL(bad));
  assert.throws(()=>d.resourceURL('https://example.test/a',true));
});
test('download and submit receipts do not coerce missing or string booleans and quotas',()=>{
  const file={url:'/file.zip',code:'',password:'',replayed:false,remaining:1};assert.deepEqual(d.downloadReceipt(file),file);
  for(const patch of [{remaining:'1'},{remaining:-2},{replayed:'false'},{url:'javascript:void(0)'},{code:undefined}])assert.throws(()=>d.downloadReceipt({...file,...patch}));
  assert.deepEqual(d.submissionReceipt({quiz_passed:false,form_complete:true,attempts_remaining:2}),{quiz_passed:false,form_complete:true,attempts_remaining:2});
  assert.throws(()=>d.submissionReceipt({quiz_passed:'true',form_complete:true,attempts_remaining:2}));assert.throws(()=>d.pointsReceipt({paid:true}));assert.throws(()=>d.pointsReceipt({paid:false,replayed:true}));
});
test('new resource payment receipts bind owner, kind, currency and exact minor units',()=>{
  assert.equal(d.resourceOrder(order(),'2',{amount:990,currency:'CNY'}).id,order().id);
  for(const patch of [{user_id:'3'},{kind:'recharge'},{amount:'9.91'},{currency:'USD'},{amount:'9.901'},{resource_url:'https://external.test/'}])assert.throws(()=>d.resourceOrder(order(patch),'2',{amount:990,currency:'CNY'}));
  assert.equal(d.resourceOrder(order({amount:'990',currency:'JPY'}),'2',{amount:990,currency:'JPY'}).amount,'990');
});
test('resumed resource orders keep original terms but sync must retain the same order id',()=>{
  const old=order({amount:'19.90',currency:'USD'});assert.equal(d.resourceOrder(old,'2').amount,'19.90');
  assert.throws(()=>d.resourceOrder(old,'2',{id:'np_aaaaaaaaaaaaaaaaaaaaaaaaaaaa'}));
});
test('download history accepts local article links and validates dates',()=>{
  const item={title:'资源',url:'/posts/resource#resource-bundle',created_at:'2026-11-28T08:00:00+08:00'};
  assert.deepEqual(d.resourceHistory({items:[item]}),[item]);
  for(const patch of [{url:'//evil.test'},{created_at:null},{created_at:'bad'}])assert.throws(()=>d.resourceHistory({items:[{...item,...patch}]}));
});
