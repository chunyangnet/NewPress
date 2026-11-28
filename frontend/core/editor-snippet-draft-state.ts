import { record } from './editor-tool-data';

export type DraftRequest = {post_id: number; version: number; digest: string; request_key: string};

export function draftRequest(value: unknown, id: number): value is DraftRequest {
  return record(value) && value.post_id === id && Number.isSafeInteger(value.version) && Number(value.version) > 0 &&
    typeof value.digest === 'string' && /^[a-f0-9]{64}$/.test(value.digest) &&
    typeof value.request_key === 'string' && /^[a-f0-9]{64}$/.test(value.request_key) && Object.keys(value).length === 4;
}

export function snippetDraftStorageKey(user: number, login: string, id: number): string {
  return `newpress:snippet-draft:${user > 0 ? 'user:' + user : 'legacy:' + login}:${id}`;
}

export function savedSnippetDraft(key: string, id: number): DraftRequest | null {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(key) || 'null');
    return draftRequest(value, id) ? value : null;
  } catch { return null; }
}
