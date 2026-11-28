const test = require('node:test');
const assert = require('node:assert/strict');
const { loadNativeModule } = require('./helpers/typescript.cjs');
const { makeSeries, sourceBreakdown, nearestIndex, percent, readNativeReport, readCount } = loadNativeModule('frontend/analytics/lite-data.ts');

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

function nativeReport() {
  return { data: { days:'7', timezone:'America/New_York', views:'70', articleViews:'30', searches:'14', misses:'7' },
    rows: Array.from({length:7}, (_,i)=>({date:`2026-11-0${i+1}`,views:'10',searches:'2',misses:'1'})),
    sources:[{sourceLabel:'<img src=x onerror=alert(1)>',sourceCount:'30'}] };
}
test('native report keeps server calendar labels through the daylight saving transition', () => {
  const { data, rows, sources } = nativeReport();
  const report = readNativeReport(data, rows, sources);
  assert.equal(report.timezone, data.timezone);
  assert.deepEqual(report.rows.map(row=>row.date),rows.map(row=>row.date));
  assert.equal(report.sources[0].label,sources[0].sourceLabel);
});
test('invalid native report data never becomes invented zero traffic or mismatched totals', () => {
  for (const value of [undefined,null,'', ' ', '-1', 'NaN', 'Infinity', '1.2', '1e2', '0x10', Number.MAX_SAFE_INTEGER+1]) assert.equal(readCount(value),null);
  for (const mutate of [
    f=>delete f.data.views, f=>f.data.views='71', f=>f.data.articleViews='71', f=>f.rows.pop(),
    f=>f.rows[0].misses='3', f=>f.rows[0].views='1', f=>f.rows[0].date='2026-02-30',
    f=>f.rows[1].date=f.rows[0].date, f=>f.rows[0].searches='NaN', f=>f.sources[0].sourceCount='71',
  ]) { const f=nativeReport(); mutate(f); assert.equal(readNativeReport(f.data,f.rows,f.sources),null); }
});
