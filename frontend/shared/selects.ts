// Simple fields use the native picker. Load the listbox only when it is needed.
import type { ChoiceMenuAPI } from './choice-types';

(() => {
  'use strict';
  if (window.NewPressSelects) return;
  const native = window.CSS?.supports('appearance', 'base-select') && 'HTMLSelectedContentElement' in window;
  const popover = typeof window.HTMLElement?.prototype.showPopover === 'function';
  if (!native && !popover) return;
  window.NewPressSelects = true;
  document.documentElement?.setAttribute('data-choice-controls', '');
  const entry = document.currentScript instanceof HTMLScriptElement ? document.currentScript.src : undefined;
  let loading: Promise<void> | undefined;
  let menu: ChoiceMenuAPI | undefined;
  let interaction = 0, failed = false;

  function loadMenu() {
    if (loading) return loading;
    const assets = [['link', '../css/chunnel-choice-menu.css'], ['script', 'chunnel-choice-menu.js']] as const;
    loading = Promise.all(assets.map(([tag, path]) => new Promise<void>((resolve, reject) => {
      const url = new URL(path, entry).href;
      const el = tag === 'link'
        ? Object.assign(document.createElement('link'), { rel: 'stylesheet', href: url })
        : Object.assign(document.createElement('script'), { src: url });
      el.onload = () => resolve();
      el.onerror = () => reject(new Error('Choice resources unavailable'));
      document.head.append(el);
    }))).then(() => {
      menu = window.NewPressChoiceMenu;
      if (!menu) throw new Error('Choice menu unavailable');
      document.querySelectorAll<HTMLSelectElement>('select[data-choice-menu]').forEach(menu.prepare);
    }).catch(() => {
      // Leave native controls available when optional assets cannot load.
      failed = true;
      document.querySelectorAll<HTMLSelectElement>('select[data-choice-menu]').forEach(prepare);
    });
    return loading;
  }

  function prepare(select: HTMLSelectElement) {
    const button = select.querySelector(':scope > button[data-select-display]');
    const single = !select.multiple && select.size <= 1;
    const eligible = !select.hasAttribute('data-icon-select') && (single || select.multiple);
    // The choice skin owns these fields across native and searchable modes.
    select.toggleAttribute('data-choice', eligible);
    const style = select.dataset?.selectUi;
    const search = style === 'searchable' || select.options?.length >= 12;
    const chips = select.multiple || style === 'tag' && !select.required;
    const rich = popover && !failed && eligible && (search || chips || !native || style === 'icon' || select.querySelector('optgroup'));
    if (rich) select.dataset.choiceMenu = search ? 'search' : 'list';
    else select.removeAttribute('data-choice-menu');
    if (menu) menu.prepare(select);
    else if (rich && chips) loadMenu();

    if (!native || !single || !eligible || rich || select.hidden) {
      select.removeAttribute('data-native-select');
      button?.remove();
      return;
    }
    let display = select.querySelector<HTMLButtonElement>(':scope > button');
    if (!display) {
      display = document.createElement('button');
      display.type = 'button';
      display.setAttribute('data-select-display', '');
      display.append(document.createElement('selectedcontent'));
      select.prepend(display);
    } else if (display === button && select.firstElementChild !== display) select.prepend(display);
    select.toggleAttribute('data-native-select', select.firstElementChild === display && !!display.querySelector('selectedcontent'));
  }

  if (popover) {
    const source = (target: EventTarget | null): HTMLSelectElement | null => {
      if (!(target instanceof Element)) return null;
      const select = target.closest('.np-choice-trigger')?.parentElement?.previousElementSibling
        || target.closest('select[data-choice-menu]');
      return select instanceof HTMLSelectElement ? select : null;
    };
    document.addEventListener('mousedown', event => { if (!event.button && source(event.target)) event.preventDefault(); });
    document.addEventListener('pointerdown', () => { interaction++; }, true);
    const activate = (event: MouseEvent | KeyboardEvent) => {
      const key = 'key' in event ? event.key : undefined;
      if (key === 'Escape' || key === 'Tab') interaction++;
      const select = source(event.target);
      if (!select || event.defaultPrevented || select.matches(':disabled, [aria-disabled="true"]')) return;
      if ('key' in event && (event.isComposing || event.ctrlKey || event.metaKey || !['Enter',' ','ArrowDown','ArrowUp','F4'].includes(event.key))) return;
      event.preventDefault();
      const options = {last: key === 'ArrowUp', keyboard: event.type === 'keydown' || event.detail === 0};
      if (menu) return menu.open(select, options);
      const request = ++interaction;
      select.focus({preventScroll:true});
      loadMenu().then(() => {
        if (request === interaction && select.isConnected && document.activeElement === select) menu?.open(select, options);
      });
    };
    document.addEventListener('click', activate);
    document.addEventListener('keydown', activate);
  }

  document.querySelectorAll<HTMLSelectElement>('select').forEach(prepare);
  document.addEventListener('reset', event => {
    const form = event.target;
    if (!(form instanceof HTMLFormElement)) return;
    // A trusted click can drain microtasks before the browser restores form defaults.
    setTimeout(() => {
      if (event.defaultPrevented) return;
      for (const select of form.elements) {
        if (!(select instanceof HTMLSelectElement)) continue;
        if (!select.multiple && select.size <= 1 && select.hasAttribute('data-native-select')) select.selectedIndex = select.selectedIndex;
        menu?.reset(select);
      }
    });
  }, true);
  function discover(node: Node, controls: Set<HTMLSelectElement>) {
    if (!(node instanceof Element)) return;
    if (node instanceof HTMLSelectElement) controls.add(node);
    else node.querySelectorAll<HTMLSelectElement>('select').forEach(select => controls.add(select));
  }
  new MutationObserver(records => {
    const controls = new Set<HTMLSelectElement>();
    for (const record of records) {
      const target = record.target;
      if (target instanceof HTMLSelectElement) controls.add(target);
      else if (target instanceof Element && target.tagName === 'FIELDSET' && record.attributeName === 'disabled') discover(target, controls);
      else if (popover && (target.nodeType === 3 || target instanceof Element && (target.tagName === 'OPTION' || target.tagName === 'OPTGROUP'))) {
        const owner = target.parentElement?.closest('select');
        if (owner) controls.add(owner);
      }
      if (record.type === 'childList') record.addedNodes.forEach(node => discover(node, controls));
    }
    controls.forEach(select => { if (select.isConnected !== false) prepare(select); });
    menu?.prune();
  }).observe(document.body, {subtree:true, childList:true, characterData:popover, attributes:true,
    attributeFilter:['multiple','size','selected','disabled','label','value','hidden','required','style','aria-disabled','aria-describedby','aria-invalid','data-select-ui','data-select-icon','data-max-selected']});
})();
