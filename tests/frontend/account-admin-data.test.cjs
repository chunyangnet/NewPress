const test=require('node:test'),assert=require('node:assert/strict');
const {loadModules}=require('./helpers/typescript.cjs'),F=require('./helpers/account-admin-fixtures.cjs'),L=require('./helpers/ledger-admin-fixtures.cjs');
const [D,R,U,H]=loadModules(['frontend/shared/admin-config-data.ts','frontend/shared/registration-admin-data.ts','frontend/shared/users-admin-data.ts','frontend/shared/registration-code-helpers.ts'],{URL,structuredClone,Date});
const plain=value=>structuredClone(value);
test('configuration reads reject missing flags, secrets, duplicate services and unsafe links',()=>{
 assert.equal(D.accessConfig(F.access()).settings.sms.timeout_seconds,8);assert.equal(D.verificationConfig(F.verification()).providers.length,7);
 for(const change of [v=>delete v.settings.sms.enabled,v=>v.settings.sms.secret='leaked',v=>v.providers[0].application_url='javascript:alert(1)',v=>v.providers[1]=v.providers[0],v=>v.settings.sms.timeout_seconds='8']){const v=F.access();change(v);assert.throws(()=>D.accessConfig(v));}
 for(const change of [v=>delete v.settings.scenes.login,v=>v.settings.scenes.login.enabled='false',v=>v.settings.default_provider='unknown',v=>v.providers[0].docs='http://example.test']){const v=F.verification();change(v);assert.throws(()=>D.verificationConfig(v));}
});
test('access receipts compare normalized values and secret identity without claiming unknown secret material',()=>{
 const raw=F.access();raw.settings.providers[0].client_id='old';raw.settings.providers[0].has_secret=true;
 const baseline=D.accessConfig(raw).settings,sent=plain(baseline);sent.providers[0].client_id=' new ';
 const saved=F.access();saved.settings.revision='changed';saved.settings.providers[0].client_id='new';
 assert.equal(D.savedAccess(saved,sent,baseline).settings.revision,'changed');
 saved.settings.providers[0].has_secret=true;assert.throws(()=>D.savedAccess(saved,sent,baseline));
 sent.providers[0].secret='new-secret';assert.equal(D.savedAccess(saved,sent,baseline).settings.providers[0].has_secret,true);
 saved.settings.sms.template_id='wrong';assert.throws(()=>D.savedAccess(saved,sent,baseline));
});
test('saving settings preserves later input and clears only submitted secret values',()=>{
 const sent=D.accessConfig(F.access()).settings;sent.sms.secret='first';const later=plain(sent);later.sms.secret='second';later.sms.template_id='new draft';
 const saved=plain(sent);saved.revision='next';saved.sms.secret='';saved.sms.has_secret=true;
 const merged=D.mergeAccess(later,sent,saved);assert.equal(merged.sms.secret,'second');assert.equal(merged.sms.template_id,'new draft');assert.equal(merged.revision,'next');
 const old=plain(sent);assert.equal(D.mergeAccess(old,sent,saved).sms.secret,'');
});
test('verification receipts compare scenes and both independent secret identities',()=>{
 const raw=F.verification();raw.settings.providers=[{id:'tencent',site_key:'100',secret_id:'old',has_secret:true,has_api_secret:true,min_score:.5,recaptcha_net:true}];
 const baseline=D.verificationConfig(raw).settings,sent=plain(baseline);sent.providers[0].secret_id='new';
 const saved=plain(raw.settings);saved.revision='new';saved.providers[0].secret_id='new';saved.providers[0].has_api_secret=false;
 assert.equal(D.savedVerification(saved,sent,baseline).providers[0].has_secret,true);
 saved.providers[0].has_api_secret=true;assert.throws(()=>D.savedVerification(saved,sent,baseline));
 saved.providers[0].has_api_secret=false;saved.scenes.login.enabled=true;assert.throws(()=>D.savedVerification(saved,sent,baseline));
 const later=plain(sent);later.providers[0].api_secret='second';const merged=D.mergeVerification(later,sent,D.verificationSettings({...saved,scenes:sent.scenes}));assert.equal(merged.providers[0].api_secret,'second');
});
test('registration inventory rejects duplicate codes, malformed versions, dates and policy',()=>{
 const value={items:[F.code()],policy:F.policy(),now:'2026-11-26T10:00:00Z',max_batch:500,max_codes:5000};assert.equal(R.codeInventory(value).items.length,1);
 for(const c of [F.code({version:1.5}),F.code({id:'x'}),F.code({used:1}),F.code({updated_at:'2020-01-01T00:00:00Z'})])assert.throws(()=>R.codeInventory({...value,items:[c]}));
 assert.throws(()=>R.codeInventory({...value,items:[F.code(),F.code()]}));assert.throws(()=>R.codeInventory({...value,policy:{...F.policy(),mode:'unknown'}}));
});
test('registration update and delete receipts require original target, version and untouched fields',()=>{
 const old=F.code({starts_at:'2026-11-26T00:00:42Z'}),operation={action:'update',targets:[{id:old.id,version:1}],patch:{note:' changed '}};
 const saved={...old,version:2,note:'changed'};assert.equal(R.codeReceipt({items:[saved],count:1},operation,[old]).count,1);
 for(const patch of [{code:'OTHER-CODE'},{version:3},{starts_at:'2026-11-26T00:00:00Z'},{used:1,last_used_at:'2026-11-26T10:00:00Z'},{note:'incorrect'}])assert.throws(()=>R.codeReceipt({items:[{...saved,...patch}],count:1},operation,[old]));
 assert.equal(R.codeReceipt({items:[old],count:1},{action:'delete',targets:operation.targets},[old]).count,1);
});
test('registration creates require exact count, novel codes and requested generator layout',()=>{
 const row=F.code({code:'NP-123-456'}),op={action:'generate',rules:{enabled:true,max_uses:0,note:'原备注'},generator:{count:1,prefix:'NP-',length:6,charset:'digits',group:3}};
 assert.equal(R.codeReceipt({items:[row],count:1},op,[]).count,1);
 for(const code of ['XX-123-456','NP-12-3456','NP-ABC-DEF'])assert.throws(()=>R.codeReceipt({items:[{...row,code}],count:1},op,[]));
 assert.throws(()=>R.codeReceipt({items:[row],count:1},op,[row]));
});
test('registration policy receipts reject stale versions and mismatched fields',()=>{
 const sent={...F.policy(),mode:'required',code_url:' /codes '},saved={...sent,revision:'next',code_url:'/codes'};assert.equal(R.policyReceipt(saved,sent).mode,'required');assert.throws(()=>R.policyReceipt({...saved,mode:'hidden'},sent));assert.throws(()=>R.policyReceipt({...saved,revision:sent.revision},sent));
});
test('user pending operations bind actor, target and allowed route while keeping decimal precision',()=>{
 const op={url:'/api/v1/admin/progression/experience',body:L.experience.payload,saved:false,actor:'1',target:'2',precision:0,uncertain:true};assert.equal(U.userOperation(op,'1','2').body.delta,'9007199254740993');assert.throws(()=>U.userOperation(op,'2','2'));assert.throws(()=>U.userOperation({...op,url:'https://example.test'},'1','2'));
 U.userReceipt(L.progressionReceipt(),op);assert.throws(()=>U.userReceipt({...L.progressionReceipt(),entry:{...L.progressionReceipt().entry,actor_id:'3'}},op));
});
test('membership set receipts require exact times, plan and membership version',()=>{
 const body={user_id:'2',version:'2',member_version:'0',action:'set',plan:'member',days:0,since:'2026-11-26T10:00:42Z',until:'2026-12-26T10:00:49Z',reason:'设置会员',request_id:'membership-set-original'},op={url:'/api/v1/admin/progression/membership',body,saved:false,actor:'1',target:'2',precision:0,uncertain:false};
 const receipt=L.progressionReceipt({kind:'membership',payload:body});receipt.entry.membership.since=body.since;receipt.entry.membership.until=body.until;U.userReceipt(receipt,op);
 receipt.entry.membership.until='2026-12-26T10:00:00Z';assert.throws(()=>U.userReceipt(receipt,op));
});
test('recognition numeric identities, grants and exact audit event drive receipts',()=>{
 const body={user_id:2,version:1,action:'grant',key:'original',label:'原创',evidence:'',reason:'验收',days:0},op={url:'/api/v1/admin/recognition/account',body,saved:false,actor:'1',target:'2',precision:0,uncertain:true};
 const t='2026-11-26T10:00:00Z',identity={version:2,grants:[{key:'original',label:'原创',actor_id:1,granted_at:t,expires_at:'0001-01-01T00:00:00Z'}],equipped:['original'],history:[{action:'grant',key:'original',reason:'验收',actor_id:1,created_at:t}]};
 assert.equal(U.userOperation(op,'1','2').body.version,1);U.recognitionReceipt({identity},op);
 for(const patch of [{version:3},{history:[]},{grants:[]},{grants:[{...identity.grants[0],label:'other'}]}])assert.throws(()=>U.recognitionReceipt({identity:{...identity,...patch}},op));
 assert.throws(()=>U.recognitionAccount({user_id:3,username:'owner',name:'owner',identity},'2','owner'));
});
test('ported registration helpers preserve imports, escaping and status boundaries',()=>{
 const row=F.code({code:'-MY-CODE',note:'=SUM(1,2)'});assert.deepEqual(plain(H.parseImport(H.exportCSV([row]))),['-MY-CODE']);assert.throws(()=>H.parseImport('[null]'));assert.throws(()=>H.parseImport('SAME\nSAME'));assert.throws(()=>H.maxUses(''));assert.equal(H.statusOf({...row,max_uses:1,used:1}),'exhausted');
});
