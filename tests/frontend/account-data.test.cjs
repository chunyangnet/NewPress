const test = require('node:test');
const assert = require('node:assert/strict');
const {loadModule} = require('./helpers/typescript.cjs');
const data=loadModule('frontend/shared/account-data.ts'), access=loadModule('frontend/shared/registration-data.ts');
test('engagement requires complete, unique states and safe integer IDs/counts', () => {
  const valid={bookmark:[],like:[{target_id:7,count:2,active:true}],follow:[]};
  assert.equal(data.parseEngagement(valid).like[0].collection,'');
  for(const patch of [{count:'2'},{count:-1},{active:'false'},{target_id:Number.MAX_SAFE_INTEGER+1}]) assert.throws(()=>data.parseEngagement({...valid,like:[{...valid.like[0],...patch}]}));
  assert.throws(()=>data.parseEngagement({...valid,like:[...valid.like,...valid.like]})); assert.throws(()=>data.parseEngagement({like:[]}));
});
test('import requires the matching preview mode and valid counts', () => {
  assert.equal(data.parseImport({preview:true,added:2,existing:1,skipped:0},true).added,2);
  for(const value of [{added:1,existing:0,skipped:0},{preview:false,added:1,existing:0,skipped:0},{preview:true,added:-1,existing:0,skipped:0}]) assert.throws(()=>data.parseImport(value,true));
  for(const value of [{},{ok:1},{ok:'true'},{ok:false}]) assert.throws(()=>data.confirmed(value));
});
test('private content rejects unsafe navigation and identity mismatch', () => {
  const row={id:1,title:'Title',url:'/posts/a',status:'publish',excerpt:'',updated_at:'2026-11-26T00:00:00Z',edit_url:'/admin/content/1/edit/'};
  assert.equal(data.parseLibrary({pages:1,total:1,items:[row]},'posts').items[0].id,1);
  for(const url of ['https://outside.test/','//outside.test/','/\\outside.test/','javascript:alert(1)','/posts/ a']) assert.throws(()=>data.parseLibrary({pages:1,total:1,items:[{...row,url}]},'posts'));
  const profile={id:1,version:1,username:'one',display_name:'One',bio:'',locale:'zh-CN',avatar_url:''};
  assert.throws(()=>data.parseProfile(profile,2)); assert.throws(()=>data.parseProfile({...profile,version:0},1)); assert.throws(()=>data.parseProfile({...profile,avatar_url:'/admin/'},1));
});
test('access policy and code responses fail closed on malformed fields', () => {
  const policy={configured:true,registration:'open',fields:Object.fromEntries(access.registrationFields.map(key=>[key,'optional'])),email_available:true,sms_available:false,recovery_available:true,email_verification:false,phone_verification:false,password_confirmation:true};
  assert.equal(access.parsePolicy(policy).phone_region.default_code,'+86');
  for(const patch of [{configured:'true'},{registration:'anything'},{fields:{}},{oauth_bindings:['not_a_field']},{phone_region:{selectable:true,default_code:'+86',codes:['+1']}}]) assert.throws(()=>access.parsePolicy({...policy,...patch}));
  for(const value of [{challenge:'',retry_after:60},{challenge:'token',retry_after:'60'},{challenge:'token',retry_after:-1}]) assert.throws(()=>access.parseCode(value));
});
