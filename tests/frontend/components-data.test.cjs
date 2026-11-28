const test = require('node:test');
const assert = require('node:assert/strict');
const {loadNativeModule} = require('./helpers/typescript.cjs');
const data = loadNativeModule('frontend/core/components-data.ts');
const configData = loadNativeModule('frontend/core/component-config.ts');
const config=(kind='poll')=>({...configData.emptyConfig(),id:'component_test_poll',kind,mode:kind==='redpacket'?'login':'both',items:kind==='poll'?[{title:'甲'},{title:'乙'}]:[],max:kind==='rating'?5:0,count:kind==='redpacket'?2:0,amount:kind==='redpacket'?'1.00':'',currency:'main'});
const encode=config=>Buffer.from(JSON.stringify(config)).toString('base64url');
const state=(kind='poll')=>({counts:kind==='poll'?[0,0]:kind==='rating'?[0,0,0,0,0,0]:[],total:0,average:0,participated:false,choice:-1,active:kind==='redpacket',claimed:0,pending:0,pending_self:false});
const snapshot=c=>({state:state(c.kind),csrf_token:'test-token',authenticated:true,owner:false,fingerprint:'a'.repeat(64),config:encode(c),currency_name:'积分'});
test('component target accepts independent article IDs and shop product keys',()=>{
  for(const value of ['1','9223372036854775807','shop:first','shop:course_2','shop:1'])assert.equal(data.validComponentTarget(value),true,value);
  for(const value of ['0','-1','shop:','shop:../../admin','shop:UPPER','1?admin','1/other'])assert.equal(data.validComponentTarget(value),false,value);
});
test('public component configuration uses strict shared decoder and fills omitted Go fields',()=>{
  const raw={v:1,id:'component_test_poll',kind:'poll',items:[{title:'甲'},{title:'乙'}],mode:'both'};
  const parsed=data.decodePublicComponent(encode(raw),'poll');
  assert.equal(parsed.text,'');assert.equal(parsed.items[0].url,'');
  for(const input of [{...raw,v:2},{...raw,items:'bad'},{...raw,id:'short'},{...raw,kind:'unknown'}])assert.throws(()=>data.decodePublicComponent(encode(input),'poll'));
  assert.throws(()=>data.decodePublicComponent(encode(raw),'rating'));
  assert.throws(()=>data.decodePublicComponent('x'.repeat(87385),'poll'));
});
test('poll counts, participation and average are mutually consistent',()=>{
  const c=config(),value={...state(),counts:[2,1],total:3,average:1/3,participated:true,choice:1};
  assert.equal(data.parseComponentState(value,c).total,3);
  for(const patch of [{total:4},{average:0},{counts:[-1,4]},{counts:[2,1,0]},{participated:'true'},{choice:-1},{pending:1}])assert.throws(()=>data.parseComponentState({...value,...patch},c));
});
test('rating uses Go one-based choices and validates the weighted average',()=>{
  const c=config('rating'),value={...state('rating'),counts:[0,0,0,1,0,1],total:2,average:4,participated:true,choice:5};
  assert.equal(data.parseComponentState(value,c).average,4);
  assert.throws(()=>data.parseComponentState({...value,choice:0},c));
  assert.throws(()=>data.parseComponentState({...value,average:NaN},c));
});
test('redpacket counts respect capacity and pending/confirmed claimant semantics',()=>{
  const c=config('redpacket'),value={...state('redpacket'),pending:1,pending_self:true};
  assert.equal(data.parseComponentState(value,c).pending_self,true);
  for(const patch of [{claimed:2},{pending:0},{participated:true}])assert.throws(()=>data.parseComponentState({...value,...patch},c));
  assert.equal(data.parseComponentState({...value,pending:0,pending_self:false,claimed:1,participated:true},c).participated,true);
});
test('snapshot binds the exact encoded config, fingerprint and login fields',()=>{
  const c=config(),value=snapshot(c);
  assert.equal(data.parseComponentSnapshot(value,c,encode(c)).fingerprint,value.fingerprint);
  for(const patch of [{config:'other'},{fingerprint:'x'},{owner:true,authenticated:false},{csrf_token:''},{owner:'true'}])assert.throws(()=>data.parseComponentSnapshot({...value,...patch},c,encode(c)));
  assert.equal(data.parseComponentSnapshot({...value,authenticated:false,csrf_token:'visitor-cookie'},c,encode(c)).authenticated,false);
});
test('vote receipt cannot confirm a different choice even when counts are valid',()=>{
  const c=config(),attempt={action:'vote',value:1,fingerprint:'a'.repeat(64),token:'csrf'};
  const value={...state(),counts:[0,1],total:1,average:1,participated:true,choice:1};
  assert.equal(data.parseComponentReceipt({state:value},c,attempt).choice,1);
  assert.throws(()=>data.parseComponentReceipt({state:{...value,counts:[1,0],average:0,choice:0}},c,attempt),/未确认/);
  assert.throws(()=>data.parseComponentReceipt({state:state()},c,attempt),/未确认/);
});
for(const action of ['claim','activate','pause'])test('redpacket '+action+' requires its actual successful receipt',()=>{
  const c=config('redpacket'),attempt={action,value:0,fingerprint:'a'.repeat(64),token:'csrf'};
  const value={...state('redpacket'),active:action!=='pause',claimed:action==='claim'?1:0,participated:action==='claim'};
  assert.equal(data.componentActionConfirmed(data.parseComponentReceipt({state:value},c,attempt),attempt),true);
  const wrong=action==='claim'?{...value,participated:false}:{...value,active:!value.active};
  assert.throws(()=>data.parseComponentReceipt({state:wrong},c,attempt),/未确认/);
});
test('profile parser rejects malformed responses and error parser handles both Go shapes',()=>{
  const value={name:'用户',bio:'说明',avatar:'/avatar.png',url:'/members/1',meta:'本站',cached:false};
  assert.equal(data.parseComponentProfile(value).name,'用户');
  assert.throws(()=>data.parseComponentProfile({...value,name:{}}));
  assert.throws(()=>data.parseComponentProfile({...value,cached:'false'}));
  assert.equal(data.componentError({error:{message:'会话过期'}}),'会话过期');
  assert.equal(data.componentError({error:'组件不可用'}),'组件不可用');
});
