import { required } from './ai-writing-dom';
import type {} from './composer-types';
import type {} from './ai-types';
import type {} from '../shared/account-types';
import { record } from '../shared/ledger-data';
import type { AIUsage } from '../shared/ai-usage-data';
import { imageProtocols } from '../shared/ai-settings-data';
import { writingJSON, writingFailure, writingSession, writingConnections, writingPreference, writingPrompts, writingDocument, writingDone, writingModels } from '../shared/ai-writing-data';
import type { WritingConnections, WritingPreference, WritingPrompt, WritingInput, WritingDocument, WritingConnection } from '../shared/ai-writing-data';
import { splitDocument, renderMarkdown, SSEDecoder, readStream, ThinkingTrace, GenerationStats, byteLength, MAX_OUTPUT, MAX_THINKING, chooseConnection, briefProblem } from '../shared/ai-writing-helpers';
type ArticleSnapshot={title:string;slug:string;excerpt:string;content_source:string;tags:string;type:string;category_ids:string[]};
type Fields={'editor':HTMLDialogElement;'image-open':HTMLButtonElement;'image-suggestion':HTMLButtonElement;'settings-toggle':HTMLButtonElement;'close':HTMLButtonElement;'connection':HTMLDetailsElement;'connection-badge':HTMLElement;'provider':HTMLSelectElement;'saved-model-field':HTMLElement;'saved-model':HTMLSelectElement;'connections-refresh':HTMLButtonElement;'temporary':HTMLElement;'base':HTMLInputElement;'key':HTMLInputElement;'key-toggle':HTMLButtonElement;'detect':HTMLButtonElement;'model':HTMLInputElement;'models':HTMLElement;'saved-help':HTMLElement;'connection-status':HTMLElement;'template-count':HTMLElement;'categories':HTMLElement;'templates':HTMLElement;'prompts-loading':HTMLElement;'prompts-retry':HTMLButtonElement;'prompts-refresh':HTMLButtonElement;'instruction':HTMLTextAreaElement;'reset-prompt':HTMLButtonElement;'writing-style':HTMLSelectElement;'style-reference':HTMLTextAreaElement;'avoid-phrases':HTMLTextAreaElement;'language':HTMLSelectElement;'max-tokens':HTMLInputElement;'stream-usage-option':HTMLElement;'stream-usage':HTMLInputElement;'stage-state':HTMLElement;'view':HTMLButtonElement;'thinking-toggle':HTMLButtonElement;'output':HTMLElement;'empty':HTMLElement;'starters':HTMLElement;'starter':HTMLButtonElement;'result':HTMLElement;'document-title':HTMLElement;'document-excerpt':HTMLElement;'preview':HTMLElement;'markdown-pane':HTMLElement;'markdown':HTMLTextAreaElement;'metadata':HTMLDetailsElement;'meta-title':HTMLInputElement;'meta-slug':HTMLInputElement;'meta-excerpt':HTMLTextAreaElement;'meta-tags':HTMLInputElement;'meta-categories':HTMLElement;'meta-cover':HTMLTextAreaElement;'word-count':HTMLElement;'elapsed':HTMLElement;'usage':HTMLDetailsElement;'usage-summary':HTMLElement;'tps':HTMLElement;'tokens':HTMLElement;'usage-panel':HTMLElement;'usage-model':HTMLElement;'usage-state':HTMLElement;'usage-input':HTMLElement;'usage-output':HTMLElement;'usage-reasoning':HTMLElement;'usage-cached':HTMLElement;'usage-cache-created':HTMLElement;'usage-total':HTMLElement;'usage-tool-row':HTMLElement;'usage-tool':HTMLElement;'usage-first':HTMLElement;'usage-sample':HTMLElement;'usage-note':HTMLElement;'humanize':HTMLButtonElement;'previous':HTMLButtonElement;'copy':HTMLButtonElement;'follow':HTMLButtonElement;'selected-template':HTMLElement;'brief':HTMLTextAreaElement;'brief-help':HTMLElement;'tone':HTMLSelectElement;'length':HTMLSelectElement;'generate':HTMLButtonElement;'generate-label':HTMLElement;'stop':HTMLButtonElement;'use-draft':HTMLInputElement;'thinking':HTMLElement;'thinking-model':HTMLElement;'thinking-close':HTMLButtonElement;'thinking-state':HTMLElement;'thinking-elapsed':HTMLElement;'thinking-output':HTMLElement;'thinking-empty':HTMLElement;'thinking-empty-title':HTMLElement;'thinking-empty-note':HTMLElement;'thinking-text':HTMLElement;'thinking-limit':HTMLElement;'thinking-count':HTMLElement;'thinking-stop':HTMLButtonElement;'thinking-follow':HTMLButtonElement;'status':HTMLElement;'login':HTMLElement;'keep-slug-label':HTMLElement;'keep-slug':HTMLInputElement;'undo-apply':HTMLButtonElement;'apply':HTMLButtonElement};
const core = {splitDocument,renderMarkdown,SSEDecoder,readStream,ThinkingTrace,GenerationStats,byteLength,MAX_OUTPUT,MAX_THINKING,chooseConnection,briefProblem};
(() => {
 if(typeof document==='undefined')return; window.ChunnelAICore=core;
  const formNode = document.querySelector<HTMLFormElement>('[data-content-editor]'), dialogNode = document.querySelector<HTMLDialogElement>('[data-ai-editor]');
  if (!formNode || !dialogNode) return;
  const form = formNode, dialog = dialogNode;
  const field = (name: string) => required(form.querySelector<HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement>(`[name="${name}"]`));
  const sourceNode = form.querySelector<HTMLTextAreaElement>('[data-content-source]');
  const launchers = [...(form.querySelectorAll<HTMLButtonElement>('[data-ai-open]') || [])];
  const firstLauncher = launchers[0], blockEditorNode = window.ChunnelBlockEditor;
  if (!sourceNode || !firstLauncher || !blockEditorNode) return;
  const source = sourceNode, blockEditor = blockEditorNode; let launcher = firstLauncher;
  const $ = <K extends keyof Fields>(key: K): Fields[K] => required(dialog.querySelector<Fields[K]>(`[data-ai-${key}]`));
  const $$ = (key: string) => [...dialog.querySelectorAll<HTMLButtonElement>(`[data-ai-${key}]`)];
  const base = $('base'), key = $('key'), model = $('model'), brief = $('brief'), instruction = $('instruction'), md = $('markdown');
  const provider = $('provider'), savedModel = $('saved-model');
  const preferenceKey = `newpress:ai-choice:${location.host}:${form.dataset.editorUserId}:${form.dataset.editorLogin}`;
  let preference: WritingPreference = {}, connections: WritingConnections = {providers: []}, connectionsController: AbortController | null = null;
  try { preference = writingPreference(JSON.parse(localStorage.getItem(preferenceKey) || '{}') as unknown); } catch { /* Storage is optional. */ }
  const output = $('output'), preview = $('preview'), status = $('status');
  const fields = {title:$('meta-title'),slug:$('meta-slug'),excerpt:$('meta-excerpt'),tags:$('meta-tags'),cover:$('meta-cover')};
  let prompts: WritingPrompt[] = [], selected = '', selectedTemplate: WritingPrompt | null = null, category = '', promptEdits = new Map<string,string>(), raw = '', rawBytes = 0, parsedMeta = '';
  let generating = false, probing = false, preparing = false, follow = true, view = 'preview', loaded = false;
  let generationController: AbortController | null = null, probeController: AbortController | null = null, prepareController: AbortController | null = null, loadController: AbortController | null = null;
  let connectionRevision = 0, renderTimer = 0, elapsedTimer = 0, previous: {document: WritingInput;thinking: ReturnType<ThinkingTrace["snapshot"]>;stats:ReturnType<GenerationStats["snapshot"]>} | null = null;
  let categoryIDs: number[] = [], preparedHTML: string | null = null, unapplied = false, lastApplication: {before: ArticleSnapshot;after:ArticleSnapshot} | null = null, openingSnapshot: ArticleSnapshot | null = null;
  const thinking = new ThinkingTrace(), thinkingPanel = $('thinking'), thinkingOutput = $('thinking-output');
  const stats = new GenerationStats();
  let thinkingTimer = 0, thinkingFollow = true, thinkingPainted = '', thinkingOpen = !matchMedia('(max-width: 1179px)').matches;
  const owner=form.dataset.editorUserId||'',login=form.dataset.editorLogin||'',csrf=field('csrf_token').value;let retired=false;
  const alive=()=>!retired&&form.isConnected&&dialog.isConnected;
  function retire(){if(retired)return;retired=true;generationController?.abort();probeController?.abort();prepareController?.abort();loadController?.abort();connectionsController?.abort();key.value='';key.type='password';raw='';md.value='';brief.value='';instruction.value='';promptEdits.clear();prompts=[];selectedTemplate=null;previous=null;lastApplication=null;openingSnapshot=null;unapplied=false;thinking.reset();stats.reset();preparedHTML=null;for(const input of Object.values(fields))input.value='';preview.replaceChildren();$('thinking-text').replaceChildren();$('login').hidden=false;dialog.close();clearTimeout(renderTimer);clearTimeout(thinkingTimer);clearInterval(elapsedTimer);controls();}
  document.addEventListener('chunnel:session',event=>{const current=event.detail;if(current?.authenticated!==true||String(current.id)!==owner||current.csrf_token!==csrf)retire();});
  document.addEventListener('chunnel:admin-before-replace',event=>{if(event.detail.regions.some(region=>region.contains(form)))retire();});
  window.addEventListener('pagehide',retire);window.addEventListener('pageshow',event=>{if(event.persisted)retire();});
  const readOnly = () => !alive() || source.disabled || source.readOnly || form.getAttribute('aria-busy') === 'true';
  const node = <K extends keyof HTMLElementTagNameMap>(tag: K, text = '', className = '') => { const el = document.createElement(tag); el.textContent = text; el.className = className; return el; };
  const selectedPrompt = () => selectedTemplate;
  const promptKey = (prompt: WritingPrompt | null) => prompt ? `${prompt.id}:${prompt.template_version || 0}` : '';
  const say = (message: string, tone = '') => { status.textContent = message; status.dataset.tone = tone; };
  function showBriefHelp(problem = '') {
    brief.setAttribute('aria-invalid', String(!!problem));
    const help = $('brief-help');
    if (help) help.textContent = problem || (selected === 'news' ? '请粘贴事件和来源原文；链接不会自动读取，材料少可以写短。' : '写清一个具体问题。新闻需附事件和来源原文，材料少可以写短。');
  }
  const connectionSay = (message: string, tone = '') => { $('connection-status').textContent = message; $('connection-status').dataset.tone = tone; };
  const snapshot = () => ({
    title: field('title').value, slug: field('slug').value, excerpt: field('excerpt').value,
    content_source: source.value, tags: field('tags')?.value || '', type: field('type').value,
    category_ids: [...form.querySelectorAll<HTMLInputElement>('[name="category_ids"]:checked')].map(input => input.value),
  });
  const sameSnapshot = (a: ArticleSnapshot, b: ArticleSnapshot) => JSON.stringify(a) === JSON.stringify(b);
  const resultSnapshot = (): WritingInput => ({
    title: fields.title.value, slug: fields.slug.value, excerpt: fields.excerpt.value,
    tags: fields.tags.value.split(/[,，]/).map(tag => tag.trim()).filter(Boolean),
    category_ids: [...categoryIDs], cover_prompt: fields.cover.value, markdown: md.value,
  });
  function settingsOpen(open: boolean) {
    dialog.classList.toggle('is-settings-open', open);
    $('settings-toggle').setAttribute('aria-expanded', String(open));
  }
  function setThinkingOpen(open: boolean, returnFocus = false) {
    thinkingOpen = open;
    dialog.classList.toggle('is-thinking-collapsed', !open);
    thinkingPanel.hidden = !open;
    $('thinking-toggle').setAttribute('aria-expanded', String(open));
    if (open) drawThinking();
    if (returnFocus) $('thinking-toggle').focus({preventScroll: true});
  }
  function thinkingMetrics() {
    $('thinking-elapsed').textContent = thinking.text ? `${(thinking.elapsed() / 1000).toFixed(1)} 秒` : '等待输出';
  }
  function drawThinking() {
    clearTimeout(thinkingTimer); thinkingTimer = 0;
    const hasText = !!thinking.text.trim(), state = thinking.state;
    const labels = {idle: '等待开始', waiting: '等待返回', streaming: '思考中', received: '已接收', complete: '思考完成', unavailable: '未返回思考', stopped: '已停止', error: '已中断'};
    thinkingPanel.dataset.state = state;
    const label = labels[state] || labels.idle;
    if ($('thinking-state').textContent !== label) $('thinking-state').textContent = label;
    $('thinking-toggle').dataset.active = String(state === 'streaming' || state === 'waiting');
    $('thinking-toggle').setAttribute('aria-label', `模型思考：${label}`);
    $('thinking-model').textContent = thinking.model || '随生成实时显示';
    $('thinking-model').title = thinking.model;
    $('thinking-empty').hidden = hasText; $('thinking-text').hidden = !hasText;
    const empty = state === 'unavailable' ? ['此模型未返回可展示的思考内容', '正文已正常生成。可切换支持思考输出的模型，或检查接口的思考设置。']
      : state === 'stopped' || state === 'error' ? ['尚未收到思考内容', '本次生成已结束，可重新生成后查看。']
      : state === 'waiting' || state === 'streaming' ? ['正在等待模型', '收到思考内容后会在这里逐步展开。部分模型只返回正文。']
      : ['留一处空间，看看思路', '开始生成后，在这里阅读模型返回的思考内容。'];
    $('thinking-empty-title').textContent = empty[0] || ""; $('thinking-empty-note').textContent = empty[1] || "";
    $('thinking-limit').hidden = !thinking.truncated;
    $('thinking-count').textContent = `${[...thinking.text.replace(/\s/g, '')].length.toLocaleString()} 字`;
    $('thinking-follow').disabled = !hasText;
    $('thinking-follow').setAttribute('aria-pressed', String(thinkingFollow));
    $('thinking-follow').textContent = thinkingFollow ? '跟随思考' : '继续跟随';
    if (thinkingPainted !== thinking.text) {
      $('thinking-text').innerHTML = renderMarkdown(thinking.text);
      thinkingPainted = thinking.text;
    }
    thinkingMetrics();
    if (thinkingFollow && thinkingOpen) thinkingOutput.scrollTop = thinkingOutput.scrollHeight;
  }
  function scheduleThinking() {
    if (thinkingTimer || document.hidden) return;
    thinkingTimer = setTimeout(drawThinking, thinking.text.length > 60000 ? 260 : 100);
  }
  function showConnection() { $('connection').open = true; if (matchMedia('(max-width: 760px)').matches) settingsOpen(true); }
  function setMetadata(value: unknown = {}) {
    const data = record(value);
    const text = (value: unknown) => typeof value === 'string' ? value : '';
    fields.title.value = text(data.title); fields.slug.value = text(data.slug); fields.excerpt.value = text(data.excerpt); fields.cover.value = text(data.cover_prompt);
    fields.tags.value = Array.isArray(data.tags) ? data.tags.filter(tag => typeof tag === 'string').slice(0, 20).join(', ') : '';
    categoryIDs = Array.isArray(data.category_ids) ? data.category_ids.filter(id => Number.isSafeInteger(id) && id > 0).slice(0, 8) : [];
    const categories = [...form.querySelectorAll<HTMLInputElement>('[name="category_ids"]')].filter(input => categoryIDs.includes(Number(input.value))).map(input => input.closest('label')?.querySelector('b')?.textContent || '');
    $('meta-categories').textContent = categories.filter(Boolean).join(' · ') || '沿用当前分类';
    updateHeading();
  }
  function updateHeading() {
    $('document-title').textContent = fields.title.value || (generating ? '正在构思标题…' : '未命名文章');
    $('document-excerpt').textContent = fields.excerpt.value;
  }
  function updateMetrics() {
    const count = [...md.value.replace(/[#*`>_~|\s]/g, '')].length;
    $('word-count').textContent = `${count.toLocaleString()} 字`;
    $('elapsed').textContent = stats.state === 'idle' ? '' : `${(stats.elapsed() / 1000).toFixed(1)} 秒`;
    drawUsage();
    thinkingMetrics();
  }
  function drawUsage() {
    const usage = stats.usage, rate = stats.tps(), hasUsage = Object.keys(usage).length > 0;
    const format = (value: number | null | undefined) => value === undefined || value === null ? '—' : value.toLocaleString('zh-CN');
    const compact = (value: number | undefined) => value === undefined ? '—' : value >= 1e12 ? `${(value / 1e12).toFixed(1)}T` : value >= 1e9 ? `${(value / 1e9).toFixed(1)}B` : value >= 1e6 ? `${(value / 1e6).toFixed(1)}M` : value >= 1e4 ? `${(value / 1e3).toFixed(1)}K` : format(value);
    $('tps').textContent = `${rate === null ? '—' : rate >= 1e4 ? compact(rate) : rate.toLocaleString('zh-CN', {maximumFractionDigits: 1})} TPS`;
    $('tokens').textContent = `${compact(usage.total_tokens)} Tokens`;
    $('usage-summary').setAttribute('aria-label', `生成统计：平均每秒 ${rate === null ? '未知' : rate.toFixed(1)} Token，总计 ${format(usage.total_tokens)} Token`);
    const label = stats.state === 'idle' ? '生成统计' : !hasUsage ? stats.state === 'running' ? '等待用量' : '未返回用量' : stats.state === 'running' || stats.state === 'stopped' || stats.state === 'error' ? '部分用量' : '接口用量';
    $('usage-state').textContent = label;
    $('usage-model').textContent = stats.model || '当前生成';
    for (const [name, field] of Object.entries({input: 'prompt_tokens', output: 'completion_tokens', total: 'total_tokens', reasoning: 'reasoning_tokens', cached: 'cached_tokens', 'cache-created': 'cache_creation_tokens', tool: 'tool_prompt_tokens'})) required(dialog.querySelector<HTMLElement>('[data-ai-usage-' + name + ']')).textContent = format(usage[field as Exclude<keyof AIUsage, "total_source">]);
    $('usage-tool-row').hidden = usage.tool_prompt_tokens === undefined;
    $('usage-first').textContent = stats.firstOutput === null ? '—' : `${(stats.firstOutput / 1000).toFixed(2)} 秒`;
    $('usage-sample').textContent = stats.sampleAt === null ? '尚未收到' : `第 ${(stats.sampleAt / 1000).toFixed(1)} 秒`;
    $('usage-note').textContent = stats.state === 'idle' ? '开始生成后显示接口实际返回的用量。'
      : !hasUsage ? stats.state === 'running' ? '部分接口在结束时才返回用量。' : '接口未返回用量，或生成在统计到达前已停止。'
      : stats.state === 'stopped' || stats.state === 'error' ? '生成已中断，仅保留最后收到的用量，不代表最终账单。'
      : stats.state === 'running' ? '随接口上报更新，当前为截至最近一次统计的用量。'
      : usage.total_source === 'calculated' ? '总计由接口返回的输入、输出及工具 Token 相加。' : '总计采用接口报告值；推理和缓存明细不再重复相加。';
  }
  function drawPreview() {
    preview.innerHTML = preparedHTML === null ? renderMarkdown(md.value) : preparedHTML;
    if (generating && follow) output.scrollTop = output.scrollHeight;
    updateMetrics();
  }
  function controls() {
    if(!alive()){dialog.querySelectorAll<HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement|HTMLButtonElement>('input,select,textarea,button').forEach(input=>{input.disabled=true;});return;}
    const busy = generating || preparing, hasText = !!md.value.trim();
    dialog.classList.toggle('is-generating', generating);
    $('generate').hidden = generating; $('stop').hidden = !generating;
    $('thinking-stop').hidden = !generating;
    $('generate').disabled = preparing || probing || !loaded || !!connectionsController;
    $('humanize').disabled = busy || probing || !loaded || !!connectionsController || !hasText || readOnly();
    $('connections-refresh').disabled = busy || !!connectionsController;
    $('generate-label').textContent = hasText ? '重新生成' : '生成全文';
    $('detect').disabled = probing || busy;
    $('detect').setAttribute('aria-busy', String(probing));
    $('apply').disabled = busy || !hasText || readOnly();
    $('apply').setAttribute('aria-busy', String(preparing));
    $('copy').disabled = generating || !hasText;
    $('previous').disabled = busy || !previous;
    $('undo-apply').disabled = busy || !lastApplication || readOnly();
    $('prompts-retry').disabled = busy;
    if ($('prompts-refresh')) $('prompts-refresh').disabled = busy || !!loadController;
    dialog.querySelectorAll<HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement>('.ai-sidebar input, .ai-sidebar select, .ai-sidebar textarea, .ai-prompt-box input, .ai-prompt-box select, .ai-prompt-box textarea, .ai-metadata input, .ai-metadata textarea').forEach(input => { input.disabled = busy; });
    dialog.querySelectorAll<HTMLButtonElement>('.ai-template, .ai-categories button, [data-ai-starter], [data-ai-reset-prompt]').forEach(button => { button.disabled = busy; });
    $('key-toggle').disabled = busy;
    md.readOnly = busy;
    $('metadata').hidden = generating || !hasText;
  }
  function paint() {
    clearTimeout(renderTimer); renderTimer = 0;
    const part = splitDocument(raw);
    md.value = part.body;
    if (part.meta && JSON.stringify(part.meta) !== parsedMeta) { setMetadata(part.meta); parsedMeta = JSON.stringify(part.meta); }
    $('empty').hidden = raw.length > 0;
    $('result').hidden = raw.length === 0;
    preparedHTML = null;
    drawPreview();
  }
  function schedulePaint() {
    if (renderTimer || document.hidden) return;
    renderTimer = setTimeout(paint, raw.length > 60000 ? 260 : 100);
  }
  function showResult(data: WritingInput & Partial<Pick<WritingDocument,"content_source">>) {
    setMetadata(data); md.value = data.markdown || ''; preparedHTML = typeof data.content_source === 'string' ? data.content_source : null;
    $('empty').hidden = !!md.value; $('result').hidden = !md.value; $('metadata').hidden = !md.value;
    drawPreview(); controls();
  }
  function updateTemplates() {
    const categories = [...new Set(prompts.map(prompt => prompt.category))];
    $('categories').replaceChildren(...categories.map(name => {
      const button = node('button', name); button.type = 'button'; button.setAttribute('aria-pressed', String(category === name));
      button.addEventListener('click', () => { category = name; updateTemplates(); }); return button;
    }));
    $('templates').replaceChildren(...prompts.filter(prompt => prompt.category === category).map((prompt, index) => {
      const button = node('button', '', 'ai-template'), description = node('span'); button.type = 'button'; button.dataset.aiTemplate = prompt.id;
      button.setAttribute('aria-pressed', String(promptKey(selectedTemplate) === promptKey(prompt)));
      description.append(node('strong', `${prompt.title}${prompt.template_version ? ` · v${prompt.template_version}` : ''}`), node('small', prompt.description)); button.append(node('span', String(index + 1).padStart(2, '0')), description);
      button.addEventListener('click', () => selectPrompt(prompt.id)); return button;
    }));
    $('template-count').textContent = `${prompts.length} 个写作模板`;
    controls();
  }
  function selectPrompt(id: string) {
    const next = prompts.find(prompt => prompt.id === id); if (!next) return;
    const previous = selectedTemplate;
    if (previous) promptEdits.set(promptKey(previous), instruction.value);
    const newer = previous?.id === next.id && promptKey(previous) !== promptKey(next);
    const localEdit = newer && instruction.value !== previous.instruction;
    selected = id; selectedTemplate = next; category = next.category;
    instruction.value = localEdit ? instruction.value : promptEdits.get(promptKey(next)) ?? next.instruction;
    $('selected-template').textContent = `${next.title}${next.template_version ? ` · v${next.template_version}` : ''}`;
    if (newer) say(`已选用 v${next.template_version}。${localEdit ? '本页提示词修改仍保留；可点击“恢复模板提示词”使用新版内容。' : '素材和生成结果保留。'}`);
    brief.placeholder = (next.base_prompt || next.id) === 'news' ? '写下具体事件、时间地点、来源原文和你想讲清的角度…' : '写下主题、读者和你想讲清的内容，也可以粘贴素材或提纲…';
    showBriefHelp();
    if (next.needs_draft) $('use-draft').checked = !!source.value.trim();
    updateTemplates();
  }
  async function parseJSON(response: Response): Promise<unknown> {
    if(response.status===401||response.status===403)retire();
    const data = await writingJSON(response); if(!alive())throw new DOMException('页面已失效','AbortError');return data;
  }
  async function session(signal: AbortSignal) {
    if(!alive())throw new DOMException('页面已失效','AbortError');
    const response = await fetch('/api/v1/admin/session', {credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal, headers: {Accept: 'application/json'}});
    const data = await parseJSON(response);
    try {const token=writingSession(data,owner,login,csrf);field('csrf_token').value=token;$('login').hidden=true;return token;}catch(error){retire();throw error;}
  }
  function post(path:string,body:unknown,signal:AbortSignal,stream:true):Promise<Response>;
  function post(path:string,body:unknown,signal:AbortSignal,stream?:false):Promise<unknown>;
  async function post(path:string,body:unknown,signal:AbortSignal,stream = false) {
    const token = await session(signal);
    const response = await fetch(`/api/v1/admin/ai/${path}`, {
      method: 'POST', credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal,
      headers: {'Content-Type': 'application/json', Accept: stream ? 'text/event-stream' : 'application/json', 'X-CSRF-Token': token}, body: JSON.stringify(body),
    });
    if (!stream || !response.ok) return parseJSON(response);
    if (!response.headers.get('content-type')?.includes('text/event-stream')) throw new Error('服务器未返回流式正文，请检查服务配置');
    return response;
  }
  async function loadPrompts() {
    if (generating || preparing) return;
    loadController?.abort(); const controller = new AbortController(); loadController = controller;
    const timer = setTimeout(() => controller.abort(), 15000);
    $('prompts-retry').hidden = true;
    try {
      const response = await fetch('/api/v1/admin/ai/prompts', {credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: controller.signal, headers: {Accept: 'application/json'}});
      const data = await parseJSON(response);
      if (controller.signal.aborted) return;
      prompts = writingPrompts(data); loaded = true;
      if (!selectedTemplate) selectPrompt(required(prompts[0]).id);
      else {
        updateTemplates();
        const latest = prompts.find(prompt => prompt.id === selected);
        say(!latest ? '当前模板已不在列表中。素材、提示词和结果已保留，请选择可用模板。' : promptKey(latest) !== promptKey(selectedTemplate) ? `模板已更新为 v${latest.template_version}。当前仍选用 v${selectedTemplate.template_version}，点击该模板可明确选用新版；输入保留。` : '模板列表已刷新，当前输入保留。');
      }
    } catch (cause) { const error=writingFailure(cause);
      if(loadController!==controller)return;
      if (dialog.open && !controller.signal.aborted) { say(`${error.message}，可以重新加载。`, 'error'); $('prompts-retry').hidden = false; }
      else if (dialog.open) { say('加载模板超时，请重试。', 'error'); $('prompts-retry').hidden = false; }
    } finally { clearTimeout(timer); if (loadController === controller) loadController = null; controls(); }
  }
  function connectionInput(): WritingConnection {
    if (provider.value) return {provider_id: provider.value, model: savedModel.value};
    if (!base.value.trim() || !key.value.trim()) { showConnection(); (!base.value.trim() ? base : key).focus(); throw new Error('请先填写 API 地址与 Key，再探测或指定写作模型'); }
    return {base_url: base.value.trim(), api_key: key.value.trim(), model: model.value.trim()};
  }
  function rememberConnection() {
    if (!provider.value) return;
    preference = {...preference, provider_id: provider.value, models: {...preference.models, [provider.value]: savedModel.value}};
    try { localStorage.setItem(preferenceKey, JSON.stringify(preference)); } catch { /* Keep the choice in this page. */ }
  }
  function selectConnection(preferredModel = '') {
    connectionRevision++; probeController?.abort(); probing = false;
    const item = connections.providers.find(item => item.id === provider.value);
    $('stream-usage').checked = preference.stream_usage?.[provider.value || 'temporary'] !== false;
    $('stream-usage-option').hidden = ['openai_responses', 'azure_responses', 'anthropic', 'gemini'].includes(item?.protocol || '');
    $('temporary').hidden = !!item; $('saved-model-field').hidden = !item; $('saved-help').hidden = !item;
    savedModel.replaceChildren(...(item?.models || []).map(id => { const option = document.createElement('option'); option.value = id; option.textContent = id; return option; }));
    if (item) {
      const choice = preferredModel || preference.models?.[item.id] || item.default_model;
      if (item.models.includes(choice)) savedModel.value = choice;
      $('connection-badge').textContent = `${item.models.length} 个模型`; $('connection-badge').classList.add('is-connected');
      connectionSay('已使用保存的接口。', 'success'); rememberConnection();
    } else {
      $('connection-badge').textContent = '临时接口'; $('connection-badge').classList.remove('is-connected');
      connectionSay(connections.providers.length ? '临时连接仅在当前页面使用。' : '还没有保存的接口，可在设置中添加，也可临时填写。');
    }
    controls();
  }
  async function loadConnections() {
    connectionsController?.abort();
    const controller = new AbortController(); connectionsController = controller; controls();
    const timer = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await fetch('/api/v1/admin/ai/connections', {credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: controller.signal, headers: {Accept: 'application/json'}});
      const data = await parseJSON(response);
      if (controller.signal.aborted) return;
      connections = writingConnections(data); connections.providers=connections.providers.filter(item=>!imageProtocols.includes(item.protocol));
      const choice = chooseConnection(connections, preference);
      provider.replaceChildren(...connections.providers.map(item => { const option = document.createElement('option'); option.value = item.id; option.textContent = item.name; return option; }));
      const temporary = document.createElement('option'); temporary.value = ''; temporary.textContent = '临时接口（仅本页）'; provider.append(temporary);
      provider.value = choice.provider_id; selectConnection(choice.model);
    } catch (cause) { const error=writingFailure(cause); if(connectionsController!==controller)return; if (dialog.open) connectionSay(controller.signal.aborted ? '接口加载已取消或超时，可点击刷新接口重试。' : error.message, 'error'); }
    finally { clearTimeout(timer); if (connectionsController === controller) connectionsController = null; controls(); }
  }
  function invalidateConnection() {
    connectionRevision++; probeController?.abort(); probing = false;
    $('models').replaceChildren(); model.value = ''; $('connection-badge').textContent = '待探测'; $('connection-badge').classList.remove('is-connected');
    connectionSay('连接信息已修改，请重新探测或输入模型 ID。'); controls();
  }
  async function detectModels() {
    if (generating || probing || preparing) return;
    let connection;
    try { connection = connectionInput(); } catch (cause) { const error=writingFailure(cause); connectionSay(error.message, 'error'); return; }
    const controller = new AbortController(), revision = connectionRevision; probeController = controller; probing = true; controls();
    const timer = setTimeout(() => controller.abort(new DOMException('Timeout', 'TimeoutError')), 25000);
    connectionSay('正在验证接口并获取可用模型…');
    try {
      const data = writingModels(await post('models', connection, controller.signal));
      if (revision !== connectionRevision || controller.signal.aborted || !dialog.open) return;
      if (!Array.isArray(data.models) || !data.models.length || data.models.some(item => typeof item.id !== 'string')) throw new Error('接口未返回可用模型，可手动填写模型 ID');
      $('models').replaceChildren(...data.models.map(item => { const option = document.createElement('option'); option.value = item.id; option.label = item.name || item.owned_by || item.id; return option; }));
      if (!data.models.some(item => item.id === model.value)) model.value = required(data.models[0]).id;
      if (typeof data.base_url === 'string') base.value = data.base_url;
      $('connection-badge').textContent = `${data.models.length} 个模型`; $('connection-badge').classList.add('is-connected');
      connectionSay(`已探测到 ${data.models.length} 个模型，可以选择或搜索模型 ID。`, 'success');
      say('模型已就绪，写下主题即可开始生成。'); model.focus();
    } catch (cause) { const error=writingFailure(cause);
      if (revision === connectionRevision && dialog.open) connectionSay(controller.signal.reason?.name === 'TimeoutError' ? '模型探测超时，请重试或手动填写模型 ID。' : error.name === 'AbortError' ? '探测已取消。' : error.message, 'error');
    } finally { clearTimeout(timer); if (probeController === controller) { probing = false; probeController = null; controls(); } }
  }
  async function generate(refine = false) {
    if (generating || probing || preparing || !loaded || connectionsController || readOnly()) return;
    let connection: WritingConnection, draft: {title:string;content:string};
    const original = refine ? resultSnapshot() : null;
    try {
      if (!refine && !brief.value.trim()) { brief.focus(); throw new Error('请写下主题、要求或素材，再开始生成'); }
      draft = original ? {title: original.title, content: original.markdown} : $('use-draft').checked ? {title: field('title').value, content: source.value} : {title: '', content: ''};
      if (!refine) {
        const problem = briefProblem(selectedPrompt(), brief.value, instruction.value, draft.content);
        showBriefHelp(problem);
        if (problem) { settingsOpen(false); brief.focus(); throw new Error(problem); }
      }
      if ((refine || selectedPrompt()?.needs_draft) && !draft.content.trim()) throw new Error('这个模板需要参考草稿，请先填写文章正文，再勾选“参考当前草稿”');
      if (byteLength(draft.content) > 128 * 1024) throw new Error('参考草稿超过 128 KiB，请缩短正文或取消参考当前草稿');
      if (!$('max-tokens').checkValidity()) throw new Error('Token 上限需为 512–32768 之间的整数，也可留空');
      connection = connectionInput();
      connection.stream_usage = $('stream-usage').checked;
      if (!connection.model) { showConnection(); (provider.value ? savedModel : model).focus(); throw new Error('请先选择写作模型，或填写模型 ID'); }
    } catch (cause) { const error=writingFailure(cause); say(error.message, 'error'); return; }
    const beforeThinking = thinking.snapshot(), beforeStats = stats.snapshot();
    const controller = new AbortController(); generationController = controller;
    const timer = setTimeout(() => controller.abort(new DOMException('Timeout', 'TimeoutError')), 375000);
    generating = true; stats.reset(connection.model, performance.now()); settingsOpen(false); controls(); updateMetrics();
    thinking.reset(connection.model); thinkingFollow = true; drawThinking();
    $('stage-state').textContent = '正在连接模型'; say('正在连接模型，文章将在这里逐步呈现。');
    elapsedTimer = setInterval(updateMetrics, 1000);
    let started = false, completed = false;
    try {
      const response = await post('generate', {...connection, prompt_id: refine ? 'humanize' : selected, template_version: refine ? 0 : selectedPrompt()?.template_version || 0, instruction: refine ? '' : instruction.value, brief: refine ? '只改写表达，保留原稿全部事实、数字、引用、链接、代码和立场。删去套话、机械对比、强行总结及重复解释，不添加新材料，也不为了目标篇幅扩写。' : brief.value.trim(), tone: $('tone').value, length: $('length').value, language: $('language').value, writing_style: $('writing-style').value, style_reference: $('style-reference').value.trim(), avoid_phrases: $('avoid-phrases').value.trim(), max_tokens: Number($('max-tokens').value) || 0, draft}, controller.signal, true);
      if (controller.signal.aborted) throw controller.signal.reason;
      if (md.value.trim()) previous = {document: resultSnapshot(), thinking: beforeThinking, stats: beforeStats};
      raw = ''; rawBytes = 0; parsedMeta = ''; setMetadata(); md.value = ''; preparedHTML = null; started = true; follow = true; $('follow').setAttribute('aria-pressed', 'true');
      paint(); controls();
      await readStream(response, (name, value) => {
        const data=record(value);
        if (controller.signal.aborted) throw controller.signal.reason;
        if (name === 'ready') { if(data.model!==connection.model)throw new Error('返回的写作模型与本次选择不一致');$('stage-state').textContent = '正在构思文章'; say('模型已连接，正在组织标题与全文。'); }
        if (name === 'usage') { stats.receive(data); drawUsage(); }
        if (name === 'thinking') {
          if (data?.text) stats.output();
          thinking.append(data); $('stage-state').textContent = '模型正在思考'; scheduleThinking();
        }
        if (name === 'thinking_done') { thinking.settle(); scheduleThinking(); }
        if (name === 'delta') {
          if (typeof data?.text !== 'string') throw new Error('生成正文格式不正确，已保留收到的内容');
          if (data.text) stats.output();
          rawBytes += byteLength(data.text);
          if (rawBytes > MAX_OUTPUT) throw new Error('生成正文已达到 512 KiB 上限，已保留收到的内容');
          if (thinking.state === 'streaming') { thinking.settle(); scheduleThinking(); }
          raw += data.text; unapplied = true; $('stage-state').textContent = '正在撰写全文'; schedulePaint();
        }
        if (name === 'error') { stats.receive(data?.usage); throw new Error(typeof data?.message === 'string' ? data.message : '生成被中断，已保留收到的内容'); }
        if (name === 'done') {
          stats.receive(data?.usage);
          const done = writingDone(data);
          stats.settle(done.complete ? 'complete' : 'stopped');
          clearTimeout(renderTimer); renderTimer = 0; completed = true; generating = false;
          thinking.settle(done.complete ? 'complete' : 'stopped'); drawThinking();
          showResult(original ? {...done.document, title: original.title, slug: original.slug, tags: original.tags, category_ids: original.category_ids, cover_prompt: original.cover_prompt} : done.document); unapplied = true;
          $('stage-state').textContent = done.complete ? '全文已生成' : '已保留部分正文';
          const warnings = Array.isArray(done.document.warnings) ? done.document.warnings.filter(item => typeof item === 'string').join(' ') : '';
          say(warnings || '全文与文章信息已生成。检查或修改后，点击“填入文章”。', warnings || !done.complete ? 'warning' : 'success');
        }
      });
    } catch (cause) { const error=writingFailure(cause);
      if (!alive()) return;
      if (started) { thinking.settle(controller.signal.aborted ? 'stopped' : 'error'); stats.settle(controller.signal.aborted ? 'stopped' : 'error'); }
      else { thinking.restore(beforeThinking); stats.restore(beforeStats); }
      drawThinking();
      if (started && !completed) paint();
      $('stage-state').textContent = md.value.trim() ? '正文已保留' : '等待再次生成';
      const stopped = md.value.trim() ? '已停止生成，收到的正文已保留，可以继续编辑。' : thinking.text ? '已停止生成，收到的思考内容已保留，可重新生成。' : '已停止生成，可重新开始。';
      const message = controller.signal.reason?.name === 'TimeoutError' ? '生成超时，已收到的内容已保留，可重新生成。' : controller.signal.aborted ? stopped : error.message || '连接失败，已保留当前内容。';
      say(message, controller.signal.aborted ? '' : 'error');
      if (String(error.code || '').startsWith('ai.template_')) $('prompts-retry').hidden = false;
    } finally {
      clearTimeout(timer); clearTimeout(renderTimer); renderTimer = 0; clearInterval(elapsedTimer); elapsedTimer = 0;
      generating = false; if (generationController === controller) generationController = null;
      if (started && !completed && !md.value && raw) { md.value = splitDocument(raw).pending ? raw : splitDocument(raw).body; $('empty').hidden = true; $('result').hidden = false; drawPreview(); }
      updateMetrics(); controls();
    }
  }

  function applyFields(value: ArticleSnapshot, expected: ArticleSnapshot) {
    if (readOnly() || !sameSnapshot(expected, snapshot())) throw new Error('文章在操作期间发生了变化，请回到文章核对后重新打开 AI 编辑器');
    blockEditor.insert(value.content_source, {start: 0, end: source.value.length, value: source.value});
    for (const name of ['title', 'slug', 'excerpt', 'tags'] as const) {
      const input = field(name); if (!input) continue;
      input.value = value[name]; input.dispatchEvent(new Event('input', {bubbles: true}));
    }
    const ids = value.category_ids.map(String);
    for (const input of form.querySelectorAll<HTMLInputElement>('[name="category_ids"]')) { input.checked = ids.includes(input.value); input.dispatchEvent(new Event('change', {bubbles: true})); }
  }
  async function applyResult() {
    if (generating || preparing || readOnly() || !md.value.trim()) return;
    const before = snapshot();
    if (openingSnapshot && !sameSnapshot(openingSnapshot, before)) { say('文章在 AI 编辑器打开期间发生了变化，请回到文章核对后重新打开。', 'error'); return; }
    if (byteLength(md.value) > MAX_OUTPUT) { say('正文超过 512 KiB，请缩短后再填入。', 'error'); return; }
    const controller = new AbortController(); prepareController = controller; preparing = true; controls();
    const timer = setTimeout(() => controller.abort(new DOMException('Timeout', 'TimeoutError')), 25000);
    say('正在检查格式并准备填入文章…');
    try {
      const doc = writingDocument(await post('prepare', resultSnapshot(), controller.signal));
      if (controller.signal.aborted || !dialog.open) return;
      if (typeof doc.content_source !== 'string' || !Array.isArray(doc.tags) || !Array.isArray(doc.category_ids)) throw new Error('文章转换结果无效，当前内容已保留');
      const after = {title: doc.title, slug: $('keep-slug').checked && before.slug ? before.slug : doc.slug, excerpt: doc.excerpt, content_source: doc.content_source, tags: doc.tags.join(', '), type: before.type, category_ids: doc.category_ids.length ? doc.category_ids.map(String) : before.category_ids};
      applyFields(after, before);
      lastApplication = {before, after: snapshot()}; openingSnapshot = lastApplication.after; unapplied = false;
      showResult(doc); $('stage-state').textContent = '已填入文章'; say('标题、正文、摘要、标签与分类已填入，可继续编辑并保存。', 'success');
      dialog.close();
    } catch (cause) { const error=writingFailure(cause); if (dialog.open) say(controller.signal.reason?.name === 'TimeoutError' ? '填入前的检查超时，请重试；生成内容已保留。' : error.name === 'AbortError' ? '已取消填入。' : error.message, 'error'); }
    finally { clearTimeout(timer); if (prepareController === controller) prepareController = null; preparing = false; controls(); }
  }
  function undoApplication() {
    if (!lastApplication || readOnly() || generating || preparing) return;
    if (!sameSnapshot(lastApplication.after, snapshot())) { say('文章已继续编辑，不能直接撤回。可使用正文的“撤销”或版本记录恢复。', 'warning'); return; }
    try { applyFields(lastApplication.before, lastApplication.after); openingSnapshot = snapshot(); lastApplication = null; unapplied = true; say('已恢复填入前的标题、正文与文章信息。', 'success'); dialog.close(); }
    catch (cause) { const error=writingFailure(cause); say(error.message, 'error'); }
    controls();
  }
  launchers.forEach(button => button.addEventListener('click', () => {
    launcher = button;
    if (readOnly()) return;
    openingSnapshot = snapshot(); $('keep-slug-label').hidden = !openingSnapshot.slug;
    if (!dialog.open) dialog.showModal(); launcher.setAttribute('aria-expanded', 'true');
    settingsOpen(false); controls();
    if (!loaded) void loadPrompts();
    void loadConnections();
    brief.focus({preventScroll: true});
  }));
  $('close').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => {
    generationController?.abort(); probeController?.abort(); prepareController?.abort(); loadController?.abort(); connectionsController?.abort();
    launcher.setAttribute('aria-expanded', 'false'); if (launcher.isConnected) launcher.focus({preventScroll: true});
  });
  $('settings-toggle').addEventListener('click', () => settingsOpen(!dialog.classList.contains('is-settings-open')));
  $('thinking-toggle').addEventListener('click', () => {
    setThinkingOpen(!thinkingOpen);
    if (thinkingOpen && matchMedia('(max-width: 1179px)').matches) thinkingOutput.focus({preventScroll: true});
  });
  $('thinking-close').addEventListener('click', () => setThinkingOpen(false, true));
  function dismissPanel(event: Event) {
    if (thinkingOpen && matchMedia('(max-width: 1179px)').matches) { event.preventDefault(); setThinkingOpen(false, true); return true; }
    if ($('usage').open) { event.preventDefault(); $('usage').open = false; $('usage-summary').focus({preventScroll: true}); return true; }
    return false;
  }
  dialog.addEventListener('cancel', dismissPanel);
  dialog.addEventListener('keydown', event => {
    // Handle the inner panel before document-level Escape handlers or the
    // browser's native dialog close action can dismiss the editor as well.
    if (event.key === 'Escape' && !event.isComposing && !event.defaultPrevented && dismissPanel(event)) event.stopPropagation();
  });
  function sizeUsagePanel() {
    if (!$('usage').open) return;
    const available = required(dialog.querySelector('.ai-output-bar')).getBoundingClientRect().top - required(dialog.querySelector('.ai-stage')).getBoundingClientRect().top - 16;
    $('usage-panel').style.maxHeight = `${Math.max(80, Math.min(360, available))}px`;
  }
  $('usage').addEventListener('toggle', () => { if ($('usage').open) $('usage-panel').scrollTop = 0; sizeUsagePanel(); });
  window.addEventListener('resize', sizeUsagePanel);
  dialog.addEventListener('click', event => { if ($('usage').open && event.target instanceof Node && !$('usage').contains(event.target)) $('usage').open = false; });
  $('stream-usage').addEventListener('change', () => {
    preference = {...preference, stream_usage: {...preference.stream_usage, [provider.value || 'temporary']: $('stream-usage').checked}};
    try { localStorage.setItem(preferenceKey, JSON.stringify(preference)); } catch { /* Storage is optional. */ }
  });
  $('prompts-retry').addEventListener('click', loadPrompts);
  $('prompts-refresh')?.addEventListener('click', loadPrompts);
  $('reset-prompt').addEventListener('click', () => { if (selectedPrompt()) { instruction.value = selectedPrompt()?.instruction || ''; promptEdits.delete(promptKey(selectedPrompt())); } });
  $('detect').addEventListener('click', detectModels);
  base.addEventListener('input', invalidateConnection); key.addEventListener('input', invalidateConnection);
  $('key-toggle').addEventListener('click', () => { const visible = key.type === 'password'; key.type = visible ? 'text' : 'password'; $('key-toggle').textContent = visible ? '隐藏' : '显示'; $('key-toggle').setAttribute('aria-label', visible ? '隐藏 API Key' : '显示 API Key'); $('key-toggle').setAttribute('aria-pressed', String(visible)); });
  provider.addEventListener('change', () => selectConnection());
  savedModel.addEventListener('change', rememberConnection);
  $('connections-refresh').addEventListener('click', loadConnections);
  $('generate').addEventListener('click', () => generate());
  $('humanize').addEventListener('click', () => generate(true));
  $('stop').addEventListener('click', () => generationController?.abort());
  $('thinking-stop').addEventListener('click', () => generationController?.abort());
  $('apply').addEventListener('click', applyResult);
  $('undo-apply').addEventListener('click', undoApplication);
  $('previous').addEventListener('click', () => {
    if (!previous || generating || preparing) return;
    const current = {document: resultSnapshot(), thinking: thinking.snapshot(), stats: stats.snapshot()};
    stats.restore(previous.stats); showResult(previous.document); thinking.restore(previous.thinking); thinkingFollow = true; drawThinking();
    previous = current.document.markdown.trim() ? current : null;
    unapplied = true; say('已恢复上一版 AI 正文，可以继续编辑或填入。'); controls();
  });
  $('copy').addEventListener('click', async () => { try { await navigator.clipboard.writeText(md.value); say('正文已复制。', 'success'); } catch { setView('markdown'); md.focus(); md.select(); say('请使用 Ctrl / ⌘ + C 复制已选中的正文。'); } });
  function setView(next: string | undefined) {
    view = next === 'markdown' ? 'markdown' : 'preview'; preview.hidden = view !== 'preview'; $('markdown-pane').hidden = view !== 'markdown';
    $$('view').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.aiView === view)));
    if (view === 'preview') drawPreview();
  }
  $$('view').forEach(button => button.addEventListener('click', () => setView(button.dataset.aiView)));
  $$('starter').forEach(button => button.addEventListener('click', () => { if (!loaded) return; selectPrompt(button.dataset.aiStarter || ''); if (!brief.value.trim()) brief.value = selectedPrompt()?.example || ''; brief.focus(); }));
  $('follow').addEventListener('click', () => { follow = !follow; $('follow').setAttribute('aria-pressed', String(follow)); if (follow) output.scrollTop = output.scrollHeight; });
  output.addEventListener('scroll', () => { if (generating && output.scrollHeight - output.scrollTop - output.clientHeight > 100) { follow = false; $('follow').setAttribute('aria-pressed', 'false'); } }, {passive: true});
  $('thinking-follow').addEventListener('click', () => { thinkingFollow = !thinkingFollow; drawThinking(); });
  thinkingOutput.addEventListener('scroll', () => {
    if (!thinkingOpen || !thinkingOutput.clientHeight) return;
    const distance = thinkingOutput.scrollHeight - thinkingOutput.scrollTop - thinkingOutput.clientHeight;
    if (distance > 80 && thinkingFollow) { thinkingFollow = false; $('thinking-follow').setAttribute('aria-pressed', 'false'); $('thinking-follow').textContent = '继续跟随'; }
    else if (distance < 16 && !thinkingFollow) { thinkingFollow = true; $('thinking-follow').setAttribute('aria-pressed', 'true'); $('thinking-follow').textContent = '跟随思考'; }
  }, {passive: true});
  md.addEventListener('input', () => { preparedHTML = null; unapplied = true; updateMetrics(); controls(); });
  Object.values(fields).forEach(input => input.addEventListener('input', () => { unapplied = true; updateHeading(); }));
  brief.addEventListener('keydown', event => { if ((event.ctrlKey || event.metaKey) && event.key === 'Enter' && !event.isComposing) { event.preventDefault(); void generate(); } });
  brief.addEventListener('input', () => showBriefHelp());
  instruction.addEventListener('input', () => showBriefHelp());
  $('use-draft').addEventListener('change', () => showBriefHelp());
  dialog.addEventListener('keydown', event => { if (event.key === 'Enter' && event.target instanceof HTMLInputElement) event.preventDefault(); });
  document.addEventListener('visibilitychange', () => { dialog.classList.toggle('is-page-hidden', document.hidden); if (!document.hidden) { if (generating) paint(); drawThinking(); } });
  window.addEventListener('beforeunload', event => { if (generating || unapplied) { event.preventDefault(); event.returnValue = ''; } });
  window.addEventListener('pagehide', () => { generationController?.abort(); probeController?.abort(); prepareController?.abort(); loadController?.abort(); connectionsController?.abort(); key.value = ''; key.type = 'password'; clearTimeout(renderTimer); clearTimeout(thinkingTimer); clearInterval(elapsedTimer); });
  setThinkingOpen(thinkingOpen); drawThinking(); updateMetrics();
  controls();
  window.ChunnelAIEditorReady = true;
})();
