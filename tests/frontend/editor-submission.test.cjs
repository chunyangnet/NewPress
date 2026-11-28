const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {webcrypto} = require('node:crypto');
const editor = require('./helpers/editor-save.cjs');
const source = editor.source();
const deferred = () => { let resolve; const promise = new Promise(done => {resolve = done;}); return {promise, resolve}; };
const good = (request, extra = {}) => ({ok: true, status: 200, json: async () => ({saved: true, submission_key: request.headers['Idempotency-Key'], id: 42,
  version: Number(request.body.get('version') || 0) + 1, action: request.body.has('version') ? request.body.get('action').replace(/^restore_revision-\d+$/, 'restore_revision') : 'create',
  replayed: false, editor_url: '/admin/content/42/edit/?notice=saved', autosave_url: '/admin/content/42/autosave/', autosave_enabled: true, ...extra})});

function fixture(fetch, isNew = false) {
  const start = source.indexOf('    const acknowledgeEditorSubmission ='), end = source.indexOf("    editorForm.addEventListener('submit'", start);
  const fields = {csrf_token: {value: 'csrf-old'}, title: {value: 'original'}, content_source: {value: '<p>original</p>'}, status: {value: 'draft', options: [{value: 'draft'}], append(option) {this.options.push(option);}}};
  if (!isNew) fields.version = {value: '1'};
  const form = {action: {length: 2}, actionAttribute: isNew ? '/admin/content/new/' : '/admin/content/42/edit/', dataset: {editorNew: String(isNew)},
    getAttribute(name) {return name==='action' ? this.actionAttribute : null;}, setAttribute(name,value) {if(name==='action') this.actionAttribute=value;},
    querySelector(selector) { if (selector === '[data-content-status]') return fields.status; return fields[/name="(.*?)"/.exec(selector)?.[1]]; },
    querySelectorAll() {return [];}, append(element) {fields[element.name] = element;}};
  class FormData {forEach(callback) {Object.entries(fields).forEach(([key, input]) => callback(input.value, key));}}
  const serializeEditorData = data => {const values = {}; data.forEach((value,key) => { if (!['action','csrf_token'].includes(key)) values[key] = value; }); return JSON.stringify(Object.fromEntries(Object.keys(values).sort().map(key=>[key,values[key]])));};
  const signature = () => serializeEditorData(new FormData());
  const navigations = [], messages = [], timers = new Map(), errors = [], requests = [], history = [], currentLink = {}, identity = {}, heading = {};
  let nextTimer = 0;
  const featureEvents = [];
  const context = vm.createContext({
    ...editor.context,
    AbortController, CustomEvent, DOMException, URL, URLSearchParams, Uint8Array, FormData, serializeEditorData, serializedEditor: signature,
    submittingEditor: false, editorPageHidden: false, pendingEditorSubmission: null, intentionalEditorNavigation: false, browserRecovery: null,
    timezoneNeedsReview: false, timezoneReview: null, publishedSource: null, reviewPublicationTimezone() {},
    autosaveTimer: 0, autosaveController: null, autosavePromise: null, editorSubmissionController: null,
    initialEditor: signature(), lastAutosaveSignature: '', autosaveStopped: false, forcedEditorDirtyLabel: '',
    autosaveURL: isNew ? '' : '/admin/content/42/autosave/', autosaveEnabled: !isNew,
    editorForm: form, recoveryActions: {querySelector: () => currentLink},
    editorIsDirty: () => signature() !== context.initialEditor,
    syncEditorSubmitControls() {}, showEditorSubmissionErrors: fields => errors.push(fields),
    setAutosaveDetail: (message,tone) => messages.push({message,tone}), setEditorRecovery() {}, updateScheduleRequirement() {}, scheduleAutosave() {},
    checkEditorSession: async () => true,
    fetch: async (url, options) => {requests.push({url,...options}); return context.fetchImpl(url,options);}, fetchImpl: fetch,
    document: {title:'新建文章 · Test', dispatchEvent: event => featureEvents.push(event), createElement: () => ({}), querySelector: selector => selector === '[data-editor-identity]' ? identity : heading, querySelectorAll: () => []},
    window: {crypto: webcrypto, confirm: () => true,
      history: {state:null, replaceState: (...args) => history.push(args)},
      location: {href: 'https://example.test/admin/content/42/edit/', origin: 'https://example.test', assign: url => navigations.push(url)},
      setTimeout(fn, ms) {timers.set(++nextTimer, {fn,ms}); return nextTimer;}, clearTimeout: id => timers.delete(id)},
  });
  vm.runInContext(source.slice(start,end)+'\nglobalThis.submit = submitEditor;', context);
  return {context, fields, form, navigations, messages, errors, timers, requests, history, featureEvents, signature, send: (action='save') => context.submit({value:action,dataset:{}}), retry: () => context.submit(null)};
}

test('timezone review blocks a new save but never blocks confirmation of the original request', async () => {
  const f = fixture(async () => {throw new Error('response lost');});
  f.context.timezoneNeedsReview = true;
  await f.send(); assert.equal(f.requests.length, 0); assert.match(f.messages.at(-1).message, /核对发布时间/);
  f.context.timezoneNeedsReview = false;
  await f.send(); const original = f.context.pendingEditorSubmission.body;
  f.context.timezoneNeedsReview = true;
  f.context.fetchImpl = async (_, options) => good(options, {replayed: true});
  await f.retry();
  assert.equal(f.requests.length, 2);
  assert.equal(f.requests[1].body.toString(), original);
  assert.equal(f.context.pendingEditorSubmission, null);
});

test('a saved recovery-derived timestamp switches to canonical before later edits are sent', async () => {
  const f = fixture(async () => {throw new Error('response lost');});
  f.fields.published_source = {value: 'autosave'};
  f.context.publishedSource = f.fields.published_source;
  await f.send(); f.fields.title.value = 'typed after save';
  f.context.fetchImpl = async (_, options) => good(options, {replayed: true});
  await f.retry();
  assert.equal(f.fields.published_source.value, 'canonical');
  assert.equal(JSON.parse(f.context.initialEditor).published_source, 'canonical');
  await f.send(); assert.equal(f.requests[2].body.get('published_source'), 'canonical');
});

test('response loss retries the exact captured write and preserves later typing', async () => {
  const f = fixture(async () => {throw new TypeError('offline');});
  f.fields.title.value = 'submitted'; await f.send();
  assert.ok(f.context.pendingEditorSubmission.uncertain); assert.equal(f.navigations.length,0);
  f.fields.title.value = 'typed later'; f.fields.csrf_token.value = 'renewed-token';
  f.context.fetchImpl = async (_,options) => good(options,{replayed:true}); await f.retry();
  assert.equal(f.requests.length,2);
  assert.equal(f.requests[0].headers['Idempotency-Key'],f.requests[1].headers['Idempotency-Key']);
  assert.equal(f.requests[1].body.get('title'),'submitted'); assert.equal(f.requests[1].body.get('csrf_token'),'renewed-token');
  assert.equal(f.fields.title.value,'typed later'); assert.equal(f.fields.version.value,'2');
  assert.equal(JSON.parse(f.context.initialEditor).title,'submitted'); assert.notEqual(f.signature(),f.context.initialEditor);
  assert.equal(f.context.pendingEditorSubmission,null); assert.equal(f.navigations.length,0);
  assert.equal(f.requests[1].redirect,'error'); assert.equal(f.requests[1].cache,'no-store');
});

test('first draft retry adopts the confirmed identity without discarding later input', async () => {
  const f = fixture(async () => {throw new Error('lost response');},true);
  await f.send(); f.fields.content_source.value = '<p>newer input</p>';
  assert.equal(f.featureEvents.length,0);
  f.context.fetchImpl = async (_,options) => good(options,{replayed:true}); await f.retry();
  assert.equal(f.form.actionAttribute,'/admin/content/42/edit/'); assert.equal(f.fields.version.value,'1');
  assert.equal(f.form.dataset.editorNew,'false'); assert.equal(f.context.autosaveURL,'/admin/content/42/autosave/');
  assert.equal(f.featureEvents[0].type,'newpress:feature-status-article-saved');
  assert.equal(f.featureEvents[0].detail.id,42);
  assert.equal(f.context.autosaveEnabled,true); assert.equal(f.fields.content_source.value,'<p>newer input</p>');
  assert.equal(f.history[0][2],'/admin/content/42/edit/'); assert.equal(f.navigations.length,0);
  assert.equal(f.requests[1].url,'/admin/content/new/');
  await f.send(); assert.equal(f.requests[2].url,'/admin/content/42/edit/');
  assert.notEqual(f.requests[2].headers['Idempotency-Key'],f.requests[1].headers['Idempotency-Key']);
});

test('rapid saves share one in-flight write and an unchanged confirmation navigates', async () => {
  const gate=deferred(), f=fixture(async (_,options) => {await gate.promise; return good(options);});
  const first=f.send(); await Promise.resolve(); await f.send(); assert.equal(f.requests.length,1);
  gate.resolve(); await first; assert.equal(f.navigations.length,1); assert.equal(f.context.intentionalEditorNavigation,true);
  assert.equal(f.context.pendingEditorSubmission,null); assert.equal(f.timers.size,0);
});

test('saving captures the confirmed fields before waiting for a fresh session', async () => {
  const gate = deferred(), f = fixture(async (_, options) => good(options));
  f.context.checkEditorSession = () => gate.promise;
  f.fields.status.value = 'publish';
  const saving = f.send();
  f.fields.title.value = 'typing while the session is checked';
  f.fields.status.value = 'private';
  assert.equal(f.requests.length, 0);
  gate.resolve(true); await saving;
  assert.equal(f.requests[0].body.get('title'), 'original');
  assert.equal(f.requests[0].body.get('status'), 'publish');
  assert.equal(f.fields.title.value, 'typing while the session is checked');
  assert.equal(f.fields.status.value, 'private');
  assert.equal(f.navigations.length, 0);
});

test('IME composition defers a new save until the committed input is available', async () => {
  const f = fixture(async (_, options) => good(options));
  f.form.dataset.editorComposing = 'true';
  await f.send(); assert.equal(f.requests.length, 0);
  assert.equal(f.context.pendingEditorSubmission, null);
  f.fields.title.value = '已经完成的中文输入';
  f.form.dataset.editorComposing = 'false';
  await f.send();
  assert.equal(f.requests[0].body.get('title'), '已经完成的中文输入');
});

test('invalid acknowledgements retain the request and never advance the version', async () => {
  for (const change of [{saved:false},{submission_key:'wrong'},{id:43},{version:3},{action:'publish'},{replayed:undefined},
    {editor_url:'//external.test/edit/'},{autosave_url:'/admin/content/99/autosave/'},{autosave_enabled:undefined}]) {
    const f=fixture(async (_,options) => good(options,change)); await f.send();
    assert.ok(f.context.pendingEditorSubmission.uncertain); assert.equal(f.navigations.length,0); assert.equal(f.fields.version.value,'1');
  }
});

test('an uncertain write stays uncertain after a later validation or version error', async () => {
  for (const error of ['validation_failed','version_conflict']) {
    const f=fixture(async () => {throw new Error('lost');}); await f.send(); const key=f.context.pendingEditorSubmission.key;
    f.context.fetchImpl=async () => ({ok:false,status:409,json:async()=>({saved:false,error,message:'Check fields'})});
    f.fields.title.value='changed'; await f.retry();
    assert.equal(f.context.pendingEditorSubmission.key,key); assert.equal(f.fields.title.value,'changed');
    assert.match(f.messages.at(-1).message,/上次保存仍待确认/);
  }
});

test('a definitive first rejection allows corrected input in a new logical save', async () => {
  const f=fixture(async () => ({ok:false,status:422,json:async()=>({saved:false,error:'validation_failed',fields:{title:'Required'}})}));
  f.fields.title.value=''; await f.send(); assert.equal(f.context.pendingEditorSubmission,null); assert.equal(f.errors.at(-1).title,'Required');
  f.fields.title.value='corrected'; f.context.fetchImpl=async (_,options)=>good(options); await f.send();
  assert.notEqual(f.requests[0].headers['Idempotency-Key'],f.requests[1].headers['Idempotency-Key']);
  assert.equal(f.requests[1].body.get('title'),'corrected'); assert.equal(f.navigations.length,1);
});

test('a failed identity check cannot send or replace a retained submission', async () => {
  const f=fixture(async()=>{throw new Error('lost');}); await f.send(); const sent=f.context.pendingEditorSubmission;
  f.context.checkEditorSession=async()=>false; await f.retry();
  assert.equal(f.context.pendingEditorSubmission,sent); assert.equal(f.requests.length,1); assert.equal(f.context.submittingEditor,false);
});

test('response-body timeout keeps the original request retryable', async () => {
  const started=deferred();
  const f=fixture(async (_,{signal})=>({ok:true,json:()=>{started.resolve(); return new Promise((_,reject)=>signal.addEventListener('abort',()=>reject(signal.reason)));}}));
  const pending=f.send(); await started.promise; const timer=[...f.timers.values()][0]; assert.equal(timer.ms,20000); timer.fn(); await pending;
  assert.equal(f.context.pendingEditorSubmission.uncertain,true); assert.equal(f.context.submittingEditor,false); assert.equal(f.timers.size,0);
});

test('status and revision actions never mark unsaved visible fields as saved', async () => {
  for (const action of ['trash','restore','publish','restore_revision-1']) {
    const f=fixture(async (_,options)=>good(options)); f.fields.title.value='unsaved title'; await f.send(action);
    assert.equal(f.fields.title.value,'unsaved title'); assert.equal(f.fields.version.value,'2');
    assert.notEqual(f.signature(),f.context.initialEditor); assert.equal(f.navigations.length,0);
    if(action==='trash') assert.equal(f.fields.status.value,'trash');
  }
});

test('leaving during an uncertain or in-flight save still protects a clean-looking form', () => {
  const start=source.indexOf("    window.addEventListener('beforeunload'", source.indexOf('    async function submitEditor(')),end=source.indexOf("    window.addEventListener('pageshow'",start);
  let listener;
  const context=vm.createContext({
    ...editor.context,intentionalEditorNavigation:false,submittingEditor:false,pendingEditorSubmission:null,editorIsDirty:()=>false,
    window:{addEventListener:(_,fn)=>{listener=fn;}}});
  vm.runInContext(source.slice(start,end),context);
  const leave=()=>{let prevented=false;const event={preventDefault(){prevented=true;}};listener(event);return prevented;};
  assert.equal(leave(),false);context.submittingEditor=true;assert.equal(leave(),true);
  context.submittingEditor=false;context.pendingEditorSubmission={};assert.equal(leave(),true);
  context.intentionalEditorNavigation=true;assert.equal(leave(),false);
});

test('autosave cannot send a newer version while the original save is still uncertain', async () => {
  const start=source.indexOf('    const performAutosave ='),end=source.indexOf('    async function saveAutosave',start);
  const context=vm.createContext({
    ...editor.context,autosaveURL:'/admin/content/42/autosave/',submittingEditor:false,pendingEditorSubmission:{},timezoneNeedsReview:false,
    serializedEditor(){throw new Error('must not read newer fields for an uncertain write');}});
  vm.runInContext(source.slice(start,end)+'\nglobalThis.save=performAutosave;',context);
  assert.equal(await context.save(true),null);
});

test('an unresolved stored operation blocks a new save until explicitly recovered or discarded', async () => {
  const f=fixture(async(_,request)=>good(request),true); let warnings=0;
  f.context.browserRecovery={busy:()=>false,blocked:()=>true,warn:()=>warnings++};
  await f.send();assert.equal(f.requests.length,0);assert.equal(warnings,1);
});

test('the original request is persisted before dispatch and consumed only after a valid acknowledgement', async () => {
  const events=[],f=fixture(async(_,request)=>{events.push('fetch');return good(request);});
  f.context.browserRecovery={busy:()=>false,blocked:()=>false,flush:()=>events.push('persist'),confirmed:()=>events.push('confirmed')};
  await f.send();assert.deepEqual(events,['persist','fetch','confirmed']);
  const g=fixture(async()=>{throw new Error('offline');});
  g.context.browserRecovery={busy:()=>false,blocked:()=>false,flush:()=>events.push('retained'),confirmed:()=>assert.fail('unconfirmed save deleted its copy')};
  await g.send();assert.ok(g.context.pendingEditorSubmission);assert.equal(events.at(-1),'retained');
});

test('document saves stay in place and report the submitted publication state', async () => {
  const f=fixture(async(_,request)=>good(request)); const events=[];
  f.fields.published_source={value:'canonical'}; f.context.publishedSource=f.fields.published_source;
  f.form.hasAttribute=name=>name==='data-document-editor';
  f.form.dispatchEvent=event=>events.push(event);
  f.context.CustomEvent=class {constructor(type,options){this.type=type;this.detail=options.detail;}};
  f.fields.status.value='publish';f.fields.content_source.value='<p>current edits</p>';
  await f.send('save');
  assert.equal(f.navigations.length,0);assert.equal(f.fields.version.value,'2');
  assert.equal(f.signature(),f.context.initialEditor);
  assert.equal(events[0].detail.status,'publish');
  assert.equal(f.requests[0].body.get('content_source'),'<p>current edits</p>');
  assert.equal(f.context.pendingEditorSubmission,null);
  assert.match(f.messages.at(-1).message,/已保存到正式版本/);
});
