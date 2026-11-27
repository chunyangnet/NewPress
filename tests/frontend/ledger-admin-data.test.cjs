const test=require('node:test'),assert=require('node:assert/strict');
const {loadModule}=require('./helpers/typescript.cjs');
const D=loadModule('frontend/core/ledger-admin-data.ts'),F=require('./helpers/ledger-admin-fixtures.cjs');
const copy=value=>structuredClone(value);
test('admin monetary values remain exact and match account/currency identity',()=>{
  const w=copy(F.wallet);w.balance='90071992547409.93';assert.equal(D.adminWallet(w,'main','READER','2').balance,w.balance);
  for(const change of [w=>w.user_id='3',w=>w.currency_key='gold',w=>w.username='someone',w=>w.balance=90071992547409.93,w=>delete w.settings.precision_locked,w=>w.next_before='unsafe']){const w=copy(F.wallet);change(w);assert.throws(()=>D.adminWallet(w,'main','reader','2'));}
  assert.equal(D.adminWallet({...w,status:'suspended'},'main','reader').status,'suspended');
});
test('currency settings require complete data and exactly the next saved version',()=>{
  assert.equal(D.currencySettingsResult({currency_key:'main',module:'identity',settings:{...F.currency,version:'9007199254740994'}},'main','9007199254740993','identity').version,'9007199254740994');
  for(const change of [r=>r.settings.version='2',r=>r.module='registration',r=>delete r.settings.registration_since,r=>r.settings.max_balance=10,r=>r.settings.precision=10]){const r={currency_key:'main',module:'identity',settings:{...F.currency,version:'3'}};change(r);assert.throws(()=>D.currencySettingsResult(r,'main','2','identity'));}
});
test('creation replay accepts current renamed settings but never incomplete receipts',()=>{
  const original={owner:'1',currency_key:'gold',key:'gold',name:'金币',symbol:'枚',precision:2,request_id:'create-gold-original'};
  assert.doesNotThrow(()=>D.currencyCreation({currency_key:'gold',settings:{...F.currency,name:'新名称',version:'8'},replayed:true},original));
  assert.throws(()=>D.currencyCreation({currency_key:'gold',settings:{version:'1'},replayed:true},original));
});
for(const field of ['currency_key','delta','balance','kind','reason','incoming','created_at','id'])test(`currency adjustment rejects invalid ${field} without inventing user echoes`,()=>{
  const receipt=F.currencyReceipt();assert.equal(D.currencyAdjustment(receipt,F.adjust).id,'20');receipt[field]={currency_key:'gold',delta:'1.26',balance:3,kind:'transfer_in',reason:'另一原因',incoming:false,created_at:'',id:'0'}[field];assert.throws(()=>D.currencyAdjustment(receipt,F.adjust));
});
test('admin growth rules normalize omitted defaults and retain administration fields',()=>{
  const cfg=copy(F.progression);delete cfg.membership.plans[0].tier;delete cfg.check_in.mode;delete cfg.check_in.timezone;
  const parsed=D.adminProgressionSettings(cfg);assert.equal(parsed.membership.plans[0].tier,1);assert.equal(parsed.check_in.mode,'interval');assert.equal(parsed.registration.since,'0001-01-01T00:00:00Z');
  for(const change of [r=>delete r.publication,r=>r.levels.items.push(r.levels.items[0]),r=>r.membership.plans[0].tier=101,r=>r.experience.maximum=9007199254740993]){const cfg=copy(F.progression);change(cfg);assert.throws(()=>D.adminProgressionSettings(cfg));}
});
test('growth accounts reject wrong owners, users, and duplicate entry pages',()=>{
  assert.equal(D.adminProgressionAccount(F.account,'reader','2').account.experience,'9007199254740993');
  for(const change of [r=>r.account.user_id='3',r=>r.username='other',r=>r.entries=[F.progressionReceipt().entry,F.progressionReceipt().entry]]){const r=copy(F.account);change(r);assert.throws(()=>D.adminProgressionAccount(r,'reader','2'));}
});
test('experience receipts verify actor, amount, reason, kind and the complete entry',()=>{
  assert.equal(D.progressionAdjustment(F.progressionReceipt(),F.experience,'1').entry.delta,'9007199254740993');
  for(const change of [r=>r.entry.actor_id='2',r=>r.entry.delta='9007199254740992',r=>r.entry.kind='check_in',r=>r.entry.reason='other',r=>delete r.entry.membership,r=>delete r.entry.days,r=>r.replayed='false']){const r=F.progressionReceipt();change(r);assert.throws(()=>D.progressionAdjustment(r,F.experience,'1'));}
});
test('membership receipts require the original action, plan, days and next membership version',()=>{
  for(const action of ['grant','renew','revoke']){const item=copy(F.member);item.payload.action=action;if(action==='revoke'){item.payload.days=0;item.payload.plan='';}assert.doesNotThrow(()=>D.progressionAdjustment(F.progressionReceipt(item),item,'1'));}
  for(const change of [r=>r.entry.membership.version='2',r=>r.entry.days=31,r=>r.entry.membership.plan='other',r=>r.entry.membership.revoked=true,r=>r.entry.delta='1']){const r=F.progressionReceipt(F.member);change(r);assert.throws(()=>D.progressionAdjustment(r,F.member,'1'));}
});
test('order recovery can remain pending and cancellation may discover an already completed payment',()=>{
  assert.equal(D.progressionOrderResult(F.order,F.order,'recover').status,'pending');assert.equal(D.progressionOrderResult({...F.order,status:'completed'},F.order,'cancel').status,'completed');
  assert.throws(()=>D.progressionOrderResult(F.order,F.order,'cancel'));
  for(const field of ['request_id','currency','amount','user_id','plan','days'])assert.throws(()=>D.progressionOrderResult({...F.order,[field]:field==='days'?31:'other'},F.order,'recover'));
});
test('preview accepts diagnostic invalid values while rejecting malformed or inconsistent checks',()=>{
  const result={...F.preview,valid:false,issues:[{severity:'error',field:'plans',code:'invalid',message:'价格无效'}],plans:[{index:0,key:'bad',name:'坏方案',currency:'main',currency_name:'积分',symbol:'分',input_price:'-1',price:'-1',amount:'-100',precision:2,days:-2,experience_percent:0,enabled:true,purchase_enabled:true,status:'error'}]};
  assert.equal(D.progressionPreview(result).plans[0].days,-2);assert.throws(()=>D.progressionPreview({...result,valid:true}));assert.throws(()=>D.progressionPreview({...result,plans:[{...result.plans[0],status:'unknown'}]}));
});
