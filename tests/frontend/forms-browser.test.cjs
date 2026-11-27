const test=require('node:test');
const assert=require('node:assert/strict');
const {readFileSync}=require('node:fs');
const {chromium}=require('playwright');
const {compile}=require('./helpers/typescript.cjs');
const input=(name,value='')=>`<input name="${name}" value="${value}">`;
const checks=names=>names.map(name=>`<input type="checkbox" name="${name}" checked>`).join('');
const settings=`<section data-feature-admin data-slug="ideas" data-version="3" data-user-id="1" data-csrf="token"><p data-feedback-status></p><div class="feature-preview-paper"><span data-live-name></span><span data-live-description></span><span data-live-submit></span><span data-preview-slug></span><span data-live-features></span></div><form id="settings" data-feedback-form="configure">
${input('slug','ideas')}${input('name','想法')}${input('description')}${input('guidelines')}${input('submit_label','提交反馈')}${input('accent','teal')}${input('page_size','20')}${input('categories','建议')}
${checks(['enabled','submissions','voting','comments','official_replies','roadmap','history','moderation','private_submissions'])}<select name="default_view"><option value="list">列表</option><option value="roadmap" selected>看板</option></select><input name="kinds" type="checkbox" value="suggestion" checked>
<div data-grant-role="subscriber">${['read','submit','vote','comment','moderate'].map(key=>`<input type="checkbox" data-grant="${key}" checked>`).join('')}</div><div data-admin-savebar data-form-id="settings"><span data-save-state></span><button type="submit" data-admin-save>保存</button><button type="button" data-admin-discard>放弃</button></div></form></section>`;
const feedback=`<section data-feedback-page data-slug="ideas" data-version="3" data-user-id="1" data-csrf="token"><p data-feedback-status></p><div data-feedback-conflict hidden><strong>版本冲突</strong><a data-feedback-latest>查看最新</a></div>
<form id="submission" data-feedback-form="submit">${input('title','新建议标题')}<textarea name="body">原始建议正文</textarea>${input('kind','suggestion')}<button type="submit">提交</button><p data-form-status></p></form>
<form id="reply" data-feedback-form="reply" data-item="5"><textarea name="body">另一处回复</textarea><button type="submit">回复</button><p data-form-status></p></form>
<button data-feedback-action="vote" data-item="5" data-enabled="true" aria-pressed="false" aria-label="支持：建议"><b>1</b><small>支持</small></button></section>`;
const invitation=`<section data-invitations data-user-id="1" data-csrf="token"><form><input type="email" name="email" required><button type="submit" disabled>发送邀请</button></form><p data-invitation-status></p><div data-invitation-list></div><button type="button" data-invitation-refresh>刷新</button></section>`;
const native=`<form id="owner" class="admin-login-form"><input name="username" value="保留输入"><button type="submit">登录</button></form>`;
const id='a'.repeat(64),pending={id,email:'first@example.test',expires_at:'2026-12-03T12:00:00+08:00',state:'pending'};
const invites=items=>({items:items??[pending],available:true,registration:'invite'});
const config=scene=>({scene,revision:'fixture',enabled:true,provider:'local',theme:'auto',language:'zh-CN'});
const until=async predicate=>{const deadline=Date.now()+8000;while(!predicate()){if(Date.now()>deadline)throw new Error('Expected intercepted request missing');await new Promise(resolve=>setTimeout(resolve,10));}};

for(const variant of ['typescript','generated'])test(`forms browser: ${variant}`,async t=>{
  const browser=await chromium.launch();t.after(()=>browser.close());
  const codes=Object.fromEntries(['feedback','invitations','verification'].map(name=>[name,variant==='typescript'?compile(`frontend/shared/${name}.ts`):readFileSync(`internal/coreui/assets/js/chunnel-${name}.js`,'utf8')]));
  const admin=compile('frontend/core/admin-ui.ts');
  async function fixture(s,name,handle){
    const context=await browser.newContext();s.after(()=>context.close());
    const page=await context.newPage(),calls=[],errors=[];let loads=0;
    page.setDefaultTimeout(8000);page.on('pageerror',error=>errors.push(error.message));page.on('dialog',dialog=>dialog.accept());s.after(()=>assert.deepEqual(errors,[]));
    await context.route('**/*',async route=>{
      const request=route.request(),url=new URL(request.url());
      if(url.pathname.endsWith('.js'))return route.fulfill({contentType:'text/javascript',body:url.pathname==='/admin.js'?admin:codes[url.pathname.slice(1,-3)]});
      if(!url.pathname.startsWith('/api/')){
        if(request.resourceType()!=='document')return route.fulfill({status:204});loads++;
        const markup={settings,feedback,invitation,native}[name];
        const scripts=name==='invitation'?['invitations']:name==='native'?['verification']:['verification','feedback'];
        return route.fulfill({contentType:'text/html; charset=utf-8',body:`<!doctype html><meta charset="utf-8">${markup}<script src="/admin.js"></script>${scripts.map(script=>`<script src="/${script}.js"></script>`).join('')}`});
      }
      const call={path:url.pathname,query:url.searchParams,method:request.method(),headers:request.headers(),body:request.postData()?request.postDataJSON():undefined};calls.push(call);
      let result=await handle?.(call);
      if(result===undefined){
        if(call.path.endsWith('/config'))result={data:{...config(call.query.get('scene')),enabled:name==='native'}};
        else if(call.path.endsWith('/challenge'))result={data:{id:`challenge-${calls.length}`,image:'data:image/png;base64,YQ==',expires_in:120}};
        else if(call.path.endsWith('/verify'))result={data:{token:'one-use',expires_in:120}};
        else result={data:invites()};
      }
      return route.fulfill({status:result.status||200,contentType:'application/json',body:JSON.stringify(result.data)});
    });
    await page.goto('http://localhost:41862/'+name);await page.waitForLoadState('networkidle');
    const text=(selector,value)=>page.waitForFunction(({selector,value})=>document.querySelector(selector)?.textContent.includes(value),{selector,value});
    return{page,calls,text,loads:()=>loads,writes:()=>calls.filter(call=>call.method==='POST'&&!call.path.includes('/verification/')),save:selector=>page.locator(selector).evaluate(form=>form.requestSubmit())};
  }
  await t.test('settings preview, roadmap fallback and role grants stay consistent with the command',async s=>{
    const f=await fixture(s,'settings',call=>call.path.startsWith('/api/v1/feedback/')?{data:{slug:'ideas',version:4}}:undefined);
    await f.page.locator('[name=name]').fill('改进建议');await f.text('[data-live-name]','改进建议');
    await f.page.locator('[data-grant=read]').uncheck();assert.equal(await f.page.locator('[data-grant]:checked').count(),0);
    await f.page.locator('[data-grant=vote]').check();assert.equal(await f.page.locator('[data-grant=read]').isChecked(),true);
    await f.page.locator('[name=roadmap]').uncheck();assert.equal(await f.page.locator('[name=default_view]').inputValue(),'list');
    await f.save('#settings');await f.page.waitForFunction(()=>document.querySelector('[data-feature-admin]').dataset.version==='4');
    const saved=f.writes()[0].body.settings;assert.equal(saved.default_view,'list');assert.deepEqual(saved.permissions,[{role:'subscriber',read:true,submit:false,vote:true,comment:false,moderate:false}]);assert.equal(f.loads(),1);
  });
  await t.test('malformed votes retain their key and never advance the board or UI',async s=>{
    let valid=false;const f=await fixture(s,'feedback',call=>call.body?.action==='vote'?{data:{slug:'ideas',version:9,item_id:5,vote_count:valid?2:-1,voted:true}}:undefined);
    await f.page.locator('[data-feedback-action]').click();await f.text('[data-feedback-status]','响应');
    assert.equal(await f.page.locator('[data-feedback-page]').getAttribute('data-version'),'3');assert.equal(await f.page.locator('[data-feedback-action]').getAttribute('aria-pressed'),'false');
    valid=true;await f.page.locator('[data-feedback-action]').click();await f.text('[data-feedback-status]','已支持');assert.deepEqual(f.writes()[0].body,f.writes()[1].body);
    assert.equal(await f.page.locator('[data-feedback-action] b').textContent(),'2');
  });
  await t.test('unchanged feedback retries keep identity after a newer vote and preserve another draft',async s=>{
    let valid=false;const f=await fixture(s,'feedback',call=>{
      if(call.body?.action==='submit')return{data:valid?{slug:'ideas',version:4,item_id:5,replay:true}:null};
      if(call.body?.action==='vote')return{data:{slug:'ideas',version:10,item_id:5,vote_count:2,voted:true}};
    });
    await f.page.locator('#reply [name=body]').fill('另一处未保存的草稿');await f.save('#submission');await f.text('[data-feedback-status]','响应');
    const original=f.writes()[0].body;await f.page.locator('[data-feedback-action]').click();await f.text('[data-feedback-status]','已支持');valid=true;
    await f.save('#submission');await f.text('[data-feedback-status]','另一处表单');await f.save('#submission');await f.text('[data-feedback-status]','另一处表单');
    const submitted=f.writes().filter(call=>call.body.action==='submit');assert.equal(submitted.length,3);for(const call of submitted)assert.deepEqual(call.body,original);
    assert.equal(await f.page.locator('[data-feedback-page]').getAttribute('data-version'),'10');assert.equal(await f.page.locator('#reply [name=body]').inputValue(),'另一处未保存的草稿');assert.equal(f.loads(),1);
  });
  await t.test('account change cancels active feedback verification and retains both drafts',async s=>{
    const f=await fixture(s,'feedback',call=>call.path.endsWith('/config')?{data:config(call.query.get('scene'))}:undefined);
    await f.page.locator('#submission [name=body]').fill('请保留这段内容');await f.save('#submission');await f.page.locator('.verification-local input').waitFor();
    await f.page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,id:2}})));
    await f.text('[data-feedback-status]','登录账号已变化');assert.equal(await f.page.locator('.verification-box').count(),0);assert.equal(f.writes().length,0);assert.equal(await f.page.locator('#submission [name=body]').inputValue(),'请保留这段内容');assert.equal(await f.page.locator('#submission button').isDisabled(),true);
  });
  await t.test('invitation refresh ignores late older data',async s=>{
    let reads=0,release;const f=await fixture(s,'invitation',()=>++reads===2?new Promise(resolve=>release=()=>resolve({data:invites()})):{data:invites(reads>2?[{...pending,email:'latest@example.test'}]:undefined)});
    await f.page.locator('[data-invitation-refresh]').click();await until(()=>release);await f.page.locator('[data-invitation-refresh]').click();await f.text('[data-invitation-list]','latest@example.test');release();
    await f.page.waitForLoadState('networkidle');assert.equal(await f.page.locator('[data-invitation-list] strong').textContent(),'latest@example.test');
  });
  await t.test('unconfirmed invitation sends require refresh before any repeat write',async s=>{
    const f=await fixture(s,'invitation',call=>call.method==='POST'?{data:{}}:undefined);
    await f.page.locator('[name=email]').fill('new@example.test');await f.save('form');await f.text('[data-invitation-status]','尚未确认');
    assert.equal(await f.page.locator('button[type=submit]').isDisabled(),true);assert.equal(await f.page.locator('[data-invitation-list] button').isDisabled(),true);assert.equal(await f.page.locator('[name=email]').inputValue(),'new@example.test');
    await f.save('form');assert.equal(f.writes().length,1);await f.page.locator('[data-invitation-refresh]').click();await f.page.waitForFunction(()=>!document.querySelector('button[type=submit]').disabled);
  });
  await t.test('confirmed send survives refresh failure, preserves new typing and uses refreshed CSRF',async s=>{
    let release,fail=false;const f=await fixture(s,'invitation',call=>call.method==='POST'?new Promise(resolve=>release=()=>{fail=true;resolve({data:{ok:true}});}):fail?{status:503,data:{error:{message:'读取失败'}}}:undefined);
    await f.page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,id:1,csrf_token:'rotated'}})));
    await f.page.locator('[name=email]').fill('sent@example.test');await f.save('form');await until(()=>release);await f.page.locator('[name=email]').fill('next@example.test');
    assert.equal(await f.page.locator('[data-invitation-refresh]').isDisabled(),true);await f.save('form');release();await f.text('[data-invitation-status]','列表暂时无法刷新');
    assert.match(await f.page.locator('[data-invitation-status]').textContent(),/邮件已交给/);assert.equal(f.writes().length,1);assert.equal(f.writes()[0].headers['x-csrf-token'],'rotated');assert.equal(await f.page.locator('[name=email]').inputValue(),'next@example.test');
    fail=false;await f.page.locator('[data-invitation-refresh]').click();await f.page.waitForFunction(()=>!document.querySelector('button[type=submit]').disabled);
  });
  await t.test('confirmed revoke stays closed through a failed or stale list refresh',async s=>{
    let fail=false;const f=await fixture(s,'invitation',call=>{if(call.method==='POST'){fail=true;return{data:{ok:true}};}if(fail)return{status:503,data:{error:{message:'读取失败'}}};});
    await f.page.locator('[data-invitation-list] button').click();await f.text('[data-invitation-status]','邀请已撤销');assert.equal(await f.page.locator('[data-invitation-list] button').count(),0);
    fail=false;await f.page.locator('[data-invitation-refresh]').click();await f.text('[data-invitation-list]','已使用或撤销');assert.equal(await f.page.locator('[data-invitation-list] button').count(),0);assert.equal(f.writes().length,1);
  });
  await t.test('permission loss clears private invitation rows while preserving the email draft',async s=>{
    let denied=false;const f=await fixture(s,'invitation',()=>denied?{status:403,data:{error:{message:'权限已失效'}}}:undefined);
    await f.page.locator('[name=email]').fill('draft@example.test');denied=true;await f.page.locator('[data-invitation-refresh]').click();await f.text('[data-invitation-status]','登录状态已变化');
    assert.equal(await f.page.locator('[data-invitation-list]').textContent(),'');assert.equal(await f.page.locator('[name=email]').inputValue(),'draft@example.test');assert.equal(await f.page.locator('[data-invitation-refresh]').isDisabled(),true);
  });
  await t.test('local verification reload uses the newest challenge, ignores IME Enter and submits once',async s=>{
    const f=await fixture(s,'native');await f.page.evaluate(()=>{window.completed=[];document.querySelector('form').addEventListener('submit',event=>{event.preventDefault();window.completed.push(new FormData(event.target).get('verification_token'));});});
    await f.save('form');await f.save('form');await f.page.locator('.verification-local input').waitFor();await f.page.getByRole('button',{name:'换一张'}).click();
    await until(()=>f.calls.filter(call=>call.path.endsWith('/challenge')).length===2);await f.page.locator('.verification-local input').fill('12345');await f.page.locator('.verification-local input').dispatchEvent('keydown',{key:'Enter',isComposing:true,keyCode:229});
    assert.equal(f.calls.filter(call=>call.path.endsWith('/verify')).length,0);await f.page.getByRole('button',{name:'确认验证'}).click();
    await f.page.waitForFunction(()=>window.completed.length===1);assert.deepEqual(await f.page.evaluate(()=>window.completed),['one-use']);
    const challenge=f.calls.filter(call=>call.path.endsWith('/challenge')).at(-1);assert.equal(f.calls.find(call=>call.path.endsWith('/verify')).body.proof.challenge_id,`challenge-${f.calls.indexOf(challenge)+1}`);assert.equal(await f.page.locator('.verification-box').count(),0);
  });
  await t.test('detaching the verification owner aborts its request and ignores late configuration',async s=>{
    let release;const f=await fixture(s,'native',call=>call.path.endsWith('/config')?new Promise(resolve=>release=()=>resolve({data:config('login')})):undefined);
    await f.page.evaluate(()=>{const owner=document.querySelector('form');window.ChunnelVerification.getToken(owner,'login').then(()=>window.outcome='resolved',error=>window.outcome=error.code);});await until(()=>release);
    await f.page.locator('form').evaluate(form=>form.remove());await f.page.waitForFunction(()=>window.outcome==='verification.cancelled');release();await f.page.waitForLoadState('networkidle');
    assert.equal(f.calls.length,1);assert.equal(await f.page.locator('.verification-box').count(),0);
  });
});
