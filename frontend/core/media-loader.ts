import { installMediaEntrypoints } from './media-entrypoints';
import { editorScriptLoader } from './editor-script-loader';
import type { MediaOpenRequest } from './media-types';

(() => {
  const entry = document.querySelector<HTMLScriptElement>('script[data-editor-media-loader]');
  const dialog = document.querySelector<HTMLDialogElement>('[data-editor-media-dialog]');
  if (!entry || !dialog) return;
  const feedback = document.querySelector<HTMLElement>('[data-composer-feedback]');
  const script = editorScriptLoader(() => entry.dataset.mediaScript, '图片工具', '图片工具加载失败，输入和待上传文件已保留。');
  let serial = 0, suspended = false;
  let notice: HTMLElement | null = null, restoreBusy: (() => void) | null = null;
  function clearNotice() {
    notice?.remove(); notice = null;
    if (feedback && !feedback.textContent?.trim()) feedback.hidden = true;
  }
  function cancel() { serial++; restoreBusy?.(); restoreBusy = null; clearNotice(); }
  function say(request: MediaOpenRequest, message: string, retry = false) {
    clearNotice();
    notice = document.createElement('span'); notice.className = 'admin-media-field-status'; notice.setAttribute('role', 'status');
    notice.textContent = message + ' ';
    const button = (label: string, action: () => void) => {
      const item = document.createElement('button'); item.type = 'button'; item.className = 'admin-button admin-button-quiet';
      item.textContent = label; item.addEventListener('click', action); notice!.append(item);
    };
    if (retry) button('重试加载图片工具', () => { void activate(request); });
    button('取消', cancel);
    if (feedback && request.mode === 'body') { feedback.replaceChildren(notice); feedback.hidden = false; }
    else request.launcher.after(notice);
  }
  async function activate(request: MediaOpenRequest): Promise<void> {
    if (suspended || dialog!.open) return;
    if (!request.valid()) { say(request, '正文、封面或编辑权限已变化，请重新选择插入位置。'); return; }
    const current = ++serial;
    restoreBusy?.(); restoreBusy = null; clearNotice();
    const launcher = request.launcher, owner = launcher.closest('dialog');
    const ownerClosed = () => { if (current === serial) cancel(); };
    owner?.addEventListener('close', ownerClosed, { once: true });
    const openDialogs = new Set(document.querySelectorAll('dialog[open]'));
    const previous = launcher.getAttribute('aria-busy');
    restoreBusy = () => { if (previous === null) launcher.removeAttribute('aria-busy'); else launcher.setAttribute('aria-busy', previous); };
    launcher.setAttribute('aria-busy', 'true');
    try {
      if (window.NewPressMediaRuntime?.dialog !== dialog) { say(request, '正在加载图片工具…'); await script.load(); }
      if (current !== serial || suspended || !dialog!.isConnected || !launcher.isConnected) return;
      if (!request.valid()) throw new Error('正文、封面或编辑权限已变化，请重新选择插入位置；文件没有上传。');
      if (owner && !owner.open || [...document.querySelectorAll('dialog[open]')].some(item => !openDialogs.has(item))) { clearNotice(); return; }
      const runtime = window.NewPressMediaRuntime;
      if (runtime?.dialog !== dialog) {
        script.reset();
        throw new Error('图片工具初始化失败，输入和待上传文件已保留。');
      }
      restoreBusy?.(); restoreBusy = null; clearNotice();
      await runtime.open(request);
    } catch (error) {
      if (current === serial && !suspended) say(request, error instanceof Error ? error.message : '图片工具未能加载，文件仍保留。', request.valid());
    } finally {
      owner?.removeEventListener('close', ownerClosed);
      if (current === serial) { restoreBusy?.(); restoreBusy = null; }
    }
  }
  installMediaEntrypoints(activate);
  document.addEventListener('keydown', event => { if (event.key === 'Escape' && (restoreBusy || notice)) cancel(); });
  window.addEventListener('pagehide', () => { suspended = true; cancel(); });
  window.addEventListener('pageshow', () => { suspended = false; });
})();
