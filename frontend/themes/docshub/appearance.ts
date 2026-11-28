import { $, $$ } from './shared';
import { colorPreference, resolvedColorMode, validColorMode } from './appearance-state';

export function initAppearance() {
  const root = document.documentElement;
  const system = window.matchMedia('(prefers-color-scheme: dark)');
  const allowSwitch = root.dataset.allowColorSwitch !== 'false';
  const preview = root.dataset.appearancePreview === 'true';
  let storedMode;
  try { storedMode = localStorage.getItem('docshub:mode'); } catch {}
  let preference = colorPreference(root.dataset.defaultMode, storedMode, allowSwitch, preview);
  const sync = () => {
    root.dataset.colorPreference = preference;
    root.dataset.mode = resolvedColorMode(preference, system.matches);
    $$('[data-color-mode]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.colorMode === preference)));
    $('meta[name="theme-color"]')?.setAttribute('content', root.dataset.mode === 'dark' ? '#111523' : '#f5f6fc');
  };
  sync();
  const controls = $('[data-appearance-controls]');
  if (controls) controls.hidden = !allowSwitch;
  $$('[data-color-mode]').forEach(button => button.addEventListener('click', () => {
    const next = validColorMode(button.dataset.colorMode);
    if (!allowSwitch || !next) return;
    preference = next;
    sync();
    if (!preview) { try { localStorage.setItem('docshub:mode', preference); } catch {} }
  }));
  system.addEventListener('change', sync);
  window.addEventListener('storage', event => {
    if (allowSwitch && !preview && (event.key === 'docshub:mode' || event.key === null)) {
      preference = colorPreference(root.dataset.defaultMode, event.newValue);
      sync();
    }
  });
}
