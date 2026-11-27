const {test} = require('node:test');
const assert = require('node:assert/strict');
const library = require('../../internal/coreui/assets/js/chunnel-component-library.source.js');
test('every component has a unique identity and lossless Unicode configuration', () => {
  const ids = new Set();
  for (const d of library.definitions) {
    const c = library.defaults(d.kind); c.title = '中文 🙂 <script>" &';
    assert.deepEqual(library.decode(library.encode(c)), c);
    assert.equal(library.markup(c).includes('<script>'), false);
    assert.equal(ids.has(c.id), false); ids.add(c.id);
  }
  assert.equal(ids.size, 26);
});
test('component picker icons all resolve to the local Lucide catalog', () => {
  const {icons} = require('../../third_party/lucide/icons.json');
  for (const definition of library.definitions) {
    assert.ok(icons[definition.icon], `${definition.kind}: ${definition.icon}`);
  }
});
test('configuration decoding rejects missing and oversized identity', () => {
  assert.throws(() => library.decode('A'.repeat(90000)));
  assert.throws(() => library.decode(Buffer.from('{"kind":"poll"}').toString('base64url')));
  const c=library.defaults('card');c.text='A'.repeat(66000);assert.throws(()=>library.encode(c));
});
