import { responseError } from './editor-tool-data';

export async function editorToolJSON(url: string): Promise<unknown> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(new DOMException('Request timed out', 'TimeoutError')), 15000);
  try {
    const response = await fetch(url, { cache: 'no-store', credentials: 'same-origin', redirect: 'error',
      headers: { Accept: 'application/json' }, signal: controller.signal });
    const payload: unknown = await response.json();
    if (controller.signal.aborted) throw new Error('请求超时，请重试。');
    if (!response.ok) throw new Error(responseError(payload, '暂时无法读取，请重试。'));
    return payload;
  } finally { clearTimeout(timeout); }
}
