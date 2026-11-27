import type { EditorSourceSelection } from './media-types';

export interface ComponentLibrary {
  open(launcher: HTMLElement, selection: EditorSourceSelection): void;
  edit(config: unknown, selection: EditorSourceSelection, launcher: HTMLElement): void;
  decode(value: string): unknown;
  markup(config: unknown): string;
  definitions?: { kind: string; name: string }[];
}
declare global {
  interface Window {
    NewPressComponentLibrary?: ComponentLibrary;
    NewPressComponentLoader?: { edit(encoded: string, selection: EditorSourceSelection, launcher: HTMLElement): Promise<void> };
  }
}
