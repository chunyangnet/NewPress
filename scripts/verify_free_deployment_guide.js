// Run in an existing named browser session against the local docs server:
// npx --yes --package @playwright/cli playwright-cli -s=newpress-guide-review run-code --filename scripts/verify_free_deployment_guide.js
// Screenshots are written to output/playwright/ (create that directory first).
async (page) => {
  const assert = (condition, message) => {
    if (!condition) throw new Error(message);
  };
  const guideLocation = new URL(page.url());
  guideLocation.searchParams.set('verify', String(Date.now()));
  const guideURL = guideLocation.href;
  assert(new URL(guideURL).pathname.endsWith('/docs/FREE_DEPLOYMENT_GUIDE.html'), 'Open the local deployment guide before running this check.');
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(guideURL);
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const labels = {
    purpose: { production: '是，准备正式使用', test: '否，只做学习或测试' },
    server: { yes: '有，我能登录', no: '没有' },
    sleep: { yes: '可以', no: '不可以，要持续在线' },
    budget: { yes: '可以，先看套餐价格', no: '暂时不想付费' },
  };
  // Explicit expected outcomes cover every answer combination, including the two
  // previously misleading test-use cases (paid hosting and refusing sleep).
  const cases = [
    ['production', 'yes', 'yes', 'yes', 'server'],
    ['production', 'yes', 'yes', 'no', 'server'],
    ['production', 'yes', 'no', 'yes', 'server'],
    ['production', 'yes', 'no', 'no', 'server'],
    ['production', 'no', 'yes', 'yes', 'paid'],
    ['production', 'no', 'yes', 'no', 'vm'],
    ['production', 'no', 'no', 'yes', 'paid'],
    ['production', 'no', 'no', 'no', 'vm'],
    ['test', 'yes', 'yes', 'yes', 'server'],
    ['test', 'yes', 'yes', 'no', 'server'],
    ['test', 'yes', 'no', 'yes', 'server'],
    ['test', 'yes', 'no', 'no', 'server'],
    ['test', 'no', 'yes', 'yes', 'paid'],
    ['test', 'no', 'yes', 'no', 'cloud'],
    ['test', 'no', 'no', 'yes', 'paid'],
    ['test', 'no', 'no', 'no', 'vm'],
  ];
  const results = [];
  const nextLinks = { server: 'LINUX_DEPLOYMENT.md', paid: '#route-paid', cloud: '#cloud-preview', vm: '#route-vm' };
  for (const [purpose, server, sleep, budget, expected] of cases) {
    for (const [name, value] of Object.entries({ purpose, server, sleep, budget })) {
      await page.getByRole('radio', { name: labels[name][value], exact: true }).check();
    }
    const recommended = page.locator('.route.is-recommended');
    assert(await recommended.count() === 1, 'Exactly one route should be recommended.');
    assert(await recommended.getAttribute('data-route') === expected, 'Wrong recommendation for ' + [purpose, server, sleep, budget].join('/'));
    const message = await page.locator('#route-recommendation').innerText();
    if (purpose === 'test') assert(!/正式|长期使用/.test(message), 'Test users received production-use copy.');
    if (expected === 'cloud') assert(/尚未开放/.test(message), 'The unavailable cloud route must state its status.');
    assert(await page.locator('#recommendation-next').getAttribute('href') === nextLinks[expected], 'Recommendation next step does not match the route.');
    results.push({ purpose, server, sleep, budget, recommended: expected });
  }

  // Restore default answers before layout inspection.
  await page.goto(guideURL);
  const viewports = [];
  for (const width of [320, 390, 768, 1024, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    const dimensions = await page.evaluate(() => ({
      viewport: window.innerWidth,
      document: document.documentElement.scrollWidth,
      body: document.body.scrollWidth,
      columns: getComputedStyle(document.querySelector('.route-grid')).gridTemplateColumns,
    }));
    assert(dimensions.document <= width && dimensions.body <= width, 'Horizontal overflow at ' + width + 'px: ' + JSON.stringify(dimensions));
    viewports.push(dimensions);
  }
  await page.screenshot({ path: 'output/playwright/free-deployment-guide-desktop.png', fullPage: true });
  await page.locator('#route-picker').screenshot({ path: 'output/playwright/free-deployment-guide-desktop-routes.png' });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await page.screenshot({ path: 'output/playwright/free-deployment-guide-mobile.png', fullPage: true });
  await page.locator('#cloud-preview').screenshot({ path: 'output/playwright/free-deployment-guide-mobile-preview.png' });

  const links = await page.locator('a').evaluateAll(nodes => nodes.map(node => ({
    href: node.getAttribute('href'), target: node.getAttribute('target'), rel: node.getAttribute('rel'),
  })));
  const approvedExternal = new Set([
    'https://render.com/docs/free',
    'https://supabase.com/pricing',
    'https://www.oracle.com/cloud/free/',
    'https://supabase.com/docs/guides/platform/backups',
    'https://supabase.com/docs/guides/api/securing-your-api',
  ]);
  const checkedLocal = new Set();
  const actualExternal = new Set();
  for (const link of links) {
    assert(link.href && link.href !== '#', 'Empty link target.');
    if (link.href.startsWith('#')) {
      assert(await page.locator(link.href).count() === 1, 'Missing fragment ' + link.href);
    } else if (link.href.startsWith('https://')) {
      assert(approvedExternal.has(link.href), 'Unexpected external or creation link: ' + link.href);
      actualExternal.add(link.href);
      assert(link.target === '_blank' && /noopener/.test(link.rel) && /noreferrer/.test(link.rel), 'External link is missing isolation attributes.');
    } else if (!checkedLocal.has(link.href)) {
      const response = await page.request.get(new URL(link.href, guideURL).href);
      assert(response.ok(), 'Local document is unavailable: ' + link.href);
      checkedLocal.add(link.href);
    }
  }
  assert(actualExternal.size === approvedExternal.size, 'An expected official guidance link is missing.');
  assert(await page.locator('#cloud-preview details').count() === 5, 'Expected five preview steps.');
  let previewStep = 0;
  for (const details of await page.locator('#cloud-preview details').all()) {
    await details.locator('summary').click();
    assert(await details.getAttribute('open') !== null, 'A preview step did not open.');
    assert(await details.locator('.step-body').isVisible(), 'Expanded step text is hidden.');
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Expanded preview overflows horizontally.');
    if (previewStep === 1) {
      await page.locator('#cloud-preview').screenshot({ path: 'output/playwright/free-deployment-guide-mobile-preview-open.png' });
    }
    await details.locator('summary').click();
    previewStep += 1;
  }
  assert(await page.locator('input:not([type="radio"]), textarea, form').count() === 0, 'The preview must not collect credentials.');
  assert(await page.evaluate(() => localStorage.length + sessionStorage.length) === 0, 'Route selection should not persist browser data.');

  const noJS = await page.context().browser().newContext({ javaScriptEnabled: false, viewport: { width: 390, height: 844 }, reducedMotion: 'reduce' });
  try {
    const fallback = await noJS.newPage();
    await fallback.goto(guideURL);
    assert(await fallback.getByText('手动选路', { exact: true }).isVisible(), 'No-script guidance is missing.');
    assert(!await fallback.locator('#route-questions').isVisible(), 'Nonfunctional question controls are visible without JavaScript.');
    assert(!await fallback.locator('#route-recommendation').isVisible(), 'A fabricated recommendation is visible without JavaScript.');
    assert(await fallback.getByRole('link', { name: '打开 Linux 小白安装指南', exact: true }).isVisible(), 'The usable installation route is missing without JavaScript.');
    await fallback.locator('#cloud-preview details').first().locator('summary').click();
    assert(await fallback.locator('#cloud-preview .step-body').first().isVisible(), 'Native preview disclosure failed without JavaScript.');
    const fallbackWidth = await fallback.evaluate(() => document.documentElement.scrollWidth);
    assert(fallbackWidth <= 390, 'No-script view overflows horizontally.');
    await fallback.locator('#route-picker').screenshot({ path: 'output/playwright/free-deployment-guide-no-js.png' });
  } finally {
    await noJS.close();
  }
  assert(errors.length === 0, 'Browser errors: ' + errors.join('; '));
  return { combinations: results, viewports, localDocuments: [...checkedLocal], externalDestinations: [...approvedExternal], noJavaScript: 'passed', previewDisclosures: 5, browserErrors: errors };
}
