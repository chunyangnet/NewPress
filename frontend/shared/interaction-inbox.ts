import type { SocialState, SocialAPI } from './interaction-types';
import { parseInbox } from './interaction-data';

export function createInbox({ state, api, render, badges }: { state: SocialState; api: SocialAPI; render(): void; badges(): void }) {
  let sequence = 0, current = 0;
  async function loadInbox(append = false): Promise<void> {
    if (!state.session?.authenticated || !state.session.id || current) return;
    const owner = state.session.id, epoch = state.epoch, request = ++sequence;
    const before = append ? state.inboxCursor : 0; current = request;
    try {
      const response = await api(`/api/v1/social/inbox${before ? `?before=${before}` : ''}`);
      if (epoch !== state.epoch || request !== sequence) return;
      const data = parseInbox(response, owner, before);
      for (const item of data.items) {
        state.inbox.set(item.peer_id, item);
        const thread = state.threads.get(item.peer_id); if (thread) thread.contact = item.contact;
      }
      if (append || !state.inboxCursor) { state.inboxCursor = data.next_cursor; state.inboxMore = data.has_more; }
      state.messageUnread = data.unread; render(); badges();
    } catch (error) { if (epoch === state.epoch && request === sequence) throw error; }
    finally { if (current === request) current = 0; }
  }
  function reset(): void { sequence++; current = 0; }
  return { loadInbox, reset };
}
