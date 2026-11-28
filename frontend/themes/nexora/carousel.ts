export function wrappedSlide(index: number, length: number) {
  return length > 0 && Number.isInteger(index) ? ((index % length) + length) % length : 0;
}

export function initCarousel(doc: Document, win = doc.defaultView) {
  const hero = doc.querySelector<HTMLElement>('[data-hero]');
  if (!hero || !win) return;
  const slides = [...hero.querySelectorAll<HTMLElement>('[data-slide]')];
  const buttons = [...hero.querySelectorAll<HTMLButtonElement>('[data-slide-to]')];
  const controls = hero.querySelector<HTMLElement>('[data-hero-controls]');
  if (slides.length < 2) return;
  const count = hero.querySelector('[data-current-slide]');
  const status = hero.querySelector('[data-slide-status]');
  const playback = hero.querySelector<HTMLButtonElement>('[data-slide-playback]');
  const motion = win?.matchMedia?.('(prefers-reduced-motion: reduce)');
  const motionDisabled = hero.dataset.motion === 'false';
  const autoplay = hero.dataset.autoplay === 'true' && !!win?.setTimeout;
  const interval = Math.min(15, Math.max(5, Number(hero.dataset.interval) || 7)) * 1000;
  let playing = autoplay && !motionDisabled && !motion?.matches;
  let hovered = false;
  let visible = true;
  let pointerPlaybackState: boolean | null = null;
  let timer: number | undefined;
  let current = Math.max(0, slides.findIndex(slide => !slide.hidden));
  const schedule = () => {
    if (timer !== undefined) win.clearTimeout(timer);
    timer = undefined;
    if (playback) {
      playback.hidden = !autoplay;
      playback.disabled = motionDisabled || !!motion?.matches;
      playback.textContent = playing ? '暂停轮播' : '播放轮播';
      playback.setAttribute('aria-label', playback.disabled ? '已根据减少动态效果设置暂停轮播' : playback.textContent);
    }
    if (!playing || hovered || !visible || doc.hidden || motionDisabled || motion?.matches) return;
    timer = win.setTimeout(() => { timer = undefined; show(current + 1, false); schedule(); }, interval);
  }
  function stop() { playing = false; schedule(); }
  function manual(index: number) { stop(); show(index); }
  function show(index: number, announce = true) {
    if (!Number.isInteger(index)) return;
    const next = wrappedSlide(index, slides.length);
    const focusInside = slides[current]?.contains(doc.activeElement);
    slides.forEach((slide, i) => {
      slide.hidden = i !== next;
      slide.classList.toggle('is-entering', announce && i === next && next !== current);
    });
    buttons.forEach(button => button.setAttribute('aria-pressed', String(Number(button.dataset.slideTo) === next)));
    current = next;
    if (count) count.textContent = String(current + 1).padStart(2, '0');
    if (announce && status) status.textContent = `第 ${current + 1} 张，共 ${slides.length} 张`;
    if (focusInside) buttons.find(button => Number(button.dataset.slideTo) === current)?.focus();
  }
  if (controls) controls.hidden = false;
  show(current, false);
  buttons.forEach(button => button.addEventListener('click', () => manual(Number(button.dataset.slideTo))));
  hero.querySelectorAll<HTMLElement>('[data-slide-step]').forEach(button => button.addEventListener('click', () => manual(current + Number(button.dataset.slideStep))));
  controls?.addEventListener('keydown', event => {
    if (!['ArrowRight', 'ArrowLeft', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? slides.length - 1 : current + (event.key === 'ArrowRight' ? 1 : -1);
    manual(next);
    if (event.target instanceof Element && event.target.hasAttribute('data-slide-to')) buttons.find(button => Number(button.dataset.slideTo) === current)?.focus();
  });
  // Pointer focus pauses before click; preserve the action shown at pointerdown.
  playback?.addEventListener('pointerdown', event => {
    pointerPlaybackState = event.button === 0 && event.isPrimary !== false ? playing : null;
  });
  playback?.addEventListener('pointercancel', () => { pointerPlaybackState = null; });
  playback?.addEventListener('click', event => {
    const wasPlaying = event.detail > 0 && pointerPlaybackState !== null ? pointerPlaybackState : playing;
    pointerPlaybackState = null;
    playing = !wasPlaying && !motionDisabled && !motion?.matches;
    schedule();
  });
  if (autoplay) {
    hero.addEventListener('mouseenter', () => { hovered = true; schedule(); });
    hero.addEventListener('mouseleave', () => { hovered = false; schedule(); });
    // Keyboard focus pauses until the reader explicitly restarts playback.
    hero.addEventListener('focusin', stop);
    doc.addEventListener('visibilitychange', schedule);
    motion?.addEventListener?.('change', () => { if (motion.matches) playing = false; schedule(); });
    if (win.IntersectionObserver) {
      new win.IntersectionObserver(entries => { visible = entries.some(entry => entry.isIntersecting); schedule(); }).observe(hero);
    }
  }
  schedule();
}
