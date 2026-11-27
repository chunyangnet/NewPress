export {};
/* Optional homepage components: loaded only when a configured block is visible. */
(() => {
  'use strict';
  const reduced = matchMedia('(prefers-reduced-motion: reduce)');
  const connection = (navigator as Navigator & { connection?: EventTarget & { saveData?: boolean } }).connection;
  const quiet = () => reduced.matches || document.body.matches('.theme-force-reduced-motion, .theme-motion-minimal');
  const carouselEffects = new Map([
    ['fade', 'chunnel-carousel-fade-in'],
    ['rotate-3d', 'chunnel-carousel-rotate-in'],
    ['stack', 'chunnel-carousel-stack-in'],
    ['accordion', 'chunnel-carousel-accordion-in'],
    ['page-turn', 'chunnel-carousel-page-out'],
  ]);
  document.querySelectorAll<HTMLElement>('[data-home-carousel]').forEach(root => {
    const track = root.querySelector<HTMLElement>('[data-carousel-track]');
    const slides = [...root.querySelectorAll<HTMLElement>('[data-carousel-slide]')];
    const controls = root.querySelector<HTMLElement>('[data-carousel-controls]');
    if (!track || slides.length < 2 || !controls) return;
    const dots = [...root.querySelectorAll<HTMLElement>('[data-carousel-dot]')];
    const status = root.querySelector('[data-carousel-status]');
    const effect = root.dataset.carouselEffect || 'slide';
    const layered = carouselEffects.has(effect);
    const accordion = effect === 'accordion';
    const images = layered ? slides.map(slide => slide.querySelector('img')) : [];
    const decodedImages = new WeakMap<HTMLImageElement, Promise<void>>();
    type Destination = { index: number; manual: boolean; direction: 'prev' | 'next' };
    let index = 0, outgoing = -1;
    let timer: ReturnType<typeof setTimeout> | undefined, scrollTimer: ReturnType<typeof setTimeout> | undefined, transitionTimer: ReturnType<typeof setTimeout> | undefined, controlsTimer: ReturnType<typeof setTimeout> | undefined;
    let queued: Destination | undefined, preparing: Destination | null | undefined;
    let controlsDeadline = 0;
    let visible = false, hover = false, ignoreClick = false;
    let pointer: { id: number; x: number; y: number; swipe: boolean } | null = null;
    const autoplay = root.dataset.autoplay === 'true';
    const autoHideControls = root.dataset.autohideControls !== 'false';
    const interval = Math.max(2, Math.min(15, Number(root.dataset.interval) || 3)) * 1000;
    const wrap = (next: number) => (next + slides.length) % slides.length;
    const panels = accordion ? slides.map((slide, i) => {
      const panel = document.createElement('button');
      panel.type = 'button';
      panel.hidden = true;
      panel.dataset.carouselPanel = '';
      panel.style.setProperty('--carousel-index', String(i));
      panel.setAttribute('aria-label', slide.getAttribute('aria-label') || '');
      return panel;
    }) : [];
    const scrollToSlide = (behavior: ScrollBehavior) => {
      track.scrollTo({left: (slides[index]?.offsetLeft || 0) - (slides[0]?.offsetLeft || 0), behavior});
    };
    const loadImage = (i: number) => {
      const image = images[i]; if (image?.loading === 'lazy') image.loading = 'eager';
    };
    const prepareImage = (i: number) => {
      const image = images[i];
      loadImage(i);
      if (!image || typeof image.decode !== 'function') return Promise.resolve();
      let ready = decodedImages.get(image);
      if (!ready) {
        try { ready = image.decode().catch(() => {}); }
        catch { ready = Promise.resolve(); }
        decodedImages.set(image, ready);
      }
      return ready;
    };
    const warmNext = () => {
      if (layered && visible && !connection?.saveData && !document.hidden && outgoing < 0 && !preparing) {
        prepareImage(wrap(index + 1));
      }
    };
    const cancelControlsHide = () => {
      clearTimeout(controlsTimer);
      controlsTimer = undefined;
    };
    const hideControls = () => {
      controlsTimer = undefined;
      if (!autoHideControls || root.matches(':focus-within') || pointer || document.hidden) return;
      const remaining = controlsDeadline - performance.now();
      if (remaining > 0) { controlsTimer = setTimeout(hideControls, remaining); return; }
      root.dataset.controlsHidden = 'true';
    };
    const showControls = () => {
      if (!autoHideControls) return;
      if (root.dataset.controlsHidden !== 'false') root.dataset.controlsHidden = 'false';
      // Pointer movement extends one idle deadline without churning DOM writes or timers.
      controlsDeadline = performance.now() + 2600;
      if (root.matches(':focus-within') || pointer || document.hidden) cancelControlsHide();
      else if (controlsTimer === undefined) controlsTimer = setTimeout(hideControls, 2600);
    };
    const canAutoplay = () => autoplay && !quiet() && !connection?.saveData && visible && !hover && !pointer && !document.hidden && !root.contains(document.activeElement);
    const schedule = () => {
      clearTimeout(timer);
      const allowed = canAutoplay();
      if (!allowed) {
        // An interaction cancels an automatic destination even while its image is decoding.
        if (preparing && !preparing.manual) preparing = null;
        if (queued && !queued.manual) queued = undefined;
      }
      const running = allowed && !preparing;
      if (root.dataset.autoplayRunning !== String(running)) root.dataset.autoplayRunning = String(running);
      if (running) timer = setTimeout(() => advance(1, false), interval);
    };
    const paint = () => {
      if (slides.some((slide, i) => i !== index && slide.contains(document.activeElement))) {
        track.focus({preventScroll: true});
      }
      if (panels[index] === document.activeElement) dots[index]?.focus({preventScroll: true});
      dots.forEach((dot, i) => dot.setAttribute('aria-current', String(i === index)));
      if (layered) root.style.setProperty('--carousel-active', String(index));
      slides.forEach((slide, i) => {
        slide.inert = i !== index;
        if (!layered) return;
        if (accordion) {
          const side = i < index ? 'before' : 'after';
          slide.dataset.carouselSide = side;
          const panel = panels[i]; if (panel) { panel.dataset.carouselSide = side; panel.hidden = i === index; }
          if (outgoing >= 0 && i !== index && i !== outgoing && (i > outgoing) !== (i > index)) {
            slide.dataset.carouselShift = '';
          } else {
            delete slide.dataset.carouselShift;
          }
        }
        if (i === index) slide.dataset.carouselState = outgoing >= 0 ? 'entering' : 'active';
        else if (i === outgoing) slide.dataset.carouselState = 'outgoing';
        else if (accordion || (effect === 'stack' && i === wrap(index + 1))) slide.dataset.carouselState = 'preview';
        else delete slide.dataset.carouselState;
      });
    };
    const settle = () => {
      if (outgoing < 0) return;
      clearTimeout(transitionTimer);
      delete track.dataset.carouselTransition;
      outgoing = -1;
      paint();
      const next = queued;
      queued = undefined;
      if (next && next.index !== index) move(next.index, next.manual, next.direction);
      else warmNext();
    };
    const playMove = (next: number, manual: boolean, direction: 'prev' | 'next') => {
      if (!manual && !canAutoplay()) { schedule(); return; }
      if (next === index && layered) return;
      if (layered) {
        if (!quiet() && !document.hidden) {
          outgoing = index;
          const from = slides[index], to = slides[next];
          if (from && to) { from.dataset.carouselState = 'outgoing'; to.dataset.carouselState = 'entering'; }
          root.style.setProperty('--carousel-previous', String(index));
          track.dataset.carouselDirection = direction;
          track.dataset.carouselTransition = '';
          transitionTimer = setTimeout(settle, 750);
        }
      }
      index = next;
      if (manual && status) status.textContent = `第 ${index + 1} 张，共 ${slides.length} 张`;
      if (!layered) scrollToSlide(quiet() ? 'instant' : 'smooth');
      paint();
      warmNext();
      schedule();
    };
    const move = (next: number, manual: boolean, direction?: 'prev' | 'next') => {
      if (!manual && !canAutoplay()) { schedule(); return; }
      next = wrap(next);
      direction ??= (next === index - 1 || (index === 0 && next === slides.length - 1)) ? 'prev' : 'next';
      if (outgoing >= 0) {
        // Keep the latest destination without interrupting the moving layers.
        queued = {index: next, manual, direction};
        prepareImage(next);
        schedule();
        return;
      }
      if (layered) {
        // A newer choice can replace a destination still waiting for image decode.
        if (preparing) preparing = null;
        if (next === index) { schedule(); return; }
        const request = {index: next, manual, direction};
        preparing = request;
        schedule();
        prepareImage(next).then(() => {
          if (preparing !== request) return;
          preparing = null;
          const destination = queued || request;
          queued = undefined;
          if (destination.index === request.index) playMove(destination.index, destination.manual, destination.direction);
          else if (destination.index !== index) move(destination.index, destination.manual, destination.direction);
          else { warmNext(); schedule(); }
        });
        return;
      }
      playMove(next, manual, direction);
    };
    const advance = (step: number, manual: boolean) => {
      if (manual) showControls();
      move((queued?.index ?? preparing?.index ?? index) + step, manual, step < 0 ? 'prev' : 'next');
    };
    root.querySelector<HTMLButtonElement>('[data-carousel-prev]')?.addEventListener('click', () => advance(-1, true));
    root.querySelector<HTMLButtonElement>('[data-carousel-next]')?.addEventListener('click', () => advance(1, true));
    dots.forEach((dot, i) => dot.addEventListener('click', () => move(i, true)));
    track.addEventListener('keydown', event => {
      if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
      event.preventDefault();
      advance(event.key === 'ArrowLeft' ? -1 : 1, true);
    });
    root.addEventListener('pointerdown', event => {
      if (!event.isPrimary || event.button !== 0) return;
      ignoreClick = false;
      pointer = {id: event.pointerId, x: event.clientX, y: event.clientY, swipe: layered && event.target instanceof Node && track.contains(event.target)};
      showControls();
      schedule();
    }, {passive: true});
    root.addEventListener('pointermove', event => {
      if (event.isPrimary) showControls();
    }, {passive: true});
    const release = (event: PointerEvent) => {
      if (pointer?.id !== event.pointerId) return;
      const {x, y, swipe} = pointer;
      pointer = null;
      if (swipe && event.type === 'pointerup') {
        const dx = event.clientX - x;
        const dy = event.clientY - y;
        if (Math.abs(dx) > 35 && Math.abs(dx) > Math.abs(dy) * 1.25) {
          ignoreClick = true;
          advance(dx < 0 ? 1 : -1, true);
          setTimeout(() => { ignoreClick = false; }, 0);
          return;
        }
      }
      showControls();
      schedule();
    };
    document.addEventListener('pointerup', release, {passive: true});
    document.addEventListener('pointercancel', release, {passive: true});
    if (layered) {
      track.addEventListener('dragstart', event => event.preventDefault());
      root.addEventListener('click', event => {
        if (!ignoreClick || !event.detail) return;
        ignoreClick = false;
        event.preventDefault();
        event.stopPropagation();
      }, true);
      slides.forEach(slide => slide.addEventListener('animationend', event => {
        // The other slide's end event can arrive after a queued transition has started.
        const completionSlide = effect === 'page-turn' ? slides[outgoing] : slides[index];
        if (outgoing >= 0 && slide === completionSlide && event.target === slide
          && !event.pseudoElement && event.animationName === carouselEffects.get(effect)) settle();
      }));
      if (accordion) {
        root.style.setProperty('--carousel-count', String(slides.length));
        slides.forEach((slide, i) => slide.style.setProperty('--carousel-index', String(i)));
        panels.forEach((panel, i) => panel.addEventListener('click', () => move(i, true)));
        controls.prepend(...panels);
      }
    } else {
      track.addEventListener('scroll', () => {
        clearTimeout(scrollTimer);
        scrollTimer = setTimeout(() => {
          if (!track.clientWidth) return;
          index = Math.max(0, Math.min(slides.length - 1, Math.round(track.scrollLeft / track.clientWidth)));
          paint();
          schedule();
        }, 120);
      }, {passive: true});
      new ResizeObserver(() => scrollToSlide('instant')).observe(track);
    }
    root.addEventListener('pointerenter', event => { if (event.pointerType === 'mouse') {hover = true; showControls(); schedule();} });
    root.addEventListener('pointerleave', event => { if (event.pointerType === 'mouse') {hover = false; showControls(); schedule();} });
    root.addEventListener('focusin', () => { showControls(); schedule(); });
    root.addEventListener('focusout', () => setTimeout(() => { showControls(); schedule(); }, 0));
    document.addEventListener('visibilitychange', () => {
      if (document.hidden) {
        pointer = null;
        cancelControlsHide();
        settle();
      } else showControls();
      warmNext();
      schedule();
    });
    reduced.addEventListener('change', () => {
      if (quiet()) settle();
      schedule();
    });
    connection?.addEventListener?.('change', schedule);
    new IntersectionObserver(([entry]) => {
      visible = !!entry && entry.intersectionRatio >= .2;
      warmNext();
      schedule();
    }, {threshold: .2}).observe(root);
    paint();
    if (layered) root.dataset.carouselMode = effect;
    showControls();
    controls.hidden = false;
    schedule();
  });

  document.querySelectorAll<HTMLElement>('[data-home-background]').forEach(root => {
    const video = root.querySelector<HTMLVideoElement>('[data-background-video]');
    const picture = root.querySelector<HTMLImageElement>('[data-background-image]');
    const available = root.dataset.homeBackground === 'gradient' || video || picture;
    if (!available || root.dataset.allowMotion !== 'true') return;
    let failed = false, visible = false, generation = 0;
    const paint = (running: boolean) => { root.dataset.running = String(running); };
    const refresh = () => {
      const version = ++generation;
      const running = !failed && !quiet() && !connection?.saveData && visible && !document.hidden;
      paint(running);
      if (picture) {
        picture.hidden = !running;
        if (running && !picture.getAttribute('src')) picture.src = picture.dataset.bgSrc || '';
      }
      if (!video) return;
      if (!running) { video.pause(); video.hidden = true; return; }
      video.muted = true;
      if (!video.getAttribute('src')) video.src = video.dataset.bgSrc || '';
      video.play().then(() => { if (version === generation) video.hidden = false; }).catch(() => {
        if (version !== generation) return;
        failed = true; video.hidden = true; paint(false);
      });
    };
    const unavailable = () => { failed = true; generation++; if (video) {video.pause(); video.hidden = true;} if (picture) picture.hidden = true; paint(false); };
    video?.addEventListener('error', unavailable);
    picture?.addEventListener('error', unavailable);
    document.addEventListener('visibilitychange', refresh);
    reduced.addEventListener('change', refresh);
    connection?.addEventListener?.('change', refresh);
    new IntersectionObserver(entries => { visible = entries[0]?.isIntersecting === true; refresh(); }, {threshold:.15}).observe(root);
    refresh();
  });
})();
