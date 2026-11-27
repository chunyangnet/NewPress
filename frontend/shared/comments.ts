import type { ExperienceDependencies, CommentPayload, CommentRequest } from './experience-types';
import type { ClientSession } from './newpress-client';
import { input as field } from './account-dom';
import { parseCommentSubmission, parseCommentReceipt, parseCommentDraft, parseCommentToken, experienceError } from './experience-data';

export function mountComments(deps: ExperienceDependencies): void {
  const { api, getSession, signIn, storage, showToast, createIcon, emoji, dom: { $, optional, node } } = deps;
  const commentForm = optional<HTMLFormElement>('[data-comment-form]');
  if (commentForm) {
    let commentBusy = false;
    let commentOwner: number | null = null;
    let commentGeneration = 0;
    const changedIdentity = () => Object.assign(new Error('评论账号已变化，输入和核对记录已保留，请恢复原提交身份后再确认。'), { code: 'comments.identity_changed' });
    const gate = optional('[data-comment-login-gate]');
    const setCommentGate = (authenticated: boolean) => {
      if (!gate) return;
      const wasLocked = commentForm.dataset.commentLocked === 'true';
      const locked = commentForm.dataset.commentLoginRequired === 'true' && !authenticated;
      const moveFocus = locked && commentForm.contains(document.activeElement);
      commentForm.dataset.commentLocked = String(locked);
      commentForm.inert = locked;
      if (locked) commentForm.setAttribute('aria-hidden', 'true'); else commentForm.removeAttribute('aria-hidden');
      gate.hidden = !locked;
      commentForm.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLButtonElement>('input:not([type="hidden"]), textarea, button').forEach(control => { control.disabled = locked; });
      if (commentBusy) $<HTMLButtonElement>('button[type="submit"]', commentForm).disabled = true;
      if (moveFocus) gate.querySelector<HTMLElement>('[data-account-login]')?.focus({ preventScroll: true });
      if (wasLocked && !locked) {
        const login = document.querySelector<HTMLDialogElement>('#account-login-modal');
        if (login?.open) login.addEventListener('close', () => $<HTMLTextAreaElement>('[data-comment-content]', commentForm)?.focus({ preventScroll: true }), { once: true });
      }
    };
    setCommentGate(commentForm.dataset.commentLocked !== 'true');
	const syncCommentIdentity = (session: ClientSession) => {
	  const authenticated = !!session?.authenticated && Number(session.id) > 0;
      const previousOwner = commentOwner;
      commentOwner = authenticated ? Number(session.id) : 0;
      if (previousOwner !== null && previousOwner !== commentOwner) { commentGeneration++; window.ChunnelVerification?.cancel(commentForm); }
      setCommentGate(authenticated);
	  $('[data-comment-identity]', commentForm).hidden = authenticated;
	  $('[data-comment-account]', commentForm).hidden = !authenticated;
	  $('[data-comment-account-name]', commentForm).textContent = session?.name || '';
	  for (const key of ['author_name', 'email']) { field(commentForm, key).required = !authenticated; if (authenticated) field(commentForm, key).value = ''; }
	};
	document.addEventListener('chunnel:session', event => syncCommentIdentity(event.detail));
	getSession().then(session => { if (commentOwner === null) syncCommentIdentity(session); }).catch(() => {});
    const input = $<HTMLTextAreaElement>('[data-comment-content]', commentForm);
    const parent = $<HTMLInputElement>('[data-comment-parent]', commentForm);
    const submit = $<HTMLButtonElement>('button[type="submit"]', commentForm);
    const key = `chunnel.comment.${location.pathname}`;
    const feedback = node('p', 'interaction-error'); feedback.dataset.commentError = ''; feedback.setAttribute('role', 'alert'); feedback.hidden = true;
    $('.comment-form-panel', commentForm).append(feedback);
    const note = node('div', 'comment-draft-note');
    const label = node('span', '', '输入会暂存在此标签页');
    const discard = node('button', '', '清除草稿'); discard.type = 'button';
    note.append(label, discard); $('.comment-body-field', commentForm).insertAdjacentElement('afterend', note);
    const notice = new URLSearchParams(location.search).get('comment');
    if (['approved', 'pending'].includes(notice || '')) storage.remove(key);
    const draftsEnabled = commentForm.dataset.commentDrafts !== 'false';
    if (!draftsEnabled) { storage.remove(key); note.hidden = true; }
    const draft = draftsEnabled ? parseCommentDraft(storage.get(key, null)) : null;
    if (!input.value && draft?.content && Date.now() - Number(draft.at) < 86400000) {
      input.value = String(draft.content).slice(0, 20000);
      if (Number(draft.parent) > 0 && document.getElementById(`comment-${Number(draft.parent)}`)) {
        parent.value = String(Number(draft.parent));
        $('[data-comment-reply-context]').hidden = false;
        $('[data-comment-reply-label]').textContent = draft.reply || `正在回复评论 #${parent.value}`;
      }
      input.dispatchEvent(new Event('input', { bubbles: true })); label.textContent = '已恢复上次未提交的草稿';
    }
    const save = () => {
      if (!draftsEnabled) return;
      if (input.value) storage.set(key, { content: input.value, parent: parent.value, reply: $('[data-comment-reply-label]').textContent, at: Date.now() });
      else storage.remove(key);
      discard.hidden = !input.value;
    };
    input.addEventListener('input', () => { feedback.hidden = true; save(); });
    commentForm.addEventListener('click', () => queueMicrotask(save));
    window.addEventListener('pagehide', save);
    discard.addEventListener('click', () => {
      input.value = ''; optional('[data-comment-reply-cancel]')?.click(); input.dispatchEvent(new Event('input', { bubbles: true })); input.focus(); label.textContent = '草稿已清除';
    });
    save();
    const commentAPI = commentForm.dataset.commentApi;
    if (commentAPI) {
      commentForm.dataset.asyncReady = '';
      let retryAt = 0;
      const retrySupported = !!optional('[data-comment-retry-supported]', commentForm);
      const pendingKey = `${key}.submission`;
      let pending = draftsEnabled && retrySupported ? parseCommentSubmission(storage.get(pendingKey, null)) : null;
      if (!pending) storage.remove(pendingKey);
      if (pending) pending.uncertain = true;
      const recovery = node('div', 'comment-recovery'); recovery.dataset.commentRecovery = ''; recovery.hidden = !pending;
      const recoveryText = node('p'); recoveryText.setAttribute('role', 'status');
      const retryButton = node('button', 'ui-button ui-button-outline ui-button-small', '确认上次提交'); retryButton.type = 'button'; retryButton.dataset.commentRetry = '';
      const endButton = node('button', 'ui-button ui-button-quiet ui-button-small', '结束核对'); endButton.type = 'button'; endButton.dataset.commentRetryEnd = '';
      const recoveryDetails = node('details'); const recoveryBody = node('p'); recoveryBody.dataset.commentOriginal = '';
      recoveryDetails.append(node('summary', '', '查看上次评论'), recoveryBody);
      const recoveryError = node('p', 'interaction-error'); recoveryError.setAttribute('role', 'alert'); recoveryError.hidden = true;
      recovery.append(recoveryText, retryButton, endButton, recoveryDetails, recoveryError);
      (commentForm.closest('[data-comment-entry]') || commentForm).after(recovery);
      const savePending = () => {
        if (pending && draftsEnabled) storage.set(pendingKey, pending); else storage.remove(pendingKey);
        recovery.hidden = !pending;
        recoveryBody.textContent = pending?.payload.content || '';
        recoveryText.textContent = pending ? '上一条评论的结果尚未确认。确认会使用原内容和回复对象，当前输入继续保留。' : '';
        if (pending && (!draftsEnabled || parseCommentSubmission(storage.get(pendingKey, null))?.nonce !== pending.nonce)) recoveryText.textContent += '本次核对仅保留在当前页面，请暂时不要刷新。';
      };
      const reset = () => {
        submit.disabled = commentForm.dataset.commentLocked === 'true'; submit.replaceChildren(createIcon('send'), document.createTextNode(pending ? '先确认上一条' : '发表评论'));
        retryButton.disabled = commentBusy; endButton.disabled = commentBusy;
        retryButton.textContent = commentBusy ? '正在确认…' : '确认上次提交';
      };
      savePending(); reset();
      endButton.addEventListener('click', () => {
        if (commentBusy || !pending || !window.confirm('结束核对不会撤回已送达的评论，再次发送相同内容可能重复。确定结束核对？')) return;
        pending = null; savePending(); reset();
        if (commentForm.dataset.commentLocked !== 'true') input.focus({ preventScroll: true });
      });
      window.addEventListener('pageshow', () => { if (!commentBusy) reset(); });
      const sendComment = async (retry = false) => {
        if (commentBusy) return;
        if (!retry && pending) { recoveryError.textContent = '请先确认上一条评论，当前输入会保留。'; recoveryError.hidden = false; feedback.hidden = true; retryButton.focus({ preventScroll: true }); return; }
        if (retry && !pending) return;
        if (!retry && commentForm.dataset.commentLocked === 'true') { gate?.querySelector<HTMLElement>('[data-account-login]')?.click(); return; }
        if (!retry && !commentForm.reportValidity()) return;
        if (retryAt > Date.now()) { feedback.textContent = `提交较频繁，请在 ${Math.ceil((retryAt - Date.now()) / 1000)} 秒后重试，草稿已保留。`; feedback.hidden = false; return; }
        if (!retry && !input.value.trim()) { feedback.textContent = '请先写下评论内容'; feedback.hidden = false; input.focus({ preventScroll: true }); return; }
        const recoveryHadFocus = retry && recovery.contains(document.activeElement);
        commentBusy = true; submit.disabled = true; submit.replaceChildren(createIcon('refresh-cw'), document.createTextNode('正在提交…')); feedback.hidden = true; recoveryError.hidden = true; save();
        let payload: CommentRequest = retry && pending ? { ...pending.payload } : { parent_id: Number(parent.value) || 0, author_name: field(commentForm, 'author_name').value, email: field(commentForm, 'email').value, website: field(commentForm, 'website').value, content: input.value };
        const wasUncertain = !!pending?.uncertain;
        let sent = false;
        let submittedGeneration = commentGeneration;
        try {
          if (retrySupported && commentOwner === null) syncCommentIdentity(await getSession());
          submittedGeneration = commentGeneration;
          const verificationOwner = commentOwner;
          const verificationToken = window.ChunnelVerification ? await window.ChunnelVerification.getToken(commentForm, 'comment') : '';
          if (commentGeneration !== submittedGeneration || retrySupported && commentOwner !== verificationOwner) throw changedIdentity();
          if (retrySupported) {
            if (commentOwner === null) syncCommentIdentity(await getSession());
            if (retry && pending && pending.owner !== commentOwner) throw Object.assign(new Error('评论账号已变化，请恢复原提交身份后再确认。'), { code: 'comments.identity_changed' });
            if (!retry) {
              const nonce = crypto.randomUUID ? crypto.randomUUID() : [...crypto.getRandomValues(new Uint8Array(20))].map(value => value.toString(16).padStart(2, '0')).join('');
              pending = { version: 1, nonce, owner: commentOwner ?? 0, payload: { ...payload }, uncertain: true };
            }
            if (!pending) throw new Error('评论核对记录缺失，请重试');
            pending.uncertain = true; savePending();
            payload.expected_user_id = pending.owner;
          }
          payload.form_token = field(commentForm, 'form_token').value;
          if (verificationToken) payload.verification_token = verificationToken;
          retryButton.disabled = true; endButton.disabled = true;
          sent = true;
          const response = await api(commentAPI, { method: 'POST', headers: pending ? { 'Idempotency-Key': pending.nonce } : {}, body: payload });
          if (commentGeneration !== submittedGeneration) throw changedIdentity();
          // Only a complete receipt may consume the submitted draft. A 2xx
          // response with incomplete or unrelated data does not confirm it.
          const result = parseCommentReceipt(response, payload, commentForm.dataset.commentPostId, retrySupported ? pending : null);
          if (!result) throw new Error('评论提交结果未确认，输入已保留。请先核对评论状态，避免重复提交。');
          pending = null; savePending();
          // The composer remains editable while sending. Only consume the
          // exact draft that was submitted, preserving newer text or replies.
          if (input.value === payload.content && (Number(parent.value) || 0) === payload.parent_id) {
            input.value = ''; storage.remove(key); input.dispatchEvent(new Event('input', { bubbles: true }));
            parent.value = '0'; $('[data-comment-reply-context]').hidden = true; $('[data-comment-reply-label]').textContent = ''; label.textContent = '评论已提交';
          } else {
            save(); label.textContent = input.value ? '上一条评论已提交，当前输入已保留' : '评论已提交';
          }
          const previous = optional('.comment-notice', $('#comments')); previous?.remove();
          const notification = node('div', 'comment-notice comment-notice-success'); notification.setAttribute('role', 'status'); notification.append(createIcon('check-circle'), node('p', '', result.notice || '评论已提交，审核通过后将公开显示。'));
          // A longer receipt/link must not push the active composer down.
          (commentForm.closest('[data-comment-entry]') || commentForm).after(notification);
          if (result.status === 'approved' && result.comment && !result.replayed && !document.getElementById(`comment-${result.comment.id}`)) {
            const item = result.comment;
            const parentComment = payload.parent_id ? document.getElementById(`comment-${payload.parent_id}`) : null;
            const depth = parentComment ? Number(parentComment.className.match(/comment-depth-(\d+)/)?.[1] || 1) + 1 : 1;
            const article = node('article', `comment comment-depth-${depth}`); article.id = `comment-${item.id}`; article.dataset.commentId = String(item.id);
            const avatar = node('span', 'avatar avatar-b', [...(item.author_name || payload.author_name)][0] || '你'); avatar.setAttribute('aria-hidden', 'true');
            const main = node('div', 'comment-main'); const header = node('header'); const meta = node('div'); meta.append(node('b', '', item.author_name || payload.author_name), node('small', '', '刚刚')); window.NewPressRecognition?.decorateComment(meta, item);
            const anchor = node('a', 'comment-anchor', `#${item.id}`); anchor.href = `#comment-${item.id}`; anchor.setAttribute('aria-label', '评论固定链接'); header.append(meta, anchor);
            main.append(header);
            if (item.origin?.location || item.origin?.device) {
              const badges = node('div', 'comment-origin-badges'); badges.setAttribute('aria-label', '评论来源信息');
              if (item.origin.location) {
                const suffix = item.origin.precision === 'region' ? '（省／地区）' : item.origin.precision === 'country' ? '（国家）' : '';
                const badge = node('span', 'comment-origin-location');
                badge.title = 'IP 大致归属地，受网络出口和服务精度影响'; badge.dataset.precision = item.origin.precision || '';
                if (/^[a-z]{2}$/.test(item.origin.flag || '')) {
                  const flag = node('img', 'comment-origin-flag'); flag.src = `/plugin-assets/comment-origin/flags/${item.origin.flag}.svg`; flag.alt = ''; flag.width = 20; flag.height = 15; flag.loading = 'lazy'; flag.setAttribute('aria-hidden', 'true'); badge.append(flag);
                } else badge.append(createIcon('globe-2'));
                badge.append(node('span', 'comment-origin-label', 'IP 属地'), node('span', 'comment-origin-value', `${item.origin.location}${suffix}`)); badges.append(badge);
              }
              if (item.origin.device) { const badge = node('span', 'comment-origin-device'); badge.title = '由客户端 UA 报告，可被修改'; badge.append(createIcon(item.origin.kind === 'mobile' ? 'smartphone' : item.origin.kind === 'tablet' ? 'tablet' : item.origin.kind === 'bot' ? 'bot' : 'laptop'), node('span', 'comment-origin-value', item.origin.device)); badges.append(badge); }
              main.append(badges);
            }
            const copy = node('div', 'comment-copy comment-plain-text'); copy.append(emoji.fragment(payload.content.replace(/\r\n/g, '\n').trim())); main.append(copy);
            if (depth < Number(commentForm.dataset.commentDepth) && commentForm.dataset.commentReplies === 'true') { const footer = node('footer'); const reply = node('button', '', '回复'); reply.type = 'button'; reply.dataset.commentReply = ''; reply.dataset.parentId = String(item.id); reply.dataset.parentName = item.author_name || payload.author_name; reply.prepend(createIcon('corner-up-left')); footer.append(reply); main.append(footer); }
            article.append(avatar, main);
            if (parentComment) { let children = optional('.comment-main > .comment-children', parentComment); if (!children) { children = node('div', 'comment-children'); $('.comment-main', parentComment).append(children); } children.append(article); }
            else { optional('.comment-empty')?.remove(); $('.comment-list').append(article); }
            const count = $('#comments > header > b'); count.textContent = `${(Number.parseInt(count.textContent || '', 10) || 0) + 1} 条评论`;
            const jump = node('a', 'comment-published-link', '查看已发布评论'); jump.href = `#comment-${item.id}`; notification.append(jump);
          }
          if (result.status === 'approved' && result.replayed) {
            const jump = node('a', 'comment-published-link', '查看评论区'); jump.href = `${location.pathname}?comments_page=1#comments`; notification.append(jump);
          }
          showToast(result.notice || (result.status === 'approved' ? '评论已发布' : result.status === 'received' ? '已确认此前提交' : '评论已提交，等待审核'));
        } catch (failure) {
          const error = experienceError(commentGeneration !== submittedGeneration ? changedIdentity() : failure);
          // Only a definitive rejection of the first attempt releases its
          // identity. An uncertain earlier write keeps the same retry key.
          if (pending && sent && !wasUncertain && ['verification.required', 'verification.invalid', 'verification.unavailable', 'comments.login_required', 'comments.closed', 'comments.validation_failed', 'comments.invalid_parent', 'comments.rate_limited', 'comments.submission_capacity', 'comments.origin_rejected', 'request.invalid_idempotency_key'].includes(error.code || '')) { pending = null; savePending(); }
          if (error.code === 'comments.login_required') { commentForm.dataset.commentLoginRequired = 'true'; setCommentGate(false); getSession(true).then(syncCommentIdentity).catch(() => {}); }
          feedback.textContent = error.message; feedback.hidden = false;
          retryAt = Date.now() + (error.retryAfter || 0) * 1000;
          const fields = error.details?.fields;
          if (fields) feedback.textContent = Object.values(fields).join('；');
          if (error.code === 'comments.form_token_invalid') {
            try { const updated = await api(commentAPI, { anonymous: true }); const token = parseCommentToken(updated); if (token && commentGeneration === submittedGeneration) { field(commentForm, 'form_token').value = token; feedback.textContent = '表单已刷新，草稿已保留，请再次提交。'; } } catch { /* Keep the original failure and draft. */ }
          }
          if (pending) { recoveryError.textContent = feedback.textContent; recoveryError.hidden = false; feedback.hidden = true; }
        } finally {
          commentBusy = false; reset();
          if (commentGeneration === submittedGeneration && recoveryHadFocus && (document.activeElement === document.body || recovery.contains(document.activeElement))) {
            if (pending) retryButton.focus({ preventScroll: true });
            else if (commentForm.dataset.commentLocked === 'true') gate?.querySelector<HTMLElement>('[data-account-login]')?.focus({ preventScroll: true });
            else input.focus({ preventScroll: true });
          }
        }
      };
      commentForm.addEventListener('submit', event => { event.preventDefault(); return sendComment(); });
      retryButton.addEventListener('click', () => sendComment(true));
    }
  }

  const closedComments = !commentForm && optional('[data-comment-closed]');
  if (closedComments) {
    const key = `chunnel.comment.${location.pathname}`, pendingKey = `${key}.submission`;
    const draftsEnabled = closedComments.dataset.commentDrafts !== 'false';
    if (!draftsEnabled) { storage.remove(key); storage.remove(pendingKey); }
    let draft = draftsEnabled ? parseCommentDraft(storage.get(key, null)) : null;
    let pending = draftsEnabled ? parseCommentSubmission(storage.get(pendingKey, null)) : null;
    if (!draft) storage.remove(key);
    if (!pending) storage.remove(pendingKey);
    const apiPath = closedComments.dataset.commentApi;
    let token = closedComments.dataset.commentToken, busy = false;
    let identity: number | null = null, generation = 0;
    const observeIdentity = (session: ClientSession) => {
      const next = session.authenticated && Number(session.id) > 0 ? Number(session.id) : 0;
      if (identity !== null && identity !== next) generation++;
      identity = next;
    };
    document.addEventListener('chunnel:session', event => observeIdentity(event.detail));
    const draftPanel = node('div', 'comment-recovery'); draftPanel.dataset.commentSavedDraft = ''; draftPanel.hidden = !draft;
    const draftTitle = node('b', '', '保留的输入');
    const draftText = node('textarea'); draftText.readOnly = true; draftText.rows = 5; draftText.value = draft?.content || ''; draftText.setAttribute('aria-label', '保留的评论输入');
    const draftReply = node('p'); draftReply.textContent = draft && Number(draft.parent) > 0 ? `回复评论 #${Number(draft.parent)}` : ''; draftReply.hidden = !draftReply.textContent;
    const draftHint = node('p', '', '草稿暂存于此标签页（最长 24 小时），评论重新开放后可继续编辑。');
    const discard = node('button', 'ui-button ui-button-quiet ui-button-small', '清除草稿'); discard.type = 'button';
    draftPanel.append(draftTitle, draftReply, draftText, draftHint, discard);
    const recovery = node('div', 'comment-recovery'); recovery.dataset.commentRecovery = ''; recovery.hidden = !pending;
    const status = node('p'); status.setAttribute('role', 'status');
    const query = node('button', 'ui-button ui-button-outline ui-button-small', '查询上次提交'); query.type = 'button'; query.dataset.commentReceiptQuery = ''; query.hidden = !apiPath;
    const end = node('button', 'ui-button ui-button-quiet ui-button-small', '结束核对'); end.type = 'button'; end.dataset.commentRetryEnd = '';
    const login = node('button', 'ui-button ui-button-outline ui-button-small', '登录原账号'); login.type = 'button'; login.hidden = true;
    const details = node('details'), original = node('p'); original.dataset.commentOriginal = ''; original.textContent = pending?.payload.content || '';
    details.append(node('summary', '', '查看上次评论'), original);
    const errorText = node('p', 'interaction-error'); errorText.setAttribute('role', 'alert'); errorText.hidden = true;
    login.addEventListener('click', async () => {
      try { await signIn(); } catch (failure) {
          const error = experienceError(failure); errorText.textContent = error.message || '暂时无法登录，原内容仍保留。'; errorText.hidden = false; }
    });
    recovery.append(status, query, end, login, details, errorText);
    const resultPanel = node('div', 'comment-notice comment-notice-success'); resultPanel.setAttribute('role', 'status'); resultPanel.setAttribute('tabindex', '-1'); resultPanel.hidden = true;
    closedComments.append(draftPanel, recovery, resultPanel);
    status.textContent = apiPath ? '上次提交的结果尚未确认。可查询已有回执，不会重新发送评论。' : '上次提交的内容仍保留。评论查询暂不可用，入口恢复后可继续核对。';
    const sameDraft = (payload: CommentPayload) => draft && draft.content === payload.content && (Number(draft.parent) || 0) === payload.parent_id;
    if (pending && sameDraft(pending.payload)) draftHint.textContent = '这份输入与上次提交一致，结果仍待确认。查询成功后会清除这份副本。';
    const finish = (message: string) => {
      pending = null; storage.remove(pendingKey); recovery.hidden = true;
      resultPanel.replaceChildren(createIcon('check-circle'), node('p', '', message)); resultPanel.hidden = false;
    };
    discard.addEventListener('click', () => {
      if (busy || !draft || !window.confirm('清除此标签页的草稿？上次提交的核对记录会继续保留。')) return;
      draft = null; storage.remove(key); draftPanel.hidden = true;
      if (pending) (apiPath ? query : end).focus({ preventScroll: true });
      else { resultPanel.replaceChildren(node('p', '', '本标签页的草稿已清除。')); resultPanel.hidden = false; resultPanel.focus({ preventScroll: true }); }
    });
    end.addEventListener('click', () => {
      if (busy || !pending || !window.confirm('结束核对不会撤回已送达的评论。原内容可能已经发表，重新发送可能重复。确定结束核对？')) return;
      finish('已结束核对；此操作不会撤回已提交的评论。');
      draftHint.textContent = '保留的输入可能已经提交。评论重新开放后，发送前请先核对评论区。';
      resultPanel.focus({ preventScroll: true });
    });
    query.addEventListener('click', async () => {
      if (busy || !pending || !apiPath) return;
      const hadFocus = recovery.contains(document.activeElement), submission = pending;
      const requestGeneration = generation;
      busy = true; query.disabled = true; end.disabled = true; discard.disabled = true; query.textContent = '正在查询…'; login.hidden = true; errorText.hidden = true;
      try {
        const session = await getSession(true);
        if (generation !== requestGeneration) throw new Error('查询期间账号已变化，原内容仍保留，请恢复原提交身份后再次查询。');
        observeIdentity(session);
        const owner = session?.authenticated && Number(session.id) > 0 ? Number(session.id) : 0;
        if (owner !== submission.owner) {
          login.hidden = !(owner === 0 && submission.owner > 0);
          throw new Error(submission.owner > 0 ? '请登录原提交账号后再次查询，输入仍保留。' : '这条评论以访客身份提交，请退出当前账号后再次查询。');
        }
        const response = await api(`${apiPath}/receipt`, { method: 'POST', headers: { 'Idempotency-Key': submission.nonce }, body: { ...submission.payload, expected_user_id: submission.owner, form_token: token } });
        if (generation !== requestGeneration) throw new Error('查询期间账号已变化，原内容仍保留，请恢复原提交身份后再次查询。');
        const result = parseCommentReceipt(response, submission.payload, closedComments.dataset.commentPostId, submission, true);
        if (!result) throw new Error('回执尚未确认，原内容仍保留。请稍后再次查询。');
        if (sameDraft(submission.payload)) { draft = null; storage.remove(key); draftPanel.hidden = true; }
        finish(result.notice || '已确认上次提交。');
        if (result.status === 'approved') {
          const link = node('a', 'comment-published-link', '查看评论区'); link.href = `${location.pathname}?comments_page=1#comments`; resultPanel.append(link);
        }
      } catch (failure) {
          const error = generation !== requestGeneration ? new Error('查询期间账号已变化，原内容仍保留，请恢复原提交身份后再次查询。') : experienceError(failure);
        let message = error.message || '暂时无法查询，原内容仍保留。';
        if ('code' in error && error.code === 'comments.form_token_invalid') {
          try { const updated = await api(apiPath, { anonymous: true }); const freshToken = parseCommentToken(updated); if (freshToken && generation === requestGeneration) { token = freshToken; message = '查询凭证已更新，原内容仍保留，请再次查询。'; } } catch { /* Preserve the saved request and original error. */ }
        }
        errorText.textContent = message; errorText.hidden = false;
      } finally {
        busy = false; query.disabled = false; end.disabled = false; discard.disabled = false; query.textContent = '查询上次提交';
        if (generation === requestGeneration && hadFocus && (document.activeElement === document.body || recovery.contains(document.activeElement))) (pending ? (login.hidden ? query : login) : resultPanel).focus({ preventScroll: true });
      }
    });
  }


}
