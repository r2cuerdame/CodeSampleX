import { strict as assert } from 'node:assert';
import { parseUser, checkUser } from '../src/index.mjs';

const valid = { email: 'dev@example.com', age: 30 };
assert.deepEqual(parseUser(valid), valid);

const good = checkUser(valid);
assert.equal(good.ok, true);
assert.equal(good.value.email, valid.email);

const bad = checkUser({ email: 'not-an-email', age: -1 });
assert.equal(bad.ok, false);
assert.ok(bad.issues.length >= 1);
for (const issue of bad.issues) {
  assert.ok(Array.isArray(issue.path), 'each issue carries a path');
  assert.equal(typeof issue.code, 'string', 'each issue carries a code');
}
console.log('CONTRACT PASS: zod parse/safeParse behave as documented');
