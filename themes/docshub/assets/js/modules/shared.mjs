export const $ = (selector, scope = document) => scope.querySelector(selector);
export const $$ = (selector, scope = document) => [...scope.querySelectorAll(selector)];

let toastTimer;
export function announce(message) {
  const toast = $('[data-toast]');
  if (!toast) return;
  toast.textContent = message;
  toast.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toast.hidden = true; }, 2800);
}

export async function copyText(text) {
  try {
    if (navigator.clipboard?.writeText) { await navigator.clipboard.writeText(text); return; }
  } catch { /* A denied Clipboard API can still allow a user-initiated copy. */ }
  const previous = document.activeElement;
  const selection = window.getSelection();
  const ranges = selection ? Array.from({ length: selection.rangeCount }, (_, i) => selection.getRangeAt(i).cloneRange()) : [];
  const field = document.createElement('textarea');
  field.value = text;
  field.className = 'sr-only';
  field.setAttribute('aria-label', '复制内容');
  document.body.append(field);
  field.select();
  let copied = false;
  try { copied = document.execCommand('copy'); }
  finally {
    field.remove(); previous?.focus({ preventScroll: true });
    selection?.removeAllRanges(); ranges.forEach(range => selection?.addRange(range));
  }
  if (!copied) throw new Error('Clipboard unavailable');
}

export function initRelativeTimes() {
  $$('[data-relative-time]').forEach(time => {
    const date = new Date(time.dateTime), seconds = (Date.now() - date.getTime()) / 1000;
    if (!Number.isFinite(seconds) || seconds < 0) return;
    if (seconds < 60) time.textContent = '刚刚';
    else if (seconds < 3600) time.textContent = Math.floor(seconds / 60) + ' 分钟前';
    else if (seconds < 86400) time.textContent = Math.floor(seconds / 3600) + ' 小时前';
    else if (seconds < 604800) time.textContent = Math.floor(seconds / 86400) + ' 天前';
    time.title = date.toLocaleString(document.documentElement.lang || 'zh-CN');
  });
}
