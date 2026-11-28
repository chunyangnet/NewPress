import type { EditorSourceSelection } from './media-types';

export type EditorToolOpener = (selection?: EditorSourceSelection) => void;

declare global {
  interface Window {
    NewPressEditorTools?: WeakMap<HTMLButtonElement, EditorToolOpener>;
  }
}

// A retried script cannot bind the same launcher twice; replacement forms get
// their own registrations instead of reusing an old page's tool.
export function editorToolReady(launcher: HTMLButtonElement): boolean {
  return window.NewPressEditorTools?.has(launcher) === true;
}

export function registerEditorTool(
  launchers: readonly HTMLButtonElement[],
  open: (launcher: HTMLButtonElement, selection?: EditorSourceSelection) => void,
  preserveSelection = true,
): void {
  const tools = window.NewPressEditorTools ??= new WeakMap();
  const lazy = document.querySelector('script[data-editor-tools-loader]');
  for (const launcher of launchers) {
    if (tools.has(launcher)) continue;
    tools.set(launcher, selection => open(launcher, selection));
    // Other owners, including the shop editor, retain their eager entry.
    if (lazy) continue;
    if (preserveSelection) launcher.addEventListener('pointerdown', event => event.preventDefault());
    launcher.addEventListener('click', () => open(launcher));
  }
}
