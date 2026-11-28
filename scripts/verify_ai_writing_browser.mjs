// Real Go pages/read APIs with all business writes, model calls and external traffic intercepted.
import assert from 'node:assert/strict';
import {readFile,writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {createHash} from 'node:crypto';
import {build} from 'esbuild';
import {chromium,firefox,webkit} from 'playwright';
const directory=resolve(process.argv[2]),fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const engines={chromium,firefox,webkit},names=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines),variant=process.argv[4]==='generated'?'generated':'typescript',assets=new Map(),evidence=[];
if(variant==='typescript')for(const name of ['ai-editor','ai-images','ai-pro-editor','ai-pro-max','writing-templates'])assets.set('chunnel-'+name+'.js',(await build({entryPoints:['frontend/core/'+name+'.ts'],bundle:true,write:false,format:'iife',platform:'browser',target:['chrome100','firefox110','safari16'],charset:'utf8'})).outputFiles[0].text);
const clone=value=>structuredClone(value),frame=(name,value)=>`event: ${name}\ndata: ${JSON.stringify(value)}\n\n`,hash=value=>createHash('sha256').update(value).digest('hex');
for(const name of names){
 const browser=await engines[name].launch(),context=await browser.newContext({viewport:{width:1440,height:1000},reducedMotion:'reduce',acceptDownloads:true}),page=await context.newPage();page.setDefaultTimeout(12000);
 let mode='',held=null,serial=1,templates=[clone(fixture.template)],receipts=new Map();const writes=[],errors=[],scripts=[];
 const check=text=>{evidence.push({engine:name,text});console.log(name+': '+text);};
 const json=(route,value,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(value)});
 const wait=async test=>{const end=Date.now()+12000;while(!test()){if(Date.now()>end)throw new Error('Missing intercepted request');await new Promise(resolve=>setTimeout(resolve,20));}};
 const text=(selector,value)=>page.waitForFunction(({selector,value})=>document.querySelector(selector)?.textContent?.includes(value),{selector,value});
 const submit=selector=>page.locator(selector).evaluate(form=>form.requestSubmit());
 const open=path=>page.goto(fixture.url+path);
 const release=async()=>{await wait(()=>held);const deliver=held;held=null;await deliver().catch(()=>{});mode='';};
 const retire=()=>page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,id:999,csrf_token:'other'}})));
 const changeSource=value=>page.evaluate(value=>{window.ChunnelBlockEditor.reset(value);document.querySelector('[data-content-source]').dispatchEvent(new Event('input',{bubbles:true}));},value);
 page.on('pageerror',error=>{errors.push(error.message);console.error('PAGE ERROR',page.url(),error.stack);});page.on('dialog',dialog=>dialog.accept());page.on('request',request=>{if(request.resourceType()==='script')scripts.push(request.url());});
 const templateList=()=>({items:templates.map(item=>{const v=item.revisions.at(-1);return {id:item.id,version:v.version,title:v.definition.title,description:v.definition.description,base_prompt:v.definition.base_prompt,archived:v.archived,updated_at:v.at};}),total:templates.length,next_offset:-1,snapshot_sha256:hash(JSON.stringify(templates))});
 const mutate=command=>{
  const item=command.id?templates.find(item=>item.id===command.id):{id:(serial++).toString(16).padStart(32,'0'),revisions:[]},last=item.revisions.at(-1),action=command.action;
  const imported=action==='import'?JSON.parse(command.document):null;
  const definition=action==='save'?command.definition:imported?imported.definition:action==='restore'?item.revisions.find(v=>v.version===command.restore_version).definition:last.definition;
  const source=action==='import'?{kind:'import',template_id:imported.template_id,version:imported.version,sha256:command.import_sha256}:action==='restore'?{kind:'history',template_id:item.id,version:command.restore_version}:{kind:action==='save'?'manual':action};
  const version=(last?.version||0)+1;item.revisions.push({version,definition:clone(definition),archived:action==='archive',actor:'owner',at:new Date().toISOString(),source});if(!command.id)templates.push(item);
  return {id:item.id,version,replayed:false};
 };
 await context.route('**/*',async route=>{
  const req=route.request(),url=new URL(req.url()),path=url.pathname,file=path.split('/').at(-1);
  if(url.origin!==fixture.url)return route.abort();
  if(assets.has(file))return route.fulfill({contentType:'text/javascript',body:assets.get(file)});
  if(path.startsWith('/fixture/'))return route.continue();
  if(req.method()==='GET'||req.method()==='HEAD'){
   if(path==='/api/v1/admin/session'&&mode==='foreign-session'){const response=await route.fetch(),data=await response.json();return json(route,{...data,user_id:999});}
   if(path==='/api/v1/admin/ai/prompts'&&mode==='bad-prompts')return json(route,{prompts:[{id:'wrong'}]});
   if(path==='/api/v1/admin/ai/templates'){if(mode==='history-failure')return json(route,{error:{message:'读取暂不可用'}},503);return json(route,templateList());}
   if(path.startsWith('/api/v1/admin/ai/templates/')){
    const id=path.split('/')[6],item=templates.find(item=>item.id===id);
    if(path.endsWith('/export')){const version=Number(url.searchParams.get('version')),revision=item.revisions.find(row=>row.version===version);return json(route,{schema:1,kind:'newpress.writing-template',template_id:id,version,definition:revision.definition});}
    return json(route,item||{error:{message:'未找到'}},item?200:404);
   }
   if(path===`/api/v1/admin/media/${fixture.media.id}`){const data=mode==='wrong-media'?{...fixture.media,id:fixture.media.id+1}:fixture.media;if(mode==='hold-media'){held=()=>json(route,data);return;}return json(route,data);}
   return route.continue();
  }
  const body=req.headers()['content-type']?.includes('json')?req.postDataJSON():{};writes.push({path,body,key:req.headers()['idempotency-key']});
  if(path==='/api/v1/admin/ai/generate'){
   const partial='## 已收到的部分\n\n保留事实，不添加新信息。',done={document:fixture.document,complete:true,usage:{prompt_tokens:0,completion_tokens:0,total_tokens:0,total_source:'reported'}};
   let stream=frame('ready',{model:body.model})+frame('thinking',{text:'模型提供的说明 <img src=x onerror=alert(1)>'})+frame('thinking_done',{})+frame('delta',{text:partial});
   if(mode==='bad-done')done.document={...fixture.document,tags:'invalid'};
   if(mode!=='broken')stream+=frame('done',done);
   const deliver=()=>route.fulfill({contentType:'text/event-stream',body:stream});if(mode==='hold-generation'){held=deliver;return;}return deliver();
  }
  if(path==='/api/v1/admin/ai/models'){const deliver=()=>json(route,{models:[{id:'discovered-model'}],base_url:'https://fixture.example.test/v1'});if(mode==='hold-models'){held=deliver;return;}return deliver();}
  if(path==='/api/v1/admin/ai/prepare'){
   const value={...fixture.document,...body,content_source:'<h2>一段正文</h2><p>这是一份可核对的生成结果。</p>',warnings:[]};
   const deliver=()=>json(route,mode==='bad-prepare'?{...value,category_ids:'invalid'}:value);if(mode==='hold-prepare'){held=deliver;return;}return deliver();
  }
  if(path==='/api/v1/admin/ai/images'){
   const item=mode==='bad-image'?{...fixture.media,public_url:'https://foreign.test/image.png'}:fixture.media;
   let stream=frame('ready',{message:'正在生成'})+frame('image',item);
   if(mode!=='broken-image')stream+=frame('done',{model:mode==='wrong-image-model'?'different':body.model,items:[item]});
   const deliver=()=>route.fulfill({contentType:'text/event-stream',body:stream});if(mode==='hold-images'){held=deliver;return;}return deliver();
  }
  if(path==='/api/v1/admin/plugins/ai-pro-max/metadata'){const data=mode==='bad-metadata'?{...fixture.metadata,Tags:[3]}:fixture.metadata;const deliver=()=>json(route,data);if(mode==='hold-metadata'){held=deliver;return;}return deliver();}
  if(path==='/api/v1/admin/plugins/ai-pro-max/paragraph'){const deliver=()=>json(route,{text:'优化后的选区'});if(mode==='hold-paragraph'){held=deliver;return;}return deliver();}
  if(path==='/api/v1/admin/plugins/ai-pro-max/topics'){const deliver=()=>json(route,{topics:mode==='bad-topics'?[4]:['如何开始第一篇文章','如何核对信息来源']});if(mode==='hold-topics'){held=deliver;return;}return deliver();}
  if(path==='/api/v1/admin/ai/template-import-preview'){const deliver=()=>json(route,{document:JSON.parse(body.document),sha256:mode==='wrong-import'?'0'.repeat(64):hash(body.document)});return deliver();}
  if(path==='/api/v1/admin/ai/templates'){
   if(mode==='template-conflict')return json(route,{error:{message:'模板版本冲突'}},409);
   const key=req.headers()['idempotency-key'];let result=receipts.has(key)?{...receipts.get(key),replayed:true}:mutate(body);receipts.set(key,result);
   if(mode==='lost-template')return route.abort();
   if(mode==='wrong-receipt')result={...result,version:result.version+4};
   const deliver=()=>json(route,result);if(mode==='hold-template'){held=deliver;return;}return deliver();
  }
  return json(route,{error:{message:'隔离验收不保存文章或队列'}},409);
 });
 const editor=async()=>{mode='';await open('/admin/content/new/');await page.waitForFunction(()=>!!window.ChunnelBlockEditor);};
 const writing=async()=>{await page.locator('[data-ai-open]:visible').first().click();await page.waitForFunction(()=>document.querySelector('[data-ai-editor]').open&&!document.querySelector('[data-ai-generate]').disabled);await text('[data-ai-connection-status]','已使用');};
 const generate=async()=>{await page.locator('[data-ai-brief]').fill('根据以下材料说明操作流程：先核对输入，再执行步骤，最后检查结果。');await page.locator('[data-ai-generate]').click();};
 const images=async()=>{await page.locator('[data-ai-image-open="cover"]').click();await page.waitForFunction(()=>document.querySelector('[data-ai-image-dialog]').open&&!document.querySelector('[data-ai-image-generate]').disabled);};
 const imageGenerate=async()=>{await page.locator('[data-ai-image-prompt]').fill('蓝绿色背景的插画，主体清晰。');await submit('[data-ai-image-form]');};
 const templatePage=async()=>{mode='';await open('/admin/settings/#site-settings-ai');await text('[data-wt-status]','列表已刷新');await page.locator('[data-wt-id]').first().click();await text('[data-wt-status]','已载入保存版本');};
 try{
  await page.goto(fixture.login);await page.waitForFunction(()=>!!window.ChunnelBlockEditor);
  assert(!scripts.some(url=>/chunnel-ai-(editor|images)\.js/.test(url)));check('Writing and images remain explicit lazy loads.');
  await writing();assert.equal(await page.locator('[data-ai-provider]').inputValue(),'writer');check('Writing uses actual redacted Go connections and built-in/team prompts.');
  await generate();await text('[data-ai-status]','全文与文章信息已生成');assert.equal(await page.locator('[data-ai-meta-title]').inputValue(),fixture.document.title);assert.equal(await page.locator('[data-ai-thinking-text] img').count(),0);check('Stream completion maps actual Go document while thinking markup stays inert.');
  mode='bad-done';await generate();await text('[data-ai-stage-state]','正文已保留');assert.match(await page.locator('[data-ai-markdown]').inputValue(),/已收到的部分/);check('Malformed completion preserves partial writing instead of accepting a false result.');
  mode='broken';await generate();await text('[data-ai-status]','中断');assert.match(await page.locator('[data-ai-markdown]').inputValue(),/已收到的部分/);mode='';check('Disconnected writing retains partial text.');
  mode='hold-generation';await generate();await wait(()=>held);await page.locator('[data-ai-stop]').click();await release();await text('[data-ai-status]','已停止');check('Stopping a pending generation ignores the late response.');
  await generate();await text('[data-ai-status]','全文与文章信息已生成');mode='bad-prepare';await page.locator('[data-ai-apply]').click();await text('[data-ai-status]','格式未确认');assert.equal(await page.locator('[data-content-source]').inputValue(),'');mode='';check('Invalid prepare response cannot fill the article.');
  mode='hold-prepare';await page.locator('[data-ai-apply]').click();await wait(()=>held);await changeSource('<p>生成期间改过的原稿</p>');await release();await text('[data-ai-status]','文章在操作期间发生了变化');assert.match(await page.locator('[data-content-source]').inputValue(),/改过的原稿/);check('Prepare completion cannot overwrite edits made after the request.');
  await page.locator('[data-ai-close]').click();await writing();await page.locator('[data-ai-apply]').click();await page.waitForFunction(()=>!document.querySelector('[data-ai-editor]').open);assert.match(await page.locator('[data-content-source]').inputValue(),/可核对/);await writing();await page.locator('[data-ai-undo-apply]').click();assert.match(await page.locator('[data-content-source]').inputValue(),/改过的原稿/);check('Explicit article fill and undo preserve the original continuous document.');
  await writing();await page.locator('[data-ai-provider]').selectOption('');await page.locator('[data-ai-base]').fill('https://fixture.example.test/v1');await page.locator('[data-ai-key]').fill('temporary-secret');mode='hold-models';await page.locator('[data-ai-detect]').click();await wait(()=>held);await page.locator('[data-ai-base]').fill('https://changed.example.test/v1');await release();assert.equal(await page.locator('[data-ai-model]').inputValue(),'');check('Temporary endpoint edits invalidate late model discovery.');
  await retire();assert.equal(await page.locator('[data-ai-key]').inputValue(),'');assert.equal(await page.locator('[data-ai-markdown]').inputValue(),'');assert.equal(await page.locator('[data-ai-generate]').isDisabled(),true);check('Actor change clears writing results, temporary secrets and pending actions.');
  await editor();await images();await imageGenerate();await text('[data-ai-image-status]','图片已保存');await page.waitForFunction(()=>!document.querySelector('[data-ai-image-cover]').disabled);check('Image stream validates actual local media and waits for preview readiness.');
  mode='wrong-media';await page.locator('[data-ai-image-cover]').click();await text('[data-ai-image-status]','另一份媒体');assert.equal(await page.locator('[name=cover_url]').inputValue(),'');mode='';check('Cover lookup rejects a different media identity.');
  mode='hold-media';await page.locator('[data-ai-image-cover]').click();await wait(()=>held);await page.locator('[name=cover_url]').evaluate(input=>{input.value='/media/old/cover.png';input.dispatchEvent(new Event('input',{bubbles:true}));});await release();await text('[data-ai-image-status]','封面已变化');check('Late image validation cannot replace a changed cover.');
  await page.locator('[data-ai-image-close]').click();await images();await page.locator('[data-ai-image-cover]').click();await page.waitForFunction(()=>!document.querySelector('[data-ai-image-dialog]').open);assert.equal(await page.locator('[name=cover_url]').inputValue(),fixture.media.public_url);check('Reopening captures current cover state and explicit apply succeeds.');
  await images();await page.locator('[data-ai-image-alt]').fill('可访问的图片说明');await page.locator('[data-ai-image-caption]').fill('<b>图注是文本</b>');await page.locator('[data-ai-image-insert]').click();await page.waitForFunction(()=>!document.querySelector('[data-ai-image-dialog]').open);assert.match(await page.locator('[data-content-source]').inputValue(),/可访问的图片说明/);assert.match(await page.locator('[data-content-source]').inputValue(),/&lt;b&gt;/);check('Image insertion uses captured document position and escaped captions.');
  await images();mode='wrong-image-model';await imageGenerate();await text('[data-ai-image-status]','完成结果未确认');mode='bad-image';await imageGenerate();await text('[data-ai-image-status]','图片已移除');mode='';check('Wrong model completion and foreign image URLs are rejected.');
  mode='hold-images';await imageGenerate();await wait(()=>held);await retire();await release();assert.equal(await page.locator('[data-ai-image-gallery] button').count(),0);check('Image actor retirement clears the gallery and ignores a late stream.');
  await editor();await changeSource('<p>需要完善的原文</p>');await page.locator('[data-pro-preview]').click();await text('[data-pro-status]','可编辑这些建议');await page.locator('[data-pro-results] [data-suggestion=excerpt]').fill('手动修订建议');await page.locator('[data-pro-apply]').click();assert.equal(await page.locator('[name=excerpt]').inputValue(),'手动修订建议');check('Pro metadata suggestions remain editable and fill only selected fields.');
  mode='hold-metadata';await page.locator('[data-pro-preview]').click();await wait(()=>held);await changeSource('<p>等待时更新了正文</p>');await release();await text('[data-pro-status]','文章已修改');assert.equal(await page.locator('[data-pro-apply]').isHidden(),true);check('Pro metadata refuses stale article snapshots.');
  mode='bad-metadata';await page.locator('[data-pro-preview]').click();await text('[data-pro-status]','格式未确认');mode='';assert.equal(await page.locator('[data-pro-apply]').isHidden(),true);check('Malformed Pro metadata never reaches article inputs.');
  // Use the document editor's actual keyboard selection, then open the assistant.
  await editor();const prose=page.locator('.newpress-document');await prose.click();await prose.pressSequentially('需要优化的一段原文');await prose.press('ControlOrMeta+A');await prose.press('Shift+ArrowLeft');await prose.press('Shift+ArrowRight');await page.locator('[data-paragraph-generate]').click();await text('[data-paragraph-status]','可先编辑建议');await page.locator('[data-paragraph-result]').fill('修改后的选区文本');await page.locator('[data-paragraph-apply]').click();assert.match(await page.locator('[data-content-source]').inputValue(),/修改后的选区文本/);check('Pro paragraph replaces a real continuous-editor selection.');
  await retire();assert.equal(await page.locator('[data-paragraph-result]').inputValue(),'');assert.equal(await page.locator('[data-pro-results]').textContent(),'');check('Pro retirement clears suggestions and disables apply actions.');
  await open('/admin/plugins/ai-pro-max/');await page.locator('[data-suggest-topics]').click();await text('[data-topic-status]','点击选题');await page.locator('[data-topic-options] button').first().click();assert.match(await page.locator('[data-topic-picker]').locator('..').locator('[name=topics]').inputValue(),/如何开始/);check('Pro Max suggestions append to the native queue form.');
  mode='hold-topics';await page.locator('[data-suggest-topics]').click();await wait(()=>held);await page.locator('[data-topic-picker]').locator('..').locator('[name=target]').selectOption('page');await release();assert.equal(await page.locator('[data-topic-options] button').count(),0);check('Queue target changes cancel old topic suggestions.');
  mode='bad-topics';await page.locator('[data-suggest-topics]').click();await text('[data-topic-status]','格式未确认');mode='';check('Non-text topic suggestions are rejected.');
  await templatePage();await page.locator('[data-wt-title]').fill('本次模板修改');await submit('[data-wt-form]');await text('[data-wt-status]','已保存 v2');check('Team template save verifies actual ID and next-version receipt.');
  await page.locator('[data-wt-title]').fill('丢失回执的修改');mode='lost-template';await submit('[data-wt-form]');await text('[data-wt-status]','原请求已保留');const original=writes.at(-1);mode='';await page.locator('[data-wt-retry]').click();await text('[data-wt-status]','已核对原请求');const retried=writes.at(-1);assert.equal(retried.key,original.key);assert.deepEqual(retried.body,original.body);check('Lost template receipts replay the exact original request and idempotency key.');
  await page.locator('[data-wt-title]').fill('不能确认的版本');mode='wrong-receipt';await submit('[data-wt-form]');await text('[data-wt-status]','回执与原模板或版本不一致');assert.equal(await page.locator('[data-wt-pending]').isVisible(),true);mode='';await page.locator('[data-wt-retry]').click();await text('[data-wt-status]','已核对原请求');check('Wrong-version template receipts retain the pending request until confirmed.');
  mode='template-conflict';await page.locator('[data-wt-title]').fill('冲突的草稿');await submit('[data-wt-form]');await text('[data-wt-status]','模板版本冲突');assert.equal(await page.locator('[data-wt-title]').inputValue(),'冲突的草稿');assert.equal(await page.locator('[data-wt-pending]').isHidden(),true);mode='';check('Definitive template conflicts preserve the draft without claiming success.');
  await page.locator('[data-wt-id]').first().click();await text('[data-wt-status]','已载入');await page.locator('[data-wt-archive]').click();await text('[data-wt-version]','已归档');await page.locator('[data-wt-archive]').click();await text('[data-wt-version]','启用中');await page.locator('[data-wt-history] summary').click();await page.locator('[data-wt-history-select]').selectOption('1');await page.locator('[data-wt-restore]').click();await text('[data-wt-status]','已保存');assert.equal(await page.locator('[data-wt-title]').inputValue(),fixture.template.revisions[0].definition.title);check('Archive, reactivation and historical restore retain complete version history.');
  const downloadPromise=page.waitForEvent('download');await page.locator('[data-wt-export]').click();const download=await downloadPromise;const exported=JSON.parse(await readFile(await download.path(),'utf8'));assert.equal(exported.template_id,fixture.template.id);check('Template export downloads the selected validated version.');
  await page.locator('.wt-import > summary').click();const raw=JSON.stringify(fixture.template_export);await page.locator('[data-wt-import-document]').fill(raw);mode='wrong-import';await page.locator('[data-wt-import-preview]').click();await text('[data-wt-status]','不一致');assert.equal(await page.locator('[data-wt-import-review]').isHidden(),true);mode='';await page.locator('[data-wt-import-preview]').click();await text('[data-wt-status]','预览已就绪');await page.locator('[data-wt-import-confirm]').check();await page.locator('[data-wt-import-save]').click();await text('[data-wt-status]','已保存 v1');assert.equal(await page.locator('[data-wt-id]').count(),2);check('Import checks exact file bytes and content before explicit creation.');
  await page.locator('[data-wt-title]').fill('保存后的新输入');mode='hold-template';await submit('[data-wt-form]');await wait(()=>held);await page.locator('[data-wt-title]').evaluate(input=>{input.value='保存期间继续编辑';input.dispatchEvent(new Event('input',{bubbles:true}));});await release();await text('[data-wt-status]','后续输入仍保留');assert.equal(await page.locator('[data-wt-title]').inputValue(),'保存期间继续编辑');check('Confirmed template saves retain edits made while the request was pending.');
  await retire();assert.equal(await page.locator('[data-wt-title]').inputValue(),'');assert.equal(await page.locator('[data-wt-id]').count(),0);check('Template actor retirement clears private drafts and history.');
  for(const color of ['light','dark']){await page.emulateMedia({colorScheme:color});await page.setViewportSize({width:390,height:844});await editor();await writing();await page.screenshot({path:resolve(directory,`${variant}-${name}-writing-${color}.png`)});assert(await page.locator('[data-ai-editor]').evaluate(dialog=>dialog.getBoundingClientRect().width<=innerWidth+1));await page.locator('[data-ai-close]').click();}check('Writing dialogs fit a narrow viewport in light and dark appearance.');
  const reader=await context.newPage();await reader.goto(fixture.url+'/fixture/login/reader');for(const path of ['/api/v1/admin/ai/prompts','/api/v1/admin/ai/connections','/api/v1/admin/ai/templates'])assert.equal((await context.request.get(fixture.url+path)).status(),403);await reader.close();check('Reader permissions deny all writing configuration and template reads.');
  assert(!scripts.some(url=>/react|echarts|analytics-react/.test(url)));assert.deepEqual(errors,[]);check('All flows finish without page errors or React/ECharts dependencies.');
  await writeFile(resolve(directory,`${variant}-${name}-writes.json`),JSON.stringify(writes,null,2)+'\n');
 }catch(error){await page.screenshot({path:resolve(directory,`${variant}-${name}-failure.png`)}).catch(()=>{});await writeFile(resolve(directory,`${variant}-${name}-failure.txt`),String(error.stack)+'\n'+await page.locator('body').innerText().catch(()=>''));throw error;}
 finally{if(held)await held().catch(()=>{});await context.close();await browser.close();}
}
await writeFile(resolve(directory,`${variant}-summary.json`),JSON.stringify({variant,checks:evidence.length,evidence},null,2)+'\n');
console.log(`${variant}: ${evidence.length} AI writing browser checks passed.`);
