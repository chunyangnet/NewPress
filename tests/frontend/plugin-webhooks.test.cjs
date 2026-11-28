const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {compile}=require('./helpers/typescript.cjs');
const source=process.env.NEWPRESS_PLUGIN_WEBHOOKS_SOURCE?fs.readFileSync(process.env.NEWPRESS_PLUGIN_WEBHOOKS_SOURCE,'utf8'):compile('frontend/core/plugin-webhooks.ts');
const tick = () => new Promise(resolve => setImmediate(resolve));
const digest = n => n.toString(16).padStart(64, '0');
const at = '2026-11-25T10:00:00Z';
const item = (n = 1, extra = {}) => ({id:digest(n),job_id:digest(100),plugin:'webhook-test',slot:'notify',target:'https://example.com',fingerprint:digest(200),version:3,state:'uncertain',created_at:at,updated_at:at,result_at:at,outcome:{code:'transport_unconfirmed'},...extra});
const page = (items = [item()], total = items.length, offset = 0) => ({items,total,limit:20,offset});

function fixture({hidden = false, available = true} = {}) {
  const make = tag => ({tag,children:[],events:{},attrs:new Map(),dataset:{},value:'',disabled:false,hidden:false,text:'',
    set textContent(x) { this.text=String(x); this.children=[]; }, get textContent() { return this.text+this.children.map(x=>x.textContent).join(''); },
    set innerHTML(x) { throw new Error('unsafe html'); }, append(...nodes) { this.children.push(...nodes); }, replaceChildren(...nodes) { this.text='';this.children=nodes; },
    addEventListener(name,fn) { this.events[name]=fn; }, setAttribute(name,value) {this.attrs.set(name,String(value));}, reportValidity(){return true;}, focus(){this.focused=true;}
  });
  const nodes = new Map(), calls = [], timers = new Map(), created = [], channels = [];
  for (const name of ['filters','list','status','summary','page','prev','next','results','access']) nodes.set(name,make(name));
  const form = nodes.get('filters'); form.elements={plugin:make('input'),job:make('input'),state:make('select'),limit:make('select')};Object.defineProperty(form.elements,'namedItem',{value:name=>form.elements[name]});form.elements.limit.value='20'; form.querySelectorAll=()=>Object.values(form.elements);
  const root={dataset:{available:String(available),userId:'7'},querySelector:s=>nodes.get(s.replace('[data-webhook-','').replace(']',''))};
  const document={hidden,events:{},querySelector:()=>root,createElement:tag=>{const node=make(tag);created.push(node);return node;},addEventListener(n,fn){this.events[n]=fn;}};
  const window={events:{},addEventListener(n,fn){this.events[n]=fn;}};
  let tid=0;
  vm.runInNewContext(source,{document,window,URLSearchParams,AbortController,
    setTimeout(fn,ms){const id=++tid;timers.set(id,{fn,ms});return id;},clearTimeout(id){timers.delete(id);},
    BroadcastChannel:class{constructor(){channels.push(this);}addEventListener(n,fn){this.message=fn;}close(){this.closed=true;}},
    fetch:(url,options)=>new Promise((resolve,reject)=>calls.push({url,options,resolve,reject}))});
  const get=n=>nodes.get(n);
  const reply=(body,{index=calls.length-1,status=200,owner='7'}={})=>calls[index].resolve({ok:status>=200&&status<300,status,headers:{get:()=>owner},text:async()=>JSON.stringify(body)});
  const confirm=async()=>{assert.match(calls.at(-1).url,/webhook-session$/);reply({user_id:'7',available:true,enabled:false,csrf_token:'csrf'});await tick();};
  const show=async data=>{await confirm();assert.match(calls.at(-1).url,/plugin-webhooks\?/);reply(data);await tick();await confirm();};
  const fire=(name,event='click')=>get(name).events[event]({preventDefault(){}});
  const timer=ms=>{const entry=[...timers].find(([,v])=>v.ms===ms);assert.ok(entry);timers.delete(entry[0]);entry[1].fn();};
  const review=()=>created.findLast(n=>n.tag==='form');
  return {get,root,calls,reply,confirm,show,fire,timer,form,document,window,channels,created,review,timers};
}

test('binds identity before and after reads, uses bounded safe projection',async()=>{
  const f=fixture();await f.confirm();f.reply(page());await tick();assert.equal(f.get('list').children.length,0);await f.confirm();
  assert.match(f.get('list').textContent,/待人工核对/);assert.match(f.get('list').textContent,/https:\/\/example.com/);
  for(const {options}of f.calls){assert.equal(options.cache,'no-store');assert.equal(options.redirect,'error');assert.equal(options.credentials,'same-origin');assert.equal(options.headers['X-NewPress-Review-User'],'7');}
});

test('maintenance preserves uncertain human-review flow and cancelled records have no action',async()=>{
  const f=fixture(); await f.show(page([item(1,{outcome:{code:'maintenance'}}),item(2,{version:2,state:'cancelled',result_at:undefined,outcome:{code:'maintenance'}})]));
  assert.match(f.get('list').textContent,/离线维护已停止旧操作/);
  assert.equal(f.created.filter(n=>n.tag==='form').length,1);
  assert.equal(f.calls.filter(c=>c.options.method==='POST').length,0);
});

test('manual review binds version and string identity, never enqueues or resends',async()=>{
  const f=fixture();await f.show(page());const form=f.review();
  form.children[0].children[0].value='not_received';form.children[1].children[0].value='核对接收方记录';
  form.events.submit({preventDefault(){}});await f.confirm();
  const call=f.calls.at(-1);assert.match(call.url,/\/review$/);assert.equal(call.options.method,'POST');assert.equal(call.options.headers['X-CSRF-Token'],'csrf');
  assert.deepEqual(JSON.parse(call.options.body),{expected_user_id:'7',version:3,decision:'not_received',reason:'核对接收方记录'});
  f.reply({recorded:true});await tick();await f.show(page([item(1,{version:4,state:'reviewed_not_received',review:{decision:'not_received',reason:'核对接收方记录',actor_id:'9223372036854775807',at}})]));
  assert.match(f.get('list').textContent,/9223372036854775807/);assert.match(f.get('list').textContent,/人工确认未收到/);
  assert.equal(f.calls.filter(c=>c.options.method==='POST').length,1);
});

test('lost mutation response clears stale action and asks to query saved result',async()=>{
  const f=fixture();await f.show(page());const form=f.review();form.children[0].children[0].value='received';form.children[1].children[0].value='remote checked';
  form.events.submit({preventDefault(){}});await f.confirm();f.calls.at(-1).reject(new TypeError('lost'));await tick();
  assert.equal(f.get('list').children.length,0);assert.match(f.get('status').textContent,/保存结果待确认/);assert.equal(f.calls.filter(c=>c.options.method==='POST').length,1);
});

for(const action of ['blur','pagehide','hidden','offline','local-session','broadcast'])test(action+' clears results and prevents delayed actions',async()=>{
  const f=fixture();await f.show(page());
  if(action==='hidden'){f.document.hidden=true;f.document.events.visibilitychange();}
  else if(action==='local-session')f.document.events['chunnel:session']();
  else if(action==='broadcast')f.channels[0].message({data:{type:'session'}});
  else f.window.events[action]();
  assert.equal(f.get('list').children.length,0);assert.equal(f.timers.size,0);
  if(!['local-session','broadcast'].includes(action)){f.document.hidden=false;f.fire('filters','submit');await f.show(page([]));assert.match(f.get('status').textContent,/没有记录/);}
});

test('revocation during confirmation or heartbeat never retains visible results',async()=>{
  const f=fixture();await f.confirm();f.reply(page());await tick();f.reply({}, {status:403});await tick();assert.equal(f.get('list').children.length,0);assert.equal(f.form.elements.plugin.disabled,true);
  const g=fixture();await g.show(page());g.timer(15000);g.reply({}, {status:403});await tick();assert.equal(g.get('list').children.length,0);
});

test('late replies cannot override changed query or hidden page',async()=>{
  const f=fixture();await f.confirm();const pending=f.calls.length-1;f.form.elements.plugin.value='different';f.form.events.input();f.reply(page(),{index:pending});await tick();assert.equal(f.get('list').children.length,0);assert.match(f.get('status').textContent,/筛选已变化/);
});

test('pagination submits filters and offset and rejects duplicate/inconsistent records',async()=>{
  const f=fixture();await f.show(page(Array.from({length:20},(_,n)=>item(n+1)),21));assert.equal(f.get('next').disabled,false);f.fire('next');await f.confirm();assert.match(f.calls.at(-1).url,/offset=20/);f.reply(page([item(21)],21,20));await tick();await f.confirm();assert.equal(f.get('prev').disabled,false);
  for(const data of [page([item(),item()]),page([item()],2),page([item(1,{version:1})]),page([item(1,{target:'https://example.com/private?token=secret'})]),page([item(1,{state:'unknown'})]),page([item(1,{result_at:undefined})])]){
    const g=fixture();await g.confirm();g.reply(data);await tick();assert.equal(g.get('list').children.length,0);assert.equal(g.get('status').dataset.tone,'error');
  }
});

test('429 and corruption have distinct recovery messages; file mode performs no reads',async()=>{
  const f=fixture();f.reply({}, {status:429});await tick();assert.match(f.get('status').textContent,/过于频繁/);
  const g=fixture();await g.confirm();g.reply({error:{code:'plugins.webhooks_corrupt'}},{status:503});await tick();assert.match(g.get('status').textContent,/持久记录不一致/);assert.doesNotMatch(g.get('status').textContent,/没有记录/);
  assert.equal(fixture({available:false}).calls.length,0);assert.equal(fixture({hidden:true}).calls.length,0);
});

test('identity response mismatch locks the original page',async()=>{
  const f=fixture();f.reply({user_id:'8',available:true,enabled:true,csrf_token:'other'});await tick();assert.equal(f.form.elements.plugin.disabled,true);assert.equal(f.get('access').hidden,false);
});


test('a malformed review receipt never becomes a confirmed decision',async()=>{
 const f=fixture();await f.show(page());const form=f.review();form.children[0].children[0].value='received';form.children[1].children[0].value='checked';
 form.events.submit({preventDefault(){}});await f.confirm();const count=f.calls.length;f.reply({ok:true});await tick();
 assert.equal(f.calls.length,count);assert.equal(f.get('list').children.length,0);assert.match(f.get('status').textContent,/保存结果待确认/);
});
test('a confirmed decision survives a failed subsequent read',async()=>{
 const f=fixture();await f.show(page());const form=f.review();form.children[0].children[0].value='received';form.children[1].children[0].value='checked';
 form.events.submit({preventDefault(){}});await f.confirm();f.reply({recorded:true});await tick();f.reply({}, {status:503});await tick();
 assert.match(f.get('status').textContent,/核对结论已保存/);assert.match(f.get('status').textContent,/列表暂未刷新/);assert.equal(f.calls.filter(x=>x.options.method==='POST').length,1);
});
test('admin region replacement aborts a webhook read and closes its session channel',async()=>{
 const f=fixture();await f.confirm();const pending=f.calls.at(-1);f.document.events['chunnel:admin-before-replace']({detail:{regions:[f.root]}});
 assert.equal(pending.options.signal.aborted,true);assert.equal(f.channels[0].closed,true);f.reply(page());await tick();
 assert.equal(f.get('list').children.length,0);const count=f.calls.length;f.fire('filters','submit');assert.equal(f.calls.length,count);
});
