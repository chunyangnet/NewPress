// Real Go pages and reads. All business writes are intercepted, and the Go
// fixture compares accounts, files, receipts, posts, revisions and series at exit.
import assert from 'node:assert/strict';
import {readFile, writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {chromium, firefox, webkit} from 'playwright';
import {compileFrontendEntry} from './frontend_entries.mjs';

const directory=resolve(process.argv[2]), fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const variant=process.argv[4]==='typescript'?'typescript':'generated', engines={chromium,firefox,webkit};
const selected=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines), assets=new Map(), evidence=[];
if(variant==='typescript')for(const entry of ['circles','links','series'])assets.set(`chunnel-${entry}.js`,(await compileFrontendEntry(entry,{minify:false})).code);
const until=async predicate=>{const deadline=Date.now()+12000;while(!predicate()){if(Date.now()>deadline)throw new Error('Expected intercepted request missing');await new Promise(resolve=>setTimeout(resolve,20));}};
for(const name of selected){
  const browser=await engines[name].launch(),context=await browser.newContext({viewport:{width:1365,height:960},reducedMotion:'reduce'}),page=await context.newPage();
  page.setDefaultTimeout(12000);
  const errors=[],writes=[],scripts=[],dialogs=[];let circleMode='invalid',linkMode='invalid',seriesMode='invalid',sourceMode='real',metadataMode='hold';
  let heldCircle,heldLink,heldSeries,heldSource,heldMetadata;
  const json=(route,data,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
  page.on('pageerror',error=>errors.push({message:error.message,url:page.url()}));
  page.on('dialog',async dialog=>{dialogs.push(dialog.message());await dialog.accept();});
  page.on('request',request=>{if(request.resourceType()==='script')scripts.push(new URL(request.url()).pathname);});
  await context.route('**/*',async route=>{
    const request=route.request(),url=new URL(request.url()),path=url.pathname,filename=path.split('/').at(-1);
    if(assets.has(filename))return route.fulfill({contentType:'text/javascript',body:assets.get(filename)});
    if(request.method()!=='GET'&&request.method()!=='HEAD'){
      if(path.startsWith('/fixture/'))return route.continue();
      if(path.endsWith('/preview'))return route.continue();
      const raw=request.postData(),body=request.headers()['content-type']?.includes('application/json')?request.postDataJSON():null;
      writes.push({path,body,raw,headers:request.headers()});
      if(path==='/api/v1/admin/link-metadata'){
        const result={url:body.url,title:'提取的站点',description:'服务端提取介绍',logo:'',notice:'已提取'};
        if(metadataMode==='hold'){heldMetadata=()=>json(route,result);return;}
        return json(route,result);
      }
      if(path.startsWith('/api/v1/community/circles/')){
        if(path.endsWith('/attachments')){
          const version=Number(raw.match(/name="version"\r\n\r\n([^\r]+)/)?.[1]);
          return json(route,{slug:'makers',version:version+1,attachment_id:'a'.repeat(64),attachment:{id:(circleMode==='invalid'?'b':'a').repeat(64),name:'验收附件.txt',size:4,image:false},replay:circleMode==='valid'});
        }
        if(circleMode==='conflict')return json(route,{error:{message:'版本已变化'}},409);
        const receipt={slug:circleMode==='invalid'?'wrong':'makers',version:body.version+1,topic_id:body.action==='topic'?1:body.topic_id,reply_id:body.action==='reply'?2:body.action==='edit_reply'?body.reply_id:undefined,reply_page:['reply','edit_reply'].includes(body.action)?1:undefined};
        if(circleMode==='hold'){heldCircle=()=>json(route,receipt);return;}
        return json(route,receipt);
      }
      if(path.startsWith('/api/v1/admin/link-pages/')){
        if(linkMode==='conflict')return json(route,{error:{message:'页面版本冲突'}},409);
        const receipt={slug:'friends',version:body.version+1,item_id:body.action==='save_item'?(linkMode==='invalid'?999:body.item.id||3):0,replay:linkMode==='valid'};
        if(linkMode==='hold'){heldLink=()=>json(route,receipt);return;}
        return json(route,receipt);
      }
      if(path.startsWith('/api/v1/admin/series')){
        if(request.method()==='DELETE')return seriesMode==='valid'?route.fulfill({status:204}):json(route,{});
        const version=Number(request.headers()['if-match']?.match(/-v(\d+)"/)?.[1]||0)+1;
        const receipt={...body,id:fixture.series_id,version,etag:`"series-${fixture.series_id}-v${version}"`,post_ids:seriesMode==='invalid'?[...body.post_ids].reverse():body.post_ids};
        if(seriesMode==='conflict')return json(route,{error:{message:'已被其他请求修改，当前输入仍保留。'}},412);
        if(seriesMode==='hold'){heldSeries=()=>json(route,receipt);return;}
        return json(route,receipt,request.method()==='POST'?201:200);
      }
      // Nonessential background writes are intercepted too; fixture state is read-only.
      return json(route,{error:{message:'只读验收未提交操作'}},503);
    }
    if(path==='/api/v1/admin/series/sources'&&sourceMode==='hold'&&url.searchParams.get('q')==='迟到'){
      heldSource=()=>json(route,{items:[{id:9000,title:'迟到结果',status:'draft'}],page:1,total:1,has_more:false});return;
    }
    return route.continue();
  });
  const check=label=>{evidence.push({browser:name,variant,check:label});console.log(`${name} ${variant}: ${label}`);};
  const text=(selector,value)=>page.waitForFunction(({selector,value})=>document.querySelector(selector)?.textContent.includes(value),{selector,value});
  const open=async path=>{await page.goto(fixture.url+path);await page.waitForLoadState('networkidle');};
  const select=async(selector,value)=>page.locator(selector).evaluate((node,value)=>{node.value=value;node.dispatchEvent(new Event('change',{bubbles:true}));},value);
  const save=selector=>page.locator(selector).evaluate(form=>form.requestSubmit());
  const discard=()=>page.evaluate(()=>{for(const form of document.querySelectorAll('form'))window.ChunnelAdminUI?.savebar(form)?.discard();});
  try{
    await page.goto(fixture.login);await open('/community/makers/');
    assert.ok(await page.locator('[data-circle-command=topic] .circle-editor-tools').count());
    await open('/admin/circles/makers/#circle-members');await page.locator('[data-member-search]').fill('圈内读者');
    assert.equal(await page.locator('[data-circle-member-row]:visible').count(),1);
    await page.locator('[data-circle-members-close]').click();await open('/community/makers/');check('Actual circle pages render the editor and filter members in the administration dialog.');

    const topic='[data-circle-command=topic]';await page.locator(topic+' [name=title]').fill('浏览器验收保留草稿');await page.locator(topic+' [name=body]').fill('## 正文预览\n\n<script>window.untrusted=true</script>');
    await page.locator(topic).getByText('图片、附件与正文预览',{exact:true}).click();
    await page.locator(topic).getByRole('button',{name:'预览正文',exact:true}).click();await page.locator(topic+' .circle-preview').waitFor();
    assert.equal(await page.locator(topic+' .circle-preview script').count(),0);assert.equal(await page.evaluate(()=>window.untrusted),undefined);
    check('Real preview endpoint renders Markdown and keeps untrusted script text inert.');
    await save(topic);await text('[data-circle-status]','保存回执未匹配');
    const firstTopic=writes.filter(row=>row.body?.action==='topic').at(-1);await save(topic);await text('[data-circle-status]','保存回执未匹配');
    assert.deepEqual(writes.filter(row=>row.body?.action==='topic').at(-1).body,firstTopic.body);
    assert.equal(await page.locator(topic+' [name=title]').inputValue(),'浏览器验收保留草稿');check('Invalid topic receipts preserve the draft and exact retry identity.');

    circleMode='conflict';await save(topic);await page.locator('[data-circle-conflict]').waitFor();
    assert.ok((await page.locator('[data-circle-latest]').textContent()).includes('创作与实践'));
    const beforeRetry=writes.length;await page.locator('[data-circle-retry]').click();await until(()=>writes.length>beforeRetry);await page.locator('[data-circle-conflict]').waitFor();
    check('Version conflicts fetch the actual owner-scoped circle state and keep the original input.');

    circleMode='invalid';await page.locator(topic+' input[type=file]').setInputFiles({name:'验收附件.txt',mimeType:'text/plain',buffer:Buffer.from('text')});
    await page.locator(topic).getByRole('button',{name:'上传附件',exact:true}).click();await text('[data-circle-status]','附件回执未确认');
    circleMode='valid';await page.locator(topic).getByRole('button',{name:'上传附件',exact:true}).click();await text('[data-circle-status]','附件已上传');
    const uploads=writes.filter(row=>row.path.endsWith('/attachments')),key=raw=>raw.match(/name="key"\r\n\r\n([^\r]+)/)[1];
    assert.equal(key(uploads.at(-1).raw),key(uploads.at(-2).raw));assert.equal(await page.locator(topic+' [data-attachment-id]:checked').count(),1);
    check('Attachment retries reuse the original key and append only validated upload results.');

    await open('/community/makers/topics/1');circleMode='invalid';const reply='[data-circle-command=reply]';await page.locator(reply+' [name=body]').fill('保留回复正文');await save(reply);await text('[data-circle-status]','保存回执未匹配');
    const replyBody=writes.filter(row=>row.body?.action==='reply').at(-1).body;assert.equal(replyBody.topic_id,1);assert.equal(replyBody.body,'保留回复正文');
    check('Actual topic replies retain their target and input after an unconfirmed write.');

    await open('/admin/features/links/?edit=friends&item=1#link-editor');const item='[data-links-form=save_item]';
    await page.locator(item+' [name=name]').fill('');await page.locator('[data-links-fetch]').click();await until(()=>heldMetadata);
    await page.locator(item+' [name=url]').fill('changed.example.test');await heldMetadata();heldMetadata=null;await text('[data-links-status]','域名已经变化');
    assert.equal(await page.locator(item+' [name=name]').inputValue(),'');check('Link metadata cannot fill a form after its requested URL changes.');
    await page.locator(item+' [name=name]').fill('待保存名称');linkMode='hold';await save(item);await until(()=>heldLink);
    await page.locator(item+' [name=name]').fill('继续输入名称');await heldLink();heldLink=null;await text('[data-links-status]','未保存的输入仍保留');
    assert.equal(await page.locator(item+' [name=name]').inputValue(),'继续输入名称');assert.equal(await page.locator(item+' [data-admin-save]').isEnabled(),true);
    check('A successful link save keeps newer typing dirty and uses the submitted field snapshot.');

    await page.locator('[data-settings-target=link-page-settings]').click();await page.locator('[data-links-form=configure] [name=style][value=wall]').check({force:true});
    linkMode='valid';await save('[data-links-form=configure]');await text('[data-links-form=configure] [data-save-state]','已保存');
    assert.equal(writes.filter(row=>row.body?.action==='configure').at(-1).body.settings.style,'wall');assert.equal(await page.locator(item+' [name=name]').inputValue(),'继续输入名称');
    check('Page settings serialize radio choices and preserve unsaved edits in the link item form.');

    await discard();await open('/admin/features/links/?edit=friends&item=1#link-editor');linkMode='hold';await page.locator(item+' [name=name]').fill('旧账号草稿');await save(item);await until(()=>heldLink);
    await page.evaluate(id=>document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,id}})),fixture.owner_id+100);await heldLink().catch(()=>{});heldLink=null;
    await text('[data-links-status]','登录状态已变化');assert.equal(await page.locator(item+' [name=name]').inputValue(),'旧账号草稿');assert.equal(await page.locator(item+' [data-admin-save]').isDisabled(),true);
    check('Account changes invalidate link requests without clearing the old owner draft.');

    await open(`/admin/series/?edit=${fixture.series_id}`);await page.locator('[data-series-sources] button').first().waitFor();
    assert.equal(await page.locator('[data-series-editor]').getAttribute('data-user-id'),String(fixture.owner_id));assert.equal(await page.locator('[data-series-sources]>div').count(),20);
    await page.locator('[data-series-source-next]').click();await text('[data-series-picker-status]','第 2 页');assert.equal(await page.locator('[data-series-sources]>div').count(),6);
    check('Series editor reads both actual source pages with the rendered account identity.');
    sourceMode='hold';await page.locator('[data-series-search]').fill('迟到');await page.locator('[data-series-find]').click();await until(()=>heldSource);
    await page.locator('[data-series-search]').fill('选集文章 02');await page.locator('[data-series-find]').click();await text('[data-series-sources]','选集文章 02');await heldSource().catch(()=>{});heldSource=null;
    assert.equal((await page.locator('[data-series-sources]').textContent()).includes('迟到结果'),false);
    await page.locator('[data-series-search]').dispatchEvent('keydown',{key:'Enter',isComposing:true,keyCode:229});check('A newer series search wins over late results, and IME Enter does not submit.');

    const first=fixture.post_ids[0];await page.locator(`[data-series-chapter="${first}"]`).focus();await page.keyboard.press('Alt+ArrowDown');
    await save('[data-series-editor]');await text('[data-save-state]','保存回执未匹配');let lastSeries=writes.filter(row=>row.path===`/api/v1/admin/series/${fixture.series_id}`).at(-1);
    assert.deepEqual(lastSeries.body.post_ids,[fixture.post_ids[1],first,fixture.post_ids[2]]);assert.equal(lastSeries.headers['if-match'],`"series-${fixture.series_id}-v1"`);
    seriesMode='valid';await save('[data-series-editor]');await text('[data-save-state]','已保存');
    await page.locator('[name=title]').fill('保存冲突保留');seriesMode='conflict';await save('[data-series-editor]');await text('[data-save-state]','已被其他请求修改');assert.equal(await page.locator('[name=title]').inputValue(),'保存冲突保留');
    check('Keyboard chapter ordering, checked receipts, If-Match advancement and 412 recovery preserve editor state.');

    seriesMode='hold';await save('[data-series-editor]');await until(()=>heldSeries);
    await page.evaluate(id=>document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,id}})),fixture.owner_id+100);await heldSeries().catch(()=>{});heldSeries=null;
    await text('[data-series-status]','登录状态已变化');assert.equal(await page.locator('.series-editor-fields').evaluate(node=>node.disabled),true);
    check('An identity change cancels series saves and keeps the disabled draft for recovery.');

    await open('/admin/series/?edit=new');assert.equal(await page.locator('[data-series-chapter]').count(),0);await page.getByRole('button',{name:/^添加：/}).first().click();assert.equal(await page.locator('[data-series-chapter]').count(),1);
    await discard();await open('/admin/features/links/?edit=new');assert.equal(await page.locator('[data-links-form=create]').count(),1);
    check('New series and new link-board pages initialize without optional existing-record controls.');

    for(const [style,scheme,width] of [['classic','light',1365],['liquid-glass','dark',390]]){
      await context.request.post(fixture.url+'/fixture/style/'+style);await page.setViewportSize({width,height:960});await page.emulateMedia({colorScheme:scheme});
      await open('/admin/series/?edit='+fixture.series_id);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+2),true);
      await page.screenshot({path:resolve(directory,`${name}-${variant}-series-${style}.png`),fullPage:true});
      await open('/links/friends/?style=wall');assert.equal((await page.locator('body').textContent()).includes('未公开站点'),false);
      await page.screenshot({path:resolve(directory,`${name}-${variant}-links-${style}.png`),fullPage:true});
      await open('/community/makers/');assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+2),true);
      await page.screenshot({path:resolve(directory,`${name}-${variant}-circle-${style}.png`),fullPage:true});
    }
    check('Desktop/light/classic and narrow/dark/glass pages preserve responsive layouts and private link visibility.');
    const guest=await browser.newContext({javaScriptEnabled:false}),guestPage=await guest.newPage();
    try{await guestPage.goto(fixture.url+'/series/collections-reading/');const content=await guestPage.locator('body').textContent();assert.equal(content.includes('选集文章 00'),false);assert.equal(content.includes('选集文章 01'),false);assert.equal(content.includes('选集文章 02'),true);await guestPage.goto(fixture.url+'/links/friends/');assert.equal((await guestPage.locator('body').textContent()).includes('创作手记'),true);}finally{await guest.close();}
    assert.equal(scripts.some(path=>/react|echarts|analytics-interactive/.test(path)),false);
    assert.deepEqual(errors,[]);check('SSR works with JavaScript disabled; public selection hides private/draft chapters and no page loads React/ECharts.');
    await writeFile(resolve(directory,`${name}-${variant}-requests.json`),JSON.stringify({writes,scripts,dialogs,errors},null,2));
  }catch(error){await page.screenshot({path:resolve(directory,`${name}-${variant}-failure.png`),fullPage:true}).catch(()=>{});await writeFile(resolve(directory,`${name}-${variant}-failure.json`),JSON.stringify({error:String(error),stack:error.stack,url:page.url(),errors,evidence,writes},null,2));throw error;}
  finally{await context.close();await browser.close();}
}
await writeFile(resolve(directory,`${variant}-${selected.join('-')}-summary.json`),JSON.stringify({checks:evidence.length,evidence},null,2));
console.log(`${variant}: ${evidence.length} real-page checks passed.`);
