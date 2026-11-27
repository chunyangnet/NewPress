export type PreviewResponse = { ok: true; html: string; css: string } | { ok: false; status: number; error: string; field: string };

export function previewResponse(value: unknown, status: number): PreviewResponse {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('预览响应格式不正确');
  const result = value as Record<string, unknown>;
  if (status >= 200 && status < 300) {
    if (typeof result.html !== 'string' || typeof result.css !== 'string') throw new Error('预览响应缺少页面或样式');
    return { ok: true, html: result.html, css: result.css };
  }
  const fields = result.fields;
  return { ok: false, status, error: typeof result.error === 'string' ? result.error : '',
    field: fields && typeof fields === 'object' && !Array.isArray(fields) ? Object.keys(fields)[0] || '' : '' };
}

export async function fetchPreview(url: string, body: URLSearchParams | undefined, signal: AbortSignal): Promise<PreviewResponse> {
  const controller = new AbortController(), abort = () => controller.abort();
  if (signal.aborted) abort(); else signal.addEventListener('abort', abort, { once: true });
  // Each timeout owns its controller; an old request cannot abort a newer one.
  const timeout = setTimeout(abort, 15000);
  try {
    const response = await fetch(url, { method: body ? 'POST' : 'GET', credentials: 'same-origin', cache: 'no-store', redirect: 'error',
      headers: { Accept: 'application/json' }, body, signal: controller.signal });
    const result: unknown = await response.json();
    if (controller.signal.aborted) throw new DOMException('预览已取消', 'AbortError');
    return previewResponse(result, response.status);
  } finally { clearTimeout(timeout); signal.removeEventListener('abort', abort); }
}

export function syncPreviewColorMode(source: Document, preview: Document | null | undefined): void {
  if (!preview) return;
  preview.documentElement.dataset.theme = source.documentElement.dataset.theme || 'light';
  preview.documentElement.dataset.colorMode = source.documentElement.dataset.colorMode || 'system';
}
