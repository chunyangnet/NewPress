  export function normalizeCollectionURL(raw: string): string|null {
    if (raw.length > 2048 || raw.includes('\\')) return null;
    try {
      const url = new URL(raw);
      if (!['http:', 'https:'].includes(url.protocol) || url.hostname !== 'mp.weixin.qq.com' || url.username || url.password || url.port && url.port !== '443') return null;
      if (/^\/s\/[A-Za-z0-9_-]{5,100}$/.test(url.pathname)) return 'https://mp.weixin.qq.com' + url.pathname;
      if (url.pathname !== '/s' && url.pathname !== '/s/') return null;
      const query = new URLSearchParams();
      for (const name of ['__biz', 'idx', 'mid', 'sn']) {
        const values = url.searchParams.getAll(name);
        if (values.length !== 1 || !values[0] || values[0].length > 160) return null;
        query.set(name, values[0]);
      }
      return 'https://mp.weixin.qq.com/s?' + query.toString();
    } catch { return null; }
  }
  export function parseCollectionInput(input: string) {
    // Keep the textarea untouched. Unsupported addresses stay in the batch so
    // the server can report each failure alongside successful articles.
    const text = input.replace(/&(?:amp|#0*38|#x0*26);/gi, '&');
    const matches = text.match(/https?:\/\/[^\s<>"'“”‘’()[\]{}（）【】《》，。；！？,]+/gi);
    const candidates = matches ? matches.map(value => value.replace(/[.;!]+$/, '')) : text.split(/\r?\n/).map(value => value.trim()).filter(Boolean);
    const seen = new Set<string>(); let duplicates = 0, unchecked = 0;
    for (const raw of candidates) {
      const normalized = normalizeCollectionURL(raw), url = normalized || raw;
      if (seen.has(url)) { duplicates++; continue; }
      seen.add(url);
      if (!normalized) unchecked++;
    }
    return { urls: [...seen], duplicates, unchecked };
  }
