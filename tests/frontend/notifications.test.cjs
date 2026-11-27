'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const {compile} = require('./helpers/typescript.cjs');
for (const [entry, source] of [
  ['TypeScript', compile('frontend/shared/notifications.ts')],
  ['generated', fs.readFileSync(path.join(__dirname, '../../themes/chunnel/assets/js/chunnel-notifications.js'), 'utf8')],
]) {
const tick = () => new Promise(resolve => setImmediate(resolve));
const response = (overrides = {}) => ({ preferences: {version:1,reply:true,mention:true,publication:true,moderation:true,email:false,weekly:false,...overrides},email_available:true,mail_status:{pending:0,failed:0,uncertain:0},timezone:'Asia/Shanghai' });
function fixture() {
  const nodes = new Map(), requests = [], documentEvents = {};
  class Input { constructor(){this.hidden=false;this.disabled=false;this.checked=false;this.textContent='';this.events={};} addEventListener(name,fn){this.events[name]=fn;} }
  const element = () => new Input();
  const get = selector => { if(!nodes.has(selector)) nodes.set(selector,element());return nodes.get(selector); };
  const form = get('form');form.elements=Object.fromEntries(['reply','mention','publication','moderation','email','weekly'].map(name=>[name,element()]));
  form.elements.namedItem=name=>form.elements[name];
  const fieldsets=[element(),element()];form.querySelector=get;form.querySelectorAll=()=>fieldsets;
  const window={addEventListener(){},ChunnelInteractions:{api:(url,options)=>new Promise((resolve,reject)=>requests.push({url,options,resolve,reject}))}};
  const root={querySelector:get,closest:()=>({dataset:{accountUser:'7'}})};
  vm.runInNewContext(source,{window,HTMLInputElement:Input,document:{querySelector:()=>root,addEventListener:(name,fn)=>{documentEvents[name]=fn;}}});
  return {requests,form,get,root,session:detail=>documentEvents['chunnel:session']?.({detail}),load:()=>window.ChunnelNotificationSettings.load(),submit:()=>form.events.submit({preventDefault(){}}),respond:(i,data)=>requests[i].resolve(data),reject:(i,error)=>requests[i].reject(error)};
}
test('preference save and reload cannot overwrite an in-flight user edit',async()=>{
  const f=fixture();f.load();f.respond(0,response());await tick();f.form.elements.reply.checked=false;
  f.submit();f.submit();f.load();assert.equal(f.requests.length,2);assert.equal(f.requests[1].options.body.reply,false);assert.equal(f.requests[1].options.body.version,1);
  f.reject(1,new Error('保存失败'));await tick();assert.equal(f.form.elements.reply.checked,false);assert.equal(f.get('[type=submit]').disabled,false);assert.equal(f.get('[data-notification-settings-result]').textContent,'保存失败');
});
test('a preference conflict keeps the old version and waits for explicit reload',async()=>{
  const f=fixture();f.load();f.respond(0,response());await tick();f.submit();f.reject(1,Object.assign(new Error('版本冲突'),{status:409}));await tick();
  assert.equal(f.requests.length,2);f.get('[data-notification-settings-refresh]').events.click();f.respond(2,response({version:4,reply:false}));await tick();f.submit();assert.equal(f.requests[3].options.body.version,4);assert.equal(f.requests[3].options.body.reply,false);f.respond(3,response({version:5,reply:false}));await tick();
});
test('mail unavailable still allows an existing subscription to be turned off',async()=>{
  const f=fixture();f.load();f.respond(0,{...response({email:true}),email_available:false});await tick();
  assert.equal(f.form.elements.email.disabled,false);assert.equal(f.form.elements.weekly.disabled,true);f.form.elements.email.checked=false;f.submit();
  assert.equal(f.requests[1].options.body.email,false);f.respond(1,{...response({version:2}),email_available:false});await tick();assert.equal(f.form.elements.email.disabled,true);
});
test('retry updates status without exposing mail identifiers or retrying twice',async()=>{
  const f=fixture();f.load();f.respond(0,{...response(),mail_status:{pending:0,failed:2,uncertain:1}});await tick();
  const retry=f.get('[data-notification-mail-retry]');assert.equal(retry.hidden,false);assert.equal(f.get('[data-notification-uncertain]').hidden,false);
  retry.events.click();retry.events.click();assert.equal(f.requests.length,2);assert.equal(f.requests[1].url,'/api/v1/social/notification-mail/retry');assert.deepEqual(JSON.parse(JSON.stringify(f.requests[1].options.body)),{});
  f.respond(1,{ok:true});await tick();f.respond(2,{...response(),mail_status:{pending:2,failed:0,uncertain:1}});await tick();assert.equal(retry.hidden,true);assert.equal(f.get('[data-notification-uncertain]').hidden,false);
});

test('returning to preferences preserves unsaved choices and their original version',async()=>{
  const f=fixture();f.load();f.respond(0,response());await tick();f.form.elements.reply.checked=false;
  f.load();f.respond(1,{...response({version:3}),mail_status:{pending:2,failed:0,uncertain:0}});await tick();
  assert.equal(f.form.elements.reply.checked,false);
  assert.match(f.get('[data-notification-delivery-status]').textContent,/等待投递 2/);
  assert.match(f.get('[data-notification-settings-result]').textContent,/其他窗口/);
  assert.equal(f.get('[data-notification-settings-discard]').hidden,false);
  f.submit();assert.equal(f.requests[2].options.body.version,1);assert.equal(f.requests[2].options.body.reply,false);
  f.respond(2,response({version:4,reply:false}));await tick();assert.equal(f.get('[data-notification-settings-discard]').hidden,true);
});

test('retrying failed mail refreshes delivery counts without overwriting unsaved preferences',async()=>{
  const f=fixture();f.load();f.respond(0,{...response(),mail_status:{pending:0,failed:1,uncertain:0}});await tick();
  f.form.elements.mention.checked=false;
  f.get('[data-notification-mail-retry]').events.click();f.respond(1,{ok:true});await tick();f.respond(2,response());await tick();
  assert.equal(f.form.elements.mention.checked,false);
  assert.equal(f.get('[data-notification-settings-discard]').hidden,false);
});

test('discarding unsaved settings is explicit and only consumes them after a successful reload',async()=>{
  const f=fixture();f.load();f.respond(0,response());await tick();f.form.elements.reply.checked=false;
  const discard=f.get('[data-notification-settings-discard]');
  assert.equal(typeof discard.events.click,'function');
  discard.events.click();f.reject(1,new Error('离线'));await tick();assert.equal(f.form.elements.reply.checked,false);
  discard.events.click();f.respond(2,response({version:5}));await tick();assert.equal(f.form.elements.reply.checked,true);
  f.submit();assert.equal(f.requests[3].options.body.version,5);
});

test('a confirmed mail retry reports refresh failure without suggesting another delivery',async()=>{
  const f=fixture();f.load();f.respond(0,{...response(),mail_status:{pending:0,failed:1,uncertain:0}});await tick();
  f.get('[data-notification-mail-retry]').events.click();f.respond(1,{ok:true});await tick();f.reject(2,new Error('离线'));await tick();
  assert.match(f.get('[data-notification-settings-result]').textContent,/已重新排队.*刷新状态/);
  assert.equal(f.get('[data-notification-mail-retry]').hidden,true);
});

test(`${entry}: malformed preferences keep unsaved choices and the original version`,async()=>{
  const f=fixture();f.load();f.respond(0,response());await tick();f.form.elements.reply.checked=false;
  f.submit();f.respond(1,response({version:8,weekly:'false'}));await tick();
  assert.equal(f.form.elements.reply.checked,false);assert.doesNotMatch(f.get('[data-notification-settings-result]').textContent,/已保存/);
  f.submit();assert.equal(f.requests[2].options.body.version,1);f.respond(2,response({version:2,reply:false}));await tick();
});
test(`${entry}: unconfirmed retry keeps delivery failure available`,async()=>{
  const f=fixture();f.load();f.respond(0,{...response(),mail_status:{pending:0,failed:1,uncertain:0}});await tick();
  f.get('[data-notification-mail-retry]').events.click();f.respond(1,{ok:false});await tick();
  assert.equal(f.requests.length,2);assert.equal(f.get('[data-notification-mail-retry]').hidden,false);
  assert.doesNotMatch(f.get('[data-notification-settings-result]').textContent,/已重新排队/);
});
test(`${entry}: identity changes clear preferences and ignore a delayed save`,async()=>{
  const f=fixture();f.load();f.respond(0,response());await tick();f.submit();f.session({authenticated:true,id:8});
  f.respond(1,response({version:2}));await tick();
  assert.equal(f.root.hidden,true);assert.equal(f.form.elements.reply.checked,false);assert.equal(f.get('[type=submit]').disabled,true);
  assert.equal(f.get('[data-notification-settings-result]').textContent,'');await f.load();assert.equal(f.requests.length,2);
});
test(`${entry}: initial invalid response cannot enable saving defaults`,async()=>{
  const f=fixture();f.load();f.respond(0,{preferences:{version:0}});await tick();
  assert.equal(f.get('[type=submit]').disabled,true);f.submit();assert.equal(f.requests.length,1);
});
}
