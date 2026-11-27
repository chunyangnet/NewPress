import type {} from '../shared/account-types';
import { create } from '../shared/newpress-client';
import { failure } from '../shared/ledger-data';

/** Each enhanced admin page belongs to the identity and CSRF token that rendered it. */
export function adminAccountRuntime(root: HTMLElement, owner: string, csrf: string, readOnly: boolean, onRetire: () => void) {
  let retired = false;
  const alive = () => !retired && root.isConnected;
  const client = create({ session: () => ({ authenticated: !retired, csrf_token: csrf }), onUnauthorized: () => retire() });
  function retire() { if (retired) return; retired = true; client.cancel(); onRetire(); }
  document.addEventListener('chunnel:session', event => { const s = event.detail; if (s?.authenticated !== true || String(s.id || '') !== owner || s.csrf_token !== csrf) retire(); });
  window.addEventListener('pagehide', retire);
  window.addEventListener('pageshow', event => { if (event.persisted) retire(); });
  async function request(path: string, body?: unknown, timeout = 15000) {
    if (!alive() || body !== undefined && readOnly) throw new DOMException('此页面已停止操作，请重新载入', 'AbortError');
    try { const result = await client.request(path, { method: body === undefined ? 'GET' : 'POST', body, timeout }); if (!alive()) throw new DOMException('页面已失效', 'AbortError'); return result; }
    catch (caught) { const error = failure(caught); if (error.status === 401 || error.status === 403 && !readOnly) retire(); throw error; }
  }
  return { alive, request, retire };
}
