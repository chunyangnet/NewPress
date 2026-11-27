const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const read = p => fs.readFileSync(path.join(__dirname, '../..', p), 'utf8').replace(/\r\n/g, '\n');
const publicTemplate = read('internal/coreui/templates/flash.html');
const admin = read('internal/coreui/templates/admin-flash.html');
test('public flash theme stays in sync and retains interaction hooks', () => {
  assert.equal(publicTemplate, read('themes/chunnel/templates/flash.html'));
  for (const hook of ['data-flash-id', 'data-flash-engagement="like"', 'data-flash-engagement="bookmark"', 'data-flash-share', 'data-flash-status']) assert.ok(publicTemplate.includes(hook), hook);
  assert.match(publicTemplate, /name="filter" value="{{.Flash.Filter}}"/);
  assert.match(publicTemplate, /{{if .Comments}}<a href="{{.URL}}#comments"/);
});
test('admin list filters preserve query and publication links remain conditional', () => {
  assert.match(admin, /name="q"[^>]+value="{{.Flash.Query}}"/);
  assert.match(admin, /name="status" value="{{.Flash.Filter}}"/);
  for (const state of ['draft','pending','publish','future','trash']) assert.ok(admin.includes(`value="${state}"`));
  assert.match(admin, /{{if eq .Status "publish"}}<a href="{{.URL}}">查看快报/);
  assert.match(admin, /flash-manuscript-excerpt">{{.Body}}/);
  assert.doesNotMatch(admin, /safeHTML .Body/);
  for (const action of ['save','submit','publish','reject','unpublish','restore','trash']) assert.ok(admin.includes(`value="${action}"`));
});
