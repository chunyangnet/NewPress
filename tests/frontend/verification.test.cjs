const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const {compile}=require('./helpers/typescript.cjs');
const typescript=compile('frontend/shared/verification.ts');
const tick = () => new Promise(resolve => setImmediate(resolve));

function fixture(provider = 'local', options = {}) {
  const requests = [], sdks = [], events = {}, loaded = [], readyCallbacks = [], windowEvents={};
  let detached;
  class Element {
    constructor(tag) { this.tagName = tag; this.children = []; this.dataset = {}; this.listeners = {}; this.isConnected = true; this.value = ''; this.clientWidth = 320; }
    append(...items) { for (const item of items) { item.parent = this; this.children.push(item); if (item.tagName === 'script') { loaded.push(item.src); queueMicrotask(() => { item.onload?.(); const callback = new URL(item.src).searchParams.get('onload'); if (callback) { const ready = () => window[callback]?.(); if (options.waitSDK) readyCallbacks.push(ready); else ready(); } }); } } }
    before(item) { this.parent?.append(item); }
    get lastChild() { return this.children.at(-1); }
    setAttribute(name, value) { this[name] = value; }
    remove() { this.isConnected = false; if (this.parent) this.parent.children = this.parent.children.filter(child => child !== this); }
    replaceChildren(...items) { this.children = []; this.append(...items); }
    addEventListener(name, fn) { this.listeners[name] = fn; }
    focus() {}
    scrollIntoView() {}
    querySelector() { return null; }
    matches(selector) { return !!options.native && selector === '.admin-login-form'; }
    requestSubmit() { this.submits = (this.submits || 0) + 1; }
  }
  const owner = new Element('form'), head = new Element('head');
  const document = { head, body: {dataset:{}}, documentElement: {dataset:{theme:'light'},lang:'zh-CN'}, createElement: tag => new Element(tag), addEventListener: (name,fn) => { events[name] = fn; } };
  const callbackSDK = name => ({ render(host, params) { sdks.push({name,params}); queueMicrotask(() => params.callback('vendor-token')); return 'widget'; }, remove() {}, reset() {} });
  const window = { addEventListener(name,callback) {windowEvents[name]=callback;}, matchMedia: () => ({matches:false}), turnstile: callbackSDK('turnstile'), hcaptcha: callbackSDK('hcaptcha'),
    grecaptcha: { ...callbackSDK('recaptcha_v2'), ready: fn => options.stallReady ? readyCallbacks.push(fn) : fn(), execute: async (key, params) => { if (options.stallToken) await new Promise(done => readyCallbacks.push(done)); sdks.push({name:'recaptcha_v3',params:{key,...params}}); return 'vendor-token'; } },
    initGeetest4(params, callback) {
      sdks.push({name:'geetest',params}); const widget = { onReady(fn) { fn(); return this; }, onSuccess(fn) { this.success = fn; return this; }, onError() { return this; }, showCaptcha() { this.success(); }, getValidate() { return {lot_number:'lot',captcha_output:'output',pass_token:'pass',gen_time:'123'}; }, destroy() {} }; callback(widget);
    },
    TencentCaptcha: class { constructor(key,callback,params) { sdks.push({name:'tencent',params:{key,...params}}); this.callback = callback; } show() { this.callback({ret:0,ticket:'vendor-token',randstr:'random'}); } destroy() {} },
  };
  let configResolve;
  const fetch = async (url, init) => {
    const body = init.body && JSON.parse(init.body); requests.push({url,body,signal:init.signal});
    if (url.includes('/config?')) {
      if (options.waitConfig) await new Promise(resolve => { configResolve = resolve; });
      return {ok:true,json:async()=>({scene:new URL(url,'https://verification.test').searchParams.get('scene'),revision:'fixture-revision',enabled:!options.disabled,provider,site_key:'public-site-key',theme:'auto',language:'zh-CN',recaptcha_net:true,...options.config})};
    }
    if (url.endsWith('/challenge')) return {ok:true,json:async()=>({id:'challenge',image:'data:image/png;base64,test',expires_in:120})};
    return {ok:!options.rejected,json:async()=>options.rejected ? {error:{code:'verification.invalid',message:'验证未通过'}} : options.badReceipt ? {} : body.preview ? {ok:true,preview:true} : {token:'one-use-receipt',expires_in:120}};
  };
  vm.runInNewContext(options.built ? fs.readFileSync('themes/chunnel/assets/js/chunnel-verification.js','utf8') : typescript, {window,document,fetch,AbortController,DOMException,HTMLFormElement:Element,HTMLButtonElement:Element,HTMLInputElement:Element,MutationObserver:class{constructor(callback){detached=callback;}observe(){}disconnect(){}},setTimeout: options.setTimeout || setTimeout,clearTimeout,URLSearchParams,queueMicrotask});
  const all = (root = owner) => [root,...root.children.flatMap(child => all(child))];
  return {owner,window,requests,sdks,loaded,events,windowEvents,all,detach:()=>{owner.isConnected=false;detached();},resolveConfig:()=>configResolve(),resolveSDK:()=>readyCallbacks.splice(0).forEach(ready=>ready()),click:text=>all().find(el=>el.tagName==='button'&&el.textContent===text).listeners.click(), answer:value=>{all().find(el=>el.tagName==='input'&&el.type==='text').value=value;}};
}

for (const built of [false,true]) {
  test(`verification disabled returns immediately without a challenge (${built ? 'built' : 'source'})`, async () => {
    const f=fixture('local',{disabled:true,built}); assert.equal(await f.window.ChunnelVerification.getToken(f.owner,'comment'),''); assert.equal(f.requests.length,1); assert.equal(f.owner.children.length,0);
  });
  test(`local verification rejects bad receipts and cancels without submitting (${built ? 'built' : 'source'})`, async () => {
    const f=fixture('local',{badReceipt:true,built}); const result=f.window.ChunnelVerification.getToken(f.owner,'comment'); await tick(); f.answer('12345'); f.click('确认验证'); await tick();
    assert.match(f.all().find(el=>el.className==='verification-status').textContent,/回执无效/);
    const rejected=assert.rejects(result,{code:'verification.cancelled'});f.click('取消');await rejected;assert.equal(f.owner.children.length,0);
  });
}

for (const provider of ['turnstile','hcaptcha','recaptcha_v2','recaptcha_v3','geetest','tencent']) {
  test(`${provider} exchanges SDK proof before returning the application receipt`, async () => {
    const f=fixture(provider);const result=f.window.ChunnelVerification.getToken(f.owner,'comment');await tick();
    if(provider==='geetest')f.click('点击开始极验验证');if(provider==='tencent')f.click('点击开始腾讯验证');
    assert.equal(await result,'one-use-receipt');const request=f.requests.find(x=>x.url.endsWith('/verify'));assert.equal(request.body.scene,'comment');
    if(provider==='geetest')assert.deepEqual(request.body.proof,{lot_number:'lot',captcha_output:'output',pass_token:'pass',gen_time:'123'});
    else {assert.equal(request.body.proof.token,'vendor-token');if(provider==='tencent')assert.equal(request.body.proof.randstr,'random');}
    if(provider==='turnstile'||provider==='recaptcha_v3')assert.equal(f.sdks[0].params.action,'comment');
    if(provider.startsWith('recaptcha_'))assert.ok(f.loaded[0].startsWith('https://www.recaptcha.net/'));
    assert.equal(f.owner.children.length,0);
  });
}

test('cancelled configuration load cannot resurrect a widget', async () => {
  const f=fixture('local',{waitConfig:true});const result=f.window.ChunnelVerification.getToken(f.owner,'register');await tick();const rejected=assert.rejects(result,{code:'verification.cancelled'});f.window.ChunnelVerification.cancel(f.owner);await rejected;f.resolveConfig();await tick();assert.equal(f.requests.length,1);assert.equal(f.owner.children.length,0);
});

test('repeated native submit while verification is loading submits only once', async () => {
  const f=fixture('local',{disabled:true,waitConfig:true,native:true});const event={target:f.owner,preventDefault(){},stopImmediatePropagation(){}};
  const first=f.events.submit(event);const second=f.events.submit(event);await tick();assert.equal(f.requests.length,1);f.resolveConfig();await Promise.all([first,second]);assert.equal(f.owner.submits,1);
});

for (const provider of ['hcaptcha','turnstile']) {
  for (const built of [false,true]) {
    test(`${provider} waits for SDK readiness after the script load event (${built ? 'built' : 'source'})`, async () => {
      const f=fixture(provider,{waitSDK:true,built});const result=f.window.ChunnelVerification.getToken(f.owner,'comment');await tick();
      assert.equal(f.loaded.length,1);assert.equal(f.sdks.length,0);assert.equal(f.requests.filter(x=>x.url.endsWith('/verify')).length,0);
      f.resolveSDK();assert.equal(await result,'one-use-receipt');assert.equal(f.sdks.length,1);
    });
  }
}

for (const built of [false, true]) {
  test(`verification scene changes cancel the previous task and copy preview options (${built})`,async()=>{
    const f=fixture('local',{built});const first=f.window.ChunnelVerification.getToken(f.owner,'comment');await tick();
    const rejected=assert.rejects(first,{code:'verification.cancelled'}),options={preview:'local',csrf:'original'};
    const second=f.window.ChunnelVerification.getToken(f.owner,'preview',options);options.preview='turnstile';options.csrf='changed';await rejected;await tick();
    f.answer('12345');f.click('确认验证');assert.equal(await second,'preview-passed');
    const verify=f.requests.find(request=>request.url.endsWith('/verify'));assert.equal(verify.body.scene,'preview');assert.equal(verify.body.preview,'local');
  });
  for(const lifecycle of ['detach','pagehide','pageshow'])test(`verification cancels on ${lifecycle} without reviving late requests (${built})`,async()=>{
    const f=fixture('local',{built,waitConfig:true});const pending=f.window.ChunnelVerification.getToken(f.owner,'login');await tick();
    const rejected=assert.rejects(pending,{code:'verification.cancelled'});if(lifecycle==='detach')f.detach();else f.windowEvents[lifecycle]({persisted:true});await rejected;
    assert.equal(f.requests[0].signal.aborted,true);f.resolveConfig();await tick();assert.equal(f.requests.length,1);assert.equal(f.owner.children.length,0);
  });
  test(`native resubmit errors clear the guard before a new verification attempt (${built})`,async()=>{
    const f=fixture('local',{built,disabled:true,native:true});f.owner.requestSubmit=()=>{throw new Error('changed submitter');};
    const event={target:f.owner,preventDefault(){},stopImmediatePropagation(){}};await f.events.submit(event);
    assert.equal(f.owner.dataset.verificationSubmitting,undefined);assert.equal(f.owner.dataset.verificationPending,undefined);
    f.owner.requestSubmit=()=>f.owner.submits=1;await f.events.submit(event);assert.equal(f.owner.submits,1);assert.equal(f.requests.length,2);
  });
  for (const phase of ['stallReady', 'stallToken']) {
    test(`recaptcha ${phase} times out and ignores late completion (${built})`, async () => {
      const timers = [];
      const f = fixture('recaptcha_v3', {built, [phase]: true, setTimeout: (fn, ms) => { const timer = setTimeout(fn, ms); if(ms === 15000) timers.push({fn,timer}); return timer; }});
      const result = f.window.ChunnelVerification.getToken(f.owner, 'comment');
      await tick();
      const latest = timers.at(-1); clearTimeout(latest.timer); latest.fn();
      assert.match(f.all().find(el => el.className === 'verification-status').textContent, /响应超时/);
      f.resolveSDK(); await tick();
      assert.equal(f.requests.filter(x => x.url.endsWith('/verify')).length, 0);
      const rejected = assert.rejects(result, {code:'verification.cancelled'}); f.click('取消'); await rejected;
    });
  }
}
