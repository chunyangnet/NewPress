'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(process.env.CHUNNEL_EMOJI_EDITOR_SOURCE || path.join(__dirname, '../../themes/chunnel/assets/js/chunnel-emoji-editor.js'), 'utf8');
const start = source.indexOf("    editor.addEventListener('keydown', (event) => {");
const end = source.indexOf("    editor.addEventListener('paste',", start);
assert.ok(start >= 0 && end > start);

function dispatch(options = {}, composing = false, preventForwarded = false) {
  let handler;
  const undos = [], forwarded = [];
  const context = {
    composing, lastEdit:{kind:'typing'},
    editor:{addEventListener(_type, callback) { handler = callback; }},
    input:{dispatchEvent(event) { forwarded.push(event); if (preventForwarded) event.defaultPrevented = true; }},
    undo:direction => undos.push(direction),
    KeyboardEvent:class {constructor(type, values) {Object.assign(this, values); this.type = type; this.defaultPrevented = false;}},
  };
  vm.runInNewContext(source.slice(start, end), context);
  const event = {key:'Enter', keyCode:13, isComposing:false, ctrlKey:false, metaKey:false, altKey:false, shiftKey:false,
    defaultPrevented:false, stopped:false, preventDefault() {this.defaultPrevented=true;}, stopPropagation() {this.stopped=true;}, ...options};
  handler(event);
  return {event, undos, forwarded};
}

for (const [name, flags, composing] of [
  ['tracked composition', {}, true],
  ['native composition flag', {isComposing:true}, false],
  ['legacy IME key code', {keyCode:229}, false],
]) {
  for (const [key, modifiers] of [['Escape', {}], ['Enter', {}], ['z', {ctrlKey:true}], ['y', {metaKey:true}]]) {
    test(`emoji editor composition: ${name} keeps ${key} with the input method`, () => {
      const result = dispatch({key, ...modifiers, ...flags}, composing);
      assert.deepEqual(result.undos, []);
      assert.deepEqual(result.forwarded, [], 'candidate keys must not reach the hidden form control');
      assert.equal(result.event.defaultPrevented, false, 'input method retains its native default action');
      assert.equal(result.event.stopped, true, 'candidate keys must not reach page shortcuts');
    });
  }
}

test('emoji editor composition: ordinary undo and redo keep their history behavior', () => {
  for (const [flags, direction] of [
    [{key:'z',ctrlKey:true},-1], [{key:'Z',metaKey:true},-1],
    [{key:'z',ctrlKey:true,shiftKey:true},1], [{key:'y',ctrlKey:true},1],
  ]) {
    const result = dispatch(flags);
    assert.deepEqual(result.undos, [direction]);
    assert.equal(result.event.defaultPrevented, true);
    assert.equal(result.forwarded.length, 0);
  }
});

test('emoji editor composition: committed Enter still reaches the form and honors submission handling', () => {
  for (const prevented of [false,true]) {
    const result = dispatch({key:'Enter',code:'Enter'}, false, prevented);
    assert.equal(result.forwarded.length, 1);
    assert.equal(result.forwarded[0].key, 'Enter');
    assert.equal(result.forwarded[0].isComposing, false);
    assert.equal(result.event.defaultPrevented, prevented);
    assert.equal(result.event.stopped, false);
  }
});

test('emoji editor composition: ordinary navigation and modified keys remain available', () => {
  for (const flags of [{key:'ArrowDown'}, {key:'Escape'}, {key:'z',ctrlKey:true,altKey:true}, {key:'Tab',shiftKey:true}]) {
    const result = dispatch(flags);
    assert.deepEqual(result.undos, []); assert.equal(result.forwarded.length, 1);
    assert.equal(result.event.defaultPrevented, false); assert.equal(result.event.stopped, false);
  }
});
