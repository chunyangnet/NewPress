// Shared icons and notices used by ordinary pages and the writing workspace.
const reducedMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
const toastStack = document.querySelector('[data-admin-toast-stack]');
export const icon = (name: string) => {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.classList.add('icon', `icon-${name}`);
  svg.setAttribute('aria-hidden', 'true');
  const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
  use.setAttribute('href', `#icon-${name}`);
  svg.append(use);
  return svg;
};

export const showToast = (message: string, tone = 'info') => {
  if (!toastStack || !message) return;
  const toast = document.createElement('div');
  toast.className = 'admin-toast';
  toast.setAttribute('data-tone', tone);
  const mark = document.createElement('span');
  mark.append(icon(tone === 'error' ? 'circle-x' : tone === 'success' ? 'check-circle' : 'info'));
  const copy = document.createElement('p');
  const title = document.createElement('b');
  title.textContent = tone === 'error' ? '操作未完成' : tone === 'success' ? '操作完成' : '提示';
  const detail = document.createElement('small');
  detail.textContent = message;
  copy.append(title, detail);
  const close = document.createElement('button');
  close.type = 'button';
  close.setAttribute('aria-label', '关闭提示');
  close.append(icon('x'));
  close.addEventListener('click', () => toast.remove());
  toast.append(mark, copy, close);
  toastStack.append(toast);
  requestAnimationFrame(() => toast.classList.add('is-visible'));
  window.setTimeout(() => {
    toast.classList.remove('is-visible');
    window.setTimeout(() => toast.remove(), reducedMotion ? 0 : 360);
  }, 4300);
};

window.ChunnelAdmin = Object.freeze({ icon, showToast });
