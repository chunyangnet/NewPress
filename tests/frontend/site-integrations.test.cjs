const test = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const vm = require('node:vm');
const source = readFileSync(join(__dirname, '../../internal/httpserver/assets/site-integrations.js'), 'utf8');
function fixture(config, raw, { consentRequired = false, controller } = {}) {
  const scripts = [];
  const listeners = {};
  const document = {
    currentScript: { dataset: { siteIntegrations: raw ?? JSON.stringify(config), consentRequired: String(consentRequired) } },
    addEventListener(name, callback) { listeners[name] = callback; },
    createElement: () => ({ attrs: {}, setAttribute(key, value) { this.attrs[key] = value; } }),
    head: { appendChild(script) { scripts.push(script); } },
  };
  const window = {};
  if (controller) window.NewPressCookieConsent = controller;
  const context = vm.createContext({ window, document });
  const run = () => vm.runInContext(source, context);
  run();
  return { window, scripts, run, change: detail => listeners['newpress:cookie-consent']?.({ detail }) };
}
test('disabled or malformed configuration produces no scripts or global queues', () => {
  for (const f of [fixture({}), fixture({ enabled: false, clarity_id: 'test' }), fixture({}, '{invalid')]) {
    assert.equal(f.scripts.length, 0);
    assert.deepEqual(Object.keys(f.window), []);
  }
});
test('providers initialize independent queues, attributes and endpoints once', () => {
  const f = fixture({ enabled: true, google_id: 'G-123456', google_tag_id: 'GTM-123456', baidu_id: 'baidu', clarity_id: 'clarity', cloudflare_token: 'cf-token', matomo_url: 'https://stats.example/matomo', matomo_site_id: '2', umami_url: 'https://umami.example/script.js', umami_website_id: 'website', plausible_url: 'https://plausible.example/js/pa-site.js' });
  assert.equal(f.scripts.length, 8);
  assert.ok(f.scripts.every(script => script.async));
  assert.equal(f.window.dataLayer[1][0], 'config');
  assert.equal(f.window.dataLayer[1][1], 'G-123456');
  assert.equal(f.window.dataLayer[2].event, 'gtm.js');
  assert.ok(Array.isArray(f.window._hmt));
  f.window.clarity('event', 'test');
  assert.equal(f.window.clarity.q[0][0], 'event');
  assert.equal(f.window._paq[0][1], 'https://stats.example/matomo/matomo.php');
  assert.equal(f.window._paq[1][1], '2');
  assert.equal(f.window._paq[2][0], 'trackPageView');
  assert.equal(f.scripts.find(script => script.src.includes('umami.example')).attrs['data-website-id'], 'website');
  assert.equal(JSON.parse(f.scripts.find(script => script.src.includes('cloudflareinsights')).attrs['data-cf-beacon']).token, 'cf-token');
  assert.equal(Object.keys(f.window.plausible.o).length, 0);
  f.run();
  assert.equal(f.scripts.length, 8);
});
test('a single provider loads without unrelated third party requests', () => {
  const f = fixture({ enabled: true, clarity_id: 'clarity' });
  assert.equal(f.scripts.length, 1);
  assert.equal(f.scripts[0].src, 'https://www.clarity.ms/tag/clarity');
  assert.equal(f.window.gtag, undefined);
  assert.equal(f.window._paq, undefined);
});

test('required consent fails closed without a controller, even for an affirmative event payload', () => {
  const f = fixture({ enabled: true, clarity_id: 'clarity' }, undefined, { consentRequired: true });
  f.change({ analytics: true });
  assert.equal(f.scripts.length, 0);
  assert.equal(f.window.clarity, undefined);
});

test('consent starts analytics only after a validated affirmative choice and never duplicates providers', () => {
  let allowed = false;
  const f = fixture({ enabled: true, clarity_id: 'clarity' }, undefined, {
    consentRequired: true, controller: { allowsAnalytics: () => allowed },
  });
  f.change({ analytics: true });
  assert.equal(f.scripts.length, 0);
  allowed = true;
  f.change();
  f.change();
  f.run();
  assert.equal(f.scripts.length, 1);
});

test('a saved affirmative choice loads analytics immediately', () => {
  const f = fixture({ enabled: true, clarity_id: 'clarity' }, undefined, {
    consentRequired: true, controller: { allowsAnalytics: () => true },
  });
  assert.equal(f.scripts.length, 1);
});
