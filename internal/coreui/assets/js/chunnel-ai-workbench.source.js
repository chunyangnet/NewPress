(() => {
  'use strict';
  function tableCells(line) {
    const value=line.trim(),cells=[];let cell='',code='',ended=false;
    for(let i=0;i<value.length;i++){
      const char=value[i];ended=false;
      if(char==='\\'&&i+1<value.length){cell+=char+value[++i];continue;}
      if(char==='`'){
        const run=value.slice(i).match(/^`+/)[0];
        if(!code)code=run;else if(code===run)code='';
        cell+=run;i+=run.length-1;continue;
      }
      if(char==='|'&&!code){cells.push(cell.trim());cell='';ended=true;}
      else cell+=char;
    }
    cells.push(cell.trim());
    if(value.startsWith('|'))cells.shift();
    if(ended)cells.pop();
    return cells;
  }
  const fenceStart=line=>line.match(/^ {0,3}(`{3,}|~{3,})(.*)$/);
  const rule=line=>/^ {0,3}(?:\*[ \t]*){3,}$|^ {0,3}(?:-[ \t]*){3,}$|^ {0,3}(?:_[ \t]*){3,}$/.test(line);
  function parseMarkdown(text,depth=0) {
    const input=String(text).replace(/\r\n?/g,'\n').split('\n'),blocks=[];
    let lines=[],kind='p',start=1;
    const flush=()=>{if(lines.length)blocks.push({kind,text:lines.join('\n'),...(kind==='ol'?{start}:{})});lines=[];kind='p';};
    for(let i=0;i<input.length;i++){
      const line=input[i],fence=fenceStart(line);
      if(fence){
        flush();const code=[];
        while(++i<input.length){
          const closing=input[i].match(/^ {0,3}(`+|~+)[ \t]*$/);
          if(closing&&closing[1][0]===fence[1][0]&&closing[1].length>=fence[1].length)break;
          code.push(input[i]);
        }
        blocks.push({kind:'pre',text:code.join('\n')});continue;
      }
      if(!line.trim()){flush();continue;}
      const heading=line.match(/^ {0,3}(#{1,6})\s+(.+)$/);
      if(heading){flush();blocks.push({kind:`h${Math.min(heading[1].length+1,6)}`,text:heading[2].replace(/\s+#+\s*$/,'').trim()});continue;}
      if(/^ {0,3}>/.test(line)&&depth<8){
        flush();const quote=[];
        while(i<input.length&&/^ {0,3}>/.test(input[i]))quote.push(input[i++].replace(/^ {0,3}> ?/,''));
        i--;blocks.push({kind:'blockquote',blocks:parseMarkdown(quote.join('\n'),depth+1)});continue;
      }
      if(rule(line)){flush();blocks.push({kind:'hr'});continue;}
      const headers=tableCells(line),delimiter=i+1<input.length?tableCells(input[i+1]):[];
      if(headers.length&&(line.includes('|')||(input[i+1]||'').includes('|'))&&headers.length===delimiter.length&&delimiter.every(cell=>/^:?-{3,}:?$/.test(cell))){
        flush();const align=delimiter.map(cell=>cell.startsWith(':')?(cell.endsWith(':')?'center':'left'):(cell.endsWith(':')?'right':'left')),rows=[];
        i++;
        while(i+1<input.length&&input[i+1].trim()&&!/^ {0,3}(?:>|#{1,6}\s|[-+*]\s|\d+[.)]\s)/.test(input[i+1])&&!fenceStart(input[i+1])&&!rule(input[i+1])){
          const cells=tableCells(input[++i]);rows.push(headers.map((_,column)=>cells[column]||''));
        }
        blocks.push({kind:'table',headers,align,rows});continue;
      }
      const bullet=line.match(/^\s*[-*+]\s+(.+)$/),ordered=line.match(/^\s*(\d+)[.)]\s+(.+)$/);
      if(bullet||ordered){
        const next=bullet?'ul':'ol';if(kind!==next)flush();
        if(!lines.length&&ordered)start=Number(ordered[1]);
        kind=next;lines.push(bullet?bullet[1]:ordered[2]);continue;
      }
      if(i+1<input.length&&/^ {0,3}(?:=+|-+)[ \t]*$/.test(input[i+1])){
        flush();blocks.push({kind:input[++i].trim()[0]==='='?'h2':'h3',text:line.trim()});continue;
      }
      if(kind!=='p')flush();lines.push(line);
    }
    flush();return blocks;
  }
  function safeLink(value) {
    if(/[\s\u0000-\u001f\u007f\\]/.test(value))return '';
    if(/^\/(?!\/)|^#/.test(value))return value;
    if(!/^(?:https?:\/\/|mailto:)/i.test(value))return '';
    try{const url=new URL(value);return url.protocol==='mailto:'?value:(url.hostname?value:'');}catch{return '';}
  }
  function parseInline(text,depth=0,links=true) {
    if(depth>=8)return [{kind:'text',text}];
    // Tokens never contain HTML. Code is recognized before formatting, and link
    // destinations pass a scheme allowlist before becoming DOM attributes.
    const regex=/\\([\\`*{}\[\]()#+\-.!_>|~])|(`+)([\s\S]*?)\2(?!`)|(!?)\[([^\]\n]*)\]\(([^ \t\n]*?)(?:[ \t]+"([^"\n]*)")?\)|(\*\*|__|~~)(?=\S)([^\n]*?\S)\8|(\*|_)(?=\S)([^\n]*?\S)\10/g;
    const tokens=[];let last=0,match;
    while((match=regex.exec(text))){
      if(match.index>last)tokens.push({kind:'text',text:text.slice(last,match.index)});
      if(match[1])tokens.push({kind:'text',text:match[1]});
      else if(match[2])tokens.push({kind:'code',text:match[3].replace(/\n/g,' ')});
      else if(match[4]!==undefined){
        const href=!match[4]&&links?safeLink(match[6]):'';
        tokens.push(href?{kind:'a',href,title:match[7]||'',children:parseInline(match[5],depth+1,false)}:{kind:'text',text:match[0]});
      }else{
        const marker=match[8]||match[10],index=match.index;
        // Underscores inside identifiers are literal, as in article_id_value.
        if(marker.includes('_')&&/[\p{L}\p{N}]/u.test(text[index-1]||'')&&/[\p{L}\p{N}]/u.test(text[regex.lastIndex]||''))tokens.push({kind:'text',text:match[0]});
        else tokens.push({kind:marker==='~~'?'del':marker.length===2?'strong':'em',children:parseInline(match[9]||match[11],depth+1,links)});
      }
      last=regex.lastIndex;
    }
    if(last<text.length)tokens.push({kind:'text',text:text.slice(last)});
    return tokens;
  }
  function renderMarkdown(text,doc) {
    const create=(tag,text,cls)=>{const el=doc.createElement(tag);if(text!=null)el.textContent=text;if(cls)el.className=cls;return el;};
    function inline(parent,text){
      const append=(target,tokens)=>{for(const token of tokens){
        if(token.kind==='text'){target.append(doc.createTextNode(token.text));continue;}
        const el=create(token.kind,token.text);
        if(token.kind==='a'){el.setAttribute('href',token.href);el.setAttribute('rel','noopener noreferrer');if(token.title)el.setAttribute('title',token.title);}
        if(token.children)append(el,token.children);target.append(el);
      }};
      append(parent,parseInline(text));
    }
    function render(block){
      if(block.kind==='table'){
        const wrapper=create('div',null,'aiw-table-scroll'),table=create('table'),head=create('thead'),row=create('tr'),body=create('tbody');
        wrapper.setAttribute('role','region');wrapper.setAttribute('aria-label','报告表格');wrapper.setAttribute('tabindex','0');
        block.headers.forEach((text,index)=>{const cell=create('th');cell.setAttribute('scope','col');cell.setAttribute('data-align',block.align[index]);inline(cell,text);row.append(cell);});
        head.append(row);
        for(const cells of block.rows){const row=create('tr');cells.forEach((text,index)=>{const cell=create('td');cell.setAttribute('data-align',block.align[index]);inline(cell,text);row.append(cell);});body.append(row);}
        table.append(head,body);wrapper.append(table);return wrapper;
      }
      const el=create(block.kind);
      if(block.kind==='blockquote')el.append(...block.blocks.map(render));
      else if(block.kind==='ul'||block.kind==='ol'){
        if(block.kind==='ol'&&block.start!==1)el.setAttribute('start',String(block.start));
        for(const text of block.text.split('\n')){
          const item=create('li'),task=text.match(/^\[([ xX])\]\s+(.*)$/);
          if(task){const check=create('input',null,'aiw-task-check');check.type='checkbox';check.disabled=true;check.checked=task[1]!==' ';check.setAttribute('aria-label',check.checked?'已完成':'未完成');item.className='aiw-task-item';item.append(check);inline(item,task[2]);}
          else inline(item,text);
          el.append(item);
        }
      }else if(block.kind==='pre')el.append(create('code',block.text));
      else if(block.text)inline(el,block.text);
      return el;
    }
    return parseMarkdown(text).map(render);
  }
  function createSSEParser(onEvent) {
    let pending='';
    return {
      push(chunk){
        pending+=chunk;
        if(pending.length>1048576)throw new Error('响应片段过大，请重试。');
        let match;
        while((match=/\r?\n\r?\n/.exec(pending))){
          const frame=pending.slice(0,match.index);pending=pending.slice(match.index+match[0].length);
          let event='message';const data=[];
          for(const line of frame.split(/\r?\n/)){if(line.startsWith('event:'))event=line.slice(6).trim();if(line.startsWith('data:'))data.push(line.slice(5).replace(/^ /,''));}
          if(data.length)onEvent(event,JSON.parse(data.join('\n')));
        }
      },
      finish(){if(pending.trim())throw new Error('连接提前结束，已保留收到的文字。');}
    };
  }
  function reportMarkdown(report, complete=true) {
    const source=report.source;
    return `# ${report.title||'AI 报告'}\n\n${complete?'':'> 未完成或未保存的报告，请核对后使用。\n\n'}生成时间：${report.created_at||''}\n\n模型：${report.model||''}\n\n${source?`范围：${source.start} 至 ${source.end}（${source.timezone}）\n\n资料：${source.scope}\n\n`:''}${report.content||''}\n\n---\nAI 分析仅供参考，操作前请核对。\n`;
  }
  function filterReports(reports,query,task) {
    const q=String(query||'').trim().toLocaleLowerCase();
    return reports.filter(r=>(!task||r.task===task)&&(!q||`${r.title} ${r.model} ${r.created_at} ${r.provider}`.toLocaleLowerCase().includes(q)));
  }
  if(typeof module!=='undefined'&&module.exports)module.exports={parseMarkdown,parseInline,renderMarkdown,createSSEParser,reportMarkdown,filterReports};
  if(typeof document==='undefined')return;
  const root=document.querySelector('[data-ai-workbench]');if(!root)return;
  const $=name=>root.querySelector(`[data-aiw-${name}]`);
  const state={tasks:[],providers:[],reports:[],task:'operations',preview:null,source:null,current:null,text:'',busy:false,loading:true,readonly:root.dataset.readOnly==='true',controller:null,started:0,complete:false,deleteID:'',disposed:false};
  const requests=new Set();const base='/api/v1/admin/ai/workbench';
  let paint=0,clock=0;
  const node=(tag,text,cls)=>{const n=document.createElement(tag);if(text!=null)n.textContent=text;if(cls)n.className=cls;return n;};
  const icon=name=>{const svg=document.createElementNS('http://www.w3.org/2000/svg','svg'),use=document.createElementNS('http://www.w3.org/2000/svg','use');svg.setAttribute('aria-hidden','true');svg.setAttribute('class','icon');use.setAttribute('href',`#icon-${name}`);svg.append(use);return svg;};
  function notice(message='',tone='info'){$('notice').textContent=message;$('notice').hidden=!message;$('notice').dataset.tone=tone;}
  function stamp(value){try{return new Intl.DateTimeFormat('zh-CN',{timeZone:root.dataset.timezone||'UTC',dateStyle:'medium',timeStyle:'short'}).format(new Date(value));}catch{return value||'';}}
  function controls(){
    const locked=state.loading||state.busy||state.readonly;
    $('inputs').disabled=locked;
    $('inputs').querySelectorAll('input,select,textarea').forEach(input=>{input.disabled=locked;});
    $('preview').disabled=locked||!state.tasks.length;
    $('generate').disabled=locked||!state.preview||!$('provider').value||!$('model').value;
    $('stop').hidden=!state.busy;$('generate').hidden=state.busy;
    $('copy').disabled=!state.text;$('export').disabled=!state.text;
    $('search-articles').disabled=locked;
    root.querySelectorAll('.aiw-task,[data-aiw-view],[data-aiw-history-refresh],.aiw-history-open,.aiw-history-delete').forEach(b=>{b.disabled=state.busy||state.loading;});
    $('result').setAttribute('aria-busy',String(state.busy));
  }
  async function rawJSON(path,options={},signal){
    const response=await fetch(path,{credentials:'same-origin',cache:'no-store',redirect:'error',...options,signal,headers:{Accept:'application/json',...options.headers}});
    let data;try{data=await response.json();}catch{throw new Error('响应无法读取，请刷新页面后重试。');}
    if(!response.ok)throw new Error(data.error?.message||data.message||`请求失败（${response.status}）`);
    return data;
  }
  async function session(signal,writing){
    if(state.readonly&&!writing)return {};
    const value=await rawJSON('/api/v1/admin/session',{},signal);
    if(!value.authenticated||String(value.user_id)!==root.dataset.userId||(writing&&(value.read_only||!value.csrf_token))){
      state.preview=null;state.text='';state.current=null;state.source=null;state.reports=[];renderDocument();renderSources();renderHistory();
      throw new Error('登录身份已变化，请刷新页面并重新登录。');
    }
    return value;
  }
  async function json(path,method='GET',body){
    const controller=new AbortController();requests.add(controller);
    const timer=setTimeout(()=>controller.abort(),15000);
    try{
      const auth=await session(controller.signal,method!=='GET');
      return await rawJSON(path,{method,headers:method==='GET'?{}:{'Content-Type':'application/json','X-CSRF-Token':auth.csrf_token},...(body===undefined?{}:{body:JSON.stringify(body)})},controller.signal);
    }catch(error){if(error.name==='AbortError')throw new Error('请求超时，请重试。');throw error;}
    finally{clearTimeout(timer);requests.delete(controller);}
  }
  function view(name){$('workspace').hidden=name!=='workspace';$('history').hidden=name!=='history';root.querySelectorAll('[data-aiw-view]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.aiwView===name)));}
  function tab(name){$('report-panel').hidden=name!=='report';$('sources-panel').hidden=name!=='sources';root.querySelectorAll('[data-aiw-tab]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.aiwTab===name)));}
  function renderTasks(){
    $('tasks').replaceChildren(...state.tasks.map(task=>{
      const button=node('button',null,'aiw-task');button.type='button';button.dataset.task=task.id;button.setAttribute('aria-pressed',String(task.id===state.task));
      const symbol=node('span',null,'aiw-task-icon');symbol.append(icon(task.icon));const text=node('span');text.append(node('strong',task.title),node('small',task.description));button.append(symbol,text);
      button.addEventListener('click',()=>selectTask(task.id));return button;
    }));
    $('history-task').replaceChildren(new Option('全部任务',''),...state.tasks.map(t=>new Option(t.title,t.id)));
  }
  function selectTask(id){
    if(state.busy||state.loading)return;
    state.task=id;state.preview=null;state.source=null;state.current=null;state.text='';state.complete=false;
    const task=state.tasks.find(t=>t.id===id);if(!task)return;
    root.querySelectorAll('[data-task]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.task===id)));
    $('task-title').textContent=task.title;$('scope').textContent=task.scope;$('summary-inputs').hidden=id!=='summary';
    const period=['briefing','planning','costs'].includes(id);$('period-label').hidden=!period;$('period-hint').hidden=!period;
    $('result-title').textContent=task.title;$('badge').textContent='未开始';$('result-meta').textContent='完整报告自动保存。';$('usage').textContent='';$('status').textContent='待预览';
    $('focus').placeholder=id==='summary'?'例如：保留关键数据':'例如：按优先级列出问题';
    notice();renderDocument();renderSources();tab('report');controls();
  }
  function renderProviders(defaultID){
    $('provider').replaceChildren(...state.providers.map(p=>new Option(p.name,p.id)));
    if(!state.providers.length)$('provider').append(new Option('尚未配置文本接口',''));
    if(state.providers.some(p=>p.id===defaultID))$('provider').value=defaultID;
    $('provider-hint').textContent=state.providers.length?'使用已配置的文本模型。':'请先配置文本模型；当前可预览数据。';
    models();
  }
  function models(){const p=state.providers.find(p=>p.id===$('provider').value);$('model').replaceChildren(...(p?p.models.map(m=>new Option(m,m)):[new Option('—','')]));if(p)$('model').value=p.default_model;controls();}
  function invalidate(){if(state.preview){state.preview=null;$('status').textContent='资料条件已变化，请重新预览。';$('badge').textContent='待重新预览';}controls();}
  function renderSources(){
    const source=state.source;$('source-details').hidden=!source;$('checks').replaceChildren();$('source-json').textContent='';
    $('source-description').textContent=source?`${source.scope}。快照时间：${stamp(source.created_at)}；统计范围：${source.start} 至 ${source.end}（${source.timezone}）。${source.focus?` 补充要求：${source.focus}`:''}`:'预览后可核对本次数据。';
    if(!source)return;
    let data;try{data=JSON.parse(source.context);}catch{data={material:source.context};}
    $('source-json').textContent=JSON.stringify(data,null,2);
    for(const c of data.system?.checks||[]){const row=node('div',null,'aiw-check');row.dataset.status=c.status;row.append(node('b',c.service),node('span',({pass:'正常',error:'待检查',disabled:'未启用'})[c.status]||'未知'));$('checks').append(row);}
  }
  function renderDocument(){
    $('empty').hidden=!!state.text||state.busy;$('document').hidden=!state.text&&!state.busy;
    $('document').replaceChildren(...renderMarkdown(state.text,document));controls();
  }
  function updateUsage(usage){const parts=[];if(Number.isFinite(usage.prompt_tokens))parts.push(`输入 ${usage.prompt_tokens.toLocaleString()}`);if(Number.isFinite(usage.completion_tokens))parts.push(`输出 ${usage.completion_tokens.toLocaleString()}`);$('usage').textContent=parts.length?`${parts.join(' · ')} Token`:'用量未报告';}
  async function preview(event){
    event.preventDefault();if(state.loading||state.busy||state.readonly)return;
    state.loading=true;state.preview=null;controls();notice();$('status').textContent='正在收集本次资料…';
    const input={task:state.task,days:Number($('days').value),focus:$('focus').value};
    if(state.task==='summary'){if($('source').value==='article')input.post_id=Number($('article').value);else input.material=$('material').value;}
    try{
      const result=await json(`${base}/preview`,'POST',input);if(state.disposed)return;
      state.preview=result;state.source=result;state.current=null;state.text='';state.complete=false;
      $('result-title').textContent=result.title;$('badge').textContent='已预览';$('result-meta').textContent=`${result.start} — ${result.end} · ${result.timezone} · ${result.scope}`;$('usage').textContent='';$('status').textContent='数据已预览，可生成报告。';
      renderDocument();renderSources();tab('sources');
    }catch(error){notice(error.message,'error');$('status').textContent='资料准备失败，可修改后重试。';}
    finally{state.loading=false;controls();}
  }
  async function generate(){
    if(!state.preview||state.busy||state.loading)return;
    if(Date.now()>=Date.parse(state.preview.expires_at)){state.preview=null;notice('预览已超过 10 分钟，请重新预览。','error');controls();return;}
    const preview=state.preview;state.preview=null;state.source=preview;state.current=null;state.text='';state.complete=false;state.busy=true;state.started=Date.now();notice();
    $('badge').textContent='生成中';$('result-meta').textContent=`${preview.start} — ${preview.end} · ${preview.timezone} · ${$('model').value}`;$('status').textContent='正在连接模型…';$('usage').textContent='';tab('report');renderDocument();
    const controller=new AbortController();state.controller=controller;
    const timeout=setTimeout(()=>controller.abort('timeout'),370000);
    clock=setInterval(()=>{if(state.busy)$('status').textContent=`${state.text?'生成中':'等待模型响应'} · ${Math.floor((Date.now()-state.started)/1000)} 秒 · ${state.text.length.toLocaleString()} 字符`;},1000);
    let terminal=false,saved=false;
    try{
      const auth=await session(controller.signal,true);
      const response=await fetch(`${base}/generate`,{method:'POST',credentials:'same-origin',redirect:'error',cache:'no-store',signal:controller.signal,headers:{'Content-Type':'application/json',Accept:'text/event-stream','X-CSRF-Token':auth.csrf_token},body:JSON.stringify({token:preview.token,provider_id:$('provider').value,model:$('model').value})});
      if(!response.ok){const data=await response.json();throw new Error(data.error?.message||data.message||'生成请求失败。');}
      if(!response.headers.get('Content-Type')?.includes('text/event-stream')||!response.body)throw new Error('生成响应格式无效，请重试。');
      const parser=createSSEParser((event,data)=>{
        if(terminal)return;
        if(event==='delta'){
          if(typeof data.text!=='string')throw new Error('报告片段格式无效。');
          state.text+=data.text;if(state.text.length>131072)throw new Error('报告超过显示上限。');
          if(!paint)paint=requestAnimationFrame(()=>{paint=0;renderDocument();});
        }else if(event==='usage')updateUsage(data);
        else if(event==='error'){terminal=true;throw new Error(data.message||'生成中断，请重试。');}
        else if(event==='done'){
          terminal=true;state.current=data.report;state.text=data.report.content;state.complete=true;saved=data.saved===true;state.source=data.report.source;
          $('badge').textContent=saved?'已保存':'未保存';$('status').textContent=saved?'报告已保存至「我的报告」。':'报告已生成，保存失败。';if(data.warning)notice(data.warning,'error');updateUsage(data.usage||{});
        }
      });
      const reader=response.body.getReader(),decoder=new TextDecoder();
      try{while(!terminal){const {value,done}=await reader.read();if(done)break;parser.push(decoder.decode(value,{stream:true}));}if(!terminal){parser.push(decoder.decode());parser.finish();throw new Error('连接提前结束，已保留收到的文字。');}}
      finally{await reader.cancel().catch(()=>{});}
    }catch(error){
      controller.abort();$('badge').textContent='未完成';
      const message=controller.signal.reason==='timeout'?'生成超时，已保留收到的文字。':error.name==='AbortError'?'已停止生成，收到的文字仍可复制或导出。':error.message;
      $('status').textContent=message;notice(message,'error');
    }finally{
      clearTimeout(timeout);clearInterval(clock);if(paint){cancelAnimationFrame(paint);paint=0;}state.busy=false;state.controller=null;
      if(!state.disposed){renderDocument();renderSources();controls();if(saved)await loadHistory().catch(error=>notice(`报告已保存，历史刷新失败：${error.message}`,'error'));}
    }
  }
  function currentExport(){return state.current||{title:$('result-title').textContent,created_at:new Date(state.started||Date.now()).toISOString(),model:$('model').value,content:state.text,source:state.source};}
  async function loadArticles(){
    if(state.busy||state.loading)return;
    state.loading=true;controls();
    try{const result=await json(`${base}/articles?search=${encodeURIComponent($('article-search').value)}`);$('article').replaceChildren(new Option('请选择公开文章',''),...result.items.map(p=>new Option(`#${p.id} ${p.title}`,String(p.id))));$('article-hint').textContent=`匹配 ${result.total} 篇，显示前 ${result.items.length} 篇。选择后只读取该文章。`;invalidate();}
    catch(error){notice(error.message,'error');}finally{state.loading=false;controls();}
  }
  async function loadHistory(){const result=await json(`${base}/reports`);if(state.disposed)return;state.reports=result.reports||[];renderHistory();}
  function renderHistory(){
    $('history-count').textContent=String(state.reports.length);
    const reports=filterReports(state.reports,$('history-search').value,$('history-task').value);
    $('history-empty').hidden=reports.length>0;$('history-empty').textContent=state.reports.length?'没有匹配的报告。':'暂无已保存的报告。';
    $('history-list').replaceChildren(...reports.map(report=>{
      const row=node('div',null,'aiw-history-row'),open=node('button',null,'aiw-history-open');open.type='button';open.append(node('strong',report.title),node('small',`${stamp(report.created_at)} · ${report.provider} / ${report.model}`));open.addEventListener('click',()=>openReport(report.id));row.append(open);
      if(!state.readonly){const remove=node('button','删除','aiw-tool aiw-history-delete');remove.type='button';remove.setAttribute('aria-label',`删除 ${report.title} ${stamp(report.created_at)}`);remove.addEventListener('click',()=>{state.deleteID=report.id;$('delete-dialog').showModal();});row.append(remove);}
      return row;
    }));controls();
  }
  async function openReport(id){
    if(state.busy||state.loading)return;state.loading=true;controls();notice();
    try{const report=await json(`${base}/reports/${encodeURIComponent(id)}`);state.current=report;state.source=report.source;state.text=report.content;state.preview=null;state.complete=true;$('result-title').textContent=report.title;$('badge').textContent='历史报告';$('result-meta').textContent=`${stamp(report.created_at)} · ${report.provider} / ${report.model}`;$('usage').textContent='';$('status').textContent='资料为生成时的快照。';renderDocument();renderSources();view('workspace');tab('report');$('result').scrollIntoView({behavior:'instant',block:'start'});}
    catch(error){notice(error.message,'error');}finally{state.loading=false;controls();}
  }
  root.querySelectorAll('[data-aiw-view]').forEach(b=>b.addEventListener('click',()=>view(b.dataset.aiwView)));
  root.querySelectorAll('[data-aiw-tab]').forEach(b=>b.addEventListener('click',()=>tab(b.dataset.aiwTab)));
  $('form').addEventListener('submit',preview);$('generate').addEventListener('click',generate);$('stop').addEventListener('click',()=>state.controller?.abort());
  $('provider').addEventListener('change',models);$('model').addEventListener('change',controls);
  for(const name of ['days','material','focus','article'])$(name).addEventListener('input',invalidate);
  $('source').addEventListener('change',()=>{invalidate();const article=$('source').value==='article';$('article-area').hidden=!article;$('material-area').hidden=article;if(article)loadArticles();});
  $('search-articles').addEventListener('click',loadArticles);$('article-search').addEventListener('keydown',event=>{if(event.key==='Enter'){event.preventDefault();loadArticles();}});
  $('history-search').addEventListener('input',renderHistory);$('history-task').addEventListener('change',renderHistory);
  $('history-refresh').addEventListener('click',async()=>{if(state.loading||state.busy)return;state.loading=true;controls();try{await loadHistory();notice();}catch(error){notice(error.message,'error');}finally{state.loading=false;controls();}});
  $('copy').addEventListener('click',async()=>{try{await navigator.clipboard.writeText(state.text);notice('报告正文已复制。');}catch{notice('复制失败，请选中报告文字手动复制。','error');}});
  $('export').addEventListener('click',()=>{const report=currentExport(),url=URL.createObjectURL(new Blob([reportMarkdown(report,state.complete)],{type:'text/markdown;charset=utf-8'}));const link=node('a');link.href=url;link.download=`newpress-ai-${report.task||state.task}-${(report.created_at||'').slice(0,10)}.md`;document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1000);});
  $('delete-cancel').addEventListener('click',()=>$('delete-dialog').close());
  $('delete-confirm').addEventListener('click',async()=>{const id=state.deleteID;if(!id)return;$('delete-confirm').disabled=true;$('delete-cancel').disabled=true;try{await json(`${base}/reports/${encodeURIComponent(id)}`,'DELETE');$('delete-dialog').close();state.reports=state.reports.filter(r=>r.id!==id);if(state.current?.id===id){state.current=null;state.text='';state.source=null;state.complete=false;renderDocument();renderSources();$('badge').textContent='已删除';$('status').textContent='报告已删除。';}renderHistory();notice('报告已删除。');}catch(error){$('delete-dialog').close();notice(error.message,'error');}finally{state.deleteID='';$('delete-confirm').disabled=false;$('delete-cancel').disabled=false;}});
  window.addEventListener('pagehide',()=>{state.disposed=true;state.controller?.abort();requests.forEach(c=>c.abort());clearInterval(clock);if(paint)cancelAnimationFrame(paint);});
  (async()=>{
    try{const config=await json(base);if(state.disposed)return;state.tasks=config.tasks;state.providers=config.providers;root.dataset.timezone=config.timezone;renderTasks();renderProviders(config.default_provider);$('meta').textContent=`${config.providers.length} 个文本接口 · ${config.timezone}`;state.loading=false;selectTask('operations');await loadHistory();}
    catch(error){notice(error.message,'error');$('meta').textContent='加载失败，请刷新页面';if(!state.tasks.length)$('tasks').replaceChildren(node('p','工作台暂时无法加载，请刷新页面重试。'));}
    finally{state.loading=false;controls();}
  })();
})();
