// Real Go pages; writes are intercepted and isolated stores are compared on stop.
import assert from 'node:assert/strict';
import {readFile,writeFile,mkdir} from 'node:fs/promises';
import {resolve} from 'node:path';
import {chromium,firefox,webkit} from 'playwright';
import {build} from 'esbuild';

const directory=resolve(process.argv[2]),variant=process.argv[3]||'generated';
const fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const engines={chromium,firefox,webkit},evidence=[];
const scripts=new Map();
if(variant==='typescript')for(const name of ['recognition','recognition-admin'])scripts.set(`/core-assets/js/newpress-${name}.js`,(await build({entryPoints:[`frontend/core/${name}.ts`],bundle:true,write:false,format:'iife',platform:'browser',target:['chrome100','firefox110','safari16']})).outputFiles[0].text);
const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(fn){const end=Date.now()+12000;while(!await fn()){if(Date.now()>end)throw Error('Browser condition timed out');await delay(20);}}
await mkdir(directory,{recursive:true});
for(const name of process.argv[4]?[process.argv[4]]:Object.keys(engines)){
  const browser=await engines[name].launch({headless:true}),context=await browser.newContext({viewport:{width:1280,height:960},reducedMotion:'reduce'}),page=await context.newPage();
  page.setDefaultTimeout(12000);
  const errors=[],writes=[],observed=new Map(),overrides=new Map(),servedTS=new Set();let writeHandler,readTransform=(url,value)=>value,readHold,release,failRead,sessionHold,stage='initial load';
  page.on('pageerror',e=>{errors.push(e.message);console.error(`${name} after ${stage}: ${e.stack||e.message}`);});
  const check=text=>{stage=text;evidence.push({browser:name,check:text});console.log(`${name}: ${text}`);};
  const tipPath=`/api/v1/social/tips/${fixture.post_id}`,socialPath='/api/v1/social/recognition',adminPath='/api/v1/admin/recognition',accountPath='/api/v1/admin/recognition/account';
  const tipStatus=page.locator('[data-tip-status]'),tipForm=page.locator('[data-tip-form]'),identityStatus=page.locator('[data-identity-status]'),adminStatus=page.locator('[data-recognition-status]'),memberStatus=page.locator('[data-recognition-account-status]');
  const byText=(locator,text)=>locator.filter({hasText:text}).waitFor();
  const login=async(role='reader')=>{await context.request.get(fixture.base+(role==='reader'?fixture.reader_login:fixture.login));};
  // Complete the preceding scenario's public background reads before a new
  // document/context; request cancellation itself is tested explicitly below.
  const visit=async key=>{await page.waitForLoadState('networkidle');await page.goto(fixture.base+fixture.paths[key],{waitUntil:'networkidle'});};
  const openTip=async()=>{await page.locator('[data-tip-open]').click();await tipForm.waitFor();};
  const submitTip=()=>tipForm.locator('[type=submit]').click();
  const reset=()=>{overrides.clear();writeHandler=undefined;readTransform=(u,v)=>v;readHold=undefined;release=undefined;failRead=undefined;sessionHold=undefined;};
  const emitSession=()=>page.evaluate(async()=>{const detail=await(await fetch('/api/v1/social/session')).json();document.dispatchEvent(new CustomEvent('chunnel:session',{detail}));});
  const updatedIdentity=(before,body,actor)=>{
    const identity=structuredClone(before),now=new Date().toISOString();identity.version=body.version+1;
    identity.history=[...(identity.history||[]),{action:body.action,key:body.key||'',reason:body.reason||'',actor_id:actor,created_at:now}].slice(-100);
    if(body.action==='equip')identity.equipped=body.equipped;
    if(body.action==='apply')identity.application={key:body.key,label:body.label,evidence:body.evidence,status:'pending',reason:'',updated_at:now};
    if(body.action==='grant'||body.action==='approve'){
      const label=body.action==='approve'?identity.application.label:body.label;
      identity.grants=identity.grants.filter(g=>g.key!==body.key);identity.grants.push({key:body.key,label,actor_id:actor,granted_at:now,expires_at:body.days?new Date(Date.parse(now)+body.days*86400000).toISOString():'0001-01-01T00:00:00Z'});
    }
    if(body.action==='approve'||body.action==='reject')Object.assign(identity.application,{status:body.action==='approve'?'approved':'rejected',reason:body.reason,updated_at:now});
    if(body.action==='revoke'){identity.grants=identity.grants.filter(g=>g.key!==body.key);identity.equipped=identity.equipped.filter(k=>k!==body.key);}
    return identity;
  };
  await context.route('**/*',async route=>{
    const request=route.request(),url=new URL(request.url()),key=url.pathname+url.search;
    const scriptPath=url.pathname.replace(/^\/core-assets\/[a-f0-9]+\/js\//,'/core-assets/js/');
    if(scripts.has(scriptPath)){servedTS.add(scriptPath);await route.fulfill({contentType:'text/javascript',body:scripts.get(scriptPath)});return;}
    if(request.method()!=='GET'&&request.method()!=='HEAD'){
      const body=request.postDataJSON();writes.push({path:url.pathname,body});
      if(writeHandler&&[tipPath,socialPath,adminPath,accountPath].includes(url.pathname)){await writeHandler(route,body,url.pathname);return;}
      await route.fulfill({status:409,json:{error:{code:'fixture.intercepted',message:'业务写入已隔离'}}});return;
    }
    if(!url.pathname.startsWith('/api/v1/')){await route.continue();return;}
    if(failRead===url.pathname){await route.fulfill({status:503,json:{error:{code:'recognition.unavailable',message:'读取暂不可用'}}});return;}
    if(sessionHold&&url.pathname==='/api/v1/social/session'){const proceed=sessionHold;sessionHold=undefined;await proceed();}
    if(![tipPath,socialPath,adminPath,accountPath].includes(url.pathname)){await route.continue();return;}
    const response=await route.fetch();if(!response.ok()){await route.fulfill({response});return;}
    const original=await response.json(),value=readTransform(url.pathname,structuredClone(overrides.get(key)||original));observed.set(key,structuredClone(value));
    if(readHold===url.pathname){readHold=undefined;await new Promise(resolve=>{release=resolve;});}
    await route.fulfill({response,json:value});
  });
  try{
    await login();await visit('article');await openTip();assert.ok(observed.get(tipPath).currencies.length);check('real Go article loads authenticated tip wallet');
    await tipForm.locator('[name=amount]').fill('3.5');writeHandler=route=>route.abort();await submitTip();await byText(tipStatus,'网络');
    const original=writes.at(-1).body;assert.equal(original.amount,'3.5');assert.equal(await tipForm.locator('[name=amount]').isDisabled(),true);
    writeHandler=route=>route.fulfill({status:403,json:{error:{code:'auth.csrf_invalid',message:'CSRF 已变化'}}});await submitTip();await byText(tipStatus,'CSRF');assert.deepEqual(writes.at(-1).body,original);check('unknown payment and CSRF rejection retain original request');
    await page.locator('[data-tip-close]').click();await page.reload({waitUntil:'domcontentloaded'});await openTip();assert.equal(await tipForm.locator('[name=amount]').inputValue(),'3.5');
    writeHandler=(route,body)=>route.fulfill({json:{completed:true,request_id:body.request_id,message:'打赏成功'}});await submitTip();await byText(page.locator('[data-tip-result]'),'打赏成功');assert.deepEqual(writes.at(-1).body,original);check('reload and deliberate retry preserve exact payment and clear only confirmed record');

    await openTip();let paymentRelease;writeHandler=(route,body)=>new Promise(resolve=>{paymentRelease=async()=>{await route.fulfill({json:{completed:true,request_id:body.request_id,message:'迟到打赏成功'}}).catch(()=>{});resolve();};});
    await submitTip();await until(()=>!!paymentRelease);await page.locator('[data-tip-close]').click();await paymentRelease();await delay(80);assert.doesNotMatch(await page.locator('[data-tip-result]').innerText(),/迟到/);
    assert.ok(await page.evaluate(key=>sessionStorage.getItem(key),`newpress:tip:${fixture.reader_id}:${fixture.post_id}`));check('closing payment dialog suppresses late success and retains recovery');
    writeHandler=route=>route.fulfill({json:{completed:true,request_id:'wrong-operation',message:'错误回执'}});await openTip();await submitTip();await byText(tipStatus,'未确认');assert.equal(await tipForm.locator('[name=amount]').isDisabled(),true);check('wrong payment receipt cannot unlock or clear pending operation');
    await page.locator('[data-tip-close]').click();await context.clearCookies();await login('owner');await emitSession();assert.doesNotMatch(await page.locator('[data-tip-result]').innerText(),/迟到/);check('session switch clears private payment presentation');
    reset();await login();await visit('article');await page.evaluate(()=>sessionStorage.clear());
    readHold=tipPath;await page.locator('[data-tip-open]').click();await until(()=>!!release);await page.locator('[data-tip-close]').click();release();await delay(80);assert.equal(await tipForm.isVisible(),false);check('closed dialog ignores delayed wallet read');
    await openTip();await tipForm.locator('[name=amount]').fill('2.5');let sessionRelease;
    sessionHold=()=>new Promise(resolve=>{sessionRelease=resolve;});writeHandler=(route,body)=>route.fulfill({json:{completed:true,request_id:body.request_id,message:'快照成功'}});
    await submitTip();await until(()=>!!sessionRelease);await tipForm.locator('[name=amount]').fill('4.5');sessionRelease();await byText(page.locator('[data-tip-result]'),'快照成功');assert.equal(writes.at(-1).body.amount,'2.5');check('confirmation snapshots amount before session refresh');
    await openTip();await tipForm.locator('[name=amount]').fill('0');const noWrite=writes.length;await submitTip();await byText(tipStatus,'范围');assert.equal(writes.length,noWrite);check('invalid amount rejected locally');

    reset();await visit('account');await page.locator('[data-identity-medals] input').first().waitFor();assert.match(await page.locator('[data-identity-application-state]').innerText(),/审核中/);check('real account loads medal and pending application');
    const equip=page.locator('[data-identity-equip]');let identityRelease;
    writeHandler=(route,body)=>new Promise(resolve=>{const fresh=structuredClone(observed.get(socialPath));fresh.identity=updatedIdentity(fresh.identity,body,fixture.reader_id);identityRelease=async()=>{overrides.set(socialPath,fresh);await route.fulfill({json:{identity:fresh.identity}});resolve();};});
    await equip.locator('button[type=submit]').click();await until(()=>!!identityRelease);await equip.locator('input[name=medal]').first().uncheck();await identityRelease();await byText(identityStatus,'已保存');assert.equal(await equip.locator('input[name=medal]').first().isChecked(),false);check('equipment edit during save survives readback');
    writeHandler=route=>route.abort();await equip.locator('button[type=submit]').click();await byText(identityStatus,'先核对');const beforeCheck=writes.length;await equip.locator('button[type=submit]').click();await byText(identityStatus,'再次提交');assert.equal(writes.length,beforeCheck);check('unknown identity write reconciles without replay');
    writeHandler=(route,body)=>{const fresh=structuredClone(observed.get(socialPath));fresh.identity=updatedIdentity(fresh.identity,body,fixture.reader_id);overrides.set(socialPath,fresh);failRead=socialPath;return route.fulfill({json:{identity:fresh.identity}});};
    await equip.locator('button[type=submit]').click();await byText(identityStatus,'展示刷新未完成');assert.match(await identityStatus.innerText(),/已保存/);check('confirmed identity save remains success when later refresh fails');
    reset();readTransform=(url,value)=>{if(url===socialPath){delete value.identity.application;value.identity.history=value.identity.history.filter(h=>h.action!=='apply');}return value;};await visit('account');const apply=page.locator('[data-identity-apply]');await apply.waitFor();
    await apply.locator('[name=label]').fill('新摄影作者');await apply.locator('[name=evidence]').fill('原作品说明');
    writeHandler=(route,body)=>{assert.equal(body.label,'新摄影作者');assert.equal(body.evidence,'原作品说明');const current=observed.get(socialPath);return route.fulfill({json:{identity:updatedIdentity(current.identity,{...body,evidence:'错误作品说明'},fixture.reader_id)}});};
    await apply.locator('button[type=submit]').click();await byText(identityStatus,'先核对');assert.equal(await apply.locator('[name=evidence]').inputValue(),'原作品说明');check('application receipt must match original private evidence');
    reset();await page.reload({waitUntil:'domcontentloaded'});await byText(identityStatus,'');await page.locator('[data-identity-content]').waitFor();readHold=socialPath;await page.locator('[data-identity-refresh]').click();await until(()=>!!release);await login('owner');await emitSession();release();await delay(100);assert.equal(await page.locator('[data-identity-content]').isVisible(),false);assert.equal(await page.locator('[data-identity-application-state]').innerText(),'');check('identity switch removes private application and rejects late read');

    reset();await login('owner');await visit('admin');await byText(adminStatus,'配置已载入');assert.ok(await page.locator('.recognition-design').count());check('actual Go admin page loads catalog and pending queue');
    const cfgForm=page.locator('[data-recognition-settings]'),help=cfgForm.locator('[name=verification_help]');await help.fill('提交时的引导');let settingsRelease;
    writeHandler=(route,body)=>new Promise(resolve=>{settingsRelease=async()=>{const next=structuredClone(observed.get(adminPath));next.settings={...body,version:body.version+1};overrides.set(adminPath,next);await route.fulfill({json:{saved:true}});resolve();};});
    await cfgForm.locator('button[type=submit]').click();await until(()=>!!settingsRelease);await help.fill('保存期间的新引导');await settingsRelease();await byText(adminStatus,'新修改');assert.equal(await help.inputValue(),'保存期间的新引导');check('admin settings save preserves newer form draft');
    writeHandler=(route,body)=>{const next=structuredClone(observed.get(adminPath));next.settings={...body,version:body.version+1};overrides.set(adminPath,next);return route.abort();};await cfgForm.locator('button[type=submit]').click();await byText(adminStatus,'先核对');const saveCount=writes.length;await cfgForm.locator('button[type=submit]').click();await byText(adminStatus,'保存成功');assert.equal(writes.length,saveCount);check('lost settings receipt is reconciled by version and full readback');
    const lookup=async user=>{await page.locator('[data-recognition-lookup] [name=username]').fill(user);await page.locator('[data-recognition-lookup] button').click();await page.locator('[data-recognition-member-summary]').filter({hasText:'@'+user}).waitFor();};
    await lookup(fixture.reader_username);assert.match(await page.locator('[data-recognition-evidence]').innerText(),/PRIVATE_RECOGNITION_EVIDENCE/);check('admin exact member lookup exposes private evidence only here');
    const manage=page.locator('[data-recognition-manage]');await manage.locator('[name=reason]').fill('审核通过');let manageRelease;
    writeHandler=(route,body)=>new Promise(resolve=>{const key=accountPath+'?username='+encodeURIComponent(fixture.reader_username),fresh=structuredClone(observed.get(key));fresh.identity=updatedIdentity(fresh.identity,body,fixture.owner_id);manageRelease=async()=>{overrides.set(key,fresh);await route.fulfill({json:{identity:fresh.identity}});resolve();};});
    await manage.locator('button[type=submit]').click();await until(()=>!!manageRelease);await lookup(fixture.owner_username);await manage.locator('[name=reason]').fill('另一位成员的新原因');await manageRelease();await until(async()=>!await page.locator('[data-recognition-manage] button[type=submit]').isDisabled());assert.match(await page.locator('[data-recognition-member-summary]').innerText(),new RegExp('@'+fixture.owner_username));assert.equal(await manage.locator('[name=reason]').inputValue(),'另一位成员的新原因');check('late review completion cannot replace newly selected member or draft');
    await lookup(fixture.reader_username);await manage.locator('[name=action]').selectOption('grant');await manage.locator('[name=key]').selectOption('original');await manage.locator('[name=reason]').fill('原授予原因');
    writeHandler=(route,body)=>new Promise(resolve=>{const key=accountPath+'?username='+encodeURIComponent(fixture.reader_username),fresh=structuredClone(observed.get(key));fresh.identity=updatedIdentity(fresh.identity,body,fixture.owner_id);manageRelease=async()=>{overrides.set(key,fresh);await route.fulfill({json:{identity:fresh.identity}});resolve();};});manageRelease=undefined;
    await manage.locator('button[type=submit]').click();await until(()=>!!manageRelease);await manage.locator('[name=reason]').fill('新审核原因');await manageRelease();await byText(memberStatus,'已更新');await until(async()=>!await manage.locator('button[type=submit]').isDisabled());assert.equal(await manage.locator('[name=reason]').inputValue(),'新审核原因');check('review preserves edits made while saving the same member');
    writeHandler=route=>route.abort();await manage.locator('button[type=submit]').click();await byText(memberStatus,'先核对');const reviewCount=writes.length;await manage.locator('button[type=submit]').click();await byText(memberStatus,'再次执行');assert.equal(writes.length,reviewCount);check('unknown member operation reads current version before another action');
    failRead=adminPath;writeHandler=(route,body)=>{const key=accountPath+'?username='+encodeURIComponent(fixture.reader_username),fresh=structuredClone(observed.get(key));fresh.identity=updatedIdentity(fresh.identity,body,fixture.owner_id);overrides.set(key,fresh);return route.fulfill({json:{identity:fresh.identity}});};
    await manage.locator('button[type=submit]').click();await byText(memberStatus,'列表刷新未完成');assert.match(await memberStatus.innerText(),/已更新/);check('completed review does not become failure when queue refresh fails');
    reset();await page.waitForLoadState('networkidle');await page.evaluate(()=>{dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true}));dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));});await byText(adminStatus,'配置已载入');assert.equal(await cfgForm.isVisible(),true);check('persisted admin page restore reloads the current session and settings');
    reset();await login();await emitSession();assert.equal(await cfgForm.isVisible(),false);assert.equal(await page.locator('[data-recognition-evidence]').innerText(),'');check('admin session switch clears private evidence and settings');

    reset();await context.clearCookies();await visit('article');await page.locator('[data-tip-open]').click();await page.locator('[data-tip-login]').waitFor();assert.equal(await tipForm.isVisible(),false);check('anonymous visitors get login guidance without payment form');
    await login('owner');await visit('admin');await byText(adminStatus,'配置已载入');
    for(const width of [1280,390])for(const colorScheme of ['light','dark']){
      await page.setViewportSize({width,height:960});await page.emulateMedia({colorScheme});await page.waitForFunction(theme=>document.documentElement.dataset.theme===theme,colorScheme);await delay(400);
      await page.waitForFunction(()=>document.documentElement.scrollWidth<=innerWidth+2);await page.screenshot({path:resolve(directory,`${variant}-${name}-admin-${width}-${colorScheme}.png`),fullPage:true});
    }
    check('admin responsive light and dark layouts stay within viewport');
    await page.setViewportSize({width:390,height:960});await login();await visit('article');const medal=page.locator('[data-medal-details]').first();await medal.click();await page.locator('.identity-popover').waitFor();await page.keyboard.press('Escape');assert.equal(await medal.getAttribute('aria-expanded'),'false');await openTip();await page.screenshot({path:resolve(directory,`${variant}-${name}-tip-dark.png`),fullPage:true});check('medal dialog keyboard dismissal and narrow tip dialog work');
    if(variant==='typescript')assert.deepEqual([...servedTS].sort(),[...scripts.keys()].sort());
    assert.deepEqual(errors,[]);check('both entry assets covered and no uncaught page errors');
  }catch(error){await page.screenshot({path:resolve(directory,`${variant}-${name}-failure.png`),fullPage:true}).catch(()=>{});await writeFile(resolve(directory,`${variant}-${name}-failure.json`),JSON.stringify({message:error.message,errors,url:page.url(),statuses:await page.locator('[data-tip-status],[data-identity-status],[data-recognition-status],[data-recognition-account-status]').allTextContents(),writes},null,2));throw error;}
  finally{await context.close();await browser.close();}
}
await writeFile(resolve(directory,`${variant}-browser-acceptance.json`),JSON.stringify({variant,checks:evidence.length,evidence},null,2));
console.log(`Recognition ${variant}: ${evidence.length} checks passed`);
