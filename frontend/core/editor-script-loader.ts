// Downloads are shared; each caller still owns its selection, cancellation,
// permission checks and initialization check. Removing a script on timeout does
// not guarantee the browser cancels its fetch or prevents its late execution.
export function editorScriptLoader(url: () => string | undefined, name: string, failure: string, retryKey = 'editor-retry') {
  let pending: Promise<void> | null = null, script: HTMLScriptElement | null = null, attempts = 0;
  const reset = () => { pending = null; script?.remove(); script = null; };
  return {
    reset,
    load(): Promise<void> {
      if (pending) return pending;
      pending = new Promise<void>((resolve, reject) => {
        const address = url();
        if (!address) { reject(new Error(name + '资源未配置。')); return; }
        const node = document.createElement('script'); script = node;
        node.src = address;
        // A distinct URL lets a retry finish independently of a hung request.
        // The optional runtimes must remain safe to execute more than once.
        if (attempts++) {
          const target = new URL(address, location.href);
          target.searchParams.set(retryKey, String(attempts)); node.src = target.href;
        }
        let settled = false;
        const fail = () => {
          if (settled) return;
          settled = true; clearTimeout(timer); node.remove(); reject(new Error(failure));
        };
        const timer = setTimeout(fail, 15000);
        node.addEventListener('error', fail, { once: true });
        node.addEventListener('load', () => { if (!settled) { settled = true; clearTimeout(timer); resolve(); } }, { once: true });
        document.head.append(node);
      }).catch((error: unknown) => { reset(); throw error; });
      return pending;
    },
  };
}
