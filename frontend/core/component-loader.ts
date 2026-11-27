import type { EditorSourceSelection } from './media-types';
import type { ComponentLibrary } from './component-types';

(() => {
  const entry = document.querySelector<HTMLScriptElement>('script[data-component-loader]');
  const dialog = document.querySelector<HTMLDialogElement>('[data-component-library]');
  const source = document.querySelector<HTMLTextAreaElement>('[data-content-source]');
  if (!entry || !dialog || !source) return;
  dialog.dataset.componentLazy = 'true';
  const feedback = document.querySelector<HTMLElement>('[data-composer-feedback]');
  let script: Promise<void> | null = null, style: Promise<void> | null = null, generation = 0;
  const say = (message: string) => { if (feedback) { feedback.textContent = message; feedback.hidden = !message; } };
  const locked = () => source.disabled || source.readOnly || source.matches(':disabled');
  function asset(kind: 'script' | 'style'): Promise<void> {
    const cached = kind === 'script' ? script : style;
    if (cached) return cached;
    const pending = new Promise<void>((resolve, reject) => {
      const url = kind === 'script' ? entry!.dataset.componentScript : entry!.dataset.componentStyle;
      if (!url) { reject(new Error('组件库资源未配置。')); return; }
      const node = kind === 'script' ? document.createElement('script') : document.createElement('link');
      if (node instanceof HTMLScriptElement) node.src = url; else { node.rel = 'stylesheet'; node.href = url; }
      let settled = false;
      const fail = () => { if (settled) return; settled = true; clearTimeout(timer); node.remove(); reject(new Error('组件库加载失败，请再次点击重试。')); };
      const timer = setTimeout(fail, 15000);
      node.addEventListener('error', fail, { once: true });
      node.addEventListener('load', () => { if (settled) return; settled = true; clearTimeout(timer); resolve(); }, { once: true });
      const anchor = kind === 'style' ? document.querySelector('[data-component-style-anchor]') : null;
      if (anchor) anchor.before(node); else document.head.append(node);
    }).catch(error => { if (kind === 'script') script = null; else style = null; throw error; });
    if (kind === 'script') script = pending; else style = pending;
    return pending;
  }
  async function open(launcher: HTMLElement, captured: EditorSourceSelection, encoded?: string): Promise<void> {
    if (locked() || dialog!.open || launcher.matches(':disabled')) return;
    const current = ++generation;
    launcher.setAttribute('aria-busy', 'true'); say('正在加载组件库…');
    try {
      await Promise.all([asset('style'), asset('script')]);
      if (current !== generation || !launcher.isConnected) return;
      if (locked() || source!.value !== captured.value) throw new Error('正文或编辑权限已变化，请重新选择位置。');
      const library: ComponentLibrary | undefined = window.NewPressComponentLibrary;
      if (!library?.open || !library.edit) { script = null; throw new Error('组件库初始化失败，请重试。'); }
      if (encoded === undefined) library.open(launcher, captured); else library.edit(library.decode(encoded), captured, launcher);
      say('');
    } catch (error) {
      if (current === generation) say(error instanceof Error ? error.message : '组件库加载失败，请重试。');
    } finally { launcher.removeAttribute('aria-busy'); }
  }
  document.querySelectorAll<HTMLButtonElement>('[data-component-library-open]').forEach(button => {
    button.addEventListener('pointerdown', event => event.preventDefault());
    button.addEventListener('click', () => {
      const selection = window.ChunnelBlockEditor?.capture();
      if (selection) void open(button, selection);
    });
  });
  window.NewPressComponentLoader = { edit: (encoded, selection, launcher) => open(launcher, selection, encoded) };
  window.addEventListener('pagehide', () => { generation++; });
})();
