const test = require('node:test');
const assert = require('node:assert/strict');
const { loadNativeModule } = require('./helpers/typescript.cjs');
const { createQueryCache } = loadNativeModule('frontend/shared/query-cache.ts');

test('query cache deduplicates reads and expires bounded LRU entries', async () => {
  let now = 0, calls = 0;
  const cache = createQueryCache({ ttl: 10, capacity: 2, now: () => now });
  const load = async () => ++calls;
  assert.deepEqual(await Promise.all([cache.get('a', load), cache.get('a', load)]), [1, 1]);
  await cache.get('b', load); cache.peek('a'); await cache.get('c', load);
  assert.equal(cache.peek('b'), undefined);
  assert.equal(cache.peek('a'), 1);
  now = 10; assert.equal(cache.peek('a'), undefined);
  assert.equal(await cache.get('a', load), 4);
});

test('invalidated and superseded requests cannot refill cache', async () => {
  const cache = createQueryCache(); let release;
  const old = cache.get('a', () => new Promise(resolve => { release = resolve; }));
  await Promise.resolve(); cache.clear();
  await cache.get('a', async () => 'new'); release('old'); await old;
  assert.equal(cache.peek('a'), 'new');
  const failed = cache.get('b', async () => { throw Error('offline'); });
  await assert.rejects(failed, /offline/);
  assert.equal(await cache.get('b', async () => 'retry'), 'retry');
});
