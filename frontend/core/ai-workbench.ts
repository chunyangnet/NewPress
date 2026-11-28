import type {} from '../shared/account-types';
import { required } from './ledger-admin-dom';
import { failure, record, string } from '../shared/ledger-data';
import { create } from '../shared/newpress-client';
import { usage } from '../shared/ai-usage-data';
import type { AIUsage } from '../shared/ai-usage-data';
import { configuration, previewReceipt, reportList, reportDetail, completedReport, articleList, deletedReport, systemChecks } from '../shared/ai-workbench-data';
import type { Task, TaskID, Provider, Preview, PreviewInput, Report, ReportSummary, ExportReport } from '../shared/ai-workbench-data';
import { renderMarkdown, createSSEParser, reportMarkdown, filterReports } from '../shared/ai-workbench-helpers';
type Fields=Record<'inputs',HTMLFieldSetElement>&Record<'provider'|'model'|'days'|'source'|'article'|'history-task',HTMLSelectElement>&Record<'focus'|'material',HTMLTextAreaElement>&Record<'article-search'|'history-search',HTMLInputElement>&Record<'preview'|'generate'|'stop'|'copy'|'export'|'search-articles'|'history-refresh'|'delete-cancel'|'delete-confirm',HTMLButtonElement>&Record<'form',HTMLFormElement>&Record<'delete-dialog',HTMLDialogElement>&Record<'notice'|'result'|'workspace'|'history'|'report-panel'|'sources-panel'|'tasks'|'task-title'|'scope'|'summary-inputs'|'period-label'|'period-hint'|'result-title'|'badge'|'result-meta'|'usage'|'status'|'provider-hint'|'source-details'|'checks'|'source-json'|'source-description'|'empty'|'document'|'article-hint'|'history-count'|'history-empty'|'history-list'|'article-area'|'material-area'|'meta',HTMLElement>;
interface State {tasks:Task[];providers:Provider[];reports:ReportSummary[];task:TaskID;preview:Preview|null;source:Preview|null;current:Report|null;text:string;busy:boolean;loading:boolean;readonly:boolean;controller:AbortController|null;started:number;complete:boolean;deleteID:string;disposed:boolean}
(() => {
  if(typeof document==='undefined')return;
  const host=document.querySelector<HTMLElement>('[data-ai-workbench]');if(!host)return;const root=host;
  const $=<K extends keyof Fields>(name:K):Fields[K]=>required<Fields[K]>(root,`[data-aiw-${name}]`);
  const state:State={tasks:[],providers:[],reports:[],task:'operations',preview:null,source:null,current:null,text:'',busy:false,loading:true,readonly:root.dataset.readOnly==='true',controller:null,started:0,complete:false,deleteID:'',disposed:false};
  const base='/api/v1/admin/ai/workbench',owner=root.dataset.userId||'',csrf=root.dataset.csrf||'';
  const alive=()=>!state.disposed&&root.isConnected;
  const client=create({session:()=>({authenticated:alive(),csrf_token:csrf}),onUnauthorized:()=>retire()});
  let paint=0,clock=0,historySerial=0;
  const node=<K extends keyof HTMLElementTagNameMap>(tag:K,text?:string|null,cls?:string)=>{const n=document.createElement(tag);if(text!=null)n.textContent=text;if(cls)n.className=cls;return n;};
  const icon=(name:string)=>{const svg=document.createElementNS('http://www.w3.org/2000/svg','svg'),use=document.createElementNS('http://www.w3.org/2000/svg','use');svg.setAttribute('aria-hidden','true');svg.setAttribute('class','icon');use.setAttribute('href',`#icon-${name}`);svg.append(use);return svg;};
  function notice(message='',tone='info'){$('notice').textContent=message;$('notice').hidden=!message;$('notice').dataset.tone=tone;}
  function stamp(value:string){try{return new Intl.DateTimeFormat('zh-CN',{timeZone:root.dataset.timezone||'UTC',dateStyle:'medium',timeStyle:'short'}).format(new Date(value));}catch{return value||'';}}
  function controls(){
    const locked=!alive()||state.loading||state.busy||state.readonly;
    $('inputs').disabled=locked;
    $('inputs').querySelectorAll<HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement>('input,select,textarea').forEach(input=>{input.disabled=locked;});
    $('preview').disabled=locked||!state.tasks.length;
    $('generate').disabled=locked||!state.preview||!$('provider').value||!$('model').value;
    $('stop').hidden=!state.busy;$('generate').hidden=state.busy;
    $('copy').disabled=!alive()||!state.text;$('export').disabled=!alive()||!state.text;
    $('search-articles').disabled=locked;
    root.querySelectorAll<HTMLButtonElement>('.aiw-task,[data-aiw-view],[data-aiw-history-refresh],.aiw-history-open,.aiw-history-delete').forEach(b=>{b.disabled=!alive()||state.busy||state.loading;});
    $('result').setAttribute('aria-busy',String(state.busy));
  }
  function retire(){
    if(state.disposed)return;state.disposed=true;historySerial++;state.controller?.abort();client.cancel();clearInterval(clock);if(paint){cancelAnimationFrame(paint);paint=0;}
    state.preview=null;state.source=null;state.current=null;state.reports=[];state.text='';state.complete=false;state.busy=false;
    $('delete-dialog').close();renderDocument();renderSources();renderHistory();$('material').value='';$('focus').value='';$('result-title').textContent='AI 报告';$('usage').textContent='';$('result-meta').textContent='';$('status').textContent='页面已失效';notice('登录状态或页面已变化，请重新载入后继续。','error');controls();
  }
  async function session(signal:AbortSignal|undefined,writing:boolean){
    if(!alive()||writing&&state.readonly)throw new DOMException('页面已失效','AbortError');
    if(state.readonly&&!writing)return {csrf_token:''};
    const value=await client.request('/api/v1/admin/session',{signal});
    if(value.authenticated!==true||String(value.user_id)!==owner||value.csrf_token!==csrf||writing&&value.read_only){retire();throw new Error('登录身份已变化，请刷新页面并重新登录。');}
    if(!alive())throw new DOMException('页面已失效','AbortError');return {csrf_token:csrf};
  }
  async function json(path:string,method='GET',body?:unknown){
    try{await session(undefined,method!=='GET');const result=await client.request(path,{method,body,timeout:15000});if(!alive())throw new DOMException('页面已失效','AbortError');return result;}
    catch(error){if(failure(error).status===401||failure(error).status===403&&!state.readonly)retire();throw error;}
  }
  function view(name:string){$('workspace').hidden=name!=='workspace';$('history').hidden=name!=='history';root.querySelectorAll<HTMLElement>('[data-aiw-view]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.aiwView===name)));}
  function tab(name:string){$('report-panel').hidden=name!=='report';$('sources-panel').hidden=name!=='sources';root.querySelectorAll<HTMLElement>('[data-aiw-tab]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.aiwTab===name)));}
  function renderTasks(){
    $('tasks').replaceChildren(...state.tasks.map(task=>{
      const button=node('button',null,'aiw-task');button.type='button';button.dataset.task=task.id;button.setAttribute('aria-pressed',String(task.id===state.task));
      const symbol=node('span',null,'aiw-task-icon');symbol.append(icon(task.icon));const text=node('span');text.append(node('strong',task.title),node('small',task.description));button.append(symbol,text);
      button.addEventListener('click',()=>selectTask(task.id));return button;
    }));
    $('history-task').replaceChildren(new Option('全部任务',''),...state.tasks.map(t=>new Option(t.title,t.id)));
  }
  function selectTask(id:TaskID){
    if(!alive()||state.busy||state.loading)return;
    state.task=id;state.preview=null;state.source=null;state.current=null;state.text='';state.complete=false;
    const task=state.tasks.find(t=>t.id===id);if(!task)return;
    root.querySelectorAll<HTMLElement>('[data-task]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.task===id)));
    $('task-title').textContent=task.title;$('scope').textContent=task.scope;$('summary-inputs').hidden=id!=='summary';
    const period=['briefing','planning','costs'].includes(id);$('period-label').hidden=!period;$('period-hint').hidden=!period;
    $('result-title').textContent=task.title;$('badge').textContent='未开始';$('result-meta').textContent='完整报告自动保存。';$('usage').textContent='';$('status').textContent='待预览';
    $('focus').placeholder=id==='summary'?'例如：保留关键数据':'例如：按优先级列出问题';
    notice();renderDocument();renderSources();tab('report');controls();
  }
  function renderProviders(defaultID:string){
    $('provider').replaceChildren(...state.providers.map(p=>new Option(p.name,p.id)));
    if(!state.providers.length)$('provider').append(new Option('尚未配置文本接口',''));
    if(state.providers.some(p=>p.id===defaultID))$('provider').value=defaultID;
    $('provider-hint').textContent=state.providers.length?'使用已配置的文本模型。':'请先配置文本模型；当前可预览数据。';
    models();
  }
  function models(){const p=state.providers.find(p=>p.id===$('provider').value);$('model').replaceChildren(...(p?p.models.map(m=>new Option(m,m)):[new Option('—','')]));if(p)$('model').value=p.default_model;controls();}
  let editing=0;
  function invalidate(){editing++;if(state.preview){state.preview=null;$('status').textContent='资料条件已变化，请重新预览。';$('badge').textContent='待重新预览';}controls();}
  function renderSources(){
    const source=state.source;$('source-details').hidden=!source;$('checks').replaceChildren();$('source-json').textContent='';
    $('source-description').textContent=source?`${source.scope}。快照时间：${stamp(source.created_at)}；统计范围：${source.start} 至 ${source.end}（${source.timezone}）。${source.focus?` 补充要求：${source.focus}`:''}`:'预览后可核对本次数据。';
    if(!source)return;
    let data;try{data=JSON.parse(source.context);}catch{data={material:source.context};}
    $('source-json').textContent=JSON.stringify(data,null,2);
    for(const c of systemChecks(source.context)){const row=node('div',null,'aiw-check');row.dataset.status=c.status;row.append(node('b',c.service),node('span',({pass:'正常',error:'待检查',disabled:'未启用'} as Record<string,string>)[c.status]||'未知'));$('checks').append(row);}
  }
  function renderDocument(){
    $('empty').hidden=!!state.text||state.busy;$('document').hidden=!state.text&&!state.busy;
    $('document').replaceChildren(...renderMarkdown(state.text,document));controls();
  }
  function updateUsage(usage:AIUsage){const parts=[];if(Number.isFinite(usage.prompt_tokens))parts.push(`输入 ${usage.prompt_tokens?.toLocaleString()}`);if(Number.isFinite(usage.completion_tokens))parts.push(`输出 ${usage.completion_tokens?.toLocaleString()}`);$('usage').textContent=parts.length?`${parts.join(' · ')} Token`:'用量未报告';}
  async function preview(event:Event){
    event.preventDefault();if(!alive()||state.loading||state.busy||state.readonly)return;
    state.loading=true;state.preview=null;controls();notice();$('status').textContent='正在收集本次资料…';
    const serial=editing;const input:PreviewInput={task:state.task,days:Number($('days').value),focus:$('focus').value};
    if(state.task==='summary'){if($('source').value==='article')input.post_id=Number($('article').value);else input.material=$('material').value;}
    try{
      const result=previewReceipt(await json(`${base}/preview`,'POST',input),input);if(!alive())return;if(serial!==editing){$('status').textContent='资料条件已变化，请重新预览。';return;}
      state.preview=result;state.source=result;state.current=null;state.text='';state.complete=false;
      $('result-title').textContent=result.title;$('badge').textContent='已预览';$('result-meta').textContent=`${result.start} — ${result.end} · ${result.timezone} · ${result.scope}`;$('usage').textContent='';$('status').textContent='数据已预览，可生成报告。';
      renderDocument();renderSources();tab('sources');
    }catch(error){if(!alive())return;notice(failure(error).message,'error');$('status').textContent='资料准备失败，可修改后重试。';}
    finally{state.loading=false;controls();}
  }
  async function generate(){
    if(!alive()||state.readonly||!state.preview||state.busy||state.loading)return;
    const selected=state.providers.find(p=>p.id===$('provider').value),model=$('model').value;if(!selected||!selected.models.includes(model))return;
    if(Date.now()>=Date.parse(state.preview.expires_at)){state.preview=null;notice('预览已超过 10 分钟，请重新预览。','error');controls();return;}
    const preview=state.preview;state.preview=null;state.source=preview;state.current=null;state.text='';state.complete=false;state.busy=true;state.started=Date.now();notice();
    $('badge').textContent='生成中';$('result-meta').textContent=`${preview.start} — ${preview.end} · ${preview.timezone} · ${$('model').value}`;$('status').textContent='正在连接模型…';$('usage').textContent='';tab('report');renderDocument();
    const controller=new AbortController();state.controller=controller;
    const timeout=setTimeout(()=>controller.abort('timeout'),370000);
    clock=setInterval(()=>{if(state.busy)$('status').textContent=`${state.text?'生成中':'等待模型响应'} · ${Math.floor((Date.now()-state.started)/1000)} 秒 · ${state.text.length.toLocaleString()} 字符`;},1000);
    let terminal=false,saved=false;
    try{
      const auth=await session(controller.signal,true);
      const response=await fetch(`${base}/generate`,{method:'POST',credentials:'same-origin',redirect:'error',cache:'no-store',signal:controller.signal,headers:{'Content-Type':'application/json',Accept:'text/event-stream','X-CSRF-Token':auth.csrf_token},body:JSON.stringify({token:preview.token,provider_id:selected.id,model})});
      if(!alive()||controller.signal.aborted)throw new DOMException('生成已停止','AbortError');
      if(!response.ok){if(response.status===401||response.status===403)retire();let message='生成请求失败。';try{message=string(record(record(await response.json()).error).message);}catch{}throw new Error(message);}
      if(!response.headers.get('Content-Type')?.includes('text/event-stream')||!response.body)throw new Error('生成响应格式无效，请重试。');
      const parser=createSSEParser((event,data)=>{
        if(terminal||!alive()||controller.signal.aborted)return;
        if(event==='ready'){if(record(data).model!==model)throw new Error('响应模型与所选模型不一致');}
        else if(event==='delta'){
          state.text+=string(record(data).text);if(state.text.length>131072)throw new Error('报告超过显示上限。');
          if(!paint)paint=requestAnimationFrame(()=>{paint=0;if(alive())renderDocument();});
        }else if(event==='usage')updateUsage(usage(data));
        else if(event==='error'){terminal=true;throw new Error(string(record(data).message)||'生成中断，请重试。');}
        else if(event==='done'){
          const result=completedReport(data,owner,preview,selected,model);terminal=true;state.current=result.report;state.text=result.report.content;state.complete=result.saved;saved=result.saved;state.source=result.report.source;
          $('badge').textContent=saved?'已保存':'未保存';$('status').textContent=saved?'报告已保存至「我的报告」。':'报告已生成，保存失败。';if(result.warning||!saved)notice(result.warning||'报告已生成，但保存未确认。请复制或导出后核对。','error');updateUsage(result.usage);
        }
      });
      const reader=response.body.getReader(),decoder=new TextDecoder();
      try{while(!terminal){const {value,done}=await reader.read();if(!alive()||controller.signal.aborted)throw new DOMException('生成已停止','AbortError');if(done)break;parser.push(decoder.decode(value,{stream:true}));}if(!terminal){parser.push(decoder.decode());parser.finish();throw new Error('连接提前结束，已保留收到的文字。');}}
      finally{await reader.cancel().catch(()=>{});}
    }catch(caught){
      if(!alive())return;const error=failure(caught);controller.abort();$('badge').textContent='未完成';
      const message=controller.signal.reason==='timeout'?'生成超时，已保留收到的文字。':error.name==='AbortError'?'已停止生成，收到的文字仍可复制或导出。':error.message;
      $('status').textContent=message;notice(message,'error');
    }finally{
      clearTimeout(timeout);clearInterval(clock);if(paint){cancelAnimationFrame(paint);paint=0;}state.busy=false;state.controller=null;
      if(!state.disposed){renderDocument();renderSources();controls();if(saved)await loadHistory().catch(error=>{if(alive())notice(`报告已保存，历史刷新失败：${failure(error).message}`,'error');});}
    }
  }
  function currentExport():ExportReport{return state.current||{title:$('result-title').textContent||'AI 报告',created_at:new Date(state.started||Date.now()).toISOString(),model:$('model').value,content:state.text,source:state.source};}
  async function loadArticles(){
    if(!alive()||state.busy||state.loading)return;
    state.loading=true;controls();
    try{const result=articleList(await json(`${base}/articles?search=${encodeURIComponent($('article-search').value)}`));if(!alive())return;$('article').replaceChildren(new Option('请选择公开文章',''),...result.items.map(p=>new Option(`#${p.id} ${p.title}`,String(p.id))));$('article-hint').textContent=`匹配 ${result.total} 篇，显示前 ${result.items.length} 篇。选择后只读取该文章。`;invalidate();}
    catch(error){if(!alive())return;notice(failure(error).message,'error');}finally{state.loading=false;controls();}
  }
  async function loadHistory(){const serial=++historySerial;const result=await json(`${base}/reports`);if(!alive()||serial!==historySerial)return;state.reports=reportList(result,owner);renderHistory();}
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
  async function openReport(id:string){
    if(!alive()||state.busy||state.loading)return;state.loading=true;controls();notice();
    try{const report=reportDetail(await json(`${base}/reports/${encodeURIComponent(id)}`),owner,id);if(!alive())return;state.current=report;state.source=report.source;state.text=report.content;state.preview=null;state.complete=true;$('result-title').textContent=report.title;$('badge').textContent='历史报告';$('result-meta').textContent=`${stamp(report.created_at)} · ${report.provider} / ${report.model}`;$('usage').textContent='';$('status').textContent='资料为生成时的快照。';renderDocument();renderSources();view('workspace');tab('report');$('result').scrollIntoView({behavior:'instant',block:'start'});}
    catch(error){if(!alive())return;notice(failure(error).message,'error');}finally{state.loading=false;controls();}
  }
  root.querySelectorAll<HTMLElement>('[data-aiw-view]').forEach(b=>b.addEventListener('click',()=>view(b.dataset.aiwView||'workspace')));
  root.querySelectorAll<HTMLElement>('[data-aiw-tab]').forEach(b=>b.addEventListener('click',()=>tab(b.dataset.aiwTab||'report')));
  $('form').addEventListener('submit',preview);$('generate').addEventListener('click',generate);$('stop').addEventListener('click',()=>state.controller?.abort());
  $('provider').addEventListener('change',models);$('model').addEventListener('change',controls);
  for(const name of (['days','material','focus','article'] as const))$(name).addEventListener('input',invalidate);
  $('source').addEventListener('change',()=>{invalidate();const article=$('source').value==='article';$('article-area').hidden=!article;$('material-area').hidden=article;if(article)loadArticles();});
  $('search-articles').addEventListener('click',loadArticles);$('article-search').addEventListener('keydown',event=>{if(event.key==='Enter'){event.preventDefault();loadArticles();}});
  $('history-search').addEventListener('input',renderHistory);$('history-task').addEventListener('change',renderHistory);
  $('history-refresh').addEventListener('click',async()=>{if(state.loading||state.busy)return;state.loading=true;controls();try{await loadHistory();notice();}catch(error){if(!alive())return;notice(failure(error).message,'error');}finally{state.loading=false;controls();}});
  $('copy').addEventListener('click',async()=>{try{await navigator.clipboard.writeText(state.text);notice('报告正文已复制。');}catch{notice('复制失败，请选中报告文字手动复制。','error');}});
  $('export').addEventListener('click',()=>{const report=currentExport(),url=URL.createObjectURL(new Blob([reportMarkdown(report,state.complete)],{type:'text/markdown;charset=utf-8'}));const link=node('a');link.href=url;link.download=`newpress-ai-${report.task||state.task}-${(report.created_at||'').slice(0,10)}.md`;document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1000);});
  $('delete-cancel').addEventListener('click',()=>$('delete-dialog').close());
  $('delete-confirm').addEventListener('click',async()=>{const id=state.deleteID;if(!alive()||state.readonly||state.loading||state.busy||!id||$('delete-confirm').disabled)return;$('delete-confirm').disabled=true;$('delete-cancel').disabled=true;try{historySerial++;deletedReport(await json(`${base}/reports/${encodeURIComponent(id)}`,'DELETE'));if(!alive())return;$('delete-dialog').close();state.reports=state.reports.filter(r=>r.id!==id);if(state.current?.id===id){state.current=null;state.text='';state.source=null;state.complete=false;renderDocument();renderSources();$('badge').textContent='已删除';$('status').textContent='报告已删除。';}renderHistory();notice('报告已删除。');}catch(error){if(alive()){$('delete-dialog').close();notice(failure(error).message,'error');}}finally{state.deleteID='';$('delete-confirm').disabled=false;$('delete-cancel').disabled=false;}});
  window.addEventListener('pagehide',retire);window.addEventListener('pageshow',event=>{if(event.persisted)retire();});
  document.addEventListener('chunnel:session',event=>{const s=event.detail;if(s?.authenticated!==true||String(s.id||'')!==owner||s.csrf_token!==csrf)retire();});
  (async()=>{
    try{const config=configuration(await json(base));if(state.disposed)return;state.tasks=config.tasks;state.providers=config.providers;root.dataset.timezone=config.timezone;renderTasks();renderProviders(config.default_provider);$('meta').textContent=`${config.providers.length} 个文本接口 · ${config.timezone}`;state.loading=false;selectTask(state.tasks.find(t=>t.id==='operations')?.id||state.tasks[0]!.id);await loadHistory();}
    catch(error){if(!alive())return;notice(failure(error).message,'error');$('meta').textContent='加载失败，请刷新页面';if(!state.tasks.length)$('tasks').replaceChildren(node('p','工作台暂时无法加载，请刷新页面重试。'));}
    finally{state.loading=false;controls();}
  })();
})();
