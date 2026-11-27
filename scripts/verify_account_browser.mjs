// Isolated Go fixture, real page assets/API reads, intercepted business mutations.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
import { compileFrontendEntry } from './frontend_entries.mjs';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const variant = process.argv[4] === 'typescript' ? 'typescript' : 'generated';
const engines = { chromium, firefox, webkit }, selected = process.argv[3] && process.argv[3] !== 'all' ? [process.argv[3]] : Object.keys(engines);
const assets = new Map(), evidence = [];
if (variant === 'typescript') for (const entry of ['registration','account','account-nav','account-import','engagement']) assets.set(`chunnel-${entry}.js`, (await compileFrontendEntry(entry, {minify:false})).code);
const policy = { configured:true,registration:'open',fields:{display_name:'required',username:'required',email:'optional',phone:'optional',password:'required',invitation_code:'hidden',registration_code:'hidden'},email_available:true,sms_available:true,recovery_available:true,email_verification:false,phone_verification:false,password_confirmation:true,code_login_enabled:true,registration_code_url:'',oauth_bindings:[],phone_region:{selectable:true,default_code:'+86',codes:['+86','+1']} };
for (const name of selected) {
  const browser=await engines[name].launch(), context=await browser.newContext({viewport:{width:1280,height:900},reducedMotion:'reduce'});
  const page=await context.newPage(), errors=[], writes=[], reads=[], scripts=[];
  page.setDefaultTimeout(12000);
  let libraryMode='real', releaseLibrary, profileMode='real', imported=false, registerMode='unconfirmed';
  const json = (route,data,status=200) => route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
  const post = title => ({id:2,title,excerpt:'用于确认读取、刷新和原子替换。',url:'/posts/home-browser-0',cover_url:'',author_name:'作者',author_url:'/authors/1/',collection:'',published_at:'2026-11-26T00:00:00Z'});
  const library = title => ({pages:1,total:1,items:[post(title)],collections:{'':1}});
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{const url=new URL(request.url());if(request.resourceType()==='script')scripts.push(url.pathname);if(request.method()==='GET'&&url.pathname.startsWith('/api/'))reads.push(url.pathname);});
  await page.route('**/*',async route=>{
    const req=route.request(),url=new URL(req.url()),path=url.pathname;
    if (!['GET','HEAD'].includes(req.method())) {
      const body=req.headers()['content-type']?.includes('application/json') ? req.postDataJSON() : null;
      writes.push({path,method:req.method(),body});
      if(path==='/api/v1/social/import') { if(!body.preview)imported=true;return json(route,{preview:body.preview,added:1,existing:0,skipped:0}); }
      if(path==='/api/v1/social/engagement') return json(route,profileMode==='unconfirmed'?{ok:false}:{ok:true});
      if(path==='/api/v1/social/profile') {
        if(profileMode==='conflict')return json(route,{error:{message:'资料已在其他地方修改，请重新载入后重试',code:'profile.conflict'}},409);
        const actual=await context.request.get(fixture.url+path), data=await actual.json();
        return json(route,profileMode==='malformed'?{...data,version:'wrong'}:{...data,display_name:body.display_name,bio:body.bio,locale:body.locale,version:data.version+1});
      }
      if(path==='/api/v1/social/profile/avatar') { const actual=await context.request.get(fixture.url+'/api/v1/social/profile');const data=await actual.json();return json(route,{...data,version:data.version+1}); }
      if(path==='/api/v1/social/access/register') return json(route,registerMode==='unconfirmed'?{}:{ok:true,username:'migration-reader'});
      if(path==='/api/v1/social/access/code') return json(route,{ok:true,challenge:'browser-fixture-challenge',retry_after:60});
      if(path==='/api/v1/social/access/code-login'||path==='/api/v1/social/access/request'||path==='/api/v1/social/access/complete')return json(route,{ok:true,username:'migration-reader'});
      return json(route,{error:{message:'浏览器验收未保存该操作'}},409);
    }
    if(path==='/api/v1/social/access') {
      const actual=await route.fetch(), data=await actual.json();
      // Keep the real policy schema; open fixture-only registration paths without changing settings.
      assert.equal(typeof data.configured,'boolean');assert.ok(data.fields);
      return json(route,{...data,...policy});
    }
    if(path==='/api/v1/social/library'&&libraryMode!=='real') {
      if(libraryMode==='malformed')return json(route,{pages:1,total:1,items:null,collections:{}});
      if(libraryMode==='hold') { releaseLibrary=()=>json(route,library('迟到内容'));return; }
      return json(route,library(libraryMode));
    }
    const code=assets.get(path.split('/').pop());
    return code ? route.fulfill({contentType:'application/javascript',body:code}) : route.continue();
  });
  const check=label=>{evidence.push({browser:name,variant,check:label});console.log(`${name} ${variant}: ${label}`);};
  const waitText=async(selector,text)=>page.waitForFunction(({selector,text})=>document.querySelector(selector)?.textContent.includes(text),{selector,text});
  const waitState=state=>page.waitForFunction(state=>document.querySelector('[data-library-status]')?.dataset.state===state,state);
  const tab=async kind=>{await page.locator(`[data-library-tab="${kind}"]`).click();await page.waitForFunction(kind=>document.querySelector('[data-account-room]')?.dataset.accountView===kind,kind);};
  const open=async path=>{
    const res=await page.goto(fixture.url+path);assert.equal(res.status(),200);await page.waitForLoadState('networkidle');
    const reject=page.locator('[data-cookie-reject]').filter({visible:true}).first();if(await reject.isVisible())await reject.click();
  };
  const until=async predicate=>{const until=Date.now()+12000;while(!predicate()){if(Date.now()>until)throw new Error('Expected request not observed');await new Promise(resolve=>setTimeout(resolve,25));}};
  const writeCount=path=>writes.filter(write=>write.path===path).length;
  try {
    await context.request.post(fixture.url+'/fixture/reload');
    await context.request.post(fixture.url+'/fixture/style/classic');
    await open('/account/access/');
    const registration=page.locator('[data-account-access]').filter({visible:true}).first();
    await registration.locator('[data-access-register]').waitFor({state:'visible'});
    await registration.locator('[data-access-register] input[name="display_name"]').fill('迁移验收读者');
    await registration.locator('[data-access-register] input[name="username"]').fill('migration-reader');
    await registration.locator('[data-access-register] input[name="password"]').fill('Fixture password 2026');
    await registration.locator('[data-access-register] input[name="confirm_password"]').fill('Fixture password 2026');
    await registration.locator('[data-registration-submit]').click();
    await waitText('[data-access-result]','未能确认');
    assert.equal(await registration.locator('[data-access-register] input[name="username"]').getAttribute('readonly'),'');
    const first=writes.find(write=>write.path.endsWith('/register')).body;
    registerMode='confirmed';await registration.locator('[data-registration-submit]').click();await registration.locator('[data-access-success]').waitFor({state:'visible'});
    assert.deepEqual(writes.filter(write=>write.path.endsWith('/register'))[1].body,first);
    check('Registration preserves the original request through an unconfirmed response and retry.');

    await open('/account/access/?flow=login');
    const login=page.locator('[data-access-register]').filter({visible:true});
    await login.locator('input[name="email"]').fill('fixture@example.test');await login.locator('[data-send-code="email"]').click();
    await waitText('[data-access-result]','验证码已发送');await login.locator('input[name="email_code"]').fill('123456');await login.locator('[data-registration-submit]').click();
    await page.locator('[data-access-success]').filter({visible:true}).waitFor();
    assert.equal(writes.find(write=>write.path.endsWith('/code-login')).body.challenge,'browser-fixture-challenge');
    await open('/account/access/?flow=recover');await page.locator('[data-access-request] input[name="email"]').fill('fixture@example.test');await page.locator('[data-access-send]').click();await waitText('[data-access-result]','申请已受理');
    await open('/account/access/#kind=recover&token=fixture-only');assert.equal(new URL(page.url()).hash,'');
    await page.locator('[data-access-complete] input[name="password"]').fill('Fixture changed password 2026');await page.locator('[data-access-complete] input[name="confirm_password"]').fill('Fixture changed password 2026');await page.locator('[data-access-finish]').click();await waitText('[data-access-success-title]','密码已更新');
    check('Code login and recovery use the existing forms; recovery tokens are removed from the URL.');

    await page.goto(fixture.login);await open('/account/?view=bookmark');await waitState('empty');
    assert.ok(reads.includes('/api/v1/social/engagement'));
    await tab('posts');await waitState('ready');assert.ok(await page.locator('[data-library-list] article').count()>0);
    await tab('comments');await waitState('empty');
    check('Real Go session, engagement, empty library and own-content responses work with the migrated entry.');

    libraryMode='初始收藏';await tab('bookmark');await waitText('[data-library-list]','初始收藏');
    libraryMode='hold';const old=await page.locator('[data-library-list]').textContent();await page.locator('[data-library-search] button[type="submit"]').click();await until(()=>releaseLibrary);
    assert.equal(await page.locator('[data-library-list]').evaluate(el=>el.inert),true);assert.equal(await page.locator('[data-library-list]').textContent(),old);
    libraryMode='当前动态';await tab('feed');await waitText('[data-library-list]','当前动态');await releaseLibrary();releaseLibrary=null;await page.waitForTimeout(100);
    assert.equal((await page.locator('[data-library-list]').textContent()).includes('迟到内容'),false);
    libraryMode='malformed';await page.locator('[data-library-search] button[type="submit"]').click();await waitState('error');await waitText('[data-library-list]','当前动态');
    libraryMode='恢复结果';await page.locator('[data-library-status]').getByRole('button',{name:'重新加载'}).click();await waitText('[data-library-list]','恢复结果');
    check('Refresh keeps old rows inert, rejects malformed payloads and suppresses obsolete view responses.');

    await tab('profile');await page.waitForFunction(()=>Number(document.querySelector('[data-profile-form] input[name="version"]')?.value)>0);
    const version=await page.locator('[data-profile-form] input[name="version"]').inputValue();
    await page.locator('[data-profile-form] input[name="display_name"]').fill('浏览器验收昵称');
    for(const mode of ['conflict','malformed']) {profileMode=mode;await page.locator('[data-profile-form] button[type="submit"]').click();await waitText('[data-profile-result]',mode==='conflict'?'其他地方修改':'数值格式错误');assert.equal(await page.locator('[data-profile-form] input[name="version"]').inputValue(),version);}
    profileMode='confirmed';await page.locator('[data-profile-form] button[type="submit"]').click();await waitText('[data-profile-result]','资料已保存');assert.equal(await page.locator('[data-profile-form] input[name="version"]').inputValue(),String(Number(version)+1));
    const before=writeCount('/api/v1/social/profile/avatar');await page.locator('[data-avatar-form] input[type="file"]').setInputFiles({name:'large.png',mimeType:'image/png',buffer:Buffer.alloc(2*1024*1024+1)});await page.locator('[data-avatar-form] button[type="submit"]').click();await waitText('[data-avatar-result]','不能超过');assert.equal(writeCount('/api/v1/social/profile/avatar'),before);
    await page.locator('[data-avatar-form] input[type="file"]').setInputFiles({name:'avatar.png',mimeType:'image/png',buffer:Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jF1sAAAAASUVORK5CYII=','base64')});await page.locator('[data-avatar-form] button[type="submit"]').click();await waitText('[data-avatar-result]','头像已更新');
    check('Profile conflict/malformed saves retain edits; confirmed profile/avatar responses update versions and oversized files stay local.');

    const local={saved:[{url:'/posts/home-browser-0'}],likes:['post:2']};await page.evaluate(local=>localStorage.setItem('chunnel.preferences',JSON.stringify(local)),local);await tab('import');
    assert.equal(await page.locator('[data-import-confirm]').isVisible(),false);await page.locator('[data-import-preview]').click();await waitText('[data-import-result]','可新增 1');await page.locator('[data-import-confirm]').click();await waitText('[data-import-result]','已新增 1');assert.equal(imported,true);
    assert.deepEqual(await page.evaluate(()=>JSON.parse(localStorage.getItem('chunnel.preferences'))),local);
    check('Guest records require preview and confirmation, and browser storage remains intact.');

    libraryMode='收藏整理';await tab('bookmark');await waitText('[data-library-list]','收藏整理');await page.locator('[data-library-list]').getByRole('button',{name:'整理',exact:true}).first().click();
    await page.locator('[data-collection-form] input[name="collection"]').fill('验收收藏夹');profileMode='unconfirmed';await page.locator('[data-collection-form] button[type="submit"]').click();await waitText('[data-collection-error]','尚未确认');assert.equal(await page.locator('[data-collection-dialog]').isVisible(),true);
    profileMode='confirmed';await page.locator('[data-collection-form] button[type="submit"]').click();await page.locator('[data-collection-dialog]').waitFor({state:'hidden'});
    check('Unconfirmed engagement writes keep the collection dialog open for retry.');

    await open('/posts/home-browser-0');
    await page.waitForFunction(()=>window.ChunnelEngagement?.authenticated);
    const favorite=page.locator('[data-favorite]').filter({visible:true}).first();
    await favorite.waitFor({state:'visible'});assert.equal(await favorite.getAttribute('aria-pressed'),'false');
    const beforeFavorite=writeCount('/api/v1/social/engagement');profileMode='unconfirmed';await favorite.click();await until(()=>writeCount('/api/v1/social/engagement')===beforeFavorite+1);await page.waitForFunction(()=>![...document.querySelectorAll('[data-favorite]')].some(button=>button.disabled));
    assert.equal(await favorite.getAttribute('aria-pressed'),'false');
    profileMode='confirmed';await favorite.click();await page.waitForFunction(()=>[...document.querySelectorAll('[data-favorite]')].some(button=>button.getAttribute('aria-pressed')==='true'));
    check('Public article bookmark controls retain confirmed state on failed acknowledgement and repaint after success.');

    for(const style of ['classic','liquid-glass']) {
      await context.request.post(fixture.url+'/fixture/style/'+style);await page.setViewportSize({width:390,height:844});await open('/account/?view=bookmark');await waitText('[data-library-list]','收藏整理');
      const toggle=page.locator('[data-account-nav-toggle]');await toggle.click();await page.locator('[data-library-tab="profile"]').click();await page.waitForFunction(()=>Number(document.querySelector('[data-profile-form] input[name="version"]')?.value)>0);
      assert.equal(await toggle.getAttribute('aria-expanded'),'false');assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
      await page.evaluate(()=>{window.scrollTo(0,0);if(document.activeElement instanceof HTMLElement)document.activeElement.blur();});
      await page.waitForTimeout(250);
      await page.screenshot({path:resolve(directory,`${name}-${variant}-${style}-mobile-profile.png`),fullPage:true});
      check(`${style} mobile account navigation, layout and profile form remain usable.`);
    }
    assert.equal(scripts.some(path=>/newpress-analytics(?:-loader)?\.js$/.test(path)),false);
    assert.deepEqual(errors,[]);check('All five assets run without page errors; account pages do not download analytics.');
    await writeFile(resolve(directory,`${name}-${variant}-account.json`),JSON.stringify({browser:name,variant,errors,reads,writes,scripts,checks:evidence.filter(item=>item.browser===name)},null,2));
  } catch(error) {
    await page.screenshot({path:resolve(directory,`${name}-${variant}-failure.png`),fullPage:true}).catch(()=>{});
    await writeFile(resolve(directory,`${name}-${variant}-failure.json`),JSON.stringify({error:error.message,errors,writes,url:page.url()},null,2));throw error;
  } finally {await context.close();await browser.close();}
}
await writeFile(resolve(directory,`account-${variant}-summary.json`),JSON.stringify(evidence,null,2));
console.log(`${evidence.length} account browser groups passed (${variant}).`);
