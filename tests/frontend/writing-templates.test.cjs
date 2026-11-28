const test = require('node:test');
const assert = require('node:assert/strict');
const {Mutation, isResult, bytes, MAX_IMPORT} = require('./helpers/typescript.cjs').loadNativeModule('frontend/shared/writing-template-data.ts');

test('unknown mutation outcome retains the original request body and key', () => {
  const body = {action: 'save', definition: {title: '原始输入'}}, mutation = new Mutation(body, 'original-key');
  body.definition.title = '另一次输入';
  assert.equal(JSON.parse(mutation.body).definition.title, '原始输入');
  assert.equal(mutation.key, 'original-key');
  assert.equal(mutation.retain(new Error('connection lost after commit')), true);
  assert.equal(mutation.retain({definite: true, message: 'login now expired'}), true, 'later denial does not disprove the earlier write');
  assert.equal(mutation.retain({beforeSend: true}), true);
});

test('known pre-send failures and first definitive rejection release a new request', () => {
  assert.equal(new Mutation({}, 'a').retain({beforeSend: true}), false);
  assert.equal(new Mutation({}, 'b').retain({definite: true}), false);
  assert.equal(new Mutation({}, 'c').retain({}), true);
});

test('a success receipt must include a bounded numeric version and complete identity', () => {
  const good = {id: 'a'.repeat(32), version: 2, replayed: true};
  assert.equal(isResult(good), true);
  for (const value of [{}, null, {...good, id: '../wrong'}, {...good, version: 0}, {...good, version: 2 ** 53}, {...good, replayed: 'true'}]) assert.ok(!isResult(value));
  assert.equal(bytes('字'), 3);
  assert.ok(bytes('字'.repeat(Math.ceil(MAX_IMPORT / 3))) > MAX_IMPORT);
});
