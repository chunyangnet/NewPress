// Run against TestResourcesMigrationBrowserFixture. Writes are intercepted;
// the Go fixture independently compares stores after an explicit stop.
import assert from 'node:assert/strict';
import {readFile,writeFile,mkdir} from 'node:fs/promises';
import {resolve} from 'node:path';
import {chromium,firefox,webkit} from 'playwright';
import {build} from 'esbuild';
import {createRequire} from 'node:module';
const require=createRequire(import.meta.url), {order}=require('../tests/frontend/fixtures/resource-public.cjs');
const directory=resolve(process.argv[2]), variant=process.argv[3]||'generated';
const fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const script=variant==='typescript'?(await build({entryPoints:['frontend/core/resources.ts'],bundle:true,write:false,platform:'browser',format:'iife',target:['chrome100','firefox110','safari16']})).outputFiles[0].text:null;
const engines={chromium,firefox,webkit}, evidence=[], captures=[];
const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(fn) {const end=Date.now()+12000;while(!await fn()){if(Date.now()>end)throw Error('Browser condition timed out');await delay(20);}}
await mkdir(directory,{recursive:true});
for(const name of process.argv[4]?[process.argv[4]]:Object.keys(engines)) {
  const browser=await engines[name].launch({headless:true}),context=await browser.newContext({viewport:{width:1280,height:960},reducedMotion:'reduce'}),page=await context.newPage();
  page.setDefaultTimeout(12000);
  const errors=[],writes=[];page.on('pageerror',e=>errors.push(e.message));
  let transform=value=>value, handleWrite, history, historyHold, lastState, held, reads=0, failReads=0;
  const endpoint=`/api/v1/article-resources/${fixture.post_id}/bundle`;
  const receipt=patch=>order({user_id:String(fixture.reader_id),resource_url:fixture.paths.article+'#resource-bundle',...patch});
  const file={url:'/core-assets/images/favicon.svg',code:'np26',password:'test-password',replayed:false,remaining:1};
  const check=check=>{evidence.push({browser:name,check});console.log(`${name}: ${check}`);};
  const root=page.locator('[data-npr-resource="bundle"]'),status=root.locator('.npr-status');
  const select=async id=>{await root.locator(`[data-version="${id}"]`).click();};
  const refresh=async()=>{const before=reads;await root.locator('[data-refresh]').click();await until(()=>reads>before);await delay(60);};
  const visit=async()=>{await page.goto(fixture.base+fixture.paths.article,{waitUntil:'domcontentloaded'});await root.locator('[data-version]').first().waitFor();};
  const reset=()=>{transform=v=>v;handleWrite=undefined;held=undefined;history=undefined;failReads=0;};
  const buy=async kind=>{
    await select('complete');
    if(kind==='points')await root.locator('.npr-alternatives').evaluate(n=>n.open=true);
    await root.locator(`[data-buy="${kind}"]`).first().click();
    await page.locator('dialog.npr-checkout[open]').waitFor();
  };
  const confirm=async()=>{const dialog=page.locator('dialog.npr-checkout[open]');await dialog.locator('[data-agree]').check();await dialog.locator('[data-confirm]').click();};
  const unlock=(value,id='complete')=>{
    const v=value.variants.find(v=>v.id===id);v.allowed=true;v.body='<p>已解锁的验收内容</p>';v.groups[0].passed=true;v.groups[0].conditions.forEach(c=>{c.passed=true;c.purchasable=false;});return value;
  };
  await context.route('**/*',async route=>{
    const request=route.request(),url=new URL(request.url());
    if(url.origin!==fixture.base)return route.abort('blockedbyclient');
    if(script&&url.pathname.endsWith('/newpress-resources.js'))return route.fulfill({contentType:'text/javascript',body:script});
    if(url.pathname===endpoint){
      if(request.method()==='GET'){
        reads++;if(failReads>0){failReads--;return route.fulfill({status:503,json:{error:{code:'resource.unavailable',message:'资源读取暂不可用'}}});}
        const response=await route.fetch(),value=await response.json();
        if(response.status()!==200)return route.fulfill({response});
        lastState=structuredClone(value);return route.fulfill({response,json:transform(value)});
      }
      const body=JSON.parse(request.postData());writes.push({path:url.pathname,body});
      if(handleWrite)return handleWrite(route,body);
      return route.fulfill({json:body.action==='points'?{paid:true,replayed:false}:body.action==='download'?file:body.action==='submit'?{quiz_passed:false,form_complete:true,attempts_remaining:2}:receipt()});
    }
    if(url.pathname==='/api/v1/social/downloads'&&request.method()==='GET'){
      if(historyHold){historyHold=false;held=()=>route.fulfill({json:{items:[{title:'旧账户私密记录',url:fixture.paths.article,created_at:new Date().toISOString()}]}}).catch(()=>{});return;}
      if(history)return route.fulfill({json:{items:history}});
    }
    if(url.pathname.match(/^\/api\/v1\/social\/billing\/orders\/[^/]+\/sync$/)){
      const body=JSON.parse(request.postData());writes.push({path:url.pathname,body});
      if(handleWrite)return handleWrite(route,body);
      return route.fulfill({json:receipt({status:'completed'})});
    }
    if(!['GET','HEAD'].includes(request.method())){writes.push({path:url.pathname,unexpected:true});return route.fulfill({status:403,json:{error:{message:'Unexpected mutation blocked'}}});}
    return route.fallback();
  });
  try {
    await context.request.get(fixture.base+fixture.reader_login);
    await visit();assert.equal(await page.locator('script[src*="newpress-resources.js"]').count(),1);assert.equal(await root.locator('[data-version]').count(),3);
    captures.push({browser:name,resource:lastState});check('real Go resource projection and three versions render with a single resource script');
    await select('complete');assert.equal(await root.locator('[data-buy="payment"]').count(),2);
    await buy('payment');await page.locator('[data-confirm]').click();await page.locator('.npr-payment-result').filter({hasText:'勾选确认'}).waitFor();assert.equal(writes.length,0);await confirm();
    await page.locator('.npr-payment-result').filter({hasText:'9.90 CNY'}).waitFor();const payment=writes.at(-1).body;assert.equal(payment.variant,'complete');assert.equal(payment.revision,lastState.revision);assert.equal(payment.payment_version,lastState.payment_version);check('payment consent sends the original resource revision, variant and policy version');
    handleWrite=(route)=>route.fulfill({json:receipt({id:'np_aaaaaaaaaaaaaaaaaaaaaaaaaaaa',status:'completed'})});
    await page.locator('[data-sync]').click();await page.locator('[data-order-status]').filter({hasText:'未确认'}).waitFor();assert.equal(await page.locator('dialog[open]').count(),1);check('payment synchronization rejects a different order id');
    handleWrite=undefined;await page.locator('[data-sync]').click();await status.filter({hasText:'购买凭据已保留'}).waitFor();check('completed payment does not claim unlock when other conditions remain unmet');

    reset();await visit();await buy('payment');handleWrite=route=>route.fulfill({status:409,json:{error:{code:'payment.pending',message:'核对原订单',details:{order:receipt({amount:'19.90',currency:'USD',expires_at:new Date(Date.now()-1000).toISOString()})}}}});await confirm();await page.locator('.npr-payment-result').filter({hasText:'19.90 USD'}).waitFor();assert.equal(await page.locator('[data-confirm]').isDisabled(),true);check('pending expired payment resumes its original amount and currency');
    await page.locator('.npr-close').click();reset();await buy('payment');handleWrite=route=>route.fulfill({status:409,json:{error:{code:'resource.changed',message:'资源已更新'}}});await confirm();await page.locator('.npr-payment-result').filter({hasText:'重新核对'}).waitFor();assert.equal(await page.locator('[data-confirm]').isDisabled(),true);check('changed resource or terms require new consent');

    reset();await visit();await buy('payment');handleWrite=route=>{held=()=>route.fulfill({json:receipt()}).catch(()=>{});};await confirm();await until(()=>!!held);await context.request.get(fixture.base+fixture.login);await page.evaluate(async()=>{const detail=await (await fetch('/api/v1/social/session')).json();document.dispatchEvent(new CustomEvent('chunnel:session',{detail}));});await until(async()=>await page.locator('dialog[open]').count()===0);await held();await root.locator('[data-version]').first().waitFor();assert.doesNotMatch(await root.innerText(),/np_0123456789/);check('account switch closes checkout and discards the old payment response');
    await context.request.get(fixture.base+fixture.reader_login);reset();await visit();await buy('points');handleWrite=route=>{transform=value=>unlock(value);return route.fulfill({json:{paid:true,replayed:false}});};await confirm();await status.filter({hasText:'对应版本已解锁'}).waitFor();check('validated points receipt refreshes actual variant eligibility');

    reset();await visit();await select('public');await root.locator('[data-download="file"]').click();await status.filter({hasText:'下载地址已准备好'}).waitFor();const first=writes.at(-1).body;await refresh();await root.locator('[data-download="file"]').click();await status.filter({hasText:'下载地址已准备好'}).waitFor();assert.equal(writes.at(-1).body.request,first.request);check('download retries retain the original request id across refresh');
    await page.evaluate(()=>{dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true}));dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));});await root.locator('[data-download="file"]').waitFor();await root.locator('[data-download="file"]').click();await status.filter({hasText:'下载地址已准备好'}).waitFor();assert.equal(writes.at(-1).body.request,first.request);check('BFCache lifecycle revalidates permission and preserves download identity');
    handleWrite=route=>route.fulfill({json:{...file,url:'javascript:alert(1)'}});await root.locator('[data-download="file"]').click();await status.filter({hasText:'地址未确认'}).waitFor();assert.equal(await root.locator('a[href^="javascript:"]').count(),0);check('unsafe download URLs are never inserted');
    await refresh();handleWrite=route=>route.abort('failed');await root.locator('[data-download="file"]').click();await until(async()=>await root.locator('[data-download="file"]').isEnabled());const lost=writes.at(-1).body;handleWrite=undefined;await root.locator('[data-download="file"]').click();await status.filter({hasText:'下载地址已准备好'}).waitFor();assert.deepEqual(writes.at(-1).body,lost);check('lost download response retries the same operation');
    handleWrite=route=>{held=()=>route.fulfill({json:{...file,password:'OLD-ACCOUNT-PASSWORD'}}).catch(()=>{});};held=undefined;await root.locator('[data-download="file"]').click();await until(()=>!!held);await context.request.get(fixture.base+fixture.login);await page.evaluate(async()=>{const detail=await (await fetch('/api/v1/social/session')).json();document.dispatchEvent(new CustomEvent('chunnel:session',{detail}));});await held();await root.locator('[data-version]').first().waitFor();assert.doesNotMatch(await root.innerText(),/OLD-ACCOUNT-PASSWORD/);check('download secrets from a previous account cannot enter the new view');

    await context.request.get(fixture.base+fixture.reader_login);reset();await visit();await select('quiz');await root.locator('[data-field="name"]').fill('原草稿');await root.locator('[data-field="agree"]').check();await root.locator('[data-answer="q"]').selectOption('正确');await select('complete');await select('quiz');assert.equal(await root.locator('[data-field="name"]').inputValue(),'原草稿');assert.equal(await root.locator('[data-field="agree"]').isChecked(),true);check('variant switches retain text, answers and checkbox drafts');
    handleWrite=(route,body)=>{held=()=>route.fulfill({json:{quiz_passed:false,form_complete:true,attempts_remaining:2}}).catch(()=>{});};held=undefined;await root.locator('.npr-form button[type="submit"]').click();await until(()=>!!held);await root.locator('[data-field="name"]').fill('提交期间的新输入');transform=value=>{const v=value.variants.find(v=>v.id==='quiz');v.form_complete=true;v.attempts_remaining=2;return value;};await held();await status.filter({hasText:'新输入已保留'}).waitFor();assert.equal(await root.locator('[data-field="name"]').inputValue(),'提交期间的新输入');assert.equal(writes.at(-1).body.fields.name,'原草稿');check('new input entered while submission is pending survives refreshed state');

    reset();await visit();await select('quiz');await root.locator('[data-field="name"]').fill('一次尝试');await root.locator('[data-field="agree"]').check();await root.locator('[data-answer="q"]').selectOption('错误');handleWrite=route=>route.abort('failed');await root.locator('.npr-form button[type="submit"]').click();await status.filter({hasText:'先核验当前状态'}).waitFor();const attempts=writes.filter(w=>w.body?.action==='submit').length;transform=value=>{value.variants.find(v=>v.id==='quiz').attempts_remaining=2;return value;};await root.locator('.npr-form button[type="submit"]').click();await status.filter({hasText:'再次提交会进行一次新的核验'}).waitFor();assert.equal(writes.filter(w=>w.body?.action==='submit').length,attempts);check('unknown quiz result performs a read before allowing a deliberate new attempt');
    handleWrite=route=>route.fulfill({json:{quiz_passed:false,form_complete:true,attempts_remaining:1}});await root.locator('.npr-form button[type="submit"]').click();await status.filter({hasText:'尚未达到通过要求'}).waitFor();assert.equal(writes.filter(w=>w.body?.action==='submit').length,attempts+1);check('a new quiz attempt requires another explicit submit after reconciliation');

    reset();failReads=100;await page.goto(fixture.base+fixture.paths.article,{waitUntil:'domcontentloaded'});await status.filter({hasText:'读取暂不可用'}).waitFor();failReads=0;await root.locator('[data-refresh]').click();await root.locator('[data-version]').first().waitFor();check('initial-load failure can be retried without an existing resource state');
    transform=value=>({...value,post:fixture.post_id+1});await refresh();await status.filter({hasText:'与当前页面不一致'}).waitFor();assert.equal(await root.locator('[data-download]').count(),0);check('wrong-target snapshots clear actionable resource content');

    reset();await context.request.get(fixture.base+fixture.login);const beforePreview=reads;await page.goto(fixture.base+fixture.paths.preview,{waitUntil:'domcontentloaded'});await root.locator('[data-preview-view="unlocked"]').first().click();await root.locator('.npr-preview-delivery').waitFor();assert.match(await root.innerText(),/SECRET_PASSWORD/);assert.equal(reads,beforePreview);assert.equal(await root.locator('[data-buy], [data-download]').count(),0);assert.equal(await root.getAttribute('data-npr-preview-state'),null);check('actual private Go preview uses only its snapshot and never accesses public purchase endpoints');

    await context.request.get(fixture.base+fixture.reader_login);history=[{title:'历史资源',url:fixture.paths.article+'#resource-bundle',created_at:new Date().toISOString()}];await page.goto(fixture.base+fixture.paths.history,{waitUntil:'domcontentloaded'});await page.locator('.npr-library-item').filter({hasText:'历史资源'}).waitFor();check('download library validates and renders local article links');
    history=undefined;historyHold=true;held=undefined;await page.reload({waitUntil:'domcontentloaded'});await until(()=>!!held);await context.request.get(fixture.base+fixture.login);history=[];await page.evaluate(async()=>{const detail=await (await fetch('/api/v1/social/session')).json();document.dispatchEvent(new CustomEvent('chunnel:session',{detail}));});await held();await page.locator('[data-npr-library]').filter({hasText:'还没有下载记录'}).waitFor();assert.doesNotMatch(await page.locator('[data-npr-library]').innerText(),/旧账户私密记录/);check('library discards a late response after account change');

    reset();await context.clearCookies();await visit();await select('complete');assert.equal(await root.locator('[data-buy]').count(),0);await root.locator('.npr-login, .npr-pricebar a').first().waitFor();check('anonymous resource access preserves login guidance and prevents purchases');
    await context.request.get(fixture.base+fixture.reader_login);await visit();await select('quiz');
    for(const width of [1280,390])for(const colorScheme of ['light','dark']){
      await page.setViewportSize({width,height:960});await page.emulateMedia({colorScheme});await page.waitForFunction(theme=>{const card=document.querySelector('.npr-card');if(!card||document.documentElement.dataset.theme!==theme)return false;const rgb=getComputedStyle(card).backgroundColor.match(/\d+/g)?.slice(0,3).map(Number);return rgb&&(theme==='dark'?Math.max(...rgb)<150:Math.min(...rgb)>150);},colorScheme);await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));await page.evaluate(()=>{document.activeElement?.blur();scrollTo(0,0);});
      await page.waitForFunction(()=>document.documentElement.scrollWidth<=innerWidth+2,{},{timeout:4000});await page.screenshot({path:resolve(directory,`${variant}-${name}-${width}-${colorScheme}.png`),fullPage:true});
    }
    check('desktop and narrow light/dark pages have no horizontal overflow');
    assert.deepEqual(errors,[]);assert.equal(writes.filter(w=>w.unexpected).length,0);check('no browser exceptions or unexpected business mutations');
    await writeFile(resolve(directory,`${variant}-${name}-writes.json`),JSON.stringify(writes,null,2));
  } catch(error) {
    await page.screenshot({path:resolve(directory,`${variant}-${name}-failure.png`),fullPage:true}).catch(()=>{});
    await writeFile(resolve(directory,`${variant}-${name}-failure.json`),JSON.stringify({error:String(error),url:page.url(),errors,writes,reads,layout:await page.evaluate(()=>({width:innerWidth,scroll:document.documentElement.scrollWidth,overflow:[...document.querySelectorAll('body *')].filter(n=>n.getBoundingClientRect().right>innerWidth+2).slice(0,15).map(n=>({tag:n.tagName,class:n.className,right:n.getBoundingClientRect().right,width:n.getBoundingClientRect().width}))})),html:await root.count()?await root.innerHTML():await page.content()},null,2));
    throw error;
  } finally {await browser.close();}
}
await writeFile(resolve(directory,`${variant}-browser-acceptance.json`),JSON.stringify({checks:evidence.length,evidence,captures},null,2));
console.log(`${variant}: ${evidence.length} resource checks passed`);
