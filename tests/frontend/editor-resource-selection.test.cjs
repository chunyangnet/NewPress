const test = require('node:test');
const assert = require('node:assert/strict');
const { EditorState, TextSelection, NodeSelection } = require('prosemirror-state');
const { history, undo } = require('prosemirror-history');
const { loadModule } = require('./helpers/typescript.cjs');
const { documentSchema, nodeHTML } = loadModule('frontend/core/editor-document.ts', { TextEncoder, URL });
const { resourceSelection } = loadModule('frontend/core/editor-resource-selection.ts');
const schema = documentSchema(), paragraph = text => schema.nodes.paragraph.create(null, schema.text(text));
const marker = '[resource:private]';
const codec = { blockHTML: nodeHTML, parse: () => schema.nodes.doc.create(null, paragraph(marker)), validate: () => '' };
function editor(doc, from, to = from) { return EditorState.create({ schema, doc, selection: TextSelection.create(doc, from, to), plugins: [history()] }); }

test('private resource extraction preserves selected inline marks and supports undo', () => {
  const body = schema.nodes.paragraph.create(null, [schema.text('before '), schema.text('secret', [schema.marks.strong.create()]), schema.text(' after')]);
  const doc = schema.nodes.doc.create(null, body); let state = editor(doc, 8, 14);
  const result = resourceSelection(state, marker, codec);
  assert.equal(result.html, '<p><strong>secret</strong></p>');
  state = state.apply(result.transaction); assert.equal(state.doc.textContent.includes('secret'), false);
  assert.equal(state.doc.textContent, 'before ' + marker + ' after');
  assert.equal(undo(state, transaction => { state = state.apply(transaction); }), true); assert.equal(state.doc.eq(doc), true);
});
test('hide-tail includes the rest of the document but preserves text before the caret', () => {
  const doc = schema.nodes.doc.create(null, [paragraph('keep secret'), paragraph('last')]);
  const state = editor(doc, 6), result = resourceSelection(state, marker, codec, true);
  assert.equal(result.html, '<p>secret</p><p>last</p>');
  assert.equal(state.apply(result.transaction).doc.textContent, 'keep ' + marker);
});
test('resource extraction keeps opaque blocks intact and rejects invalid transactions', () => {
  const raw = '<div data-private="true">private markup</div>', doc = schema.nodes.doc.create(null, [schema.nodes.raw_block.create({ html: raw }), paragraph('after')]);
  const state = EditorState.create({ schema, doc, selection: NodeSelection.create(doc, 0) });
  const result = resourceSelection(state, marker, codec); assert.equal(result.html, raw);
  assert.throws(() => resourceSelection(state, marker, { ...codec, validate: () => '文档超过限制' }), /文档超过限制/);
  assert.equal(state.doc.eq(doc), true); assert.throws(() => resourceSelection(editor(doc, 2), marker, codec), /选择/);
});
