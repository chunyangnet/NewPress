(() => {
  'use strict';
  const container = document.querySelector<HTMLElement>('[data-article-badge-editor]');
  const owner = container?.closest('form');
  if (!container || !owner) return;
  const root = container, form = owner;
  const select = (name: string) => root.querySelector<HTMLSelectElement>(`[data-article-badge-field="${name}"]`)!;
  const fields = { kind: select('kind'), tone: select('tone'), icon: select('icon'), style: select('style'), placement: select('placement'), hide_mobile: select('hide_mobile'),
    text: root.querySelector<HTMLInputElement>('[data-article-badge-field="text"]')! };
  const value = (name: string) => form.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[name="${name}"]`)?.value || '';
  const preview = root.querySelector<HTMLElement>('[data-article-badge-preview]')!;
  const note = root.querySelector<HTMLElement>('[data-article-badge-note]')!;
  const presets = [...root.querySelectorAll<HTMLButtonElement>('[data-article-badge-preset]')];
  const statuses: Record<string, [string, string, string]> = {
    publish: ['已发布', 'green', 'check-circle'], draft: ['草稿', 'silver', 'pen-line'],
    pending: ['待审核', 'gold', 'shield-check'], future: ['定时发布', 'blue', 'clock-3'],
    private: ['私密', 'violet', 'lock-keyhole'], trash: ['已下架', 'orange', 'trash-2']
  };
  const allowed = (field: Exclude<keyof typeof fields, 'text'>, fallback: string) => [...fields[field].options].some(option => option.value === fields[field].value && option.value) ? fields[field].value : fallback;
  function update() {
    const option = fields.kind.selectedOptions[0];
    const kind = option?.value || '';
    const defaults = option?.dataset || {};
    let text = fields.text.value.trim() || defaults.text || '';
    let tone = allowed('tone', defaults.tone || 'blue');
    let icon = allowed('icon', defaults.icon || 'none');
    let style = allowed('style', defaults.style || 'glass');
    const isArticle = value('type') === 'post';
    const pinned = isArticle && value('pinned') === 'true';
    const pinControl = form.querySelector<HTMLElement>('[data-article-pin-control]');
    if (pinControl) pinControl.hidden = !isArticle;
    fields.text.readOnly = kind === 'status' || kind === 'pinned';
    if (kind === 'status') {
      let state = value('status');
      if (state === 'future' && value('published_at') === root.dataset.publishedLocal && Date.parse(root.dataset.publishedAt || '') <= Date.now()) state = 'publish';
      [text, tone, icon] = statuses[state] || statuses.draft!;
    }
    if (kind === 'pinned') text = '置顶';
    if (kind === 'status' || kind === 'pinned') fields.text.value = text;
    fields.tone.disabled = kind === 'status';
    fields.icon.disabled = kind === 'status';
    if (kind === 'status') { fields.tone.value = tone; fields.icon.value = icon; }
    if (pinned) {
      text = '置顶';
      if (kind !== 'pinned') { style = 'ribbon'; tone = 'gold'; icon = 'arrow-up-to-line'; }
    }
    preview.replaceChildren();
    preview.dataset.placement = kind ? allowed('placement', 'top-left') : 'top-left';
    preview.dataset.shape = style;
    if (pinned || (kind && kind !== 'pinned')) {
      const badge = document.createElement('span');
      badge.className = 'np-article-badge'; badge.dataset.style = style; badge.dataset.tone = tone;
      if (icon !== 'none') {
        const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        svg.setAttribute('class', 'icon'); svg.setAttribute('viewBox', '0 0 24 24'); svg.setAttribute('aria-hidden', 'true');
        const use = document.createElementNS('http://www.w3.org/2000/svg', 'use'); use.setAttribute('href', '#icon-' + icon);
        svg.append(use); badge.append(svg);
      }
      const label = document.createElement('span'); label.className = 'np-article-badge-text'; label.textContent = text;
      badge.append(label); preview.append(badge);
    }
    root.querySelector('[data-article-badge-count]')!.textContent = `${Array.from(fields.text.value).length}/12`;
    presets.forEach(button => button.setAttribute('aria-pressed', String(button.dataset.articleBadgePreset === kind)));
    note.textContent = (pinned ? kind === 'pinned' ? '已置顶，按当前设置显示置顶角标；取消置顶后自动隐藏。' : '已置顶，自动优先显示置顶角标；取消置顶后恢复原角标。选择“置顶”预设可调整其外观。' : kind === 'pinned' ? isArticle ? '尚未置顶，此角标暂不显示。可在发布控制中开启置顶。' : '置顶角标仅用于文章，页面不显示。' : !kind ? '当前未启用角标，选择下方预设开始。' : kind === 'status' ? `跟随文章状态：${text}。文字、图标与配色会自动更新。` : '保存文章后，角标会同步展示。') + (kind && fields.hide_mobile.value === 'true' ? ' 手机端隐藏，预览为桌面效果。' : '');
    root.querySelector('[data-article-badge-title]')!.textContent = value('title').trim() || '下一篇好内容，从这里开始';
    const image = root.querySelector<HTMLImageElement>('[data-article-badge-image]')!;
    const cover = value('cover_url').trim();
    if (cover.startsWith('/') && !cover.startsWith('//') && !/[\\\u0000-\u001f\u007f-\u009f]/.test(cover)) {
      if (image.getAttribute('src') !== cover) { image.src = cover; image.hidden = false; }
    } else { image.hidden = true; image.removeAttribute('src'); }
  }
  fields.kind.addEventListener('change', () => {
    const defaults = fields.kind.selectedOptions[0]?.dataset || {};
    for (const name of ['text', 'style', 'tone', 'icon'] as const) fields[name].value = defaults[name] || '';
    update();
  });
  presets.forEach(button => button.addEventListener('click', () => {
    if (button.matches(':disabled') || fields.kind.matches(':disabled')) return;
    fields.kind.value = button.dataset.articleBadgePreset || '';
    fields.kind.dispatchEvent(new Event('change', { bubbles: true }));
  }));
  const image = root.querySelector<HTMLImageElement>('[data-article-badge-image]')!;
  image.addEventListener('error', () => { image.hidden = true; });
  form.addEventListener('input', update);
  form.addEventListener('change', update);
  form.addEventListener('reset', () => queueMicrotask(update));
  form.addEventListener('newpress:article-badge-refresh', update);
  update();
})();
