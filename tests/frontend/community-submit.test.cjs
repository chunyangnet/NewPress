const assert = require('node:assert/strict');
const test = require('node:test');
const {loadModule} = require('./helpers/typescript.cjs');
const defer=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no;});return {promise,resolve,reject};};
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function fixture({owner=2,content='Original <text> & a second line\n🙂',canPublish=true}={}){
  const state={owner,generation:0,busy:false,pending:null},input={value:content,readOnly:false},submit={replaceChildren(){}},errorBox={hidden:true};
  let session={authenticated:true,id:2,can_publish:canPublish},signIn=action=>action();
  const requests=[],gates=[],messages=[],stored=new Map(),navigation=[],history={state:{unrelated:true},replaceState(value,_,url){this.state=value;navigation.push(String(url));}};
  const location={href:'https://site.test/community/?discussion=mine&page=2',pathname:'/community/',search:'?discussion=mine&page=2',assign:url=>navigation.push(url),reload:()=>navigation.push('reload')};
  const key=(id=state.owner)=>`chunnel.community.${id||0}`;
  const storage={get:(k,fallback)=>stored.get(k)??fallback,set:(k,v)=>stored.set(k,JSON.parse(JSON.stringify(v))),remove:k=>stored.delete(k)};
  const update=()=>{if(input.value)storage.set(key(),{content:input.value,pending:state.pending,at:Date.now()});else storage.remove(key());};
  const globals={crypto:require('node:crypto').webcrypto,URL,location,history,document:{createTextNode:text=>text}};
  const action=loadModule('frontend/shared/community-submit.ts',globals).createCommunitySubmission({
    state,input,submit,errorBox,key,storage,update,reveal(){},createIcon(){},showToast:text=>messages.push(text),run:fn=>fn(),signIn:fn=>signIn(fn),
    getSession:()=>gates.length?gates.shift().promise:Promise.resolve(session),api:(url,options)=>{const gate=defer();requests.push({url,...options,...gate});return gate.promise;},
  });
  return {state,input,errorBox,stored,key,requests,navigation,messages,history,location,update,submit:action,
    setSession:value=>{session=value;},signIn:fn=>{signIn=fn;},holdSession:()=>{const gate=defer();gates.push(gate);return gate;},
    switchTo(id,text='Next account draft'){update();state.generation++;state.owner=id;state.busy=false;state.pending=null;input.value=text;input.readOnly=false;},
    async start(status='publish'){const task=action(status);await tick();return {task};},
  };
}
const valid={content:{id:25,status:'publish',permalink:'/posts/confirmed'}};

test('uncertain writes retain the exact payload and key until a matching receipt',async()=>{
  const f=fixture(),first=await f.start();assert.equal(f.state.busy,true);assert.equal(f.input.readOnly,true);
  const request=f.requests[0];assert.equal(request.body.content_source,'<p>Original &lt;text&gt; &amp; a second line<br>🙂</p>');
  request.reject(new Error('Network disconnected'));await first.task;
  assert.equal(f.input.readOnly,false);assert.equal(f.state.busy,false);assert.match(f.errorBox.textContent,/disconnected/);
  const retry=await f.start();assert.deepEqual(f.requests[1].body,request.body);assert.deepEqual(f.requests[1].headers,request.headers);
  f.requests[1].resolve(valid);await retry.task;assert.equal(f.input.value,'');assert.equal(f.state.pending,null);
  assert.equal(f.stored.has(f.key()),false);assert.deepEqual(f.navigation,['https://site.test/community/#discussion-25']);
});
for(const [label,response] of [
  ['empty',{}],['wrong status',{content:{...valid.content,status:'draft'}}],['string id',{content:{...valid.content,id:'25'}}],
  ['zero id',{content:{...valid.content,id:0}}],['no url',{content:{id:25,status:'publish'}}],
  ['cross-origin',{content:{...valid.content,permalink:'https://other.test/posts/a'}}],['script URL',{content:{...valid.content,permalink:'javascript:alert(1)'}}],
  ['admin URL',{content:{...valid.content,permalink:'/admin/'}}],['null content',{content:null}],
])test(`invalid community acknowledgement (${label}) keeps draft and original nonce`,async()=>{
  const f=fixture(),{task}=await f.start(),nonce=f.state.pending.nonce;
  f.requests[0].resolve(response);await task;
  assert.equal(f.stored.get(f.key()).pending.nonce,nonce);assert.equal(f.input.value.startsWith('Original'),true);
  assert.equal(f.errorBox.hidden,false);assert.equal(f.state.busy,false);assert.deepEqual(f.navigation,[]);
});
for(const success of [false,true])test(`a late ${success?'success':'failure'} cannot touch another account composer`,async()=>{
  const f=fixture(),{task}=await f.start(),nonce=f.state.pending.nonce;
  f.switchTo(3);f.state.busy=true;f.input.readOnly=true;
  if(success)f.requests[0].resolve(valid);else f.requests[0].reject(new Error('Obsolete error'));await task;
  assert.equal(f.input.value,'Next account draft');assert.equal(f.state.busy,true);assert.equal(f.input.readOnly,true);
  assert.equal(f.stored.get(f.key(2)).pending.nonce,nonce);assert.deepEqual(f.navigation,[]);assert.equal(f.errorBox.hidden,true);
});
test('switching away and back still invalidates the original pending callback',async()=>{
  const f=fixture(),{task}=await f.start();f.switchTo(3);f.switchTo(2,'Original <text> & a second line\n🙂');
  f.requests[0].resolve(valid);await task;assert.notEqual(f.input.value,'');assert.deepEqual(f.navigation,[]);
});
test('duplicate clicks during session lookup lead to one write',async()=>{
  const f=fixture(),old=f.holdSession(),next=f.holdSession();const first=f.submit('publish'),second=f.submit('publish');
  next.resolve({authenticated:true,id:2,can_publish:true});await tick();assert.equal(f.requests.length,1);
  f.requests[0].resolve(valid);await second;
  old.resolve({authenticated:true,id:2,can_publish:true});await first;assert.equal(f.requests.length,1);
});
test('duplicate clicks while saving lead to one write',async()=>{
  const f=fixture(),{task}=await f.start();await f.submit('draft');await f.submit('publish');assert.equal(f.requests.length,1);f.requests[0].resolve(valid);await task;
});
test('permission rejection preserves input without issuing a write',async()=>{
  const f=fixture({canPublish:false});await f.submit('publish');assert.equal(f.requests.length,0);assert.match(f.errorBox.textContent,/发布权限/);assert.ok(f.stored.get(f.key()).content);
});
test('a confirmed draft opens the editor only after clearing its saved copy',async()=>{
  const f=fixture(),{task}=await f.start('draft');f.requests[0].resolve({id:42,status:'draft'});await task;
  assert.deepEqual(f.navigation,['/admin/content/42/edit/']);assert.equal(f.stored.has(f.key()),false);
});
test('changing status after failure uses a fresh key and keeps escaped text',async()=>{
  const f=fixture(),first=await f.start();f.requests[0].reject(new Error('lost'));await first.task;
  const next=await f.start('draft');assert.notEqual(f.requests[0].headers['Idempotency-Key'],f.requests[1].headers['Idempotency-Key']);
  assert.equal(f.requests[1].body.status,'draft');f.requests[1].resolve({id:42,status:'draft'});await next.task;
});
test('guest login keeps an existing account draft and sends nothing',async()=>{
  const f=fixture({owner:0});f.signIn(action=>{f.switchTo(2,'Existing account draft');return action();});
  await f.submit('publish');assert.equal(f.requests.length,0);assert.equal(f.input.value,'Existing account draft');assert.match(f.errorBox.textContent,/已有未提交/);
});
test('confirmed guest-to-account publish only removes its matching guest copy',async()=>{
  const f=fixture({owner:0});f.update();f.signIn(action=>{f.switchTo(2,'');return action();});
  const {task}=await f.start();f.requests[0].resolve(valid);await task;
  assert.equal(f.stored.has(f.key(0)),false);assert.equal(f.stored.has(f.key(2)),false);
});
test('a changed guest copy survives confirmation for the original draft',async()=>{
  const f=fixture({owner:0});f.signIn(action=>{f.switchTo(2,'');return action();});
  const {task}=await f.start();f.stored.set(f.key(0),{content:'New guest thought',at:Date.now()});f.requests[0].resolve(valid);await task;
  assert.equal(f.stored.get(f.key(0)).content,'New guest thought');
});
test('same-route publish preserves unrelated history before reload',async()=>{
  const f=fixture();f.location.search='';f.location.href='https://site.test/community/';const {task}=await f.start();f.requests[0].resolve(valid);await task;
  assert.equal(f.history.state.unrelated,true);assert.deepEqual(f.navigation,['https://site.test/community/#discussion-25','reload']);
});
