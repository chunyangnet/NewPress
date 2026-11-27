const test = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const { chromium } = require('playwright');
const { compile } = require('./helpers/typescript.cjs');
const origin = 'http://localhost:41861';
const bar = id => `<div data-admin-savebar data-form-id="${id}"><span data-save-state></span><button type="submit" data-admin-save>保存</button><button type="button" data-admin-discard>放弃</button></div>`;
const input = (name, value = '') => `<input name="${name}" value="${value}">`;
const checks = names => names.map(name => `<input name="${name}" type="checkbox" checked>`).join('');
const links = `<section data-links-admin data-user-id="1" data-csrf="token" data-slug="friends" data-version="3"><p data-links-status hidden></p><div data-links-conflict hidden><a>重载</a></div>
  <form id="settings" data-links-form="configure">${input('slug', 'friends')}${input('name', '我的友链')}${input('description')}<input type="radio" name="style" value="cards" checked><input type="radio" name="style" value="wall">${input('accent', 'teal')}${checks(['enabled', 'search', 'switcher', 'new_tab'])}${bar('settings')}</form>
  <form id="item" data-links-form="save_item" data-item-id="1">${input('name', '原始名称')}${input('url', 'example.test')}${input('description')}${input('category')}${input('logo')}${input('logo_url')}${input('order', '1')}${checks(['enabled', 'featured'])}<button type="button" data-links-fetch>提取</button><div class="links-logo-editor"><span data-links-logo-preview></span><button type="button" data-links-logo-clear>清除</button></div>${bar('item')}</form><button type="button" data-links-delete="1" data-name="站点">删除</button></section>`;
const series = `<form id="series" data-series-editor data-user-id="1" data-id="7" data-version="2">${input('csrf_token', 'token')}<input type="hidden" data-series-selection data-admin-value><fieldset class="series-editor-fields">${input('kind', 'series')}${input('status', 'draft')}${input('title', '选集')}${input('slug', 'reading')}${input('description')}${input('cover_url')}
  <ol data-series-chapters><li data-series-chapter data-id="1" data-status="publish"><span data-series-chapter-title>第一篇</span></li><li data-series-chapter data-id="2" data-status="draft"><span data-series-chapter-title>第二篇</span></li></ol><p data-series-count></p><input data-series-search data-admin-transient><button type="button" data-series-find>查找</button><p data-series-picker-status></p><div data-series-sources></div><button type="button" data-series-source-prev hidden>上一页</button><button type="button" data-series-source-next hidden>下一页</button><p data-series-status></p>${bar('series')}<button type="button" data-series-reload>重载</button><button type="button" data-series-delete>删除</button></fieldset></form>`;
const circles = `<section data-circle-page data-user-id="1" data-csrf="token" data-slug="makers" data-version="7"><p data-circle-status hidden></p><div data-circle-conflict hidden><p data-circle-latest></p><button type="button" data-circle-retry>使用新版本重试</button></div>
  <form data-circle-command="topic">${input('title', '标题')}<label>正文<textarea name="body">原始正文</textarea></label>${input('kind', 'discussion')}<p data-form-status></p><button type="submit">发布话题</button></form>
  <form data-circle-command="settings">${input('name', '圈子')}${input('description')}${input('rules')}${input('visibility', 'members')}${input('join_policy', 'open')}${input('post_policy', 'members')}<button type="submit">保存设置</button></form>
  <button type="button" data-circle-members-open>查看成员</button><dialog data-circle-members-dialog><button type="button" data-circle-members-close>关闭</button><input data-member-search><button type="button" data-member-filter="all"><span data-member-filter-count></span>全部</button><button type="button" data-member-filter="member"><span data-member-filter-count></span>成员</button><div data-circle-member-list><div data-circle-member-row data-member-role="owner" data-member-query="圈主">圈主</div><div data-circle-member-row data-member-role="member" data-member-query="读者">读者</div><p data-member-filter-empty hidden>没有匹配</p></div></dialog></section>`;
const until = async predicate => { const deadline = Date.now() + 8000; while (!predicate()) { if (Date.now() > deadline) throw new Error('Expected intercepted request was not sent'); await new Promise(resolve => setTimeout(resolve, 10)); } };

for (const variant of ['typescript', 'generated']) test(`collections browser: ${variant}`, async t => {
  const browser = await chromium.launch(); t.after(() => browser.close());
  const code = Object.fromEntries(['links', 'series', 'circles'].map(name => [name, variant === 'typescript' ? compile(`frontend/shared/${name}.ts`) : readFileSync(resolve(`internal/coreui/assets/js/chunnel-${name}.js`), 'utf8')]));
  const admin = compile('frontend/core/admin-ui.ts');
  async function fixture(scenario, name, handle) {
    const context = await browser.newContext(); scenario.after(() => context.close());
    const page = await context.newPage(), errors = [], calls = []; let loads = 0;
    page.setDefaultTimeout(8000); page.on('pageerror', error => errors.push(error.message)); page.on('dialog', dialog => dialog.accept());
    scenario.after(() => assert.deepEqual(errors, []));
    await context.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url());
      if (url.pathname === '/admin.js' || url.pathname === '/entry.js') return route.fulfill({ contentType: 'text/javascript', body: url.pathname === '/admin.js' ? admin : code[name] });
      if (!url.pathname.startsWith('/api/')) {
        if (request.resourceType() !== 'document') return route.fulfill({ status: 204 });
        loads++;
        return route.fulfill({ contentType: 'text/html; charset=utf-8', body: `<!doctype html><meta charset="utf-8"><title>验收</title>${{links, series, circles}[name]}<script src="/admin.js"></script><script src="/entry.js"></script>` });
      }
      const call = { path: url.pathname, query: url.searchParams, method: request.method(), raw: request.postData(), headers: request.headers() }; calls.push(call);
      if (call.headers['content-type']?.includes('application/json')) call.body = request.postDataJSON();
      const result = await handle?.(call);
      if (result?.status === 204) return route.fulfill({ status: 204 });
      return route.fulfill({ status: result?.status || 200, contentType: 'application/json', body: JSON.stringify(result?.data ?? { items: [{ id: 3, title: '可添加文章', status: 'publish' }], page: 1, total: 1, has_more: false }) });
    });
    await page.goto(origin + '/' + name); await page.waitForLoadState('networkidle');
    return { page, calls, loads: () => loads, text: (selector, value) => page.waitForFunction(({selector, value}) => document.querySelector(selector)?.textContent.includes(value), {selector, value}) };
  }
  await t.test('metadata ignores a changed URL and preserves new typing; direct logo URL is visible', async s => {
    let release;
    const f = await fixture(s, 'links', () => new Promise(resolve => release = () => resolve({data:{title:'提取名称',description:'介绍',logo:'',notice:''}})));
    await f.page.locator('#item [name=name]').fill(''); await f.page.locator('[data-links-fetch]').click(); await until(() => release);
    await f.page.locator('#item [name=url]').fill('new.example.test'); release(); await f.text('[data-links-status]', '域名已经变化');
    assert.equal(await f.page.locator('#item [name=name]').inputValue(), '');
    release = null; await f.page.locator('[data-links-fetch]').click(); await until(() => release); await f.page.locator('#item [name=name]').fill('正在输入'); release(); await f.text('[data-links-status]', '信息已填入');
    assert.equal(await f.page.locator('#item [name=name]').inputValue(), '正在输入');
    await f.page.locator('#item [name=logo_url]').fill('/logo.png'); assert.equal(await f.page.locator('.links-logo-editor').isVisible(), true);
  });
  await t.test('save snapshot preserves edits even if the user returns to the old baseline while saving', async s => {
    let release;
    const f = await fixture(s, 'links', () => new Promise(resolve => release = () => resolve({data:{slug:'friends',version:4,item_id:1,replay:false}})));
    await f.page.locator('#item [name=name]').fill('待保存名称'); await f.page.locator('#item [data-admin-save]').click(); await until(() => release);
    await f.page.locator('#item [name=name]').fill('原始名称'); release(); await f.text('[data-links-status]', '未保存的输入仍保留');
    assert.equal(f.loads(), 1); assert.equal(await f.page.locator('#item [name=name]').inputValue(), '原始名称');
    assert.equal(await f.page.locator('#item [data-admin-save]').isEnabled(), true);
  });
  await t.test('configure handles radio groups and keeps another form dirty', async s => {
    const f = await fixture(s, 'links', () => ({data:{slug:'friends',version:4,item_id:0,replay:false}}));
    await f.page.locator('#item [name=name]').fill('其他草稿'); await f.page.locator('#settings [name=style][value=wall]').check();
    await f.page.locator('#settings [data-admin-save]').click(); await f.page.waitForFunction(() => document.querySelector('[data-links-admin]').dataset.version === '4');
    assert.equal(f.calls[0].body.settings.style, 'wall'); assert.equal(f.calls[0].body.expected_user_id, 1);
    assert.equal(await f.page.locator('#item [name=name]').inputValue(), '其他草稿'); assert.equal(f.loads(), 1);
  });
  await t.test('delete retries keep the original key and accept Go zero-item receipts', async s => {
    let valid = false;
    const f = await fixture(s, 'links', () => ({data:{slug:'friends',version:4,item_id:valid?0:1,replay:valid}}));
    await f.page.locator('[data-links-delete]').click(); await f.text('[data-links-status]', '保存结果未确认');
    valid = true; await f.page.locator('[data-links-delete]').click(); await until(() => f.loads() === 2);
    assert.equal(f.calls.length, 2); assert.deepEqual(f.calls[0].body, f.calls[1].body);
  });
  await t.test('account changes suppress late link save acknowledgments and preserve fields', async s => {
    let release;
    const f = await fixture(s, 'links', () => new Promise(resolve => release = () => resolve({data:{slug:'friends',version:4,item_id:1,replay:false}})));
    await f.page.locator('#item [name=name]').fill('保留内容'); await f.page.locator('#item [data-admin-save]').click(); await until(() => release);
    await f.page.evaluate(() => document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,id:2}}))); release();
    await f.text('[data-links-status]', '登录状态已变化'); assert.equal(f.loads(), 1); assert.equal(await f.page.locator('#item [name=name]').inputValue(), '保留内容');
    assert.equal(await f.page.locator('#item [data-admin-save]').isDisabled(), true);
  });
  await t.test('series search cancels old results and ignores composing Enter', async s => {
    let release;
    const f = await fixture(s, 'series', call => call.query.get('q') === '旧' ? new Promise(resolve => release = () => resolve({data:{items:[{id:4,title:'旧结果',status:'draft'}],page:1,total:1,has_more:false}})) : {data:{items:[{id:3,title:'新结果',status:'publish'}],page:1,total:1,has_more:false}});
    const search = f.page.locator('[data-series-search]'); await search.fill('旧'); await f.page.locator('[data-series-find]').click(); await until(() => release);
    await search.fill('新'); await f.page.locator('[data-series-find]').click(); await f.text('[data-series-sources]', '新结果'); release();
    const count = f.calls.length; await search.dispatchEvent('keydown', {key:'Enter',isComposing:true,keyCode:229}); assert.equal(f.calls.length, count);
    assert.equal(await f.page.locator('[data-series-sources]').textContent(), '新结果已发布添加');
  });
  await t.test('series ordering, add/remove and saved versions match the request', async s => {
    let valid = false;
    const f = await fixture(s, 'series', call => call.method === 'PUT' ? {data:{...call.body,id:7,version:3,etag:'"series-7-v3"',post_ids:valid?call.body.post_ids:[1,2]}} : undefined);
    await f.page.locator('[data-series-chapter="1"]').focus(); await f.page.keyboard.press('Alt+ArrowDown');
    await f.page.getByRole('button', {name:'添加：可添加文章',exact:true}).click(); await f.page.getByRole('button', {name:'移除：可添加文章',exact:true}).click();
    await f.page.locator('[data-admin-save]').click(); await f.text('[data-save-state]', '保存回执未匹配');
    assert.deepEqual(f.calls.find(call => call.method === 'PUT').body.post_ids,[2,1]); assert.equal(f.calls.find(call => call.method === 'PUT').headers['if-match'],'"series-7-v2"');
    valid = true; await f.page.locator('[data-admin-save]').click(); await f.text('[data-save-state]', '已保存');
    await f.page.locator('[name=title]').fill('再次修改'); await f.page.locator('[data-admin-save]').click(); await until(() => f.calls.filter(call => call.method === 'PUT').length === 3);
    assert.equal(f.calls.at(-1).headers['if-match'], '"series-7-v3"');
  });
  await t.test('series delete requires 204 and keeps state after a malformed success', async s => {
    let valid = false;
    const f = await fixture(s, 'series', call => call.method === 'DELETE' ? valid ? {status:204} : {data:{}} : undefined);
    await f.page.locator('[data-series-delete]').click(); await f.text('[data-series-status]', '删除结果未确认');
    valid = true; await f.page.locator('[data-series-delete]').click(); await until(() => f.loads() === 2);
  });
  await t.test('circle members filter by role and query, then restore focus on close', async s => {
    const f = await fixture(s, 'circles'); await f.page.locator('[data-circle-members-open]').click(); await f.page.locator('[data-member-filter=member]').click();
    assert.equal(await f.page.locator('[data-circle-member-row]:visible').count(), 1); await f.page.locator('[data-member-search]').fill('不存在');
    assert.equal(await f.page.locator('[data-member-filter-empty]').isVisible(), true); await f.page.locator('[data-circle-members-close]').click();
    assert.equal(await f.page.locator('[data-circle-members-open]').evaluate(node => node === document.activeElement), true);
  });
  await t.test('circle write retry keeps its key and confirms the draft only on valid receipt', async s => {
    let valid = false;
    const f = await fixture(s, 'circles', () => ({data:{slug:valid?'makers':'other',version:8,topic_id:4}}));
    await f.page.evaluate(() => { window.saved = 0; document.querySelector('[data-circle-command=topic]').addEventListener('community:saved', () => window.saved++); });
    await f.page.getByRole('button',{name:'发布话题',exact:true}).click(); await f.text('[data-circle-status]', '保存回执未匹配'); assert.equal(await f.page.evaluate(() => window.saved), 0);
    valid = true; await f.page.getByRole('button',{name:'发布话题',exact:true}).click(); await until(() => f.loads() === 2);
    assert.deepEqual(f.calls[0].body, f.calls[1].body); assert.ok(f.calls[0].body.key); assert.equal(f.calls[0].body.format, 'markdown');
  });
  await t.test('circle conflict refresh validates owner and re-confirms changed public settings', async s => {
    let valid = false, prompts = 0;
    const f = await fixture(s, 'circles', call => call.method === 'POST' ? {status:409,data:{error:{message:'版本冲突'}}} : {data:{signed_in:true,user_id:valid?1:2,csrf:'fresh',circle:{slug:'makers',version:9,name:'圈子',visibility_label:'私密',join_label:'开放',role_label:'圈主',description:'',rules:''}}});
    await f.page.getByRole('button',{name:'保存设置',exact:true}).click(); await f.text('[data-circle-status]', '最新状态暂时无法读取'); assert.equal(await f.page.locator('[data-circle-conflict]').isVisible(), false);
    valid = true; await f.page.getByRole('button',{name:'保存设置',exact:true}).click(); await f.page.locator('[data-circle-conflict]').waitFor();
    await f.page.locator('[name=visibility]').fill('public'); f.page.on('dialog', () => prompts++); await f.page.locator('[data-circle-retry]').click(); await until(() => f.calls.filter(call => call.method === 'POST').length === 3);
    assert.equal(prompts, 1); const writes = f.calls.filter(call => call.method === 'POST'); assert.equal(writes[2].body.version,9); assert.equal(writes[2].headers['x-csrf-token'],'fresh'); assert.notEqual(writes[1].body.key,writes[2].body.key);
  });
  await t.test('circle uploads reuse an uncertain attempt and preview validates HTML', async s => {
    let valid = false;
    const f = await fixture(s, 'circles', call => call.path.endsWith('/preview') ? {data:{html:valid?'<em>正文预览</em>':12}} : {data:{slug:'makers',version:8,attachment_id:'a'.repeat(64),attachment:{id:(valid?'a':'b').repeat(64),name:'附件.txt',size:4,image:false}}});
    await f.page.locator('input[type=file]').setInputFiles({name:'附件.txt',mimeType:'text/plain',buffer:Buffer.from('text')});
    await f.page.getByRole('button',{name:'上传附件',exact:true}).click(); await f.text('[data-circle-status]', '附件回执未确认');
    valid = true; await f.page.getByRole('button',{name:'上传附件',exact:true}).click(); await f.text('[data-circle-status]', '附件已上传');
    const key = raw => raw.match(/name="key"\r\n\r\n([^\r]+)/)[1]; assert.equal(key(f.calls[0].raw),key(f.calls[1].raw));
    assert.equal(await f.page.locator('[data-attachment-id]:checked').count(),1); assert.equal(await f.page.locator('[data-circle-page]').getAttribute('data-version'),'8');
    valid = false; await f.page.getByRole('button',{name:'预览正文',exact:true}).click(); await f.text('[data-circle-status]', '预览暂时不可用');
    valid = true; await f.page.getByRole('button',{name:'预览正文',exact:true}).click(); await f.page.locator('.circle-preview em').waitFor();
    assert.deepEqual(f.calls.at(-1).body.attachments,['a'.repeat(64)]);
  });
});
