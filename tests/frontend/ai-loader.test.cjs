const test = require('node:test');
const assert = require('node:assert/strict');
const {createAssetLoader} = require('../../internal/coreui/assets/js/chunnel-ai-loader.source.js');

function fixture() {
  const nodes = [];
  const doc = {head: {append(node) { nodes.push(node); }}, createElement(tag) { return {tag, remove() { this.removed = true; }}; }};
  return {load: createAssetLoader(doc), nodes};
}
test('AI assets are not requested before use and concurrent launches share one request', async () => {
  const {load, nodes} = fixture();
  assert.equal(nodes.length, 0);
  const first = load('/text.js', 'script'), duplicate = load('/text.js', 'script');
  assert.equal(first, duplicate); assert.equal(nodes.length, 1); assert.equal(nodes[0].src, '/text.js');
  nodes[0].onload(); await first;
  await load('/text.js', 'script'); assert.equal(nodes.length, 1);
});
test('failed AI resources can retry without reloading successful dependencies', async () => {
  const {load, nodes} = fixture();
  const script = load('/text.js', 'script'), style = load('/text.css', 'style');
  nodes[0].onload(); nodes[1].onerror();
  await script; await assert.rejects(style, /再次点击重试/);
  const retried = load('/text.css', 'style');
  assert.equal(nodes.length, 3); assert.equal(nodes[1].removed, true);
  assert.equal(nodes[2].rel, 'stylesheet'); assert.equal(nodes[2].href, '/text.css');
  nodes[2].onload(); await retried;
  await load('/text.js', 'script'); assert.equal(nodes.length, 3);
});
test('image assets are loaded independently after the shared text assets', async () => {
  const {load, nodes} = fixture();
  const text = load('/text.js', 'script'); nodes[0].onload(); await text;
  assert.equal(nodes.length, 1);
  const image = load('/image.js', 'script'); assert.equal(nodes.length, 2);
  nodes[1].onload(); await image; await text;
});
test('missing AI asset configuration fails without appending an empty resource', async () => {
  const {load, nodes} = fixture();
  await assert.rejects(load('', 'script'), /地址缺失/); assert.equal(nodes.length, 0);
});
