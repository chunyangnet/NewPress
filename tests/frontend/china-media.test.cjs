const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const {stripTypeScriptTypes}=require('node:module');
test('external media retry replaces frames and cancellation cleans timers',()=>{
 const source=stripTypeScriptTypes(fs.readFileSync('frontend/core/components.ts','utf8'));
 const body=source.slice(source.indexOf('  function player('),source.indexOf('  let worker'));
 class Element {
  constructor(tag){this.tag=tag;this.children=[];this.listeners={};this.dataset={};}
  append(...children){for(const c of children){c.parent=this;this.children.push(c);}}
  remove(){if(this.parent)this.parent.children=this.parent.children.filter(x=>x!==this);}
  addEventListener(name,fn){this.listeners[name]=fn;}
  setAttribute(){}
 }
 const root=new Element('aside'),mount=new Element('div'),button=new Element('button');
 root.querySelector=()=>mount;
 const configs=new Map([[root,{provider:'bilibili',uid:'BV1234567890',title:'test'}]]),disposers=new Set(),timers=new Map();
 let seq=0,message='';
 const ctx={configs,configFor:root=>configs.get(root),required:(root,selector)=>root.querySelector(selector),disposers,node:tag=>new Element(tag),announce:(_,text)=>message=text,setTimeout:fn=>{timers.set(++seq,fn);return seq;},clearTimeout:id=>timers.delete(id)};
 vm.runInNewContext(body+'\nthis.player=player;',ctx);
 ctx.player(root,button);
 assert.equal(mount.children.filter(x=>x.tag==='iframe').length,1);
 assert.equal(button.hidden,true);
 const first=mount.children[0];
 mount.children[1].children[0].listeners.click();
 assert.notEqual(mount.children[0],first);
 assert.equal(mount.children.length,2);
 assert.equal(timers.size,1);
 [...timers.values()][0]();assert.match(message,/若播放器仍未显示/);
 mount.children[1].children[1].listeners.click();
 assert.equal(mount.children.length,0);assert.equal(timers.size,0);assert.equal(disposers.size,0);assert.equal(button.hidden,false);
 ctx.player(root,button);for(const dispose of disposers)dispose();assert.equal(mount.children.length,0);
});
