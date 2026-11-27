(() => {
  'use strict';
  const form = document.querySelector('[data-image-process]');
  if (!form) return;
  const x = form.elements.focal_x;
  const y = form.elements.focal_y;
  const source = form.querySelector('[data-focus-image]');
  const mark = form.querySelector('[data-focus-mark]');
  const clamp = (value) => Math.max(0, Math.min(100, Number(value) || 0));
  function update() {
    const fx = clamp(x.value) / 100;
    const fy = clamp(y.value) / 100;
    mark.style.left = `${fx * 100}%`;
    mark.style.top = `${fy * 100}%`;
    const w = source.naturalWidth;
    const h = source.naturalHeight;
    if (!w || !h) return;
    form.querySelectorAll('[data-focus-crop]').forEach((preview) => {
      const ratio = Number(preview.dataset.focusCrop);
      const cw = Math.min(w, h * ratio);
      const ch = Math.min(h, w / ratio);
      const left = Math.max(0, Math.min(w - cw, w * fx - cw / 2));
      const top = Math.max(0, Math.min(h - ch, h * fy - ch / 2));
      const px = w === cw ? 50 : left / (w - cw) * 100;
      const py = h === ch ? 50 : top / (h - ch) * 100;
      preview.style.objectPosition = `${px}% ${py}%`;
    });
  }
  x.addEventListener('input', update);
  y.addEventListener('input', update);
  source.addEventListener('load', update);
  source.addEventListener('click', (event) => {
    const rect = source.getBoundingClientRect();
    x.value = Math.round(clamp((event.clientX - rect.left) / rect.width * 100));
    y.value = Math.round(clamp((event.clientY - rect.top) / rect.height * 100));
    update();
  });
  form.querySelector('[data-focus-center]').addEventListener('click', () => {
    x.value = y.value = 50;
    update();
    x.focus();
  });
  form.addEventListener('submit', () => {
    form.querySelector('[data-image-submit]').disabled = true;
    form.querySelector('[data-image-status]').textContent = '正在生成图片，请稍候…';
    form.setAttribute('aria-busy', 'true');
  });
  window.addEventListener('pageshow', () => {
    form.querySelector('[data-image-submit]').disabled = false;
    form.querySelector('[data-image-status]').textContent = '';
    form.removeAttribute('aria-busy');
  });
  update();
})();
