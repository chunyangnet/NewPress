const test = require('node:test');
const assert = require('node:assert/strict');
const { loadModules } = require('./helpers/typescript.cjs');
const [api, { createReportController }] = loadModules(['frontend/analytics/api.ts', 'frontend/analytics/controller.ts'], { AbortController, DOMException });
const tick = () => new Promise(resolve => setImmediate(resolve));

function sample(days = 7) {
  const end = Date.parse('2026-11-25T00:00:00Z');
  const trend = Array.from({ length: days }, (_, i) => ({ date: new Date(end - (days - i - 1) * 86400000).toISOString().slice(0, 10), views: i, searches: 0, misses: 0 }));
  return { timezone: 'Asia/Shanghai', enabled: true, days, start: trend[0].date, end: trend.at(-1).date,
    views: trend.reduce((sum, row) => sum + row.views, 0), article_views: 0, searches: 0, misses: 0, dropped_since_start: 0,
    updated_at: '2026-11-25T01:00:00+08:00', trend, sources: [], terms: [], articles: [] };
}
function requests() {
  const calls = [];
  const controller = createReportController(api.parseReport(sample(30)), (days, signal) => new Promise((resolve, reject) => calls.push({ days, signal, resolve, reject })));
  return { controller, calls };
}
test('valid reports keep calendar labels and counts for each supported range', () => {
  for (const days of [7, 30, 90]) {
    const input = sample(days), report = api.parseReport(input, days);
    assert.equal(report.timezone, 'Asia/Shanghai');
    assert.equal(report.start, input.start);
    assert.equal(report.views, input.views);
    assert.equal(report.trend.length, days);
    assert.equal(report.trend.at(-1).date, '2026-11-25');
  }
});
test('missing fields, malformed counts, mismatched periods and unsafe article links fail visibly', () => {
  const mutations = [r => delete r.views, r => r.views = '21', r => r.views = NaN, r => r.views = -1,
    r => r.trend[1].date = r.trend[0].date, r => r.end = '2026-11-26', r => r.timezone = 'Local',
    r => r.sources = [{ label: 'source', count: r.views + 1 }], r => r.updated_at = '2026-11-25T01:00:00',
    r => r.articles = [{ id: 1, title: 'unsafe', url: 'javascript:alert(1)', views: 0, likes: 0, bookmarks: 0, comments: 0 }]];
  for (const change of mutations) { const value = sample(); change(value); assert.throws(() => api.parseReport(value), error => error.failure.kind === 'data'); }
  assert.throws(() => api.parseReport(sample(7), 30));
});
test('empty report and disabled collection with retained history are different successful states', () => {
  const empty = sample();
  empty.trend.forEach(row => row.views = 0); empty.views = 0; empty.updated_at = '0001-01-01T00:00:00Z';
  assert.equal(api.parseReport(empty).views, 0);
  const disabled = sample(); disabled.enabled = false;
  assert.equal(api.parseReport(disabled).views, 21);
});
test('bootstrapping performs no API read; changing periods labels the retained report accurately', async () => {
  const { controller, calls } = requests();
  assert.equal(calls.length, 0);
  const pending = controller.select(7);
  assert.equal(controller.getSnapshot().phase, 'refreshing');
  assert.equal(controller.getSnapshot().requestedDays, 7);
  assert.equal(controller.getSnapshot().report.days, 30);
  calls[0].resolve(api.parseReport(sample(7))); await pending;
  assert.equal(controller.getSnapshot().report.days, 7);
});
test('late responses and failures cannot replace a newer report even if a transport ignores abort', async () => {
  const { controller, calls } = requests();
  const first = controller.select(7), last = controller.select(90);
  assert.equal(calls[0].signal.aborted, true);
  calls[1].resolve(api.parseReport(sample(90))); await last;
  calls[0].resolve(api.parseReport(sample(7))); await first;
  assert.equal(controller.getSnapshot().report.days, 90);
  const stale = controller.select(7), fresh = controller.select(30);
  calls[3].resolve(api.parseReport(sample(30))); await fresh;
  calls[2].reject(new api.AnalyticsError({ kind: 'permission', message: 'old error' })); await stale;
  assert.equal(controller.getSnapshot().phase, 'ready');
});
test('permission/session loss clears the old report; ordinary failure keeps its real period', async () => {
  for (const kind of ['session', 'permission', 'unavailable', 'data', 'network']) {
    const { controller, calls } = requests();
    const pending = controller.select(7);
    calls[0].reject(new api.AnalyticsError({ kind, message: 'failure' })); await pending;
    const state = controller.getSnapshot();
    assert.equal(state.phase, ['session', 'permission'].includes(kind) ? 'denied' : 'error');
    assert.equal(state.report?.days ?? null, state.phase === 'denied' ? null : 30);
    assert.equal(state.requestedDays, 7);
  }
});
test('a loader returning the wrong period cannot be accepted as success', async () => {
  const { controller, calls } = requests();
  const pending = controller.select(7);
  calls[0].resolve(api.parseReport(sample(90))); await pending;
  assert.equal(controller.getSnapshot().phase, 'error');
  assert.equal(controller.getSnapshot().report.days, 30);
});
test('unmount aborts in-flight work and releases all subscribers', async () => {
  const { controller, calls } = requests();
  let updates = 0;
  controller.subscribe(() => updates++);
  const pending = controller.select(7);
  controller.dispose();
  assert.equal(calls[0].signal.aborted, true);
  calls[0].resolve(api.parseReport(sample(7))); await pending; await tick();
  assert.equal(updates, 1);
  await controller.select(90); assert.equal(calls.length, 1);
});
