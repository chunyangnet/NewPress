const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(__dirname, '../..');
const template = fs.readFileSync(path.join(root, 'internal/coreui/templates/admin-editor.html'), 'utf8');
const script = require('node:module').stripTypeScriptTypes(fs.readFileSync(path.join(root, 'frontend/core/editor-workspace.ts'), 'utf8'));
test('writing canvas leads with title and body, supporting tools follow below', () => {
 const canvas = template.split('<div class="admin-editor-canvas">')[1].split('<aside class="admin-editor-sidebar"')[0];
 assert.match(canvas, /name="title"/); assert.match(canvas, /name="content_source"/);
 assert.doesNotMatch(canvas.split('<section class="editor-below"')[0], /name="excerpt"|name="slug"|template "seo-editor"/);
 assert.match(canvas.split('<section class="editor-below"')[1], /template "seo-editor"/);
 const sidebar = template.split('<aside class="admin-editor-sidebar"')[1].split('</aside>')[0];
 for (const name of ['excerpt','slug','status','tags','cover_url']) assert.ok(sidebar.includes(`name="${name}"`), name);
 assert.ok(template.indexOf('data-editor-savebar') < template.indexOf('admin-editor-layout'));
});
test('advanced insertion controls have one entry and native buttons retain submit actions', () => {
 for (const hook of ['data-component-library-open','data-editor-widget-open','data-editor-reuse-open']) assert.equal(template.split(hook).length-1,1,hook);
 assert.match(template, /name="action" value="save"/);
 assert.match(template, /name="action" value="trash"/);
});
test('workspace preserves invalid-field access and confirms publication via existing submit path', () => {
 assert.match(script, /addEventListener\('invalid', event => reveal\(event.target\), true\)/);
 assert.match(script, /form.requestSubmit\(save\)/);
 assert.match(script, /event.stopImmediatePropagation\(\)/);
 assert.match(script, /action !== 'save'/);
 assert.doesNotMatch(template, /name="action" value="publish"/);
});

const {needsPublicationReview} = require('./helpers/typescript.cjs').loadNativeModule('frontend/core/editor-workspace.ts');
test('ordinary updates skip review, visibility and schedule changes require review', () => {
 assert.equal(needsPublicationReview('publish','publish','',''),false);
 assert.equal(needsPublicationReview('draft','draft','',''),false);
 assert.equal(needsPublicationReview('draft','publish','',''),true);
 assert.equal(needsPublicationReview('publish','draft','',''),true);
 assert.equal(needsPublicationReview('publish','private','',''),true);
 assert.equal(needsPublicationReview('future','future','2026-12-01T12:00','2026-12-01T12:00'),false);
 assert.equal(needsPublicationReview('future','future','2026-12-01T12:00','2026-12-02T12:00'),true);
});
