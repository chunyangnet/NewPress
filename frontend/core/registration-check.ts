interface RegistrationCheck { key: string; title: string; ok: boolean; message: string }
export function initializeRegistrationCheck(): void {
  const dialog = document.querySelector<HTMLDialogElement>('[data-registration-check-dialog]');
  if (!dialog) return;
  const list = dialog.querySelector<HTMLElement>('[data-registration-check-list]')!;
  const summary = dialog.querySelector<HTMLElement>('[data-registration-check-summary]')!;
  let generation = 0;
  let request: AbortController | undefined;
  let dirty = false;
  const buttons = [...document.querySelectorAll<HTMLButtonElement>('[data-registration-check]')];
  function inline(items: RegistrationCheck[]) {
    for (const item of items) {
      const field = item.key.startsWith('provider_') ? document.querySelector(`[data-registration-provider="${CSS.escape(item.key.slice(9))}"]`) : document.querySelector(`[data-setting-field="${CSS.escape(item.key)}"] .admin-setting-control`);
      if (!field) continue;
      let small = field.querySelector<HTMLElement>('.registration-field-status');
      if (!small) { small = document.createElement('small'); small.className = 'registration-field-status'; field.append(small); }
      small.dataset.ok = String(item.ok);
      small.textContent = `${item.ok ? '✓' : '✕'} ${item.message}`;
      if (dirty) { small.textContent = '配置尚未保存；以下检查结果仅针对已保存配置。'; delete small.dataset.ok; }
    }
  }
  async function load(signal?: AbortSignal): Promise<RegistrationCheck[]> {
    const controller = new AbortController();
    const abort = () => controller.abort();
    if (signal?.aborted) abort();
    signal?.addEventListener('abort', abort, { once: true });
    const timer = setTimeout(abort, 15000);
    try {
    const response = await fetch('/api/v1/admin/access/check', { credentials: 'same-origin', cache: 'no-store', signal: controller.signal });
    if (!response.ok) throw new Error('暂时无法检查，请确认登录状态后重试。');
    const data = await response.json();
    if (!Array.isArray(data.items)) throw new Error('检查结果格式不正确，请重试。');
    return data.items;
    } finally { clearTimeout(timer); signal?.removeEventListener('abort', abort); }
  }
  async function run() {
    const current = ++generation;
    request?.abort(); request = new AbortController();
    if (!dialog!.open) dialog!.showModal();
    buttons.forEach(button => { button.disabled = true; });
    list.replaceChildren(); summary.textContent = '正在读取已保存配置…';
    try {
      const items = await load(request.signal);
      if (current !== generation) return;
      inline(items);
      const rows = items.map(item => {
        const row = document.createElement('div'); row.className = 'registration-check-row'; row.dataset.state = 'waiting';
        const icon = document.createElement('span'); icon.className = 'registration-check-icon'; icon.setAttribute('aria-hidden', 'true'); icon.textContent = '·';
        const copy = document.createElement('div'), title = document.createElement('strong'), message = document.createElement('small');
        title.textContent = item.title; message.textContent = '等待检查'; copy.append(title, message); row.append(icon, copy); list.append(row);
        return { row, icon, message };
      });
      for (let i = 0; i < items.length; i++) {
        if (current !== generation) return;
        const { row, icon, message } = rows[i]!; const item = items[i]!;
        row.dataset.state = 'checking'; icon.textContent = ''; message.textContent = '正在核对配置…';
        if (!matchMedia('(prefers-reduced-motion: reduce)').matches) await new Promise(resolve => setTimeout(resolve, 160));
        if (current !== generation) return;
        row.dataset.state = item.ok ? 'passed' : 'failed'; icon.textContent = item.ok ? '✓' : '✕'; message.textContent = item.message;
      }
      const failed = items.filter(item => !item.ok).length;
      summary.textContent = failed ? `检查完成，${failed} 项需要处理。关闭弹窗后可在对应配置下查看说明。` : '检查完成，当前配置没有发现阻塞项。关闭的可选功能按正常状态显示。';
    } catch (error) { if (current === generation) summary.textContent = error instanceof Error ? error.message : '检查失败，请重试。'; }
    finally { if (current === generation) buttons.forEach(button => { button.disabled = false; }); }
  }
  buttons.forEach(button => button.addEventListener('click', () => { void run(); }));
  dialog.querySelector('[data-registration-check-close]')?.addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => { generation++; request?.abort(); buttons.forEach(button => { button.disabled = false; }); });
  document.querySelector('[data-settings-form]')?.addEventListener('change', () => {
    dirty = true;
    document.querySelectorAll<HTMLElement>('.registration-field-status').forEach(node => { node.textContent = '配置已修改，保存后点击检查可查看实际状态。'; delete node.dataset.ok; });
  });
  void load().then(inline).catch(() => {});
}
