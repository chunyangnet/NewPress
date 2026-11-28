const test=require('node:test'),assert=require('node:assert/strict');
const {loadNativeModule}=require('./helpers/typescript.cjs');
const data=loadNativeModule('frontend/shared/wechat-data.ts'),helpers=loadNativeModule('frontend/shared/wechat-helpers.ts');
const sample=require('./fixtures/wechat-contracts.json'),copy=value=>structuredClone(value);
test('actual Go workspace, preview, import, local check and queued job pass typed boundaries',()=>{
 const workspace=data.wechatWorkspace(sample.workspace),post=workspace.selected_post;
 assert.equal(data.collectPreview(sample.preview).articles.length,2);
 assert.equal(data.collectImport(sample.applied,new Set(sample.preview.articles.map(a=>a.key))).plan.new,2);
 assert.equal(data.syncCheck(sample.check.check,{post_id:post.id,version:post.version,config_version:workspace.config.version}).ready,true);
 assert.equal(data.syncedJob({job:sample.queued_job},post,workspace.config).post_id,post.id);
});
test('workspace requires typed counts, permissions, posts, jobs and selected article',()=>{
 for(const corrupt of [r=>r.jobs[0].post_id=0,r=>r.posts[0].version=1.5,r=>r.posts[0].title=3,r=>r.selected_post.status='private',r=>r.can_collect='true',r=>r.active_jobs=-1,r=>r.job_page=r.job_pages+1,r=>r.jobs[0].status='invalid',r=>r.jobs[0].updated_at='yesterday']){const r=copy(sample.workspace);corrupt(r);assert.throws(()=>data.wechatWorkspace(r));}
});
test('workspace rejects duplicate identities and incomplete lists',()=>{
 for(const key of ['jobs','posts']){const r=copy(sample.workspace);r[key].push(r[key][0]);assert.throws(()=>data.wechatWorkspace(r));r[key]=null;assert.throws(()=>data.wechatWorkspace(r));}
});
test('public config rejects secrets and malformed optional settings',()=>{
 for(const patch of [{app_secret:'private'},{has_secret:'true'},{collect_drafts:'true'},{since:''},{version:0},{app_id:'invalid'}])assert.throws(()=>data.wechatConfig({...sample.workspace.config,...patch}));
 const c=copy(sample.workspace.config);delete c.collect_drafts;delete c.has_secret;assert.equal(data.wechatConfig(c).collect_drafts,false);
});
test('saved config validates all submitted fields, next version and retained secret',()=>{
 const previous=data.wechatConfig(sample.workspace.config),input={...previous,app_secret:''};
 const saved={...previous,version:previous.version+1};assert.equal(data.savedConfig(saved,input,previous,false).has_secret,true);
 for(const patch of [{version:saved.version+1},{author:'other'},{enabled:false},{has_secret:false}])assert.throws(()=>data.savedConfig({...saved,...patch},input,previous,false));
});
test('secret clearing and account change cannot acknowledge old credentials',()=>{
 const previous=data.wechatConfig(sample.workspace.config),input={...previous,enabled:false,app_secret:'',app_id:'wx0000000000000000'};
 assert.throws(()=>data.savedConfig({...input,has_secret:true,version:input.version+1},input,previous,false));
 assert.equal(data.savedConfig({...input,has_secret:false,version:input.version+1},input,previous,true).has_secret,false);
});
test('sync receipt is bound to post version and official account while retaining prior task mode',()=>{
 const workspace=data.wechatWorkspace(sample.workspace),post=workspace.selected_post;
 for(const patch of [{post_id:post.id+1},{version:post.version+1},{app_id:'wx0000000000000000'}])assert.throws(()=>data.syncedJob({job:{...sample.queued_job,...patch}},post,workspace.config));
 assert.equal(data.syncedJob({job:{...sample.queued_job,auto_publish:true}},post,workspace.config).auto_publish,true);
});
test('job actions require explicit boolean acknowledgement',()=>{
 data.jobAcknowledgement({ok:true});for(const value of [{},{ok:'true'},{ok:false},null])assert.throws(()=>data.jobAcknowledgement(value));
});
test('local checks bind article, config and ready state to verified metrics',()=>{
 const input=sample.check.check;for(const patch of [{post_id:input.post_id+1},{config_version:input.config_version+1},{checked_images:input.images+1},{issues:[{field:'body',message:'failed'}],ready:true},{warnings:[4]}])assert.throws(()=>data.syncCheck({...input,...patch},input));
});
test('preview rejects mismatched counts, duplicate plans, missing receipts and foreign URLs',()=>{
 for(const corrupt of [r=>r.plan.new++,r=>r.plan.items[1].key=r.plan.items[0].key,r=>r.articles[1].key=r.articles[0].key,r=>r.receipts={},r=>r.articles[0].url='https://elsewhere.test/path',r=>r.articles[0].images=null,r=>r.receipts.foreign='token']){const r=copy(sample.preview);corrupt(r);assert.throws(()=>data.collectPreview(r));}
});
test('failure-only previews have no importable records or fabricated expiry',()=>{
 const result=data.collectPreview({articles:[],failures:[{url:'bad',message:'unsupported'}]});assert.equal(result.plan.new,0);assert.equal(result.expires,'');assert.deepEqual(result.receipts,{});
});
test('import receipts require exact selected keys, target IDs and created/existing totals',()=>{
 const keys=new Set(sample.preview.articles.map(a=>a.key));for(const corrupt of [r=>r.applied='true',r=>r.plan.items[0].post_id=0,r=>r.plan.items[0].key='wrong',r=>r.plan.items[1].key=r.plan.items[0].key,r=>r.plan.existing++,r=>r.plan.new=0]){const r=copy(sample.applied);corrupt(r);assert.throws(()=>data.collectImport(r,keys));}
});
test('share links normalize and deduplicate without discarding unsupported input',()=>{
 const r=helpers.parseCollectionInput('标题 https://mp.weixin.qq.com/s/firstx?from=1。\nhttps://mp.weixin.qq.com/s/firstx?from=2\nhttps://wrong.test/a');assert.equal(r.urls.length,2);assert.equal(r.duplicates,1);assert.equal(r.unchecked,1);
 for(const url of ['https://name:secret@mp.weixin.qq.com/s/firstx','https://mp.weixin.qq.com:444/s/firstx','https://mp.weixin.qq.com/s?__biz=x&__biz=y&mid=1&idx=1&sn=x','javascript:alert(1)'])assert.equal(helpers.normalizeCollectionURL(url),null);
});
