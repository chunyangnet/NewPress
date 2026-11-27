import { renderBlock, serialize, replacement, localImageURL, localPostURL, History, bytes, MAX_BYTES, MAX_BLOCKS, MAX_ROWS, MAX_COLUMNS } from './composer-core';
import { createComposerParser } from './composer-parser';
import type { BlockKind, ComposerBlock, ComposerState } from './composer-types';
import type { EditorSourceSelection } from './media-types';

const core = { renderBlock, serialize, replacement, localImageURL, localPostURL, History, MAX_BYTES, MAX_BLOCKS };
declare const module: { exports: unknown } | undefined;
const commonJS = typeof module === 'undefined' ? undefined : module;
if (commonJS?.exports) commonJS.exports = core;
export {};

type TextField = 'kind' | 'text' | 'language' | 'tone' | 'layout' | 'alt' | 'caption';
interface FieldOptions { choices?: [string, string][]; language?: boolean; rows?: number; code?: boolean }
(() => {
  if (typeof document === 'undefined') return;
  const formNode = document.querySelector<HTMLFormElement>('[data-content-editor]');
  const sourceNode = formNode?.querySelector<HTMLTextAreaElement>('[data-content-source]');
  // The article entry owns its continuous editor and initializes independently.
  if (!formNode || !sourceNode || formNode.hasAttribute('data-document-editor')) return;
  const form = formNode, source = sourceNode;
  const root = form.querySelector<HTMLElement>('[data-composer]')!, list = form.querySelector<HTMLElement>('[data-composer-list]')!;
  const toolbar = form.querySelector<HTMLElement>('[data-composer-toolbar]')!, sourcePane = form.querySelector<HTMLElement>('[data-composer-source-pane]')!;
  const feedback = form.querySelector<HTMLElement>('[data-composer-feedback]')!, count = form.querySelector<HTMLElement>('[data-composer-count]')!;
  const undo = form.querySelector<HTMLButtonElement>('[data-composer-undo]')!, redo = form.querySelector<HTMLButtonElement>('[data-composer-redo]')!;
  if (!root || !list || !toolbar || !sourcePane || !feedback || !count || !undo || !redo) return;
  const labels: Record<BlockKind, string> = {paragraph: '段落', h2: '二级标题', h3: '三级标题', quote: '引用', code: '代码', table: '表格', callout: '提示', list: '列表', image: '图片', post: '文章卡片', component: '文章组件', html: 'HTML 保留块'};
  const history = new History(), { parse, nextID } = createComposerParser(document);
  const sourceLimit = Math.min(MAX_BYTES, Number(source.dataset.maxBytes) || MAX_BYTES);
  const sizeError = `内容超过 ${sourceLimit / 1024} KiB，请缩短后保存`;
  let active = '', mode = 'source', syncing = false, previous = source.value;
  const readOnly = () => source.disabled || source.readOnly || source.matches(':disabled');
  const element = <K extends keyof HTMLElementTagNameMap>(tag: K, className = '', text = ''): HTMLElementTagNameMap[K] => { const node = document.createElement(tag); node.className = className; node.textContent = text; return node; };
  const say = (message: string) => { feedback.textContent = message; feedback.hidden = !message; };
  let state = parse(source.value);
  function updateControls() {
    count.textContent = String(state.blocks.length);
    undo.disabled = readOnly() || !history.past.length;
    redo.disabled = readOnly() || !history.future.length;
    root.querySelectorAll<HTMLButtonElement>('[data-block-add], [data-editor-media-open], [data-editor-widget-open], [data-component-library-open]').forEach(button => { button.disabled = readOnly(); });
  }
  function emit(value: string, group = '', record = true) {
    if (value !== previous && record) history.record(previous, group);
    previous = value; syncing = true; source.value = value;
    source.dispatchEvent(new Event('input', {bubbles: true})); syncing = false;
    updateControls();
  }
  function activate(id: string) {
    active = id;
    list.querySelectorAll<HTMLElement>('[data-block-id]').forEach(card => card.classList.toggle('is-active', card.dataset.blockId === id));
  }
  const focusBlock = (id: string) => { const card = [...list.querySelectorAll<HTMLElement>('[data-block-id]')].find(node => node.dataset.blockId === id); (card?.querySelector<HTMLElement>('textarea, input, select') || card)?.focus(); };
  function apply(candidate: ComposerState, focus = '', message = '') {
    if (readOnly()) return false;
    let value;
    try {
      if (candidate.blocks.length > MAX_BLOCKS) throw new Error(`内容块最多 ${MAX_BLOCKS} 个，可在源码中继续编辑`);
      value = serialize(candidate);
      if (bytes(value) > sourceLimit) throw new Error(sizeError);
    } catch (error) { say(error instanceof Error ? error.message : '内容块无效'); return false; }
    state = candidate; emit(value); render(); if (focus) { activate(focus); focusBlock(focus); } say(message); return true;
  }
  function edit(block: ComposerBlock, key: TextField | 'rows', value: string | string[][], input: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement) {
    if (readOnly()) return;
    if (key === 'rows') { if (typeof value === 'string') return; block.rows = value; }
    else { if (typeof value !== 'string') return; if (key === 'kind') { if (value !== 'h2' && value !== 'h3') return; block.kind = value; } else block[key] = value; }
    block.dirty = true;
    const serialized = serialize(state), error = bytes(serialized) > sourceLimit ? sizeError : '';
    input.setCustomValidity(error); say(error); emit(serialized, `${block.id}:${key}`);
  }
  function field(card: HTMLElement, block: ComposerBlock, title: string, key: TextField, options: FieldOptions = {}) {
    const label = element('label', 'admin-block-field', title);
    let input: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;
    if (options.choices) {
      input = document.createElement('select');
      for (const [value, title] of options.choices) { const item = element('option', '', title); item.value = value; input.append(item); }
      if (block[key] && ![...input.options].some(item => item.value === block[key])) { const option = element('option', '', block[key]!); option.value = block[key]!; input.append(option); }
    } else if (options.language) {
      input = document.createElement('input'); input.type = 'text'; input.spellcheck = false;
      input.setAttribute('list', 'npw-languages'); input.placeholder = 'auto / python / c++ / c#'; input.maxLength = 40;
    } else { input = document.createElement('textarea'); input.rows = options.rows || 3; input.spellcheck = !options.code; input.classList.toggle('is-code', !!options.code); }
    input.value = block[key] || ''; input.disabled = readOnly(); input.setAttribute('aria-label', title);
    input.addEventListener(options.choices ? 'change' : 'input', () => {
      const value = options.language ? input.value.trim() : input.value;
      if (options.language && value && !/^[a-zA-Z0-9_+.#-]{1,40}$/.test(value)) { input.setCustomValidity('请输入语言名称或别名，如 python、c++、c#'); return; }
      input.setCustomValidity(''); edit(block, key, value, input);
    });
    label.append(input); card.append(label); return input;
  }
  function action(title: string, handler: () => void, disabled = false) {
    const button = element('button', '', title); button.type = 'button'; button.disabled = readOnly() || disabled;
    button.addEventListener('click', handler); return button;
  }
  function move(id: string, direction: number) {
    const blocks = [...state.blocks], index = blocks.findIndex(block => block.id === id), next = index + direction;
    if (index < 0 || next < 0 || next >= blocks.length) return;
    [blocks[index], blocks[next]] = [blocks[next]!, blocks[index]!];
    apply({...state, blocks}, id, `已移至第 ${next + 1} 个内容块`);
  }
  function table(card: HTMLElement, block: ComposerBlock) {
    if (!block.rows?.length) return;
    const viewport = element('div', 'admin-block-table'); viewport.tabIndex = 0; viewport.setAttribute('aria-label', '表格单元格，超出宽度时可横向滚动');
    const grid = document.createElement('table');
    block.rows.forEach((row, r) => {
      const tr = document.createElement('tr');
      row.forEach((value, c) => {
        const cell = document.createElement(r ? 'td' : 'th'), input = document.createElement('textarea');
        input.rows = 2; input.value = value; input.disabled = readOnly(); input.setAttribute('aria-label', `${r ? `第 ${r + 1} 行` : '表头'}第 ${c + 1} 列`);
        input.addEventListener('input', () => { const rows = block.rows!.map(row => [...row]); rows[r]![c] = input.value; edit(block, 'rows', rows, input); });
        cell.append(input); tr.append(cell);
      });
      grid.append(tr);
    });
    viewport.append(grid); card.append(viewport);
    const controls = element('div', 'admin-block-table-controls');
    const resize = (rows: string[][], message: string) => {
      const replacement = {...block, rows, dirty: true};
      apply({...state, blocks: state.blocks.map(item => item.id === block.id ? replacement : item)}, block.id, message);
    };
    controls.append(
      action('加一行', () => resize([...block.rows!, block.rows![0]!.map(() => '')], '已添加一行'), block.rows.length >= MAX_ROWS),
      action('加一列', () => resize(block.rows!.map(row => [...row, '']), '已添加一列'), block.rows[0]!.length >= MAX_COLUMNS),
      action('删除末行', () => resize(block.rows!.slice(0, -1), '末行已删除，可撤销'), block.rows.length <= 2),
      action('删除末列', () => resize(block.rows!.map(row => row.slice(0, -1)), '末列已删除，可撤销'), block.rows[0]!.length <= 1)
    );
    card.append(controls, element('small', 'admin-block-hint', '第一行为表头 · 最多 30 行 × 10 列'));
  }
  function render() {
    const fragment = document.createDocumentFragment();
    if (!state.blocks.length) {
      const empty = element('div', 'admin-composer-empty'); empty.append(element('h3', '', '暂无内容块'), element('p', '', '添加段落或从组件库中选择内容。')); fragment.append(empty);
    }
    state.blocks.forEach((block, index) => {
      const card = element('section', `admin-block admin-block-${block.kind}`); card.dataset.blockId = block.id; card.tabIndex = -1;
      card.setAttribute('aria-label', `第 ${index + 1} 块：${labels[block.kind]}`);
      const header = element('header', 'admin-block-heading');
      header.append(element('span', '', String(index + 1).padStart(2, '0')), element('b', '', labels[block.kind]));
      const tools = element('nav', 'admin-block-tools'); tools.setAttribute('aria-label', `第 ${index + 1} 块操作`);
      tools.append(action('上移', () => move(block.id, -1), index === 0), action('下移', () => move(block.id, 1), index === state.blocks.length - 1), action('复制', () => {
        const copy = {...structuredClone(block), id: nextID()};
        if (copy.kind === 'component' && copy.config) { copy.config.id = crypto.randomUUID().replace(/-/g, ''); copy.dirty = true; }
        const blocks = [...state.blocks]; blocks.splice(index + 1, 0, copy); apply({...state, blocks}, copy.id, '内容块已复制');
      }), action('删除', () => {
        const blocks = state.blocks.filter(item => item.id !== block.id); apply({...state, blocks}, blocks[Math.min(index, blocks.length - 1)]?.id, '内容块已删除，可撤销');
      }));
      header.append(tools); card.append(header);
      if (block.kind === 'table') table(card, block);
      else if (block.kind === 'component') {
        const summary = element('div', 'admin-block-component-summary');
        const config = block.config!;
        const definition = window.NewPressComponentLibrary?.definitions?.find(d => d.kind === config.kind);
        summary.append(element('small', '', definition?.name || config.kind), element('strong', '', config.title || '未命名组件'));
        const editButton = action('编辑组件', () => {
          const start = serialize({blocks: state.blocks.slice(0, index), tail: ''}).length + (block.prefix || '').length;
          const end = start + (block.dirty ? renderBlock(block) : block.raw!).length;
          window.NewPressComponentLibrary?.edit(config, {start, end, value: source.value}, editButton);
        });
        summary.append(editButton); card.append(summary);
      }
      else if (block.kind === 'post') {
        const summary = element('div', 'admin-block-post-summary');
        summary.append(element('small', '', `本站文章 · #${block.postID}`), element('strong', '', block.title || ''));
        card.append(summary);
        field(card, block, '展示样式', 'layout', {choices: [['card', '图文卡片'], ['compact', '紧凑链接']]});
        card.append(element('small', 'admin-block-hint', '展示时读取文章的最新公开信息，外观跟随当前主题。保存后可在前台查看。'));
      } else if (block.kind === 'image') {
        const preview = element('img', 'admin-block-image-preview'); preview.src = block.url || ''; preview.alt = block.alt || ''; preview.loading = 'lazy'; preview.width = block.width!; preview.height = block.height!;
        card.append(preview); field(card, block, '图片替代文本（装饰图片可留空）', 'alt', {rows: 2}); field(card, block, '图片说明', 'caption', {rows: 2});
      } else {
        if (['h2', 'h3'].includes(block.kind)) field(card, block, '标题层级', 'kind', {choices: [['h2', '二级标题'], ['h3', '三级标题']]});
        if (block.kind === 'code') field(card, block, '代码语言（留空自动识别，plaintext 为纯文本）', 'language', {language: true});
        if (block.kind === 'callout') field(card, block, '提示类型', 'tone', {choices: [['note', '说明'], ['tip', '建议'], ['warning', '注意']]});
        if (block.kind === 'list') {
          const toggle = action(block.ordered ? '切换为无序列表' : '切换为有序列表', () => { const next = {...block, ordered: !block.ordered, dirty: true}; apply({...state, blocks: state.blocks.map(item => item.id === block.id ? next : item)}, block.id); }); card.append(toggle);
        }
        field(card, block, block.kind === 'html' ? 'HTML 源码' : block.kind === 'list' ? '列表项目（每行一项）' : labels[block.kind] + '内容', 'text', {code: ['code', 'html'].includes(block.kind), rows: ['code', 'html'].includes(block.kind) ? 6 : ['h2', 'h3'].includes(block.kind) ? 2 : 3});
        if (block.kind === 'html') card.append(element('small', 'admin-block-hint', '复杂格式会原样保留，可直接修改源码。保存后可在前台查看效果。'));
      }
      card.addEventListener('focusin', () => activate(block.id));
      card.addEventListener('keydown', event => { if (event.altKey && ['ArrowUp', 'ArrowDown'].includes(event.key)) { event.preventDefault(); move(block.id, event.key === 'ArrowUp' ? -1 : 1); } });
      fragment.append(card);
    });
    list.replaceChildren(fragment); activate(active); updateControls();
  }
  function setMode(next: string, focus = false) {
    mode = next;
    if (mode === 'blocks') { state = parse(source.value); render(); }
    root.hidden = mode !== 'blocks'; sourcePane.hidden = mode !== 'source';
    toolbar.querySelectorAll<HTMLButtonElement>('[data-composer-mode]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.composerMode === mode)));
    if (focus) { if (mode === 'source') source.focus(); else if (state.blocks.length) focusBlock(state.blocks[0]!.id); else { const inserter = root.querySelector<HTMLDetailsElement>('.editor-insert-menu'); if (inserter) inserter.open = true; root.querySelector<HTMLButtonElement>('[data-block-add]')?.focus(); } }
  }
  toolbar.querySelectorAll<HTMLButtonElement>('[data-composer-mode]').forEach(button => button.addEventListener('click', () => { setMode(button.dataset.composerMode || 'source', true); say(''); }));
  root.querySelectorAll<HTMLButtonElement>('[data-block-add]').forEach(button => button.addEventListener('click', () => {
    const kind = button.dataset.blockAdd;
    if (kind !== 'paragraph' && kind !== 'h2' && kind !== 'h3' && kind !== 'quote' && kind !== 'code' && kind !== 'callout' && kind !== 'list' && kind !== 'table' && kind !== 'html') return;
    const block: ComposerBlock = {kind, id: nextID(), text: '', prefix: '\n', dirty: true};
    if (block.kind === 'table') block.rows = [['项目', '说明'], ['', ''], ['', '']];
    if (block.kind === 'callout') block.tone = 'note';
    const blocks = [...state.blocks], index = blocks.findIndex(item => item.id === active);
    blocks.splice(index < 0 ? blocks.length : index + 1, 0, block);
    apply({...state, blocks}, block.id, `已添加${labels[block.kind]}`);
  }));
  function restore(direction: 'undo' | 'redo') {
    if (readOnly()) return;
    const value = history[direction](source.value); if (value === null) return;
    state = parse(value); emit(value, '', false); render(); if (mode === 'blocks' && state.blocks.length) focusBlock(state.blocks[0]!.id); say(direction === 'undo' ? '已撤销' : '已重做');
  }
  undo.addEventListener('click', () => restore('undo')); redo.addEventListener('click', () => restore('redo'));
  source.addEventListener('input', () => {
    if (syncing) return;
    if (source.value !== previous) history.record(previous, 'source');
    previous = source.value; state = parse(source.value); if (mode === 'blocks') render(); updateControls();
  });
  source.addEventListener('invalid', () => { setMode('source'); say('请检查正文长度后再保存，当前内容已保留'); });
  // Both media insertion and content reuse capture the exact original draft.
  // In block mode the insertion point is after the focused block, never inside its HTML.
  function capture() {
    let start = source.selectionStart, end = source.selectionEnd;
    if (mode === 'blocks') {
      const index = state.blocks.findIndex(block => block.id === active);
      start = index < 0 ? source.value.length - state.tail.length : serialize({blocks: state.blocks.slice(0, index + 1), tail: ''}).length;
      end = start;
    }
    return {start, end, value: source.value};
  }
  function insert(markup: string, selection: EditorSourceSelection) {
    if (readOnly()) throw new Error('当前内容不可编辑');
    const value = replacement(source.value, markup, selection);
    if (bytes(value) > sourceLimit) throw new Error(sizeError);
    const next = parse(value);
    if (mode === 'blocks' && next.blocks.length > MAX_BLOCKS) throw new Error(`内容块最多 ${MAX_BLOCKS} 个`);
    state = next; emit(value); render();
    queueMicrotask(() => {
      if (mode === 'source') { source.focus(); source.setSelectionRange(selection.start + markup.length, selection.start + markup.length); return; }
      let offset = 0, target = state.blocks.at(-1)?.id;
      for (const block of state.blocks) { offset += serialize({blocks: [block], tail: ''}).length; if (offset >= selection.start + markup.length) { target = block.id; break; } }
      if (target) { activate(target); focusBlock(target); }
    });
    say('内容已插入，保存后生效');
  }
  function reset(value = '') {
    source.value = previous = value; active = ''; history.past = []; history.future = []; history.group = ''; history.at = 0;
    source.setCustomValidity(''); state = parse(value); say('');
    setMode(!value || state.blocks.some(block => block.kind !== 'html') ? 'blocks' : 'source'); updateControls();
  }
  window.ChunnelBlockEditor = {reset, capture, insert, parse, serialize, renderBlock, localPostURL, localImageURL};
  toolbar.hidden = false;
  setMode(!source.value || state.blocks.some(block => block.kind !== 'html') ? 'blocks' : 'source');
})();
