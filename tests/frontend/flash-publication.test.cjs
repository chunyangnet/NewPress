const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs');
const {chromium}=require('playwright'),{compile}=require('./helpers/typescript.cjs'),f=require('./fixtures/publishing.cjs');
const source=compile('frontend/core/flash.ts');
const wait=async(page,part)=>page.waitForFunction(part=>document.querySelector('[data-flash-status]')?.textContent.includes(part),part);
async function fixture(variant,options={}){
  const browser=await chromium.launch({headless:true}),context=await browser.newContext(),page=await context.newPage(),writes=[],errors=[];
  let reply,session=f.session(),state=f.engagement(),read,documents=0;
  page.on('pageerror',error=>errors.push(error.message));page.setDefaultTimeout(7000);
  await page.route('https://example.test/**',async route=>{
    const request=route.request(),url=new URL(request.url());
    if(url.pathname==='/api/v1/social/session')return route.fulfill({json:session});
    if(request.method()==='POST'){
      const body=request.postDataJSON();writes.push({body,encoded:request.postData(),key:request.headers()['idempotency-key']});
      return reply?reply(route,body):route.fulfill({json:f.receipt(body)});
    }
    if(url.pathname==='/api/v1/social/engagement')return read?read(route):route.fulfill({json:state});
    documents++;return route.fulfill({contentType:'text/html; charset=utf-8',body:options.public?f.publicHTML():f.html(options)});
  });
  const mount=()=>page.addScriptTag({content:variant==='typescript'?source:fs.readFileSync('internal/coreui/assets/js/chunnel-flash.js','utf8')});
  await page.goto(`https://example.test/${options.public?'flash/':options.admin===false?'flash/submit/':'admin/flash/'}?edit=4`);
  if(options.storageUnavailable)await page.evaluate(()=>{Storage.prototype.getItem=()=>{throw Error('unavailable')};Storage.prototype.setItem=()=>{throw Error('quota')};});
  await mount();
  return {page,writes,errors,mount,context,get documents(){return documents},set reply(value){reply=value},set session(value){session=value},set state(value){state=value},set read(value){read=value},
    save:()=>page.locator('[type=submit][value=save]').click(),retry:()=>page.locator('[data-flash-retry]').click(),
    close:async()=>{await browser.close();assert.deepEqual(errors,[]);}};
}
for(const variant of ['typescript','built']){
  test(`${variant}: flash unknown save preserves original content, time, key and newer input`,async()=>{
    const t=await fixture(variant);try{
      t.reply=route=>route.abort();await t.save();await wait(t.page,'网络');await t.page.locator('[name=body]').fill('保存结果未知之后的新正文');
      t.reply=(route,body)=>route.fulfill({json:f.receipt(body,{id:99})});await t.retry();await wait(t.page,'回执未确认');assert.equal(t.documents,1);
      t.reply=(route,body)=>route.fulfill({json:f.receipt(body,{replayed:true})});await t.retry();await wait(t.page,'修改仍保留');
      assert.equal(t.writes.length,3);for(const write of t.writes.slice(1)){assert.equal(write.encoded,t.writes[0].encoded);assert.equal(write.key,t.writes[0].key);}
      assert.equal(t.writes[0].body.published_at,'2026-11-01T02:30');assert.equal(t.writes[0].body.published_timezone,'America/New_York');
      assert.equal(await t.page.locator('[name=body]').inputValue(),'保存结果未知之后的新正文');assert.equal(await t.page.locator('[data-flash-editor]').getAttribute('data-version'),'3');
      assert.equal(await t.page.locator('[data-disabled]').isDisabled(),true);assert.equal(await t.page.locator('[data-flash-retry]').isVisible(),false);
      await t.save();await t.page.waitForFunction(()=>!document.querySelector('[data-flash-editor]')?.dataset.flashReady);
      assert.equal(t.writes.at(-1).body.version,3);assert.equal(t.writes.at(-1).body.body,'保存结果未知之后的新正文');assert.notEqual(t.writes.at(-1).key,t.writes[0].key);
    }finally{await t.close();}
  });
  test(`${variant}: flash reload restores a pending operation without writing automatically`,async()=>{
    const t=await fixture(variant);try{
      t.reply=route=>route.abort();await t.save();await wait(t.page,'网络');await t.page.reload();await t.mount();
      assert.equal(t.writes.length,1);assert.equal(await t.page.locator('[data-flash-retry]').isVisible(),true);
      t.reply=(route,body)=>route.fulfill({json:f.receipt(body,{replayed:true})});await t.retry();await t.page.waitForFunction(()=>!document.querySelector('[data-flash-editor]')?.dataset.flashReady);
      assert.equal(t.writes[0].key,t.writes[1].key);assert.equal(t.writes[0].encoded,t.writes[1].encoded);assert.equal(await t.page.evaluate(()=>sessionStorage.length),0);
    }finally{await t.close();}
  });
  test(`${variant}: flash timezone rejection requires explicit confirmation and keeps the local minute`,async()=>{
    const t=await fixture(variant);try{
      t.reply=route=>route.fulfill({status:422,json:{error:{code:'flash.timezone_changed',message:'请核对当前时区',details:{published_timezone:'Asia/Shanghai'}}}});
      await t.save();await t.page.locator('[data-flash-timezone-review]').waitFor();
      assert.equal(await t.page.evaluate(()=>sessionStorage.length),0);assert.equal(await t.page.locator('[value=save]').isDisabled(),true);
      assert.equal(await t.page.locator('[data-flash-timezone-label]').textContent(),'Asia/Shanghai');assert.equal(await t.page.locator('[name=published_timezone]').inputValue(),'America/New_York');
      await t.page.locator('[data-flash-timezone-review] button').click();assert.equal(t.writes.length,1);assert.equal(await t.page.locator('[name=published_at]').inputValue(),'2026-11-01T02:30');
      t.reply=undefined;await t.save();await t.page.waitForFunction(()=>!document.querySelector('[data-flash-editor]')?.dataset.flashReady);
      assert.equal(t.writes[1].body.published_timezone,'Asia/Shanghai');assert.notEqual(t.writes[1].key,t.writes[0].key);
    }finally{await t.close();}
  });
  test(`${variant}: a later timezone refusal cannot release an earlier unknown publication`,async()=>{
    const t=await fixture(variant);try{
      t.reply=route=>route.abort();await t.save();await wait(t.page,'网络');
      t.reply=route=>route.fulfill({status:422,json:{error:{code:'flash.timezone_changed',message:'时区已变化',details:{published_timezone:'Asia/Shanghai'}}}});
      await t.retry();await wait(t.page,'时区已变化');assert.equal(await t.page.locator('[data-flash-timezone-review]').isVisible(),false);assert.equal(await t.page.locator('[data-flash-retry]').isVisible(),true);
      await t.page.locator('[name=body]').fill('后续新草稿');t.reply=(route,body)=>route.fulfill({json:f.receipt(body,{replayed:true})});await t.retry();await wait(t.page,'修改仍保留');
      assert.equal(t.writes[0].key,t.writes[2].key);assert.equal(t.writes[0].encoded,t.writes[2].encoded);
    }finally{await t.close();}
  });
  test(`${variant}: flash withdrawal preserves unsaved content and exposes the next valid save`,async()=>{
    const t=await fixture(variant,{admin:false,state:'pending'});try{
      await t.page.locator('[name=body]').fill('撤回前尚未保存的正文');await t.page.locator('[value=withdraw]').click();await wait(t.page,'尚未保存');
      assert.equal(t.writes[0].body.action,'withdraw');assert.equal(await t.page.locator('[name=body]').inputValue(),'撤回前尚未保存的正文');
      assert.equal(await t.page.locator('[value=withdraw]').isVisible(),false);assert.equal(await t.page.locator('[value=save]').isVisible(),true);
      await t.save();await t.page.waitForFunction(()=>!document.querySelector('[data-flash-editor]')?.dataset.flashReady);assert.equal(t.writes[1].body.version,3);
    }finally{await t.close();}
  });
  test(`${variant}: flash edits during save survive and old-account receipts cannot clear recovery`,async()=>{
    const t=await fixture(variant);try{
      let release;t.reply=(route,body)=>new Promise(resolve=>{release=async()=>{await route.fulfill({json:f.receipt(body)}).catch(()=>{});resolve();};});
      await t.save();while(!release)await new Promise(resolve=>setTimeout(resolve,10));await t.page.locator('[name=title]').fill('保存期间的新标题');await release();await wait(t.page,'修改仍保留');
      assert.equal(await t.page.locator('[name=title]').inputValue(),'保存期间的新标题');
      release=undefined;await t.save();while(!release)await new Promise(resolve=>setTimeout(resolve,10));
      await t.page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,configured:true,id:8,csrf_token:'other'}})));
      await release();await wait(t.page,'登录账号已变化');assert.equal(await t.page.locator('[name=title]').inputValue(),'');assert.equal(await t.page.evaluate(()=>sessionStorage.length),1);
    }finally{await t.close();}
  });
  test(`${variant}: state-only withdrawal does not discard an unsent review note`,async()=>{
    const t=await fixture(variant,{state:'pending'});try{
      await t.page.locator('[name=review_note]').fill('尚未提交的审核备注');await t.page.locator('[value=withdraw]').click();await wait(t.page,'尚未保存');
      assert.equal(t.writes[0].body.review_note,'');assert.equal(await t.page.locator('[name=review_note]').inputValue(),'尚未提交的审核备注');assert.equal(t.documents,1);
    }finally{await t.close();}
  });
  test(`${variant}: flash storage failures retain in-memory retry`,async()=>{
    const t=await fixture(variant,{storageUnavailable:true});try{
      t.reply=route=>route.abort();await t.save();await wait(t.page,'网络');t.reply=undefined;await t.retry();await t.page.waitForFunction(()=>!document.querySelector('[data-flash-editor]')?.dataset.flashReady);
      assert.equal(t.writes[0].key,t.writes[1].key);assert.equal(t.writes[0].encoded,t.writes[1].encoded);
    }finally{await t.close();}
  });
  test(`${variant}: flash duplicate controls share desired state and confirmed fresh count`,async()=>{
    const t=await fixture(variant,{public:true});try{
      await t.page.waitForFunction(()=>document.querySelector('[data-flash-engagement=like] span').textContent==='5');
      t.reply=route=>route.fulfill({json:{ok:true,target_id:99,active:true,collection:''}});await t.page.locator('[data-flash-engagement=like]').first().click();await wait(t.page,'回执未确认');
      assert.deepEqual(await t.page.locator('[data-flash-engagement=like]').evaluateAll(nodes=>nodes.map(node=>node.getAttribute('aria-pressed'))),['false','false']);
      t.reply=(route,body)=>route.fulfill({json:{ok:true,target_id:body.target_id,active:body.active,collection:body.collection}});t.state=f.engagement({like:[{target_id:4,count:9,active:true}]});
      await t.page.locator('[data-flash-engagement=like]').last().click();await t.page.waitForFunction(()=>document.querySelector('[data-flash-engagement=like] span').textContent==='9');
      assert.deepEqual(t.writes[0].body,t.writes[1].body);assert.deepEqual(await t.page.locator('[data-flash-engagement=like]').evaluateAll(nodes=>nodes.map(node=>node.getAttribute('aria-pressed'))),['true','true']);
      await t.page.locator('.flash-more').first().click();assert.equal(await t.page.locator('.flash-more').first().getAttribute('aria-expanded'),'true');
    }finally{await t.close();}
  });
  test(`${variant}: flash confirmed interaction remains success if the count read fails`,async()=>{
    const t=await fixture(variant,{public:true});try{
      await t.page.waitForFunction(()=>document.querySelector('[data-flash-engagement=like] span').textContent==='5');
      t.reply=(route,body)=>{t.read=route=>route.fulfill({status:503,json:{error:{message:'read unavailable'}}});return route.fulfill({json:{ok:true,target_id:body.target_id,active:body.active,collection:body.collection}});};
      await t.page.locator('[data-flash-engagement=like]').first().click();await wait(t.page,'互动已保存');assert.equal(await t.page.locator('[data-flash-engagement=like]').first().getAttribute('aria-pressed'),'true');
    }finally{await t.close();}
  });
  test(`${variant}: flash missing targets never become zero or an unverified mutation`,async()=>{
    const t=await fixture(variant,{public:true});try{
      await t.page.waitForFunction(()=>document.querySelector('[data-flash-engagement=like] span').textContent==='5');t.state={like:[],bookmark:[],follow:[]};
      await t.page.evaluate(()=>window.dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true})));await t.page.evaluate(()=>window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true})));
      await t.page.waitForFunction(()=>!document.querySelector('[data-flash-engagement=like]').disabled);
      await t.page.locator('[data-flash-engagement=like]').first().click();await wait(t.page,'尚未确认');assert.equal(t.writes.length,0);
    }finally{await t.close();}
  });
}
