import { cropPosition, focusPercent } from '../shared/image-focus';
import { bindNativeSubmission } from './admin-native-submit';

const mounted = new Map<HTMLFormElement, () => void>();
function initialize() {
  for (const [form, dispose] of mounted) if (!form.isConnected) { dispose(); mounted.delete(form); }
  document.querySelectorAll<HTMLFormElement>('form[data-image-process]').forEach(form => {
    if (mounted.has(form)) return;
    const fieldX = form.elements.namedItem('focal_x'), fieldY = form.elements.namedItem('focal_y');
    const source = form.querySelector<HTMLImageElement>('img[data-focus-image]');
    const mark = form.querySelector<HTMLElement>('[data-focus-mark]');
    if (!(fieldX instanceof HTMLInputElement) || !(fieldY instanceof HTMLInputElement) || !source || !mark) return;
    const x = fieldX, y = fieldY;
    const abort = new AbortController(), options = { signal: abort.signal };
    const button = form.querySelector<HTMLButtonElement>('button[data-image-submit]');
    const status = form.querySelector<HTMLElement>('[data-image-status]');
    let disabled = button?.disabled ?? false, locked = false;
    let resetTimer: ReturnType<typeof setTimeout> | undefined;
    function update() {
      if (!source || !mark) return;
      const fx = focusPercent(x.value), fy = focusPercent(y.value);
      if (fx === null || fy === null) return;
      mark.style.left = `${fx}%`; mark.style.top = `${fy}%`;
      form.querySelectorAll<HTMLImageElement>('img[data-focus-crop]').forEach(preview => {
        const point = cropPosition(source.naturalWidth, source.naturalHeight, Number(preview.dataset.focusCrop), fx, fy);
        if (point) preview.style.objectPosition = `${point[0]}% ${point[1]}%`;
      });
    }
    const submission = bindNativeSubmission(form, {
      onPending() {
        if (button) { disabled = button.disabled; button.disabled = true; }
        if (status) status.textContent = '正在生成图片，请稍候…';
        locked = true;
      },
      onRestore() {
        if (locked) {
          if (button) button.disabled = disabled;
          if (status) status.textContent = '';
          locked = false;
        }
        update();
      },
    });
    function setFocus(fx: number, fy: number) {
      for (const [input, value] of [[x, fx], [y, fy]] as const) {
        const next = String(Math.round(value));
        if (input.value === next) continue;
        input.value = next;
        input.dispatchEvent(new Event('input', { bubbles: true }));
        input.dispatchEvent(new Event('change', { bubbles: true }));
      }
      update();
    }
    x.addEventListener('input', update, options); y.addEventListener('input', update, options);
    source.addEventListener('load', update, options);
    source.addEventListener('click', event => {
      const rect = source.getBoundingClientRect();
      if (submission.pending() || rect.width <= 0 || rect.height <= 0) return;
      const fx = focusPercent((event.clientX - rect.left) / rect.width * 100);
      const fy = focusPercent((event.clientY - rect.top) / rect.height * 100);
      if (fx !== null && fy !== null) setFocus(fx, fy);
    }, options);
    form.querySelector('[data-focus-center]')?.addEventListener('click', () => {
      if (submission.pending()) return;
      setFocus(50, 50); x.focus();
    }, options);
    form.addEventListener('reset', event => {
      clearTimeout(resetTimer);
      resetTimer = setTimeout(() => { if (!event.defaultPrevented) update(); }, 0);
    }, options);
    mounted.set(form, () => { abort.abort(); submission.dispose(); clearTimeout(resetTimer); });
    update();
  });
}
initialize();
document.addEventListener('chunnel:admin-updated', initialize);
document.addEventListener('chunnel:admin-before-replace', event => {
  const regions: unknown = event.detail?.regions;
  if (!Array.isArray(regions)) return;
  for (const [form, dispose] of mounted) {
    if (regions.some(region => region instanceof Element && (region === form || region.contains(form)))) {
      dispose(); mounted.delete(form);
    }
  }
});
