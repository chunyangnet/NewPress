// Real Go templates and read APIs; mail, feedback and verification writes are
// controlled browser responses. The disposable Go fixture proves state on exit.
import assert from 'node:assert/strict';
import {readFile,writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {chromium,firefox,webkit} from 'playwright';
import {compileFrontendEntry} from './frontend_entries.mjs';
const directory=resolve(process.argv[2]),fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const variant=process.argv[4]==='typescript'?'typescript':'generated',engines={chromium,firefox,webkit},selected=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines);
const assets=new Map(),evidence=[];
if(variant==='typescript')for(const entry of ['feedback','invitations','verification'])assets.set(`chunnel-${entry}.js`,(await compileFrontendEntry(entry,{minify:false})).code);
const until=async predicate=>{const deadline=Date.now()+12000;while(!predicate()){if(Date.now()>deadline)throw new Error('Expected intercepted request missing');await new Promise(resolve=>setTimeout(resolve,20));}};
const png='data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j3ioAAAAASUVORK5CYII=';
for(const name of selected){
  const browser=await engines[name].launch(),context=await browser.newContext({viewport:{width:1365,height:960},reducedMotion:'reduce'}),page=await context.newPage();
  page.setDefaultTimeout(12000);
  let feedbackMode='invalid',invitationMode='invalid',listMode='real',challenge=0,heldFeedback,heldInvitation,heldList;
  const errors=[],writes=[],scripts=[];
  const json=(route,data,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
  page.on('pageerror',error=>errors.push({message:error.message,url:page.url()}));page.on('dialog',dialog=>dialog.accept());
  page.on('request',request=>{if(request.resourceType()==='script')scripts.push(new URL(request.url()).pathname);});
  await context.route('**/*',async route=>{
    const request=route.request(),url=new URL(request.url()),path=url.pathname,filename=path.split('/').at(-1);
    if(assets.has(filename))return route.fulfill({contentType:'text/javascript',body:assets.get(filename)});
    if(path==='/api/v1/admin/invitations'&&request.method()==='GET'){
      if(listMode==='fail')return json(route,{error:{message:'列表暂不可用'}},503);
      if(listMode==='denied')return json(route,{error:{message:'权限已失效'}},403);
      if(listMode==='hold'){const response=await route.fetch();const data=await response.json();heldList=()=>json(route,{...data,items:[{...data.items[0],email:'late@example.test'}]});return;}
    }
    if(request.method()!=='GET'&&request.method()!=='HEAD'){
      if(path.startsWith('/fixture/'))return route.continue();
      const raw=request.postData(),body=request.headers()['content-type']?.includes('application/json')?request.postDataJSON():null;
      writes.push({path,raw,body,headers:request.headers()});
      if(path==='/api/v1/verification/challenge')return json(route,{id:`challenge-${++challenge}`,image:png,expires_in:120});
      if(path==='/api/v1/verification/verify')return json(route,body.preview?{ok:true,preview:true}:{token:'controlled-browser-receipt',expires_in:120});
      if(path.startsWith('/api/v1/feedback/')){
        if(feedbackMode==='conflict')return json(route,{error:{message:'内容已更新，请核对最新版本。'}},409);
        const receipt={slug:'ideas',version:body.version+(['submit','reply','vote'].includes(body.action)?4:1),item_id:body.action==='submit'?fixture.item_id:body.item_id,reply_id:body.action==='reply'?fixture.reply_id:undefined,reply_page:body.action==='reply'?1:undefined,vote_count:2,voted:body.enabled??false};
        if(feedbackMode==='invalid'){if(body.action==='vote')receipt.vote_count=-1;else receipt.slug='wrong-board';}
        if(feedbackMode==='hold'){heldFeedback=()=>json(route,receipt);return;}
        return json(route,receipt);
      }
      if(path.startsWith('/api/v1/admin/invitations')){
        if(invitationMode==='hold'){heldInvitation=()=>json(route,{ok:true});return;}
        return json(route,invitationMode==='valid'?{ok:true}:{});
      }
      if(path==='/admin/login/')return route.fulfill({contentType:'text/html; charset=utf-8',body:'<!doctype html><p>登录提交已拦截</p>'});
      return json(route,{error:{message:'只读验收未提交此操作'}},503);
    }
    return route.continue();
  });
  const check=label=>{evidence.push({browser:name,variant,check:label});console.log(`${name} ${variant}: ${label}`);};
  const text=(selector,value)=>page.waitForFunction(({selector,value})=>document.querySelector(selector)?.textContent.includes(value),{selector,value});
  const open=async path=>{await page.goto(fixture.url+path);await page.waitForLoadState('networkidle');};
  const save=selector=>page.locator(selector).evaluate(form=>form.requestSubmit());
  const change=async(selector,value)=>page.locator(selector).evaluate((node,value)=>{if(node.type==='checkbox')node.checked=value;else node.value=value;node.dispatchEvent(new Event('change',{bubbles:true}));},value);
  const proof=async()=>{const input=page.locator('.verification-local input');await input.fill('12345');await page.getByRole('button',{name:'确认验证',exact:true}).click();};
  const feedbackWrites=()=>writes.filter(row=>row.path.startsWith('/api/v1/feedback/'));
  const invitationWrites=()=>writes.filter(row=>row.path.startsWith('/api/v1/admin/invitations'));
  const invitationsReady=()=>page.waitForFunction(()=>!document.querySelector('[data-invitations] button[type=submit]').disabled);
  try{
    await page.goto(fixture.login);await open('/admin/features/?edit=ideas');
    await page.locator('[name=name]').fill('保留这份配置草稿');await text('[data-live-name]','保留这份配置草稿');
    await change('[data-grant-role=subscriber] [data-grant=read]',false);assert.equal(await page.locator('[data-grant-role=subscriber] [data-grant]:checked').count(),0);
    await change('[data-grant-role=subscriber] [data-grant=vote]',true);assert.equal(await page.locator('[data-grant-role=subscriber] [data-grant=read]').isChecked(),true);
    await change('[name=default_view]','roadmap');await change('[name=roadmap]',false);assert.equal(await page.locator('[name=default_view]').inputValue(),'list');
    feedbackMode='conflict';await save('[data-feedback-form=configure]');await page.locator('[data-feedback-conflict]').waitFor();assert.equal(await page.locator('[name=name]').inputValue(),'保留这份配置草稿');
    feedbackMode='valid';await save('[data-feedback-form=configure]');await text('[data-save-state]','已保存');check('Actual settings preview, role grants, roadmap fallback and 409 recovery preserve the reviewed form.');
    await open('/admin/features/?edit=new');assert.equal(await page.locator('[data-feedback-form=create]').count(),1);check('New feedback boards initialize with the existing savebar and optional fields.');

    await open('/feedback/ideas/');const vote=page.locator(`[data-feedback-action=vote][data-item="${fixture.item_id}"]`);const version=await page.locator('[data-feedback-page]').getAttribute('data-version');
    feedbackMode='invalid';await vote.click();await text('[data-feedback-status]','响应');assert.equal(await page.locator('[data-feedback-page]').getAttribute('data-version'),version);
    feedbackMode='valid';await vote.click();await text('[data-feedback-status]','已支持');assert.deepEqual(feedbackWrites().at(-1).body,feedbackWrites().at(-2).body);check('Malformed vote receipts leave the count, version and retry key intact until a valid receipt arrives.');

    await page.locator('#feedback-compose summary').click();const submit='[data-feedback-form=submit]';
    await page.locator(submit+' [name=title]').fill('保存与重试的浏览器验收');await page.locator(submit+' [name=body]').fill('请保留这段正在编辑的反馈内容。');
    feedbackMode='invalid';await save(submit);await page.locator('.verification-local input').fill('12345');
    const verifies=writes.filter(row=>row.path.endsWith('/verify')).length;await page.locator('.verification-local input').dispatchEvent('keydown',{key:'Enter',isComposing:true,keyCode:229});assert.equal(writes.filter(row=>row.path.endsWith('/verify')).length,verifies);
    await page.getByRole('button',{name:'确认验证',exact:true}).click();await text('[data-feedback-status]','保存回执');const first=feedbackWrites().at(-1).body;
    await save(submit);await proof();await text('[data-feedback-status]','保存回执');assert.deepEqual(feedbackWrites().at(-1).body,first);check('Local verification precedes feedback writes; IME Enter and uncertain retries do not duplicate the command.');

    await open(`/feedback/ideas/items/${fixture.item_id}`);const reply='[data-feedback-form=reply]:not([data-official])';
    await page.locator('[data-feedback-form=edit]').evaluate(form=>form.closest('details').open=true);await page.locator('[data-feedback-form=edit] [name=title]').fill('另一处编辑草稿');await page.locator(reply+' [name=body]').fill('需要保留的回复草稿');
    feedbackMode='valid';await save(reply);await proof();await text('[data-feedback-status]','另一处表单');const firstReply=feedbackWrites().at(-1).body;
    await save(reply);await proof();await text('[data-feedback-status]','另一处表单');assert.deepEqual(feedbackWrites().at(-1).body,firstReply);assert.equal(await page.locator('[data-feedback-form=edit] [name=title]').inputValue(),'另一处编辑草稿');check('Confirmed replies retain their request identity while another unsaved editor keeps the page open.');

    await page.locator(reply+' [name=body]').fill('账号切换时需要保留的内容');feedbackMode='hold';await save(reply);await proof();await until(()=>heldFeedback);
    await page.evaluate(id=>document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,id}})),fixture.owner_id+100);await heldFeedback().catch(()=>{});heldFeedback=null;
    await text('[data-feedback-status]','登录账号已变化');assert.equal(await page.locator(reply+' [name=body]').inputValue(),'账号切换时需要保留的内容');assert.equal(await page.locator(reply+' button[type=submit]').isDisabled(),true);check('Account changes suppress late feedback acknowledgements and retain disabled drafts.');

    await open('/admin/users/');assert.equal(await page.locator('[data-invitations]').getAttribute('data-user-id'),String(fixture.owner_id));await text('[data-invitation-list]','invited@example.test');
    listMode='hold';await page.locator('[data-invitation-refresh]').click();await until(()=>heldList);listMode='real';await page.locator('[data-invitation-refresh]').click();await text('[data-invitation-list]','invited@example.test');await heldList().catch(()=>{});heldList=null;
    await invitationsReady();assert.equal((await page.locator('[data-invitation-list]').textContent()).includes('late@example.test'),false);check('Invitation reads use real owner data and ignore an older refresh response.');
    const email=page.locator('[data-invitations] [name=email]');await email.fill('next@example.test');await save('[data-invitations] form');await text('[data-invitation-status]','尚未确认');
    assert.equal(await page.locator('[data-invitations] button[type=submit]').isDisabled(),true);assert.equal(await email.inputValue(),'next@example.test');
    await page.locator('[data-invitation-refresh]').click();await page.waitForFunction(()=>!document.querySelector('[data-invitations] button[type=submit]').disabled);check('Unconfirmed invitation writes preserve the email and require a successful review before resending.');
    invitationMode='hold';await save('[data-invitations] form');await until(()=>heldInvitation);await email.fill('later@example.test');listMode='fail';await heldInvitation().catch(()=>{});heldInvitation=null;
    await text('[data-invitation-status]','列表暂时无法刷新');assert.match(await page.locator('[data-invitation-status]').textContent(),/邮件已交给/);assert.equal(await email.inputValue(),'later@example.test');check('A confirmed invitation remains successful when refresh fails and newer email input is preserved.');
    listMode='real';await page.locator('[data-invitation-refresh]').click();await invitationsReady();invitationMode='valid';listMode='fail';await page.locator('[data-invitation-list] button').click();await text('[data-invitation-status]','邀请已撤销');assert.equal(await page.locator('[data-invitation-list] button').count(),0);
    listMode='real';await page.locator('[data-invitation-refresh]').click();await invitationsReady();await text('[data-invitation-list]','已使用或撤销');assert.equal(await page.locator('[data-invitation-list] button').count(),0);check('Confirmed revocations cannot be revived by a failed or stale list response.');
    listMode='denied';await page.locator('[data-invitation-refresh]').click();await text('[data-invitation-status]','登录状态已变化');assert.equal(await page.locator('[data-invitation-list]').textContent(),'');assert.equal(await email.inputValue(),'later@example.test');listMode='real';check('Invitation permission loss clears the private list while retaining the email draft.');

    await open('/admin/settings/#site-settings-captcha');await page.locator('[data-verification-test]').waitFor();await page.locator('[data-verification-test]').click();await proof();await text('[data-verification-status]','通过');
    const preview=writes.filter(row=>row.path.endsWith('/verify')).at(-1);assert.equal(preview.body.preview,'local');assert.ok(preview.headers['x-csrf-token']);check('The existing settings preview exchanges a separate authorized preview receipt.');

    for(const [style,scheme,width]of [['classic','light',1365],['liquid-glass','dark',390]]){
      await context.request.post(fixture.url+'/fixture/style/'+style);await page.setViewportSize({width,height:960});await page.emulateMedia({colorScheme:scheme});
      await open('/admin/users/');await text('[data-invitation-list]','invited@example.test');assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+2),true);await page.evaluate(()=>document.fonts.ready);await page.screenshot({path:resolve(directory,`${name}-${variant}-invitations-${style}.png`),fullPage:true,animations:'disabled'});
      await open(`/feedback/ideas/items/${fixture.item_id}`);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+2),true);const rejectOptional=page.getByRole('button',{name:'拒绝可选',exact:true});if(await rejectOptional.count())await rejectOptional.click();await page.evaluate(()=>document.fonts.ready);await page.screenshot({path:resolve(directory,`${name}-${variant}-feedback-${style}.png`),fullPage:true,animations:'disabled'});
    }
    check('Desktop/light/classic and narrow/dark/glass keep real feedback and invitation pages usable.');
    await page.goto(fixture.url+'/fixture/login/guest');await open('/admin/login/');await page.locator('.admin-login-form [name=username]').fill('fixture-user');await page.locator('.admin-login-form [name=password]').fill('fixture-password-only');
    await save('.admin-login-form');await page.locator('.verification-local input').waitFor();await page.getByRole('button',{name:'换一张',exact:true}).click();await page.locator('.verification-local input').fill('12345');
    await page.getByRole('button',{name:'取消',exact:true}).click();assert.equal(await page.locator('.admin-login-form [name=username]').inputValue(),'fixture-user');
    await save('.admin-login-form');await proof();await page.getByText('登录提交已拦截',{exact:true}).waitFor();const logins=writes.filter(row=>row.path==='/admin/login/');assert.equal(logins.length,1);assert.equal(new URLSearchParams(logins[0].raw).get('verification_token'),'controlled-browser-receipt');check('Native login preserves input on cancellation and submits exactly once with the exchanged receipt.');

    const guest=await browser.newContext({javaScriptEnabled:false}),guestPage=await guest.newPage();try{
      await guestPage.goto(fixture.url+'/feedback/ideas/');const body=await guestPage.locator('body').textContent();assert.ok(body.includes('让阅读与讨论更方便'));assert.equal(body.includes('仅本人可见的反馈'),false);assert.equal(await guestPage.locator('[data-feedback-form=submit]').count(),0);
      const denied=await guestPage.request.get(fixture.url+'/api/v1/admin/invitations');assert.equal(denied.status(),401);
    }finally{await guest.close();}
    assert.equal(scripts.some(path=>/react|echarts|analytics-interactive/.test(path)),false);assert.deepEqual(errors,[]);assert.ok(invitationWrites().length>0);check('Public SSR hides private feedback without JS; guests cannot read invitations, and no page loads React/ECharts.');
    await writeFile(resolve(directory,`${name}-${variant}-requests.json`),JSON.stringify({writes,scripts,errors},null,2));
  }catch(error){await page.screenshot({path:resolve(directory,`${name}-${variant}-failure.png`),fullPage:true}).catch(()=>{});await writeFile(resolve(directory,`${name}-${variant}-failure.json`),JSON.stringify({error:String(error),stack:error.stack,url:page.url(),errors,evidence,writes},null,2));throw error;}
  finally{await context.close();await browser.close();}
}
await writeFile(resolve(directory,`${variant}-${selected.join('-')}-summary.json`),JSON.stringify({checks:evidence.length,evidence},null,2));
console.log(`${variant}: ${evidence.length} real-page checks passed.`);
