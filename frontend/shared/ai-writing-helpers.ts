import { record } from './ledger-data';
import type { AIUsage } from './ai-usage-data';
import type { WritingPrompt, WritingConnections, WritingPreference } from './ai-writing-data';
type ThinkingState = 'idle'|'waiting'|'streaming'|'received'|'complete'|'unavailable'|'stopped'|'error';
type GenerationState = 'idle'|'running'|'complete'|'stopped'|'error';
type ThinkingSnapshot = {text:string;bytes:number;truncated:boolean;model:string;state:ThinkingState;duration:number};
type StatsSnapshot = {model:string;duration:number;firstOutput:number|null;usage:AIUsage;sampleAt:number|null;state:GenerationState};
// Non-null regex captures below are bounded by successful matches.
export { MAX_OUTPUT, byteLength } from "./ai-writing-data";
export const MAX_THINKING = 256 * 1024;

export const escapeText = (value: unknown) => String(value ?? '').replace(/[&<>"']/g, char => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[char] || char));
export function splitDocument(value: unknown) {
  let text = String(value).replace(/^\uFEFF/, '').trimStart(), wrapped = false;
  for (const fence of ['```markdown\n', '```md\n']) {
    if (text.startsWith(fence)) { text = text.slice(fence.length); wrapped = true; break; }
  }
  const opening = '<newpress-meta>', closing = '</newpress-meta>';
  if (opening.startsWith(text) && text.length < opening.length) return {body: '', meta: null, pending: true};
  let meta: Record<string, unknown> | null = null;
  if (text.startsWith(opening)) {
    const end = text.indexOf(closing);
    if (end < 0) return {body: '', meta: null, pending: true};
    try { const data: unknown = JSON.parse(text.slice(opening.length, end)); if (data && typeof data === 'object' && !Array.isArray(data)) meta = record(data); } catch { /* Keep the body available even if metadata is malformed. */ }
    text = text.slice(end + closing.length).trimStart();
  }
  if (wrapped) text = text.replace(/\n```\s*$/, '');
  return {body: text, meta, pending: false};
}

// The lightweight live preview only creates fixed markup from escaped text.
// It never mounts model-supplied HTML, links or images. Final insertion uses
// the server's full Markdown renderer and content sanitizer.
export function renderMarkdown(markdown: string) {
  const inline = (text: string) => escapeText(text)
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>')
    .replace(/(^|[^*])\*([^*\n]+)\*(?!\*)/g, '$1<em>$2</em>');
  const lines = String(markdown).replace(/\r\n?/g, '\n').split('\n'), output = [];
  let paragraph: string[] = [], list = '', code: string[] | null = null, fence = '';
  const flushParagraph = () => { if (paragraph.length) output.push(`<p>${paragraph.map(inline).join('<br>')}</p>`); paragraph = []; };
  const closeList = () => { if (list) output.push(`</${list}>`); list = ''; };
  for (const line of lines) {
    if (code !== null) {
      if (line.trimStart().startsWith(fence)) { output.push(`<pre><code>${escapeText(code.join('\n'))}</code></pre>`); code = null; }
      else code.push(line);
      continue;
    }
    const startFence = line.match(/^\s*(`{3,}|~{3,})/);
    if (startFence) { flushParagraph(); closeList(); code = []; fence = startFence[1]!; continue; }
    if (!line.trim()) { flushParagraph(); closeList(); continue; }
    const heading = line.match(/^(#{1,6})\s+(.+)$/), item = line.match(/^\s*(?:([-+*])|\d+[.)])\s+(.+)$/);
    if (heading) { flushParagraph(); closeList(); const level = Math.max(2, heading[1]!.length); output.push(`<h${level}>${inline(heading[2]!)}</h${level}>`); }
    else if (item) { flushParagraph(); const kind = item[1] ? 'ul' : 'ol'; if (list !== kind) { closeList(); output.push(`<${kind}>`); list = kind; } output.push(`<li>${inline(item[2]!)}</li>`); }
    else if (/^>\s?/.test(line)) { flushParagraph(); closeList(); output.push(`<blockquote><p>${inline(line.replace(/^>\s?/, ''))}</p></blockquote>`); }
    else if (/^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/.test(line)) { flushParagraph(); closeList(); output.push('<hr>'); }
    else { closeList(); paragraph.push(line); }
  }
  if (code !== null) output.push(`<pre><code>${escapeText(code.join('\n'))}</code></pre>`);
  flushParagraph(); closeList();
  return output.join('');
}

export class SSEDecoder {
  onEvent: (name: string, value: unknown) => void; buffer: string; data: string[]; event: string; first: boolean; size: number;
  constructor(onEvent: (name: string, value: unknown) => void) { this.onEvent = onEvent; this.buffer = ''; this.data = []; this.event = ''; this.first = true; this.size = 0; }
  line(line: string) {
    line = line.replace(/\r$/, '');
    if (this.first) { line = line.replace(/^\uFEFF/, ''); this.first = false; }
    if (!line) { this.dispatch(); return; }
    if (line.startsWith(':')) return;
    const colon = line.indexOf(':'), field = colon < 0 ? line : line.slice(0, colon), value = colon < 0 ? '' : line.slice(colon + 1).replace(/^ /, '');
    if (field === 'event') this.event = value;
    if (field === 'data') { this.size += value.length; if (this.size > 1024 * 1024) throw new Error('生成响应过大，已保留收到的正文'); this.data.push(value); }
  }
  dispatch() {
    if (this.data.length) {
      let payload: unknown;
      try { payload = JSON.parse(this.data.join('\n')); } catch { throw new Error('生成数据格式不正确，已保留收到的正文'); }
      this.onEvent(this.event || 'message', payload);
    }
    this.data = []; this.event = ''; this.size = 0;
  }
  feed(text: string) {
    this.buffer += text;
    let end;
    while ((end = this.buffer.indexOf('\n')) >= 0) { const line = this.buffer.slice(0, end); this.buffer = this.buffer.slice(end + 1); this.line(line); }
    if (this.buffer.length > 1024 * 1024) throw new Error('生成响应过大，已保留收到的正文');
  }
  finish() { if (this.buffer) this.line(this.buffer); this.buffer = ''; this.dispatch(); }
}

export async function readStream(response: Response, onEvent: (name: string, value: unknown) => void) {
  if (!response.body?.getReader) throw new Error('当前浏览器不能读取流式响应，请更新浏览器后重试');
  const reader = response.body.getReader(), decoder = new TextDecoder();
  let terminal = false;
  const events = new SSEDecoder((name, data) => {
    if (terminal) return;
    if (name === 'done' || name === 'error') terminal = true;
    onEvent(name, data);
  });
  try {
    while (!terminal) {
      const chunk = await reader.read();
      if (chunk.done) { events.feed(decoder.decode()); events.finish(); break; }
      events.feed(decoder.decode(chunk.value, {stream: true}));
    }
    if (!terminal) throw new Error('连接在生成完成前中断，已保留收到的正文');
  } finally { await reader.cancel().catch(() => {}); reader.releaseLock(); }
}
// A transient side channel. Never include this state in resultSnapshot(),
// prepare requests, clipboard text, draft storage or article metadata.
export class ThinkingTrace {
  text = ''; bytes = 0; truncated = false; model = ''; state: ThinkingState = 'idle'; activeAt: number | null = null; duration = 0;
  constructor() { this.reset('', 'idle'); }
  reset(model = '', state: ThinkingState = 'waiting') {
    this.text = ''; this.bytes = 0; this.truncated = false; this.model = model;
    this.state = state; this.activeAt = null; this.duration = 0;
  }
  append(value: unknown, now = performance.now()) {
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('思考内容格式不正确，已保留收到的内容');
    const data = record(value);
    if (typeof data?.text !== 'string') throw new Error('思考内容格式不正确，已保留收到的内容');
    if (this.truncated) return;
    const bytes = new TextEncoder().encode(data.text), remaining = MAX_THINKING - this.bytes;
    let end = Math.min(bytes.length, remaining);
    while (end > 0 && end < bytes.length && (bytes[end]! & 0xc0) === 0x80) end--;
    const text = new TextDecoder().decode(bytes.subarray(0, end));
    this.text += text; this.bytes += end;
    this.truncated = data.truncated === true || end < bytes.length;
    if (text || this.truncated) {
      this.state = 'streaming';
      if (this.activeAt === null) this.activeAt = now;
    }
  }
  settle(state: ThinkingState = 'received', now = performance.now()) {
    if (this.activeAt !== null) this.duration += Math.max(0, now - this.activeAt);
    this.activeAt = null;
    if (state === 'received') { if (this.text) this.state = state; }
    else this.state = state === 'complete' && !this.text.trim() ? 'unavailable' : state;
  }
  elapsed(now = performance.now()) { return this.duration + (this.activeAt === null ? 0 : Math.max(0, now - this.activeAt)); }
  snapshot() { return {text: this.text, bytes: this.bytes, truncated: this.truncated, model: this.model, state: this.state, duration: this.elapsed()}; }
  restore(value: ThinkingSnapshot) { this.reset(); Object.assign(this, value); this.activeAt = null; }
}
// Counts are provider snapshots, never character-based estimates. Keep their
// sampling time so a stalled/interrupted stream cannot skew the displayed TPS.
export class GenerationStats {
  model = ''; startedAt: number | null = null; duration = 0; firstOutput: number | null = null; usage: AIUsage = {}; sampleAt: number | null = null; state: GenerationState = 'idle';
  constructor() { this.reset(); }
  reset(model = '', now: number | null = null) {
    this.model = model; this.startedAt = now; this.duration = 0; this.firstOutput = null;
    this.usage = {}; this.sampleAt = null; this.state = now === null ? 'idle' : 'running';
  }
  elapsed(now = performance.now()) { return this.startedAt === null ? this.duration : Math.max(0, now - this.startedAt); }
  output(now = performance.now()) { if (this.state === 'running' && this.firstOutput === null) this.firstOutput = this.elapsed(now); }
  receive(value: unknown, now = performance.now()) {
    if (this.state !== 'running' || !value || typeof value !== 'object' || Array.isArray(value)) return;
    const data = record(value), usage: AIUsage = {};
    for (const field of ['prompt_tokens', 'completion_tokens', 'total_tokens', 'reasoning_tokens', 'cached_tokens', 'cache_creation_tokens', 'tool_prompt_tokens'] as const) {
      if (typeof data[field] === 'number' && Number.isSafeInteger(data[field]) && data[field] >= 0) usage[field] = data[field];
    }
    if (!Object.keys(usage).length) return;
    if (usage.total_tokens === undefined && usage.prompt_tokens !== undefined && usage.completion_tokens !== undefined) {
      const total = usage.prompt_tokens + usage.completion_tokens + (usage.tool_prompt_tokens || 0);
      if (Number.isSafeInteger(total)) { usage.total_tokens = total; usage.total_source = 'calculated'; }
    } else if (usage.total_tokens !== undefined) usage.total_source = data.total_source === 'calculated' ? 'calculated' : 'reported';
    if (JSON.stringify(this.usage) !== JSON.stringify(usage)) { this.usage = usage; this.sampleAt = this.elapsed(now); }
  }
  settle(state: GenerationState = 'complete', now = performance.now()) { this.duration = this.elapsed(now); this.startedAt = null; this.state = state; }
  tps() { return this.firstOutput !== null && this.sampleAt !== null && this.sampleAt >= this.firstOutput && this.sampleAt >= 250 && this.usage.completion_tokens !== undefined ? this.usage.completion_tokens * 1000 / this.sampleAt : null; }
  snapshot() { return {model: this.model, duration: this.elapsed(), firstOutput: this.firstOutput, usage: {...this.usage}, sampleAt: this.sampleAt, state: this.state}; }
  restore(value: StatsSnapshot | null) { this.reset(); if (value) { Object.assign(this, value); this.usage = {...value.usage}; } this.startedAt = null; }
}
export function chooseConnection(data: WritingConnections, preference: WritingPreference = {}) {
  const providers = Array.isArray(data.providers) ? data.providers.filter(item => ['', 'openai_responses', 'anthropic', 'gemini', 'azure_openai', 'azure_responses', 'azure_openai_legacy'].includes(item.protocol || '')) : [];
  const selected = providers.find(item => item.id === preference.provider_id) || providers.find(item => item.id === data.default_provider) || providers[0];
  if (!selected) return {provider_id: '', model: ''};
  const preferredModel = preference.models?.[selected.id];
  return {provider_id: selected.id, model: preferredModel !== undefined && selected.models.includes(preferredModel) ? preferredModel : selected.default_model || selected.models[0] || ''};
}
const NEWS_BRIEF_HELP = '请补充具体事件和来源原文；只写“今日新闻”等主题还不能成稿。当前写作不自动检索新闻，有原稿可勾选“参考当前草稿”。';
export function briefProblem(prompt: Pick<WritingPrompt, 'id'|'instruction'|'example'|'base_prompt'> | null | undefined, value: string, customInstruction = '', draft = '') {
  if (prompt?.base_prompt) prompt = {...prompt, id: prompt.base_prompt};
  if (!prompt || !['article', 'news', 'essay'].includes(prompt.id) || draft.replace(/<[^<>]*>|&nbsp;|&#160;|&#x[aA]0;/g, '').trim()) return '';
  if (customInstruction.trim() && customInstruction.trim() !== prompt.instruction) return '';
  // Same conservative rule as internal/aieditor/brief.go; shared fixtures
  // cover both implementations. This does not validate or fetch news facts.
  const normalized = value.replace(/[\s\u00a0\u3000，,。.!！?？:：;；、/／_—“”"'‘’（）()\-]+/g, '');
  const generic = /^(请|帮我|给我|写|生成|整理|一篇|一则|一份|篇|则|份|关于)*(今日|今天|最新|最近|近期|本周|当日|每日|热点|时事)?的?(科技|财经|国际|国内|社会)?(新闻|消息)(汇总|摘要|简报|报道|文章|吧)*$/;
  return generic.test(normalized) || (prompt.id === 'news' && value.trim() === prompt.example) ? NEWS_BRIEF_HELP : '';
}
