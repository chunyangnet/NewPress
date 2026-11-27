const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

function walk(dir) {
  return fs.readdirSync(dir,{withFileTypes:true}).flatMap(e => e.isDirectory() ? walk(path.join(dir,e.name)) : [path.join(dir,e.name)]);
}
test('bundled templates and CSS do not add remote script or font CDNs', () => {
  for (const root of ['internal/coreui/templates','themes']) {
    for (const file of walk(root).filter(p=>/\.(html|css)$/.test(p))) {
      const source=fs.readFileSync(file,'utf8');
      assert.doesNotMatch(source, /<script\b[^>]*\bsrc\s*=\s*["'](?:https?:)?\/\//i, file);
      assert.doesNotMatch(source, /(?:fonts\.googleapis\.com|fonts\.gstatic\.com|use\.typekit\.net|cdnjs\.cloudflare\.com|cdn\.jsdelivr\.net)/i,file);
    }
  }
});
test('CDN worker retains revision but uses page origin', () => {
  const scriptURL='https://static.example.test/core-assets/1234567890abcdef12345678/js/newpress-components.js';
  const workerURL=new URL('newpress-highlight-worker.js',scriptURL);
  const local=new URL(workerURL.pathname,'https://site.example.test');
  assert.equal(local.href,'https://site.example.test/core-assets/1234567890abcdef12345678/js/newpress-highlight-worker.js');
  assert.match(fs.readFileSync('internal/coreui/assets/js/newpress-components.source.js','utf8'),/new Worker\(new URL\(workerURL.pathname, window.location.origin\)\)/);
});
test('installation entry retains integrity, TLS and explicit registry selection', () => {
 const script=fs.readFileSync('scripts/install_frontend.mjs','utf8');
 for(const flag of ['--ignore-scripts','--strict-ssl=true','--replace-registry-host=always']) assert.ok(script.includes(flag));
 const lock=JSON.parse(fs.readFileSync('package-lock.json','utf8'));
 for(const [name,pkg] of Object.entries(lock.packages)) {
  if(!name) continue;
  assert.match(pkg.integrity,/^sha(256|384|512)-/);
  assert.ok(['registry.npmjs.org','registry.npmmirror.com'].includes(new URL(pkg.resolved).hostname));
 }
});
