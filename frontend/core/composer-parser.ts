import { MAX_BLOCKS, escapeText, renderBlock, serialize, record } from './composer-core';
import type { ComposerBlock, ComposerState, ComponentConfig } from './composer-types';
type Fields = Omit<ComposerBlock, 'id' | 'raw' | 'prefix' | 'dirty'>;

function plain(node: Element): string | null {
  let text = '';
  for (const child of node.childNodes) {
    if (child.nodeType === 3) text += child.textContent || '';
    else if (child.nodeType === 1 && child.nodeName === 'BR' && (child as Element).attributes.length === 0) text += '\n';
    else return null;
  }
  return text;
}
function component(value: unknown): ComponentConfig {
  const config = record(value);
  if (typeof config.id !== 'string' || typeof config.kind !== 'string' || (config.title !== undefined && typeof config.title !== 'string')) throw new Error('组件数据无效');
  return config as ComponentConfig;
}
function decode(node: Element, raw: string): Fields {
  let candidate: Fields | null = null;
  const tag = node.nodeName.toLowerCase(), text = plain(node), first = node.firstElementChild;
  if ((tag === 'p' || tag === 'h2' || tag === 'h3') && text !== null) candidate = { kind: tag === 'p' ? 'paragraph' : tag, text };
  else if (tag === 'blockquote' && node.children.length === 1 && first?.tagName === 'P' && plain(first) !== null) candidate = { kind: 'quote', text: plain(first)! };
  else if (tag === 'pre' && node.children.length === 1 && first?.tagName === 'CODE' && [...first.childNodes].every(child => child.nodeType === 3)) candidate = { kind: 'code', text: node.textContent || '', language: (first.getAttribute('class') || '').replace(/^language-/, '') };
  else if (tag === 'aside' && node.getAttribute('data-np-widget') === 'post') {
    const link = node.querySelector('p > a');
    if (link) candidate = { kind: 'post', postID: Number(node.getAttribute('data-np-post')), layout: node.getAttribute('data-np-layout') || '', url: link.getAttribute('href') || '', title: link.textContent || '' };
  } else if (tag === 'aside' && node.hasAttribute('data-np-config') && window.NewPressComponentLibrary) {
    try {
      const config = component(window.NewPressComponentLibrary.decode(node.getAttribute('data-np-config')!));
      if (node.attributes.length === 2 && config.kind === node.getAttribute('data-np-widget') && node.children.length === 1 && first?.tagName === 'P' && plain(first) !== null) return { kind: 'component', config };
    } catch { /* Keep unknown source intact. */ }
  } else if (tag === 'aside' && node.children.length === 1 && first?.tagName === 'P' && plain(first) !== null) candidate = { kind: 'callout', tone: node.getAttribute('data-callout') || '', text: plain(first)! };
  else if ((tag === 'ul' || tag === 'ol') && [...node.childNodes].every(child => child.nodeName === 'LI' && [...child.childNodes].every(value => value.nodeType === 3))) candidate = { kind: 'list', ordered: tag === 'ol', text: [...node.children].map(child => child.textContent).join('\n') };
  else if (tag === 'table') {
    const rows = [...node.querySelectorAll('tr')];
    if (rows.every(row => [...row.children].every(cell => ['TH', 'TD'].includes(cell.tagName) && plain(cell) !== null))) candidate = { kind: 'table', rows: rows.map(row => [...row.children].map(cell => plain(cell)!)) };
  } else if (tag === 'figure' && first?.tagName === 'IMG') {
    candidate = { kind: 'image', url: first.getAttribute('src') || '', alt: first.getAttribute('alt') || '', width: Number(first.getAttribute('width')), height: Number(first.getAttribute('height')), caption: node.querySelector('figcaption')?.textContent || '' };
  }
  // Decode only an exact representation; richer or malformed markup stays raw.
  try { if (candidate && renderBlock(candidate) === raw) return candidate; } catch { /* Keep raw. */ }
  return { kind: 'html', text: raw };
}
export function createComposerParser(document: Document): { parse(value: string): ComposerState; nextID(): string } {
  let sequence = 0;
  const nextID = () => `block-${++sequence}`;
  function parse(value: string): ComposerState {
    const rawState = (): ComposerState => ({ blocks: value ? [{ id: nextID(), kind: 'html', text: value, raw: value, prefix: '' }] : [], tail: '' });
    if (!value) return rawState();
    // Avoid instantiating executable/foreign elements even in an inert fragment.
    if (/<\s*(script|style|svg|math|iframe|object|embed|link|meta|base)\b|<[^>]*\bon[a-z]+\s*=/i.test(value)) return rawState();
    const template = document.createElement('template');
    template.innerHTML = value;
    if (template.innerHTML !== value || template.content.childNodes.length > MAX_BLOCKS * 2) return rawState();
    const blocks: ComposerBlock[] = []; let prefix = '';
    for (const node of template.content.childNodes) {
      const raw = node.nodeType === 1 ? (node as Element).outerHTML : node.nodeType === 8 ? `<!--${node.textContent}-->` : escapeText(node.textContent || '').replace(/\u00a0/g, '&nbsp;');
      if (node.nodeType === 3 && !node.textContent?.trim()) { prefix += raw; continue; }
      const fields: Fields = node.nodeType === 1 ? decode(node as Element, raw) : { kind: 'html', text: raw };
      blocks.push({ ...fields, raw, prefix, id: nextID() }); prefix = '';
    }
    const result = { blocks, tail: prefix };
    return blocks.length > MAX_BLOCKS || serialize(result) !== value ? rawState() : result;
  }
  return { parse, nextID };
}
