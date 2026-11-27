import { parseCustomBlocks, customBlocksError, MAX_CUSTOM_BLOCKS, isHomeVisibility } from './layout-data';
import type { HomeCustomBlock } from './layout-data';
// The list editor and visual canvas share module and custom HTML controls.
(() => {
  const form = document.querySelector<HTMLFormElement>('#theme-settings-form');
  if (!form) return;
  const editors = [...form.querySelectorAll<HTMLElement>('[data-home-editor]')];
  if (!editors.length) return;
  const lists = editors.map(editor => editor.querySelector<HTMLOListElement>('.home-editor-list')!);
  if (lists.some(list => !list)) return;
  let restoreError = '';
  const status = (message: string) => { const node = form.querySelector<HTMLElement>('[data-home-editor-status]'); if (node) node.textContent = message; };
  const report = (error: unknown) => { restoreError = error instanceof Error ? error.message : 'HTML 组件格式不正确'; status(restoreError); };
  // Reveal invalid controls before the browser attempts to focus them.
  form.addEventListener('invalid', event => {
    const panel = event.target instanceof Element ? event.target.closest<HTMLDetailsElement>('[data-home-config]') : null;
    if (panel) panel.open = true;
  }, true);
  form.querySelectorAll<HTMLDetailsElement>('[data-home-config]').forEach(panel => {
    if (panel.querySelector('.admin-field-error')) panel.open = true;
  });
  const locked = () => !!restoreError || !!form.querySelector('fieldset')?.disabled;
  const children = (list: Element): HTMLElement[] => [...list.children].filter((node): node is HTMLElement => node instanceof HTMLElement);
  const allRows = () => lists.flatMap(children);
  const item = form.elements.namedItem('home_custom_blocks');
  const payload = item instanceof HTMLInputElement ? item : null;
  let payloadSnapshot = payload?.value || '';
  const customRows = () => allRows().filter(row => row.hasAttribute('data-home-custom'));
  const readCustom = (row: HTMLElement): HomeCustomBlock => {
    const visibility = row.querySelector<HTMLSelectElement>('[data-home-visibility]')!.value;
    if (!isHomeVisibility(visibility)) throw new Error('HTML 组件显示设备无效');
    return { key: row.dataset.homeKey!, title: row.querySelector<HTMLInputElement>('[data-home-custom-title]')!.value, html: row.querySelector<HTMLTextAreaElement>('[data-home-custom-html]')!.value, surface: row.querySelector<HTMLInputElement>('[data-home-custom-surface]')!.checked, visibility };
  };
  const writeCustom = (row: HTMLElement, block: HomeCustomBlock) => {
    const title = row.querySelector<HTMLInputElement>('[data-home-custom-title]')!, html = row.querySelector<HTMLTextAreaElement>('[data-home-custom-html]')!;
    if (title.value !== block.title) title.value = block.title;
    if (html.value !== block.html) html.value = block.html;
    row.querySelector<HTMLInputElement>('[data-home-custom-surface]')!.checked = block.surface;
    row.querySelector<HTMLSelectElement>('[data-home-visibility]')!.value = block.visibility;
    row.dataset.homeTitle = block.title.trim() || '自定义 HTML';
    row.querySelector<HTMLElement>('.home-editor-copy > b')!.textContent = row.dataset.homeTitle;
  };
  const validateCustom = () => {
    if (!payload) return;
    const message = customBlocksError(customRows().map(readCustom), payload.value);
    customRows().forEach((row, index) => row.querySelector<HTMLTextAreaElement>('[data-home-custom-html]')!.setCustomValidity(index === 0 ? message : ''));
  };
  const serializeCustom = () => {
    if (!payload || restoreError) return;
    if (payload.value !== payloadSnapshot) { restore(); return; }
    try {
      const blocks = customRows().map(readCustom).sort((a, b) => a.key.localeCompare(b.key, 'en'));
      const raw = JSON.stringify(blocks); parseCustomBlocks(raw);
      payload.value = payloadSnapshot = raw; validateCustom();
      customRows().forEach(row => writeCustom(row, readCustom(row)));
    } catch (error) { report(error); paint(); }
  };
  let drag: { row: HTMLElement; original: HTMLElement[][]; valid: boolean } | null = null;
  let pointer: { id: number; x: number; y: number; startX: number; startY: number; active: boolean; handle: HTMLButtonElement } | null = null;
  let scrollFrame = 0, lastScroll = 0;
  const paint = () => {
    const full = customRows().length >= MAX_CUSTOM_BLOCKS;
    form.querySelectorAll<HTMLButtonElement>('[data-home-custom-add], [data-home-custom-copy]').forEach(button => { button.disabled = locked() || full; button.title = full ? '最多添加 12 个 HTML 组件' : ''; });
    editors.forEach(editor => {
      const list = editor.querySelector<HTMLOListElement>('.home-editor-list')!;
      children(list).forEach((row, index) => {
        row.querySelector<HTMLButtonElement>('[data-home-move="up"]')!.disabled = locked() || index === 0;
        row.querySelector<HTMLButtonElement>('[data-home-move="down"]')!.disabled = locked() || index === list.children.length - 1;
        row.querySelector<HTMLSelectElement>('[data-home-region]')!.value = editor.dataset.homeEditor || 'top';
        row.classList.toggle('is-home-module-hidden', row.querySelector<HTMLSelectElement>('[data-home-visibility]')!.value === 'hidden');
      });
    });
  };
  const sync = (row: HTMLElement | null, announce = true) => {
    // Update every region before dispatch: no transient duplicate reaches preview.
    editors.forEach(editor => {
      editor.querySelector<HTMLInputElement>('[data-home-order]')!.value = children(editor.querySelector('.home-editor-list')!).map(item => item.dataset.homeKey).join(',');
    });
    serializeCustom();
    paint();
    if (announce && row) {
      const editor = row.closest<HTMLElement>('[data-home-editor]')!;
      const region = row.querySelector<HTMLSelectElement>('[data-home-region]')!.selectedOptions[0]?.textContent || '';
      editor.querySelector<HTMLElement>('[data-home-editor-status]')!.textContent = `${row.dataset.homeTitle}已移至${region}第 ${children(row.parentElement!).indexOf(row) + 1} 项，保存后生效。`;
    }
    form.dispatchEvent(new Event('change', { bubbles: true }));
    document.dispatchEvent(new Event('appearance-preview-change'));
  };
  const restore = () => {
    if (drag) return;
    if (payload) {
      let blocks: HomeCustomBlock[];
      try { blocks = parseCustomBlocks(payload.value); restoreError = ''; payloadSnapshot = payload.value; } catch (error) { report(error); paint(); return; }
      const current = new Map(customRows().map(row => [row.dataset.homeKey, row]));
      for (const block of blocks) {
        const row = current.get(block.key) || createCustom(block);
        writeCustom(row, block); current.delete(block.key);
      }
      current.forEach(row => row.remove());
    }
    const byKey = new Map(allRows().map(row => [row.dataset.homeKey, row]));
    const ordered = editors.map(editor => {
      const rows: HTMLElement[] = [];
      editor.querySelector<HTMLInputElement>('[data-home-order]')!.value.split(',').forEach(key => {
        const row = byKey.get(key.trim());
        if (row) { rows.push(row); byKey.delete(key.trim()); }
      });
      return rows;
    });
    // Unlisted modules return to their catalog region when restoring defaults.
    byKey.forEach(row => ordered[Math.max(0, editors.findIndex(editor => editor.dataset.homeEditor === (row.dataset.homeDefaultRegion || 'top')))]!.push(row));
    // Preview updates must not detach an unchanged row: that would steal focus
    // and discard keystrokes while typing into an inline HTML editor.
    ordered.forEach((rows, index) => rows.forEach((row, position) => {
      if (lists[index]!.children[position] !== row) lists[index]!.insertBefore(row, lists[index]!.children[position] || null);
    }));
    validateCustom();
    paint();
  };
  const move = (row: HTMLElement, direction: 'up' | 'down') => {
    if (locked()) return;
    const neighbor = direction === 'up' ? row.previousElementSibling : row.nextElementSibling;
    if (!neighbor) return;
    row.parentElement!.insertBefore(direction === 'up' ? row : neighbor, direction === 'up' ? neighbor : row);
    sync(row);
  };
  const begin = (row: HTMLElement) => {
    if (locked()) return false;
    drag = { row, original: lists.map(children), valid: false };
    row.classList.add('is-home-dragging');
    return true;
  };
  const place = (target: Element | null, y: number) => {
    if (!drag) return;
    const list = target?.closest<HTMLOListElement>('.home-editor-list');
    drag.valid = !!list && lists.includes(list);
    if (!drag.valid || !list || !target) return;
    const row = target.closest('[data-home-key]');
    if (!row) list.append(drag.row);
    else if (row !== drag.row) {
      const rect = row.getBoundingClientRect();
      list.insertBefore(drag.row, y < rect.top + rect.height / 2 ? row : row.nextSibling);
    }
  };
  const end = (commit: boolean) => {
    if (!drag) return;
    const active = drag; drag = null;
    active.row.classList.remove('is-home-dragging');
    const accepted = commit && !locked();
    if (!accepted) active.original.forEach((rows, index) => rows.forEach(row => lists[index]!.append(row)));
    if (accepted) sync(active.row); else paint();
  };
  const finishPointer = (commit: boolean) => {
    const current = pointer; pointer = null;
    cancelAnimationFrame(scrollFrame); scrollFrame = 0; lastScroll = 0;
    end(commit);
    if (current?.handle.hasPointerCapture(current.id)) current.handle.releasePointerCapture(current.id);
  };
  // Continue reaching offscreen regions while a captured pointer stays at an edge.
  const scrollDrag = (now: number) => {
    if (!pointer?.active || !drag) return;
    if (locked()) { finishPointer(false); return; }
    let scroller: Element = document.scrollingElement || document.documentElement;
    for (let parent = form.parentElement; parent; parent = parent.parentElement) {
      if (/auto|scroll/.test(getComputedStyle(parent).overflowY) && parent.scrollHeight > parent.clientHeight) { scroller = parent; break; }
    }
    const documentScroll = scroller === document.scrollingElement;
    const bounds = documentScroll ? { top: 0, bottom: innerHeight } : scroller.getBoundingClientRect();
    const edge = 76, speed = pointer.y < bounds.top + edge ? -1 : pointer.y > bounds.bottom - edge ? 1 : 0;
    const elapsed = lastScroll ? Math.min(32, now - lastScroll) : 16; lastScroll = now;
    if (speed) {
      const before = scroller.scrollTop;
      scroller.scrollBy({ top: speed * elapsed * 0.6, behavior: 'instant' });
      if (scroller.scrollTop !== before) place(document.elementFromPoint(pointer.x, pointer.y), pointer.y);
    }
    scrollFrame = requestAnimationFrame(scrollDrag);
  };
  const prepareRow = (row: HTMLElement) => {
    row.dataset.homeDefaultRegion ||= ['hero', 'notice', 'channels', 'categories', 'carousel', 'search_banner'].includes(row.dataset.homeKey || '') || row.hasAttribute('data-home-custom') ? 'top' : ['feed', 'series', 'spotlight', 'community'].includes(row.dataset.homeKey || '') ? 'main' : 'aside';
    const handle = row.querySelector<HTMLButtonElement>('[data-home-handle]')!;
    // Use one pointer path: native drag on buttons/SVG descendants differs by engine.
    handle.draggable = false;
    row.querySelectorAll<HTMLElement>('.home-editor-moves, [data-home-handle], .home-editor-position, [data-home-custom-copy], [data-home-custom-remove]').forEach(node => { node.hidden = false; });
    handle.addEventListener('keydown', event => {
      if (locked() || !['ArrowUp', 'ArrowDown'].includes(event.key)) return;
      event.preventDefault(); move(row, event.key === 'ArrowUp' ? 'up' : 'down'); handle.focus();
    });
    handle.addEventListener('pointerdown', event => {
      if (!event.isPrimary || event.button !== 0 || locked() || pointer) return;
      event.preventDefault(); handle.focus({ preventScroll: true });
      pointer = { id: event.pointerId, x: event.clientX, y: event.clientY, startX: event.clientX, startY: event.clientY, active: false, handle };
      handle.setPointerCapture(event.pointerId);
    });
    handle.addEventListener('pointermove', event => {
      if (!pointer || event.pointerId !== pointer.id) return;
      if (locked()) { finishPointer(false); return; }
      pointer.x = event.clientX; pointer.y = event.clientY;
      if (!pointer.active && Math.hypot(event.clientX - pointer.startX, event.clientY - pointer.startY) > 6) {
        pointer.active = begin(row);
        if (pointer.active) scrollFrame = requestAnimationFrame(scrollDrag);
      }
      if (pointer.active) {
        event.preventDefault();
        place(document.elementFromPoint(event.clientX, event.clientY), event.clientY);
        // Reparenting a dragged row releases capture in Chromium. Reacquire it
        // after placement so subsequent moves and pointerup reach this handle.
        handle.setPointerCapture(event.pointerId);
      }
    });
    handle.addEventListener('pointerup', event => {
      if (!pointer || event.pointerId !== pointer.id) return;
      if (pointer.active) place(document.elementFromPoint(event.clientX, event.clientY), event.clientY);
      finishPointer(!!drag?.valid); handle.focus({ preventScroll: true });
    });
    handle.addEventListener('pointercancel', () => finishPointer(false));
    handle.addEventListener('lostpointercapture', event => {
      if (pointer?.id === event.pointerId && !handle.hasPointerCapture(event.pointerId)) finishPointer(false);
    });
  };
  const createCustom = (block: HomeCustomBlock) => {
    const row = form.querySelector<HTMLTemplateElement>('[data-home-custom-template]')!.content.firstElementChild!.cloneNode(true) as HTMLElement;
    row.dataset.homeKey = block.key;
    row.querySelectorAll('[id], [for]').forEach(element => {
      for (const attr of ['id', 'for']) if (element.hasAttribute(attr)) element.setAttribute(attr, element.getAttribute(attr)!.replace('custom_template', block.key));
    });
    writeCustom(row, block); lists[0]!.append(row); prepareRow(row); return row;
  };
  allRows().forEach(prepareRow);
  form.querySelectorAll<HTMLButtonElement>('[data-home-custom-add]').forEach(button => { button.hidden = !payload; });
  form.addEventListener('click', event => {
    if (!(event.target instanceof Element)) return;
    const action = event.target.closest<HTMLButtonElement>('[data-home-custom-add], [data-home-custom-copy], [data-home-custom-remove]');
    if (action && !locked() && !action.disabled) {
      const original = action.closest<HTMLElement>('[data-home-custom]');
      if (action.hasAttribute('data-home-custom-remove') && original) {
        const editor = original.closest<HTMLElement>('[data-home-editor]')!;
        original.remove(); sync(null); editor.querySelector<HTMLButtonElement>('[data-home-custom-add]')?.focus();
        editor.querySelector<HTMLElement>('[data-home-editor-status]')!.textContent = '组件已删除，保存后生效。可通过撤销恢复。';
      } else if (customRows().length < MAX_CUSTOM_BLOCKS) {
        const block: HomeCustomBlock = original ? readCustom(original) : { key: '', title: '', html: '', surface: true, visibility: 'both' };
        block.key = 'custom_' + crypto.randomUUID().replaceAll('-', '');
        const row = createCustom(block);
        if (original) original.after(row); else action.closest('[data-home-editor]')!.querySelector('.home-editor-list')!.append(row);
        sync(row); row.querySelector<HTMLInputElement>('[data-home-custom-title]')!.focus();
      }
      return;
    }
    const button = event.target.closest<HTMLButtonElement>('[data-home-move]');
    if (!button || button.disabled) return;
    const row = button.closest<HTMLElement>('[data-home-key]'); if (!row || (button.dataset.homeMove !== 'up' && button.dataset.homeMove !== 'down')) return; move(row, button.dataset.homeMove);
    (button.disabled ? row.querySelector<HTMLButtonElement>('[data-home-handle]')! : button).focus();
  });
  // Serialize before the savebar and preview read the form. A form-level input
  // is the shared savebar's discard event, so rebuild dynamic rows from its payload.
  form.addEventListener('input', event => {
    if (event.target === form || event.target === payload) restore();
    else if (event.target instanceof Element && event.target.closest('[data-home-custom]') && !locked()) {
      serializeCustom(); document.dispatchEvent(new Event('appearance-preview-change'));
    }
  }, true);
  form.addEventListener('change', event => {
    if (event.target instanceof Element && event.target.closest('[data-home-custom]') && !locked()) serializeCustom();
  }, true);
  form.addEventListener('change', event => {
    const target = event.target;
    if (target === form || target === payload) { restore(); return; }
    if (!(target instanceof HTMLInputElement || target instanceof HTMLSelectElement)) return;
    if (target.matches('[data-home-region]') && !locked()) {
      const list = editors.find(editor => editor.dataset.homeEditor === target.value)?.querySelector('.home-editor-list');
      const row = target.closest<HTMLElement>('[data-home-key]'); if (list && row) { list.append(row); sync(row); } return;
    }
    if (target.matches('[data-home-order]')) restore();
    paint();
  });
  document.addEventListener('pointerdown', event => { if (pointer && pointer.id !== event.pointerId) finishPointer(false); }, true);
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape') { const handle = pointer?.handle; finishPointer(false); handle?.focus({ preventScroll: true }); }
  });
  window.addEventListener('blur', () => finishPointer(false));
  window.addEventListener('pagehide', () => finishPointer(false));
  form.addEventListener('reset', () => queueMicrotask(restore));
  document.addEventListener('appearance-preview-change', restore);
  form.addEventListener('submit', event => {
    try { if (payload) { const blocks = parseCustomBlocks(payload.value), error = customBlocksError(blocks, payload.value); if (error) throw new Error(error); } }
    catch (error) { event.preventDefault(); status(error instanceof Error ? error.message : 'HTML 组件格式不正确'); }
  }, true);
  restore();
})();
