// A single decorative lens. Links keep their native tap, keyboard and new-window behavior.
(() => {
  type Item = HTMLAnchorElement | (HTMLButtonElement & { host?: never; href?: never });
  interface Gesture { id: number; item: Item; startX: number; startY: number; x: number; y: number; quiet: boolean; dragging?: boolean }
  const bar = document.querySelector<HTMLElement & { chunnelSelect?: () => void }>('html[data-style="liquid-glass"] .mobile-bottom-nav');
  if (!bar || !window.PointerEvent || document.body.matches('.theme-mobile-gesture-off')) return;
  const items = [...bar.querySelectorAll<Item>('a, button')].filter(item => !item.classList.contains('mobile-create'));
  if (!items.length) return;
  const lens = document.createElement('span');
  lens.className = 'nav-lens'; lens.setAttribute('aria-hidden', 'true'); bar.prepend(lens);
  const reduced = matchMedia('(prefers-reduced-motion: reduce)');
  let gesture: Gesture | null | undefined, frame = 0, suppressed: number | null | undefined;
  let bounds: DOMRect, boxes: DOMRect[], preview: Item | null | undefined, origin: number, width: number;
  const current = () => items.find(item => item.matches('[aria-current=page]'));
  const highlight = (item: Item | null | undefined) => {
    if (preview === item) return;
    preview?.classList.remove('is-target'); preview = item; preview?.classList.add('is-target');
  };
  const paint = (item: Item | null | undefined, x?: number, y = 0) => {
    lens.style.opacity = item ? '1' : '0';
    if (!item) return;
    const box = boxes[items.indexOf(item)];
    if (!box) return;
    const stretch = gesture && !gesture.quiet ? 1.05 : 1;
    lens.style.transform = `translate3d(${(x ?? box.left - origin) + (box.width - width) / 2}px,${y}px,0) scale(${box.width / width * stretch},${stretch})`;
  };
  const settle = (item: Item | null | undefined = current()) => {
    cancelAnimationFrame(frame); frame = 0;
    const old = gesture; gesture = null;
    if (old && bar.hasPointerCapture(old.id)) bar.releasePointerCapture(old.id);
    bar.classList.remove('is-pressing', 'is-dragging'); highlight(item === current() ? null : item);
    paint(item);
  };
  // Measure on resize, page restore or an explicit account-view change.
  // Gestures write transforms, never widths.
  const measure = bar.chunnelSelect = () => {
    bounds = bar.getBoundingClientRect(); boxes = items.map(item => item.getBoundingClientRect());
    origin = bounds.left + bar.clientLeft;
    width = boxes[0]?.width || 1;
    lens.classList.remove('is-ready');
    lens.style.width = width + 'px';
    bar.classList.toggle('has-lens', bounds.width > 0);
    if (gesture) suppressed = gesture.id;
    settle();
  };
  const cancel = () => { if (gesture) { suppressed = gesture.id; settle(); } };
  const targetAt = (x: number, y: number) => {
    if (x < bounds.left || x > bounds.right || y < bounds.top - 12 || y > bounds.bottom + 12) return null;
    // measure() maps the nonempty item list before any pointer event can run.
    const nearest = boxes.reduce((best, box) => Math.abs(x - box.left - box.width / 2) < Math.abs(x - best.left - best.width / 2) ? box : best);
    if (x < nearest.left - 3 || x > nearest.right + 3) return null;
    return items[boxes.indexOf(nearest)] || null;
  };
  const move = () => {
    frame = 0;
    if (!gesture) return;
    const { x, y, startX, startY, item, dragging } = gesture;
    const target = dragging ? targetAt(x, y) : item;
    highlight(target);
    if (!target || gesture.quiet) { paint(target); return; }
    const box = boxes[items.indexOf(target)];
    const start = boxes[items.indexOf(item)];
    if (!box || !start) return;
    // Preserve where the finger landed, including near an edge, without spilling out of the bar.
    const center = Math.max(bounds.left + box.width / 2 + 6, Math.min(bounds.right - box.width / 2 - 6,
      start.left + start.width / 2 + (x - startX) * (dragging ? 1 : .25)));
    // A little attraction to the destination keeps the lens legible while it follows the finger.
    const offset = (center - box.left - box.width / 2) * .72;
    paint(target, box.left - origin + offset, Math.max(-6, Math.min(6, (y - startY) * .18)));
  };
  bar.addEventListener('pointerdown', (event) => {
    if (!event.isPrimary || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || bounds.width === 0) return;
    const item = event.target instanceof Element ? event.target.closest<Item>('a, button') : null;
    if (!item || !items.includes(item)) return;
    suppressed = null; lens.classList.add('is-ready'); settle(item);
    gesture = { id: event.pointerId, item, startX: event.clientX, startY: event.clientY, x: event.clientX, y: event.clientY, quiet: reduced.matches || document.body.matches('.theme-force-reduced-motion, .theme-motion-minimal') };
    bar.classList.add('is-pressing'); move();
  });
  bar.addEventListener('pointermove', (event) => {
    if (!gesture || gesture.id !== event.pointerId) return;
    if (event.buttons === 0) { cancel(); return; }
    const dx = Math.abs(event.clientX - gesture.startX), dy = Math.abs(event.clientY - gesture.startY);
    if (!gesture.dragging && dy > 10 && dy > dx) { cancel(); return; }
    if (!gesture.dragging && dx > 8 && dx > dy) {
      gesture.dragging = true; bar.setPointerCapture(event.pointerId); bar.classList.add('is-dragging');
    }
    gesture.x = event.clientX; gesture.y = event.clientY;
    if (!frame) frame = requestAnimationFrame(move);
  });
  bar.addEventListener('pointerup', (event) => {
    if (!gesture || gesture.id !== event.pointerId) return;
    const dragged = gesture.dragging;
    const target = targetAt(event.clientX, event.clientY);
    const destination = target && (dragged ? target : gesture.item);
    if (dragged || !target) suppressed = event.pointerId;
    // Keep a different document selected while it loads. Anchors and external
    // handlers stay on this page, as do new-window and download links.
    settle(destination?.host && !destination.matches('[target=_blank],[download]') && destination.href.split('#')[0] !== location.href.split('#')[0] ? destination : current());
    // Click synchronously in the user activation so target=_blank and lazy drawers still work.
    if (dragged && target) { target.focus({ preventScroll: true }); target.click(); }
  });
  bar.addEventListener('pointercancel', event => { if (gesture?.id === event.pointerId) cancel(); });
  bar.addEventListener('pointerleave', () => { if (!gesture?.dragging) cancel(); });
  // Touch starts with implicit capture on the link. Its transfer to the bar is not cancellation.
  bar.addEventListener('lostpointercapture', event => { if (event.target === bar && gesture?.id === event.pointerId) cancel(); });
  bar.addEventListener('dragstart', event => { if (gesture) event.preventDefault(); });
  bar.addEventListener('contextmenu', cancel);
  document.addEventListener('pointerdown', event => {
    suppressed = null;
    if (gesture && (!event.isPrimary || !(event.target instanceof Node) || !bar.contains(event.target))) cancel();
  }, true);
  // Capturing precedes the homepage's lazy click handler. Never consume keyboard or replayed clicks.
  document.addEventListener('click', event => {
    if (suppressed != null && event.detail > 0 && (event.pointerId === suppressed || event.target instanceof Node && bar.contains(event.target))) {
      suppressed = null; event.preventDefault(); event.stopImmediatePropagation();
    }
  }, true);
  document.addEventListener('keydown', event => { if (event.key === 'Escape') cancel(); });
  document.addEventListener('visibilitychange', cancel);
  window.addEventListener('pagehide', cancel);
  window.addEventListener('pageshow', measure);
  window.addEventListener('resize', measure);
  window.addEventListener('blur', cancel);
  reduced.addEventListener('change', cancel);
  new ResizeObserver(measure).observe(bar);
  measure();
})();
