(() => {
  const body = document.body;
  const header = document.querySelector('[data-header]');
  const toastStack = document.querySelector('[data-toast-stack]') || createToastStack();
  const motionPreference = window.matchMedia('(prefers-reduced-motion: reduce)');
  const reduceMotion = () => motionPreference.matches || body.matches('.theme-force-reduced-motion, .theme-motion-minimal');
  const toastRecords = new Map();
  const overlayRecords = new WeakMap();
  const disclosureAnimations = new WeakMap();
  const keepScroll = (change) => window.ChunnelScroll ? window.ChunnelScroll.keep(change) : change();
  function animate(element, frames, duration = 220) {
    const spatial = frames.some(frame => frame.transform || frame.height);
    const easing = spatial ? getComputedStyle(body).getPropertyValue('--motion-ease').trim() : '';
    return element.animate(frames, {
      duration: reduceMotion() || body.classList.contains('theme-popup-off') ? 0 : duration,
      easing: easing || 'cubic-bezier(.2,.8,.2,1)',
      fill: 'both',
    });
  }

  function createToastStack() {
    const stack = document.createElement('div');
    stack.className = 'toast-stack';
    stack.dataset.toastStack = '';
    stack.setAttribute('aria-live', 'polite');
    stack.setAttribute('aria-atomic', 'false');
    stack.setAttribute('aria-relevant', 'additions');
    document.body.append(stack);
    return stack;
  }

  const toastMeta = {
    success: { title: '操作完成', icon: 'check-circle', duration: 3800 },
    info: { title: '状态更新', icon: 'info', duration: 4200 },
    warning: { title: '请留意', icon: 'triangle-alert', duration: 4800 },
    error: { title: '操作失败', icon: 'circle-x', duration: 5600 },
    loading: { title: '处理中', icon: 'refresh-cw', duration: 0 },
  };

  function showToast(message, tone = 'success', options = {}) {
    if (!message) return;
    const resolvedTone = toastMeta[tone] ? tone : 'success';
    const meta = toastMeta[resolvedTone];
    const requestedDuration = Number.parseInt(options.duration, 10);
    const duration = Number.isFinite(requestedDuration) ? Math.max(0, requestedDuration) : meta.duration;
    const key = JSON.stringify([resolvedTone, options.title || meta.title, message]);
    if (toastRecords.has(key)) {
      toastRecords.get(key).restart();
      return;
    }
    while (toastRecords.size >= 3) toastRecords.values().next().value.dismiss(true);
    const toast = document.createElement('div');
    toast.className = `toast toast-${resolvedTone}`;
    toast.dataset.tone = resolvedTone;
    toast.setAttribute('role', ['warning', 'error'].includes(resolvedTone) ? 'alert' : 'status');
    toast.toggleAttribute('aria-busy', resolvedTone === 'loading');
    toast.style.setProperty('--toast-duration', `${duration}ms`);

    const icon = document.createElement('span');
    icon.className = 'toast-icon';
    icon.append(createIcon(meta.icon));

    const copy = document.createElement('span');
    copy.className = 'toast-copy';
    const title = document.createElement('strong');
    title.textContent = options.title || meta.title;
    const detail = document.createElement('span');
    detail.textContent = message;
    copy.append(title, detail);

    const close = document.createElement('button');
    close.type = 'button';
    close.className = 'toast-close';
    close.setAttribute('aria-label', '关闭提示');
    close.append(createIcon('x'));

    const progress = document.createElement('span');
    progress.className = 'toast-progress';
    progress.setAttribute('aria-hidden', 'true');

    toast.append(icon, copy, close, progress);
    if (duration === 0) toast.classList.add('is-persistent');
    toastStack.append(toast);

    let timer;
    let remaining = duration;
    let startedAt = 0;
    let leaving = false;
    const pauses = new Set(document.hidden ? ['document'] : []);
    const lifetime = duration > 0 ? progress.animate([
      { transform: 'scaleX(1)' }, { transform: 'scaleX(0)' },
    ], { duration, fill: 'forwards' }) : null;
    lifetime?.pause();
    const entrance = animate(toast, [
      { opacity: 0, transform: 'translateY(10px)' },
      { opacity: 1, transform: 'translateY(0)' },
    ]);
    const dismiss = (immediate = false) => {
      if (leaving) return;
      leaving = true;
      clearTimeout(timer);
      lifetime?.cancel();
      entrance.cancel();
      toastRecords.delete(key);
      if (toast.contains(document.activeElement)) {
        const neighbor = toast.nextElementSibling || toast.previousElementSibling;
        (neighbor?.querySelector('button') || options.trigger)?.focus({ preventScroll: true });
      }
      if (immediate || reduceMotion()) {
        toast.remove();
        return;
      }
      const exit = animate(toast, [{ opacity: 1, transform: 'translateY(0)' }, { opacity: 0, transform: 'translateY(8px)' }], 160);
      exit.finished.then(() => { toast.remove(); exit.cancel(); });
    };
    const resume = () => {
      if (leaving || !duration || pauses.size) return;
      startedAt = performance.now();
      lifetime?.play();
      timer = window.setTimeout(dismiss, remaining);
    };
    const pause = (reason, active) => {
      if (active) {
        if (!pauses.size && duration) {
          remaining = Math.max(0, remaining - (performance.now() - startedAt));
          clearTimeout(timer);
          lifetime?.pause();
        }
        pauses.add(reason);
      } else if (pauses.delete(reason) && !pauses.size) resume();
    };
    const restart = () => {
      remaining = duration;
      clearTimeout(timer);
      if (lifetime) lifetime.currentTime = 0;
      resume();
    };
    toastRecords.set(key, { dismiss, restart, pause });
    close.addEventListener('click', () => dismiss());
    toast.addEventListener('pointerenter', () => pause('pointer', true));
    toast.addEventListener('pointerleave', () => pause('pointer', false));
    toast.addEventListener('focusin', () => pause('focus', true));
    toast.addEventListener('focusout', (event) => {
      if (!toast.contains(event.relatedTarget)) pause('focus', false);
    });
    resume();
  }

  document.addEventListener('visibilitychange', () => {
    toastRecords.forEach((record) => record.pause('document', document.hidden));
  });

  function createIcon(name, className = '') {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.classList.add('icon', `icon-${name}`);
    className.split(/\s+/).filter(Boolean).forEach((item) => svg.classList.add(item));
    svg.setAttribute('aria-hidden', 'true');
    svg.setAttribute('focusable', 'false');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', `#icon-${name}`);
    svg.append(use);
    return svg;
  }

  function setIcon(target, name) {
    const use = target?.querySelector('use');
    if (use) use.setAttribute('href', `#icon-${name}`);
  }

  function setOverlay(target, open) {
    if (!(target instanceof HTMLDialogElement)) return;
    const previous = overlayRecords.get(target);
    if ((open && target.open && !previous?.closing) || (!open && (!target.open || previous?.closing))) return;
    const panel = target.querySelector(':scope > section');
    const backdrop = target.querySelector('.modal-backdrop, .drawer-backdrop');
    const lively = body.classList.contains('theme-popup-lively');
    const origin = target.classList.contains('quick-actions') && matchMedia('(max-width: 700px)').matches
      ? { y: lively ? 40 : 20, opacity: 0 } : target.hasAttribute('data-drawer')
      ? { x: lively ? 48 : 24, opacity: 0 } : { y: lively ? 22 : 10, scale: lively ? .95 : .985, opacity: 0 };
    const from = `translate(${origin.x || 0}px,${origin.y || 0}px) scale(${origin.scale || 1})`;
    const backdropOpacity = previous ? getComputedStyle(backdrop).opacity : open ? 0 : 1;
    const duration = Math.max(120, Math.min(400, parseFloat(getComputedStyle(body).getPropertyValue('--popup-duration')) || 260));
    const state = { closing: !open, animations: [] };
    overlayRecords.set(target, state);
    if (open) {
      target.hidden = false;
      if (!target.open) target.showModal();
      body.classList.add('modal-open');
    }
    target.dispatchEvent(new CustomEvent('chunnel:overlay', { bubbles: true, detail: { open } }));
    document.querySelectorAll('[data-modal-open], [data-drawer-open]').forEach(trigger => {
      if ((trigger.dataset.modalOpen || trigger.dataset.drawerOpen) !== target.id) return;
      trigger.setAttribute('aria-controls', target.id); trigger.setAttribute('aria-expanded', String(open)); trigger.setAttribute('aria-haspopup', 'dialog');
    });
    state.animations = [
      window.ChunnelMotion ? window.ChunnelMotion.surface(panel, open ? origin : {}, open ? {} : origin, { duration: open ? duration : duration * .7, lively }) : animate(panel, open
        ? [{ opacity: 0, transform: from }, { opacity: 1, transform: 'none' }]
        : [{ opacity: 1, transform: 'none' }, { opacity: 0, transform: from }], open ? duration : duration * .7),
      animate(backdrop, [{ opacity: backdropOpacity }, { opacity: open ? 1 : 0 }], duration * .7),
    ];
    // The new spring adopts the old velocity before its obsolete handle cancels.
    previous?.animations.forEach((animation) => animation.cancel());
    Promise.allSettled(state.animations.map((animation) => animation.finished)).then(() => {
      if (overlayRecords.get(target) !== state) return;
      if (!open) target.close();
      state.animations.forEach((animation) => animation.cancel());
      overlayRecords.delete(target);
    });
  }

  document.querySelectorAll('dialog[data-modal], dialog[data-drawer]').forEach((dialog) => {
    dialog.addEventListener('cancel', (event) => {
      event.preventDefault();
      setOverlay(dialog, false);
    });
    dialog.addEventListener('close', () => {
      if (dialog.open) return;
      dialog.hidden = true;
      overlayRecords.get(dialog)?.animations.forEach(animation => animation.cancel());
      overlayRecords.delete(dialog);
      document.querySelectorAll('[data-modal-open], [data-drawer-open]').forEach(trigger => {
        if ((trigger.dataset.modalOpen || trigger.dataset.drawerOpen) === dialog.id) trigger.setAttribute('aria-expanded', 'false');
      });
      body.classList.toggle('modal-open', !!document.querySelector('dialog[open]'));
    });
  });

  document.querySelectorAll('[data-tabs]').forEach((tabs, tabGroup) => {
    const buttons = [...tabs.querySelectorAll('[data-tab]')];
    const panels = [...tabs.querySelectorAll('[data-tab-panel]')];
    const activate = (button, focus = false, preserve = false) => {
      const change = () => {
        buttons.forEach((item) => {
          const active = item === button;
          item.classList.toggle('is-active', active);
          item.setAttribute('aria-selected', String(active));
          item.tabIndex = active ? 0 : -1;
        });
        const target = panels.find((panel) => panel.id === button.dataset.tab);
        if (target) target.hidden = false;
        panels.forEach((panel) => { if (panel !== target) panel.hidden = true; });
        if (focus) {
          button.focus({ preventScroll: true });
          const list = button.closest('[role="tablist"]');
          if (list) {
            const offset = button.getBoundingClientRect().left - list.getBoundingClientRect().left;
            if (offset < 0) list.scrollLeft += offset;
            else if (offset + button.offsetWidth > list.clientWidth) list.scrollLeft += offset + button.offsetWidth - list.clientWidth;
          }
        }
      };
      if (preserve) keepScroll(change);
      else change();
    };
    buttons.forEach((button, index) => {
      button.id ||= `tab-${tabGroup}-${index}`;
      button.setAttribute('aria-controls', button.dataset.tab);
      const panel = panels.find((item) => item.id === button.dataset.tab);
      panel?.setAttribute('role', 'tabpanel');
      panel?.setAttribute('aria-labelledby', button.id);
      if (panel) panel.tabIndex = 0;
      button.addEventListener('click', () => activate(button, false, true));
      button.addEventListener('keydown', (event) => {
        const enabled = buttons.filter((item) => !item.disabled);
        const current = enabled.indexOf(button);
        const next = { ArrowRight: (current + 1) % enabled.length, ArrowLeft: (current - 1 + enabled.length) % enabled.length, Home: 0, End: enabled.length - 1 }[event.key];
        if (next === undefined) return;
        event.preventDefault();
        activate(enabled[next], true, true);
      });
    });
    const initial = buttons.find((button) => !button.disabled && button.classList.contains('is-active')) || buttons.find((button) => !button.disabled);
    if (initial) activate(initial);
  });

  document.querySelectorAll('[data-accordion]').forEach((accordion, group) => {
    accordion.querySelectorAll('article > button').forEach((button, index) => {
      const panel = button.nextElementSibling;
      if (!panel) return;
      panel.id ||= `disclosure-${group}-${index}`;
      button.setAttribute('aria-controls', panel.id);
      button.addEventListener('click', () => {
        const expanded = button.getAttribute('aria-expanded') === 'true';
        const previous = disclosureAnimations.get(panel);
        const height = panel.getBoundingClientRect().height;
        const padding = panel.hidden ? '0px' : getComputedStyle(panel).paddingBottom;
        previous?.cancel();
        button.setAttribute('aria-expanded', String(!expanded));
        const marker = button.querySelector('[data-accordion-icon]');
        if (marker) setIcon(marker, expanded ? 'plus' : 'minus');
        panel.hidden = false;
        const animation = animate(panel, [
          { height: `${height}px`, paddingBottom: padding, opacity: expanded ? 1 : 0 },
          { height: expanded ? '0px' : `${panel.scrollHeight}px`, paddingBottom: expanded ? '0px' : getComputedStyle(panel).paddingBottom, opacity: expanded ? 0 : 1 },
        ]);
        disclosureAnimations.set(panel, animation);
        animation.finished.then(() => {
          panel.hidden = expanded;
          animation.cancel();
          disclosureAnimations.delete(panel);
        }).catch(() => {});
      });
    });
  });

  document.querySelectorAll('[data-character-count]').forEach((input) => {
    const counter = document.getElementById(input.getAttribute('aria-describedby'));
    if (!counter) return;
    const update = () => {
      const maximum = Number(input.dataset.emojiMax || input.maxLength);
      counter.textContent = `${[...input.value].length} / ${maximum}`;
      counter.classList.toggle('is-limit', [...input.value].length >= maximum);
    };
    input.addEventListener('input', update);
    update();
  });
  document.querySelectorAll('.switch-stack input').forEach((input) => input.setAttribute('role', 'switch'));

  const commentForm = document.querySelector('[data-comment-form]');
  const commentContent = commentForm?.querySelector('[data-comment-content]');
  const commentCounter = commentForm?.querySelector('[data-comment-count]');
  const commentParent = commentForm?.querySelector('[data-comment-parent]');
  const commentReplyContext = commentForm?.querySelector('[data-comment-reply-context]');
  const commentReplyLabel = commentForm?.querySelector('[data-comment-reply-label]');
  const updateCommentCounter = () => {
    if (!commentContent || !commentCounter) return;
    const count = [...commentContent.value].length;
    const maximum = Number.parseInt(commentForm?.dataset.commentMax || commentContent.maxLength || '0', 10);
    commentCounter.textContent = String(count);
    commentCounter.closest('span')?.classList.toggle('is-limit', maximum > 0 && count >= maximum);
  };
  updateCommentCounter();
  commentContent?.addEventListener('input', updateCommentCounter);
  const nativeCommentButton = commentForm?.querySelector('button[type="submit"]');
  const nativeCommentLabel = nativeCommentButton?.innerHTML;
  window.addEventListener('submit', event => {
    if (event.target !== commentForm || event.defaultPrevented || commentForm.hasAttribute('data-async-ready')) return;
    if (commentForm.dataset.submitOnce === 'true') { event.preventDefault(); return; }
    commentForm.dataset.submitOnce = 'true';
    if (nativeCommentButton) { nativeCommentButton.setAttribute('aria-disabled', 'true'); nativeCommentButton.dataset.submitState = 'pending'; nativeCommentButton.textContent = '正在提交…'; }
  });
  window.addEventListener('pageshow', () => {
    if (!commentForm || commentForm.hasAttribute('data-async-ready')) return;
    delete commentForm.dataset.submitOnce;
    if (nativeCommentButton) { nativeCommentButton.removeAttribute('aria-disabled'); delete nativeCommentButton.dataset.submitState; nativeCommentButton.innerHTML = nativeCommentLabel; }
  });

  document.addEventListener('click', (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (!target) return;

    const toastTrigger = target.closest('[data-toast]');
    if (toastTrigger) {
      showToast(toastTrigger.dataset.toast, toastTrigger.dataset.toastTone, {
        title: toastTrigger.dataset.toastTitle,
        duration: toastTrigger.dataset.toastDuration,
        trigger: toastTrigger,
      });
    }

    const reply = target.closest('[data-comment-reply]');
    if (reply && commentForm && commentParent && commentReplyContext && commentReplyLabel) {
      commentParent.value = reply.dataset.parentId || '0';
      commentReplyLabel.textContent = `正在回复 ${reply.dataset.parentName || `评论 #${commentParent.value}`}`;
      commentReplyContext.hidden = false;
      commentForm.scrollIntoView({ behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth', block: 'center' });
      if (commentForm.dataset.commentLocked === 'true') document.querySelector('[data-comment-login-gate] [data-account-login]')?.click();
      else commentContent?.focus({ preventScroll: true });
    }

    if (target.closest('[data-comment-reply-cancel]') && commentParent && commentReplyContext && commentReplyLabel) {
      commentParent.value = '0';
      commentReplyLabel.textContent = '';
      commentReplyContext.hidden = true;
      commentContent?.focus({ preventScroll: true });
    }

    const modalOpen = target.closest('[data-modal-open]');
    if (modalOpen) setOverlay(document.getElementById(modalOpen.dataset.modalOpen), true);
    const modalClose = target.closest('[data-modal-close]');
    if (modalClose) setOverlay(modalClose.closest('[data-modal]'), false);

    const drawerOpen = target.closest('[data-drawer-open]');
    if (drawerOpen) setOverlay(document.getElementById(drawerOpen.dataset.drawerOpen), true);
    const drawerClose = target.closest('[data-drawer-close]');
    if (drawerClose) setOverlay(drawerClose.closest('[data-drawer]'), false);

    const alertClose = target.closest('.ui-alert > button');
    if (alertClose) {
      const alert = alertClose.closest('.ui-alert');
      const next = alert.nextElementSibling || alert.previousElementSibling;
      next?.querySelector('button')?.focus({ preventScroll: true });
      animate(alert, [{ opacity: 1 }, { opacity: 0, transform: 'translateX(6px)' }], 140).finished.then(() => alert.remove());
    }

    const filterButton = target.closest('.filter-tabs button');
    if (filterButton) {
      keepScroll(() => {
        filterButton.parentElement?.querySelectorAll('button').forEach((button) => {
          button.classList.toggle('is-active', button === filterButton);
          button.setAttribute('aria-pressed', String(button === filterButton));
        });
        const drawer = filterButton.closest('[data-drawer]');
        if (drawer && filterButton.dataset.noticeFilter) {
          drawer.querySelectorAll('[data-notice-kind]').forEach((notice) => {
            notice.hidden = filterButton.dataset.noticeFilter !== 'all' && notice.dataset.noticeKind !== filterButton.dataset.noticeFilter;
          });
        }
      });
    }

    const viewToggle = target.closest('[data-view-toggle]');
    if (viewToggle) {
      const results = document.querySelector('[data-results-view]');
      if (results) keepScroll(() => {
        const top = window.scrollY || 0;
        history.replaceState({ ...history.state, chunnelScroll: top }, '');
        const view = viewToggle.dataset.viewToggle || 'grid';
        results.dataset.resultsView = view;
        const url = new URL(location.href);
        if (view === 'grid') url.searchParams.delete('view'); else url.searchParams.set('view', view);
        if (url.href !== location.href) history.pushState({ ...history.state, chunnelScroll: top }, '', url);
        viewToggle.parentElement?.querySelectorAll('[data-view-toggle]').forEach((button) => {
          button.classList.toggle('is-active', button === viewToggle);
          button.setAttribute('aria-pressed', String(button === viewToggle));
        });
      });
    }

  });

  const progress = document.querySelector('[data-reader-settings]') ? null : document.querySelector('[data-reading-progress]');
  const directory = [...document.querySelectorAll('.component-nav nav a[href^="#"]')].map((link) => ({ link, section: document.getElementById(link.hash.slice(1)) })).filter((item) => item.section);
  if (progress || directory.length) {
    let frame = 0;
    const update = () => {
      frame = 0;
      if (progress) {
        const height = document.documentElement.scrollHeight - window.innerHeight;
        const value = height > 0 ? Math.min(100, (window.scrollY / height) * 100) : 0;
        progress.style.transform = `scaleX(${value / 100})`;
      }
      if (directory.length) {
        const top = (header?.getBoundingClientRect().height || 0) + 48;
        const active = directory.filter((item) => item.section.getBoundingClientRect().top <= top).at(-1) || directory[0];
        directory.forEach(({ link }) => {
          link.classList.toggle('is-active', link === active.link);
          if (link === active.link) link.setAttribute('aria-current', 'location');
          else link.removeAttribute('aria-current');
        });
      }
    };
    const schedule = () => { if (!frame) frame = requestAnimationFrame(update); };
    update();
    window.addEventListener('scroll', schedule, { passive: true });
    window.addEventListener('resize', schedule);
  }
  window.Chunnel = { showToast, createIcon, setOverlay };
})();
