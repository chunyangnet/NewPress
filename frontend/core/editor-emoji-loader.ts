import { editorScriptLoader } from './editor-script-loader';

(() => {
  const entry = document.querySelector<HTMLScriptElement>('script[data-editor-emoji-script]');
  const form = document.querySelector<HTMLFormElement>('[data-content-editor]');
  const source = form?.querySelector<HTMLTextAreaElement>('[data-content-source]');
  const launcher = form?.querySelector<HTMLButtonElement>('[data-emoji-trigger][data-emoji-target="content-source"]');
  const status = form?.querySelector<HTMLElement>('[data-editor-emoji-status]');
  if (!entry || !form || !source || !launcher || !status) return;
  const script = editorScriptLoader(() => entry.dataset.editorEmojiScript, '表情', '表情加载失败，请再次点击重试。', 'emoji-retry');
  let owner: MutationObserver | null = null;
  let generation = 0, suspended = false, restoreBusy: (() => void) | null = null;
  const say = (message: string) => { status.textContent = message; status.hidden = !message; };
  const ready = () => !!window.ChunnelEmoji;
  const available = () => !suspended && form.isConnected && source.isConnected && launcher.isConnected &&
    !source.disabled && !source.readOnly && !source.matches(':disabled') && !launcher.matches(':disabled') &&
    !source.closest('[hidden], [inert], dialog:not([open])') && !launcher.closest('[inert]') && source.getClientRects().length > 0 &&
    getComputedStyle(source).visibility === 'visible' && getComputedStyle(launcher).visibility === 'visible' && launcher.getClientRects().length > 0;
  const cancel = () => { generation++; owner?.disconnect(); owner = null; restoreBusy?.(); restoreBusy = null; say(''); };
  async function activate(keyboard: boolean): Promise<void> {
    if (!available()) return;
    const value = source!.value, start = source!.selectionStart, end = source!.selectionEnd, direction = source!.selectionDirection;
    const current = ++generation;
    owner?.disconnect();
    restoreBusy?.();
    const busy = launcher!.getAttribute('aria-busy');
    const restore = () => { if (busy === null) launcher!.removeAttribute('aria-busy'); else launcher!.setAttribute('aria-busy', busy); };
    restoreBusy = restore; launcher!.setAttribute('aria-busy', 'true'); say('正在加载表情…');
    // Hiding and reopening the source or locking and unlocking it invalidates
    // the old intent even when its final value and selection happen to match.
    owner = new MutationObserver(cancel);
    const watched = new Set<HTMLElement>();
    for (const startNode of [source!, launcher!]) {
      for (let node: HTMLElement | null = startNode; node && !watched.has(node); node = node.parentElement) {
        watched.add(node);
        owner.observe(node, { attributes: true, childList: node !== source && node !== launcher,
          attributeFilter: ['hidden', 'disabled', 'readonly', 'inert', 'data-editor-mode'] });
        if (node === form) break;
      }
    }
    try {
      await script.load();
      if (current !== generation) return;
      if (!available()) { say(''); return; }
      if (source!.value !== value || source!.selectionStart !== start || source!.selectionEnd !== end ||
          document.querySelector('dialog[open]')) { say('正文或选区已变化，请重新打开表情。'); return; }
      if (!ready()) {
        script.reset();
        throw new Error('表情初始化失败，请再次点击重试。');
      }
      source!.setSelectionRange(start, end, direction);
      say('');
      launcher!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, detail: keyboard ? 0 : 1 }));
    } catch (error) {
      if (current === generation && !suspended) say(error instanceof Error ? error.message : '表情加载失败，请重试。');
    } finally {
      if (current === generation) { owner?.disconnect(); owner = null; restore(); restoreBusy = null; }
    }
  }
  document.addEventListener('click', event => {
    if (!(event.target instanceof Element) || event.target.closest('[data-emoji-trigger]') !== launcher || ready()) return;
    event.preventDefault(); event.stopImmediatePropagation(); void activate(event.detail === 0);
  });
  document.addEventListener('pointerdown', event => {
    if (restoreBusy && event.target instanceof Node && !launcher.contains(event.target)) cancel();
  }, true);
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && !event.isComposing && event.keyCode !== 229 && restoreBusy) cancel();
  }, true);
  source.addEventListener('input', () => { if (restoreBusy) cancel(); });
  source.addEventListener('compositionstart', () => { if (restoreBusy) cancel(); });
  window.addEventListener('pagehide', () => { suspended = true; cancel(); });
  window.addEventListener('pageshow', () => { suspended = false; });
})();
