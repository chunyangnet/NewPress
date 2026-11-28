import { create } from './newpress-client';
import type { ClientSession } from './newpress-client';

export function flashRuntime(onIdentity: (session: ClientSession) => void, onSuspend: () => void, onResume: () => void) {
  const client = create();
  let generation = 0, suspended = false, identity: string | undefined;
  client.subscribe(session => {
    if (typeof session.authenticated !== 'boolean') return;
    const next = `${session.authenticated ? session.id || 0 : 0}:${session.csrf_token || ''}`;
    if (identity !== undefined && identity !== next) generation++;
    identity = next; onIdentity(session as ClientSession);
  });
  document.addEventListener('chunnel:session', event => {
    const session = event.detail;
    if (session && typeof session.authenticated === 'boolean') client.setSession(session);
  });
  window.addEventListener('pagehide', () => { suspended = true; generation++; client.cancel(); onSuspend(); });
  window.addEventListener('pageshow', async event => {
    if (!event.persisted) return;
    try { await client.getSession(true); suspended = false; onResume(); } catch { /* No writes while identity is unavailable. */ }
  });
  return { client, epoch: () => generation, current: (ticket: number) => !suspended && ticket === generation, session: () => client.getSession(true) };
}
