/** Bounded, page-local cache. Callers own decoding and identity-scoped keys. */
export function createQueryCache<T>({ ttl = 30000, capacity = 30, now = Date.now } = {}) {
  const values = new Map<string, { value: T; expires: number }>();
  const pending = new Map<string, Promise<T>>();
  let generation = 0;
  function peek(key: string): T | undefined {
    const entry = values.get(key);
    if (!entry) return undefined;
    if (entry.expires <= now()) { values.delete(key); return undefined; }
    values.delete(key); values.set(key, entry);
    return entry.value;
  }
  function clear() { generation++; values.clear(); pending.clear(); }
  function get(key: string, loader: () => Promise<T>, force = false): Promise<T> {
    if (!force) {
      const value = peek(key);
      if (value !== undefined) return Promise.resolve(value);
      const existing = pending.get(key);
      if (existing) return existing;
    }
    const owner = generation;
    let loaded: Promise<T>;
    try { loaded = loader(); } catch (error) { return Promise.reject(error); }
    const task = loaded.then(value => {
      if (owner === generation && pending.get(key) === task) {
        values.delete(key);
        values.set(key, { value, expires: now() + ttl });
        while (values.size > Math.max(0, capacity)) values.delete(values.keys().next().value!);
      }
      return value;
    }).finally(() => { if (pending.get(key) === task) pending.delete(key); });
    pending.set(key, task);
    return task;
  }
  return { get, peek, clear };
}
