const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const crypto = require('node:crypto').webcrypto;
const tick = () => new Promise(resolve => setImmediate(resolve));
const orderId = 'np_0123456789abcdef0123456789ab';

class Element {
  constructor(tag='div') { this.tag=tag; this.dataset={}; this.listeners=new Map(); this.selectors=new Map(); this.children=[]; this.disabled=false; this.checked=false; this.open=false; this.textContent=''; this.value=''; this._html=''; }
  get innerHTML() { return this._html; }
  set innerHTML(value) { this._html=value; this.selectors.clear(); }
  querySelector(selector) {
    if(this.selectors.has(selector)) return this.selectors.get(selector);
    if(selector==='[data-sync]'&&!this._html.includes('data-sync')) return null;
    const element=new Element(selector.includes('confirm')||selector.includes('sync')?'button':'div');
    if(selector==='[data-confirm]') element.disabled=/<button[^>]*data-confirm[^>]*disabled/.test(this._html);
    if(selector==='[data-channel]') element.value='gateway';
    this.selectors.set(selector,element);return element;
  }
  querySelectorAll() { return []; }
  addEventListener(name,fn) { const handlers=this.listeners.get(name)||[]; handlers.push(fn);this.listeners.set(name,handlers); }
  async emit(name,data={}) { for(const handler of this.listeners.get(name)||[]) await handler({target:this,currentTarget:this,preventDefault(){},...data}); }
  hasAttribute(name) { return Object.hasOwn(this.dataset,name.replace('data-','').replace(/-([a-z])/g,(_,c)=>c.toUpperCase())); }
  closest(selector) { return selector==='button'&&this.tag==='button'?this:null; }
  append(element) { this.children.push(element); }
  showModal() { this.open=true; }
  close() { this.open=false;for(const handler of this.listeners.get('close')||[]) handler({target:this}); }
  remove() { this.removed=true; }
}

function resourceState(kind='payment') {
  const condition={id:'price',kind,label:kind==='points'?'使用 1.00 积分':'支付 9.90 CNY',amount:kind==='points'?100:990,currency:kind==='points'?'main':'CNY',passed:false,purchasable:true};
  return {post:1,id:'bundle',title:'完整资源',author:1,revision:'revision-1',csrf:'csrf-1',user_id:'2',logged_in:true,payment_version:'2',remaining:2,selection:'all',channels:[{key:'gateway',name:'测试支付',currency:'CNY'}],refund_policy:'原退款规则',disclaimer:'原支付说明',variants:[{id:'full',name:'完整版本',kind:'download',available:true,allowed:false,blocked:'',groups:[{name:'购买',passed:false,conditions:[condition]}],downloads:[],questions:[],fields:[]}]};
}
function order(patch={}) { return {id:orderId,user_id:'2',kind:'resource',amount:'9.90',currency:'CNY',status:'pending',expires_at:new Date(Date.now()+1800000).toISOString(),...patch}; }
function button(data) { const node=new Element('button');node.dataset=data;return node; }
function harness(source,initial=resourceState(),options={}) {
  let state=initial,respond=async(path,data)=>path.endsWith('/sync')?order({status:'completed'}):data?(data.action==='points'?{paid:true,replayed:false}:order()):state;
  const area=new Element(),root=new Element(),body=new Element(),events=new Element(),windowEvents=new Element(),calls=[];
  root.dataset={nprPost:'1',nprResource:'bundle'};root.selectors.set('.npr-content',area);
  if(Object.hasOwn(options,'preview')) {
    root.dataset.nprPreview=options.preview;
    area.innerHTML='静态预览';
    root.dataset.nprPreviewState=Buffer.from(options.raw ?? JSON.stringify(initial),'utf8').toString('base64url');
  }
  const document={body,querySelectorAll:selector=>selector==='[data-npr-resource]'?[root]:[],querySelector:selector=>selector==='.npr-checkout[open]'?body.children.find(d=>d.open&&!d.removed)||null:null,createElement:tag=>new Element(tag),addEventListener:(...args)=>events.addEventListener(...args)};
  const context={document,window:{addEventListener:(...args)=>windowEvents.addEventListener(...args)},location:{pathname:'/posts/resource',hash:'',origin:'https://example.test'},crypto,Date,URL,console,TextDecoder,atob,fetch:async(path,options={})=>{
    const data=options.body?JSON.parse(options.body):undefined;calls.push({path,data,headers:options.headers});
    const result=await respond(path,data);
    return {ok:!result?.httpStatus,json:async()=>JSON.parse(JSON.stringify(result?.body||result))};
  }};
  return {area,body,calls,events,windowEvents,state(value){state=value;},respond(fn){respond=fn;},dialog(){return body.children.at(-1);},async start(){vm.runInNewContext(fs.readFileSync(source,'utf8'),context);await tick();await tick();},async buy(){await area.emit('click',{target:button({buy:state.variants[0].groups[0].conditions[0].kind,condition:'price',group:'0'})});},async refresh(){await area.emit('click',{target:button({refresh:''})});},async confirm(){const dialog=this.dialog();dialog.querySelector('[data-agree]').checked=true;await dialog.querySelector('[data-confirm]').emit('click');},async session(){await events.emit('chunnel:session');await tick();await tick();}};
}

function previewState() {
  const state={...resourceState(),preview:true};
  state.variants[0].groups[0].conditions[0].amount=1990;
  state.variants[0].body='<p>本次草稿的私密正文</p>';
  state.variants[0].downloads=[{id:'file',name:'草稿文件',provider:'direct',url:'https://example.test/draft.zip?a=<b>',code:'code<&',password:'draft-password'}];
  state.variants.push({...state.variants[0],id:'alternate',name:'备选版本',body:'<p>备选私密正文</p>',kind:'image',media_url:'/media/preview.png',downloads:[]});
  return state;
}

for(const source of ['internal/coreui/assets/js/newpress-resources.source.js','internal/coreui/assets/js/newpress-resources.js']) {
  test(`${source}: a single payment condition has one action and no redundant disclosure`,async()=>{
    const h=harness(source);await h.start();
    assert.equal((h.area.innerHTML.match(/data-buy="payment"/g)||[]).length,1);
    assert.doesNotMatch(h.area.innerHTML,/npr-alternatives/);
    await h.buy();assert.equal(h.dialog().open,true);
  });
  test(`${source}: compact payment retains alternate conditions and the original purchase target`,async()=>{
    const state=resourceState();state.variants[0].groups.push({name:'会员方式',passed:false,conditions:[{id:'member',kind:'membership',label:'年度会员',passed:false}]});
    const h=harness(source,state);await h.start();
    assert.match(h.area.innerHTML,/npr-alternatives/);assert.match(h.area.innerHTML,/年度会员/);
    await h.buy();assert.match(h.dialog().innerHTML,/支付 9.90 CNY/);
  });
  test(`${source}: preview uses its snapshot and switches both views without live requests`,async()=>{
    const h=harness(source,previewState(),{preview:'snapshot'});await h.start();
    assert.match(h.area.innerHTML,/19\.90/);assert.doesNotMatch(h.area.innerHTML,/本次草稿的私密正文|data-buy=|href="\/billing|npr-login/);
    await h.area.emit('click',{target:button({previewView:'unlocked'})});
    assert.match(h.area.innerHTML,/解锁后效果预览/);assert.match(h.area.innerHTML,/本次草稿的私密正文/);assert.match(h.area.innerHTML,/draft-password/);assert.match(h.area.innerHTML,/a=&lt;b&gt;/);
    assert.doesNotMatch(h.area.innerHTML,/data-download=|href="https:\/\/example.test\/draft/);
    await h.area.emit('click',{target:button({previewView:'locked'})});assert.doesNotMatch(h.area.innerHTML,/draft-password|本次草稿的私密正文/);assert.equal(h.calls.length,0);
  });
  test(`${source}: preview can inspect later and closed variants without granting access`,async()=>{
    const state=previewState();state.selection='first';state.variants.forEach(v=>{v.available=false;});state.ends_at=new Date(Date.now()-60000).toISOString();
    const h=harness(source,state,{preview:'snapshot'});await h.start();assert.match(h.area.innerHTML,/仅展示首个符合条件/);assert.match(h.area.innerHTML,/下方仅预览内容效果/);
    await h.area.emit('click',{target:button({previewView:'unlocked'})});await h.area.emit('click',{target:button({version:'alternate'})});
    assert.match(h.area.innerHTML,/备选私密正文/);assert.match(h.area.innerHTML,/src="\/media\/preview.png"/);assert.doesNotMatch(h.area.innerHTML,/本次草稿的私密正文/);assert.equal(h.calls.length,0);
  });
  test(`${source}: preview blocks forged purchase, engagement, download and form actions`,async()=>{
    const state=previewState();state.variants[0].questions=[{id:'q',title:'预览题目',options:[]}];state.variants[0].fields=[{id:'name',label:'称呼',type:'text'}];
    const h=harness(source,state,{preview:'snapshot'});await h.start();assert.match(h.area.innerHTML,/fieldset class="npr-questions" disabled/);assert.match(h.area.innerHTML,/type="submit" disabled/);
    await h.buy();for(const data of [{buy:'points',group:'0',condition:'price'},{download:'file'},{engage:'like'},{engage:'follow'},{tip:''},{refresh:''}])await h.area.emit('click',{target:button(data)});
    await h.area.emit('submit',{target:{matches:()=>true,querySelectorAll(){throw Error('preview submitted fields');}}});
    await h.session();await h.windowEvents.emit('focus');assert.equal(h.calls.length,0);assert.equal(h.body.children.length,0);
  });
  test(`${source}: invalid preview payload never falls back to a published resource`,async()=>{
    for(const raw of ['', '{bad}', JSON.stringify({...previewState(),post:42}), JSON.stringify({...previewState(),id:'other'}), JSON.stringify({...previewState(),preview:false}), JSON.stringify({...previewState(),variants:[]})]) {
      const h=harness(source,previewState(),{preview:'snapshot',raw});await h.start();await h.refresh();assert.match(h.area.innerHTML,/预览配置无法读取/);assert.equal(h.calls.length,0);
    }
    for(const mode of ['appearance','','unknown']) {const h=harness(source,previewState(),{preview:mode});await h.start();assert.equal(h.area.innerHTML,'静态预览');assert.equal(h.calls.length,0);}
  });
  test(`${source}: suppressed and prerequisite-blocked variants do not offer payment`,async()=>{
    for(const blocked of ['selection','prerequisite','limit']) {
      const state=resourceState();state.variants[0].groups[0].conditions[0].purchasable=false;state.variants[0].groups[0].conditions[0].purchase_reason=blocked;if(blocked==='selection')state.variants[0].blocked='selection';
      const h=harness(source,state);await h.start();assert.doesNotMatch(h.area.innerHTML,/data-buy=/);await h.buy();assert.equal(h.body.children.length,0);assert.equal(h.calls.some(c=>c.data),false);
    }
  });
  test(`${source}: pending purchase retains its original currency and recovery link`,async()=>{
    const state=resourceState(),condition=state.variants[0].groups[0].conditions[0];Object.assign(condition,{amount:1990,currency:'USD',order_id:orderId,order_status:'pending',order_amount:'9.90',order_currency:'CNY'});
    const h=harness(source,state);await h.start();assert.match(h.area.innerHTML,new RegExp('/billing/\\?order='+orderId));assert.match(h.area.innerHTML,/9\.90 CNY/);assert.doesNotMatch(h.area.innerHTML,/data-buy=/);assert.equal(h.calls.some(c=>c.data),false);
  });
  test(`${source}: consent pins variant, revision, csrf and policy version`,async()=>{
    const state=resourceState(),h=harness(source,state);state.variants.push({...state.variants[0],id:'preview',name:'另一个版本'});await h.start();await h.buy();
    await h.area.emit('click',{target:button({version:'preview'})});
    h.state({...state,revision:'revision-2',csrf:'csrf-2',payment_version:'3'});await h.refresh();await h.confirm();
    const write=h.calls.find(c=>c.data?.action==='pay');assert.equal(write.data.variant,'full');assert.equal(write.data.revision,'revision-1');assert.equal(write.headers['X-CSRF-Token'],'csrf-1');assert.equal(write.data.payment_version,'2');assert.equal(write.data.amount,undefined);assert.equal(write.data.price,undefined);
  });
  test(`${source}: deadline appears in the article and consent dialog`,async()=>{
    const state=resourceState();state.ends_at=new Date(Date.now()+60000).toISOString();state.timezone='Asia/Shanghai';const h=harness(source,state);await h.start();assert.match(h.area.innerHTML,/访问截止/);await h.buy();assert.match(h.dialog().innerHTML,/资源访问截止/);
  });
  test(`${source}: a completed condition does not imply the version is unlocked`,async()=>{
    const h=harness(source);h.respond(async(path,data)=>data?order({status:'completed'}):resourceState());await h.start();await h.buy();await h.confirm();assert.match(h.area.querySelector('.npr-status').textContent,/购买凭据已保留/);assert.doesNotMatch(h.area.querySelector('.npr-status').textContent,/已解锁/);
  });
  test(`${source}: paid delivery review never reports successful unlock`,async()=>{
    const h=harness(source);h.respond(async(path,data)=>data?order({status:'review',error_code:'resource_delivery_requires_reconciliation'}):resourceState());await h.start();await h.buy();await h.confirm();const dialog=h.dialog();assert.match(dialog.querySelector('.npr-payment-result').innerHTML,/管理员核对/);assert.doesNotMatch(dialog.querySelector('.npr-payment-result').innerHTML,/已解锁/);assert.equal(dialog.querySelector('[data-confirm]').disabled,true);
  });
  test(`${source}: policy/revision conflict requires a new consent dialog`,async()=>{
    const h=harness(source);h.respond(async(path,data)=>data?{httpStatus:409,body:{error:{code:'resource.changed',message:'配置已更新'}}}:resourceState());await h.start();await h.buy();await h.confirm();assert.match(h.dialog().querySelector('.npr-payment-result').textContent,/重新核对/);assert.equal(h.dialog().querySelector('[data-confirm]').disabled,true);await h.confirm();assert.equal(h.calls.filter(c=>c.data?.action==='pay').length,1);
  });
  test(`${source}: unknown expired order resumes its original id without a second creation`,async()=>{
    const h=harness(source);h.respond(async(path,data)=>data?{httpStatus:409,body:{error:{code:'payment.pending',message:'核对原订单',details:{order:order({expires_at:new Date(Date.now()-1000).toISOString()})}}}}:resourceState());await h.start();await h.buy();await h.confirm();assert.match(h.dialog().querySelector('.npr-payment-result').innerHTML,new RegExp(orderId));await h.confirm();assert.equal(h.calls.filter(c=>c.data?.action==='pay').length,1);
  });
  test(`${source}: account switch discards late payment responses`,async()=>{
    const h=harness(source);let finish;h.respond(async(path,data)=>data?new Promise(resolve=>{finish=()=>resolve(order());}):resourceState());await h.start();await h.buy();const pending=h.confirm();await tick();const dialog=h.dialog();h.state({...resourceState(),user_id:'3'});h.respond(async()=>({...resourceState(),user_id:'3'}));await h.session();finish();await pending;assert.equal(dialog.removed,true);assert.doesNotMatch(h.area.innerHTML,new RegExp(orderId));assert.equal(h.calls.filter(c=>c.data?.action==='pay').length,1);
  });
}
