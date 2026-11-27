(() => {
  'use strict';
  const limit = 65536, lifetime = 86400000;
  const valid = (value, owner, now) => value?.version === 1 && value.owner === owner && Number.isSafeInteger(owner) && owner > 0 &&
    Number.isFinite(value.at) && value.at <= now + 60000 && now - value.at < lifetime &&
    ['input', 'baseline'].every(name => typeof value[name] === 'string' && value[name].length <= 22000) &&
    typeof value.uncertain === 'boolean' && Array.isArray(value.choices) && value.choices.length <= 10 &&
    new Set(value.choices.map(item => item?.key)).size === value.choices.length &&
    value.choices.every(item => item && typeof item.key === 'string' && /^[A-Za-z0-9._:-]{1,128}$/.test(item.key) && typeof item.digest === 'string' && /^[a-f0-9]{64}$/.test(item.digest) && typeof item.selected === 'boolean');
  function create(owner) {
    const key = `newpress.wechat.collect.${owner}`;
    let previous = null;
    const payload = value => JSON.stringify([value.input, value.baseline, value.choices, value.uncertain]);
    return {
      read() {
        const raw = window.sessionStorage.getItem(key);
        if (!raw) return null;
        let value;
        try { value = raw.length <= limit ? JSON.parse(raw) : null; } catch { value = null; }
        if (!valid(value, owner, Date.now())) { this.clear(); return null; }
        previous = value;
        return { ...value, choices: value.choices.map(item => ({ ...item })) };
      },
      save(input, baseline, choices, uncertain) {
        // Deliberately whitelist fields: article HTML, credentials, signed
        // import receipts and configuration never enter browser storage.
        const value = { version: 1, owner, at: Date.now(), input, baseline, choices: choices.map(({ key, digest, selected }) => ({ key, digest, selected })), uncertain: !!uncertain };
        if (previous && payload(previous) === payload(value)) {
          if (Date.now() - previous.at >= lifetime) { this.clear(); return null; }
          value.at = previous.at;
        }
        if (!valid(value, owner, Date.now())) throw new Error('采集进度超出暂存范围，当前输入仍保留；请减少链接或重新开启暂存。');
        const raw = JSON.stringify(value);
        if (raw.length > limit) throw new Error('采集进度超过暂存容量，当前输入与上次副本仍保留。');
        window.sessionStorage.setItem(key, raw);
        previous = value;
        return value;
      },
      clear() { window.sessionStorage.removeItem(key); previous = null; },
    };
  }
  async function fingerprints(articles) {
    const result = new Map();
    if (!window.crypto?.subtle || typeof TextEncoder !== 'function') return result;
    for (const article of articles) {
      const value = JSON.stringify(['url', 'title', 'author', 'excerpt', 'body', 'cover', 'preview', 'images'].map(name => article[name]));
      const digest = await window.crypto.subtle.digest('SHA-256', new TextEncoder().encode(value));
      result.set(article.key, [...new Uint8Array(digest)].map(byte => byte.toString(16).padStart(2, '0')).join(''));
    }
    return result;
  }
  window.ChunnelWeChatDrafts = { create, fingerprints };
})();
