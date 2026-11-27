(() => {
  'use strict';
  const numeric = value => typeof value === 'number' && Number.isFinite(value);
  const purpose = record => ({operations:'运维诊断',briefing:'运营简报',summary:'内容总结',seo:'SEO 检查',planning:'选题规划',costs:'AI 成本分析',comment_reply_draft:'评论回复草稿',content_queue:'内容生成队列',topic_suggestions:'分类选题建议'})[record.purpose] || (record.kind==='image'?'图片生成':'文本写作');
  const count = value => numeric(value) ? value.toLocaleString('zh-CN') : '—';
  const compact = value => !numeric(value) ? '—' : Math.abs(value) >= 1e6 ? `${(value/1e6).toFixed(2)}M` : Math.abs(value) >= 1e3 ? `${(value/1e3).toFixed(1)}K` : count(value);
  const duration = ms => !numeric(ms) ? '—' : ms >= 60000 ? `${Math.floor(ms/60000)}分 ${Math.round(ms%60000/1000)}秒` : `${(ms/1000).toFixed(2)}s`;
  const tps = (tokens, ms) => numeric(tokens) && numeric(ms) && ms >= 250 ? `${(tokens*1000/ms).toFixed(1)} tps` : '—';
  const money = (value, currency) => numeric(value) ? `${currency || ''} ${value.toLocaleString('zh-CN',{minimumFractionDigits:2,maximumFractionDigits:6})}` : '未计价';
  function dayOffset(day, offset) { const date=new Date(`${day}T12:00:00Z`); date.setUTCDate(date.getUTCDate()+offset); return date.toISOString().slice(0,10); }
  function fillDays(start,end,trend) { const map=new Map(trend.map(day=>[day.day,day])); const days=[]; for(let day=start;day<=end && days.length<91;day=dayOffset(day,1)) days.push(map.get(day)||{day,requests:0,tokens:0,usage_known:0,cost_known:0,costs:{}}); return days; }
  function chartValue(day,metric,currency) {
    if(metric==='costs') return day.requests && !day.cost_known ? null : (day.costs?.[currency] ?? 0);
    if(metric==='tokens') return day.requests && !day.usage_known ? null : day.tokens;
    return day.requests;
  }
  if (typeof module!=='undefined' && module.exports) module.exports={numeric,count,compact,duration,tps,money,dayOffset,fillDays,chartValue};
  if (typeof document==='undefined') return;
  const root=document.querySelector('[data-ai-usage]'); if(!root) return;
  const $=name=>root.querySelector(`[data-aiu-${name}]`), $$=name=>[...root.querySelectorAll(`[data-aiu-${name}]`)];
  const node=(tag,text,className)=>{const el=document.createElement(tag);if(text!=null)el.textContent=text;if(className)el.className=className;return el;};
  const statuses={success:'成功',error:'失败',timeout:'超时',cancelled:'已取消'};
  const key=`newpress:ai-usage:columns:${root.dataset.userId}`;
  let report=null, controller=null, serial=0, before=0, history=[], next=0, selectedID='', loadPending=false;
  const balanceControllers=new Set();
  function applyColumns() {
    const preferences={}; $$('column').forEach(input=>{preferences[input.dataset.aiuColumn]=input.checked;root.querySelectorAll(`[data-col="${input.dataset.aiuColumn}"]`).forEach(cell=>cell.hidden=!input.checked);});
    try{localStorage.setItem(key,JSON.stringify(preferences));}catch{}
  }
  try{const saved=JSON.parse(localStorage.getItem(key)||'{}');$$('column').forEach(input=>{if(typeof saved[input.dataset.aiuColumn]==='boolean')input.checked=saved[input.dataset.aiuColumn];});}catch{}
  $$('column').forEach(input=>input.addEventListener('change',applyColumns));
  function options() {
    const previous=$('provider').value;
    const providers=new Map(report.providers.map(p=>[p.id,p.name]));
    report.models.forEach(m=>{if(!providers.has(m.provider_id))providers.set(m.provider_id,m.provider);});
    const option=(value,text)=>{const el=node('option',text);el.value=value;return el;};
    if(previous && !providers.has(previous))providers.set(previous,`${previous}（历史接口）`);
    $('provider').replaceChildren(option('','全部接口'),...Array.from(providers,([id,name])=>option(id,name)));$('provider').value=previous;
    const models=new Set(report.providers.filter(p=>!previous||p.id===previous).flatMap(p=>p.models));report.models.forEach(m=>models.add(m.model));
    $('models').replaceChildren(...[...models].map(model=>option(model,model)));
  }
  function query() {
    const params=new URLSearchParams();
    for(const name of ['provider','model','kind','status'])if($(name).value.trim())params.set(name,$(name).value.trim());
    let end=$('end').value, start=$('start').value;
    if($('range').value!=='custom') { end=report?.today || report?.end; if(end)start=dayOffset(end,1-Number($('range').value)); }
    if(start)params.set('start',start);if(end)params.set('end',end);
    if(before)params.set('before',String(before));return params;
  }
  function announce(message,error=false){$('notice').textContent=message;$('notice').hidden=!message;$('notice').dataset.error=String(error);}
  async function load() {
    controller?.abort();controller=new AbortController();const current=++serial,active=controller;loadPending=true;
    root.classList.add('is-loading');$('refresh').disabled=true;$('prev').disabled=true;$('next').disabled=true;
    const timer=setTimeout(()=>active.abort(),15000);
    try {
      const response=await fetch(`/api/v1/admin/ai/usage?${query()}`,{credentials:'same-origin',redirect:'error',cache:'no-store',signal:active.signal,headers:{Accept:'application/json'}});
      const data=await response.json();if(!response.ok)throw new Error(data?.error?.message||'统计暂时不可用，请重试。');
      if(current!==serial)return;
      if(!data.config||!Array.isArray(data.records)||!Array.isArray(data.trend))throw new Error('统计响应格式无效，请更新主题后重试。');
      report=data;next=data.next;options();render();
    }catch(error){if(current===serial){next=0;history=[];before=0;announce((error.name==='AbortError'?'读取统计超时，请重试。':error.message)+(report?' 当前仍显示上次成功结果。':''),true);}}
    finally{clearTimeout(timer);if(current===serial){loadPending=false;root.classList.remove('is-loading');$('refresh').disabled=false;$('prev').disabled=!history.length;$('next').disabled=!next;}}
  }
  function render() {
    const t=report.totals,c=report.config;
    $('period').textContent=`${report.start} — ${report.end} · ${report.timezone}`;
    $('collection').textContent=c.enabled?'采集已开启':'采集已暂停';$('collection').classList.toggle('is-paused',!c.enabled);
    if(!$('start').value)$('start').value=report.start;if(!$('end').value)$('end').value=report.end;
    const notices=[];if(!c.enabled)notices.push('采集已暂停，当前显示保留期内的历史数据。');if(report.persist_error)notices.push('统计尚未写入磁盘，请检查数据目录权限；当前数据暂存内存。');if(t.saturated)notices.push('Token 累计超出精确显示范围，已封顶显示。');if(report.collapsed)notices.push('部分高基数维度已归入其他模型，汇总用量仍保留。');announce(notices.join(' '),report.persist_error);
    $('count').textContent=count(t.requests);$('success').textContent=t.requests?`${(t.success/t.requests*100).toFixed(1)}% 成功 · ${t.errors} 失败/超时 · ${t.cancelled} 取消`:'尚无生成请求';
    $('tokens').textContent=t.usage_known?compact(t.tokens):'—';$('token-detail').textContent=`输入 ${t.input_known?compact(t.input):'—'} · 输出 ${t.output_known?compact(t.output):'—'} · ${t.usage_known}/${t.requests} 次报告总量`;
    const costs=Object.entries(t.costs||{}).sort(([a],[b])=>a.localeCompare(b));$('cost').textContent=costs.length?costs.map(([currency,value])=>money(value,currency)).join(' / '):'—';$('cost').classList.toggle('has-multiple',costs.length>1);
    $('cost-detail').textContent=`${t.cost_known}/${t.requests} 次可估算${t.cost_partial?` · ${t.cost_partial} 次用量或结果不完整`:''}${!c.costs?' · 新费用采集已关闭':''}`;
    $('duration').textContent=t.requests?duration(t.duration_ms/t.requests):'—';$('speed').textContent=`首响应 ${t.first_token_known?duration(t.first_token_ms/t.first_token_known):'—'} · ${tps(t.speed_output,t.output_duration_ms)}`;
    $('charts').hidden=root.dataset.view==='ai-requests'||!c.charts;
    if(!$('charts').hidden){const old=$('currency').value;$('currency').replaceChildren(...(costs.length?costs.map(([currency])=>currency):['USD']).map(currency=>{const el=node('option',currency);el.value=currency;return el;}));if(costs.some(([currency])=>currency===old))$('currency').value=old;drawChart();drawDistribution();}
    $('record-hint').textContent=`保留 ${c.retention_days} 天 · 最多 ${count(c.max_records)} 条 · ${c.details?'明细采集中':'已停止新增明细'}${t.requests>report.retained?' · 汇总包含已滚动移除或未保存明细的请求':''}`;
    renderRows();renderBalances();applyColumns();
  }
  function renderRows() {
    $('rows').replaceChildren(...report.records.map(record=>{
      const row=node('tr');const cell=(main,sub,column)=>{const td=node('td');if(column)td.dataset.col=column;td.append(node('span',main),node('small',sub));row.append(td);return td;};
      let stamp;try{stamp=new Intl.DateTimeFormat('zh-CN',{timeZone:report.timezone,month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}).format(new Date(record.at));}catch{stamp=record.at;}
      cell(stamp,record.id.slice(0,10)).title=record.id;cell(record.model,record.provider).className='aiu-model-cell';
      const state=cell(purpose(record),'');const badge=node('small',statuses[record.status]||record.status,'aiu-status');badge.dataset.status=record.status;state.lastChild.replaceWith(badge);
      cell(`${compact(record.usage.prompt_tokens)} / ${compact(record.usage.completion_tokens)}`,`总计 ${compact(record.usage.total_tokens)}${record.images?` · ${record.images} 张图片`:''}`,'tokens');
      cell(money(record.cost.amount,record.cost.currency),numeric(record.cost.amount)?record.cost.complete?'估算':'部分估算':'用量或价格未配置','cost');
      cell(duration(record.duration_ms),record.kind==='text'?tps(record.usage.completion_tokens,record.duration_ms):'图片生成','duration');cell(`#${record.user_id}`,'','user');
      const action=node('td'),button=node('button','详情','aiu-detail-button');button.type='button';button.setAttribute('aria-label',`查看 ${record.id} 请求详情`);button.addEventListener('click',()=>detail(record));action.append(button);row.append(action);return row;
    }));
    $('empty').hidden=report.records.length>0;
    $('page-info').textContent=`第 ${history.length+1} 页 · 本页 ${report.records.length} 条 · 筛选内保留 ${count(report.retained)} 条`;
  }
  function svg(tag,attrs={}) {const el=document.createElementNS('http://www.w3.org/2000/svg',tag);for(const[key,value]of Object.entries(attrs))el.setAttribute(key,String(value));return el;}
  function drawChart() {
    const metric=$('metric').value,currency=$('currency').value,days=fillDays(report.start,report.end,report.trend),values=days.map(day=>chartValue(day,metric,currency)),max=Math.max(1,...values.filter(numeric));
    $('currency').hidden=metric!=='costs';$('chart-description').textContent=metric==='costs'?`${currency} · 部分用量仅计已知费用`:metric==='tokens'?'每日已报告 Token · 未报告保持未知':'每日生成请求 · 包含失败与取消';
    const chart=svg('svg',{viewBox:'0 0 900 250',role:'img','aria-label':`${report.start} 至 ${report.end} ${$('metric').selectedOptions[0].textContent}趋势`}),points=[];
    const left=62,right=876,top=24,bottom=212,step=(right-left)/Math.max(days.length-1,1);
    for(let i=0;i<4;i++){const y=top+i*(bottom-top)/3;chart.append(svg('line',{x1:left,y1:y,x2:right,y2:y,class:'aiu-grid-line'}));const label=svg('text',{x:left-12,y:y+4,'text-anchor':'end'});label.textContent=compact(max*(1-i/3));chart.append(label);}
    const segments=[];let segment=[];
    values.forEach((value,i)=>{if(!numeric(value)){if(segment.length)segments.push(segment);segment=[];return;}const point=[left+i*step,bottom-value/max*(bottom-top)];segment.push(point);points.push([i,...point]);});if(segment.length)segments.push(segment);
    for(const points of segments){if(points.length>1){chart.append(svg('polygon',{points:`${points[0][0]},${bottom} ${points.map(p=>p.join(',')).join(' ')} ${points.at(-1)[0]},${bottom}`,class:'aiu-area'}));chart.append(svg('polyline',{points:points.map(p=>p.join(',')).join(' '),class:'aiu-line'}));}}
    const display=i=>{const day=days[i],value=values[i];$('tooltip').textContent=`${day.day} · ${numeric(value)?metric==='costs'?money(value,currency):count(value):'未报告'} · ${day.requests} 次请求`;};
    days.forEach((day,i)=>{const value=values[i],x=left+i*step,y=numeric(value)?bottom-value/max*(bottom-top):bottom;const point=svg('circle',{cx:x,cy:y,r:days.length<=7?4:2.4,class:numeric(value)?'aiu-point':'aiu-point-unknown'});chart.append(point);const hit=svg('rect',{x:x-step/2,y:top,width:Math.max(8,step),height:bottom-top,fill:'transparent',tabindex:'0','aria-label':`${day.day} ${numeric(value)?count(value):'未知'}`});hit.addEventListener('pointerenter',()=>display(i));hit.addEventListener('focus',()=>display(i));chart.append(hit);});
    [...new Set([0,Math.floor((days.length-1)/2),days.length-1])].forEach(i=>{const label=svg('text',{x:left+i*step,y:240,'text-anchor':i===0?'start':i===days.length-1?'end':'middle'});label.textContent=days[i].day.slice(5);chart.append(label);});
    $('chart').replaceChildren(chart);$('tooltip').textContent=report.totals.requests?'选择图中日期查看数值':'暂无数据；生成请求后会显示趋势。';
  }
  function drawDistribution(){const total=report.models.reduce((sum,m)=>sum+m.requests,0);$('distribution').replaceChildren(...report.models.slice(0,8).map(model=>{const row=node('div',null,'aiu-distribution-row'),head=node('div'),name=node('span',model.model),amount=node('b',`${model.requests} 次`),track=node('div',null,'aiu-bar'),fill=node('i');name.title=`${model.provider} · ${model.model}`;head.append(name,amount);fill.style.width=`${total?model.requests/total*100:0}%`;track.append(fill);row.append(head,track,node('small',`${model.provider} · ${model.usage_known?compact(model.tokens):'未报告'} Token`));return row;}));if(!report.models.length)$('distribution').append(node('p','暂无模型用量','aiu-distribution-empty'));}
  async function postBalance(id,button,result) {
    button.disabled=true;result.textContent='正在查询…';const active=new AbortController();balanceControllers.add(active);const timer=setTimeout(()=>active.abort(),15000);
    try {
      const options={credentials:'same-origin',redirect:'error',cache:'no-store',signal:active.signal,headers:{Accept:'application/json'}};
      const authResponse=await fetch('/api/v1/admin/session',options),session=await authResponse.json();
      if(!authResponse.ok||!session.authenticated||session.read_only||String(session.user_id)!==root.dataset.userId||!session.csrf_token)throw new Error('登录身份已变化，请重新登录。');
      const response=await fetch('/api/v1/admin/ai/balance',{...options,method:'POST',headers:{...options.headers,'Content-Type':'application/json','X-CSRF-Token':session.csrf_token},body:JSON.stringify({provider_id:id})}),data=await response.json();
      if(!response.ok)throw new Error(data?.error?.message||'余额查询失败');result.replaceChildren(node('strong',money(data.remaining,data.currency)),node('small',`${data.cached?'缓存结果':'已更新'} · ${new Date(data.checked_at).toLocaleTimeString('zh-CN')}${numeric(data.total)?` · 总额 ${money(data.total,data.currency)}`:''}`));
    }catch(error){result.textContent=error.name==='AbortError'?'查询超时，请重试。':error.message;}
    finally{clearTimeout(timer);balanceControllers.delete(active);button.disabled=false;}
  }
  function renderBalances(){const providers=report.providers.filter(p=>p.balance);$('balances').hidden=!report.config.balances||!providers.length||root.dataset.manage!=='true';if($('balances').hidden)return;$('balance-grid').replaceChildren(...providers.map(p=>{const card=node('article'),head=node('div'),button=node('button','查询余额','aiu-detail-button'),result=node('div','尚未查询','aiu-balance-result');button.type='button';button.addEventListener('click',()=>postBalance(p.id,button,result));head.append(node('b',p.name),button);card.append(head,result);return card;}));}
  function detail(record){selectedID=record.id;$('copy-status').textContent='';const body=$('detail-body');body.replaceChildren();
    const section=(title,entries)=>{const block=node('section'),dl=node('dl');block.append(node('h3',title));for(const[label,value]of entries){const row=node('div');row.append(node('dt',label),node('dd',String(value)));dl.append(row);}block.append(dl);body.append(block);};
    section('请求信息',[['请求 ID',record.id],['开始时间',new Date(record.at).toLocaleString('zh-CN',{timeZone:report.timezone,hour12:false})+' · '+report.timezone],['接口',record.provider],['模型',record.model],['协议',record.protocol||'Chat Completions'],['用途',purpose(record)],['状态',statuses[record.status]||record.status],['用户 ID',record.user_id],['错误代码',record.error_code||'—'],['上游状态码',record.upstream_status||'—']]);
    section('用量明细',[['输入 Token',count(record.usage.prompt_tokens)],['输出 Token',count(record.usage.completion_tokens)],['总 Token',count(record.usage.total_tokens)],['总量来源',record.usage.total_source==='reported'?'接口报告':record.usage.total_source==='calculated'?'由完整输入 / 输出计算':'未报告'],['缓存读取',count(record.usage.cached_tokens)],['缓存写入',count(record.usage.cache_creation_tokens)],['推理 Token',count(record.usage.reasoning_tokens)],['工具提示 Token',count(record.usage.tool_prompt_tokens)],['图片数量',record.kind==='image'?record.images:'—']]);
    section('响应表现',[['请求耗时',duration(record.duration_ms)],['首响应',duration(record.first_token_ms)],['平均输出速度',record.kind==='text'?tps(record.usage.completion_tokens,record.duration_ms):'—']]);
    const price=record.cost.pricing,entries=[['估算费用',money(record.cost.amount,record.cost.currency)],['完整性',numeric(record.cost.amount)?record.cost.complete?'完整估算':'部分估算；缺失用量或请求未成功':'未计价']];
    if(price){entries.push(['计价方式',({tokens:'每百万 Token',image:'每张图片',request:'每次成功请求'})[price.mode]],['币种',price.currency],['倍率',price.multiplier]);if(price.mode==='tokens')entries.push(['输入单价',count(price.input)],['输出单价',count(price.output)],['缓存读取单价',numeric(price.cache_read)?count(price.cache_read):'按输入单价'],['缓存写入单价',numeric(price.cache_write)?count(price.cache_write):'按输入单价']);else entries.push(['单价',count(price.flat)]);}
    section('当次计价快照',entries);body.append(node('p','— 表示未报告或未配置。未保存提示词、正文、思考内容或密钥。','aiu-footnote'));$('detail').showModal();body.scrollTop=0;
  }
  $('close').addEventListener('click',()=>$('detail').close());$('detail').addEventListener('click',event=>{if(event.target===$('detail')){const r=$('detail').getBoundingClientRect();if(event.clientX<r.left||event.clientX>r.right||event.clientY<r.top||event.clientY>r.bottom)$('detail').close();}});
  $('copy').addEventListener('click',async()=>{try{await navigator.clipboard.writeText(selectedID);$('copy-status').textContent='已复制';}catch{$('copy-status').textContent='复制失败，可手动选择请求 ID。';}});
  $('filters').addEventListener('submit',event=>{event.preventDefault();before=0;history=[];void load();});
  $('range').addEventListener('change',()=>{$$('custom').forEach(el=>el.hidden=$('range').value!=='custom');if($('range').value!=='custom'){before=0;history=[];void load();}});
  $('provider').addEventListener('change',()=>{if(report)options();});
  $('refresh').addEventListener('click',()=>{before=0;history=[];void load();});$('metric').addEventListener('change',()=>{if(report)drawChart();});$('currency').addEventListener('change',()=>{if(report)drawChart();});
  $('next').addEventListener('click',()=>{if(!next||loadPending)return;history.push(before);before=next;void load();});$('prev').addEventListener('click',()=>{if(!history.length||loadPending)return;before=history.pop();void load();});
  window.addEventListener('pagehide',()=>{serial++;controller?.abort();balanceControllers.forEach(c=>c.abort());});
  window.addEventListener('pageshow',event=>{if(event.persisted)void load();});void load();
})();
