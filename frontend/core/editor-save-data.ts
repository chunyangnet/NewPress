import type { RecoveryPending } from './editor-recovery-types';

export type EditorSubmission = Omit<RecoveryPending, 'uncertain'> & { uncertain: boolean };
export interface EditorSaveReceipt {
  saved: true;
  submission_key: string;
  action: string;
  id: number;
  version: number;
  replayed: boolean;
  editor_url: string;
  autosave_url: string;
  autosave_enabled: boolean;
}
export interface EditorAutosaveReceipt {
  available: true;
  post_id: number;
  source_version: number;
  updated_at: string;
  expires_at: string;
  recovery_url: string;
  preview_url: string;
}
export interface EditorFailure {
  saved: boolean | null;
  error: string;
  message: string;
  fields: Record<string, string>;
  published_timezone: string;
}
const record = (value: unknown): Record<string, unknown> | null =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null;

export function editorFailure(value: unknown): EditorFailure {
  const data = record(value), error = record(data?.error), fields = record(data?.fields);
  return {
    saved: typeof data?.saved === 'boolean' ? data.saved : null,
    error: typeof data?.error === 'string' ? data.error : '',
    message: typeof data?.message === 'string' ? data.message.slice(0, 500)
      : typeof error?.message === 'string' ? error.message.slice(0, 500) : '',
    fields: Object.fromEntries(Object.entries(fields || {}).slice(0, 12)
      .filter((entry): entry is [string, string] => typeof entry[1] === 'string')
      .map(([name, message]) => [name, message.slice(0, 300)])),
    published_timezone: typeof data?.published_timezone === 'string' && data.published_timezone.length <= 100 ? data.published_timezone : '',
  };
}

export function editorSaveReceipt(sent: EditorSubmission, value: unknown): EditorSaveReceipt {
  const data = record(value), id = data?.id, version = data?.version;
  const base = `/admin/content/${id}`;
  if (!data || data.saved !== true || data.submission_key !== sent.key || data.action !== sent.action ||
      typeof id !== 'number' || !Number.isSafeInteger(id) || id <= 0 || (sent.id !== 0 && id !== sent.id) ||
      typeof version !== 'number' || !Number.isSafeInteger(version) || version !== sent.version + 1 ||
      typeof data.replayed !== 'boolean' || typeof data.autosave_enabled !== 'boolean' ||
      data.editor_url !== `${base}/edit/?notice=saved` || data.autosave_url !== `${base}/autosave/`) {
    throw new Error('Unconfirmed submission');
  }
  return { saved: true, submission_key: sent.key, action: sent.action, id, version, replayed: data.replayed,
    editor_url: data.editor_url, autosave_url: data.autosave_url, autosave_enabled: data.autosave_enabled };
}

export function editorAutosaveReceipt(value: unknown, autosaveURL: string, pageURL: string, version: number): EditorAutosaveReceipt {
  const data = record(value);
  if (data?.available !== true || typeof data.post_id !== 'number' || !Number.isSafeInteger(data.post_id) || data.post_id < 1 ||
      !Number.isSafeInteger(version) || version < 1 || data.source_version !== version ||
      typeof data.updated_at !== 'string' || !Number.isFinite(Date.parse(data.updated_at)) ||
      typeof data.expires_at !== 'string' || !(Date.parse(data.expires_at) > Date.parse(data.updated_at)) ||
      data.recovery_url !== `/admin/content/${data.post_id}/edit/?recovery=autosave` || typeof data.preview_url !== 'string') {
    throw new Error('Unconfirmed autosave');
  }
  const page = new URL(pageURL), preview = new URL(data.preview_url, page);
  const autosave = new URL(autosaveURL, page);
  if (autosave.origin !== page.origin || autosave.pathname !== `/admin/content/${data.post_id}/autosave/`) throw new Error('Unconfirmed autosave identity');
  const expected = autosave.pathname.replace(/autosave\/$/, 'preview/');
  if (preview.origin !== page.origin || preview.pathname !== expected || preview.username || preview.password || preview.hash || !preview.searchParams.get('token')) {
    throw new Error('Invalid preview address');
  }
  return { available: true, post_id: data.post_id, source_version: version, updated_at: data.updated_at,
    expires_at: data.expires_at, recovery_url: data.recovery_url, preview_url: data.preview_url };
}

export function editorSession(value: unknown) {
  const data = record(value);
  if (data?.authenticated === false) return { authenticated: false } as const;
  if (data?.authenticated !== true || typeof data.user_id !== 'number' || !Number.isSafeInteger(data.user_id) || data.user_id < 0 ||
      typeof data.username !== 'string' || !data.username || typeof data.csrf_token !== 'string' || data.csrf_token.length < 32 || data.csrf_token.length > 256 ||
      typeof data.read_only !== 'boolean' || !Array.isArray(data.role_capabilities) || !data.role_capabilities.every((cap: unknown) => typeof cap === 'string')) return null;
  return { authenticated: true as const, user_id: data.user_id, username: data.username, csrf_token: data.csrf_token,
    read_only: data.read_only, role_capabilities: data.role_capabilities as string[] };
}

export function editorSlug(value: unknown): { slug: string; notice: string } {
  const data = record(value);
  if (!data || typeof data.slug !== 'string' || data.slug.length > 128 || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(data.slug) || typeof data.notice !== 'string') {
    throw new Error('别名结果未确认，请重试。');
  }
  return { slug: data.slug, notice: data.notice.slice(0, 500) };
}
