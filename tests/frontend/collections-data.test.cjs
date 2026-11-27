const test = require('node:test');
const assert = require('node:assert/strict');
const { loadModule } = require('./helpers/typescript.cjs');
const data = loadModule('frontend/shared/collection-data.ts');

const circle = { slug: 'makers', version: 8, topic_id: 4, reply_id: 9, reply_page: 2 };
const reply = { action: 'reply', version: 7, topic_id: 4 };
test('circle replies validate the target, page and next version, including replay', () => {
  assert.equal(data.circleReceipt(circle, 'makers', reply).reply_id, 9);
  assert.equal(data.circleReceipt({ ...circle, replay: true }, 'makers', reply).reply_page, 2);
  assert.equal(data.circleReceipt({ slug: 'new', version: 1 }, 'new', { action: 'create', version: 0 }).version, 1);
});
for (const [label, patch] of Object.entries({ 'wrong circle': { slug: 'elsewhere' }, 'stale version': { version: 7 }, 'unrelated version': { version: 9 }, 'wrong topic': { topic_id: 5 }, 'missing reply': { reply_id: undefined }, 'missing page': { reply_page: undefined }, 'unsafe id': { reply_id: 2 ** 54 }, 'string replay': { replay: 'true' } })) {
  test(`circle rejects ${label} without confirming a draft`, () => assert.throws(() => data.circleReceipt({ ...circle, ...patch }, 'makers', reply)));
}
test('edit reply cannot acknowledge a different reply', () => assert.throws(() => data.circleReceipt(circle, 'makers', { ...reply, action: 'edit_reply', reply_id: 12 })));
const latest = { signed_in: true, user_id: 1, csrf: 'new-token', circle: { slug: 'makers', version: 9, name: '创作', visibility_label: '公开', join_label: '开放', role_label: '圈主', description: '', rules: '' } };
test('conflict state matches the rendered owner and circle before refreshing the token', () => {
  assert.equal(data.circleLatest(latest, 'makers', 1).csrf, 'new-token');
  for (const bad of [{ ...latest, user_id: 2 }, { ...latest, signed_in: false }, { ...latest, csrf: '' }, { ...latest, circle: { ...latest.circle, slug: 'private' } }]) assert.throws(() => data.circleLatest(bad, 'makers', 1));
});
const attachment = { slug: 'makers', version: 8, attachment_id: 'a'.repeat(64), attachment: { id: 'a'.repeat(64), name: '图片.png', size: 123, image: true } };
test('attachment receipts validate version, identifier, size and representation', () => {
  assert.equal(data.circleUpload(attachment, 'makers', 7).attachment.image, true);
  for (const bad of [{ ...attachment, version: 7 }, { ...attachment, slug: 'other' }, { ...attachment, attachment_id: 'invalid' }, { ...attachment, attachment: { ...attachment.attachment, id: 'b'.repeat(64) } }, { ...attachment, attachment: { ...attachment.attachment, size: 10485761 } }, { ...attachment, attachment: { ...attachment.attachment, image: 'yes' } }]) assert.throws(() => data.circleUpload(bad, 'makers', 7));
});

test('link receipts follow actual Go create/configure/save/delete conventions', () => {
  for (const action of ['create', 'configure', 'delete_item']) assert.equal(data.linkReceipt({ slug: 'friends', version: 2, item_id: 0, replay: false }, 'friends', { action, version: 1, item_id: 4 }).version, 2);
  assert.equal(data.linkReceipt({ slug: 'friends', version: 3, item_id: 4, replay: true }, 'friends', { action: 'save_item', version: 2, item: { id: 4 } }).item_id, 4);
});
for (const [label, patch] of Object.entries({ slug: { slug: 'wrong' }, version: { version: 1 }, target: { item_id: 12 }, replay: { replay: undefined } })) {
  test(`link save rejects a mismatched ${label}`, () => assert.throws(() => data.linkReceipt({ slug: 'friends', version: 2, item_id: 4, replay: false, ...patch }, 'friends', { action: 'save_item', version: 1, item: { id: 4 } })));
}
test('metadata is validated as a whole before touching fields', () => {
  assert.equal(data.linkMetadata({ title: '站点', description: '', logo: '', notice: '' }).title, '站点');
  assert.throws(() => data.linkMetadata({ title: '站点', description: null, logo: '', notice: '' }));
});

const page = { page: 2, items: [{ id: 1, title: '中文标题', status: 'publish' }, { id: 2, title: '<script>', status: 'private' }], total: 22, has_more: false };
test('series search accepts public and private editor source titles as text', () => {
  assert.equal(data.seriesSources(page, 2).items[1].title, '<script>');
  assert.equal(data.seriesSources({ ...page, page: 1, items: [], total: 0 }, 1).items.length, 0);
});
for (const [label, patch] of Object.entries({ page: { page: 1 }, total: { total: '22' }, more: { has_more: 'false' }, repeated: { items: [page.items[0], page.items[0]] }, status: { items: [{ ...page.items[0], status: 'deleted' }] }, title: { items: [{ ...page.items[0], title: null }] }, 'too many': { items: Array.from({ length: 21 }, (_, i) => ({ id: i + 1, title: '文章', status: 'draft' })) } })) {
  test(`series search rejects invalid ${label}`, () => assert.throws(() => data.seriesSources({ ...page, ...patch }, 2)));
}
const input = { kind: 'series', status: 'draft', title: ' 标题 ', slug: 'slug', description: '', cover_url: '', post_ids: [2, 1] };
const saved = { ...input, title: '标题', id: 7, version: 3, etag: '"series-7-v3"' };
test('series receipt accepts server-normalized titles and unchanged saves retaining their version', () => {
  assert.equal(data.seriesReceipt(saved, 7, 2, input).version, 3);
  assert.equal(data.seriesReceipt({ ...saved, version: 2, etag: '"series-7-v2"' }, 7, 2, input).version, 2);
  assert.equal(data.seriesReceipt({ ...saved, version: 1, etag: '"series-7-v1"' }, 0, 0, input).id, 7);
});
for (const [label, patch] of Object.entries({ id: { id: 8 }, version: { version: 1 }, etag: { etag: 'W/"series-7-v3"' }, order: { post_ids: [1, 2] }, kind: { kind: 'topic' }, status: { status: 'publish' }, title: { title: '其他输入' }, description: { description: null } })) {
  test(`series save rejects mismatched ${label}`, () => assert.throws(() => data.seriesReceipt({ ...saved, ...patch }, 7, 2, input)));
}

function transport(fetch) {
  const document = new EventTarget(), window = new EventTarget();
  const runtime = loadModule('frontend/shared/collection-runtime.ts', { document, window, fetch, AbortController, DOMException, setTimeout, clearTimeout, Error });
  let lost = 0;
  const requests = runtime.pageRequests(1, () => lost++);
  return { requests, runtime, document, window, lost: () => lost };
}
test('identity is rechecked after delayed JSON decoding, even if fetch ignores abort', async () => {
  let release, reading;
  const ready = new Promise(resolve => reading = resolve);
  const f = transport(async () => ({ ok: true, status: 200, json: () => { reading(); return new Promise(resolve => release = resolve); } }));
  const pending = f.requests.request('/api/write');
  await ready;
  const event = new Event('chunnel:session'); event.detail = { authenticated: true, id: 2 }; f.document.dispatchEvent(event);
  release({ saved: true });
  await assert.rejects(pending, /登录状态已变化/);
  await assert.rejects(f.requests.request('/api/write'), /登录状态已变化/);
  assert.equal(f.lost(), 1);
});
test('same-owner session refresh stays valid, persisted pages retire requests', async () => {
  const f = transport(async () => ({ ok: true, status: 204 }));
  const event = new Event('chunnel:session'); event.detail = { authenticated: true, id: 1 }; f.document.dispatchEvent(event);
  assert.equal(await f.requests.request('/api/delete'), null);
  const restore = new Event('pageshow'); restore.persisted = true; f.window.dispatchEvent(restore);
  assert.equal(f.requests.lost, true);
});
test('timeouts and external cancellation have distinct outcomes', async () => {
  const f = transport((_, options) => new Promise((resolve, reject) => options.signal.addEventListener('abort', () => reject(new DOMException('cancelled', 'AbortError')))));
  await assert.rejects(f.requests.request('/api/read', {}, 2), error => error.name === 'TimeoutError' && /超时/.test(error.message));
  const external = new AbortController(), pending = f.requests.request('/api/read', { signal: external.signal }); external.abort();
  await assert.rejects(pending, error => error.name === 'AbortError');
  assert.equal(f.runtime.failure(new DOMException('cancelled', 'AbortError')).name, 'AbortError');
});
test('transport keeps server conflict statuses and rejects non-JSON success', async () => {
  const f = transport(async () => ({ ok: false, status: 409, json: async () => ({ error: { message: '版本已更新' } }) }));
  await assert.rejects(f.requests.request('/api/write'), error => error.status === 409 && error.message === '版本已更新');
  const malformed = transport(async () => ({ ok: true, status: 200, json: async () => null }));
  await assert.rejects(malformed.requests.request('/api/write'), /响应格式/);
});
