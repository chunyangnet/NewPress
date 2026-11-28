const test = require('node:test');
const assert = require('node:assert/strict');
const {chromium} = require('playwright');
const {buildSync} = require('esbuild');
const path = require('node:path');
const {readFileSync} = require('node:fs');
const root = path.resolve(__dirname,'../..');
const codecBundle=buildSync({entryPoints:[path.join(root,'frontend/core/editor-document.ts')],bundle:true,format:'iife',globalName:'DocumentCodec',write:false}).outputFiles[0].text;
const generated=process.env.NEWPRESS_DOCUMENT_EDITOR_GENERATED==='1';
const editorBundle=generated?readFileSync(path.join(root,'internal/coreui/assets/js/newpress-document-editor.js'),'utf8'):buildSync({entryPoints:[path.join(root,'frontend/core/document-editor.ts')],bundle:true,format:'iife',write:false}).outputFiles[0].text;

test(`document editor (${generated?'generated':'typescript'}): real browser editing and lossless source contract`, {timeout:60000}, async t => {
  const browser=await chromium.launch({headless:true,...(process.platform==='win32'?{channel:'msedge'}:{})});
  t.after(()=>browser.close());
  const page=await browser.newPage();
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.route('https://newpress.test/document-editor/',route=>route.fulfill({contentType:'text/html; charset=utf-8',body:'<form data-content-editor data-document-editor><div data-composer-toolbar><button type="button" data-document-command="bold">粗体</button><button type="button" data-composer-undo>撤销</button><button type="button" data-composer-redo>重做</button><button type="button" data-composer-mode="source">源码</button><button type="button" data-composer-mode="blocks">可视编辑</button><button type="button" data-block-add="table">表格</button></div><div data-composer-source-pane><textarea data-content-source></textarea></div><div data-composer><b data-composer-count></b><div data-composer-list></div></div><div data-document-table-tools><button type="button" data-document-command="row-add">加行</button><button type="button" data-document-command="column-add">加列</button><button type="button" data-document-command="table-delete">删除表格</button></div><p data-composer-feedback></p></form>'}));
  await page.goto('https://newpress.test/document-editor/');
  await page.addScriptTag({content:codecBundle});
  await page.addScriptTag({content:editorBundle});
  // Article editing starts from its own entry without loading the shop renderer.
  const editor=page.locator('.newpress-document'), source=()=>page.locator('[data-content-source]').inputValue();
  const reset=html=>page.evaluate(value=>window.ChunnelBlockEditor.reset(value),html);

  await t.test('a pristine empty document accepts a pinned snippet and undo restores emptiness',async()=>{
    const markup='<aside data-np-snippet="7" data-np-snippet-version="3"></aside>';
    await reset('');
    const range=await page.evaluate(()=>window.ChunnelBlockEditor.capture());
    assert.deepEqual(range,{start:0,end:0,value:''});
    await page.evaluate(html=>{const api=window.ChunnelBlockEditor;api.insert(html,api.capture());},markup);
    assert.equal(await source(),markup);
    assert.equal(await editor.locator('.document-preserved strong').textContent(),'共享片段 · 保留固定版本引用');
    await page.getByRole('button',{name:'撤销',exact:true}).click();assert.equal(await source(),'');
  });

  await t.test('untouched markup roundtrips exactly; editing one block preserves its neighbors',async()=>{
    const result=await page.evaluate(()=>{
      const {documentSchema,createDocumentCodec}=DocumentCodec,schema=documentSchema(),codec=createDocumentCodec(schema,document);
      const values=['\n<p>中文 <b>旧粗体</b> &amp; 空格</p>\n<!--备注--><div class="widget">保留</div>\n','<p><a href="https://example.test">链接</a></p>','<p class="legacy">旧格式</p>','<p>broken<div>nest</div>','<table><tr><td>单元格</td></tr></table>','<figure><img src="/media/test.png" alt="说明" width="20" height="10"><figcaption>图注</figcaption></figure>'];
      return values.map(value=>({value,result:codec.serialize(codec.parse(value))}));
    });
    result.forEach(({value,result})=>assert.equal(result,value));
    assert.equal(await page.evaluate(()=>{const schema=DocumentCodec.documentSchema(),codec=DocumentCodec.createDocumentCodec(schema,document);return codec.parse('<table><tbody><tr><td colspan="99999">large span</td></tr></tbody></table>').firstChild.type.name;}),'raw_block');
    await reset('<p>正文</p>\n<!--备注--><div class="legacy">旧内容</div>\n');
    await editor.locator('p').first().click();await page.keyboard.press('End');await page.keyboard.insertText('新增');
    assert.equal(await source(),'<p>正文新增</p>\n<!--备注--><div class="legacy">旧内容</div>\n');
    assert.equal(await editor.locator('.legacy').count(),0);
  });
  await t.test('continuous paragraphs, inline formatting, cross-paragraph deletion and undo',async()=>{
    await reset('');await editor.click();await page.keyboard.insertText('第一段');await page.keyboard.press('Enter');await page.keyboard.insertText('第二段');await page.keyboard.press('Shift+Enter');await page.keyboard.insertText('换行');
    assert.equal(await source(),'<p>第一段</p><p>第二段<br>换行</p>');
    await page.keyboard.press('Control+a');await page.getByRole('button',{name:'粗体',exact:true}).click();
    assert.match(await source(),/<strong>第一段<\/strong>/);
    await editor.focus();await page.keyboard.press('Control+a');await page.keyboard.press('Backspace');assert.equal(await source(),'');
    await page.keyboard.press('Control+z');assert.match(await source(),/第一段/);assert.match(await source(),/第二段/);
  });
  await t.test('Chinese composition commits once without replacing the editable paragraph',async()=>{
    await reset('<p>开始</p>');await editor.locator('p').click();await page.keyboard.press('End');
    await page.evaluate(()=>{window.originalParagraph=document.querySelector('.newpress-document p');window.compositionEmits=0;document.querySelector('[data-content-source]').addEventListener('input',()=>window.compositionEmits++);});
    const cdp=await page.context().newCDPSession(page);
    await cdp.send('Input.imeSetComposition',{text:'中文',selectionStart:2,selectionEnd:2});
    assert.equal(await source(),'<p>开始</p>');
    await cdp.send('Input.insertText',{text:'中文'});
    await page.waitForFunction(()=>document.querySelector('[data-content-source]').value.includes('中文'));
    assert.equal(await source(),'<p>开始中文</p>');
    assert.equal(await page.evaluate(()=>window.originalParagraph===document.querySelector('.newpress-document p')),true);
    assert.equal(await page.evaluate(()=>window.compositionEmits),1);await cdp.detach();
  });
  await t.test('table actions outside the formatter update the current table',async()=>{
    await reset('');await editor.click();await page.getByRole('button',{name:'表格',exact:true}).click();
    assert.equal(await editor.locator('tr').count(),3);
    await page.getByRole('button',{name:'加行',exact:true}).click();assert.equal(await editor.locator('tr').count(),4);
    await page.getByRole('button',{name:'加列',exact:true}).click();assert.equal(await editor.locator('tr').first().locator('th,td').count(),3);
    await page.getByRole('button',{name:'删除表格',exact:true}).click();assert.equal(await editor.locator('table').count(),0);
    await page.getByRole('button',{name:'撤销',exact:true}).click();assert.equal(await editor.locator('table').count(),1);
  });
  await t.test('delayed insertions and AI suggestions cannot overwrite a changed document',async()=>{
    await reset('<p>原始文字</p>');await editor.click();await page.keyboard.press('Control+a');
    await page.evaluate(()=>{window.oldInsertion=ChunnelBlockEditor.capture();window.oldText=ChunnelBlockEditor.captureText();});
    await page.keyboard.press('End');await page.keyboard.insertText('修改');
    const result=await page.evaluate(()=>{let rejected=false;try{ChunnelBlockEditor.insert('<p>插入</p>',window.oldInsertion);}catch{rejected=true;}return {rejected,applied:ChunnelBlockEditor.replaceText('替换',window.oldText)};});
    assert.deepEqual(result,{rejected:true,applied:false});assert.match(await source(),/修改/);
  });
  await t.test('rich clipboard paste strips active content and outside images',async()=>{
    await reset('');await editor.click();
    await editor.evaluate(element=>{const clipboardData=new DataTransfer();clipboardData.setData('text/html','<p>粘贴 <strong>粗体</strong><script>window.pasteExecuted=true</script><img src="https://outside.invalid/image.png" onerror="window.pasteExecuted=true"></p>');element.dispatchEvent(new ClipboardEvent('paste',{clipboardData,bubbles:true,cancelable:true}));});
    assert.match(await source(),/<strong>粗体<\/strong>/);assert.doesNotMatch(await source(),/script|outside\.invalid|onerror/);
    assert.equal(await page.evaluate(()=>!!window.pasteExecuted),false);
  });
  await t.test('media insertion preserves the figure and remains undoable',async()=>{
    await reset('<p>图片之前</p>');await editor.click();
    const markup='<figure><img src="/media/example.png" alt="本地图片" width="640" height="480"><figcaption>说明文字</figcaption></figure>';
    await page.evaluate(html=>{const api=ChunnelBlockEditor;api.insert(html,api.capture());},markup);
    assert.equal(await editor.locator('figure img').getAttribute('alt'),'本地图片');
    assert.equal(await editor.locator('figcaption').textContent(),'说明文字');assert.ok((await source()).includes(markup));
    await page.getByRole('button',{name:'撤销',exact:true}).click();assert.equal(await source(),'<p>图片之前</p>');
  });
  await t.test('HTML clipboard images keep same-origin media and undo while rejecting remote or active content',async()=>{
    await reset('<p>图片之前</p>');await editor.click();await page.keyboard.press('Control+End');
    await editor.evaluate(node=>{
      const data=new DataTransfer();
      data.setData('text/html','<p><img src="https://newpress.test/media/7/copied.png" alt="同站图片"><img src="https://outside.test/media/7/remote.png"><img src="https://newpress.test/admin/private.png"></p>');
      node.dispatchEvent(new ClipboardEvent('paste',{clipboardData:data,bubbles:true,cancelable:true}));
    });
    assert.equal(await editor.locator('img:not(.ProseMirror-separator)').count(),1);
    assert.equal(await editor.locator('img:not(.ProseMirror-separator)').getAttribute('src'),'/media/7/copied.png');
    assert.ok(!(await source()).includes('outside.test'));
    await page.getByRole('button',{name:'撤销',exact:true}).click();assert.equal(await source(),'<p>图片之前</p>');
  });
  await t.test('source and visual modes share one body; read-only blocks edits',async()=>{
    await page.getByRole('button',{name:'源码',exact:true}).click();await page.locator('[data-content-source]').fill('<p>来自源码 <em>斜体</em></p>');
    await page.getByRole('button',{name:'可视编辑',exact:true}).click();assert.equal(await editor.locator('em').textContent(),'斜体');
    await page.evaluate(()=>{document.querySelector('[data-content-source]').disabled=true;});
    await page.waitForFunction(()=>document.querySelector('.newpress-document').contentEditable==='false');
    assert.equal(await page.getByRole('button',{name:'粗体',exact:true}).isDisabled(),true);
  });
  assert.deepEqual(errors,[]);
});
