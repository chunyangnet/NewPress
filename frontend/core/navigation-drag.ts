interface NavigationDragOptions {
  list: HTMLElement;
  locked(): boolean;
  depth(card: HTMLElement): number;
  move(source: HTMLElement, target: HTMLElement, after: boolean): void;
  keyboard(card: HTMLElement, key: string): void;
}

interface DragState {
  id: number;
  card: HTMLElement;
  handle: HTMLButtonElement;
  x: number;
  y: number;
  startX: number;
  startY: number;
  active: boolean;
  target: HTMLElement | null;
  after: boolean;
}

export function navigationDrag(options: NavigationDragOptions): (card: HTMLElement) => void {
  const { list } = options;
  let drag: DragState | null = null, scrollFrame = 0, lastScroll = 0;
  const locked = () => options.locked() || Boolean(drag?.handle.matches(':disabled'));
  const locate = () => {
    if (!drag) return;
    const hit = document.elementFromPoint(drag.x, drag.y)?.closest<HTMLElement>('[data-navigation-item]');
    const candidate = hit && list.contains(hit) && hit !== drag.card && options.depth(hit) === options.depth(drag.card) ? hit : null;
    if (drag.target !== candidate) {
      drag.target?.classList.remove('is-drop-target'); drag.target = candidate;
    }
    if (candidate) {
      candidate.classList.add('is-drop-target');
      const bounds = candidate.getBoundingClientRect();
      drag.after = drag.y > bounds.top + bounds.height / 2;
    }
  };
  const finish = (commit: boolean) => {
    const current = drag;
    if (!current) return;
    const accepted = commit && current.active && current.target && !locked();
    drag = null;
    cancelAnimationFrame(scrollFrame); scrollFrame = 0; lastScroll = 0;
    current.card.classList.remove('is-drag-source'); current.target?.classList.remove('is-drop-target');
    list.classList.remove('is-dragging');
    if (current.handle.hasPointerCapture(current.id)) current.handle.releasePointerCapture(current.id);
    if (accepted && current.target) options.move(current.card, current.target, current.after);
    if (current.handle.isConnected) current.handle.focus({ preventScroll: true });
  };
  const scroll = (now: number) => {
    if (!drag?.active) return;
    if (locked()) { finish(false); return; }
    let scroller: Element = document.scrollingElement || document.documentElement;
    for (let parent: HTMLElement | null = list; parent; parent = parent.parentElement) {
      if (/auto|scroll/.test(getComputedStyle(parent).overflowY) && parent.scrollHeight > parent.clientHeight) { scroller = parent; break; }
    }
    const bounds = scroller === document.scrollingElement ? { top: 0, bottom: innerHeight } : scroller.getBoundingClientRect();
    const speed = drag.y < bounds.top + 76 ? -1 : drag.y > bounds.bottom - 76 ? 1 : 0;
    const elapsed = lastScroll ? Math.min(32, now - lastScroll) : 16; lastScroll = now;
    if (speed) { scroller.scrollBy({ top: speed * elapsed * 0.6, behavior: 'instant' }); locate(); }
    scrollFrame = requestAnimationFrame(scroll);
  };
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && drag) { event.preventDefault(); finish(false); }
  });
  document.addEventListener('pointerdown', event => { if (drag && drag.id !== event.pointerId) finish(false); });
  window.addEventListener('blur', () => finish(false));
  window.addEventListener('pagehide', () => finish(false));
  return card => {
    const handle = card.querySelector<HTMLButtonElement>('[data-navigation-drag]');
    card.draggable = false;
    if (!handle) return;
    handle.draggable = false;
    handle.addEventListener('dragstart', event => event.preventDefault());
    handle.addEventListener('keydown', event => {
      if (options.locked() || handle.matches(':disabled') || !['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(event.key)) return;
      event.preventDefault(); finish(false); options.keyboard(card, event.key); handle.focus();
    });
    handle.addEventListener('pointerdown', event => {
      if (!event.isPrimary || event.button !== 0 || options.locked() || handle.matches(':disabled') || drag) return;
      event.preventDefault(); handle.focus({ preventScroll: true });
      drag = { id: event.pointerId, card, handle, x: event.clientX, y: event.clientY, startX: event.clientX, startY: event.clientY, active: false, target: null, after: false };
      handle.setPointerCapture(event.pointerId);
    });
    handle.addEventListener('pointermove', event => {
      if (!drag || drag.id !== event.pointerId) return;
      if (locked()) { finish(false); return; }
      drag.x = event.clientX; drag.y = event.clientY;
      if (!drag.active && Math.hypot(drag.x - drag.startX, drag.y - drag.startY) > 6) {
        drag.active = true; card.classList.add('is-drag-source'); list.classList.add('is-dragging');
        scrollFrame = requestAnimationFrame(scroll);
      }
      if (drag.active) { event.preventDefault(); locate(); }
    });
    handle.addEventListener('pointerup', event => {
      if (drag?.id !== event.pointerId) return;
      drag.x = event.clientX; drag.y = event.clientY; locate(); finish(true);
    });
    handle.addEventListener('pointercancel', event => { if (drag?.id === event.pointerId) finish(false); });
    handle.addEventListener('lostpointercapture', event => { if (drag?.id === event.pointerId) finish(false); });
  };
}
