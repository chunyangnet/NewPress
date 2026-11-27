const test = require('node:test');
const assert = require('node:assert/strict');
const { loadModule } = require('./helpers/typescript.cjs');
const { resourceFixture, originalConfig } = require('./helpers/editor-tools.cjs');
const data = loadModule('frontend/core/resource-data.ts');

function selected(f) { return f.config().items[0].variants[0]; }
for (const variant of ['TypeScript', 'generated']) {
  test(`${variant}: null resource collections remain editable and reach the preview snapshot`, async () => {
    const f = resourceFixture(variant); let changes = 0; f.input.addEventListener('input', () => changes++); await f.options();
    assert.match(f.workspace.innerHTML, /已有隐藏内容/); assert.match(f.workspace.innerHTML, /0 个问题 · 0 个字段/);
    assert.equal(f.message.textContent, ''); assert.equal(changes, 0);
    f.update('items.0.variants.0.groups.0.conditions.0.amount', '19.90', { moneyKey: 'pay' }); await f.action('add-question').completion;
    const snapshot = selected(f); assert.equal(changes, 2); assert.equal(snapshot.groups[0].conditions[0].amount, 1990);
    assert.equal(snapshot.body, '私密正文'); assert.deepEqual(snapshot.downloads, []); assert.deepEqual(snapshot.fields, []); assert.equal(snapshot.questions.length, 1);
  });
  test(`${variant}: invalid amount drafts block submit and are retained across repaint`, async () => {
    const f = resourceFixture(variant); await f.options();
    const control = f.update('items.0.variants.0.groups.0.conditions.0.amount', '9.999', { moneyKey: 'pay' });
    assert.match(control.validationMessage, /2 位/); assert.equal(selected(f).groups[0].conditions[0].amount, 990);
    assert.equal(f.form.emit('submit').defaultPrevented, true); await f.action('add-question').completion;
    assert.match(f.workspace.innerHTML, /value="9.999"/); assert.equal(f.form.emit('submit').defaultPrevented, true);
    f.update('items.0.variants.0.groups.0.conditions.0.amount', '10.00', { moneyKey: 'pay' }); assert.equal(f.form.emit('submit').defaultPrevented, false);
    assert.equal(selected(f).groups[0].conditions[0].amount, 1000);
  });
  test(`${variant}: restoring a form refreshes resources and malformed recovery cannot overwrite them`, async () => {
    const f = resourceFixture(variant); await f.options();
    f.update('items.0.variants.0.groups.0.conditions.0.amount', '9.999', { moneyKey: 'pay' });
    const restored = originalConfig(); restored.items[0].title = '恢复的资源'; restored.items[0].variants[0].body = '恢复的私密正文';
    f.input.value = JSON.stringify(restored); f.form.emit('input');
    assert.match(f.workspace.innerHTML, /恢复的资源/); assert.equal(f.form.emit('submit').defaultPrevented, false);
    f.update('items.0.title', '继续编辑'); assert.equal(selected(f).body, '恢复的私密正文');
    f.input.value = '{broken'; f.form.emit('change');
    await f.action('add-resource').completion; assert.equal(f.input.value, '{broken');
    assert.equal(f.form.emit('submit').defaultPrevented, true); assert.match(f.message.textContent, /无法读取/);
    f.input.value = JSON.stringify({ items: [] }); f.form.emit('input');
    await f.action('preset-member').completion; assert.equal(selected(f).kind, 'content');
  });
  test(`${variant}: submitting an invalid amount in another version returns to the field needing correction`, async () => {
    const f = resourceFixture(variant); await f.options();
    f.update('items.0.variants.0.groups.0.conditions.0.amount', '9.999', { moneyKey: 'pay' });
    await f.action('add-version').completion;
    assert.doesNotMatch(f.workspace.innerHTML, /value="9.999"/);
    assert.equal(f.form.emit('submit').defaultPrevented, true);
    assert.match(f.workspace.innerHTML, /value="9.999"/);
    assert.match(f.workspace.innerHTML, /data-action="version"[^>]*data-index="0" aria-pressed="true"/);
    f.update('items.0.variants.0.groups.0.conditions.0.amount', '9.99', { moneyKey: 'pay' });
    assert.equal(f.form.emit('submit').defaultPrevented, false);
  });
  test(`${variant}: currency switches preserve exact value or refuse precision loss`, async () => {
    const f = resourceFixture(variant); await f.options();
    const currency = f.update('items.0.variants.0.groups.0.conditions.0.currency', 'JPY', { event: 'change' });
    assert.equal(currency.value, 'CNY'); assert.equal(selected(f).groups[0].conditions[0].currency, 'CNY');
    f.update('items.0.variants.0.groups.0.conditions.0.amount', '10', { moneyKey: 'pay' });
    f.update('items.0.variants.0.groups.0.conditions.0.currency', 'JPY', { event: 'change' });
    assert.equal(selected(f).groups[0].conditions[0].amount, 10); assert.equal(selected(f).groups[0].conditions[0].currency, 'JPY');
    f.update('items.0.variants.0.groups.0.conditions.0.kind', 'points', { event: 'change' });
    assert.match(f.workspace.innerHTML, /加载货币精度/); assert.equal(selected(f).groups[0].conditions[0].id, 'pay');
  });
  test(`${variant}: presets, variant ordering and protected last rules preserve configuration`, async () => {
    const f = resourceFixture(variant, { items: [] }); await f.options();
    await f.action('preset-member').completion; assert.equal(selected(f).kind, 'content'); assert.equal(selected(f).groups[0].conditions[0].kind, 'membership');
    await f.action('add-version').completion; const added = f.config().items[0].variants[1].id;
    await f.action('move-version').completion; assert.equal(f.config().items[0].variants[0].id, added);
    await f.action('remove', 'items.0.variants.1').completion; await f.action('remove', 'items.0.variants.0').completion;
    assert.equal(f.config().items[0].variants.length, 1); assert.match(f.message.textContent, /至少保留/);
    await f.action('remove', 'items.0.variants.0.groups.1').completion;
    await f.action('remove', 'items.0.variants.0.groups.0.conditions.0').completion;
    assert.equal(selected(f).groups[0].conditions.length, 1);
    await f.action('remove', 'items.0').completion; assert.deepEqual(f.config().items, []);
  });
  test(`${variant}: locked or invalid configurations cannot be changed`, async () => {
    const invalid = { items: [{ id: 'bundle', variants: null }] }, broken = resourceFixture(variant, invalid);
    assert.deepEqual(broken.config(), invalid); assert.match(broken.message.textContent, /无法读取/); assert.equal(broken.requests.length, 0);
    const f = resourceFixture(variant); await f.options(); const original = f.input.value; f.fieldset.disabled = true;
    f.update('items.0.title', 'changed'); await f.action('add-resource').completion; assert.equal(f.input.value, original);
  });
  test(`${variant}: rejected body insertion leaves both private delivery and source unchanged`, async () => {
    const f = resourceFixture(variant); await f.options();
    const source = { value: 'secret body', selectionStart: 0, selectionEnd: 6, disabled: false, readOnly: false };
    f.document.selectors.set('[name=content_source]', source); f.window.ChunnelBlockEditor = { capture: () => ({ value: source.value, start: 0, end: 6 }), insert: () => { throw new Error('stale selection'); } };
    const original = f.input.value; await f.action('hide-selection').completion;
    assert.equal(f.input.value, original); assert.equal(source.value, 'secret body'); assert.match(f.message.textContent, /stale selection/);
    f.window.ChunnelBlockEditor.insert = (markup, selection) => { source.value = markup + source.value.slice(selection.end); };
    await f.action('hide-selection').completion; assert.equal(selected(f).body, '私密正文\nsecret'); assert.match(source.value, /\[resource:bundle\]/);
  });
  test(`${variant}: visual resource extraction publishes private HTML only after document replacement succeeds`, async () => {
    const f = resourceFixture(variant); await f.options();
    const source = { value: '<p>secret</p>', disabled: false, readOnly: false };
    f.document.selectors.set('[name=content_source]', source);
    f.window.ChunnelBlockEditor = { replaceSelectedHTML() { throw new Error('changed document'); } };
    const original = f.input.value; await f.action('hide-selection').completion;
    assert.equal(f.input.value, original); assert.equal(source.value, '<p>secret</p>');
    f.window.ChunnelBlockEditor.replaceSelectedHTML = (markup, tail) => { assert.equal(tail, true); source.value = markup; return '<p>secret</p>'; };
    await f.action('hide-tail').completion; assert.equal(selected(f).body, '私密正文\n<p>secret</p>'); assert.match(source.value, /\[resource:bundle\]/);
  });
}

test('resource decoder preserves optional collections and rejects malformed nested configuration', () => {
  const config = data.decodeResourceConfig(originalConfig()); assert.equal(config.items[0].variants[0].questions.length, 0);
  for (const bad of [null, {}, { items: null }, { items: [{ id: '../x', variants: [] }] }, { items: [{ id: 'x', variants: [] }] }]) assert.throws(() => data.decodeResourceConfig(bad));
  const unknown = originalConfig(); unknown.items[0].future_metadata = 'retained'; assert.equal(data.decodeResourceConfig(unknown).items[0].future_metadata, 'retained');
  assert.equal(data.setResourcePath(config, 'items.0.title', 'new'), true); assert.equal(config.items[0].title, 'new');
  assert.equal(data.setResourcePath(config, 'items.0.variants', []), false);
  assert.equal(data.setResourcePath(config, 'items.0.daily_limit', '4'), false);
  assert.equal(data.setResourcePath(config, 'items.0.__proto__.polluted', true), false);
  assert.equal(data.resourcePath(config, 'items.__proto__'), undefined);
  assert.equal(data.resourcePath(config, 'items.0.constructor'), undefined);
  assert.equal(data.setResourcePath(config, 'items.0.starts_at', 'invalid'), false);
  assert.equal(data.setResourcePath(config, 'items.0.starts_at', '2026-11-25T10:00:00Z'), true);
});

test('resource option decoders support the Go currency projection and reject untrusted precision', () => {
  const options = data.decodeResourceOptions({ currencies: [{ Key: 'main', Settings: { name: '积分', precision: 1 } }], plans: [{ key: 'member', name: '会员', days: 30 }], roles: null });
  assert.equal(options.currencies[0].settings.precision, 1); assert.equal(options.roles.length, 0);
  for (const precision of [undefined, null, '1', -1, 7, 1.5]) assert.throws(() => data.decodeResourceOptions({ currencies: [{ key: 'main', settings: { name: 'x', precision } }] }));
});
