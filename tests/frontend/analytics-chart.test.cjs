const test = require('node:test');
const assert = require('node:assert/strict');
const { makeSeries, sourceBreakdown, nearestIndex, percent } = require('../../internal/coreui/assets/js/chunnel-analytics.js');

test('an empty period has finite geometry and no invented traffic or percentage', () => {
  for (const rows of [[], Array.from({ length: 30 }, () => ({ views: 0 }))]) {
    const series = makeSeries(rows, 'views');
    assert.equal(series.total, 0);
    assert.equal(series.average, 0);
    assert.equal(series.peak, 0);
    assert.ok(series.ticks.every(Number.isInteger));
    assert.ok(series.points.every(point => Number.isFinite(point.x) && point.y === 180));
  }
  assert.equal(percent(0, 0), '—');
  assert.deepEqual(sourceBreakdown([], 0), []);
});

test('each supported period preserves its daily counts, zeros and total', () => {
  for (const days of [7, 30, 90]) {
    const rows = Array.from({ length: days }, (_, index) => ({ views: index % 4 === 0 ? 0 : index * 3, searches: index % 3 }));
    for (const key of ['views', 'searches']) {
      const series = makeSeries(rows, key);
      const expected = rows.reduce((sum, row) => sum + row[key], 0);
      assert.equal(series.points.length, days);
      assert.equal(series.total, expected);
      assert.equal(series.average, expected / days);
      assert.deepEqual(series.points.map(point => point.value), rows.map(row => row[key]));
      assert.ok(series.ceiling >= series.peak);
      assert.ok(series.points.every(point => point.x >= 0 && point.x <= 900 && point.y >= 20 && point.y <= 180));
    }
  }
});

test('single observations and large spikes remain inside the chart', () => {
  for (const values of [[1], [0, 1, 0], [0, 1000000000000, 0]]) {
    const series = makeSeries(values.map(views => ({ views })), 'views');
    assert.equal(series.peak, Math.max(...values));
    assert.ok(series.points.every(point => Number.isFinite(point.x) && Number.isFinite(point.y) && point.y >= 20 && point.y <= 180));
    assert.ok(series.ticks.every(Number.isInteger));
  }
});

test('a truncated source ranking leaves its omitted traffic visible in the ring', () => {
  const parts = sourceBreakdown([{ label: '站内跳转', count: 60 }, { label: '来源二', count: 20 }], 100);
  assert.deepEqual(parts.map(part => part.count), [60, 20, 20]);
  assert.deepEqual(parts.map(part => part.share), [60, 20, 20]);
  assert.equal(parts.at(-1).index, -1);
  assert.equal(parts.reduce((sum, part) => sum + part.share, 0), 100);
  const complete = sourceBreakdown([{ label: '单一来源', count: 5 }], 5);
  assert.equal(complete.length, 1);
  assert.equal(complete[0].share, 100);
});

test('out-of-bounds pointers select real dates at either end of the report', () => {
  assert.equal(nearestIndex(-20, 900, 30), 0);
  assert.equal(nearestIndex(1000, 900, 30), 29);
  assert.equal(nearestIndex(450, 900, 7), 3);
  assert.equal(nearestIndex(0, 0, 30), 0);
  assert.equal(nearestIndex(50, 900, 1), 0);
});

test('small nonzero shares stay distinguishable from zero', () => {
  assert.equal(percent(0, 100), '0%');
  assert.equal(percent(5, 10), '50%');
  assert.equal(percent(1, 10000), '<0.1%');
  assert.equal(percent(3, 3), '100%');
});
