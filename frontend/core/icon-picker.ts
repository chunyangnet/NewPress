import type { AdminAPI } from './admin-types';
import type { IconPickerAPI } from './navigation-types';

interface IconOption {
  button: HTMLButtonElement;
  value: string;
  label: string;
  search: string;
  disabled: boolean;
}

interface PickerState {
  dialog: HTMLDialogElement;
  search: HTMLInputElement;
  grid: HTMLDivElement;
  result: HTMLParagraphElement;
  target: HTMLSelectElement | null;
  opener: HTMLButtonElement | null;
  options: IconOption[];
}

// One local SVG grid is shared by the cards and only created on first open.
export function createIconPicker(admin: AdminAPI): IconPickerAPI {
  const attached = new WeakMap<HTMLSelectElement, HTMLButtonElement>();
  let state: PickerState | undefined;
  const node = <K extends keyof HTMLElementTagNameMap>(tag: K, className = '', text = ''): HTMLElementTagNameMap[K] => {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (text) element.textContent = text;
    return element;
  };
  const localIcon = (value: string) => admin.icon(value && document.getElementById(`icon-${value}`) ? value : value ? 'circle-help' : 'minus');
  const visibleOptions = (picker: PickerState) => picker.options.filter(option => !option.button.hidden && !option.disabled);
  const filter = (picker: PickerState) => {
    const needle = picker.search.value.trim().toLocaleLowerCase();
    let count = 0;
    for (const option of picker.options) {
      option.button.hidden = needle !== '' && !option.search.includes(needle);
      if (!option.button.hidden && !option.disabled) count += 1;
    }
    picker.result.textContent = count ? `${count} 个图标可选` : '没有找到图标，试试「首页」或 house。';
  };
  const ensureDialog = (): PickerState => {
    if (state) return state;
    const dialog = node('dialog', 'admin-icon-dialog');
    dialog.id = 'navigation-icon-picker';
    dialog.setAttribute('aria-labelledby', 'navigation-icon-title');
    const heading = node('header');
    const title = node('h2', '', '选择图标'); title.id = 'navigation-icon-title';
    const close = node('button', 'admin-icon-close');
    close.type = 'button'; close.setAttribute('aria-label', '关闭图标选择'); close.append(admin.icon('x'));
    close.addEventListener('click', () => dialog.close());
    heading.append(title, close);
    const searchLabel = node('label', 'admin-icon-search fluent-input-group');
    const search = node('input'); search.type = 'search'; search.placeholder = '搜索中文名称或英文名称'; search.autocomplete = 'off';
    search.setAttribute('aria-label', '搜索图标');
    searchLabel.append(admin.icon('search'), search);
    const result = node('p', 'admin-icon-result'); result.setAttribute('aria-live', 'polite'); result.setAttribute('role', 'status');
    const grid = node('div', 'admin-icon-grid'); grid.setAttribute('role', 'group'); grid.setAttribute('aria-label', '图标库');
    const picker: PickerState = { dialog, search, result, grid, target: null, opener: null, options: [] };
    search.addEventListener('input', () => filter(picker));
    search.addEventListener('keydown', event => {
      if (event.key === 'ArrowDown') { event.preventDefault(); visibleOptions(picker)[0]?.button.focus(); }
    });
    grid.addEventListener('click', event => {
      const button = event.target instanceof Element ? event.target.closest<HTMLButtonElement>('[data-icon-option]') : null;
      const target = picker.target;
      if (!button || !target || !target.isConnected || target.matches(':disabled') || button.disabled) return;
      const choice = picker.options.find(option => option.button === button);
      if (!choice) return;
      target.value = choice.value;
      target.dispatchEvent(new Event('change', { bubbles: true }));
      dialog.close();
    });
    grid.addEventListener('keydown', event => {
      const choices = visibleOptions(picker);
      const index = choices.findIndex(option => option.button === event.target);
      if (index < 0) return;
      const columns = Math.max(1, getComputedStyle(grid).gridTemplateColumns.trim().split(/\s+/).length);
      const offsets: Readonly<Record<string, number>> = { ArrowRight: 1, ArrowLeft: -1, ArrowDown: columns, ArrowUp: -columns };
      let next = event.key === 'Home' ? 0 : event.key === 'End' ? choices.length - 1 : index + (offsets[event.key] ?? NaN);
      if (!Number.isFinite(next)) return;
      event.preventDefault(); next = Math.max(0, Math.min(choices.length - 1, next)); choices[next]?.button.focus();
    });
    dialog.append(heading, searchLabel, result, grid);
    dialog.addEventListener('click', event => { if (event.target === dialog) dialog.close(); });
    dialog.addEventListener('close', () => {
      picker.opener?.setAttribute('aria-expanded', 'false');
      if (picker.opener?.isConnected) picker.opener.focus({ preventScroll: true });
      picker.target = null; picker.opener = null;
    });
    document.body.append(dialog);
    state = picker;
    return picker;
  };
  const open = (select: HTMLSelectElement, button: HTMLButtonElement) => {
    if (select.matches(':disabled')) return;
    const picker = ensureDialog(); picker.target = select; picker.opener = button;
    const available = [...select.options];
    if (available.length !== picker.options.length || available.some((option, index) => option.value !== picker.options[index]?.value || (option.textContent || '') !== picker.options[index]?.label || Boolean(option.disabled) !== picker.options[index]?.disabled)) {
      const fragment = document.createDocumentFragment();
      picker.options = available.map(option => {
        const label = option.textContent || '';
        const entry = node('button', 'admin-icon-option'); entry.type = 'button'; entry.dataset.iconOption = option.value; entry.disabled = Boolean(option.disabled);
        entry.title = option.value ? `${label} · ${option.value}` : label;
        entry.append(localIcon(option.value), node('span', '', label)); fragment.append(entry);
        return { button: entry, value: option.value, label, search: `${label} ${option.value}`.toLocaleLowerCase(), disabled: entry.disabled };
      });
      picker.grid.replaceChildren(fragment);
    }
    picker.options.forEach(option => option.button.setAttribute('aria-pressed', String(option.value === select.value)));
    picker.search.value = ''; filter(picker); button.setAttribute('aria-expanded', 'true'); picker.dialog.showModal(); picker.search.focus();
  };
  const attach = (root: ParentNode) => {
    // Leave native selects visible in browsers without modal dialog support.
    if (typeof HTMLDialogElement === 'undefined' || !HTMLDialogElement.prototype.showModal) return;
    root.querySelectorAll<HTMLSelectElement>('select[data-icon-select]').forEach(select => {
      if (attached.has(select) || !select.parentElement) return;
      // Restored clones retain their trigger, but need fresh event bindings.
      const button = select.parentElement.querySelector<HTMLButtonElement>('[data-icon-picker-trigger]') || node('button', 'admin-icon-trigger');
      button.type = 'button'; button.dataset.iconPickerTrigger = ''; button.setAttribute('aria-haspopup', 'dialog');
      button.setAttribute('aria-controls', 'navigation-icon-picker'); button.setAttribute('aria-expanded', 'false');
      const refresh = () => {
        const label = select.selectedOptions[0]?.textContent || '无图标';
        button.replaceChildren(localIcon(select.value), node('span', '', label), admin.icon('chevron-down'));
        button.setAttribute('aria-label', `选择图标：${label}`); button.disabled = select.matches(':disabled');
      };
      button.addEventListener('click', () => open(select, button)); select.addEventListener('change', refresh);
      select.hidden = true; select.after(button); attached.set(select, button); refresh();
    });
  };
  return Object.freeze({ attach });
}
