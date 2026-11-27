const test = require('node:test');
const assert = require('node:assert/strict');
const {loadModule} = require('./helpers/typescript.cjs');
const {parseCustomBlocks,customBlocksError} = loadModule('frontend/core/layout-data.ts',{TextEncoder});
const block = () => ({key:'custom_a',title:'中文标题',html:'<div class="custom">&amp; 原文</div>',surface:true,visibility:'both'});

test('custom layout data preserves exact HTML, including astral text and invisible blocks',()=>{
  const input=[block(),{...block(),key:'custom_b',html:'\n<p>🙂</p>\r\n',visibility:'hidden',surface:false}];
  const decoded=parseCustomBlocks(JSON.stringify(input));
  assert.deepEqual(JSON.parse(JSON.stringify(decoded)),input);
  assert.equal(customBlocksError(decoded,JSON.stringify(input)),'');
  assert.equal(parseCustomBlocks('').length,0);
  assert.deepEqual(JSON.parse(JSON.stringify(parseCustomBlocks('[{"key":"custom","visibility":"mobile"}]'))),[{key:'custom',visibility:'mobile',title:'',html:'',surface:false}]);
});
test('malformed collections and ambiguous custom identities are rejected before restore',()=>{
  const invalid=['{','null','{}','[1]','[null]','[] []'];
  for(const raw of invalid)assert.throws(()=>parseCustomBlocks(raw));
  for(const patch of [{key:'custom_../../x'},{key:'__proto__'},{key:'custom_ABC'},{html:42},{title:{}},{surface:'true'},{visibility:'phone'},{extra:'keep?'}])assert.throws(()=>parseCustomBlocks(JSON.stringify([{...block(),...patch}])));
  assert.throws(()=>parseCustomBlocks(JSON.stringify([block(),block()])),/重复/);
  assert.throws(()=>parseCustomBlocks('[{"key":"custom_a","visibility":"both","__proto__":{"polluted":true}}]'),/未知字段/);
  assert.equal({}.polluted,undefined);
});
test('oversized drafts remain parseable for correction but cannot pass save validation',()=>{
  for(const value of [{...block(),title:'字'.repeat(81)},{...block(),html:'🙂'.repeat(4097)}]){
    const raw=JSON.stringify([value]), decoded=parseCustomBlocks(raw);
    assert.equal(decoded[0].html,value.html);assert.match(customBlocksError(decoded,raw),/80|4096/);
  }
  const values=Array.from({length:13},(_,index)=>({...block(),key:'custom_'+index})), raw=JSON.stringify(values);
  assert.equal(parseCustomBlocks(raw).length,13);assert.match(customBlocksError(values,raw),/12/);
  const large=JSON.stringify([{...block(),html:'文'.repeat(24000)}]);
  assert.match(customBlocksError(parseCustomBlocks(large),large),/64 KiB/);
});
