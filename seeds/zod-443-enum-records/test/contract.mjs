import assert from 'node:assert/strict';
import { z } from 'zod';

const keys = z.enum(['id', 'name']);
const exhaustive = z.record(keys, z.string());
const partial = z.partialRecord(keys, z.string());
const open = z.record(z.string(), z.string());

assert.deepEqual(exhaustive.parse({ id: '7', name: 'Ada' }), {
  id: '7',
  name: 'Ada'
});

const missing = exhaustive.safeParse({ id: '7' });
assert.equal(missing.success, false);
assert.deepEqual(missing.error.issues, [{
  expected: 'string',
  code: 'invalid_type',
  path: ['name'],
  message: 'Invalid input: expected string, received undefined'
}]);

const wrongAndMissing = exhaustive.safeParse({ id: 7 });
assert.equal(wrongAndMissing.success, false);
assert.deepEqual(
  wrongAndMissing.error.issues.map(issue => ({ code: issue.code, path: issue.path })),
  [
    { code: 'invalid_type', path: ['id'] },
    { code: 'invalid_type', path: ['name'] }
  ]
);

const exhaustiveExtra = exhaustive.safeParse({ id: '7', name: 'Ada', extra: 'kept?' });
assert.equal(exhaustiveExtra.success, false);
assert.deepEqual(exhaustiveExtra.error.issues, [{
  code: 'unrecognized_keys',
  keys: ['extra'],
  path: [],
  message: 'Unrecognized key: "extra"'
}]);

assert.deepEqual(partial.parse({ id: '7' }), { id: '7' });
assert.deepEqual(partial.parse({}), {});

const partialExtra = partial.safeParse({ id: '7', extra: 'kept?' });
assert.equal(partialExtra.success, false);
assert.equal(partialExtra.error.issues.length, 1);
assert.equal(partialExtra.error.issues[0].code, 'invalid_key');
assert.equal(partialExtra.error.issues[0].origin, 'record');
assert.deepEqual(partialExtra.error.issues[0].path, ['extra']);
assert.equal(partialExtra.error.issues[0].issues[0].code, 'invalid_value');
assert.deepEqual(partialExtra.error.issues[0].issues[0].values, ['id', 'name']);

assert.deepEqual(open.parse({ id: '7' }), { id: '7' });
assert.deepEqual(open.parse({ extra: 'accepted' }), { extra: 'accepted' });

console.log('CONTRACT PASS: Zod 4.4.3 enum-key record completeness is measured');
