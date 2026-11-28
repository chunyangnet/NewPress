const test = require('node:test');
const assert = require('node:assert/strict');
const {loadModule} = require('./helpers/typescript.cjs');

function fixture(preferences, cards, authors = []) {
  const document = new EventTarget(), window = new EventTarget();
  let raw = JSON.stringify(preferences), parses = 0;
  class Node {
    constructor(data = {}) { this.dataset = data; this.attributes = new Map(); this.lastChild = {textContent: ''}; this.classList = {toggle() {}}; }
    setAttribute(name, value) { this.attributes.set(name, value); }
    querySelector() { return null; }
  }
  const favorites = cards.map(({title, url}) => {
    const card = new Node({contentUrl: url});
    card.querySelector = () => ({textContent: title});
    const button = new Node(); button.closest = () => card;
    return button;
  });
  const follows = authors.map(follow => new Node({follow}));
  document.querySelector = () => null;
  document.querySelectorAll = selector => selector === '[data-favorite]' ? favorites : selector === '[data-follow]' ? follows : [];
  const {initHomeSession} = loadModule('frontend/themes/chunnel/home-session.ts', {
    document, window, AbortController,
    URL: class extends URL { constructor(...args) { parses++; super(...args); } },
    location: {origin: 'https://example.test'},
    localStorage: {getItem: () => { if (raw instanceof Error) throw raw; return raw; }},
    BroadcastChannel: class { close() {} },
    setTimeout: () => 1, clearTimeout() {},
    fetch: () => new Promise(() => {}),
  });
  initHomeSession(() => Promise.resolve(), () => false);
  return {
    favorites, follows, get parses() { return parses; },
    refresh(value) {
      raw = value instanceof Error ? value : typeof value === 'string' ? value : JSON.stringify(value);
      const event = new Event('storage'); event.key = 'chunnel.preferences'; window.dispatchEvent(event);
    },
    saved: () => favorites.map(button => button.attributes.get('aria-pressed') === 'true'),
    following: () => follows.map(button => button.attributes.get('aria-pressed') === 'true'),
  };
}

test('homepage local preferences retain canonical paths, legacy titles and exact follow matching', () => {
  const f = fixture({saved: [
    {title: 'canonical', url: 'https://example.test/posts/one/?q=discarded#part'},
    {title: 'legacy title', url: '/'},
    {title: 'market legacy', url: '/market/'},
    {title: '', url: '/posts/empty-title/'},
  ], following: ['Alice', 'alice', null]}, [
    {title: 'different', url: '/posts/one/#part'},
    {title: 'same path but other hash', url: '/posts/one/'},
    {title: 'legacy title', url: '/posts/legacy/'},
    {title: 'market legacy', url: '/posts/market/'},
    {title: '', url: '/posts/empty-title/'},
    {title: 'canonical', url: '/posts/missing/'},
  ], ['Alice', 'alice', ' ALICE ', undefined]);
  assert.deepEqual(f.saved(), [true, false, true, true, true, false]);
  assert.deepEqual(f.following(), [true, true, false, false]);
  assert.equal(f.favorites[2].attributes.get('aria-label'), '取消本机收藏：legacy title');
});

test('invalid and foreign preferences cannot create homepage favorite state', () => {
  const f = fixture({saved: [null, false, [], {}, {title: 7, url: '/'}, {title: 'x', url: 1},
    {title: 'x', url: ' '}, {title: 'x', url: 'https://elsewhere.test/'},
    {title: 'x', url: 'javascript:alert(1)'}, {title: 'x', url: 'blob:https://example.test/object'},
    {title: 'x', url: 'http://['},
  ], following: 'x'}, [{title: 'x', url: '/posts/x/'}, {title: 'x', url: undefined}], ['x']);
  assert.deepEqual(f.saved(), [false, false]);
  assert.deepEqual(f.following(), [false]);
});

test('bounded homepage preferences parse stored URLs once per refresh regardless of card count', () => {
  const saved = Array.from({length: 201}, (_, i) => ({title: `saved ${i}`, url: `/posts/saved-${i}/`}));
  const cards = Array.from({length: 100}, (_, i) => ({title: `card ${i}`, url: `/posts/unmatched-${i}/`}));
  cards.push({title: 'saved 200', url: '/posts/saved-200/'});
  const following = Array.from({length: 201}, (_, i) => `author ${i}`);
  const f = fixture({saved, following}, cards, ['author 0', 'author 199', 'author 200']);
  assert.equal(f.parses, 200, 'a refresh must not reparse every saved URL for every card');
  assert.ok(f.saved().every(value => value === false));
  assert.deepEqual(f.following(), [true, true, false]);
  f.refresh({saved: [{title: 'updated', url: '/posts/unmatched-0/'}], following: []});
  assert.equal(f.parses, 201);
  assert.equal(f.saved()[0], true);
  assert.ok(f.saved().slice(1).every(value => value === false));
  assert.deepEqual(f.following(), [false, false, false]);
  for (const invalid of ['{broken', 'null', '[]', new Error('local storage disabled')]) {
    f.refresh(invalid);
    assert.ok(f.saved().every(value => value === false));
  }
});
