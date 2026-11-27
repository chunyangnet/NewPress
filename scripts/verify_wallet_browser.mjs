// Real Go templates and reads; every business mutation is intercepted.
import assert from 'node:assert/strict';
import {readFile, writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {chromium, firefox, webkit} from 'playwright';
import {compileFrontendEntry} from './frontend_entries.mjs';

const directory=resolve(process.argv[2]),fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
assert.equal(fixture.wallet,true,'enable NEWPRESS_WALLET_BROWSER');
const variant=process.argv[4]==='typescript'?'typescript':'generated',engines={chromium,firefox,webkit};
const selected=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines),assets=new Map(),evidence=[];
const entries=['currency-shared','wallet','progression-shared','progression','progression-badges','home-widgets'];
if(variant==='typescript'){
  const code=async name=>(await compileFrontendEntry(name,{minify:false})).code;
  for(const entry of entries)assets.set(`chunnel-${entry}.js`,await code(entry));
  const names=['newpress-client','site-time','motion','chunnel','chunnel-emoji-editor','chunnel-emoji','interactions','engagement','experience','currency-shared','home-widgets'];
  const parts=await Promise.all(names.map(name=>name.startsWith('chunnel')?readFile(resolve('themes/chunnel/assets/js',name+'.js'),'utf8'):code(name)));
  assets.set('chunnel-home-features.js','if(!window.ChunnelHomeFeaturesReady){window.ChunnelHome?.stop();\n'+parts.join('\n')+'\nwindow.ChunnelHomeFeaturesReady=true;}');
}
for(const name of selected){
  const browser=await engines[name].launch(),context=await browser.newContext({viewport:{width:1280,height:900},reducedMotion:'reduce'}),page=await context.newPage();
  page.setDefaultTimeout(12000);
  const errors=[],errorDetails=[],requestFailures=[],writes=[],scripts=[];let phase='account',transferMode='invalid',purchaseMode='invalid',rewardMode='invalid',walletMode='real',progressionMode='real',homeSource='progression';
  let heldTransfer,heldPage,lastOrder,failAfterPurchase=false,failAfterReward=false,homeReadCount=0;
  const owner=String(fixture.owner_id),recipient=String(fixture.wallet_recipient_id);
  const json=(route,data,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
  const currencyEntry=(patch={})=>({id:'99001',delta:'1.00',balance:'35.00',kind:'reward_check_in',label:'签到奖励',incoming:true,created_at:'2026-11-26T00:00:00Z',...patch});
  const progressionReceipt=()=>({replayed:true,entry:{id:'99002',user_id:owner,actor_id:'0',kind:'reward_check_in',delta:'10',balance:'185',reason:'签到奖励',created_at:'2026-11-26T00:00:00Z'}});
  const order=body=>({user_id:owner,request_id:body.request_id,status:'completed',version:body.version,member_version:body.member_version,plan:body.plan,mode:body.mode,plan_name:'标准会员',days:30,currency:'main',symbol:'积分',amount:'1.25'});
  page.on('pageerror',error=>{errors.push(error.message);errorDetails.push({message:error.message,stack:error.stack,url:page.url(),phase,lastCheck:evidence.at(-1)?.check});});page.on('request',request=>{if(request.resourceType()==='script')scripts.push(new URL(request.url()).pathname);});
  page.on('requestfailed',request=>requestFailures.push({url:request.url(),failure:request.failure(),phase}));
  await page.route('**/*',async route=>{
    const request=route.request(),url=new URL(request.url()),path=url.pathname;
    if(url.origin!==fixture.url)return route.abort();
    if(!['GET','HEAD'].includes(request.method())){
      let body;try{body=request.postDataJSON();}catch{body=request.postData();}
      writes.push({path,method:request.method(),body});
      if(path.endsWith('/transfer')){
        const value={currency_key:'main',recipient_id:recipient,replayed:true,entry:currencyEntry({delta:'-'+body.amount,kind:'transfer_out',label:'转出',incoming:false})};
        if(transferMode==='hold'){heldTransfer=()=>json(route,value);return;}
        return json(route,transferMode==='invalid'?{...value,entry:{...value.entry,incoming:true}}:value);
      }
      if(path==='/api/v1/social/progression/purchase'){
        lastOrder=order(body);if(purchaseMode==='confirmed'&&failAfterPurchase)progressionMode='fail';
        return json(route,purchaseMode==='invalid'?{...lastOrder,plan:'wrong-plan'}:lastOrder);
      }
      if(path==='/api/v1/social/progression/purchase/cancel')return json(route,{...lastOrder,status:'cancelled'});
      if(path.endsWith('/check-in')){
        const value=path.includes('/progression/')?progressionReceipt():{currency_key:'main',replayed:true,entry:currencyEntry()};
        if(rewardMode==='invalid')delete value.replayed;
        else if(failAfterReward){walletMode='fail';progressionMode='fail';}
        return json(route,value);
      }
      if(path.endsWith('/sync'))return json(route,path.includes('/wallet')?{currency_key:'main',awarded:false}:{ok:true});
      return json(route,{error:{code:'fixture.intercepted',message:'浏览器验收未保存此操作'}},409);
    }
    const asset=assets.get(path.split('/').pop());if(asset)return route.fulfill({contentType:'application/javascript',body:asset});
    if(path==='/'&&homeSource==='wallet'){
      const response=await route.fetch(),html=(await response.text()).replace('data-checkin-source="progression"','data-checkin-source="wallet"');
      return route.fulfill({response,body:html});
    }
    if(/^\/api\/v1\/social\/wallets?\/(main)?$/.test(path)||path==='/api/v1/social/wallet'){
      homeReadCount++;
      if(walletMode==='fail')return json(route,{error:{message:'钱包刷新暂不可用'}},503);
      if(url.searchParams.has('before')&&walletMode==='hold-page'){heldPage=async()=>route.fulfill({response:await route.fetch()});return;}
    }
    if(path==='/api/v1/social/progression'&&progressionMode==='fail')return json(route,{error:{message:'成长刷新暂不可用'}},503);
    return route.continue();
  });
  const check=label=>{evidence.push({browser:name,variant,check:label});console.log(`${name} ${variant}: ${label}`);};
  const text=(selector,value)=>page.waitForFunction(({selector,value})=>document.querySelector(selector)?.textContent.includes(value),{selector,value});
  const until=async fn=>{const deadline=Date.now()+12000;while(!fn()){if(Date.now()>deadline)throw new Error('Expected intercepted request missing');await new Promise(resolve=>setTimeout(resolve,20));}};
  const open=async path=>{await page.goto(fixture.url+path);await page.waitForLoadState('networkidle');};
  const tab=async kind=>{await page.locator(`[data-library-tab=${kind}]`).click();await page.locator(kind==='wallet'?'[data-currency-wallet]':'[data-progression-panel]').waitFor();};
  const dispatchOther=async()=>page.evaluate(async id=>{const session=await window.ChunnelInteractions.getSession();document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{...session,id}}));return {balance:document.querySelector('[data-wallet-balance]').textContent,result:document.querySelector('[data-wallet-transfer-result]').textContent,roomHidden:document.querySelector('[data-account-room]').hidden};},fixture.owner_id+100);
  try{
    await page.goto(fixture.login);await open('/account/');await tab('wallet');await text('[data-wallet-balance]','35.00');
    assert.equal(await page.locator('[data-wallet-history] tbody tr').count(),30);
    walletMode='hold-page';await page.locator('[data-wallet-more]').click();await until(()=>heldPage);assert.equal(await page.locator('[data-wallet-more]').isDisabled(),true);
    await heldPage();heldPage=null;await page.waitForFunction(()=>document.querySelectorAll('[data-wallet-history] tbody tr').length===35);walletMode='real';
    assert.equal(await page.locator('[data-wallet-more]').isVisible(),false);check('Real wallet DTO renders balances and all 35 ledger rows with guarded pagination.');

    await page.locator('[data-wallet-transfer] [name=recipient]').fill(fixture.wallet_recipient);await page.locator('[data-wallet-transfer] [name=amount]').fill('1.25');
    const beforeTransfer=writes.filter(w=>w.path.endsWith('/transfer')).length;await page.locator('[data-wallet-transfer-submit]').click();await page.locator('[data-wallet-confirm]').waitFor();
    assert.equal(writes.filter(w=>w.path.endsWith('/transfer')).length,beforeTransfer);await page.locator('[data-wallet-confirm-send]').click();await text('[data-wallet-confirm-status]','重试原转账');
    const original=writes.filter(w=>w.path.endsWith('/transfer')).at(-1);assert.equal(await page.locator('[data-wallet-transfer] [name=amount]').getAttribute('readonly'),'');
    transferMode='confirmed';await page.locator('[data-wallet-confirm-send]').click();await text('[data-wallet-transfer-result]','已确认');assert.deepEqual(writes.filter(w=>w.path.endsWith('/transfer')).at(-1).body,original.body);
    assert.equal(await page.evaluate(owner=>sessionStorage.getItem(`chunnel.currency.v2.main.transfer.${owner}`),owner),null);check('Transfer requires confirmation; a mismatched receipt retains the frozen request and explicit retry reuses it.');

    rewardMode='invalid';await page.locator('[data-wallet-check-in]').click();await text('[data-wallet-message]','未确认');
    const reward=writes.filter(w=>w.path.endsWith('/check-in')).at(-1);rewardMode='confirmed';await page.locator('[data-wallet-check-in]').click();await text('[data-wallet-message]','本轮签到已领取');
    assert.deepEqual(writes.filter(w=>w.path.endsWith('/check-in')).at(-1).body,reward.body);check('Wallet check-in rejects incomplete acknowledgments and confirms only the original retry.');

    transferMode='hold';await page.locator('[data-wallet-transfer] [name=recipient]').fill(fixture.wallet_recipient);await page.locator('[data-wallet-transfer] [name=amount]').fill('1.25');
    await page.locator('[data-wallet-transfer-submit]').click();await page.locator('[data-wallet-confirm-send]').click();await until(()=>heldTransfer);
    const reloaded=page.waitForEvent('load'),cleared=await dispatchOther();await heldTransfer().catch(()=>{});heldTransfer=null;await reloaded;await page.waitForLoadState('networkidle');
    assert.equal(cleared.balance,'—');assert.equal(cleared.result,'');assert.equal(cleared.roomHidden,true);
    assert.ok(await page.evaluate(owner=>sessionStorage.getItem(`chunnel.currency.v2.main.transfer.${owner}`),owner));check('Account change clears the private wallet before account reload; late receipts preserve the old owner request.');
    await page.evaluate(owner=>sessionStorage.removeItem(`chunnel.currency.v2.main.transfer.${owner}`),owner);

    await open('/account/');await tab('progression');await page.locator('[data-progression-buy]').first().waitFor();
    await text('[data-progression-summary]','175');assert.equal(await page.locator('[data-progression-history] tbody tr').count(),30);
    await page.locator('[data-progression-more]').click();await page.waitForFunction(()=>document.querySelectorAll('[data-progression-history] tbody tr').length===35);
    check('Actual progression DTO renders level, membership choices and both history pages.');
    const beforePurchase=writes.filter(w=>w.path.endsWith('/purchase')).length;await page.locator('[data-progression-buy]').first().click();await page.locator('[data-progression-dialog]').waitFor();
    assert.equal(writes.filter(w=>w.path.endsWith('/purchase')).length,beforePurchase);await page.locator('[data-progression-confirm-submit]').click();await text('[data-progression-confirm-status]','未确认');
    const purchase=writes.filter(w=>w.path.endsWith('/purchase')).at(-1);purchaseMode='confirmed';failAfterPurchase=true;
    await page.locator('[data-progression-confirm-submit]').click();await text('[data-progression-status]','最新状态暂未载入');
    assert.deepEqual(writes.filter(w=>w.path.endsWith('/purchase')).at(-1).body,purchase.body);assert.equal(await page.locator('[data-progression-buy]').count(),0);
    assert.equal(await page.locator('[data-progression-pending]').isVisible(),false);assert.equal(await page.evaluate(owner=>sessionStorage.getItem(`chunnel.progression.v1.purchase.${owner}`),owner),null);
    check('Membership payment preserves a mismatched request; confirmed retry stays successful and removes stale purchase controls if refresh fails.');

    progressionMode='real';failAfterPurchase=false;await page.locator('[data-progression-refresh]').click();await page.locator('[data-progression-buy]').first().waitFor();
    const pending={kind:'purchase',owner,payload:{version:lastOrder.version,member_version:lastOrder.member_version,plan:lastOrder.plan,mode:lastOrder.mode,request_id:'browser-cancel-original-request'},quote:{plan:lastOrder.plan_name,days:30,amount:'1.25',currency:'main',symbol:'积分'}};
    lastOrder={...lastOrder,request_id:pending.payload.request_id};await page.evaluate(value=>sessionStorage.setItem(`chunnel.progression.v1.purchase.${value.owner}`,JSON.stringify(value)),pending);
    await open('/account/');const beforeRestore=writes.length;await tab('progression');await page.locator('[data-progression-pending]').waitFor();
    assert.equal(writes.slice(beforeRestore).filter(w=>w.path.endsWith('/purchase')).length,0);await page.locator('[data-progression-cancel]').click();await text('[data-progression-status]','订单已取消');
    assert.equal(writes.filter(w=>w.path.endsWith('/purchase/cancel')).at(-1).body.request_id,pending.payload.request_id);check('Restored membership orders never auto-charge; cancellation uses the original request and clears it only after acknowledgment.');
    rewardMode='confirmed';failAfterReward=true;await page.locator('[data-progression-check-in]').click();await text('[data-progression-status]','最新状态暂未载入');
    assert.match(await page.locator('[data-progression-status]').textContent(),/这次签到已领取/);assert.equal(await page.locator('[data-progression-buy]').count(),0);check('Confirmed experience reward remains confirmed when its follow-up read fails.');

    walletMode='real';progressionMode='real';failAfterReward=false;await open('/admin/currency/?username=owner#currency-ledger');await page.locator('[data-currency-lookup]').waitFor();
    await page.locator('[data-currency-lookup] [name=username]').fill('owner');await page.locator('[data-currency-lookup] button').click();await page.waitForLoadState('networkidle');
    await open('/admin/progression/#progression-accounts');await page.locator('[data-progression-lookup] [name=username]').fill('owner');await page.locator('[data-progression-lookup] button').click();
    await text('[data-progression-admin-history]','成长验收流水');check('Legacy currency and progression administration consume the shared TS APIs and real account records.');

    for(const sourceKind of ['progression','wallet']){
      phase=`${sourceKind}-home-open`;
      homeSource=sourceKind;walletMode='real';progressionMode='real';failAfterReward=false;rewardMode='invalid';
      await page.evaluate(()=>{sessionStorage.clear();});await page.goto('about:blank');await open('/');
      phase=`${sourceKind}-home-invalid-reward`;
      assert.equal(await page.evaluate(()=>typeof window.ChunnelHomeFeaturesReady),'undefined');
      if(sourceKind==='wallet')assert.equal(await page.locator('[data-home-checkin-card]').getAttribute('data-checkin-source'),'wallet');
      const before=writes.filter(w=>w.path.endsWith('/check-in')).length;await page.locator('[data-home-checkin]').click();await text('[data-home-checkin-status]','未确认');
      assert.equal(writes.filter(w=>w.path.endsWith('/check-in')).length,before+1);const homeReward=writes.filter(w=>w.path.endsWith('/check-in')).at(-1);
      phase=`${sourceKind}-home-confirmed-reward`;failAfterReward=true;rewardMode='confirmed';await page.locator('[data-home-checkin]').click();await text('[data-home-checkin-label]','本轮已领取');await text('[data-home-checkin-meta]','暂未更新');
      // The confirmed reward also refreshes the separate member card. Finish that
      // read before the next scenario changes response modes or replaces this page.
      await page.waitForLoadState('networkidle');
      assert.deepEqual(writes.filter(w=>w.path.endsWith('/check-in')).at(-1).body,homeReward.body);assert.equal(await page.locator('[data-home-checkin]').isDisabled(),true);
      check(`Homepage ${sourceKind} reward loads on use, retries the original nonce and keeps confirmed success after refresh failure.`);
    }
    homeSource='progression';walletMode='real';progressionMode='real';failAfterReward=false;
    await context.request.post(fixture.url+'/fixture/style/liquid-glass');await open('/account/');await page.emulateMedia({colorScheme:'dark'});await tab('wallet');await text('[data-wallet-balance]','35.00');
    await page.setViewportSize({width:390,height:844});await page.screenshot({path:resolve(directory,`${name}-${variant}-wallet-mobile.png`),fullPage:true});
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+2));
    await tab('progression');await page.locator('[data-progression-buy]').first().waitFor();await page.screenshot({path:resolve(directory,`${name}-${variant}-progression-mobile.png`),fullPage:true});
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+2));check('Wallet and progression remain usable in dark glass mobile layout without horizontal page overflow.');
    assert.deepEqual(errors,[]);assert.ok(!scripts.some(path=>/analytics-react|echarts|react-dom/.test(path)));
    await writeFile(resolve(directory,`${name}-${variant}-wallet.json`),JSON.stringify({browser:name,variant,checks:evidence.filter(item=>item.browser===name),errors,writes,scripts,homeReadCount},null,2));
  }finally{
    const controls=await page.evaluate(()=>Object.fromEntries(['data-home-checkin-label','data-home-checkin-status','data-home-checkin-meta','data-home-checkin','data-progression-admin-status'].map(key=>{const node=document.querySelector(`[${key}]`);return[key,node?{text:node.textContent,disabled:node.disabled,hidden:node.hidden}:null];}))).catch(()=>null);
    await writeFile(resolve(directory,`${name}-${variant}-wallet-last.json`),JSON.stringify({url:page.url(),controls,errors,errorDetails,requestFailures,writes,checks:evidence.filter(item=>item.browser===name)},null,2));
    await page.screenshot({path:resolve(directory,`${name}-${variant}-wallet-last.png`),fullPage:true}).catch(()=>{});
    await context.request.post(fixture.url+'/fixture/style/classic').catch(()=>{});await browser.close();
  }
}
console.log(`${evidence.length} wallet browser checks passed (${variant}).`);
