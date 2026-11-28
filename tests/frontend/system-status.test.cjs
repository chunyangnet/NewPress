const test = require('node:test');
const assert = require('node:assert/strict');
const { createStatusPoller, validSnapshot } = require('./helpers/typescript.cjs').loadNativeModule('frontend/core/system.ts');

function fixture() {
  let id = 0, visible = true;
  const timers = new Map(), requests = [], renders = [], errors = [], states = [];
  const poller = createStatusPoller({
    setTimer(fn, delay) { const key = ++id; timers.set(key, { fn, delay }); return key; },
    clearTimer(key) { timers.delete(key); },
    visible: () => visible,
    load(signal) { return new Promise((resolve, reject) => requests.push({ signal, resolve, reject })); },
    onData: (data, manual) => renders.push({ data, manual }),
    onError: (error, timedOut) => errors.push({ error, timedOut }),
    onState: state => states.push(state),
  });
  return {
    poller, requests, renders, errors, states, timers,
    visibility(value) { visible = value; },
    run(delay) { const entry = [...timers].find(([, item]) => item.delay === delay); assert.ok(entry, `missing ${delay}ms timer`); timers.delete(entry[0]); entry[1].fn(); },
  };
}
const settle = () => new Promise(resolve => setImmediate(resolve));

test('status polling has one request in flight and schedules after completion', async () => {
  const f = fixture();
  f.poller.start(); f.run(2200);
  f.poller.refresh(true);
  assert.equal(f.requests.length, 1);
  assert.deepEqual([...f.timers.values()].map(item => item.delay), [10000]);
  f.requests[0].resolve({ revision: 1 }); await settle();
  assert.equal(f.renders.length, 1);
  assert.deepEqual([...f.timers.values()].map(item => item.delay), [15000]);
  f.poller.destroy();
});

test('pausing aborts requests and ignores late data even after re-enabling', async () => {
  const f = fixture();
  f.poller.start(); f.run(2200);
  f.poller.setEnabled(false);
  assert.equal(f.requests[0].signal.aborted, true);
  assert.equal(f.timers.size, 0);
  f.poller.setEnabled(true);
  f.requests[0].resolve('outdated'); await settle();
  assert.equal(f.renders.length, 0);
  f.requests[1].resolve('current'); await settle();
  assert.deepEqual(f.renders, [{ data: 'current', manual: false }]);
  f.poller.destroy();
});

test('hidden and BFCache pages stop polling and refresh when visible', async () => {
  const f = fixture();
  f.poller.start(); f.run(2200);
  f.visibility(false); f.poller.suspend();
  f.requests[0].resolve('hidden'); await settle();
  assert.equal(f.renders.length, 0);
  assert.equal(f.timers.size, 0);
  f.poller.refresh(); assert.equal(f.requests.length, 1);
  f.visibility(true); f.poller.resume();
  f.requests[1].resolve('resumed'); await settle();
  assert.equal(f.renders[0].data, 'resumed');
  f.poller.destroy();
});

test('transient failures preserve the last snapshot and back off retries', async () => {
  const f = fixture();
  const first = f.poller.refresh(true); f.requests[0].resolve('last good'); await first;
  f.run(15000); f.requests[1].reject(new Error('offline')); await settle();
  assert.deepEqual(f.renders, [{ data: 'last good', manual: true }]);
  assert.equal(f.states.at(-1).failures, 1);
  f.run(30000); f.requests[2].resolve('recovered'); await settle();
  assert.equal(f.states.at(-1).failures, 0);
  assert.deepEqual([...f.timers.values()].map(item => item.delay), [15000]);
  f.poller.destroy();
});

test('expired or revoked access stops automatic retries', async () => {
  for (const status of [401, 403]) {
    const f = fixture();
    const request = f.poller.refresh(); f.requests[0].reject(Object.assign(new Error('access'), { status })); await request;
    assert.equal(f.states.at(-1).enabled, false);
    assert.equal(f.states.at(-1).authStatus, status);
    assert.equal(f.timers.size, 0);
    f.poller.destroy();
  }
});

test('timed-out responses never replace the displayed snapshot', async () => {
  const f = fixture();
  const request = f.poller.refresh();
  f.run(10000);
  assert.equal(f.requests[0].signal.aborted, true);
  f.requests[0].resolve('too late'); await request;
  assert.equal(f.renders.length, 0);
  assert.equal(f.errors[0].timedOut, true);
  f.poller.destroy();
});

test('manual refresh remains usable while auto-refresh is paused', async () => {
  const f = fixture(); f.poller.setEnabled(false);
  const request = f.poller.refresh(true); f.requests[0].resolve('manual'); await request;
  assert.equal(f.renders[0].manual, true);
  assert.equal(f.states.at(-1).enabled, false);
  assert.equal(f.timers.size, 0);
  f.poller.destroy();
});

test('malformed snapshots and impossible percentages are rejected before DOM updates', () => {
  const report = {
    collected_at: '2026-11-11T10:00:00Z', collected_label: '18:00:00', status: 'pass', status_label: '服务可用', status_detail: '', uptime: '2 分', started_at: '2026-11-11 17:58:00', healthy: 1, total: 1, version: 'test', hostname: 'server', platform: 'linux / amd64', timezone: 'Asia/Shanghai',
    metrics: Array.from({ length: 4 }, (_, i) => ({ id: String(i), value: '0.0', unit: '%', detail: '', note: '', tone: 'normal', percent: 0 })),
    checks: [{ id: 'content', detail: '', status_label: '正常', latency: '< 1 ms', status: 'pass' }],
    groups: [{ rows: [{ id: 'hostname', value: 'server' }] }],
  };
  assert.equal(validSnapshot(report), true);
  for (const bad of [null, '<html>login</html>', { ...report, healthy: 2 }, { ...report, metrics: [] }, { ...report, collected_at: 'invalid' }, { ...report, checks: [null] }]) assert.equal(validSnapshot(bad), false);
  report.metrics[0].percent = 101;
  assert.equal(validSnapshot(report), false);
});
