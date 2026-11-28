import type { ClientSession } from '../shared/newpress-client';

export function required<T extends Element = HTMLElement>(root: ParentNode, selector: string): T {
  const el = root.querySelector<T>(selector); if (!el) throw new Error('互动控件未加载，请刷新页面'); return el;
}
export function field(form: HTMLFormElement, name: string): HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement {
  const el = form.elements.namedItem(name);
  if (!(el instanceof HTMLInputElement || el instanceof HTMLSelectElement || el instanceof HTMLTextAreaElement)) throw new Error('互动表单字段缺失'); return el;
}
export function check(form: HTMLFormElement, name: string): HTMLInputElement {
  const el = field(form, name); if (!(el instanceof HTMLInputElement)) throw new Error('互动表单字段缺失'); return el;
}
export function report(el: HTMLElement, text: string, error = false): void { el.textContent = text; el.dataset.error = String(error); }
export function busy(form: HTMLFormElement, on: boolean): void {
  form.setAttribute('aria-busy', String(on)); form.querySelectorAll<HTMLButtonElement>('button[type=submit], button:not([type])').forEach(b => b.disabled = on);
}
export const errorMessage = (e: unknown) => e instanceof Error ? e.message : '操作结果未确认，请重新核对';
export const ownerKey = (s: Partial<ClientSession>) => `${s.authenticated ? s.id || 0 : 0}:${s.csrf_token || ''}`;
export function sessionClient(reset: () => void) {
  const client = window.NewPressClient?.create(); let owner: string | undefined, epoch = 0, suspended = false;
  client?.subscribe(session => {
    const next = ownerKey(session);
    if (owner !== undefined && next !== owner) { epoch++; reset(); }
    owner = next;
  });
  document.addEventListener('chunnel:session', e => { if (e.detail) client?.setSession(e.detail); });
  window.addEventListener('pagehide', () => { suspended = true; epoch++; client?.cancel(); reset(); });
  window.addEventListener('pageshow', () => { suspended = false; });
  return {
    client, epoch: () => epoch, current: (ticket: number) => ticket === epoch && !suspended,
    async session(): Promise<ClientSession> { if (!client) throw new Error('页面资源未加载，请刷新后重试'); return client.getSession(true); },
    async request(url: string, body?: object) { if (!client) throw new Error('页面资源未加载，请刷新后重试'); return client.request(url, body ? { method: 'POST', body } : {}); },
  };
}
