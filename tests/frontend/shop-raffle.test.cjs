const test = require('node:test');
const assert = require('node:assert/strict');
const admin = require('./helpers/typescript.cjs').loadModule('frontend/shared/shop-admin-helpers.ts', { Date });
const raffleDrawTime = admin.raffleDrawTime;
const raffleAllocation = (...args) => structuredClone(admin.raffleAllocation(...args));
const shop = require('./helpers/typescript.cjs').loadModule('frontend/shared/shop-helpers.ts', { Date, URL });
const { raffleDeadline, rafflePhase, rafflePurchaseCapacity } = shop;
const raffleCountdown = (...args) => ({ ...shop.raffleCountdown(...args) });

test('editing an existing draw preserves its exact timestamp and offset', () => {
  const original = '2027-01-01T10:02:17.123456789+08:00';
  const date = new Date(original);
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  assert.equal(raffleDrawTime(local, original), original);
  const changed = '2027-01-04T12:30';
  assert.equal(raffleDrawTime(changed, original), new Date(changed).toISOString());
  assert.throws(() => raffleDrawTime('', original));
});

test('pool preview and submission share exact basis points and quota limits', () => {
  const pool = raffleAllocation('instant', ['99.97', '0.01'], true);
  assert.equal(pool.message, '总中奖概率 99.98% · 未中奖 0.02%');
  assert.deepEqual(pool.amounts, [9997, 1]);
  assert.equal(raffleAllocation('instant', ['0'], false).valid, true);
  assert.throws(() => raffleAllocation('instant', ['0'], true));
  assert.throws(() => raffleAllocation('instant', ['0.001'], true));
  assert.equal(raffleAllocation('instant', ['100', '0.01'], true).valid, false);
  assert.equal(raffleAllocation('scheduled', ['999','1'], true).valid, true);
  assert.equal(raffleAllocation('scheduled', ['999','2'], true).valid, false);
  assert.throws(() => raffleAllocation('scheduled', ['1.5'], true));
});

test('a past deadline closes entry while publication follows the server snapshot', () => {
  const drawAt = '2027-01-01T12:00:00+08:00';
  const deadline = raffleDeadline({kind:'raffle', raffle:{mode:'scheduled', draw_at:drawAt}});
  assert.equal(deadline, Date.parse(drawAt));
  assert.equal(raffleDeadline({kind:'raffle', raffle:{mode:'instant'}}), Infinity);
  assert.equal(rafflePhase(deadline, deadline - 1, {entries:0}, deadline - 1), '距截止 1 分钟');
  assert.equal(rafflePhase(deadline, deadline, {entries:0}, deadline - 1), '已截止，等待开奖');
  assert.equal(rafflePhase(deadline, deadline, {entries:0}, deadline), '本轮无人参与');
  assert.equal(rafflePhase(deadline, deadline, {entries:3}, deadline), '已截止，等待开奖');
  assert.equal(rafflePhase(deadline, deadline, {entries:3, drawn:true}, deadline), '已开奖');
});

test('countdown rolls through days, minutes and cutoff without negative digits', () => {
  const now = Date.parse('2027-01-01T12:00:00+08:00');
  assert.deepEqual(raffleCountdown(now + (3 * 86400 + 12 * 3600 + 45 * 60 + 27) * 1000, now), { days: 3, hours: 12, minutes: 45, seconds: 27 });
  assert.deepEqual(raffleCountdown(now + 60001, now), { days: 0, hours: 0, minutes: 1, seconds: 1 });
  assert.deepEqual(raffleCountdown(now + 60000, now), { days: 0, hours: 0, minutes: 1, seconds: 0 });
  assert.deepEqual(raffleCountdown(now + 59999, now), { days: 0, hours: 0, minutes: 1, seconds: 0 });
  assert.deepEqual(raffleCountdown(now + 1, now), { days: 0, hours: 0, minutes: 0, seconds: 1 });
  assert.deepEqual(raffleCountdown(now, now), { days: 0, hours: 0, minutes: 0, seconds: 0 });
  assert.deepEqual(raffleCountdown(now - 1, now), { days: 0, hours: 0, minutes: 0, seconds: 0 });
  assert.equal(raffleCountdown(Infinity, now).seconds, 0);
});

test('combined caps choose the tightest remaining stock, activity and personal scope', () => {
  const p = { stock: -1, raffle: { ticket_limit: 100, user_limit: 2, order_limit: 1 } };
  const campaign = { ticket_limit: 500, user_limit: 5 };
  const pub = { product_left: 100, campaign_left: 500 };
  const personal = { product_left: 98, campaign_left: 496, user_product_left: 1, user_campaign_left: 1 };
  assert.equal(rafflePurchaseCapacity(p, pub, personal, campaign).max, 1);
  assert.equal(rafflePurchaseCapacity({ ...p, raffle: { ...p.raffle, order_limit: 0 } }, pub, undefined, campaign).max, 2);
  assert.equal(rafflePurchaseCapacity({ stock: 4, raffle: {} }, undefined, undefined).max, 4);
  assert.equal(rafflePurchaseCapacity({ stock: -1, raffle: { user_limit: 0 } }, undefined, undefined, { user_limit: 0 }).max, 99);
  assert.equal(rafflePurchaseCapacity(p, pub, { ...personal, user_product_left: 0 }, campaign).personalLimit, true);
  const sold = rafflePurchaseCapacity(p, { ...pub, campaign_left: 0 }, { ...personal, campaign_left: 0, user_product_left: 0 }, campaign);
  assert.equal(sold.max, 0);
  assert.equal(sold.soldOut, true);
  assert.equal(sold.personalLimit, false);
});
