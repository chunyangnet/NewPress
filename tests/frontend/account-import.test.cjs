const nodeTest = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
for (const variant of ['TypeScript', 'generated']) {
const source = variant === 'TypeScript' ? require('./helpers/typescript.cjs').compile('frontend/shared/account-import.ts') : fs.readFileSync(require('node:path').join(__dirname, '../../themes/chunnel/assets/js/chunnel-account-import.js'), 'utf8');
const test = (name, fn) => nodeTest(variant + ': ' + name, fn);
const tick = () => new Promise(resolve => setImmediate(resolve));
function fixture(raw = {}) {
  const nodes = new Map(), calls = [], events = {}, storage = {}, win = {};
  const get = s => { if (!nodes.has(s)) nodes.set(s, { hidden: true, disabled: false, textContent: '', addEventListener(k, v) { this[k] = v; } }); return nodes.get(s); };
  const root = { dataset: { importAccount: '2' }, querySelector: get };
  let session = { id: 2, authenticated: true };
  win.addEventListener = (k, fn) => { storage[k] = fn; };
  win.ChunnelInteractions = { getSession: async () => session, api: (path, options) => new Promise((resolve, reject) => calls.push({ path, options, resolve, reject })) };
  vm.runInNewContext(source, { window: win, Error, document: { querySelector: () => root, addEventListener: (k, fn) => { events[k] = fn; } }, URL, location: { origin: 'https://example.test' }, localStorage: { getItem: () => JSON.stringify(raw), setItem: () => { throw new Error('must not clear local preferences'); } } });
  return { get, calls, win, events, storage, changeSession(value) { session = value; events['chunnel:session']({ detail: value }); } };
}

test('guest import accepts same-origin articles and stable IDs, deduplicates, and skips legacy names', () => {
  const f = fixture(), n = f.win.ChunnelGuestImport.normalize({ saved: [{url:'/posts/a#comments'}, {url:'https://example.test/posts/a?q=x'}, {url:'https://evil.test/posts/a'}, {url:'javascript:alert(1)'}, {url:'/admin/'}], likes:['post:2','like:2','post:3','post:0'], following:['alice','bob'], following_ids:[4] }, 'https://example.test');
  assert.deepEqual(JSON.parse(JSON.stringify(n)), {bookmarks:['/posts/a'],likes:[2,3],following:[4],skipped:5});
});

test('import needs explicit preview and confirmation, retains local data, and allows failed save retry', async () => {
  const f = fixture({saved:[{url:'/posts/a'}]}); assert.equal(f.calls.length,0);
  f.get('[data-import-preview]').click(); await tick();
  assert.equal(f.calls[0].options.body.preview,true); assert.equal(f.calls[0].options.body.account_id,2);
  f.calls[0].resolve({preview:true,added:1,existing:0,skipped:0}); await tick();
  assert.equal(f.get('[data-import-confirm]').hidden,false);
  f.get('[data-import-confirm]').click(); f.get('[data-import-confirm]').click(); await tick();
  assert.equal(f.calls.length,2); assert.equal(f.calls[1].options.body.preview,false);
  f.calls[1].reject(new Error('服务不可用')); await tick(); assert.match(f.get('[data-import-result]').textContent,/服务不可用/);
  f.get('[data-import-confirm]').click(); await tick(); f.calls[2].resolve({preview:false,added:1,existing:0,skipped:0}); await tick();
  assert.match(f.get('[data-import-result]').textContent,/已新增 1 项/); assert.equal(f.get('[data-import-confirm]').hidden,true);
});

test('switching account or changing local records invalidates pending preview and ignores stale response', async () => {
  const f = fixture({likes:['post:1']}); f.get('[data-import-preview]').click(); await tick();
  f.changeSession({id:3,authenticated:true}); f.calls[0].resolve({preview:true,added:1,existing:0,skipped:0}); await tick();
  assert.equal(f.get('[data-import-confirm]').hidden,true); assert.equal(f.get('[data-import-result]').textContent,'');
  f.get('[data-import-confirm]').click(); await tick(); assert.equal(f.calls.length,1);
  f.changeSession({id:2,authenticated:true}); f.get('[data-import-preview]').click(); await tick(); f.calls[1].resolve({preview:true,added:1,existing:0,skipped:0}); await tick();
  f.storage.storage({key:'chunnel.preferences'}); assert.equal(f.get('[data-import-confirm]').hidden,true); assert.match(f.get('[data-import-result]').textContent,/重新检查/);
});

test('failed or malformed previews never authorize import', async () => {
  const f=fixture({likes:['post:1']});
  for(const data of [{preview:false,added:1,existing:0,skipped:0},{preview:true,added:'1',existing:0,skipped:0}]) {
    f.get('[data-import-preview]').click(); await tick(); const count=f.calls.length;
    f.calls[count-1].resolve(data); await tick(); assert.equal(f.get('[data-import-confirm]').hidden,true);
    f.get('[data-import-confirm]').click(); await tick(); assert.equal(f.calls.length,count);
  }
});

test('a preview acknowledgement cannot be mistaken for a committed import', async () => {
  const f=fixture({likes:['post:1']}); f.get('[data-import-preview]').click(); await tick(); f.calls[0].resolve({preview:true,added:1,existing:0,skipped:0}); await tick();
  f.get('[data-import-confirm]').click(); await tick(); f.calls[1].resolve({preview:true,added:1,existing:0,skipped:0}); await tick();
  assert.equal(f.get('[data-import-confirm]').hidden,false); assert.match(f.get('[data-import-result]').textContent,/尚未确认/);
});

}
