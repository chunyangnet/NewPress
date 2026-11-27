(() => {
  'use strict';
  const body = document.querySelector('#article-body');
  const rail = document.querySelector('.article-rail');
  if (!body || !rail) return;
  const $ = (selector) => rail.querySelector(selector);
  const header = document.querySelector('.site-header');
  const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches || document.body.classList.contains('theme-force-reduced-motion');
  const headings = [...body.querySelectorAll('h2, h3')].filter(heading => !heading.closest('[data-np-widget]'));
  const toc = $('[data-reader-toc]');
  const entries = [];
  headings.forEach((heading, index) => {
    if (!heading.textContent.trim()) return;
    if (!heading.id) {
      let id = `reader-section-${index + 1}`;
      while (document.getElementById(id)) id += '-section';
      heading.id = id;
    }
    const link = document.createElement('a');
    link.href = `#${encodeURIComponent(heading.id)}`;
    link.textContent = heading.textContent.trim();
    link.classList.toggle('reader-subchapter', heading.tagName === 'H3');
    entries.push({ link, heading });
  });
  if (entries.length) toc.replaceChildren(...entries.map(({ link }) => link));
  else entries.push({ link: toc.querySelector('a'), heading: body });
  $('[data-reader-chapters]').textContent = headings.length ? `${entries.length} 节` : '';
  const text = body.textContent.trim();
  const chinese = (text.match(/[\u3400-\u9fff]/g) || []).length;
  const words = (text.replace(/[\u3400-\u9fff]/g, ' ').match(/\S+/g) || []).length;
  $('[data-reader-time]').textContent = `约 ${Math.max(1, Math.ceil(chinese / 350 + words / 220))} 分钟阅读`;
  const headerTime = document.querySelector('[data-article-reading-time]');
  if (headerTime) {
    headerTime.querySelector('span').textContent = $('[data-reader-time]').textContent;
    headerTime.hidden = false;
  }
  $('[data-reader-summary]').hidden = false;
  $('[data-reader-settings]').hidden = false;

  // Persist presentation only; never store article or preview content.
  let size = 0;
  try { const saved = Number(localStorage.getItem('chunnel.reader.size')); if (Number.isInteger(saved) && saved >= -1 && saved <= 2) size = saved; } catch { /* Storage is optional. */ }
  const sizes = ['紧凑', '标准', '舒适', '大字'];
  const applySize = () => {
    body.dataset.readerSize = String(size);
    $('[data-reader-size-label]').textContent = sizes[size + 1];
    $('[data-reader-size="-1"]').disabled = size === -1;
    $('[data-reader-size="1"]').disabled = size === 2;
  };
  rail.querySelectorAll('[data-reader-size]').forEach((button) => button.addEventListener('click', () => {
    size = Math.max(-1, Math.min(2, size + Number(button.dataset.readerSize)));
    applySize(); schedule();
    try { localStorage.setItem('chunnel.reader.size', String(size)); } catch { /* Keep this page usable. */ }
  }));
  applySize();
  const focus = $('[data-reader-focus]');
  const setFocus = (active) => {
    if (active === document.body.classList.contains('reader-focused')) return;
    document.body.classList.toggle('reader-focused', active);
    focus.setAttribute('aria-pressed', String(active));
    focus.querySelector('span').textContent = active ? '退出专注' : '专注阅读';
    window.scrollTo({ top: window.scrollY + rail.getBoundingClientRect().top - offset(), behavior: 'instant' });
    schedule();
  };
  focus.addEventListener('click', () => setFocus(focus.getAttribute('aria-pressed') !== 'true'));
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && !document.querySelector('dialog[open]')) setFocus(false);
  });

  const mobile = matchMedia('(max-width: 760px)');
  const directory = $('.reader-directory');
  const syncDirectory = () => { directory.open = !mobile.matches; };
  mobile.addEventListener('change', syncDirectory); syncDirectory();
  const offset = () => (header?.getBoundingClientRect().height || 0) + 28;
  entries.forEach(({ link, heading }) => link.addEventListener('click', (event) => {
    if (event.button || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    if (mobile.matches) directory.open = false;
    history.pushState(null, '', link.hash);
    heading.setAttribute('tabindex', '-1');
    heading.focus({ preventScroll: true });
    window.scrollTo({ top: window.scrollY + heading.getBoundingClientRect().top - offset(), behavior: reduced() ? 'instant' : 'smooth' });
  }));
  let frame = 0;
  let activeLink;
  directory.addEventListener('toggle', () => { activeLink = null; schedule(); });
  const update = () => {
    frame = 0;
    const rect = body.getBoundingClientRect();
    const top = offset();
    rail.style.setProperty('--reader-top', `${top}px`);
    const distance = Math.max(1, rect.height - Math.max(1, innerHeight - top));
    const value = rect.bottom <= innerHeight && rect.top <= top ? 1 : Math.max(0, Math.min(1, (top - rect.top) / distance));
    $('[data-reader-percent]').textContent = `${Math.round(value * 100)}%`;
    $('[data-reader-meter]').style.transform = `scaleX(${value})`;
    const progress = document.querySelector('[data-reading-progress]');
    if (progress) progress.style.transform = `scaleX(${value})`;
    const active = entries.filter(({ heading }) => heading.getBoundingClientRect().top <= top + 16).at(-1) || entries[0];
    entries.forEach(({ link }) => {
      if (link === active.link) link.setAttribute('aria-current', 'location');
      else link.removeAttribute('aria-current');
    });
    // Follow the current chapter inside the directory without moving the page.
    if (active.link !== activeLink && directory.open) {
      const item = active.link.getBoundingClientRect();
      const viewport = toc.getBoundingClientRect();
      if (item.top < viewport.top || item.bottom > viewport.bottom) {
        toc.scrollTo({ top: toc.scrollTop + item.top - viewport.top - (toc.clientHeight - item.height) / 2, behavior: 'instant' });
      }
    }
    activeLink = active.link;
  };
  function schedule() { if (!frame) frame = requestAnimationFrame(update); }
  window.addEventListener('scroll', schedule, { passive: true });
  window.addEventListener('resize', schedule);
  window.addEventListener('pageshow', schedule);
  if ('ResizeObserver' in window) { const observer = new ResizeObserver(schedule); observer.observe(body); if (header) observer.observe(header); }
  document.fonts?.ready.then(schedule);
  update();
})();
