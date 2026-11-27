// Real isolated Go pages; business writes are intercepted and recorded.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
import { compileFrontendEntry } from './frontend_entries.mjs';
const directory=resolve(process.argv[2]),fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
assert.equal(fixture.experience,true);
const variant=process.argv[4]==='typescript'?'typescript':'generated',engines={chromium,firefox,webkit};
const selected=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines),evidence=[];
const code=variant==='typescript'?(await compileFrontendEntry('experience',{minify:false})).code:null;
const exploreCode=variant==='typescript'?(await compileFrontendEntry('explore',{minify:false})).code:null;
for(const name of selected){
  const browser=await engines[name].launch(),context=await browser.newContext({viewport:{width:1280,height:900},reducedMotion:'reduce'}),page=await context.newPage();
  page.setDefaultTimeout(12000);
  const errors=[],writes=[],scripts=[];
  let commentMode='hold',releaseComment,receiptMode='invalid',communityMode='invalid',discoveryMode='real',releaseDiscovery;
  const json=(route,data,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{if(request.resourceType()==='script')scripts.push(new URL(request.url()).pathname);});
  await page.route('**/*',async route=>{
    const request=route.request(),url=new URL(request.url()),path=url.pathname;
    if(url.origin!==fixture.url)return route.abort();
    if(!['GET','HEAD'].includes(request.method())){
      let body;try{body=request.postDataJSON();}catch{body=request.postData();}
      const write={path,method:request.method(),body,key:request.headers()['idempotency-key']};writes.push(write);
      if(path.endsWith('/comments/receipt'))return json(route,receiptMode==='invalid'?{status:'pending',replayed:true,submission_key:'wrong-key'}:{status:'pending',replayed:true,submission_key:write.key,notice:'已确认此前提交'});
      if(path.endsWith('/comments')){
        const receipt={status:'approved',submission_key:write.key,comment:{id:99001,post_id:fixture.post_id,parent_id:body.parent_id,author_name:'评论验收读者',origin:{location:'中国',precision:'country',flag:'cn',device:'桌面浏览器',kind:'desktop'}}};
        if(commentMode==='hold'){releaseComment=()=>json(route,receipt);return;}
        return json(route,commentMode==='invalid'?{}:{status:'pending',submission_key:write.key,replayed:true});
      }
      if(path==='/api/v1/admin/contents')return json(route,communityMode==='invalid'?{}:{content:{id:fixture.post_id,status:body.status,permalink:'/posts/browser-story'}});
      if(/analytics/.test(path))return json(route,{ok:true});
      return json(route,{ok:true});
    }
    if(path==='/explore/'&&request.resourceType()==='fetch'&&discoveryMode!=='real'){
      if(discoveryMode==='fail')return route.fulfill({status:503,contentType:'text/html',body:'unavailable'});
      releaseDiscovery=async()=>route.fulfill({response:await route.fetch()});return;
    }
    if(code&&path.endsWith('/chunnel-experience.js'))return route.fulfill({contentType:'application/javascript',body:code});
    if(exploreCode&&path.endsWith('/chunnel-explore.js'))return route.fulfill({contentType:'application/javascript',body:exploreCode});
    return route.continue();
  });
  const check=label=>{evidence.push({browser:name,variant,check:label});console.log(`${name} ${variant}: ${label}`);};
  const until=async predicate=>{const deadline=Date.now()+12000;while(!predicate()){if(Date.now()>deadline)throw new Error('Expected intercepted request missing');await new Promise(resolve=>setTimeout(resolve,25));}};
  const text=(selector,value)=>page.waitForFunction(({selector,value})=>document.querySelector(selector)?.textContent.includes(value),{selector,value});
  const open=async path=>{assert.equal((await page.goto(fixture.url+path)).status(),200);await page.waitForLoadState('networkidle');const reject=page.locator('[data-cookie-reject]').filter({visible:true}).first();if(await reject.isVisible())await reject.click();};
  const edit=async(selector,value)=>{
    const editor=page.locator(`${selector} [data-emoji-editor]`);
    if(await editor.count())await editor.fill(value);else await page.locator(`${selector} textarea`).first().fill(value);
  };
  const dispatchSession=async id=>page.evaluate(id=>document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,id,name:'验收账号',can_publish:true}})),id);
  try{
    await context.request.post(fixture.url+'/fixture/reload');await context.request.post(fixture.url+'/fixture/style/classic');await context.request.get(fixture.login);
    await open('/posts/browser-story');await page.waitForFunction(()=>document.querySelector('[data-comment-form]')?.dataset.asyncReady!==undefined);
    await page.locator(`#comment-${fixture.parent_comment_id} [data-comment-reply]`).click();await edit('[data-comment-form]','已提交的第一条回复 🙂');
    await page.locator('[data-comment-form] button[type=submit]').click();await until(()=>releaseComment);
    await edit('[data-comment-form]','发送中继续写的新内容');const top=await page.evaluate(()=>scrollY);await releaseComment();releaseComment=null;
    await page.locator('#comment-99001').waitFor();assert.equal(await page.locator('[data-comment-content]').inputValue(),'发送中继续写的新内容');
    assert.equal(await page.locator(`#comment-${fixture.parent_comment_id} #comment-99001`).count(),1);assert.match(await page.locator('#comment-99001').textContent(),/第一条回复/);
    assert.ok(Math.abs((await page.evaluate(()=>scrollY))-top)<=3);check('Delayed approved replies render the original text under the correct parent while preserving newer input and scroll.');

    await page.locator('[data-comment-reply-cancel]').click();commentMode='invalid';await edit('[data-comment-form]','等待核对的原评论');await page.locator('[data-comment-form] button[type=submit]').click();
    await text('[data-comment-recovery]','结果未确认');const original=writes.filter(w=>w.path.endsWith('/comments')).at(-1);
    await edit('[data-comment-form]','当前账号仍在编辑的内容');commentMode='hold';await page.locator('[data-comment-retry]').click();await until(()=>releaseComment);
    await dispatchSession(fixture.owner_id+100);await dispatchSession(fixture.owner_id);await releaseComment();releaseComment=null;await text('[data-comment-recovery]','账号已变化');
    assert.equal(await page.locator('[data-comment-content]').inputValue(),'当前账号仍在编辑的内容');
    commentMode='confirmed';await page.locator('[data-comment-retry]').click();await page.locator('[data-comment-recovery]').waitFor({state:'hidden'});
    const retries=writes.filter(w=>w.path.endsWith('/comments')).slice(-3);assert.ok(retries.every(w=>w.key===original.key&&w.body.content===original.body.content));
    assert.equal(await page.locator('[data-comment-content]').inputValue(),'当前账号仍在编辑的内容');check('Incomplete and obsolete receipts retain the original key and payload; explicit retry preserves the current draft.');

    await page.evaluate(({owner})=>{
      const key='chunnel.comment./posts/browser-closed',payload={parent_id:0,author_name:'验收读者',email:'reader@example.test',website:'',content:'关闭前的原评论'};
      sessionStorage.setItem(key,JSON.stringify({content:'关闭后的新草稿',parent:'0',at:Date.now()}));
      sessionStorage.setItem(key+'.submission',JSON.stringify({version:1,nonce:'closed-browser-request',owner,payload,uncertain:true}));
    },{owner:fixture.owner_id});
    const countBefore=writes.length;await open('/posts/browser-closed');assert.equal(await page.locator('[data-comment-form]').count(),0);
    assert.equal(await page.locator('[data-comment-saved-draft] textarea').inputValue(),'关闭后的新草稿');assert.equal(writes.slice(countBefore).filter(w=>w.path.includes('/comments')).length,0);
    await page.locator('[data-comment-receipt-query]').click();await text('[data-comment-recovery]','回执尚未确认');receiptMode='confirmed';await page.locator('[data-comment-receipt-query]').click();await text('.comment-notice-success','已确认此前提交');
    assert.equal(await page.locator('[data-comment-saved-draft] textarea').inputValue(),'关闭后的新草稿');assert.equal(writes.filter(w=>w.path.endsWith('/comments/receipt')).length,2);
    check('Closed comments restore readable drafts without a form or automatic resend; receipt-only lookup rejects mismatches.');

    await open('/explore/?sort=updated');const input=page.locator('#explore-query');await input.waitFor();
    const before=await page.locator('[data-discovery-results]').innerHTML();discoveryMode='hold';await input.fill('首页');await input.press('Enter');await until(()=>releaseDiscovery);
    await input.fill('继续输入的新搜索');await releaseDiscovery();releaseDiscovery=null;await page.waitForFunction(()=>!document.querySelector('[data-discovery]').classList.contains('is-loading'));
    assert.equal(await input.inputValue(),'继续输入的新搜索');assert.ok(new URL(page.url()).searchParams.toString().includes(encodeURIComponent('首页')));
    const successful=await page.locator('[data-discovery-results]').innerHTML();discoveryMode='fail';await input.press('Enter');await text('body','内容加载失败');
    assert.ok((await page.locator('[data-discovery-results]').innerHTML()).includes('home-browser'));discoveryMode='real';
    await page.locator('[data-view-toggle=list]').click();assert.equal(new URL(page.url()).searchParams.get('view'),'list');
    assert.ok(successful.length>0&&before.length>0);check('Discovery commits only ready results, preserves ongoing search typing, and retains usable content after a failed fetch.');

    await open('/community/');const initial=await page.locator('.discussion-post-card').evaluateAll(nodes=>nodes.map(node=>node.dataset.postId));
    await page.locator('[data-discussion-sort]').selectOption('oldest',{force:true});assert.deepEqual(await page.locator('.discussion-post-card').evaluateAll(nodes=>nodes.map(node=>node.dataset.postId)),[...initial].reverse());
    assert.ok((await page.locator('[data-community-page]').evaluateAll(nodes=>nodes.map(node=>node.href))).every(href=>new URL(href).searchParams.get('discussion_sort')==='oldest'));
    await page.locator('[data-composer-open]').first().click();await edit('[data-community-composer]','社区中的保留草稿 <tag> 🙂');await page.locator('[data-community-submit]').click();await text('[data-community-composer] .interaction-error','结果暂未确认');
    const publish=writes.filter(w=>w.path==='/api/v1/admin/contents').at(-1);await page.locator('[data-community-submit]').click();await text('[data-community-composer] .interaction-error','结果暂未确认');
    assert.equal(writes.filter(w=>w.path==='/api/v1/admin/contents').at(-1).key,publish.key);
    await page.locator('[data-composer-close]').click();await page.locator('[data-composer-open]').first().click();assert.equal(await page.locator('[data-community-content]').inputValue(),'社区中的保留草稿 <tag> 🙂');
    communityMode='confirmed';await page.locator('[data-composer-editor]').click();await page.waitForURL(`**/admin/content/${fixture.post_id}/edit/`);
    assert.equal(await page.evaluate(id=>sessionStorage.getItem(`chunnel.community.${id}`),fixture.owner_id),null);
    check('Community sorting preserves page filters; uncertain publishing retries reuse the nonce, and confirmed drafts open the editor.');

    await context.clearCookies();await open('/posts/browser-story');await page.waitForFunction(async()=>!(await window.ChunnelInteractions.getSession()).authenticated);
    const favorite=page.locator('article.article [data-favorite]').first();await favorite.click();await page.waitForFunction(()=>JSON.parse(localStorage.getItem('chunnel.preferences'))?.saved.length===1);
    const stored=await page.evaluate(()=>JSON.parse(localStorage.getItem('chunnel.preferences')));assert.equal(stored.saved[0].url,'/posts/browser-story');
    await page.locator('[data-saved-open]').filter({visible:true}).first().click();await page.waitForFunction(()=>document.querySelector('#saved-drawer').open);
    await page.locator('.saved-item > button').first().click();await page.locator('[data-saved-restore]').click();assert.equal(await page.locator('.saved-item').count(),1);
    await page.locator('#saved-drawer button[data-drawer-close]').click();check('Guest bookmarks use canonical post URLs and saved-drawer removal can be undone.');
    await page.evaluate(()=>{Object.defineProperty(navigator,'clipboard',{value:{writeText:async text=>{window.__copiedLink=text;}},configurable:true});});
    await page.locator('article.article [data-share]').first().click();await page.waitForFunction(()=>!!window.__copiedLink);assert.equal(await page.evaluate(()=>window.__copiedLink),fixture.url+'/posts/browser-story');
    check('Sharing copies the canonical public post link.');

    await context.request.post(fixture.url+'/fixture/style/liquid-glass');await context.request.get(fixture.login);await page.setViewportSize({width:390,height:844});await open('/posts/browser-story');await edit('[data-comment-form]','窄屏评论草稿 🙂');
    await page.locator('[data-comment-form]').scrollIntoViewIfNeeded();assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
    await page.screenshot({path:resolve(directory,`${name}-${variant}-glass-comments-mobile.png`)});
    await open('/community/');await page.locator('[data-composer-open]').first().click();await edit('[data-community-composer]','窄屏讨论草稿');await page.screenshot({path:resolve(directory,`${name}-${variant}-glass-community-mobile.png`)});
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));check('Glass comments and community composer fit a 390-pixel viewport and retain editable drafts.');
    assert.deepEqual(errors,[]);assert.ok(!scripts.some(path=>/analytics-react|echarts/i.test(path)));check('Public interactions produce no page errors and load no statistics React/ECharts entry.');
    await writeFile(resolve(directory,`experience-${name}-${variant}.json`),JSON.stringify({evidence:evidence.filter(item=>item.browser===name),errors,writes,scripts},null,2)+'\n');
  }catch(error){await page.screenshot({path:resolve(directory,`${name}-${variant}-failure.png`)}).catch(()=>{});await writeFile(resolve(directory,`experience-${name}-${variant}-failure.json`),JSON.stringify({error:String(error),errors,writes},null,2)+'\n');throw error;}
  finally{await browser.close();}
}
await writeFile(resolve(directory,`experience-acceptance-${variant}.json`),JSON.stringify({evidence},null,2)+'\n');
