import type { FormControl } from './admin-types';
import type { NavigationItem, NavigationSource } from './navigation-types';
import { createIconPicker } from './icon-picker';
import { navigationTypeLabel, exploreItemRoles } from './navigation-model';
import { navigationDrag } from './navigation-drag';

(() => {
  const admin = window.ChunnelAdmin;
  if (!admin) return;
  const { icon, showToast } = admin;
  const iconPicker = window.NewPressIconPicker ??= createIconPicker(admin);
  const reducedMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
  const navigationEditor = document.querySelector<HTMLElement>('[data-navigation-editor]');
  if (navigationEditor) {
    const canWrite = navigationEditor.dataset.canWrite === 'true';
    const list = navigationEditor.querySelector<HTMLElement>('[data-navigation-list]');
    const empty = navigationEditor.querySelector<HTMLElement>('[data-navigation-empty]');
    const form = navigationEditor.querySelector<HTMLFormElement>('[data-navigation-items-form]');
    const payload = navigationEditor.querySelector<HTMLTextAreaElement>('[data-navigation-payload]');
    const itemTemplate = navigationEditor.querySelector<HTMLTemplateElement>('[data-navigation-item-template]');
    const defaultNavigationAccent = itemTemplate?.content.querySelector<HTMLInputElement>('[data-nav-field="accent"]')?.value || '';
    const savebar = navigationEditor.querySelector<HTMLElement>('[data-navigation-savebar]');
    const countOutput = navigationEditor.querySelector<HTMLElement>('[data-navigation-count]');
    const preview = navigationEditor.querySelector<HTMLOListElement>('[data-navigation-preview]');
    let submittingNavigation = false;
    let nextTemporaryID = -1;
    const cards = () => [...(list?.querySelectorAll<HTMLElement>('[data-navigation-item]') || [])];
    cards().forEach((card) => {
      const id = Number(card.dataset.navId);
      if (Number.isFinite(id) && id <= nextTemporaryID) nextTemporaryID = id - 1;
    });

    const field = (card: HTMLElement, name: string) => card.querySelector<FormControl>(`[data-nav-field="${name}"]`);
    const cardDepth = (card: HTMLElement | undefined) => Math.max(0, Math.min(2, Math.trunc(Number(card?.dataset.navDepth)) || 0));
    const updateNavigationCard = (card: HTMLElement) => {
      const depth = cardDepth(card);
      card.dataset.navDepth = String(depth);
      card.classList.toggle('depth-0', depth === 0);
      card.classList.toggle('depth-1', depth === 1);
      card.classList.toggle('depth-2', depth === 2);
      const depthLabel = card.querySelector('[data-navigation-depth-label]');
      if (depthLabel) depthLabel.textContent = depth === 0 ? '顶层入口' : `第 ${depth} 级子项`;
      const label = field(card, 'label')?.value.trim() || '未命名项目';
      const title = card.querySelector('[data-navigation-card-title]');
      if (title) title.textContent = label;
      const typeLabel = card.querySelector('[data-navigation-type-label]');
      if (typeLabel) typeLabel.textContent = navigationTypeLabel(card.dataset.navType || 'custom');
      const presentation = field(card, 'presentation');
      if (presentation) {
        if (depth > 0) presentation.value = 'standard';
        presentation.disabled = !canWrite || depth > 0;
      }
      const iconName = field(card, 'icon')?.value || 'route';
      const iconBox = card.querySelector<HTMLElement>('.admin-navigation-item-icon');
      if (iconBox && iconBox.dataset.currentIcon !== iconName) {
        iconBox.replaceChildren(icon(iconName)); iconBox.dataset.currentIcon = iconName;
      }
      window.NewPressNavigationBadges?.previewCard(card, label);
    };

    const readNavigationCards = () => {
      const lastAtDepth: HTMLElement[] = [];
      return cards().map<NavigationItem>((card) => {
        let depth = cardDepth(card);
        if (depth > 0 && !lastAtDepth[depth - 1]) {
          depth = 0;
          card.dataset.navDepth = '0';
          updateNavigationCard(card);
        }
        const id = Number(card.dataset.navId);
        const parentID = depth === 0 ? 0 : Number(lastAtDepth[depth - 1]?.dataset.navId || 0);
        lastAtDepth[depth] = card;
        lastAtDepth.length = depth + 1;
        return {
          id,
          parent_id: parentID,
          type: card.dataset.navType || 'custom',
          object_id: Number(card.dataset.navObjectId) || 0,
          label: field(card, 'label')?.value || '',
          url: field(card, 'url')?.value || '',
          description: field(card, 'description')?.value || '',
          target: card.querySelector<HTMLInputElement>('[data-nav-field="new_window"]')?.checked ? 'blank' : 'self',
          rel: field(card, 'rel')?.value || '',
          icon: field(card, 'icon')?.value || '',
          accent: (field(card, 'accent')?.value || defaultNavigationAccent).toUpperCase(),
          presentation: depth === 0 ? (field(card, 'presentation')?.value || 'standard') : 'standard',
          ...window.NewPressNavigationBadges?.readCard(card)
        };
      });
    };

    const renderNavigationPreview = (items: NavigationItem[]) => {
      if (!preview) return;
      preview.replaceChildren();
      items.forEach((item) => {
        const row = document.createElement('li');
        let depth = 0;
        let parent = item.parent_id;
        const seen = new Set([item.id]);
        while (parent && depth < 2 && !seen.has(parent)) {
          seen.add(parent);
          depth += 1;
          parent = items.find((candidate) => candidate.id === parent)?.parent_id || 0;
        }
        row.className = `depth-${depth}`;
        const mark = item.icon ? icon(item.icon) : document.createElement('i');
        const label = document.createElement('span');
        label.textContent = item.label || '未命名项目';
        label.classList.add('nav-link-label');
        const copy = document.createElement('span'); copy.textContent = label.textContent; label.replaceChildren(copy);
        if (item.badge) { const badge = document.createElement('span'); window.NewPressNavigationBadges?.paint(badge, item.badge); label.append(badge); }
        row.append(mark, label);
        preview.append(row);
      });
      if (!items.length) {
        const row = document.createElement('li');
        row.append(document.createTextNode('等待添加第一条路径'));
        preview.append(row);
      }
    };

    let initialNavigationPayload = payload?.value.trim() || '[]';
    const syncNavigation = (mark = true) => {
      const items = readNavigationCards();
      if (navigationEditor.dataset.exploreEditor === 'true') {
        const roles = exploreItemRoles(items);
        cards().forEach((card, index) => {
          const label = card.querySelector('[data-navigation-depth-label]');
          const item = items[index];
          if (label && item) label.textContent = roles[index] || '';
        });
      }
      const serialized = JSON.stringify(items);
      if (payload) payload.value = serialized;
      if (countOutput) countOutput.textContent = String(items.length);
      list?.classList.toggle('is-empty', items.length === 0);
      renderNavigationPreview(items);
      if (mark && savebar) {
        const dirty = serialized !== initialNavigationPayload;
        savebar.classList.toggle('is-dirty', dirty);
        const state = savebar.querySelector('[data-navigation-save-state]');
        if (state) state.textContent = dirty ? '未保存' : '';
        window.ChunnelAdminUI?.paintSavebar(savebar, { dirty, busy: submittingNavigation, readOnly: !canWrite });
      }
      return serialized;
    };

    const cardBlock = (card: HTMLElement) => {
      const all = cards();
      const start = all.indexOf(card);
      const depth = cardDepth(card);
      let end = start + 1;
      while (end < all.length && cardDepth(all[end]) > depth) end += 1;
      return { all, start, end, depth, nodes: all.slice(start, end) };
    };
    const insertBlock = (nodes: HTMLElement[], reference?: Node | null) => {
      const fragment = document.createDocumentFragment();
      nodes.forEach((node) => fragment.append(node));
      list?.insertBefore(fragment, reference || empty || null);
    };
    const changeBlockDepth = (card: HTMLElement, delta: number) => {
      if (!canWrite || submittingNavigation) return;
      const block = cardBlock(card);
      const maximum = Math.max(...block.nodes.map(cardDepth));
      if (block.depth + delta < 0) return;
      if (maximum + delta > 2) {
        showToast('菜单最多支持三层，无法继续缩进。', 'warning');
        return;
      }
      if (delta > 0) {
        let previousRoot: HTMLElement | undefined;
        for (let index = block.start - 1; index >= 0; index -= 1) {
          const depth = cardDepth(block.all[index]);
          if (depth === block.depth) { previousRoot = block.all[index]; break; }
          if (depth < block.depth) break;
        }
        if (!previousRoot) {
          showToast('前方没有可作为父级的同层入口。', 'warning');
          return;
        }
      }
      block.nodes.forEach((node) => {
        node.dataset.navDepth = String(cardDepth(node) + delta);
        updateNavigationCard(node);
      });
      syncNavigation();
    };
    const moveNavigationBlock = (card: HTMLElement, direction: string | undefined) => {
      if (!canWrite || submittingNavigation || (direction !== 'up' && direction !== 'down')) return;
      const block = cardBlock(card);
      if (direction === 'up') {
        let previous = -1;
        for (let index = block.start - 1; index >= 0; index -= 1) {
          const depth = cardDepth(block.all[index]);
          if (depth === block.depth) { previous = index; break; }
          if (depth < block.depth) break;
        }
        if (previous < 0) return;
        insertBlock(block.nodes, block.all[previous]);
      } else {
        if (block.end >= block.all.length || cardDepth(block.all[block.end]) !== block.depth) return;
        let nextEnd = block.end + 1;
        while (nextEnd < block.all.length && cardDepth(block.all[nextEnd]) > block.depth) nextEnd += 1;
        insertBlock(block.nodes, block.all[nextEnd] || empty || null);
      }
      syncNavigation();
    };

    const attachDrag = list ? navigationDrag({
      list, locked: () => !canWrite || submittingNavigation, depth: cardDepth,
      move: (card, targetCard, after) => {
        const dragged = cardBlock(card), target = cardBlock(targetCard);
        if (dragged.nodes.includes(targetCard)) return;
        const afterReference = target.all.slice(target.end).find(candidate => !dragged.nodes.includes(candidate)) || empty;
        insertBlock(dragged.nodes, after ? afterReference : targetCard);
        syncNavigation();
      },
      keyboard: (card, key) => {
        if (key === 'ArrowUp' || key === 'ArrowDown') moveNavigationBlock(card, key === 'ArrowUp' ? 'up' : 'down');
        else changeBlockDepth(card, key === 'ArrowRight' ? 1 : -1);
      },
    }) : () => {};

    const attachNavigationCard = (card: HTMLElement) => {
      updateNavigationCard(card);
      iconPicker.attach(card);
      attachDrag(card);
      card.querySelectorAll<FormControl>('input,textarea,select').forEach((input) => {
        input.addEventListener('input', () => { if (!canWrite || submittingNavigation) return; updateNavigationCard(card); syncNavigation(); });
        input.addEventListener('change', () => { if (!canWrite || submittingNavigation) return; updateNavigationCard(card); syncNavigation(); });
      });
      card.querySelectorAll<HTMLButtonElement>('[data-navigation-move]').forEach((button) => button.addEventListener('click', () => moveNavigationBlock(card, button.dataset.navigationMove)));
      card.querySelector('[data-navigation-indent]')?.addEventListener('click', () => changeBlockDepth(card, 1));
      card.querySelector('[data-navigation-outdent]')?.addEventListener('click', () => changeBlockDepth(card, -1));
      card.querySelector('[data-navigation-remove]')?.addEventListener('click', () => {
        const block = cardBlock(card);
        if (!canWrite || submittingNavigation) return;
        const suffix = block.nodes.length > 1 ? `及其 ${block.nodes.length - 1} 个下级` : '';
        if (!window.confirm(`删除“${field(card, 'label')?.value || '未命名项目'}”${suffix}？`)) return;
        block.nodes.forEach((node) => node.remove());
        syncNavigation();
      });
    };

    const addNavigationItem = (source: NavigationSource = {}) => {
      if (!canWrite || submittingNavigation || !list || !itemTemplate || cards().length >= 80) {
        if (cards().length >= 80) showToast('每个菜单最多包含 80 个项目。', 'warning');
        return;
      }
      const card = itemTemplate.content.firstElementChild?.cloneNode(true);
      if (!(card instanceof HTMLElement)) return;
      const id = nextTemporaryID--;
      card.dataset.navId = String(id);
      card.dataset.navParent = '0';
      card.dataset.navDepth = '0';
      card.dataset.navType = source.type || 'custom';
      card.dataset.navObjectId = String(source.objectId || 0);
      card.querySelector('[data-navigation-id-label]')?.replaceChildren(document.createTextNode('NEW'));
      const typeLabel = card.querySelector('[data-navigation-type-label]');
      if (typeLabel) typeLabel.textContent = navigationTypeLabel(card.dataset.navType || 'custom');
      const labelInput = field(card, 'label'); if (labelInput) labelInput.value = source.label || '新导航项目';
      const urlInput = field(card, 'url'); if (urlInput) urlInput.value = source.url || '/';
      const descriptionInput = field(card, 'description'); if (descriptionInput) descriptionInput.value = source.description || '';
      const iconInput = field(card, 'icon'); if (iconInput) iconInput.value = source.icon || '';
      list.insertBefore(card, empty || null);
      attachNavigationCard(card);
      field(card, source.label ? 'url' : 'label')?.focus();
      syncNavigation();
    };

    cards().forEach(attachNavigationCard);
    syncNavigation(false);
    initialNavigationPayload = payload?.value || initialNavigationPayload;
    const originalCards = cards().map(card => card.cloneNode(true));
    syncNavigation();
    savebar?.querySelector('[data-admin-discard]')?.addEventListener('click', () => {
      if (!canWrite || submittingNavigation) return;
      cards().forEach(card => card.remove());
      originalCards.forEach(original => { const card = original.cloneNode(true); if (list && card instanceof HTMLElement) { list.insertBefore(card, empty || null); attachNavigationCard(card); } });
      syncNavigation();
    });
    navigationEditor.querySelector('[data-navigation-add-custom]')?.addEventListener('click', () => addNavigationItem());
    navigationEditor.querySelectorAll<HTMLButtonElement>('[data-navigation-source]').forEach((button) => button.addEventListener('click', () => addNavigationItem({
      type: button.dataset.sourceType,
      objectId: Number(button.dataset.sourceObjectId) || 0,
      label: button.dataset.sourceLabel,
      url: button.dataset.sourceUrl,
      description: button.dataset.sourceDescription,
      icon: button.dataset.sourceIcon
    })));
    const sourceSearch = navigationEditor.querySelector<HTMLInputElement>('[data-navigation-source-search]');
    sourceSearch?.addEventListener('input', () => {
      const needle = sourceSearch.value.trim().toLocaleLowerCase();
      navigationEditor.querySelectorAll<HTMLButtonElement>('[data-navigation-source]').forEach((button) => {
        const haystack = `${button.dataset.sourceLabel || ''} ${button.dataset.sourceUrl || ''} ${button.dataset.sourceDescription || ''}`.toLocaleLowerCase();
        button.hidden = needle !== '' && !haystack.includes(needle);
      });
    });
    navigationEditor.querySelector('[data-navigation-open-create]')?.addEventListener('click', () => {
      const details = navigationEditor.querySelector<HTMLDetailsElement>('.admin-navigation-create');
      if (details) { details.open = true; details.scrollIntoView({ behavior: reducedMotion ? 'auto' : 'smooth', block: 'center' }); details.querySelector<HTMLInputElement>('input[name="name"]')?.focus(); }
    });
    form?.addEventListener('submit', event => {
      if (submittingNavigation || !canWrite) { event.preventDefault(); return; }
      if (event.defaultPrevented) return;
      submittingNavigation = true;
      syncNavigation();
      queueMicrotask(() => { if (event.defaultPrevented) { submittingNavigation = false; syncNavigation(); } });
    });
    window.addEventListener('pageshow', event => { if (event.persisted) { submittingNavigation = false; syncNavigation(); } });
    window.addEventListener('beforeunload', (event) => {
      if (!submittingNavigation && savebar?.classList.contains('is-dirty')) { event.preventDefault(); event.returnValue = ''; }
    });
  }

})();
