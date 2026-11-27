const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const {compile}=require('./helpers/typescript.cjs');
const {environment,Element}=require('./helpers/editor-tools.cjs');
const plan={source:{id:4,version:2},type:'post',title:'独立草稿',excerpt:'summary',cover_url:'',content_html:'<p>pinned body</p>',digest:'a'.repeat(64),occurrences:1,references:[{id:9,version:1,snippet_version:2,title:'shared'}]};
const session={authenticated:true,read_only:false,user_id:7,username:'editor',csrf_token:'renewed',capabilities:['content.read','content.create']};
function fixture(variant,store=new Map()){
  const f=environment(),form=new Element('form'),launcher=new Element('button'),dialog=new Element('dialog');
  form.dataset={editorUserId:'7',editorLogin:'editor'};launcher.dataset.post='4';
  f.document.selectors.set('[data-content-editor]',form);f.document.selectors.set('[data-snippet-draft-dialog]',dialog);form.selectors.set('[data-snippet-draft-open]',launcher);
  const version=new Element('input');version.value='2';form.selectors.set('[name="version"]',version);
  const nodes={};for(const [k,tag]of Object.entries({status:'p',preview:'button',confirm:'button',consent:'input',content:'div',result:'a',frame:'iframe',title:'h3',details:'p',references:'ul',source:'pre',close:'button'})){nodes[k]=new Element(tag);dialog.selectors.set(`[data-snippet-draft-${k}]`,nodes[k]);}
  f.globals.sessionStorage={getItem:k=>store.get(k)||null,setItem:(k,v)=>store.set(k,v),removeItem:k=>store.delete(k)};
  const code=variant==='generated'?fs.readFileSync('internal/coreui/assets/js/newpress-editor-snippets.js','utf8'):compile('frontend/core/editor-snippet-draft.ts');vm.runInNewContext(code,f.globals);
  return {...f,form,launcher,dialog,nodes,store,version,open(){launcher.click();},async preview(){nodes.preview.click();await f.reply(0,session);await f.reply(1,plan);},consent(){nodes.consent.checked=true;nodes.consent.emit('change');},confirm(){nodes.confirm.click();}};
}
for(const variant of ['TypeScript','generated']){
  test(`${variant}: rejected preview explains field errors as text and leaves creation disabled`,async()=>{
    for(const [details,expected] of [
      [{fields:{content_source:'原文章没有共享片段引用',invalid:123}},'请检查正文和预览请求：原文章没有共享片段引用'],
      [{fields:{content_source:'<aside> 引用没有闭合'}},'请检查正文和预览请求：<aside> 引用没有闭合'],
      [{fields:['unexpected']},'请检查正文和预览请求'],
    ]){
      const f=fixture(variant);f.open();f.nodes.preview.click();await f.reply(0,session);
      await f.reply(1,{error:{code:'snippet_draft.invalid_input',message:'请检查正文和预览请求',details}},422);
      assert.equal(f.nodes.status.textContent,expected);assert.equal(f.nodes.confirm.disabled,true);assert.equal(f.nodes.preview.disabled,false);assert.equal(f.store.size,0);
    }
  });
  test(`${variant}: preview, explicit consent, exact confirmation and safe new-window result`,async()=>{
    const f=fixture(variant);f.open();assert.equal(f.nodes.confirm.disabled,true);await f.preview();assert.equal(f.requests.length,2);assert.equal(f.nodes.confirm.disabled,true);
    assert.match(f.nodes.source.textContent,/pinned body/);assert.match(f.nodes.frame.srcdoc,/default-src 'none'/);f.consent();f.confirm();await f.reply(2,session);
    const body=JSON.parse(f.requests[3].init.body);assert.equal(body.version,2);assert.match(body.request_key,/^[a-f0-9]{64}$/);assert.equal(f.requests[3].init.headers['X-CSRF-Token'],'renewed');assert.equal(f.store.size,1);
    await f.reply(3,{id:11,version:1,request_key:body.request_key,replayed:false,editor_url:'/admin/content/11/edit/'},201);
    assert.equal(f.nodes.result.href,'/admin/content/11/edit/');assert.equal(f.store.size,0);assert.equal(f.dialog.open,true);assert.equal(f.version.value,'2');
  });
  test(`${variant}: dropped response keeps identical request across refresh and renewed CSRF`,async()=>{
    const f=fixture(variant);f.open();await f.preview();f.consent();f.confirm();await f.reply(2,session);const body=f.requests[3].init.body;f.requests[3].reject(Error('lost response'));await f.settle();
    assert.equal(f.nodes.preview.disabled,true);assert.match(f.nodes.confirm.textContent,/核对/);
    const g=fixture(variant,f.store);g.open();g.confirm();await g.reply(0,{...session,csrf_token:'new-session'});assert.equal(g.requests[1].init.body,body);
    const key=JSON.parse(body).request_key;await g.reply(1,{id:11,version:1,request_key:key,replayed:true,editor_url:'/admin/content/11/edit/'},201);assert.equal(g.store.size,0);
  });
  test(`${variant}: different account, stale preview and malformed success never cause fresh duplicate`,async()=>{
    const f=fixture(variant);f.open();await f.preview();f.consent();f.confirm();await f.reply(2,{...session,user_id:8});assert.equal(f.requests.length,3);assert.match(f.nodes.status.textContent,/账号/);
    f.confirm();await f.reply(3,session);await f.reply(4,{error:{code:'snippet_draft.version_conflict',message:'已变化'}},412);assert.equal(f.nodes.preview.disabled,false);assert.equal(f.nodes.confirm.disabled,true);assert.equal(f.store.size,0);
    const g=fixture(variant);g.open();await g.preview();g.consent();g.confirm();await g.reply(2,session);await g.reply(3,{id:12,version:1,request_key:'x',replayed:false,editor_url:'https://attacker.test/'},201);
    assert.equal(g.nodes.result.href,undefined);assert.equal(g.nodes.preview.disabled,true);assert.equal(g.store.size,1);
  });
  test(`${variant}: uncertainty survives later definitive-looking errors and pagehide`,async()=>{
    const f=fixture(variant);f.open();await f.preview();f.consent();f.confirm();await f.reply(2,session);f.requests[3].reject(Error('lost'));await f.settle();const body=f.requests[3].init.body;
    f.confirm();await f.reply(4,session);await f.reply(5,{error:{code:'snippet_draft.version_conflict',message:'conflict'}},412);assert.equal(f.nodes.preview.disabled,true);assert.equal(f.requests[5].init.body,body);
    f.confirm();await f.reply(6,session);f.window.emit('pagehide');await f.reply(7,{id:11,version:1,request_key:JSON.parse(body).request_key,replayed:true,editor_url:'/admin/content/11/edit/'},201);assert.equal(f.nodes.result.href,undefined);assert.equal(f.store.size,1);
  });
  test(`${variant}: unavailable session storage prevents an unrecoverable creation`,async()=>{
    const f=fixture(variant);f.open();await f.preview();f.consent();f.globals.sessionStorage.setItem=()=>{throw Error('blocked')};f.confirm();await f.reply(2,session);
    assert.equal(f.requests.length,3);assert.match(f.nodes.status.textContent,/会话存储/);
  });
}
