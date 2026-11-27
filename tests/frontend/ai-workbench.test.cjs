const test=require('node:test');
const assert=require('node:assert/strict');

class Element {
  constructor(tag){this.tag=tag;this.children=[];this.attributes={};this.className='';}
  append(...children){this.children.push(...children);}
  setAttribute(name,value){this.attributes[name]=value;}
  set textContent(value){this.children=[{tag:'#text',textContent:String(value)}];}
  get textContent(){return this.children.map(child=>child.textContent).join('');}
}
const doc={createElement:tag=>new Element(tag),createTextNode:text=>({tag:'#text',textContent:text})};
const descendants=elements=>elements.flatMap(el=>[el,...descendants(el.children||[])]);
const table='| 分类 | 数量 | 文章 ID |\n|---|---:|---|\n| 项目日志 | 3 | ID1、ID4、ID6 |\n| 主题设计 | 3 | ID2、ID5、ID8 |\n| 扩展架构 | 3 | ID3、ID7、ID9 |';

for(const path of ['../../internal/coreui/assets/js/chunnel-ai-workbench.source.js','../../internal/coreui/assets/js/chunnel-ai-workbench.js']){
  const api=require(path);
  test(`${path}: SSE handles every chunk boundary and CRLF without losing text`,()=>{
    const stream='event: delta\r\ndata: {"text":"中文"}\r\n\r\nevent: done\ndata: {"saved":true}\n\n';
    for(let split=1;split<stream.length;split++){
      const events=[],parser=api.createSSEParser((event,data)=>events.push([event,data]));
      parser.push(stream.slice(0,split));parser.push(stream.slice(split));parser.finish();
      assert.deepEqual(events,[['delta',{text:'中文'}],['done',{saved:true}]]);
    }
    const parser=api.createSSEParser(()=>{});parser.push('event: delta\ndata: {');assert.throws(()=>parser.finish());
    assert.throws(()=>api.createSSEParser(()=>{}).push('event: delta\ndata: invalid\n\n'));
  });
  test(`${path}: hostile report content remains text with an allowlisted block type`,()=>{
    const input='# 标题\n\n<img src=x onerror=alert(1)>\n\n- **事实**\n- [危险](javascript:alert(1))\n\n```html\n<script>alert(1)</script>\n```';
    const blocks=api.parseMarkdown(input);
    assert.deepEqual(blocks.map(b=>b.kind),['h2','p','ul','pre']);
    assert.equal(blocks[1].text,'<img src=x onerror=alert(1)>');
    assert.equal(blocks[3].text,'<script>alert(1)</script>');
  });
  test(`${path}: category report becomes a semantic table with numeric column alignment`,()=>{
    const blocks=api.parseMarkdown(table.replace(/\n/g,'\r\n'));
    assert.deepEqual(blocks,[{kind:'table',headers:['分类','数量','文章 ID'],align:['left','right','left'],rows:[['项目日志','3','ID1、ID4、ID6'],['主题设计','3','ID2、ID5、ID8'],['扩展架构','3','ID3、ID7、ID9']]}]);
    const nodes=descendants(api.renderMarkdown(table,doc));
    assert.equal(nodes.filter(el=>el.tag==='table').length,1);
    assert.deepEqual(nodes.filter(el=>el.tag==='th').map(el=>[el.textContent,el.attributes.scope,el.attributes['data-align']]),[['分类','col','left'],['数量','col','right'],['文章 ID','col','left']]);
    assert.equal(nodes.filter(el=>el.tag==='td').length,9);
    assert.equal(nodes[0].attributes.tabindex,'0');
    assert.equal(nodes[0].attributes['aria-label'],'报告表格');
  });
  test(`${path}: tables accept optional outer pipes, escapes, code and uneven rows`,()=>{
    const input='分类 | 数量 | 说明\n:--- | ---: | :---:\n**项目** | 3 | `a|b` 和 a\\|b\n只有分类\n主题 | 4 | 正常 | 忽略多余列\n\n后续正文';
    const blocks=api.parseMarkdown(input);
    assert.deepEqual(blocks[0].align,['left','right','center']);
    assert.deepEqual(blocks[0].rows,[['**项目**','3','`a|b` 和 a\\|b'],['只有分类','',''],['主题','4','正常']]);
    assert.deepEqual(blocks[1],{kind:'p',text:'后续正文'});
    const nodes=descendants(api.renderMarkdown(input,doc));
    assert.equal(nodes.find(el=>el.tag==='strong').textContent,'项目');
    assert.equal(nodes.find(el=>el.tag==='code').textContent,'a|b');
    assert.equal(nodes.filter(el=>el.tag==='td')[2].textContent,'a|b 和 a|b');
  });
  test(`${path}: incomplete tables, invalid delimiters and fenced code keep their text`,()=>{
    for(const input of ['a | b\n--- | --','a | b\n--- | --- | ---','|\n|'])assert.equal(api.parseMarkdown(input)[0].kind,'p');
    for(const fence of ['```','~~~~']){
      const blocks=api.parseMarkdown(`${fence}markdown\n${table}\n${fence}\n\n结尾`);
      assert.deepEqual(blocks,[{kind:'pre',text:table},{kind:'p',text:'结尾'}]);
    }
    assert.deepEqual(api.parseMarkdown('````md\n```\n'+table),[{kind:'pre',text:'```\n'+table}]);
    // Every partial stream frame remains renderable until its delimiter completes.
    for(let end=0;end<=table.length;end++)assert.doesNotThrow(()=>api.renderMarkdown(table.slice(0,end),doc));
    assert.deepEqual(api.parseMarkdown(table+'\n## 下一节\n正文').map(block=>block.kind),['table','h3','p']);
  });
  test(`${path}: quotes, rules, task lists and original ordered numbering render`,()=>{
    const input='> **提醒**\n>\n> '+table.replace(/\n/g,'\n> ')+'\n\n---\n\n- [x] 核对数据\n- [ ] 更新报告\n\n3. 第三项\n4. 第四项\n\n小标题\n===\n\n# C#';
    const nodes=descendants(api.renderMarkdown(input,doc));
    assert.equal(nodes.filter(el=>el.tag==='blockquote').length,1);
    assert.equal(nodes.filter(el=>el.tag==='table').length,1);
    assert.equal(nodes.filter(el=>el.tag==='hr').length,1);
    assert.deepEqual(nodes.filter(el=>el.tag==='input').map(el=>[el.type,el.checked,el.disabled]),[['checkbox',true,true],['checkbox',false,true]]);
    assert.equal(nodes.find(el=>el.tag==='ol').attributes.start,'3');
    assert.deepEqual(nodes.filter(el=>el.tag==='h2').map(el=>el.textContent),['小标题','C#']);
  });
  test(`${path}: inline emphasis, strikeout, code, escapes and safe links retain meaning`,()=>{
    const input='**重点**、__强调__、*斜体*、_斜体_、~~旧建议~~、`**代码**`、\\*原样\\*、article_id_value、[**资料**](https://example.test/report?q=1&n=2 "来源")、[后台](/admin/)、[定位](#details)';
    const nodes=descendants(api.renderMarkdown(input,doc));
    assert.deepEqual(nodes.filter(el=>el.tag==='strong').map(el=>el.textContent),['重点','强调','资料']);
    assert.deepEqual(nodes.filter(el=>el.tag==='em').map(el=>el.textContent),['斜体','斜体']);
    assert.equal(nodes.find(el=>el.tag==='del').textContent,'旧建议');
    assert.equal(nodes.find(el=>el.tag==='code').textContent,'**代码**');
    assert.deepEqual(nodes.filter(el=>el.tag==='a').map(el=>el.attributes.href),['https://example.test/report?q=1&n=2','/admin/','#details']);
    assert.equal(nodes.find(el=>el.tag==='a').attributes.title,'来源');
    assert.match(nodes[0].textContent,/\*原样\*、article_id_value/);
  });
  test(`${path}: untrusted table cells and links cannot mount HTML or load remote images`,()=>{
    const hostile='| 内容 | 引用 |\n|---|---|\n| <svg onload=alert(1)> | [危险](javascript:alert(1)) |\n| <img src=x onerror=alert(1)> | ![图片](https://tracker.test/pixel) |\n| [编码](javascript%3Aalert) | [跨站](//evil.test) |\n| [数据](data:text/html,evil) | [反斜线](/\\evil.test) |\n| [控制符](java\u0000script:evil) | <script>alert(1)</script> |';
    const nodes=descendants(api.renderMarkdown(hostile,doc));
    assert.equal(nodes.filter(el=>['script','img','svg','iframe','a'].includes(el.tag)).length,0);
    assert.match(nodes[0].textContent,/<svg onload=alert\(1\)>/);
    assert.match(nodes[0].textContent,/!\[图片\]\(https:\/\/tracker.test\/pixel\)/);
    assert(nodes.every(el=>Object.keys(el.attributes||{}).every(key=>!key.startsWith('on'))));
  });
  test(`${path}: exports identify partial results and preserve evidence period`,()=>{
    const value=api.reportMarkdown({title:'周报',content:'结果',model:'fixture',created_at:'2026-11-17',source:{start:'2026-11-11',end:'2026-11-17',timezone:'Asia/Shanghai',scope:'聚合数据'}},false);
    assert.match(value,/未完成/);assert.match(value,/Asia\/Shanghai/);assert.match(value,/2026-11-11/);assert.match(value,/聚合数据/);
  });
  test(`${path}: report filters combine type and case-insensitive query`,()=>{
    const reports=[{task:'costs',title:'成本',model:'MODEL-X',created_at:'2026-11-17'},{task:'summary',title:'总结',model:'MODEL-X',created_at:'2026-11-16'}];
    assert.deepEqual(api.filterReports(reports,'model-x','costs'),[reports[0]]);assert.deepEqual(api.filterReports(reports,'2026-11-16','summary'),[reports[1]]);assert.equal(api.filterReports(reports,'missing','').length,0);
  });
}
