import { node } from '../shared/dom';

(() => {
  'use strict';
  const picker = window.NewPressMediaPicker;
  if (!picker) return;
  const fields = new WeakMap<Node, () => void>();
  const previewURL = (value: string | undefined) => {
    if (!value || /[\\\x00-\x20]/.test(value) || value.startsWith('//')) return '';
    try {
      const url = new URL(value, location.origin);
      return !url.username && !url.password && (value.startsWith('/') || url.protocol === 'https:') ? url.href : '';
    } catch { return ''; }
  };
  const enhance = (input: HTMLInputElement | HTMLTextAreaElement) => {
    if (fields.has(input)) return;
    const multiple = Number(input.dataset.mediaMultiple) || 0;
    const title = input.dataset.mediaTitle || '图片';
    if (!input.hasAttribute('aria-label') && !input.hasAttribute('aria-labelledby')) input.setAttribute('aria-label', title);
    const wrap = node('span', 'admin-media-field');
    const preview = node('span', 'admin-media-field-preview');
    const placeholder = node('span', 'admin-media-field-placeholder', '尚未选择图片');
    const image = node('img', '');
    image.alt = `${title}预览`; image.hidden = true;
    preview.append(image, placeholder);
    const actions = node('span', 'admin-media-field-actions');
    const choose = node('button', 'admin-button admin-button-secondary', multiple ? '添加图片' : '选择 / 上传图片');
    const clear = node('button', 'admin-button admin-button-quiet', '清除');
    choose.type = clear.type = 'button';
    choose.setAttribute('aria-haspopup', 'dialog'); choose.setAttribute('aria-expanded', 'false');
    choose.setAttribute('aria-label', `${multiple ? '添加' : '选择或上传'}${title}`);
    clear.setAttribute('aria-label', `清除${title}`);
    const status = node('span', 'admin-media-field-status');
    status.setAttribute('role', 'status');
    actions.append(choose, clear);
    input.before(wrap);
    // Keep the original field first, so an enclosing label still labels it.
    wrap.append(input, preview, actions, status);
    const values = () => multiple ? [...new Set(input.value.split(/\r?\n/).map(value => value.trim()).filter(Boolean))] : [input.value.trim()].filter(Boolean);
    const locked = () => !input.isConnected || input.matches(':disabled') || input.readOnly || !!input.closest('[inert]');
    let previous: string | null = null;
    const refresh = () => {
      const urls = values();
      choose.disabled = locked(); clear.disabled = locked() || !input.value;
      const url = previewURL(urls[0]);
      if (url !== previous) {
        previous = url; image.hidden = true; placeholder.hidden = false;
        placeholder.textContent = urls.length ? '预览不可用' : '尚未选择图片';
        if (url) image.src = url;
        else image.removeAttribute('src');
      }
      status.textContent = multiple ? `${urls.length} / ${multiple} 张 · 按地址顺序展示` : '';
    };
    image.addEventListener('load', () => { image.hidden = false; placeholder.hidden = true; });
    image.addEventListener('error', () => { image.hidden = true; placeholder.hidden = false; });
    const changed = () => {
      input.dispatchEvent(new Event('input', { bubbles: true }));
      input.dispatchEvent(new Event('change', { bubbles: true }));
    };
    choose.addEventListener('click', () => {
      if (locked()) return;
      void picker.open(choose, {
        title: `选择${title}`, uploadSource: input.dataset.mediaUploadSource || 'settings-image', disabled: locked,
        onSelect(url) {
          if (locked()) throw new Error('当前字段不可编辑');
          const urls = values();
          if (multiple && !urls.includes(url) && urls.length >= multiple) throw new Error(`最多选择 ${multiple} 张图片，请先移除不需要的图片`);
          input.value = multiple ? [...new Set([...urls, url])].join('\n') : url;
          changed();
        },
      });
    });
    clear.addEventListener('click', () => { if (!locked()) { input.value = ''; changed(); input.focus(); } });
    input.addEventListener('input', refresh); input.addEventListener('change', refresh);
    input.form?.addEventListener('input', event => { if (event.target === input.form) refresh(); });
    input.form?.addEventListener('reset', () => queueMicrotask(refresh));
    fields.set(input, refresh); refresh();
  };
  function scan(root: Node) {
    if ((root instanceof HTMLInputElement || root instanceof HTMLTextAreaElement) && root.matches('[data-media-input]')) enhance(root);
    const container = root === document ? document : root instanceof Element ? root : null;
    container?.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>('input[data-media-input], textarea[data-media-input]').forEach(enhance);
  }
  scan(document);
  // Shop variants and badge designs are inserted after the initial page load.
  new MutationObserver(records => {
    for (const record of records) {
      if (record.type === 'childList') record.addedNodes.forEach(scan);
      else {
        fields.get(record.target)?.();
        if (record.target instanceof Element) record.target.querySelectorAll('[data-media-input]').forEach(input => fields.get(input)?.());
      }
    }
  }).observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ['disabled', 'readonly', 'inert'] });
})();
