import type { CommunityState, ExperienceDependencies } from './experience-types';
import { experienceError, parseCommunityDraft, parseCommunityReceipt } from './experience-data';

interface SubmissionDependencies extends Pick<ExperienceDependencies, 'api' | 'getSession' | 'signIn' | 'run' | 'storage' | 'showToast' | 'createIcon'> {
  state: CommunityState;
  input: HTMLTextAreaElement;
  submit: HTMLButtonElement;
  errorBox: HTMLElement;
  reveal(): void;
  update(save?: boolean): void;
  key(id?: number | null): string;
}

export function createCommunitySubmission(deps: SubmissionDependencies) {
  const { state, input, submit, errorBox, reveal, update, key, api, getSession, signIn, run, storage, showToast, createIcon } = deps;
  let attempt = 0;
  return function submitDiscussion(status: 'publish' | 'draft'): Promise<unknown> | undefined {
    if (state.busy) return;
    const request = ++attempt;
    const sourceOwner = state.owner, sourceGeneration = state.generation;
    const sourceContent = input.value.trim(), sourcePending = state.pending;
    return run(() => signIn(async () => {
      if (request !== attempt) return;
      const session = await getSession(true);
      if (state.busy || request !== attempt) return;
      if (!session.authenticated || sourceOwner !== 0 && (sourceOwner !== session.id || sourceGeneration !== state.generation)) {
        showToast('登录状态已变化，请重新打开编辑器后提交', 'warning'); return;
      }
      if (sourceOwner === state.owner && input.value.trim() !== sourceContent) {
        errorBox.textContent = '输入内容已更新，请确认后再次提交。'; errorBox.hidden = false; update(); return;
      }
      reveal();
      if (sourceOwner === 0 && input.value.trim() && input.value.trim() !== sourceContent) {
        errorBox.textContent = '当前账号已有未提交的草稿。登录前的输入也已保留，请先完成当前草稿。'; errorBox.hidden = false; return;
      }
      if (state.owner !== session.id) {
        showToast('登录状态已变化，请重新打开编辑器后提交', 'warning'); return;
      }
      input.value = sourceContent; state.pending = sourcePending;
      const content = input.value.trim();
      if ([...content].length > 500 || !content && status === 'publish') { update(); return; }
      if (!session.can_publish) { update(); errorBox.textContent = '当前账号尚未开通话题发布权限。你可以在文章下参与评论，或使用私信交流；输入内容已保留。'; errorBox.hidden = false; return; }
      if (!content) { location.assign('/admin/content/new/'); return; }
      state.busy = true; errorBox.hidden = true; input.readOnly = true;
      const submittedGeneration = state.generation;
      const signature = JSON.stringify([state.owner, status, content]);
      if (!state.pending || state.pending.signature !== signature) state.pending = {
        signature, nonce: crypto.randomUUID(), guestContent: sourceOwner === 0 ? content : state.pending?.guestContent === content ? content : null,
      };
      const submission = state.pending;
      update(); submit.replaceChildren(createIcon('refresh-cw'), document.createTextNode(status === 'draft' ? '保存草稿…' : '正在发布…'));
      const escapes: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
      const escaped = content.replace(/[&<>"']/g, char => escapes[char] || char);
      const current = () => state.owner === session.id && state.generation === submittedGeneration;
      try {
        const response = await api('/api/v1/admin/contents', { method: 'POST', headers: { 'Idempotency-Key': submission.nonce }, body: {
          type: 'post', status, title: [...(content.split('\n')[0] || '')].slice(0, 70).join(''), slug: `discussion-${submission.nonce}`,
          excerpt: [...content].slice(0, 180).join(''), content_source: `<p>${escaped.replace(/\n/g, '<br>')}</p>`, comment_status: 'open',
        } });
        const post = parseCommunityReceipt(response, status, location.href);
        if (!current()) return;
        if (submission.guestContent && parseCommunityDraft(storage.get(key(0), null)).content.trim() === submission.guestContent) storage.remove(key(0));
        input.value = ''; state.pending = null; storage.remove(key()); update();
        if (status === 'draft') { location.assign(`/admin/content/${post.id}/edit/`); return; }
        storage.set('chunnel.community.published', { id: post.id, owner: session.id, at: Date.now() });
        const url = new URL(location.href);
        for (const parameter of ['page', 'discussion', 'discussion_sort']) url.searchParams.delete(parameter);
        url.hash = `discussion-${post.id}`;
        if (url.pathname + url.search === location.pathname + location.search) {
          history.replaceState({ ...history.state }, '', url); location.reload();
        } else location.assign(url.href);
      } catch (failure) {
        if (current()) { errorBox.textContent = experienceError(failure).message; errorBox.hidden = false; }
      } finally {
        if (current()) { state.busy = false; input.readOnly = false; update(); submit.replaceChildren(createIcon('send'), document.createTextNode('发布讨论')); }
      }
    }));
  };
}
