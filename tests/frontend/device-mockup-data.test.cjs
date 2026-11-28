const test = require('node:test');
const assert = require('node:assert/strict');
const {loadNativeModule} = require('./helpers/typescript.cjs');
const data = loadNativeModule('frontend/core/device-mockup-data.ts');
const session = {type:'session',id:'12345678-1234-1234-1234-123456789abc',interval:1000};
function frame(id = 'phone') {
  const device = data.mockupDevices.find(item => item.id === id), bytes = Buffer.alloc(36);
  bytes.writeUInt32BE(0x89504e47,0);bytes.writeUInt32BE(0x0d0a1a0a,4);bytes.writeUInt32BE(13,8);bytes.write('IHDR',12);
  bytes.writeUInt32BE(device.width,16);bytes.writeUInt32BE(device.height,20);
  return {type:'frame',id,width:device.width,height:device.height,screenshot:bytes.toString('base64'),capturedAt:1795810000000};
}
test('device stream accepts every split boundary and CRLF without duplicating events', () => {
  const wire = JSON.stringify(session) + '\r\n' + JSON.stringify(frame()) + '\n';
  for (let split = 0; split <= wire.length; split++) {
    const events=[],stream=data.createMockupStream(event=>events.push(event));
    stream.push(wire.slice(0,split));stream.push(wire.slice(split));stream.finish();
    assert.equal(events.length,2);assert.equal(events[1].id,'phone');
  }
});
test('device stream rejects an oversized partial line before JSON parsing',()=>{
  const stream=data.createMockupStream(()=>assert.fail('unexpected event'));
  stream.push(' '.repeat(data.maxMockupLineLength));
  assert.throws(()=>stream.push('x'),/过大/);
});
test('device stream rejects incomplete tails, malformed JSON and unknown events',()=>{
  const stream=data.createMockupStream(()=>{});stream.push('{"type":');assert.throws(()=>stream.finish(),/完整/);
  assert.throws(()=>data.createMockupStream(()=>{}).push('oops\n'));
  assert.throws(()=>data.parseMockupEvent({type:'future'}),/无效/);
});
for(const device of data.mockupDevices) test('frame validates actual PNG header and viewport '+device.id,()=>{
  const value=frame(device.id);assert.equal(data.parseMockupEvent(value).id,device.id);
  assert.throws(()=>data.parseMockupEvent({...value,width:value.width+1}),/无效/);
  const bytes=Buffer.from(value.screenshot,'base64');bytes.writeUInt32BE(1,16);
  assert.throws(()=>data.parseMockupEvent({...value,screenshot:bytes.toString('base64')}),/无效/);
  assert.throws(()=>data.parseMockupEvent({...value,screenshot:'data:image/svg+xml;base64,'+value.screenshot}),/无效/);
  assert.throws(()=>data.parseMockupEvent({...value,capturedAt:NaN}),/无效/);
});
test('mobile viewport PNG rounding permits only one actual pixel of difference',()=>{
  const value=frame('tablet'),bytes=Buffer.from(value.screenshot,'base64');bytes.writeUInt32BE(1023,20);
  assert.equal(data.parseMockupEvent({...value,screenshot:bytes.toString('base64')}).id,'tablet');
  bytes.writeUInt32BE(1022,20);assert.throws(()=>data.parseMockupEvent({...value,screenshot:bytes.toString('base64')}),/无效/);
});
test('device-ready validates arrays, known IDs, response status and redirect URL',()=>{
  const value={type:'device-ready',id:'phone',width:390,height:844,title:'试验页面',finalUrl:'https://example.test/',status:200,warnings:['图片未加载'],blockedRequests:1};
  assert.deepEqual(data.parseMockupEvent(value).warnings,['图片未加载']);
  for(const patch of [{id:'unknown'},{status:'200'},{finalUrl:'javascript:alert(1)'},{warnings:'bad'},{blockedRequests:-1}]) assert.throws(()=>data.parseMockupEvent({...value,...patch}));
  assert.equal(data.parseMockupEvent({...value,status:null}).status,null);
});
test('service health and control never coerce strings or accept a stopped/wrong state',()=>{
  assert.equal(data.parseMockupHealth({ready:true,service:'device-mockup'}),true);
  for(const value of [{ready:'false',service:'device-mockup'},{ready:true,service:'another'}])assert.throws(()=>data.parseMockupHealth(value));
  assert.equal(data.parseMockupControl({paused:true,stopped:false},true),true);
  for(const value of [{paused:'true',stopped:false},{paused:false,stopped:false},{paused:true,stopped:true}])assert.throws(()=>data.parseMockupControl(value,true));
});
test('device session, ready and pause events require their actual protocol fields',()=>{
  assert.equal(data.parseMockupEvent(session).id,session.id);
  for(const value of [{...session,id:'x'},{...session,interval:0},{type:'ready',count:5},{type:'heartbeat',paused:'false'},{type:'device-error',id:'other',error:'bad'}])assert.throws(()=>data.parseMockupEvent(value));
  assert.equal(data.parseMockupEvent({type:'stopped'}).type,'stopped');
});
test('preview address normalization preserves localhost but rejects credentials and non-HTTP schemes',()=>{
  assert.equal(data.normalizedMockupURL('localhost:3000/page'),'http://localhost:3000/page');
  assert.equal(data.normalizedMockupURL('example.test'),'https://example.test/');
  for(const url of ['','ftp://example.test','https://user:pass@example.test','x'.repeat(2049)])assert.equal(data.normalizedMockupURL(url),null);
});
