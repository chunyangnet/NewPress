(() => {
  'use strict';
  const { icon, showToast } = window.ChunnelAdmin;
  const editorForm = document.querySelector('[data-content-editor]');
  const editorSavebar = document.querySelector('[data-editor-savebar]');
  let initialEditor = '';
  let submittingEditor = false;
  let pendingEditorSubmission = null;
  let forcedEditorDirtyLabel = '';
  const serializeEditorData = (data) => {
    const values = {};
    data.forEach((value, key) => {
      if (key === 'csrf_token' || key === 'action') return;
      if (Object.prototype.hasOwnProperty.call(values, key)) values[key] = [].concat(values[key], String(value));
      else values[key] = String(value);
    });
    return JSON.stringify(Object.fromEntries(Object.keys(values).sort().map(key => [key, values[key]])));
  };
  const serializedEditor = () => editorForm ? serializeEditorData(new FormData(editorForm)) : '';
  const markEditorDirty = () => {
    if (!editorForm || !editorSavebar) return;
    const dirty = serializedEditor() !== initialEditor;
    editorSavebar.classList.toggle('is-dirty', dirty);
    const state = editorSavebar.querySelector('[data-editor-save-state]');
    if (state) state.textContent = submittingEditor ? '正在确认保存…' : pendingEditorSubmission ? '保存结果待确认，输入已保留' : dirty ? (forcedEditorDirtyLabel || '有尚未保存的编辑') : editorForm.dataset.editorNew === 'true' ? '尚未保存草稿' : '所有更改已保存';
  };
  const slugify = (value) => value.toLowerCase().trim()
    .replace(/[_\s]+/g, '-')
    .replace(/[^a-z0-9-]/g, '')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '')
    .slice(0, 128)
    .replace(/-$/, '');
  const updateEditorCount = (input) => {
    const key = input.dataset.countInput;
    const output = key ? document.querySelector(`[data-count-output="${key}"]`) : null;
    if (output) output.textContent = String([...input.value].length);
  };
  const updateWordCount = () => {
    const source = editorForm?.querySelector('[data-content-source]');
    const output = editorForm?.querySelector('[data-editor-word-count]');
    if (!source || !output) return;
    let text = source.value;
    if (text.includes('<')) {
      const template = document.createElement('template');
      template.innerHTML = text;
      template.content.querySelectorAll('script, style').forEach((element) => element.remove());
      text = template.content.textContent;
    }
    const compact = text.replace(/\s+/g, '');
    output.textContent = `${[...compact].length} 字`;
  };
  const updateScheduleRequirement = () => {
    const status = editorForm?.querySelector('[data-content-status]');
    const field = editorForm?.querySelector('[data-schedule-field]');
    const input = field?.querySelector('input');
    const required = status?.value === 'future';
    field?.classList.toggle('is-required', required);
    if (input) input.required = required;
  };
  const updateEditorSaveLabel = () => {
    if (!editorForm || pendingEditorSubmission) return;
    const status = editorForm.querySelector('[data-content-status]')?.value || 'draft';
    const output = editorForm.querySelector('[data-editor-save-label]');
    if (editorForm.hasAttribute?.('data-document-editor')) {
      const canonical = editorForm.dataset.editorCanonicalStatus;
      const labels = {draft:'保存草稿', pending:'提交审核', publish:canonical === 'publish' ? '更新' : '发布…', future:'保存定时发布', private:'保存私密内容', trash:'保存修改'};
      if (output) output.textContent = labels[status] || '保存';
      return;
    }
    if (editorForm.dataset.editorNew !== 'true') return;
    const labels = {
      draft: '创建草稿', pending: '提交审核', publish: '创建并发布',
      future: '安排定时发布', private: '创建私密内容'
    };
    if (output) output.textContent = labels[status] || '创建内容';
  };
  if (editorForm) {
    const source = editorForm.querySelector('[data-content-source]');
    editorForm.querySelectorAll('[data-source-format]').forEach((button) => {
      button.addEventListener('pointerdown', (event) => event.preventDefault());
      button.addEventListener('click', () => {
        if (!source || source.disabled || source.readOnly || !window.ChunnelEmoji) return;
        const tag = button.dataset.sourceFormat;
        if (!['p', 'strong', 'h2', 'code'].includes(tag)) return;
        const opening = `<${tag}>`, closing = `</${tag}>`;
        const start = source.selectionStart, end = source.selectionEnd;
        const selected = source.value.slice(start, end);
        const unwrap = tag !== 'code' && source.value.slice(start - opening.length, start) === opening && source.value.slice(end, end + closing.length) === closing;
        const text = tag === 'code' ? selected.replace(/[&<>]/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' })[char]) : selected;
        const range = unwrap ? { start: start - opening.length, end: end + closing.length } : { start, end };
        const error = window.ChunnelEmoji.replaceSelection(source, unwrap ? text : opening + text + closing, range);
        if (error) { showToast(error, 'warning'); return; }
        const cursor = unwrap ? range.start : start + opening.length;
        source.setSelectionRange(cursor, cursor + text.length);
      });
    });
    const title = editorForm.querySelector('[data-count-input="title"]');
    const slug = editorForm.querySelector('[data-slug-input]');
    const type = editorForm.querySelector('[data-content-type]');
    const classificationPanel = editorForm.querySelector('[data-classification-panel]');
    const categoryChoices = [...editorForm.querySelectorAll('input[name="category_ids"]')];
    const tagInput = editorForm.querySelector('[data-tag-input]');
    const tagSuggestions = [...editorForm.querySelectorAll('[data-tag-suggestion]')];
    const coverInput = editorForm.querySelector('input[name="cover_url"]');
    const coverPreview = editorForm.querySelector('.admin-cover-preview');
    const coverChoices = [...editorForm.querySelectorAll('[data-cover-media]')];
    const previewButton = document.querySelector('[data-editor-preview]');
    const autosaveDetail = editorSavebar?.querySelector('[data-editor-autosave-detail]');
    const recoveryActions = editorSavebar?.querySelector('[data-editor-recovery-actions]');
    const recoveryButton = recoveryActions?.querySelector('[data-editor-retry]');
    const editorOwnerID = Number(editorForm.dataset.editorUserId || 0);
    const editorOwnerLogin = editorForm.dataset.editorLogin || '';
    let autosaveURL = editorForm.dataset.autosaveUrl || '';
    let autosaveEnabled = editorForm.dataset.autosaveEnabled === 'true' && Boolean(autosaveURL);
    const autosaveInterval = Math.min(300, Math.max(10, Number(editorForm.dataset.autosaveInterval) || 30)) * 1000;
    const autosaveRetention = Math.min(720, Math.max(1, Number(editorForm.dataset.autosaveRetention) || 72));
    const forceEditorDirty = editorForm.dataset.editorForceDirty === 'true';
    if (forceEditorDirty) forcedEditorDirtyLabel = editorSavebar?.querySelector('[data-editor-save-state]')?.textContent?.trim() || '有尚未保存的编辑';
    let autosaveTimer = 0;
    let autosavePromise = null;
    let autosaveQueued = false;
    let autosaveStopped = false;
    const publishedZone = editorForm.querySelector('[name="published_timezone"]');
    const publishedSource = editorForm.querySelector('[name="published_source"]');
    let editorTimezone = publishedZone?.value || 'UTC';
    const timezoneReview = editorForm.querySelector('[data-published-timezone-review]');
    let timezoneNeedsReview = false;
    const reviewPublicationTimezone = (payload) => {
      if (!payload?.fields?.published_at || typeof payload.published_timezone !== 'string' || !payload.published_timezone || payload.published_timezone.length > 100) return;
      editorTimezone = payload.published_timezone;
      timezoneNeedsReview = true;
      if (timezoneReview) timezoneReview.hidden = false;
      editorForm.querySelectorAll('[data-published-timezone-label]').forEach(label => { label.textContent = editorTimezone; });
      // Keep the old hidden token until the editor explicitly confirms the new calendar.
    };
    let lastAutosaveSignature = '';
    let autosaveController = null;
    let editorSessionPromise = null;
    let editorSessionController = null;
    let editorSubmissionController = null;
    let intentionalEditorNavigation = false;
    let editorPageHidden = false;
    let browserRecovery = null;
    let slugTouched = Boolean(slug?.value);
    const updateCoverPreview = () => {
      if (!coverPreview || !coverInput) return;
      const value = coverInput.value.trim();
      coverChoices.forEach((choice) => choice.classList.toggle('is-selected', choice.dataset.coverMedia === value));
      coverPreview.replaceChildren();
      if (value.startsWith('/') && !value.startsWith('//') && !/[\\\u0000-\u001f\u007f-\u009f]/.test(value)) {
        const preview = document.createElement('img');
        preview.src = value;
        preview.alt = '封面预览';
        coverPreview.append(preview);
      } else {
        const mark = document.createElement('span');
        mark.append(icon('image'));
        const message = document.createElement('p');
        message.textContent = value ? '仅接受站内绝对路径' : '尚未设置封面';
        coverPreview.append(mark, message);
      }
    };
    editorForm.querySelectorAll('[data-count-input]').forEach((input) => {
      updateEditorCount(input);
      input.addEventListener('input', () => updateEditorCount(input));
    });
    updateWordCount();
    updateScheduleRequirement();
    updateEditorSaveLabel();
    updateCoverPreview();
    let slugRequest = null, slugSequence = 0, suggestedTitle = '';
    const slugGenerate = editorForm.querySelector('[data-slug-generate]');
    const slugStatus = editorForm.querySelector('[data-slug-status]');
    const slugAutomatic = editorForm.dataset.slugMode !== 'manual';
    const stopSlug = () => { slugSequence++; slugRequest?.abort(); slugRequest = null; if (slugGenerate) { slugGenerate.disabled = !!slug?.disabled; slugGenerate.removeAttribute('aria-busy'); } };
    function permalinkPreview() {
      const page = type?.value === 'page', style = editorForm.dataset[page ? 'pagePermalink' : 'postPermalink'];
      const prefix = page ? '/pages/' : '/posts/';
      const date = editorForm.querySelector('[name="published_at"]')?.value.slice(0,10).replaceAll('-','/') || editorForm.dataset.permalinkDate;
      const alias = slug?.value || 'english-slug';
      const part = style === 'id' ? `id/${Number(editorForm.dataset.permalinkId) > 0 ? editorForm.dataset.permalinkId : '保存后分配'}` : style === 'month' ? `${date.slice(0,7)}/${alias}` : style === 'date' ? `${date}/${alias}` : alias;
      const output = editorForm.querySelector('[data-permalink-preview]'); if (output) output.textContent = prefix + part;
    }
    async function suggestSlug(manual = false) {
      if (!slug || slug.disabled || !title?.value.trim() || (!manual && (!slugAutomatic || slugTouched || suggestedTitle === title.value))) return;
      stopSlug(); const sequence = slugSequence, originalTitle = title.value, originalSlug = slug.value;
      const controller = new AbortController(); slugRequest = controller;
      slugGenerate.disabled = true; slugGenerate.setAttribute('aria-busy','true'); slugStatus.textContent = '正在生成英文别名…';
      const timer = setTimeout(() => controller.abort(), 25000);
      try {
        const response = await fetch('/api/v1/admin/contents/slug-suggestion', {method:'POST', credentials:'same-origin', cache:'no-store', signal:controller.signal, headers:{'Content-Type':'application/json','X-CSRF-Token':editorForm.querySelector('[name="csrf_token"]').value}, body:JSON.stringify({title:originalTitle,type:type.value})});
        const result = await response.json();
        if (!response.ok) throw new Error(result.error?.message || result.message || '别名生成失败，请重试。');
        if (sequence !== slugSequence || title.value !== originalTitle || slug.value !== originalSlug || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(result.slug) || result.slug.length > 128) return;
        slug.value = result.slug; suggestedTitle = originalTitle;
        slug.dispatchEvent(new Event('input',{bubbles:true})); slugTouched = manual;
        slugStatus.textContent = result.notice; permalinkPreview();
      } catch (error) { if (sequence === slugSequence) slugStatus.textContent = error.name === 'AbortError' ? '生成已中断，可重新尝试。' : error.message; }
      finally { clearTimeout(timer); if (sequence === slugSequence) { slugRequest = null; slugGenerate.disabled = false; slugGenerate.removeAttribute('aria-busy'); } }
    }
    title?.addEventListener('input', () => {
      stopSlug();
      if (slug && !slugTouched && slugAutomatic) slug.value = slugify(title.value);
      permalinkPreview();
    });
    title?.addEventListener('blur', () => { void suggestSlug(); });
    slugGenerate?.addEventListener('click', () => { void suggestSlug(true); });
    slug?.addEventListener('input', event => { if (event.isTrusted) stopSlug(); slugTouched = slug.value !== ''; permalinkPreview(); });
    editorForm.addEventListener('submit', stopSlug, true);
    window.addEventListener('pagehide', stopSlug);
    type?.addEventListener('change', () => { stopSlug(); permalinkPreview(); });
    editorForm.querySelector('[name="published_at"]')?.addEventListener('input', permalinkPreview);
    permalinkPreview();
    coverInput?.addEventListener('input', updateCoverPreview);
    coverChoices.forEach((choice) => choice.addEventListener('click', () => {
      if (!coverInput || coverInput.disabled || coverInput.readOnly) return;
      coverInput.value = choice.dataset.coverMedia || '';
      coverInput.dispatchEvent(new Event('input', { bubbles: true }));
    }));
    type?.addEventListener('change', () => {
      const prefix = slug?.closest('div')?.querySelector('span');
      if (prefix) prefix.textContent = type.value === 'page' ? '/pages/' : '/posts/';
      if (classificationPanel) classificationPanel.hidden = type.value === 'page';
    });
    categoryChoices.forEach((choice) => choice.addEventListener('change', () => {
      const checked = categoryChoices.filter((item) => item.checked);
      if (checked.length <= 8) return;
      choice.checked = false;
      showToast('每篇文章最多选择 8 个分类。', 'warning');
    }));
    tagSuggestions.forEach((button) => button.addEventListener('click', () => {
      if (!tagInput) return;
      const current = tagInput.value.split(/[,，]/).map((item) => item.trim()).filter(Boolean);
      const suggestion = button.dataset.tagSuggestion || '';
      if (suggestion && !current.some((item) => item.toLocaleLowerCase() === suggestion.toLocaleLowerCase())) current.push(suggestion);
      tagInput.value = current.join(', ');
      tagInput.dispatchEvent(new Event('input', { bubbles: true }));
    }));
    editorForm.querySelector('[data-content-status]')?.addEventListener('change', () => {
      updateScheduleRequirement();
      updateEditorSaveLabel();
    });
    editorForm.querySelector('[data-content-source]')?.addEventListener('input', updateWordCount);
    initialEditor = serializedEditor();
    if (forceEditorDirty) initialEditor = `canonical:${Date.now()}`;
    const editorIsDirty = () => serializedEditor() !== initialEditor;
    const editorNeedsAutosave = () => {
      const signature = serializedEditor();
      return signature !== initialEditor || (Boolean(lastAutosaveSignature) && signature !== lastAutosaveSignature);
    };
    const setAutosaveDetail = (message, tone = '') => {
      if (!autosaveDetail) return;
      autosaveDetail.textContent = message;
      autosaveDetail.dataset.tone = tone;
      if (recoveryActions) recoveryActions.hidden = tone !== 'error';
    };
    const setEditorRecovery = (status) => {
      const login = recoveryActions?.querySelector('[data-editor-login-link]');
      const current = recoveryActions?.querySelector('[data-editor-current-link]');
      if (login) login.hidden = status !== 401 && status !== 403;
      if (current) current.hidden = status !== 409;
    };
    const scheduleAutosave = () => {
      window.clearTimeout(autosaveTimer);
      if (!autosaveEnabled || autosaveStopped || timezoneNeedsReview || submittingEditor || pendingEditorSubmission || browserRecovery?.blocked() || browserRecovery?.busy() || editorPageHidden || !editorNeedsAutosave()) return;
      // The policy interval is both an idle debounce and a lower bound between writes.
      autosaveTimer = window.setTimeout(() => { void saveAutosave(false); }, autosaveInterval);
    };
    const autosaveErrorMessage = (payload, status) => {
      setEditorRecovery(status);
      if (status === 409) return '正式版本已变化；自动保存已暂停，请在新窗口核对。当前输入仍保留。';
      if (status === 422 && payload?.fields) return Object.values(payload.fields)[0] || '恢复点未通过字段校验。';
      if (status === 401) return '登录已失效；请在新窗口登录，再回到这里重试。当前输入仍保留，请勿刷新。';
      if (status === 403) return '当前账号没有保存权限，或登录凭证需要更新；输入已保留。';
      if (status === 413) return '编辑内容超过自动保存请求上限。';
      return '自动保存暂时不可用；当前输入仍保留在浏览器中。';
    };
    const checkEditorSession = () => {
      if (editorSessionPromise) return editorSessionPromise;
      const controller = new AbortController();
      editorSessionController = controller;
      const timeout = window.setTimeout(() => controller.abort(new DOMException('Timeout', 'TimeoutError')), 15000);
      editorSessionPromise = (async () => {
        try {
          const response = await fetch('/api/v1/admin/session', {
            credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: controller.signal,
            headers: {Accept: 'application/json'}
          });
          const session = await response.json();
          if (controller.signal.aborted) throw controller.signal.reason;
          if (editorPageHidden) return false;
          if (response.status === 401 || session?.authenticated === false) {
            autosaveStopped = true;
            setAutosaveDetail(autosaveErrorMessage(null, 401), 'error'); return false;
          }
          if (!response.ok || session?.authenticated !== true || typeof session.csrf_token !== 'string' ||
              session.csrf_token.length < 32 || session.csrf_token.length > 256 || !Array.isArray(session.role_capabilities)) throw new Error('Unconfirmed session');
          const sameOwner = editorOwnerID > 0 ? session.user_id === editorOwnerID : !session.user_id && editorOwnerLogin && session.username === editorOwnerLogin;
          if (!sameOwner) {
            autosaveStopped = true; setEditorRecovery(401);
            setAutosaveDetail('当前账号已切换。请在新窗口登录原编辑账号，再回到这里重试；' + (pendingEditorSubmission ? '原保存仍待确认，当前输入已保留。' : '草稿尚未发送。'), 'error'); return false;
          }
          const can = name => session.role_capabilities.some(value => typeof value === 'string' &&
            (value === '*' || value === name || (value.endsWith(':*') && name.startsWith(value.slice(0, -1)))));
          if (session.read_only !== false || !can('admin:access') || (!can('content:write') && !can('content:write:own'))) {
            autosaveStopped = true;
            setAutosaveDetail(autosaveErrorMessage(null, 403), 'error'); return false;
          }
          editorForm.querySelectorAll('[name="csrf_token"]').forEach(input => { input.value = session.csrf_token; });
          return true;
        } catch (error) {
          if (!editorPageHidden && controller.signal.reason?.name !== 'AbortError') {
            setEditorRecovery(0);
            setAutosaveDetail(controller.signal.reason?.name === 'TimeoutError' ? '登录检查超时；当前输入已保留，请重试。' : '暂时无法确认登录状态；当前输入已保留，请重试。', 'error');
          }
          return false;
        } finally {
          window.clearTimeout(timeout); editorSessionController = null; editorSessionPromise = null;
        }
      })();
      return editorSessionPromise;
    };
    const performAutosave = async (manual) => {
      if (timezoneNeedsReview) {
        setAutosaveDetail('请先核对副本的发布时间，并确认按当前站点时区解释。', 'error');
        return null;
      }
      if (!autosaveURL || submittingEditor || pendingEditorSubmission || browserRecovery?.blocked() || browserRecovery?.busy()) return null;
      const signature = serializedEditor();
      if (!manual && (!editorNeedsAutosave() || signature === lastAutosaveSignature)) return null;
      editorSavebar?.classList.add('is-autosaving');
      setAutosaveDetail(manual ? '正在建立预览恢复点…' : '正在建立身份专属恢复点…', 'progress');
      const body = new URLSearchParams();
      new FormData(editorForm).forEach((value, key) => body.append(key, String(value)));
      body.append('action', 'autosave');
      autosaveController = new AbortController();
      const controller = autosaveController;
      let timedOut = false;
      const timeout = window.setTimeout(() => { timedOut = true; controller.abort(); }, 15000);
      try {
        const response = await fetch(autosaveURL, {
          method: 'POST', body, credentials: 'same-origin', signal: controller.signal,
          headers: { Accept: 'application/json' }
        });
        let payload = null;
        try { payload = await response.json(); } catch { /* A bounded fallback message is shown below. */ }
        if (controller.signal.aborted) throw new DOMException('Cancelled', 'AbortError');
        if (!response.ok) {
          reviewPublicationTimezone(payload);
          const message = autosaveErrorMessage(payload, response.status);
          setAutosaveDetail(message, 'error');
          if (response.status === 409 || response.status === 401 || response.status === 403) autosaveStopped = true;
          if (manual) showToast(message, 'error');
          return null;
        }
        if (payload?.available !== true || typeof payload.updated_at !== 'string' ||
            !Number.isFinite(Date.parse(payload.updated_at)) || typeof payload.preview_url !== 'string') throw new Error('Unconfirmed autosave');
        const preview = new URL(payload.preview_url, window.location.href);
        const expectedPath = new URL(autosaveURL, window.location.href).pathname.replace(/autosave\/$/, 'preview/');
        if (preview.origin !== window.location.origin || preview.pathname !== expectedPath ||
            preview.username || preview.password || preview.hash || !preview.searchParams.get('token')) throw new Error('Invalid preview address');
        lastAutosaveSignature = signature;
        autosaveStopped = false;
        const label = window.ChunnelTime?.formatClock(payload.updated_at) || '刚刚';
        setAutosaveDetail(`恢复点已更新于 ${label}，保留 ${autosaveRetention} 小时；正式版本尚未改变。`, 'success');
        return payload;
      } catch (error) {
        if (timedOut || error?.name !== 'AbortError') {
          const message = timedOut ? '自动保存连接超时，当前输入已保留，将稍后重试；请勿关闭页面。' : '自动保存结果未确认，当前输入已保留，将稍后重试；请勿关闭页面。';
          setAutosaveDetail(message, 'error');
          if (manual) showToast(message, 'error');
        }
        return null;
      } finally {
        window.clearTimeout(timeout);
        autosaveController = null;
        editorSavebar?.classList.remove('is-autosaving');
      }
    };
    async function saveAutosave(manual) {
      if (autosavePromise) {
        autosaveQueued = true;
        await autosavePromise;
        if (manual && !submittingEditor) return saveAutosave(true);
        return null;
      }
      autosavePromise = performAutosave(manual);
      let payload = null;
      try { payload = await autosavePromise; } finally { autosavePromise = null; }
      if (autosaveQueued) {
        autosaveQueued = false;
        scheduleAutosave();
      } else if (editorNeedsAutosave() && serializedEditor() !== lastAutosaveSignature) {
        scheduleAutosave();
      }
      return payload;
    }
    const editorChanged = () => { syncEditorSubmitControls(); scheduleAutosave(); browserRecovery?.schedule(); };
    editorForm.addEventListener('input', editorChanged);
    editorForm.addEventListener('change', editorChanged);
    markEditorDirty();
    if (forceEditorDirty) scheduleAutosave();
    recoveryButton?.addEventListener('click', async () => {
      if (submittingEditor || recoveryButton.disabled) return;
      if (pendingEditorSubmission) { await submitEditor(null); return; }
      recoveryButton.disabled = true;
      try {
        if (!await checkEditorSession()) return;
        if (autosaveURL) await saveAutosave(true);
        else setAutosaveDetail('登录已确认，当前输入已保留。请点击创建草稿或相应保存操作。', 'success');
      } finally { recoveryButton.disabled = false; }
    });
    previewButton?.addEventListener('click', async () => {
      if (submittingEditor || pendingEditorSubmission) return;
      const previewWindow = window.open('about:blank', '_blank');
      if (previewWindow) previewWindow.opener = null;
      previewButton.disabled = true;
      const original = previewButton.textContent;
      previewButton.replaceChildren(icon('refresh-cw'), document.createTextNode('正在准备预览…'));
      const payload = await checkEditorSession() ? await saveAutosave(true) : null;
      previewButton.disabled = false;
      previewButton.replaceChildren(icon('scan-search'), document.createTextNode(original?.trim() || '预览草稿'));
      if (!payload?.preview_url) {
        previewWindow?.close();
        return;
      }
      if (previewWindow) previewWindow.location.replace(payload.preview_url);
      else {
        showToast('浏览器阻止了预览窗口；请允许本站打开新窗口后重试。', 'warning');
      }
    });
    const editorActionButtons = [...editorForm.querySelectorAll('button[name="action"]')];
    const originallyDisabled = new Set(editorActionButtons.filter(button => button.disabled));
    const syncEditorSubmitControls = () => {
      editorForm.dataset.editorPendingSubmission = String(Boolean(pendingEditorSubmission));
      editorActionButtons.forEach(button => {
        button.disabled = originallyDisabled.has(button) || button.hidden || submittingEditor || browserRecovery?.busy() || ((Boolean(pendingEditorSubmission) || browserRecovery?.blocked()) && button.value !== 'save');
      });
      if (previewButton) previewButton.disabled = submittingEditor || Boolean(pendingEditorSubmission) || browserRecovery?.blocked() || browserRecovery?.busy();
      if (recoveryButton) {
        recoveryButton.disabled = submittingEditor;
        recoveryButton.textContent = pendingEditorSubmission ? '重试原保存' : autosaveURL ? '重试恢复点' : '重新检查登录';
      }
      const label = editorForm.querySelector('[data-editor-save-label]');
      if (label) {
        if (pendingEditorSubmission) label.textContent = submittingEditor ? '正在保存…' : '重试原保存';
        else if (browserRecovery?.blocked()) label.textContent = '先处理本机副本';
        else if (editorForm.dataset.editorNew === 'true' || editorForm.hasAttribute?.('data-document-editor')) updateEditorSaveLabel();
        else label.textContent = '保存新版本';
      }
      if (submittingEditor) editorForm.setAttribute('aria-busy', 'true');
      else editorForm.removeAttribute('aria-busy');
      markEditorDirty();
      if (!submittingEditor && !pendingEditorSubmission && browserRecovery?.blocked()) {
        const state = editorSavebar?.querySelector('[data-editor-save-state]');
        if (state) state.textContent = '发现待确认保存，请先恢复本机副本';
      }
    };
    const showEditorSubmissionErrors = (fields = {}) => {
      const list = editorSavebar?.querySelector('[data-editor-submit-errors]');
      list?.replaceChildren();
      editorForm.querySelectorAll('[data-editor-invalid]').forEach(input => {
        input.removeAttribute('aria-invalid'); input.removeAttribute('data-editor-invalid');
      });
      if (!list) return;
      const labels = {title: '标题', slug: '别名', content_source: '正文', status: '发布状态', published_at: '发布时间', cover_url: '封面', category_ids: '分类', tags: '标签', type: '内容类型', excerpt: '摘要'};
      Object.entries(fields || {}).slice(0, 12).forEach(([name, message]) => {
        if (typeof message !== 'string') return;
        const item = document.createElement('li');
        item.textContent = `${labels[name] || '字段'}：${message.slice(0, 300)}`; list.append(item);
        editorForm.querySelectorAll('input, textarea, select').forEach(input => {
          if (input.name === name) { input.setAttribute('aria-invalid', 'true'); input.setAttribute('data-editor-invalid', ''); }
        });
      });
      list.hidden = !list.childElementCount;
    };
    const acknowledgeEditorSubmission = (sent, payload) => {
      const base = `/admin/content/${payload?.id}`;
      if (payload?.saved !== true || payload.submission_key !== sent.key || payload.action !== sent.action ||
          !Number.isSafeInteger(payload.id) || payload.id <= 0 || (sent.id && payload.id !== sent.id) ||
          !Number.isSafeInteger(payload.version) || payload.version !== sent.version + 1 ||
          typeof payload.replayed !== 'boolean' || typeof payload.autosave_enabled !== 'boolean' ||
          payload.editor_url !== `${base}/edit/?notice=saved` || payload.autosave_url !== `${base}/autosave/`) throw new Error('Unconfirmed submission');
      const savesFields = sent.action === 'create' || sent.action === 'save' || (sent.action === 'publish' && new URLSearchParams(sent.body).has('ai_pro_max'));
      const keepInput = serializedEditor() !== sent.signature || (!savesFields && sent.wasDirty);
      const stayInDocument = editorForm.hasAttribute?.('data-document-editor') && sent.action === 'save';
      if (!keepInput && !stayInDocument) {
        intentionalEditorNavigation = true;
        window.location.assign(payload.editor_url);
        return;
      }
      let version = editorForm.querySelector('[name="version"]');
      if (!version) { version = document.createElement('input'); version.type = 'hidden'; version.name = 'version'; editorForm.append(version); }
      version.value = String(payload.version);
      editorForm.setAttribute('action', `${base}/edit/`);
      const wasNew = editorForm.dataset.editorNew === 'true';
      editorForm.dataset.editorNew = 'false';
      autosaveURL = payload.autosave_url; autosaveEnabled = payload.autosave_enabled;
      editorForm.dataset.autosaveUrl = autosaveURL; editorForm.dataset.autosaveEnabled = String(autosaveEnabled);
      const captured = new URLSearchParams(sent.body); captured.set('version', String(payload.version));
      if (savesFields) {
        // The recovery point may be removed by this save. Its instant is now
        // canonical; subsequent edits must not depend on the old recovery key.
        if (publishedSource) publishedSource.value = 'canonical';
        captured.set('published_source', 'canonical');
      }
      // Status/revision actions do not save the visible fields. Keep those fields dirty.
      initialEditor = savesFields ? serializeEditorData(captured) : `unsaved-after:${sent.action}:${payload.version}`;
      const status = editorForm.querySelector('[data-content-status]');
      const changedStatus = {trash: 'trash', restore: 'draft', publish: 'publish'}[sent.action];
      if (status && changedStatus && status.value === captured.get('status')) {
        if (![...status.options].some(option => option.value === changedStatus)) {
          const option = document.createElement('option'); option.value = changedStatus; option.textContent = '回收站'; status.append(option);
        }
        status.value = changedStatus;
      }
      lastAutosaveSignature = ''; autosaveStopped = false; forcedEditorDirtyLabel = '';
      const currentLink = recoveryActions?.querySelector('[data-editor-current-link]');
      if (currentLink) currentLink.href = `${base}/edit/`;
      const identity = document.querySelector('[data-editor-identity]');
      if (identity) identity.textContent = `#${payload.id} · 版本 ${payload.version}`;
      const versionLabel = editorForm.querySelector('[data-editor-version]');
      if (versionLabel) versionLabel.textContent = `v${payload.version}`;
      if (wasNew) {
        const heading = document.querySelector('[data-editor-heading]');
        if (heading) heading.textContent = '编辑内容';
        document.title = document.title.replace(/^新建(?:文章|页面)/, '编辑内容');
        window.history.replaceState(window.history.state, '', `${base}/edit/`);
      }
      // Header state and revision controls describe the page loaded before this save.
      document.querySelectorAll('[data-editor-canonical-state], [data-editor-modified], .admin-autosave-recovery, .admin-content-notice').forEach(element => { element.hidden = true; });
      if (!stayInDocument) {
        document.querySelectorAll('[data-editor-revisions]').forEach(element => { element.hidden = true; });
        editorForm.querySelectorAll('button[name="action"]').forEach(button => { if (button.value !== 'save') button.hidden = true; });
      }
      updateScheduleRequirement();
      document.dispatchEvent(new CustomEvent('newpress:feature-status-article-saved', {detail:{id:payload.id}}));
      if (stayInDocument) {
        editorForm.dispatchEvent(new CustomEvent('newpress:editor-saved', {detail:{status:captured.get('status'), publishedAt:captured.get('published_at'), version:payload.version, url:payload.editor_url}}));
        setEditorRecovery(0);
        setAutosaveDetail(keepInput ? `已保存 v${payload.version}；后续输入尚未保存。` : `已保存到正式版本 v${payload.version}。`, 'success');
        return;
      }
      setEditorRecovery(409);
      setAutosaveDetail(savesFields ? `已确认保存 v${payload.version}；之后输入的内容仍在本页，请继续保存。` : `操作已完成，正式版本为 v${payload.version}；当前表单内容仍未保存，请先核对正式版本。`, 'success');
      if (recoveryActions) recoveryActions.hidden = false;
    };
    async function submitEditor(submitter) {
      if (submittingEditor || editorPageHidden) return;
      if (browserRecovery?.busy()) return;
      if (!pendingEditorSubmission && browserRecovery?.blocked()) { browserRecovery.warn(); return; }
      if (!pendingEditorSubmission && (!submitter || submitter.disabled)) return;
      if (!pendingEditorSubmission && timezoneNeedsReview && submitter.value === 'save') {
        setAutosaveDetail('请先核对发布时间，并确认按当前站点时区解释。', 'error');
        timezoneReview?.scrollIntoView({block: 'nearest'});
        return;
      }
      if (!pendingEditorSubmission && submitter.dataset.confirmAction && !window.confirm(submitter.dataset.confirmAction)) return;
      submittingEditor = true; syncEditorSubmitControls();
      window.clearTimeout(autosaveTimer); autosaveController?.abort();
      let timeout = 0;
      try {
        if (autosavePromise) await autosavePromise;
        if (!await checkEditorSession() || editorPageHidden) return;
        if (!pendingEditorSubmission) {
          // Controls named "action" shadow HTMLFormElement.action in real forms.
          const actionURL = new URL(editorForm.getAttribute('action'), window.location.href);
          const match = /^\/admin\/content\/(new|[1-9]\d*\/edit)\/$/.exec(actionURL.pathname);
          if (actionURL.origin !== window.location.origin || actionURL.search || actionURL.hash || !match) throw new Error('Invalid editor route');
          const id = match[1] === 'new' ? 0 : Number(match[1].split('/')[0]);
          const body = new URLSearchParams();
          new FormData(editorForm).forEach((value, key) => body.append(key, String(value)));
          body.set('action', submitter.value);
          const bytes = new Uint8Array(16); window.crypto.getRandomValues(bytes);
          const key = 'editor-' + Array.from(bytes, value => value.toString(16).padStart(2, '0')).join('');
          pendingEditorSubmission = {key, route: actionURL.pathname, body: body.toString(), signature: serializeEditorData(body),
            wasDirty: editorIsDirty(), id, version: Number(body.get('version') || 0),
            action: id ? submitter.value.replace(/^restore_revision-\d+$/, 'restore_revision') : 'create', uncertain: false, createdAt: Date.now()};
        }
        browserRecovery?.flush();
        const sent = pendingEditorSubmission;
        const body = new URLSearchParams(sent.body);
        body.set('csrf_token', editorForm.querySelector('[name="csrf_token"]').value);
        syncEditorSubmitControls(); showEditorSubmissionErrors();
        setAutosaveDetail(sent.uncertain ? '正在核对并重试原保存；后续输入仍保留在本页…' : '正在保存当前版本；可以继续输入…', 'progress');
        const controller = new AbortController(); editorSubmissionController = controller;
        timeout = window.setTimeout(() => controller.abort(new DOMException('Timeout', 'TimeoutError')), 20000);
        const response = await fetch(sent.route, {method: 'POST', body, credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: controller.signal,
          headers: {Accept: 'application/json', 'X-NewPress-Editor': '1', 'Idempotency-Key': sent.key}});
        const payload = await response.json();
        if (controller.signal.aborted) throw controller.signal.reason;
        if (!response.ok) {
          const definitive = ['validation_failed', 'slug_conflict', 'redirect_conflict', 'version_conflict', 'not_found', 'invalid_action', 'invalid_version', 'invalid_revision'];
          if (payload?.saved !== false || !definitive.includes(payload.error) || response.status >= 500) throw new Error('Unconfirmed submission');
          showEditorSubmissionErrors(payload.fields);
          reviewPublicationTimezone(payload);
          if (!sent.uncertain) pendingEditorSubmission = null;
          if (response.status === 409 || response.status === 404) autosaveStopped = true;
          setEditorRecovery(response.status);
          const message = typeof payload.message === 'string' ? payload.message.slice(0, 500) : '保存未完成；请检查输入。';
          setAutosaveDetail(message + (sent.uncertain ? ' 上次保存仍待确认，请继续重试原保存或核对正式版本。' : ''), 'error');
          return;
        }
        acknowledgeEditorSubmission(sent, payload);
        browserRecovery?.confirmed();
        pendingEditorSubmission = null;
      } catch (error) {
        intentionalEditorNavigation = false;
        if (pendingEditorSubmission) pendingEditorSubmission.uncertain = true;
        setEditorRecovery(409);
        setAutosaveDetail(pendingEditorSubmission ? '保存结果尚未确认。请保留本页并重试原保存；本页后续输入会继续保留。' : '暂时无法发起保存，当前输入已保留，请重试。', 'error');
      } finally {
        window.clearTimeout(timeout); editorSubmissionController = null;
        submittingEditor = false; syncEditorSubmitControls();
        if (!intentionalEditorNavigation) browserRecovery?.flush();
        if (!intentionalEditorNavigation) scheduleAutosave();
      }
    }
    editorForm.addEventListener('submit', async (event) => {
      event.preventDefault();
      await submitEditor(event.submitter || editorForm.querySelector('button[name="action"][value="save"]'));
    });
    window.addEventListener('beforeunload', (event) => {
      if (!intentionalEditorNavigation && (submittingEditor || pendingEditorSubmission || editorIsDirty())) { event.preventDefault(); event.returnValue = ''; }
    });
    window.addEventListener('pageshow', (event) => {
      if (!event.persisted) return;
      editorPageHidden = false;
      submittingEditor = false;
      intentionalEditorNavigation = false;
      syncEditorSubmitControls();
      scheduleAutosave();
    });
    window.addEventListener('pagehide', () => {
      if (!intentionalEditorNavigation) browserRecovery?.flush();
      editorPageHidden = true; window.clearTimeout(autosaveTimer);
      editorSessionController?.abort(); autosaveController?.abort(); editorSubmissionController?.abort();
    });
    document.addEventListener('keydown', (event) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') {
        event.preventDefault();
        if (!document.querySelector('dialog[open]')) editorForm.querySelector('button[name="action"][value="save"]')?.click();
      }
    });
    browserRecovery = window.ChunnelEditorRecovery?.mount({
      allowed: editorForm.dataset.browserRecoveryEnabled === 'true',
      owner: editorOwnerID > 0 ? `id:${editorOwnerID}` : `login:${editorOwnerLogin}`,
      hours: autosaveRetention,
      route: () => new URL(editorForm.getAttribute('action'), window.location.href).pathname,
      pending: () => pendingEditorSubmission,
      dirty: editorIsDirty, signature: serializedEditor,
      persistable: () => !intentionalEditorNavigation,
      idle: () => !submittingEditor && !pendingEditorSubmission && !editorPageHidden && !autosavePromise,
      check: checkEditorSession,
      settled: scheduleAutosave,
      changed: syncEditorSubmitControls,
      snapshot: () => ({route: new URL(editorForm.getAttribute('action'), window.location.href).pathname, draft: [...new FormData(editorForm)].map(([key, value]) => [key, String(value)]), pending: pendingEditorSubmission}),
      restore(record) {
        const values = new URLSearchParams(record.draft);
        timezoneNeedsReview = values.get('published_timezone') !== editorTimezone;
        if (publishedZone) publishedZone.value = values.get('published_timezone');
        if (publishedSource) publishedSource.value = values.get('published_source');
        if (timezoneReview) timezoneReview.hidden = !timezoneNeedsReview;
        const loadedVersion = editorForm.querySelector('[name="version"]')?.value || '';
        editorForm.querySelectorAll('[data-browser-restored-field]').forEach(input => input.remove());
        for (const name of new Set(values.keys())) {
          const controls = [...editorForm.querySelectorAll('input, textarea, select')].filter(input => input.name === name);
          const selected = values.getAll(name);
          if (name === 'category_ids' || !controls.length) {
            for (const value of selected.filter(value => !controls.some(input => input.value === value))) {
              const input = document.createElement('input'); input.type = 'hidden'; input.name = name; input.value = value; input.dataset.browserRestoredField = ''; editorForm.append(input);
            }
          } else {
            const input = controls[0];
            if (input.tagName === 'SELECT' && ![...input.options].some(option => option.value === selected[0])) {
              const option = document.createElement('option'); option.value = selected[0]; option.textContent = selected[0]; input.append(option);
            }
            input.value = selected[0];
          }
        }
        categoryChoices.forEach(input => { input.checked = values.getAll('category_ids').includes(input.value); });
        editorForm.dispatchEvent(new Event('newpress:article-badge-refresh'));
        pendingEditorSubmission = record.pending;
        initialEditor = `browser-copy:${record.id}`; lastAutosaveSignature = '';
        const stale = loadedVersion !== (values.get('version') || '');
        autosaveStopped = stale || Boolean(pendingEditorSubmission);
        forcedEditorDirtyLabel = '本机编辑已载入，等待正式保存'; slugTouched = Boolean(slug?.value);
        source?.dispatchEvent(new Event('input', {bubbles: true}));
        type?.dispatchEvent(new Event('change', {bubbles: true}));
        editorForm.querySelectorAll('[data-count-input]').forEach(updateEditorCount);
        updateCoverPreview(); updateScheduleRequirement(); syncEditorSubmitControls();
        setEditorRecovery(stale ? 409 : 0);
        setAutosaveDetail(pendingEditorSubmission ? '原保存结果待确认，请重试原保存；恢复的后续输入会继续保留。' : timezoneNeedsReview ? '副本时区与当前页面不同。请核对发布时间并点击确认；自动保存已暂停。' : stale ? `副本基于 v${values.get('version')}，页面正式版本为 v${loadedVersion}。恢复点已暂停，请先核对；保存仍会校验原版本。` : '本机副本已载入，等待正式保存。', pendingEditorSubmission || stale || timezoneNeedsReview ? 'error' : 'success');
        scheduleAutosave();
      }
    }) || null;
    timezoneReview?.querySelector('[data-published-timezone-confirm]')?.addEventListener('click', () => {
      timezoneNeedsReview = false;
      if (publishedZone) publishedZone.value = editorTimezone;
      if (publishedSource) publishedSource.value = 'canonical';
      if (timezoneReview) timezoneReview.hidden = true;
      editorChanged();
    });
    syncEditorSubmitControls();
  }

  document.querySelector('.admin-autosave-recovery form')?.addEventListener('submit', (event) => {
    const confirmation = event.submitter?.dataset.confirmAction;
    if (confirmation && !window.confirm(confirmation)) event.preventDefault();
  });

})();
