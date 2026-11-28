const test = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = process.env.CHUNNEL_UPDATES_SOURCE ? readFileSync(process.env.CHUNNEL_UPDATES_SOURCE,'utf8') : require('./helpers/typescript.cjs').compile('frontend/core/updates.ts');
const tick = () => new Promise(resolve => setImmediate(resolve));
const defaults = { auto_check: true, check_interval_minutes: 360, observation_seconds: 120,
  customized: false, launcher_applies_window: true, revision: '1'.repeat(64) };
const state=(extra={})=>({id:'',phase:'idle',percent:0,downloaded:0,total:0,target:'',started_at:'0001-01-01T00:00:00Z',updated_at:'0001-01-01T00:00:00Z',logs:[],...extra});
const sourceDTO={official:false,owner:'private-owner',repo:'private-repo',ref:'main',manifest:'updates/stable.json'};
const checked=revision=>({current:'1.0.0',platform:'windows/amd64',environment:'test',managed:true,configured:true,trusted_publisher:true,token_set:false,environment_token:false,discovery:{checked:'0001-01-01T00:00:00Z',message:''},source:{...sourceDTO},policy:defaults,revision,available:revision?{version:'1.1.0',notes:''}:undefined,state:state()});

function fixture(ticket = '', readOnly = false) {
  const calls=[],timers=new Map(),nodes=new Map(),documentEvents={};let clock=0;
  class Input {constructor(value=''){this.value=value;this.checked=false;this.events={};} addEventListener(type,fn){this.events[type]=fn;}}
  const inputs=values=>Object.assign(values,{namedItem(name){return values[name];}});
  const node = () => ({ dataset: {}, checked: false, events: {}, querySelector: () => null,
    setAttribute(key, value) { this[key] = value; },
    addEventListener(type, fn) { this.events[type] = fn; }, append() {}, replaceChildren() {} });
  const number=(min,max)=>{const field=new Input();Object.defineProperties(field,{valueAsNumber:{get(){return this.value===''?NaN:Number(this.value);}},validity:{get(){return {valid:this.valueAsNumber>=min&&this.valueAsNumber<=max};}}});return field;};
  function get(name) { if (!nodes.has(name)) nodes.set(name, node()); return nodes.get(name); }
  get('source').elements=inputs({official:new Input(),token:new Input(),clear_token:new Input(),owner:new Input('private-owner'),repo:new Input('private-repo'),ref:new Input('main'),manifest:new Input('updates/stable.json')});
  const form = get('policy-form'), bar = get('policy-bar');
  form.elements=inputs({auto_check:new Input(),check_interval_minutes:number(1,10080),observation_seconds:number(2,600)});form.checkValidity=()=>Object.values(form.elements).every(field=>!field.validity||field.disabled||field.validity.valid);
  form.querySelector = selector => selector === '[data-admin-savebar]' ? bar : null;
  bar.querySelector = selector => selector === '[data-admin-discard]' ? get('discard') : null;
  const window = { events: {}, addEventListener(name, fn) { this.events[name] = fn; },
    ChunnelAdminUI: { savebar: () => null, paintSavebar: (node, state) => { node.state = state; } } };
  const root = { dataset: { readonly: String(readOnly), csrf: 'test-csrf' }, querySelectorAll: () => [], querySelector(selector) {
    const name = /^\[data-update-([\w-]+)\]$/.exec(selector)?.[1]; return name ? get(name) : null;
  } };
  vm.runInNewContext(source, { window, document: {querySelector:()=>root,createElement:node,addEventListener(name,fn){documentEvents[name]=fn;}},AbortSignal,AbortController,HTMLInputElement:Input,HTMLSelectElement:class{},HTMLTextAreaElement:class{},
    sessionStorage: { getItem: () => ticket, setItem(_, value) { ticket = value; }, removeItem() { ticket = ''; } },
    setTimeout(fn,ms){const id=++clock;timers.set(id,{fn,ms});return id;},clearTimeout(id){timers.delete(id);},fetch(url,init){return new Promise((resolve,reject)=>calls.push({url,init,resolve,reject}));} });
  const respond = async (data, status = 200, call = calls.at(-1)) => {
    call.resolve({ ok: status < 400, status, json: async () => data }); await tick();
  };
  return {get,calls,respond,form,bar,window,documentEvents,root,timers,fields:form.elements,
    async ready(policy = defaults) { await respond({ ...checked(), policy }); await respond({ ...checked(), policy }); },
    input(name, value) { const target = form.elements[name]; target[name === 'auto_check' ? 'checked' : 'value'] = name==='auto_check'?value:String(value); form.events.input({ target }); },
    submit() { form.events.submit({ preventDefault() {} }); },
    poll(){const [id,timer]=[...timers].find(([,t])=>t.ms===1000||t.ms===5000)||[];assert.ok(timer,'poll scheduled');timers.delete(id);timer.fn();},
    confirm() { get('backup').checked = true; get('backup').events.change(); } };
}

test('official public source submission excludes all private repository fields and credentials', async () => {
  const f = fixture(); await f.ready();
  const sourceForm = f.get('source');
  sourceForm.elements.token.value = 'private-token';
  sourceForm.elements.clear_token.checked = false;
  sourceForm.elements.official.checked = true;
  sourceForm.elements.official.events.change();
  assert.equal(sourceForm.elements.token.disabled, true);
  assert.equal(sourceForm.elements.owner.disabled, true);
  sourceForm.events.submit({ preventDefault() {} });
  const call = f.calls.at(-1);
  assert.ok(call.url.endsWith('/source'));
  assert.deepEqual(JSON.parse(call.init.body), { official: true });
  await f.respond({ saved: true });
  await f.respond({ ...checked(), source: {...sourceDTO,official:true} });
  assert.equal(f.get('source-label').textContent, 'GitHub / Gitee 公开双源');
});

test('backup confirmation follows the exact release revision, including withdrawal and republication', async () => {
  const a = 'a'.repeat(64), b = 'b'.repeat(64), f = fixture();
  await f.respond(checked(a));
  f.confirm();
  await f.respond(checked(a));
  assert.equal(f.get('start').disabled, false);
  f.poll(); await f.respond(checked(b));
  assert.equal(f.get('backup').checked, false);
  assert.equal(f.get('start').disabled, true);
  f.get('start').events.click();
  assert.ok(f.calls.every(call => call.init.method === 'GET'));
  f.confirm();
  f.poll(); await f.respond(checked(undefined));
  assert.equal(f.get('backup').checked, false);
  assert.equal(f.get('install').hidden, true);
  f.poll(); await f.respond(checked(b));
  assert.equal(f.get('start').disabled, true);
  f.confirm(); f.get('start').events.click();
  assert.deepEqual(JSON.parse(f.calls.at(-1).init.body), { version: '1.1.0', revision: b, backup_confirmed: true });
});

test('the progress receipt shows the restored version after a post-commit rollback', async () => {
  const f = fixture('t'.repeat(64));
  const completed = state({ phase: 'completed', previous: '1.0.0', target: '1.1.0', protected_until: new Date(Date.now() + 60000).toISOString() });
  await f.respond({ ...checked(undefined), current: '1.1.0', state: completed });
  assert.ok(f.calls.at(-1).url.endsWith('/progress'));
  await f.respond(completed);
  assert.equal(f.get('current').textContent, '1.1.0');
  assert.equal(f.get('check').disabled, true);
  f.poll(); await f.respond(state({ phase: 'rolled_back', previous: '1.0.0', target: '1.1.0' }));
  assert.equal(f.get('current').textContent, '1.0.0');
  assert.equal(f.get('check').disabled, false);
});

test('a disabled schedule retains its interval and stale polling cannot undo a save', async () => {
  const f = fixture(); await f.ready();
  f.input('check_interval_minutes', 15); f.input('auto_check', false);
  assert.equal(f.fields.check_interval_minutes.disabled, true);
  f.poll(); const inFlight = f.calls.at(-1);
  f.submit(); const write = f.calls.at(-1);
  assert.ok(write.url.endsWith('/policy')); assert.equal(write.init.method, 'PUT');
  assert.equal(write.init.headers['X-CSRF-Token'], 'test-csrf');
  assert.deepEqual(JSON.parse(write.init.body), { revision: defaults.revision, auto_check: false, check_interval_minutes: 15, observation_seconds: 120 });
  const saved = { ...defaults, auto_check: false, check_interval_minutes: 15, customized: true, revision: '2'.repeat(64) };
  await f.respond(saved); await f.respond({ ...checked(), policy: saved }); await f.respond(checked(), 200, inFlight);
  assert.equal(f.get('policy-origin').textContent, '站点自定义');
  assert.equal(f.bar.state.dirty, false);
  f.input('auto_check', true);
  assert.equal(f.fields.check_interval_minutes.disabled, false); assert.equal(f.fields.check_interval_minutes.value, '15');
  f.input('check_interval_minutes', ''); f.input('auto_check', false);
  assert.equal(f.fields.check_interval_minutes.value, '15');
  f.input('observation_seconds', 28); f.get('discard').events.click();
  assert.equal(f.fields.observation_seconds.value, '120'); assert.equal(f.bar.state.dirty, false);
  f.get('policy-defaults').events.click();
  assert.deepEqual(JSON.parse(f.calls.at(-1).init.body), { revision: saved.revision, use_defaults: true });
  await f.respond(defaults); await f.respond(checked());
  assert.equal(f.get('policy-defaults').hidden, true); assert.equal(f.get('policy-origin').textContent, '部署默认');
});

test('polls preserve an edited policy, and a conflicting writer requires an explicit reload', async () => {
  const f = fixture(); await f.ready();
  f.input('observation_seconds', 55);
  f.poll(); await f.respond(checked()); assert.equal(f.fields.observation_seconds.value, '55');
  f.submit(); await f.respond({ error: { code: 'updates.policy_changed', message: '策略已变更' } }, 409);
  const latest = { ...defaults, customized: true, observation_seconds: 80, revision: '3'.repeat(64) };
  await f.respond({ ...checked(), policy: latest });
  assert.equal(f.fields.observation_seconds.value, '55'); assert.equal(f.get('policy-conflict').hidden, false);
  const writes = f.calls.length; f.submit(); f.get('policy-defaults').events.click(); assert.equal(f.calls.length, writes);
  f.get('policy-reload').events.click(); await f.respond({ ...checked(), policy: latest }); await f.respond({ ...checked(), policy: latest });
  assert.equal(f.fields.observation_seconds.value, '80'); assert.equal(f.get('policy-conflict').hidden, true);
  assert.equal(f.bar.state.dirty, false);
});

test('read-only access and active protection disable policy mutations', async () => {
  for (const readOnly of [true, false]) {
    const f = fixture('', readOnly); await f.ready({ ...defaults, launcher_applies_window: false });
    assert.equal(f.get('policy-launcher').hidden, false);
    if (!readOnly) {
      f.poll(); await f.respond({ ...checked(), state: state({ phase: 'completed', protected_until: new Date(Date.now() + 60000).toISOString() }) });
    }
    assert.equal(f.get('policy-fields').disabled, true);
    f.input('observation_seconds', 45); f.submit(); f.get('policy-defaults').events.click();
    assert.ok(f.calls.every(c => c.init.method === 'GET'));
  }
});

test('an expired session preserves the draft and disables writes', async () => {
  const f = fixture(); await f.ready();
  f.input('observation_seconds', 45);
  f.poll(); const inFlight = f.calls.at(-1);
  await f.respond({ error: { message: '请重新登录' } }, 401);
  assert.equal(f.get('policy-fields').disabled, true); assert.equal(f.get('login').hidden, false);
  f.submit(); f.get('check').events.click();
  assert.equal(f.calls.at(-1), inFlight); assert.equal(f.fields.observation_seconds.value, '45');
});

test('a valid progress receipt keeps polling after the admin session expires', async () => {
  const f = fixture('t'.repeat(64));
  await f.respond({ error: { message: '请重新登录' } }, 401);
  assert.ok(f.calls.at(-1).url.endsWith('/progress'));
  await f.respond(state({ phase: 'observing', target: '1.1.0' }));
  f.poll(); assert.ok(f.calls.at(-1).url.endsWith('/progress'));
  await f.respond(state({ phase: 'rolled_back', previous: '1.0.0' }));
  assert.equal(f.get('current').textContent, '1.0.0');
  assert.equal(f.get('login').hidden, false); assert.equal(f.get('policy-fields').disabled, true);
});

test('a policy write returning 401 keeps the session locked when its following refresh succeeds', async () => {
  const f = fixture(); await f.ready(); f.input('observation_seconds', 45); f.submit();
  await f.respond({ error: { message: '请重新登录' } }, 401);
  assert.ok(f.calls.at(-1).url.endsWith('/policy'));
  assert.equal(f.get('policy-fields').disabled, true); assert.equal(f.get('login').hidden, false);
  assert.equal(f.fields.observation_seconds.value, '45'); assert.equal(f.get('connection').textContent, '需要重新登录');
});

test('navigation warns about unsaved policy changes and a successful save clears the warning', async () => {
  const f = fixture(); await f.ready();
  const leaving = () => { let warned = false; f.window.events.beforeunload({ preventDefault() { warned = true; } }); return warned; };
  assert.equal(leaving(), false);
  f.input('observation_seconds', 33); assert.equal(leaving(), true);
  f.submit(); assert.equal(f.form['aria-busy'], 'true'); assert.equal(leaving(), true);
  const saved = { ...defaults, customized: true, observation_seconds: 33, revision: '4'.repeat(64) };
  await f.respond(saved); await f.respond({ ...checked(), policy: saved });
  assert.equal(f.form['aria-busy'], 'false'); assert.equal(leaving(), false);
});

test('a policy receipt preserves newer edits and uses the saved revision for the next submission',async()=>{
  const f=fixture();await f.ready();f.input('observation_seconds',33);f.submit();
  f.input('observation_seconds',47);
  const saved={...defaults,customized:true,observation_seconds:33,revision:'2'.repeat(64)};
  await f.respond(saved);await f.respond({...checked(),policy:saved});
  assert.equal(f.fields.observation_seconds.value,'47');assert.equal(f.bar.state.dirty,true);
  f.submit();assert.equal(JSON.parse(f.calls.at(-1).init.body).revision,saved.revision);
});

test('saving a source never erases a newly typed token or repository',async()=>{
  const f=fixture();await f.ready();const form=f.get('source');
  form.elements.token.value='submitted-token';form.events.submit({preventDefault(){}});
  form.elements.token.value='later-token';form.elements.repo.value='later-repo';
  await f.respond({saved:true});await f.respond(checked());
  assert.equal(form.elements.token.value,'later-token');assert.equal(form.elements.repo.value,'later-repo');
});

test('a malformed source acknowledgment preserves submitted credentials for explicit recovery',async()=>{
  const f=fixture();await f.ready();const form=f.get('source');form.elements.token.value='submitted-token';form.events.submit({preventDefault(){}});
  await f.respond({saved:false});assert.equal(form.elements.token.value,'submitted-token');assert.match(f.get('feedback').textContent,/响应不完整/);
});

test('an unconfirmed installation remains locked after an old idle snapshot and never repeats POST',async()=>{
  const f=fixture(),revision='a'.repeat(64);await f.respond(checked(revision));await f.respond(checked(revision));f.confirm();f.get('start').events.click();
  f.calls.at(-1).reject(new Error('lost receipt'));await tick();f.poll();await f.respond(checked(revision));f.confirm();f.get('start').events.click();
  assert.equal(f.get('start').disabled,true);assert.equal(f.calls.filter(c=>c.init.method==='POST').length,1);
  f.poll();await f.respond({...checked(revision),state:state({id:'accepted-job',phase:'downloading',target:'1.1.0'})});assert.equal(f.get('phase').textContent,'正在下载更新包');
});

test('replacement and session changes retire outstanding work without replaying writes',async()=>{
  for(const event of ['chunnel:admin-before-replace','chunnel:session']){
    const f=fixture();await f.ready();f.input('observation_seconds',33);f.submit();const write=f.calls.at(-1);
    f.documentEvents[event]({detail:{regions:[f.root]}});assert.equal(write.init.signal.aborted,true);
    await f.respond({...defaults,observation_seconds:33,revision:'2'.repeat(64)},200,write);
    f.submit();assert.equal(f.calls.filter(c=>c.init.method==='PUT').length,1);assert.equal(f.fields.observation_seconds.value,'33');
  }
});

test('BFCache retires old polls and restores one fresh request',async()=>{
  const f=fixture();await f.ready();f.poll();const old=f.calls.at(-1);
  f.window.events.pagehide();assert.equal(old.init.signal.aborted,true);assert.equal([...f.timers.values()].filter(t=>[1000,5000].includes(t.ms)).length,0);
  f.window.events.pageshow({persisted:true});const fresh=f.calls.at(-1);assert.notEqual(old,fresh);
  await f.respond(checked('b'.repeat(64)),200,fresh);await f.respond(checked(),200,old);assert.equal(f.get('target').textContent,'1.1.0');
});
