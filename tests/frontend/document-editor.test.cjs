const test = require('node:test');
const assert = require('node:assert/strict');
const {loadModule} = require('./helpers/typescript.cjs');
const {documentSchema, nodeHTML, safeLink, safeImage, pastedImageURL, createDocumentCodec} = loadModule('frontend/core/editor-document.ts', {TextEncoder, URL});
const schema = documentSchema();

test('document HTML escapes text and attributes while retaining inline marks', () => {
  const link = schema.marks.link.create({href:'https://example.test/?q=&x=1',title:'"quote"'});
  const text = schema.text('<字 & text>', [link,schema.marks.strong.create()]);
  const paragraph = schema.nodes.paragraph.create(null,text);
  assert.equal(nodeHTML(paragraph),'<p><a href="https://example.test/?q=&amp;x=1" title="&quot;quote&quot;"><strong>&lt;字 &amp; text&gt;</strong></a></p>');
  const image = schema.nodes.image.create({src:'/media/test.png',alt:'A "caption"'},null,[link]);
  assert.match(nodeHTML(image),/^<a .*<img .*&quot;caption&quot;.*<\/a>$/);
  assert.equal(nodeHTML(schema.nodes.hard_break.create(null,null,[schema.marks.strong.create()])), '<strong><br></strong>');
});
test('document links and media reject active protocols, remote images and traversal', () => {
  for(const value of ['javascript:alert(1)','data:text/html,x','//outside.test','https://a.test/\n','/\\evil','https://a.test/"']) assert.equal(safeLink(value),false,value);
  for(const value of ['https://example.test/a','mailto:a@example.test','/posts/a/','#toc']) assert.equal(safeLink(value),true,value);
  for(const value of ['https://example.test/a.png','/media/../private.png','/media/a.svg?x=1','//media/a.png']) assert.equal(safeImage(value),false,value);
  assert.equal(safeImage('/media/image.png'),true);
});
test('raw HTML is preserved as data without ever constructing an active DOM', () => {
  const codec=createDocumentCodec(schema,{createElement(){throw Error('must not parse executable markup');}});
  for(const raw of ['<script>alert(1)</script>','<p onclick="alert(1)">test</p>','<svg><script>alert(1)</script></svg>']){
    const doc=codec.parse(raw);assert.equal(doc.firstChild.type.name,'raw_block');assert.equal(codec.serialize(doc),raw);
  }
});

test('HTML image paste normalizes same-origin media without accepting foreign origins or normalized traversal', () => {
  const page='https://newpress.test/admin/content/1/edit/';
  for(const [input,expected] of [
    ['/media/7/image.png','/media/7/image.png'],
    ['https://newpress.test/media/7/image.png','/media/7/image.png'],
    ['https://NEWPRESS.test:443/theme-assets/theme/cover.webp','/theme-assets/theme/cover.webp'],
  ]) assert.equal(pastedImageURL(input,page),expected,input);
  for(const input of ['https://outside.test/media/7/image.png','http://newpress.test/media/7/image.png',
    'https://newpress.test:8443/media/7/image.png','https://owner@newpress.test/media/7/image.png',
    '//newpress.test/media/7/image.png','https://newpress.test/admin/private.png',
    'https://newpress.test/media/../media/image.png','https://newpress.test/media/%2e%2e/media/image.png',
    'https://newpress.test/media/image.png?token=private','https://newpress.test/media/image.png#fragment',
    'data:image/png;base64,AA==','javascript:alert(1)','https://newpress.test/media/\\image.png']) {
    assert.equal(pastedImageURL(input,page),null,input);
  }
});
test('document limits cover UTF-8 bytes, top-level blocks and table dimensions', () => {
  const codec=createDocumentCodec(schema,{});
  const doc=schema.nodes.doc.create(null,Array.from({length:201},()=>schema.nodes.paragraph.create()));
  assert.match(codec.validate(doc),/200/);
  const tooLarge=schema.nodes.doc.create(null,schema.nodes.paragraph.create(null,schema.text('汉'.repeat(700000))));
  assert.match(codec.validate(tooLarge),/2 MiB/);
  const row=schema.nodes.table_row.create(null,Array.from({length:11},()=>schema.nodes.table_cell.createAndFill()));
  const table=schema.nodes.doc.create(null,schema.nodes.table.create(null,row));
  assert.match(codec.validate(table),/10 列/);
});
