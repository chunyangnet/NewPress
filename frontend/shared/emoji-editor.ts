import type { EmojiRange } from './emoji-types';
type Snapshot = EmojiRange & {value: string};
(() => {
  'use strict';
  const selections = new WeakMap<Element, () => EmojiRange>();
  document.addEventListener('selectionchange', () => { if (document.activeElement) selections.get(document.activeElement)?.(); });
  // The textarea remains the canonical plain-text form control. Only the editing
  // surface is rich; HTML from paste/drop is never accepted or submitted.
  window.ChunnelEmojiEditor = (input, { fragment, validate }) => {
    const editor = document.createElement('div');
    editor.className = 'emoji-editor';
    editor.id = `${input.id}-visual`;
    editor.dataset.emojiEditor = input.id;
    editor.setAttribute('role', 'textbox');
    editor.setAttribute('aria-multiline', 'true');
    editor.setAttribute('aria-label', input.getAttribute('aria-label') || input.labels?.[0]?.textContent.trim().slice(0, 120) || input.placeholder || '输入内容');
    editor.style.setProperty('--emoji-editor-rows', String(Math.max(2, Math.min(8, input.rows))));
    const error = document.createElement('p');
    error.className = 'emoji-editor-error'; error.id = `${input.id}-error`; error.hidden = true; error.setAttribute('role', 'alert');
    editor.setAttribute('aria-describedby', [input.getAttribute('aria-describedby'), error.id].filter(Boolean).join(' '));
    input.before(editor); input.after(error);
    input.classList.add('emoji-source'); input.tabIndex = -1; input.setAttribute('aria-hidden', 'true');
    const valueProperty = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value');
    const nativeSelection = input.setSelectionRange.bind(input);
    if (!valueProperty?.get || !valueProperty.set) throw new Error('Textarea value accessor unavailable');
    const readValue = (): string => valueProperty.get?.call(input) ?? '';
    const writeValue = (value: string) => valueProperty.set?.call(input, value);
    let composing = false, syncing = false, currentRange: EmojiRange = { start: 0, end: 0, direction: 'none' }, history: Snapshot[] = [], historyIndex = -1;
    let pendingEdit: Snapshot | null = null, lastEdit: {kind: string; time: number; end: number} | null = null;
    let emojiPointer: {image: HTMLImageElement; id: number; x: number; y: number; time: number; range: EmojiRange; moved?: boolean} | null = null;
    const graphemes = typeof Intl.Segmenter === 'function' ? new Intl.Segmenter(undefined, { granularity: 'grapheme' }) : null;

    function plain(root: Node): string {
      if (root.childNodes.length === 1 && root.firstChild?.nodeName === 'BR') return '';
      let text = '', hasLine = false, previousBlock = false;
      for (const child of root.childNodes) {
        let value, block = false;
        if (child instanceof Text) value = child.data;
        else {
          if (!(child instanceof HTMLElement) || child.matches('script, style')) continue;
          // Native multiline insertion can move our caret stop to the first
          // empty line. It contributes no text, but the next block starts a
          // new line. Empty text nodes must not create such a line themselves.
          if (child.matches('[data-emoji-tail]')) { hasLine = true; continue; }
          block = ['DIV', 'P', 'LI'].includes(child.tagName);
          value = child.matches('img[data-emoji-value]') ? child.dataset.emojiValue : child.tagName === 'BR' ? '\n' : plain(child);
        }
        if (!value && !block) continue;
        // Count block boundaries, including empty blocks, rather than testing
        // whether accumulated text is nonempty or already ends in a newline.
        if (hasLine && (block || previousBlock)) text += '\n';
        text += value; hasLine = true; previousBlock = block;
      }
      return text;
    }
    function selection() {
      const selected = getSelection();
      if (!selected?.rangeCount) return currentRange;
      const range = selected.getRangeAt(0);
      if (!editor.contains(range.startContainer) || !editor.contains(range.endContainer)) return currentRange;
      const prefix = document.createRange(); prefix.selectNodeContents(editor);
      prefix.setEnd(range.startContainer, range.startOffset);
      const start = plain(prefix.cloneContents()).length;
      prefix.setEnd(range.endContainer, range.endOffset);
      const end = plain(prefix.cloneContents()).length;
      const backward = !range.collapsed && selected.anchorNode === range.endContainer && selected.anchorOffset === range.endOffset;
      currentRange = { start, end, direction: range.collapsed ? 'none' : backward ? 'backward' : 'forward' };
      nativeSelection(start, end, currentRange.direction);
      return currentRange;
    }
    function point(offset: number, end: boolean): [Node, number] {
      let remaining = Math.max(0, offset);
      for (let i = 0; i < editor.childNodes.length; i++) {
        const child = editor.childNodes[i];
        if (child instanceof Text) {
          if (remaining <= child.length) return [child, remaining];
          remaining -= child.length;
        } else if (child instanceof HTMLElement && child.dataset.emojiValue) {
          const length = child.dataset.emojiValue.length;
          if (remaining === 0) return [editor, i];
          if (remaining < length) return [editor, i + (end ? 1 : 0)];
          remaining -= length;
        }
      }
      return [editor, Math.max(0, editor.childNodes.length - 1)];
    }
    function select(start: number, end = start, direction: EmojiRange['direction'] = 'none') {
      nativeSelection(start, end, direction);
      start = input.selectionStart; end = input.selectionEnd;
      currentRange = { start, end, direction };
      const range = document.createRange(); range.setStart(...point(start, false)); range.setEnd(...point(end, true));
      const selected = getSelection(); if (!selected) return; selected.removeAllRanges(); selected.addRange(range);
      if (direction === 'backward' && !range.collapsed) selected.setBaseAndExtent(range.endContainer, range.endOffset, range.startContainer, range.startOffset);
    }
    function focus(options?: FocusOptions) {
      if (document.activeElement === editor) return;
      const range = { ...currentRange };
      editor.focus(options);
      select(range.start, range.end, range.direction);
    }
    function contents(value: string) {
      const nodes = fragment(value);
      nodes.querySelectorAll<HTMLImageElement>('img.newpress-emoji').forEach(img => {
        img.dataset.emojiValue = img.alt; img.contentEditable = 'false';
      });
      return nodes;
    }
    function appendTail() {
      // A trailing caret stop makes empty editors and final newlines editable.
      const tail = document.createElement('br'); tail.dataset.emojiTail = ''; editor.append(tail);
    }
    function paint(value: string, range?: EmojiRange | null) {
      const top = editor.scrollTop;
      editor.replaceChildren(contents(value)); appendTail();
      editor.dataset.empty = String(!value);
      if (range) select(range.start, range.end, range.direction);
      editor.scrollTop = top;
    }
    function normalize(value: string, range: EmojiRange) {
      // Ordinary typing must leave the browser's text node, caret and existing
      // images in place, especially next to an emoji or during IME composition.
      const flat = [...editor.childNodes].every(child => child instanceof Text || child instanceof Element && child.matches('img[data-emoji-value], br[data-emoji-tail]'));
      if (!flat) { paint(value, range); return; }
      editor.normalize();
      let changed = false;
      for (const child of [...editor.childNodes]) {
        if (!(child instanceof Text) || !child.data.includes('[') || !child.data.includes(']')) continue;
        const nodes = contents(child.data);
        if (nodes.querySelector('img[data-emoji-value]')) { child.replaceWith(nodes); changed = true; }
      }
      if (!(editor.lastChild instanceof Element && editor.lastChild.matches('[data-emoji-tail]'))) appendTail();
      if (changed) select(range.start, range.end, range.direction);
    }
    function revealCaret() {
      const selected = getSelection();
      if (document.activeElement !== editor || !selected?.isCollapsed || !selected.rangeCount) return;
      const range = selected.getRangeAt(0);
      if (!editor.contains(range.startContainer)) return;
      const caret = range.getClientRects()[0], box = editor.getBoundingClientRect();
      if (!caret?.height) return;
      if (caret.bottom > box.bottom - 12) editor.scrollTop += caret.bottom - box.bottom + 12;
      else if (caret.top < box.top + 12) editor.scrollTop -= box.top + 12 - caret.top;
    }
    function snapshot(): Snapshot { return { value: readValue(), ...selection() }; }
    function record(before: Snapshot, kind = '') {
      const state = snapshot();
      if (before.value === state.value) return;
      const now = Date.now();
      const merge = kind && lastEdit?.kind === kind && now - lastEdit.time < 1000 && before.value === history[historyIndex]?.value && before.start === before.end && before.start === lastEdit.end && state.start === state.end;
      history = history.slice(0, historyIndex + 1);
      if (merge) history[historyIndex] = state;
      else { history[historyIndex] = before; history.push(state); }
      while (history.length > 60 || (history.length > 2 && history.reduce((sum, item) => sum + item.value.length, 0) > 250000)) history.shift();
      historyIndex = history.length - 1;
      lastEdit = kind ? { kind, time: now, end: state.end } : null;
    }
    function feedback(message: string) {
      error.textContent = message; error.hidden = !message;
      editor.setAttribute('aria-invalid', String(!!message));
    }
    function emit() {
      syncing = true; input.dispatchEvent(new Event('input', { bubbles: true })); syncing = false;
      editor.dataset.empty = String(!input.value);
      feedback(validate(input, input.value));
    }
    function update(event?: Event) {
      const before = pendingEdit || history[historyIndex] || snapshot();
      const range = selection(); const value = plain(editor);
      writeValue(value); nativeSelection(range.start, range.end, range.direction);
      if (!composing) normalize(value, range);
      emit();
      if (!composing) {
        pendingEdit = null;
        record(before, event instanceof InputEvent && event.inputType === 'insertText' ? 'typing' : '');
      }
    }
    function replace(text: string, range = selection(), kind = '') {
      if (input.matches(':disabled') || input.readOnly) return '当前内容不可编辑';
      if (composing) return '请先完成当前输入';
      const value = input.value.slice(0, range.start) + text + input.value.slice(range.end);
      const message = validate(input, value);
      if (message && value.length >= input.value.length) { feedback(message); return message; }
      const before = snapshot();
      pendingEdit = null;
      editor.focus({ preventScroll: true });
      const change = document.createRange();
      change.setStart(...point(range.start, false)); change.setEnd(...point(range.end, true));
      change.deleteContents();
      if (text) change.insertNode(contents(text));
      writeValue(value);
      const caret = { start: range.start + text.length, end: range.start + text.length };
      normalize(value, caret); select(caret.start); revealCaret();
      emit(); record(before, kind); return '';
    }
    function undo(direction: number) {
      if (input.matches(':disabled') || input.readOnly) return;
      const next = historyIndex + direction;
      if (next < 0 || next >= history.length) return;
      lastEdit = null; pendingEdit = null;
      historyIndex = next; const state = history[next]; if (!state) return;
      writeValue(state.value); paint(state.value, state); revealCaret(); emit();
    }
    function erase(backward: boolean) {
      const range = { ...selection() };
      if (range.start === range.end) {
        const value = input.value, caret = range.start;
        if (backward ? caret === 0 : caret === value.length) return;
        // Native contenteditable deletion may consume both a newline and the
        // following non-editable emoji. Delete one canonical atom instead.
        let offset = 0, atom;
        for (const child of editor.childNodes) {
          const token = child instanceof HTMLElement ? child.dataset.emojiValue : undefined;
          const length = child instanceof Text ? child.length : token?.length || 0;
          if (token && (backward ? offset < caret && caret <= offset + length : offset <= caret && caret < offset + length)) atom = { start: offset, end: offset + length };
          offset += length;
        }
        if (atom) Object.assign(range, atom);
        else {
          let index = 0;
          const segments = graphemes ? graphemes.segment(value) : Array.from(value, segment => { const part = { segment, index }; index += segment.length; return part; });
          for (const part of segments) {
            const end = part.index + part.segment.length;
            if (backward ? part.index < caret && caret <= end : part.index <= caret && caret < end) { range.start = part.index; range.end = end; break; }
          }
        }
      }
      replace('', range, backward ? 'deleteBackward' : 'deleteForward');
    }
    function resetHistory() { currentRange = { start: input.value.length, end: input.value.length, direction: 'none' }; nativeSelection(currentRange.start, currentRange.end); history = [{ value: input.value, ...currentRange }]; historyIndex = 0; lastEdit = null; pendingEdit = null; }
    function syncAttributes() {
      const disabled = input.matches(':disabled');
      editor.contentEditable = String(!disabled && !input.readOnly);
      editor.tabIndex = disabled ? -1 : 0;
      editor.setAttribute('aria-disabled', String(disabled));
      editor.setAttribute('aria-readonly', String(input.readOnly));
      editor.setAttribute('aria-required', String(input.required));
      editor.dataset.placeholder = input.placeholder;
    }
    Object.defineProperty(input, 'value', {
      configurable: true,
      get() { return readValue(); },
      set(value: string) {
        writeValue(value);
        const current = readValue();
        if (current !== plain(editor)) { paint(current, document.activeElement === editor ? { start: current.length, end: current.length } : null); feedback(''); resetHistory(); }
      },
    });
    input.focus = focus;
    input.setSelectionRange = (start, end = start, direction = 'none') => { lastEdit = null; select(start ?? 0, end ?? 0, direction); };
    input.addEventListener('focus', () => focus({ preventScroll: true }));
    input.addEventListener('input', () => {
      if (!syncing && !composing && input.value !== plain(editor)) { paint(input.value); resetHistory(); }
    });
    input.addEventListener('invalid', (event) => { event.preventDefault(); feedback(input.validationMessage); editor.focus(); });
    input.form?.addEventListener('reset', () => setTimeout(() => {
      paint(input.value); currentRange = { start: 0, end: 0 }; resetHistory(); emit();
    }, 0));
    editor.addEventListener('input', update);
    // A click inside an implicit label would also activate its hidden textarea.
    // The browser places the caret on pointerdown; cancel only label activation
    // on click so mouse/touch positioning and drag selection stay native.
    editor.addEventListener('click', (event) => {
      if ([...input.labels].some(label => label.contains(editor))) event.preventDefault();
      const pointer = emojiPointer; emojiPointer = null;
      if (!pointer || pointer.moved || composing || event.timeStamp - pointer.time > 500 || event.detail > 1 || event.ctrlKey || event.metaKey || event.altKey || !pointer.image.isConnected || input.matches(':disabled')) return;
      const rect = pointer.image.getBoundingClientRect();
      const prefix = document.createRange(); prefix.selectNodeContents(editor); prefix.setEndBefore(pointer.image);
      const after = (event.clientX >= rect.left + rect.width / 2) !== (getComputedStyle(pointer.image).direction === 'rtl');
      const offset = plain(prefix.cloneContents()).length + (after ? (pointer.image.dataset.emojiValue || '').length : 0);
      editor.focus({ preventScroll: true });
      if (event.shiftKey) {
        const anchor = pointer.range.direction === 'backward' ? pointer.range.end : pointer.range.start;
        select(Math.min(anchor, offset), Math.max(anchor, offset), offset < anchor ? 'backward' : 'forward');
      } else select(offset);
    });
    editor.addEventListener('compositionstart', () => { pendingEdit = snapshot(); lastEdit = null; composing = true; });
    editor.addEventListener('compositionend', () => { composing = false; update(); });
    editor.addEventListener('beforeinput', (event) => {
      if (composing || event.isComposing || !event.cancelable) return;
      if (event.inputType === 'insertText' && /[\r\n]/.test(event.data || '')) {
        event.preventDefault(); replace((event.data || '').replace(/\r\n?/g, '\n')); return;
      }
      if (event.inputType === 'deleteContentBackward' || event.inputType === 'deleteContentForward') { event.preventDefault(); erase(event.inputType === 'deleteContentBackward'); return; }
      if (event.inputType === 'insertParagraph' || event.inputType === 'insertLineBreak') { event.preventDefault(); replace('\n'); return; }
      if (event.inputType === 'historyUndo' || event.inputType === 'historyRedo') { event.preventDefault(); undo(event.inputType === 'historyUndo' ? -1 : 1); return; }
      pendingEdit = snapshot();
    });
    editor.addEventListener('keydown', (event) => {
      if (composing || event.isComposing || event.keyCode === 229) { event.stopPropagation(); return; }
      if (['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End', 'PageUp', 'PageDown', 'Escape', 'Tab'].includes(event.key)) lastEdit = null;
      if ((event.ctrlKey || event.metaKey) && !event.altKey && ['z', 'y'].includes(event.key.toLowerCase())) {
        event.preventDefault(); undo(event.key.toLowerCase() === 'y' || event.shiftKey ? 1 : -1); return;
      }
      const forwarded = new KeyboardEvent('keydown', { key: event.key, code: event.code, keyCode: event.keyCode, shiftKey: event.shiftKey, ctrlKey: event.ctrlKey, metaKey: event.metaKey, altKey: event.altKey, isComposing: composing || event.isComposing, cancelable: true });
      input.dispatchEvent(forwarded);
      if (forwarded.defaultPrevented) event.preventDefault();
    });
    editor.addEventListener('paste', (event) => { event.preventDefault(); if (event.clipboardData) replace(event.clipboardData.getData('text/plain').replace(/\r\n?/g, '\n')); });
    editor.addEventListener('drop', (event) => { event.preventDefault(); });
    for (const type of ['copy', 'cut'] as const) editor.addEventListener(type, (event) => {
      if (!event.clipboardData) return;
      const range = selection(); if (range.start === range.end) return;
      event.preventDefault(); event.stopPropagation();
      event.clipboardData.setData('text/plain', input.value.slice(range.start, range.end));
      if (type === 'cut') replace('', range);
    });
    editor.addEventListener('pointerdown', (event) => {
      lastEdit = null; emojiPointer = null;
      const image = event.target instanceof Element ? event.target.closest<HTMLImageElement>('img[data-emoji-value]') : null;
      if (image && event.isPrimary && event.button === 0) emojiPointer = { image, id: event.pointerId, x: event.clientX, y: event.clientY, time: event.timeStamp, range: { ...selection() } };
    });
    // Keep native drag selection and touch scrolling; only a stationary click
    // needs help placing the caret on either side of a non-editable picture.
    editor.addEventListener('pointermove', (event) => {
      if (emojiPointer?.id === event.pointerId && Math.hypot(event.clientX - emojiPointer.x, event.clientY - emojiPointer.y) > 5) emojiPointer.moved = true;
    });
    editor.addEventListener('pointercancel', () => { emojiPointer = null; });
    editor.addEventListener('blur', () => { lastEdit = null; selection(); });
    editor.addEventListener('pointerup', selection);
    editor.addEventListener('keyup', selection);
    // Weak keys avoid retaining private-message editors after switching threads.
    selections.set(editor, selection);
    const observer = new MutationObserver(syncAttributes);
    observer.observe(input, { attributes: true, attributeFilter: ['disabled', 'readonly', 'required', 'placeholder'] });
    syncAttributes(); paint(input.value); resetHistory();
    return { editor, selection, replace };
  };
})();
