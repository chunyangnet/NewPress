import { bindNativeSubmission } from './admin-native-submit';
(() => {
  'use strict';
  const showToast = (message: string, tone = 'info') => window.ChunnelAdmin?.showToast(message, tone);
  const mediaUpload = document.querySelector<HTMLFormElement>('[data-media-upload]');
  const mediaDropzone = mediaUpload?.querySelector<HTMLElement>('[data-media-dropzone]');
  const mediaFileInput = mediaUpload?.querySelector<HTMLInputElement>('[data-media-file]');
  let mediaPreviewRequest = 0;
  const formatFileSize = (bytes: number) => {
    if (!Number.isFinite(bytes) || bytes < 1024) return `${Math.max(0, bytes || 0)} B`;
    const units = ['KiB', 'MiB', 'GiB'];
    let value = bytes / 1024;
    let unit = units[0];
    for (let index = 1; value >= 1024 && index < units.length; index += 1) { value /= 1024; unit = units[index]; }
    return `${value.toFixed(value >= 10 ? 1 : 2)} ${unit}`;
  };
  const updateMediaFile = (file: File | undefined) => {
    if (!mediaDropzone) return;
    const previewRequest = ++mediaPreviewRequest;
    const title = mediaDropzone.querySelector('[data-media-file-title]');
    const detail = mediaDropzone.querySelector('[data-media-file-detail]');
    mediaDropzone.querySelector('.admin-media-upload-preview')?.remove();
    mediaDropzone.classList.toggle('has-file', Boolean(file));
    if (!file) {
      if (title) title.textContent = '拖入文件，或点击选择';
      return;
    }
    if (title) title.textContent = file.name;
    if (detail) detail.textContent = `${file.type || '等待服务端识别'} · ${formatFileSize(file.size)} · 上传后按内容哈希去重`;
    const previewableRaster = ['image/jpeg', 'image/png', 'image/gif', 'image/webp'].includes(file.type);
    if (previewableRaster && file.size <= 8 * 1024 * 1024) {
      const reader = new FileReader();
      reader.addEventListener('load', () => {
        if (previewRequest !== mediaPreviewRequest || typeof reader.result !== 'string') return;
        const preview = document.createElement('img');
        preview.className = 'admin-media-upload-preview';
        preview.src = reader.result;
        preview.alt = '';
        mediaDropzone.prepend(preview);
      });
      reader.addEventListener('error', () => {
        if (previewRequest === mediaPreviewRequest) showToast('本地图片预览读取失败，仍可交由服务端校验。', 'warning');
      });
      reader.readAsDataURL(file);
    } else if (previewableRaster && detail) {
      detail.textContent += ' · 大图不在浏览器内展开预览';
    }
  };
  mediaFileInput?.addEventListener('change', () => updateMediaFile(mediaFileInput.files?.[0]));
  ['dragenter', 'dragover'].forEach((name) => mediaDropzone?.addEventListener(name, (event) => {
    event.preventDefault();
    mediaDropzone.classList.add('is-dragging');
  }));
  ['dragleave', 'drop'].forEach((name) => mediaDropzone?.addEventListener(name, (event) => {
    event.preventDefault();
    mediaDropzone.classList.remove('is-dragging');
  }));
  mediaDropzone?.addEventListener('drop', (event) => {
    const file = event.dataTransfer?.files?.[0];
    if (!file || !mediaFileInput) return;
    try {
      const transfer = new DataTransfer();
      transfer.items.add(file);
      mediaFileInput.files = transfer.files;
      updateMediaFile(file);
    } catch { showToast('当前浏览器不能从拖放区接收文件，请点击选择。', 'error'); }
  });
  if (mediaUpload) bindNativeSubmission(mediaUpload, {
    onPending() {
      const button = mediaUpload.querySelector<HTMLButtonElement>('button[type="submit"]');
      if (button) { button.disabled = true; button.setAttribute('aria-busy', 'true'); }
    },
    onRestore() {
      const button = mediaUpload.querySelector<HTMLButtonElement>('button[type="submit"]');
      if (button) { button.disabled = false; button.removeAttribute('aria-busy'); }
    },
  });


})();
