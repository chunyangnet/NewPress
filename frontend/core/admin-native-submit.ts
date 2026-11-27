type SubmissionState = { kind: 'idle' } | { kind: 'checking'; event: SubmitEvent } | { kind: 'pending' } | { kind: 'disposed' };

// Confirmations and verification may cancel later in the same event dispatch.
// Defer visual locking until native submission has serialized its submitter.
export function bindNativeSubmission(form: HTMLFormElement, callbacks: { onPending(): void; onRestore(): void }) {
  let state: SubmissionState = { kind: 'idle' };
  let timer: ReturnType<typeof setTimeout> | undefined;
  const pending = () => state.kind === 'pending' || (state.kind === 'checking' && !state.event.defaultPrevented);
  const submit = (event: SubmitEvent) => {
    if (event.defaultPrevented || state.kind === 'disposed') return;
    if (pending()) { event.preventDefault(); return; }
    state = { kind: 'checking', event };
    queueMicrotask(() => {
      if (state.kind !== 'checking' || state.event !== event) return;
      if (event.defaultPrevented) { state = { kind: 'idle' }; return; }
      state = { kind: 'pending' };
      form.setAttribute('aria-busy', 'true');
      timer = setTimeout(() => { if (state.kind === 'pending') callbacks.onPending(); }, 0);
    });
  };
  const restore = (event: PageTransitionEvent) => {
    if (!event.persisted || state.kind === 'disposed') return;
    clearTimeout(timer);
    state = { kind: 'idle' };
    form.setAttribute('aria-busy', 'false');
    callbacks.onRestore();
  };
  form.addEventListener('submit', submit);
  window.addEventListener('pageshow', restore);
  return {
    pending,
    dispose() {
      state = { kind: 'disposed' }; clearTimeout(timer);
      form.removeEventListener('submit', submit);
      window.removeEventListener('pageshow', restore);
    },
  };
}
