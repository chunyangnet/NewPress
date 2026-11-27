const test=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm'),esbuild=require('esbuild'),F=require('./helpers/payment-fixtures.cjs');
const source=esbuild.buildSync({entryPoints:['frontend/shared/payment-data.ts'],bundle:true,write:false,format:'cjs',target:'es2022'}).outputFiles[0].text;
const moduleValue={exports:{}};vm.runInNewContext(source,{module:moduleValue,exports:moduleValue.exports,URL,Date,Map,Set});const D=moduleValue.exports;
test('payment catalogs keep exact integer strings and reject incomplete fields and duplicates',()=>{
  const cfg=F.billing();cfg.products[0].credit='9007199254740993';assert.equal(D.billingConfig(cfg,'2').products[0].credit,'9007199254740993');
  for(const change of [r=>r.user_id='3',r=>delete r.csrf_token,r=>delete r.require_verified_email,r=>r.products[0].enabled='true',r=>r.products[0].price=1000,r=>r.products[0].kind='resource',r=>r.channels.push(r.channels[0]),r=>r.currencies[0].precision=7,r=>r.currencies[0].enabled='true',r=>r.exchanges[0].maximum='0',r=>r.exchanges[0].fee_bps=5001,r=>r.exchanges[0].from=r.exchanges[0].to]){const value=F.billing();change(value);assert.throws(()=>D.billingConfig(value,'2'));}
});
test('public orders validate every displayed field and exact requested identity',()=>{
  const value=F.order();assert.equal(D.paymentOrder(value,'2',value.id).id,value.id);
  for(const field of Object.keys(value)){const v=F.order();delete v[field];assert.throws(()=>D.paymentOrder(v,'2'),field);}
  for(const change of [r=>r.user_id=2,r=>r.id='np_ffffffffffffffffffffffffffff',r=>r.status='succeeded',r=>r.amount='01.00',r=>r.credit='1e9',r=>r.kind='shop',r=>r.days='1',r=>r.expires_at='not-date',r=>r.checkout.fields={token:4}]){const v=F.order();change(v);assert.throws(()=>D.paymentOrder(v,'2',value.id));}
});
test('admin orders include shop and string ledger references without exposing extra fields',()=>{
  const value=F.adminOrder({kind:'shop',credit_entry:'9007199254740993',sealed_config:'not-public'});const result=D.adminPaymentOrders({orders:[value]},value.id)[0];assert.equal(result.credit_entry,'9007199254740993');assert.equal(result.sealed_config,undefined);
  assert.throws(()=>D.adminPaymentOrders({orders:[]},value.id));assert.throws(()=>D.adminPaymentOrders({orders:[value,value]}));assert.throws(()=>D.adminPaymentOrders({orders:[{...value,source_entry:4}]}));
});
test('payment amount validation respects bigint limits and explicit decimal precision',()=>{
  for(const value of ['0','0.00','90071992547409.93','9223372036854.775807'])assert.equal(D.paymentAmount(value),value);
  for(const value of ['9223372036854.775808','1.0000000','1e2','+1','-1','00.1',1,null])assert.throws(()=>D.paymentAmount(value));
});
test('admin catalog validates provider fields, masked settings and next save version',()=>{
  const cfg=D.paymentAdminConfig(F.admin());assert.equal(cfg.settings.channels[0].saved_secrets[0],'secret');assert.equal(cfg.settings.channels[0].config.secret,'');
  assert.equal(D.paymentSettings({...F.settings(),version:'3'},'2').version,'3');assert.throws(()=>D.paymentSettings(F.settings(),'2'));
  for(const change of [r=>r.providers[0].fields.push('unknown_secret'),r=>r.settings.channels[0].config.provider='unknown',r=>delete r.settings.channels[0].config.sandbox,r=>r.settings.channels[0].clear_secrets=['endpoint'],r=>r.settings.channels[0].saved_secrets=['secret','secret'],r=>r.settings.order_minutes=0,r=>r.plans[0].days=0]){const value=F.admin();change(value);assert.throws(()=>D.paymentAdminConfig(value));}
});
test('merchant secrets may only appear in an explicit outgoing settings draft',()=>{
  const value=F.settings();value.channels[0].config.secret='new-secret';assert.throws(()=>D.paymentSettings(value));assert.equal(D.paymentSettings(value,undefined,false).channels[0].config.secret,'new-secret');
});
test('pending payments reconstruct original payloads and disallow injected fields',()=>{
  const value=F.pending(),parsed=D.billingPending(value,'2');assert.deepEqual(JSON.parse(JSON.stringify(parsed)),value);value.body.product='changed';assert.equal(parsed.body.product,'coins');
  for(const change of [r=>r.owner='3',r=>r.body.amount='0.01',r=>r.body.request_id='short',r=>r.body.accepted=false,r=>r.body.version=2,r=>r.expected.kind='resource',r=>r.review[0]=['missing value']]){const v=F.pending();change(v);assert.throws(()=>D.billingPending(v,'2'));}
  const legacy=F.pending();delete legacy.expected.credit;delete legacy.expected.target_currency;assert.doesNotThrow(()=>D.billingPending(legacy,'2'));
});
test('pending exchanges keep the exact decimal amount and do not accept client credit overrides',()=>{
  const v=F.pending({kind:'exchange',body:{version:'2',rule:'stars',amount:'1.23',request_id:'original-exchange-request',accepted:true},expected:{kind:'exchange',amount:'1.23',currency:'main',credit:'3',target_currency:'stars'}});assert.equal(D.billingPending(v,'2').body.amount,'1.23');v.body.credit='30';assert.throws(()=>D.billingPending(v,'2'));delete v.body.credit;v.expected.amount='1.24';assert.throws(()=>D.billingPending(v,'2'));
});
test('confirmed purchase receipts match all available promised fields without invented echoes',()=>{
  const original=F.pending();assert.equal(D.paymentReceipt(F.order(),original).id,F.order().id);
  for(const field of ['amount','currency','credit','target_currency','kind'])assert.throws(()=>D.paymentReceipt(F.order({[field]:({amount:'10.01',currency:'USD',credit:'4.99',target_currency:'stars',kind:'membership'})[field]}),original));
  const legacy=F.pending();delete legacy.expected.credit;delete legacy.expected.target_currency;assert.doesNotThrow(()=>D.paymentReceipt(F.order(),legacy));
});
