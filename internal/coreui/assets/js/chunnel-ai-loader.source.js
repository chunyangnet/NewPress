(() => {
  'use strict';
  function createAssetLoader(doc) {
    const pending = new Map();
    return (url, kind) => {
      if (!url) return Promise.reject(new Error('AI 工具资源地址缺失，请刷新页面。'));
      const key = `${kind}:${url}`;
      if (pending.has(key)) return pending.get(key);
      const ready = new Promise((resolve, reject) => {
        const node = doc.createElement(kind === 'style' ? 'link' : 'script');
        if (kind === 'style') { node.rel = 'stylesheet'; node.href = url; }
        else { node.src = url; node.async = true; }
        node.onload = () => resolve();
        node.onerror = () => { pending.delete(key); node.remove(); reject(new Error('AI 工具加载失败，请再次点击重试。')); };
        doc.head.append(node);
      });
      pending.set(key, ready);
      return ready;
    };
  }
  if (typeof module !== 'undefined' && module.exports) module.exports = {createAssetLoader};
  if (typeof document === 'undefined') return;
  const config = document.querySelector('[data-ai-assets]');
  if (!config) return;
  const assets = config.dataset, load = createAssetLoader(document);
  const status = document.createElement('span');
  status.className = 'ai-load-status'; status.setAttribute('role', 'status');
  let opening = false, textReady = false, imageReady = false;
  document.addEventListener('click', async event => {
    const button = event.target.closest?.('[data-ai-open],[data-ai-image-open]');
    if (!button || button.disabled) return;
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
      if (!window.ChunnelAIEditorReady) throw new Error('AI 编辑器暂不可用，请刷新页面后重试。');
      textReady = true;
      if (image) {
        await Promise.all([load(assets.imageStyle, 'style'), load(assets.imageScript, 'script')]);
        if (!window.ChunnelAIImagesReady) throw new Error('AI 生图暂不可用，请刷新页面后重试。');
        imageReady = true;
      }
      status.textContent = '';
      replay = true;
    } catch (error) { status.textContent = error.message; }
    finally { opening = false; button.removeAttribute('aria-busy'); }
    if (replay && button.isConnected && !button.disabled) button.click();
  }, true);
})();
