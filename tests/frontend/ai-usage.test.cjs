const test = require('node:test');
const assert = require('node:assert/strict');
for (const path of ['../../internal/coreui/assets/js/chunnel-ai-usage.source.js','../../internal/coreui/assets/js/chunnel-ai-usage.js']) {
  const api = require(path);
  test(`${path}: unknown usage stays unknown and zero remains zero`, () => {
    assert.equal(api.compact(undefined),'—');assert.equal(api.count(null),'—');assert.equal(api.money(undefined,'USD'),'未计价');assert.equal(api.money(0,'USD'),'USD 0.00');
    assert.equal(api.tps(undefined,1000),'—');assert.equal(api.tps(20,100),'—');assert.equal(api.tps(20,1000),'20.0 tps');
    assert.equal(api.chartValue({requests:2,usage_known:0,tokens:0},'tokens'),null);
    assert.equal(api.chartValue({requests:2,usage_known:2,tokens:0},'tokens'),0);
    assert.equal(api.chartValue({requests:2,cost_known:0,costs:{}},'costs','USD'),null);
    assert.equal(api.chartValue({requests:2,cost_known:2,costs:{USD:2,CNY:100}},'costs','USD'),2);
  });
  test(`${path}: chart fills calendar gaps across DST and leap days`, () => {
    assert.equal(api.dayOffset('2024-03-01',-1),'2024-02-29');
    const days=api.fillDays('2026-10-30','2026-11-02',[{day:'2026-11-01',requests:4,tokens:10,usage_known:1}]);
    assert.equal(days.length,4);assert.equal(days[0].requests,0);assert.equal(days[2].requests,4);assert.equal(days[3].day,'2026-11-02');
    assert.ok(api.fillDays('2025-01-01','2026-12-31',[]).length<=91);
  });
}
