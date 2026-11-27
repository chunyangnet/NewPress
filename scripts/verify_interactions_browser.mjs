// Real isolated Go reads with intercepted business writes; never use a live site.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
import { compileFrontendEntry } from './frontend_entries.mjs';

const directory=resolve(process.argv[2]), fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
assert.equal(fixture.interactions,true);
const variant=process.argv[4]==='typescript'?'typescript':'generated', engines={chromium,firefox,webkit};
const selected=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines), assets=new Map(), evidence=[];
if(variant==='typescript')for(const name of ['interactions','notifications'])assets.set(`chunnel-${name}.js`,(await compileFrontendEntry(name,{minify:false})).code);
for(const name of selected){
  const browser=await engines[name].launch(),context=await browser.newContext({viewport:{width:1280,height:900},reducedMotion:'reduce'}),page=await context.newPage();
  page.setDefaultTimeout(12000);
  const errors=[],writes=[],reads=[],scripts=[],overlay=[];
  let sendMode='uncertain',preferenceMode='real',preferences,contactMode='real',releaseContact,noticeMode='real',releaseNotice,logoutMode='uncertain';
  const json=(route,data,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
  const message=(id,content,own=false,nonce='')=>({id,sender_id:own?fixture.owner_id:fixture.peer_id,recipient_id:own?fixture.peer_id:fixture.owner_id,content,nonce,created_at:'2026-11-26T01:00:00Z',read_at:null});
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',req=>{const path=new URL(req.url()).pathname;if(req.resourceType()==='script')scripts.push(path);if(req.method()==='GET'&&path.startsWith('/api/'))reads.push(req.url());});
  await page.route('**/*',async route=>{
    const req=route.request(),url=new URL(req.url()),path=url.pathname,body=req.method()==='POST'?req.postDataJSON():null;
    if(!['GET','HEAD'].includes(req.method())){
      writes.push({path,method:req.method(),body});
      if(path===`/api/v1/social/messages/${fixture.peer_id}`){
        const sent=message(57,body.content,true,body.nonce);
        if(sendMode==='uncertain')return json(route,{message:{...sent,nonce:'wrong-confirmation-nonce'}});
        if(!overlay.length)overlay.push(message(56,'发送之前未读取的新消息'),sent);
        return json(route,{message:sent});
      }
      if(path.endsWith('/read')||path==='/api/v1/social/notice-center')return json(route,{ok:true});
      if(path==='/api/v1/social/notification-preferences'){
        if(preferenceMode==='malformed')return json(route,{...preferences,preferences:{...body,version:'invalid'}});
        preferences={...preferences,preferences:{...body,version:body.version+1}};return json(route,preferences);
      }
      if(path==='/api/v1/social/notification-mail/retry')return json(route,preferenceMode==='retry-uncertain'?{ok:false}:{ok:true});
      if(path==='/api/v1/social/logout')return json(route,logoutMode==='uncertain'?{ok:false}:{ok:true});
      return json(route,{error:{message:'隔离浏览器验收未保存该操作'}},409);
    }
    if(path==='/api/v1/social/contacts'&&contactMode!=='real'){
      if(contactMode==='hold'){releaseContact=()=>json(route,{items:[{id:fixture.peer_id,name:'迟到的联系人',username:'stale',available:true}],page:1,has_more:false});return;}
      return json(route,{items:[{id:fixture.peer_id,name:'当前联系人',username:'current',available:true}],page:1,has_more:false});
    }
    if(path==='/api/v1/social/notice-center'&&noticeMode!=='real'){
      const body={items:[{id:'a101',kind:'publication',title:'当前筛选的通知',url:'/posts/home-browser-0',created_at:'2026-11-26T00:00:00Z',read:false}],unread:1,has_more:false,next_cursor:'-1.-1',through:{message_id:55,activity_id:101}};
      if(noticeMode==='hold'){releaseNotice=()=>json(route,{...body,items:[{...body.items[0],id:'a100',title:'迟到的通知'}]});return;}
      if(noticeMode==='malformed')return json(route,{...body,items:[{...body.items[0],url:'https://outside.invalid'}]});
      return json(route,body);
    }
    if(path==='/api/v1/social/notification-preferences'){
      if(preferences&&preferenceMode!=='real')return json(route,preferences);
      const response=await route.fetch();preferences=await response.json();return json(route,preferences);
    }
    if(path===`/api/v1/social/messages/${fixture.peer_id}`&&overlay.length){
      const response=await route.fetch(),data=await response.json(),before=Number(url.searchParams.get('before')),after=Number(url.searchParams.get('after'));
      const items=[...data.items,...overlay.filter(m=>(!before||m.id<before)&&(!after||m.id>after))].sort((a,b)=>after?a.id-b.id:b.id-a.id);
      data.items=items.slice(0,50);data.has_more=data.has_more||items.length>50;data.next_cursor=data.items.at(-1)?.id||0;return json(route,data);
    }
    const code=assets.get(path.split('/').pop());return code?route.fulfill({contentType:'application/javascript',body:code}):route.continue();
  });
  const check=label=>{evidence.push({browser:name,variant,check:label});console.log(`${name} ${variant}: ${label}`);};
  const until=async predicate=>{const end=Date.now()+12000;while(!predicate()){if(Date.now()>end)throw new Error('Expected request missing');await new Promise(resolve=>setTimeout(resolve,25));}};
  const text=async(selector,value)=>page.waitForFunction(({selector,value})=>document.querySelector(selector)?.textContent.includes(value),{selector,value});
  const open=async path=>{assert.equal((await page.goto(fixture.url+path)).status(),200);await page.waitForLoadState('networkidle');const reject=page.locator('[data-cookie-reject]').filter({visible:true}).first();if(await reject.isVisible())await reject.click();};
  const drawer=async id=>{const triggers=page.locator(`[data-drawer-open="${id}"]`).filter({visible:true});if(await triggers.count()===0)await page.locator('[data-nav-toggle]').click();await triggers.first().click();await page.waitForFunction(id=>document.getElementById(id)?.open,id);};
  const close=async()=>{await page.locator('dialog[open] header button[data-drawer-close]').click();await page.waitForFunction(()=>!document.querySelector('dialog[open]'));};
  try{
    await context.request.post(fixture.url+'/fixture/reload');await context.request.post(fixture.url+'/fixture/style/classic');await context.request.get(fixture.login);
    await open('/account/?view=notifications');await page.waitForFunction(()=>!!window.ChunnelNotificationSettings);
    await page.waitForFunction(()=>!document.querySelector('[data-notification-settings] [type=submit]')?.disabled);
    assert.equal(preferences.preferences.version,0);check('Real Go notification preferences accept version zero and render on the account page.');
    const reply=page.locator('[data-notification-settings] input[name=reply]');await page.locator('.notification-preference:has(input[name=reply])').click();assert.equal(await reply.isChecked(),false);preferenceMode='malformed';
    await page.locator('[data-notification-settings] [type=submit]').click();await text('[data-notification-settings-result]','格式错误');assert.equal(await reply.isChecked(),false);
    preferenceMode='fixture';await page.locator('[data-notification-settings] [type=submit]').click();await text('[data-notification-settings-result]','已保存');
    const saves=writes.filter(w=>w.path.endsWith('/notification-preferences'));assert.equal(saves.length,2);assert.deepEqual(saves[0].body,saves[1].body);
    check('Malformed saves preserve changed preferences and retry with the original version.');

    preferences={...preferences,mail_status:{pending:0,failed:1,uncertain:1}};await page.locator('[data-notification-settings-refresh]').click();await page.locator('[data-notification-mail-retry]').waitFor({state:'visible'});
    preferenceMode='retry-uncertain';await page.locator('[data-notification-mail-retry]').click();await text('[data-notification-settings-result]','尚未确认');assert.equal(await page.locator('[data-notification-mail-retry]').isVisible(),true);
    preferenceMode='fixture';preferences={...preferences,mail_status:{pending:1,failed:0,uncertain:1}};await page.locator('[data-notification-mail-retry]').click();await text('[data-notification-settings-result]','已重新排队');
    assert.equal(await page.locator('[data-notification-uncertain]').isVisible(),true);check('Mail retry requires confirmation and keeps uncertain delivery visible.');

    await drawer('message-drawer');await page.locator(`[data-conversation-list] [data-peer-id="${fixture.peer_id}"]`).click();await text('[data-message-log]','私信验收记录 55');
    assert.equal(await page.locator('[data-message-log] [data-message-id]').count(),50);
    await page.locator('[data-message-history]').click();await text('[data-message-log]','私信验收记录 01');assert.equal(await page.locator('[data-message-log] [data-message-id]').count(),55);
    check('Real Go private messages load fifty recent rows and five older rows without duplication.');
    const input=page.locator('[data-message-input]'),editor=page.locator('[data-emoji-editor="private-message-input"]');await editor.fill('迁移验收消息');await page.locator('.message-composer [type=submit]').click();await text('[data-message-log]','发送失败');
    const firstSend=writes.find(w=>w.path===`/api/v1/social/messages/${fixture.peer_id}`).body;sendMode='confirmed';await page.locator('.message-retry').click();await text('[data-message-log]','已送达');
    assert.deepEqual(writes.filter(w=>w.path===`/api/v1/social/messages/${fixture.peer_id}`)[1].body,firstSend);
    await page.locator('.message-thread-header [aria-label="刷新会话"]').click();await text('[data-message-log]','发送之前未读取的新消息');assert.equal(await page.locator('[data-message-log] [data-message-id]').count(),57);
    check('Uncertain sends preserve nonce and payload; acknowledgements do not skip unseen incoming messages.');
    const count=writes.filter(w=>w.path===`/api/v1/social/messages/${fixture.peer_id}`).length;await editor.fill('输入法中的消息');
    await editor.dispatchEvent('keydown',{key:'Enter',code:'Enter',keyCode:229,isComposing:true});await page.waitForTimeout(100);assert.equal(writes.filter(w=>w.path===`/api/v1/social/messages/${fixture.peer_id}`).length,count);
    await editor.fill('暂存的私信草稿');await close();await drawer('message-drawer');assert.equal(await input.inputValue(),'暂存的私信草稿');
    check('Composition Enter does not send; closing and reopening the drawer preserves the draft.');

    await page.locator('[data-new-conversation]').click();await text('[data-contact-results]','私信验收读者');contactMode='hold';await page.locator('[data-contact-query]').fill('old');await until(()=>releaseContact);
    contactMode='current';await page.locator('[data-contact-query]').fill('current');await text('[data-contact-results]','当前联系人');await releaseContact();releaseContact=null;await page.waitForTimeout(100);
    assert.doesNotMatch(await page.locator('[data-contact-results]').textContent(),/迟到/);await close();check('A delayed contact search cannot overwrite the current query.');

    await drawer('notification-drawer');await page.waitForFunction(()=>document.querySelectorAll('[data-notification-list] [data-notification-id]').length===50);
    await page.locator('[data-notification-more]').click();await page.waitForFunction(()=>document.querySelectorAll('[data-notification-list] [data-notification-id]').length===55);
    check('Real notice pagination retains fifty-five distinct rows and the merged cursor.');
    noticeMode='hold';await page.locator('[data-notification-filter=activity]').click();await until(()=>releaseNotice);
    noticeMode='current';await page.locator('[data-notification-filter=unread]').click();await text('[data-notification-list]','当前筛选');await releaseNotice();releaseNotice=null;await page.waitForTimeout(100);assert.doesNotMatch(await page.locator('[data-notification-list]').textContent(),/迟到/);
    noticeMode='malformed';await page.locator('[data-notification-filter=all]').click();await text('[data-notification-list]','通知加载失败');assert.equal(await page.locator('[data-notification-list] .notification-open').count(),0);await close();
    check('Filter races discard late results; invalid notice links remain an error rather than an action.');

    noticeMode='real';await context.request.post(fixture.url+'/fixture/style/liquid-glass');await page.setViewportSize({width:390,height:844});await open('/account/?view=notifications');
    await drawer('message-drawer');await page.locator(`[data-conversation-list] [data-peer-id="${fixture.peer_id}"]`).click();await page.locator('[data-message-input]').waitFor({state:'visible'});
    const bounds=await page.locator('#message-drawer').boundingBox();assert.ok(bounds.x>=-1&&bounds.x+bounds.width<=391);
    await page.screenshot({path:resolve(directory,`${name}-${variant}-glass-message-mobile.png`)});await close();
    await drawer('notification-drawer');await page.locator('[data-notification-list] .notification-item').first().waitFor();await page.screenshot({path:resolve(directory,`${name}-${variant}-glass-notice-mobile.png`)});await close();
    check('Glass mobile message and notice drawers fit the viewport and retain functional controls.');

    await page.evaluate(()=>window.ChunnelInteractions.run(()=>document.querySelector('[data-account-logout]').click()));await page.waitForTimeout(150);
    assert.equal((await page.evaluate(()=>window.ChunnelInteractions.getSession())).authenticated,true);check('An unconfirmed logout keeps the current session intact.');
    assert.equal(errors.length,0,errors.join('\n'));assert.ok(!scripts.some(p=>/analytics-react|echarts/i.test(p)));
    check('Interaction pages produce no page errors and load no statistics React/ECharts entry.');
    await writeFile(resolve(directory,`interactions-${name}-${variant}.json`),JSON.stringify({evidence:evidence.filter(e=>e.browser===name),errors,writes,reads,scripts},null,2)+'\n');
  }catch(error){await page.screenshot({path:resolve(directory,`${name}-${variant}-failure.png`)}).catch(()=>{});await writeFile(resolve(directory,`interactions-${name}-${variant}-failure.json`),JSON.stringify({error:String(error),errors,writes,reads},null,2)+'\n');throw error;}
  finally{await browser.close();}
}
await writeFile(resolve(directory,`interactions-acceptance-${variant}.json`),JSON.stringify({evidence},null,2)+'\n');
