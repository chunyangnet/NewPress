import { bindNativeSubmission } from './admin-native-submit';
import type { AdminUI, FieldSnapshot, FormControl, Savebar, SavebarAppearance, SavebarOptions, SettingsNavigation } from './admin-types';
import { initializeWorkspaces } from './admin-workspaces';
import { initializeSwitches, syncSwitch } from './admin-switch';
import { node } from '../shared/dom';

// Some workbench pages introduce their first select only after adding a row.
(() => {
  const entry = document.currentScript?.getAttribute('data-choice-entry');
  if (!entry) return;
  const load = () => {
    const script = document.createElement('script');
    script.src = entry;
    document.head.append(script);
  };
  if (document.querySelector('select')) return load();
  const observer = new MutationObserver(records => {
    const addedSelect = records.some(record => [...record.addedNodes].some(node =>
      node instanceof Element && (node.matches('select') || node.querySelector('select'))));
    if (addedSelect) { observer.disconnect(); load(); }
  });
  observer.observe(document.body, { subtree: true, childList: true });
})();

(() => {
  const switchControl = (input: HTMLInputElement, title: string, help = '') => {
    const label = node('label', 'admin-switch admin-switch-field');
    input.type = 'checkbox'; input.setAttribute('role', 'switch');
    const track = node('span', 'admin-switch-track'); track.setAttribute('aria-hidden', 'true'); track.append(node('i'));
    const copy = node('span', 'admin-switch-copy'); copy.append(node('b', '', title));
    if (help) copy.append(node('small', '', help));
    label.append(input, track, copy); return label;
  };
  const capture = (form: HTMLFormElement): FieldSnapshot[] => [...form.querySelectorAll<FormControl>('input,select,textarea')]
    .filter(input => !input.hasAttribute('data-admin-transient') && (input.type !== 'hidden' || input.hasAttribute('data-admin-value')) && !['submit', 'button', 'file'].includes(input.type))
    .map(input => ({ input, value: input.value, checked: 'checked' in input ? input.checked : undefined,
      selected: 'options' in input && input.multiple ? [...input.options].map(o => o.selected) : null }));
  const signature = (snapshot: FieldSnapshot[]) => JSON.stringify(snapshot.map(({ value, checked, selected }) => [value, checked, selected]));
  const paintSavebar = (bar: HTMLElement | null, { dirty = false, busy = false, readOnly = false, disabled = false, message = '', error = false }: SavebarAppearance = {}) => {
    if (!bar) return;
    const wasSaving = bar.classList.contains('is-saving');
    const submit = bar.querySelector<HTMLButtonElement>('[data-admin-save]'), discard = bar.querySelector<HTMLButtonElement>('[data-admin-discard]'), output = bar.querySelector<HTMLElement>('[data-save-state]');
    if (!submit || !discard || !output) return;
    submit.dataset.defaultLabel ||= submit.textContent || '';
    submit.disabled = busy || readOnly || disabled || !dirty;
    submit.textContent = busy ? '保存中…' : submit.dataset.defaultLabel;
    discard.hidden = !dirty || readOnly; discard.disabled = busy || disabled;
    output.textContent = message || (readOnly ? '只读' : dirty ? '未保存' : '');
    output.dataset.tone = error ? 'error' : '';
    bar.classList.toggle('is-dirty', dirty); bar.classList.toggle('is-saving', busy);
    if (wasSaving && !busy && !error) document.dispatchEvent(new Event('newpress:feature-status-refresh'));
  };
  const savebars = new Map<HTMLFormElement, Savebar>();
  function savebar(form: HTMLFormElement | null, options: SavebarOptions = {}): Savebar | null {
    if (!form) return null;
    const existing = savebars.get(form);
    if (existing) return existing;
    const bar = options.bar || document.querySelector<HTMLElement>(`[data-admin-savebar][data-form-id="${form.getAttribute('id')}"]`);
    if (!bar) return null;
    const output = bar.querySelector<HTMLElement>('[data-save-state]');
    const submit = bar.querySelector<HTMLButtonElement>('[data-admin-save]');
    const discard = bar.querySelector<HTMLButtonElement>('[data-admin-discard]');
    if (!output || !submit || !discard) return null;
    const readOnly = bar.dataset.readonly === 'true' || !!form.querySelector('fieldset[disabled]');
    const initialLabel = submit.textContent;
    const initialMessage = output.dataset.initialMessage, initialTone = output.dataset.tone;
    type State = { kind: 'idle' } | { kind: 'saving'; snapshot: FieldSnapshot[]; departing: boolean } | { kind: 'disposed' };
    let state: State = { kind: 'idle' };
    let baseline = capture(form), feedbackTimer: ReturnType<typeof setTimeout> | undefined, submitTimer: ReturnType<typeof setTimeout> | undefined;
    const isDirty = () => signature(capture(form)) !== signature(baseline);
    const message = (text: string, tone = '') => { clearTimeout(feedbackTimer); output.textContent = text; output.dataset.tone = tone; };
    const refresh = (preserveFeedback = false) => {
      if (state.kind === 'disposed') return;
      const dirty = isDirty(), busy = state.kind === 'saving';
      bar.classList.toggle('is-dirty', dirty); bar.classList.toggle('is-saving', busy);
      submit.disabled = readOnly || busy || (!dirty && !options.allowClean);
      submit.textContent = busy ? '保存中…' : initialLabel;
      discard.hidden = !dirty || readOnly; discard.disabled = busy;
      form.setAttribute('aria-busy', String(busy));
      if (!busy && preserveFeedback !== true) message(readOnly ? '只读' : dirty ? '未保存' : '');
    };
    const changed = () => refresh();
    const invalid = (event: Event) => {
      const section = event.target instanceof Element ? event.target.closest('[data-settings-section]') : null;
      if (section) document.querySelector<HTMLElement>(`[data-settings-target="${section.getAttribute('id')}"]`)?.click();
    };
    const nativeSubmission = options.native ? bindNativeSubmission(form, {
      onPending() {
        if (state.kind !== 'idle') return;
        state = { kind: 'saving', snapshot: capture(form), departing: true };
        refresh();
      },
      onRestore() { state = { kind: 'idle' }; refresh(); },
    }) : null;
    const nativeSubmit = (event: SubmitEvent) => {
      if (state.kind !== 'idle' || readOnly) event.preventDefault();
    };
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (!nativeSubmission?.pending() && !(state.kind === 'saving' && state.departing) && (state.kind === 'saving' || isDirty())) { event.preventDefault(); event.returnValue = ''; }
    };
    const pageShow = (event: PageTransitionEvent) => {
      if (event.persisted) { clearTimeout(submitTimer); state = { kind: 'idle' }; refresh(); }
    };
    const api: Savebar = {
      dirty: isDirty, refresh,
      pending: () => state.kind === 'saving',
      start() { if (state.kind !== 'idle' || readOnly) return false; state = { kind: 'saving', snapshot: capture(form), departing: false }; refresh(); message(''); return true; },
      success(snapshot) {
        if (state.kind === 'disposed') return;
        baseline = snapshot || (state.kind === 'saving' ? state.snapshot : capture(form));
        state = { kind: 'idle' }; refresh();
        document.dispatchEvent(new Event('newpress:feature-status-refresh'));
        if (!isDirty()) { message('已保存', 'success'); feedbackTimer = setTimeout(() => message(''), 1800); }
      },
      fail(error) { if (state.kind === 'disposed') return; state = { kind: 'idle' }; refresh(); message(error || '保存失败，请重试', 'error'); },
      message,
      discard() {
        if (state.kind !== 'idle' || readOnly) return;
        for (const item of baseline) {
          item.input.value = item.value;
          if ('checked' in item.input && typeof item.checked === 'boolean') item.input.checked = item.checked;
          if (item.selected && 'options' in item.input) {
            const options = item.input.options;
            item.selected.forEach((selected, i) => { const option = options[i]; if (option) option.selected = selected; });
          }
          syncSwitch(item.input);
        }
        options.onDiscard?.(); form.dispatchEvent(new Event('input', { bubbles: true })); refresh();
      },
      snapshot: () => capture(form),
      dispose() {
        state = { kind: 'disposed' }; clearTimeout(feedbackTimer); clearTimeout(submitTimer);
        discard.removeEventListener('click', api.discard);
        form.removeEventListener('input', changed); form.removeEventListener('change', changed);
        form.removeEventListener('invalid', invalid, true); form.removeEventListener('submit', nativeSubmit);
        window.removeEventListener('beforeunload', beforeUnload); window.removeEventListener('pageshow', pageShow);
        nativeSubmission?.dispose();
        savebars.delete(form);
      }
    };
    discard.addEventListener('click', api.discard);
    form.addEventListener('input', changed); form.addEventListener('change', changed);
    form.addEventListener('invalid', invalid, true);
    if (options.native) form.addEventListener('submit', nativeSubmit);
    window.addEventListener('beforeunload', beforeUnload); window.addEventListener('pageshow', pageShow);
    savebars.set(form, api); refresh();
    if (initialMessage) {
      message(initialMessage, initialTone);
      if (initialTone === 'success') feedbackTimer = setTimeout(() => { if (!isDirty()) message(''); }, 1800);
    }
    return api;
  }
  const navigations = new WeakMap<Document | HTMLElement, SettingsNavigation>();
  function settingsNavigation(root: Document | HTMLElement = document): SettingsNavigation {
    const current = navigations.get(root);
    if (current) { current.refresh(); return current; }
    let sections: HTMLElement[] = [], links: HTMLElement[] = [], active = '';
    const positions = new Map<string, number>();
    // Named form controls can shadow DOM properties, including id.
    const sectionID = (section: HTMLElement) => section.getAttribute('id') || '';
    const activate = (id: string, updateHash = false, preserve = false) => {
      const target = sections.find(section => sectionID(section) === id || sectionID(section).replace(/^(site-settings-|settings-)/, '') === id) || sections.find(section => sectionID(section) === links[0]?.dataset.settingsTarget) || sections[0];
      if (!target) return;
      const nextID = sectionID(target), changed = active !== nextID;
      if (changed && active) positions.set(active, window.scrollY || 0);
      const top = changed ? (positions.get(nextID) ?? 0) : (window.scrollY || 0);
      const change = () => {
        active = sectionID(target);
        sections.forEach(section => { section.hidden = section !== target; });
        links.forEach(link => { const selected = link.dataset.settingsTarget === active; link.classList.toggle('is-active', selected); if (selected) link.setAttribute('aria-current', 'true'); else link.removeAttribute('aria-current'); });
        if (updateHash) history.replaceState(history.state, '', '#' + active);
        document.dispatchEvent(new CustomEvent('appearance-preview-section', { detail: active }));
      };
      if (preserve && window.ChunnelScroll) window.ChunnelScroll.keep(change, { top });
      else {
        change();
        if (preserve && changed) window.scrollTo({ top, left: window.scrollX || 0, behavior: 'instant' });
      }
    };
    const refresh = () => {
      sections = [...root.querySelectorAll<HTMLElement>('[data-settings-section]')];
      links = [...root.querySelectorAll<HTMLElement>('[data-settings-target]')];
      const error = sections.find(section => section.querySelector('.has-error, .admin-field-error'));
      activate((error && sectionID(error)) || active || location.hash.slice(1));
    };
    root.addEventListener('click', event => {
      const link = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-settings-target]') : null;
      if (link && links.includes(link)) { event.preventDefault(); activate(link.dataset.settingsTarget || '', true, true); }
    });
    root.addEventListener('invalid', event => {
      const section = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-settings-section]') : null;
      if (section && sections.includes(section)) activate(sectionID(section), true);
    }, true);
    window.addEventListener('hashchange', () => activate(location.hash.slice(1), false, true));
    const api = { activate, refresh }; navigations.set(root, api); refresh(); return api;
  }
  window.ChunnelAdminUI = Object.freeze({ switchControl, syncSwitch, savebar, paintSavebar, settingsNavigation } satisfies AdminUI);
  const initializeSavebars = () => document.querySelectorAll<HTMLFormElement>('form[data-admin-native-save]').forEach(form => savebar(form, { native: true, allowClean: form.hasAttribute('data-admin-allow-clean') }));
  initializeSavebars();
  document.addEventListener('chunnel:admin-updated', initializeSavebars);
  document.addEventListener('chunnel:admin-before-update', event => {
    for (const [form, api] of savebars) if (event.detail.regions.some(region => region === form || region.contains(form)) && (api.dirty() || api.pending())) { event.preventDefault(); break; }
  });
  document.addEventListener('chunnel:admin-before-replace', event => {
    for (const [form, api] of savebars) if (event.detail.regions.some(region => region === form || region.contains(form))) api.dispose();
  });
  initializeSwitches();

  const main = document.querySelector<HTMLElement>('.admin-main');
  let docks = [...document.querySelectorAll<HTMLElement>('.admin-save-dock')];
  if (main && docks.length && window.ResizeObserver) {
    const reserveDockSpace = () => {
      const height = Math.max(0, ...docks.map(dock => dock.getBoundingClientRect().height));
      main.style.setProperty('--admin-ui-dock-space', `${Math.ceil(height)}px`);
    };
    const observer = new ResizeObserver(reserveDockSpace);
    docks.forEach(dock => observer.observe(dock)); reserveDockSpace();
    document.addEventListener('chunnel:admin-updated', () => {
      observer.disconnect(); docks = [...document.querySelectorAll<HTMLElement>('.admin-save-dock')];
      docks.forEach(dock => observer.observe(dock)); reserveDockSpace();
    });
  }
  initializeWorkspaces();
})();
