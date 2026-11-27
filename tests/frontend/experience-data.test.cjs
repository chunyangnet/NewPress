const assert=require('node:assert/strict'),test=require('node:test');
const {loadModule}=require('./helpers/typescript.cjs');
const data=loadModule('frontend/shared/experience-data.ts',{URL}),base='https://site.test/posts/example';
const original={version:1,nonce:'saved-comment-request',owner:2,payload:{parent_id:0,author_name:'Reader',email:'reader@example.test',website:'',content:'saved text'},uncertain:true};
test('persisted comment request retains exact whitespace, original identity and nonce',()=>{
  const value=structuredClone(original);value.payload.content='Line one\n\n 🙂 ';value.uncertain=false;
  const parsed=data.parseCommentSubmission(value);assert.equal(parsed.payload.content,value.payload.content);assert.equal(parsed.nonce,value.nonce);assert.equal(parsed.owner,2);assert.equal(parsed.uncertain,true);
});
for(const [name,mutate]of [
  ['version',v=>v.version=2],['owner',v=>v.owner='2'],['nonce',v=>v.nonce='bad space'],['parent',v=>v.payload.parent_id=-1],
  ['content',v=>v.payload.content={}],['email',v=>delete v.payload.email],['oversize',v=>v.payload.content='x'.repeat(20001)],
])test(`invalid saved comment ${name} cannot become a retry request`,()=>{const value=structuredClone(original);mutate(value);assert.equal(data.parseCommentSubmission(value),null);});
for(const path of ['https://other.test/a','//other.test/a','javascript:alert(1)','/admin/','/market/x','/components/x','https://reader@site.test/posts/a','/posts\\evil','/posts/a b'])test(`unsafe saved URL ${path} is rejected`,()=>assert.equal(data.safeExperienceURL(path,base),null));
test('local guest records normalize duplicates and discard malformed entries',()=>{
  const prefs=data.parseGuestPreferences({saved:[{title:'Old',url:'/posts/a/?q=1#reply'},{title:'Current',url:'/posts/a'},{title:42,url:'/posts/b'},{title:'Bad',url:'https://other.test'}],following:['A',3],following_ids:[1,1,'2',-1],likes:['legacy:25','post:25',null]},base);
  assert.deepEqual(JSON.parse(JSON.stringify(prefs)),{saved:[{key:'/posts/a',title:'Current',url:'/posts/a'}],following:['A'],following_ids:[1],likes:['post:25']});
});
test('future, expired and malformed persisted drafts are not restored',()=>{
  const now=100000000;
  for(const at of [now+1,now-86400000,'10',Infinity]){
    assert.equal(data.parseCommentDraft({content:'text',at},now),null);
    assert.equal(data.parseCommunityDraft({content:'text',at},now).content,'');
  }
  assert.equal(data.parseCommentDraft({content:'kept',parent:'-4',at:now},now).parent,'0');
});
test('malformed saved community key is dropped while readable draft remains',()=>{
  const draft=data.parseCommunityDraft({content:'readable draft',at:Date.now(),pending:{signature:'[2,"publish","text"]',nonce:'bad key',guestContent:null}});
  assert.equal(draft.content,'readable draft');assert.equal(draft.pending,null);
});
test('malformed optional receipt data never consumes a valid pending request',()=>{
  const receipt={status:'approved',submission_key:original.nonce,comment:{id:4,post_id:7,parent_id:0,author_name:'Reader'}};
  for(const extra of [{badges:[{name:'Incomplete'}]},{support:{supported:'yes'}},{origin:{location:{}}}]){
    assert.equal(data.parseCommentReceipt({...receipt,comment:{...receipt.comment,...extra}},original.payload,'7',original),null);
  }
  assert.equal(data.parseCommentReceipt(receipt,original.payload,'7',original).comment.id,4);
});
