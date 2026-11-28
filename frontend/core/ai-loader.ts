import type {} from './ai-types';
import type {} from '../shared/account-types';
import { failure } from '../shared/ledger-data';
export function createAssetLoader(doc: Pick<Document,'createElement'|'head'>) {
    const pending = new Map<string,Promise<void>>();
    return (url: string | undefined, kind: 'style'|'script'): Promise<void> => {
      if (!url) return Promise.reject(new Error('AI 工具资源地址缺失，请刷新页面。'));
      const key = `${kind}:${url}`;
      const previous=pending.get(key);if(previous)return previous;
      const ready = new Promise<void>((resolve, reject) => {
        const node = doc.createElement(kind === 'style' ? 'link' : 'script');
        if (kind === 'style') { const link=node as HTMLLinkElement;link.rel = 'stylesheet'; link.href = url; }
        else { const script=node as HTMLScriptElement;script.src = url; script.async = true; }
        const failed=()=>{clearTimeout(timer);node.onload=null;node.onerror=null;pending.delete(key);node.remove();reject(new Error('AI 工具加载失败，请再次点击重试。'));};
        const timer=setTimeout(failed,30000);
        node.onload = () => {clearTimeout(timer);node.onload=null;node.onerror=null;resolve();};
        node.onerror = failed;
        doc.head.append(node);
      });
      pending.set(key, ready);
      return ready;
    };
  }
(() => {
  if (typeof document === 'undefined') return;
  const config = document.querySelector<HTMLElement>('[data-ai-assets]');
  if (!config) return;
  const assets = config.dataset, load = createAssetLoader(document);
  let retired=false;const owner=config.dataset.userId,csrf=config.dataset.csrf;
  const retire=()=>{retired=true;};
  document.addEventListener('chunnel:session',event=>{const session=event.detail;if(session?.authenticated!==true||String(session.id||'')!==owner||session.csrf_token!==csrf)retire();});
  window.addEventListener('pagehide',retire);
  window.addEventListener('pageshow',event=>{if(event.persisted)retire();});
  const current=()=>!retired&&config.isConnected;
  const status = document.createElement('span');
  status.className = 'ai-load-status'; status.setAttribute('role', 'status');
  let opening = false, textReady = false, imageReady = false;
  document.addEventListener('click', async event => {
    const button = event.target instanceof Element ? event.target.closest<HTMLButtonElement>('[data-ai-open],[data-ai-image-open]') : null;
    if (!button || button.disabled) return;
    if(!current()){event.preventDefault();event.stopImmediatePropagation();button.after(status);status.textContent='登录状态或页面已变化，请重新载入后继续。';return;}
    const image = button.hasAttribute('data-ai-image-open');
    if (!opening && (image ? imageReady : textReady)) return;
    event.preventDefault(); event.stopImmediatePropagation();
    if (opening) return;
    opening = true; button.setAttribute('aria-busy', 'true');
    button.after(status); status.textContent = '正在打开 AI 工具…';
    let replay = false;
    try {
      // The image workbench shares the writing dialog's parser and metrics.
      await Promise.all([load(assets.textStyle, 'style'), load(assets.textScript, 'script')]);
      if(!current())return;
      if (!window.ChunnelAIEditorReady) throw new Error('AI 编辑器暂不可用，请刷新页面后重试。');
      textReady = true;
      if (image) {
        await Promise.all([load(assets.imageStyle, 'style'), load(assets.imageScript, 'script')]);
        if(!current())return;
        if (!window.ChunnelAIImagesReady) throw new Error('AI 生图暂不可用，请刷新页面后重试。');
        imageReady = true;
      }
      status.textContent = '';
      replay = true;
    } catch (error) { if(current())status.textContent = failure(error).message; }
    finally { opening = false; button.removeAttribute('aria-busy'); }
    if (replay && current() && button.isConnected && !button.disabled) button.click();
  }, true);
})();
