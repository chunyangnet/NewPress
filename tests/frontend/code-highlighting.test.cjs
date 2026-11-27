const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const hljs = require('highlight.js');
const composer = require('../../internal/coreui/assets/js/chunnel-composer.js');
const source = fs.readFileSync(path.resolve(__dirname, '../../internal/coreui/assets/js/newpress-highlight-worker.js'), 'utf8');
let response;
const context = vm.createContext({self: {postMessage: data => { response = data; }}, console});
vm.runInContext(source, context);
const highlight = (text, language) => {
  response = undefined;
  context.self.onmessage({data: {id: 7, text, language}});
  assert.equal(response.id, 7);
  return response;
};

test('the shipped worker supports every installed grammar and its registered aliases', () => {
  assert.ok(hljs.listLanguages().length >= 190);
  for (const language of hljs.listLanguages()) {
    for (const name of [language, ...(hljs.getLanguage(language).aliases || [])]) {
      const result = highlight('example', name);
      assert.equal(result.error, undefined, name);
      const grammar = hljs.getLanguage(name);
      assert.equal(result.label, grammar === hljs.getLanguage('plaintext') ? '纯文本' : grammar.name || name, name);
    }
  }
});

test('common language aliases produce highlighted tokens and readable language labels', () => {
  for (const [language, code, label] of [
    ['py', 'def hello():\n    return True', 'Python'],
    ['C#', 'public class Demo { public int Count = 1; }', 'C#'],
    ['c++', '#include <iostream>\nint main() { return 0; }', 'C++'],
    ['tsx', 'const count: number = 1;', 'TypeScript'],
    ['ps1', 'Write-Host "Hello"', 'PowerShell'],
    ['kotlin', 'fun main() { println("Hello") }', 'Kotlin'],
    ['swift', 'let message = "Hello"', 'Swift'],
    ['docker', 'FROM node:22\nRUN npm ci', 'Dockerfile'],
  ]) {
    const result = highlight(code, language);
    assert.equal(result.label, label);
    assert.match(result.html, /class="hljs-/);
  }
});

test('auto detection reports the language and unknown labels keep escaped plain text', () => {
  const code = 'def hello(name):\n    print("Hello", name)\n    return True';
  for (const language of ['', 'auto', undefined]) assert.equal(highlight(code, language).label, 'Python');
  for (const language of ['plaintext', 'made-up-language']) {
    const result = highlight('<script>alert("x")</script>', language);
    assert.doesNotMatch(result.html, /<script>|hljs-/);
    assert.match(result.html, /&lt;script&gt;/);
    assert.equal(result.label, language === 'plaintext' ? '纯文本' : language);
  }
  assert.equal(highlight('x'.repeat(48001), 'javascript').error, true);
});

test('structured editor accepts language aliases without permitting extra classes or attributes', () => {
  for (const language of ['C#', 'c++', 'Objective-C', 'vb.net', 'tsx', 'plaintext']) {
    assert.ok(composer.renderBlock({kind: 'code', text: 'hello', language}).includes(`language-${language}`));
  }
  for (const language of ['python bad', 'go" onclick="x', '<script>', 'x'.repeat(41)]) {
    assert.throws(() => composer.renderBlock({kind: 'code', text: 'hello', language}));
  }
});
