import {required} from './ledger-admin-dom';
(() => {
  'use strict';
  const found = document.querySelector<HTMLElement>('[data-theme-market]');
  if (!found) return;
  const root=found,$=<T extends HTMLElement=HTMLElement>(selector:string):T=>required<T>(root,selector);
  const cards = [...root.querySelectorAll<HTMLElement>('[data-theme-card]')];
  const tabs = [...root.querySelectorAll<HTMLElement>('[data-theme-filter]')];
  const search = $<HTMLInputElement>('[data-theme-search]');
  const empty = $('[data-theme-empty]');
  const clear = $('[data-theme-clear]');
  const dialog = $<HTMLDialogElement>('[data-theme-preview-dialog]');
  let filter = 'all', opener:HTMLElement|undefined;
  const update = () => {
    const query = search.value.trim().toLocaleLowerCase();
    let count = 0;
    cards.forEach(card => {
      const matches = (filter === 'all' || card.dataset[filter === 'installed' ? 'installed' : 'update'] === 'true') && (card.dataset.search || '').toLocaleLowerCase().includes(query);
      card.hidden = !matches;
      if (matches) count++;
    });
    tabs.forEach(tab => tab.setAttribute('aria-pressed', String(tab.dataset.themeFilter === filter)));
    $('[data-theme-count]').textContent = count + ' 个主题';
    empty.hidden = count !== 0;
    clear.hidden = !query && filter === 'all';
    $('[data-theme-empty-text]').textContent = cards.length ? '没有符合条件的主题，试试其他关键词或筛选。' : '刷新市场获取可安装主题，或通过命令行安装本地主题包。';
  };
  tabs.forEach(tab => tab.addEventListener('click', () => { filter = tab.dataset.themeFilter || 'all'; update(); }));
  search.addEventListener('input', update);
  clear.addEventListener('click', () => { filter = 'all'; search.value = ''; update(); search.focus(); });
  root.querySelectorAll<HTMLImageElement>('[data-cover-image]').forEach(img => {
    const button = img.parentElement?.querySelector<HTMLButtonElement>('[data-theme-preview]');if(!button)return;
    const failed = () => { img.hidden = true; button.hidden = true; };
    img.addEventListener('error', failed);
    if (img.complete && img.naturalWidth === 0) failed();
    button.addEventListener('click', () => {
      opener = button;
      $('[data-theme-preview-title]').textContent = button.dataset.name || '';
      const preview = $<HTMLImageElement>('[data-theme-preview-image]');
      preview.src = img.src;
      preview.alt = img.alt;
      dialog.showModal();
      $('[data-theme-preview-close]').focus();
    });
  });
  $('[data-theme-preview-close]').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => opener?.focus());
  dialog.addEventListener('click', event => { if (event.target === dialog) { const rect = dialog.getBoundingClientRect(); if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) dialog.close(); } });
  update();
})();
