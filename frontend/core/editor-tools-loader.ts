import type { EditorSourceSelection } from './media-types';
import type { EditorToolOpener } from './editor-tool-launch';
import { savedSnippetDraft, snippetDraftStorageKey } from './editor-snippet-draft-state';
import { editorScriptLoader } from './editor-script-loader';

(() => {
  const entry = document.querySelector<HTMLScriptElement>('script[data-editor-tools-loader]');
  const form = document.querySelector<HTMLFormElement>('[data-content-editor]');
  const source = form?.querySelector<HTMLTextAreaElement>('[data-content-source]');
  if (!entry || !form || !source) return;
  const feedback = document.querySelector<HTMLElement>('[data-composer-feedback]');
  const launchers = [...form.querySelectorAll<HTMLButtonElement>(
    '[data-editor-reuse-open], [data-editor-snippets-open], [data-editor-widget-open], [data-snippet-draft-open]',
  )];
  const script = editorScriptLoader(() => entry.dataset.editorToolsScript, '插入工具', '插入工具加载失败，请再次点击重试。');
  let generation = 0, suspended = false;
  let restoreBusy: (() => void) | null = null;
  const listeners = new AbortController();
  const say = (message: string) => {
    if (feedback) { feedback.textContent = message; feedback.hidden = !message; }
  };
  const locked = () => source.disabled || source.readOnly || source.matches(':disabled');

  async function activate(launcher: HTMLButtonElement): Promise<void> {
    const savedDraft = launcher.hasAttribute('data-snippet-draft-open');
    if (suspended || launcher.matches(':disabled') || !savedDraft && locked()) return;
    const editor = window.ChunnelBlockEditor;
    const captured: EditorSourceSelection | undefined = savedDraft ? undefined : editor?.capture();
    if (!savedDraft && !captured) return;
    const current = ++generation;
    restoreBusy?.();
    const previousBusy = launcher.getAttribute('aria-busy');
    const restore = () => {
      if (previousBusy === null) launcher.removeAttribute('aria-busy');
      else launcher.setAttribute('aria-busy', previousBusy);
    };
    restoreBusy = restore;
    launcher.setAttribute('aria-busy', 'true');
    try {
      let open: EditorToolOpener | undefined = window.NewPressEditorTools?.get(launcher);
      if (!open) { say('正在加载插入工具…'); await script.load(); }
      if (current !== generation || suspended || !form!.isConnected || !source!.isConnected || !launcher.isConnected) return;
      if (launcher.matches(':disabled') || captured && (locked() || source!.value !== captured.value || editor !== window.ChunnelBlockEditor)) {
        throw new Error('正文或编辑权限已变化，请重新选择插入位置。');
      }
      if (document.querySelector('dialog[open]')) { say(''); return; }
      open = window.NewPressEditorTools?.get(launcher);
      if (!open) {
        script.reset();
        throw new Error('插入工具初始化失败，请再次点击重试。');
      }
      open(captured); say('');
    } catch (error) {
      if (current === generation && !suspended) say(error instanceof Error ? error.message : '插入工具加载失败，请重试。');
    } finally {
      if (current === generation) { restore(); restoreBusy = null; }
    }
  }

  for (const launcher of launchers) {
    if (!launcher.hasAttribute('data-snippet-draft-open')) launcher.addEventListener('pointerdown', event => event.preventDefault(), { signal: listeners.signal });
    launcher.addEventListener('click', () => { void activate(launcher); }, { signal: listeners.signal });
  }
  document.addEventListener('keydown', event => {
    if (event.key !== 'Escape' || !restoreBusy) return;
    generation++; restoreBusy(); restoreBusy = null; say('');
  }, { signal: listeners.signal });
  // Retain the warning for an uncertain creation before any tool is downloaded.
  const draftLauncher = launchers.find(launcher => launcher.hasAttribute('data-snippet-draft-open'));
  const draftID = Number(draftLauncher?.dataset.post), login = form.dataset.editorLogin || '';
  const draftKey = snippetDraftStorageKey(Number(form.dataset.editorUserId || 0), login, draftID);
  window.addEventListener('beforeunload', event => {
    if (draftLauncher && login && Number.isSafeInteger(draftID) && draftID > 0 &&
        !window.NewPressEditorTools?.has(draftLauncher) && savedSnippetDraft(draftKey, draftID)) {
      event.preventDefault(); event.returnValue = '';
    }
  });
  window.addEventListener('pagehide', event => {
    suspended = true; generation++; restoreBusy?.(); restoreBusy = null;
    say('');
    if (!event.persisted) listeners.abort();
  });
  window.addEventListener('pageshow', () => { suspended = false; });
})();
