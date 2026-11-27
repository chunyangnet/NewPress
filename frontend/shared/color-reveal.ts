// Prepared on interaction; the first selection waits briefly for the circular reveal.
import type { ColorReveal } from './color-types';

(() => {
  const root = document.documentElement;
  const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)');
  if (!document.startViewTransition || !('pseudoElement' in (window.KeyframeEffect?.prototype || {}))) return;
  let active: ViewTransition | null | undefined;
  reduced?.addEventListener('change', () => { if (reduced.matches) active?.skipTransition(); });

  const reveal: ColorReveal = (origin, apply) => {
    active?.skipTransition();
    active = null;
    if (!origin || reduced?.matches || document.body.matches('.theme-force-reduced-motion, .theme-motion-minimal')) {
      apply();
      return;
    }
    const { left, top, width, height } = origin.getBoundingClientRect();
    if (!width || !height) { apply(); return; }
    const x = left + width / 2, y = top + height / 2;
    const radius = Math.ceil(Math.hypot(Math.max(x, window.innerWidth - x), Math.max(y, window.innerHeight - y)));
    let transition: ViewTransition;
    try {
      transition = document.startViewTransition(() => {
        if (active !== transition) return;
        const running = new Set(document.getAnimations());
        apply();
        // Capture final control colors without cutting short any pre-existing animation.
        for (const animation of document.getAnimations()) {
          if (!running.has(animation) && 'transitionProperty' in animation && typeof animation.transitionProperty === 'string'
            && /color$|^(fill|stroke|box-shadow|text-shadow)$/.test(animation.transitionProperty)) animation.finish();
        }
      });
    } catch { apply(); return; }
    active = transition;
    const animations: Animation[] = [];
    transition.ready.then(() => {
      if (active !== transition) return;
      // Override the browser's crossfade only on these snapshots; leave other view transitions alone.
      const timing = { duration: 480, easing: 'ease-in' };
      const opaque = { opacity: [1, 1], mixBlendMode: ['normal', 'normal'] };
      const center = `at ${x}px ${y}px`;
      try {
        animations.push(root.animate(opaque, { ...timing, pseudoElement: '::view-transition-old(root)' }));
        animations.push(root.animate({ ...opaque, clipPath: [`circle(0px ${center})`, `circle(${radius}px ${center})`] },
          { ...timing, pseudoElement: '::view-transition-new(root)' }));
      } catch { transition.skipTransition(); }
    }, () => { /* Skipping a capture rejects ready but still runs its update. */ });
    const release = () => {
      animations.forEach(animation => animation.cancel());
      if (active === transition) active = null;
    };
    transition.finished.then(release, release);
  };
  window.ChunnelColorReveal = reveal;
})();
