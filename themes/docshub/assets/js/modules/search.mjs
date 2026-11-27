import { $, $$ } from './shared.mjs';
import { discoveryURL, resolveListView } from './state.mjs';

export function initSearch() {
  const listing = $('[data-discovery]');
  if (!listing) return;
  const current = new URL(location.href), form = $('[data-filter-form]', listing);
  const anchor = listing.id ? '#' + encodeURIComponent(listing.id) : '';
  const view = resolveListView(current.searchParams.get('view'), listing.dataset.defaultView);
  listing.dataset.view = view;
  const viewSwitch = $('.view-switch', listing); if (viewSwitch) viewSwitch.hidden = false;
  if (form) {
    const sort = $('select[name="sort"]', form), requested = current.searchParams.get('sort');
    if ([...sort.options].some(option => option.value === requested)) sort.value = requested;
    const hiddenView = $('input[name="view"]', form);
    if (hiddenView) hiddenView.value = view;
    sort.addEventListener('change', () => form.requestSubmit());
    $('[data-apply-filters]', form).hidden = true;
  }
  $$('[data-filter-category]', listing).forEach(link => { link.href = discoveryURL(location.href, { category: link.dataset.filterCategory }); });
  $$('[data-list-view]', listing).forEach(link => {
    link.href = discoveryURL(location.href, { view: link.dataset.listView }) + anchor;
    if (link.dataset.listView === view) link.setAttribute('aria-current', 'true'); else link.removeAttribute('aria-current');
  });
  $$('[data-clear-filter]', listing).forEach(link => { link.href = discoveryURL(location.href, { [link.dataset.clearFilter]: '' }); });
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
