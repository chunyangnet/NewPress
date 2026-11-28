// Isolated real Go pages. Every mutation and cloud request is intercepted.
import assert from 'node:assert/strict';
import {readFile,writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {build} from 'esbuild';
import {chromium,firefox,webkit} from 'playwright';
const directory=resolve(process.argv[2]),fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const variant=process.argv[4]==='generated'?'generated':'typescript',engines={chromium,firefox,webkit};
const names=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines),evidence=[],assets=new Map();
if(variant==='typescript')for(const [entry,file] of [['object-storage','chunnel-object-storage.js'],['content-import','chunnel-import.js']])assets.set(file,(await build({entryPoints:['frontend/core/'+entry+'.ts'],bundle:true,write:false,format:'iife',platform:'browser',target:['chrome100','firefox110','safari16'],charset:'utf8'})).outputFiles[0].text);
const copy=value=>structuredClone(value),delay=ms=>new Promise(resolve=>setTimeout(resolve,ms)),base='/api/v1/admin/plugins/object-storage';
for(const engine of names){
 const browser=await engines[engine].launch(),context=await browser.newContext({viewport:{width:1440,height:1000},reducedMotion:'reduce'}),page=await context.newPage();page.setDefaultTimeout(10000);
 let mode='',held,heldRead,holdRead=false,state;const writes=[],errors=[],network=[];
 const check=text=>{evidence.push({engine,text});console.log(engine+': '+text);};
 const reset=()=>{mode='';held=undefined;heldRead=undefined;holdRead=false;state=copy(fixture.snapshot);state.records=[copy(fixture.record)];state.counts={orphaned:1};state.profile_counts={archive:{orphaned:1}};state.total_bytes=128;};reset();
 const json=(route,value,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(value)});
 const wait=async predicate=>{const end=Date.now()+10000;while(!predicate()){if(Date.now()>end)throw Error('Missing request');await delay(20);}};
 const release=async()=>{await wait(()=>held);const fn=held;held=undefined;await fn().catch(()=>{});};
 const has=(selector,text)=>page.waitForFunction(({selector,text})=>document.querySelector(selector)?.textContent?.includes(text),{selector,text});
 const value=(selector,text)=>page.waitForFunction(({selector,text})=>document.querySelector(selector)?.value===text,{selector,text});
 const set=(selector,text)=>page.locator(selector).evaluate((n,text)=>{n.value=text;n.dispatchEvent(new Event('input',{bubbles:true}));},text);
 const submit=selector=>page.locator(selector).evaluate(form=>form.requestSubmit());
 const ready=()=>value('[data-profile-field="name"]','媒体归档');
 const fresh=async()=>{await page.waitForLoadState('networkidle');reset();await page.goto(fixture.url+'/admin/plugins/object-storage/');await ready();};
 const show=async selector=>{const id=await page.locator(selector).evaluate(n=>n.closest('[data-settings-section]')?.id);if(id)await page.locator('[data-settings-target="'+id+'"]').click();};
 const click=async selector=>{await show(selector);await page.locator(selector).click();};
 const close=(selector,accepted)=>page.locator(selector).evaluate((dialog,accepted)=>{dialog.returnValue=accepted?'confirm':'cancel';dialog.close();},accepted);
 const refreshed=()=>page.locator('[data-storage-refresh]').evaluate(n=>n.click());
 const history=async()=>{await page.evaluate(()=>{window.dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true}));window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));});};
 const setFiles=()=>page.locator('[name=files]').setInputFiles(fixture.input.files.map(file=>({name:file.name,mimeType:'text/markdown',buffer:Buffer.from(file.text)})));
 const importFresh=async()=>{await page.waitForLoadState('networkidle');reset();await page.goto(fixture.url+'/admin/import/');await page.locator('[name=source]').fill(fixture.input.source);await setFiles();};
 const preview=async()=>{await submit('[data-import-form]');await has('[data-import-status]','预览已生成');};
 const previewData=body=>{const result=copy(fixture.preview);result.plan.source=body.input.source;result.expires=new Date(Date.now()+600000).toISOString();return result;};
 const save=body=>{const result=copy(body.settings);result.version++;result.profiles.forEach(p=>{p.name=p.name.trim();p.endpoint=p.endpoint.trim().replace(/\/+$/,'');p.public_url=p.public_url.trim().replace(/\/+$/,'');p.region=p.region.trim();p.bucket=p.bucket.trim();p.prefix=p.prefix.trim().replace(/^\/+|\/+$/g,'');});result.profile_views=result.profiles.map(p=>({...p,secret_key:'',password:'',session_token:'',secret_saved:!['webdav','cloudreve'].includes(p.provider),password_saved:['webdav','cloudreve'].includes(p.provider),token_saved:!body.clear_tokens.includes(p.id)&&!!(p.session_token||state.settings.profile_views.find(x=>x.id===p.id)?.token_saved)}));result.profiles.forEach(p=>{delete p.secret_key;delete p.password;delete p.session_token;});return result;};
 page.on('pageerror',error=>{errors.push(error.message);console.error('PAGE ERROR',error.stack);});
 page.on('dialog',dialog=>dialog.type()==='beforeunload'?dialog.accept():dialog.dismiss());
 await context.route('**/*',async route=>{
  const request=route.request(),url=new URL(request.url()),path=url.pathname,file=path.split('/').at(-1),method=request.method();network.push({method,path});
  if(url.origin!==fixture.url)return route.abort();
  if(assets.has(file))return route.fulfill({contentType:'text/javascript',body:assets.get(file)});
  if(path.startsWith(base)&&method==='GET'){
   if(mode==='auth')return json(route,{error:{code:'storage.authentication',message:'请重新登录'}},403);
   if(path===base+'/status'){const result=copy(state);if(mode==='bad-read')result.settings.profiles[0].secret_key='leak';if(holdRead){holdRead=false;heldRead=()=>json(route,result);return;}return json(route,result);}
   if(path===base+'/records'){const matches=state.records.filter(r=>(!url.searchParams.get('profile')||r.profile_id===url.searchParams.get('profile'))&&(!url.searchParams.get('status')||r.status===url.searchParams.get('status'))&&(!url.searchParams.get('q')||(r.filename+' '+r.remote_key).includes(url.searchParams.get('q'))));return json(route,{records:matches,total:matches.length,page:1,pages:1});}
   if(path===base+'/files'){if(url.searchParams.get('download'))return route.fulfill({contentType:'application/octet-stream',body:'fixture'});return json(route,fixture.files);}
  }
  if(method==='GET'||method==='HEAD')return route.continue();
  const upload=path===base+'/files/upload',body=upload?null:request.postDataJSON();writes.push({path,body,query:url.search});assert(request.headers()['x-csrf-token']);
  if(path.startsWith(base))assert.equal(upload?Number(url.searchParams.get('expected_user_id')):body.expected_user_id,fixture.owner_id);
  if(mode==='conflict')return json(route,{error:{code:'storage.conflict',message:'配置已变化'}},409);
  if(mode==='identity')return json(route,{error:{code:'plugins.identity_changed',message:'账号已变化'}},409);
  if(mode==='lost')return route.abort();
  let deliver;
  if(path===base+'/settings'){const saved=save(body);if(mode==='bad')saved.workers++;deliver=()=>{state.settings=saved;return json(route,{ok:true,settings:saved});};}
  else if(path===base+'/action'){
   assert.equal(body.expected_version,state.settings.version);
   if(['preview_migration','migrate'].includes(body.action)){const result={...copy(fixture.migration),source:body.profile_id,target:body.target_profile_id};if(mode==='bad')result.target='wrong';deliver=()=>json(route,{ok:true,migration:result});}
   else if(body.action==='test')deliver=()=>json(route,{ok:true,probe:mode==='bad'?{...fixture.probe,cleanup_ok:false}:fixture.probe});
   else deliver=()=>json(route,{ok:true,scheduled:mode==='bad'?'wrong':1});
  }else if(path===base+'/files/action'||upload)deliver=()=>json(route,{ok:mode!=='bad'});
  else if(path==='/api/v1/admin/imports/preview'){
   const result=previewData(body);if(mode==='bad')result.document.items[0].key='unrelated.md';if(mode==='reordered')result.document.items.reverse();if(mode==='expired')result.expires=new Date(Date.now()-1000).toISOString();
   deliver=()=>json(route,result);
  }else if(path==='/api/v1/admin/imports/apply'){
   if(mode==='token-expired')return json(route,{error:{code:'import.preview_required',message:'请重新预览'}},409);
   const result=copy(fixture.applied);result.plan.source=body.input.source;if(mode==='bad')result.plan.items[0].post_id=0;deliver=()=>json(route,result);
  }else throw Error('Unexpected write '+path);
  if(mode==='held'){held=deliver;return;}return deliver();
 });
 try{
  await page.goto(fixture.login);await ready();await refreshed();await has('[data-storage-records]','fixture.png');check('Real Go storage page renders redacted profiles and typed records.');
  await writeFile(resolve(directory,variant+'-'+engine+'-storage.aria.txt'),await page.locator('body').ariaSnapshot());await page.screenshot({path:resolve(directory,variant+'-'+engine+'-storage-desktop.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});for(const theme of ['light','dark']){await page.evaluate(theme=>document.documentElement.dataset.theme=theme,theme);await delay(300);assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'narrow storage overflow');await page.screenshot({path:resolve(directory,variant+'-'+engine+'-storage-'+theme+'.png'),fullPage:true});}await page.setViewportSize({width:1440,height:1000});check('Storage desktop and narrow light/dark layouts fit.');
  await fresh();await set('[data-profile-field="name"]','  提交名称  ');await set('[data-profile-field="secret_key"]','submitted');mode='held';await submit('[data-storage-form]');await wait(()=>held);await set('[data-profile-field="name"]','保存中的新名称');await set('[data-profile-field="secret_key"]','newer');await release();await has('[data-save-state]','新输入仍待保存');assert.equal(await page.locator('[data-profile-field="name"]').inputValue(),'保存中的新名称');assert.equal(await page.locator('[data-profile-field="secret_key"]').inputValue(),'newer');check('Storage save preserves newer names and credentials.');
  mode='';await submit('[data-storage-form]');await has('[data-save-state]','设置已保存');await value('[data-profile-field="secret_key"]','');assert.equal(await page.locator('[data-admin-save]').isDisabled(),true);check('Successful save clears only confirmed credentials and resets dirty state.');
  await fresh();mode='conflict';await set('[data-profile-field="name"]','冲突草稿');await submit('[data-storage-form]');await has('[data-save-state]','配置已变化');assert.equal(await page.locator('[data-profile-field="name"]').inputValue(),'冲突草稿');assert.equal(await page.locator('[data-admin-save]').isDisabled(),false);check('Storage version conflict retains the full draft.');
  await fresh();mode='bad';await set('[data-profile-field="name"]','不匹配');await submit('[data-storage-form]');await has('[data-save-state]','结果待核对');assert.equal(await page.locator('[data-admin-save]').isDisabled(),true);check('Mismatched storage saves remain unconfirmed.');
  await fresh();mode='lost';await click('[data-storage-test]');await has('[data-storage-action-status]','未能确认');let count=writes.length;mode='';await refreshed();await delay(100);assert.equal(writes.length,count);assert.equal(await page.locator('[data-storage-test]').isDisabled(),true);check('Lost storage action replies stay locked after status refresh, without replay.');
  await fresh();mode='bad';await click('[data-storage-test]');await has('[data-storage-action-status]','未能确认');check('Probe success requires both upload verification and cleanup confirmation.');
  await fresh();await show('[data-migration-source]');await page.locator('[data-migration-source]').selectOption('archive');await page.locator('[data-migration-target]').selectOption('backup');await click('[data-migration-preview]');await has('[data-migration-summary]','迁移预览');await click('[data-migration-apply]');await has('[data-migration-summary]','迁移已安排');assert.equal(writes.at(-1).body.target_profile_id,'backup');check('Migration preview and apply bind both profiles and the saved configuration version.');
  await fresh();await show('[data-migration-source]');await page.locator('[data-migration-source]').selectOption('archive');await page.locator('[data-migration-target]').selectOption('backup');await click('[data-migration-preview]');await has('[data-migration-summary]','迁移预览');await page.locator('[data-migration-target]').selectOption('archive');assert.equal(await page.locator('[data-migration-apply]').isDisabled(),true);check('Changing the migration target invalidates its confirmation.');
  await fresh();await refreshed();await has('[data-storage-records]','fixture.png');await click('[data-storage-action="cleanup"]');count=writes.length;await close('[data-storage-confirm]',false);await delay(70);assert.equal(writes.length,count);check('Cancelling cleanup submits nothing.');
  await click('[data-storage-action="cleanup"]');state.settings.version++;await refreshed();await delay(100);await close('[data-storage-confirm]',true);await delay(100);assert.equal(writes.length,count);check('A changed configuration invalidates an open cleanup confirmation.');
  await fresh();holdRead=true;await refreshed();await wait(()=>heldRead);await set('[data-profile-field="name"]','新保存');await submit('[data-storage-form]');await has('[data-save-state]','设置已保存');await heldRead().catch(()=>{});await delay(80);assert.equal(await page.locator('[data-profile-field="name"]').inputValue(),'新保存');check('Reads begun before a save cannot overwrite its result.');
  await fresh();mode='auth';await refreshed();await has('[data-storage-action-status]','登录或权限已变化');assert.equal(await page.locator('[data-storage-profiles] button').count(),0);assert.equal(await page.locator('[data-profile-field="access_key"]').inputValue(),'');check('Read permission loss clears private storage profiles and records.');
  await fresh();mode='held';await set('[data-profile-field="name"]','旧账号');await submit('[data-storage-form]');await wait(()=>held);await page.evaluate(()=>document.dispatchEvent(new Event('chunnel:session')));await release();await delay(70);assert.equal(await page.locator('[data-storage-profiles] button').count(),0);check('Session changes retire late storage writes.');
  await fresh();mode='held';await click('[data-storage-test]');await wait(()=>held);await history();await release();await delay(70);assert.equal(await page.locator('[data-storage-test]').isDisabled(),true);check('BFCache return preserves uncertainty and rejects pre-navigation results.');
  await fresh();await show('[data-cloud-profile]');await page.locator('[data-cloud-profile]').selectOption('archive');await has('[data-cloud-status]','当前页 3 项');assert.equal(await page.locator('[data-cloud-files] tr').nth(1).locator('button').count(),1);check('Cloud files distinguish managed objects and prohibit their move/delete controls.');
  await page.locator('[data-cloud-files] tr').first().getByRole('button',{name:'复制',exact:true}).click();count=writes.length;await close('[data-cloud-dialog]',false);await delay(60);assert.equal(writes.length,count);check('Cancelling cloud copy leaves remote state untouched.');
  await page.locator('[data-cloud-files] tr').first().getByRole('button',{name:'复制',exact:true}).click();await close('[data-cloud-dialog]',true);await has('[data-cloud-status]','文件操作完成');assert.equal(writes.at(-1).body.key,'media/example.txt');check('Cloud copy uses the confirmed source, target and version.');
  mode='bad';await page.locator('[data-cloud-upload]').setInputFiles({name:'upload.txt',mimeType:'text/plain',buffer:Buffer.from('fixture')});await has('[data-cloud-status]','已上传 0 / 1');assert.equal(await page.locator('[data-cloud-upload]').isDisabled(),true);check('An invalid upload receipt is not counted as a completed file.');

  await importFresh();await preview();assert.equal(await page.locator('.import-item').count(),2);check('Real Go import projection renders both source identities and sanitized previews.');
  await writeFile(resolve(directory,variant+'-'+engine+'-import.aria.txt'),await page.locator('body').ariaSnapshot());await page.screenshot({path:resolve(directory,variant+'-'+engine+'-import-desktop.png'),fullPage:true});await page.setViewportSize({width:390,height:844});await delay(300);assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'narrow import overflow');await page.screenshot({path:resolve(directory,variant+'-'+engine+'-import-narrow.png'),fullPage:true});await page.setViewportSize({width:1440,height:1000});check('Import preview and mapping layout fits narrow screens.');
  await page.locator('[data-import-apply]').click();await has('[data-import-status]','迁入已完成');assert.equal(await page.locator('.import-item a[href^="/admin/content/"]').count(),2);check('Import apply validates created identities before displaying success.');
  await importFresh();mode='reordered';await preview();assert.match(await page.locator('.import-item').first().locator('.import-prose').textContent(),/正文一/);check('Reordered document projections remain paired by source key rather than array position.');
  await importFresh();mode='bad';await submit('[data-import-form]');await has('[data-import-status]','响应不完整');assert.equal(await page.locator('[data-import-apply]').isDisabled(),true);check('Unrelated import document identities cannot become an applicable preview.');
  await importFresh();mode='expired';await submit('[data-import-form]');await has('[data-import-status]','响应不完整');assert.equal(await page.locator('[data-import-apply]').isDisabled(),true);check('Expired preview credentials are rejected.');
  await importFresh();mode='held';await submit('[data-import-form]');await wait(()=>held);await set('[name=source]','changed-source');await release();await has('[data-import-status]','输入已变化');assert.equal(await page.locator('[data-import-apply]').isDisabled(),true);check('Input changes during preview discard the old response.');
  await importFresh();await preview();await set('[data-author]','0');count=writes.length;await submit('[data-import-form]');await has('[data-import-status]','账号编号');assert.equal(writes.length,count);check('Invalid author mappings stop before sending a preview.');
  await importFresh();await preview();mode='lost';await page.locator('[data-import-apply]').click();await has('[data-import-status]','未能确认');const original=copy(writes.at(-1).body);assert.equal(await page.locator('[name=source]').isDisabled(),true);await page.locator('[name=files]').evaluate(n=>n.dispatchEvent(new Event('change',{bubbles:true})));mode='';await page.locator('[data-import-apply]').click();await has('[data-import-status]','迁入已完成');assert.deepEqual(writes.at(-1).body,original);check('Explicit retry after a lost import response retains the exact input and token even after file-change events.');
  await importFresh();await preview();mode='bad';await page.locator('[data-import-apply]').click();await has('[data-import-status]','未能确认');assert.equal(await page.locator('[name=source]').isDisabled(),true);check('Malformed import apply receipts retain the original batch for reconciliation.');
  await importFresh();await preview();mode='token-expired';await page.locator('[data-import-apply]').click();await has('[data-import-status]','原预览已失效');mode='';await page.locator('[data-import-apply]').click();await has('[data-import-status]','原批次已重新预览');assert.equal(writes.at(-1).path,'/api/v1/admin/imports/preview');check('Expired apply credentials re-preview the original batch and require another confirmation.');
  await importFresh();await preview();mode='held';await page.locator('[data-import-apply]').click();await wait(()=>held);await page.evaluate(()=>document.dispatchEvent(new Event('chunnel:session')));await release();await delay(80);assert.equal(await page.locator('.import-item').count(),0);assert.equal(await page.locator('[name=source]').inputValue(),'');check('Account change clears import files, previews and pending receipts.');
  await importFresh();await preview();mode='held';await page.locator('[data-import-apply]').click();await wait(()=>held);await history();await release();await delay(80);assert.equal(await page.locator('[name=source]').isDisabled(),true);assert.match(await page.locator('[data-import-status]').textContent(),/待核对/);check('Browser restoration preserves an uncertain import batch without accepting a late success.');
  await importFresh();mode='held';await submit('[data-import-form]');await wait(()=>held);await page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:admin-before-replace',{detail:{regions:[document.querySelector('[data-content-import]')]}})));await release();await delay(80);assert.equal(await page.locator('.import-item').count(),0);check('Partial navigation retires import preview responses.');
  assert.deepEqual(errors,[]);check('No uncaught browser errors occurred.');
 }finally{await writeFile(resolve(directory,variant+'-'+engine+'-network.json'),JSON.stringify(network,null,2));await browser.close();}
}
await writeFile(resolve(directory,variant+'-results.json'),JSON.stringify({variant,checks:evidence.length,evidence},null,2)+'\n');
console.log('Verified '+evidence.length+' storage/import checks.');
