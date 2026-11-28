const test = require('node:test');
const assert = require('node:assert/strict');
const {splitDocument, renderMarkdown, SSEDecoder, readStream, ThinkingTrace, GenerationStats, MAX_THINKING, MAX_OUTPUT, byteLength, chooseConnection, briefProblem} = require('./helpers/typescript.cjs').loadNativeModule('frontend/shared/ai-writing-helpers.ts');

test('provider usage stays unknown until reported, and explicit zero is retained', () => {
  const stats = new GenerationStats(); stats.reset('writer', 1000);
  stats.output(2000); stats.receive(null, 2000); stats.receive({}, 2000);
  assert.deepEqual(stats.usage, {}); assert.equal(stats.tps(), null);
  stats.receive({prompt_tokens: 10}, 2500);
  assert.equal(stats.usage.total_tokens, undefined); assert.equal(stats.usage.completion_tokens, undefined);
  stats.receive({prompt_tokens: 10, completion_tokens: 0, total_tokens: 10}, 3000);
  assert.equal(stats.usage.completion_tokens, 0); assert.equal(stats.tps(), 0);
});

test('average TPS includes waiting and uses the last provider sample, even after a stop', () => {
  const stats = new GenerationStats(); stats.reset('writer', 1000); stats.output(6000);
  stats.receive({prompt_tokens: 100, completion_tokens: 200, reasoning_tokens: 120, total_tokens: 300}, 11000);
  assert.equal(stats.tps(), 20); assert.equal(stats.firstOutput, 5000);
  stats.receive({prompt_tokens: 100, completion_tokens: 200, reasoning_tokens: 120, total_tokens: 300}, 15000);
  assert.equal(stats.tps(), 20); assert.equal(stats.sampleAt, 10000);
  stats.settle('stopped', 21000);
  assert.equal(stats.elapsed(99000), 20000); assert.equal(stats.tps(), 20);
  stats.receive({completion_tokens: 99999}, 22000);
  assert.equal(stats.usage.completion_tokens, 200);
});

test('usage snapshots do not add reasoning/cache twice and honor provider totals', () => {
  const stats = new GenerationStats(); stats.reset('writer', 0);
  stats.receive({prompt_tokens: 100, completion_tokens: 20, reasoning_tokens: 10, cached_tokens: 60}, 5000);
  assert.equal(stats.usage.total_tokens, 120); assert.equal(stats.usage.total_source, 'calculated');
  stats.receive({prompt_tokens: 100, completion_tokens: 40, reasoning_tokens: 15, total_tokens: 160, tool_prompt_tokens: 20}, 6000);
  assert.equal(stats.usage.total_tokens, 160); assert.equal(stats.usage.completion_tokens, 40);
  assert.equal(stats.usage.total_source, 'reported');
});

test('missing and invalid optional usage never invents token values or breaks a generation', () => {
  const stats = new GenerationStats(); stats.reset('writer', 0);
  for (const value of [undefined, null, [], 'bad', {prompt_tokens: -1, completion_tokens: '30', total_tokens: NaN}, {total_tokens: Infinity}, {total_tokens: Number.MAX_SAFE_INTEGER + 1}]) stats.receive(value, 1000);
  assert.deepEqual(stats.usage, {}); assert.equal(stats.tps(), null);
  stats.output(50); stats.receive({completion_tokens: 100}, 100);
  assert.equal(stats.tps(), null, 'too short to measure reliably');
  stats.receive({prompt_tokens: Number.MAX_SAFE_INTEGER, completion_tokens: 101}, 2000);
  assert.equal(stats.usage.total_tokens, undefined);
});

test('stats reset per generation and restore with the matching result without running timers', () => {
  const stats = new GenerationStats(); stats.reset('first', 0); stats.output(100);
  stats.receive({prompt_tokens: 100, completion_tokens: 50}, 5000); stats.settle('complete', 5100);
  const previous = stats.snapshot();
  stats.reset('second', 9000); assert.deepEqual(stats.usage, {}); assert.equal(stats.firstOutput, null);
  stats.receive({prompt_tokens: 10}, 9500); stats.settle('error', 10000);
  stats.restore(previous);
  assert.equal(stats.model, 'first'); assert.equal(stats.tps(), 10); assert.equal(stats.elapsed(99999), 5100);
  stats.usage.prompt_tokens = 200; assert.equal(previous.usage.prompt_tokens, 100);
});

test('usage-only events survive a stream error and late events cannot alter retained stats', async () => {
  const stats = new GenerationStats(); stats.reset('writer', 0); stats.output(1000);
  const response = new Response('event: usage\ndata: {"prompt_tokens":10,"completion_tokens":40}\n\nevent: error\ndata: {}\n\nevent: usage\ndata: {"total_tokens":9999}\n\n');
  await readStream(response, (name, data) => {
    if (name === 'usage') stats.receive(data, 5000);
    if (name === 'error') stats.settle('error', 6000);
  });
  assert.equal(stats.usage.total_tokens, 50); assert.equal(stats.tps(), 8); assert.equal(stats.state, 'error');
});
const {parseModels} = require('./helpers/typescript.cjs').loadNativeModule('frontend/shared/ai-settings-data.ts');

const briefs = require('../../internal/aieditor/testdata/briefs.json');
for (const item of briefs) {
  test(`brief guidance: ${item.name}`, () => {
    const prompt = {id: item.prompt_id, instruction: '模板提示词', needs_draft: ['humanize', 'rewrite', 'expand', 'shorten'].includes(item.prompt_id)};
    const message = briefProblem(prompt, item.brief, item.custom_instruction || prompt.instruction, item.draft || '');
    assert.equal(!!message, item.blocked);
    if (item.blocked) assert.match(message, /补充具体事件和来源原文/);
  });
}
test('an unfilled news starter needs material and ordinary starters remain available', () => {
  const prompt = {id: 'news', instruction: '根据原文写消息', example: '事件与日期：\n来源原文：'};
  assert.match(briefProblem(prompt, prompt.example, prompt.instruction), /来源原文/);
  assert.equal(briefProblem(prompt, prompt.example + '\n图书馆恢复开放。', prompt.instruction), '');
  assert.equal(briefProblem({...prompt, id: 'tutorial'}, prompt.example, prompt.instruction), '');
});

test('all text protocols remain selectable independently of image providers', () => {
  for (const protocol of ['', 'openai_responses', 'anthropic', 'gemini', 'azure_openai', 'azure_responses', 'azure_openai_legacy']) {
    const data = {default_provider: 'writer', providers: [{id: 'image', protocol: 'gemini_images', models: ['image']}, {id: 'writer', protocol, models: ['model'], default_model: 'model'}]};
    assert.deepEqual(chooseConnection(data), {provider_id: 'writer', model: 'model'});
  }
  assert.deepEqual(chooseConnection({providers: [{id: 'future', protocol: 'unknown', models: ['model']}]}), {provider_id: '', model: ''});
});

test('saved model choices are scoped to their provider and recover from removed configuration', () => {
  const data = {default_provider: 'first', providers: [{id: 'first', models: ['a', 'b'], default_model: 'b'}, {id: 'second', models: ['c', 'd'], default_model: 'c'}]};
  assert.deepEqual(chooseConnection(data), {provider_id: 'first', model: 'b'});
  assert.deepEqual(chooseConnection(data, {provider_id: 'second', models: {second: 'd', first: 'a'}}), {provider_id: 'second', model: 'd'});
  assert.deepEqual(chooseConnection(data, {provider_id: 'second', models: {second: 'a'}}), {provider_id: 'second', model: 'c'});
  assert.deepEqual(chooseConnection(data, {provider_id: 'deleted', models: {first: 'a'}}), {provider_id: 'first', model: 'a'});
  assert.deepEqual(chooseConnection({providers: []}), {provider_id: '', model: ''});
});

test('manual model lists accept lines and commas without changing provider model IDs', () => {
  assert.deepEqual(parseModels('vendor/model-v2\r\nwriter, writer，model:latest\n\n'), ['vendor/model-v2', 'writer', 'model:latest']);
  assert.deepEqual(parseModels('  '), []);
});

test('metadata stays out of the live article, including at every split boundary', () => {
  const metadata = '<newpress-meta>{"title":"文章标题","tags":["写作"]}</newpress-meta>';
  for (let end = 0; end < metadata.length; end++) assert.equal(splitDocument(metadata.slice(0, end)).body, '');
  const result = splitDocument(metadata + '\n\n## 第一节\n\n正文 🌏');
  assert.equal(result.meta.title, '文章标题');
  assert.equal(result.body, '## 第一节\n\n正文 🌏');
  assert.equal(splitDocument('<newpress-meta>{bad}</newpress-meta>\n已收到正文').body, '已收到正文');
  assert.equal(splitDocument('普通正文').body, '普通正文');
});

test('a wrapping Markdown fence is removed without discarding code inside the article', () => {
  const text = '```markdown\n<newpress-meta>{"title":"代码文章"}</newpress-meta>\n说明\n\n```js\nconst x = 1;\n```\n\n结尾。\n```';
  const parsed = splitDocument(text);
  assert.equal(parsed.meta.title, '代码文章');
  assert.equal(parsed.body, '说明\n\n```js\nconst x = 1;\n```\n\n结尾。');
  assert.match(splitDocument('```js\nconst x = 1;\n```').body, /^```js/);
});

test('live Markdown preview cannot create executable markup or load images', () => {
  const html = renderMarkdown('## 标题\n\n<script>alert(1)</script>\n<img src=x onerror=evil()>\n\n[链接](javascript:alert(1))\n![图片](https://tracker.example/a)\n\n```html\n<iframe src=evil></iframe>\n```\n\n**强调**\n\n- 条目一\n- 条目二');
  assert.doesNotMatch(html, /<(script|img|iframe|a)\b/i);
  assert.match(html, /&lt;script&gt;/);
  assert.match(html, /<h2>标题<\/h2>/);
  assert.match(html, /<strong>强调<\/strong>/);
  assert.match(html, /<ul><li>条目一<\/li><li>条目二<\/li><\/ul>/);
  assert.match(html, /<pre><code>/);
});

test('SSE decoder accepts CRLF, comments, multiline JSON and arbitrary chunk boundaries', () => {
  const events = [], decoder = new SSEDecoder((name, value) => events.push([name, value]));
  const text = '\ufeff: ping\r\nevent: delta\r\ndata: {\r\ndata: "text":"你好 🌏"}\r\n\r\nevent: done\ndata: {"complete":true}\n\n';
  for (const char of text) decoder.feed(char);
  decoder.finish();
  assert.deepEqual(events, [['delta', {text: '你好 🌏'}], ['done', {complete: true}]]);
});

test('stream reader preserves UTF-8 characters split across network bytes', async () => {
  const text = 'event: delta\ndata: {"text":"中文 🌏"}\n\nevent: done\ndata: {"complete":true}\n\n';
  const bytes = new TextEncoder().encode(text), received = [];
  const body = new ReadableStream({start(controller) { for (const byte of bytes) controller.enqueue(Uint8Array.of(byte)); controller.close(); }});
  await readStream(new Response(body), (name, data) => received.push([name, data]));
  assert.deepEqual(received, [['delta', {text: '中文 🌏'}], ['done', {complete: true}]]);
});

test('a disconnected stream keeps partial text and cannot report success', async () => {
  let text = '';
  await assert.rejects(readStream(new Response('event: delta\ndata: {"text":"收到的部分"}\n\n'), (name, data) => { if (name === 'delta') text += data.text; }), /中断/);
  assert.equal(text, '收到的部分');
});

test('reader cancels after a terminal error instead of consuming later data', async () => {
  const received = [];
  await readStream(new Response('event: error\ndata: {"message":"额度不足"}\n\nevent: delta\ndata: {"text":"迟到的内容"}\n\n'), (name, data) => received.push([name, data]));
  assert.deepEqual(received, [['error', {message: '额度不足'}]]);
});

test('malformed and oversized frames fail with an actionable local error', () => {
  const decoder = new SSEDecoder(() => {});
  assert.throws(() => decoder.feed('event: delta\ndata: broken\n\n'), /格式/);
  const oversized = new SSEDecoder(() => {});
  assert.throws(() => oversized.feed('x'.repeat(1024 * 1024 + 1)), /过大/);
  assert.equal(byteLength('中文🌏'), 10);
  assert.equal(MAX_OUTPUT, 524288);
});

test('thinking streams independently, closes before body, and ignores events after done', async () => {
  const thinking = new ThinkingTrace(); thinking.reset('writer');
  let body = '', received = [];
  const events = [
    ['thinking', {text: '先看材料。'}], ['thinking', {text: '\n\n再确定结构。'}], ['thinking_done', {}],
    ['delta', {text: '文章正文'}], ['done', {}], ['thinking', {text: '迟到的思考'}]
  ];
  const bytes = new TextEncoder().encode(events.map(([name, data]) => `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`).join(''));
  const stream = new ReadableStream({start(controller) { for (const byte of bytes) controller.enqueue(Uint8Array.of(byte)); controller.close(); }});
  await readStream(new Response(stream), (name, data) => {
    received.push(name);
    if (name === 'thinking') thinking.append(data, 1000);
    if (name === 'thinking_done') thinking.settle('received', 4000);
    if (name === 'delta') { assert.equal(thinking.state, 'received'); body += data.text; }
    if (name === 'done') thinking.settle('complete', 9000);
  });
  assert.equal(thinking.text, '先看材料。\n\n再确定结构。');
  assert.equal(body, '文章正文'); assert.equal(thinking.state, 'complete');
  assert.equal(thinking.elapsed(12000), 3000);
  assert.deepEqual(received, ['thinking', 'thinking', 'thinking_done', 'delta', 'done']);
});

test('thinking resets for regeneration and restores only the corresponding local version', () => {
  const thinking = new ThinkingTrace(); thinking.reset('first');
  thinking.append({text: '旧版思考'}, 100); thinking.settle('complete', 600);
  const previous = thinking.snapshot();
  thinking.reset('second'); assert.equal(thinking.text, ''); assert.equal(thinking.bytes, 0); assert.equal(thinking.state, 'waiting');
  thinking.append({text: '新版思考'}, 2000); thinking.settle('stopped', 2500);
  thinking.restore(previous);
  assert.equal(thinking.model, 'first'); assert.equal(thinking.text, '旧版思考');
  assert.equal(thinking.state, 'complete'); assert.equal(thinking.elapsed(9000), 500);
  assert.equal(previous.text, '旧版思考');
});

test('interleaved thinking measures its own active periods without article time', () => {
  const thinking = new ThinkingTrace();
  thinking.append({text: '第一段'}, 1000); thinking.settle('received', 2000);
  thinking.append({text: '第二段'}, 6000); thinking.settle('complete', 8000);
  assert.equal(thinking.elapsed(20000), 3000);
});

test('absent thinking and interrupted thinking have honest separate empty states', async () => {
  const thinking = new ThinkingTrace(); thinking.reset(); thinking.settle('complete');
  assert.equal(thinking.state, 'unavailable'); assert.equal(thinking.text, '');
  thinking.reset();
  await assert.rejects(readStream(new Response('event: thinking\ndata: {"text":"已收到的思考"}\n\n'), (name, data) => {
    if (name === 'thinking') thinking.append(data, 1000);
  }), /中断/);
  thinking.settle('error', 2000);
  assert.equal(thinking.state, 'error'); assert.equal(thinking.text, '已收到的思考');
  assert.equal(thinking.elapsed(9000), 1000);
  thinking.reset(); thinking.settle('stopped');
  assert.equal(thinking.state, 'stopped'); assert.equal(thinking.text, '');
});

test('thoughts have a separate UTF-8 budget, preserve complete characters and do not execute markup', () => {
  const thinking = new ThinkingTrace();
  thinking.append({text: '<img src=x onerror=evil()>\n\n**材料**\n\n' + '想🌏'.repeat(MAX_THINKING)});
  assert.equal(thinking.truncated, true);
  assert.equal(thinking.bytes, byteLength(thinking.text)); assert.ok(thinking.bytes <= MAX_THINKING);
  assert.doesNotMatch(thinking.text, /\ufffd/);
  const retained = thinking.text;
  thinking.append({text: '超出的思考'}); assert.equal(thinking.text, retained);
  const html = renderMarkdown(thinking.text);
  assert.doesNotMatch(html, /<img/); assert.match(html, /&lt;img/); assert.match(html, /<strong>材料<\/strong>/);
  thinking.reset(); thinking.append({text: '服务器截断的前缀', truncated: true});
  assert.equal(thinking.truncated, true);
  assert.throws(() => thinking.append(null), /思考内容格式/);
});

test('late thinking after a terminal error cannot mutate retained trace', async () => {
  const thinking = new ThinkingTrace();
  await readStream(new Response('event: thinking\ndata: {"text":"保留"}\n\nevent: error\ndata: {}\n\nevent: thinking\ndata: {"text":"丢弃"}\n\n'), (name, data) => {
    if (name === 'thinking') thinking.append(data);
    if (name === 'error') thinking.settle('error');
  });
  assert.equal(thinking.text, '保留'); assert.equal(thinking.state, 'error');
});
