export type FormControl = HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;

export interface AdminAPI {
  icon(name: string): SVGSVGElement;
  showToast(message: string, tone?: string): void;
}

export interface FieldSnapshot {
  input: FormControl;
  value: string;
  checked: boolean | undefined;
  selected: boolean[] | null;
}

export interface SavebarOptions {
  bar?: HTMLElement | null;
  native?: boolean;
  allowClean?: boolean;
  onDiscard?: () => void;
}

export interface SavebarAppearance {
  dirty?: boolean;
  busy?: boolean;
  readOnly?: boolean;
  disabled?: boolean;
  message?: string;
  error?: boolean;
}

export interface Savebar {
  dirty(): boolean;
  pending(): boolean;
  refresh(preserveFeedback?: boolean): void;
  start(): boolean;
  success(snapshot?: FieldSnapshot[]): void;
  fail(message?: string): void;
  message(text: string, tone?: string): void;
  discard(): void;
  snapshot(): FieldSnapshot[];
  dispose(): void;
}

export interface SettingsNavigation {
  activate(id: string, updateHash?: boolean, preserve?: boolean): void;
  refresh(): void;
}

export interface AdminUI {
  switchControl(input: HTMLInputElement, title: string, help?: string): HTMLLabelElement;
  syncSwitch(input: FormControl): void;
  savebar(form: HTMLFormElement | null, options?: SavebarOptions): Savebar | null;
  paintSavebar(bar: HTMLElement | null, state?: SavebarAppearance): void;
  settingsNavigation(root?: Document | HTMLElement): SettingsNavigation;
}

export interface AdminRegionChange { regions: Element[]; url?: string }
