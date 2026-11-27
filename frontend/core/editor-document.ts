import { DOMParser as DocumentParser, Schema } from 'prosemirror-model';
import type { Node as DocumentNode, NodeSpec, MarkSpec } from 'prosemirror-model';
import { schema as basicSchema } from 'prosemirror-schema-basic';
import { addListNodes } from 'prosemirror-schema-list';
import { tableNodes } from 'prosemirror-tables';

export const MAX_DOCUMENT_BYTES = 2 * 1024 * 1024;
export const MAX_DOCUMENT_BLOCKS = 200;
export const textHTML = (value: unknown): string => String(value ?? '').replace(/[&<>]/g, character => ({'&': '&amp;', '<': '&lt;', '>': '&gt;'}[character]!));
const attributeHTML = (value: unknown): string => textHTML(value).replace(/"/g, '&quot;');
export function safeLink(value: string): boolean {
  if (!value || /[\u0000-\u0020\u007f\\<>"']/.test(value)) return false;
  return /^(https?:\/\/|mailto:|\/(?!\/)|#)/i.test(value);
}
export function safeImage(value: string): boolean {
  if (!/^\/(media|theme-assets)\//.test(value) || /[\s\\<>"'?#]/.test(value)) return false;
  try { return new URL(value, 'https://newpress.invalid').pathname === value; } catch { return false; }
}

export function documentSchema(): Schema {
  let nodes = addListNodes(basicSchema.spec.nodes, 'paragraph block*', 'block');
  nodes = nodes.update('doc', {content: 'block+', attrs: {tail: {default: ''}}});
  nodes = nodes.update('code_block', {
    ...nodes.get('code_block')!, attrs: {language: {default: ''}},
    parseDOM: [{tag: 'pre', preserveWhitespace: 'full', getAttrs: element => ({language: element.querySelector('code')?.className.replace(/^language-/, '') || ''})}],
    toDOM: node => ['pre', ['code', node.attrs.language ? {class: 'language-' + node.attrs.language} : {}, 0]],
  });
  nodes = nodes.update('image', {
    ...nodes.get('image')!,
    parseDOM: [{tag: 'img[src]', getAttrs: element => safeImage(element.getAttribute('src') || '') ? {src: element.getAttribute('src'), alt: element.getAttribute('alt'), title: element.getAttribute('title')} : false}],
  });
  nodes = nodes.append(tableNodes({tableGroup: 'block', cellContent: 'block+',cellAttributes:{}}));
  nodes = nodes.append({
    callout: {group: 'block', content: 'block+', defining: true, attrs: {tone: {default: 'note'}},
      parseDOM: [{tag: 'aside[data-callout]', getAttrs: element => ['note','tip','warning'].includes(element.dataset.callout || '') ? {tone: element.dataset.callout} : false}],
      toDOM: node => ['aside', {'data-callout': node.attrs.tone}, 0]},
    figure: {group: 'block', atom: true, draggable: true, attrs: {src: {}, alt: {default: ''}, width: {default: 1}, height: {default: 1}, caption: {default: ''}},
      parseDOM: [{tag: 'figure', getAttrs: element => { const image = element.querySelector('img'); return image && safeImage(image.getAttribute('src') || '') ? {src:image.getAttribute('src'),alt:image.alt,width:Number(image.getAttribute('width')) || 1,height:Number(image.getAttribute('height')) || 1,caption:element.querySelector('figcaption')?.textContent || ''} : false; }}],
      toDOM: node => ['figure', ['img', {src:node.attrs.src,alt:node.attrs.alt,width:node.attrs.width,height:node.attrs.height}], ['figcaption', node.attrs.caption]]},
    raw_block: {group: 'block', atom: true, draggable: true, attrs: {html: {default: ''}, label: {default: '保留的 HTML'}, kind: {default: 'html'}},
      toDOM: node => ['div', {class:'document-preserved', 'data-preserved-block':''}, node.attrs.label]},
  } as Record<string, NodeSpec>);
  // Source identity is metadata, never an HTML attribute. Immutable editor nodes
  // let the codec reuse untouched original markup, including whitespace.
  nodes.forEach((name, spec) => {
    if (name !== 'text' && name !== 'doc') nodes = nodes.update(name, {...spec, attrs: {...spec.attrs, origin:{default:null}, prefix:{default:''}}});
  });
  let marks = basicSchema.spec.marks;
  marks = marks.update('link', {...marks.get('link')!, parseDOM: [{tag:'a[href]', getAttrs: element => safeLink(element.getAttribute('href') || '') ? {href:element.getAttribute('href'),title:element.getAttribute('title')} : false}]});
  marks = marks.addToEnd('underline', {parseDOM:[{tag:'u'}],toDOM:()=>['u',0]} as MarkSpec);
  return new Schema({nodes,marks});
}

export function nodeHTML(node: DocumentNode): string {
  let output = '';
  node.forEach(child => { output += nodeHTML(child); });
  switch(node.type.name) {
    case 'text': output = textHTML(node.text); break;
    case 'doc': return output;
    case 'paragraph': return `<p>${output}</p>`;
    case 'heading': return `<h${node.attrs.level}>${output}</h${node.attrs.level}>`;
    case 'blockquote': return `<blockquote>${output}</blockquote>`;
    case 'code_block': return `<pre><code${node.attrs.language ? ` class="language-${attributeHTML(node.attrs.language)}"` : ''}>${textHTML(node.textContent)}</code></pre>`;
    case 'hard_break': output = '<br>'; break;
    case 'horizontal_rule': return '<hr>';
    case 'bullet_list': return `<ul>${output}</ul>`;
    case 'ordered_list': return `<ol${node.attrs.order !== 1 ? ` start="${node.attrs.order}"` : ''}>${output}</ol>`;
    case 'list_item': return `<li>${output}</li>`;
    case 'callout': return `<aside data-callout="${attributeHTML(node.attrs.tone)}">${output}</aside>`;
    case 'figure': return `<figure><img src="${attributeHTML(node.attrs.src)}" alt="${attributeHTML(node.attrs.alt)}" width="${node.attrs.width}" height="${node.attrs.height}">${node.attrs.caption ? `<figcaption>${textHTML(node.attrs.caption)}</figcaption>` : ''}</figure>`;
    case 'image': output = `<img src="${attributeHTML(node.attrs.src)}" alt="${attributeHTML(node.attrs.alt || '')}"${node.attrs.title ? ` title="${attributeHTML(node.attrs.title)}"` : ''}>`; break;
    case 'table': return `<table><tbody>${output}</tbody></table>`;
    case 'table_row': return `<tr>${output}</tr>`;
    case 'table_cell': case 'table_header': {
      const tag = node.type.name === 'table_header' ? 'th' : 'td';
      return `<${tag}${tag === 'th' ? ' scope="col"' : ''}${node.attrs.colspan > 1 ? ` colspan="${node.attrs.colspan}"` : ''}${node.attrs.rowspan > 1 ? ` rowspan="${node.attrs.rowspan}"` : ''}>${output}</${tag}>`;
    }
    case 'raw_block': return String(node.attrs.html);
    default: throw new Error('无法保存这个内容块');
  }
  for (const mark of [...node.marks].reverse()) {
    if (mark.type.name === 'link' && safeLink(mark.attrs.href)) output = `<a href="${attributeHTML(mark.attrs.href)}"${mark.attrs.title ? ` title="${attributeHTML(mark.attrs.title)}"` : ''}>${output}</a>`;
    else if (mark.type.name === 'strong') output = `<strong>${output}</strong>`;
    else if (mark.type.name === 'em') output = `<em>${output}</em>`;
    else if (mark.type.name === 'code') output = `<code>${output}</code>`;
    else if (mark.type.name === 'underline') output = `<u>${output}</u>`;
  }
  return output;
}

const allowedAttributes: Record<string, string[]> = {
  p:[],h1:[],h2:[],h3:[],h4:[],h5:[],h6:[],blockquote:[],pre:[],code:['class'],
  strong:[],b:[],em:[],i:[],u:[],a:['href','title'],br:[],hr:[],ul:[],ol:['start'],li:[],
  table:[],thead:[],tbody:[],tr:[],th:['scope','colspan','rowspan'],td:['colspan','rowspan'],
  figure:[],figcaption:[],img:['src','alt','title','width','height'],aside:['data-callout'],
};
function supported(root: Element): boolean {
  const comments = root.ownerDocument.createTreeWalker(root, 128);
  if (comments.nextNode()) return false;
  for (const element of [root,...root.querySelectorAll('*')]) {
    const name=element.tagName.toLowerCase(), attrs=allowedAttributes[name];
    if (!attrs || [...element.attributes].some(attribute=>!attrs.includes(attribute.name))) return false;
    if (name==='a' && !safeLink(element.getAttribute('href') || '')) return false;
    if (name==='img' && !safeImage(element.getAttribute('src') || '')) return false;
    if (name==='img' && element.parentElement?.tagName !== 'FIGURE' && (element.hasAttribute('width') || element.hasAttribute('height'))) return false;
    if (name==='img' && element.parentElement?.tagName === 'FIGURE' && element.hasAttribute('title')) return false;
    if (name==='th' && element.hasAttribute('scope') && element.getAttribute('scope') !== 'col') return false;
    if ((name==='td'||name==='th') && ['colspan','rowspan'].some(attribute=>element.hasAttribute(attribute) && (!/^\d{1,2}$/.test(element.getAttribute(attribute)!) || Number(element.getAttribute(attribute))<1 || Number(element.getAttribute(attribute))>(attribute==='colspan'?10:30)))) return false;
    if (name==='tr' && [...element.children].reduce((sum,cell)=>sum+(Number(cell.getAttribute('colspan'))||1),0)>10) return false;
    if (name==='table' && element.querySelectorAll('tr').length>30) return false;
    if (name==='aside' && !['note','tip','warning'].includes(element.getAttribute('data-callout') || '')) return false;
    if (name==='code' && element.hasAttribute('class') && !/^language-[a-zA-Z0-9_+.#-]{1,40}$/.test(element.className)) return false;
    if (name==='code' && element.hasAttribute('class') && element.parentElement?.tagName !== 'PRE') return false;
    if (name==='figure' && (element.children.length>2 || element.firstElementChild?.tagName!=='IMG' || [...element.childNodes].some(child=>child.nodeType===3&&child.nodeValue?.trim()) || (element.children.length===2 && element.lastElementChild?.tagName!=='FIGCAPTION'))) return false;
  }
  return !root.querySelector('figure figure, pre > :not(code), figcaption > *');
}

export function createDocumentCodec(schema: Schema, ownerDocument: Document) {
  let serial = 0;
  // Tokens stay alive only while the document/history references them. No
  // eviction can silently rewrite a still-live original after many insertions.
  const originals = new WeakMap<object,{node:DocumentNode;raw:string}>();
  function preserved(html:string, prefix=''): DocumentNode {
    const kind = /data-np-snippet=/.test(html) ? 'snippet' : /data-np-widget=/.test(html) ? 'component' : 'html';
    const label = kind==='snippet' ? '共享片段 · 保留固定版本引用' : kind==='component' ? '文章组件 · 保留原有配置' : 'HTML 保留块';
    return schema.nodes.raw_block!.create({html,prefix,label,kind});
  }
  function remember(node:DocumentNode, raw:string, prefix:string): DocumentNode {
    const origin={id:++serial};
    const result=node.type.create({...node.attrs,origin,prefix},node.content,node.marks);
    originals.set(origin,{node:result,raw});
    return result;
  }
  function parse(value:string): DocumentNode {
    const rawDoc=()=>schema.nodes.doc!.create({tail:''},value ? preserved(value) : schema.nodes.paragraph!.create());
    if(!value) return rawDoc();
    if(/<\s*(script|style|svg|math|iframe|object|embed|link|meta|base)\b|<[^>]*\bon[a-z]+\s*=/i.test(value)) return rawDoc();
    const template=ownerDocument.createElement('template');
    template.innerHTML=value;
    if(template.innerHTML!==value || template.content.childNodes.length>MAX_DOCUMENT_BLOCKS*2) return rawDoc();
    const blocks:DocumentNode[]=[];
    let prefix='';
    for(const child of template.content.childNodes) {
      const raw=child.nodeType===1 ? (child as Element).outerHTML : child.nodeType===8 ? `<!--${child.nodeValue}-->` : textHTML(child.nodeValue).replace(/\u00a0/g,'&nbsp;');
      if(child.nodeType===3 && !child.nodeValue?.trim()){prefix+=raw;continue;}
      let node:DocumentNode|undefined;
      if(child.nodeType===1 && supported(child as Element)) {
        const wrapper=ownerDocument.createElement('div');wrapper.append(child.cloneNode(true));
        const parsed=DocumentParser.fromSchema(schema).parse(wrapper,{preserveWhitespace:true});
        if(parsed.childCount===1) node=parsed.firstChild!;
      }
      blocks.push(remember(node || preserved(raw),raw,prefix));prefix='';
    }
    if(!blocks.length || blocks.length>MAX_DOCUMENT_BLOCKS) return rawDoc();
    return schema.nodes.doc!.create({tail:prefix},blocks);
  }
  function blockHTML(node:DocumentNode):string {
    const original=node.attrs.origin && originals.get(node.attrs.origin);
    return String(node.attrs.prefix || '')+(original && node.eq(original.node) ? original.raw : nodeHTML(node));
  }
  function serialize(doc:DocumentNode):string {
    if(doc.childCount===1 && doc.firstChild!.type.name==='paragraph' && !doc.firstChild!.content.size && !doc.firstChild!.attrs.origin) return String(doc.attrs.tail || '');
    const pieces:string[]=[];doc.forEach(node=>pieces.push(blockHTML(node)));return pieces.join('')+String(doc.attrs.tail || '');
  }
  function validate(doc:DocumentNode):string {
    if(doc.childCount>MAX_DOCUMENT_BLOCKS) return `正文最多 ${MAX_DOCUMENT_BLOCKS} 个顶层内容块，请在源码中继续编辑。`;
    let tableError=false;
    doc.descendants(node=>{if(node.type.name==='table') {if(node.childCount>30)tableError=true;node.forEach(row=>{let columns=0;row.forEach(cell=>{columns+=cell.attrs.colspan||1;if(cell.attrs.rowspan>30)tableError=true;});if(columns>10)tableError=true;});}});
    if(tableError) return '表格最多支持 30 行、10 列。';
    if(new TextEncoder().encode(serialize(doc)).length>MAX_DOCUMENT_BYTES) return '正文超过 2 MiB，请缩短后保存。';
    return '';
  }
  return {parse,serialize,blockHTML,preserved,validate};
}
