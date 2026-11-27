import { bindNativeSubmission } from './admin-native-submit';
(() => {
  'use strict';
  const showToast = (message: string, tone = 'info') => window.ChunnelAdmin?.showToast(message, tone);
  const listControllers = new Map<HTMLFormElement, () => void>();
  function initializeListControls() {
  const contentBulkForm = document.querySelector<HTMLFormElement>('[data-content-bulk]');
  if (contentBulkForm && !listControllers.has(contentBulkForm)) {
    const selections = [...contentBulkForm.querySelectorAll<HTMLInputElement>('[data-content-select]')];
    const selectAll = contentBulkForm.querySelector<HTMLInputElement>('[data-content-select-all]');
    const action = contentBulkForm.querySelector<HTMLSelectElement>('[data-content-bulk-action]');
    const submit = contentBulkForm.querySelector<HTMLButtonElement>('[data-content-bulk-submit]');
    const count = contentBulkForm.querySelector('[data-content-selected-count]');
    const live = contentBulkForm.querySelector('[data-content-bulk-live]');
    const bar = contentBulkForm.querySelector('.admin-content-bulkbar');
    const actionLabels: Record<string, string> = {
      'ai-enrich':'AI 补全缺失字段', draft: '恢复 / 设为草稿', pending: '提交审核', publish: '立即发布', private: '设为私密', trash: '移入回收站', delete: '永久删除'
    };
    const selected = () => selections.filter((input) => input.checked);
    const syncBulkContent = () => {
      const selectedInputs = selected();
      const selectedCount = selectedInputs.length;
      contentBulkForm.classList.toggle('is-empty', selectedCount === 0);
      selections.forEach((input) => input.closest('[data-content-row]')?.classList.toggle('is-selected', input.checked));
      if (selectAll) {
        selectAll.checked = selections.length > 0 && selectedCount === selections.length;
        selectAll.indeterminate = selectedCount > 0 && selectedCount < selections.length;
      }
      if (count) count.textContent = String(selectedCount);
      if (submit) submit.disabled = submission.pending() || selectedCount === 0 || !action?.value;
      bar?.classList.toggle('is-danger', ['trash', 'private', 'delete'].includes(action?.value || ''));
      bar?.classList.toggle('is-publish', action?.value === 'publish');
      if (live) {
        if (selectedCount === 0) live.textContent = '请选择当前页内容和一个动作。';
        else if (!action?.value) live.textContent = `已选择 ${selectedCount} 项；请选择批量动作。`;
        else live.textContent = `准备对 ${selectedCount} 项执行“${actionLabels[action.value] || action.value}”；提交时会重新校验全部版本。`;
      }
    };
    selections.forEach((input) => input.addEventListener('change', syncBulkContent));
    selectAll?.addEventListener('change', () => {
      selections.forEach((input) => { input.checked = selectAll.checked; });
      syncBulkContent();
    });
    action?.addEventListener('change', syncBulkContent);
    contentBulkForm.addEventListener('submit', (event) => {
      if (event.defaultPrevented) return;
      if (submission.pending()) { event.preventDefault(); return; }
      const selectedCount = selected().length;
      if (selectedCount === 0 || !action?.value) {
        event.preventDefault();
        syncBulkContent();
        showToast(selectedCount === 0 ? '请至少选择一项内容。' : '请选择一个批量动作。', 'error');
        return;
      }
      const confirmations: Record<string, string> = {
        delete: `永久删除 ${selectedCount} 项内容？正文、修订和自动保存将被删除，无法恢复。`,
        trash: `将 ${selectedCount} 项内容一起移入回收站？全部公开地址会在事务提交后停止提供。`,
        publish: `立即发布 ${selectedCount} 项内容？任一版本冲突都会阻止整批发布。`,
        private: `将 ${selectedCount} 项内容设为私密？现有公开地址会在事务提交后停止提供。`
      };
      const confirmation = confirmations[action.value];
      if (confirmation && !window.confirm(confirmation)) {
        event.preventDefault();
        return;
      }
    });
    const submission = bindNativeSubmission(contentBulkForm, {
      onPending() {
        if (submit) { submit.disabled = true; submit.setAttribute('aria-busy', 'true'); }
        if (live) live.textContent = `正在校验并提交 ${selected().length} 项内容，请勿重复操作。`;
      },
      onRestore() { submit?.removeAttribute('aria-busy'); syncBulkContent(); },
    });
    listControllers.set(contentBulkForm, submission.dispose);
    syncBulkContent();
  }

  const commentBulkForm = document.querySelector<HTMLFormElement>('[data-comment-bulk]');
  if (commentBulkForm && !listControllers.has(commentBulkForm)) {
    const selections = [...document.querySelectorAll<HTMLInputElement>('[data-comment-select]')];
    const selectAll = commentBulkForm.querySelector<HTMLInputElement>('[data-comment-select-all]');
    const action = commentBulkForm.querySelector<HTMLSelectElement>('[data-comment-bulk-action]');
    const submit = commentBulkForm.querySelector<HTMLButtonElement>('[data-comment-bulk-submit]');
    const count = commentBulkForm.querySelector('[data-comment-selected-count]');
    const live = commentBulkForm.querySelector('[data-comment-bulk-live]');
    const actionLabels: Record<string, string> = {
      approved: '批准并公开', pending: '移回待审核', spam: '标记为垃圾', trash: '移入回收站'
    };
    const selected = () => selections.filter((input) => input.checked);
    const syncBulkComments = () => {
      const selectedCount = selected().length;
      commentBulkForm.classList.toggle('is-empty', selectedCount === 0);
      selections.forEach((input) => {
        const card = input.closest('[data-comment-row]');
        card?.classList.toggle('is-selected', input.checked);
        card?.setAttribute('aria-selected', input.checked ? 'true' : 'false');
      });
      if (selectAll) {
        selectAll.checked = selections.length > 0 && selectedCount === selections.length;
        selectAll.indeterminate = selectedCount > 0 && selectedCount < selections.length;
      }
      if (count) count.textContent = String(selectedCount);
      if (submit) submit.disabled = submission.pending() || selectedCount === 0 || !action?.value;
      commentBulkForm.classList.toggle('is-approve', action?.value === 'approved');
      commentBulkForm.classList.toggle('is-pending', action?.value === 'pending');
      commentBulkForm.classList.toggle('is-spam', action?.value === 'spam');
      commentBulkForm.classList.toggle('is-trash', action?.value === 'trash');
      if (live) {
        if (selectedCount === 0) live.textContent = '请选择当前页评论和一个审核动作。';
        else if (!action?.value) live.textContent = `已选择 ${selectedCount} 条；请选择审核动作。`;
        else live.textContent = `准备对 ${selectedCount} 条评论执行“${actionLabels[action.value] || action.value}”；提交时会重新校验全部版本。`;
      }
    };
    selections.forEach((input) => input.addEventListener('change', syncBulkComments));
    selectAll?.addEventListener('change', () => {
      selections.forEach((input) => { input.checked = selectAll.checked; });
      syncBulkComments();
    });
    action?.addEventListener('change', syncBulkComments);
    commentBulkForm.addEventListener('submit', (event) => {
      if (event.defaultPrevented) return;
      if (submission.pending()) { event.preventDefault(); return; }
      const selectedCount = selected().length;
      if (selectedCount === 0 || !action?.value) {
        event.preventDefault();
        syncBulkComments();
        showToast(selectedCount === 0 ? '请至少选择一条评论。' : '请选择一个审核动作。', 'error');
        return;
      }
      const confirmations: Record<string, string> = {
        approved: `批准并公开 ${selectedCount} 条评论？它们会在事务提交后进入公开讨论线程。`,
        spam: `将 ${selectedCount} 条评论一起隔离为垃圾内容？任一版本冲突都会阻止整批操作。`,
        trash: `将 ${selectedCount} 条评论一起移入回收站？永久删除仍需逐条明确确认。`
      };
      const confirmation = confirmations[action.value];
      if (confirmation && !window.confirm(confirmation)) {
        event.preventDefault();
        return;
      }
    });
    const submission = bindNativeSubmission(commentBulkForm, {
      onPending() {
        if (submit) { submit.disabled = true; submit.setAttribute('aria-busy', 'true'); }
        if (live) live.textContent = `正在审核 ${selected().length} 条评论，请勿重复操作。`;
      },
      onRestore() { submit?.removeAttribute('aria-busy'); syncBulkComments(); },
    });
    listControllers.set(commentBulkForm, submission.dispose);
    syncBulkComments();
  }
  }
  initializeListControls();
  document.addEventListener('chunnel:admin-updated', initializeListControls);
  document.addEventListener('chunnel:admin-before-replace', event => {
    for (const [form, dispose] of listControllers) if (event.detail.regions.some(region => region.contains(form))) { dispose(); listControllers.delete(form); }
  });

  const taxonomyForms = new WeakSet<HTMLFormElement>();
  function initializeTaxonomy() {
  const taxonomyForm = document.querySelector<HTMLFormElement>('[data-taxonomy-form]');
  if (taxonomyForm && !taxonomyForms.has(taxonomyForm)) {
    taxonomyForms.add(taxonomyForm);
    const name = taxonomyForm.querySelector<HTMLInputElement>('[data-term-name]');
    const slug = taxonomyForm.querySelector<HTMLInputElement>('[data-term-slug]');
    let slugTouched = Boolean(slug?.value);
    const taxonomySlugify = (value: string) => value.normalize('NFKC').toLocaleLowerCase().trim()
      .replace(/[^\p{L}\p{N}]+/gu, '-')
      .replace(/-+/g, '-')
      .replace(/^-|-$/g, '');
    name?.addEventListener('input', () => {
      if (slug && !slugTouched) slug.value = taxonomySlugify(name.value);
    });
    slug?.addEventListener('input', () => { slugTouched = slug.value.trim() !== ''; });
    taxonomyForm.addEventListener('submit', (event) => {
      const confirmation = event.submitter?.dataset.confirmAction;
      if (confirmation && !window.confirm(confirmation)) event.preventDefault();
    });
  }
  }
  initializeTaxonomy();
  document.addEventListener('chunnel:admin-updated', initializeTaxonomy);


})();
