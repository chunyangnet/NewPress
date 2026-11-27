import type { EditorSourceSelection } from './media-types';
import type {} from './component-types';

export const MAX_BYTES = 2 * 1024 * 1024, MAX_BLOCKS = 200, MAX_ROWS = 30, MAX_COLUMNS = 10;
export const bytes = (value: string): number => new TextEncoder().encode(value).length;
export const escapeText = (value: string): string => value.replace(/[&<>]/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' })[char]!);
const escapeAttr = (value: string): string => escapeText(value).replace(/"/g, '&quot;');
const lines = (value: string): string => escapeText(value).replace(/\r\n?/g, '\n').replace(/\n/g, '<br>');
export function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('内容块数据无效');
  return value as Record<string, unknown>;
}
const textField = (value: unknown): string => {
  if (value === undefined || value === null) return '';
  if (typeof value !== 'string') throw new Error('内容块文本无效');
  return value;
};
export function localImageURL(value: unknown): value is string {
  if (typeof value !== 'string' || !/^\/(media|theme-assets)\//.test(value) || /[\s\\<>"'?#]/.test(value)) return false;
  try { const url = new URL(value, 'https://newpress.invalid'); return url.origin === 'https://newpress.invalid' && url.pathname === value; } catch { return false; }
}
export function localPostURL(value: unknown): value is string {
  if (typeof value !== 'string' || !/^\/posts\/[^/?#\\\s<>"']+$/.test(value)) return false;
  try { const url = new URL(value, 'https://newpress.invalid'); return url.origin === 'https://newpress.invalid' && url.pathname === value; } catch { return false; }
}
function dimension(value: unknown): value is number { return typeof value === 'number' && Number.isSafeInteger(value) && value >= 1 && value <= 32768; }
function tableRows(value: unknown): string[][] {
  if (!Array.isArray(value) || value.length < 2 || value.length > MAX_ROWS) throw new Error('表格需要 2–30 行、1–10 列，每行列数一致');
  const first: unknown = value[0];
  if (!Array.isArray(first) || first.length < 1 || first.length > MAX_COLUMNS) throw new Error('表格需要 2–30 行、1–10 列，每行列数一致');
  return value.map((row: unknown) => {
    if (!Array.isArray(row) || row.length !== first.length) throw new Error('表格需要 2–30 行、1–10 列，每行列数一致');
    return row.map((cell: unknown) => textField(cell));
  });
}
export function renderBlock(value: unknown): string {
  const block = record(value), text = textField(block.text);
  switch (block.kind) {
    case 'paragraph': return `<p>${lines(text)}</p>`;
    case 'h2': case 'h3': return `<${block.kind}>${lines(text)}</${block.kind}>`;
    case 'quote': return `<blockquote><p>${lines(text)}</p></blockquote>`;
    case 'code': {
      const language = textField(block.language);
      if (language && !/^[a-zA-Z0-9_+.#-]{1,40}$/.test(language)) throw new Error('代码语言不正确');
      return `<pre><code${language ? ` class="language-${language}"` : ''}>${escapeText(text)}</code></pre>`;
    }
    case 'callout': {
      const tone = textField(block.tone);
      if (!['note', 'tip', 'warning'].includes(tone)) throw new Error('提示类型不正确');
      return `<aside data-callout="${tone}"><p>${lines(text)}</p></aside>`;
    }
    case 'list': {
      if (block.ordered !== undefined && typeof block.ordered !== 'boolean') throw new Error('列表类型不正确');
      const tag = block.ordered ? 'ol' : 'ul';
      return `<${tag}>${text.replace(/\r\n?/g, '\n').split('\n').map(value => `<li>${escapeText(value)}</li>`).join('')}</${tag}>`;
    }
    case 'table': {
      const rows = tableRows(block.rows);
      const row = (cells: string[], head: boolean) => `<tr>${cells.map(value => head ? `<th scope="col">${lines(value)}</th>` : `<td>${lines(value)}</td>`).join('')}</tr>`;
      return `<table><thead>${row(rows[0]!, true)}</thead><tbody>${rows.slice(1).map(cells => row(cells, false)).join('')}</tbody></table>`;
    }
    case 'image': {
      if (!localImageURL(block.url) || !dimension(block.width) || !dimension(block.height)) throw new Error('图片地址或尺寸无效，请从媒体库重新选择');
      const caption = textField(block.caption);
      return `<figure><img src="${escapeAttr(block.url)}" alt="${escapeAttr(textField(block.alt))}" width="${block.width}" height="${block.height}">${caption ? `<figcaption>${escapeText(caption)}</figcaption>` : ''}</figure>`;
    }
    case 'post': {
      if (typeof block.postID !== 'number' || !Number.isSafeInteger(block.postID) || block.postID < 1 || !['card', 'compact'].includes(textField(block.layout)) || !localPostURL(block.url) || typeof block.title !== 'string' || !block.title.trim() || [...block.title].length > 200) throw new Error('文章引用无效，请重新选择本站文章');
      return `<aside data-np-widget="post" data-np-post="${block.postID}" data-np-layout="${block.layout}"><p><a href="${escapeAttr(block.url)}">${escapeText(block.title)}</a></p></aside>`;
    }
    case 'component': {
      if (typeof window === 'undefined' || !window.NewPressComponentLibrary) throw new Error('组件库尚未加载');
      return window.NewPressComponentLibrary.markup(block.config);
    }
    case 'html': return text;
    default: throw new Error('不支持的内容块');
  }
}
export function serialize(value: unknown): string {
  const state = record(value);
  if (!Array.isArray(state.blocks)) throw new Error('内容块列表无效');
  return state.blocks.map((value: unknown) => {
    const block = record(value);
    if (block.dirty !== undefined && typeof block.dirty !== 'boolean') throw new Error('内容块状态无效');
    if (!block.dirty && typeof block.raw !== 'string') throw new Error('内容块原文无效');
    return textField(block.prefix) + (block.dirty ? renderBlock(block) : block.raw);
  }).join('') + textField(state.tail);
}
export function replacement(value: string, markup: string, selection: EditorSourceSelection): string {
  if (!selection || selection.value !== value) throw new Error('正文已变化，请重新选择插入位置');
  if (!Number.isInteger(selection.start) || !Number.isInteger(selection.end) || selection.start < 0 || selection.end < selection.start || selection.end > value.length) throw new Error('插入位置无效，请重新选择');
  const candidate = value.slice(0, selection.start) + markup + value.slice(selection.end);
  if (bytes(candidate) > MAX_BYTES) throw new Error('插入后正文超过 2 MiB，请缩短内容');
  return candidate;
}
// History stays in this page's memory. No article content goes into browser storage.
export class History {
  past: string[] = [];
  future: string[] = [];
  group = '';
  at = 0;
  record(value: string, group = '', now = Date.now()): void {
    if (!group || group !== this.group || now - this.at > 900) {
      if (this.past.at(-1) !== value) this.past.push(value);
    }
    this.group = group; this.at = now; this.future = []; this.trim();
  }
  trim(): void {
    const size = () => [...this.past, ...this.future].reduce((total, value) => total + value.length * 2, 0);
    while (this.past.length + this.future.length > 20 || (this.past.length + this.future.length > 1 && size() > 8 * 1024 * 1024)) (this.past.length ? this.past : this.future).shift();
  }
  undo(current: string): string | null { if (!this.past.length) return null; const value = this.past.pop()!; this.future.push(current); this.trim(); this.group = ''; return value; }
  redo(current: string): string | null { if (!this.future.length) return null; const value = this.future.pop()!; this.past.push(current); this.trim(); this.group = ''; return value; }
}
