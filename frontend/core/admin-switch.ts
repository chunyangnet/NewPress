import type { FormControl } from './admin-types';

export const syncSwitch = (input: FormControl) => {
  const state = input.closest?.('.admin-switch')?.querySelector('[data-switch-state]');
  if (state && 'checked' in input) state.textContent = input.checked ? '开启' : '关闭';
};

export function initializeSwitches() {
  document.addEventListener('change', event => {
    if (event.target instanceof HTMLInputElement && event.target.matches('.admin-switch input')) syncSwitch(event.target);
  });
  document.addEventListener('reset', event => {
    const form = event.target;
    if (form instanceof HTMLFormElement) queueMicrotask(() => form.querySelectorAll<HTMLInputElement>('.admin-switch input').forEach(syncSwitch));
  });
  document.querySelectorAll<HTMLInputElement>('.admin-switch input').forEach(syncSwitch);
}
