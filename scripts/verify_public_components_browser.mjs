// Run against TestPublicComponentsBrowserFixture. Business mutations are
// intercepted, and the fixture verifies its data after an explicit stop.
import assert from 'node:assert/strict';
import {readFile, writeFile, mkdir} from 'node:fs/promises';
import {resolve} from 'node:path';
import {chromium, firefox, webkit} from 'playwright';
import {build} from 'esbuild';

const directory = resolve(process.argv[2]), variant = process.argv[3] || 'generated';
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const engines = {chromium, firefox, webkit}, selected = process.argv[4] ? [process.argv[4]] : Object.keys(engines);
const scripts = new Map(), evidence = [], traffic = [];
if (variant === 'typescript') for (const name of ['components', 'device-mockup']) {
  scripts.set(`newpress-${name}.js`, (await build({entryPoints:[`frontend/core/${name}.ts`], bundle:true, write:false, platform:'browser', format:'iife', target:['chrome100','firefox110','safari16']})).outputFiles[0].text);
}
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(predicate) {
  const deadline = Date.now() + 10000;
  while (!await predicate()) { if (Date.now() > deadline) throw new Error('Expected browser state did not arrive'); await delay(20); }
}
const rootSelector = kind => `aside.npw[data-np-widget="${kind}"]`;

for (const name of selected) {
  const browser = await engines[name].launch({headless:true});
  const context = await browser.newContext({viewport:{width:1280,height:960}, reducedMotion:'reduce', acceptDownloads:true});
  const page = await context.newPage(), errors = [], writes = [];
  page.setDefaultTimeout(15000);
  page.on('pageerror', error => errors.push(error.message));
  const states = new Map(), baseState = kind => structuredClone(fixture.states[`browser_component_${kind}:`]);
  let mode = '', heldWrite, postReads = 0;
  const check = text => {evidence.push({browser:name,check:text}); console.log(`${name}: ${text}`);};
  const reset = () => {mode=''; heldWrite=undefined; states.clear();};
  const visit = async () => {
    await page.goto(fixture.base + fixture.paths.components, {waitUntil:'domcontentloaded'});
    await page.waitForFunction(() => window.NewPressComponents?.count === 11);
  };
  await context.route('**/*', async route => {
    const request=route.request(), url=new URL(request.url());
    if (url.origin !== fixture.base) return route.abort('blockedbyclient');
    if (scripts.has(url.pathname.split('/').pop())) return route.fulfill({contentType:'text/javascript',body:scripts.get(url.pathname.split('/').pop())});
    if (url.pathname.startsWith('/api/v1/article-components/')) {
      const id=url.pathname.split('/').pop(), kind=id.replace('browser_component_','');
      if (request.method()==='GET') {
        postReads++;
        const response=await route.fetch(), data=await response.json();
        data.state=structuredClone(states.get(kind) || baseState(kind));
        if (kind==='redpacket' && !states.has(kind)) data.state.active=true;
        if (mode==='wrong-config' && kind==='poll') data.config='wrong';
        return route.fulfill({response,json:data});
      }
      const body=JSON.parse(request.postData());writes.push({path:url.pathname,body});
      let state=structuredClone(states.get(kind)||baseState(kind));
      if (body.action==='vote') {
        const choice=mode==='wrong-receipt' ? (body.value+1)%state.counts.length : body.value;
        state.counts[choice]++;state.total++;state.choice=choice;state.participated=true;
        state.average=state.counts.reduce((sum,count,index)=>sum+count*index,0)/state.total;
      } else if (body.action==='claim') {state.claimed=1;state.participated=true;state.active=true;}
      else state.active=body.action==='activate';
      if (mode==='lost-before-commit') return route.abort('failed');
      if (mode==='hold') {heldWrite=()=>route.fulfill({json:{state}}).catch(()=>{});return;}
      if (mode!=='wrong-receipt') states.set(kind,state);
      if (mode==='lost-committed') return route.abort('failed');
      return route.fulfill({json:{state}});
    }
    if (['GET','HEAD'].includes(request.method())) return route.fallback();
    writes.push({path:url.pathname,unexpected:true});
    return route.fulfill({status:403,json:{error:'Unexpected write blocked'}});
  });
  // A real ReadableStream exercises fragmented NDJSON, cancel, control races,
  // pause and reconnect in each browser while keeping the worker unreachable.
  await page.addInitScript(() => {
    const original=window.fetch.bind(window), sessions=[], calls=[];
    const devices=[['desktop',1920,1080],['laptop',1440,900],['tablet',768,1024],['phone',390,844]];
    const harness=window.__deviceHarness={sessions,calls,holdControl:false,holdHealth:false,badControl:false,badStream:false};
    const json=data=>new Response(JSON.stringify(data),{headers:{'Content-Type':'application/json'}});
    window.fetch=async (input,options={})=>{
      const path=new URL(typeof input==='string'?input:input.url||input,location.href).pathname;
      if (!path.startsWith('/api/v1/admin/tools/device-mockup/'))return original(input,options);
      if (path.endsWith('/health')) {
        if(harness.holdHealth)return new Promise(resolve=>harness.releaseHealth=()=>resolve(json({ready:false,service:'device-mockup'})));
        return json({ready:true,service:'device-mockup'});
      }
      const body=JSON.parse(options.body);calls.push({path,body});
      if(path.endsWith('/control')){
        if(harness.holdControl)return new Promise(resolve=>harness.releaseControl=()=>resolve(json({paused:body.action==='pause',stopped:false})));
        return json({paused:harness.badControl?'true':body.action==='pause',stopped:false});
      }
      if(harness.badStream)return new Response('{}',{headers:{'Content-Type':'application/json'}});
      const session={index:sessions.length,ended:false};sessions.push(session);
      session.stream=new ReadableStream({start(controller){
        session.controller=controller;
        session.send=event=>{if(!session.ended){const encoded=new TextEncoder().encode(JSON.stringify(event)+'\n');const split=Math.min(19,encoded.length);controller.enqueue(encoded.slice(0,split));controller.enqueue(encoded.slice(split));}};
        session.frame=(id,time,color='#557855')=>{const device=devices.find(item=>item[0]===id),canvas=document.createElement('canvas');canvas.width=device[1];canvas.height=device[2];const c=canvas.getContext('2d');c.fillStyle=color;c.fillRect(0,0,canvas.width,canvas.height);session.send({type:'frame',id,width:canvas.width,height:canvas.height,screenshot:canvas.toDataURL('image/png').split(',')[1],capturedAt:time});};
        session.send({type:'session',id:'12345678-1234-1234-1234-'+String(session.index+1).padStart(12,'0'),interval:1000});
        for(const [id,width,height]of devices){session.frame(id,1000);session.send({type:'device-ready',id,width,height,title:'流式验收',finalUrl:'https://example.test/',status:200,warnings:[],blockedRequests:0});}
        session.send({type:'ready',count:4});
        options.signal?.addEventListener('abort',()=>{session.ended=true;try{controller.error(new DOMException('Aborted','AbortError'));}catch{}},{once:true});
      },cancel(){session.ended=true;}});
      return new Response(session.stream,{headers:{'Content-Type':'application/x-ndjson; charset=utf-8'}});
    };
  });
  try {
    await context.request.get(fixture.base+fixture.reader_login);
    await visit();
    const poll=page.locator(rootSelector('poll')), rating=page.locator(rootSelector('rating'));
    await poll.locator('.npw-results').filter({hasText:'0 人参与'}).waitFor();
    assert.equal(await page.locator('script[src*="newpress-components.js"]').count(),1);
    check('real Go article enhances 11 components and retains readable server markup.');
    await poll.locator('[data-npw-index="1"]').click();
    await poll.locator('.npw-status').filter({hasText:'结果已保存'}).waitFor();
    assert.equal(await poll.locator('[aria-pressed="true"]').getAttribute('data-npw-index'),'1');
    assert.equal(await poll.locator('[data-npw-action="vote"]:enabled').count(),0);
    check('poll submission confirms the exact selected choice and prevents duplicate voting.');
    await rating.locator('[data-npw-index="4"]').click();
    await rating.locator('.npw-result-value').filter({hasText:'4.0'}).waitFor();
    check('rating preserves one-based choices and displays the validated Go average.');

    reset();await visit();mode='lost-committed';
    await poll.locator('[data-npw-index="0"]').click();
    await poll.locator('.npw-status').waitFor();
    await until(async()=>await poll.locator('[data-npw-retry]').isVisible());
    const count=writes.length;mode='';await poll.locator('[data-npw-retry]').click();
    await poll.locator('.npw-status').filter({hasText:'上次操作已确认'}).waitFor();
    assert.equal(writes.length,count);
    check('a lost successful vote is reconciled by reading its state without another mutation.');

    reset();await visit();mode='lost-before-commit';
    await poll.locator('[data-npw-index="1"]').click();await poll.locator('.npw-status').waitFor();
    const original=writes.at(-1),beforeRetry=writes.length;mode='';
    await poll.locator('[data-npw-index="0"]').click();
    await poll.locator('.npw-status').filter({hasText:'核对上次操作'}).waitFor();
    assert.equal(writes.length,beforeRetry);
    await poll.locator('[data-npw-retry]').click();
    await poll.locator('.npw-status').filter({hasText:'结果已保存'}).waitFor();
    assert.deepEqual(writes.at(-1),original);
    check('an uncertain vote retains its original choice and fingerprint until explicitly retried.');

    reset();await visit();mode='wrong-receipt';await poll.locator('[data-npw-index="0"]').click();
    await poll.locator('.npw-status').filter({hasText:'尚未确认'}).waitFor();
    assert.equal(await poll.locator('[aria-pressed="true"]').count(),0);
    check('a valid-looking receipt for another choice never displays success.');

    reset();mode='wrong-config';await visit();await poll.locator('.npw-status').filter({hasText:'已更新'}).waitFor();
    const beforeMismatch=writes.length;await poll.locator('[data-npw-index="0"]').click();await delay(120);
    assert.equal(writes.length,beforeMismatch);
    check('changed component configuration prevents submission with stale terms.');

    reset();await visit();mode='hold';await poll.locator('[data-npw-index="1"]').click();await until(()=>!!heldWrite);
    await page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,id:98765,csrf_token:'changed-session'}})));
    await heldWrite();mode='';await delay(160);
    assert.doesNotMatch(await poll.textContent(),/感谢参与，结果已保存/);
    check('a late receipt after an account change cannot mark the new view as successful.');

    reset();await visit();const packet=page.locator(rootSelector('redpacket'));
    await packet.locator('[data-npw-action="packet"]').click();
    await page.locator('.npw-packet-coin:enabled').waitFor();await page.locator('.npw-packet-coin').click();
    await page.locator('.npw-packet-result').filter({hasText:'已领取'}).waitFor();
    assert.equal((await page.locator('.npw-packet-result-amount').textContent()).trim(),'1.00');
    await page.locator('.npw-packet-close').click();
    check('redpacket claim opens only after a confirmed receipt and preserves the exact amount.');

    const gift=page.locator(rootSelector('gift'));await gift.locator('[data-npw-action="packet"]').click();
    await page.locator('.npw-packet-coin').click();await page.locator('.npw-packet-result-gift').filter({hasText:'测试礼物'}).waitFor();
    await page.locator('.npw-packet-close').click();check('gift dialog remains a local interaction with accessible close and result focus.');
    const tabs=page.locator(rootSelector('tabs'));await tabs.locator('[role="tab"]').nth(1).click();
    assert.equal(await tabs.locator('[role="tab"]').nth(1).getAttribute('aria-selected'),'true');
    check('server details become keyboard-accessible tabs.');
    const chart=page.locator(rootSelector('chart'));await chart.scrollIntoViewIfNeeded();await chart.locator('.npw-chart svg').waitFor();
    assert.ok(await chart.locator('.npw-chart-data').count());check('lazy chart keeps its accessible data table.');
    const compare=page.locator(rootSelector('compare'));await compare.locator('input').evaluate(input=>{input.value='73';input.dispatchEvent(new Event('input',{bubbles:true}));});
    assert.equal(await compare.locator('.npw-compare').evaluate(node=>node.style.getPropertyValue('--npw-compare')),'73%');
    check('comparison slider retains native input and pointer-driven layout.');
    const carousel=page.locator(rootSelector('carousel'));await carousel.scrollIntoViewIfNeeded();await carousel.locator('[data-npw-action="next"]').click();
    await carousel.locator('.npw-carousel-count').filter({hasText:'2 / 2'}).waitFor();
    await carousel.locator('[data-npw-action="lightbox"]').nth(1).click();await page.locator('.npw-lightbox[open]').waitFor();await page.locator('.npw-lightbox button').click();
    check('carousel navigation and local image lightbox remain usable.');
    const video=page.locator(rootSelector('video'));await video.locator('[data-npw-action="play"]').click();
    assert.equal(await video.locator('iframe').count(),1);await video.getByRole('button',{name:'重新加载',exact:true}).click();assert.equal(await video.locator('iframe').count(),1);
    await video.getByRole('button',{name:'取消加载',exact:true}).click();assert.equal(await video.locator('iframe').count(),0);
    check('external media is explicit, retry replaces its frame, and cancel restores the launcher.');
    const mention=page.locator(rootSelector('mention'));await mention.locator('.npw-mention-link').hover();
    await mention.locator('.npw-user-meta').filter({hasText:'本站用户'}).waitFor();check('mention profile consumes the real Go site-user response.');
    const code=page.locator(rootSelector('code'));await code.scrollIntoViewIfNeeded();await code.locator('.hljs-keyword').first().waitFor();
    assert.match(await code.locator('.npw-code-language').textContent(),/TypeScript/);check('code component uses the actual revisioned worker and renders safe highlighted tokens.');
    await page.evaluate(()=>{dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true}));dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));});
    await carousel.scrollIntoViewIfNeeded();await carousel.locator('[data-npw-action="previous"]').click();await carousel.locator('.npw-carousel-count').filter({hasText:'1 / 2'}).waitFor();
    check('persisted page restoration reinstates observers and carousel controls once.');

    reset();await page.goto(fixture.base+fixture.paths.shop,{waitUntil:'domcontentloaded'});
    await page.waitForFunction(()=>window.NewPressComponents?.count===3);
    await poll.locator('[data-npw-index="1"]').click();await poll.locator('.npw-status').filter({hasText:'结果已保存'}).waitFor();
    assert.match(writes.at(-1).path,/\/shop:component-product\//);
    await tabs.locator('[role="tab"]').nth(1).click();assert.equal(await tabs.locator('[role="tab"]').nth(1).getAttribute('aria-selected'),'true');
    await code.scrollIntoViewIfNeeded();await code.locator('.hljs-keyword').first().waitFor();
    check('real product page uses its shop target for voting while retaining tabs and code highlighting.');

    await context.request.get(fixture.base+fixture.login);reset();await visit();
    await packet.scrollIntoViewIfNeeded();await packet.locator('[data-npw-action="pause"]').click();
    await packet.locator('.npw-status').filter({hasText:'已暂停发放'}).waitFor();
    await packet.locator('[data-npw-action="activate"]').click();await packet.locator('.npw-status').filter({hasText:'已启用'}).waitFor();
    check('packet owner can pause and explicitly reactivate using confirmed receipts.');

    await context.clearCookies();reset();await visit();
    await poll.locator('[data-npw-index="0"]').click();await poll.locator('.npw-status').filter({hasText:'结果已保存'}).waitFor();
    assert.ok((await context.cookies()).some(cookie=>cookie.name==='np_widget_visitor'));
    await packet.locator('[data-npw-action="packet"]').click();await page.locator('.npw-packet-coin:enabled').waitFor();await page.locator('.npw-packet-coin').click();
    await page.locator('.npw-packet-login').waitFor();await page.locator('.npw-packet-close').click();
    check('anonymous polls retain the actual visitor cookie and anonymous redpacket claims show login.');
    for(const width of [390,1280]){
      await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:width===390?'light':'dark'});
      await page.evaluate(()=>{document.activeElement?.blur();scrollTo({top:0,behavior:'instant'});});
      await page.screenshot({path:resolve(directory,`${variant}-${name}-components-${width}.png`),fullPage:true});
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),true);
    }
    check('public components fit narrow and desktop real pages in both color schemes.');

    await context.request.get(fixture.base+fixture.login);
    await page.goto(fixture.base+fixture.paths.device,{waitUntil:'domcontentloaded'});
    await page.locator('.dm-device-item').nth(3).waitFor();
    assert.equal(await page.locator('#saveCompositeButton').isEnabled(),true);
    await page.locator('[data-color="#ffffff"]').click();assert.equal(await page.locator('#stageBackground').getAttribute('fill'),'#ffffff');
    await page.locator('[data-finish="silver"]').click();assert.equal(await page.locator('[data-finish="silver"]').getAttribute('aria-pressed'),'true');
    await page.locator('#exportScale').selectOption('1');
    const [download]=await Promise.all([page.waitForEvent('download'),page.locator('#saveCompositeButton').click()]);
    const file=resolve(directory,`${variant}-${name}-composition.png`);await download.saveAs(file);const png=await readFile(file);
    assert.equal(png.readUInt32BE(16),1600);assert.equal(png.readUInt32BE(20),1000);check('real SVG composition exports a 1600 x 1000 PNG with selected background and finish.');
    await page.locator('#includeFrame').uncheck();
    const [deviceDownload]=await Promise.all([page.waitForEvent('download'),page.locator('[data-device="phone"] button').click()]);
    const phoneFile=resolve(directory,`${variant}-${name}-phone.png`);await deviceDownload.saveAs(phoneFile);const phone=await readFile(phoneFile);
    assert.equal(phone.readUInt32BE(16),390);assert.equal(phone.readUInt32BE(20),844);check('single-device PNG uses its exact native viewport dimensions.');
    const connect=async value=>{await page.locator('#siteUrl').fill(value);await page.locator('#renderButton').click();await page.locator('#previewBadge').filter({hasText:'实时预览'}).waitFor();};
    await connect('example.test');assert.equal(await page.locator('#image-phone').getAttribute('data-frame-at'),'1000');
    assert.equal(await page.locator('#stageLoading').isHidden(),true);check('fragmented NDJSON initializes all four devices before ending the loading state.');
    await page.locator('#pauseLiveButton').click();await page.locator('#previewBadge').filter({hasText:'已暂停'}).waitFor();
    await page.evaluate(()=>window.__deviceHarness.sessions.at(-1).frame('phone',2000));assert.equal(await page.locator('#image-phone').getAttribute('data-frame-at'),'1000');
    await page.locator('#pauseLiveButton').click();await page.evaluate(()=>window.__deviceHarness.sessions.at(-1).frame('phone',3000));
    await until(async()=>await page.locator('#image-phone').getAttribute('data-frame-at')==='3000');
    await page.evaluate(()=>window.__deviceHarness.sessions.at(-1).frame('phone',2500));assert.equal(await page.locator('#image-phone').getAttribute('data-frame-at'),'3000');
    check('pause freezes frames, resume accepts new frames, and older timestamps cannot overwrite them.');
    await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});
    await until(async()=>await page.evaluate(()=>window.__deviceHarness.calls.at(-1).body.action==='pause'));
    await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));});
    await until(async()=>await page.evaluate(()=>window.__deviceHarness.calls.at(-1).body.action==='resume'));
    check('visibility changes pause and resume the server without changing the user pause preference.');
    await page.evaluate(()=>window.__deviceHarness.holdControl=true);await page.locator('#pauseLiveButton').click();
    await until(async()=>await page.evaluate(()=>!!window.__deviceHarness.releaseControl));
    await connect('second.example.test');await page.evaluate(()=>{window.__deviceHarness.holdControl=false;window.__deviceHarness.releaseControl();});
    assert.equal(await page.locator('#previewBadge').textContent(),'实时预览');
    assert.match(await page.locator('#captureTitle').textContent(),/second.example.test/);
    check('reconnect aborts the old stream and ignores its delayed control receipt.');
    await page.evaluate(()=>window.__deviceHarness.badControl=true);await page.locator('#pauseLiveButton').click();
    await page.locator('#renderMessage').filter({hasText:'数据无效'}).waitFor();assert.equal(await page.locator('#previewBadge').textContent(),'已定格');
    assert.equal(await page.locator('#saveCompositeButton').isEnabled(),true);check('malformed control receipt ends the stream and retains exportable final frames.');
    await page.evaluate(()=>window.__deviceHarness.badControl=false);await connect('third.example.test');
    await page.evaluate(()=>window.__deviceHarness.sessions.at(-1).send({type:'frame',id:'other',width:1,height:1,screenshot:'x',capturedAt:1}));
    await page.locator('#renderMessage').filter({hasText:'数据无效'}).waitFor();check('unknown device frames are rejected without corrupting the four previews.');
    await connect('fourth.example.test');await page.locator('#stopLiveButton').click();assert.equal(await page.locator('#previewBadge').textContent(),'已定格');
    await page.evaluate(()=>{dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true}));dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));});
    assert.equal(await page.locator('#saveCompositeButton').isEnabled(),true);check('stop and persisted-page lifecycle retain the last frame without reconnecting automatically.');
    await page.evaluate(()=>window.__deviceHarness.badStream=true);await page.locator('#renderButton').click();await page.locator('#renderMessage').filter({hasText:'流格式无效'}).waitFor();
    check('non-NDJSON success responses fail visibly and restore retry controls.');
    for(const width of [390,1280]) {
      await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:width===390?'light':'dark'});
      await page.evaluate(()=>{document.activeElement?.blur();scrollTo({top:0,behavior:'instant'});});
      await page.screenshot({path:resolve(directory,`${variant}-${name}-device-${width}.png`),fullPage:true});
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth <= innerWidth+1),true);
    }
    check('device workspace fits desktop and narrow viewports in light and dark modes.');
    assert.deepEqual(errors,[]);assert.equal(writes.filter(item=>item.unexpected).length,0);
    traffic.push({browser:name,writes,componentReads:postReads,device:await page.evaluate(()=>window.__deviceHarness.calls)});
  } finally {await context.close();await browser.close();}
}
await mkdir(directory,{recursive:true});
await writeFile(resolve(directory,`${variant}-browser-acceptance.json`),JSON.stringify({variant,evidence,traffic},null,2)+'\n');
console.log(`${variant}: ${evidence.length} checks passed.`);
