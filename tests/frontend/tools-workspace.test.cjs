const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { loadModule } = require('./helpers/typescript.cjs');
function setup({hash='', search='', pathname='/admin/tools/',restore='false'}={}) {
 const panels=['tools-downloads','tools-recovery','tools-diagnostics'].map(id=>({id,hidden:false,closest(){return this},scrollIntoView(){}}));
 const links=panels.map(p=>({dataset:{toolsTarget:p.id},setAttribute(k,v){this[k]=v},removeAttribute(k){delete this[k]}}));
 const tools={dataset:{toolsRestore:restore},querySelectorAll(s){return s.includes('panel')?panels:links},addEventListener(){}};
 const events={};const location={hash,search,pathname};
 const document={querySelector(){return tools},getElementById(id){return panels.find(p=>p.id===id)||(id==='audit-events'?panels[2]:id==='navigation-import'?panels[1]:null)}};
 loadModule('frontend/core/admin-workspaces.ts',{document,location,URLSearchParams,window:{addEventListener(k,fn){events[k]=fn}}}).initializeTools();
 return {panels,links,location,events};
}
test('tools defaults to downloads and preserves exactly one visible panel',()=>{const s=setup();assert.deepEqual(s.panels.map(p=>p.hidden),[false,true,true]);assert.equal(s.links[0]['aria-current'],'page')});
test('tools restores preview, notice and audit query context',()=>{for(const config of [{restore:'true'},{pathname:'/admin/tools/navigation-import/'}])assert.equal(setup(config).panels[1].hidden,false);for(const key of ['audit_cursor','audit_action','audit_object_type'])assert.equal(setup({search:`?${key}=example`}).panels[2].hidden,false)});
test('tools anchors and hash history reveal nested targets',()=>{const s=setup({hash:'#audit-events'});assert.equal(s.panels[2].hidden,false);s.location.hash='#navigation-import';s.events.hashchange();assert.equal(s.panels[1].hidden,false);assert.equal(s.links[2]['aria-current'],undefined);s.location.hash='';s.events.hashchange();assert.equal(s.panels[0].hidden,false)});
