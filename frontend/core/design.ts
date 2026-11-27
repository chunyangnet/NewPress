import { parseCustomBlocks } from './layout-data';
import type { FormControl } from './admin-types';

(() => {
  const center = document.querySelector<HTMLElement>('[data-module-center]');
  if (center) {
    const search = center.querySelector<HTMLInputElement>('[data-module-search]');
    const count = center.querySelector<HTMLElement>('[data-module-count]'), empty = center.querySelector<HTMLElement>('[data-module-empty]');
    let filter = 'all';
    const apply = () => {
      let visible = 0;
      const query = search?.value.trim().toLocaleLowerCase() || '';
      center.querySelectorAll<HTMLElement>('[data-module-card]').forEach(card => {
        card.hidden = (filter !== 'all' && card.dataset.moduleControl !== filter) || !(card.textContent || '').toLocaleLowerCase().includes(query);
        if (!card.hidden) visible++;
      });
      if (count) count.textContent = `显示 ${visible} 个模块`;
      if (empty) empty.hidden = visible !== 0;
    };
    search?.addEventListener('input', apply);
    center.querySelectorAll<HTMLButtonElement>('[data-module-filter]').forEach(button => button.addEventListener('click', () => {
      filter = button.dataset.moduleFilter || 'all';
      center.querySelectorAll('[data-module-filter]').forEach(item => item.setAttribute('aria-pressed', String(item === button)));
      apply();
    }));
  }
  const form = document.querySelector<HTMLFormElement>('[data-layout-form]');
  const undo = document.querySelector<HTMLButtonElement>('[data-layout-undo]'), redo = document.querySelector<HTMLButtonElement>('[data-layout-redo]');
  if (!form || !undo || !redo) return;
  const locked = () => !!form.querySelector('fieldset')?.disabled;
  const sidebar = () => { const input = form.elements.namedItem('sidebar_enabled'); return input instanceof HTMLInputElement ? input : null; };
  form.querySelector('[data-sidebar-enable]')?.addEventListener('click', () => {
    const input = sidebar();
    if (locked() || !input) return;
    input.checked = true; window.ChunnelAdminUI?.syncSwitch(input);
    input.dispatchEvent(new Event('change', { bubbles: true })); input.focus();
  });
  const controls = (): FormControl[] => [...form.elements].filter((input): input is FormControl =>
    (input instanceof HTMLInputElement || input instanceof HTMLSelectElement || input instanceof HTMLTextAreaElement) && !!input.name && !['csrf_token', 'revision'].includes(input.name));
  type Snapshot = Record<string, string | boolean>;
  const capture = (): Snapshot => Object.fromEntries(controls().filter(input => !(input instanceof HTMLInputElement) || input.type !== 'radio' || input.checked)
    .map(input => [input.name, input instanceof HTMLInputElement && input.type === 'checkbox' ? input.checked : input.value]));
  let history = [capture()], cursor = 0, applying = false;
  const paint = () => {
    undo.disabled = locked() || cursor === 0; redo.disabled = locked() || cursor >= history.length - 1;
    const note = form.querySelector<HTMLElement>('[data-sidebar-state]'); if (note) note.hidden = sidebar()?.checked !== false;
  };
  const record = () => {
    if (applying || locked()) return;
    const state = capture();
    if (JSON.stringify(state) === JSON.stringify(history[cursor])) return;
    history = history.slice(0, cursor + 1); history.push(state);
    if (history.length > 50) history.shift();
    cursor = history.length - 1; paint();
  };
  const restore = (state: Snapshot) => {
    applying = true;
    try {
      controls().forEach(input => {
        if (!Object.hasOwn(state, input.name)) return;
        const value = state[input.name];
        if (input instanceof HTMLInputElement && input.type === 'checkbox') input.checked = value === true;
        else if (input instanceof HTMLInputElement && input.type === 'radio') input.checked = input.value === value;
        else input.value = String(value ?? '');
        window.ChunnelAdminUI?.syncSwitch(input);
        const range = input.closest('.admin-range-control')?.querySelector('[data-range-output]');
        if (range) range.textContent = input.value;
        const color = input.closest('.admin-color-control');
        if (color) {
          const label = color.querySelector('[data-color-output]'), copy = color.querySelector<HTMLElement>('[data-copy-value]');
          if (label) label.textContent = input.value; if (copy) copy.dataset.copyValue = input.value;
        }
      });
      document.dispatchEvent(new Event('appearance-preview-change'));
      form.dispatchEvent(new Event('change', { bubbles: true }));
    } finally { applying = false; paint(); }
  };
  form.addEventListener('change', () => queueMicrotask(() => { record(); paint(); document.dispatchEvent(new Event('appearance-preview-change')); }));
  form.addEventListener('reset', () => queueMicrotask(() => { record(); document.dispatchEvent(new Event('appearance-preview-change')); }));
  undo.addEventListener('click', () => { if (cursor > 0 && !locked()) restore(history[--cursor]!); });
  redo.addEventListener('click', () => { if (cursor < history.length - 1 && !locked()) restore(history[++cursor]!); });
  document.querySelector('[data-layout-defaults]')?.addEventListener('click', () => {
    if (locked()) return;
    const state = capture();
    try {
      if (typeof state.home_custom_blocks === 'string') state.home_custom_blocks = JSON.stringify(parseCustomBlocks(state.home_custom_blocks).map(block => ({ ...block, visibility: 'hidden' })));
    } catch (error) {
      const message = error instanceof Error ? error.message : 'HTML 组件格式不正确';
      window.ChunnelAdmin?.showToast(message, 'warning');
      const status = form.querySelector('[data-home-editor-status]'); if (status) status.textContent = message;
      return;
    }
    for (const [key, value] of Object.entries({ top: 'hero,notice,channels,categories', main: 'feed,series,spotlight,community', aside: 'welcome,checkin,recent,authors,tags,stats,subscribe' })) state[`home_${key}_order`] = value;
    Object.keys(state).filter(key => /^home_.+_visibility$/.test(key)).forEach(key => { state[key] = 'both'; });
    for (const key of ['carousel', 'search_banner', 'custom']) state[`home_${key}_visibility`] = 'hidden';
    if ('sidebar_enabled' in state) state.sidebar_enabled = true;
    restore(state); record();
  });
  paint();
})();
