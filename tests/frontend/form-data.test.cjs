const test = require('node:test');
const assert = require('node:assert/strict');
const {loadModule} = require('./helpers/typescript.cjs');
const data = loadModule('frontend/shared/form-data.ts');
const reply = {action:'reply',version:3,item_id:5};
const receipt = {slug:'ideas',version:8,item_id:5,reply_id:9,reply_page:2,replay:true};

test('feedback appends and votes accept the Go receipt after intervening board writes', () => {
  assert.equal(data.feedbackReceipt(receipt,'ideas',reply).reply_page,2);
  assert.equal(data.feedbackReceipt(receipt,'ideas',{action:'submit',version:3}).item_id,5);
  const vote=data.feedbackReceipt({...receipt,vote_count:0,voted:false},'ideas',{action:'vote',version:3,item_id:5,enabled:true});
  assert.equal(vote.voted,false); // Replays report current votes, not the historical requested state.
  assert.throws(()=>data.feedbackReceipt(receipt,'ideas',{action:'edit',version:3,item_id:5}));
  assert.equal(data.feedbackReceipt({...receipt,version:4},'ideas',{action:'edit',version:3,item_id:5}).version,4);
  assert.equal(data.feedbackReceipt({slug:'new-board',version:1},'new-board',{action:'create',version:0}).version,1);
});
for(const [label,patch] of Object.entries({slug:{slug:'private'},stale:{version:3},target:{item_id:6},missingItem:{item_id:undefined},missingReply:{reply_id:undefined},missingPage:{reply_page:undefined},unsafeID:{reply_id:2**54},replay:{replay:'yes'}})) {
  test(`feedback rejects a ${label} receipt before confirming the draft`,()=>assert.throws(()=>data.feedbackReceipt({...receipt,...patch},'ideas',reply)));
}
test('feedback hide_reply follows Go receipt without an echoed reply ID',()=>assert.equal(data.feedbackReceipt({slug:'ideas',version:4,item_id:5},'ideas',{action:'hide_reply',version:3,item_id:5,reply_id:9}).version,4));
test('vote count and state are validated together before acknowledging a mutation',()=>{
  const command={action:'vote',version:3,item_id:5};
  for(const patch of [{vote_count:-1,voted:true},{vote_count:1,voted:'true'},{vote_count:2**54,voted:true},{voted:false}])assert.throws(()=>data.feedbackReceipt({...receipt,...patch},'ideas',command));
});

const invite={id:'a'.repeat(64),email:'reader@example.test',expires_at:'2026-12-03T10:00:00+08:00',state:'pending'};
const list={items:[invite],available:true,registration:'invite'};
test('invitation list requires complete unique records and valid registration state',()=>{
  assert.equal(data.invitations(list).items[0].email,invite.email);
  for(const patch of [{available:'true'},{registration:'unknown'},{items:null},{items:[invite,invite]}])assert.throws(()=>data.invitations({...list,...patch}));
});
for(const [label,patch]of Object.entries({identifier:{id:'unsafe"selector'},email:{email:''},expiry:{expires_at:'not-a-date'},state:{state:'queued'}})) {
  test(`invitation list rejects invalid ${label} without partially replacing rows`,()=>assert.throws(()=>data.invitations({...list,items:[invite,{...invite,id:'b'.repeat(64),...patch}]})));
}
test('invitation send and revoke require an affirmative acknowledgement',()=>{
  assert.doesNotThrow(()=>data.invitationReceipt({ok:true}));
  for(const bad of [null,{},[],{ok:'true'},{ok:false}])assert.throws(()=>data.invitationReceipt(bad));
});

const config={scene:'feedback',revision:'current',enabled:true,provider:'local',theme:'auto',language:'zh-CN'};
test('verification binds even disabled configuration to the requested scene and revision',()=>{
  assert.equal(data.verificationConfig({...config,enabled:false},'feedback').enabled,false);
  for(const patch of [{scene:'login'},{revision:''},{enabled:0}])assert.throws(()=>data.verificationConfig({...config,...patch},'feedback'));
});
test('verification accepts all configured providers and requires external site keys',()=>{
  for(const provider of data.verificationProviders)assert.equal(data.verificationConfig({...config,provider,site_key:'public'},'feedback').provider,provider);
  for(const patch of [{provider:'unknown'},{provider:'turnstile'},{theme:'unknown'},{language:''},{recaptcha_net:'true'}])assert.throws(()=>data.verificationConfig({...config,...patch},'feedback'));
});
test('local challenges require bounded expiry and a PNG data URL',()=>{
  const challenge={id:'abc_X-42',image:'data:image/png;base64,YQ==',expires_in:120};
  assert.equal(data.verificationChallenge(challenge).id,challenge.id);
  for(const patch of [{id:''},{id:'../../unsafe'},{image:'https://external.test/image.png'},{image:'data:image/svg+xml;base64,YQ=='},{expires_in:0},{expires_in:601},{expires_in:'120'}])assert.throws(()=>data.verificationChallenge({...challenge,...patch}));
});
test('preview confirmation cannot be substituted for a one-use business token',()=>{
  assert.equal(data.verificationReceipt({ok:true,preview:true},true),'preview-passed');
  assert.equal(data.verificationReceipt({token:'one-use',expires_in:120},false),'one-use');
  for(const bad of [{ok:true,preview:true},{token:''},{token:'x',expires_in:121},{token:'x',expires_in:0}])assert.throws(()=>data.verificationReceipt(bad,false));
  for(const bad of [{ok:true},{preview:true},{token:'one-use',expires_in:120}])assert.throws(()=>data.verificationReceipt(bad,true));
});
