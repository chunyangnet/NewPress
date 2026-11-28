const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const net = require('node:net');
const path = require('node:path');
const {spawn} = require('node:child_process');
const {pathToFileURL} = require('node:url');
const {loadNativeModule} = require('../frontend/helpers/typescript.cjs');
const {createMockupStream, parseMockupControl} = loadNativeModule('frontend/core/device-mockup-data.ts');
const delay = ms => new Promise(resolve=>setTimeout(resolve,ms));

test('actual worker stream validates all four PNGs and pause/resume/stop receipts', {timeout:60000}, async t => {
  const target=http.createServer((_request,response)=>{
    response.setHeader('Content-Type','text/html; charset=utf-8');
    // No viewport meta: the worker must still return the configured PNG size.
    response.end('<!doctype html><title>实际流协议</title><h1>Local device stream</h1><script>setInterval(()=>document.body.style.background=document.body.style.background==="gold"?"white":"gold",200)</script>');
  });
  await new Promise(resolve=>target.listen(0,'127.0.0.1',resolve));
  t.after(()=>new Promise(resolve=>{target.closeAllConnections();target.close(resolve);}));
  const portProbe=net.createServer();await new Promise(resolve=>portProbe.listen(0,'127.0.0.1',resolve));
  const port=portProbe.address().port;await new Promise(resolve=>portProbe.close(resolve));
  const script=path.resolve(__dirname,'../../scripts/device_mockup_server.mjs');
  const bootstrap=`process.argv[1]=${JSON.stringify(script)};process.on('message',value=>{if(value==='stop')process.emit('SIGTERM');});await import(${JSON.stringify(pathToFileURL(script).href)});`;
  const child=spawn(process.execPath,['--input-type=module','-e',bootstrap],{windowsHide:true,env:{...process.env,DEVICE_MOCKUP_PORT:String(port)},stdio:['ignore','pipe','pipe','ipc']});
  let logs='';child.stdout.on('data',value=>logs+=value);child.stderr.on('data',value=>logs+=value);
  const exited=new Promise(resolve=>child.once('exit',(code)=>resolve(code)));
  const controller=new AbortController();
  t.after(async()=>{
    controller.abort();
    if(child.connected)child.send('stop');
    const timeout=setTimeout(()=>child.kill(),10000);try{await exited;}finally{clearTimeout(timeout);}
  });
  const base=`http://127.0.0.1:${port}`;
  const waitFor=async predicate=>{
    const deadline=Date.now()+20000;
    while(!await predicate()){if(Date.now()>deadline)throw new Error('Worker did not reach expected state: '+logs);await delay(50);}
  };
  await waitFor(async()=>{try{return (await fetch(base+'/api/health')).ok;}catch{return false;}});
  const response=await fetch(base+'/api/live',{method:'POST',headers:{'Content-Type':'application/json',Origin:base},body:JSON.stringify({url:`http://127.0.0.1:${target.address().port}/`,waitMs:0}),signal:controller.signal});
  assert.equal(response.status,200);assert.match(response.headers.get('content-type'),/application\/x-ndjson/);
  const events=[],stream=createMockupStream(event=>events.push(event)),decoder=new TextDecoder(),reader=response.body.getReader();
  let readError, wire='';
  const reading=(async()=>{try{while(true){const {value,done}=await reader.read();if(done){stream.push(decoder.decode());stream.finish();return;}const chunk=decoder.decode(value,{stream:true});wire+=chunk;stream.push(chunk);}}catch(error){readError=error;}finally{reader.releaseLock();}})();
  await waitFor(()=>readError || events.some(event=>event.type==='ready'));
  if(readError)t.diagnostic(JSON.stringify(wire.split('\n').filter(Boolean).flatMap(line=>{try{const value=JSON.parse(line);if(value.type!=='frame')return[{type:value.type,id:value.id}];const png=Buffer.from(value.screenshot,'base64');return[{type:value.type,id:value.id,width:value.width,height:value.height,pngWidth:png.readUInt32BE(16),pngHeight:png.readUInt32BE(20)}];}catch{return[];}})));
  assert.equal(readError,undefined);
  assert.deepEqual(new Set(events.filter(event=>event.type==='frame').map(event=>event.id)),new Set(['desktop','laptop','tablet','phone']));
  assert.equal(events.find(event=>event.type==='ready').count,4);
  const id=events.find(event=>event.type==='session').id;
  const control=async action=>{
    const result=await fetch(base+'/api/live/control',{method:'POST',headers:{'Content-Type':'application/json',Origin:base},body:JSON.stringify({id,action})});
    assert.equal(result.status,200);return result.json();
  };
  assert.equal(parseMockupControl(await control('pause'),true),true);
  await waitFor(()=>events.some(event=>event.type==='state'&&event.paused));
  await delay(1300);
  const frameCount=events.filter(event=>event.type==='frame').length;
  await delay(1300);assert.equal(events.filter(event=>event.type==='frame').length,frameCount);
  assert.equal(parseMockupControl(await control('resume'),false),false);
  await waitFor(()=>events.filter(event=>event.type==='frame').length>frameCount);
  const stopped=await control('stop');assert.equal(stopped.stopped,true);
  await reading;assert.equal(readError,undefined);assert.equal(events.at(-1).type,'stopped');
  await waitFor(async()=>(await(await fetch(base+'/api/health')).json()).liveSessions===0);
  t.diagnostic(`${events.length} validated events, all viewports, zero live sessions after stop`);
});
