const test = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const source = readFileSync('internal/coreui/assets/js/chunnel-ai-settings.source.js', 'utf8');
const tick = () => new Promise(resolve => setImmediate(resolve));

function fixture({ missing, missingProvider, readOnly = false, script = source } = {}) {
  const nodes = {}, requests = [], timers = new Map();
  let reloads = 0;
  const element = () => ({ dataset: {}, disabled: false, value: '', checked: false, textContent: '', listeners: {}, children: [],
    addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); },
    setAttribute() {}, removeAttribute() {}, focus() {}, reportValidity() { return true; }, querySelectorAll() { return []; },
    replaceChildren(...children) { this.children = children; }, append(...children) { this.children.push(...children); }
  });
  for (const name of ['form', 'fields', 'list', 'default', 'image-default', 'status', 'save', 'add', 'reload', 'empty', 'template', 'ops-enabled', 'ops-details', 'ops-costs', 'ops-balances', 'ops-charts', 'ops-retention', 'ops-records']) nodes[name] = element();
  nodes.fields.disabled = readOnly;
  nodes.form.dataset.userId = 'owner';
  nodes.list.querySelectorAll = () => nodes.list.children;
  const cloneCard = () => {
    const card = element(), fields = {};
    card.isConnected = true;
    card.querySelector = selector => fields[selector] ||= element();
    return card;
  };
  nodes.template.content = { querySelector: selector => selector === `[data-provider-${missingProvider}]` ? null : ({}), firstElementChild: { cloneNode: cloneCard } };
  nodes.status.textContent = '正在加载 AI 设置…';
  if (missing) delete nodes[missing];
  const root = { hidden: false, querySelector: selector => nodes[selector.slice('[data-ai-config-'.length, -1)] || null };
  const document = { querySelector: selector => selector === '[data-ai-config]' ? root : null, createElement: element, addEventListener() {} };
  const window = { location: { reload() { reloads++; } }, addEventListener() {} };
  const context = { document, window, AbortController, crypto: {randomUUID: () => 'new-provider'},
    setTimeout(fn) { const key = Symbol(); timers.set(key, fn); return key; }, clearTimeout(key) { timers.delete(key); },
    fetch(url, options) { return new Promise((resolve, reject) => {
      options.signal.addEventListener('abort', () => reject(Object.assign(new Error('aborted'), {name: 'AbortError'})));
      requests.push({ url, body: options.body, resolve(data, ok = true) { resolve({ok, json: async () => data}); }, reject });
    }); }
  };
  vm.runInNewContext(script, context);
  return { nodes, requests, timers, reloads: () => reloads, click(name) { nodes[name].listeners.click.forEach(fn => fn()); },
    field(name) { return nodes.list.children[0].querySelector(`[data-provider-${name}]`); },
    fire(name, event = 'change') { this.field(name).listeners[event].forEach(fn => fn()); }
  };
}

for (const script of [source, readFileSync('internal/coreui/assets/js/chunnel-ai-settings.js', 'utf8')]) {
  test('older cached template reports version mismatch instead of hanging before fetch', () => {
    const f = fixture({missing: 'image-default', script});
    assert.equal(f.requests.length, 0);
    assert.match(f.nodes.status.textContent, /版本不一致/);
    assert.equal(f.nodes.status.dataset.tone, 'error');
    assert.equal(f.nodes.save.disabled, true);
    assert.equal(f.nodes.add.disabled, true);
    f.click('reload'); assert.equal(f.reloads(), 1);
  });
  test('template without endpoint approval field reports mismatch before fetching', () => {
    const f = fixture({missingProvider: 'allow-insecure', script});
    assert.equal(f.requests.length, 0);
    assert.match(f.nodes.status.textContent, /版本不一致/);
  });
}

const localProvider = {id: 'local', name: '本地', base_url: 'http://localhost:11434/v1', allow_insecure: true, models: ['local-model'], default_model: 'local-model'};
async function providerFixture(provider = localProvider) {
  const f = fixture();
  f.requests[0].resolve({revision: 'revision-a', providers: [provider], default_provider: 'local'}); await tick();
  return f;
}
async function resolveSession(f) {
  f.requests.at(-1).resolve({authenticated: true, user_id: 'owner', csrf_token: 'test-token'}); await tick();
}

test('saved local approval restores, key is optional, editing address clears approval', async () => {
  const f = await providerFixture();
  assert.equal(f.field('allow-insecure').checked, true);
  assert.equal(f.field('key').required, false);
  f.field('base').value = 'http://localhost:1234/v1'; f.fire('base', 'input');
  assert.equal(f.field('allow-insecure').checked, false);
  assert.equal(f.field('key').required, true);
  f.field('allow-insecure').checked = true; f.fire('allow-insecure');
  assert.equal(f.field('key').required, false);
});

test('saved credentials cannot be probed or silently reused after changing address', async () => {
  const f = await providerFixture({...localProvider, has_key: true});
  f.field('base').value = 'http://localhost:1234/v1'; f.fire('base', 'input');
  f.field('allow-insecure').checked = true; f.fire('allow-insecure');
  assert.equal(f.field('key').required, true);
  f.fire('probe', 'click');
  assert.equal(f.requests.length, 1);
  assert.match(f.field('status').textContent, /重新填写 Key/);
});

test('saved probe uses provider id and changed approval cannot silently use saved trust', async () => {
  const f = await providerFixture({...localProvider, has_key: true});
  f.field('allow-insecure').checked = false; f.fire('allow-insecure');
  f.fire('probe', 'click');
  assert.equal(f.requests.length, 1);
  assert.match(f.field('status').textContent, /先保存/);
  f.field('allow-insecure').checked = true; f.fire('allow-insecure');
  f.fire('probe', 'click'); await resolveSession(f);
  assert.deepEqual(JSON.parse(f.requests.at(-1).body), {provider_id: 'local'});
  f.requests.at(-1).resolve({models: [{id: 'local-model'}]}); await tick();
  assert.match(f.field('status').textContent, /找到 1 个/);
});

test('keyless probe sends explicit approval and ignores result when approval changes', async () => {
  const f = await providerFixture();
  f.field('base').value = 'http://localhost:1234/v1'; f.fire('base', 'input');
  f.field('allow-insecure').checked = true; f.fire('allow-insecure');
  f.fire('probe', 'click'); await resolveSession(f);
  assert.deepEqual(JSON.parse(f.requests.at(-1).body), {base_url: 'http://localhost:1234/v1', api_key: '', protocol: '', allow_insecure: true});
  f.field('allow-insecure').checked = false; f.fire('allow-insecure');
  f.requests.at(-1).resolve({models: [{id: 'obsolete-model'}]}); await tick();
  assert.match(f.field('status').textContent, /重新探测/);
  assert.equal(f.field('discovered').hidden, true);
});

test('save sends approval and reload restores the approved local provider', async () => {
  const f = await providerFixture();
  f.nodes.form.listeners.submit[0]({preventDefault() {}}); await resolveSession(f);
  const payload = JSON.parse(f.requests.at(-1).body);
  assert.equal(payload.providers[0].allow_insecure, true);
  assert.equal(payload.providers[0].api_key, '');
  f.requests.at(-1).resolve({...payload, revision: 'revision-b'}); await tick();
  assert.equal(f.field('allow-insecure').checked, true);
  assert.match(f.nodes.status.textContent, /已保存/);
});

test('settings load enables adding providers and clears the loading message', async () => {
  const f = fixture();
  assert.equal(f.requests[0].url, '/api/v1/admin/ai/settings');
  f.requests[0].resolve({revision: 'revision-a', providers: [], default_provider: ''}); await tick();
  assert.match(f.nodes.status.textContent, /添加第一个/);
  assert.equal(f.nodes.add.disabled, false);
  assert.equal(f.nodes.save.disabled, true);
  assert.equal(f.timers.size, 0);
});

test('timeout exits loading and retry can recover', async () => {
  const f = fixture();
  [...f.timers.values()][0](); await tick();
  assert.match(f.nodes.status.textContent, /超时/);
  assert.equal(f.nodes.reload.disabled, false);
  assert.equal(f.nodes.add.disabled, true);
  f.click('reload');
  f.requests[1].resolve({revision: 'revision-b', providers: []}); await tick();
  assert.equal(f.nodes.add.disabled, false);
});

test('authorization failures display server error and remain retryable', async () => {
  const f = fixture();
  f.requests[0].resolve({error: {message: '请重新登录'}}, false); await tick();
  assert.equal(f.nodes.status.textContent, '请重新登录');
  assert.equal(f.nodes.reload.disabled, false);
  assert.equal(f.nodes.save.disabled, true);
});

test('unsupported saved protocols cannot silently become Chat Completions', async () => {
  const f = fixture();
  f.requests[0].resolve({revision: 'revision-a', providers: [{id: 'future', protocol: 'future-protocol', models: ['model']}]}); await tick();
  assert.match(f.nodes.status.textContent, /不兼容/);
  assert.equal(f.nodes.add.disabled, true);
  assert.equal(f.nodes.save.disabled, true);
});

test('read-only settings stay disabled after a successful load', async () => {
  const f = fixture({readOnly: true});
  f.requests[0].resolve({revision: 'revision-a', providers: []}); await tick();
  assert.equal(f.nodes.add.disabled, true);
  assert.equal(f.nodes.fields.disabled, true);
});
