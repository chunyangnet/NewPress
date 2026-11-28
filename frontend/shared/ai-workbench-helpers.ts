import type { ExportReport, ReportSummary } from './ai-workbench-data';
type TextKind='p'|'pre'|'h2'|'h3'|'h4'|'h5'|'h6'|'ul'|'ol';
export type Block={kind:TextKind;text:string;start?:number}|{kind:'hr'}|{kind:'blockquote';blocks:Block[]}|{kind:'table';headers:string[];align:string[];rows:string[][]};
export type Inline={kind:'text'|'code';text:string;children?:never}|{kind:'a';href:string;title:string;children:Inline[];text?:never}|{kind:'strong'|'em'|'del';children:Inline[];text?:never};
// Non-null captures below follow a successful regex match, or an explicit array bound.
  function tableCells(line:string) {
    const value=line.trim(),cells=[];let cell='',code='',ended=false;
    for(let i=0;i<value.length;i++){
      const char=value[i];ended=false;
      if(char==='\\'&&i+1<value.length){cell+=char+value[++i];continue;}
      if(char==='`'){
        const run=value.slice(i).match(/^`+/)![0]!;
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
  const fenceStart=(line:string)=>line.match(/^ {0,3}(`{3,}|~{3,})(.*)$/);
  const rule=(line:string)=>/^ {0,3}(?:\*[ \t]*){3,}$|^ {0,3}(?:-[ \t]*){3,}$|^ {0,3}(?:_[ \t]*){3,}$/.test(line);
  export function parseMarkdown(text:string,depth=0):Block[] {
    const input=String(text).replace(/\r\n?/g,'\n').split('\n'),blocks:Block[]=[];
    let lines:string[]=[],kind:TextKind='p',start=1;
    const flush=()=>{if(lines.length)blocks.push({kind,text:lines.join('\n'),...(kind==='ol'?{start}:{})});lines=[];kind='p';};
    for(let i=0;i<input.length;i++){
      const line=input[i]!,fence=fenceStart(line);
      if(fence){
        flush();const code=[];
        while(++i<input.length){
          const closing=input[i]!.match(/^ {0,3}(`+|~+)[ \t]*$/);
          if(closing&&closing[1]![0]===fence[1]![0]&&closing[1]!.length>=fence[1]!.length)break;
          code.push(input[i]!);
        }
        blocks.push({kind:'pre',text:code.join('\n')});continue;
      }
      if(!line.trim()){flush();continue;}
      const heading=line.match(/^ {0,3}(#{1,6})\s+(.+)$/);
      if(heading){flush();blocks.push({kind:`h${Math.min(heading[1]!.length+1,6)}` as TextKind,text:heading[2]!.replace(/\s+#+\s*$/,'').trim()});continue;}
      if(/^ {0,3}>/.test(line)&&depth<8){
        flush();const quote=[];
        while(i<input.length&&/^ {0,3}>/.test(input[i]!))quote.push(input[i++]!.replace(/^ {0,3}> ?/,''));
        i--;blocks.push({kind:'blockquote',blocks:parseMarkdown(quote.join('\n'),depth+1)});continue;
      }
      if(rule(line)){flush();blocks.push({kind:'hr'});continue;}
      const headers=tableCells(line),delimiter=i+1<input.length?tableCells(input[i+1]!):[];
      if(headers.length&&(line.includes('|')||(input[i+1]!||'').includes('|'))&&headers.length===delimiter.length&&delimiter.every(cell=>/^:?-{3,}:?$/.test(cell))){
        flush();const align=delimiter.map(cell=>cell.startsWith(':')?(cell.endsWith(':')?'center':'left'):(cell.endsWith(':')?'right':'left')),rows=[];
        i++;
        while(i+1<input.length&&input[i+1]!.trim()&&!/^ {0,3}(?:>|#{1,6}\s|[-+*]\s|\d+[.)]\s)/.test(input[i+1]!)&&!fenceStart(input[i+1]!)&&!rule(input[i+1]!)){
          const cells=tableCells(input[++i]!);rows.push(headers.map((_,column)=>cells[column]||''));
        }
        blocks.push({kind:'table',headers,align,rows});continue;
      }
      const bullet=line.match(/^\s*[-*+]\s+(.+)$/),ordered=line.match(/^\s*(\d+)[.)]\s+(.+)$/);
      if(bullet||ordered){
        const next=bullet?'ul':'ol';if(kind!==next)flush();
        if(!lines.length&&ordered)start=Number(ordered[1]!);
        kind=next;lines.push(bullet?bullet[1]!:ordered![2]!);continue;
      }
      if(i+1<input.length&&/^ {0,3}(?:=+|-+)[ \t]*$/.test(input[i+1]!)){
        flush();blocks.push({kind:input[++i]!.trim()[0]==='='?'h2':'h3',text:line.trim()});continue;
      }
      if(kind!=='p')flush();lines.push(line);
    }
    flush();return blocks;
  }
  export function safeLink(value:string) {
    if(/[\s\u0000-\u001f\u007f\\]/.test(value))return '';
    if(/^\/(?!\/)|^#/.test(value))return value;
    if(!/^(?:https?:\/\/|mailto:)/i.test(value))return '';
    try{const url=new URL(value);return url.protocol==='mailto:'?value:(url.hostname?value:'');}catch{return '';}
  }
  export function parseInline(text:string,depth=0,links=true):Inline[] {
    if(depth>=8)return [{kind:'text',text}];
    // Tokens never contain HTML. Code is recognized before formatting, and link
    // destinations pass a scheme allowlist before becoming DOM attributes.
    const regex=/\\([\\`*{}\[\]()#+\-.!_>|~])|(`+)([\s\S]*?)\2(?!`)|(!?)\[([^\]\n]*)\]\(([^ \t\n]*?)(?:[ \t]+"([^"\n]*)")?\)|(\*\*|__|~~)(?=\S)([^\n]*?\S)\8|(\*|_)(?=\S)([^\n]*?\S)\10/g;
    const tokens:Inline[]=[];let last=0,match;
    while((match=regex.exec(text))){
      if(match.index>last)tokens.push({kind:'text',text:text.slice(last,match.index)});
      if(match[1]!)tokens.push({kind:'text',text:match[1]!});
      else if(match[2]!)tokens.push({kind:'code',text:match[3]!.replace(/\n/g,' ')});
      else if(match[4]!!==undefined){
        const href=!match[4]!&&links?safeLink(match[6]!):'';
        tokens.push(href?{kind:'a',href,title:match[7]!||'',children:parseInline(match[5]!,depth+1,false)}:{kind:'text',text:match[0]!});
      }else{
        const marker=match[8]!||match[10]!,index=match.index;
        // Underscores inside identifiers are literal, as in article_id_value.
        if(marker.includes('_')&&/[\p{L}\p{N}]/u.test(text[index-1]||'')&&/[\p{L}\p{N}]/u.test(text[regex.lastIndex]||''))tokens.push({kind:'text',text:match[0]!});
        else tokens.push({kind:marker==='~~'?'del':marker.length===2?'strong':'em',children:parseInline(match[9]!||match[11]!,depth+1,links)});
      }
      last=regex.lastIndex;
    }
    if(last<text.length)tokens.push({kind:'text',text:text.slice(last)});
    return tokens;
  }
  export function renderMarkdown(text:string,doc:Pick<Document,'createElement'|'createTextNode'>) {
    const create=<K extends keyof HTMLElementTagNameMap>(tag:K,text?:string|null,cls?:string)=>{const el=doc.createElement(tag);if(text!=null)el.textContent=text;if(cls)el.className=cls;return el;};
    function inline(parent:HTMLElement,text:string){
      const append=(target:HTMLElement,tokens:Inline[])=>{for(const token of tokens){
        if(token.kind==='text'){target.append(doc.createTextNode(token.text));continue;}
        const el=create(token.kind,token.text);
        if(token.kind==='a'){el.setAttribute('href',token.href);el.setAttribute('rel','noopener noreferrer');if(token.title)el.setAttribute('title',token.title);}
        if(token.children)append(el,token.children);target.append(el);
      }};
      append(parent,parseInline(text));
    }
    function render(block:Block):HTMLElement{
      if(block.kind==='table'){
        const wrapper=create('div',null,'aiw-table-scroll'),table=create('table'),head=create('thead'),row=create('tr'),body=create('tbody');
        wrapper.setAttribute('role','region');wrapper.setAttribute('aria-label','报告表格');wrapper.setAttribute('tabindex','0');
        block.headers.forEach((text,index)=>{const cell=create('th');cell.setAttribute('scope','col');cell.setAttribute('data-align',block.align[index]||'left');inline(cell,text);row.append(cell);});
        head.append(row);
        for(const cells of block.rows){const row=create('tr');cells.forEach((text,index)=>{const cell=create('td');cell.setAttribute('data-align',block.align[index]||'left');inline(cell,text);row.append(cell);});body.append(row);}
        table.append(head,body);wrapper.append(table);return wrapper;
      }
      const el=create(block.kind);
      if(block.kind==='blockquote')el.append(...block.blocks.map(render));
      else if(block.kind==='ul'||block.kind==='ol'){
        if(block.kind==='ol'&&block.start!==1)el.setAttribute('start',String(block.start));
        for(const text of block.text.split('\n')){
          const item=create('li'),task=text.match(/^\[([ xX])\]\s+(.*)$/);
          if(task){const check=create('input',null,'aiw-task-check');check.type='checkbox';check.disabled=true;check.checked=task[1]!!==' ';check.setAttribute('aria-label',check.checked?'已完成':'未完成');item.className='aiw-task-item';item.append(check);inline(item,task[2]!);}
          else inline(item,text);
          el.append(item);
        }
      }else if(block.kind==='pre')el.append(create('code',block.text));
      else if('text' in block&&block.text)inline(el,block.text);
      return el;
    }
    return parseMarkdown(text).map(render);
  }
  export function createSSEParser(onEvent:(event:string,data:unknown)=>void) {
    let pending='';
    return {
      push(chunk:string){
        pending+=chunk;
        if(pending.length>1048576)throw new Error('响应片段过大，请重试。');
        let match;
        while((match=/\r?\n\r?\n/.exec(pending))){
          const frame=pending.slice(0,match.index);pending=pending.slice(match.index+match[0]!.length);
          let event='message';const data=[];
          for(const line of frame.split(/\r?\n/)){if(line.startsWith('event:'))event=line.slice(6).trim();if(line.startsWith('data:'))data.push(line.slice(5).replace(/^ /,''));}
          if(data.length)onEvent(event,JSON.parse(data.join('\n')));
        }
      },
      finish(){if(pending.trim())throw new Error('连接提前结束，已保留收到的文字。');}
    };
  }
  export function reportMarkdown(report:ExportReport, complete=true) {
    const source=report.source;
    return `# ${report.title||'AI 报告'}\n\n${complete?'':'> 未完成或未保存的报告，请核对后使用。\n\n'}生成时间：${report.created_at||''}\n\n模型：${report.model||''}\n\n${source?`范围：${source.start} 至 ${source.end}（${source.timezone}）\n\n资料：${source.scope}\n\n`:''}${report.content||''}\n\n---\nAI 分析仅供参考，操作前请核对。\n`;
  }
  export function filterReports<T extends ReportSummary>(reports:T[],query:string,task:string):T[] {
    const q=String(query||'').trim().toLocaleLowerCase();
    return reports.filter(r=>(!task||r.task===task)&&(!q||`${r.title} ${r.model} ${r.created_at} ${r.provider}`.toLocaleLowerCase().includes(q)));
  }
