// The rich picker is loaded on first use. The select owns the submitted values.
import type { ChoiceMenuAPI, ChoiceOpenOptions } from './choice-types';

interface ChipView {
  shell: HTMLSpanElement;
  trigger: HTMLButtonElement;
  values: HTMLSpanElement;
  original: [string, string | null][];
}

interface ChoiceRow {
  el: HTMLDivElement;
  option: HTMLOptionElement;
  disabled: boolean;
}

interface ActiveMenu {
  select: HTMLSelectElement;
  anchor: HTMLSelectElement | HTMLButtonElement;
  panel: HTMLDivElement;
  controller: AbortController;
  filterable: boolean;
  render(preferred?: HTMLOptionElement | null): void;
  nativeAnchor: boolean;
  originalHidden: string | null;
}

(() => {
  'use strict';
  const chips = new WeakMap<HTMLSelectElement, ChipView>();
  let active: ActiveMenu | null = null;
  let sequence = 0;

  const node = <K extends keyof HTMLElementTagNameMap>(tag: K, className?: string, text?: string): HTMLElementTagNameMap[K] => {
    const el = document.createElement(tag);
    if (className) el.className = className;
    if (text != null) el.textContent = text;
    return el;
  };
  const glyph = (name: string) => {
    const el = node('span', 'np-choice-glyph');
    el.dataset.glyph = name;
    el.setAttribute('aria-hidden', 'true');
    return el;
  };
  const labelFor = (select: HTMLSelectElement): string => {
    const named = (select.getAttribute('aria-labelledby') || '').split(/\s+/).map(id => document.getElementById(id)?.textContent).filter(Boolean).join(' ');
    const ariaLabel = select.getAttribute('aria-label');
    if (named || ariaLabel) return named || ariaLabel || '选项';
    const label = select.labels?.[0]?.cloneNode(true);
    if (label instanceof Element) label.querySelectorAll('select, button, input, small, [data-choice-chips]').forEach(el => el.remove());
    return label?.textContent?.trim() || select.title || '选项';
  };
  const normalize = (value: string) => value.normalize('NFKC').trim().toLocaleLowerCase();
  const selectedChips = (select: HTMLSelectElement) => [...select.selectedOptions].filter(option => select.multiple || option.value !== '');
  const isDisabled = (option: HTMLOptionElement) => option.matches(':disabled');
  const emptyOption = (select: HTMLSelectElement) => [...select.options].find(option => option.value === '' && !isDisabled(option));
  const disabledControl = (select: HTMLSelectElement) => select.matches(':disabled, [aria-disabled="true"]');
  const appearanceProperties = [
    '--choice-accent', '--choice-muted', '--choice-surface', '--choice-edge', '--choice-paper-edge',
    '--choice-wash', '--choice-hover', '--choice-shadow', '--choice-height', '--choice-inset',
    '--choice-end-inset', '--choice-arrow-inset', '--choice-menu-padding', '--choice-menu-gap',
    '--choice-menu-radius', '--choice-option-radius', '--fluent-validation', '--fluent-error',
    '--admin-ui-control-height', '--fluent-control-height'
  ];
  function syncAppearance(source: HTMLElement, target: HTMLElement) {
    const style = getComputedStyle(source);
    for (const property of appearanceProperties) {
      const value = style.getPropertyValue(property);
      if (value) target.style.setProperty(property, value);
      else target.style.removeProperty(property);
    }
    // A popup or a tag shell may live outside the field's local palette.
    target.style.setProperty('--choice-ink', style.color);
    target.style.setProperty('--choice-radius', style.getPropertyValue('--choice-radius') || style.borderStartStartRadius);
    target.style.setProperty('--choice-size', style.fontSize);
    target.style.setProperty('--choice-font', style.fontFamily);
    target.style.fontFamily = style.fontFamily;
    target.style.fontSize = style.fontSize;
    target.style.fontWeight = style.fontWeight;
    target.style.lineHeight = style.lineHeight;
    target.dir = style.direction;
    return style;
  }
  const setValue = (select: HTMLSelectElement, option: HTMLOptionElement, selected: boolean) => {
    if (option.selected === selected) return;
    option.selected = selected;
    select.dispatchEvent(new Event('input', {bubbles: true}));
    select.dispatchEvent(new Event('change', {bubbles: true}));
  };

  function refreshChips(select: HTMLSelectElement) {
    const view = chips.get(select);
    if (!view) return;
    syncAppearance(select, view.shell);
    const selected = selectedChips(select);
    const label = labelFor(select);
    view.trigger.disabled = disabledControl(select);
    view.trigger.setAttribute('aria-label', `${label}：${selected.map(option => option.label).join('、') || '未选择'}`);
    for (const attr of ['aria-describedby', 'aria-invalid']) {
      const value = select.getAttribute(attr);
      if (value !== null) view.trigger.setAttribute(attr, value);
      else view.trigger.removeAttribute(attr);
    }
    view.trigger.setAttribute('aria-required', String(select.required));
    view.values.replaceChildren();
    if (!selected.length) {
      view.values.append(node('span', 'np-choice-placeholder', select.multiple ? (select.dataset.selectPlaceholder || '请选择') : (select.selectedOptions[0]?.label || '请选择')));
    }
    for (const option of selected) {
      const chip = node('span', 'np-choice-chip');
      chip.append(node('span', 'np-choice-chip-text', option.label));
      const clear = node('button', 'np-choice-clear');
      clear.type = 'button'; clear.setAttribute('aria-label', `移除${option.label}`);
      clear.disabled = view.trigger.disabled || isDisabled(option);
      clear.append(glyph('close'));
      clear.addEventListener('click', event => {
        event.preventDefault(); event.stopPropagation();
        const empty = emptyOption(select);
        if (select.multiple) setValue(select, option, false);
        else if (empty) setValue(select, empty, true);
        if (view.trigger.isConnected) view.trigger.focus({preventScroll: true});
      });
      chip.append(clear); view.values.append(chip);
    }
  }

  function chipView(select: HTMLSelectElement, needed: boolean) {
    const old = chips.get(select);
    if (old && (!needed || old.shell.previousElementSibling !== select)) {
      old.shell.remove(); chips.delete(select);
      for (const [attr, value] of old.original) value == null ? select.removeAttribute(attr) : select.setAttribute(attr, value);
      select.removeAttribute('data-choice-source');
    }
    if (!needed || chips.has(select)) return;
    // Card editors clone existing rows. Rebind their visual controls to the clone.
    if (select.nextElementSibling?.hasAttribute('data-choice-chips')) select.nextElementSibling.remove();
    const shell = node('span', 'np-choice-chips'); shell.dataset.choiceChips = '';
    const trigger = node('button', 'np-choice-trigger'); trigger.type = 'button';
    trigger.setAttribute('role', 'combobox'); trigger.setAttribute('aria-haspopup', 'listbox'); trigger.setAttribute('aria-expanded', 'false');
    const values = node('span', 'np-choice-values');
    trigger.append(glyph('down')); shell.append(trigger, values);
    const original = ['tabindex', 'aria-hidden'].map((attr): [string, string | null] => [attr, select.getAttribute(attr)]);
    if (select.tabIndex >= 0) trigger.tabIndex = select.tabIndex;
    select.after(shell); select.dataset.choiceSource = ''; select.tabIndex = -1; select.setAttribute('aria-hidden', 'true');
    chips.set(select, {shell, trigger, values, original});
  }

  function closeMenu(restore = false) {
    if (!active) return;
    const {panel, anchor, controller, originalHidden, nativeAnchor} = active;
    active = null; controller.abort();
    anchor.setAttribute('aria-expanded', 'false');
    anchor.removeAttribute('aria-controls');
    if (nativeAnchor) {
      if (originalHidden === null) anchor.removeAttribute('aria-hidden');
      else anchor.setAttribute('aria-hidden', originalHidden);
    }
    panel.hidePopover(); panel.remove();
    if (restore && anchor.isConnected && !anchor.matches(':disabled')) anchor.focus({preventScroll: true});
  }

  function openMenu(select: HTMLSelectElement, {last = false, keyboard = false}: ChoiceOpenOptions = {}) {
    if (disabledControl(select)) return;
    if (active?.select === select) { closeMenu(true); return; }
    closeMenu();
    const view = chips.get(select);
    const anchor = view?.trigger || select;
    const frame = view?.shell || select;
    if (!frame.getClientRects().length) return;
    const name = labelFor(select);
    const panel = node('div', 'np-choice-panel'); panel.setAttribute('popover', 'manual');
    panel.toggleAttribute('data-keyboard', keyboard);
    if (select.dataset.selectUi) panel.dataset.variant = select.dataset.selectUi;
    const checkboxes = !!view;
    const filterable = select.dataset.choiceMenu === 'search';
    const list = node('div', 'np-choice-list'); list.id = `np-choice-list-${++sequence}`;
    list.setAttribute('role', 'listbox'); list.setAttribute('aria-label', name); list.tabIndex = -1;
    if (select.multiple) list.setAttribute('aria-multiselectable', 'true');
    if (checkboxes) panel.dataset.checkboxes = '';
    const search = node('input', 'np-choice-search');
    if (filterable) {
      const box = node('div', 'np-choice-searchbox');
      search.type = 'search'; search.autocomplete = 'off'; search.placeholder = '请输入关键词';
      search.setAttribute('role', 'combobox'); search.setAttribute('aria-label', `搜索${name}`);
      search.setAttribute('aria-controls', list.id); search.setAttribute('aria-expanded', 'true'); search.setAttribute('aria-autocomplete', 'list');
      box.append(glyph('search'), search); panel.append(box);
    }
    const status = node('div', 'np-choice-status'); status.setAttribute('role','status');
    panel.append(list, status);
    // A top-layer panel can escape clipping without leaving its originating dialog.
    (select.closest('dialog[open], [popover]:popover-open') || document.body).append(panel);
    const ruler = document.createElement('canvas').getContext('2d');
    let frameStyle: CSSStyleDeclaration;
    let fontSize: number;
    let labelWidth = 0, aboveAnchor = false;
    function syncMetrics() {
      frameStyle = syncAppearance(frame, panel);
      fontSize = parseFloat(frameStyle.fontSize);
      if (ruler) {
        ruler.font = `${frameStyle.fontWeight} ${frameStyle.fontSize} ${frameStyle.fontFamily}`;
        labelWidth = [...select.options].reduce((width, option) => option.hidden || option.parentElement?.hidden ? width : Math.max(width, ruler.measureText(option.label).width), 0);
      }
    }
    syncMetrics();
    const focusOwner = filterable ? search : list;
    let rows: ChoiceRow[] = [];
    let cursor = -1, prefix = '', lastKey = 0;
    const controller = new AbortController();
    // EventTarget erases the map; callers still pair each event name with its DOM type.
    const listen = <K extends keyof WindowEventMap>(el: EventTarget, type: K, fn: (event: WindowEventMap[K]) => void, capture = false) =>
      el.addEventListener(type, fn as EventListener, {capture, signal: controller.signal});

    function position() {
      if (active?.panel !== panel) return;
      if (!frame.isConnected || !frame.getClientRects().length) { closeMenu(); return; }
      const r = frame.getBoundingClientRect(), vv = window.visualViewport;
      const left = vv?.offsetLeft || 0, top = vv?.offsetTop || 0;
      const right = left + (vv?.width || innerWidth), bottom = top + (vv?.height || innerHeight);
      if (r.bottom <= top || r.top >= bottom || r.right <= left || r.left >= right) { closeMenu(); return; }
      const gutter = 8, gap = parseFloat(frameStyle.getPropertyValue('--choice-menu-gap')) || 8;
      const listWidth = (checkboxes || select.dataset.selectIcon ? 12 : 8.3) * fontSize;
      const contentWidth = Math.min(labelWidth + (checkboxes || select.dataset.selectIcon ? 4.5 : 3.5) * fontSize, 20 * fontSize);
      const width = Math.min(Math.max(r.width, contentWidth, filterable ? 220 : listWidth), right - left - 2 * gutter);
      panel.style.width = `${width}px`;
      const below = bottom - gutter - r.bottom - gap, above = r.top - top - gap - gutter;
      const wanted = Math.min(366, panel.offsetHeight - list.clientHeight + list.scrollHeight);
      // Keep the search field on the same side when filtering makes both sides fit.
      if (Math.min(above, below) < wanted) aboveAnchor = above > below;
      const space = aboveAnchor ? above : below;
      const height = Math.min(366, bottom - top - 2 * gutter, Math.max(52, space));
      panel.style.maxHeight = `${height}px`;
      // Align the other edge to the field before clamping at the screen margin.
      const alignLeft = panel.dir === 'rtl' ? r.right - width < left + gutter : r.left + width <= right - gutter;
      const x = alignLeft ? r.left : r.right - width;
      panel.style.left = `${Math.max(left + gutter, Math.min(x, right - width - gutter))}px`;
      const y = aboveAnchor ? Math.max(top + gutter + height, r.top - gap) : Math.min(bottom - gutter - height, r.bottom + gap);
      // Pin the near edge, so changes in list height cannot open a gap.
      panel.style.top = aboveAnchor ? 'auto' : `${y}px`;
      panel.style.bottom = aboveAnchor ? `calc(100% - ${y}px)` : 'auto';
    }
    function point(index: number | undefined, scroll: boolean) {
      rows[cursor]?.el.removeAttribute('data-current');
      cursor = index ?? -1;
      const row = rows[cursor];
      if (!row) { focusOwner.removeAttribute('aria-activedescendant'); return; }
      row.el.dataset.current = ''; focusOwner.setAttribute('aria-activedescendant', row.el.id);
      if (scroll) row.el.scrollIntoView({block: 'nearest', inline: 'nearest'});
    }
    function render(preferred: HTMLOptionElement | null | undefined = rows[cursor]?.option) {
      syncMetrics();
      const query = normalize(search.value);
      const content = document.createDocumentFragment();
      rows = []; cursor = -1;
      let lastGroup: HTMLOptGroupElement | null | undefined;
      let parent: DocumentFragment | HTMLDivElement = content;
      const max = Number(select.dataset.maxSelected) || Infinity;
      const full = select.multiple && select.selectedOptions.length >= max;
      for (const option of select.options) {
        const group = option.parentElement instanceof HTMLOptGroupElement ? option.parentElement : null;
        if (checkboxes && !select.multiple && option.value === '') continue;
        if (option.hidden || group?.hidden || query && !normalize(`${option.label} ${option.value} ${group?.label || ''}`).includes(query)) continue;
        if (group !== lastGroup) {
          parent = content;
          if (group) {
            parent = node('div', 'np-choice-group'); parent.setAttribute('role','group'); parent.setAttribute('aria-label', group.label);
            const heading = node('div','np-choice-heading',group.label); heading.setAttribute('aria-hidden','true');
            parent.append(heading); content.append(parent);
          }
          lastGroup = group;
        }
        const el = node('div','np-choice-option'); el.id = `${list.id}-${option.index}`;
        const disabled = isDisabled(option) || full && !option.selected;
        el.setAttribute('role','option'); el.setAttribute('aria-selected', String(option.selected)); el.dataset.row = String(rows.length);
        if (disabled) el.setAttribute('aria-disabled','true');
        if (checkboxes) el.append(glyph('box'));
        else {
          const icon = option.dataset.selectIcon || select.dataset.selectIcon;
          if (icon) el.append(glyph(icon));
        }
        el.append(node('span','np-choice-text',option.label));
        if (!checkboxes && option.selected && select.dataset.selectUi !== 'soft') el.append(glyph('check'));
        parent.append(el); rows.push({el, option, disabled});
      }
      list.replaceChildren(content);
      status.textContent = !rows.length ? (query ? '没有匹配的选项' : '暂无可选项') : full ? `最多选择 ${max} 项` : '';
      const enabled = rows.findIndex(row => !row.disabled && row.option === preferred);
      point(enabled >= 0 ? enabled : rows.findIndex(row => !row.disabled && row.option.selected), false);
      position();
    }
    function choose(index: number | undefined) {
      const row = rows[index ?? -1];
      if (!row || row.disabled || disabledControl(select)) return;
      if (select.multiple) {
        setValue(select, row.option, !row.option.selected);
      } else {
        const next = checkboxes && row.option.selected ? emptyOption(select) : row.option;
        closeMenu(true);
        if (next) setValue(select, next, true);
      }
      if (active?.panel === panel) { if (frame.isConnected) render(row.option); else closeMenu(); }
    }
    listen(panel, 'pointermove', () => panel.removeAttribute('data-keyboard'));
    listen(list,'mousedown', event => event.preventDefault());
    listen(list,'click', event => {
      const row = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-row]') : null;
      if (row) choose(Number(row.dataset.row));
    });
    listen(search,'input', () => { render(null); if (cursor < 0) point(rows.findIndex(row => !row.disabled), false); });
    listen(panel,'keydown', event => {
      if (event.isComposing || event.keyCode === 229) return;
      panel.setAttribute('data-keyboard', '');
      const {key} = event;
      if (key === 'Tab') { closeMenu(true); return; }
      const enabled = rows.map((row, index) => row.disabled ? -1 : index).filter(index => index !== -1);
      const current = enabled.indexOf(cursor);
      if (key === 'Escape') closeMenu(true);
      else if (key === 'Enter' || key === ' ' && !filterable) choose(cursor >= 0 ? cursor : enabled[0]);
      else if (key === 'ArrowDown') point(enabled[Math.min(current + 1, enabled.length - 1)], true);
      else if (key === 'ArrowUp') point(enabled[current < 0 ? enabled.length - 1 : Math.max(0, current - 1)], true);
      else if (!filterable && (key === 'Home' || key === 'End')) point(key === 'Home' ? enabled[0] : enabled.at(-1), true);
      else if (!filterable && key.length === 1 && !event.ctrlKey && !event.metaKey) {
        prefix = (Date.now() - lastKey > 600 ? '' : prefix) + normalize(key); lastKey = Date.now();
        const match = enabled.find(index => { const row = rows[index]; return row && normalize(row.option.label).startsWith(prefix); });
        if (match !== undefined) point(match, true);
      } else return;
      event.preventDefault(); event.stopPropagation();
    });
    listen(document,'pointerdown', event => { if (!(event.target instanceof Node) || !frame.contains(event.target) && !panel.contains(event.target)) closeMenu(); }, true);
    listen(document,'focusin', event => { if (!(event.target instanceof Node) || !frame.contains(event.target) && !panel.contains(event.target)) closeMenu(); });
    listen(document,'scroll', event => { if (!(event.target instanceof Node) || !panel.contains(event.target)) position(); }, true);
    const resize = () => { syncMetrics(); position(); };
    listen(window,'resize',resize);
    if (window.ResizeObserver) {
      const observer = new ResizeObserver(resize);
      observer.observe(frame);
      controller.signal.addEventListener('abort', () => observer.disconnect(), {once:true});
    }
    if (window.visualViewport) { listen(window.visualViewport,'resize',resize); listen(window.visualViewport,'scroll',position); }
    listen(window,'pagehide', () => closeMenu());
    const dialog = select.closest('dialog');
    if (dialog) listen(dialog, 'close', () => closeMenu());
    active = {select, anchor, panel, controller, filterable, render, nativeAnchor: anchor === select, originalHidden: anchor.getAttribute('aria-hidden')};
    anchor.setAttribute('aria-expanded','true'); anchor.setAttribute('aria-controls',list.id);
    panel.showPopover(); render();
    if (cursor < 0 || last) {
      let index = rows.findIndex(row => !row.disabled);
      // Keep this entry within the configured ES2022 library contract.
      if (last) {
        for (let i = rows.length - 1; i >= 0; i--) {
          const row = rows[i];
          if (row && !row.disabled) { index = i; break; }
        }
      }
      point(index, false);
    }
    focusOwner.focus({preventScroll:true});
    // Expose one set of options while focus is in the popup. Restore the native
    // control before returning focus or dispatching its change event.
    if (anchor === select && document.activeElement === focusOwner) anchor.setAttribute('aria-hidden', 'true');
    rows[cursor]?.el.scrollIntoView({block:'nearest'});
  }

  function prepare(select: HTMLSelectElement) {
    const tag = select.dataset.selectUi === 'tag' && !select.required && !!emptyOption(select);
    const showChips = select.hasAttribute('data-choice-menu') && (select.multiple || tag);
    chipView(select, showChips);
    if (showChips) refreshChips(select);
    if (active?.select !== select) return;
    const anchor = chips.get(select)?.trigger || select;
    if (disabledControl(select) || !anchor.getClientRects().length || !select.hasAttribute('data-choice-menu') ||
        anchor !== active.anchor || active.filterable !== (select.dataset.choiceMenu === 'search')) {
      closeMenu();
    } else {
      active.render();
    }
  }

  document.addEventListener('change', event => {
    if (event.target instanceof HTMLSelectElement && chips.has(event.target)) refreshChips(event.target);
  });
  document.addEventListener('focusin', event => { if (event.target instanceof HTMLSelectElement) chips.get(event.target)?.trigger.focus(); });
  const syncContext = () => {
    document.querySelectorAll<HTMLSelectElement>('select[data-choice-source]').forEach(select => {
      const view = chips.get(select);
      if (view) syncAppearance(select, view.shell);
    });
  };
  window.addEventListener('resize', syncContext);
  // Theme changes update outer variables without resizing the fields.
  // Keep chip buttons intact while refreshing the cached appearance.
  const appearanceObserver = new MutationObserver(() => { syncContext(); active?.render(); });
  appearanceObserver.observe(document.documentElement, {attributes:true,
    attributeFilter:['data-theme','data-mode','data-style','data-input-style','class','style']});
  appearanceObserver.observe(document.body, {attributes:true, attributeFilter:['class','style']});
  const api: ChoiceMenuAPI = {
    prepare, open: openMenu,
    reset(select) {
      if (chips.has(select)) refreshChips(select);
      if (active?.select === select) closeMenu();
    },
    prune() { if (active && !active.anchor.isConnected) closeMenu(); }
  };
  window.NewPressChoiceMenu = api;
})();
