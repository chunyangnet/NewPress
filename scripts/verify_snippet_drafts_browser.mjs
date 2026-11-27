// Isolated real editor fixture. Writes target only the loopback test server.
import assert from 'node:assert/strict';
import {readFile,writeFile,mkdir} from 'node:fs/promises';
import {resolve} from 'node:path';
import {chromium} from 'playwright';

const directory=resolve(process.argv[2]);
const fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const screenshots=resolve(process.argv[3]||'output/playwright/snippet-drafts-20261126');
await mkdir(screenshots,{recursive:true});
assert.match(fixture.url,/^http:\/\/127\.0\.0\.1:\d+$/);
const browser=await chromium.launch({headless:true});
const context=await browser.newContext({viewport:{width:1280,height:1000},reducedMotion:'reduce'});
const page=await context.newPage();page.setDefaultTimeout(15000);
const errors=[],checks=[],submissions=[];
page.on('pageerror',e=>errors.push(e.message));page.on('dialog',d=>d.accept());
const check=label=>{checks.push(label);console.log(label);};
try{
  await page.goto(fixture.url+'/__snippet_draft_login');
  await page.locator('[name="title"]').fill('尚未保存的标题，须留在原页面');
  if(!await page.locator('[data-snippet-draft-open]').isVisible())await page.locator('.editor-page-more > summary').click();
  await page.locator('[data-snippet-draft-open]').click();
  const dialog=page.locator('[data-snippet-draft-dialog]');
  await page.locator('[data-snippet-draft-preview]').click();
  await page.locator('[data-snippet-draft-content]').waitFor({state:'visible'});
  assert.equal(await page.locator('[data-snippet-draft-confirm]').isDisabled(),true);
  assert.match(await page.locator('[data-snippet-draft-title]').textContent(),/独立草稿验收文章/);
  assert.equal(await page.locator('[name="title"]').inputValue(),'尚未保存的标题，须留在原页面');
  assert.match(await page.locator('[data-snippet-draft-source]').textContent(),/固定版本/);
  assert.doesNotMatch(await page.locator('[data-snippet-draft-source]').textContent(),/data-np-snippet/);
  check('saved version preview, pinned expansion and unsaved title preserved');
  await writeFile(resolve(directory,'preview-dom.txt'),await page.locator('body').ariaSnapshot());
  for(const variant of [{name:'desktop-light',width:1280,height:1000,mode:'light'},{name:'desktop-dark',width:1280,height:1000,mode:'dark'},{name:'mobile-dark',width:390,height:844,mode:'dark'}]){
    await page.setViewportSize({width:variant.width,height:variant.height});
    await page.evaluate(mode=>window.ChunnelColorMode.set(mode),variant.mode);
    await page.emulateMedia({colorScheme:variant.mode});
    const bounds=await dialog.boundingBox();assert.ok(bounds.x>=-1&&bounds.width<=variant.width+1);
    await page.screenshot({path:resolve(screenshots,variant.name+'.png'),fullPage:true});
    check(variant.name+': dialog stays within viewport');
  }
  await page.setViewportSize({width:1280,height:1000});
  await page.locator('[data-snippet-draft-consent]').check();
  let lost=true;
  await page.route('**/api/v1/admin/contents/*/snippet-draft',async route=>{
    submissions.push(route.request().postDataJSON());
    if(lost){lost=false;const response=await route.fetch();assert.equal(response.status(),201);await route.abort('failed');}
    else await route.continue();
  });
  await page.locator('[data-snippet-draft-confirm]').click();
  await page.waitForFunction(()=>document.querySelector('[data-snippet-draft-status]').textContent.includes('尚未确认'));
  assert.equal(await page.locator('[data-snippet-draft-preview]').isDisabled(),true);
  assert.equal(await page.locator('[name="title"]').inputValue(),'尚未保存的标题，须留在原页面');
  check('committed response dropped: retry identity retained, no duplicate preview');
  const status=await page.evaluate(async fixture=>{
    const session=await (await fetch('/api/v1/admin/session')).json();
    const response=await fetch(`/api/v1/admin/snippets/${fixture.snippet_id}/withdraw`,{method:'POST',headers:{'X-CSRF-Token':session.csrf_token,'If-Match':`"snippet-${fixture.snippet_id}-v${fixture.snippet_version}"`}});
    return response.status;
  },fixture);assert.equal(status,200);
  await page.locator('[data-snippet-draft-close]').click();
  if(!await page.locator('[data-snippet-draft-open]').isVisible())await page.locator('.editor-page-more > summary').click();
  await page.locator('[data-snippet-draft-open]').click();
  await page.locator('[data-snippet-draft-confirm]').focus();await page.keyboard.press('Enter');
  await page.locator('[data-snippet-draft-result]').waitFor({state:'visible'});
  assert.equal(submissions.length,2);assert.deepEqual(submissions[0],submissions[1]);
  assert.equal(await page.locator('[name="title"]').inputValue(),'尚未保存的标题，须留在原页面');
  const href=await page.locator('[data-snippet-draft-result]').getAttribute('href');assert.match(href,/^\/admin\/content\/\d+\/edit\/$/);
  assert.equal(await page.locator('[data-snippet-draft-result]').getAttribute('target'),'_blank');
  check('keyboard replay after withdrawal returns one existing draft and preserves original input');
  await page.screenshot({path:resolve(screenshots,'confirmed.png'),fullPage:true});
  await page.locator('[data-snippet-draft-close]').click();
  await page.waitForFunction(()=>document.activeElement===document.querySelector('[data-snippet-draft-open]'));
  const draft=await context.newPage();await draft.goto(fixture.url+href);
  assert.match(await draft.locator('[data-content-source]').inputValue(),/固定版本/);
  assert.doesNotMatch(await draft.locator('[data-content-source]').inputValue(),/data-np-snippet/);
  check('draft opens as ordinary editable HTML in a new page; dialog returns focus');
  assert.deepEqual(errors,[]);
  await writeFile(resolve(directory,'browser-result.json'),JSON.stringify({checks,page_errors:errors,screenshots,submission_attempts:submissions.length,identical_replay:true},null,2));
  await writeFile(resolve(directory,'stop'),'verified');
}finally{await context.close();await browser.close();}
