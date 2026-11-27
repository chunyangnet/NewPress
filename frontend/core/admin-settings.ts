import { initializeImportDialog } from './admin-import-dialog';
import { initializeRegistrationCheck } from './registration-check';
import { bindNativeSubmission } from './admin-native-submit';
import type { FormControl, Savebar } from './admin-types';
(() => {
  'use strict';
  initializeRegistrationCheck();
  const body = document.body;
  const settingsForm = document.querySelector<HTMLFormElement>('[data-settings-form]');
  const showToast = (message: string, tone = 'info') => window.ChunnelAdmin?.showToast(message, tone);
  const updatePreview = (input: FormControl) => {
    if (input?.name) document.dispatchEvent(new Event('appearance-preview-change'));
  };

  const sitePreviewTitle = document.querySelector('[data-site-preview-title]');
  const sitePreviewDescription = document.querySelector('[data-site-preview-description]');
  const sitePreviewLocale = document.querySelector('[data-site-preview-locale]');
  const sitePolicyLabels: Record<string, [string, string]> = {
    public_posts_enabled: ['已开放', '已隐藏'],
    sitemap_enabled: ['已生成', '已关闭'],
    admin_content_api_enabled: ['已启用', '已隐藏'],
    maintenance_enabled: ['维护模式', '正常服务']
  };
  const updateSitePreview = (input: FormControl) => {
    if (!input?.name) return;
    if (input.name === 'site_title' && sitePreviewTitle) sitePreviewTitle.textContent = input.value || '未命名站点';
    if (input.name === 'site_description' && sitePreviewDescription) sitePreviewDescription.textContent = input.value || '尚未填写站点描述';
    if (input.name === 'site_locale' && sitePreviewLocale) sitePreviewLocale.textContent = input.value || '—';
    const policy = document.querySelector(`[data-site-policy="${CSS.escape(input.name)}"]`);
    if (!policy || !(input instanceof HTMLInputElement) || input.type !== 'checkbox') return;
    const indicator = policy.querySelector('i');
    const label = policy.querySelector('small');
    const enabled = input.checked;
    indicator?.classList.toggle('is-on', input.name === 'maintenance_enabled' ? !enabled : enabled);
    indicator?.classList.toggle('is-warn', input.name === 'maintenance_enabled' && enabled);
    if (label) label.textContent = sitePolicyLabels[input.name]?.[enabled ? 0 : 1] || (enabled ? '开启' : '关闭');
  };

  const updateRange = (input: FormControl) => {
    const output = input.closest('.admin-range-control')?.querySelector('[data-range-output]');
    if (output) output.textContent = input.value;
  };
  document.querySelectorAll<HTMLInputElement>('[data-range-input]').forEach((input) => {
    updateRange(input);
    updatePreview(input);
    input.addEventListener('input', () => { updateRange(input); updatePreview(input); markDirty(); });
  });
  const updateColor = (input: FormControl) => {
    const control = input.closest('.admin-color-control');
    const output = control?.querySelector('[data-color-output]');
    const copy = control?.querySelector<HTMLElement>('[data-copy-value]');
    if (output) output.textContent = input.value;
    if (copy) copy.dataset.copyValue = input.value;
  };
  document.querySelectorAll<HTMLInputElement>('[data-color-input]').forEach((input) => {
    updateColor(input);
    updatePreview(input);
    input.addEventListener('input', () => {
      updateColor(input);
      updatePreview(input);
      markDirty();
    });
  });
  document.querySelectorAll<HTMLInputElement>('.admin-switch input').forEach((input) => {
    updatePreview(input);
    input.addEventListener('change', () => {
      const state = input.closest('.admin-switch')?.querySelector('[data-switch-state]');
      if (state) state.textContent = input.checked ? '开启' : '关闭';
      updatePreview(input);
      markDirty();
    });
  });

  let settingsController: Savebar | null | undefined;
  const markDirty = () => settingsController?.refresh();
  if (settingsForm) {
    settingsForm.querySelectorAll<HTMLElement>('[data-setting-field]').forEach((field) => {
      const label = field.querySelector('.admin-setting-copy > label');
      const help = field.querySelector('.admin-setting-copy > p');
      const error = field.querySelector('.admin-field-error');
      if (label) label.id = `${field.dataset.settingField}-label`;
      if (error) error.id = `${field.dataset.settingField}-error`;
      field.querySelectorAll('input, select, textarea, [role="radiogroup"]').forEach((input) => {
        if (input.closest('[data-setting-field]') !== field) return;
        if (input.getAttribute('type') !== 'radio' && label && !input.closest('[data-home-editor]')) input.setAttribute('aria-labelledby', label.id);
        const description = [help?.id, error?.id].filter(Boolean).join(' ');
        if (description) input.setAttribute('aria-describedby', description);
        if (error) input.setAttribute('aria-invalid', 'true');
      });
    });
    const updateControls = () => settingsForm.querySelectorAll<FormControl>('input,select,textarea').forEach(input => {
      if (input.matches('[data-range-input]')) updateRange(input);
      if (input.matches('[data-color-input]')) updateColor(input);
      if (input.matches('[data-home-order]')) input.dispatchEvent(new Event('change', { bubbles: true }));
      window.ChunnelAdminUI?.syncSwitch(input);
      updatePreview(input); updateSitePreview(input);
    });
    settingsController = window.ChunnelAdminUI?.savebar(settingsForm, { native: true, allowClean: !!settingsForm.querySelector('.has-error'), onDiscard: updateControls });
    const updateField = (event: Event) => {
      const input = event.target;
      if (!(input instanceof HTMLInputElement || input instanceof HTMLSelectElement || input instanceof HTMLTextAreaElement)) return;
      updatePreview(input); updateSitePreview(input);
    };
    settingsForm.addEventListener('input', updateField);
    settingsForm.addEventListener('change', updateField);
  }

  const footerCredit = settingsForm?.querySelector<HTMLInputElement>('input[name="footer_hide_credit"]');
  const footerCreditDialog = document.querySelector<HTMLDialogElement>('[data-footer-credit-dialog]');
  if (footerCredit && footerCreditDialog) {
    footerCredit.addEventListener('click', event => {
      if (!footerCredit.checked) return;
      // Cancel native activation so the form stays unchanged until confirmation.
      event.preventDefault();
      if (!footerCreditDialog.open) footerCreditDialog.showModal();
      body.classList.add('admin-dialog-open');
      footerCreditDialog.querySelector<HTMLButtonElement>('button[autofocus]')?.focus();
    });
    footerCreditDialog.querySelectorAll('[data-footer-credit-cancel]').forEach(button => {
      button.addEventListener('click', () => footerCreditDialog.close());
    });
    footerCreditDialog.querySelector('[data-footer-credit-confirm]')?.addEventListener('click', () => {
      footerCredit.checked = true;
      footerCredit.dispatchEvent(new Event('input', { bubbles: true }));
      footerCredit.dispatchEvent(new Event('change', { bubbles: true }));
      footerCreditDialog.close();
    });
    footerCreditDialog.addEventListener('close', () => {
      if (footerCreditDialog.open || document.querySelector('dialog[open]')) return;
      body.classList.remove('admin-dialog-open');
      footerCredit.focus({ preventScroll: true });
    });
  }

  const importDialog = initializeImportDialog(document);
  const settingsFile = importDialog?.querySelector<HTMLInputElement>('[data-settings-file]');
  const settingsDrop = importDialog?.querySelector<HTMLElement>('.admin-file-drop');
  const updateSettingsFile = () => {
    const file = settingsFile?.files?.[0];
    const output = importDialog?.querySelector('[data-file-name]');
    const error = file && file.size > 256 * 1024 ? '文件超过 256 KiB，请重新选择' : '';
    settingsFile?.setCustomValidity(error);
    settingsDrop?.classList.toggle('is-selected', Boolean(file && !error));
    if (output) { output.textContent = error || (file ? `${file.name} · ${(file.size / 1024).toFixed(1)} KiB` : '尚未选择文件'); output.classList.toggle('has-error', Boolean(error)); }
  };
  settingsFile?.addEventListener('change', updateSettingsFile);
  settingsDrop?.addEventListener('dragover', (event) => { event.preventDefault(); settingsDrop.classList.add('is-dragging'); });
  settingsDrop?.addEventListener('dragleave', () => settingsDrop.classList.remove('is-dragging'));
  settingsDrop?.addEventListener('drop', (event) => {
    event.preventDefault();
    settingsDrop.classList.remove('is-dragging');
    const file = event.dataTransfer?.files?.[0];
    if (!file || !settingsFile) return;
    try {
      const transfer = new DataTransfer();
      transfer.items.add(file);
      settingsFile.files = transfer.files;
      updateSettingsFile();
    } catch { showToast('请点击选择配置文件', 'error'); }
  });
  const navigationImportFile = document.querySelector<HTMLInputElement>('[data-tools-navigation-file]');
  navigationImportFile?.addEventListener('change', () => {
    const file = navigationImportFile.files?.[0];
    const output = document.querySelector('[data-tools-navigation-file-name]');
    if (output) output.textContent = file ? `${file.name} · ${(file.size / 1024).toFixed(1)} KiB` : '尚未选择文件';
    if (file && file.size > 256 * 1024) showToast('导航导入文件不能超过 256 KiB', 'warning');
  });
  const navigationImport = document.querySelector<HTMLFormElement>('[data-navigation-import-preview]');
  const navigationSubmit = navigationImport?.querySelector<HTMLButtonElement>('button[type="submit"]');
  const navigationContents = navigationSubmit ? [...navigationSubmit.childNodes].map(node => node.cloneNode(true)) : [];
  if (navigationImport) bindNativeSubmission(navigationImport, {
    onPending() {
      if (navigationSubmit) {
        navigationSubmit.disabled = true;
        const spinner = window.ChunnelAdmin?.icon('refresh-cw');
        navigationSubmit.replaceChildren(...(spinner ? [spinner] : []), document.createTextNode('正在严格校验并计算差异…'));
      }
    },
    onRestore() {
      if (navigationSubmit) { navigationSubmit.disabled = false; navigationSubmit.replaceChildren(...navigationContents.map(node => node.cloneNode(true))); }
    },
  });

})();
