'use strict';
const nodeTest=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
for (const variant of ['TypeScript', 'generated']) {
const source = variant === 'TypeScript' ? require('./helpers/typescript.cjs').compile('frontend/shared/account-nav.ts') : fs.readFileSync('themes/chunnel/assets/js/chunnel-account-nav.js','utf8');
const test = (name, fn) => nodeTest(variant + ': ' + name, fn);
function fixture(paths,current){
  const links=paths.map(href=>({attrs:{href},classes:new Set(),getAttribute(k){return this.attrs[k];},setAttribute(k,v){this.attrs[k]=v;},removeAttribute(k){delete this.attrs[k];},classList:{toggle(k,v){const item=links.find(a=>a.classList===this);if(v)item.classes.add(k);else item.classes.delete(k);}}}));
  let refreshes=0;const events={},window={addEventListener:(k,v)=>{events[k]=v;}},location={href:'https://example.test'+current};
  const bar={querySelectorAll:()=>links,chunnelSelect:()=>{refreshes++;}};
  vm.runInNewContext(source,{document:{querySelector:()=>bar},window,location,URL,URLSearchParams,decodeURIComponent});
  return {window,events,links,refreshes:()=>refreshes,selected:()=>links.filter(n=>n.attrs['aria-current']==='page').map(n=>n.attrs.href),go(path){location.href='https://example.test'+path;window.ChunnelAccountNav();}};
}
for(const [name,current,paths,want] of [
  ['deepest route','/account/access/',['/account/','/account/access/','/','/explore/'],'/account/access/'],
  ['query view','/account/?view=bookmark',['/account/','/account/?view=bookmark','/account/?view=like','/'],'/account/?view=bookmark'],
  ['query order','/account/?sort=new&view=bookmark',['/account/','/account/?view=bookmark&sort=new','/account/?view=like','/'],'/account/?view=bookmark&sort=new'],
  ['query mismatch','/account/?view=like',['/account/','/account/?view=bookmark','/account/?view=feed','/'],'/account/'],
  ['anchor','/',['/#latest','/','/account/','/explore/'],'/'],
  ['external','/account/',['//outside.test/account/','https://outside.test/account/','/account/','/'],'/account/'],
  ['no match','/unrelated/',['/account/','/explore/','/series/','/'],''],
  ['escaped percent','/topics/100%25/',['/topics/','/topics/100%25/','/account/','/'],'/topics/100%25/'],
])test('account dock '+name,()=>{const f=fixture(paths,current);assert.deepEqual(f.selected(),want?[want]:[]);assert.equal(f.refreshes(),1);});
test('account tab replacement removes the previous state and updates the shared lens',()=>{
  const f=fixture(['/account/','/account/?view=bookmark','/account/?view=like','/'],'/account/?view=bookmark');
  f.go('/account/?view=like');assert.deepEqual(f.selected(),['/account/?view=like']);assert.equal(f.links[1].classes.has('is-current'),false);assert.equal(f.links[2].classes.has('is-current'),true);assert.equal(f.refreshes(),2);
  f.events.pageshow();assert.deepEqual(f.selected(),['/account/?view=like']);assert.equal(f.refreshes(),3);
});
test('disabled dock registers no location updater',()=>{
  const window={};vm.runInNewContext(source,{document:{querySelector:()=>null},window});assert.equal(window.ChunnelAccountNav,undefined);
});

}
