const assert = require('node:assert/strict');
const test = require('node:test');
const path = require('node:path');
const {buildSync} = require('esbuild');

// Inspect the actual dependency graph: an inert composer can still cost a
// download. This route must retain guest actions without shipping editors.
test('discovery entry includes preferences and navigation without comment or community writers', () => {
  const root = path.resolve(__dirname, '../..');
  const result = buildSync({absWorkingDir:root,entryPoints:['frontend/shared/explore.ts'],bundle:true,write:false,metafile:true,platform:'browser',format:'iife'});
  const inputs = Object.keys(result.metafile.inputs);
  for (const name of ['experience-context','guest-preferences','discovery']) assert.ok(inputs.includes(`frontend/shared/${name}.ts`),name);
  for (const name of ['comments','comment-submit','community-list','community-composer','community-submit']) assert.ok(!inputs.includes(`frontend/shared/${name}.ts`),name);
  assert.ok(!/import\(/.test(result.outputFiles[0].text),'no deferred editor download');
});
