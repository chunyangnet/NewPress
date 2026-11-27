// Isolated Go fixture only. Every business mutation is intercepted.
import assert from 'node:assert/strict';
import {readFile,writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {chromium,firefox,webkit} from 'playwright';
import {compileFrontendEntry} from './frontend_entries.mjs';

const directory=resolve(process.argv[2]);
const fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
assert.equal(fixture.experience,true);
const engines={chromium,firefox,webkit};
const selected=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines);
const variant=process.argv[4]==='typescript'?'typescript':'generated';
const code=variant==='typescript'?(await compileFrontendEntry('explore',{minify:false})).code:null;
const evidence=[];
for(const name of selected){
 const browser=await engines[name].launch();
 const context=await browser.newContext({viewport:{width:1280,height:900},reducedMotion:'reduce'});
 const page=await context.newPage();page.setDefaultTimeout(12000);
 const errors=[],writes=[],scripts=[];
 let blockedEntry=false;
 page.on('pageerror',error=>errors.push(error.message));
 page.on('request',request=>{if(request.resourceType()==='script')scripts.push(new URL(request.url()).pathname);});
 const check=message=>{evidence.push({browser:name,check:message});console.log(name+': '+message);};
 await page.route('**/*',async route=>{
  const request=route.request(),url=new URL(request.url());
  if(url.origin!==fixture.url)return route.abort();
  if(!['GET','HEAD'].includes(request.method())){writes.push({path:url.pathname,method:request.method()});return route.fulfill({status:503,contentType:'application/json',body:'{"error":"browser fixture blocks writes"}'});}
  if(url.pathname.endsWith('/chunnel-explore.js')){
   if(blockedEntry)return route.abort();
   if(code)return route.fulfill({contentType:'application/javascript',body:code});
  }
  return route.continue();
 });
 try{
  await context.request.post(fixture.url+'/fixture/reload');
  await context.request.post(fixture.url+'/fixture/style/classic');
  await page.goto(fixture.url+'/explore/?sort=updated');
  await page.waitForFunction(()=>!!window.ChunnelInteractions&&!!window.ChunnelEmoji);
  await page.waitForFunction(()=>document.querySelector('[data-favorite]')?.getAttribute('aria-pressed')!==null);
  const routeScripts=()=>scripts.filter(path=>/chunnel-(explore|experience)\.js$/.test(path));
  assert.equal(routeScripts().length,1);assert.ok(routeScripts()[0].endsWith('/chunnel-explore.js'));
  check('Discovery loads one dedicated entry and no full comment/community experience entry.');

  const favorite=page.locator('[data-discovery-results] [data-favorite]').first();
  await favorite.click();
  await page.waitForFunction(()=>JSON.parse(localStorage.getItem('chunnel.preferences')||'null')?.saved.length===1);
  const saved=await page.evaluate(()=>JSON.parse(localStorage.getItem('chunnel.preferences')).saved[0]);
  assert.ok(saved.url.startsWith('/posts/'));
  await page.locator('[data-saved-open]').filter({visible:true}).first().click();
  await page.locator('#saved-drawer[open] .saved-item').waitFor();
  await page.locator('.saved-item > button').first().click();await page.locator('[data-saved-restore]').click();
  assert.equal(await page.locator('#saved-drawer .saved-item').count(),1);
  await page.locator('#saved-drawer button[data-drawer-close]').click();
  check('Guest bookmark, saved drawer and undo operate from the dedicated entry.');

  const other=await context.newPage();await other.goto(fixture.url+'/explore/');
  await other.evaluate(()=>localStorage.setItem('chunnel.preferences',JSON.stringify({saved:[],following:[],following_ids:[],likes:[]})));
  await page.waitForFunction(()=>!document.querySelector('[data-discovery-results] [data-favorite]')?.classList.contains('is-favorite'));
  await other.close();check('A second tab preference update refreshes bookmark state.');

  const input=page.locator('#explore-query');
  await page.evaluate(()=>{window.__exploreSameDocument=true;});
  await input.fill('首页');await input.press('Enter');
  await page.waitForURL(url=>url.searchParams.get('q')==='首页');
  await page.waitForFunction(()=>!document.querySelector('[data-discovery]')?.classList.contains('is-loading'));
  assert.equal(await page.evaluate(()=>window.__exploreSameDocument),true);
  const listed=await page.locator('[data-discovery-results]').innerText();assert.ok(listed.includes('首页'));
  await page.goBack();await page.waitForURL(url=>!url.searchParams.has('q'));
  await page.waitForFunction(()=>!document.querySelector('[data-discovery]')?.classList.contains('is-loading'));
  assert.equal(await input.inputValue(),'');
  check('Keyboard search and browser history update results without replacing the document.');

  await page.locator('[data-view-toggle=list]').click();
  assert.equal(new URL(page.url()).searchParams.get('view'),'list');
  const select=page.locator('[data-content-sort]');
  await select.selectOption('title',{force:true});
  await page.waitForURL(url=>url.searchParams.get('sort')==='title');
  await page.waitForFunction(()=>!document.querySelector('[data-discovery]')?.classList.contains('is-loading'));
  assert.equal(new URL(page.url()).searchParams.get('view'),'list');
  assert.ok(!scripts.some(path=>path.endsWith('/chunnel-experience.js')));
  check('View and sort survive partial navigation without downloading the full entry.');

  for(const style of ['classic','liquid-glass']){
   await context.request.post(fixture.url+'/fixture/style/'+style);
   await page.setViewportSize({width:390,height:844});
   await page.goto(fixture.url+'/explore/?sort=updated');
   for(const dark of [false,true]){
    const button=page.getByRole('button',{name:dark?'深色模式':'浅色模式',exact:true});
    if(await button.isVisible())await button.click();
    await page.evaluate(()=>scrollTo(0,0));
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
    await page.screenshot({path:resolve(directory,`${name}-${variant}-explore-${style}-${dark?'dark':'light'}.png`),fullPage:true});
   }
  }
  check('Classic and glass discovery layouts fit mobile light and dark views.');

  blockedEntry=true;await page.goto(fixture.url+'/explore/?sort=updated');
  await input.fill('首页');await input.press('Enter');
  await page.waitForURL(url=>url.searchParams.get('q')==='首页');
  assert.ok((await page.locator('[data-discovery-results]').innerText()).includes('首页'));
  check('A failed dedicated script still allows native search submission and results.');
  blockedEntry=false;
  const native=await browser.newContext({javaScriptEnabled:false,viewport:{width:390,height:844}});
  const nativePage=await native.newPage();await nativePage.goto(fixture.url+'/explore/?sort=updated');
  await nativePage.locator('#explore-query').fill('首页');await nativePage.locator('#explore-query').press('Enter');
  await nativePage.waitForURL(url=>url.searchParams.get('q')==='首页');
  assert.ok((await nativePage.locator('[data-discovery-results]').innerText()).includes('首页'));
  await native.close();check('Native discovery search also works with JavaScript disabled.');
  assert.deepEqual(errors,[]);assert.deepEqual(writes,[]);
  await writeFile(resolve(directory,`explore-${name}-${variant}.json`),JSON.stringify({evidence:evidence.filter(e=>e.browser===name),errors,writes,scripts},null,2)+'\n');
 }catch(error){await page.screenshot({path:resolve(directory,`${name}-${variant}-explore-failure.png`)}).catch(()=>{});await writeFile(resolve(directory,`${name}-${variant}-explore-failure.json`),JSON.stringify({error:String(error),errors,writes},null,2)+'\n');throw error;}
 finally{await browser.close();}
}
await writeFile(resolve(directory,`explore-acceptance-${variant}.json`),JSON.stringify({evidence},null,2)+'\n');
