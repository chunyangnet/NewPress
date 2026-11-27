const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const {compile} = require('./helpers/typescript.cjs');
const variants = {TypeScript: compile('frontend/core/design.ts'), generated: fs.readFileSync('internal/coreui/assets/js/chunnel-design.js','utf8')};

for (const [variant, source] of Object.entries(variants)) test(variant + ': layout undo and redo preserve radio values and selection', () => {
  class Control {}
  const element = extra => ({handlers:{}, addEventListener(name, fn){this.handlers[name]=fn;}, closest(){return null;}, ...extra});
  const first = Object.assign(new Control(), element({name:'hero_visual', type:'radio', value:'cover', checked:true}));
  const second = Object.assign(new Control(), element({name:'hero_visual', type:'radio', value:'minimal', checked:false}));
  const form = element({elements:[first,second], querySelector(){return null;}, dispatchEvent(event){this.handlers[event.type]?.(event);}});
  form.elements.namedItem = name => form.elements.find(item => item.name === name) || null;
  const undo = element(), redo = element(), defaults = element();
  const nodes = {'[data-layout-form]':form, '[data-layout-undo]':undo, '[data-layout-redo]':redo, '[data-layout-defaults]':defaults};
  const queue=[];
  const flush=()=>{while(queue.length)queue.shift()();};
  vm.runInNewContext(source, {
    document:{querySelector:s=>nodes[s]||null,dispatchEvent(){}}, window:{}, Event,
    HTMLInputElement:Control, HTMLSelectElement:Control, HTMLTextAreaElement:Control,
    queueMicrotask:fn=>queue.push(fn),
  });
  first.checked=false; second.checked=true;
  form.dispatchEvent(new Event('change')); flush();
  undo.handlers.click(); flush();
  assert.equal(first.checked,true); assert.equal(second.checked,false);
  assert.equal(first.value,'cover'); assert.equal(second.value,'minimal');
  redo.handlers.click(); flush();
  assert.equal(first.checked,false); assert.equal(second.checked,true);
  assert.equal(first.value,'cover'); assert.equal(second.value,'minimal');
});
