// Native slides remain fully readable if an enhancement cannot initialize.
const focusWithoutScroll = (element: HTMLElement | null) => element?.focus({ preventScroll: true });
type Motion = {media: MediaQueryList; allowed(): boolean};
type Browser = Window & typeof globalThis;
const savesData = (win: Window) => 'connection' in win.navigator && !!(win.navigator.connection as {saveData?: boolean} | undefined)?.saveData;

export const carouselInterval = (value: unknown) => Math.min(15, Math.max(2, Number(value) || 5)) * 1000;
export const carouselIndex = (value: number, count: number) => count > 0 ? ((value % count) + count) % count : 0;

function initFilters(doc: Document) {
  const panels = [...doc.querySelectorAll<HTMLDetailsElement>('.filter-panel')];
  panels.forEach(panel => panel.addEventListener('toggle', () => {
    if (panel.open) panels.forEach(other => { if (other !== panel) other.open = false; });
  }));
  doc.addEventListener('click', event => panels.forEach(panel => {
    if (panel.open && (!(event.target instanceof Node) || !panel.contains(event.target))) panel.open = false;
  }));
  doc.addEventListener('keydown', event => {
    if (event.key !== 'Escape' || event.defaultPrevented) return;
    const panel = panels.find(item => item.open && item.contains(doc.activeElement));
    if (!panel) return;
    event.preventDefault();
    panel.open = false;
    panel.querySelector('summary')?.focus();
  });
}

function initBackgroundVideo(doc: Document, win: Browser, motion: Motion) {
  doc.querySelectorAll<HTMLElement>('.home-search-banner[data-background-motion=true]').forEach(root => {
    const video = root.querySelector<HTMLVideoElement>('.home-search-video');
    const button = root.querySelector<HTMLElement>('[data-background-pause]');
    if (!video && !root.classList.contains('home-search-gradient')) return;
    let visible = !('IntersectionObserver' in win);
    let paused = savesData(win);
    let attempt = 0;
    const update = () => {
      const currentAttempt = ++attempt;
      const playing = visible && !paused && !doc.hidden && motion.allowed();
      root.dataset.backgroundPlaying = String(playing);
      if (button) {
        button.hidden = !motion.allowed();
        button.textContent = paused ? '播放背景' : '暂停背景';
        button.setAttribute('aria-pressed', String(paused));
      }
      if (!video) return;
      if (!playing) { video.pause(); delete video.dataset.playing; return; }
      const play = video.play();
      if (play?.then) play.then(() => {
        if (currentAttempt !== attempt) { if (!visible || paused || doc.hidden || !motion.allowed()) video.pause(); return; }
        if (motion.allowed() && !doc.hidden && visible && !paused) video.dataset.playing = 'true';
        else { video.pause(); delete video.dataset.playing; }
      }).catch(() => {
        if (currentAttempt !== attempt) return;
        paused = true;
        video.pause();
        delete video.dataset.playing;
        root.dataset.backgroundPlaying = 'false';
        if (button) { button.textContent = '播放背景'; button.setAttribute('aria-pressed', 'true'); }
      });
    };
    button?.addEventListener('click', () => { paused = !paused; update(); });
    if ('IntersectionObserver' in win) {
      new win.IntersectionObserver(entries => { visible = entries.some(item => item.isIntersecting); update(); }).observe(root);
    }
    doc.addEventListener('visibilitychange', update);
    motion.media.addEventListener?.('change', update);
    update();
  });
}

export function initCarousel(doc: Document, win: Browser, motion: Motion) {
  initFilters(doc);
  initBackgroundVideo(doc, win, motion);
  doc.querySelectorAll<HTMLElement>('[data-carousel]').forEach(root => {
    const track = root.querySelector<HTMLElement>('[data-carousel-track]');
    const slides = [...root.querySelectorAll<HTMLElement>('[data-carousel-slide]')];
    const controls = root.querySelector<HTMLElement>('[data-carousel-controls]');
    const previous = root.querySelector('[data-carousel-prev]');
    const next = root.querySelector('[data-carousel-next]');
    const status = root.querySelector('[data-carousel-status]');
    const pause = root.querySelector<HTMLElement>('[data-carousel-pause]');
    if (!track || !slides.length || !controls || !previous || !next || !status) return;
    let index = 0, timer = 0, hovered = false, focused = false, paused = savesData(win), focusPaused = false, pointerOnPause = false, visible = true, explicitlyPlaying = false, suppressClickUntil = 0;
    let pointer: {x: number; y: number} | null = null;
    const wantsAuto = root.dataset.carouselAutoplay === 'true';
    const canPlay = () => wantsAuto && slides.length > 1 && !paused && !focusPaused && (explicitlyPlaying || !hovered) && !doc.hidden && visible && motion.allowed();
    const schedule = () => {
      win.clearTimeout(timer);
      track.setAttribute('aria-live', canPlay() ? 'off' : 'polite');
      if (pause) {
        pause.hidden = !wantsAuto || slides.length < 2 || !motion.allowed();
        pause.textContent = paused || focusPaused ? '播放轮播' : '暂停轮播';
        pause.setAttribute('aria-pressed', String(paused || focusPaused));
      }
      if (canPlay()) timer = win.setTimeout(() => show(index + 1, false), carouselInterval(root.dataset.carouselInterval));
    };
    const show = (value: number, user = true) => {
      index = carouselIndex(value, slides.length);
      slides.forEach((slide, number) => {
        const current = number === index;
        slide.hidden = !current;
        slide.classList.remove('is-entering');
        if (current && motion.allowed()) { void slide.offsetWidth; slide.classList.add('is-entering'); }
      });
      status.textContent = (index + 1) + ' / ' + slides.length;
      if (user) status.setAttribute('aria-live', 'polite');
      else status.setAttribute('aria-live', 'off');
      schedule();
    };
    previous.addEventListener('click', () => show(index - 1));
    next.addEventListener('click', () => show(index + 1));
    const cancelPointerIntent = () => {
      if (!pointerOnPause) return;
      pointerOnPause = false;
      if (doc.activeElement === pause) { focusPaused = true; explicitlyPlaying = false; schedule(); }
    };
    pause?.addEventListener('pointerdown', () => { pointerOnPause = true; });
    pause?.addEventListener('pointerup', () => { win.setTimeout(cancelPointerIntent, 0); });
    pause?.addEventListener('pointercancel', cancelPointerIntent);
    doc.addEventListener('pointerup', () => { if (pointerOnPause) win.setTimeout(cancelPointerIntent, 0); });
    doc.addEventListener('pointercancel', cancelPointerIntent);
    pause?.addEventListener('click', () => {
      const resume = paused || focusPaused;
      paused = !resume; focusPaused = false; explicitlyPlaying = resume; pointerOnPause = false; schedule();
    });
    root.addEventListener('pointerenter', event => { if (event.pointerType !== 'touch') { explicitlyPlaying = false; hovered = true; schedule(); } });
    root.addEventListener('pointerleave', () => { hovered = false; schedule(); });
    root.addEventListener('focusin', event => {
      if (!(event.target === pause && pointerOnPause)) { focusPaused = true; explicitlyPlaying = false; }
      focused = true; schedule();
    });
    root.addEventListener('focusout', event => { focused = event.relatedTarget instanceof Node && root.contains(event.relatedTarget); if (!focused) explicitlyPlaying = false; schedule(); });
    root.addEventListener('keydown', event => {
      if (!(event.target instanceof Element) || event.altKey || event.ctrlKey || event.metaKey || /INPUT|TEXTAREA|SELECT/.test(event.target.tagName)) return;
      if (!['ArrowLeft', 'ArrowRight'].includes(event.key)) return;
      event.preventDefault();
      const targetNode = event.target;
      const moveFocus = slides.some(slide => slide.contains(targetNode));
      show(index + (event.key === 'ArrowLeft' ? -1 : 1));
      if (moveFocus) {
        const slide = slides[index]; if (!slide) return;
        let target = slide.querySelector<HTMLElement>('a[href],button');
        if (!target) { slide.setAttribute('tabindex', '-1'); target = slide; }
        focusWithoutScroll(target);
      }
    });
    track.addEventListener('pointerdown', event => { if (event.pointerType === 'touch') pointer = { x: event.clientX, y: event.clientY }; });
    track.addEventListener('pointerup', event => {
      if (!pointer) return;
      const deltaX = event.clientX - pointer.x, deltaY = event.clientY - pointer.y;
      pointer = null;
      if (Math.abs(deltaX) > 55 && Math.abs(deltaX) > Math.abs(deltaY) * 1.3) {
        suppressClickUntil = Date.now() + 500;
        show(index + (deltaX < 0 ? 1 : -1));
      }
    });
    track.addEventListener('click', event => { if (Date.now() < suppressClickUntil) { event.preventDefault(); event.stopPropagation(); } }, true);
    track.addEventListener('pointercancel', () => { pointer = null; });
    doc.addEventListener('visibilitychange', schedule);
    motion.media.addEventListener?.('change', schedule);
    if ('IntersectionObserver' in win) new win.IntersectionObserver(entries => { visible = entries.some(entry => entry.isIntersecting); schedule(); }).observe(root);
    controls.hidden = slides.length < 2;
    root.dataset.carouselReady = 'true';
    show(0, false);
  });
}
