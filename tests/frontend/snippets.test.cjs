const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const {webcrypto}=require('node:crypto');
const {compile,loadModule}=require('./helpers/typescript.cjs');
const {environment,Element}=require('./helpers/editor-tools.cjs');

test('snippet response validates exact numeric identities and sharing state',()=>{
  const {snippet,requestKey}=loadModule('frontend/core/snippets.ts',{document:{querySelector(){return null;}},crypto:webcrypto});
  const valid={id:1,version:3,current_version:2,title:'片段',visibility:'shared'};
  assert.equal(snippet(valid).version,3);
  for(const value of [null,{}, {...valid,id:'1'}, {...valid,version:NaN},{...valid,id:Number.MAX_SAFE_INTEGER+1},{...valid,visibility:'published'}])assert.throws(()=>snippet(value));
  const keys=new Set(Array.from({length:20},()=>requestKey()));assert.equal(keys.size,20);for(const key of keys)assert.match(key,/^[a-f0-9]{64}$/);
});

function picker(variant){
  const f=environment(),form=new Element('form'),source=new Element('textarea'),trigger=new Element('button'),dialog=new Element('dialog');
  source.value='draft';f.document.selectors.set('[data-content-editor]',form);f.document.selectors.set('[data-editor-snippets-dialog]',dialog);
  form.selectors.set('[data-content-source]',source);form.selectors.set('[data-editor-snippets-open]',trigger);
  const nodes={};for(const [name,tag]of Object.entries({status:'p',list:'div',prev:'button',next:'button',page:'span',close:'button'})){nodes[name]=new Element(tag);dialog.selectors.set(`[data-snippet-picker-${name}]`,nodes[name]);}
  const inserted=[];f.window.ChunnelBlockEditor={capture:()=>({value:source.value,start:5,end:5}),insert:(html,range)=>inserted.push({html,range})};
  const code=variant==='generated'?fs.readFileSync('internal/coreui/assets/js/newpress-editor-snippets.js','utf8'):compile('frontend/core/editor-snippets.ts');vm.runInNewContext(code,f.globals);
  return {...f,source,trigger,dialog,nodes,inserted,open(){trigger.click();},insert(){nodes.list.children[0].children[1].click();}};
}
const shared={id:5,version:3,current_version:3,title:'共享说明',visibility:'shared'};
for(const variant of ['TypeScript','generated']){
  test(`${variant}: snippet picker inserts only pinned shell and keeps captured selection`,async()=>{
    const f=picker(variant);f.open();await f.reply(0,{items:[shared],page:1,pages:1});f.insert();await f.reply(1,{...shared,content_html:'must not copy body'});
    assert.equal(f.inserted.length,1);assert.equal(f.inserted[0].range.value,'draft');assert.equal(f.inserted[0].html,'\n<aside data-np-snippet="5" data-np-snippet-version="3"></aside>\n');assert.equal(f.dialog.open,false);
  });
  test(`${variant}: withdrawal, changed version and changed editor refuse insertion`,async()=>{
    for(const state of [{...shared,visibility:'withdrawn'},{...shared,version:4},shared]){
      const f=picker(variant);f.open();await f.reply(0,{items:[shared],page:1,pages:1});f.insert();if(state===shared)f.source.value='new edit';await f.reply(1,state);assert.equal(f.inserted.length,0);assert.equal(f.dialog.open,true);assert.match(f.nodes.status.textContent,/变化/);
    }
  });
  test(`${variant}: closing invalidates in-flight responses and private objects are not selectable`,async()=>{
    const f=picker(variant);f.open();f.dialog.close();f.open();await f.reply(0,{items:[shared],page:1,pages:1});assert.equal(f.nodes.list.children.length,0);await f.reply(1,{items:[{...shared,visibility:'private'}],page:1,pages:1});assert.equal(f.nodes.list.children.length,0);assert.equal(f.requests[0].init.signal.aborted,true);
  });
}
