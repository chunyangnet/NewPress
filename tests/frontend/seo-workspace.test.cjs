const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const { loadModule } = require('./helpers/typescript.cjs');
function setup(hash='',editing='false') {
 const panels=['redirects','link-check'].map(id=>({id,hidden:false,closest(){return this},value:'preserved'}));
 const links=panels.map(p=>({dataset:{seoTask:p.id},setAttribute(k,v){this[k]=v},removeAttribute(k){delete this[k]}}));
 const root={dataset:{seoEditing:editing},querySelectorAll(s){return s.includes('panel')?panels:links},addEventListener(){}};
 const events={},location={hash};
 loadModule('frontend/core/admin-workspaces.ts',{document:{querySelector(){return root},getElementById(id){return panels.find(p=>p.id===id)}},location,window:{addEventListener(k,fn){events[k]=fn}}}).initializeSEO();
 return {panels,links,events,location};
}
test('SEO defaults to rules and handles unknown anchors',()=>{for(const hash of ['', '#unknown']){const s=setup(hash);assert.deepEqual(s.panels.map(p=>p.hidden),[false,true]);assert.equal(s.links[0]['aria-current'],'page')}});
test('SEO direct check anchor and history preserve panel contents',()=>{const s=setup('#link-check');assert.deepEqual(s.panels.map(p=>p.hidden),[true,false]);s.location.hash='#redirects';s.events.hashchange();assert.deepEqual(s.panels.map(p=>p.hidden),[false,true]);assert.equal(s.links[1]['aria-current'],undefined);assert.equal(s.panels[1].value,'preserved')});
test('SEO edit errors initially reveal rules but allow subsequent task switches',()=>{const s=setup('#link-check','true');assert.equal(s.panels[0].hidden,false);s.events.hashchange();assert.equal(s.panels[1].hidden,false)});
