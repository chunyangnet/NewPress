const presentationKeys = new Set(['page', 'sort', 'view', 'comment_page', 'comment_notice']);
const pathKey = path => {
  try { path = decodeURI(path); } catch {}
  return path.replace(/\/+$/, '') || '/';
};
const valueKey = (key, value) => key === 'q' ? value.trim().toLocaleLowerCase() : value;

// The most specific same-origin route wins, including search/category identity.
export function navigationMatch(href, currentHref) {
  let target, current;
  try { current = new URL(currentHref); target = new URL(href, current); } catch { return null; }
  if (!['http:', 'https:'].includes(target.protocol) || target.origin !== current.origin) return null;
  const path = pathKey(target.pathname), currentPath = pathKey(current.pathname);
  const exact = path === currentPath;
  if (!exact && (path === '/' || !currentPath.startsWith(path + '/'))) return null;
  const query = [...target.searchParams].filter(([key]) => !presentationKeys.has(key));
  if (query.some(([key, value]) => valueKey(key, current.searchParams.get(key) || '') !== valueKey(key, value))) return null;
  if (target.hash && target.hash !== current.hash) return null;
  const currentQuery = [...current.searchParams].filter(([key]) => !presentationKeys.has(key));
  return { score: path.length + (exact ? 1000 : 0) + query.length * 100 + (target.hash ? 50 : 0), current: exact && query.length === currentQuery.length ? 'page' : 'location' };
}

export function discoveryURL(currentHref, changes = {}) {
  const current = new URL(currentHref), params = new URLSearchParams();
  for (const key of ['q', 'category', 'sort', 'view']) {
    const value = Object.hasOwn(changes, key) ? changes[key] : current.searchParams.get(key);
    if (value) params.set(key, String(value).trim());
  }
  const query = params.toString();
  return current.pathname + (query ? '?' + query : '');
}

export function resolveListView(requested, fallback = 'grid') {
  return ['grid', 'list'].includes(requested) ? requested : fallback === 'list' ? 'list' : 'grid';
}

export function tocHeadingSelector(depth = 'h3') {
  return depth === 'h2' ? 'h2' : depth === 'h4' ? 'h2,h3,h4' : 'h2,h3';
}

export function activeHeadingIndex(tops, offset, atEnd = false) {
  if (!tops.length) return -1;
  if (atEnd) return tops.length - 1;
  let active = 0;
  for (let index = 0; index < tops.length; index++) {
    if (tops[index] <= offset) active = index; else break;
  }
  return active;
}

export function headingIDs(existing, reserved = []) {
  const used = new Set([...reserved, ...existing.filter(Boolean)]), seen = new Set(reserved);
  return existing.map((id, index) => {
    if (id && !seen.has(id)) { seen.add(id); return id; }
    let next = 'section-' + (index + 1);
    while (used.has(next)) next += '-doc';
    used.add(next); seen.add(next); return next;
  });
}
