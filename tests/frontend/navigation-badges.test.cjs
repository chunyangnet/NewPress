const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const {compile} = require('./helpers/typescript.cjs');
for (const variant of ['typescript','generated']) {
const source = variant === 'typescript' ? compile('frontend/core/navigation-badges.ts') : fs.readFileSync('internal/coreui/assets/js/newpress-navigation-badges.js','utf8');

function fixture() {
  const window = {}, fields = {text: {value: '新'}, show_on_mobile: {checked: true}};
  vm.runInNewContext(source, {window});
  const card = {querySelector(selector) { return fields[selector.match(/badge_(.+)"/)[1]]; }};
  return {fields, read: () => JSON.parse(JSON.stringify(window.NewPressNavigationBadges.readCard(card)))};
}

test(variant + ': mobile visibility is opt-out and the legacy badge payload stays unchanged', () => {
  const f = fixture(), legacy = f.read();
  assert.ok(!Object.hasOwn(legacy.badge, 'hide_on_mobile'));
  f.fields.show_on_mobile.checked = false;
  assert.deepEqual(f.read(), {badge: {...legacy.badge, hide_on_mobile: true}});
  f.fields.show_on_mobile.checked = true;
  assert.deepEqual(f.read(), legacy);
  delete f.fields.show_on_mobile;
  assert.deepEqual(f.read(), legacy);
});

test(variant + ': clearing badge text also removes its mobile visibility override', () => {
  const f = fixture();
  f.fields.show_on_mobile.checked = false;
  f.fields.text.value = '  ';
  assert.deepEqual(f.read(), {});
});

test(variant + ': badge paint treats markup as text and bounds only preview styles', () => {
  const window={}; vm.runInNewContext(source,{window});
  const values={}, node={dataset:{},style:{setProperty(key,value){values[key]=value;}}};
  const badge={text:'<img src=x onerror=alert(1)>',style:'unknown',placement:'unexpected',background:'red; display:none',color:'#aabbcc',font_size:99,radius:-4,offset_x:NaN,offset_y:-20};
  window.NewPressNavigationBadges.paint(node,badge);
  assert.equal(node.textContent,badge.text); assert.equal(node.hidden,false);
  assert.equal(node.dataset.style,'bubble'); assert.equal(node.dataset.placement,'corner');
  assert.equal(values['--nav-badge-bg'],'#D14343'); assert.equal(values['--nav-badge-color'],'#aabbcc');
  assert.equal(values['--nav-badge-size'],'16px'); assert.equal(values['--nav-badge-radius'],'0px');
  assert.equal(values['--nav-badge-x'],'0px'); assert.equal(values['--nav-badge-y'],'-12px');
  assert.equal(badge.radius,-4); assert.equal(badge.font_size,99);
  window.NewPressNavigationBadges.paint(node); assert.equal(node.hidden,true); assert.equal(node.textContent,'');
});

test(variant + ': preset switches adjust default colors and preserve custom ones', () => {
  const window={}; vm.runInNewContext(source,{window});
  const fields={style:{value:'solid'},color:{value:'#FFFFFF'},background:{value:'#D14343'},text:{value:'新'}};
  const preview={dataset:{},style:{setProperty(){}}}, label={};
  const card={dataset:{},querySelector(selector){if(selector==='[data-badge-preview]')return preview;if(selector==='[data-badge-preview-label]')return label;return fields[selector.match(/badge_(.+)"/)[1]];}};
  const refresh=()=>window.NewPressNavigationBadges.previewCard(card,'标题');
  refresh(); fields.style.value='outline'; refresh(); assert.equal(fields.color.value,'#D14343');
  fields.style.value='solid'; refresh(); assert.equal(fields.color.value,'#FFFFFF');
  fields.color.value='#123456'; fields.style.value='soft'; refresh(); assert.equal(fields.color.value,'#123456');
  assert.equal(label.textContent,'标题');
  delete fields.style; refresh(); assert.equal(preview.dataset.style,'bubble');
});
}
