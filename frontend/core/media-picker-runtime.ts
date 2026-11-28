import { node as element } from '../shared/dom';
import { decodeMediaImage, decodeMediaList, decodeMediaUpload, mediaFailure, mediaURL, requestMediaJSON } from './media-data';
import type { ComponentMediaSelection, EditorSourceSelection, MediaImage, MediaPickerOptions, MediaOpenRequest } from './media-types';

type PickerMode = 'body' | 'cover' | 'component' | 'field';
type RetryAction = () => unknown;
type MediaRequest = { id: number; signal: AbortSignal; timeout: ReturnType<typeof setTimeout> };

export function initializeMediaPicker(): void {
  'use strict';
  const editor = document.querySelector<HTMLFormElement>('[data-content-editor]');
  const mediaDialog = document.querySelector<HTMLDialogElement>('[data-editor-media-dialog]');
  if (!mediaDialog) return;
  const dialog = mediaDialog;
  if (window.NewPressMediaRuntime?.dialog === dialog) return;
  const $ = <T extends HTMLElement = HTMLElement>(selector: string): T => {
    const target = dialog.querySelector<T>(selector);
    if (!target) throw new Error(`Missing media picker control: ${selector}`);
    return target;
  };
  const source = editor?.querySelector<HTMLTextAreaElement>('[data-content-source]');
  const cover = editor?.querySelector<HTMLInputElement>('[name="cover_url"]');
  const search = $<HTMLFormElement>('[data-editor-media-search]');
  const query = $<HTMLInputElement>('[data-editor-media-search] [name="q"]');
  const grid = $('[data-editor-media-grid]');
  const preview = $('[data-editor-media-preview]');
  const emptyPreview = preview.firstElementChild?.cloneNode(true) || element('span');
  const details = $<HTMLFormElement>('[data-editor-media-insert]');
  const altText = $<HTMLInputElement>('[name="alt_text"]');
  const decorative = $<HTMLInputElement>('[name="decorative"]');
  const caption = $<HTMLTextAreaElement>('[name="caption"]');
  const width = $<HTMLInputElement>('[name="width"]');
  const fields = $<HTMLFieldSetElement>('[data-editor-image-fields]');
  const apply = $<HTMLButtonElement>('[data-editor-media-apply]');
  const uploadButton = dialog.querySelector<HTMLButtonElement>('[data-editor-media-upload]');
  const fileInput = dialog.querySelector<HTMLInputElement>('[data-editor-media-file]');
  const previous = $<HTMLButtonElement>('[data-editor-media-previous]');
  const next = $<HTMLButtonElement>('[data-editor-media-next]');
  const status = $('[data-editor-media-status]');
  const errorBox = $('[data-editor-media-error]');
  let mode: PickerMode = 'body';
  let launcher: HTMLElement | null = null;
  let returnToLauncher = true;
  let selection: EditorSourceSelection | null = null;
  let selected: MediaImage | null = null;
  let imageReady = false;
  let page = 1;
  let pages = 1;
  let busy = false;
  let serial = 0;
  let opening = 0;
  let controller: AbortController | null = null;
  let canUpload = false;
  let maximum = 0;
  let pendingFile: File | null = null;
  let retry: RetryAction | null = null;
  let componentPick: ComponentMediaSelection | null = null;
  let fieldPick: MediaPickerOptions | null = null;
  let uploadSource = 'settings-image';
  const sizeLabel = (bytes: number) => bytes < 1024 ? `${bytes} B` : bytes < 1048576 ? `${(bytes / 1024).toFixed(1)} KiB` : `${(bytes / 1048576).toFixed(1)} MiB`;
  const controls = () => {
    $<HTMLButtonElement>('[data-editor-media-search] button').disabled = busy;
    if (uploadButton) uploadButton.disabled = busy || !canUpload;
    previous.disabled = busy || page <= 1;
    next.disabled = busy || page >= pages;
    grid.querySelectorAll('button').forEach((button) => { button.disabled = busy; });
    fields.disabled = busy || !selected || mode !== 'body';
    fields.hidden = mode !== 'body';
    altText.disabled = decorative.checked;
    altText.required = !decorative.checked;
    apply.disabled = busy || !selected || !imageReady;
    $('[data-editor-media-library]').setAttribute('aria-busy', String(busy));
  };
  const begin = (uploading = false) => {
    controller?.abort();
    controller = new AbortController();
    const pending = controller;
    const request: MediaRequest = { id: ++serial, signal: pending.signal,
      timeout: setTimeout(() => pending.abort(new DOMException('Request timed out', 'TimeoutError')), uploading ? 120000 : 15000) };
    busy = true; errorBox.hidden = true;
    $('[data-editor-media-progress]').hidden = !uploading;
    controls();
    return request;
  };
  const current = (request: MediaRequest) => dialog.open && request.id === serial;
  const finish = (request: MediaRequest) => {
    clearTimeout(request.timeout);
    if (!current(request)) return;
    busy = false; $('[data-editor-media-progress]').hidden = true; controls();
  };
  const showError = (message: string, action: RetryAction | null = null, recoveryID: number | null = null) => {
    retry = action;
    $('[data-editor-media-retry]').hidden = !action;
    $('[data-editor-media-error] span').textContent = message;
    const recovery = $<HTMLAnchorElement>('[data-editor-media-recover]');
    recovery.hidden = recoveryID === null || !Number.isSafeInteger(recoveryID) || recoveryID <= 0;
    if (recovery.hidden) recovery.removeAttribute('href');
    else recovery.href = `/admin/media/${recoveryID}/edit/`;
    errorBox.hidden = false;
    status.textContent = '';
  };
  const markSelection = () => {
    grid.querySelectorAll<HTMLButtonElement>('[data-editor-image-id]').forEach((button) => button.setAttribute('aria-pressed', String(Number(button.dataset.editorImageId) === selected?.id)));
  };
  function selectItem(item: MediaImage, preserveFields = false) {
    const url = mediaURL(item);
    if (!url) { showError('这项媒体不是可用的图片'); return false; }
    selected = item; imageReady = false;
    const image = element('img', '');
    image.alt = item.alt_text || '';
    image.addEventListener('load', () => {
      if (!dialog.open || selected !== item || preview.firstElementChild !== image) return;
      imageReady = image.naturalWidth > 0; controls();
    });
    image.addEventListener('error', () => {
      if (!dialog.open || selected !== item || preview.firstElementChild !== image) return;
      imageReady = false; showError('图片无法加载，可能已被移除', () => { errorBox.hidden = true; selectItem(item, true); }); controls();
    });
    image.src = url;
    preview.replaceChildren(image);
    $('[data-editor-media-name]').textContent = item.title || item.filename;
    $('[data-editor-media-dimensions]').textContent = `${item.width} × ${item.height} · ${sizeLabel(item.size_bytes)}`;
    if (!preserveFields) {
      altText.value = item.alt_text || '';
      altText.setCustomValidity('');
      decorative.checked = false;
      caption.value = item.caption || '';
      width.value = String(item.width);
    }
    width.max = String(item.width);
    markSelection(); controls();
    return true;
  }
  async function loadPage(number = 1): Promise<boolean> {
    const request = begin();
    status.textContent = '正在加载图片…';
    const url = new URL('/api/v1/admin/media', location.origin);
    url.searchParams.set('status', 'active'); url.searchParams.set('kind', 'image'); url.searchParams.set('page', String(number));
    if (query.value.trim()) url.searchParams.set('q', query.value.trim());
    try {
      const payload = await requestMediaJSON(url, { signal: request.signal });
      if (!current(request)) return false;
      const result = decodeMediaList(payload);
      if (result.pagination.page !== number) throw new Error('图片列表页码不一致，请重试');
      page = result.pagination.page; pages = result.pagination.pages;
      maximum = result.max_upload_bytes;
      canUpload = dialog.dataset.canUpload === 'true' && result.can_upload === true;
      if (page > pages) return await loadPage(pages);
      const fragment = document.createDocumentFragment();
      result.items.forEach((item) => {
        const url = mediaURL(item); if (!url) return;
        const button = element('button', 'admin-editor-image-choice');
        button.type = 'button'; button.dataset.editorImageId = String(item.id);
        const image = element('img', ''); image.src = url; image.alt = ''; image.loading = 'lazy'; image.width = item.width; image.height = item.height;
        button.append(image, element('span', '', item.title || item.filename));
        button.title = item.filename;
        button.addEventListener('click', () => { errorBox.hidden = true; selectItem(item); });
        fragment.append(button);
      });
      grid.replaceChildren(fragment);
      markSelection();
      $('[data-editor-media-total]').textContent = `${result.pagination.total} 张图片`;
      $('[data-editor-media-page]').textContent = `${page} / ${pages}`;
      status.textContent = result.items.length ? '' : query.value.trim() ? '没有匹配的图片' : '媒体库还没有图片';
      return true;
    } catch (error) {
      const failure = mediaFailure(error);
      if (current(request) && failure.name !== 'AbortError') showError(failure.message || '图片列表加载失败', () => loadPage(number));
      return false;
    } finally { finish(request); }
  }
  async function upload(file: File | null | undefined): Promise<void> {
    if (!file || !canUpload || busy) return;
    if (!file.size) { showError('所选文件为空'); return; }
    if (maximum > 0 && file.size > maximum) { showError(`图片超过上传上限 ${sizeLabel(maximum)}`); return; }
    if ((file.type && !['image/jpeg', 'image/png', 'image/gif', 'image/webp'].includes(file.type)) || (!file.type && !/\.(jpe?g|png|gif|webp)$/i.test(file.name))) {
      showError('图片支持 JPEG、PNG、GIF 和 WebP'); return;
    }
    const csrf = dialog.dataset.csrf;
    if (!csrf) { showError('上传凭据已失效，请刷新页面后重试'); return; }
    pendingFile = file;
    const generation = opening;
    const request = begin(true);
    status.textContent = `正在上传 ${file.name}…`;
    const form = new FormData();
    form.set('upload_source', uploadSource);
    form.set('csrf_token', csrf); form.set('file', file); form.set('title', ''); form.set('alt_text', ''); form.set('caption', '');
    try {
      const payload = await requestMediaJSON('/api/v1/admin/media', { method: 'POST', headers: { 'X-CSRF-Token': csrf }, body: form, signal: request.signal });
      if (!current(request)) return;
      const result = decodeMediaUpload(payload);
      if (!selectItem(result.item)) return;
      pendingFile = null; if (fileInput) fileInput.value = ''; query.value = '';
      const loaded = await loadPage(1);
      if (loaded && dialog.open && generation === opening && selected?.id === result.item.id) status.textContent = result.duplicate ? '已选用媒体库中的相同图片' : `已上传 ${result.item.filename}`;
    } catch (error) {
      const failure = mediaFailure(error);
      if (current(request) && failure.name !== 'AbortError') showError(failure.message || '图片上传失败，文件仍可重试', () => upload(pendingFile), failure.recoveryID);
    } finally { finish(request); }
  }
  async function open(request: MediaOpenRequest): Promise<void> {
    const { launcher: button, file, pick } = request;
    if (!request.valid()) return;
    if (dialog.open || button.matches(':disabled') || button.closest('[inert]')) return;
    const requestedMode = request.mode;
    if (requestedMode !== 'body' && requestedMode !== 'cover' && requestedMode !== 'component' && requestedMode !== 'field') return;
    mode = requestedMode;
    if (mode !== 'field' && (source?.disabled || source?.readOnly)) return;
    if (mode === 'body' && !source) return;
    if (pick?.disabled?.()) return;
    if (mode === 'cover' && (!cover || cover.disabled || cover.readOnly)) return;
    fieldPick = pick; componentPick = request.component;
    uploadSource = pick?.uploadSource || (mode === 'cover' ? 'editor-cover' : mode === 'component' ? 'editor-component' : 'editor-body');
    launcher = button;
    returnToLauncher = true;
    const generation = ++opening;
    selection = request.selection;
    selected = null; imageReady = false; pendingFile = file; canUpload = false; retry = null;
    details.reset(); search.reset();
    preview.replaceChildren(emptyPreview.cloneNode(true));
    $('[data-editor-media-name]').textContent = '尚未选择图片';
    $('[data-editor-media-dimensions]').textContent = '';
    $('#editor-media-title').textContent = pick?.title || (mode === 'component' ? '选择组件图片' : mode === 'cover' ? '选择封面图片' : '插入图片');
    $('[data-editor-media-apply] span').textContent = mode === 'field' ? '使用此图片' : mode === 'component' ? '用于组件' : mode === 'cover' ? '设为封面' : '插入正文';
    button.setAttribute('aria-expanded', 'true');
    dialog.showModal(); query.focus();
    const loaded = await loadPage(1);
    if (!dialog.open || generation !== opening) return;
    if (request.error) { showError(request.error); return; }
    if (!file) return;
    const uploadPending = async () => {
      if (!dialog.open || generation !== opening || !pendingFile) return;
      if (!canUpload) {
        const ready = await loadPage(1);
        if (!dialog.open || generation !== opening) return;
        if (!ready) { showError('图片未上传，文件已保留，请重试', uploadPending); return; }
        if (!canUpload) { showError('当前账号没有上传图片的权限'); return; }
      }
      await upload(pendingFile);
    };
    if (loaded) await uploadPending();
    else showError('图片未上传，文件已保留，请重试', uploadPending);
  }
  $('[data-editor-media-close]').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => {
    if (dialog.open) return;
    opening++; serial++; controller?.abort(); controller = null; busy = false;
    pendingFile = null; retry = null; selected = null; selection = null;
    componentPick = null;
    fieldPick = null;
    launcher?.setAttribute('aria-expanded', 'false');
    if (returnToLauncher && launcher?.isConnected) launcher.focus({ preventScroll: true });
  });
  search.addEventListener('submit', (event) => { event.preventDefault(); void loadPage(1); });
  previous.addEventListener('click', () => { if (page > 1) void loadPage(page - 1); });
  next.addEventListener('click', () => { if (page < pages) void loadPage(page + 1); });
  $('[data-editor-media-retry]').addEventListener('click', () => { if (!busy && retry) void retry(); });
  decorative.addEventListener('change', controls);
  altText.addEventListener('input', () => altText.setCustomValidity(''));
  uploadButton?.addEventListener('click', () => { if (fileInput) { fileInput.value = ''; fileInput.click(); } });
  fileInput?.addEventListener('change', () => { void upload(fileInput.files?.[0]); });
  dialog.addEventListener('dragover', (event) => { if (event.dataTransfer?.types.includes('Files')) { event.preventDefault(); event.dataTransfer.dropEffect = canUpload && !busy ? 'copy' : 'none'; } });
  dialog.addEventListener('drop', (event) => {
    if (!event.dataTransfer?.files.length) return;
    event.preventDefault();
    if (event.dataTransfer.files.length !== 1) { showError('一次上传仅接受一张图片'); return; }
    void upload(event.dataTransfer.files[0]);
  });
  details.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!dialog.open || busy || !selected || !imageReady) return;
    if (mode === 'field' ? fieldPick?.disabled?.() : source?.disabled || source?.readOnly) return;
    if (mode === 'body' && !decorative.checked && !altText.value.trim()) {
      altText.setCustomValidity('请填写替代文本，或将图片标记为装饰图片'); details.reportValidity(); return;
    }
    if (!details.reportValidity()) return;
    const item = selected;
    const options = { alt: decorative.checked ? '' : altText.value.trim(), caption: caption.value.trim(), width: Number(width.value) };
    const request = begin();
    try {
      const payload = await requestMediaJSON(`/api/v1/admin/media/${item.id}`, { signal: request.signal });
      if (!current(request)) return;
      const latest = decodeMediaImage(payload);
      if (!latest || latest.id !== item.id) throw new Error('所选图片已不可用，请重新选择');
      const url = mediaURL(latest);
      if (!url) throw new Error('所选图片已不可用，请重新选择');
      if (mode !== 'field' && (source?.disabled || source?.readOnly)) throw new Error('当前正文不可编辑');
      if (mode === 'field') {
        if (!fieldPick || fieldPick.disabled?.()) throw new Error('当前字段不可编辑，请关闭选择器后重试');
        await fieldPick.onSelect(url, latest);
        if (current(request)) dialog.close();
        return;
      }
      if (mode === 'component') {
        componentPick?.(url, latest);
        dialog.close(); return;
      }
      if (mode === 'cover') {
        if (!cover || cover.disabled || cover.readOnly) return;
        cover.value = url; cover.dispatchEvent(new Event('input', { bubbles: true }));
        returnToLauncher = false;
        dialog.close(); cover.focus(); return;
      }
      if (!source || !selection || source.disabled || source.readOnly) throw new Error('当前正文不可编辑');
      if (source.value !== selection.value) throw new Error('正文已变化，请重新选择插入位置');
      const image = element('img', '');
      image.setAttribute('src', url); image.setAttribute('alt', options.alt);
      image.setAttribute('width', String(options.width));
      image.setAttribute('height', String(Math.max(1, Math.round(options.width * latest.height / latest.width))));
      const figure = document.createElement('figure'); figure.append(image);
      if (options.caption) figure.append(element('figcaption', '', options.caption));
      const markup = '\n' + figure.outerHTML + '\n';
      if (window.ChunnelBlockEditor) {
        window.ChunnelBlockEditor.insert(markup, selection);
        returnToLauncher = false;
        dialog.close();
      } else {
        const emoji = window.ChunnelEmoji;
        if (!emoji) throw new Error('编辑器尚未就绪，请重试');
        const error = emoji.validateReplacement(source, markup, selection);
        if (error) throw new Error(error);
        returnToLauncher = false;
        const savedSelection = selection;
        dialog.close();
        emoji.replaceSelection(source, markup, savedSelection);
        source.focus();
      }
    } catch (error) {
      const failure = mediaFailure(error);
      if (current(request) && failure.name !== 'AbortError') showError(failure.message || '图片未能插入，正文已保留');
    } finally { finish(request); }
  });
  window.addEventListener('pagehide', () => {
    opening++; serial++; controller?.abort(); controller = null;
    const interrupted = busy; busy = false;
    $('[data-editor-media-progress]').hidden = true; controls();
    if (!dialog.open || !interrupted) return;
    const resume = async () => {
      const generation = opening, file = pendingFile;
      const ready = await loadPage(page);
      if (!dialog.open || generation !== opening || pendingFile !== file) return;
      if (!ready) { showError('媒体请求未完成；输入和文件已保留，请重试。', resume); return; }
      if (file) await upload(file);
    };
    showError('页面切换中断了媒体请求；输入和文件已保留，请重试。', resume);
  });
  window.NewPressMediaRuntime = { dialog, open };
}
