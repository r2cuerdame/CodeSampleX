import { strict as assert } from 'node:assert';
import { newId, describe } from '../src/index.mjs';

const id = newId();
assert.match(id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);

const good = describe(id);
assert.equal(good.valid, true);
assert.equal(good.version, 4);

const bad = describe('not-a-uuid');
assert.equal(bad.valid, false);
assert.equal(bad.version, null);
console.log('CONTRACT PASS: uuid named exports generated and validated a v4 id');
