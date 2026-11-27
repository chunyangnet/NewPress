import { colorPreference, resolvedColorMode } from './modules/appearance-state.mjs';

// Run before stylesheets to avoid a light flash. Storage may be unavailable.
const root = document.documentElement;
let storedMode;
try { storedMode = localStorage.getItem('docshub:mode'); } catch {}
const preference = colorPreference(root.dataset.defaultMode, storedMode, root.dataset.allowColorSwitch !== 'false', root.dataset.appearancePreview === 'true');
root.dataset.colorPreference = preference;
root.dataset.mode = resolvedColorMode(preference, window.matchMedia('(prefers-color-scheme: dark)').matches);
