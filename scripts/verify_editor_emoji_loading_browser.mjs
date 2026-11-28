// Explicit source emoji loading on real, isolated Go editor pages.
import assert from 'node:assert/strict';
import {readFile, writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {createHash} from 'node:crypto';
import {chromium, firefox, webkit} from 'playwright';
import {compileFrontendEntry} from './frontend_entries.mjs';

const directory=resolve(process.argv[2]), variant=process.argv[3]||'generated';
const fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const engines={chromium,firefox,webkit}, selected=process.argv[4]?[process.argv[4]]:Object.keys(engines);
const scripts=new Map(), evidence=[];
if(variant==='typescript')for(const name of ['editor-page','editor-emoji-picker'])scripts.set(`newpress-${name}.js`,(await compileFrontendEntry(name,{minify:false})).code);
for(const name of selected){
 const browser=await engines[name].launch({headless:true});
 const context=await browser.newContext({viewport:{width:1280,height:900},reducedMotion:'reduce'});
 const page=await context.newPage();page.setDefaultTimeout(12000);
 const errors=[],writes=[],intercepted=[],downloads=[],assets=[];
 let mode='',held=null;
 const check=check=>{evidence.push({browser:name,check});console.log(`${name}: ${check}`);};
 page.on('pageerror',error=>errors.push(error.message));page.on('dialog',dialog=>dialog.accept());
 page.on('request',request=>{if(request.resourceType()==='script')downloads.push(new URL(request.url()).pathname.split('/').pop());if(!['GET','HEAD'].includes(request.method()))writes.push({method:request.method(),url:request.url()});});
 await page.addInitScript(()=>{
  const timeout=window.setTimeout.bind(window);
  window.setTimeout=(callback,delay,...args)=>timeout(callback,window.__shortEmojiTimeout&&delay===15000?150:delay,...args);
  let runtime;window.__emojiMounts=0;
  Object.defineProperty(window,'ChunnelEmoji',{configurable:true,get(){return runtime;},set(value){runtime=value;window.__emojiMounts++;}});
 });
 await context.route('**/*',async route=>{
  const request=route.request(),url=new URL(request.url()),file=url.pathname.split('/').pop();
  if(url.origin!==fixture.base)return route.abort('blockedbyclient');
  if(!['GET','HEAD'].includes(request.method())){intercepted.push({method:request.method(),url:request.url()});return route.fulfill({status:204});}
  if(file==='newpress-editor-emoji-picker.js'){
   if(mode==='fail')return route.abort('failed');
   if(mode==='empty')return route.fulfill({contentType:'text/javascript',body:'/* unavailable initialization */'});
   if(mode==='hold'){held=route;return;}
  }
  if(scripts.has(file))return route.fulfill({contentType:'text/javascript',body:scripts.get(file)});
  if(url.pathname==='/api/v1/admin/updates')return route.fulfill({status:503,contentType:'application/json',body:'{"error":{"message":"isolated fixture"}}'});
  return route.continue();
 });
 const source=page.locator('[data-content-source]'),launcher=page.locator('[data-emoji-trigger][data-emoji-target="content-source"]');
 const picker=page.locator('[data-emoji-popover]'),status=page.locator('[data-editor-emoji-status]');
 const original='<p>甲乙丙丁</p>';
 const reset=()=>page.evaluate(value=>window.ChunnelBlockEditor.reset(value),original);
 const select=async(start=3,end=5)=>{await source.focus();await source.evaluate((input,range)=>{input.setSelectionRange(...range);input.dispatchEvent(new Event('select'));},[start,end]);};
 const choose=async()=>{
  await picker.waitFor({state:'visible'});await picker.locator('[data-emoji-token="[得意]"]').first().click();
  await picker.waitFor({state:'hidden'});
  assert.equal(await launcher.getAttribute('aria-expanded'),'false');
  assert.equal(await source.evaluate(node=>node===document.activeElement),true);
 };
 const close=()=>picker.locator('button[aria-label="关闭表情面板"]').click();
 const fresh=async()=>{
  mode='';await page.goto(fixture.base+fixture.paths.editor,{waitUntil:'load'});await page.locator('.ProseMirror').waitFor();
  await page.locator('.document-block-menu > summary').click();await page.locator('[data-composer-mode="source"]').click();
  await reset();await select();
 };
 const waitHeld=async()=>{const until=Date.now()+12000;while(!held){if(Date.now()>until)throw Error('Emoji request was not held');await new Promise(resolve=>setTimeout(resolve,20));}};
 const release=async()=>{const route=held;held=null;assert.ok(route);if(scripts.has('newpress-editor-emoji-picker.js'))await route.fulfill({contentType:'text/javascript',body:scripts.get('newpress-editor-emoji-picker.js')});else await route.continue();};
 try{
  await page.goto(fixture.base+fixture.login);downloads.length=0;await fresh();
  assert.equal(downloads.includes('newpress-editor-emoji-picker.js'),false);assert.equal(await page.evaluate(()=>window.__emojiMounts),0);
  await page.locator('[data-source-format="strong"]').click();
  assert.equal(await source.inputValue(),'<p><strong>甲乙</strong>丙丁</p>');
  assert.equal(downloads.includes('newpress-editor-emoji-picker.js'),false);
  await page.locator('[data-composer-undo]').click();assert.equal(await source.inputValue(),original);
  check('Source formatting and undo work immediately without downloading the emoji picker.');

  await select();mode='fail';await launcher.click();await page.waitForFunction(()=>document.querySelector('[data-editor-emoji-status]').textContent.includes('加载失败'));
  assert.equal(await source.inputValue(),original);assert.equal(await launcher.getAttribute('aria-busy'),null);
  mode='empty';await launcher.click();await page.waitForFunction(()=>document.querySelector('[data-editor-emoji-status]').textContent.includes('初始化失败'));
  assert.equal(await picker.count(),0);
  mode='';await launcher.click();await choose();assert.equal(await source.inputValue(),'<p>[得意]丙丁</p>');
  await page.locator('[data-composer-undo]').click();assert.equal(await source.inputValue(),original);
  check('Failed downloads and empty initialization retry; the first successful activation replaces the captured range and undo restores it.');

  await select(5,7);await launcher.click();await choose();assert.equal(await source.inputValue(),'<p>甲乙[得意]</p>');
  await page.locator('[data-composer-undo]').click();assert.equal(await source.inputValue(),original);
  assert.equal(await page.evaluate(()=>window.__emojiMounts),1);assert.equal(await picker.count(),1);
  check('Reopening uses the new textarea selection while reusing one picker instance.');

  for(const action of ['escape','body','body-return','selection','mode','mode-return','disabled','disabled-return','fieldset','dialog','pagehide','outside']){
   await fresh();mode='hold';await launcher.click();await waitHeld();
   await page.evaluate(action=>{
    const input=document.querySelector('[data-content-source]');
    if(action==='escape')document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));
    if(action==='body'){input.value='<p>下载期间的新正文</p>';input.dispatchEvent(new Event('input',{bubbles:true}));}
    if(action==='body-return'){const original=input.value;input.value='changed';input.dispatchEvent(new Event('input',{bubbles:true}));input.value=original;input.dispatchEvent(new Event('input',{bubbles:true}));input.setSelectionRange(3,5);}
    if(action==='selection')input.setSelectionRange(5,7);
    if(action==='mode')window.ChunnelBlockEditor.setMode('blocks');
    if(action==='mode-return'){window.ChunnelBlockEditor.setMode('blocks');window.ChunnelBlockEditor.setMode('source');}
    if(action==='disabled')input.readOnly=true;
    if(action==='disabled-return'){input.readOnly=true;input.readOnly=false;}
    if(action==='fieldset'){const fieldset=document.createElement('fieldset');fieldset.id='emoji-fieldset';input.before(fieldset);fieldset.append(input);fieldset.disabled=true;}
    if(action==='dialog'){const dialog=document.createElement('dialog');dialog.id='emoji-other-dialog';document.body.append(dialog);dialog.showModal();}
    if(action==='pagehide')window.dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true}));
    if(action==='outside')document.body.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true}));
   },action);
   const unchanged=await source.inputValue();await release();await page.waitForFunction(()=>!!window.ChunnelEmoji);
   assert.equal(await picker.isVisible(),false);assert.equal(await source.inputValue(),unchanged);
   await page.evaluate(()=>{
    const input=document.querySelector('[data-content-source]'),fieldset=document.querySelector('#emoji-fieldset');
    input.readOnly=false;if(fieldset)fieldset.replaceWith(input);document.querySelector('#emoji-other-dialog')?.remove();
    window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));window.ChunnelBlockEditor.setMode('source');
   });
   mode='';await launcher.click();await picker.waitFor({state:'visible'});await close();assert.equal(await page.evaluate(()=>window.__emojiMounts),1);
   check(`A delayed picker cannot reopen after ${action}; a new explicit click reuses the completed runtime.`);
  }

  await fresh();mode='hold';await page.evaluate(()=>window.__shortEmojiTimeout=true);await launcher.click();await waitHeld();
  await page.waitForFunction(()=>document.querySelector('[data-editor-emoji-status]').textContent.includes('加载失败'));
  mode='';await page.evaluate(()=>window.__shortEmojiTimeout=false);await launcher.click();await picker.waitFor({state:'visible'});await close();await release();
  // Network-idle observes the released old response, including script evaluation.
  await page.waitForLoadState('networkidle');assert.equal(await page.evaluate(()=>window.__emojiMounts),1);assert.equal(await picker.count(),1);
  await select();await launcher.click();await choose();assert.equal(await source.inputValue(),'<p>[得意]丙丁</p>');
  check('A retry and a late timed-out download mount one runtime and insert one token.');

  await fresh();await source.evaluate(input=>input.readOnly=true);const before=downloads.length;await launcher.click();
  assert.equal(downloads.length,before);assert.equal(await source.inputValue(),original);
  await source.evaluate(input=>input.readOnly=false);await select();await launcher.focus();await page.keyboard.press('Enter');
  await picker.waitFor({state:'visible'});assert.equal(await picker.locator('input[type="search"]').evaluate(node=>node===document.activeElement),true);await close();
  check('Read-only source cannot start loading; keyboard activation focuses the picker search.');
  for(const width of [1280,390]){
   await page.setViewportSize({width,height:900});await launcher.click();await picker.waitFor({state:'visible'});
   // Inline coordinates update before the browser finishes its layout transition.
   await page.waitForFunction(()=>{const r=document.querySelector('[data-emoji-popover]').getBoundingClientRect();return r.left>=-1&&r.right<=innerWidth+1&&r.top>=-1&&r.bottom<=innerHeight+1;});
   const rect=await picker.evaluate(node=>{const r=node.getBoundingClientRect();return{left:r.left,right:r.right,top:r.top,bottom:r.bottom,width:innerWidth,height:innerHeight};});
   assert.ok(rect.left>=-1&&rect.right<=rect.width+1&&rect.top>=-1&&rect.bottom<=rect.height+1,JSON.stringify(rect));
   await page.screenshot({path:resolve(directory,`${variant}-${name}-emoji-${width}.png`)});await close();
  }
  if(variant==='generated'){
   const url=await page.locator('script[data-editor-emoji-script]').getAttribute('data-editor-emoji-script');
   const response=await context.request.get(new URL(url,fixture.base).href),bytes=await response.body();
   assert.equal(response.status(),200);assert.deepEqual(bytes,await readFile('internal/coreui/assets/js/newpress-editor-emoji-picker.js'));
   assert.ok(response.headers().etag);assert.match(response.headers()['cache-control'],/immutable/);
   assets.push({bytes:bytes.length,sha256:createHash('sha256').update(bytes).digest('hex'),etag:response.headers().etag});
  }
  assert.deepEqual(errors,[]);assert.deepEqual(writes,intercepted);
  check('Desktop/mobile layouts and resource identity pass with no browser exceptions or unintercepted writes.');
 }catch(error){
  console.log('FAILURE STATE',JSON.stringify(await page.evaluate(()=>({status:document.querySelector('[data-editor-emoji-status]')?.textContent,busy:document.querySelector('[data-emoji-trigger]')?.getAttribute('aria-busy'),mounts:window.__emojiMounts,mode:document.querySelector('[data-content-editor]')?.dataset.editorMode,scripts:[...document.scripts].map(node=>node.src).filter(src=>src.includes('emoji')),value:document.querySelector('[data-content-source]')?.value}))));
  await page.screenshot({path:resolve(directory,`${variant}-${name}-emoji-failure.png`)}).catch(()=>{});throw error;
 }finally{
  if(held)await held.abort().catch(()=>{});
  await writeFile(resolve(directory,`${variant}-${name}-emoji-loading.json`),JSON.stringify({evidence:evidence.filter(e=>e.browser===name),errors,writes,intercepted,downloads,assets},null,2)+'\n');
  await context.close();await browser.close();
 }
}
await writeFile(resolve(directory,`${variant}-emoji-loading-acceptance.json`),JSON.stringify({evidence},null,2)+'\n');
