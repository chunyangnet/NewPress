const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(require('node:path').join(__dirname, '../../internal/coreui/assets/js/chunnel-plugins.js'), 'utf8');
const tick = () => new Promise(resolve => setImmediate(resolve));

function fixture() {
  const nodes = new Map(), calls = [], events = {}, fields = {}, rules = [];
  let reloads = 0;
  const node = (value = '') => ({ value, checked: false, hidden: true, disabled: false, textContent: '', dataset: {}, events: {},
    addEventListener(type, fn) { this.events[type] = fn; }, setAttribute() {}, removeAttribute() {}, scrollIntoView() {}, focus() {} });
  const get = selector => nodes.get(selector) || null;
  const put = selector => { const n = node(); nodes.set(selector, n); return n; };
  for (const selector of ['[data-plugin-status]', '[data-settings-state]', '[data-provider-help]', '[data-provider-key]', '[data-provider-clear]', '[data-provider-custom]', '[data-preview-role]', '[data-preview-device-select]', '[data-preview-location]', '[data-preview-device]', '[data-preview-hidden]', '[data-preview-device-label]', '[data-retry-result]', '[data-test-result]', '[data-record-result]', '[data-preview-country]', '[data-preview-flag]', '[data-preview-globe]', '[data-preview-location-label]', '[data-flag-options]', '[data-flag-hint]']) put(selector);
  const values = { provider: 'ipwhois', key: '', endpoint: '', country_path: 'country', country_code_path: 'country_code', flag_mode: 'foreign', region_path: 'region', city_path: 'city', success_path: 'success', success_value: 'true', retention_days: '30', cache_days: '7', daily_limit: '900' };
  for (const [name, value] of Object.entries(values)) fields[name] = Object.assign(node(value), {name, type: 'text'});
  const flagInputs = ['off', 'all', 'foreign'].map(value => Object.assign(node(value), { name: 'flag_mode', type: 'radio', checked: value === 'foreign' }));
  fields.flag_mode = { get value() { return flagInputs.find(input => input.checked)?.value || ''; }, set value(value) { flagInputs.forEach(input => { input.checked = input.value === value; }); } };
  for (const name of ['location', 'device', 'browser', 'fallback', 'clear_key']) fields[name] = Object.assign(node(), {name, type: 'checkbox', checked: ['location','device','browser'].includes(name)});
  for (const role of ['guest', 'administrator']) {
    const location = Object.assign(node(), {type: 'checkbox', dataset: {rule: 'location'}, checked: role === 'guest'});
    const device = Object.assign(node(), {type: 'checkbox', dataset: {rule: 'device'}, checked: role === 'guest'});
    rules.push({dataset: {originRole: role}, location, device, querySelector: selector => selector.includes('location') ? location : device});
  }
  get('[data-preview-role]').value = 'guest';
  get('[data-preview-country]').value = 'domestic';
  get('[data-preview-device-select]').value = 'mobile';
  const submit = node();
  const form = put('[data-origin-settings]'); form.dataset.version = '1';
  form.elements = {namedItem: name => fields[name]};
  form.querySelectorAll = () => [...Object.values(fields).filter(input => input !== fields.flag_mode), ...flagInputs, ...rules.flatMap(r => [r.location, r.device])];
  form.querySelector = () => submit;
  const tester = put('[data-origin-test]'); tester.elements = {ip: node('8.8.8.8')}; tester.querySelector = () => node();
  const record = put('[data-origin-record]'); record.elements = {comment_id: node('1')}; record.querySelector = () => node();
  const retry = node(); retry.dataset = {pluginAction: 'retry', slug: 'comment-origin'};
  const root = {dataset: {userId: '1', csrf: 'csrf'}, querySelector: get, querySelectorAll: selector => selector === '[data-origin-role]' ? rules : selector === '[data-plugin-action]' ? [retry] : []};
  vm.runInNewContext(source, { document: {querySelector: () => root}, window: {addEventListener: (type, fn) => { events[type] = fn; }}, location: {reload: () => { reloads++; }}, fetch: (path, options) => new Promise((resolve, reject) => calls.push({path, options, resolve, reject})) });
  const respond = (result, status = 200) => calls.at(-1).resolve({ok: status < 400, status, json: async () => result});
  const fire = (target, type) => target.events[type]({preventDefault() {}, currentTarget: target});
  const dirty = () => { let prevented = false; events.beforeunload({preventDefault() {prevented = true;}}); return prevented; };
  return {get, calls, fields, rules, form, tester, record, retry, respond, fire, dirty, reloads: () => reloads};
}

test('retry preserves an unsaved settings draft without navigation', async () => {
  const f = fixture(); f.fields.retention_days.value = '14'; f.fire(f.form, 'input');
  assert.equal(f.dirty(), true);
  f.fire(f.retry, 'click'); f.respond({ok: true}); await tick();
  assert.equal(f.reloads(), 0); assert.equal(f.fields.retention_days.value, '14'); assert.equal(f.dirty(), true);
  assert.match(f.get('[data-retry-result]').textContent, /草稿已保留/);
});

test('a failed second diagnostic replaces the first successful result in place', async () => {
  const f = fixture(); f.fire(f.tester, 'submit');
  f.respond({country: '中国', region: '浙江', city: '杭州', precision: 'city', provider: 'fixture'}); await tick();
  assert.match(f.get('[data-test-result]').textContent, /8\.8\.8\.8/);
  f.tester.elements.ip.value = '127.0.0.1'; f.fire(f.tester, 'submit');
  assert.doesNotMatch(f.get('[data-test-result]').textContent, /杭州/);
  f.respond({error: {message: '请输入有效公网 IPv4 或 IPv6'}}, 422); await tick();
  assert.equal(f.get('[data-test-result]').dataset.tone, 'error');
  assert.doesNotMatch(f.get('[data-test-result]').textContent, /杭州/);
  assert.equal(f.get('[data-plugin-status]').hidden, true);
});

test('failed private record lookup clears the previous IP instead of retaining it', async () => {
  const f = fixture(); f.fire(f.record, 'submit');
  f.respond({id: 1, ip: '8.8.8.8', status: 'resolved', location: {country: '美国'}, ua: 'Windows'}); await tick();
  assert.match(f.get('[data-record-result]').textContent, /美国/);
  f.record.elements.comment_id.value = '2'; f.fire(f.record, 'submit');
  f.respond({error: {message: '评论不存在'}}, 404); await tick();
  assert.equal(f.get('[data-record-result]').textContent, '评论不存在');
});

test('unverified CDN source explains missing IP without claiming retention expiry', async () => {
  const f = fixture(); f.fire(f.record, 'submit');
  f.respond({id: 1, status: 'unavailable', source_label: '阿里云 ESA', source_issue_label: '缺少真实 IP 标头', location: {}, ua: 'Windows'}); await tick();
  const message = f.get('[data-record-result]').textContent;
  assert.match(message, /阿里云 ESA/);
  assert.match(message, /未采集（来源未通过验证）/);
  assert.match(message, /缺少真实 IP 标头/);
  assert.doesNotMatch(message, /已到期/);
});

test('role and device previews leave the saved form clean; reverting edits removes leave prompt', () => {
  const f = fixture(); f.get('[data-preview-device-select]').value = 'desktop'; f.fire(f.get('[data-preview-device-select]'), 'change');
  assert.equal(f.get('[data-preview-device-label]').textContent, 'Windows · Edge'); assert.equal(f.dirty(), false);
  f.get('[data-preview-role]').value = 'administrator'; f.fire(f.get('[data-preview-role]'), 'change');
  assert.equal(f.get('[data-preview-location]').hidden, true); assert.equal(f.dirty(), false);
  f.fields.retention_days.value = '14'; f.fire(f.form, 'input'); assert.equal(f.dirty(), true);
  f.fields.retention_days.value = '30'; f.fire(f.form, 'input'); assert.equal(f.dirty(), false);
});

test('save preserves newer role edits and only clears the submitted secret', async () => {
  const f = fixture(); f.fields.key.value = 'submitted'; f.fire(f.form, 'input'); f.fire(f.form, 'submit');
  f.rules[0].location.checked = false; f.fields.key.value = 'newer-secret'; f.fire(f.form, 'input');
  f.respond({settings: {version: 2}, key_saved: true}); await tick();
  assert.equal(f.form.dataset.version, 2); assert.equal(f.rules[0].location.checked, false);
  assert.equal(f.fields.key.value, 'newer-secret'); assert.equal(f.dirty(), true);
  f.fire(f.form, 'submit'); f.respond({settings: {version: 3}, key_saved: true}); await tick();
  assert.equal(f.fields.key.value, ''); assert.equal(f.dirty(), false);
  f.rules[0].device.checked = false; f.fire(f.form, 'change'); assert.equal(f.dirty(), true);
  f.rules[0].device.checked = true; f.fire(f.form, 'change'); assert.equal(f.dirty(), false);
});

test('malformed save acknowledgement preserves secret and version', async () => {
  const f = fixture(); f.fields.key.value = 'keep-me'; f.fire(f.form, 'input'); f.fire(f.form, 'submit');
  f.respond({settings: {version: 2}}); await tick();
  assert.equal(f.fields.key.value, 'keep-me'); assert.equal(f.form.dataset.version, '1'); assert.equal(f.dirty(), true);
  assert.equal(f.get('[data-plugin-status]').dataset.tone, 'error');
});


test('flag preview supports all three modes, missing country codes and location privacy', () => {
  const f = fixture();
  const place = f.get('[data-preview-country]'), flag = f.get('[data-preview-flag]');
  assert.equal(flag.hidden, true);
  place.value = 'foreign'; f.fire(place, 'change');
  assert.equal(flag.hidden, false); assert.equal(flag.src, '/plugin-assets/comment-origin/flags/jp.svg');
  assert.equal(f.get('[data-preview-globe]').hidden, true); assert.equal(f.dirty(), false);
  f.fields.flag_mode.value = 'all'; f.fire(f.form, 'change');
  place.value = 'domestic'; f.fire(place, 'change');
  assert.equal(flag.hidden, false); assert.equal(flag.src, '/plugin-assets/comment-origin/flags/cn.svg');
  place.value = 'unknown'; f.fire(place, 'change'); assert.equal(flag.hidden, true);
  assert.equal(f.get('[data-preview-globe]').hidden, false);
  place.value = 'foreign'; f.fire(place, 'change');
  f.fields.flag_mode.value = 'off'; f.fire(f.form, 'change'); assert.equal(flag.hidden, true);
  f.fields.flag_mode.value = 'all'; f.fields.location.checked = false; f.fire(f.form, 'change');
  assert.equal(flag.hidden, true); assert.equal(f.get('[data-flag-options]').disabled, true);
  assert.equal(f.fields.flag_mode.value, 'all');
  f.fields.location.checked = true; f.fire(f.form, 'change');
  assert.equal(flag.hidden, false); assert.equal(f.get('[data-flag-options]').disabled, false);
  f.get('[data-preview-role]').value = 'administrator'; f.fire(f.get('[data-preview-role]'), 'change');
  assert.equal(flag.hidden, true); assert.equal(f.get('[data-preview-location]').hidden, true);
});

test('flag radios track dirty state, persist mode and code mapping, and preserve in-flight changes', async () => {
  const f = fixture();
  f.fields.flag_mode.value = 'all'; f.fire(f.form, 'change'); assert.equal(f.dirty(), true);
  f.fields.flag_mode.value = 'foreign'; f.fire(f.form, 'change'); assert.equal(f.dirty(), false);
  f.fields.flag_mode.value = 'off'; f.fields.country_code_path.value = 'geo.iso'; f.fire(f.form, 'change');
  f.fire(f.form, 'submit');
  const saved = JSON.parse(f.calls.at(-1).options.body).settings;
  assert.equal(saved.flag_mode, 'off'); assert.equal(saved.country_code_path, 'geo.iso');
  f.fields.flag_mode.value = 'all'; f.fire(f.form, 'change');
  f.respond({settings: {version: 2}, key_saved: false}); await tick();
  assert.equal(f.fields.flag_mode.value, 'all'); assert.equal(f.dirty(), true);
  f.fields.flag_mode.value = 'off'; f.fire(f.form, 'change'); assert.equal(f.dirty(), false);
});
