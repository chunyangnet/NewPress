const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const playwright = require('playwright');
const { compile } = require('./helpers/typescript.cjs');
const root = path.join(__dirname, '../..');
const core = 'internal/coreui/assets/';
const theme = 'themes/chunnel/assets/';
const asset = (kind, ext, name = 'chunnel-selects') => path.join(root, kind, ext, `${name}.${ext}`);

test('theme and embedded UI ship the same dropdown controls', () => {
  for (const name of ['chunnel-selects','chunnel-choice-menu']) {
    for (const ext of ['js', 'css']) assert.deepEqual(fs.readFileSync(asset(core, ext, name)), fs.readFileSync(asset(theme, ext, name)));
  }
});

const engine = process.env.NEWPRESS_SELECT_BROWSER || 'chromium';
for (const entry of ['generated', 'TypeScript']) {
  test(`${entry}: dropdown interactions in ${engine}`, async t => {
    let browser;
    try { browser = await playwright[engine].launch(); }
    catch (err) {
      if (/Executable doesn't exist/.test(err.message) && !process.env.NEWPRESS_SELECT_BROWSER) return t.skip('Install the Playwright browser to run the interaction tests');
      throw err;
    }
    t.after(() => browser.close());
    const page = await browser.newPage({viewport: {width: 860, height: 700}});
    page.setDefaultTimeout(4000);
    const origin = 'http://choices.test';
    const requests = [];
    const compiled = entry === 'TypeScript' ? {
      'chunnel-selects': compile('frontend/shared/selects.ts'),
      'chunnel-choice-menu': compile('frontend/shared/choice-menu.ts'),
    } : {};
    page.on('request', request => requests.push(new URL(request.url()).pathname));
    await page.route(origin + '/**', route => {
      const resource = new URL(route.request().url()).pathname;
      if (resource === '/') return route.fulfill({contentType:'text/html; charset=utf-8', body:'<!doctype html><title>选择框</title>'});
      if (/^\/assets\/images\/select-(folder|pin)\.svg$/.test(resource)) {
        return route.fulfill({path:path.join(root, core, 'images', path.basename(resource)), contentType:'image/svg+xml'});
      }
      const match = /^\/assets\/(css|js)\/(chunnel-selects|chunnel-choice-menu)\.(?:css|js)$/.exec(resource);
      if (!match) return route.abort();
      if (match[1] === 'js' && compiled[match[2]]) return route.fulfill({ body: compiled[match[2]], contentType: 'text/javascript; charset=utf-8' });
      return route.fulfill({path:asset(core, match[1], match[2]), contentType:match[1] === 'css' ? 'text/css; charset=utf-8' : 'text/javascript; charset=utf-8'});
    });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const mount = async (content, { native = true, popover = true } = {}) => {
      await page.goto(origin);
      requests.length = 0;
      await page.setContent(`<!doctype html><html lang="zh-CN" data-input-style="fluent"><head><style>body{margin:24px;--primary:#236c54}label{display:inline-grid;width:230px;max-width:100%;margin:4px}select{width:100%}</style></head><body class="site"><form>${content}<button id="submit">提交</button></form></body></html>`);
      if (!native || !popover) await page.evaluate(({ native, popover }) => {
        if (!native) { const supports = CSS.supports.bind(CSS); CSS.supports = (...args) => args[0] === 'appearance' && args[1] === 'base-select' ? false : supports(...args); }
        if (!popover) HTMLElement.prototype.showPopover = undefined;
      }, { native, popover });
      await page.addStyleTag({url:origin+'/assets/css/chunnel-selects.css'});
      await page.addScriptTag({url:origin+'/assets/js/chunnel-selects.js'});
      await page.evaluate(() => {
        window.changes = [];
        const form = document.querySelector('form');
        for (const type of ['input','change']) form.addEventListener(type, e => { if (e.target.matches('select')) window.changes.push([type,e.target.name,e.target.value]); });
        form.onsubmit = event => { event.preventDefault(); window.submitted = [...new FormData(form)]; };
      });
    };
    const panel = page.locator('.np-choice-panel:popover-open');
    const row = name => panel.getByRole('option', {name, exact:true});

    await t.test('choosing an option inside a labeled form closes once and submits its actual value', async () => {
      await mount('<label>收藏夹<select name="folder" data-select-ui="icon" data-select-icon="folder"><option value="a">工作</option><option value="b">生活</option></select></label><label>分类<select name="category"><optgroup label="服务类"><option>国内</option><option>国际</option></optgroup></select></label>');
      await page.getByLabel('收藏夹').click();
      await row('生活').click();
      assert.equal(await panel.count(), 0);
      assert.deepEqual(await page.evaluate(() => window.changes), [['input','folder','b'],['change','folder','b']]);
      await page.getByLabel('收藏夹').click();
      await page.locator('[name="category"]').click();
      assert.equal(await panel.count(), 1);
      assert.equal(await panel.getByRole('group',{name:'服务类'}).count(), 1);
      await page.keyboard.press('ArrowDown'); await page.keyboard.press('Escape');
      assert.equal(await page.locator('[name="category"]').inputValue(),'国内');
      assert.equal(await page.evaluate(() => document.activeElement.name),'category');
      await page.locator('#submit').click();
      assert.deepEqual(await page.evaluate(() => window.submitted), [['folder','b'],['category','国内']]);
    });

    await t.test('long lists support search, no matches, keyboard selection and replacement options', async () => {
      await mount('<label>文章<select name="post">' + Array.from({length:12}, (_,i) => `<option value="${i}">文章 ${i}</option>`).join('') + '</select></label>');
      const select = page.locator('[name="post"]');
      await select.press('ArrowDown');
      const search = panel.getByRole('combobox');
      await search.fill('无此项');
      assert.equal(await panel.getByRole('status').innerText(), '没有匹配的选项');
      await search.fill('１１');
      assert.equal(await page.getByRole('option', {name:'文章 11',exact:true}).count(),1);
      await search.press('Enter');
      assert.equal(await select.getAttribute('aria-hidden'),null);
      assert.equal(await select.inputValue(), '11');
      await select.click();
      await select.evaluate(el => { el.dataset.selectUi = 'searchable'; el.replaceChildren(new Option('更新的文章','new'),new Option('第二篇文章','second')); });
      await row('第二篇文章').click();
      assert.equal(await select.inputValue(),'second');
    });

    await t.test('removable tags, capped multi-selects, disabled choices and reset track form state', async () => {
      await mount('<label>标签<select name="tag" data-select-ui="tag"><option value="">全部标签</option><option value="news" selected>新闻</option></select></label><fieldset><label>线路<select name="lines" multiple data-max-selected="2"><option value="a" selected>线路甲</option><option value="b">线路乙</option><option value="c">线路丙</option><optgroup label="暂停"><option disabled>线路丁</option></optgroup></select></label></fieldset><button type="reset">重置</button>');
      await page.getByRole('button', {name:'移除新闻'}).click();
      assert.equal(await page.locator('[name="tag"]').inputValue(),'');
      const tag = page.getByRole('combobox', {name:/标签：/});
      await tag.click(); await row('新闻').click();
      await tag.click(); await row('新闻').click();
      assert.equal(await page.locator('[name="tag"]').inputValue(), '');
      const trigger = page.getByRole('combobox',{name:/线路：/});
      await trigger.click(); await row('线路乙').click();
      assert.equal(await row('线路丙').getAttribute('aria-disabled'),'true');
      assert.equal(await row('线路丁').getAttribute('aria-disabled'),'true');
      assert.equal(await panel.getByRole('status').innerText(),'最多选择 2 项');
      await page.keyboard.press('Escape'); await page.locator('#submit').click();
      assert.deepEqual(await page.evaluate(() => window.submitted), [['tag',''],['lines','a'],['lines','b']]);
      await page.getByRole('button',{name:'重置'}).click();
      await page.getByRole('button', {name:'移除新闻'}).waitFor();
      assert.equal(await page.getByRole('button', {name:'移除线路乙'}).count(), 0);
      await page.locator('fieldset').evaluate(f => { f.disabled = true; });
      assert.equal(await trigger.isDisabled(), true);
    });

    await t.test('long tags leave the arrow and remove buttons clear and show their full names in the list', async () => {
      const names = ['跨部门设计资料与体验研究年度归档', 'InternationalEditorialCollectionWithoutSpaces123456789'];
      await mount(`<label style="width:180px;--choice-size:14px">线路<select name="lines" multiple>${names.map(n => `<option selected>${n}</option>`).join('')}</select></label>`);
      const shell = page.locator('.np-choice-chips');
      await shell.waitFor();
      const visibleChips = await shell.evaluate(el => {
        const arrow = el.querySelector('[data-glyph="down"]').getBoundingClientRect();
        return [...el.querySelectorAll('.np-choice-chip')].map(chip => {
          const text = chip.querySelector('.np-choice-chip-text');
          const remove = chip.querySelector('button').getBoundingClientRect();
          return text.scrollWidth > text.clientWidth && text.getBoundingClientRect().right <= remove.left + 1 && remove.right < arrow.left;
        });
      });
      assert.deepEqual(visibleChips, [true, true]);
      await shell.getByRole('combobox').click();
      assert.equal(await panel.evaluate(el => getComputedStyle(el).fontSize), await shell.evaluate(el => getComputedStyle(el).fontSize));
      for (const name of names) {
        assert.ok(await row(name).evaluate(el => {
          const text = el.querySelector('.np-choice-text');
          return text.offsetHeight > parseFloat(getComputedStyle(text).lineHeight) + 1 && text.scrollWidth <= text.clientWidth + 1 &&
            el.querySelector('[data-glyph="box"]').getBoundingClientRect().right < text.getBoundingClientRect().left;
        }));
      }
      await page.keyboard.press('Escape');
      await shell.getByRole('button', {name: `移除${names[1]}`}).click();
      assert.deepEqual(await page.locator('[name="lines"]').evaluate(el => [...el.selectedOptions].map(opt => opt.label)), names.slice(0,1));
    });

    await t.test('native auto-width filters keep their label and fixed fields reserve space for the arrow', async t => {
      await mount('<select name="auto" style="width:auto"><option>全部状态</option><option>等待审核</option></select><label>说明<select name="long" style="width:132px"><option>跨部门设计资料与年度研究归档 International collection</option><option>普通选项</option></select></label>');
      if (await page.locator('[name="auto"]').getAttribute('data-native-select') === null) return t.skip('This engine uses the listbox fallback');
      assert.ok(await page.locator('[name="auto"]').evaluate(el => {
        const text = el.querySelector('selectedcontent');
        return text.clientWidth >= text.scrollWidth && el.getBoundingClientRect().width > 80;
      }), 'intrinsic field sizing keeps the complete short label');
      assert.ok(await page.locator('[name="long"]').evaluate(el => {
        const text = el.querySelector('selectedcontent'), icon = getComputedStyle(el, '::picker-icon');
        const arrowStart = el.getBoundingClientRect().right - parseFloat(icon.insetInlineEnd) - parseFloat(icon.width);
        return text.scrollWidth > text.clientWidth && text.getBoundingClientRect().right + 3 < arrowStart;
      }), 'long text is clipped before the arrow');
    });

    await t.test('tag remove buttons retain their own size inside the actual admin button skin', async () => {
      await mount('<div class="admin-main"><label>分类<select name="tags" multiple><option selected>设计</option><option>开发</option></select></label></div>');
      await page.evaluate(() => { document.body.className = 'admin-workbench'; document.body.style.setProperty('--admin-ui-control-height', '38px'); });
      await page.addStyleTag({content:fs.readFileSync(path.join(root,core,'css/chunnel-admin-components.source.css'),'utf8')});
      const shell = page.locator('.np-choice-chips');
      await shell.waitFor();
      assert.ok(await shell.evaluate(el => {
        const clear = el.querySelector('.np-choice-clear').getBoundingClientRect(), arrow = el.querySelector('[data-glyph="down"]').getBoundingClientRect();
        return clear.width === 24 && clear.height === 24 && el.getBoundingClientRect().height === 38 && clear.right + 3 < arrow.left;
      }));
      await shell.getByRole('button', {name:'移除设计'}).click();
      assert.deepEqual(await page.locator('[name="tags"]').evaluate(el => [...el.selectedOptions].map(option => option.label)), []);
    });

    await t.test('a popup inherits a local field palette and follows a container resize', async () => {
      await mount('<label style="width:240px;--fluent-text:#eef3ff;--tone-surface:#182438;--white:#fff;--fluent-accent:#58c5ad;--muted:#b5c0d2;--choice-radius:12px">本地区域<select name="local" data-select-ui="searchable"><option>设计与研究</option><option>跨部门年度资料归档</option></select></label>');
      await page.addStyleTag({content:fs.readFileSync(path.join(root,core,'css/chunnel-inputs.css'),'utf8')});
      const select = page.locator('[name="local"]');
      await select.hover();
      await page.waitForFunction(() => getComputedStyle(document.querySelector('[name="local"]')).backgroundColor === 'rgb(24, 36, 56)');
      assert.equal(await select.evaluate(el => getComputedStyle(el).backgroundColor), 'rgb(24, 36, 56)', 'the input hover skin preserves the local choice surface');
      await select.focus();
      await page.waitForFunction(() => getComputedStyle(document.querySelector('[name="local"]')).backgroundColor === 'rgb(24, 36, 56)');
      assert.equal(await select.evaluate(el => getComputedStyle(el).backgroundColor), 'rgb(24, 36, 56)', 'the input focus skin preserves the local choice surface');
      await select.click();
      const fieldPalette = await select.evaluate(el => { const style = getComputedStyle(el); return [style.color,style.fontFamily,...['--choice-accent','--choice-surface'].map(property => style.getPropertyValue(property))]; });
      const popupPalette = await panel.evaluate(el => { const style = getComputedStyle(el); return [style.color,style.fontFamily,...['--choice-accent','--choice-surface'].map(property => style.getPropertyValue(property))]; });
      assert.deepEqual(popupPalette, fieldPalette);
      assert.equal(await panel.evaluate(el => getComputedStyle(el).borderRadius), '14px');
      await select.evaluate(el => { el.parentElement.style.width = '160px'; el.parentElement.style.marginInlineStart = '64px'; });
      await page.waitForFunction(() => {
        const popup = document.querySelector('.np-choice-panel'), field = document.querySelector('[name="local"]');
        return Math.abs(popup.getBoundingClientRect().left - field.getBoundingClientRect().left) < 1;
      });
      const [p,s] = await Promise.all([panel.boundingBox(),select.boundingBox()]);
      assert.ok(Math.abs(p.y - s.y - s.height - 8) < 1 && p.width >= s.width);
      await row('跨部门年度资料归档').click();
      assert.equal(await select.inputValue(), '跨部门年度资料归档');
    });

    await t.test('theme changes update tags and an open search without losing the query or focus', async () => {
      await mount('<label>分类<select name="colors" multiple><option selected>设计资料</option><option>开发资料</option></select></label><label>主题<select name="theme" data-select-ui="searchable"><option>起点</option><option>另一个选项</option></select></label>');
      await page.addStyleTag({content:fs.readFileSync(path.join(root,core,'css/chunnel-color-mode.source.css'),'utf8')});
      await page.locator('.np-choice-chips').waitFor();
      const clear = await page.getByRole('button', {name:'移除设计资料'}).elementHandle();
      for (const mode of ['dark', 'light']) {
        await page.evaluate(mode => { document.documentElement.dataset.theme = mode; }, mode);
        await page.waitForFunction(() => {
          const field = document.querySelector('[name="colors"]'), shell = field.nextElementSibling;
          const source = getComputedStyle(field), view = getComputedStyle(shell);
          return source.color === view.color && source.getPropertyValue('--choice-surface') === view.getPropertyValue('--choice-surface');
        });
        assert.ok(await clear.evaluate(el => el.isConnected), 'theme updates preserve the existing remove button');
      }
      const select = page.locator('[name="theme"]');
      await select.click();
      const search = panel.getByRole('combobox');
      await search.fill('另');
      await page.evaluate(() => { document.documentElement.dataset.theme = 'dark'; });
      await page.waitForFunction(() => getComputedStyle(document.querySelector('.np-choice-panel')).color === getComputedStyle(document.querySelector('[name="theme"]')).color);
      assert.equal(await search.inputValue(), '另');
      assert.ok(await search.evaluate(el => document.activeElement === el));
      await search.press('Enter');
      assert.equal(await select.inputValue(), '另一个选项');
    });

    await t.test('a narrow-screen search stays attached above its field as results shrink and grow', async () => {
      await page.setViewportSize({width:320,height:550});
      await mount('<label style="position:fixed;bottom:135px;right:8px;width:150px">区号<select name="dial" data-select-ui="searchable"><option>+86</option><option>+852</option><option>+853</option><option>+886</option></select></label>');
      const select = page.locator('[name="dial"]');
      await select.click();
      const search = panel.getByRole('combobox');
      for (const filter of ['', '+852', '']) {
        await search.fill(filter);
        const [p, s] = await Promise.all([panel.boundingBox(), select.boundingBox()]);
        assert.ok(Math.abs(p.x + p.width - s.x - s.width) <= 1, 'wide picker aligns with the right edge of the field');
        const gap = s.y - (p.y + p.height);
        assert.ok(p.x >= 7 && p.x + p.width <= 313 && p.y >= 7 && gap >= 7 && gap <= 10, `gap ${gap}, panel ${JSON.stringify(p)}`);
      }
      await row('+852').click();
      assert.equal(await select.inputValue(),'+852');
    });

    await t.test('empty and disabled results accept keyboard navigation without inventing a selection', async () => {
      await page.setViewportSize({width:860,height:700});
      await mount('<label>项目<select name="items" data-select-ui="searchable"><option disabled>不可用</option></select></label>');
      await page.locator('[name="items"]').click();
      const search = panel.getByRole('combobox');
      for (const key of ['ArrowUp','ArrowDown','Enter']) await search.press(key);
      assert.equal(await panel.count(),1);
      assert.equal(await page.locator('[name="items"]').inputValue(),'');
      await search.fill('没有这个项目');
      for (const key of ['ArrowDown','ArrowUp','Enter']) await search.press(key);
      assert.equal(await search.getAttribute('aria-activedescendant'),null);
      assert.deepEqual(await page.evaluate(() => window.changes),[]);
      await search.press('Escape');
      assert.equal(await page.evaluate(() => document.activeElement.name),'items');
    });

    await t.test('group labels and option text stay text, and ArrowUp opens at the last enabled row', async () => {
      await mount('<label>菜单<select name="menu" data-select-ui="icon"><optgroup label="&lt;em&gt;分组&lt;/em&gt;"><option>第一项</option><option>&lt;img src=x onerror=alert(1)&gt;</option><option disabled>禁用尾项</option></optgroup></select></label>');
      await page.locator('[name="menu"]').press('ArrowUp');
      assert.equal(await panel.getByRole('group').getAttribute('aria-label'),'<em>分组</em>');
      assert.equal(await panel.locator('img,em').count(),0);
      await page.keyboard.press('Enter');
      assert.equal(await page.locator('[name="menu"]').inputValue(),'<img src=x onerror=alert(1)>');
      await page.locator('[name="menu"]').press('Enter');
      await page.keyboard.press('Home'); await page.keyboard.press('End'); await page.keyboard.press('Escape');
      assert.equal(await page.locator('[name="menu"]').inputValue(),'<img src=x onerror=alert(1)>');
    });

    await t.test('closing a containing dialog and removing a control clear the active popup', async () => {
      await mount('<dialog><label>弹窗选择<select name="dialog" data-select-ui="searchable"><option>一</option><option>二</option></select></label></dialog><label>动态选择<select name="dynamic" data-select-ui="searchable"><option>甲</option><option>乙</option></select></label>');
      await page.locator('dialog').evaluate(el => el.showModal());
      await page.locator('[name="dialog"]').click();
      await panel.waitFor();
      assert.equal(await page.locator('dialog .np-choice-panel:popover-open').count(),1);
      await page.locator('dialog').evaluate(el => el.close());
      await panel.waitFor({state:'detached'});
      await page.locator('[name="dynamic"]').click();
      await panel.waitFor();
      await page.locator('[name="dynamic"]').evaluate(el => el.remove());
      await panel.waitFor({state:'detached'});
      await page.evaluate(() => document.querySelector('form').insertAdjacentHTML('beforeend','<label>重建选择<select name="replacement" data-select-ui="searchable"><option>甲</option><option>乙</option></select></label>'));
      await page.locator('[name="replacement"]').click();
      await row('乙').click();
      assert.equal(await page.locator('[name="replacement"]').inputValue(),'乙');
    });

    await t.test('unsupported browsers keep native form semantics without downloading the menu', async () => {
      await mount('<label>原生选择<select name="plain"><option value="a">甲</option><option value="b">乙</option></select></label>', {native:false,popover:false});
      assert.equal(await page.evaluate(() => window.NewPressSelects),undefined);
      assert.equal(await page.locator('.np-choice-chips,[data-native-select]').count(),0);
      await page.locator('[name="plain"]').selectOption('b');
      await page.locator('#submit').click();
      assert.deepEqual(await page.evaluate(() => window.submitted),[['plain','b']]);
      assert.equal(requests.some(path => path.includes('chunnel-choice-menu')),false);
    });

    for (const failure of ['script request', 'stylesheet request', 'missing menu API']) {
      await t.test(`${failure} failure leaves a native select able to submit`, async () => {
        const resource = origin + (failure === 'stylesheet request' ? '/assets/css/chunnel-choice-menu.css' : '/assets/js/chunnel-choice-menu.js');
        await page.route(resource, route => failure === 'missing menu API'
          ? route.fulfill({contentType:'text/javascript',body:'/* Unavailable optional picker. */'}) : route.abort());
        try {
          await mount('<label>失败回退<select name="fallback" data-select-ui="searchable"><option value="a">甲</option><option value="b">乙</option></select></label>', {native:false});
          await page.getByLabel('失败回退').click();
          await page.waitForFunction(() => !document.querySelector('[name="fallback"]').hasAttribute('data-choice-menu'));
          assert.equal(await page.locator('[name="fallback"]').getAttribute('aria-hidden'),null);
          await page.locator('[name="fallback"]').selectOption('b');
          await page.locator('#submit').click();
          assert.deepEqual(await page.evaluate(() => window.submitted),[['fallback','b']]);
          assert.equal(await page.locator('.np-choice-panel').count(),0);
        } finally { await page.unroute(resource); }
      });
    }

    await t.test('moving focus while menu resources load does not steal it back', async () => {
      const script = origin + '/assets/js/chunnel-choice-menu.js';
      let unblock, started;
      const gate = new Promise(resolve => { unblock = resolve; });
      const requested = new Promise(resolve => { started = resolve; });
      await page.route(script, async route => { started(); await gate; await route.fallback(); });
      try {
        await mount('<label>延迟加载<select name="slow" data-select-ui="searchable"><option>甲</option><option>乙</option></select></label><input name="other">');
        await page.locator('[name="slow"]').click(); await requested;
        await page.locator('[name="other"]').focus();
        unblock(); await page.waitForFunction(() => !!window.NewPressChoiceMenu);
        assert.equal(await page.evaluate(() => document.activeElement.name),'other');
        assert.equal(await panel.count(),0);
        await page.locator('[name="slow"]').click(); await row('乙').click();
        assert.equal(requests.filter(path => path.endsWith('chunnel-choice-menu.js')).length,1);
      } finally { unblock(); await page.unroute(script); }
    });
    assert.deepEqual(errors, []);
  });
}
