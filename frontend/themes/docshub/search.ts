import { $, $$ } from './shared';
import { discoveryURL, resolveListView } from './state';

export function initSearch() {
  const listing = $('[data-discovery]');
  if (!listing) return;
  const current = new URL(location.href), form = $<HTMLFormElement>('[data-filter-form]', listing);
  const anchor = listing.id ? '#' + encodeURIComponent(listing.id) : '';
  const view = resolveListView(current.searchParams.get('view'), listing.dataset.defaultView);
  listing.dataset.view = view;
  const viewSwitch = $('.view-switch', listing); if (viewSwitch) viewSwitch.hidden = false;
  if (form) {
    const sort = $<HTMLSelectElement>('select[name="sort"]', form), requested = current.searchParams.get('sort');
    if (sort && requested && [...sort.options].some(option => option.value === requested)) sort.value = requested;
    const hiddenView = $<HTMLInputElement>('input[name="view"]', form);
    if (hiddenView) hiddenView.value = view;
    sort?.addEventListener('change', () => form.requestSubmit());
    const apply = $('[data-apply-filters]', form); if (apply) apply.hidden = true;
  }
  $$<HTMLAnchorElement>('[data-filter-category]', listing).forEach(link => { link.href = discoveryURL(location.href, { category: link.dataset.filterCategory }); });
  $$<HTMLAnchorElement>('[data-list-view]', listing).forEach(link => {
    link.href = discoveryURL(location.href, { view: link.dataset.listView }) + anchor;
    if (link.dataset.listView === view) link.setAttribute('aria-current', 'true'); else link.removeAttribute('aria-current');
  });
  $$<HTMLAnchorElement>('[data-clear-filter]', listing).forEach(link => { if (link.dataset.clearFilter) link.href = discoveryURL(location.href, { [link.dataset.clearFilter]: '' }); });
  const searches = document.body.dataset.pageKind === 'explore' ? $$('.search-form,[data-listing-search]') : $$('[data-listing-search]', listing);
  for (const search of searches) {
    for (const key of ['category', 'sort', 'view']) {
      const value = key === 'category' ? listing.dataset.category : key === 'view' ? view : current.searchParams.get(key) || listing.dataset.defaultSort;
      if (!value) continue;
      if ($('input[name="' + key + '"]', search)) continue;
      const input = document.createElement('input'); input.type = 'hidden'; input.name = key; input.value = value; search.append(input);
    }
  }
}
