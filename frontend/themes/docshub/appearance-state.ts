const colorModes = new Set(['light', 'dark', 'system']);

export const validColorMode = (value: unknown): string | null => typeof value === 'string' && colorModes.has(value) ? value : null;

// Preview pages and administrator-locked appearance never inherit browser choices.
export function colorPreference(defaultMode: unknown, storedMode: unknown, allowSwitch = true, preview = false) {
  return (allowSwitch && !preview && validColorMode(storedMode)) || validColorMode(defaultMode) || 'light';
}

export function resolvedColorMode(preference: string, systemDark = false) {
  return preference === 'dark' || (preference === 'system' && systemDark) ? 'dark' : 'light';
}
