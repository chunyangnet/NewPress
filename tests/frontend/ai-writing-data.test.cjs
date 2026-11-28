const test=require('node:test'),assert=require('node:assert/strict');
const {loadNativeModule}=require('./helpers/typescript.cjs');
const data=loadNativeModule('frontend/shared/ai-writing-data.ts'),templates=loadNativeModule('frontend/shared/writing-template-data.ts');
const sample=require('./fixtures/ai-writing-contracts.json'),copy=value=>structuredClone(value);
test('actual Go writing connections, prompts, document and media pass the runtime boundary',()=>{
  assert.equal(data.writingConnections(sample.connections).providers.length,2);
  assert(data.writingPrompts(sample.prompts).some(p=>p.id==='team:'+sample.template.id));
  assert.deepEqual(data.writingDocument(sample.document),sample.document);
  assert.equal(data.aiMedia(sample.media).id,sample.media.id);
  assert.equal(data.metadataResult(sample.metadata).excerpt,sample.metadata.Excerpt);
});
test('writing connections reject secrets, duplicate identities and mismatched defaults',()=>{
  for(const alter of [v=>v.providers[0].api_key='secret',v=>v.providers.push(v.providers[0]),v=>v.default_provider='image',v=>v.providers[0].models=['ok',3],v=>v.providers[0].default_model='missing']){const value=copy(sample.connections);alter(value);assert.throws(()=>data.writingConnections(value));}
});
test('optional local preferences discard malformed values and prototype property names',()=>{
  assert.deepEqual(data.writingPreference(null),{});
  assert.deepEqual(data.writingPreference({provider_id:4,models:{writer:42},stream_usage:{writer:'true'}}),{models:{},stream_usage:{}});
  assert.deepEqual(data.writingPreference(JSON.parse('{"models":{"__proto__":"x","constructor":"x","writer":"m"}}')),{models:{writer:'m'}});
});
test('team prompt selection binds a real template identity and positive version',()=>{
  const value=copy(sample.prompts),team=value.prompts.find(p=>p.id.startsWith('team:'));
  for(const alter of [p=>p.template_id='a'.repeat(32),p=>p.template_version=0,p=>p.template_version=2**53,p=>p.base_prompt='team:other',p=>p.needs_draft='false']){const broken=copy(value);alter(broken.prompts.find(p=>p.id===team.id));assert.throws(()=>data.writingPrompts(broken));}
  value.prompts.push(value.prompts[0]);assert.throws(()=>data.writingPrompts(value));
});
test('completed writing requires full document and boolean completion with actual usage',()=>{
  const value={document:sample.document,complete:false,usage:{completion_tokens:0}};
  assert.equal(data.writingDone(value).usage.completion_tokens,0);
  for(const bad of [{...value,complete:'false'},{...value,usage:{total_tokens:-1}},{...value,document:{...sample.document,tags:[7]}},{...value,document:{...sample.document,content_source:''}}])assert.throws(()=>data.writingDone(bad));
});
test('session binds exact owner, login, CSRF and writable status',()=>{
  const session={authenticated:true,user_id:5,username:'owner',csrf_token:'csrf',read_only:false};
  assert.equal(data.writingSession(session,'5','owner','csrf'),'csrf');
  for(const bad of [{...session,user_id:6},{...session,username:'other'},{...session,csrf_token:'rotated'},{...session,read_only:true},{...session,read_only:undefined}])assert.throws(()=>data.writingSession(bad,'5','owner','csrf'));
  assert.equal(data.writingSession({...session,read_only:true},'5','owner','csrf',false),'csrf');
});
test('media lookup must return the requested active local image with matching URL identity',()=>{
  assert.throws(()=>data.aiMedia(sample.media,sample.media.id+1));
  for(const bad of [{...sample.media,public_url:`/media/${sample.media.id+1}/image.png`},{...sample.media,status:'trash'},{...sample.media,public_url:'/media/1/evil\\file'},{...sample.media,width:0}])assert.throws(()=>data.aiMedia(bad));
});
test('media paths cannot normalize outside their declared identity',()=>{
  for(const path of ['../image.png','%2e%2e/image.png','image%2ffile.png','image%5cfile.png'])assert.equal(data.mediaURL({...sample.media,public_url:`/media/${sample.media.id}/${path}`}), '');
});
test('image completion matches model and every previously received immutable media field',()=>{
  const image=data.aiMedia(sample.media),value={model:'image-fast',items:[sample.media]};
  assert.equal(data.imageDone(value,'image-fast',[image])[0].id,image.id);
  for(const bad of [{...value,model:'different'},{...value,items:[]},{...value,items:[sample.media,sample.media]},{...value,items:[{...sample.media,width:321}]}])assert.throws(()=>data.imageDone(bad,'image-fast',[image]));
});
test('malformed paragraph and topics responses cannot create editable suggestions',()=>{
  for(const value of [{}, {text:3},{text:'   '}])assert.throws(()=>data.paragraphResult(value));
  assert.equal(data.paragraphResult({text:'优化后的正文'}),'优化后的正文');
  for(const topics of [[],[42],['重复','重复'],['一行\n另一行'],['   ']])assert.throws(()=>data.suggestedTopics({topics}));
});
test('actual Go template list, detail, export and receipt pass with exact identity',()=>{
  assert.equal(templates.templateList(sample.templates)[0].id,sample.template.id);
  assert.deepEqual(templates.templateDetail(sample.template,sample.template.id),sample.template);
  assert.deepEqual(templates.templateExport(sample.template_export,sample.template.id,1),sample.template_export);
  assert.deepEqual(templates.templateReceipt(sample.template_result,{action:'save',id:'',expected_version:0}),sample.template_result);
});
test('template list rejects truncated pagination, false totals and duplicate identities',()=>{
  for(const alter of [v=>v.total++,v=>v.next_offset=1,v=>v.items.push(v.items[0]),v=>v.snapshot_sha256='bad',v=>v.items[0].archived='false']){const value=copy(sample.templates);alter(value);assert.throws(()=>templates.templateList(value));}
});
test('template history must be continuous and attached to requested identity',()=>{
  assert.throws(()=>templates.templateDetail(sample.template,'a'.repeat(32)));
  for(const revisions of [[],[{...sample.template.revisions[0],version:2}],[{...sample.template.revisions[0],source:{kind:'history',template_id:sample.template.id,version:1}}]])assert.throws(()=>templates.templateDetail({...sample.template,revisions},sample.template.id));
});
test('saved template receipt cannot confirm another template or a wrong next version',()=>{
  const command={action:'save',id:sample.template.id,expected_version:1};
  assert.equal(templates.templateReceipt({...sample.template_result,version:2,replayed:true},command).version,2);
  for(const value of [sample.template_result,{...sample.template_result,version:2,id:'a'.repeat(32)},{...sample.template_result,version:3}])assert.throws(()=>templates.templateReceipt(value,command));
});
test('confirmed template reads must contain the original saved definition and action',()=>{
  const command={action:'save',id:'',expected_version:0,definition:sample.template.revisions[0].definition};
  assert.equal(templates.savedTemplate(sample.template,sample.template_result,command).id,sample.template.id);
  const bad=copy(sample.template);bad.revisions[0].definition.title='另一份已保存内容';assert.throws(()=>templates.savedTemplate(bad,sample.template_result,command));
});
test('exports preserve selected version and reject another template or schema',()=>{
  for(const value of [{...sample.template_export,version:2},{...sample.template_export,template_id:'a'.repeat(32)},{...sample.template_export,kind:'untrusted'}])assert.throws(()=>templates.templateExport(value,sample.template.id,1));
});
test('import preview is bound to exact bytes and parsed definition of original file',async()=>{
  const raw=JSON.stringify(sample.template_export),hash=require('node:crypto').createHash('sha256').update(raw).digest('hex');
  assert.equal((await templates.importPreview({document:sample.template_export,sha256:hash},raw)).raw,raw);
  await assert.rejects(templates.importPreview({document:sample.template_export,sha256:'0'.repeat(64)},raw));
  await assert.rejects(templates.importPreview({document:{...sample.template_export,definition:{...sample.template_export.definition,title:'另一份'}},sha256:hash},raw));
});
