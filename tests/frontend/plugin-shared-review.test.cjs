const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {compile}=require('./helpers/typescript.cjs');
const source=process.env.NEWPRESS_PLUGIN_RECORDS_SOURCE?fs.readFileSync(process.env.NEWPRESS_PLUGIN_RECORDS_SOURCE,'utf8'):compile('frontend/core/plugin-records.ts');
const tick = () => new Promise(resolve => setImmediate(resolve));
const digest = n => n.toString(16).padStart(64, '0');
const receipt = (n = 1, extra = {}) => ({type: 'content_effect', reference: digest(n), bytes: 600, plugin: 'reader', job_id: digest(100), result: {operation_id: digest(n + 1000), post_id: 12, previous_version: 3, version: 4, changed: true, committed_at: '2026-11-25T01:00:00Z'}, ...extra});
const quarantine = (extra = {}) => ({id: digest(10000), type: 'content_effect', reference: digest(1), bytes: 600, fault: 'index_mismatch', captured_at: '2026-11-25T02:00:00Z', operator: 'maintainer', reason: 'Checked original identity', ...extra});
const page = (items = [receipt()], total = items.length, next_offset = -1) => ({items, total, next_offset});

function fixture({available = true, hidden = false} = {}) {
  const make = () => ({dataset: {}, value: '', disabled: false, hidden: false, children: [], events: {}, attrs: new Map(), text: '',
    get textContent() { return this.text + this.children.map(n => n.textContent).join(''); },
    set textContent(value) { this.text = String(value); this.children = []; },
    set innerHTML(_) { throw new Error('Unsafe HTML rendering'); },
    append(...nodes) { this.children.push(...nodes); }, replaceChildren(...nodes) { this.text = ''; this.children = nodes; },
    setAttribute(name, value) { this.attrs.set(name, String(value)); }, hasAttribute(name) { return this.attrs.has(name); },
    addEventListener(name, fn) { this.events[name] = fn; }
  });
  const nodes = new Map(), calls = [], timers = new Map(), channels = [];
  let timerID = 0;
  for (const name of ['form', 'list', 'status', 'results', 'prev', 'next', 'summary', 'page', 'access', 'title', 'type', 'plugin', 'job', 'limit', 'search']) nodes.set(name, make());
  const get = name => nodes.get(name);
  const scope = make(); scope.value = 'records'; scope.attrs.set('data-review-scope', '');
  get('type').value = 'content_effect'; get('limit').value = '20'; get('access').hidden = true;
  const selector = value => nodes.get(value.replace(/^\[data-review-/, '').replace(/\]$/, '')) || null;
  get('form').querySelector = value => value === '[data-review-scope]:checked' ? scope : selector(value);
  get('form').querySelectorAll = () => [scope, ...['type', 'plugin', 'job', 'limit', 'search'].map(get)];
  get('form').reportValidity = () => true;
  const root = {dataset: {available: String(available), userId: '7'}, querySelector: selector};
  const document = {hidden, events: {}, documentElement: {dataset: {siteTimezone: 'Asia/Shanghai'}}, querySelector: () => root, createElement: () => make(), addEventListener(name, fn) { this.events[name] = fn; }};
  const window = {events: {}, addEventListener(name, fn) { this.events[name] = fn; }};
  vm.runInNewContext(source, {document, window, URLSearchParams, AbortController, DOMException,
    setTimeout(fn, ms) { const id = ++timerID; timers.set(id, {fn, ms}); return id; }, clearTimeout(id) { timers.delete(id); },
    BroadcastChannel: class { constructor(name) { this.name = name; channels.push(this); } addEventListener(_, fn) { this.message = fn; } close(){this.closed=true;} },
    fetch: (url, options) => new Promise((resolve, reject) => calls.push({url, options, resolve, reject}))
  });
  const fire = (name, event = 'click') => get(name).events[event]({target: get(name), preventDefault() {}});
  const change = (name, value, event = 'change') => { const target = name === 'scope' ? scope : get(name); target.value = value; get('form').events[event]({target}); };
  const reply = (body, {index = calls.length - 1, status = 200, owner = '7', text} = {}) => calls[index].resolve({ok: status >= 200 && status < 300, status, headers: {get: () => owner}, text: text || (async () => typeof body === 'string' ? body : JSON.stringify(body))});
  const confirm = async () => { await tick(); assert.match(calls.at(-1).url, /shared-review-session$/); reply({user_id: '7', available: true}); await tick(); };
  const show = async body => { reply(body); await confirm(); };
  const timer = ms => { const entry = [...timers].find(([, task]) => task.ms === ms); assert.ok(entry, 'expected timer ' + ms); timers.delete(entry[0]); void entry[1].fn(); };
  return {get, root, calls, timers, channels, document, window, fire, change, reply, confirm, show, timer};
}

test('shows historical results only after identity confirmation, preserving read-only request options', async () => {
  const f = fixture(); f.reply(page()); await tick();
  assert.equal(f.get('list').children.length, 0);
  await f.confirm();
  assert.match(f.get('list').textContent, /3 → 4/);
  assert.match(f.get('list').textContent, /不代表内容当前版本/);
  assert.match(f.get('list').textContent, /09:00:00/);
  for (const {options} of f.calls) {
    assert.equal(options.method, 'GET'); assert.equal(options.credentials, 'same-origin');
    assert.equal(options.cache, 'no-store'); assert.equal(options.redirect, 'error');
    assert.equal(options.headers['X-NewPress-Review-User'], '7'); assert.equal(options.body, undefined);
  }
  assert.equal(f.get('results').attrs.get('aria-busy'), 'false');
});

test('pagination uses the accepted filter and editing clears rows before another query', async () => {
  const f = fixture(); await f.show(page(Array.from({length: 20}, (_, i) => receipt(i + 1)), 21, 20));
  f.fire('next'); assert.equal(f.get('list').children.length, 0);
  assert.equal(new URL(f.calls.at(-1).url, 'https://example.test').searchParams.get('offset'), '20');
  await f.show(page([receipt(21)], 21)); assert.match(f.get('page').textContent, /21–21 \/ 21/);
  f.fire('prev'); await f.show(page(Array.from({length: 20}, (_, i) => receipt(i + 1)), 21, 20));
  f.change('plugin', 'demo--plugin', 'input'); assert.equal(f.get('list').children.length, 0);
  f.fire('form', 'submit'); await f.show(page([receipt(1, {plugin: 'demo--plugin'})]));
  assert.match(f.get('list').textContent, /demo--plugin/);
});

test('authority selection removes an incompatible job filter and accepts omitted false flags', async () => {
  const f = fixture(); await f.show(page());
  f.change('job', digest(100), 'input'); f.change('type', 'authority');
  assert.equal(f.get('job').disabled, true); assert.equal(f.get('job').value, '');
  f.fire('form', 'submit'); assert.equal(new URL(f.calls.at(-1).url, 'https://example.test').searchParams.has('job'), false);
  await f.show(page([{type: 'authority', reference: digest(1), bytes: 300, plugin: 'reader', revision: 5}]));
  assert.match(f.get('list').textContent, /已停用/);
});

test('renders maintenance declarations as text and exposes repair provenance without hidden fields', async () => {
  const f = fixture(); f.change('scope', 'quarantines');
  const reason = '<img src=x onerror=alert(1)> 9007199254740993 \\"quoted"';
  await f.show(page([quarantine({reason, original: 'SECRET', repair: {at: '2026-11-25T03:00:00Z', operator: '<script>bad()</script>', reason: 'Identity checked', disposition: 'receipt_preserved', output_sha256: digest(3), input_sha256: digest(4)}})]));
  assert.match(f.calls[1].url, /shared-quarantines/);
  assert.ok(f.get('list').textContent.includes(reason)); assert.ok(f.get('list').textContent.includes('<script>bad()</script>'));
  assert.match(f.get('list').textContent, /已修复 · 保留审计/); assert.doesNotMatch(f.get('list').textContent, /SECRET/);
});

test('late responses including delayed body reads cannot repaint a newer query', async () => {
  const f = fixture(); let finishBody;
  f.reply(null, {text: () => new Promise(resolve => { finishBody = resolve; })}); await tick();
  f.change('plugin', 'new-reader', 'input'); f.fire('form', 'submit');
  await f.show(page([receipt(2, {plugin: 'new-reader'})]));
  finishBody(JSON.stringify(page())); await tick();
  assert.match(f.get('list').textContent, /new-reader/); assert.equal(f.get('list').children.length, 1);
});

test('an ignored abort and a late identity failure cannot lock the current query', async () => {
  const f = fixture(); f.fire('form', 'submit');
  assert.equal(f.calls[0].options.signal.aborted, true);
  await f.show(page()); f.reply({}, {index: 0, status: 403}); await tick();
  assert.equal(f.get('access').hidden, true); assert.equal(f.get('list').children.length, 1);
});

for (const [name, options] of [['401', {status: 401}], ['403', {status: 403}], ['409', {status: 409}], ['different account header', {owner: '8'}], ['missing account header', {owner: null}]]) {
  test('clears and locks results for ' + name, async () => {
    const f = fixture(); await f.show(page()); f.fire('form', 'submit'); f.reply(page(), options); await tick();
    assert.equal(f.get('list').children.length, 0); assert.equal(f.get('access').hidden, false); assert.equal(f.get('search').disabled, true);
    const before = f.calls.length; f.fire('form', 'submit'); assert.equal(f.calls.length, before);
  });
}

for (const event of ['blur', 'pagehide', 'visibilitychange']) {
  test(event + ' clears records and requires manual requery on return', async () => {
    const f = fixture(); await f.show(page());
    if (event === 'visibilitychange') { f.document.hidden = true; f.document.events[event](); }
    else f.window.events[event]();
    assert.equal(f.get('list').children.length, 0); assert.equal(f.timers.size, 0);
    f.document.hidden = false; f.window.events.focus();
    assert.equal(f.calls.length, 2); assert.match(f.get('status').textContent, /请重新查询/);
    f.fire('form', 'submit'); await f.show(page()); assert.equal(f.get('list').children.length, 1);
  });
}

test('visible heartbeat checks permissions without rescanning records and stops after failure', async () => {
  const f = fixture(); await f.show(page()); f.timer(15000);
  assert.match(f.calls.at(-1).url, /shared-review-session$/); f.reply({}, {status: 503}); await tick();
  assert.equal(f.get('list').children.length, 0); assert.equal(f.timers.size, 0); assert.equal(f.get('search').disabled, false);
});

test('session revocation between record read and confirmation never exposes results', async () => {
  const f = fixture(); f.reply(page()); await tick(); f.reply({}, {status: 403}); await tick();
  assert.equal(f.get('list').children.length, 0); assert.equal(f.get('search').disabled, true);
});

for (const event of ['local', 'broadcast', 'metadata']) {
  test(event + ' account change prevents delayed results from being shown', async () => {
    const f = fixture(); f.reply(page()); await tick();
    if (event === 'local') f.document.events['chunnel:session']();
    else if (event === 'broadcast') f.channels[0].message({data: {type: 'session'}});
    else f.root.dataset.userId = '8';
    f.reply({user_id: '7', available: true}); await tick(); assert.equal(f.get('list').children.length, 0);
  });
}

for (const [status, message] of [[429, /过于频繁/], [503, /暂不可用/], [500, /未能读取/]]) {
  test('HTTP ' + status + ' clears stale results and allows manual retry', async () => {
    const f = fixture(); await f.show(page()); f.fire('form', 'submit'); f.reply({}, {status}); await tick();
    assert.equal(f.get('list').children.length, 0); assert.match(f.get('status').textContent, message); assert.equal(f.get('search').disabled, false);
    f.fire('form', 'submit'); await f.show(page([])); assert.match(f.get('status').textContent, /没有符合/);
  });
}

test('timeout and network errors stop loading without preserving results', async () => {
  for (const timeout of [false, true]) {
    const f = fixture();
    if (timeout) { f.timer(12000); f.calls[0].reject(new DOMException('aborted', 'AbortError')); }
    else f.calls[0].reject(new TypeError('offline'));
    await tick(); assert.equal(f.get('results').attrs.get('aria-busy'), 'false'); assert.equal(f.get('list').children.length, 0);
    assert.match(f.get('status').textContent, timeout ? /读取超时/ : /网络连接失败/);
  }
});

test('fatal diagnostics stay separate from an empty successful result', async () => {
  for (const fatal of ['invalid_quarantine', 'storage_capacity']) {
    const f = fixture(); f.reply({...page([]), fatal}); await tick();
    assert.equal(f.calls.length, 1); assert.equal(f.get('list').children.length, 0); assert.equal(f.get('status').dataset.tone, 'error');
    assert.doesNotMatch(f.get('status').textContent, /没有符合/);
  }
});

test('rejects malformed pagination, filter mismatches and impossible result versions', async () => {
  const bad = [page([receipt()], 21, 20), page([receipt(), receipt()]), page([receipt()], 1, 1), page([receipt(1, {plugin: 'a'})]),
    page([{...receipt(), result: {...receipt().result, version: 5}}]), page([receipt()], -1), {...page([]), fatal: 'unknown'}];
  for (const data of bad) {
    const f = fixture(); f.reply(data); await tick(); assert.equal(f.get('list').children.length, 0); assert.match(f.get('status').textContent, /格式异常/);
  }
});

test('retains exact int64 identifiers, revisions and adjacent versions above safe Number range', async () => {
  const f = fixture();
  const raw = JSON.stringify(page()).replace('"post_id":12', '"post_id":9223372036854775807').replace('"previous_version":3', '"previous_version":9007199254740992').replace('"version":4', '"version":9007199254740993');
  await f.show(raw); assert.match(f.get('list').textContent, /9223372036854775807/); assert.match(f.get('list').textContent, /9007199254740992 → 9007199254740993/);
  f.change('type', 'authority'); f.fire('form', 'submit');
  await f.show(JSON.stringify(page([{type: 'authority', reference: digest(1), bytes: 300, plugin: 'reader', revision: 'BIG'}])).replace('"BIG"', '9223372036854775807'));
  assert.match(f.get('list').textContent, /9223372036854775807/);
});

test('rejects malformed numeric syntax, unsafe exponents, wrong numeric types and int64 overflow', async () => {
  for (const token of ['09007199254740992', '-9007199254740993', '9.007199254740993e15', '9007199254740993.1', '9223372036854775808', '"9007199254740993"']) {
    const f = fixture(); f.reply(JSON.stringify(page()).replace('"post_id":12', '"post_id":' + token)); await tick();
    assert.equal(f.get('list').children.length, 0); assert.match(f.get('status').textContent, /格式异常/);
  }
});

test('numeric audit declarations remain invalid while escaped marker-like user text is preserved', async () => {
  const f = fixture(); f.change('scope', 'quarantines');
  f.reply(JSON.stringify(page([quarantine()])).replace('"maintainer"', '9007199254740993')); await tick();
  assert.match(f.get('status').textContent, /格式异常/);
  f.fire('form', 'submit');
  await f.show(JSON.stringify(page([quarantine({reason: '__review_integer_0 and 9007199254740993'})])).replace('__review_integer_', '\\u005f_review_integer_'));
  assert.match(f.get('list').textContent, /__review_integer_0 and 9007199254740993/);
});

test('file storage mode makes no record or session requests', () => {
  assert.equal(fixture({available: false}).calls.length, 0);
});

const webhookRecord = (extra = {}) => ({type:'webhook_intent',reference:digest(2),bytes:1500,plugin:'reader',job_id:digest(100),webhook:{operation_id:digest(1002),version:3,state:'uncertain',created_at:'2026-11-25T01:00:00Z',updated_at:'2026-11-25T01:02:00Z',result_at:'2026-11-25T01:02:00Z',outcome:{code:'maintenance'}},...extra});

test('maintenance webhook projection shows original state without rendering request secrets', async () => {
  const f = fixture(); f.change('type','webhook_intent'); f.fire('form','submit');
  const value = webhookRecord({url:'SECRET URL',body:'SECRET BODY',attempt_token:'SECRET TOKEN',slot:'SECRET SLOT'});
  await f.show(page([value]));
  assert.match(f.calls[1].url,/type=webhook_intent/); assert.match(f.get('list').textContent,/Webhook 意图/);
  assert.match(f.get('list').textContent,/待人工核对/); assert.match(f.get('list').textContent,/离线维护已停止旧操作/);
  assert.match(f.get('list').textContent,/不会重新发送/); assert.doesNotMatch(f.get('list').textContent,/SECRET/);
  assert.equal(f.get('job').disabled,false);
});

test('all-types results include webhook diagnostics and bounded invalid-data records', async () => {
  const f = fixture(); f.change('type',''); f.fire('form','submit');
  await f.show(page([receipt(),webhookRecord(),{type:'webhook_intent',reference:digest(3),bytes:1,fault:'invalid_data'}]));
  assert.equal(f.get('list').children.length,3); assert.match(f.get('list').textContent,/身份无法确认/);
});

test('webhook maintenance audits accept only their own dispositions and preserve declaration text', async () => {
  for (const disposition of ['webhook_cancelled','webhook_uncertain','webhook_preserved','duplicate_webhook_preserved']) {
    const f=fixture(); f.change('type','webhook_intent'); f.change('scope','quarantines');
    const repair={at:'2026-11-25T03:00:00Z',operator:'offline',reason:'<script>declared text</script>',disposition,output_sha256:digest(4)};
    if(disposition.startsWith('duplicate_'))repair.kept_reference=digest(4);
    await f.show(page([quarantine({type:'webhook_intent',repair})]));
    assert.equal(f.get('list').children.length,1); assert.match(f.get('list').textContent,/已修复 · 保留审计/);
    assert.ok(f.get('list').textContent.includes(repair.reason));
  }
  const f=fixture(); f.change('type','webhook_intent'); f.change('scope','quarantines');
  f.reply(page([quarantine({type:'webhook_intent',repair:{at:'2026-11-25T03:00:00Z',operator:'offline',reason:'checked',disposition:'receipt_preserved',output_sha256:digest(4)}})]));
  await tick(); assert.equal(f.get('list').children.length,0); assert.match(f.get('status').textContent,/格式异常/);
});

test('webhook projection rejects impossible versions, missing result time and mixed domain fields', async () => {
  const good=webhookRecord();
  for(const bad of [{...good,webhook:{...good.webhook,version:2}},{...good,webhook:{...good.webhook,result_at:undefined}},{...good,result:receipt().result},{...good,webhook:{...good.webhook,state:'http_confirmed'}}]){
    const f=fixture(); f.change('type','webhook_intent'); f.fire('form','submit'); f.reply(page([bad])); await tick();
    assert.equal(f.get('list').children.length,0); assert.match(f.get('status').textContent,/格式异常/);
  }
});


test('admin region replacement retires shared record requests and session channel',async()=>{
 const f=fixture();const pending=f.calls[0];f.document.events['chunnel:admin-before-replace']({detail:{regions:[f.root]}});
 assert.equal(pending.options.signal.aborted,true);assert.equal(f.channels[0].closed,true);f.reply(page());await tick();
 assert.equal(f.get('list').children.length,0);assert.equal(f.calls.length,1);f.fire('form','submit');assert.equal(f.calls.length,1);
});
