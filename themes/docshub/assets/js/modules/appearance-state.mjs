const colorModes = new Set(['light', 'dark', 'system']);

export const validColorMode = value => colorModes.has(value) ? value : null;

// Preview pages and administrator-locked appearance never inherit browser choices.
export function colorPreference(defaultMode, storedMode, allowSwitch = true, preview = false) {
  return (allowSwitch && !preview && validColorMode(storedMode)) || validColorMode(defaultMode) || 'light';
}

export function resolvedColorMode(preference, systemDark = false) {
  return preference === 'dark' || (preference === 'system' && systemDark) ? 'dark' : 'light';
}
