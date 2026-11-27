import { EditorState, Plugin, TextSelection, NodeSelection, Selection } from 'prosemirror-state';
import type { Command } from 'prosemirror-state';
import { EditorView } from 'prosemirror-view';
import type { NodeView } from 'prosemirror-view';
import type { Node as DocumentNode } from 'prosemirror-model';
import { keymap } from 'prosemirror-keymap';
import { baseKeymap, toggleMark, setBlockType, wrapIn, chainCommands, exitCode, lift, selectParentNode } from 'prosemirror-commands';
import { history, undo, redo, undoDepth, redoDepth, closeHistory } from 'prosemirror-history';
import { wrapInList, splitListItem, liftListItem, sinkListItem } from 'prosemirror-schema-list';
import { inputRules, textblockTypeInputRule, wrappingInputRule, undoInputRule } from 'prosemirror-inputrules';
import { tableEditing, addRowAfter, addColumnAfter, deleteRow, deleteColumn, deleteTable, goToNextCell } from 'prosemirror-tables';
import { createDocumentCodec, documentSchema, safeLink, safeImage, MAX_DOCUMENT_BYTES } from './editor-document';
import { resourceSelection } from './editor-resource-selection';
import type {} from './component-types';
import { renderBlock, serialize, replacement, localImageURL, localPostURL } from './composer-core';
import { createComposerParser } from './composer-parser';
import type { BlockEditorAPI } from './composer-types';

type SourceRange = {start:number;end:number;value:string};
type TextRange = {from:number;to:number;text:string;body:string};
type DocumentAPI = BlockEditorAPI & Required<Pick<BlockEditorAPI, 'captureText' | 'replaceText' | 'focus' | 'setMode'>>;
declare global {
  interface Window {
    NewPressDocumentEditor?: (form:HTMLFormElement)=>boolean;
  }
}

const initialized = new WeakSet<HTMLFormElement>();
window.NewPressDocumentEditor = form => {
  if (initialized.has(form)) return true;
  const source=form.querySelector<HTMLTextAreaElement>('[data-content-source]')!;
  const root=form.querySelector<HTMLElement>('[data-composer]')!;
  const mount=form.querySelector<HTMLElement>('[data-composer-list]')!;
  const toolbar=form.querySelector<HTMLElement>('[data-composer-toolbar]')!;
  const sourcePane=form.querySelector<HTMLElement>('[data-composer-source-pane]')!;
  if(!source || !root || !mount || !toolbar || !sourcePane) return false;
  const {parse} = createComposerParser(document);
  const feedback=form.querySelector<HTMLElement>('[data-composer-feedback]');
  const readOnly=()=>source.disabled||source.readOnly||source.matches(':disabled');
  const schema=documentSchema(), codec=createDocumentCodec(schema,document);
  let previous=source.value, syncing=false, mode='blocks', composing=false;
  const say=(message:string)=>{if(feedback){feedback.textContent=message;feedback.hidden=!message;}};
  const emit=()=>{
    const value=codec.serialize(view.state.doc);
    if(value===previous)return;
    previous=value;syncing=true;source.value=value;
    source.setCustomValidity(codec.validate(view.state.doc));
    source.dispatchEvent(new Event('input',{bubbles:true}));syncing=false;
  };
  const topRange=(position:number):{index:number;from:number;to:number;node:DocumentNode}=>{
    let found={index:0,from:0,to:view.state.doc.firstChild!.nodeSize,node:view.state.doc.firstChild!};
    view.state.doc.forEach((node,offset,index)=>{if(position>=offset && position<offset+node.nodeSize)found={index,from:offset,to:offset+node.nodeSize,node};});
    return found;
  };
  const closeMenus=()=>form.querySelectorAll<HTMLDetailsElement>('.editor-insert-menu[open],.document-block-menu[open]').forEach(menu=>{menu.open=false;});
  function insertionMenu():boolean {
    const menu=form.querySelector<HTMLDetailsElement>('.editor-insert-menu');
    if(!menu || readOnly())return false;
    menu.dataset.returnToDocument='true';menu.open=true;menu.querySelector<HTMLInputElement>('[data-insert-search]')?.focus();return true;
  }
  const commands:Record<string,Command>={
    bold:toggleMark(schema.marks.strong!),italic:toggleMark(schema.marks.em!),code:toggleMark(schema.marks.code!),
    paragraph:setBlockType(schema.nodes.paragraph!),h2:setBlockType(schema.nodes.heading!,{level:2}),h3:setBlockType(schema.nodes.heading!,{level:3}),
    quote:wrapIn(schema.nodes.blockquote!),list:wrapInList(schema.nodes.bullet_list!),ordered:wrapInList(schema.nodes.ordered_list!),
    'code-block':setBlockType(schema.nodes.code_block!),undo,redo,lift,
    'row-add':addRowAfter,'column-add':addColumnAfter,'row-delete':deleteRow,'column-delete':deleteColumn,'table-delete':deleteTable,
  };
  const plugins=[
    new Plugin({filterTransaction(transaction){
      if(!transaction.docChanged)return true;
      // Loading existing oversized source is allowed; editing must remain within limits.
      if(transaction.getMeta('source-load'))return true;
      const message=codec.validate(transaction.doc);if(message){say(message);return false;}return true;
    }}),
    history({depth:20,newGroupDelay:700}),
    inputRules({rules:[
      textblockTypeInputRule(/^(#{1,3})\s$/,schema.nodes.heading!,match=>({level:Math.max(2,match[1]!.length)})),
      wrappingInputRule(/^>\s$/,schema.nodes.blockquote!),
      wrappingInputRule(/^[-*]\s$/,schema.nodes.bullet_list!),
      wrappingInputRule(/^(\d+)\.\s$/,schema.nodes.ordered_list!,match=>({order:Number(match[1])})),
      textblockTypeInputRule(/^```\s$/,schema.nodes.code_block!),
    ]}),
    keymap({
      'Mod-z':undo,'Mod-y':redo,'Shift-Mod-z':redo,'Mod-b':commands.bold!,'Mod-i':commands.italic!,
      'Mod-`':commands.code!,'Mod-k':()=>{openLink();return true;},
      'Enter':splitListItem(schema.nodes.list_item!),
      'Shift-Enter':chainCommands(exitCode,(state,dispatch)=>{dispatch?.(state.tr.replaceSelectionWith(schema.nodes.hard_break!.create()).scrollIntoView());return true;}),
      'Tab':chainCommands(goToNextCell(1),sinkListItem(schema.nodes.list_item!)),
      'Shift-Tab':chainCommands(goToNextCell(-1),liftListItem(schema.nodes.list_item!)),
      'Backspace':undoInputRule,'Alt-ArrowUp':()=>moveBlock(-1),'Alt-ArrowDown':()=>moveBlock(1),
      'Mod-Enter':exitCode,'Escape':selectParentNode,
    }),
    keymap(baseKeymap),tableEditing(),
  ];
  const view=new EditorView(mount,{
    state:EditorState.create({schema,doc:codec.parse(source.value),plugins}),
    editable:()=>!readOnly(),
    attributes:{class:'newpress-document','aria-label':'正文','aria-multiline':'true',role:'textbox',spellcheck:'true','data-placeholder':'从这里开始写作，输入 / 插入内容…'},
    dispatchTransaction(transaction){
      const result=view.state.applyTransaction(transaction);view.updateState(result.state);
      if(result.transactions.some(item=>item.docChanged) && !composing)emit();
      controls();
    },
    handleKeyDown(_view,event){
      if(event.isComposing || composing)return false;
      if(event.key==='/' && view.state.selection.empty && view.state.selection.$from.parent.type.name==='paragraph' && !view.state.selection.$from.parent.content.size)return insertionMenu();
      return false;
    },
    handleDOMEvents:{
      compositionstart(){composing=true;return false;},
      compositionend(){composing=false;queueMicrotask(emit);return false;},
    },
    transformPastedHTML(html){
      const template=document.createElement('template');template.innerHTML=html;
      template.content.querySelectorAll('script,style,iframe,object,embed,svg,math,link,meta,base').forEach(node=>node.remove());
      template.content.querySelectorAll('img').forEach(node=>{if(!safeImage(node.getAttribute('src')||''))node.remove();});
      template.content.querySelectorAll('*').forEach(node=>{for(const attribute of [...node.attributes])if(/^on/i.test(attribute.name))node.removeAttribute(attribute.name);});
      return template.innerHTML;
    },
    nodeViews:{raw_block:(node,_view,getPosition)=>rawView(node,getPosition),figure:(node,_view,getPosition)=>figureView(node,getPosition)},
  });
  function controls():void {
    form.querySelectorAll<HTMLButtonElement>('[data-composer-undo]').forEach(button=>{button.disabled=readOnly()||!undoDepth(view.state);});
    form.querySelectorAll<HTMLButtonElement>('[data-composer-redo]').forEach(button=>{button.disabled=readOnly()||!redoDepth(view.state);});
    const count=form.querySelector('[data-composer-count]');if(count)count.textContent=String(view.state.doc.childCount);
    form.querySelectorAll<HTMLButtonElement>('[data-document-command]').forEach(button=>{
      const command=commands[button.dataset.documentCommand!];button.disabled=readOnly()||!command?.(view.state);
      const name={bold:'strong',italic:'em',code:'code'}[button.dataset.documentCommand!];
      if(name){const {from,to,empty,$from}=view.state.selection;const mark=schema.marks[name]!;button.setAttribute('aria-pressed',String(empty?!!mark.isInSet(view.state.storedMarks||$from.marks()):view.state.doc.rangeHasMark(from,to,mark)));}
    });
    let table=false;for(let depth=view.state.selection.$from.depth;depth>0;depth--)if(view.state.selection.$from.node(depth).type.name==='table')table=true;
    form.querySelectorAll<HTMLElement>('[data-document-table-tools]').forEach(element=>{element.hidden=!table||mode!=='blocks';});
    root.dataset.empty=String(view.state.doc.childCount===1&&!view.state.doc.firstChild!.content.size&&view.state.doc.firstChild!.type.name==='paragraph');
    const style=form.querySelector<HTMLSelectElement>('[data-document-style]');
    if(style){const parent=view.state.selection.$from.parent;style.value=parent.type.name==='heading'?'h'+parent.attrs.level:parent.type.name==='code_block'?'code-block':'paragraph';}
  }
  function run(command:Command):void {if(readOnly()||(mode!=='blocks'&&command!==undo&&command!==redo))return;command(view.state,view.dispatch,view);if(mode==='blocks')view.focus();else source.focus();closeMenus();}
  function rawView(node:DocumentNode,getPosition:()=>number|undefined):NodeView {
    const dom=document.createElement('section');dom.className='document-preserved';dom.contentEditable='false';
    const title=document.createElement('strong');title.textContent=node.attrs.label;
    const hint=document.createElement('p');hint.textContent='原始内容会完整保留，保存后按原有方式展示。';
    const edit=document.createElement('button');edit.type='button';edit.textContent='编辑内容';edit.disabled=readOnly();
    edit.addEventListener('click',()=>{const position=getPosition();if(position===undefined)return;openRaw(position);});
    dom.append(title,hint,edit);
    if(node.attrs.kind==='component' && (window.NewPressComponentLibrary || window.NewPressComponentLoader)){
      const component=document.createElement('button');component.type='button';component.textContent='编辑组件';component.disabled=readOnly();
      component.addEventListener('click',()=>{
        const position=getPosition();if(position===undefined)return;
        const encoded=String(node.attrs.html).match(/data-np-config="([^"]*)"/)?.[1];
        if(!encoded){openRaw(position);return;}
        try{const template=document.createElement('template');template.innerHTML=String(node.attrs.html);const value=template.content.querySelector('[data-np-config]')?.getAttribute('data-np-config');if(!value)return;const range=sourceRangeForNode(position);if(window.NewPressComponentLoader)void window.NewPressComponentLoader.edit(value,range,component);else window.NewPressComponentLibrary!.edit(window.NewPressComponentLibrary!.decode(value),range,component);}catch{openRaw(position);}
      });dom.append(component);
    }
    return {dom,stopEvent:event=>event.target instanceof Element&&!!event.target.closest('button'),ignoreMutation:()=>true};
  }
  function figureView(node:DocumentNode,getPosition:()=>number|undefined):NodeView {
    const dom=document.createElement('figure');dom.className='document-figure';dom.contentEditable='false';
    const image=document.createElement('img');image.src=node.attrs.src;image.alt=node.attrs.alt;image.width=node.attrs.width;image.height=node.attrs.height;
    const caption=document.createElement('figcaption');caption.textContent=node.attrs.caption;
    const edit=document.createElement('button');edit.type='button';edit.className='document-figure-edit';edit.textContent='图片说明';edit.disabled=readOnly();
    edit.addEventListener('click',()=>{const position=getPosition();if(position!==undefined)openImage(position);});
    dom.append(image,caption,edit);return {dom,stopEvent:event=>event.target instanceof Element&&!!event.target.closest('button'),ignoreMutation:()=>true};
  }
  const dialog=document.createElement('dialog');dialog.className='document-edit-dialog';dialog.setAttribute('aria-labelledby','document-edit-title');
  dialog.innerHTML='<header><h2 id="document-edit-title"></h2><button type="button" data-document-dialog-close aria-label="关闭">×</button></header><div data-document-dialog-body></div><p data-document-dialog-error role="alert"></p><footer><button type="button" data-document-dialog-cancel>取消</button><button type="button" class="admin-button admin-button-primary" data-document-dialog-apply>应用到正文</button></footer>';
  document.body.append(dialog);
  let applyDialog:(()=>boolean)|null=null;
  const closeDialog=()=>{dialog.close();view.focus();};
  dialog.querySelector('[data-document-dialog-close]')!.addEventListener('click',closeDialog);
  dialog.querySelector('[data-document-dialog-cancel]')!.addEventListener('click',closeDialog);
  dialog.addEventListener('close',()=>view.focus());
  dialog.querySelector('[data-document-dialog-apply]')!.addEventListener('click',()=>{if(!readOnly()&&applyDialog?.())closeDialog();});
  function field(label:string,value:string,multiline=false):HTMLInputElement|HTMLTextAreaElement {
    const wrapper=document.createElement('label');wrapper.textContent=label;
    const input=multiline?document.createElement('textarea'):document.createElement('input');input.value=value;
    if(input instanceof HTMLTextAreaElement)input.rows=12;
    wrapper.append(input);dialog.querySelector('[data-document-dialog-body]')!.append(wrapper);return input;
  }
  function prepareDialog(title:string):void {dialog.querySelector('h2')!.textContent=title;dialog.querySelector('[data-document-dialog-body]')!.replaceChildren();dialog.querySelector('[data-document-dialog-error]')!.textContent='';}
  function openRaw(position:number):void {
    if(readOnly())return;const node=view.state.doc.nodeAt(position);if(!node)return;const before=source.value;
    prepareDialog('编辑保留的 HTML');const input=field('HTML 源码',String(node.attrs.html),true);
    applyDialog=()=>{if(readOnly()||source.value!==before){dialog.querySelector('[data-document-dialog-error]')!.textContent='正文已经变化，请重新打开此内容块。';return false;}const replacement=codec.preserved(input.value,String(node.attrs.prefix||''));const transaction=closeHistory(view.state.tr).replaceWith(position,position+node.nodeSize,replacement);const error=codec.validate(transaction.doc);if(error){dialog.querySelector('[data-document-dialog-error]')!.textContent=error;return false;}view.dispatch(transaction);return true;};
    dialog.showModal();input.focus();
  }
  function openImage(position:number):void {
    if(readOnly())return;const node=view.state.doc.nodeAt(position);if(!node)return;const before=source.value;
    prepareDialog('图片说明');const alt=field('替代文本',node.attrs.alt),caption=field('图片说明',node.attrs.caption);
    applyDialog=()=>{if(source.value!==before){dialog.querySelector('[data-document-dialog-error]')!.textContent='正文已经变化，请重新打开图片设置。';return false;}view.dispatch(view.state.tr.setNodeMarkup(position,undefined,{...node.attrs,alt:alt.value,caption:caption.value}));return true;};dialog.showModal();alt.focus();
  }
  function openLink():void {
    if(readOnly()||mode!=='blocks')return;
    const selected=view.state.selection, before=source.value;
    const current=schema.marks.link!.isInSet(view.state.storedMarks||selected.$from.marks());
    prepareDialog('插入链接');const href=field('链接地址',current?.attrs.href||'https://');const label=selected.empty?field('链接文字',''):null;
    applyDialog=()=>{
      if(source.value!==before){dialog.querySelector('[data-document-dialog-error]')!.textContent='正文已经变化，请重新选择文字。';return false;}
      const url=href.value.trim();if(url&&!safeLink(url)){href.setCustomValidity('请输入 https://、http://、mailto:、站内路径或锚点。');href.reportValidity();return false;}href.setCustomValidity('');
      const transaction=view.state.tr.setSelection(selected);
      if(selected.empty){if(!url||!label?.value.trim()){href.setCustomValidity('请填写链接地址和文字。');href.reportValidity();return false;}transaction.replaceSelectionWith(schema.text(label.value,[schema.marks.link!.create({href:url})]),false);}
      else if(url)transaction.addMark(selected.from,selected.to,schema.marks.link!.create({href:url}));else transaction.removeMark(selected.from,selected.to,schema.marks.link!);
      view.dispatch(transaction.scrollIntoView());return true;
    };dialog.showModal();href.focus();
  }
  function sourceRangeForNode(position:number):SourceRange {
    const selected=topRange(position);let start=0;
    for(let index=0;index<selected.index;index++)start+=codec.blockHTML(view.state.doc.child(index)).length;
    const value=codec.blockHTML(selected.node);return {start:start+String(selected.node.attrs.prefix||'').length,end:start+value.length,value:source.value};
  }
  function capture():SourceRange {
    if(mode==='source')return {start:source.selectionStart,end:source.selectionEnd,value:source.value};
	// The empty visual document contains a synthetic paragraph, while its
	// untouched canonical source is still empty. Its serialized end is not
	// a valid source offset until a real edit has been emitted.
	if(source.value==='')return {start:0,end:0,value:''};
    const top=topRange(view.state.selection.from),range=sourceRangeForNode(view.state.selection.from);
    if(top.node.type.name==='paragraph'&&!top.node.content.size)return range;
    return {start:range.end,end:range.end,value:source.value};
  }
  function insert(markup:string,selection:SourceRange):void {
    if(readOnly())throw new Error('当前内容不可编辑。');
    const value=replacement(source.value,markup,selection),doc=codec.parse(value),error=codec.validate(doc);
    if(error)throw new Error(error);
    let offset=0,position=1,target=0;
    doc.forEach((node,pos)=>{offset+=codec.blockHTML(node).length;if(!target&&offset>=selection.start+markup.length){position=pos+node.nodeSize;target=1;}});
    const transaction=closeHistory(view.state.tr).replaceWith(0,view.state.doc.content.size,doc.content).setDocAttribute('tail',doc.attrs.tail);
    transaction.setSelection(Selection.near(transaction.doc.resolve(Math.min(position,transaction.doc.content.size)),-1));
    view.dispatch(transaction.scrollIntoView());
    if(mode==='blocks')view.focus();else{source.focus();source.setSelectionRange(selection.start+markup.length,selection.start+markup.length);}
    say('已插入，可撤销。');
  }
  function addBlock(kind:string):void {
    if(readOnly())return;
    let node:DocumentNode;
    const paragraph=schema.nodes.paragraph!;
    if(kind==='table'){
      const rows=[true,false,false].map(header=>schema.nodes.table_row!.create(null,[0,1].map(()=>schema.nodes[header?'table_header':'table_cell']!.createAndFill()!)));
      node=schema.nodes.table!.create(null,rows);
    }else if(kind==='quote')node=schema.nodes.blockquote!.create(null,paragraph.create());
    else if(kind==='callout')node=schema.nodes.callout!.create({tone:'note'},paragraph.create());
    else if(kind==='list')node=schema.nodes.bullet_list!.create(null,schema.nodes.list_item!.create(null,paragraph.create()));
    else if(kind==='code')node=schema.nodes.code_block!.create();
    else if(kind==='html')node=codec.preserved('');
    else if(kind==='h2'||kind==='h3')node=schema.nodes.heading!.create({level:kind==='h2'?2:3});
    else node=paragraph.create();
    const top=topRange(view.state.selection.from),empty=top.node.type.name==='paragraph'&&!top.node.content.size;
    const at=empty?top.from:top.to;
    const transaction=view.state.tr.replaceWith(at,empty?top.to:at,node);
    transaction.setSelection(Selection.near(transaction.doc.resolve(at+(node.isAtom?0:1)),1));
    view.dispatch(transaction.scrollIntoView());closeMenus();view.focus();if(kind==='html')openRaw(at);
  }
  function moveBlock(direction:number):boolean {
    if(readOnly())return false;const top=topRange(view.state.selection.from),next=top.index+direction;
    if(next<0||next>=view.state.doc.childCount)return false;
    const adjacent=view.state.doc.child(next),start=direction<0?top.from-adjacent.nodeSize:top.from;
    const transaction=view.state.tr.replaceWith(start,start+top.node.nodeSize+adjacent.nodeSize,direction<0?[top.node,adjacent]:[adjacent,top.node]);
    transaction.setSelection(Selection.near(transaction.doc.resolve(start+(direction>0?adjacent.nodeSize:0)+1)));view.dispatch(transaction.scrollIntoView());view.focus();return true;
  }
  function setMode(next:string):void {
    mode=next==='source'?'source':'blocks';root.hidden=mode!=='blocks';sourcePane.hidden=mode!=='source';
    toolbar.querySelectorAll('[data-composer-mode]').forEach(button=>button.setAttribute('aria-pressed',String((button as HTMLElement).dataset.composerMode===mode)));
    form.querySelectorAll<HTMLElement>('[data-document-visual-only]').forEach(element=>{element.hidden=mode!=='blocks';});
    form.dataset.editorMode=mode;controls();
  }
  form.querySelectorAll<HTMLButtonElement>('[data-document-command]').forEach(button=>{button.addEventListener('pointerdown',event=>event.preventDefault());button.addEventListener('click',()=>{const command=commands[button.dataset.documentCommand!];if(command)run(command);});});
  form.querySelector('[data-document-link]')?.addEventListener('click',openLink);
  form.querySelector<HTMLSelectElement>('[data-document-style]')?.addEventListener('change',event=>{const command=commands[(event.target as HTMLSelectElement).value];if(command)run(command);});
  form.querySelectorAll<HTMLButtonElement>('[data-composer-mode]').forEach(button=>button.addEventListener('click',()=>{setMode(button.dataset.composerMode!);if(mode==='source')source.focus();else view.focus();closeMenus();}));
  form.querySelectorAll<HTMLButtonElement>('[data-block-add]').forEach(button=>button.addEventListener('click',()=>{if(mode==='source'){const kind=button.dataset.blockAdd!;try{const block:Record<string,unknown>={kind,text:'',tone:'note'};if(kind==='table')block.rows=[['项目','说明'],['','']];insert(renderBlock(block),capture());closeMenus();}catch(error){say((error as Error).message);}}else addBlock(button.dataset.blockAdd!);}));
  form.querySelector('[data-composer-undo]')?.addEventListener('click',()=>run(undo));form.querySelector('[data-composer-redo]')?.addEventListener('click',()=>run(redo));
  form.querySelector('[data-document-move-up]')?.addEventListener('click',()=>moveBlock(-1));form.querySelector('[data-document-move-down]')?.addEventListener('click',()=>moveBlock(1));
  form.querySelector('[data-document-copy]')?.addEventListener('click',()=>{if(readOnly())return;const top=topRange(view.state.selection.from);view.dispatch(view.state.tr.insert(top.to,top.node).scrollIntoView());view.focus();});
  form.querySelector('[data-document-delete]')?.addEventListener('click',()=>{if(readOnly())return;const top=topRange(view.state.selection.from);view.dispatch(view.state.tr.delete(top.from,top.to).scrollIntoView());view.focus();});
  form.querySelector('[data-document-insert]')?.addEventListener('click',insertionMenu);
  source.addEventListener('input',()=>{
    if(syncing||source.value===previous)return;
    previous=source.value;const doc=codec.parse(source.value);
    const transaction=view.state.tr.replaceWith(0,view.state.doc.content.size,doc.content).setDocAttribute('tail',doc.attrs.tail).setMeta('source-load',true);
    view.dispatch(transaction);source.setCustomValidity(new TextEncoder().encode(source.value).length>MAX_DOCUMENT_BYTES?'正文超过 2 MiB。':'');
  });
  source.addEventListener('invalid',()=>{setMode('source');source.focus();});
  form.addEventListener('submit',()=>{if(mode==='blocks')emit();},true);
  const readonlyObserver=new MutationObserver(()=>{view.setProps({editable:()=>!readOnly()});controls();});readonlyObserver.observe(source,{attributes:true,attributeFilter:['disabled','readonly']});
  const api:DocumentAPI={
    capture,insert,setMode,focus:()=>mode==='blocks'?view.focus():source.focus(),
    replaceSelectedHTML(markup,tail=false){
      if(mode!=='blocks')return null;
      if(readOnly())throw new Error('当前内容不可编辑。');
      if(source.value!==codec.serialize(view.state.doc))throw new Error('正文已变化，请重新选择位置。');
      const result=resourceSelection(view.state,markup,codec,tail);
      view.dispatch(result.transaction.scrollIntoView());say('已移入资源，可撤销正文替换。');return result.html;
    },
    reset(value=''){previous=value;source.value=value;source.setCustomValidity('');view.updateState(EditorState.create({schema,doc:codec.parse(value),plugins}));controls();},
    captureText(){if(mode!=='blocks'||view.state.selection.empty)return null;const {from,to}=view.state.selection;return {from,to,text:view.state.doc.textBetween(from,to,'\n'),body:source.value};},
    replaceText(value,selection){if(readOnly()||selection.body!==source.value||selection.from<0||selection.to>view.state.doc.content.size||view.state.doc.textBetween(selection.from,selection.to,'\n')!==selection.text)return false;const transaction=view.state.tr.insertText(value,selection.from,selection.to);if(codec.validate(transaction.doc))return false;view.dispatch(transaction.scrollIntoView());view.focus();return true;},
    parse,serialize,renderBlock,localImageURL,localPostURL,
  };
  window.ChunnelBlockEditor=api;toolbar.hidden=false;setMode('blocks');root.classList.add('is-document-editor');initialized.add(form);
  form.dispatchEvent(new CustomEvent('newpress:document-ready'));
  window.addEventListener('pagehide',event=>{if(!(event as PageTransitionEvent).persisted){readonlyObserver.disconnect();view.destroy();dialog.remove();}});
  return true;
};
const form = document.querySelector<HTMLFormElement>('[data-content-editor][data-document-editor]');
if (form) window.NewPressDocumentEditor(form);
