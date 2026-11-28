import { bindNativeSubmission } from './admin-native-submit';

(() => {
  'use strict';
  const form = document.querySelector<HTMLFormElement>('.admin-login-form');
  const button = form?.querySelector<HTMLButtonElement>('button[type="submit"]');
  const original = button?.innerHTML || '';
  if (form) bindNativeSubmission(form, {
    onPending() {
      if (button) { button.setAttribute('aria-disabled', 'true'); button.dataset.submitState = 'pending'; button.textContent = '正在登录…'; }
    },
    onRestore() {
      delete form.dataset.verificationSubmitting; delete form.dataset.verificationPending;
      if (button) { button.removeAttribute('aria-disabled'); delete button.dataset.submitState; button.innerHTML = original; }
    },
  });
  const passwordToggle = document.querySelector<HTMLButtonElement>('[data-password-toggle]');
  passwordToggle?.addEventListener('click', () => {
    const button = passwordToggle;
    const input = button.closest('div')?.querySelector('input');
    if (!input) return;
    const visible = input.type === 'text';
    input.type = visible ? 'password' : 'text';
    button.setAttribute('aria-label', visible ? '显示密码' : '隐藏密码');
    button.setAttribute('aria-pressed', String(!visible));
  });
})();
