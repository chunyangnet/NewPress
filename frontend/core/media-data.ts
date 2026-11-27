import type { MediaImage, MediaList } from './media-types';

const record = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
const integer = (value: unknown, minimum = 0): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum;

export function mediaURL(item: MediaImage, origin = location.origin): string | null {
  if (/[\\\x00-\x20]/.test(item.public_url)) return null;
  try {
    const url = new URL(item.public_url, origin);
    return url.origin === origin && !url.username && !url.password && url.pathname.startsWith('/media/') && !url.search && !url.hash ? url.pathname : null;
  } catch { return null; }
}

export function decodeMediaImage(value: unknown): MediaImage | null {
  if (!record(value) || value.status !== 'active' || value.kind !== 'image'
    || !integer(value.id, 1) || !integer(value.width, 1) || !integer(value.height, 1) || !integer(value.size_bytes)
    || typeof value.public_url !== 'string' || typeof value.filename !== 'string'
    || typeof value.title !== 'string' || typeof value.alt_text !== 'string' || typeof value.caption !== 'string') return null;
  return { id: value.id, status: value.status, kind: value.kind, public_url: value.public_url,
    filename: value.filename, title: value.title, alt_text: value.alt_text, caption: value.caption,
    size_bytes: value.size_bytes, width: value.width, height: value.height };
}

export function decodeMediaList(value: unknown): MediaList {
  if (!record(value) || !Array.isArray(value.items) || !record(value.pagination)
    || !integer(value.pagination.page, 1) || !integer(value.pagination.pages, 1) || !integer(value.pagination.total)
    || typeof value.can_upload !== 'boolean' || !integer(value.max_upload_bytes)) throw new Error('图片列表响应不完整，请重试');
  return { items: value.items.map(decodeMediaImage).filter((item): item is MediaImage => item !== null),
    pagination: { page: value.pagination.page, pages: value.pagination.pages, total: value.pagination.total },
    can_upload: value.can_upload, max_upload_bytes: value.max_upload_bytes };
}

export function decodeMediaUpload(value: unknown): { item: MediaImage; duplicate: boolean } {
  const item = record(value) ? decodeMediaImage(value.item) : null;
  if (!item || !record(value) || typeof value.duplicate !== 'boolean') throw new Error('未能确认上传结果，文件已保留，请重试');
  return { item, duplicate: value.duplicate };
}

class MediaRequestError extends Error {
  constructor(message: string, readonly recoveryID: number | null = null) { super(message); }
}

export function mediaFailure(error: unknown): { name: string; message: string; recoveryID: number | null } {
  return { name: record(error) && typeof error.name === 'string' ? error.name : '',
    message: record(error) && typeof error.message === 'string' ? error.message : '',
    recoveryID: error instanceof MediaRequestError ? error.recoveryID : null };
}

export async function requestMediaJSON(url: string | URL, options: RequestInit = {}): Promise<unknown> {
  const timedOut = () => mediaFailure(options.signal?.reason).name === 'TimeoutError';
  const timeoutError = () => new Error('媒体请求超时，当前输入和文件已保留，请重试');
  let response: Response;
  const headers = new Headers(options.headers);
  if (!headers.has('Accept')) headers.set('Accept', 'application/json');
  try {
    response = await fetch(url, { credentials: 'same-origin', cache: 'no-store', redirect: 'error', ...options, headers });
  } catch (error) {
    if (timedOut()) throw timeoutError();
    if (mediaFailure(error).name === 'AbortError') throw error;
    throw new Error('网络连接暂时不可用，请重试');
  }
  const payload: unknown = await response.json().catch(() => null);
  if (timedOut()) throw timeoutError();
  if (!response.ok || !payload) {
    const error = record(payload) && record(payload.error) ? payload.error : null;
    const recovery = error?.code === 'media.duplicate_trashed' && record(error.details) ? error.details.media_id : null;
    throw new MediaRequestError(typeof error?.message === 'string' && error.message ? error.message
      : response.status === 401 ? '登录已过期，请重新登录后重试' : '媒体请求未能完成，请重试', integer(recovery, 1) ? recovery : null);
  }
  return payload;
}
