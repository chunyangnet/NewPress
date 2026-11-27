import type { MotionAPI, SurfaceValues, SurfaceOptions } from './motion-types';

// Retargetable surface springs. CSS owns small controls; this runs only after
// interaction on home, and only while a dialog surface is actually moving.
(() => {
  type Velocity = Pick<SurfaceValues, 'x' | 'y' | 'scale'>;
  interface MotionState {
    value: SurfaceValues;
    velocity: Velocity;
    original: Pick<CSSStyleDeclaration, 'transform' | 'opacity'>;
    running: boolean;
    advance(time: number): void;
    stop(): void;
    finish(): void;
  }
  const records = new WeakMap<HTMLElement, MotionState>();
  const active = new Set<MotionState>();
  const preference = matchMedia('(prefers-reduced-motion: reduce)');
  const quiet = () => preference.matches || document.body.matches('.theme-motion-minimal, .theme-force-reduced-motion, .theme-popup-off');
  const keys = ['x', 'y', 'scale'] as const;
  const defaults = { x: 0, y: 0, scale: 1, opacity: 1 };

  // Exact integration avoids frame-rate-dependent damping and catches up after
  // a delayed frame. Both position and velocity survive a direction change.
  function step(value: number, velocity: number, target: number, dt: number, omega: number, damping: number): [number, number] {
    const offset = value - target;
    const decay = Math.exp(-damping * omega * dt);
    if (damping === 1) {
      const slope = velocity + omega * offset;
      return [target + (offset + slope * dt) * decay, (velocity - omega * slope * dt) * decay];
    }
    const frequency = omega * Math.sqrt(1 - damping * damping);
    const slope = (velocity + damping * omega * offset) / frequency;
    const sine = Math.sin(frequency * dt), cosine = Math.cos(frequency * dt);
    const next = offset * cosine + slope * sine;
    return [target + decay * next, decay * ((-offset * sine + slope * cosine) * frequency - damping * omega * next)];
  }

  function surface(element: HTMLElement, from: Partial<SurfaceValues>, to: Partial<SurfaceValues>, { duration = 320, lively = false }: SurfaceOptions = {}) {
    const previous = records.get(element);
    const now = performance.now();
    // Start at the last painted position so retargeting cannot jump between frames.
    const value = { ...defaults, ...(previous?.value || from) };
    const velocity = { x: 0, y: 0, scale: 0, ...previous?.velocity };
    previous?.stop();
    const goal = { ...defaults, ...to };
    const original = previous?.original || { transform: element.style.transform, opacity: element.style.opacity };
    let resolve: (() => void) | undefined, frame = 0, last = now;
    const finished = new Promise<void>(done => { resolve = done; });
    const startOpacity = value.opacity;
    const milliseconds = Math.max(120, Math.min(600, duration));
    const omega = 9 / (milliseconds / 1000);
    const state = { value, velocity, original, running: true, advance, stop, finish };
    records.set(element, state); active.add(state);

    function paint() {
      element.style.transform = `translate3d(${value.x}px,${value.y}px,0) scale(${value.scale})`;
      element.style.opacity = String(value.opacity);
    }
    function stop() {
      cancelAnimationFrame(frame);
      state.running = false;
      active.delete(state);
      resolve?.();
    }
    function finish() {
      Object.assign(value, goal);
      keys.forEach(key => { velocity[key] = 0; });
      paint(); stop();
    }
    function advance(time: number) {
      const dt = Math.max(0, time - last) / 1000;
      last = time;
      keys.forEach(key => {
        [value[key], velocity[key]] = step(value[key], velocity[key], goal[key], dt, omega, lively ? .82 : 1);
      });
      const progress = Math.max(0, Math.min(1, (time - now) / (milliseconds * .7)));
      value.opacity = startOpacity + (goal.opacity - startOpacity) * (1 - (1 - progress) ** 3);
      paint();
    }
    function tick(time: number) {
      if (quiet() || document.hidden || !element.isConnected) { finish(); return; }
      advance(time);
      const settled = keys.every(key => Math.abs(value[key] - goal[key]) < (key === 'scale' ? .0001 : .02) && Math.abs(velocity[key]) < (key === 'scale' ? .001 : .2));
      if ((settled && time - now >= milliseconds * .7) || time - now > milliseconds * 2) finish();
      else frame = requestAnimationFrame(tick);
    }
    if (quiet() || document.hidden) finish();
    else { paint(); frame = requestAnimationFrame(tick); }
    return {
      finished,
      cancel() {
        if (records.get(element) !== state) return;
        stop(); element.style.transform = original.transform; element.style.opacity = original.opacity;
        records.delete(element);
      },
    };
  }
  const finishAll = () => [...active].forEach(state => state.finish());
  preference.addEventListener('change', () => { if (quiet()) finishAll(); });
  document.addEventListener('visibilitychange', () => { if (document.hidden) finishAll(); });
  window.addEventListener('pagehide', finishAll);
  new MutationObserver(() => { if (quiet()) finishAll(); }).observe(document.body, { attributes: true, attributeFilter: ['class'] });
  window.ChunnelMotion = { surface } satisfies MotionAPI;
})();
