const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const editor = require('./helpers/editor-save.cjs');
const source = editor.source();

function fixture() {
  const controls = ['published_at', 'published_timezone', 'published_source', 'version', 'title'].map(name => ({name, value: '', tagName: 'INPUT'}));
  const fields = Object.fromEntries(controls.map(control => [control.name, control]));
  fields.published_timezone.value = 'Asia/Kathmandu'; fields.published_source.value = 'canonical'; fields.version.value = '1';
  const labels = [{textContent: 'Asia/Kathmandu'}];
  let confirm, changed = 0;
  const context = vm.createContext({
    ...editor.context,URLSearchParams, Event,
    editorTimezone: 'Asia/Kathmandu', timezoneNeedsReview: false, publishedZone: fields.published_timezone, publishedSource: fields.published_source,
    timezoneReview: {hidden: true, querySelector: () => ({addEventListener: (_, listener) => {confirm = listener;}})},
    editorForm: Object.assign(new EventTarget(), {querySelector: selector => fields[/name="(.*?)"/.exec(selector)?.[1]], querySelectorAll: selector => selector === 'input, textarea, select' ? controls : selector === '[data-published-timezone-label]' ? labels : []}),
    document: {createElement() {throw new Error('unexpected new control');}}, categoryChoices: [], pendingEditorSubmission: null,
    initialEditor: '', lastAutosaveSignature: '', autosaveStopped: false, forcedEditorDirtyLabel: '', slugTouched: false, slug: null, source: null, type: null,
    updateEditorCount() {}, updateCoverPreview() {}, updateScheduleRequirement() {}, syncEditorSubmitControls() {}, setEditorRecovery() {}, setAutosaveDetail() {}, scheduleAutosave() {}, editorChanged() {changed++;},
  });
  const helperStart = source.indexOf('    const reviewPublicationTimezone =');
  const helperEnd = source.indexOf("    let lastAutosaveSignature", helperStart);
  assert.ok(helperStart > 0 && helperEnd > helperStart);
  vm.runInContext(source.slice(helperStart, helperEnd) + '\nglobalThis.review = reviewPublicationTimezone;', context);
  const restoreStart = source.indexOf('      restore(record) {'), restoreEnd = source.indexOf('    }) || null;', restoreStart);
  assert.ok(restoreStart > 0 && restoreEnd > restoreStart);
  vm.runInContext('globalThis.restore = ({' + source.slice(restoreStart, restoreEnd) + '}).restore;', context);
  const confirmStart = source.indexOf("    timezoneReview?.querySelector('[data-published-timezone-confirm]')"), confirmEnd = source.indexOf('    syncEditorSubmitControls();', confirmStart);
  vm.runInContext(source.slice(confirmStart, confirmEnd), context);
  return {context, fields, labels, confirm: () => confirm(), changes: () => changed};
}

  test('recovering a copy from another timezone keeps its wall clock until explicit confirmation', () => {
    const zone = 'America/New_York';
    const f = fixture();
    const pending = {body: 'published_at=2026-11-01T01%3A30&published_timezone=America%2FNew_York', key: 'unchanged'};
    const draft = [['title', 'Recovered'], ['version', '1'], ['published_at', '2026-11-01T01:30']];
    draft.push(['published_timezone', zone], ['published_source', 'autosave']);
    f.context.restore({id: 'copy', draft, pending});
    assert.equal(f.fields.published_at.value, '2026-11-01T01:30');
    assert.equal(f.fields.published_timezone.value, zone);
    assert.equal(f.context.timezoneNeedsReview, true);
    assert.equal(f.context.timezoneReview.hidden, false);
    assert.equal(f.context.pendingEditorSubmission, pending);
    const body = pending.body;
    f.confirm();
    assert.equal(f.fields.published_at.value, '2026-11-01T01:30');
    assert.equal(f.fields.published_timezone.value, 'Asia/Kathmandu');
    assert.equal(f.fields.published_source.value, 'canonical');
    assert.equal(f.context.timezoneNeedsReview, false);
    assert.equal(pending.body, body, 'confirmation cannot rewrite an uncertain request');
    assert.equal(f.changes(), 1);
  });

test('same-zone recovered copy preserves its trusted recovery source without a timezone warning', () => {
  const f = fixture();
  f.context.restore({id: 'copy', draft: [['title', 'Recovered'], ['version', '1'], ['published_at', '2026-11-01T01:30'], ['published_timezone', 'Asia/Kathmandu'], ['published_source', 'autosave']], pending: null});
  assert.equal(f.context.timezoneNeedsReview, false);
  assert.equal(f.fields.published_source.value, 'autosave');
  assert.equal(f.context.autosaveStopped, false);
});

test('server timezone rejection relabels the input but cannot silently replace its token or value', () => {
  const f = fixture(); f.fields.published_at.value = '2026-03-08T02:30';
  f.context.review({fields: {published_at: 'Review the changed calendar'}, published_timezone: 'America/New_York'});
  assert.equal(f.context.editorTimezone, 'America/New_York');
  assert.equal(f.context.timezoneNeedsReview, true);
  assert.equal(f.labels[0].textContent, 'America/New_York');
  assert.equal(f.fields.published_timezone.value, 'Asia/Kathmandu');
  f.confirm();
  assert.equal(f.fields.published_timezone.value, 'America/New_York');
  assert.equal(f.fields.published_at.value, '2026-03-08T02:30', 'server still validates nonexistent local times');
});
