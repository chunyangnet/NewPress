import type { ClientSession } from './newpress-client';
import type { SocialAPI } from './interaction-types';
import { parseSocialSession } from './interaction-data';
import { confirmed } from './account-data';

export function createSocialSession(state: { session: ClientSession | null; epoch: number }, api: SocialAPI, apply: (session: ClientSession) => void): (force?: boolean) => Promise<ClientSession> {
  let current: Promise<ClientSession> | null = null, sequence = 0;
  return async function getSession(force = false): Promise<ClientSession> {
    if (current && !force) return current;
    if (state.session && !force) return state.session;
    const request = ++sequence, epoch = state.epoch;
    const pending = api('/api/v1/social/session', { anonymous: true }).then(value => {
      if (request !== sequence || epoch !== state.epoch) return state.session || { authenticated: false, configured: true };
      const session = parseSocialSession(value); apply(session); return session;
    }).finally(() => { if (current === pending) current = null; });
    current = pending; return pending;
  };
}

interface LogoutDependencies {
  state: { epoch: number }; api: SocialAPI;
  getSession(): Promise<ClientSession>;
  apply(session: ClientSession): void;
  clearDrafts(user: number | undefined): void;
  completed(): void;
}
export function createSocialLogout({ state, api, getSession, apply, clearDrafts, completed }: LogoutDependencies): () => Promise<void> {
  let pending: Promise<void> | null = null;
  return function logout(): Promise<void> {
    if (pending) return pending;
    const request = (async () => {
      // Lazy homepage enhancement may still be restoring the session and CSRF token.
      const session = await getSession(), epoch = state.epoch;
      if (session.authenticated) {
        const response = await api('/api/v1/social/logout', { method: 'POST', body: {} });
        if (epoch !== state.epoch) return;
        confirmed(response);
      }
      if (epoch !== state.epoch) return;
      apply({ authenticated: false, configured: true });
      clearDrafts(session.id); completed();
    })().finally(() => { if (pending === request) pending = null; });
    pending = request; return request;
  };
}
