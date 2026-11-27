const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../../internal/coreui/assets/js/chunnel-plugin-runtime.js'), 'utf8');
const tick = () => new Promise(resolve => setImmediate(resolve));
const summary = (status = 'cancelled', extra = {}) => ({ id: 'a'.repeat(64), plugin: 'task-fixture', kind: 'task.execute.v1', status, version: 2, attempts: 0, ...extra });

function fixture({review = false} = {}) {
  const nodes = new Map(), calls = [];
  const make = () => ({ value: '', disabled: false, hidden: false, dataset: {}, children: [], events: {}, attributes: new Map(), text: '',
    get textContent() { return this.text + this.children.map(n => n.textContent).join(''); },
    set textContent(v) { this.text = String(v); this.children = []; },
    addEventListener(type, fn) { this.events[type] = fn; },
    setAttribute(name, value) { this.attributes.set(name, value); }, removeAttribute(name) { this.attributes.delete(name); },
    append(node) { this.children.push(node); }, replaceChildren() { this.children = []; }
  });
  const put = name => { const node = make(); nodes.set(name, node); return node; };
  for (const name of ['data-task-status', 'data-task-filter', 'data-task-scope', 'data-task-archive-age', 'data-task-prev', 'data-task-next', 'data-task-list', 'data-task-refresh', 'data-task-create', 'data-task-archive', 'data-fanout-archive', 'data-task-payload']) put('[' + name + ']');
  if (review) for (const name of ['data-task-review-panel', 'data-task-reissue-fields', 'data-task-reissue-payload', 'data-task-reissue-reason', 'data-task-reissue-confirm', 'data-task-reissue-submit', 'data-task-review-close', 'data-task-review-source', 'data-task-review-audit']) put('[' + name + ']');
  const get = selector => nodes.get(selector) || null;
  get('[data-task-scope]').value = 'live'; get('[data-task-archive-age]').value = '30'; get('[data-task-payload]').value = '{"keep":"unsaved draft"}';
  if (review) { get('[data-task-review-panel]').hidden = true; get('[data-task-reissue-fields]').hidden = true; get('[data-task-reissue-payload]').value = '{}'; get('[data-task-reissue-confirm]').checked = false; }
  const tasks = make(); tasks.querySelector = get;
  tasks.querySelectorAll = selector => selector.split(',').map(s => get(s.trim())).filter(Boolean);
  let dirty = false, nextKey = 0;
  const controls = {dirty: () => dirty, start: () => true, success() {}, fail() {}};
  const form = make(); form.reportValidity = () => true; form.querySelectorAll = () => []; form.querySelector = () => null;
  const root = {dataset: {slug: 'task-fixture', userId: '7', csrf: 'csrf', version: '1'},
    querySelector: selector => selector === '[data-runtime-form]' ? form : selector === '[data-runtime-status]' ? make() : selector === '[data-plugin-tasks]' ? tasks : null};
  const document = { querySelector: () => root, createElement: () => make() };
  vm.runInNewContext(source, { document, window: {ChunnelAdminUI: {savebar: () => controls}}, URLSearchParams, TextEncoder, crypto: {randomUUID: () => 'request-' + ++nextKey},
    fetch: (url, options) => new Promise((resolve, reject) => calls.push({url, options, resolve, reject})) });
  const fire = (name, type = 'click') => { const target = get(name); return target.events[type]({currentTarget: target, target, preventDefault() {}}); };
  const respond = (result, status = 200) => calls.at(-1).resolve({ok: status < 400, status, json: async () => result});
  const openReview = (id = 'a'.repeat(64)) => { const button = make(); button.dataset.taskId = id; button.closest = selector => selector === '[data-task-review-open]' ? button : null; tasks.events.click({target: button}); };
  return {get, calls, tasks, root, fire, respond, openReview, dirty: value => { dirty = value; }};
}

test('archive scopes task maintenance to the current plugin and preserves unsaved parameters', async () => {
  const f = fixture(); f.dirty(true);
  const draft = f.get('[data-task-payload]').value;
  const action = f.fire('[data-task-archive]');
  assert.equal(f.calls[0].url, '/api/v1/admin/plugins/task-fixture/tasks/archive');
  const body = JSON.parse(f.calls[0].options.body);
  assert.equal(body.expected_user_id, 7); assert.equal(body.limit, 100);
  assert.ok(Math.abs(Date.now() - Date.parse(body.before) - 30 * 86400000) < 1000);
  assert.equal(f.calls[0].options.headers['X-CSRF-Token'], 'csrf');
  assert.equal(f.get('[data-task-scope]').disabled, true);
  f.respond({tasks: 3, fanouts: 0}); await tick();
  assert.equal(f.calls.length, 2); assert.equal(new URL(f.calls[1].url, 'https://example.test').searchParams.get('archived'), 'false');
  f.respond({items: [], total: 0}); await action;
  assert.match(f.get('[data-task-status]').textContent, /已归档本插件终态任务 3 条/);
  assert.equal(f.get('[data-task-payload]').value, draft); assert.equal(f.get('[data-task-scope]').disabled, false);
});

test('site receipt archive uses its shared endpoint and reports the returned recipient receipt count', async () => {
  const f = fixture(); const action = f.fire('[data-fanout-archive]');
  assert.equal(f.calls[0].url, '/api/v1/admin/plugins/fanouts/archive');
  f.respond({tasks: 0, fanouts: 5}); await tick(); f.respond({items: [], total: 0}); await action;
  assert.match(f.get('[data-task-status]').textContent, /已归档站点事件回执 5 条/);
  assert.equal(f.calls.filter(call => call.options.method === 'POST').length, 1);
});

test('history selection resets pagination and removes execution actions', async () => {
  const f = fixture(); let action = f.fire('[data-task-next]');
  assert.equal(new URL(f.calls[0].url, 'https://example.test').searchParams.get('offset'), '20');
  f.respond({items: [summary('pending')], total: 40}); await action;
  assert.equal(f.get('[data-task-list]').children[0].children.length, 1);
  f.get('[data-task-scope]').value = 'archived'; action = f.fire('[data-task-scope]', 'change');
  const query = new URL(f.calls.at(-1).url, 'https://example.test').searchParams;
  assert.equal(query.get('offset'), '0'); assert.equal(query.get('archived'), 'true');
  f.respond({items: [summary('cancelled', {archived_at: '2026-11-24T00:00:00Z'})], total: 1}); await action;
  const row = f.get('[data-task-list]').children[0];
  assert.equal(row.children.length, 0); assert.match(row.textContent, /已归档/);
  assert.match(f.get('[data-task-status]').textContent, /归档历史共 1 条/);
});

test('failed and malformed archival acknowledgements preserve inputs and restore controls', async () => {
  for (const [result, status] of [[{error: {message: '历史配额已满'}}, 429], [{tasks: 1}, 200]]) {
    const f = fixture(); const draft = f.get('[data-task-payload]').value;
    const action = f.fire('[data-task-archive]'); f.respond(result, status); await action;
    assert.equal(f.calls.length, 1); assert.equal(f.get('[data-task-payload]').value, draft);
    assert.equal(f.get('[data-task-archive]').disabled, false); assert.equal(f.tasks.attributes.has('aria-busy'), false);
    assert.doesNotMatch(f.get('[data-task-status]').textContent, /已归档本插件/);
  }
});

test('confirmed archive result survives a subsequent list read failure', async () => {
  const f = fixture(); const action = f.fire('[data-task-archive]');
  f.respond({tasks: 4, fanouts: 0}); await tick(); f.respond({error: {message: '读取失败'}}, 503); await action;
  assert.match(f.get('[data-task-status]').textContent, /已归档本插件终态任务 4 条/);
  assert.match(f.get('[data-task-status]').textContent, /列表暂未刷新/);
});

test('unconfirmed enqueue acknowledgement retains the same request key for a deliberate retry', async () => {
  const f = fixture(); let action = f.fire('[data-task-create]');
  const first = JSON.parse(f.calls[0].options.body);
  f.respond({ok: true}, 202); await action;
  assert.equal(f.calls.length, 1); assert.match(f.get('[data-task-status]').textContent, /结果不完整/);
  action = f.fire('[data-task-create]');
  assert.equal(JSON.parse(f.calls[1].options.body).key, first.key);
  f.respond(summary('succeeded', {archived_at: '2026-11-24T00:00:00Z'}), 202); await tick();
  f.respond({items: [], total: 0}); await action;
  assert.match(f.get('[data-task-status]').textContent, /原请求已归档，未重复入队/);
  assert.equal(f.get('[data-task-payload]').value, '{"keep":"unsaved draft"}');
});
const trace = (extra = {}) => ({source_id: 'a'.repeat(64), source_version: 2, source_status: 'cancelled', reviewed_at: '2026-11-24T00:00:00Z', ...extra});
const childSummary = (extra = {}) => summary('pending', {id: 'b'.repeat(64), version: 1, reissue: trace(), ...extra});
const reviewDetails = (extra = {}) => ({task: summary(), reissuable: true, current_user_id: 7, ...extra});
async function reviewedFixture() {
  const f = fixture({review: true});
  f.openReview(); f.respond(reviewDetails()); await tick();
  f.get('[data-task-reissue-reason]').value = 'Checked the prior business result';
  f.get('[data-task-reissue-confirm]').checked = true;
  return f;
}

test('archived cancelled tasks expose deliberate reissue separately from execution actions', async () => {
  const f = fixture({review: true});
  f.get('[data-task-scope]').value = 'archived'; const reading = f.fire('[data-task-scope]', 'change');
  f.respond({items: [summary('cancelled', {archived_at: '2026-11-24T00:00:00Z'})], total: 1}); await reading;
  const row = f.get('[data-task-list]').children[0];
  assert.equal(row.children.length, 1);
  assert.equal(row.children[0].dataset.taskReviewOpen, '');
  assert.equal(row.children[0].dataset.taskAction, undefined);
  assert.match(row.children[0].textContent, /核对后重新投递/);
  f.openReview(); assert.match(f.calls.at(-1).url, /tasks\/[a-f0-9]{64}\/review$/);
  f.respond(reviewDetails({task: summary('cancelled', {archived_at: '2026-11-24T00:00:00Z'})})); await tick();
  assert.equal(f.get('[data-task-review-panel]').hidden, false);
  assert.equal(f.get('[data-task-reissue-fields]').hidden, false);
  assert.match(f.get('[data-task-review-source]').textContent, /原参数已移除/);
  assert.equal(f.calls.filter(call => call.options.method === 'POST').length, 0);
});

test('reissue requires reviewed parameters, a short reason and an explicit confirmation', async () => {
  const f = fixture({review: true}); f.openReview(); f.respond(reviewDetails()); await tick();
  await f.fire('[data-task-reissue-submit]');
  assert.equal(f.calls.length, 1); assert.match(f.get('[data-task-status]').textContent, /明确确认/);
  f.get('[data-task-reissue-confirm]').checked = true;
  await f.fire('[data-task-reissue-submit]');
  assert.equal(f.calls.length, 1); assert.match(f.get('[data-task-status]').textContent, /原因/);
  f.get('[data-task-reissue-reason]').value = 'checked';
  f.get('[data-task-reissue-payload]').value = '{bad json';
  await f.fire('[data-task-reissue-submit]');
  assert.equal(f.calls.length, 1); assert.equal(f.get('[data-task-reissue-payload]').value, '{bad json');
  assert.match(f.get('[data-task-status]').textContent, /不是有效 JSON/);
});

test('uncertain reissue keeps its exact original body through later edits and a failed list refresh', async () => {
  const f = await reviewedFixture();
  f.get('[data-task-reissue-payload]').value = '{"reviewed":1}';
  let action = f.fire('[data-task-reissue-submit]');
  const original = JSON.parse(f.calls.at(-1).options.body);
  assert.equal(original.expected_user_id, 7); assert.equal(original.source_version, 2); assert.equal(original.confirmed, true);
  f.get('[data-task-reissue-payload]').value = '{"newerDraft":2}'; f.get('[data-task-reissue-reason]').value = 'Newer unsaved decision';
  f.respond({ok: true}, 202); await action;
  assert.equal(f.get('[data-task-review-close]').disabled, true);
  assert.match(f.get('[data-task-reissue-submit]').textContent, /重试原/);
  f.dirty(true); f.get('[data-task-reissue-confirm]').checked = false;
  action = f.fire('[data-task-reissue-submit]');
  assert.deepEqual(JSON.parse(f.calls.at(-1).options.body), original);
  f.respond(childSummary(), 202); await tick();
  f.respond({error: {message: 'read failed'}}, 503); await action;
  assert.match(f.get('[data-task-status]').textContent, /重新投递已确认/);
  assert.match(f.get('[data-task-status]').textContent, /任务列表暂未刷新/);
  assert.equal(f.get('[data-task-reissue-payload]').value, '{"newerDraft":2}');
  assert.equal(f.get('[data-task-reissue-reason]').value, 'Newer unsaved decision');
  assert.equal(f.get('[data-task-review-close]').disabled, false);
  assert.equal(f.get('[data-task-reissue-confirm]').checked, false);
});

test('a mismatched reissue acknowledgement does not release the original request key', async () => {
  const f = await reviewedFixture();
  let action = f.fire('[data-task-reissue-submit]');
  const original = JSON.parse(f.calls.at(-1).options.body);
  f.respond(childSummary({reissue: trace({source_id: 'c'.repeat(64)})}), 202); await action;
  assert.match(f.get('[data-task-status]').textContent, /结果不完整/);
  assert.equal(f.get('[data-task-review-close]').disabled, true);
  action = f.fire('[data-task-reissue-submit]');
  assert.deepEqual(JSON.parse(f.calls.at(-1).options.body), original);
  f.respond(childSummary({status: 'cancelled', archived_at: '2026-11-24T00:00:00Z'}), 202); await tick();
  f.respond({items: [], total: 0}); await action;
  assert.match(f.get('[data-task-status]').textContent, /已归档/);
  assert.equal(f.get('[data-task-review-close]').disabled, false);
});

test('fresh source reads retain draft parameters and require confirmation again', async () => {
  const f = await reviewedFixture();
  f.get('[data-task-reissue-payload]').value = '{"keep":1}';
  f.get('[data-task-reissue-reason]').value = 'Keep this unsaved review';
  f.openReview();
  f.get('[data-task-reissue-payload]').value = '{"newer":2}';
  f.respond(reviewDetails({task: summary('cancelled', {version: 3})})); await tick();
  assert.equal(f.get('[data-task-reissue-payload]').value, '{"newer":2}');
  assert.equal(f.get('[data-task-reissue-reason]').value, 'Keep this unsaved review');
  assert.equal(f.get('[data-task-reissue-confirm]').checked, false);
  assert.match(f.get('[data-task-review-source]').textContent, /版本 3/);
});

test('changed account or incomplete provenance refuses a new source view without overwriting drafts', async () => {
  for (const details of [reviewDetails({current_user_id: 8}), reviewDetails({task: childSummary(), provenance: undefined})]) {
    const f = fixture({review: true}); f.get('[data-task-reissue-payload]').value = '{"keep":1}';
    f.openReview(); f.respond(details); await tick();
    assert.equal(f.get('[data-task-review-panel]').hidden, true);
    assert.equal(f.get('[data-task-reissue-payload]').value, '{"keep":1}');
    assert.match(f.get('[data-task-status]').textContent, /不完整或当前账户已变化/);
    assert.equal(f.calls.length, 1);
  }
});

test('network failure retains a reissue for deliberate retry and explains it in Chinese', async () => {
  const f = await reviewedFixture();
  const action = f.fire('[data-task-reissue-submit]');
  f.calls.at(-1).reject(new TypeError('Failed to fetch')); await action;
  assert.match(f.get('[data-task-status]').textContent, /网络连接中断/);
  assert.match(f.get('[data-task-status]').textContent, /输入已保留/);
  assert.equal(f.get('[data-task-review-close]').disabled, true);
  assert.match(f.get('[data-task-reissue-submit]').textContent, /重试原/);
});

test('a pending reissue keeps the original account identity when page metadata changes', async () => {
  const f = await reviewedFixture();
  let action = f.fire('[data-task-reissue-submit]');
  const original = f.calls.at(-1).options.body;
  f.calls.at(-1).reject(new TypeError('Failed to fetch')); await action;
  f.root.dataset.userId = '8';
  action = f.fire('[data-task-reissue-submit]');
  assert.equal(f.calls.at(-1).options.body, original);
  f.respond({error: {code: 'plugins.identity_changed', message: 'account changed'}}, 409); await action;
  assert.equal(f.get('[data-task-review-close]').disabled, true);
});

test('a first confirmed rejection allows a fresh source review while keeping parameters', async () => {
  const f = await reviewedFixture();
  f.get('[data-task-reissue-payload]').value = '{"keep":1}';
  let action = f.fire('[data-task-reissue-submit]');
  const firstKey = JSON.parse(f.calls.at(-1).options.body).key;
  f.respond({error: {code: 'plugins.tasks_conflict', message: 'source changed'}}, 409); await action;
  assert.equal(f.get('[data-task-review-close]').disabled, false);
  assert.equal(f.get('[data-task-reissue-confirm]').checked, false);
  assert.equal(f.get('[data-task-reissue-payload]').value, '{"keep":1}');
  assert.match(f.get('[data-task-status]').textContent, /本次请求已被拒绝/);
  f.openReview(); f.respond(reviewDetails({task: summary('cancelled', {version: 3})})); await tick();
  f.get('[data-task-reissue-confirm]').checked = true;
  action = f.fire('[data-task-reissue-submit]');
  const next = JSON.parse(f.calls.at(-1).options.body);
  assert.equal(next.source_version, 3); assert.notEqual(next.key, firstKey);
  f.respond(childSummary({reissue: trace({source_version: 3})}), 202); await tick();
  f.respond({items: [], total: 0}); await action;
});
