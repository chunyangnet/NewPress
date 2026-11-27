(() => {
  'use strict';
  // Candidate navigation belongs to the IME, including engines that omit the
  // keyboard flag during composition. Weak keys also cover dynamic editors.
  const composing = new WeakSet();
  const isComposing = event => event.isComposing || event.keyCode === 229 || composing.has(event.target);
  document.addEventListener('compositionstart', event => composing.add(event.target), true);
  document.addEventListener('compositionend', event => composing.delete(event.target), true);
  document.addEventListener('focusout', event => composing.delete(event.target), true);
  const scriptURL = document.currentScript.src;
  const assetRoot = new URL('../images/emoji/', scriptURL);
  // Owner-supplied GPT sheets; provenance and rights live beside the assets.
  // BEGIN GENERATED EMOJI CATALOG
  const packs = [
    {
      "id": "daily",
      "name": "日常",
      "prefix": "",
      "names": [
        "得意",
        "笑哭",
        "星星眼",
        "点赞",
        "OK",
        "喜欢",
        "大哭",
        "生气",
        "尴尬",
        "震惊",
        "思考",
        "酷",
        "叹气",
        "睡觉",
        "吃饭",
        "奶茶",
        "打call",
        "庆祝",
        "疑惑",
        "灵感",
        "发抖",
        "无语",
        "捂脸",
        "晕",
        "给心心",
        "加油",
        "敬礼",
        "感动",
        "喝茶",
        "躺平"
      ]
    },
    {
      "id": "whale",
      "name": "鲸伴",
      "prefix": "鲸伴",
      "names": [
        "开心",
        "抱抱",
        "满足",
        "点赞",
        "期待",
        "比心",
        "疑问",
        "思考",
        "灵感",
        "震惊",
        "汗颜",
        "大哭",
        "生气",
        "哼",
        "无语",
        "酷",
        "喝茶",
        "吃饼",
        "晚安",
        "工作",
        "写代码",
        "读书",
        "分析",
        "庆祝",
        "招手",
        "探头",
        "开箱",
        "躲藏",
        "施法",
        "自闭"
      ]
    }
  ];
  // END GENERATED EMOJI CATALOG
  const catalog = new Map(packs.flatMap(pack => pack.names.map((name, index) => [
    `[${pack.prefix}${name}]`, { name, pack: pack.id, label: `${pack.name} · ${name}`,
      file: `${pack.id}-${String(index + 1).padStart(2, '0')}.png` },
  ])));
  const inputSelector = 'textarea[data-emoji-input], textarea[data-comment-content], textarea[data-community-content], textarea[data-comment-edit-field="content_source"], textarea[data-comment-reply-source]';
  const renderSelector = '[data-emoji-render], .comment-copy, .prose, .comment-main > p, .discussion-card > p';
  const ranges = new WeakMap();
  const enhanced = new WeakSet();
  const editors = new WeakMap();
  let target = null;
  let trigger = null;
  let serial = 0;
  let recents = [];
  try { recents = [...new Set(JSON.parse(localStorage.getItem('newpress.emoji.recent.v1') || '[]'))].filter((token) => catalog.has(token)).slice(0, 8); } catch { /* Optional preference storage. */ }

  const element = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text) node.textContent = text;
    return node;
  };
  const image = (token) => {
    const emoji = catalog.get(token);
    const img = element('img', 'newpress-emoji');
    img.src = new URL(emoji.file, assetRoot).href;
    img.alt = token;
    img.title = `${emoji.label} · AI 生成${emoji.pack === 'whale' ? ' · 非官方二创' : ''}`;
    img.setAttribute('aria-label', img.title);
    img.dataset.aiGenerated = 'true';
    img.dataset.emojiPack = emoji.pack;
    img.width = 24;
    img.height = 24;
    img.decoding = 'async';
    img.draggable = false;
    img.addEventListener('error', () => img.replaceWith(document.createTextNode(token)), { once: true });
    return img;
  };

  function fragment(text) {
    const result = document.createDocumentFragment();
    let start = 0;
    for (const match of String(text).matchAll(/\[[^\[\]\n]{1,16}\]/g)) {
      if (!catalog.has(match[0])) continue;
      result.append(document.createTextNode(text.slice(start, match.index)), image(match[0]));
      start = match.index + match[0].length;
    }
    result.append(document.createTextNode(String(text).slice(start)));
    return result;
  }

  function render(root = document) {
    const containers = [...root.querySelectorAll(renderSelector)];
    if (root instanceof Element && root.matches(renderSelector)) containers.unshift(root);
    containers.forEach((container) => {
      const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT, {
        acceptNode: (node) => node.textContent.includes('[') && !node.parentElement.closest('script, style, textarea, pre, code, [data-no-emoji]') ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT,
      });
      const nodes = [];
      while (walker.nextNode()) nodes.push(walker.currentNode);
      nodes.forEach((node) => {
        if ([...node.textContent.matchAll(/\[[^\[\]\n]{1,16}\]/g)].some((match) => catalog.has(match[0]))) node.replaceWith(fragment(node.textContent));
      });
    });
  }

  const panel = document.querySelector('[data-emoji-popover]') || element('div', 'emoji-popover');
  panel.dataset.emojiPopover = '';
  panel.id = 'chunnel-emoji-picker';
  panel.hidden = true;
  panel.setAttribute('role', 'dialog');
  panel.setAttribute('aria-label', 'NewPress 表情');
  if ('showPopover' in panel) panel.setAttribute('popover', 'manual');
  if (!panel.isConnected) document.body.append(panel);
  const head = element('div', 'emoji-popover-head');
  head.append(element('b', '', '表情'), element('span', 'emoji-ai-label', 'AI 生成'));
  const closeButton = element('button', '', '×');
  closeButton.type = 'button';
  closeButton.setAttribute('aria-label', '关闭表情面板');
  head.append(closeButton);
  const search = element('input', 'emoji-search');
  search.type = 'search';
  search.placeholder = '搜索表情';
  search.setAttribute('aria-label', '搜索全部表情');
  search.maxLength = 20;
  let activePack = packs[0].id;
  const categories = element('div', 'emoji-categories');
  categories.setAttribute('role', 'group');
  categories.setAttribute('aria-label', '表情分组');
  const categoryButtons = packs.map(pack => {
    const button = element('button', 'emoji-category', pack.name);
    button.type = 'button';
    button.dataset.emojiCategory = pack.id;
    button.setAttribute('aria-pressed', String(pack.id === activePack));
    button.addEventListener('click', () => {
      activePack = pack.id;
      search.value = '';
      populate();
      position();
    });
    categories.append(button);
    return button;
  });
  const recentSection = element('div', 'emoji-recent');
  const recentGrid = element('div', 'emoji-grid');
  recentSection.append(element('small', '', '最近使用'), recentGrid);
  const grid = element('div', 'emoji-grid');
  grid.dataset.emojiGrid = '';
  grid.setAttribute('aria-label', '表情列表');
  const status = element('p', 'emoji-picker-status');
  status.setAttribute('role', 'status');
  const notice = element('p', 'emoji-pack-notice');
  const info = element('details', 'emoji-info');
  const infoTrigger = element('summary', 'emoji-info-trigger');
  infoTrigger.setAttribute('aria-label', '表情说明');
  infoTrigger.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4m0-4h.01"/></svg>';
  const infoBubble = element('div', 'emoji-info-bubble');
  infoBubble.append(notice, element('p', '', '点击插入，可连续选择。'));
  info.append(infoTrigger, infoBubble);
  head.insertBefore(info, closeButton);
  info.addEventListener('pointerenter', event => { if (event.pointerType === 'mouse') info.open = true; });
  info.addEventListener('pointerleave', () => { if (!info.contains(document.activeElement)) info.open = false; });
  info.addEventListener('focusout', event => { if (!info.contains(event.relatedTarget)) info.open = false; });
  panel.replaceChildren(head, search, categories, recentSection, grid, status);

  function emojiButton(token) {
    const button = element('button', 'emoji-choice');
    button.type = 'button';
    button.dataset.emojiToken = token;
    button.title = catalog.get(token).label;
    button.setAttribute('aria-label', `插入${token}`);
    button.append(image(token));
    return button;
  }
  function populate() {
    info.open = false;
    const query = search.value.trim().toLocaleLowerCase();
    const tokens = [...catalog].filter(([token, item]) => query
      ? `${token} ${item.label}`.toLocaleLowerCase().includes(query)
      : item.pack === activePack).map(([token]) => token);
    categoryButtons.forEach(button => button.setAttribute('aria-pressed', String(!query && button.dataset.emojiCategory === activePack)));
    grid.setAttribute('aria-label', query ? `搜索结果：${tokens.length} 个表情` : `${packs.find(pack => pack.id === activePack).name}表情`);
    notice.textContent = query || activePack === 'whale' ? 'GPT 生成，鲸伴为非官方二创。' : 'GPT 生成的日常表情。';
    grid.replaceChildren(...tokens.map(emojiButton));
    if (!tokens.length) grid.append(element('p', 'emoji-no-results', '没有找到，试试「笑」或「鲸伴」'));
    recentSection.hidden = !!query || !recents.length;
    recentGrid.replaceChildren(...recents.map(emojiButton));
  }

  let ownerObserver = null;
  let ownerTransitions = null;
  function available(input = target, button = trigger) {
    return input?.isConnected && button?.isConnected && !input.matches(':disabled') && !input.readOnly &&
      !button.matches(':disabled') && !button.closest('[inert]') && button.getClientRects().length > 0 &&
      getComputedStyle(button).visibility === 'visible';
  }
  function watchOwner() {
    // Watch only the active controls and their ancestors, never the document
    // subtree. Hidden/replaced composers must not leave a top-layer picker.
    ownerObserver = new MutationObserver(() => { if (!available()) close(); });
    const watched = new Set();
    for (const start of [target, trigger]) {
      for (let node = start; node && !watched.has(node); node = node.parentElement) {
        watched.add(node);
        ownerObserver.observe(node, { childList: true, attributes: true,
          attributeFilter: ['class', 'style', 'hidden', 'inert', 'open', 'disabled', 'readonly'] });
      }
    }
    // Discrete visibility/display transitions can stay visible until they end.
    ownerTransitions = event => {
      if (watched.has(event.target) && ['visibility', 'display'].includes(event.propertyName) && !available()) close();
    };
    document.addEventListener('transitionend', ownerTransitions, true);
    document.addEventListener('transitioncancel', ownerTransitions, true);
  }
  function position() {
    if (panel.hidden) return;
    if (!available()) { close(); return; }
    const viewport = window.visualViewport;
    const leftEdge = viewport?.offsetLeft || 0;
    const topEdge = viewport?.offsetTop || 0;
    const width = viewport?.width || innerWidth;
    const height = viewport?.height || innerHeight;
    const rect = trigger.getBoundingClientRect();
    const panelWidth = Math.min(344, width - 24);
    panel.style.width = `${panelWidth}px`;
    panel.style.maxHeight = `${Math.max(140, height - 24)}px`;
    const panelHeight = Math.min(panel.scrollHeight, height - 24);
    const above = rect.top - panelHeight - 8;
    const top = above >= topEdge + 12 ? above : Math.min(rect.bottom + 8, topEdge + height - panelHeight - 12);
    panel.style.left = `${Math.max(leftEdge + 12, Math.min(rect.left, leftEdge + width - panelWidth - 12))}px`;
    panel.style.top = `${Math.max(topEdge + 12, top)}px`;
  }
  function close(restore = false) {
    if (panel.hidden) return;
    ownerObserver?.disconnect();
    ownerObserver = null;
    document.removeEventListener('transitionend', ownerTransitions, true);
    document.removeEventListener('transitioncancel', ownerTransitions, true);
    ownerTransitions = null;
    if (panel.hidePopover && panel.matches(':popover-open')) panel.hidePopover();
    panel.hidden = true;
    trigger?.setAttribute('aria-expanded', 'false');
    if (restore) (target?.isConnected ? target : trigger)?.focus({ preventScroll: true });
    target = null;
    trigger = null;
  }
  function remember(input) {
    ranges.set(input, editors.get(input)?.selection() || { start: input.selectionStart, end: input.selectionEnd });
  }
  function lengthError(input, value) {
    const bytes = Number(input.dataset.emojiMaxBytes);
    if (bytes > 0) return new TextEncoder().encode(value.trim()).length > bytes ? '正文内容超过长度上限，请删减后重试' : '';
    const maximum = Number(input.dataset.emojiMax || input.maxLength);
    return maximum > 0 && [...value].length > maximum ? `最多输入 ${maximum} 个字符` : '';
  }
  function validateReplacement(input, text, range = { start: input.selectionStart, end: input.selectionEnd }) {
    if (!input.isConnected || input.disabled || input.readOnly) return '当前内容不可编辑';
    const candidate = input.value.slice(0, range.start) + text + input.value.slice(range.end);
    return lengthError(input, candidate);
  }
  function replaceSelection(input, text, range = { start: input.selectionStart, end: input.selectionEnd }) {
    if (editors.has(input)) {
      const error = editors.get(input).replace(text, range);
      remember(input); return error;
    }
    const error = validateReplacement(input, text, range);
    if (error) return error;
    input.focus({ preventScroll: true });
    input.setSelectionRange(range.start, range.end);
    // Native insertion preserves the textarea's undo history where supported.
    let inserted = false;
    try { if (document.activeElement === input) inserted = document.execCommand('insertText', false, text); } catch { /* Use the range API below. */ }
    if (!inserted) input.setRangeText(text, range.start, range.end, 'end');
    remember(input);
    input.dispatchEvent(new Event('input', { bubbles: true }));
    return '';
  }
  function open(button, input, keyboard) {
    if (!available(input, button)) return;
    if (editors.has(input)) remember(input);
    if (trigger === button && !panel.hidden) { close(true); return; }
    close();
    target = input;
    trigger = button;
    if (!ranges.has(input)) ranges.set(input, { start: input.value.length, end: input.value.length });
    (input.closest('dialog') || document.body).append(panel);
    search.value = '';
    populate();
    status.textContent = '';
    panel.hidden = false;
    if (panel.showPopover) panel.showPopover();
    button.setAttribute('aria-controls', panel.id);
    button.setAttribute('aria-expanded', 'true');
    watchOwner();
    position();
    if (keyboard && !panel.hidden) search.focus({ preventScroll: true });
  }
  function insert(token) {
    if (!target?.isConnected || target.disabled || target.readOnly) { close(); return; }
    // The panel can stay open while the user moves the rich editor's caret.
    // Its live selection is newer than the range captured when opening it.
    const range = editors.get(target)?.selection() || ranges.get(target) || { start: target.value.length, end: target.value.length };
    const error = replaceSelection(target, token, range);
    if (error) { status.textContent = error; return; }
    recents = [token, ...recents.filter((item) => item !== token)].slice(0, 8);
    try { localStorage.setItem('newpress.emoji.recent.v1', JSON.stringify(recents)); } catch { /* In-memory selection remains usable. */ }
    const recentFocus = recentGrid.contains(document.activeElement) ? document.activeElement.dataset.emojiToken : null;
    recentSection.hidden = !!search.value.trim();
    recentGrid.replaceChildren(...recents.map(emojiButton));
    if (recentFocus) [...recentGrid.children].find(button => button.dataset.emojiToken === recentFocus)?.focus({ preventScroll: true });
    status.textContent = `已插入${token}`;
    position();
  }
  function enhance(root = document) {
    const inputs = [...root.querySelectorAll(inputSelector)];
    if (root instanceof Element && root.matches(inputSelector)) inputs.unshift(root);
    inputs.forEach((input) => {
      if (enhanced.has(input) || input.hasAttribute('data-emoji-disabled')) return;
      enhanced.add(input);
      input.id ||= `emoji-input-${++serial}`;
      input.dataset.emojiInput = '';
      if (!input.dataset.emojiMax && !input.dataset.emojiMaxBytes && input.maxLength > 0) {
        input.dataset.emojiMax = String(input.maxLength);
        // HTML maxlength counts UTF-16 units; our APIs count Unicode characters.
        input.maxLength *= 2;
      }
      if (window.ChunnelEmojiEditor) editors.set(input, window.ChunnelEmojiEditor(input, { fragment, validate: lengthError }));
      const update = () => {
        input.setCustomValidity(lengthError(input, input.value));
        remember(input);
      };
      input.addEventListener('input', update);
      input.addEventListener('select', () => remember(input));
      input.addEventListener('keyup', () => remember(input));
      input.addEventListener('pointerup', () => remember(input));
      input.addEventListener('blur', () => remember(input));
      const existing = [...document.querySelectorAll('[data-emoji-trigger]')].find((button) => button.dataset.emojiTarget === input.id);
      if (!existing) {
        const toolbar = element('div', 'emoji-input-tools');
        const button = element('button', 'emoji-trigger', '表情');
        button.type = 'button';
        button.dataset.emojiTrigger = '';
        button.dataset.emojiTarget = input.id;
        button.disabled = input.disabled || input.readOnly;
        button.setAttribute('aria-label', '添加表情');
        button.setAttribute('aria-haspopup', 'dialog');
        button.setAttribute('aria-expanded', 'false');
        const sample = image('[得意]');
        sample.alt = '';
        button.prepend(sample);
        toolbar.append(button);
        input.insertAdjacentElement('afterend', toolbar);
      }
      update();
    });
    render(root);
  }

  document.addEventListener('click', (event) => {
    const button = event.target.closest?.('[data-emoji-trigger]');
    if (!button) return;
    event.preventDefault();
    open(button, document.getElementById(button.dataset.emojiTarget), event.detail === 0);
  });
  panel.addEventListener('pointerdown', (event) => {
    if (!info.contains(event.target)) info.open = false;
    if (event.target.closest('[data-emoji-token]')) event.preventDefault();
  });
  panel.addEventListener('click', (event) => { const button = event.target.closest('[data-emoji-token]'); if (button) insert(button.dataset.emojiToken); });
  panel.addEventListener('keydown', (event) => {
    if (isComposing(event)) return;
    const button = event.target.closest('[data-emoji-token]');
    if (!button) return;
    const buttons = [...button.parentElement.querySelectorAll('button')];
    const index = buttons.indexOf(button);
    const cols = getComputedStyle(button.parentElement).gridTemplateColumns.split(' ').length;
    const next = { ArrowRight: index + 1, ArrowLeft: index - 1, ArrowDown: index + cols, ArrowUp: index - cols, Home: 0, End: buttons.length - 1 }[event.key];
    if (next !== undefined) { event.preventDefault(); buttons[(next + buttons.length) % buttons.length]?.focus(); }
  });
  search.addEventListener('input', event => { if (!isComposing(event)) { populate(); position(); } });
  search.addEventListener('compositionend', () => { populate(); position(); });
  search.addEventListener('keydown', (event) => { if (event.key === 'ArrowDown' && !isComposing(event)) { event.preventDefault(); grid.querySelector('button')?.focus(); } });
  closeButton.addEventListener('click', () => close(true));
  document.addEventListener('pointerdown', (event) => { if (!panel.hidden && !panel.contains(event.target) && !trigger?.contains(event.target)) close(); }, true);
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && !panel.hidden && !isComposing(event)) { event.preventDefault(); event.stopImmediatePropagation(); close(true); }
  }, true);
  document.addEventListener('chunnel:overlay', () => close());
  document.addEventListener('selectionchange', () => { if (document.activeElement?.matches(inputSelector)) remember(document.activeElement); });
  document.addEventListener('copy', (event) => {
    const selection = getSelection();
    if (!selection?.rangeCount || selection.isCollapsed || !event.clipboardData) return;
    const copy = selection.getRangeAt(0).cloneContents();
    if (!copy.querySelector('.newpress-emoji')) return;
    copy.querySelectorAll('.newpress-emoji').forEach((img) => img.replaceWith(document.createTextNode(img.alt)));
    const box = element('div'); box.append(copy);
    event.clipboardData.setData('text/plain', box.textContent);
    event.preventDefault();
  });
  window.addEventListener('resize', position);
  window.visualViewport?.addEventListener('resize', position);
  window.visualViewport?.addEventListener('scroll', position);
  document.addEventListener('scroll', (event) => { if (!panel.contains(event.target)) position(); }, true);
  enhance();
  window.ChunnelEmoji = { enhance, render, fragment, close, replaceSelection, validateReplacement };
})();
