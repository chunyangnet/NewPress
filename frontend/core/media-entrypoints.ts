import type { ComponentMediaSelection, MediaOpenRequest, MediaPickerOptions } from './media-types';

// Kept in the initial page so paste/drop, cover, component and field callers
// capture their intent before the optional dialog code is downloaded.
export function installMediaEntrypoints(activate: (request: MediaOpenRequest) => Promise<void>): void {
  const form = document.querySelector<HTMLFormElement>('[data-content-editor]');
  const dialog = document.querySelector<HTMLDialogElement>('[data-editor-media-dialog]');
  if (!dialog) return;
  const source = form?.querySelector<HTMLTextAreaElement>('[data-content-source]');
  const cover = form?.querySelector<HTMLInputElement>('[name="cover_url"]');
  const locked = (input: HTMLInputElement | HTMLTextAreaElement | null | undefined) => !!input && (!input.isConnected || input.disabled || input.readOnly || input.matches(':disabled'));

  function open(launcher: HTMLElement, file: File | null = null, pick: MediaPickerOptions | null = null,
    component: ComponentMediaSelection | null = null, error = ''): Promise<void> {
    const mode = pick ? 'field' : component ? 'component' : launcher.dataset.editorMediaOpen;
    if (mode !== 'body' && mode !== 'cover' && mode !== 'component' && mode !== 'field') return Promise.resolve();
    if (dialog!.open || launcher.matches(':disabled') || launcher.closest('[inert]') ||
        (mode !== 'field' && locked(source)) || mode === 'body' && !source || mode === 'cover' && (!cover || locked(cover)) || pick?.disabled?.()) return Promise.resolve();
    const editor = window.ChunnelBlockEditor;
    const selection = mode === 'body' && source ? editor?.capture() || { start: source.selectionStart, end: source.selectionEnd, value: source.value } : null;
    const previousCover = cover?.value;
    const valid = () => launcher.isConnected && !launcher.matches(':disabled') && !launcher.closest('[inert]') &&
      (mode === 'field' ? !pick?.disabled?.() : !locked(source)) &&
      (mode !== 'body' || !!source && source.value === selection?.value && editor === window.ChunnelBlockEditor) &&
      (mode !== 'cover' || !!cover && !locked(cover) && cover.value === previousCover);
    return activate({ launcher, mode, file, pick, selection, component, error, valid });
  }
  form?.querySelectorAll<HTMLElement>('[data-editor-media-open]').forEach(button => {
    button.addEventListener('pointerdown', event => event.preventDefault());
    button.addEventListener('click', () => { void open(button); });
  });
  window.NewPressMediaPicker = Object.freeze({
    open(button: HTMLElement, options: MediaPickerOptions) {
      if (!button || typeof options?.onSelect !== 'function') return;
      return open(button, options.file || null, { uploadSource: 'settings-image', ...options });
    },
  });
  window.NewPressComponentMedia = (button, callback) => { void open(button, null, null, callback); };

  const bodyLauncher = form?.querySelector<HTMLElement>('[data-editor-media-open="body"]');
  const bodyTarget = (target: EventTarget | null) => target === source || target instanceof Element && !!target.closest('[data-emoji-editor="content-source"], [data-composer]');
  const canUpload = () => !!bodyLauncher && !!source && !locked(source) && dialog!.dataset.canUpload === 'true';
  // Only FileList input is intercepted. Ordinary text and HTML use the editor's
  // existing paste rules, without downloading the media dialog.
  form?.addEventListener('paste', event => {
    if (!canUpload() || !bodyTarget(event.target)) return;
    const files = [...(event.clipboardData?.files || [])];
    if (!files.length) return;
    event.preventDefault(); event.stopImmediatePropagation();
    void open(bodyLauncher!, files.length === 1 ? files[0]! : null, null, null, files.length === 1 ? '' : '一次粘贴仅接受一张图片');
  }, true);
  form?.addEventListener('dragover', event => {
    if (!bodyTarget(event.target) || !event.dataTransfer?.types.includes('Files')) return;
    event.preventDefault(); event.dataTransfer.dropEffect = canUpload() ? 'copy' : 'none';
  });
  form?.addEventListener('drop', event => {
    if (!bodyTarget(event.target) || !event.dataTransfer?.files.length) return;
    event.preventDefault(); event.stopImmediatePropagation();
    if (!canUpload()) return;
    const files = [...event.dataTransfer.files];
    void open(bodyLauncher!, files.length === 1 ? files[0]! : null, null, null, files.length === 1 ? '' : '一次拖入仅接受一张图片');
  }, true);
}
