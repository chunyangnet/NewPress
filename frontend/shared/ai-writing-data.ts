import { record, string, boolean, number, list, failure } from './ledger-data';
import { usage } from './ai-usage-data';
import { protocolIDs, imageProtocols } from './ai-settings-data';

export const MAX_OUTPUT = 512 * 1024;
export const byteLength = (value: string) => new TextEncoder().encode(value).length;
export interface WritingProvider { id: string; name: string; protocol: string; models: string[]; default_model: string }
export interface WritingConnections { providers: WritingProvider[]; default_provider?: string; default_image_provider?: string }
export interface WritingPreference { provider_id?: string; models?: Record<string, string>; stream_usage?: Record<string, boolean> }
export interface WritingPrompt { id: string; category: string; title: string; description: string; instruction: string; example: string; needs_draft?: boolean; template_id?: string; template_version?: number; base_prompt?: string }
export interface WritingDocument { title: string; slug: string; excerpt: string; tags: string[]; category_ids: number[]; cover_prompt: string; markdown: string; content_source: string; warnings: string[] }
export type WritingInput = Omit<WritingDocument, 'content_source' | 'warnings'>;
export interface WritingConnection { provider_id?: string; base_url?: string; api_key?: string; model: string; stream_usage?: boolean }
export interface AIMedia { id: number; kind: 'image'; status: 'active'; public_url: string; width: number; height: number; title: string; filename: string; alt_text: string; caption: string }
export type WritingError = Error & { code?: string; status?: number; definite?: boolean; beforeSend?: boolean };
export function writingFailure(value: unknown): WritingError {
  const error: WritingError = failure(value);
  if (value && typeof value === 'object') {
    if ('definite' in value && value.definite === true) error.definite = true;
    if ('beforeSend' in value && value.beforeSend === true) error.beforeSend = true;
  }
  return error;
}
export async function writingJSON(response: Response): Promise<unknown> {
  let data: unknown;
  try { data = await response.json(); } catch { throw new Error('服务器响应无法确认，请重试或核对原操作。'); }
  if (!response.ok) {
    const row = data && typeof data === 'object' && !Array.isArray(data) ? record(data) : {};
    const detail = row.error && typeof row.error === 'object' && !Array.isArray(row.error) ? record(row.error) : {};
    throw Object.assign(writingFailure(detail), {status: response.status, definite: response.status >= 400 && response.status < 500 && response.status !== 408});
  }
  return data;
}
export function writingSession(value: unknown, owner: string, login: string, csrf: string, writable = true): string {
  const row = record(value);
  if (row.authenticated !== true || String(number(row.user_id, 1)) !== owner || row.username !== login || typeof row.csrf_token !== 'string' || !csrf || row.csrf_token !== csrf || writable && row.read_only !== false) throw new Error('登录身份已变化，请刷新页面并重新登录。');
  return row.csrf_token;
}
export function writingConnections(value: unknown): WritingConnections {
  const row = record(value);
  const providers = list(row.providers, value => {
    const p = record(value), protocol = p.protocol === undefined ? '' : string(p.protocol);
    const models = list(p.models, string);
    if (!protocolIDs.some(id => id === protocol) || !models.length || models.some(model => !model) || new Set(models).size !== models.length || p.api_key !== undefined && p.api_key !== '') throw new Error('保存的接口列表无效');
    const provider = {id: string(p.id), name: string(p.name), protocol, models, default_model: string(p.default_model)};
    if (!provider.id || provider.default_model && !models.includes(provider.default_model)) throw new Error('保存的默认模型无效');
    return provider;
  });
  if (new Set(providers.map(p => p.id)).size !== providers.length) throw new Error('保存的接口列表重复');
  const result = {providers, default_provider: string(row.default_provider), default_image_provider: row.default_image_provider === undefined ? '' : string(row.default_image_provider)};
  for (const [id, image] of [[result.default_provider, false], [result.default_image_provider, true]] as const) {
    if (id && !providers.some(p => p.id === id && imageProtocols.includes(p.protocol) === image)) throw new Error('默认接口与可用接口不匹配');
  }
  return result;
}
export function writingPreference(value: unknown): WritingPreference {
  try {
    const row = record(value), result: WritingPreference = {};
    if (typeof row.provider_id === 'string') result.provider_id = row.provider_id;
    for (const key of ['models', 'stream_usage'] as const) {
      if (!row[key] || typeof row[key] !== 'object' || Array.isArray(row[key])) continue;
      const pairs = Object.entries(record(row[key])).filter(([name, value]) => name !== '__proto__' && name !== 'constructor' && typeof value === (key === 'models' ? 'string' : 'boolean'));
      if (key === 'models') result.models = Object.fromEntries(pairs) as Record<string, string>;
      else result.stream_usage = Object.fromEntries(pairs) as Record<string, boolean>;
    }
    return result;
  } catch { return {}; }
}
export function writingPrompts(value: unknown): WritingPrompt[] {
  const prompts = list(record(value).prompts, value => {
    const row = record(value);
    const p: WritingPrompt = {id: string(row.id), category: string(row.category), title: string(row.title), description: string(row.description), instruction: string(row.instruction), example: string(row.example)};
    if (row.needs_draft !== undefined) p.needs_draft = boolean(row.needs_draft);
    if (!p.id || !p.category || !p.title || !p.instruction) throw new Error('写作模板暂不可用');
    if (p.id.startsWith('team:')) {
      p.template_id = string(row.template_id); p.template_version = number(row.template_version, 1); p.base_prompt = string(row.base_prompt);
      if (!/^[0-9a-f]{32}$/.test(p.template_id) || p.id !== `team:${p.template_id}` || !p.base_prompt || p.base_prompt.startsWith('team:')) throw new Error('团队模板版本未确认');
    } else if (row.template_version !== undefined || row.template_id !== undefined || row.base_prompt !== undefined) throw new Error('内置模板不接受团队版本');
    return p;
  });
  if (!prompts.length || new Set(prompts.map(p => p.id)).size !== prompts.length) throw new Error('写作模板列表为空或重复');
  return prompts;
}
export function writingDocument(value: unknown): WritingDocument {
  const row = record(value), doc = {title: string(row.title), slug: string(row.slug), excerpt: string(row.excerpt), tags: list(row.tags, string), category_ids: list(row.category_ids, value => number(value, 1)), cover_prompt: string(row.cover_prompt), markdown: string(row.markdown), content_source: string(row.content_source), warnings: row.warnings === null ? [] : list(row.warnings, string)};
  if (!doc.markdown.trim() || !doc.content_source.trim() || byteLength(doc.markdown) > MAX_OUTPUT || byteLength(doc.content_source) > MAX_OUTPUT || doc.tags.length > 20 || doc.category_ids.length > 8 || new Set(doc.category_ids).size !== doc.category_ids.length) throw new Error('生成结果不完整，已保留收到的正文');
  return doc;
}
export function writingDone(value: unknown) {
  const row = record(value); return {document: writingDocument(row.document), complete: boolean(row.complete), usage: usage(row.usage)};
}
export function writingModels(value: unknown) {
  const row = record(value), models = list(row.models, value => {
    const row = record(value), id = string(row.id);
    if (!id) throw new Error('模型标识为空');
    return {id, name: row.name === undefined ? '' : string(row.name), owned_by: row.owned_by === undefined ? '' : string(row.owned_by)};
  });
  if (!models.length || new Set(models.map(model => model.id)).size !== models.length) throw new Error('接口未返回可用模型');
  return {models, base_url: string(row.base_url)};
}
export function mediaURL(value: unknown): string {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return '';
  const row = record(value);
  if (typeof row.public_url !== 'string') return '';
  try {
    const name = decodeURIComponent(row.public_url.split('/').at(-1) || '');
    if (/[\\/]/.test(name) || new URL(row.public_url, 'https://media.invalid').pathname !== row.public_url) return '';
  } catch { return ''; }
  return row.kind === 'image' && row.status === 'active' && typeof row.id === 'number' && Number.isSafeInteger(row.id) && row.id > 0 && typeof row.public_url === 'string' && new RegExp(`^/media/${row.id}/[^\\\\?#\\u0000-\\u0020]+$`).test(row.public_url) && typeof row.width === 'number' && Number.isSafeInteger(row.width) && row.width > 0 && typeof row.height === 'number' && Number.isSafeInteger(row.height) && row.height > 0 ? row.public_url : '';
}
export function aiMedia(value: unknown, expectedID?: number): AIMedia {
  const row = record(value), url = mediaURL(row), id = number(row.id, 1);
  if (!url || expectedID !== undefined && id !== expectedID) throw new Error('图片已移除或返回了另一份媒体，请重新选择。');
  return {id, public_url: url, kind: 'image', status: 'active', width: number(row.width, 1), height: number(row.height, 1), title: string(row.title), filename: string(row.filename), alt_text: string(row.alt_text), caption: string(row.caption)};
}
export function imageDone(value: unknown, model: string, received: AIMedia[]): AIMedia[] {
  const row = record(value), items = list(row.items, item => aiMedia(item));
  if (row.model !== model || !items.length || new Set(items.map(item => item.id)).size !== items.length || received.some(image => !items.some(item => JSON.stringify(item) === JSON.stringify(image)))) throw new Error('图片完成结果未确认，请到媒体库核对已保存图片。');
  return items;
}
export function metadataResult(value: unknown) {
  const row = record(value); return {excerpt: string(row.Excerpt), slug: string(row.Slug), tags: row.Tags === null ? [] : list(row.Tags, string), categoryID: number(row.CategoryID)};
}
export function paragraphResult(value: unknown): string {
  const text = string(record(value).text); if (!text.trim() || byteLength(text) > MAX_OUTPUT) throw new Error('段落建议为空或过长'); return text;
}
export function suggestedTopics(value: unknown): string[] {
  const topics = list(record(value).topics, string);
  if (!topics.length || topics.length > 10 || topics.some(title => !title.trim() || title !== title.trim() || /[\r\n]/.test(title)) || new Set(topics).size !== topics.length) throw new Error('模型没有返回有效选题');
  return topics;
}
