const test=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const {buildSync}=require('esbuild');
for(const minify of [false,true]){
  const path=minify?'minified TS helpers':'TS helpers',module={exports:{}};
  const code=buildSync({entryPoints:['frontend/shared/registration-code-helpers.ts'],bundle:true,write:false,platform:'browser',format:'cjs',target:'es2022',minify}).outputFiles[0].text;
  vm.runInNewContext(code,{module,exports:module.exports});
  const api=Object.fromEntries(Object.entries(module.exports).filter(([,value])=>typeof value==='function').map(([name,fn])=>[name,(...args)=>structuredClone(fn(...args))]));
  test(`${path}: imports text, exported CSV and JSON without silently dropping duplicates`,()=>{
    assert.deepEqual(api.parseImport('VIP-001\nVIP-002,VIP-003；VIP-004'),['VIP-001','VIP-002','VIP-003','VIP-004']);
    assert.deepEqual(api.parseImport('\uFEFF"备注","注册码"\r\n"含,逗号","VIP-001"\r\n"多\n行","VIP-002"'),['VIP-001','VIP-002']);
    assert.deepEqual(api.parseImport('["VIP-001",{"code":"VIP-002"}]'),['VIP-001','VIP-002']);
    assert.deepEqual(api.parseImport('{"items":[{"code":"VIP-003","used":10}]}'),['VIP-003']);
    for(const input of ['DUPL\nDUPL','<script>','[null]','"code\nVIP-001','{"other":[]}','"code"x\nVIP-001',Array.from({length:501},(_,i)=>'CODE-'+i).join('\n')])assert.throws(()=>api.parseImport(input));
  });
  test(`${path}: a code named code stays a code unless CSV is explicitly selected`,()=>{
    assert.deepEqual(api.parseImport('code'),['code']);
    assert.deepEqual(api.parseImport('code\nVIP-001'),['code','VIP-001']);
    assert.deepEqual(api.parseImport('code,VIP-001'),['code','VIP-001']);
    assert.deepEqual(api.parseImport('CODE\nVIP-001','csv'),['VIP-001']);
    assert.deepEqual(api.parseImport('code,notes\nVIP-001,备注','csv'),['VIP-001']);
    assert.throws(()=>api.parseImport('VIP-001\nVIP-002','csv'),/列名/);
    assert.throws(()=>api.parseImport('code,code\nVIP-001,VIP-002','csv'),/一个注册码列/);
  });
  test(`${path}: unlimited uses requires an explicit zero`,()=>{
    assert.equal(api.maxUses('0'),0);assert.equal(api.maxUses('25'),25);
    for(const value of ['', '  ', '-1', '0.5', '1000001', 'text'])assert.throws(()=>api.maxUses(value));
  });
  test(`${path}: CSV safely quotes formulas and roundtrips code values`,()=>{
    const item={code:'SAFE-1234',enabled:true,used:0,max_uses:1,note:'=HYPERLINK("https://test")',created_at:'2026-11-17T00:00:00Z'};
    const csv=api.exportCSV([item]);assert.match(csv,/'=HYPERLINK/);assert.deepEqual(api.parseImport(csv),[item.code]);
    const minusCode={...item,code:'-MY-CODE'};assert.deepEqual(api.parseImport(api.exportCSV([minusCode])),[minusCode.code]);
    assert.equal(api.csvCell(' +SUM(1,1)'),`"' +SUM(1,1)"`);
    assert.equal(api.csvCell('normal "quote"'),`"normal ""quote"""`);
  });
  test(`${path}: exact time and usage boundaries match registration rules`,()=>{
    const now=Date.parse('2026-11-17T12:00:00Z'),base={enabled:true,used:1,max_uses:2};
    assert.equal(api.statusOf(base,now),'active');assert.equal(api.statusOf({...base,used:2},now),'exhausted');assert.equal(api.statusOf({...base,max_uses:0},now),'active');
    assert.equal(api.statusOf({...base,starts_at:'2026-11-17T12:00:00Z'},now),'active');
    assert.equal(api.statusOf({...base,starts_at:'2026-11-17T12:00:01Z'},now),'scheduled');
    assert.equal(api.statusOf({...base,expires_at:'2026-11-17T12:00:00Z'},now),'expired');
    assert.equal(api.statusOf({...base,enabled:false},now),'disabled');
  });
  test(`${path}: format preview includes prefix and grouping without counting them as random length`,()=>{
    const value=api.formatPreview({prefix:'NP-',length:16,group:4,charset:'digits'});assert.match(value,/^NP-\d{4}-\d{4}-\d{4}-\d{4}$/);assert.equal(value.length,22);
  });
}
