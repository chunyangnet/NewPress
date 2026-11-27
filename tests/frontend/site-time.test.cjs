const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');

const source = compile('frontend/shared/site-time-global.ts');
const zones = ['UTC', 'Asia/Shanghai', 'America/New_York', 'Asia/Kathmandu'];
const dateFormats = ['formatDate', 'formatMonth', 'formatDateDot'];
const timeFormats = ['formatTime', 'formatClock', 'formatDateTime', 'formatDateTimeSeconds'];
const formats = [...dateFormats, ...timeFormats, 'relative'];

function fixture(zone = 'UTC', now = '2027-01-01T01:30:00Z') {
  const window = {};
  let attribute = zone;
  const reads = [];
  const document = { documentElement: { getAttribute(name) { reads.push(name); return attribute; } } };
  // Keep the VM's own Date brand while freezing only its no-argument clock.
  // Dates passed in from this test are deliberately from another realm.
  const context = vm.createContext({ window, document, nowMilliseconds: Date.parse(now) });
  vm.runInContext(`
    const NativeDate = Date;
    Date = class extends NativeDate {
      constructor(...args) { super(...(args.length ? args : [nowMilliseconds])); }
      static now() { return nowMilliseconds; }
    };
  `, context);
  vm.runInContext(source, context, { filename: 'site-time-global.ts' });
  return { time: window.ChunnelTime, reads, context, setZone(value) { attribute = value; } };
}

function assertFormats(time, value, date, clock) {
  assert.equal(time.valid(value), true);
  assert.equal(time.formatDate(value), date);
  assert.equal(time.formatMonth(value), date.slice(0, 7));
  assert.equal(time.formatDateDot(value), date.replaceAll('-', '.'));
  assert.equal(time.formatTime(value), clock.slice(0, 5));
  assert.equal(time.formatClock(value), clock);
  assert.equal(time.formatDateTime(value), `${date} ${clock.slice(0, 5)}`);
  assert.equal(time.formatDateTimeSeconds(value), `${date} ${clock}`);
}

function assertEmpty(time, value) {
  assert.equal(time.valid(value), false, `valid(${String(value)})`);
  for (const format of formats) assert.equal(time[format](value), '', `${format}(${String(value)})`);
  assert.equal(time.sameDay(value, value), false);
  assert.equal(time.sameDay(value, '2027-01-01'), false);
  assert.equal(time.sameDay('2027-01-01', value), false);
  assert.equal(time.isToday(value), false);
}

test('ChunnelTime preserves its frozen public API', () => {
  const { time } = fixture();
  assert.equal(Object.isFrozen(time), true);
  assert.deepEqual(Object.keys(time).sort(), ['zone', 'valid', ...dateFormats, ...timeFormats, 'sameDay', 'isToday', 'relative'].sort());
});

test('all formats use the site timezone across calendar years and fractional offsets', () => {
  const instant = '2026-12-31T18:30:45Z';
  for (const [zone, date, clock] of [
    ['UTC', '2026-12-31', '18:30:45'],
    ['Asia/Shanghai', '2027-01-01', '02:30:45'],
    ['America/New_York', '2026-12-31', '13:30:45'],
    ['Asia/Kathmandu', '2027-01-01', '00:15:45'],
  ]) {
    const { time } = fixture(zone);
    assert.equal(time.zone(), zone);
    assertFormats(time, instant, date, clock);
    assertFormats(time, new Date(instant), date, clock);
    assertFormats(time, Date.parse(instant), date, clock);
  }
  assertFormats(fixture('America/New_York').time, '2027-01-01T01:30:45Z', '2026-12-31', '20:30:45');
});

test('explicit RFC3339 offsets, lowercase separators and fractions describe the same instant', () => {
  const { time } = fixture('Asia/Kathmandu');
  for (const value of [
    '2026-12-31T18:30:45Z',
    '2026-12-31t18:30:45z',
    '2026-12-31T18:30:45.123456789Z',
    '2026-12-31T18:30:45+00:00',
    '2026-12-31T18:30:45-00:00',
    '2027-01-01T02:30:45+08:00',
    '2026-12-31T13:30:45-05:00',
    '2027-01-01T00:15:45+05:45',
  ]) assertFormats(time, value, '2027-01-01', '00:15:45');
});

test('epoch zero is an instant, not an empty value, including historical Kathmandu offset', () => {
  for (const [zone, date, clock] of [
    ['UTC', '1970-01-01', '00:00:00'],
    ['Asia/Shanghai', '1970-01-01', '08:00:00'],
    ['America/New_York', '1969-12-31', '19:00:00'],
    ['Asia/Kathmandu', '1970-01-01', '05:30:00'],
  ]) {
    const { time } = fixture(zone);
    for (const value of [0, new Date(0), '1970-01-01T00:00:00Z']) assertFormats(time, value, date, clock);
    assert.equal(time.sameDay(0, new Date(0)), true);
  }
  assertFormats(fixture().time, -1, '1969-12-31', '23:59:59');
  assertFormats(fixture().time, 1.5, '1970-01-01', '00:00:00');
  assert.equal(fixture('UTC', '1970-01-01T00:00:01Z').time.relative(0), '00:00');
});

test('New York spring and fall DST transitions use site wall clocks without changing instants', () => {
  const { time } = fixture('America/New_York');
  for (const [instant, date, clock] of [
    ['2026-03-08T06:59:59Z', '2026-03-08', '01:59:59'],
    ['2026-03-08T07:00:00Z', '2026-03-08', '03:00:00'],
    ['2026-11-01T05:59:59Z', '2026-11-01', '01:59:59'],
    ['2026-11-01T06:00:00Z', '2026-11-01', '01:00:00'],
    ['2026-11-01T01:30:00-04:00', '2026-11-01', '01:30:00'],
    ['2026-11-01T01:30:00-05:00', '2026-11-01', '01:30:00'],
  ]) assertFormats(time, instant, date, clock);
  assert.equal(time.sameDay('2026-03-08T06:59:59Z', '2026-03-08T07:00:00Z'), true);
  assert.equal(time.sameDay('2026-11-01T01:30:00-04:00', '2026-11-01T01:30:00-05:00'), true);
  const today = fixture('America/New_York', '2026-03-08T07:00:00Z').time;
  assert.equal(today.isToday('2026-03-08T06:59:59Z'), true);
  assert.equal(today.relative('2026-03-08T06:59:59Z'), '01:59');
});

test('pure dates are calendar labels in every zone and never invent a clock', () => {
  for (const zone of zones) {
    const { time } = fixture(zone);
    for (const label of ['2026-12-31', '2027-01-01', '2026-03-08', '2026-11-01', '2024-02-29', '2000-02-29', '0099-01-02']) {
      assert.equal(time.valid(label), true);
      assert.equal(time.formatDate(label), label);
      assert.equal(time.formatMonth(label), label.slice(0, 7));
      assert.equal(time.formatDateDot(label), label.replaceAll('-', '.'));
      assert.equal(time.sameDay(label, label), true);
      for (const format of timeFormats) assert.equal(time[format](label), '', `${zone} ${format}(${label})`);
    }
    assert.equal(time.sameDay('2026-12-31', '2027-01-01'), false);
  }
  const { time } = fixture('UTC', '0099-01-02T12:00:00Z');
  assertFormats(time, '0099-01-02T12:00:00Z', '0099-01-02', '12:00:00');
  assert.equal(time.sameDay('0099-01-02', '0099-01-02T12:00:00Z'), true);
});

test('today, same-day and relative labels compare the site calendar rather than UTC or browser dates', () => {
  for (const [zone, today, todayInstant, clock, older, previousYear] of [
    ['UTC', '2027-01-01', '2027-01-01T00:15:00Z', '00:15', '2027-01-02', '2026-12-31'],
    ['Asia/Shanghai', '2027-01-01', '2026-12-31T18:30:00Z', '02:30', '2027-01-02', '2026-12-31'],
    ['America/New_York', '2026-12-31', '2027-01-01T00:15:00Z', '19:15', '2026-12-30', '2025-12-31'],
    ['Asia/Kathmandu', '2027-01-01', '2026-12-31T18:30:00Z', '00:15', '2027-01-02', '2026-12-31'],
  ]) {
    const { time } = fixture(zone);
    assert.equal(time.isToday(todayInstant), true, zone);
    assert.equal(time.isToday(today), true, zone);
    assert.equal(time.sameDay(today, todayInstant), true, zone);
    assert.equal(time.sameDay(todayInstant, today), true, zone);
    assert.equal(time.relative(todayInstant), clock, zone);
    const compact = label => `${Number(label.slice(5, 7))}/${Number(label.slice(8, 10))}`;
    assert.equal(time.relative(today), compact(today), 'date-only today must not acquire midnight');
    assert.equal(time.isToday(older), false);
    assert.equal(time.relative(older), compact(older));
    assert.equal(time.relative(previousYear), `${previousYear.slice(0, 4)}/${compact(previousYear)}`);
  }
  const { time } = fixture('America/New_York');
  assert.equal(time.isToday('2027-01-01T05:00:00Z'), false);
  assert.equal(time.relative('2027-01-01T05:00:00Z'), '2027/1/1');
  assert.equal(time.sameDay('2027-01-01T04:59:59Z', '2027-01-01T05:00:00Z'), false);
});

test('invalid dates and timezone-less datetime strings stay empty instead of normalizing or using host time', () => {
  const invalid = [
    undefined, null, '', ' ', false, true, {}, [], new Number(0), { getTime: () => 0 },
    new Date(NaN), NaN, Infinity, -Infinity, Number.MAX_VALUE, 8640000000000001,
    '0', 'today', '2026', '2026-12', '2026-2-01', '2026-02-1', '2026/02/01',
    '2026-12-31\n', '2026-12-31\r\n', '2026-12-31T18:30:45Z\n',
    '2026-00-10', '2026-13-01', '2026-01-00', '2026-01-32', '2026-02-29', '1900-02-29', '2026-04-31',
    '2026-02-29T12:00:00Z', '1900-02-29T12:00:00Z', '2026-04-31T12:00:00+08:00',
    '2026-00-01T12:00:00Z', '2026-13-01T12:00:00Z', '2026-01-00T12:00:00Z',
    '2026-03-08T02:30', '2026-03-08T02:30:00', '2026-03-08T02:30:00.123',
    '2026-11-01T01:30:00', '2026-11-01 01:30:00', '2026-12-31 18:30:45Z',
    '2026-12-31T18:30Z', '2026-12-31T18:30:45+08', '2026-12-31T18:30:45+0800',
    '2026-12-31T24:00:00Z', '2026-12-31T12:60:00Z', '2026-12-31T12:00:60Z',
    '2026-12-31T12:00:00+24:00', '2026-12-31T12:00:00+08:60', '2026-12-31T12:00:00-24:00',
    '2026-12-31T18:30:45.Z', '2026-12-31T18:30:45,123Z',
    ' 2026-12-31T18:30:45Z', '2026-12-31T18:30:45Z ', '2026-12-31T18:30:45Zjunk',
    'Thu, 31 Dec 2026 18:30:45 GMT', '+002026-12-31T18:30:45Z',
  ];
  for (const zone of zones) {
    const { time } = fixture(zone);
    for (const value of invalid) assertEmpty(time, value);
    assert.equal(time.valid('2024-02-29T12:00:00+00:00'), true);
    assert.equal(time.valid('2000-02-29T12:00:00Z'), true);
  }
});

test('Go zero time remains empty as a label, timestamp, Date or epoch milliseconds', () => {
  const zero = '0001-01-01T00:00:00Z';
  for (const zone of zones) {
    const { time } = fixture(zone);
    for (const value of ['0001-01-01', zero, '0001-01-01T00:00:00.000000000Z', '0001-01-01T00:00:00+00:00', '0001-01-01T08:00:00+08:00', new Date(zero), Date.parse(zero), Date.parse(zero) - 0.5]) {
      assertEmpty(time, value);
    }
  }
});

test('missing, invalid and Local site zones fall back explicitly to UTC', () => {
  for (const zone of ['', null, ' \t', 'Not/A_Zone', 'Local', ' Local ']) {
    const { time } = fixture(zone);
    assert.equal(time.zone(), 'UTC');
    assertFormats(time, '2026-12-31T18:30:45Z', '2026-12-31', '18:30:45');
  }
  for (const document of [undefined, {}, { documentElement: { getAttribute() { throw new Error('unavailable'); } } }]) {
    const window = {};
    vm.runInNewContext(source, { window, document });
    assert.equal(window.ChunnelTime.zone(), 'UTC');
    assertFormats(window.ChunnelTime, 0, '1970-01-01', '00:00:00');
  }
});

test('a page captures its configured zone once, and a new page reads the new setting', () => {
  const first = fixture(' Asia/Shanghai ');
  assert.equal(first.time.zone(), 'Asia/Shanghai');
  first.setZone('America/New_York');
  assertFormats(first.time, '2026-12-31T18:30:45Z', '2027-01-01', '02:30:45');
  assert.deepEqual(first.reads, ['data-site-timezone']);
  assertFormats(fixture('America/New_York').time, '2026-12-31T18:30:45Z', '2026-12-31', '13:30:45');
  const fallback = fixture('invalid');
  assert.equal(fallback.time.zone(), 'UTC');
  fallback.setZone('Asia/Kathmandu');
  assert.equal(fallback.time.zone(), 'UTC');
});
