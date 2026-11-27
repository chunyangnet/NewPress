const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { compile, loadModule } = require('./helpers/typescript.cjs');

function member() {
  return {
    account: { user_id: '7', experience: '150', membership: { plan: 'reader', until: '2099-12-31T16:00:00Z' } },
    settings: { experience: { name: '经验值' }, membership: { enabled: true, plans: [{ key: 'reader', name: '阅读会员', description: '每周专题' }] },
      check_in: { enabled: true, amount: '10', mode: 'calendar_day', timezone: 'Asia/Shanghai', interval_hours: 24 } },
    level: { name: '常来常往', threshold: '100' }, next_level: { name: '热心分享', threshold: '200' },
    membership_active: true, next_check_in: '0001-01-01T00:00:00Z',
  };
}

const { parseMember } = loadModule('frontend/themes/chunnel/home-member-data.ts');
test('member parser preserves Go decimal strings beyond the safe integer range', () => {
  const data = member();
  data.account.experience = '9007199254740993'; data.settings.check_in.amount = '9223372036854775807';
  const parsed = parseMember(data);
  assert.equal(parsed.account.experience, '9007199254740993');
  assert.equal(parsed.settings.check_in.amount, '9223372036854775807');
  assert.equal(parsed.next_check_in, '0001-01-01T00:00:00Z');
});

const malformed = [
  ['missing account', data => { delete data.account; }],
  ['numeric experience', data => { data.account.experience = 150; }],
  ['negative threshold', data => { data.next_level.threshold = '-1'; }],
  ['fractional reward', data => { data.settings.check_in.amount = '1.5'; }],
  ['missing membership status', data => { delete data.membership_active; }],
  ['string check-in flag', data => { data.settings.check_in.enabled = 'false'; }],
  ['invalid plans', data => { data.settings.membership.plans = {}; }],
  ['invalid interval', data => { data.settings.check_in.interval_hours = Infinity; }],
  ['invalid next check-in date', data => { data.next_check_in = 'invalid'; }],
];
for (const [label, change] of malformed) test(`member parser rejects ${label}`, () => {
  const data = member(); change(data); assert.throws(() => parseMember(data));
});

function fixture(source) {
  const nodes = new Map();
  const node = selector => {
    if (!nodes.has(selector)) nodes.set(selector, { textContent: '', hidden: false, disabled: false, style: {}, dataset: {},
      attributes: {}, querySelector: node, setAttribute(name, value) { this.attributes[name] = value; } });
    return nodes.get(selector);
  };
  node('[data-home-checkin-card]').dataset.checkinSource = 'progression';
  const window = {}, document = { querySelector: node, documentElement: { getAttribute: () => 'Asia/Shanghai' } };
  vm.runInNewContext(source, { window, document, Intl, Date, console });
  return { node, window, paint: window.ChunnelHomeMember.paint,
    snapshot: () => JSON.stringify([...nodes].map(([key, value]) => [key, value.textContent, value.hidden, value.disabled, value.style, value.attributes])) };
}

for (const variant of ['typescript', 'generated']) {
  const source = variant === 'typescript' ? compile('frontend/themes/chunnel/home-member.ts') : fs.readFileSync('themes/chunnel/assets/js/chunnel-home-member.js', 'utf8');
  test(`${variant}: member display uses the current owner, site timezone and exact large experience values`, () => {
    const f = fixture(source), data = member();
    data.account.experience = '9007199254740993'; data.level.threshold = '9007199254740992'; data.next_level.threshold = '9007199254740994';
    assert.equal(f.paint(data, 7), true);
    assert.equal(f.node('[data-home-xp]').textContent, '9,007,199,254,740,993 经验值');
    assert.match(f.node('[data-home-level-next]').textContent, /再获得 1 经验值/);
    assert.equal(f.node('[data-home-level-meter]').attributes['aria-valuenow'], '50');
    assert.equal(f.node('[data-home-level-fill]').style.width, '50%');
    assert.equal(f.node('[data-home-membership-until]').textContent, '有效期至 2100.01.01');
    assert.equal(f.node('[data-home-checkin]').disabled, false);
    assert.match(f.node('[data-home-checkin-meta]').textContent, /北京时间/);
    const initial = f.snapshot();
    assert.equal(f.paint(data, 8), false); assert.equal(f.snapshot(), initial);
    assert.equal(f.paint({ ...data, account: { ...data.account, user_id: '0' } }, 0), false);
    assert.equal(f.snapshot(), initial);
  });
  test(`${variant}: malformed member bodies preserve the previous painted card`, () => {
    const f = fixture(source); assert.equal(f.paint(member(), 7), true);
    const initial = f.snapshot();
    for (const [, change] of malformed) { const data = member(); change(data); assert.equal(f.paint(data, 7), false); assert.equal(f.snapshot(), initial); }
    for (const value of [null, [], true, 'invalid']) { assert.equal(f.paint(value, 7), false); assert.equal(f.snapshot(), initial); }
  });
  test(`${variant}: disabled membership and check-in replace stale labels and hide the old countdown`, () => {
    const f = fixture(source), data = member();
    data.next_check_in = '2099-11-27T00:00:00Z'; f.paint(data, 7);
    assert.equal(f.node('[data-home-checkin]').disabled, true);
    assert.equal(f.node('[data-home-checkin-label]').textContent, '本轮已领取');
    assert.match(f.node('[data-home-checkin-meta]').textContent, /下次可签/);
    data.membership_active = false; data.settings.membership.enabled = false; data.settings.check_in.enabled = false;
    data.level = null; data.next_level = null; f.paint(data, 7);
    assert.equal(f.node('[data-home-member-state]').textContent, '社区成员');
    assert.equal(f.node('[data-home-member-plan]').hidden, true);
    assert.equal(f.node('[data-home-member-description]').hidden, true);
    assert.equal(f.node('[data-home-level-block]').hidden, true);
    assert.equal(f.node('[data-home-checkin-label]').textContent, '签到暂未开放');
    assert.equal(f.node('[data-home-checkin-meta]').hidden, true);
    assert.equal(f.node('[data-home-checkin-meta]').textContent, '');
  });
  test(`${variant}: a wallet check-in card is not overwritten by progression rewards`, () => {
    const f = fixture(source); f.node('[data-home-checkin-card]').dataset.checkinSource = 'wallet';
    f.node('[data-home-checkin-label]').textContent = '领取金币'; f.paint(member(), 7);
    assert.equal(f.node('[data-home-checkin-label]').textContent, '领取金币');
  });
  test(`${variant}: full reward form owns confirmed and pending controls during member refresh`,()=>{
    const f=fixture(source);f.window.ChunnelHomeFeaturesReady=true;
    f.node('[data-home-checkin-label]').textContent='本轮已领取';f.node('[data-home-checkin]').disabled=true;
    f.node('[data-home-checkin-meta]').textContent='下次可签到时间暂未更新，请刷新查看。';
    assert.equal(f.paint(member(),7),true);
    assert.equal(f.node('[data-home-checkin-label]').textContent,'本轮已领取');assert.equal(f.node('[data-home-checkin]').disabled,true);
    assert.match(f.node('[data-home-checkin-meta]').textContent,/暂未更新/);
  });
}
