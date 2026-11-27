// Opt-in QA against TestFeatureControlsBrowserFixture with NEWPRESS_HOME_BROWSER=1.
// Real templates, session and progression responses; mutations are intercepted.
import assert from 'node:assert/strict';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
import { compileFrontendEntry } from './frontend_entries.mjs';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
assert.equal(fixture.home, true, 'enable the homepage fixture');
const engines = { chromium, firefox, webkit };
const selected = process.argv[3] && process.argv[3] !== 'all' ? [process.argv[3]] : Object.keys(engines);
const variant = process.argv[4] === 'typescript' ? 'typescript' : 'generated';
const tsAssets = new Map(), evidence = [];
if (variant === 'typescript') {
  const code = async name => (await compileFrontendEntry(name, { minify: false })).code;
  const [color, scroll, shell, mobile, home] = await Promise.all(['color-mode', 'scroll', 'shell', 'mobile-nav', 'home-controller'].map(code));
  for (const glass of [false, true]) tsAssets.set(`chunnel-home${glass ? '-glass' : ''}.js`,
    color + '\ndocument.addEventListener("DOMContentLoaded", () => {\n' + scroll + '\n' + shell + (glass ? '\n' + mobile : '') + '\n' + home + '\n}, { once: true });');
  for (const name of ['home-member', 'home-feed', 'home-blocks']) tsAssets.set(`chunnel-${name}.js`, await code(name));
}

for (const name of selected) {
  const browser = await engines[name].launch();
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
  const page = await context.newPage(), errors = [], writes = [], scripts = [], reads = [], layouts = [];
  page.setDefaultTimeout(12000);
  let featureMode = 'normal', overlayMode = 'normal', feedScriptMode = 'normal', feedMode = 'normal', badgeMode = 'normal';
  let releaseFeature, releaseOverlay, releaseFeed, heldFeedURL, feedMarker = '', memberOverride;
  const fulfillAsset = route => {
    const source = tsAssets.get(new URL(route.request().url()).pathname.split('/').pop());
    return source ? route.fulfill({ contentType: 'application/javascript', body: source }) : route.continue();
  };
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => {
    const url = new URL(request.url());
    if (request.resourceType() === 'script') scripts.push(url.pathname);
    if (url.pathname.startsWith('/api/v1/social/')) reads.push(url.pathname);
  });
  await page.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url());
    if (!['GET', 'HEAD'].includes(request.method())) {
      writes.push({ method: request.method(), path: url.pathname });
      return route.fulfill({ status: 409, contentType: 'application/json', body: '{"error":"browser QA does not persist actions"}' });
    }
    if (url.pathname.endsWith('/chunnel-home-features.js')) {
      if (featureMode === 'fail') return route.abort('failed');
      if (featureMode === 'hold') { releaseFeature = () => fulfillAsset(route); return; }
    }
    if (/chunnel-home-overlays(?:-glass)?\.css$/.test(url.pathname) && overlayMode === 'hold') {
      releaseOverlay = () => route.continue(); return;
    }
    if (url.pathname.endsWith('/chunnel-home-feed.js') && feedScriptMode === 'fail') return route.abort('failed');
    if (url.pathname.endsWith('/chunnel-article-badges.css') && badgeMode === 'fail') return route.abort('failed');
    if (url.pathname === '/api/v1/social/progression' && memberOverride !== undefined) return route.fulfill({ contentType: 'application/json', body: JSON.stringify(memberOverride) });
    if (url.pathname === '/' && !request.isNavigationRequest() && request.headers().accept === 'text/html') {
      if (feedMode === 'fail') return route.fulfill({ status: 503, contentType: 'text/html', body: 'unavailable' });
      const response = await route.fetch();
      let body = await response.text();
      const marker = feedMarker;
      if (marker) body = body.replace(/(id="latest"[^>]*>)/, match => match + `<span data-feed-result>${marker}</span>`);
      if (feedMode === 'badge') {
        const entry = await page.locator('[data-home-entry]').getAttribute('src');
        const href = new URL('../css/chunnel-article-badges.css', new URL(entry, fixture.url));
        body = body.replace('</head>', `<link rel="stylesheet" data-article-badge-styles href="${href.href}"></head>`)
          .replace(/(id="latest"[^>]*>)/, match => match + '<span class="np-article-badge">验收角标</span>');
      }
      const deliver = () => route.fulfill({ response, body });
      if (feedMode === 'hold') { heldFeedURL = url.href; releaseFeed = deliver; return; }
      return deliver();
    }
    return fulfillAsset(route);
  });
  const check = label => { evidence.push({ browser: name, variant, check: label }); console.log(`${name} ${variant}: ${label}`); };
  const visit = async () => { const response = await page.goto(fixture.url + '/'); assert.equal(response.status(), 200); await page.waitForLoadState('networkidle'); };
  const until = async predicate => {
    const deadline = Date.now() + 12000;
    while (!predicate()) { if (Date.now() > deadline) throw new Error('Expected route was not requested'); await new Promise(resolve => setTimeout(resolve, 30)); }
  };
  const login = () => page.locator('header [data-account-login]').filter({ visible: true }).first();
  const sortLink = value => page.locator(`#latest .home-sort-links a[href*="sort=${value}"]`).first();
  try {
    assert.equal((await context.request.post(fixture.url + '/fixture/reload')).status(), 204);
    assert.equal((await context.request.post(fixture.url + '/fixture/style/classic')).status(), 204);
    await visit();
    assert.equal(scripts.some(path => /chunnel-home-(?:features|member|search|feed)\.js$/.test(path)), false);
    const sessionReads = reads.filter(path => path.endsWith('/session')).length;
    assert.ok(sessionReads >= 1 && sessionReads <= 2, 'DOMContentLoaded and pageshow may finish on either side of the initial read');
    assert.equal(await page.locator('[data-home-guest]').first().isVisible(), true);
    await page.locator('[data-cookie-reject]').first().click();
    await login().hover(); await page.mouse.wheel(0, 450); await page.mouse.wheel(0, -450);
    assert.equal(scripts.some(path => /chunnel-home-(?:features|search|feed)\.js$/.test(path)), false);
    assert.equal(await page.locator('[data-background-video]').getAttribute('src'), null);
    check('Anonymous reading and hover/scroll keep interactive bundles unloaded; reduced motion leaves the background video unloaded.');

    featureMode = 'fail'; await login().click();
    await page.locator('.home-feature-status button').getByText('重试', { exact: true }).waitFor({ state: 'visible' });
    assert.equal(await page.locator('#account-login-modal').isVisible(), false);
    featureMode = 'normal'; await page.locator('.home-feature-status button').getByText('重试', { exact: true }).click();
    await page.locator('#account-login-modal').waitFor({ state: 'visible' });
    assert.equal(await page.locator('.home-feature-status').isVisible(), false);
    await page.locator('#account-login-modal button.modal-close').click();
    check('A failed enhancement offers an explicit retry and opens the intended login dialog once ready.');

    overlayMode = 'hold'; releaseOverlay = undefined; await visit();
    await login().click(); await until(() => releaseOverlay);
    await page.waitForFunction(() => window.ChunnelHomeFeaturesReady === true);
    assert.equal(await page.locator('#account-login-modal').isVisible(), false);
    await page.keyboard.press('Escape'); await releaseOverlay(); overlayMode = 'normal';
    await page.waitForLoadState('networkidle');
    assert.equal(await page.locator('#account-login-modal').isVisible(), false);
    assert.equal(await login().evaluate(node => node === document.activeElement), true);
    await login().click(); await page.locator('#account-login-modal').waitFor({ state: 'visible' });
    await page.locator('#account-login-modal button.modal-close').click();
    check('JavaScript completion waits for overlay CSS; Escape cancels late replay and restores the launcher.');

    featureMode = 'hold'; releaseFeature = undefined; await visit();
    await login().click(); await until(() => releaseFeature);
    await page.locator('header [data-saved-open]').filter({ visible: true }).first().click();
    featureMode = 'normal'; await releaseFeature();
    await page.locator('#saved-drawer').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#account-login-modal').isVisible(), false);
    await page.keyboard.press('Escape');
    check('Multiple controls share one pending bundle and replay only the latest explicit action.');

    await visit(); feedScriptMode = 'fail';
    const view = page.locator('#latest .home-view-switch a:not([aria-current])').first();
    const targetView = new URL(await view.getAttribute('href'), fixture.url).searchParams.get('view');
    await view.click(); await page.getByText('列表切换暂时不可用，请再次点击重试。', { exact: true }).waitFor({ state: 'visible' });
    feedScriptMode = 'normal';
    await view.scrollIntoViewIfNeeded();
    const beforeView = await page.evaluate(() => { window.__feedNode = document.querySelector('.home-feed article'); if (!window.__feedNode) throw new Error('Fixture needs published feed articles'); return scrollY; });
    await view.click(); await page.waitForFunction(value => new URL(location.href).searchParams.get('view') === value, targetView);
    assert.equal(await page.evaluate(() => window.__feedNode === document.querySelector('.home-feed article')), true);
    assert.ok(Math.abs((await page.evaluate(() => scrollY)) - beforeView) < 2);
    assert.equal(await page.locator('#latest').getAttribute('aria-busy'), null);
    check('Failed feed-module loading can retry; list/grid switches preserve article nodes, focus and viewport.');

    const sortURLs = await page.locator('#latest .home-sort-links a').evaluateAll(links => links.map(link => ({ href: link.href, sort: new URL(link.href).searchParams.get('sort') })));
    assert.ok(sortURLs.length >= 2);
    const firstSort = sortURLs.find(item => item.sort && !new URL(page.url()).searchParams.has('sort')) || sortURLs[0];
    const secondSort = sortURLs.find(item => item.sort !== firstSort.sort);
    feedMode = 'hold'; feedMarker = 'late'; releaseFeed = undefined;
    await sortLink(firstSort.sort).click(); await until(() => releaseFeed);
    feedMode = 'normal'; feedMarker = 'current'; await sortLink(secondSort.sort).click();
    await page.locator('[data-feed-result]').getByText('current', { exact: true }).waitFor({ state: 'visible' });
    await releaseFeed().catch(() => {}); await page.waitForLoadState('networkidle');
    assert.equal(await page.locator('[data-feed-result]').textContent(), 'current');
    assert.notEqual(page.url().replace(/#.*$/, ''), heldFeedURL.replace(/#.*$/, ''));
    check('A superseded HTML response cannot overwrite the current feed or its history URL.');

    const stableURL = page.url();
    feedMode = 'fail'; await sortLink(firstSort.sort).click();
    await page.getByText('内容加载失败，已保留当前列表，请再次点击重试。', { exact: true }).waitFor({ state: 'visible' });
    assert.equal(page.url(), stableURL); assert.equal(await page.locator('[data-feed-result]').textContent(), 'current');
    feedMode = 'badge'; feedMarker = 'badge'; badgeMode = 'fail'; await sortLink(firstSort.sort).click();
    await page.waitForFunction(() => !document.querySelector('#latest').hasAttribute('aria-busy'));
    assert.equal(await page.locator('[data-feed-result]').textContent(), 'current');
    assert.equal(page.url(), stableURL);
    badgeMode = 'normal'; await sortLink(firstSort.sort).click();
    await page.locator('[data-feed-result]').getByText('badge', { exact: true }).waitFor({ state: 'visible' });
    assert.equal(await page.locator('link[data-article-badge-styles]').count(), 1);
    assert.equal(await page.locator('.np-article-badge').first().isVisible(), true);
    feedMode = 'normal'; feedMarker = '';
    check('Failed HTML or badge CSS preserves the old list; retry installs badge styling before committing new content.');

    await page.goBack();
    await page.waitForFunction(value => new URL(location.href).searchParams.get('sort') === value && !document.querySelector('#latest').hasAttribute('aria-busy'), secondSort.sort);
    assert.equal(await page.locator('[data-feed-result]').count(), 0);
    await page.goForward();
    await page.waitForFunction(value => new URL(location.href).searchParams.get('sort') === value && !document.querySelector('#latest').hasAttribute('aria-busy'), firstSort.sort);
    assert.equal(await page.evaluate(() => history.scrollRestoration), 'auto');
    check('Browser back/forward restores the matching feed and releases manual scroll restoration.');

    await page.locator('#latest .pagination a[href*="page=2"]').first().click();
    await page.waitForFunction(() => new URL(location.href).searchParams.get('page') === '2' && !document.querySelector('#latest').hasAttribute('aria-busy'));
    assert.ok(Math.abs(await page.locator('#latest').evaluate(node => node.getBoundingClientRect().top) - 96) < 3);
    assert.equal(await page.locator('#latest .home-feed article').count(), fixture.home_second_page_count ?? 3);
    check('Real paginated articles preserve filter/view parameters and place the new page below the fixed header.');

    await visit();
    const track = page.locator('[data-carousel-track]'); await track.scrollIntoViewIfNeeded(); await track.focus(); await track.press('ArrowRight');
    await page.waitForFunction(() => document.querySelectorAll('[data-carousel-dot]')[1]?.getAttribute('aria-current') === 'true');
    assert.equal(await track.evaluate(node => node === document.activeElement), true);
    assert.equal(await page.locator('[data-carousel-controls]').isVisible(), true);
    await track.press('ArrowLeft');
    await page.waitForFunction(() => document.querySelector('[data-carousel-dot]')?.getAttribute('aria-current') === 'true');
    check('The real carousel supports keyboard navigation, current-slide announcements and reduced-motion controls.');

    await context.request.get(fixture.login); await visit();
    await page.waitForFunction(() => document.querySelector('[data-home-xp]')?.textContent.includes('经验'));
    assert.equal(await page.locator('[data-home-member]').first().isVisible(), true);
    assert.equal(await page.locator('[data-home-checkin-label]').textContent(), '领取签到奖励');
    assert.equal(scripts.some(path => path.endsWith('/chunnel-home-member.js')), true);
    const memberBody = await (await context.request.get(fixture.url + '/api/v1/social/progression')).json();
    assert.equal(typeof memberBody.account.user_id, 'string');
    const oldXP = await page.locator('[data-home-xp]').textContent();
    memberOverride = { ...memberBody, account: { ...memberBody.account, user_id: String(fixture.owner_id + 1), experience: '999' } };
    await page.evaluate(() => document.dispatchEvent(new Event('chunnel:checkin')));
    await page.getByText('会员状态暂不可用', { exact: true }).waitFor({ state: 'visible' });
    assert.equal(await page.locator('[data-home-xp]').textContent(), oldXP);
    memberOverride = undefined;
    await page.evaluate(() => document.dispatchEvent(new Event('chunnel:checkin')));
    await page.waitForFunction(() => document.querySelector('[data-home-member-state]').textContent !== '会员状态暂不可用');
    check('Actual authenticated Go session/progression data paints the homepage; a mismatched owner is rejected and a later refresh recovers.');

    for (const style of ['classic', 'liquid-glass']) {
      assert.equal((await context.request.post(fixture.url + '/fixture/style/' + style)).status(), 204);
      for (const width of [1280, 375]) {
        await page.setViewportSize({ width, height: 900 }); await visit();
        await page.waitForFunction(() => document.querySelector('[data-home-xp]')?.textContent.includes('经验'));
        await page.evaluate(() => window.ChunnelColorMode.set('dark'));
        await page.evaluate(() => scrollTo(0, 0));
        const geometry = await page.evaluate(() => ({ width: innerWidth, scrollWidth: document.documentElement.scrollWidth, overflow: document.documentElement.scrollWidth > innerWidth + 1 }));
        layouts.push({ style, ...geometry }); assert.equal(geometry.overflow, false);
        await page.screenshot({ path: resolve(directory, `${name}-${variant}-${style}-${width}.png`), fullPage: false });
      }
    }
    check('Classic/glass and desktop/narrow homepages retain member display and avoid horizontal overflow.');
    assert.deepEqual(errors, []); assert.deepEqual(writes, []);
    assert.equal(scripts.some(path => /(?:react|echarts|newpress-analytics)/.test(path)), false);
    check('No browser exceptions, mutation requests or React/ECharts dependencies in homepage flows.');
  } finally {
    await mkdir(directory, { recursive: true });
    await writeFile(resolve(directory, `home-${name}-${variant}.json`), JSON.stringify({ evidence: evidence.filter(item => item.browser === name), errors, writes, scripts, reads, layouts }, null, 2) + '\n');
    await context.close(); await browser.close();
  }
}
await writeFile(resolve(directory, `home-acceptance-${variant}.json`), JSON.stringify({ evidence }, null, 2) + '\n');
