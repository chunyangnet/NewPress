import { record, text } from './collection-data';
import type {} from './account-types';
export class CollectionError extends Error { constructor(message: string, public status = 0) { super(message); } }
export function failure(value: unknown): CollectionError {
  if (value instanceof CollectionError) return value;
  const error = new CollectionError(value instanceof Error ? value.message : '网络暂不可用，当前输入已保留。');
  if (value instanceof Error) error.name = value.name;
  return error;
}
export function required<T extends Element = HTMLElement>(root: ParentNode, selector: string): T {
  const element = root.querySelector<T>(selector); if (!element) throw new Error('页面缺少控件：' + selector); return element;
}
export function field(form: HTMLFormElement, name: string): HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement | RadioNodeList {
  const element = form.elements.namedItem(name);
  if (!(element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement || element instanceof HTMLSelectElement || element instanceof RadioNodeList)) throw new Error('表单缺少字段：' + name);
  return element;
}
export const checked = (form: HTMLFormElement, name: string): boolean => required<HTMLInputElement>(form, `input[name="${name}"]`).checked;
export function pageRequests(owner: number, onLost: () => void) {
  let lost = false;
  const requests = new Set<AbortController>();
  const cancel = () => { if (lost) return; lost = true; requests.forEach(controller => controller.abort()); onLost(); };
  const assert = () => { if (lost) throw new CollectionError('登录状态已变化，请重新打开页面。'); };
  document.addEventListener('chunnel:session', event => { if (owner && (event.detail?.authenticated !== true || event.detail.id !== owner)) cancel(); });
  window.addEventListener('pagehide', cancel);
  window.addEventListener('pageshow', event => { if (event.persisted) cancel(); });
  async function request(url: string, options: RequestInit = {}, timeout = 15000): Promise<unknown> {
    assert();
    const controller = new AbortController(), abort = () => controller.abort();
    requests.add(controller);
    options.signal?.addEventListener('abort', abort, {once:true}); if (options.signal?.aborted) abort();
    let timedOut = false;
    const timer = setTimeout(() => { timedOut = true; abort(); }, timeout);
    try {
      const response = await fetch(url, {credentials:'same-origin',cache:'no-store',redirect:'error',...options,headers:{Accept:'application/json',...options.headers},signal:controller.signal});
      let value: unknown;
      if (response.status !== 204) value = await response.json().catch(() => null);
      assert();
      if (controller.signal.aborted) throw new DOMException('请求已取消', 'AbortError');
      if (!response.ok) {
        let message = '操作未完成，输入已保留，请重试。';
        try { const data = record(record(value).error); message = text(data.message) || message; } catch { /* Non-JSON failures do not acknowledge writes. */ }
        throw new CollectionError(message, response.status);
      }
      if (response.status === 204) return null;
      return record(value);
    } catch (problem) {
      assert();
      if (timedOut) { const error = new CollectionError('响应超时，输入已保留，请核对后重试。'); error.name = 'TimeoutError'; throw error; }
      throw problem;
    } finally { clearTimeout(timer); requests.delete(controller); options.signal?.removeEventListener('abort', abort); }
  }
  return {request,assert,cancel,get lost(){return lost;}};
}
