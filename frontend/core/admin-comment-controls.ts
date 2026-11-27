import type { AdminAPI, FormControl } from './admin-types';
import { bindNativeSubmission } from './admin-native-submit';

export function initializeCommentEditor(commentEditor: HTMLElement, icon: AdminAPI['icon']) {
    const editForm = commentEditor.querySelector<HTMLFormElement>('[data-comment-edit-form]');
    const editFields = [...(editForm?.querySelectorAll<FormControl>('[data-comment-edit-field]') || [])];
    const editSource = editForm?.querySelector<HTMLTextAreaElement>('[data-comment-edit-source]');
    const editCount = editForm?.querySelector('[data-comment-edit-count]');
    const editPreview = editForm?.querySelector('[data-comment-safe-preview]');
    const editSavebar = editForm?.querySelector<HTMLElement>('[data-comment-edit-savebar]') || editForm?.querySelector<HTMLElement>('[data-admin-savebar]') || null;
    const editState = editForm?.querySelector('[data-comment-edit-state]') || editForm?.querySelector('[data-save-state]');
    const editSubmit = editForm?.querySelector<HTMLButtonElement>('[data-comment-edit-submit]') || editForm?.querySelector<HTMLButtonElement>('[data-admin-save]');
    const initialEditValues = new Map(editFields.map((field) => [field.dataset.commentEditField, field.value]));

    const renderPlainCommentPreview = (source: string) => {
      if (!editPreview) return;
      editPreview.replaceChildren();
      const normalized = source.replace(/\r\n?/g, '\n').trim();
      if (!normalized) {
        const empty = document.createElement('p');
        empty.textContent = '暂无内容';
        editPreview.append(empty);
        return;
      }
      normalized.split(/\n\s*\n/).forEach((paragraph) => {
        const node = document.createElement('p');
        paragraph.split('\n').forEach((line, index) => {
          if (index > 0) node.append(document.createElement('br'));
          node.append(document.createTextNode(line));
        });
        editPreview.append(node);
      });
    };
    const syncCommentEdit = () => {
      const changed = editFields.filter((field) => field.value !== initialEditValues.get(field.dataset.commentEditField));
      const dirty = changed.length > 0;
      const source = editSource?.value || '';
      const characters = [...source].length;
      const bytes = new TextEncoder().encode(source).length;
      if (editCount) editCount.textContent = String(characters);
      renderPlainCommentPreview(source);
      editSavebar?.classList.toggle('is-dirty', dirty);
      window.ChunnelAdminUI?.paintSavebar(editSavebar, { dirty, busy: editSubmission?.pending(), readOnly: editSavebar?.dataset.readonly === 'true' });
      editForm?.querySelectorAll<HTMLElement>('[data-comment-change]').forEach((chip) => {
        chip.classList.toggle('is-changed', changed.some((field) => field.dataset.commentEditField === chip.dataset.commentChange));
      });
      if (editState) {
        if (bytes > 12 * 1024) editState.textContent = `正文为 ${(bytes / 1024).toFixed(1)} KiB，超过 12 KiB 上限`;
        else if (editSavebar?.dataset.readonly === 'true') editState.textContent = '只读';
        else if (dirty) editState.textContent = '未保存';
        else editState.textContent = '';
      }
      if (editSubmit) editSubmit.disabled = !!editSubmission?.pending() || editSavebar?.dataset.readonly === 'true' || !dirty || bytes > 12 * 1024 || !editForm?.checkValidity();
    };
    editSavebar?.querySelector('[data-admin-discard]')?.addEventListener('click', () => { if (editSubmission?.pending() || editSavebar.dataset.readonly === 'true') return; editFields.forEach(field => { field.value = initialEditValues.get(field.dataset.commentEditField) ?? ''; }); syncCommentEdit(); });
    editFields.forEach((field) => field.addEventListener('input', syncCommentEdit));
    const editSubmission = editForm ? bindNativeSubmission(editForm, {
      onPending() {
        if (editSubmit) {
          editSubmit.disabled = true;
          editSubmit.replaceChildren(icon('refresh-cw'), document.createTextNode('保存中…'));
        }
        window.ChunnelAdminUI?.paintSavebar(editSavebar, { dirty: true, busy: true });
      },
      onRestore: syncCommentEdit,
    }) : null;
    syncCommentEdit();

    const replyForm = commentEditor.querySelector<HTMLFormElement>('[data-comment-reply-form]');
    const replySource = replyForm?.querySelector<HTMLTextAreaElement>('[data-comment-reply-source]');
    const replyCount = replyForm?.querySelector('[data-comment-reply-count]');
    const replySubmit = replyForm?.querySelector<HTMLButtonElement>('[data-comment-reply-submit]');
    const replyButtonContents = replySubmit ? [...replySubmit.childNodes].map(node => node.cloneNode(true)) : [];
    const syncCommentReply = () => {
      const source = replySource?.value || '';
      const characters = [...source].length;
      const bytes = new TextEncoder().encode(source).length;
      if (replyCount) replyCount.textContent = String(characters);
      if (replySubmit) replySubmit.disabled = !!replySubmission?.pending() || source.trim() === '' || bytes > 12 * 1024 || !replyForm?.checkValidity();
    };
    replySource?.addEventListener('input', syncCommentReply);
    const replySubmission = replyForm ? bindNativeSubmission(replyForm, {
      onPending() {
        if (replySubmit) {
          replySubmit.disabled = true;
          replySubmit.replaceChildren(icon('refresh-cw'), document.createTextNode('发送中…'));
        }
      },
      onRestore() { replySubmit?.replaceChildren(...replyButtonContents.map(node => node.cloneNode(true))); syncCommentReply(); },
    }) : null;
    syncCommentReply();
    const aiReplyButton = replyForm?.querySelector<HTMLButtonElement>('[data-ai-reply-draft]');
    const aiReplyStatus = replyForm?.querySelector<HTMLElement>('[data-ai-reply-status]');
    let generatingAIReply = false;
    aiReplyButton?.addEventListener('click', async () => {
      if (generatingAIReply || !replyForm || !replySource || replySubmission?.pending()) return;
      if (replySource.value.trim() && !window.confirm('用新草稿替换当前回复文本？')) return;
      const startingText = replySource.value;
      const csrfToken = replyForm.querySelector<HTMLInputElement>('input[name="csrf_token"]')?.value || '';
      if (!csrfToken) {
        if (aiReplyStatus) { aiReplyStatus.hidden = false; aiReplyStatus.textContent = '安全令牌不可用，请刷新页面后重试。'; }
        return;
      }
      generatingAIReply = true;
      aiReplyButton.disabled = true;
      aiReplyButton.setAttribute('aria-busy', 'true');
      aiReplyButton.replaceChildren(icon('refresh-cw'), document.createTextNode('正在生成…'));
      if (aiReplyStatus) { aiReplyStatus.hidden = false; aiReplyStatus.textContent = '正在根据文章与评论生成草稿…'; }
      try {
        const endpoint = aiReplyButton.dataset.aiReplyUrl;
        if (!endpoint) throw new Error('AI 草稿入口不可用，请刷新页面后重试。');
        const response = await fetch(endpoint, {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
          body: '{}',
        });
        const payload: unknown = await response.json().catch(() => null);
        if (!response.ok) {
          const detail = payload && typeof payload === 'object' && 'error' in payload ? payload.error : null;
          const message = detail && typeof detail === 'object' && 'message' in detail && typeof detail.message === 'string' ? detail.message : '';
          throw new Error(message || 'AI 草稿生成失败，请稍后重试。');
        }
        if (!payload || typeof payload !== 'object' || !('draft' in payload) || typeof payload.draft !== 'string' || !payload.draft.trim()) throw new Error('模型没有返回可用草稿，请重试。');
        if (replySource.value !== startingText || replySubmission?.pending()) {
          if (aiReplyStatus) aiReplyStatus.textContent = '生成期间回复已更改，已保留你输入的文字。';
          return;
        }
        replySource.value = payload.draft;
        replySource.dispatchEvent(new Event('input', { bubbles: true }));
        replySource.focus();
        if (aiReplyStatus) aiReplyStatus.textContent = '草稿已填入。请检查并编辑后，再手动发送回复。';
      } catch (error) {
        if (aiReplyStatus) aiReplyStatus.textContent = error instanceof Error ? error.message : 'AI 草稿生成失败，请稍后重试。';
      } finally {
        generatingAIReply = false;
        aiReplyButton.disabled = false;
        aiReplyButton.removeAttribute('aria-busy');
        aiReplyButton.replaceChildren(icon('sparkles'), document.createTextNode('生成 AI 草稿'));
      }
    });
    commentEditor.querySelectorAll<HTMLFormElement>('[data-comment-revision-restore]').forEach(form => {
      const button = form.querySelector<HTMLButtonElement>('button[type="submit"]');
      const original = button ? [...button.childNodes].map(node => node.cloneNode(true)) : [];
      bindNativeSubmission(form, {
        onPending() {
          if (button) {
            button.disabled = true;
            button.replaceChildren(icon('refresh-cw'), document.createTextNode('正在恢复快照…'));
          }
        },
        onRestore() { if (button) { button.disabled = false; button.replaceChildren(...original.map(node => node.cloneNode(true))); } },
      });
    });

    document.addEventListener('keydown', (event) => {
      if (event.defaultPrevented || event.isComposing || !(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== 's' || !editForm) return;
      event.preventDefault();
      if (!editSubmit?.disabled) editForm.requestSubmit();
    });
    window.addEventListener('beforeunload', (event) => {
      const dirtyEdit = editSavebar?.classList.contains('is-dirty');
      const dirtyReply = Boolean(replySource?.value.trim());
      if ((!editSubmission?.pending() && dirtyEdit) || (!replySubmission?.pending() && dirtyReply)) {
        event.preventDefault();
        event.returnValue = '';
      }
    });
  }

