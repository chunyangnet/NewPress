export type ColorTheme = 'light' | 'dark';
export type ColorMode = ColorTheme | 'system';

export interface ColorModeChange {
  mode: ColorMode;
  theme: ColorTheme;
}

export interface ColorModeAPI {
  set(value: ColorMode): void;
  readonly mode: ColorMode;
  /** Current root attribute; it changes when a pending reveal applies. */
  readonly theme: string | undefined;
}

export type ColorReveal = (origin: HTMLElement | false | undefined, apply: () => void) => void;
